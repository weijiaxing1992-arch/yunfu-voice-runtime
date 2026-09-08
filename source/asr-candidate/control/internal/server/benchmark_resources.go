package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"rustswitch/control/internal/config"
	"strconv"
	"strings"
	"syscall"
)

// benchmarkPorts 是仅由本机配置推导的隔离方案；每通服务媒体固定四个连续端口。
type benchmarkPorts struct {
	IP                 string `json:"ip"` // 优先使用独立回环地址，不能绑定时排除主实例声明范围。
	IsolatedIP         bool   `json:"isolated_ip"`
	MediaStart         int    `json:"media_port_start"`
	MediaEnd           int    `json:"media_port_end"`
	EndpointStart      int    `json:"endpoint_port_start"`
	EndpointEnd        int    `json:"endpoint_port_end"`
	SIP                int    `json:"sip_port"`
	UAS                int    `json:"uas_port"`
	Caller             int    `json:"caller_port"`
	Admin              int    `json:"admin_port"`
	SocketGroups       int    `json:"socket_groups"`
	ExcludedPortBlocks []int  `json:"excluded_port_blocks,omitempty"` // 在整个媒体范围内冻结跳过的四端口块。
}

// benchmarkPlan 只存在于执行 goroutine；临时目录和保留监听的所有权在 cleanup 中统一释放。
type benchmarkPlan struct {
	ports             benchmarkPorts
	config            config.Config
	directory         string
	controller        string
	generator         string
	media             string
	identity          string
	reserved          []io.Closer
	reservedMedia     []io.Closer
	reservedEndpoints []io.Closer
	evidence          map[string]any
	hashes            map[string]string
}

// benchmarkSocketGroups 为G711按来源分流预留最多64个读取协程；透传保留原128组合同。
func benchmarkSocketGroups(q benchmarkRequest) int {
	if q.normalized().MediaProcessing == "g711" {
		return min(q.Concurrency, 32)
	}
	return min(q.Concurrency, 128)
}

// benchmarkSafePorts 根据冻结运行值与待启动值计算可用区间；即使使用独立 IP，也排除信令端口。
// 主媒体绑定通配地址时不能依靠 IP 隔离，必须和回退地址一样排除整个声明区间。
func benchmarkSafePorts(ip string, configs []config.Config) [65536]bool {
	var free [65536]bool
	for p := 1024; p <= 65535; p++ {
		free[p] = true
	}
	for _, c := range configs {
		if c.Media.BindIP == ip || c.Media.BindIP == "0.0.0.0" || ip == "127.0.0.1" {
			for p := max(0, c.Media.PortStart); p <= min(65535, c.Media.PortEnd); p++ {
				free[p] = false
			}
		}
		// 主管理入口使用 TCP，和媒体 UDP 是独立空间；排除它会无谓切碎可用媒体连续区间。
		for _, addr := range []string{c.SIP.Listen, c.SIP.Advertise, c.SIP.Upstream} {
			if a, e := netip.ParseAddrPort(addr); e == nil {
				free[a.Port()] = false
			}
		}
	}
	return free
}

// benchmarkTakeRange 只取完整的偶数起点连续区间；不把碎片总和误报为可分配容量。
func benchmarkTakeRange(free *[65536]bool, count int, high bool) (int, int, error) {
	if count < 1 || count > 64512 {
		return 0, 0, errors.New("隔离端口不足")
	}
	start, end, step := 1024, 65536-count, 2
	if high {
		start = (65536 - count) &^ 1
		end = 1024
		step = -2
	}
	for p := start; (!high && p <= end) || (high && p >= end); p += step {
		ok := true
		for k := p; k < p+count; k++ {
			if !free[k] {
				ok = false
				break
			}
		}
		if ok {
			for k := p; k < p+count; k++ {
				free[k] = false
			}
			return p, p + count - 1, nil
		}
	}
	return 0, 0, errors.New("本机隔离端口不足，不能占用主服务媒体范围；请在 Linux 隔离地址或独立压测服务器运行该档位")
}

// benchmarkPortPlan 是可重复测试的纯计算：发生器共享端点、服务媒体、三个信令口和管理口互不重叠。
func benchmarkPortPlan(ip string, configs []config.Config, q benchmarkRequest) (benchmarkPorts, error) {
	return benchmarkPortPlanExcluding(ip, configs, q, nil)
}

// benchmarkWorkers 与媒体池的均分算法使用相同工作进程数，不通过减少请求并发适配资源。
func benchmarkWorkers(q benchmarkRequest) int {
	return min(q.Concurrency, min(64, max(1, runtime.NumCPU())))
}

// benchmarkMediaSpace 只界定可声明的媒体大范围，主信令等离散保留端口稍后整块排除。
func benchmarkMediaSpace(ip string, configs []config.Config) [65536]bool {
	var free [65536]bool
	for port := 1024; port < len(free); port++ {
		free[port] = true
	}
	for _, c := range configs {
		if c.Media.BindIP == ip || c.Media.BindIP == "0.0.0.0" || ip == "127.0.0.1" {
			for port := max(0, c.Media.PortStart); port <= min(65535, c.Media.PortEnd); port++ {
				free[port] = false
			}
		}
	}
	return free
}

// benchmarkSpanFits 逐片计入不可用块和隔离余量，不能只看全局剩余块总和。
func benchmarkSpanFits(prefix []int, blocks int, q benchmarkRequest) bool {
	workers := benchmarkWorkers(q)
	extra := (q.CPS*200 + 999) / 1000
	offset := 0
	for id := 0; id < workers; id++ {
		count := blocks / workers
		if id < blocks%workers {
			count++
		}
		calls := q.Concurrency / workers
		if id < q.Concurrency%workers {
			calls++
		}
		// 每片预留向上取整的隔离余量，避免只有少量全局余量的片无法接满。
		if count-(prefix[offset+count]-prefix[offset]) < calls+(extra+workers-1)/workers {
			return false
		}
		offset += count
	}
	return true
}

// benchmarkPortPlanExcluding 优先把端点和信令放在高端，媒体使用可声明的大范围内的安全块。
// 大范围不跨越主媒体声明；主信令及已探测繁忙端口所在块作为显式排除传入Rust，释放后也不会被借用。
func benchmarkPortPlanExcluding(ip string, configs []config.Config, q benchmarkRequest, excluded []int) (benchmarkPorts, error) {
	p := benchmarkPorts{IP: ip, IsolatedIP: ip != "127.0.0.1", SocketGroups: benchmarkSocketGroups(q)}
	free := benchmarkSafePorts(ip, configs)
	space := benchmarkMediaSpace(ip, configs)
	for _, port := range excluded {
		if port >= 0 && port <= 65535 {
			free[port] = false
		}
	}
	var err error
	p.EndpointStart, p.EndpointEnd, err = benchmarkTakeRange(&free, p.SocketGroups*4, true)
	if err != nil {
		return p, err
	}
	for port := p.EndpointStart; port <= p.EndpointEnd; port++ {
		space[port] = false
	}
	for _, target := range []*int{&p.SIP, &p.UAS, &p.Caller, &p.Admin} {
		for n := 65535; n >= 1024; n-- {
			if free[n] {
				*target = n
				free[n] = false
				space[n] = false
				break
			}
		}
		if *target == 0 {
			return p, errors.New("没有隔离信令端口")
		}
	}
	workers := benchmarkWorkers(q)
	extra := (q.CPS*200 + 999) / 1000
	minimum := q.Concurrency + workers*((extra+workers-1)/workers)
	bestBlocks := 65536
	for begin := 1024; begin < len(space); {
		if !space[begin] {
			begin++
			continue
		}
		end := begin
		for end < len(space) && space[end] {
			end++
		}
		start := (begin + 1) &^ 1
		blocks := (end - start) / 4
		prefix := make([]int, blocks+1)
		for block := 0; block < blocks; block++ {
			base := start + block*4
			prefix[block+1] = prefix[block]
			for port := base; port < base+4; port++ {
				if !free[port] {
					prefix[block+1]++
					break
				}
			}
		}
		// 只取足够的前缀，避免低并发任务也预留整个可用地址空间的FD。
		for count := minimum; count <= blocks && count < bestBlocks; count++ {
			if !benchmarkSpanFits(prefix, count, q) {
				continue
			}
			p.MediaStart, p.MediaEnd = start, start+count*4-1
			p.ExcludedPortBlocks = nil
			for block := 0; block < count; block++ {
				if prefix[block+1] != prefix[block] {
					p.ExcludedPortBlocks = append(p.ExcludedPortBlocks, start+block*4)
				}
			}
			bestBlocks = count
			break
		}
		begin = end
	}
	if p.MediaStart == 0 {
		return p, errors.New("本机安全媒体端口块不足；主服务保留范围不会被占用")
	}
	return p, nil
}

// benchmarkPortCapacity 描述特定地址与CPS下的端口规划边界，不是CPU或媒体性能认证。
type benchmarkPortCapacity struct {
	IP                   string `json:"ip"`
	RequestedConcurrency int    `json:"requested_concurrency"`
	CPS                  int    `json:"cps"`
	MediaPortsPerCall    int    `json:"media_ports_per_call"`
	RequiredMediaBlocks  int    `json:"required_media_blocks"`
	AvailableMediaBlocks int    `json:"available_media_blocks"`
	ExcludedBlocks       int    `json:"excluded_blocks"`
	EndpointUDPPorts     int    `json:"endpoint_udp_ports"`
	MaxConcurrency       int    `json:"max_concurrency"`
	AvailablePresets     []int  `json:"available_presets"`
	Verified             bool   `json:"verified"`
	VerifiedConcurrency  int    `json:"verified_concurrency"` // 只有本次全部端口实际预留成功才填写请求并发，其他档位仍为规划估计。
	Scope                string `json:"scope"`
}

// benchmarkCapacity 以同一规划器计算当前保守上限；未进行真实绑定的结果始终标记未验证。
func benchmarkCapacity(ip string, configs []config.Config, q benchmarkRequest, excluded []int, verified bool) benchmarkPortCapacity {
	capacity := benchmarkPortCapacity{IP: ip, RequestedConcurrency: q.Concurrency, CPS: q.CPS, MediaPortsPerCall: 4,
		RequiredMediaBlocks: q.Concurrency + (q.CPS*200+999)/1000, EndpointUDPPorts: benchmarkSocketGroups(q) * 4,
		AvailablePresets: []int{}, Verified: verified, Scope: "port_plan_only_not_performance_certification"}
	if verified {
		capacity.VerifiedConcurrency = q.Concurrency
	}
	free, space := benchmarkSafePorts(ip, configs), benchmarkMediaSpace(ip, configs)
	for _, port := range excluded {
		if port >= 1024 && port <= 65535 {
			free[port] = false
		}
	}
	// 发生器端点和信令也占空间；即使请求容量不足，规划器仍会返回它们的布局。
	layout, _ := benchmarkPortPlanExcluding(ip, configs, q, excluded)
	if layout.EndpointStart > 0 {
		for port := layout.EndpointStart; port <= layout.EndpointEnd; port++ {
			free[port] = false
			space[port] = false
		}
	}
	for _, port := range []int{layout.SIP, layout.UAS, layout.Caller, layout.Admin} {
		if port > 0 {
			free[port] = false
			space[port] = false
		}
	}
	// 只报告单个安全大范围中最多的可用块，不能把主媒体两侧的碎片总和当成单实例容量。
	for begin := 1024; begin < len(space); {
		if !space[begin] {
			begin++
			continue
		}
		end := begin
		for end < len(space) && space[end] {
			end++
		}
		available, excludedBlocks := 0, 0
		for base := (begin + 1) &^ 1; base+3 < end; base += 4 {
			ok := true
			for port := base; port < base+4; port++ {
				if !free[port] {
					ok = false
					break
				}
			}
			if ok {
				available++
			} else {
				excludedBlocks++
			}
		}
		if available > capacity.AvailableMediaBlocks {
			capacity.AvailableMediaBlocks, capacity.ExcludedBlocks = available, excludedBlocks
		}
		begin = end
	}
	lo, hi := 0, 10000
	for lo < hi {
		mid := (lo + hi + 1) / 2
		trial := q
		trial.Concurrency = mid
		if _, err := benchmarkPortPlanExcluding(ip, configs, trial, excluded); err == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	capacity.MaxConcurrency = lo
	for _, calls := range []int{500, 1000, 2000, 3000, 5000} {
		trial := q
		trial.Concurrency = calls
		if _, err := benchmarkPortPlanExcluding(ip, configs, trial, excluded); err == nil {
			capacity.AvailablePresets = append(capacity.AvailablePresets, calls)
		}
	}
	return capacity
}

// benchmarkHash 对确定的部署文件计算摘要；不会接受 HTTP 传入路径，也不会把文件正文写入报告。
func benchmarkHash(ctx context.Context, path string) (string, error) {
	// 非阻塞打开使误部署的 FIFO 立即进入类型检查；不让准备阶段卡住退出清理。
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		return "", e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return "", e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("不是可执行普通文件：%s", filepath.Base(path))
	}
	if st.Size() > 128<<20 {
		return "", errors.New("测试部署二进制超过 128MiB 上限")
	}
	h := sha256.New()
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buffer)
		total += int64(n)
		if total > 128<<20 {
			return "", errors.New("测试部署二进制超过 128MiB 上限")
		}
		_, _ = h.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// prepare 固定部署二进制、继承 FD 上限及全部地址，再生成一份与生产存储完全分离的配置。
func (m *benchmarkManager) prepare(ctx context.Context, q benchmarkRequest) (*benchmarkPlan, error) {
	q = q.normalized()
	p := &benchmarkPlan{identity: randomID(), evidence: map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU()}, hashes: map[string]string{}}
	if err := q.validate(); err != nil {
		return p, &benchmarkPreflightError{code: "invalid_request", cause: err}
	}
	p.evidence["media_processing"] = q.MediaProcessing
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return p, errors.New("测试执行器当前只支持 Linux 和 macOS")
	}
	var err error
	p.controller, err = os.Executable()
	if err != nil {
		return p, err
	}
	p.generator = filepath.Join(filepath.Dir(p.controller), "callbench")
	p.media, err = filepath.Abs(m.server.Config.Media.Binary)
	if err != nil {
		return p, err
	}
	for key, path := range map[string]string{"controller": p.controller, "generator": p.generator, "media": p.media} {
		p.hashes[key], err = benchmarkHash(ctx, path)
		if err != nil {
			return p, &benchmarkPreflightError{code: "binary_unavailable", cause: fmt.Errorf("缺少或不能读取 %s 二进制：%w", key, err)}
		}
	}
	var limit syscall.Rlimit
	if err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return p, fmt.Errorf("读取进程文件描述符上限失败：%w", err)
	}
	workers := benchmarkWorkers(q)
	workerFD := ((q.Concurrency+workers-1)/workers)*4 + 96
	generatorFD := benchmarkSocketGroups(q)*4 + 96
	p.evidence["fd_soft_limit"], p.evidence["fd_hard_limit"] = limit.Cur, limit.Max
	p.evidence["required_worker_fds"], p.evidence["required_generator_fds"], p.evidence["workers"] = workerFD, generatorFD, workers
	p.evidence["memory_preflight"] = "未将内存估算当作可用容量证明；运行中采集媒体 RSS 与发生器资源"
	if uint64(max(workerFD, generatorFD)) > limit.Cur {
		return p, &benchmarkPreflightError{code: "fd_limit", cause: fmt.Errorf("继承的文件描述符上限 %d 不足；媒体分片需约 %d，发生器需约 %d", limit.Cur, workerFD, generatorFD)}
	}
	m.server.admin.mu.Lock()
	configs := []config.Config{m.server.admin.active, m.server.admin.state.Desired}
	m.server.admin.mu.Unlock()
	ip := "127.0.0.1"
	for _, candidate := range []string{"127.77.0.2", "127.0.0.2"} {
		occupied := false
		for _, c := range configs {
			if c.Media.BindIP == candidate {
				occupied = true
			}
		}
		if occupied {
			continue
		}
		// 地址可绑定探测也必须避开主服务尚未分配的声明范围，不能临时占用它们。
		allowed := benchmarkSafePorts(candidate, configs)
		probePort := 0
		for n := 1024; n <= 65535; n++ {
			if allowed[n] {
				probePort = n
				break
			}
		}
		if probePort == 0 {
			continue
		}
		probe, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(candidate), Port: probePort})
		if e == nil {
			probe.Close()
			ip = candidate
			break
		}
	}
	p.evidence["endpoint_model"] = "发生器最多 128 组共享 RTP/RTCP 端点，按 SSRC 分流；服务每通仍独占四端口，不等同于每通真实终端独立 FD 的负载"
	if q.MediaProcessing == "g711" {
		p.evidence["endpoint_model"] = "发生器最多32组共享RTP/RTCP端点、64个媒体读取协程，按协商来源和socket分流；服务每通仍独占四端口，验证PCMU/PCMA异律PCM内容"
	}
	// 繁忙端口属于其他本机进程，排除后重新规划；有界重试不会降低请求并发或侵入生产声明范围。
	excluded := []int{}
	for attempt := 0; attempt < 128; attempt++ {
		if err = ctx.Err(); err != nil {
			return p, err
		}
		p.ports, err = benchmarkPortPlanExcluding(ip, configs, q, excluded)
		p.evidence["ports"] = p.ports
		p.evidence["port_replans"] = attempt
		p.evidence["excluded_busy_ports"] = append([]int{}, excluded...)
		p.evidence["port_capacity"] = benchmarkCapacity(ip, configs, q, excluded, false)
		if err != nil {
			capacity := p.evidence["port_capacity"].(benchmarkPortCapacity)
			return p, &benchmarkPreflightError{code: "port_capacity", cause: fmt.Errorf("请求 %d 路需要至少 %d 个媒体端口块；%s 当前规划上限 %d 路（CPS=%d），主服务保留范围不变", q.Concurrency, capacity.RequiredMediaBlocks, ip, capacity.MaxConcurrency, q.CPS)}
		}
		// 暂留全部测试端口会消耗父进程FD，明确给主服务额外留下512个描述符余量。
		coordinatorFD := (p.ports.MediaEnd - p.ports.MediaStart + 1) - len(p.ports.ExcludedPortBlocks)*4 + p.ports.SocketGroups*4 + 4 + 512
		p.evidence["required_coordinator_fds"] = coordinatorFD
		if uint64(coordinatorFD) > limit.Cur {
			return p, &benchmarkPreflightError{code: "fd_limit", cause: fmt.Errorf("安全预留测试端口需父进程约 %d 个FD（含512余量），继承上限为 %d", coordinatorFD, limit.Cur)}
		}
		err = p.reserveMediaPorts(ctx)
		if err == nil {
			err = p.reservePorts()
		}
		if err == nil {
			p.evidence["port_capacity"] = benchmarkCapacity(ip, configs, q, excluded, true)
			break
		}
		p.releaseReservations(true)
		p.reserved = nil
		p.reservedMedia, p.reservedEndpoints = nil, nil
		var conflict *benchmarkPortError
		if !errors.As(err, &conflict) || !errors.Is(err, syscall.EADDRINUSE) {
			return p, &benchmarkPreflightError{code: "port_probe_failed", cause: fmt.Errorf("隔离端口预检失败：%w", err)}
		}
		excluded = append(excluded, conflict.port)
		if attempt == 127 {
			return p, &benchmarkPreflightError{code: "port_busy", cause: fmt.Errorf("隔离端口连续 128 次规划遇到占用：%w", err)}
		}
	}
	p.directory, err = os.MkdirTemp("", "rustswitch-test-")
	if err != nil {
		return p, err
	}
	addr := func(port int) string { return net.JoinHostPort(ip, strconv.Itoa(port)) }
	p.config = config.Config{
		SIP:    config.SIP{Listen: addr(p.ports.SIP), Advertise: addr(p.ports.SIP), Upstream: addr(p.ports.UAS), TrustedNetworks: []string{ip + "/32"}, SetupTimeoutMS: 10000, AckTimeoutMS: 10000, MaxCallSeconds: (q.Concurrency+q.CPS-1)/q.CPS + q.DurationSeconds + 90},
		Media:  config.Media{Binary: p.media, BindIP: ip, AdvertiseIP: ip, PortStart: p.ports.MediaStart, PortEnd: p.ports.MediaEnd, ExcludedPortBlocks: append([]int(nil), p.ports.ExcludedPortBlocks...), Workers: workers, ReceiveBufferBytes: 65536, PortReuseDelayMS: 200, MaxPacketsPerSecondPerLeg: 200, AllowedRemoteNetworks: []string{ip + "/32"}, CPUCores: []int{}, AdaptiveAdmission: true, AdmissionCPUHigh: .85, AdmissionCPULow: .65, ConnectSockets: true},
		Limits: config.Limits{MaxCalls: q.Concurrency, CallsPerSecond: q.CPS, BurstCalls: max(q.Concurrency, q.CPS), MaxTransactions: max(10000, q.Concurrency*8)},
		Admin:  config.Admin{Listen: addr(p.ports.Admin)}, Journal: config.Journal{Path: filepath.Join(p.directory, "events.jsonl"), QueueCapacity: 65536},
	}
	// 隔离发生器只使用自身端点；任何后续配置复制都不能让压测实例注册真实生产中继。
	p.config.SIP.TrunkAuth, p.config.SIP.Registration = nil, nil
	p.config.SIP.Dialplan = nil        // 隔离实例不执行主服务的XML业务动作。
	p.config.Media.PlaybackRoot = ""   // 压测不能读取部署实例的提示音文件。
	p.config.SIP.LocalExtensions = nil // 隔离压测只走其模拟上游，不能继承真实IVR自动应答路由。
	// 媒体模式来自已冻结的测试请求；主服务即使启用g711，默认小电话仍独立验证relay。
	p.config.Media.Processing = q.MediaProcessing
	if err = p.config.Validate(); err != nil {
		return p, err
	}
	p.evidence["media_adaptive_admission"] = true
	p.evidence["port_reuse_delay_ms"] = 200
	return p, nil
}

// releaseReservations 分阶段释放自己持有的预留监听；关闭后标记 nil，便于失败清理重复调用。
func (p *benchmarkPlan) releaseReservations(all bool) {
	// 控制器启动前交接媒体；发生器端点继续保留到发生器启动前。
	for i, c := range p.reservedMedia {
		if c != nil {
			_ = c.Close()
			p.reservedMedia[i] = nil
		}
	}
	if all {
		for i, c := range p.reservedEndpoints {
			if c != nil {
				_ = c.Close()
				p.reservedEndpoints[i] = nil
			}
		}
	}
	for i, c := range p.reserved {
		if c != nil && (all || i == 0 || i == 3) {
			_ = c.Close()
			p.reserved[i] = nil
		}
	}
}

// benchmarkPortError 保留冲突的具体传输层及端口，只有已被占用错误允许重新规划。
type benchmarkPortError struct {
	network string
	ip      string
	port    int
	cause   error
}

// Error 返回可读诊断，报告不会掩盖原始端口冲突。
func (e *benchmarkPortError) Error() string {
	return fmt.Sprintf("%s %s:%d：%v", e.network, e.ip, e.port, e.cause)
}

// Unwrap 允许调用者区分占用、权限和不支持的地址，不把所有错误都当成繁忙端口。
func (e *benchmarkPortError) Unwrap() error { return e.cause }

// reservePorts 在完整媒体预检后暂留信令和管理监听，缩小启动交接窗口；失败时由拥有者关闭已取得部分。
func (p *benchmarkPlan) reservePorts() error {
	for _, port := range []int{p.ports.SIP, p.ports.UAS, p.ports.Caller} {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(p.ports.IP), Port: port})
		if err != nil {
			return &benchmarkPortError{"UDP", p.ports.IP, port, err}
		}
		p.reserved = append(p.reserved, c)
	}
	l, err := net.Listen("tcp4", net.JoinHostPort(p.ports.IP, strconv.Itoa(p.ports.Admin)))
	if err != nil {
		return &benchmarkPortError{"TCP", p.ports.IP, p.ports.Admin, err}
	}
	p.reserved = append(p.reserved, l)
	return nil
}

// benchmarkExcludedSet 把冻结的禁用块转换为查找集合；每个块中的四个端口一同跳过。
func benchmarkExcludedSet(p benchmarkPorts) map[int]bool {
	out := make(map[int]bool, len(p.ExcludedPortBlocks))
	for _, base := range p.ExcludedPortBlocks {
		out[base] = true
	}
	return out
}

// reserveMediaPorts 实际保留所有可分配媒体和端点socket；主SIP块与已知繁忙块绝不触发bind。
// 任一步失败后，由prepare统一关闭本次已经取得的监听，再带着冲突证据重新规划。
func (p *benchmarkPlan) reserveMediaPorts(ctx context.Context) error {
	excluded := benchmarkExcludedSet(p.ports)
	for port := p.ports.MediaStart; port <= p.ports.MediaEnd; port++ {
		base := p.ports.MediaStart + (port-p.ports.MediaStart)/4*4
		if excluded[base] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(p.ports.IP), Port: port})
		if err != nil {
			return &benchmarkPortError{"UDP", p.ports.IP, port, err}
		}
		p.reservedMedia = append(p.reservedMedia, c)
	}
	for port := p.ports.EndpointStart; port <= p.ports.EndpointEnd; port++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(p.ports.IP), Port: port})
		if err != nil {
			return &benchmarkPortError{"UDP", p.ports.IP, port, err}
		}
		p.reservedEndpoints = append(p.reservedEndpoints, c)
	}
	return nil
}

// benchmarkProbePorts 清理核对只检查本任务可分配的端口，不能把主服务或第三方禁用块误判为泄漏。
func benchmarkProbePorts(ctx context.Context, p benchmarkPorts) error {
	if p.MediaStart == 0 {
		return nil
	}
	excluded := benchmarkExcludedSet(p)
	for _, span := range [][2]int{{p.MediaStart, p.MediaEnd}, {p.EndpointStart, p.EndpointEnd}, {p.SIP, p.SIP}, {p.UAS, p.UAS}, {p.Caller, p.Caller}} {
		for port := span[0]; port <= span[1]; port++ {
			if port >= p.MediaStart && port <= p.MediaEnd && excluded[p.MediaStart+(port-p.MediaStart)/4*4] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(p.IP), Port: port})
			if err != nil {
				return &benchmarkPortError{"UDP", p.IP, port, err}
			}
			c.Close()
		}
	}
	c, err := net.Listen("tcp4", net.JoinHostPort(p.IP, strconv.Itoa(p.Admin)))
	if err != nil {
		return &benchmarkPortError{"TCP", p.IP, p.Admin, err}
	}
	return c.Close()
}

// benchmarkEnvironment 去掉任何继承的子实例身份，再设置本任务唯一标记；其他部署环境保持原样。
func benchmarkEnvironment(identity string) []string {
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, benchmarkChildEnv+"=") {
			env = append(env, value)
		}
	}
	return append(env, benchmarkChildEnv+"="+identity)
}
