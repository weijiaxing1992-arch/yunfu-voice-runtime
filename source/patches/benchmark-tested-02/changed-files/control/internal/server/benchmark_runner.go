package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/journal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// benchmarkTail 只保存最近 64KiB 日志，避免高负载或启动失败刷屏占满父进程内存。
type benchmarkTail struct {
	mu   sync.Mutex
	data []byte
}

// Write 按 io.Writer 约定确认全部输入，截断仅影响保留证据，不给子进程制造管道阻塞。
func (b *benchmarkTail) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	if len(data) >= 65536 {
		b.data = append(b.data[:0], data[len(data)-65536:]...)
	} else {
		excess := len(b.data) + len(data) - 65536
		if excess > 0 {
			copy(b.data, b.data[excess:])
			b.data = b.data[:len(b.data)-excess]
		}
		b.data = append(b.data, data...)
	}
	return n, nil
}

// String 在锁内复制日志；只由最终报告或测试读取。
func (b *benchmarkTail) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

// benchmarkProcess 每个直接子进程拥有独立进程组；退出通知由唯一 Wait goroutine 关闭。
// Rust 媒体子进程继承控制器组，因此控制器异常退出时仍可清理这组受本任务拥有的进程。
type benchmarkProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	log  *benchmarkTail
}

// benchmarkStart 从固定参数数组创建子进程，不经过 shell，也不使用名称匹配杀进程。
func benchmarkStart(binary string, args []string, env []string, log *benchmarkTail) (*benchmarkProcess, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// 孙进程可能继承 stderr；直接子进程退出后不能无限等待日志复制管道的 EOF。
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &benchmarkProcess{cmd: cmd, done: make(chan struct{}), log: log}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

// exited 使用 Wait 完成通知避免并发读取尚未发布的退出状态。
func (p *benchmarkProcess) exited() bool {
	if p == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// wait 最多等待指定时间，调用者负责决定下一次只对本进程或本进程组发送何种信号。
func (p *benchmarkProcess) wait(d time.Duration) bool {
	if p == nil {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.done:
		return true
	case <-timer.C:
		return false
	}
}

// terminate 先让控制器走正常排空，再触发强制取消，最终只杀创建时独占的进程组。
// 直接子进程已退出时仍检查同组媒体，不能仅依据控制器 PID 消失就宣布全部清理完毕。
func (p *benchmarkProcess) terminate() (bool, bool) {
	if p == nil {
		return true, true
	}
	if !p.exited() {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		if !p.wait(2 * time.Second) {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
			p.wait(2 * time.Second)
		}
	}
	groupGone := func() bool { return syscall.Kill(-p.cmd.Process.Pid, 0) == syscall.ESRCH }
	if !groupGone() {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	p.wait(2 * time.Second)
	for until := time.Now().Add(2 * time.Second); !groupGone() && time.Now().Before(until); {
		time.Sleep(20 * time.Millisecond)
	}
	return p.exited(), groupGone()
}

// benchmarkReadJSON 对文件或 HTTP 证据实施硬大小限制，并拒绝尾部垃圾或多个对象。
func benchmarkReadJSON(reader io.Reader, limit int64, value any) error {
	wire, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(wire)) > limit {
		return errors.New("测试证据超过大小上限")
	}
	return json.Unmarshal(wire, value)
}

// benchmarkFile 只读取执行器生成的私有固定文件名；缺文件保留明确错误，不能生成假结果。
func benchmarkFile(path string, limit int64, value any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return benchmarkReadJSON(f, limit, value)
}

// benchmarkHTTP 禁用环境代理与跳转，所有请求只到计划中固定的本机实例。
type benchmarkHTTP struct {
	client *http.Client
	base   string
}

// newBenchmarkHTTP 限制单次观测等待时间；总生命周期另由任务上下文限制。
func newBenchmarkHTTP(address string) *benchmarkHTTP {
	return &benchmarkHTTP{base: "http://" + address, client: &http.Client{Timeout: 1500 * time.Millisecond, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 2}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("测试实例不能重定向管理请求")
	}}}
}

// request 解码有限 JSON，写操作必须带子实例自己的 CSRF 令牌，绝不使用主服务令牌。
func (h *benchmarkHTTP) request(ctx context.Context, method, path, token string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if e := json.NewEncoder(&buf).Encode(body); e != nil {
			return e
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, h.base+path, &buf)
	if e != nil {
		return e
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-RustSwitch-CSRF", token)
	}
	resp, e := h.client.Do(r)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("子实例 %s %s 返回 HTTP %d", method, path, resp.StatusCode)
	}
	return benchmarkReadJSON(resp.Body, 2<<20, out)
}

// verifyAndConfigure 先核对随机私有日志路径在内的完整启动配置和禁止嵌套标志，再调整子实例策略。
// 固定启动身份避免端口竞争时误把另一实例当成测试对象；配置修改采用 revision 和读回验证。
func (h *benchmarkHTTP) verifyAndConfigure(ctx context.Context, p *benchmarkPlan) error {
	var cfg struct {
		Revision uint64        `json:"revision"`
		CSRF     string        `json:"csrf_token"`
		Active   config.Config `json:"active"`
		Guard    GuardSnapshot `json:"guard"`
	}
	if err := h.request(ctx, "GET", "/v1/config", "", nil, &cfg); err != nil {
		return err
	}
	if !reflect.DeepEqual(cfg.Active, p.config) || cfg.Revision == 0 || cfg.CSRF == "" {
		return errors.New("测试实例启动身份或完整配置不符，拒绝修改保护策略")
	}
	var capabilities struct {
		Enabled        bool   `json:"enabled"`
		DisabledReason string `json:"disabled_reason"`
	}
	if err := h.request(ctx, "GET", "/v1/tests", "", nil, &capabilities); err != nil {
		return err
	}
	if capabilities.Enabled || capabilities.DisabledReason != "独立测试实例禁止嵌套创建测试" {
		return errors.New("测试实例未启用嵌套测试禁令")
	}
	policy := cfg.Guard.Policy
	policy.Enabled = false
	var saved struct {
		Revision uint64        `json:"revision"`
		Guard    GuardSnapshot `json:"guard"`
	}
	if err := h.request(ctx, "PUT", "/v1/guard", cfg.CSRF, map[string]any{"revision": cfg.Revision, "policy": policy}, &saved); err != nil {
		return err
	}
	if saved.Revision != cfg.Revision+1 || saved.Guard.Policy != policy {
		return errors.New("测试容量策略未正确保存")
	}
	var reread struct {
		Revision uint64        `json:"revision"`
		Guard    GuardSnapshot `json:"guard"`
	}
	if err := h.request(ctx, "GET", "/v1/guard", "", nil, &reread); err != nil {
		return err
	}
	if reread.Revision != saved.Revision || reread.Guard.Policy != policy {
		return errors.New("测试容量策略读回不一致")
	}
	return nil
}

// benchmarkStatus 只解码观测所需字段，包含日志健康和媒体进程身份以供最终验收。
type benchmarkStatus struct {
	Controller     *controllerState `json:"controller"` // 不把控制进程的资源误加到Rust媒体进程统计中。
	Active         uint64           `json:"active_calls"`
	Established    uint64           `json:"established_calls"`
	JournalHealthy bool             `json:"journal_healthy"`
	JournalWritten uint64           `json:"journal_written_events"`
	JournalSynced  uint64           `json:"journal_synced_events"`
	JournalLost    uint64           `json:"journal_lost_events"`
	Workers        []struct {
		ID         int            `json:"id"`
		PID        int            `json:"pid"`
		Generation uint64         `json:"generation"`
		Healthy    bool           `json:"healthy"`
		Restarts   uint64         `json:"restarts"`
		Stats      map[string]any `json:"stats"`
		Admission  struct {
			SampledAt time.Time `json:"sampled_at"`
		} `json:"admission"`
	} `json:"workers"`
}

// UnmarshalJSON 拒绝缺失的控制面状态，避免完整HTTP响应中的空对象被当作零通话和零丢失。
func (s *benchmarkStatus) UnmarshalJSON(wire []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		return err
	}
	for _, key := range []string{"active_calls", "established_calls", "journal_healthy", "journal_written_events", "journal_synced_events", "journal_lost_events", "workers"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("测试状态字段 %s 缺失", key)
		}
	}
	type plain benchmarkStatus
	var decoded plain
	if err := json.Unmarshal(wire, &decoded); err != nil {
		return err
	}
	*s = benchmarkStatus(decoded)
	return nil
}

// updateProgress 读取发生器已完整替换的快照，阶段名称映射不改写原始 progress.phase。
func (m *benchmarkManager) updateProgress(t *benchmarkTask, path string) {
	var progress benchmarkProgress
	if benchmarkFile(path, 8192, &progress) != nil {
		return
	}
	if progress.RequestedCalls != t.run.Concurrency || progress.AttemptedCalls < 0 || progress.EstablishedCalls < 0 || progress.SetupFailed < 0 {
		return
	}
	phase := "setup"
	switch progress.Phase {
	case "media":
		phase = "media"
	case "teardown", "finished":
		phase = "teardown"
	case "starting", "dialing":
	default:
		return
	}
	m.mu.Lock()
	t.run.Progress = progress
	t.run.Phase = phase
	m.mu.Unlock()
}

// observe 只追加有效 HTTP 采样，保留最近 300 个样本；全程峰值另存，不因窗口截断而丢失。
func (m *benchmarkManager) observe(ctx context.Context, t *benchmarkTask, h *benchmarkHTTP, evidence map[string]any) (*benchmarkStatus, error) {
	var status benchmarkStatus
	if err := h.request(ctx, "GET", "/v1/status", "", nil, &status); err != nil {
		evidence["status_sampling_errors"] = evidence["status_sampling_errors"].(int) + 1
		evidence["last_status_sampling_error"] = err.Error()
		return nil, err
	}
	sample, err := status.checkedSample(t.created, t.run.benchmarkRequest)
	if err != nil {
		evidence["status_sampling_errors"] = evidence["status_sampling_errors"].(int) + 1
		evidence["last_status_sampling_error"] = err.Error()
		return nil, err
	}
	evidence["peak_sampled_active_calls"] = max(evidence["peak_sampled_active_calls"].(uint64), sample.ActiveCalls)
	evidence["peak_sampled_established_calls"] = max(evidence["peak_sampled_established_calls"].(uint64), sample.EstablishedCalls)
	m.mu.Lock()
	if len(t.run.Samples) == 300 {
		copy(t.run.Samples, t.run.Samples[1:])
		t.run.Samples = t.run.Samples[:299]
	}
	t.run.Samples = append(t.run.Samples, sample)
	m.mu.Unlock()
	return &status, nil
}

// observeFinal 只发布明确阶段内新取得的状态，不把媒体中途的旧快照冒充最终证据。
// 子请求最多等待1.5秒且继承任务总期限；采样失败单列，调用方保留原始验收失败原因。
func (m *benchmarkManager) observeFinal(ctx context.Context, t *benchmarkTask, h *benchmarkHTTP, evidence map[string]any, phase string) (*benchmarkStatus, error) {
	evidence["final_status_phase"] = phase
	evidence["final_status_sampled_at"] = nil
	evidence["final_status_sampling_error"] = nil
	readCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	status, err := m.observe(readCtx, t, h, evidence)
	if err != nil {
		evidence["final_status_sampling_error"] = err.Error()
		return nil, err
	}
	// 这是HTTP响应成功接收时刻；worker内部计数自身仍按原有周期更新。
	evidence["final_status_sampled_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	return status, nil
}

// benchmarkWorkerStatsAfterExit只核验同一批worker的统计覆盖，不把媒体错误或活跃通话改写为成功。
// sampled_at是同机控制器收到worker统计后的发布时间，退出边界使用同一主机的墙上时钟比较。
func benchmarkWorkerStatsAfterExit(status *benchmarkStatus, expectedWorkers int, notBefore time.Time) error {
	if status == nil || expectedWorkers < 1 || len(status.Workers) != expectedWorkers || notBefore.IsZero() {
		return errors.New("最终统计未覆盖全部预期worker")
	}
	ids, pids := map[int]bool{}, map[int]bool{}
	for _, worker := range status.Workers {
		if worker.ID < 0 || worker.ID >= expectedWorkers || ids[worker.ID] || worker.PID <= 0 || pids[worker.PID] || worker.Restarts != 0 || worker.Generation != 1 {
			return errors.New("最终统计worker身份或进程代次不一致")
		}
		ids[worker.ID], pids[worker.PID] = true, true
		if !worker.Admission.SampledAt.After(notBefore) {
			return fmt.Errorf("worker %d统计尚未越过发生器退出时刻", worker.ID)
		}
	}
	return nil
}

// observeFailureFinal补采样只属于失败诊断，不改动任务结论、原始错误或发生器报告。
// 最多5秒且继承任务的更短期限/取消；仅读取隔离实例已有HTTP缓存，不额外请求Rust Stats RPC。
// 网络等待和200ms间隔均不持manager锁，主服务的任务查询与停止请求仍可立即取得状态。
func (m *benchmarkManager) observeFailureFinal(ctx context.Context, t *benchmarkTask, h *benchmarkHTTP, evidence map[string]any, phase string, expectedWorkers int, generatorExitedAt time.Time) *benchmarkStatus {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deadline, _ := readCtx.Deadline()
	evidence["final_status_phase"] = phase
	evidence["final_status_sampling_deadline"] = deadline.UTC().Format(time.RFC3339Nano)
	evidence["final_status_sampling_attempts"] = 0
	evidence["worker_stats_after_generator_exit"] = false
	evidence["final_status_sampled_at"] = nil
	evidence["final_status_sampling_error"] = nil
	evidence["final_status_freshness_error"] = nil
	var latest *benchmarkStatus
	var latestAt any
	var freshnessErr error
	for {
		if err := readCtx.Err(); err != nil {
			// 取消/超时不丢掉此前成功取得的原始统计；它是否新鲜另由明确标志说明。
			evidence["final_status_sampling_outcome"] = "deadline_exceeded"
			if errors.Is(err, context.Canceled) {
				evidence["final_status_sampling_outcome"] = "cancelled"
			}
			detail := err.Error()
			if freshnessErr != nil {
				detail += ": " + freshnessErr.Error()
			}
			evidence["final_status_freshness_error"] = detail
			evidence["final_status_sampled_at"] = latestAt
			return latest
		}
		evidence["final_status_sampling_attempts"] = evidence["final_status_sampling_attempts"].(int) + 1
		status, err := m.observeFinal(readCtx, t, h, evidence, phase)
		if err == nil {
			latest, latestAt = status, evidence["final_status_sampled_at"]
			freshnessErr = benchmarkWorkerStatsAfterExit(status, expectedWorkers, generatorExitedAt)
			if freshnessErr == nil && readCtx.Err() == nil {
				evidence["worker_stats_after_generator_exit"] = true
				evidence["final_status_sampling_outcome"] = "fresh"
				evidence["final_status_freshness_error"] = nil
				return latest
			}
		} else {
			freshnessErr = err
		}
		// 本次HTTP失败时仍保留最后成功响应的时间；最新读取失败另存sampling_error。
		evidence["final_status_sampled_at"] = latestAt
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-readCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// benchmarkJournalPeak 逐行读取有界事件日志，校验单一启动身份并计算真实建立事件的并发峰值。
func benchmarkJournalPeak(path string) (int, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, 0, 0, err
	}
	if st.Size() > 64<<20 {
		return 0, 0, 0, errors.New("子实例事件日志超过 64MiB 上限")
	}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 65536)
	active := map[string]bool{}
	peak, count := 0, 0
	runID := ""
	for scan.Scan() {
		var e journal.Event
		if err = json.Unmarshal(scan.Bytes(), &e); err != nil {
			return peak, len(active), count, err
		}
		count++
		if e.Kind == "controller_started" {
			if runID != "" {
				return peak, len(active), count, errors.New("测试日志存在多个启动周期")
			}
			runID = e.RunID
		}
		if runID == "" || e.RunID != runID {
			return peak, len(active), count, errors.New("测试日志启动身份不一致")
		}
		switch e.Kind {
		case "call_established":
			active[e.CallID] = true
			peak = max(peak, len(active))
		case "call_ended":
			delete(active, e.CallID)
		}
	}
	return peak, len(active), count, scan.Err()
}

// runReal 拥有一次测试的全部进程和文件，任何返回路径都先清理再发布报告。
func (m *benchmarkManager) runReal(parent context.Context, t *benchmarkTask) {
	q := t.run.benchmarkRequest.normalized()
	// 建立阶段最长约并发/CPS 秒，不能仅用媒体时长作为整个任务超时。
	deadlineSeconds := (q.Concurrency+q.CPS-1)/q.CPS + q.DurationSeconds + 90
	ctx, cancel := context.WithTimeout(parent, time.Duration(deadlineSeconds)*time.Second)
	defer cancel()
	report := benchmarkReport{Logs: map[string]string{}, Evidence: map[string]any{"scope": "isolated_capacity_test", "capacity_mode": true, "production_capacity_certified": false, "peak_sampled_active_calls": uint64(0), "peak_sampled_established_calls": uint64(0), "status_sampling_errors": 0, "deadline_seconds": deadlineSeconds}}
	report.Evidence["media_processing"] = q.MediaProcessing
	report.Evidence["validation_contract"] = "testlab-media-v2"
	controllerLog, generatorLog := &benchmarkTail{}, &benchmarkTail{}
	var controller, generator *benchmarkProcess
	var plan *benchmarkPlan
	var finalStatus *benchmarkStatus
	status := "failed"
	failureStage := "preflight"
	var runErr error
	// 发生器退出或报告已经明确失败后，补采样的取消/到期不能改写该原始失败。
	preserveGeneratorFailure := false
	defer func() {
		// 即使执行器自身出现意外，仍按同一拥有关系回收，不能遗留正在发生媒体的后台任务。
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("测试执行器异常：%v", recovered)
			status = "failed"
		}
		if parent.Err() != nil && !preserveGeneratorFailure {
			status = "stopped"
			runErr = errors.New("测试已停止")
		} else if ctx.Err() != nil && !preserveGeneratorFailure {
			status = "failed"
			runErr = fmt.Errorf("测试超过 %d 秒总期限", deadlineSeconds)
		}
		m.mu.Lock()
		t.run.Phase = "teardown"
		m.mu.Unlock()
		gExited, gGroupGone := generator.terminate()
		cExited, cGroupGone := controller.terminate()
		cleanup := benchmarkCleanup{GeneratorExited: gExited && gGroupGone, ControllerExited: cExited, MediaProcessesExited: cGroupGone, PortsReleased: true, WorkDirRemoved: true}
		if plan != nil {
			plan.releaseReservations(true)
			report.Evidence["resource_preflight"] = plan.evidence
			report.Evidence["binary_sha256"] = plan.hashes
			if plan.directory != "" {
				var raw json.RawMessage
				if err := benchmarkFile(filepath.Join(plan.directory, "report.json"), 2<<20, &raw); err == nil {
					report.Result = raw
				}
				peak, unended, events, err := benchmarkJournalPeak(plan.config.Journal.Path)
				report.Evidence["journal_peak_established_calls"] = peak
				report.Evidence["journal_unended_calls"] = unended
				report.Evidence["journal_events"] = events
				if err != nil {
					report.Evidence["journal_error"] = err.Error()
					if status == "completed" {
						status = "failed"
						runErr = err
					}
				}
				if status == "completed" && (peak < q.Concurrency || unended != 0) {
					status = "failed"
					runErr = errors.New("事件日志未证明达到目标并发并完整结束")
				}
				if err := os.RemoveAll(plan.directory); err != nil {
					cleanup.WorkDirRemoved = false
					cleanup.Detail = err.Error()
				}
			}
			// 预检冲突来自其他进程时，本任务只需关闭自己的 reservations；不能把他人的占用误判为遗留。
			if controller != nil || generator != nil {
				probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := benchmarkProbePorts(probeCtx, plan.ports)
				probeCancel()
				if err != nil {
					cleanup.PortsReleased = false
					cleanup.Detail = err.Error()
				}
			}
		}
		cleanup.Complete = cleanup.GeneratorExited && cleanup.ControllerExited && cleanup.MediaProcessesExited && cleanup.PortsReleased && cleanup.WorkDirRemoved
		if !cleanup.Complete {
			status = "failed"
			runErr = fmt.Errorf("测试资源未完全确认清理：%s", cleanup.Detail)
		}
		report.Logs["controller_tail"] = controllerLog.String()
		report.Logs["generator_tail"] = generatorLog.String()
		report.Evidence["final_status"] = finalStatus
		if runErr != nil {
			failure := &benchmarkFailure{Stage: failureStage, Code: "execution_failed", Message: runErr.Error(), ProcessesStarted: controller != nil || generator != nil}
			var preflight *benchmarkPreflightError
			if errors.As(runErr, &preflight) {
				failure.Code = preflight.code
			} else if !failure.ProcessesStarted {
				failure.Code = "preflight_failed"
			}
			if status == "stopped" {
				failure.Code = "stopped"
			}
			if !cleanup.Complete {
				failure.Stage = "teardown"
				failure.Code = "cleanup_incomplete"
			}
			if plan != nil {
				if capacity, ok := plan.evidence["port_capacity"].(benchmarkPortCapacity); ok {
					failure.Capacity = &capacity
				}
			}
			report.Evidence["failure"] = failure
		}
		m.finish(t, status, runErr, report, cleanup)
	}()
	plan, runErr = m.prepare(ctx, q)
	if runErr != nil {
		return
	}
	if runErr = ctx.Err(); runErr != nil {
		return
	}
	failureStage = "startup"
	wire, err := json.MarshalIndent(plan.config, "", "  ")
	if err != nil {
		runErr = err
		return
	}
	configPath := filepath.Join(plan.directory, "config.json")
	if runErr = os.WriteFile(configPath, wire, 0600); runErr != nil {
		return
	}
	plan.releaseReservations(false)
	controller, runErr = benchmarkStart(plan.controller, []string{"--config", configPath}, benchmarkEnvironment(plan.identity), controllerLog)
	if runErr != nil {
		return
	}
	h := newBenchmarkHTTP(plan.config.Admin.Listen)
	defer h.client.CloseIdleConnections()
	readyCtx, readyCancel := context.WithTimeout(ctx, 30*time.Second)
	defer readyCancel()
	for {
		if controller.exited() {
			runErr = fmt.Errorf("测试控制器启动失败：%v", controller.err)
			return
		}
		var raw map[string]any
		if h.request(readyCtx, "GET", "/healthz", "", nil, &raw) == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			runErr = fmt.Errorf("等待测试控制器启动失败：%w", readyCtx.Err())
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if runErr = h.verifyAndConfigure(ctx, plan); runErr != nil {
		return
	}
	report.Evidence["startup_identity_verified"] = true
	report.Evidence["child_business_guard_disabled"] = true
	plan.releaseReservations(true)
	args := []string{"--server", plan.config.SIP.Listen, "--uas-listen", plan.config.SIP.Upstream, "--bind-ip", plan.ports.IP, "--advertise-ip", plan.ports.IP, "--media-network", plan.ports.IP + "/32", "--calls", strconv.Itoa(q.Concurrency), "--cps", strconv.Itoa(q.CPS), "--seconds", strconv.Itoa(q.DurationSeconds), "--payload", strconv.Itoa(q.Payload), "--senders", strconv.Itoa(min(8, max(1, runtime.NumCPU()))), "--socket-groups", strconv.Itoa(plan.ports.SocketGroups), "--media-port-start", strconv.Itoa(plan.ports.EndpointStart), "--media-port-end", strconv.Itoa(plan.ports.EndpointEnd), "--caller-port", strconv.Itoa(plan.ports.Caller), "--output", filepath.Join(plan.directory, "report.json"), "--progress", filepath.Join(plan.directory, "progress.json")}
	args = append(args, "--media-processing", q.MediaProcessing)
	if runErr = ctx.Err(); runErr != nil {
		return
	}
	generator, runErr = benchmarkStart(plan.generator, args, benchmarkEnvironment(plan.identity), generatorLog)
	if runErr != nil {
		return
	}
	m.mu.Lock()
	if t.run.Status != "stopping" {
		t.run.Status = "running"
	}
	t.run.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	t.run.Phase = "setup"
	m.mu.Unlock()
	failureStage = "setup"
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	running := true
	for running {
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
			return
		case <-controller.done:
			runErr = fmt.Errorf("测试控制器提前退出：%v", controller.err)
			return
		case <-generator.done:
			running = false
		case <-ticker.C:
			m.updateProgress(t, filepath.Join(plan.directory, "progress.json"))
			m.mu.Lock()
			if t.run.Progress.Phase == "media" {
				failureStage = "media"
			} else if t.run.Progress.Phase == "teardown" {
				failureStage = "teardown"
			}
			m.mu.Unlock()
			// 普通媒体采样只进入时间序列；最终状态须在退出/收敛阶段重新读取。
			_, _ = m.observe(ctx, t, h, report.Evidence)
			if st, e := os.Stat(plan.config.Journal.Path); e == nil && st.Size() > 64<<20 {
				runErr = errors.New("测试事件日志超过 64MiB，已中止")
				return
			}
		}
	}
	// HTTP响应新鲜不代表worker缓存已经刷新；最终每个worker都必须在发生器退出之后发布统计。
	generatorExitedAt := time.Now()
	report.Evidence["generator_exited_at"] = generatorExitedAt.UTC().Format(time.RFC3339Nano)
	m.updateProgress(t, filepath.Join(plan.directory, "progress.json"))
	if generator.err != nil {
		runErr = fmt.Errorf("发生器验收未通过：%v", generator.err)
		preserveGeneratorFailure = true
		// 此时只停止了发生器，控制器和Rust分片尚由本执行器拥有并存活。
		finalStatus = m.observeFailureFinal(ctx, t, h, report.Evidence, "generator_exit_failure", plan.config.Media.Workers, generatorExitedAt)
		return
	}
	var result map[string]any
	if runErr = benchmarkFile(filepath.Join(plan.directory, "report.json"), 2<<20, &result); runErr != nil {
		preserveGeneratorFailure = true
		finalStatus = m.observeFailureFinal(ctx, t, h, report.Evidence, "generator_report_failure", plan.config.Media.Workers, generatorExitedAt)
		return
	}
	resultSummary, err := benchmarkValidateResult(q, result)
	if err != nil {
		runErr = err
		preserveGeneratorFailure = true
		finalStatus = m.observeFailureFinal(ctx, t, h, report.Evidence, "generator_report_failure", plan.config.Media.Workers, generatorExitedAt)
		return
	}
	report.Evidence["generator_contract_verified"] = true
	// 拆线完成与媒体分片下一次统计发布之间存在间隔，最多等待五秒，保留实际收敛快照。
	failureStage = "teardown"
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		finalStatus, err = m.observeFinal(ctx, t, h, report.Evidence, "teardown_convergence")
		if err == nil {
			sample, sampleErr := finalStatus.checkedSample(t.created, q)
			processingErr := error(nil)
			if sampleErr == nil {
				processingErr = benchmarkValidateFinal(q, finalStatus, sample, plan.config.Media.Workers, generatorExitedAt, resultSummary)
				if processingErr != nil {
					report.Evidence["processing_validation_error"] = processingErr.Error()
				}
			}
			if sampleErr == nil && processingErr == nil {
				report.Evidence["worker_stats_after_generator_exit"] = true
				report.Evidence["processing_validation_error"] = nil
				report.Evidence["processing_verified"] = q.MediaProcessing == "g711"
				status = "completed"
				return
			}
		}
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	runErr = errors.New("拆线后资源、媒体健康或事件落盘尚未收敛，不能确认通过")
}
