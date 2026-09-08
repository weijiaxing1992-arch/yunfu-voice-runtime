package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"rustswitch/control/internal/codecprofile"
	"rustswitch/control/internal/config"
	"sync"
	"time"
)

// benchmarkChildEnv 只由父进程设置，防止独立测试实例再次创建测试进程树。
const benchmarkChildEnv = "RUSTSWITCH_TEST_CHILD"

// benchmarkRequest 是封闭的测试输入，不接受可执行文件、主机、端口或磁盘路径。
type benchmarkRequest struct {
	RequestID       string `json:"request_id"`       // 浏览器生成的幂等键，仅在当前控制进程生命周期内有效。
	Mode            string `json:"mode"`             // pressure 为容量测试；phone 为一组模拟主被叫。
	Concurrency     int    `json:"concurrency"`      // 目标同时建立通话数，不是累计呼叫数。
	CPS             int    `json:"cps"`              // 建立阶段每秒发起速率。
	DurationSeconds int    `json:"duration_seconds"` // 全部建立完成后发送双向媒体的秒数。
	Payload         int    `json:"payload"`          // codecprofile 中固定测试配置的RTP载荷号；结构有效媒体不等于编解码质量认证。
	MediaProcessing string `json:"media_processing"` // 独立测试冻结的媒体模式；旧请求固定归一为relay，不继承主服务模式。
}

// benchmarkProgress 来自发生器的原子进度文件；建立数是累计结果，不能解释为当前活跃数。
type benchmarkProgress struct {
	Phase               string  `json:"phase"`
	RequestedCalls      int     `json:"requested_calls"`
	AttemptedCalls      int     `json:"attempted_calls"`
	EstablishedCalls    int     `json:"established_calls"`
	SetupFailed         int     `json:"setup_failed"`
	ElapsedSeconds      float64 `json:"elapsed_seconds"`
	MediaElapsedSeconds float64 `json:"media_elapsed_seconds"`
}

// benchmarkSample 只记录成功读取的子实例快照；媒体计数为子实例累计数，不推算端到端丢包。
// CPU 和峰值内存只汇总 Rust 媒体进程，发生器资源以最终报告为准。
type benchmarkSample struct {
	At                   string           `json:"at"`
	ElapsedSeconds       float64          `json:"elapsed_seconds"`
	ActiveCalls          uint64           `json:"active_calls"`
	EstablishedCalls     uint64           `json:"established_calls"`
	MediaActiveCalls     uint64           `json:"media_active_calls"`
	RXPackets            uint64           `json:"rx_packets"`
	TXPackets            uint64           `json:"tx_packets"`
	SocketRXDrops        uint64           `json:"socket_rx_drops"`
	SendErrors           uint64           `json:"send_errors"`
	SendQueueDrops       uint64           `json:"send_queue_drops"`
	SendExpired          uint64           `json:"send_expired"`
	InvalidPackets       uint64           `json:"invalid_packets"`
	RateLimited          uint64           `json:"rate_limited"`
	UserCPUSeconds       float64          `json:"user_cpu_seconds"`
	SystemCPUSeconds     float64          `json:"system_cpu_seconds"`
	PeakResidentBytes    uint64           `json:"peak_resident_bytes"`
	WorkerCount          int              `json:"worker_count"`
	HealthyWorkers       int              `json:"healthy_workers"`
	Controller           *controllerState `json:"controller"` // 测试控制进程的CPU、协程、线程和队列观测；未取得时为null。
	MediaProcessing      string           `json:"media_processing"`
	Processing           map[string]any   `json:"processing"`              // relay为null；g711只有完整核验的处理统计才发布，缺失不能填零。
	WorkerStatsSampledAt []time.Time      `json:"worker_stats_sampled_at"` // worker实际统计发布时间，与HTTP读取时刻分别保留。
}

// benchmarkCleanup 区分每个资源的确认状态；未能核实释放时不能把任务标记为成功。
type benchmarkCleanup struct {
	Complete             bool   `json:"complete"`
	ControllerExited     bool   `json:"controller_exited"`
	GeneratorExited      bool   `json:"generator_exited"`
	MediaProcessesExited bool   `json:"media_processes_exited"`
	PortsReleased        bool   `json:"ports_released"`
	WorkDirRemoved       bool   `json:"work_dir_removed"`
	Detail               string `json:"detail"`
}

// benchmarkRun 是页面可读取的任务副本；时间为空表示尚未进入相应生命周期阶段。
type benchmarkRun struct {
	benchmarkRequest
	ID              string            `json:"id"`
	Status          string            `json:"status"`
	Phase           string            `json:"phase"`
	CreatedAt       string            `json:"created_at"`
	StartedAt       string            `json:"started_at"`
	FinishedAt      string            `json:"finished_at"`
	ElapsedSeconds  float64           `json:"elapsed_seconds"`
	Progress        benchmarkProgress `json:"progress"`
	Samples         []benchmarkSample `json:"samples"`
	Error           string            `json:"error"`
	Failure         *benchmarkFailure `json:"failure,omitempty"` // 机器可读失败阶段；预检未启动不会归类为媒体验收失败。
	ReportAvailable bool              `json:"report_available"`
	Cleanup         benchmarkCleanup  `json:"cleanup"`
}

// benchmarkFailure 让界面区分资源预检和真实运行失败，不从中文错误消息猜测阶段。
type benchmarkFailure struct {
	Stage            string                 `json:"stage"`
	Code             string                 `json:"code"`
	Message          string                 `json:"message"`
	ProcessesStarted bool                   `json:"processes_started"`
	Capacity         *benchmarkPortCapacity `json:"capacity,omitempty"`
}

// benchmarkPreflightError 保留底层错误身份，并为尚未启动的资源问题提供稳定原因码。
type benchmarkPreflightError struct {
	code  string
	cause error
}

func (e *benchmarkPreflightError) Error() string { return e.cause.Error() }
func (e *benchmarkPreflightError) Unwrap() error { return e.cause }

// benchmarkReport 保留失败证据和有界日志；result 只有发生器实际输出有效 JSON 时才存在。
type benchmarkReport struct {
	Run      benchmarkRun      `json:"run"`
	Result   json.RawMessage   `json:"result"`
	Logs     map[string]string `json:"logs"`
	Evidence map[string]any    `json:"evidence"`
}

// benchmarkTask 内部生命周期状态只由 manager.mu 保护；取消函数可以重复调用。
type benchmarkTask struct {
	run     benchmarkRun
	created time.Time
	cancel  context.CancelFunc
	done    chan struct{}
	report  benchmarkReport
}

// benchmarkKey 保存精简幂等记录；历史报告被淘汰后仍返回 410，避免网络重试意外再次加压。
type benchmarkKey struct {
	request benchmarkRequest
	id      string
}

// benchmarkManager 把启动排他、停止和历史发布放在同一把锁下，HTTP 不持锁等待子进程。
// 最多保存 20 份完整报告和 4096 个幂等键；达到键上限后拒绝新建，避免常驻服务内存无界增长。
type benchmarkManager struct {
	mu            sync.Mutex
	server        *Server
	enabled       bool
	closed        bool
	blockedReason string // 清理未确认时保留冻结原因，不能误说服务正在退出。
	active        *benchmarkTask
	history       []*benchmarkTask
	keys          map[string]benchmarkKey
	runner        func(context.Context, *benchmarkTask) // 测试可替换执行器，生产固定为 runReal。
	portPreview   *benchmarkPortCapacity                // 以配置版本缓存纯规划预览，不在只读刷新时反复探测socket。
	portRevision  uint64
}

// capacityPreview 只计算回退回环地址在CPS1000下的端口模型；真实地址可绑定性及占用仍须任务预检。
func (m *benchmarkManager) capacityPreview() benchmarkPortCapacity {
	m.server.admin.mu.Lock()
	revision := m.server.admin.state.Revision
	configs := []config.Config{cloneAdminConfig(m.server.admin.active), cloneAdminConfig(m.server.admin.state.Desired)}
	m.server.admin.mu.Unlock()
	m.mu.Lock()
	if m.portPreview != nil && m.portRevision == revision {
		value := *m.portPreview
		m.mu.Unlock()
		return value
	}
	m.mu.Unlock()
	q := benchmarkRequest{Mode: "pressure", Concurrency: 1000, CPS: 1000, DurationSeconds: 1}
	value := benchmarkCapacity("127.0.0.1", configs, q, nil, false)
	m.mu.Lock()
	m.portPreview, m.portRevision = &value, revision
	m.mu.Unlock()
	return value
}

// newBenchmarkManager 不探测端口或启动进程，保持服务启动和只读管理请求的成本有界。
func newBenchmarkManager(s *Server) *benchmarkManager {
	m := &benchmarkManager{server: s, enabled: os.Getenv(benchmarkChildEnv) == "", keys: make(map[string]benchmarkKey)}
	m.runner = m.runReal
	return m
}

// registerBenchmark 注册精确 API 和固定静态资源；所有路由继承 protectAdmin 的同源保护。
func (s *Server) registerBenchmark(mux *http.ServeMux) {
	s.benchmarks = newBenchmarkManager(s)
	mux.HandleFunc("GET /tests.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "tests.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /tests.css", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, "tests.css", "text/css; charset=utf-8") })
	mux.HandleFunc("GET /v1/tests", s.getTests)
	mux.HandleFunc("POST /v1/tests", s.startTest)
	mux.HandleFunc("GET /v1/tests/{id}", s.getTest)
	mux.HandleFunc("POST /v1/tests/{id}/stop", s.stopTest)
	mux.HandleFunc("GET /v1/tests/{id}/report", s.getTestReport)
}

// benchmarkRequestID 禁止空键、超长键和控制字符，方便浏览器恢复一次未知网络结果。
var benchmarkRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// normalized 在幂等比较前统一旧客户端默认值，避免网络重试改变实际测试模式。
func (q benchmarkRequest) normalized() benchmarkRequest {
	if q.MediaProcessing == "" {
		q.MediaProcessing = "relay"
	}
	return q
}

// validate 在任何资源创建之前检查封闭枚举和硬边界。
func (q benchmarkRequest) validate() error {
	q = q.normalized()
	if !benchmarkRequestID.MatchString(q.RequestID) {
		return errors.New("request_id 必须是 1 到 128 个字母、数字或 _ . : - 字符")
	}
	if q.Mode != "pressure" && q.Mode != "phone" {
		return errors.New("mode 仅支持 pressure 或 phone")
	}
	if q.Concurrency < 1 || q.Concurrency > 10000 || q.Mode == "phone" && q.Concurrency != 1 {
		return errors.New("并发必须为 1 到 10000；模拟电话固定为 1")
	}
	if q.CPS < 1 || q.CPS > 1000 || q.DurationSeconds < 1 || q.DurationSeconds > 300 {
		return errors.New("CPS 必须为 1 到 1000，媒体时长必须为 1 到 300 秒")
	}
	if q.Payload < 0 || q.Payload > 127 {
		return errors.New("payload 必须在 0 到 127 范围内")
	}
	if _, ok := codecprofile.Lookup(uint8(q.Payload)); !ok {
		return errors.New("payload 不在已定义的测试编码配置中")
	}
	if q.MediaProcessing != "relay" && q.MediaProcessing != "g711" {
		return errors.New("media_processing 仅支持 relay 或 g711")
	}
	if q.MediaProcessing == "g711" && q.Payload != 0 && q.Payload != 8 {
		return errors.New("g711 实时转码测试仅支持 A 腿 payload 0 或 8；B 腿固定使用另一种 G711 律")
	}
	return nil
}

// snapshotLocked 拷贝切片避免 HTTP 编码和采样追加并发访问同一数组，调用者必须持锁。
func (m *benchmarkManager) snapshotLocked(t *benchmarkTask, samples bool) benchmarkRun {
	r := t.run
	r.Samples = []benchmarkSample{}
	if samples {
		r.Samples = append(r.Samples, t.run.Samples...)
	}
	if !r.ReportAvailable {
		r.ElapsedSeconds = time.Since(t.created).Seconds()
	}
	return r
}

// findLocked 只查保留中的任务；已淘汰的幂等键不会恢复已删除的报告。
func (m *benchmarkManager) findLocked(id string) *benchmarkTask {
	if m.active != nil && m.active.run.ID == id {
		return m.active
	}
	for _, t := range m.history {
		if t.run.ID == id {
			return t
		}
	}
	return nil
}

// getTests 返回真实运行任务和最近终态摘要；能力档位不代表本机资源预检一定通过。
func (s *Server) getTests(w http.ResponseWriter, r *http.Request) {
	m := s.benchmarks
	m.mu.Lock()
	var current *benchmarkRun
	if m.active != nil {
		v := m.snapshotLocked(m.active, true)
		current = &v
	}
	history := []benchmarkRun{}
	for _, t := range m.history {
		history = append(history, m.snapshotLocked(t, false))
	}
	reason := ""
	if !m.enabled {
		reason = "独立测试实例禁止嵌套创建测试"
	}
	if m.closed {
		reason = "服务正在退出"
	}
	if m.blockedReason != "" {
		reason = m.blockedReason
	}
	enabled := m.enabled && !m.closed
	m.mu.Unlock()
	writeJSON(w, map[string]any{"enabled": enabled, "disabled_reason": reason, "capabilities": map[string]any{
		"modes": []string{"pressure", "phone"}, "concurrency_options": []int{500, 1000, 2000, 3000, 5000},
		"max_concurrency": 10000, "max_cps": 1000, "max_duration_seconds": 300, "supported_payloads": codecprofile.TestPayloads(), "codec_profiles": codecprofile.List(),
		"media_processing_modes": []string{"relay", "g711"}, "default_media_processing": "relay", "processing_payloads": map[string]any{"relay": codecprofile.TestPayloads(), "g711": []int{0, 8}},
		"scope": "isolated_capacity_test", "production_capacity_certified": false,
		"port_capacity": m.capacityPreview(),
	}, "current": current, "history": history})
}

// startTest 原子占有唯一测试槽，随后异步预检；资源失败也保留一份可下载的任务报告。
func (s *Server) startTest(w http.ResponseWriter, r *http.Request) {
	var q benchmarkRequest
	if !decodeAdmin(w, r, &q) {
		return
	}
	q = q.normalized()
	if err := q.validate(); err != nil {
		adminError(w, 422, err)
		return
	}
	m := s.benchmarks
	m.mu.Lock()
	if !m.enabled {
		m.mu.Unlock()
		adminError(w, 403, errors.New("测试子实例禁止嵌套测试"))
		return
	}
	if key, ok := m.keys[q.RequestID]; ok {
		if key.request != q {
			m.mu.Unlock()
			adminError(w, 409, errors.New("request_id 已用于不同参数"))
			return
		}
		t := m.findLocked(key.id)
		if t == nil {
			m.mu.Unlock()
			adminError(w, 410, errors.New("该幂等请求的报告已过期，不会重新执行"))
			return
		}
		v := m.snapshotLocked(t, true)
		m.mu.Unlock()
		writeJSON(w, v)
		return
	}
	if m.closed || s.stopping.Load() || m.active != nil {
		m.mu.Unlock()
		adminError(w, 409, errors.New("已有测试正在执行，或服务正在退出"))
		return
	}
	if len(m.keys) >= 4096 {
		m.mu.Unlock()
		adminError(w, 429, errors.New("本进程测试幂等记录已达 4096 条；重启后才能创建新任务"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	t := &benchmarkTask{created: now, cancel: cancel, done: make(chan struct{}), run: benchmarkRun{benchmarkRequest: q, ID: randomID(), Status: "preparing", Phase: "preflight", CreatedAt: now.UTC().Format(time.RFC3339Nano), Samples: []benchmarkSample{}, Progress: benchmarkProgress{Phase: "preflight", RequestedCalls: q.Concurrency}}}
	m.active = t
	m.keys[q.RequestID] = benchmarkKey{request: q, id: t.run.ID}
	v := m.snapshotLocked(t, true)
	m.mu.Unlock()
	go m.runner(ctx, t)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(202)
	writeJSON(w, v)
}

// getTest 返回保留中的完整任务详情；ID 仅查内存索引，不参与任何文件路径计算。
func (s *Server) getTest(w http.ResponseWriter, r *http.Request) {
	m := s.benchmarks
	m.mu.Lock()
	t := m.findLocked(r.PathValue("id"))
	if t == nil {
		m.mu.Unlock()
		adminError(w, 404, errors.New("测试记录不存在或已过期"))
		return
	}
	v := m.snapshotLocked(t, true)
	m.mu.Unlock()
	writeJSON(w, v)
}

// stopTest 仅取消指定任务的执行上下文；重复停止终态任务返回原结果。
func (s *Server) stopTest(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if !decodeAdmin(w, r, &body) {
		return
	}
	if body == nil || len(body) != 0 {
		adminError(w, 422, errors.New("停止请求必须是空 JSON 对象"))
		return
	}
	m := s.benchmarks
	m.mu.Lock()
	t := m.findLocked(r.PathValue("id"))
	if t == nil {
		m.mu.Unlock()
		adminError(w, 404, errors.New("测试记录不存在或已过期"))
		return
	}
	code := 200
	if !t.run.ReportAvailable {
		t.run.Status = "stopping"
		t.cancel()
		code = 202
	}
	v := m.snapshotLocked(t, true)
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	writeJSON(w, v)
}

// getTestReport 只发布清理结束后的不可变证据；运行中不返回可能被误认为完整结果的文件。
func (s *Server) getTestReport(w http.ResponseWriter, r *http.Request) {
	m := s.benchmarks
	m.mu.Lock()
	t := m.findLocked(r.PathValue("id"))
	if t == nil {
		m.mu.Unlock()
		adminError(w, 404, errors.New("测试记录不存在或已过期"))
		return
	}
	if !t.run.ReportAvailable {
		m.mu.Unlock()
		adminError(w, 409, errors.New("测试仍在运行或清理，报告尚未完成"))
		return
	}
	// 终态报告发布后完全只读，可在解锁后编码，慢客户端下载不会阻塞停止和采样。
	report := t.report
	m.mu.Unlock()
	w.Header().Set("Content-Disposition", `attachment; filename="rustswitch-test-`+report.Run.ID+`.json"`)
	writeJSON(w, report)
}

// stopAll 在主服务第一次收到退出信号时立即取消实验，避免生产排空等待过程中继续加压。
func (m *benchmarkManager) stopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.active != nil {
		m.active.run.Status = "stopping"
		m.active.cancel()
	}
}

// close 等待拥有的执行器完成清理；每个子进程有独立的有界终止过程。
func (m *benchmarkManager) close() {
	if m == nil {
		return
	}
	m.stopAll()
	m.mu.Lock()
	t := m.active
	m.mu.Unlock()
	if t != nil {
		<-t.done
	}
}

// finish 原子发布终态及报告，清空独占槽后才允许下一任务启动。
func (m *benchmarkManager) finish(t *benchmarkTask, status string, err error, report benchmarkReport, cleanup benchmarkCleanup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t.cancel()
	// 停止可以在清理阶段到达；锁内最后裁决，保证已经接受的停止不会被旧局部状态覆盖。
	if cleanup.Complete && t.run.Status == "stopping" {
		status = "stopped"
		err = errors.New("测试已停止")
	}
	t.run.Status = status
	t.run.Phase = "finished"
	t.run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	t.run.ElapsedSeconds = time.Since(t.created).Seconds()
	t.run.Cleanup = cleanup
	t.run.ReportAvailable = true
	// 清理证据不完整时冻结新建入口，不能把可能残留的实例与下一次压测叠加。
	if !cleanup.Complete {
		m.closed = true
		m.blockedReason = "上一次测试的资源尚未确认完全清理，已冻结新建测试"
	}
	if err != nil {
		t.run.Error = err.Error()
	}
	if failure, ok := report.Evidence["failure"].(*benchmarkFailure); ok {
		t.run.Failure = failure
	}
	report.Run = m.snapshotLocked(t, true)
	t.report = report
	m.history = append([]*benchmarkTask{t}, m.history...)
	if len(m.history) > 20 {
		m.history = m.history[:20]
	}
	if m.active == t {
		m.active = nil
	}
	close(t.done)
}
