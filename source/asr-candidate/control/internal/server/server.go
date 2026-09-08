// Package server 将 SIP 会话状态集中在单一主循环，媒体控制异步执行，管理线程只读取原子快照。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/dialplan"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// datagram 携带已解析信令及真实传输来源，来源不能由报文中的 Via 或 Contact 代替。
type datagram struct {
	message *sip.Message   // 与接收缓冲分离的解析结果。
	source  netip.AddrPort // 用于白名单、事务来源及对话来源校验。
}

// cacheEntry 保存最终响应，支持同一来源、同一事务的幂等重放。
type cacheEntry struct {
	flow    uint64         // 原始可靠连接身份，零表示 UDP。
	wire    []byte         // 完整响应报文，重传时保留原标签和头部。
	source  netip.AddrPort // 原请求来源，也是响应目标。
	version uint64         // 缓存版本，避免旧定时器移除更新后的条目。
}

// mediaResult 将后台 RPC 完成结果送回主循环，后台不得直接修改 Call。
type mediaResult struct {
	session   uint64      // 目标通话资源编号。
	operation string      // 分配、早期连接、最终连接或释放阶段。
	version   uint64      // 连接请求的控制层版本，用于过滤旧结果。
	reply     media.Reply // 媒体进程返回的确认内容。
	err       error       // 队列、传输或媒体操作错误。
}

// transaction 保存出站请求及重传状态；多个字段共同限制可推进此事务的响应。
type transaction struct {
	flow                    uint64         // 本事务固定的可靠连接身份，重连不可复用。
	wire                    []byte         // 不变的请求报文，重传时复用。
	destination             netip.AddrPort // 目标传输地址，也是响应来源约束。
	session                 uint64         // 关联通话资源编号。
	method, callID, fromTag string         // 请求方法、Call-ID 和本地 From 标签。
	cseq                    uint32         // 必须精确匹配的请求序号。
	interval                time.Duration  // 当前重传间隔。
	provisional             bool           // INVITE 已收到临时响应后停止请求重传。
	version                 uint64         // 与重传及到期定时器绑定的版本。
}

// mediaConnectUpdate 保存尚未执行的最新协商，不为重复临时响应累计媒体任务。
type mediaConnectUpdate struct {
	operation string        // 完成后生成早期应答还是最终成功应答。
	version   uint64        // 本次协商版本，只能由主循环递增。
	request   media.Request // 独立的对端和载荷参数，入队后不再修改。
}

// forkCleanup 保存未选中分支的短期清理状态；最终响应之后仍保留去重标记，避免重建 BYE。
type forkCleanup struct {
	key       string // 固定 BYE 事务键；同一远端标签的重传复用该键。
	wire      []byte // 全局事务额度不足时复用的无状态 BYE，不另建重传上下文。
	completed bool   // 严格匹配到 BYE 最终响应后只重发 ACK，不再重建清理事务。
}

// Call 保存双腿通话状态及媒体资源所有权，仅由呼叫主循环访问和修改。
type Call struct {
	Dialplan           *dialplan.Extension     // 命中冻结XML时共享只读动作表；无命中保留既有路由。
	Local              bool                    // 明确配置的本地自动应答通道，只有A腿，不创建或宣称有B腿。
	pcmHandle          *PCMHandle              // 当前已确认轮次；媒体身份冻结，音频调用不读取Call。
	pcmPending         *PCMHandle              // 唯一在途begin的TX预留，真实媒体确认前不交付可写句柄。
	rxHandle           *RXHandle               // 当前上行授权/清理句柄，与PCM和提示音TX归属独立。
	rxPending          *RXHandle               // 唯一在途上行订阅；挂断先撤读权，迟回执仍保留清理地址。
	authentication     *inviteAuthentication   // 遇到上游挑战时才分配的有界认证/旧ACK状态。
	BInviteCSeq        uint32                  // 当前初始/认证重试 INVITE 序号；后续 BYE 另用 BLocalCSeq。
	BFlow              uint64                  // B 腿真实连接身份；A 腿保存在 ARequest.TransportID。
	compatUUIDs        [2]string               // 可选 ESL 对双腿发布的兼容 UUID。
	compatVariables    [2]map[string]string    // 可选 ESL 变量，仅由主循环读写。
	ID                 uint64                  // 控制进程内唯一的媒体会话编号。
	AID, BID           string                  // A/B 两腿分别使用的 SIP Call-ID。
	ARequest           *sip.Message            // A 腿初始 INVITE，生成响应和校验重传时使用。
	ASource            netip.AddrPort          // A 腿固定信令来源。
	ATag               string                  // 对 A 腿响应使用的本地标签。
	BTag               string                  // 向 B 腿请求使用的本地标签。
	BRemoteTag         string                  // 已选择 B 腿的远端标签。
	ForkCleanups       map[string]*forkCleanup // 最多八个未选中分支，首次观察后固定三十二秒过期。
	ForkCleanupLimited bool                    // 每通呼叫只记录一次分支清理容量降级，防止重复信令制造无限日志。
	AContact           string                  // A 腿远端对话目标。
	BContact           string                  // B 腿最终响应给出的对话目标。
	BURI               string                  // 初始 B 腿请求 URI。
	BFrom              string                  // B 腿请求的完整 From 头部。
	BTo                string                  // B 腿选定对话的 To 头部。
	InviteBranch       string                  // 初始 B 腿 INVITE 的事务分支。
	AInviteCSeq        uint32                  // A 腿初始 INVITE 序号。
	ARemoteCSeq        uint32                  // A 腿已接受的远端对话内请求序号。
	BRemoteCSeq        uint32                  // B 腿已接受的远端对话内请求序号。
	ALocalCSeq         uint32                  // 本地发往 A 腿的请求序号。
	BLocalCSeq         uint32                  // 本地发往 B 腿的请求序号。
	Worker             int                     // 所属媒体分片。
	Generation         uint64                  // 分配时的媒体进程代次。
	Offer              sip.SDP                 // 选定的音频参数及当前协商电话事件载荷。
	ProcessedOffer     *sip.SDP                // 实时图原始报价；183临时收紧不能抹除200最终应答仍可选的能力。
	Allocation         media.Reply             // 媒体分配确认，包括两腿本地端口。
	LastA              []byte                  // 最近发给 A 腿的响应，供 INVITE 重传使用。
	AStatus            int                     // 最近 A 腿响应状态码；最终响应提交后不可回退。
	AAck               bool                    // A 腿最终响应是否已经确认。
	UASVersion         uint64                  // A 腿响应重传/到期定时器版本。
	UASInterval        time.Duration           // 当前 A 腿最终响应重传间隔。
	Reserved           bool                    // 总并发和分片负载是否仍预留一份容量。
	AllocatePending    bool                    // 分配 RPC 结果仍未返回。
	Allocated          bool                    // 媒体资源已存在且尚未确认释放。
	Releasing          bool                    // 释放 RPC 已提交，避免重复提交。
	Ended              bool                    // 业务结束已经记录，资源可能仍等待释放。
	BInviteSent        bool                    // 已向 B 腿发送初始 INVITE。
	BAnswered          bool                    // 已选定并确认 B 腿成功最终响应。
	BFinal             bool                    // B 腿已经出现最终响应。
	BByeSent           bool                    // 已向 B 腿发起 BYE 事务。
	AByeSent           bool                    // 已向 A 腿发起 BYE 事务。
	UASPending         bool                    // A 腿最终响应仍等待 ACK 或到期。
	Established        bool                    // 成功应答已被 A 腿 ACK 且业务尚未结束。
	BProvisional       bool                    // B 腿已出现允许发送 CANCEL 的临时响应。
	CancelWanted       bool                    // 已记录取消 B 腿的意图，可能仍在等临时响应。
	BCancelSent        bool                    // B 腿 CANCEL 事务已经创建。
	ConnectVersion     uint64                  // 最新连接请求版本，供结果过滤与待处理更新合并使用。
	ConnectInFlight    uint64                  // 当前唯一在途连接版本；零表示没有在途连接。
	ConnectQueued      *mediaConnectUpdate     // 最多保留一个最新待更新目标，覆盖过时早期媒体。
	ReleaseRetries     uint8                   // 队列背压后的连续释放重试数，用于有界退避。
	Created            time.Time               // 呼叫进入控制面时刻。
}

// Stats 使用原子变量跨接收、主循环和 HTTP 线程发布计数；它们并非同一时刻的事务快照。
type Stats struct {
	Timers      atomic.Uint64 // 当前活跃定时器数。
	Incoming    atomic.Uint64 // 收到的有效传输信令消息总数。
	Malformed   atomic.Uint64 // 信令解析失败数。
	Untrusted   atomic.Uint64 // 来源白名单拒绝数。
	QueueDrops  atomic.Uint64 // 信令输入队列满导致的丢弃数。
	SendErrors  atomic.Uint64 // 信令发送失败数。
	Accepted    atomic.Uint64 // 完成控制面资源预留的新呼叫数。
	Rejected    atomic.Uint64 // 容量或保护策略拒绝的新呼叫数。
	Completed   atomic.Uint64 // 已记录业务结束的呼叫数。
	Abnormal    atomic.Uint64 // 以异常原因结束的呼叫数。
	Active      atomic.Uint64 // 仍持有或等待媒体资源的预留数。
	Established atomic.Uint64 // 已 ACK 且业务尚未结束的呼叫数。
}

// Server 管理信令、媒体及管理入口；映射、定时器与分片负载只属于 Run 主循环。
type Server struct {
	dialplan            *dialplan.Plan                          // 启动时严格加载的可选XML计划。
	trunk               *sipTrunk                               // 唯一主循环持有的可选注册与私有认证状态。
	trunkSnapshot       atomic.Pointer[SIPRegistrationStatus]   // 管理线程只读快照，不含账户或凭据。
	applicationRequests chan applicationRequest                 // ESL应用进入唯一呼叫循环的有界队列。
	applicationResults  chan applicationMediaResult             // 媒体应用完成结果，不由后台修改通话。
	applications        *applicationEngine                      // 有界应用调度、收号和提示音状态。
	pcm                 *pcmService                             // 仅轮次授权进入主循环，PCM批次走独立二进制媒体通道。
	rx                  *rxService                              // 仅上行订阅授权进入主循环，逐帧读取由固定媒体lane承接。
	asr                 *asrManager                             // 可选供应商适配；有界流额度和固定清理执行者独立于信令主循环。
	pcmEndpoint         *pcmEndpoint                            // 可选私有PCM外部入口；关闭顺序先断开连接再停止媒体。
	flowReferences      map[uint64]*sipFlowReferences           // 仅主循环访问的连接资源索引，断线不扫描全局通话。
	compatChannels      map[string]compatibilityChannel         // 可选 ESL UUID 索引，仅由主循环按生命周期维护。
	streams             *sipStreams                             // 可选 TCP/TLS 连接集合。
	streamClosed        chan uint64                             // 将连接关闭传回唯一呼叫主循环。
	compatRequests      chan compatibilityRequest               // 可选 ESL 管理请求的有界队列。
	compatEvents        func(string, map[string]string, []byte) // 仅启动前设置的可选 ESL 事件发布器。
	Config              config.Config                           // 本次启动真实生效的冻结配置。
	pool                *media.Pool                             // Rust 媒体分片池。
	journal             *journal.Journal                        // 非阻塞提交的业务事件日志。
	conn                *net.UDPConn                            // 当前 UDP SIP 监听及发送 socket。
	http                *http.Server                            // 本地管理与指标服务器。
	ctx                 context.Context                         // 服务生命周期上下文。
	cancel              context.CancelFunc                      // 强制停止服务的取消函数。
	normal, critical    chan datagram                           // 有界普通请求队列与已有对话优先队列。
	results             chan mediaResult                        // 媒体后台任务送回的完成结果。
	calls               map[uint64]*Call                        // 按资源编号索引的通话。
	byDialog            map[string]uint64                       // 双腿 Call-ID 到资源编号的映射。
	transactions        map[string]*transaction                 // 出站事务表。
	cache               map[string]cacheEntry                   // 按事务与来源索引的最终响应缓存。
	timers              timerHeap                               // 业务定时器最小堆。
	timerIndex          map[timerKey]*timerItem                 // 支持替换和取消的定时器身份索引。
	bucket              *bucket                                 // 未装配动态保护器时的静态限流后备路径。
	guard               *AdmissionGuard                         // 动态峰值策略；HTTP 更新与呼叫检查由其内部锁串行化。
	admin               *adminStore                             // 配置版本、待重启草稿及 FreeSWITCH XML 导出状态。
	adminToken          string                                  // 本进程管理写入令牌，避免跨站请求修改本机服务。
	adminLoad           *adminLoad                              // 管理连接、重请求预算及控制进程资源观测。
	codecInfo           codecProbeCache                         // 当前可信媒体程序的有界只读音频能力探测。
	benchmarks          *benchmarkManager                       // 独立测试实例的排他调度与进程清理，不共享生产媒体资源。
	stopping            atomic.Bool                             // 终止信号已经到达；此后页面不能取消退出排空。
	workerLoads         []int                                   // 各分片仍预留的媒体会话数。
	nextID, nextVersion uint64                                  // 主循环生成的资源编号及缓存/事务版本。
	runID               string                                  // 本次启动唯一标识，写入事件日志。
	draining            atomic.Bool                             // 是否停止新准入但继续处理已有通话。
	started             time.Time                               // 启动时间，用于运行时长观测。
	Stats               Stats                                   // 可跨线程读取的统计。
}

// New 读取已保存管理草稿并创建实际运行配置，依次启动日志、媒体、信令和管理监听。
// 任一步失败都会关闭前面已创建的资源，不返回半初始化的服务。
func New(parent context.Context, c config.Config) (*Server, error) {
	management, loadErr := openAdminStore(c)
	if loadErr != nil {
		return nil, loadErr
	}
	c = management.active
	guard, loadErr := NewAdmissionGuard(c.Limits, management.state.Policy)
	if loadErr != nil {
		return nil, loadErr
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Server{Config: c, ctx: ctx, cancel: cancel, normal: make(chan datagram, 1024), critical: make(chan datagram, 4096), results: make(chan mediaResult, c.Limits.MaxCalls*2+c.Media.Workers*128), calls: make(map[uint64]*Call), byDialog: make(map[string]uint64), transactions: make(map[string]*transaction), cache: make(map[string]cacheEntry), bucket: newBucket(c.Limits.CallsPerSecond, c.Limits.BurstCalls), workerLoads: make([]int, c.Media.Workers), runID: randomID(), started: time.Now()}
	s.admin, s.guard, s.adminToken = management, guard, randomID()
	s.compatRequests = make(chan compatibilityRequest, 128)
	if err := s.initDialplan(); err != nil {
		cancel()
		return nil, err
	}
	if err := s.initTrunk(); err != nil {
		cancel()
		return nil, err
	}
	var err error
	s.journal, err = journal.Open(c.Journal.Path, c.Journal.QueueCapacity)
	if err != nil {
		cancel()
		return nil, err
	}
	s.pool, err = media.Start(ctx, c)
	if err != nil {
		s.journal.Close()
		cancel()
		return nil, err
	}
	addr := net.UDPAddrFromAddrPort(netip.MustParseAddrPort(c.SIP.Listen))
	s.initPCM()
	s.initRX()
	s.initApplications()
	s.conn, err = net.ListenUDP("udp4", addr)
	if err != nil {
		s.pool.Close()
		s.journal.Close()
		cancel()
		return nil, err
	}
	_ = s.conn.SetReadBuffer(4 << 20)
	if err = s.startTransports(); err != nil {
		cancel()
		s.closeTransports()
		s.conn.Close()
		s.pool.Close()
		s.journal.Close()
		return nil, err
	}
	listener, err := net.Listen("tcp4", c.Admin.Listen)
	if err != nil {
		cancel()
		s.closeTransports()
		s.conn.Close()
		s.pool.Close()
		s.journal.Close()
		cancel()
		return nil, err
	}
	if err = s.startPCMTransport(); err != nil {
		_ = listener.Close()
		cancel()
		s.closePCMTransport()
		s.closeTransports()
		_ = s.conn.Close()
		s.pool.Close()
		s.journal.Close()
		return nil, err
	}
	s.initASR()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /v1/status", s.status)
	s.registerAdmin(mux)
	s.http = &http.Server{Handler: s.protectAdmin(mux), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		if e := s.http.Serve(s.limitAdminConnections(listener)); e != nil && e != http.ErrServerClosed {
			slog.Error("admin server failed", "error", e)
			s.draining.Store(true)
		}
	}()
	return s, nil
}

// Run 串行推进所有呼叫状态；首次停止通知仅排空，强制取消上下文才直接退出。
func (s *Server) Run(stop <-chan struct{}) error {
	// 手工构造的旧测试/嵌入式实例可不启用PCM；nil通道使对应select分支保持禁用。
	var pcmRequests <-chan *pcmBeginRequest
	var pcmResults <-chan pcmBeginResult
	if s.pcm != nil {
		pcmRequests, pcmResults = s.pcm.requests, s.pcm.results
	}
	// 未启用RX的手工测试保留nil分支；订阅执行由固定控制协程完成，不阻塞信令。
	var rxRequests <-chan *rxSubscribeRequest
	var rxResults <-chan rxSubscribeResult
	if s.rx != nil {
		rxRequests, rxResults = s.rx.requests, s.rx.results
	}
	go s.receive()
	s.event(nil, "controller_started", "")
	s.startRegistration()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	shuttingDown := false
	for {
		// 每轮先处理最多八条已有对话消息，为 ACK、BYE 和响应保留执行机会。
		for i := 0; i < 8; i++ {
			select {
			case d := <-s.critical:
				s.handle(d)
			default:
				goto next
			}
		}
	next:
		if shuttingDown && s.Stats.Active.Load() == 0 && len(s.transactions) == 0 && !s.hasPendingFinals() && !s.registrationPending() {
			return nil
		}
		select {
		case <-stop:
			s.admin.mu.Lock()
			s.stopping.Store(true)
			s.draining.Store(true)
			s.admin.mu.Unlock()
			s.benchmarks.stopAll()
			shuttingDown = true
			s.stopRegistration()
			stop = nil
			slog.Info("draining existing calls")
		case <-s.ctx.Done():
			return s.ctx.Err()
		case d := <-s.critical:
			s.handle(d)
		case d := <-s.normal:
			s.handle(d)
		case request := <-s.compatRequests:
			s.handleCompatibilityRequest(request)
		case request := <-s.applicationRequests:
			s.handleApplicationRequest(request)
		case result := <-s.applicationResults:
			s.handleApplicationMedia(result)
		case request := <-pcmRequests:
			s.handlePCMBegin(request)
		case result := <-pcmResults:
			s.handlePCMBeginResult(result)
		case request := <-rxRequests:
			s.handleRXSubscribe(request)
		case result := <-rxResults:
			s.handleRXSubscribeResult(result)
		case id := <-s.streamClosed:
			s.streamEnded(id)
		case r := <-s.results:
			s.onMedia(r)
		case f := <-s.pool.Failures:
			s.workerFailed(f)
		case <-ticker.C:
			s.expireTimers()
			s.advanceApplications(time.Now())
		}
	}
}

// hasPendingFinals 检查最终响应是否仍需重传，防止资源释放后过早结束排空。
func (s *Server) hasPendingFinals() bool {
	for _, c := range s.calls {
		if c.UASPending {
			return true
		}
	}
	return false
}

// Close 在 Run 结束后关闭监听、媒体进程及日志；构造成功后由拥有者调用一次。
func (s *Server) Close() {
	defer s.clearTrunkCredentials()
	s.asr.beginClose() // 第一阶段只关识别网络；未知RX额度继续保留至真实媒体池退出。
	s.closePCM()
	s.closeRX()
	s.draining.Store(true)
	s.benchmarks.close()
	s.cancel()
	s.closePCMTransport()
	s.closeTransports()
	_ = s.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.http.Shutdown(ctx)
	s.pool.Close()
	s.asr.afterPoolClose() // 第二阶段此处才确认池已经关闭，避免撤权被误判为资源回收。
	if s.rx != nil {
		s.rx.workers.Wait()
	} // 服务上下文已取消，固定RX控制协程退出后再完成关闭。
	s.journal.Close()
}

// receive 在独立协程接收并解析 UDP 信令，只把通过来源校验的报文送入有界队列。
func (s *Server) receive() {
	buffer := make([]byte, 65536)
	for {
		n, source, err := s.conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			return
		}
		source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
		s.Stats.Incoming.Add(1)
		if !config.Allowed(source.Addr(), s.Config.SIP.TrustedNetworks) && !(s.Config.SIP.StreamOptions().UpstreamTransport == "udp" && source.String() == s.Config.SIP.Upstream) {
			s.Stats.Untrusted.Add(1)
			continue
		}
		message, e := sip.Parse(buffer[:n])
		if e != nil {
			s.Stats.Malformed.Add(1)
			continue
		}
		if message.Transport() != "udp" {
			s.Stats.Malformed.Add(1)
			continue
		}
		_ = s.enqueue(datagram{message, source})
	}
}

// enqueue 为所有真实传输复用同一组有界队列；流式传输满队列时关闭连接以避免静默丢信令。
func (s *Server) enqueue(d datagram) bool {
	channel := s.normal
	message := d.message
	if message.Status > 0 || message.Method == "ACK" || message.Method == "BYE" || message.Method == "CANCEL" {
		channel = s.critical
	}
	select {
	case channel <- d:
		return true
	case <-s.ctx.Done():
		return false
	default:
		s.Stats.QueueDrops.Add(1)
		return false
	}
}

// send 同步发送一条 SIP 报文，最多等待二十毫秒；当前出口阻塞仍会占用调用它的主循环。
func (s *Server) send(wire []byte, destination netip.AddrPort) {
	if len(wire) == 0 {
		return
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := s.conn.WriteToUDPAddrPort(wire, destination); err != nil {
		s.Stats.SendErrors.Add(1)
	}
}

// reply 生成无会话响应并缓存最终结果，使重传可重放相同标签和重试建议。
func (s *Server) reply(d datagram, status int, reason string, extra ...sip.Header) {
	tag := ""
	if status > 100 {
		tag = randomID()[:16]
	}
	wire := sip.Response(d.message, status, reason, tag, "", d.source.String(), nil, extra...)
	s.sendFlow(wire, d.source, d.message.TransportID)
	if status >= 200 {
		s.remember(d.message.Key()+"|"+transportKey(d.source, d.message.TransportID), wire, d.source, d.message.TransportID)
	}
}

// remember 在事务容量内保存最终响应，刷新版本及三十二秒过期时间。
func (s *Server) remember(key string, wire []byte, source netip.AddrPort, flows ...uint64) {
	var flow uint64
	if len(flows) > 0 {
		flow = flows[0]
	}
	if _, ok := s.cache[key]; !ok && len(s.cache)+len(s.calls) >= s.Config.Limits.MaxTransactions {
		return
	}
	s.nextVersion++
	v := s.nextVersion
	s.cache[key] = cacheEntry{wire: wire, source: source, version: v, flow: flow}
	if refs := s.flowRefs(flow, true); refs != nil {
		refs.cache[key] = struct{}{}
	}
	s.schedule("cache", 0, key, v, 32*time.Second)
}

// submit 在呼叫主循环内按顺序进入有界媒体队列，完成结果由固定分片协程送回。
// 入队失败尚未执行命令，直接在主循环处理；不为每个请求创建等待 goroutine。
func (s *Server) submit(c *Call, operation string, version uint64, request media.Request) {
	request.Generation = c.Generation
	id := c.ID
	err := s.pool.Submit(s.ctx, c.Worker, request, func(reply media.Reply, err error) {
		select {
		case s.results <- mediaResult{id, operation, version, reply, err}:
		case <-s.ctx.Done():
		}
	})
	if err != nil {
		s.onMedia(mediaResult{session: id, operation: operation, version: version, err: err})
	}
}

// event 提交生命周期日志；提交失败时记录诊断，后续新准入通过日志健康检查收紧。
func (s *Server) event(c *Call, kind, reason string) {
	e := journal.Event{Time: time.Now().UTC(), RunID: s.runID, Kind: kind, Reason: reason}
	if c != nil {
		e.CallID = c.AID
	}
	if !s.journal.Record(e) {
		slog.Error("journal unavailable; new admission disabled", "event", kind)
	}
	s.compatibilityEvent(c, kind, reason)
}

// randomID 从系统随机源生成一百二十八位标识；随机源失败时不继续使用不可靠的事务身份。
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// contact 构造当前生效信令地址对应的本机 Contact URI。
func (s *Server) contact() string { return "sip:rustswitch@" + s.Config.SIP.Advertise }

// upstream 读取已经在启动校验中确认有效的固定上游传输地址。
func (s *Server) upstream() netip.AddrPort { return netip.MustParseAddrPort(s.Config.SIP.Upstream) }

// writeJSON 设置 JSON 类型并编码响应；状态码需要由调用者在编码前设置。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// health 表示控制进程的 HTTP 入口仍能响应，不等同于当前允许新呼叫。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": "ok", "version": "0.3.0"})
}

// ready 合并排空、日志、媒体压力和峰值准入快照；返回成功仍只是瞬时可接入信号。
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ready := !s.draining.Load() && s.journal.Healthy()
	if s.guard != nil {
		ready = ready && s.guardSnapshot().LimitReason == ""
	}
	available := false
	for _, worker := range s.pool.Workers {
		available = available || worker.Admission(time.Now()).Reason == ""
	}
	ready = ready && available
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	writeJSON(w, map[string]bool{"ready": ready})
}

// status 汇总原子统计与管理版本，不遍历主循环私有的通话和事务映射。
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	workers := []map[string]any{}
	for _, worker := range s.pool.Workers {
		workers = append(workers, map[string]any{"id": worker.Config.WorkerID, "pid": worker.PID.Load(), "generation": worker.Generation.Load(), "healthy": worker.Healthy.Load(), "restarts": worker.Restarts.Load(), "stats": worker.Snapshot(), "admission": worker.Admission(time.Now())})
	}
	s.admin.mu.Lock()
	revision, restartRequired := s.admin.state.Revision, s.admin.restartRequiredLocked()
	s.admin.mu.Unlock()
	writeJSON(w, map[string]any{"version": "0.3.0", "asr_stream": s.ASRSnapshot(), "sip_registration": s.RegistrationStatus(), "controller": s.controllerSnapshot(), "config_revision": revision, "restart_required": restartRequired, "guard": s.guardSnapshot(), "stopping": s.stopping.Load(), "draining": s.draining.Load(), "active_timers": s.Stats.Timers.Load(), "active_calls": s.Stats.Active.Load(), "established_calls": s.Stats.Established.Load(), "uptime_seconds": time.Since(s.started).Seconds(), "workers": workers, "journal_healthy": s.journal.Healthy(), "journal_written_events": s.journal.Written.Load(), "journal_synced_events": s.journal.Synced.Load(), "journal_lost_events": s.journal.Lost.Load()})
}

// metrics 输出 Prometheus 文本指标，区分新准入拒绝、资源占用和媒体分片压力。
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	if s.guard != nil {
		guard := s.guardSnapshot()
		fmt.Fprintf(w, "rustswitch_guard_effective_cps %.6g\nrustswitch_guard_active_limit %d\nrustswitch_guard_establishing_limit %d\nrustswitch_guard_rejected_total %d\nrustswitch_guard_rejected_active_total %d\nrustswitch_guard_rejected_establishing_total %d\nrustswitch_guard_rejected_rate_total %d\n", guard.EffectiveCPS, guard.Policy.MaxActiveCalls, guard.Policy.MaxEstablishingCalls, guard.Rejected, guard.RejectedActive, guard.RejectedEstablishing, guard.RejectedRate)
	}
	values := map[string]uint64{"sip_received_total": s.Stats.Incoming.Load(), "sip_malformed_total": s.Stats.Malformed.Load(), "sip_untrusted_total": s.Stats.Untrusted.Load(), "sip_queue_drops_total": s.Stats.QueueDrops.Load(), "sip_send_errors_total": s.Stats.SendErrors.Load(), "calls_accepted_total": s.Stats.Accepted.Load(), "calls_rejected_total": s.Stats.Rejected.Load(), "calls_completed_total": s.Stats.Completed.Load(), "calls_abnormal_total": s.Stats.Abnormal.Load(), "active_timers": s.Stats.Timers.Load(), "active_calls": s.Stats.Active.Load(), "established_calls": s.Stats.Established.Load(), "journal_written_total": s.journal.Written.Load(), "journal_synced_total": s.journal.Synced.Load(), "journal_lost_total": s.journal.Lost.Load()}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "rustswitch_%s %d\n", k, values[k])
	}
	for _, worker := range s.pool.Workers {
		admission := worker.Admission(time.Now())
		blocked := 0
		if admission.Reason != "" {
			blocked = 1
		}
		fmt.Fprintf(w, "rustswitch_media_admission_blocked{worker=\"%d\",reason=\"%s\"} %d\n", worker.Config.WorkerID, admission.Reason, blocked)
		fmt.Fprintf(w, "rustswitch_media_cpu_cores_used{worker=\"%d\"} %.6g\n", worker.Config.WorkerID, admission.CPUCores)
		for k, v := range worker.Snapshot() {
			switch value := v.(type) {
			case float64:
				fmt.Fprintf(w, "rustswitch_media_%s{worker=\"%d\",generation=\"%d\"} %.6g\n", k, worker.Config.WorkerID, worker.Generation.Load(), value)
			}
		}
	}
	s.controllerMetrics(w)
	s.asrMetrics(w)
}

// errUnsupported 标记当前 UDP 原型无法完成的扩展能力，不伪装为协商成功。
var errUnsupported = errors.New("unsupported protocol feature")

// supportedInitial 筛选当前可接受的初始 INVITE 扩展及 SDP 内容类型；具体媒体参数另行校验。
func supportedInitial(m *sip.Message) error {
	if m.Header("record-route") != "" || m.Header("route") != "" || m.Header("require") != "" || m.Header("session-expires") != "" {
		return errUnsupported
	}
	if !strings.EqualFold(strings.TrimSpace(strings.Split(m.Header("content-type"), ";")[0]), "application/sdp") {
		return errUnsupported
	}
	return nil
}
