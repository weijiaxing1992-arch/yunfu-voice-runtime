// callbench 同时模拟 SIP 主叫和被叫，建立呼叫后发送双向 G.711 RTP 并核对接收结果。
// 可在独立压测机运行；RustSwitch 的上游必须指向 --uas-listen。
// 结果仅覆盖本程序实际产生的呼叫、负载与持续时间，不能代替全部 FreeSWITCH 功能兼容验收。
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	mathrand "math/rand"
	"net"
	"net/netip"
	"os"
	"runtime"
	"rustswitch/control/internal/codecprofile"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/sip"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// flow 保存一通模拟呼叫的信令状态、两个媒体方向及各方向计数。
// 建立阶段允许信令协程更新协商结果；媒体阶段读取冻结快照，避免每包竞争锁。
// 发送计数由分配到此呼叫的唯一发送协程修改，接收计数由对应端点的唯一读取协程修改；
// 主协程等待相应 WaitGroup 完成后才汇总普通整数和位图。
type flow struct {
	// mediaDestinations 在所有拨号任务完成后复制并冻结，媒体线程启动后不再修改。
	mediaDestinations [2]netip.AddrPort
	// endpointPorts 保存最初绑定的 A RTP、A RTCP、B RTP、B RTCP 端口，供信令安全读取。
	endpointPorts [4]int
	// index 用于分配工作协程及构造唯一 SSRC；user、id、tag 标识本次模拟呼叫。
	index         int
	user, id, tag string
	// sockets 按 A RTP、A RTCP、B RTP、B RTCP 排列；共享模式下多通呼叫引用同一组连接。
	sockets [4]*net.UDPConn
	// mu 保护建立阶段的 destinations 与 invite；媒体计数不依赖此锁。
	mu           sync.Mutex
	destinations [2]netip.AddrPort
	// failure 由此呼叫的拨号协程写入，主协程在 dialing.Wait 后读取。
	failure string
	invite  []byte
	// 有界响应通道让信令读取协程不必等待拨号/挂断协程；重复响应在队列满时丢弃。
	answer    chan *sip.Message
	byeAnswer chan bool
	// ack 原子发布不可变的 []byte，供拨号、重传响应处理和迟到成功响应清理使用。
	ack atomic.Value
	// accepted 表示建立流程通过；abandoned 表示失败后需要 CANCEL 或 ACK/BYE 清理。
	accepted  atomic.Bool
	abandoned atomic.Bool
	// sent 按发送端编号统计完整写入本地 UDP 套接字的数据报，不表示远端已接收。
	sent [2]uint64
	// received、duplicate、invalid 按接收端编号记录通过校验的唯一包、重复包和坏包。
	received  [2]uint64
	duplicate [2]uint64
	invalid   [2]uint64
	// seen 以完整时间戳序号去重，避免 RTP 的 16 位序列号回绕导致重复误判。
	seen [2][]byte
	// largest 是当前最大有效序号；更小的首次到达序号计入 reordered。
	largest   [2]uint32
	reordered [2]uint64
	// writeErrors 是生成器本地发送错误或短写次数，与接收缺包分开报告。
	writeErrors [2]uint64
	// processed 由唯一接收者拥有；相位在发送计划建立时冻结，普通计数不由进度线程读取。
	processed       [2]processedReceiveState
	phaseOffset     time.Duration
	generatorLate   [2]uint64
	processedWrites [2]processedWriteDiagnostic
}

// bench 持有一次压测的配置和共享生命周期状态，不在运行中调整呼叫集合。
type bench struct {
	// phaseSlots 将每个 20 ms 周期拆分成相位组；仅 spread 为真时生效。
	phaseSlots int
	// target 是唯一允许的 SIP 服务端来源；caller 与 uas 分别模拟主叫、被叫。
	target      netip.AddrPort
	caller, uas *net.UDPConn
	// advertise 用于 SDP；两个 Contact 使用生成器对服务端可达的信令地址。
	advertise, callerContact, uasContact string
	// flows 与两个索引在信令读取协程启动前构建完成，此后只读，不需要并发 map 写锁。
	flows        []*flow
	byID, byUser map[string]*flow
	// mediaConfig 提供 SDP 远端地址校验边界；这里不是服务端的完整媒体运行配置。
	mediaConfig config.Media
	// duration 仅计全部拨号任务完成后的媒体收发时长，不含建立和拆线。
	duration time.Duration
	// packetType 使用本测试的载荷映射；编码名、包长度和时钟由不可变 profile 决定。
	packetType   uint8
	codecProfile codecprofile.Profile // 每任务只选择一次，不在热路径查找或分配描述符。
	// stage：0 建立、1 媒体、2 拆线；前两阶段收到服务端 BYE 视为意外断话。
	stage atomic.Int32
	// 三种信令异常由不同协程累计；建立明细另以每通呼叫的 failure 汇总进报告。
	unexpectedBye  atomic.Uint64
	setupFailed    atomic.Uint64
	teardownFailed atomic.Uint64
	// attemptedCalls 在启动拨号任务前递增；进度读取它时不访问拨号协程的普通字段。
	attemptedCalls atomic.Uint64
	// lateTicks 统计发送相位比计划晚超过 2 ms 的次数，不单独决定 passed。
	lateTicks atomic.Uint64
	// I/O 次数在各读写者退出时一次性汇总，避免每包原子争用；Linux 统计 mmsg 尝试，其他平台统计 UDP API。
	sendCalls    atomic.Uint64
	receiveCalls atomic.Uint64
	// mediaSendingDone 区分尾包窗口的正常读超时与媒体窗口内的提前退出。
	mediaSendingDone atomic.Bool
	readerErrors     atomic.Uint64
	readerTimeouts   atomic.Uint64
	// done 在报告完成后关闭；当前读取循环依靠连接关闭/读超时退出，并不监听此通道。
	done chan struct{}
	// readers 只追踪 RTP 读取协程，保证汇总前接收侧普通计数已经停止变化。
	readers sync.WaitGroup
	// connectedMedia 仅允许独占套接字，并在媒体协程启动前替换成连接到固定远端的 UDP。
	connectedMedia bool
	// unknownRTP 记录无法归属有效呼叫/套接字组的数据报，多个读取协程通过原子值累计。
	unknownRTP atomic.Uint64
	// spread 选择分散相位或同步突发，报告必须保留此负载形态以便比较结果。
	spread bool
	// mediaProcessing 显式选择负载语义，绝不从主服务配置猜测；源索引在媒体线程启动前冻结。
	mediaProcessing  string
	processedSources map[processedEndpointKey]*flow
	mediaStarted     atomic.Pointer[time.Time]
}

// main 按建立、冻结媒体配置、收发、拆线、汇总的顺序运行压测。
// 退出码 0 表示当前场景通过，1 表示报告中的验收条件未满足，2 表示配置或运行准备错误。
func main() {
	// 命令行名称、默认值和英文帮助文本是已有脚本接口，保持稳定。
	server := flag.String("server", "127.0.0.1:5060", "RustSwitch SIP address")
	uas := flag.String("uas-listen", "127.0.0.1:5070", "SIP UAS address; set RustSwitch upstream to this address")
	bind := flag.String("bind-ip", "127.0.0.1", "local IPv4 address for caller and media sockets")
	advertise := flag.String("advertise-ip", "127.0.0.1", "IPv4 address reachable from RustSwitch")
	mediaNetwork := flag.String("media-network", "", "allowed RustSwitch media network; defaults to server IP/32")
	count := flag.Int("calls", 100, "number of simultaneous bidirectional calls")
	callerPort := flag.Int("caller-port", 0, "caller SIP UDP port; 0 uses an OS ephemeral port")
	cps := flag.Int("cps", 10, "call setup rate")
	seconds := flag.Int("seconds", 10, "media duration after all calls are established")
	senders := flag.Int("senders", 4, "traffic generator sender goroutines")
	payload := flag.Int("payload", 0, "test RTP payload (0 PCMU, 8 PCMA, 9 G722, 111 Opus, 18 G729, 110 G726-32)")
	processing := flag.String("media-processing", "relay", "relay preserves bytes; g711 verifies different-law PCMU/PCMA processing at 20ms")
	connectMedia := flag.Bool("connect-media", false, "connect generator UDP sockets to the negotiated server peers; requires dedicated sockets")
	socketGroups := flag.Int("socket-groups", 0, "endpoint socket groups; relay 0 is dedicated, g711 0 uses up to 32 shared groups; g711 demultiplexes by negotiated source")
	sequentialPorts := flag.Bool("sequential-media-ports", false, "use sequential explicit ports instead of deterministic shuffle; useful for hash-collision stress")
	portStart := flag.Int("media-port-start", 0, "explicit endpoint UDP port range start; 0 uses OS ephemeral ports")
	portEnd := flag.Int("media-port-end", 0, "explicit endpoint UDP port range end")
	output := flag.String("output", "", "optional JSON report path")
	progressPath := flag.String("progress", "", "optional atomic JSON progress path; parent directory must exist")
	direct := flag.Bool("direct-media", false, "diagnostic only: bypass the media server and send RTP directly between simulated endpoints")
	phaseSlots := flag.Int("phase-slots", 20, "phase groups within each 20ms interval (1..1000); used only with --spread")
	spread := flag.Bool("spread", true, "spread call packet phases across each 20ms interval; false sends synchronized bursts")
	flag.Parse()
	// 处理图使用固定端点组限制接收工作者数量，呼叫数仍完整保留到10000，不以降载代替验收。
	if err := validateProcessingOptions(*processing, *payload, *count, *socketGroups, *connectMedia, *direct); err != nil {
		fatal(err)
	}
	if *processing == "g711" && *socketGroups == 0 && !*connectMedia {
		*socketGroups = min(*count, processedMaxSocketGroups)
	}
	// 限制生成器资源规模和互斥模式；共享套接字不能连接到各不相同的媒体远端。
	if *phaseSlots < 1 || *phaseSlots > 1000 {
		fatal(fmt.Errorf("invalid phase slot count"))
	}
	if *socketGroups < 0 || *socketGroups > *count || (*socketGroups > 0 && *connectMedia) {
		fatal(fmt.Errorf("invalid socket groups or connected shared sockets"))
	}
	if (*portStart != 0 || *portEnd != 0) && (*portStart < 1024 || *portEnd < *portStart || *portEnd > 65535) {
		fatal(fmt.Errorf("invalid explicit generator UDP range"))
	}
	// 隔离编排器可显式规划主叫端口，避免系统临时端口与待分配的媒体端口重叠。
	if *callerPort < 0 || *callerPort > 65535 {
		fatal(fmt.Errorf("caller SIP UDP port must be 0 or within 1..65535"))
	}
	if *count < 1 || *count > 10000 || *cps < 1 || *cps > 10000 || *seconds < 1 || *seconds > 3600 || *senders < 1 || *senders > 64 || *payload < 0 || *payload > 127 {
		fatal(fmt.Errorf("invalid benchmark limits"))
	}
	profile, supported := codecprofile.Lookup(uint8(*payload))
	if !supported {
		fatal(fmt.Errorf("unsupported test payload"))
	}
	// 只有显式指定路径才启动进度发布；准备错误保留 starting，不能误写为完成。
	progress, err := newProgressWriter(*progressPath, *output, *count, time.Now())
	if err != nil {
		fatal(err)
	}
	defer progress.close(false)
	target, err := netip.ParseAddrPort(*server)
	if err != nil || !target.Addr().Is4() {
		fatal(fmt.Errorf("literal IPv4 server required"))
	}
	ip, err := netip.ParseAddr(*bind)
	if err != nil || !ip.Is4() {
		fatal(fmt.Errorf("literal IPv4 bind address required"))
	}
	advertised, err := netip.ParseAddr(*advertise)
	if err != nil || !config.UsableIP(advertised) {
		fatal(fmt.Errorf("usable advertised IPv4 required"))
	}
	uaddr, err := netip.ParseAddrPort(*uas)
	if err != nil || !uaddr.Addr().Is4() {
		fatal(fmt.Errorf("literal IPv4 UAS required"))
	}
	if *mediaNetwork == "" {
		// 默认仅接受 SIP 服务端这一 IPv4 地址发布的媒体地址；分离部署需显式扩大网段。
		*mediaNetwork = target.Addr().String() + "/32"
	}
	if _, err = netip.ParsePrefix(*mediaNetwork); err != nil {
		fatal(err)
	}
	caller, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(*callerPort))))
	if err != nil {
		fatal(err)
	}
	defer caller.Close()
	downstream, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(uaddr))
	if err != nil {
		fatal(err)
	}
	defer downstream.Close()
	b := &bench{target: target, caller: caller, uas: downstream, advertise: *advertise, duration: time.Duration(*seconds) * time.Second, packetType: uint8(*payload), codecProfile: profile, done: make(chan struct{}), byID: make(map[string]*flow), byUser: make(map[string]*flow), mediaConfig: config.Media{BindIP: "0.0.0.0", AdvertiseIP: "0.0.0.0", PortStart: 1, PortEnd: 2, AllowedRemoteNetworks: []string{*mediaNetwork}}}
	b.spread = *spread
	b.mediaProcessing = *processing
	b.phaseSlots = *phaseSlots
	b.connectedMedia = *connectMedia
	b.callerContact = netip.AddrPortFrom(advertised, caller.LocalAddr().(*net.UDPAddr).AddrPort().Port()).String()
	b.uasContact = netip.AddrPortFrom(advertised, uaddr.Port()).String()
	groupCount := *socketGroups
	if groupCount == 0 {
		groupCount = *count
	}
	if *portStart != 0 && *portEnd-*portStart+1 < groupCount*4 {
		fatal(fmt.Errorf("endpoint range cannot accommodate %d sockets", groupCount*4))
	}
	groups := make([][4]*net.UDPConn, groupCount)
	// 显式端口范围可复现网卡/内核哈希分布；固定种子打散不用于安全随机标识。
	var endpointPorts []int
	if *portStart != 0 {
		for port := *portStart; port <= *portEnd; port++ {
			endpointPorts = append(endpointPorts, port)
		}
		if !*sequentialPorts {
			mathrand.New(mathrand.NewSource(1)).Shuffle(len(endpointPorts), func(i, j int) { endpointPorts[i], endpointPorts[j] = endpointPorts[j], endpointPorts[i] })
		}
	}
	nextPort := 0
	for g := range groups {
		for side := range groups[g] {
			for {
				port := 0
				if *portStart != 0 {
					if nextPort >= len(endpointPorts) {
						fatal(fmt.Errorf("endpoint UDP range exhausted"))
					}
					port = endpointPorts[nextPort]
					nextPort++
				}
				conn, e := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port))))
				if e != nil {
					if port != 0 {
						// 显式范围中的占用端口可跳过；全部耗尽时失败，避免默默改变端口布局。
						continue
					}
					fatal(e)
				}
				if e = conn.SetReadBuffer(262144); e != nil {
					fatal(e)
				}
				groups[g][side] = conn
				defer conn.Close()
				break
			}
		}
	}
	for index := 0; index < *count; index++ {
		// RTCP 套接字用于保留所公布端口；此生成器当前只发送并校验 RTP。
		f := &flow{index: index, user: "bench-" + strconv.Itoa(index), id: identifier() + "@bench", tag: identifier()[:16], answer: make(chan *sip.Message, 4), byeAnswer: make(chan bool, 1)}
		f.sockets = groups[index%groupCount]
		for side, conn := range f.sockets {
			f.endpointPorts[side] = conn.LocalAddr().(*net.UDPAddr).Port
		}
		b.flows = append(b.flows, f)
		b.byID[f.id] = f
		b.byUser[f.user] = f
	}
	// flows 已构建完毕；发布后只读其原子 accepted 标志，不读取 RTP 计数数组。
	if err = progress.beginDialing(b, time.Now()); err != nil {
		fatal(err)
	}
	go b.readCaller()
	go b.readUAS()
	// CPS 控制启动速率而非成功建立速率；每通呼叫独立处理重传和超时。
	setupStart := time.Now()
	var dialing sync.WaitGroup
	pace := time.NewTicker(time.Second / time.Duration(*cps))
	defer pace.Stop()
	for _, f := range b.flows {
		<-pace.C
		b.attemptedCalls.Add(1)
		dialing.Add(1)
		go func(f *flow) { defer dialing.Done(); b.dial(f) }(f)
	}
	dialing.Wait()
	setupDuration := time.Since(setupStart)
	accepted := 0
	for _, f := range b.flows {
		if f.accepted.Load() {
			accepted++
		}
	}
	fmt.Fprintf(os.Stderr, "Established %d/%d calls in %s; sending bidirectional RTP for %s.\n", accepted, *count, setupDuration.Round(time.Millisecond), b.duration)
	if *direct {
		// 直连模式仅诊断生成器/网络路径，结果必须标明绕过服务端媒体，不能作媒体转发验收。
		for _, f := range b.flows {
			f.mu.Lock()
			f.destinations[0] = netip.AddrPortFrom(advertised, uint16(f.sockets[2].LocalAddr().(*net.UDPAddr).Port))
			f.destinations[1] = netip.AddrPortFrom(advertised, uint16(f.sockets[0].LocalAddr().(*net.UDPAddr).Port))
			f.mu.Unlock()
		}
	}
	// 冻结后即使有迟到 SIP 报文更新建立阶段字段，RTP 线程也只读取此处发布的快照。
	for _, f := range b.flows {
		f.mu.Lock()
		f.mediaDestinations = f.destinations
		f.mu.Unlock()
	}
	if *connectMedia {
		// 关闭并重新绑定原端口会有短暂空窗，故只允许在收发线程启动前执行。
		for _, f := range b.flows {
			if !f.accepted.Load() {
				continue
			}
			for side := 0; side < 2; side++ {
				original := f.sockets[side*2]
				local := original.LocalAddr().(*net.UDPAddr)
				if e := original.Close(); e != nil {
					fatal(e)
				}
				conn, e := net.DialUDP("udp4", local, net.UDPAddrFromAddrPort(f.mediaDestinations[side]))
				if e != nil {
					fatal(e)
				}
				if e = conn.SetReadBuffer(262144); e != nil {
					fatal(e)
				}
				f.sockets[side*2] = conn
				defer conn.Close()
			}
		}
	}
	// 连接式端点可能已经替换conn对象，必须在此后按最终实际socket建立只读来源索引。
	if err = b.prepareProcessedSources(); err != nil {
		fatal(err)
	}
	for _, f := range b.flows {
		if f.accepted.Load() {
			for side := 0; side < 2; side++ {
				// 预分配去重位图，包含额外序号余量和向上取整，避免媒体热路径正常情况下分配。
				f.seen[side] = make([]byte, (int(b.duration/(20*time.Millisecond))+107)/8)
			}
		}
	}
	// 所有发送包、批量适配及接收缓冲在媒体计时前准备，避免准备成本被误算为供给不足。
	plans, err := b.prepareSendPlans(*senders, newPacketWriter)
	if err != nil {
		fatal(err)
	}
	b.stage.Store(1)
	// 每个实际套接字只有一个读取者；relay按原SSRC，g711按冻结的协商源分流，最多64个g711读取者。
	endpointFlows := b.endpointFlowCounts()
	startedReaders := map[*net.UDPConn]bool{}
	var ready sync.WaitGroup
	for _, f := range b.flows {
		if f.accepted.Load() {
			for side := 0; side < 2; side++ {
				conn := f.sockets[side*2]
				if !startedReaders[conn] {
					reader, e := newPacketReader(conn, min(endpointFlows[conn], mediaBatchCapacity))
					if e != nil {
						fatal(e)
					}
					startedReaders[conn] = true
					b.readers.Add(1)
					ready.Add(1)
					go b.readRTP(conn, side, reader, &ready)
				}
			}
		}
	}
	ready.Wait()
	var sending sync.WaitGroup
	runStart := time.Now()
	b.mediaStarted.Store(&runStart)
	if err = progress.beginMedia(runStart); err != nil {
		fatal(err)
	}
	deadline := runStart.Add(b.duration)
	// 每个实际共享 socket 只设置一次期限，且写入不得借两秒宽限继续越过负载窗口。
	for conn := range startedReaders {
		if err = conn.SetWriteDeadline(deadline); err != nil {
			fatal(err)
		}
		if err = conn.SetReadDeadline(deadline.Add(3 * time.Second)); err != nil {
			fatal(err)
		}
	}
	activeSenders := 0
	for _, plan := range plans {
		if len(plan.writers) == 0 {
			continue
		}
		activeSenders++
		sending.Add(1)
		go func(plan mediaSendPlan) { defer sending.Done(); b.sendPlannedRTP(plan, runStart, deadline) }(plan)
	}
	sending.Wait()
	// 最后一个合法 20ms 包可在窗口结束前提交；仍保持完整媒体观察窗口，不提前开始拆线。
	if wait := time.Until(deadline); wait > 0 {
		time.Sleep(wait)
	}
	b.mediaSendingDone.Store(true)
	// 媒体计时只覆盖发送窗口，随后接收尾包和拆线耗时不会继续增加此值。
	if err = progress.endMedia(time.Now()); err != nil {
		fatal(err)
	}
	// 停止发送后保留 500 ms 接收尾包窗口；等全部读取协程退出后再汇总接收结果。
	tailDeadline := time.Now().Add(500 * time.Millisecond)
	for conn := range startedReaders {
		_ = conn.SetReadDeadline(tailDeadline)
	}
	b.readers.Wait()
	b.stage.Store(2)
	if err = progress.beginTeardown(time.Now()); err != nil {
		fatal(err)
	}
	b.teardownCalls()
	var sent, received, duplicates, invalid, reordered, writeErrors uint64
	for _, f := range b.flows {
		for side := 0; side < 2; side++ {
			sent += f.sent[side]
			received += f.received[side]
			duplicates += f.duplicate[side]
			invalid += f.invalid[side]
			reordered += f.reordered[side]
			writeErrors += f.writeErrors[side]
		}
	}
	// 各方向独立核算缺包，其他方向的多收不能抵消；仍无法单独定位丢失发生在哪一跳。
	flowLoad := assessFlowLoad(b.flows, uint64(50*(*seconds)))
	missing := flowLoad.missingPackets
	setupErrors := map[string]int{}
	shownErrors := 0
	for _, f := range b.flows {
		if f.failure != "" {
			setupErrors[f.failure]++
			if shownErrors < 20 {
				fmt.Fprintf(os.Stderr, "Call %d failed: %s\n", f.index, f.failure)
				shownErrors++
			}
		}
	}
	nominal := uint64(accepted * 2 * 50 * (*seconds))
	// 负载至少达到成功呼叫的双向标称包量 98%，避免生成器少发造成“零缺包”假象。
	loadRatio := float64(0)
	if nominal > 0 {
		loadRatio = float64(sent) / float64(nominal)
	}
	// 还必须全部呼叫建立、无缺包/重复/非法包/发送错误/意外断话/拆线失败。
	// relay沿用原有乱序/调度迟到仅报告语义；g711下面追加独立内容及播放时序门槛，不代表会议或长期稳定性。
	passed := flowLoad.underloadedDirections == 0 && flowLoad.unexpectedReceived == 0 && b.readerErrors.Load() == 0 && b.unknownRTP.Load() == 0 && loadRatio >= 0.98 && accepted == *count && missing == 0 && duplicates == 0 && invalid == 0 && writeErrors == 0 && b.unexpectedBye.Load() == 0 && b.teardownFailed.Load() == 0
	processedReport, processedPassed := b.processedSummary()
	passed = passed && processedPassed
	report := map[string]any{"generator_resources": processUsage(), "phase_slots": b.phaseSlots, "sequential_media_ports": *sequentialPorts, "endpoint_port_start": *portStart, "endpoint_port_end": *portEnd, "endpoint_socket_groups": groupCount, "endpoint_udp_sockets": groupCount * 4, "generator_connected_media": b.connectedMedia, "unknown_rtp_packets": b.unknownRTP.Load(), "passed": passed, "spread_packet_phases": b.spread, "media_server_bypassed": *direct, "setup_errors": setupErrors, "offered_load_ratio": loadRatio, "scenario": "SIP B2BUA with bidirectional codec RTP passthrough", "codec": profile, "validation_scope": profile.Validation, "generator_os": runtime.GOOS, "generator_arch": runtime.GOARCH, "generator_cpus": runtime.NumCPU(), "server": *server, "requested_calls": *count, "established_calls": accepted, "setup_cps": *cps, "setup_seconds": setupDuration.Seconds(), "media_seconds": *seconds, "ptime_ms": 20, "payload_type": *payload, "nominal_packets": uint64(accepted * 2 * 50 * (*seconds)), "sent_packets": sent, "received_unique_packets": received, "unreceived_packets": missing, "duplicate_packets": duplicates, "invalid_packets": invalid, "reordered_packets": reordered, "generator_write_errors": writeErrors, "generator_late_ticks": b.lateTicks.Load(), "unexpected_byes": b.unexpectedBye.Load(), "teardown_failures": b.teardownFailed.Load(), "measured_at": time.Now().UTC().Format(time.RFC3339), "measurement_boundary": "generator socket sends versus unique RTP received at simulated endpoints; does not identify the location of loss"}
	if *direct {
		report["scenario"] = "DIAGNOSTIC: direct RTP between endpoints, media server bypassed"
	}
	report["media_processing"] = *processing
	report["media_demux"] = "source_ssrc_and_socket"
	report["generator_receiver_workers"] = len(startedReaders)
	report["socket_groups"] = groupCount
	report["receiver_workers"] = len(startedReaders)
	if *processing == "g711" {
		report["media_demux"] = "negotiated_source_and_socket"
		report["scenario"] = "SIP B2BUA with bidirectional G711 decode/encode and complete PCM oracle"
		report["validation_scope"] = "PCMU/PCMA异律20ms双向处理；逐帧归属及全部160样本量化内容、RTP身份和有限接收时序；非全codec或听感认证"
		processedCodec := profile
		processedCodec.Validation = report["validation_scope"].(string)
		report["codec"] = processedCodec
		report["processed_media"] = processedReport
		report["processed_diagnostics"] = b.processedDiagnosticsSummary()
	}
	report["generator_io_mode"] = mediaIOMode
	report["generator_send_calls"] = b.sendCalls.Load()
	report["generator_receive_calls"] = b.receiveCalls.Load()
	report["generator_reader_errors"] = b.readerErrors.Load()
	report["generator_reader_timeouts"] = b.readerTimeouts.Load()
	report["generator_sender_workers"] = activeSenders
	report["generator_io_count_boundary"] = "Linux counts mmsg syscall attempts including EAGAIN; portable mode counts UDP API calls, excluding internal retries"
	report["underloaded_flow_directions"] = flowLoad.underloadedDirections
	report["unexpected_received_packets"] = flowLoad.unexpectedReceived

	directions := []map[string]any{}
	worst := []map[string]any{}
	// 从发送端 side 到接收端 side^1 配对，单独暴露单向故障并保留缺包最多的 20 个方向。
	for side := 0; side < 2; side++ {
		var submitted, got, affected, underloaded, minimum, minimumSent, maximum uint64
		minimum = ^uint64(0)
		minimumSent = ^uint64(0)
		for _, f := range b.flows {
			if !f.accepted.Load() {
				continue
			}
			count := f.received[side^1]
			loss := uint64(0)
			if f.sent[side] > count {
				loss = f.sent[side] - count
			}
			if loss > 0 {
				affected++
			}
			if !sufficientDirectionLoad(f.sent[side], count, uint64(50*(*seconds))) {
				underloaded++
			}
			submitted += f.sent[side]
			got += count
			minimum = min(minimum, count)
			minimumSent = min(minimumSent, f.sent[side])
			maximum = max(maximum, count)
			if loss > 0 {
				worst = append(worst, map[string]any{"call": f.index, "direction": side, "sent": f.sent[side], "received": count, "missing": loss})
			}
		}
		if accepted == 0 {
			minimum = 0
			minimumSent = 0
		}
		directions = append(directions, map[string]any{"source_side": side, "sent": submitted, "received": got, "flows_with_loss": affected, "underloaded_flows": underloaded, "minimum_sent_per_flow": minimumSent, "minimum_received_per_flow": minimum, "maximum_received_per_flow": maximum})
	}
	sort.Slice(worst, func(i, j int) bool { return worst[i]["missing"].(uint64) > worst[j]["missing"].(uint64) })
	if len(worst) > 20 {
		worst = worst[:20]
	}
	report["directions"] = directions
	report["worst_flows"] = worst
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
	if *output != "" {
		// 报告可能包含部署地址，使用仅当前用户可读写的权限创建输出文件。
		if err = os.WriteFile(*output, append(data, '\n'), 0600); err != nil {
			fatal(err)
		}
	}
	close(b.done)
	// 先输出完整报告，再发布完成；验收失败仍保留 teardown，并沿用原有退出码 1。
	if err = progress.close(passed); err != nil {
		fatal(err)
	}
	if !passed {
		os.Exit(1)
	}
}

// fatal 打印准备阶段错误并退出；os.Exit 不执行 defer，连接由进程退出时的系统清理回收。
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(2) }

// identifier 从系统密码学随机源生成信令标识，避免并发呼叫共享 Call-ID 或事务分支。
func identifier() string {
	var value [16]byte
	if _, e := rand.Read(value[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(value[:])
}

// offer 生成指定端点的固定 G.711/20 ms SDP；side 只能是内部调用传入的 0 或 1。
// 使用预存端口而非正在替换的 socket，允许迟到的信令读取协程安全生成响应。
func (b *bench) offer(f *flow, side int) []byte {
	rtp := f.endpointPorts[side*2]
	rtcp := f.endpointPorts[side*2+1]
	profile := b.sideProfile(side)
	return sip.RenderSDP(uint64(f.index+1), b.advertise, rtp, rtcp, sip.SDP{Payload: profile.Payload, PTime: 20, Codec: profile.Spec()})
}

// dial 驱动一通 INVITE 的建立，最多等待 8 秒，每 500 ms 重传。
// 只有 200 响应中的 SDP 与 Contact 均通过校验才发布 accepted；失败始终尝试清理事务。
func (b *bench) dial(f *flow) {
	uri := "sip:" + f.user + "@" + b.target.String()
	from := "<sip:bench@" + b.callerContact + ">;tag=" + f.tag
	f.mu.Lock()
	f.invite = sip.Request("INVITE", uri, b.callerContact, "z9hG4bK"+identifier(), from, "<"+uri+">", f.id, 1, b.offer(f, 0))
	f.mu.Unlock()
	_, _ = b.caller.WriteToUDPAddrPort(f.invite, b.target)
	timeout := time.NewTimer(8 * time.Second)
	defer timeout.Stop()
	retry := time.NewTicker(500 * time.Millisecond)
	defer retry.Stop()
	for {
		select {
		case answer := <-f.answer:
			if answer.Status != 200 {
				f.failure = fmt.Sprintf("SIP %d %s", answer.Status, answer.Reason)
				b.setupFailed.Add(1)
				b.abandon(f)
				return
			}
			description, e := sip.ParseSDP(answer.Body, b.mediaConfig)
			if e == nil && b.mediaProcessing == "g711" {
				e = b.validateProcessedSDP(description, 0)
			}
			if e != nil {
				f.failure = "answer SDP: " + e.Error()
				b.setupFailed.Add(1)
				b.abandon(f)
				return
			}
			target, e := sip.URI(answer.Header("contact"))
			if e != nil {
				f.failure = "answer Contact: " + e.Error()
				b.setupFailed.Add(1)
				b.abandon(f)
				return
			}
			f.mu.Lock()
			f.destinations[0] = netip.MustParseAddrPort(description.Peer.RTP)
			f.mu.Unlock()
			// 发布不可变 ACK 后再标记建立成功，让后续媒体/拆线逻辑可依赖 ACK 已存在。
			ack := sip.Request("ACK", target, b.callerContact, "z9hG4bK"+identifier(), from, answer.Header("to"), f.id, 1, nil)
			f.ack.Store(ack)
			_, _ = b.caller.WriteToUDPAddrPort(ack, b.target)
			f.accepted.Store(true)
			return
		case <-retry.C:
			_, _ = b.caller.WriteToUDPAddrPort(f.invite, b.target)
		case <-timeout.C:
			f.failure = "setup timeout"
			b.setupFailed.Add(1)
			b.abandon(f)
			return
		}
	}
}

// readCaller 串行读取主叫 SIP 端点，处理最终响应重传、迟到响应和服务端主动拆线。
// 本压测场景只接收配置的服务端源地址，既不承担生产 SIP 栈角色，也不支持任意对端漫游。
func (b *bench) readCaller() {
	buffer := make([]byte, 65536)
	for {
		n, source, e := b.caller.ReadFromUDPAddrPort(buffer)
		if e != nil {
			return
		}
		if source != b.target {
			continue
		}
		m, e := sip.Parse(buffer[:n])
		if e != nil {
			continue
		}
		f := b.byID[m.CallID()]
		if f == nil {
			continue
		}
		_, method, _ := m.CSeq()
		if m.Status >= 200 && method == "INVITE" {
			if m.Status < 300 {
				// 2xx 的 ACK 是独立事务；即使拨号已经超时，仍先 ACK 再发 BYE 清理迟到会话。
				if f.ack.Load() == nil {
					if target, e := sip.URI(m.Header("contact")); e == nil {
						from := "<sip:bench@" + b.callerContact + ">;tag=" + f.tag
						f.ack.Store(sip.Request("ACK", target, b.callerContact, "z9hG4bK"+identifier(), from, m.Header("to"), f.id, 1, nil))
					}
				}
				if ack := f.ack.Load(); ack != nil {
					_, _ = b.caller.WriteToUDPAddrPort(ack.([]byte), b.target)
				}
				if f.abandoned.Load() {
					b.abandon(f)
				}
			} else {
				// 非 2xx 最终响应复用原 INVITE 分支确认，不能使用成功响应的独立 ACK 分支。
				f.mu.Lock()
				invite, _ := sip.Parse(f.invite)
				f.mu.Unlock()
				if invite != nil {
					ack := sip.Request("ACK", invite.URI, b.callerContact, invite.Branch(), invite.Header("from"), m.Header("to"), f.id, 1, nil)
					_, _ = b.caller.WriteToUDPAddrPort(ack, b.target)
				}
			}
			if !f.accepted.Load() && !f.abandoned.Load() {
				// 有界队列避免一个未及时消费响应的拨号协程阻塞所有呼叫的信令读取。
				select {
				case f.answer <- m:
				default:
				}
			}
		}
		if m.Status >= 200 && method == "BYE" {
			select {
			case f.byeAnswer <- m.Status == 200:
			default:
			}
		}
		if m.Method == "BYE" {
			_, _ = b.caller.WriteToUDPAddrPort(sip.Response(m, 200, "OK", "", "", source.String(), nil), source)
			if b.stage.Load() < 2 {
				b.unexpectedBye.Add(1)
			}
		}
	}
}

// readUAS 模拟立即应答的被叫，缓存每个 INVITE 事务的响应以处理 UDP 重传。
// 缓存仅由此协程访问，生命周期为本次进程；这里只覆盖压测使用的最小信令行为。
func (b *bench) readUAS() {
	buffer := make([]byte, 65536)
	cached := make(map[string][]byte)
	for {
		n, source, e := b.uas.ReadFromUDPAddrPort(buffer)
		if e != nil {
			return
		}
		if source != b.target {
			continue
		}
		m, e := sip.Parse(buffer[:n])
		if e != nil {
			continue
		}
		if m.Method == "INVITE" {
			if wire, ok := cached[m.Key()]; ok {
				_, _ = b.uas.WriteToUDPAddrPort(wire, source)
				continue
			}
			user, e := sip.User(m.URI)
			if e != nil {
				continue
			}
			f := b.byUser[user]
			if f == nil {
				continue
			}
			description, e := b.parseUASOffer(m.Body)
			if e != nil {
				// 被叫明确拒绝未提供另一律的报价，旧relay服务器不能被发生器假装成转码成功。
				if b.mediaProcessing == "g711" {
					_, _ = b.uas.WriteToUDPAddrPort(sip.Response(m, 488, "Not Acceptable Here", "", "", source.String(), nil), source)
				}
				continue
			}
			f.mu.Lock()
			f.destinations[1] = netip.MustParseAddrPort(description.Peer.RTP)
			f.mu.Unlock()
			wire := sip.Response(m, 200, "OK", "bench-b-"+strconv.Itoa(f.index), "sip:bench@"+b.uasContact, source.String(), b.offer(f, 1))
			cached[m.Key()] = wire
			_, _ = b.uas.WriteToUDPAddrPort(wire, source)
		} else if m.Method == "BYE" || m.Method == "CANCEL" {
			_, _ = b.uas.WriteToUDPAddrPort(sip.Response(m, 200, "OK", "", "", source.String(), nil), source)
			if m.Method == "BYE" && b.stage.Load() < 2 {
				b.unexpectedBye.Add(1)
			}
		}
	}
}

// readRTP 独占读取一个实际端点套接字，并更新此方向对应呼叫的接收统计。
// 主线程在发送完毕后缩短读截止时间；任何读取错误都结束本读者，缺包由最终报告体现。
func (b *bench) readRTP(conn *net.UDPConn, side int, reader packetReader, ready *sync.WaitGroup) {
	defer b.readers.Done()
	defer func() { b.receiveCalls.Add(reader.calls()) }()
	slots := int(b.duration/(20*time.Millisecond)) + 100
	payload := b.audioProfile().Frame()
	ready.Done()
	for {
		packets, e := reader.readBatch()
		for _, packet := range packets {
			b.recordRTP(conn, side, packet.source, packet.data, slots, payload)
		}
		if e != nil {
			if timeout, ok := e.(net.Error); ok && timeout.Timeout() && b.mediaSendingDone.Load() {
				b.readerTimeouts.Add(1)
			} else {
				b.readerErrors.Add(1)
			}
			return
		}
	}
}

// recordRTP 校验本生成器约定的固定 RTP 头、指定编码测试载荷、来源、SSRC 与时间戳，并去重计数。
// 它不是通用 RTP 解码器：带扩展头、CSRC、不同载荷或其他合法 RTP 形态也会被判为非法。
// 调用者必须保证每个 (conn, side) 仅有一个读取者；普通计数和 seen 位图不支持并发写。
func (b *bench) recordRTP(conn *net.UDPConn, side int, source netip.AddrPort, buffer []byte, slots int, payload []byte) {
	if b.mediaProcessing == "g711" {
		b.recordProcessedRTP(conn, side, source, buffer, time.Now())
		return
	}
	n := len(buffer)
	if n < 12 {
		b.unknownRTP.Add(1)
		return
	}
	ssrc := binary.BigEndian.Uint32(buffer[8:12])
	if ssrc == 0 || int((ssrc-1)/2) >= len(b.flows) {
		b.unknownRTP.Add(1)
		return
	}
	f := b.flows[(ssrc-1)/2]
	// SSRC 能定位呼叫还不够，必须核对当前接收套接字组，防止跨流投递被误算成功。
	if !f.accepted.Load() || f.sockets[side*2] != conn {
		b.unknownRTP.Add(1)
		return
	}
	expectedSSRC := uint32(f.index*2 + (side ^ 1) + 1)
	expectedSource := f.mediaDestinations[side]
	if source != expectedSource || n != 12+len(payload) || buffer[0] != 0x80 || buffer[1] != b.packetType || binary.BigEndian.Uint32(buffer[8:12]) != expectedSSRC || !bytes.Equal(buffer[12:n], payload) {
		f.invalid[side]++
		return
	}
	step := b.audioProfile().TimestampStep()
	ordinal := binary.BigEndian.Uint32(buffer[4:8]) / step
	// 按协商时钟验证20ms刻度（G722=160、Opus=960），允许16位序列号自然回绕。
	if binary.BigEndian.Uint32(buffer[4:8])%step != 0 || ordinal == 0 || int(ordinal) >= slots || binary.BigEndian.Uint16(buffer[2:4]) != uint16(ordinal) {
		f.invalid[side]++
		return
	}
	if f.seen[side] == nil {
		// 正常压测已预分配；此兜底也允许单元测试直接调用校验函数。
		f.seen[side] = make([]byte, (slots+7)/8)
	}
	seen := f.seen[side]
	index, mask := ordinal/8, byte(1<<(ordinal%8))
	if seen[index]&mask != 0 {
		f.duplicate[side]++
		return
	}
	seen[index] |= mask
	f.received[side]++
	if ordinal < f.largest[side] {
		f.reordered[side]++
	}
	if ordinal > f.largest[side] {
		f.largest[side] = ordinal
	}
}

// abandon 尝试清理失败或放弃的 INVITE，避免压测失败后遗留会话。
// 已有成功响应 ACK 时重发 ACK 并发 BYE，否则沿原事务分支发 CANCEL；迟到响应会再次触发。
// 本函数只提交清理报文，不等待所有清理事务确认；服务端残留资源仍需结合其统计/日志核验。
func (b *bench) abandon(f *flow) {
	f.abandoned.Store(true)
	if wire := f.ack.Load(); wire != nil {
		ack, e := sip.Parse(wire.([]byte))
		if e != nil {
			return
		}
		_, _ = b.caller.WriteToUDPAddrPort(wire.([]byte), b.target)
		bye := sip.Request("BYE", ack.URI, b.callerContact, "z9hG4bK-abandoned-"+f.tag, ack.Header("from"), ack.Header("to"), f.id, 2, nil)
		_, _ = b.caller.WriteToUDPAddrPort(bye, b.target)
		return
	}
	f.mu.Lock()
	invite, e := sip.Parse(f.invite)
	f.mu.Unlock()
	if e != nil {
		return
	}
	cancel := sip.Request("CANCEL", invite.URI, b.callerContact, invite.Branch(), invite.Header("from"), invite.Header("to"), f.id, 1, nil)
	_, _ = b.caller.WriteToUDPAddrPort(cancel, b.target)
}
