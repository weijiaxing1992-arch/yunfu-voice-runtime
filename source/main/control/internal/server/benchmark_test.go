package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"rustswitch/control/internal/config"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// validBenchmarkRequest 返回最小真实场景，边界测试只修改需要验证的字段。
func validBenchmarkRequest(id string) benchmarkRequest {
	return benchmarkRequest{RequestID: id, Mode: "phone", Concurrency: 1, CPS: 1, DurationSeconds: 3, Payload: 0}
}

// decodeBenchmarkRun 检查返回任务结构，避免把错误 JSON 当成空任务继续断言。
func decodeBenchmarkRun(t *testing.T, w *httptest.ResponseRecorder) benchmarkRun {
	t.Helper()
	var run benchmarkRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil || run.ID == "" {
		t.Fatalf("任务响应无效：%s (%v)", w.Body.String(), err)
	}
	return run
}

// completeBenchmarkCleanup 表示伪执行器没有创建系统资源，不用于替代真实进程验收。
func completeBenchmarkCleanup() benchmarkCleanup {
	return benchmarkCleanup{Complete: true, ControllerExited: true, GeneratorExited: true, MediaProcessesExited: true, PortsReleased: true, WorkDirRemoved: true}
}

// awaitBenchmark 只等待内存执行器结束，不监听网络端口；超时明确报告生命周期缺陷。
func awaitBenchmark(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("测试任务未按时清理")
	}
}

// TestBenchmarkValidation 验证容量边界、幂等键及禁止扩展输入，不启动任何发生器。
func TestBenchmarkValidation(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	s.benchmarks.runner = func(context.Context, *benchmarkTask) { t.Error("无效请求启动了执行器") }
	cases := []struct {
		name   string
		mutate func(*benchmarkRequest)
	}{
		{"空幂等键", func(q *benchmarkRequest) { q.RequestID = "" }}, {"过长键", func(q *benchmarkRequest) { q.RequestID = strings.Repeat("a", 129) }},
		{"任意命令", func(q *benchmarkRequest) { q.Mode = "shell" }}, {"手机并发", func(q *benchmarkRequest) { q.Concurrency = 2 }},
		{"零并发", func(q *benchmarkRequest) { q.Concurrency = 0 }}, {"超万路", func(q *benchmarkRequest) { q.Mode = "pressure"; q.Concurrency = 10001 }},
		{"零速率", func(q *benchmarkRequest) { q.CPS = 0 }}, {"超速率", func(q *benchmarkRequest) { q.CPS = 1001 }},
		{"零时长", func(q *benchmarkRequest) { q.DurationSeconds = 0 }}, {"超时长", func(q *benchmarkRequest) { q.DurationSeconds = 301 }},
		{"未知载荷", func(q *benchmarkRequest) { q.Payload = 127 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := validBenchmarkRequest("validation")
			tc.mutate(&q)
			w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, q)
			if w.Code != 422 {
				t.Fatalf("状态=%d %s", w.Code, w.Body.String())
			}
		})
	}
	w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, map[string]any{"request_id": "x", "mode": "phone", "concurrency": 1, "cps": 1, "duration_seconds": 1, "payload": 0, "command": "ignored"})
	if w.Code != 400 {
		t.Fatalf("任意字段未拒绝：%d", w.Code)
	}
	if w = adminRequest(t, h, "POST", "/v1/tests", "", validBenchmarkRequest("csrf")); w.Code != 403 {
		t.Fatalf("缺令牌=%d", w.Code)
	}
	q := validBenchmarkRequest("maximum")
	q.Mode = "pressure"
	q.Concurrency = 10000
	q.CPS = 1000
	q.DurationSeconds = 300
	q.Payload = 8
	if err := q.validate(); err != nil {
		t.Fatal(err)
	}
}

// TestBenchmarkIdempotencyExclusionAndStop 验证并发重试只产生一个执行器，停止直到清理完成才释放槽。
func TestBenchmarkIdempotencyExclusionAndStop(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	m := s.benchmarks
	var executions atomic.Int32
	entered := make(chan *benchmarkTask, 1)
	m.runner = func(ctx context.Context, task *benchmarkTask) {
		executions.Add(1)
		entered <- task
		<-ctx.Done()
		m.finish(task, "stopped", nil, benchmarkReport{}, completeBenchmarkCleanup())
	}
	q := validBenchmarkRequest("same")
	first := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, q)
	if first.Code != 202 {
		t.Fatal(first.Body.String())
	}
	run := decodeBenchmarkRun(t, first)
	task := <-entered
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, q)
			if w.Code != 200 || decodeBenchmarkRun(t, w).ID != run.ID {
				t.Errorf("幂等失败：%d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	changed := q
	changed.Payload = 8
	if w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, changed); w.Code != 409 {
		t.Fatalf("不同参数=%d", w.Code)
	}
	if w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("other")); w.Code != 409 {
		t.Fatalf("排他=%d", w.Code)
	}
	if w := adminRequest(t, h, "GET", "/v1/tests/"+run.ID+"/report", "", nil); w.Code != 409 {
		t.Fatalf("提前报告=%d", w.Code)
	}
	if w := adminRequest(t, h, "POST", "/v1/tests/"+run.ID+"/stop", s.adminToken, map[string]any{"anything": true}); w.Code != 422 {
		t.Fatalf("停止扩展输入=%d", w.Code)
	}
	if w := adminRequest(t, h, "POST", "/v1/tests/"+run.ID+"/stop", s.adminToken, map[string]any{}); w.Code != 202 {
		t.Fatalf("停止=%d", w.Code)
	}
	awaitBenchmark(t, task.done)
	w := adminRequest(t, h, "GET", "/v1/tests/"+run.ID, "", nil)
	finished := decodeBenchmarkRun(t, w)
	if finished.Status != "stopped" || !finished.Cleanup.Complete || !finished.ReportAvailable || executions.Load() != 1 {
		t.Fatalf("终态不符：%+v", finished)
	}
	if w = adminRequest(t, h, "POST", "/v1/tests/"+run.ID+"/stop", s.adminToken, map[string]any{}); w.Code != 200 {
		t.Fatalf("终态重复停止=%d", w.Code)
	}
	var listing struct {
		Current *benchmarkRun  `json:"current"`
		History []benchmarkRun `json:"history"`
	}
	w = adminRequest(t, h, "GET", "/v1/tests", "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || listing.Current != nil || len(listing.History) != 1 {
		t.Fatalf("列表错误 %s", w.Body.String())
	}
}

// TestBenchmarkShutdownAndCleanupFreeze 检查退出立即取消，以及清理未确认时不允许下一轮叠加负载。
func TestBenchmarkShutdownAndCleanupFreeze(t *testing.T) {
	for _, unclean := range []bool{false, true} {
		t.Run(fmt.Sprint(unclean), func(t *testing.T) {
			s, h, _ := newAdminTestServer(t)
			m := s.benchmarks
			entered := make(chan *benchmarkTask, 1)
			m.runner = func(ctx context.Context, task *benchmarkTask) {
				entered <- task
				<-ctx.Done()
				clean := completeBenchmarkCleanup()
				clean.Complete = !unclean
				m.finish(task, "stopped", nil, benchmarkReport{}, clean)
			}
			w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("stop-all"))
			if w.Code != 202 {
				t.Fatal(w.Body.String())
			}
			task := <-entered
			if unclean {
				task.cancel()
				awaitBenchmark(t, task.done)
			} else {
				m.close()
			}
			if w = adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("new")); w.Code != 409 {
				t.Fatalf("清理冻结=%d", w.Code)
			}
		})
	}
}

// TestBenchmarkHistoryBoundAndExpiredKeys 保留的完整报告有界，已淘汰请求不会因重试再次产生流量。
func TestBenchmarkHistoryBoundAndExpiredKeys(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	m := s.benchmarks
	m.runner = func(ctx context.Context, task *benchmarkTask) {
		m.finish(task, "completed", nil, benchmarkReport{}, completeBenchmarkCleanup())
	}
	for i := 0; i < 22; i++ {
		w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest(fmt.Sprintf("history-%d", i)))
		if w.Code != 202 {
			t.Fatalf("新建=%d %s", w.Code, w.Body.String())
		}
		id := decodeBenchmarkRun(t, w).ID
		m.mu.Lock()
		task := m.findLocked(id)
		m.mu.Unlock()
		awaitBenchmark(t, task.done)
	}
	if len(m.history) != 20 || len(m.keys) != 22 {
		t.Fatalf("历史未限制：%d/%d", len(m.history), len(m.keys))
	}
	if w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("history-0")); w.Code != 410 {
		t.Fatalf("过期重试=%d", w.Code)
	}
	for i := len(m.keys); i < 4096; i++ {
		m.keys[fmt.Sprint(i)] = benchmarkKey{}
	}
	if w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("overflow")); w.Code != 429 {
		t.Fatalf("幂等记录未限长：%d", w.Code)
	}
}

// TestBenchmarkChildDisabled 确认子实例保留只读能力说明，但不能递归创建进程。
func TestBenchmarkChildDisabled(t *testing.T) {
	t.Setenv(benchmarkChildEnv, "owned-test")
	s, h, _ := newAdminTestServer(t)
	if w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("nested")); w.Code != 403 {
		t.Fatalf("允许嵌套=%d", w.Code)
	}
	if w := adminRequest(t, h, "GET", "/v1/tests/not-found", "", nil); w.Code != 404 {
		t.Fatalf("未知记录=%d", w.Code)
	}
}

// TestBenchmarkPortIsolation 覆盖 macOS 共享回环地址、Linux 独立地址以及主实例通配绑定。
func TestBenchmarkPortIsolation(t *testing.T) {
	c, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Media.PortStart = 10000
	c.Media.PortEnd = 64999
	c.SIP.Listen = "127.0.0.1:5068"
	c.SIP.Upstream = "127.0.0.1:5070"
	q := validBenchmarkRequest("ports")
	q.Mode = "pressure"
	q.Concurrency = 1000
	q.CPS = 100
	for _, ip := range []string{"127.0.0.1", "127.77.0.2"} {
		p, err := benchmarkPortPlan(ip, []config.Config{c}, q)
		if err != nil {
			t.Fatal(err)
		}
		used := map[int]bool{}
		excluded := benchmarkExcludedSet(p)
		for _, span := range [][2]int{{p.MediaStart, p.MediaEnd}, {p.EndpointStart, p.EndpointEnd}, {p.SIP, p.SIP}, {p.UAS, p.UAS}, {p.Caller, p.Caller}, {p.Admin, p.Admin}} {
			for n := span[0]; n <= span[1]; n++ {
				if n >= p.MediaStart && n <= p.MediaEnd && excluded[p.MediaStart+(n-p.MediaStart)/4*4] {
					continue
				}
				if used[n] || n == 5068 || n == 5070 || n == 5060 {
					t.Fatalf("端口重叠：%s:%d", ip, n)
				}
				used[n] = true
				if ip == "127.0.0.1" && n >= 10000 && n <= 64999 {
					t.Fatalf("占用生产声明：%d", n)
				}
			}
		}
		if p.SocketGroups != 128 {
			t.Fatalf("端点共享组数=%d", p.SocketGroups)
		}
	}
	q.Concurrency = 10000
	if _, err = benchmarkPortPlan("127.0.0.1", []config.Config{c}, q); err == nil {
		t.Fatal("共享回环万路应因端口不足拒绝")
	}
	if _, err = benchmarkPortPlan("127.77.0.2", []config.Config{c}, q); err != nil {
		t.Fatalf("独立地址应具备万路端口容量：%v", err)
	}
	c.Media.BindIP = "0.0.0.0"
	if _, err = benchmarkPortPlan("127.77.0.2", []config.Config{c}, q); err == nil {
		t.Fatal("通配绑定应排除主媒体范围")
	}
	desired := c
	desired.Media.PortStart = 1024
	desired.Media.PortEnd = 65535
	free := benchmarkSafePorts("127.77.0.2", []config.Config{c, desired})
	for _, available := range free {
		if available {
			t.Fatal("待启动通配范围未排除")
		}
	}
}

// TestBenchmarkEvidenceBounds 验证日志环与 JSON 上限，拒绝过大和拼接的伪报告。
func TestBenchmarkEvidenceBounds(t *testing.T) {
	tail := &benchmarkTail{}
	tail.Write(bytes.Repeat([]byte("a"), 70000))
	tail.Write([]byte("END"))
	if len(tail.String()) != 65536 || !strings.HasSuffix(tail.String(), "END") {
		t.Fatal("日志保留边界错误")
	}
	var value any
	for _, wire := range []string{`{"x":1}{"x":2}`, strings.Repeat(" ", 100) + `{}`} {
		if benchmarkReadJSON(strings.NewReader(wire), 64, &value) == nil {
			t.Fatal("接受了无效或过大 JSON")
		}
	}
}

// TestBenchmarkJournalPeak 使用事件生命周期证明并发峰值，不用累计成功数冒充并发。
func TestBenchmarkJournalPeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	wire := `{"run_id":"r","kind":"controller_started"}
{"run_id":"r","kind":"call_established","call_id":"a"}
{"run_id":"r","kind":"call_established","call_id":"b"}
{"run_id":"r","kind":"call_ended","call_id":"a"}
{"run_id":"r","kind":"call_ended","call_id":"b"}
`
	if err := os.WriteFile(path, []byte(wire), 0600); err != nil {
		t.Fatal(err)
	}
	peak, unended, events, err := benchmarkJournalPeak(path)
	if err != nil || peak != 2 || unended != 0 || events != 5 {
		t.Fatalf("事件峰值=%d 未结束=%d 事件=%d 错误=%v", peak, unended, events, err)
	}
	if err = os.WriteFile(path, []byte(wire+`{"run_id":"other","kind":"controller_started"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = benchmarkJournalPeak(path); err == nil {
		t.Fatal("混合启动身份没有拒绝")
	}
}

// benchmarkRoundTrip 用内存响应模拟子实例，测试身份验证无需开监听端口。
type benchmarkRoundTrip func(*http.Request) (*http.Response, error)

// RoundTrip 实现测试用 HTTP 传输接口。
func (f benchmarkRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestBenchmarkIdentityBeforeGuard 检查配置或嵌套标志不符时绝不发送写请求。
func TestBenchmarkIdentityBeforeGuard(t *testing.T) {
	c, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	c.SourcePath = ""
	c.Journal.Path = filepath.Join(t.TempDir(), "identity.jsonl")
	for _, mismatch := range []string{"config", "nested", "none"} {
		t.Run(mismatch, func(t *testing.T) {
			writes := 0
			policy := DefaultGuardPolicy(c.Limits)
			revision := uint64(1)
			h := &benchmarkHTTP{base: "http://127.0.0.1:1027", client: &http.Client{Transport: benchmarkRoundTrip(func(r *http.Request) (*http.Response, error) {
				var value any
				switch r.URL.Path {
				case "/v1/config":
					active := c
					if mismatch == "config" {
						active.Journal.Path = "wrong"
					}
					value = map[string]any{"revision": revision, "csrf_token": "child-token", "active": active, "guard": map[string]any{"policy": policy}}
				case "/v1/tests":
					value = map[string]any{"enabled": mismatch == "nested", "disabled_reason": "独立测试实例禁止嵌套创建测试"}
				case "/v1/guard":
					if r.Method == "PUT" {
						writes++
						if r.Header.Get("X-RustSwitch-CSRF") != "child-token" {
							t.Error("使用了错误令牌")
						}
						policy.Enabled = false
						revision++
					}
					value = map[string]any{"revision": revision, "guard": map[string]any{"policy": policy}}
				default:
					t.Errorf("非预期路径 %s", r.URL.Path)
				}
				wire, _ := json.Marshal(value)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire)), Header: make(http.Header)}, nil
			})}}
			err := h.verifyAndConfigure(context.Background(), &benchmarkPlan{config: c})
			if mismatch == "none" {
				if err != nil || writes != 1 {
					t.Fatalf("正常配置未完成：%v writes=%d", err, writes)
				}
			} else if err == nil || writes != 0 {
				t.Fatalf("身份不符仍修改策略：%v writes=%d", err, writes)
			}
		})
	}
}

// TestBenchmarkBusyPortReplanning 用真实失败端口 1053 验证重新规划，不绑定或占用系统端口。
func TestBenchmarkBusyPortReplanning(t *testing.T) {
	c, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Media.PortStart = 10000
	c.Media.PortEnd = 64999
	c.SIP.Listen = "127.0.0.1:5068"
	c.SIP.Upstream = "127.0.0.1:5070"
	for _, calls := range []int{500, 1000} {
		q := validBenchmarkRequest("busy")
		q.Mode = "pressure"
		q.Concurrency = calls
		q.CPS = 100
		p, err := benchmarkPortPlanExcluding("127.0.0.1", []config.Config{c}, q, []int{1053})
		if err != nil {
			t.Fatal(err)
		}
		if p.MediaStart <= 1053 && p.MediaEnd >= 1053 && !benchmarkExcludedSet(p)[p.MediaStart+(1053-p.MediaStart)/4*4] || (p.MediaEnd-p.MediaStart+1)/4-len(p.ExcludedPortBlocks) < calls+20 {
			t.Fatalf("重新规划越界或缩小容量：%+v", p)
		}
	}
	occupied := &benchmarkPortError{network: "UDP", ip: "127.0.0.1", port: 1053, cause: syscall.EADDRINUSE}
	if !errors.Is(occupied, syscall.EADDRINUSE) {
		t.Fatal("丢失占用错误身份")
	}
}

// TestBenchmarkPreflightFailureDoesNotFreeze 验证失败部署只生成失败报告，未启动进程时允许再次修正重试。
func TestBenchmarkPreflightFailureDoesNotFreeze(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	s.Config.Media.Binary = filepath.Join(t.TempDir(), "missing-media")
	for _, id := range []string{"bad-deployment-1", "bad-deployment-2"} {
		w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest(id))
		if w.Code != 202 {
			t.Fatalf("预检失败冻结入口：%d %s", w.Code, w.Body.String())
		}
		run := decodeBenchmarkRun(t, w)
		m := s.benchmarks
		m.mu.Lock()
		task := m.findLocked(run.ID)
		m.mu.Unlock()
		awaitBenchmark(t, task.done)
		w = adminRequest(t, h, "GET", "/v1/tests/"+run.ID, "", nil)
		run = decodeBenchmarkRun(t, w)
		if run.Status != "failed" || !run.Cleanup.Complete || !run.ReportAvailable || run.Error == "" {
			t.Fatalf("预检证据不完整：%+v", run)
		}
		if run.Failure == nil || run.Failure.Stage != "preflight" || run.Failure.Code != "binary_unavailable" || run.Failure.ProcessesStarted || run.StartedAt != "" || len(run.Samples) != 0 {
			t.Fatalf("未启动的预检失败被误报为媒体运行失败：%+v", run.Failure)
		}
	}
}

// TestBenchmarkHashRejectsFIFOAndCancellation 防止错误部署的命名管道卡住准备阶段和服务退出。
func TestBenchmarkHashRejectsFIFOAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary.fifo")
	if err := syscall.Mkfifo(path, 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := benchmarkHash(context.Background(), path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("接受命名管道")
		}
	case <-time.After(time.Second):
		t.Fatal("哈希被 FIFO 阻塞")
	}
	path = filepath.Join(t.TempDir(), "executable")
	if err := os.WriteFile(path, []byte("not executed"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := benchmarkHash(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("准备取消未生效：%v", err)
	}
}

// TestBenchmarkStopDuringCleanup 验证清理开始后接受的停止不能被旧成功状态覆盖。
func TestBenchmarkStopDuringCleanup(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	m := s.benchmarks
	entered := make(chan *benchmarkTask, 1)
	release := make(chan struct{})
	m.runner = func(ctx context.Context, task *benchmarkTask) {
		entered <- task
		<-release
		m.finish(task, "completed", nil, benchmarkReport{}, completeBenchmarkCleanup())
	}
	w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("stop-cleanup"))
	task := <-entered
	id := decodeBenchmarkRun(t, w).ID
	if w = adminRequest(t, h, "POST", "/v1/tests/"+id+"/stop", s.adminToken, map[string]any{}); w.Code != 202 {
		t.Fatal(w.Code)
	}
	close(release)
	awaitBenchmark(t, task.done)
	if run := decodeBenchmarkRun(t, adminRequest(t, h, "GET", "/v1/tests/"+id, "", nil)); run.Status != "stopped" {
		t.Fatalf("已接受停止被覆盖：%s", run.Status)
	}
}

// benchmarkSlowWriter 模拟只读页面卡在响应正文，验证管理锁没有跨网络写入持有。
type benchmarkSlowWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

// Write 阻塞一次正文发送，由测试释放；本身不持管理器锁。
func (w *benchmarkSlowWriter) Write(data []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(data)
}

// TestBenchmarkSlowReaderDoesNotBlockStop 防止慢客户端下载延后停止、采样与最终资源清理。
func TestBenchmarkSlowReaderDoesNotBlockStop(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	m := s.benchmarks
	entered := make(chan *benchmarkTask, 1)
	m.runner = func(ctx context.Context, task *benchmarkTask) {
		entered <- task
		<-ctx.Done()
		m.finish(task, "stopped", nil, benchmarkReport{}, completeBenchmarkCleanup())
	}
	w := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, validBenchmarkRequest("slow"))
	task := <-entered
	id := decodeBenchmarkRun(t, w).ID
	slow := &benchmarkSlowWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(slow, httptest.NewRequest("GET", "http://127.0.0.1:9080/v1/tests/"+id, nil))
		close(done)
	}()
	<-slow.entered
	stopped := make(chan struct{})
	go func() { m.stopAll(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		close(slow.release)
		t.Fatal("慢读取持有了调度锁")
	}
	close(slow.release)
	<-done
	awaitBenchmark(t, task.done)
}

// TestBenchmarkProcessHelper 是子进程辅助入口；普通运行不执行，也不创建监听端口。
func TestBenchmarkProcessHelper(t *testing.T) {
	switch os.Getenv("RUSTSWITCH_TEST_PROCESS_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestBenchmarkProcessHelper$")
		child.Env = append(os.Environ(), "RUSTSWITCH_TEST_PROCESS_HELPER=holder")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if child.Start() != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "holder":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

// TestBenchmarkInheritedPipeExitBound 验证控制器退出后孙进程仍持日志管道时，Wait 仍在限定时间内结束。
func TestBenchmarkInheritedPipeExitBound(t *testing.T) {
	p, err := benchmarkStart(os.Args[0], []string{"-test.run=^TestBenchmarkProcessHelper$"}, append(os.Environ(), "RUSTSWITCH_TEST_PROCESS_HELPER=parent"), &benchmarkTail{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.terminate()
	if !p.wait(4 * time.Second) {
		t.Fatal("继承 stderr 让直接子进程退出通知无限等待")
	}
	if !errors.Is(p.err, exec.ErrWaitDelay) {
		t.Fatalf("未确认管道超时退出：%v", p.err)
	}
}

// TestBenchmarkFragmentedPortCapacity 复现真实macOS失败中的离散占用，1000/2000保持目标并发而3000/5000明确受限。
func TestBenchmarkFragmentedPortCapacity(t *testing.T) {
	c, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Media.PortStart, c.Media.PortEnd = 10000, 64999
	c.SIP.Listen, c.SIP.Advertise, c.SIP.Upstream = "127.0.0.1:5068", "127.0.0.1:5068", "127.0.0.1:5070"
	busy := []int{1053, 3722, 5353, 7890}
	for _, calls := range []int{1000, 2000, 3000, 5000} {
		q := validBenchmarkRequest("fragmented")
		q.Mode = "pressure"
		q.Concurrency = calls
		q.CPS = 100
		p, err := benchmarkPortPlanExcluding("127.0.0.1", []config.Config{c}, q, busy)
		if calls > 2000 {
			if err == nil {
				t.Fatalf("%d路不应虚报有足够安全端口", calls)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%d路仍被安全范围内碎片阻止：%v", calls, err)
		}
		if p.MediaStart >= 10000 || p.MediaEnd >= 10000 {
			t.Fatalf("侵入主媒体范围：%+v", p)
		}
		excluded := benchmarkExcludedSet(p)
		for _, port := range append(append([]int{}, busy...), 5068, 5070) {
			if port >= p.MediaStart && port <= p.MediaEnd && !excluded[p.MediaStart+(port-p.MediaStart)/4*4] {
				t.Fatalf("端口%d未冻结整块排除", port)
			}
		}
		blocks := (p.MediaEnd - p.MediaStart + 1) / 4
		prefix := make([]int, blocks+1)
		for i := 0; i < blocks; i++ {
			prefix[i+1] = prefix[i]
			if excluded[p.MediaStart+i*4] {
				prefix[i+1]++
			}
		}
		if !benchmarkSpanFits(prefix, blocks, q) {
			t.Fatal("某媒体分片缺少接满与隔离余量")
		}
	}
	q := validBenchmarkRequest("capacity")
	q.Mode = "pressure"
	q.Concurrency = 3000
	q.CPS = 100
	capacity := benchmarkCapacity("127.0.0.1", []config.Config{c}, q, busy, false)
	if capacity.Verified || capacity.MaxConcurrency < 2000 || capacity.MaxConcurrency >= 3000 || capacity.RequiredMediaBlocks != 3020 {
		t.Fatalf("端口预估错误：%+v", capacity)
	}
	if fmt.Sprint(capacity.AvailablePresets) != "[500 1000 2000]" {
		t.Fatalf("可规划档位错误：%v", capacity.AvailablePresets)
	}
	if capacity.AvailableMediaBlocks != 2239 || capacity.ExcludedBlocks != 5 || capacity.VerifiedConcurrency != 0 {
		t.Fatalf("把主范围两侧碎片或发生器端点混入媒体容量：%+v", capacity)
	}
}

// TestBenchmarkReservationsSkipExcludedBlocks 用少量真实UDP/TCP监听核对冻结洞、分阶段交接和清理。
func TestBenchmarkReservationsSkipExcludedBlocks(t *testing.T) {
	// 只在测试高端小范围找可用位置；不接触主服务9080或其10000..64999声明范围。
	var plan *benchmarkPlan
	var occupied *net.UDPConn
	for base := 65000; base+27 <= 65535; base += 32 {
		candidate := &benchmarkPlan{ports: benchmarkPorts{IP: "127.0.0.1", MediaStart: base, MediaEnd: base + 15, EndpointStart: base + 20, EndpointEnd: base + 23, SIP: base + 24, UAS: base + 25, Caller: base + 26, Admin: base + 27, SocketGroups: 1, ExcludedPortBlocks: []int{base + 4}}}
		busy, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: base + 4})
		if e != nil {
			continue
		}
		if e = candidate.reserveMediaPorts(context.Background()); e == nil {
			e = candidate.reservePorts()
		}
		if e == nil {
			plan, occupied = candidate, busy
			break
		}
		candidate.releaseReservations(true)
		busy.Close()
	}
	if plan == nil {
		t.Fatal("无法建立隔离小范围socket回归夹具")
	}
	defer occupied.Close()
	defer plan.releaseReservations(true)
	if len(plan.reservedMedia) != 12 || len(plan.reservedEndpoints) != 4 {
		t.Fatal("保留端口数量未排除完整四端口块")
	}
	tryBind := func(port int) error {
		c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		if c != nil {
			c.Close()
		}
		return e
	}
	if !errors.Is(tryBind(plan.ports.MediaStart), syscall.EADDRINUSE) {
		t.Fatal("媒体端口未被实际保留")
	}
	plan.releaseReservations(false)
	if tryBind(plan.ports.MediaStart) != nil || !errors.Is(tryBind(plan.ports.EndpointStart), syscall.EADDRINUSE) {
		t.Fatal("控制器交接提前释放发生器端点，或未释放媒体")
	}
	plan.releaseReservations(true)
	if err := benchmarkProbePorts(context.Background(), plan.ports); err != nil {
		t.Fatalf("清理误把其他进程的禁用块当成遗留：%v", err)
	}
}
