package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/media"
	"sync"
	"testing"
	"time"
)

// TestAdminHeavyBudgetKeepsControlResponsive 模拟阻塞的XML/文档响应，验证过量请求立即退避且停止入口仍可执行。
func TestAdminHeavyBudgetKeepsControlResponsive(t *testing.T) {
	b := newAdminLoad()
	entered, release := make(chan struct{}, adminHeavyRequestLimit), make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heavyAdminRequest(r) {
			entered <- struct{}{}
			<-release
		}
		w.WriteHeader(200)
	})
	var group sync.WaitGroup
	for i := 0; i < adminHeavyRequestLimit; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			b.serve(next, httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/docs/export.zip", nil))
		}()
	}
	defer group.Wait()
	defer close(release)
	for i := 0; i < adminHeavyRequestLimit; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("重请求未进入测试阻塞点")
		}
	}
	rejected := httptest.NewRecorder()
	b.serve(next, rejected, httptest.NewRequest("GET", "/v1/fs-config/export", nil))
	if rejected.Code != 503 || rejected.Header().Get("Retry-After") != "1" || b.rejected.Load() != 1 {
		t.Fatal("额度用尽后未立即返回可重试错误", rejected.Code)
	}
	for _, path := range []string{"/healthz", "/v1/status", "/v1/tests/task/stop", "/v1/drain", "/v1/guard"} {
		result := httptest.NewRecorder()
		b.serve(next, result, httptest.NewRequest("POST", path, nil))
		if result.Code != 200 {
			t.Fatal("重请求阻塞了管理控制入口", path)
		}
	}
	if b.active.Load() != adminHeavyRequestLimit {
		t.Fatal("重请求在途数量未受到硬预算限制")
	}
}

// TestAdminBudgetReturnsSlotAfterPanic 防止单个处理器异常泄漏额度并让以后所有管理请求长期503。
func TestAdminBudgetReturnsSlotAfterPanic(t *testing.T) {
	b := newAdminLoad()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("测试处理器未触发预期异常")
			}
		}()
		b.serve(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("fixture") }), httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/docs", nil))
	}()
	if len(b.heavy) != 0 || b.active.Load() != 0 {
		t.Fatal("异常处理器泄漏重请求槽")
	}
}

// TestAdminConnectionBudget 在真实TCP监听上限制接纳量，验证Close归还及等待Accept的可终止性。
func TestAdminConnectionBudget(t *testing.T) {
	raw, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	load := newAdminLoad()
	l := &limitedAdminListener{Listener: raw, slots: make(chan struct{}, 2), closed: make(chan struct{}), load: load}
	defer l.Close()
	var accepted []net.Conn
	defer func() {
		for _, c := range accepted {
			c.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		peer, e := net.DialTimeout("tcp4", raw.Addr().String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer peer.Close()
		conn, e := l.Accept()
		if e != nil {
			t.Fatal(e)
		}
		accepted = append(accepted, conn)
	}
	waiting := make(chan error, 1)
	go func() {
		c, e := l.Accept()
		if c != nil {
			c.Close()
		}
		waiting <- e
	}()
	select {
	case <-waiting:
		t.Fatal("连接额度用尽时仍执行Accept")
	case <-time.After(30 * time.Millisecond):
	}
	if load.connections.Load() != 2 {
		t.Fatal("连接数量观测不正确")
	}
	l.Close()
	select {
	case e := <-waiting:
		if e == nil {
			t.Fatal("关闭后Accept未返回错误")
		}
	case <-time.After(time.Second):
		t.Fatal("关闭监听无法解除额度等待")
	}
	accepted[0].Close()
	accepted[0].Close()
	accepted[1].Close()
	if load.connections.Load() != 0 || len(l.slots) != 0 {
		t.Fatal("重复关闭导致连接额度泄漏或重复归还")
	}
}

// adminSlowConfigWriter 在正文写入处阻塞，验证慢客户端下载不占用配置锁。
type adminSlowConfigWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	status  int
}

func (w *adminSlowConfigWriter) Header() http.Header { return w.header }

// wait 同时拦住响应头和正文；错误响应也不能在管理锁内触发网络写入。
func (w *adminSlowConfigWriter) wait() {
	w.once.Do(func() { close(w.entered) })
	<-w.release
}
func (w *adminSlowConfigWriter) WriteHeader(status int) {
	w.status = status
	w.wait()
}
func (w *adminSlowConfigWriter) Write(p []byte) (int, error) {
	w.wait()
	return len(p), nil
}

// TestAdminConfigSlowResponseDoesNotHoldLock 复现原GET配置响应持锁写网络、阻碍排空与配置操作的问题。
func TestAdminConfigSlowResponseDoesNotHoldLock(t *testing.T) {
	s, _, _ := newAdminTestServer(t)
	w := &adminSlowConfigWriter{header: make(http.Header), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.getAdminConfig(w, httptest.NewRequest("GET", "/v1/config", nil)) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("配置未进入网络写入")
	}
	if !s.admin.mu.TryLock() {
		t.Fatal("慢响应仍持有配置锁")
	}
	s.admin.mu.Unlock()
}

// TestAdminSlowResponsesReleaseLock 覆盖实际配置、XML和控制处理器，包含冲突、校验、持久化失败分支。
func TestAdminSlowResponsesReleaseLock(t *testing.T) {
	cases := []struct {
		name, method, path string
		status             int
		prepare            func(*Server, config.Config) any
	}{
		{"config", "GET", "/v1/config", 200, nil},
		{"guard", "GET", "/v1/guard", 200, nil},
		{"fs_catalog", "GET", "/v1/fs-config", 200, nil},
		{"fs_file", "GET", "/v1/fs-config/file?path=vars.xml", 200, nil},
		{"fs_missing", "GET", "/v1/fs-config/file?path=autoload_configs/missing-test.xml", 404, nil},
		{"drain", "POST", "/v1/drain", 200, nil},
		{"resume", "POST", "/v1/resume", 200, nil},
		{"resume_stopping", "POST", "/v1/resume", 409, func(s *Server, _ config.Config) any {
			s.stopping.Store(true)
			return nil
		}},
		{"save_config", "PUT", "/v1/config", 200, func(_ *Server, c config.Config) any {
			c.SIP.MaxCallSeconds++
			return map[string]any{"revision": 1, "config": c}
		}},
		{"readonly_config", "PUT", "/v1/config", 422, func(_ *Server, c config.Config) any {
			c.Media.Binary += ".forbidden"
			return map[string]any{"revision": 1, "config": c}
		}},
		{"save_guard", "PUT", "/v1/guard", 200, func(s *Server, _ config.Config) any {
			return map[string]any{"revision": 1, "policy": s.admin.state.Policy}
		}},
		{"invalid_guard", "PUT", "/v1/guard", 422, func(s *Server, _ config.Config) any {
			policy := s.admin.state.Policy
			policy.MaxActiveCalls = 0
			return map[string]any{"revision": 1, "policy": policy}
		}},
		{"save_fs", "PUT", "/v1/fs-config", 200, func(*Server, config.Config) any {
			return map[string]any{"revision": 1, "overrides": map[string]string{}}
		}},
		{"invalid_fs", "PUT", "/v1/fs-config", 422, func(*Server, config.Config) any {
			return map[string]any{"revision": 1, "overrides": map[string]string{"missing-test.xml#0": "x"}}
		}},
		{"save_fs_file", "PUT", "/v1/fs-config/file", 200, func(*Server, config.Config) any {
			return map[string]any{"revision": 1, "path": "autoload_configs/test.xml", "xml": "<include/>"}
		}},
		{"save_failure", "PUT", "/v1/guard", 500, func(s *Server, _ config.Config) any {
			// 把状态文件目标指向已有目录，真实原子替换失败且不会发布新版本。
			s.admin.path = filepath.Dir(s.admin.path)
			return map[string]any{"revision": 1, "policy": s.admin.state.Policy}
		}},
	}
	// 四个写接口各自验证旧版本分支，避免只修复成功路径而遗漏锁内409。
	for _, path := range []string{"/v1/config", "/v1/guard", "/v1/fs-config", "/v1/fs-config/file"} {
		path := path
		cases = append(cases, struct {
			name, method, path string
			status             int
			prepare            func(*Server, config.Config) any
		}{"conflict_" + path, "PUT", path, 409, func(s *Server, c config.Config) any {
			switch path {
			case "/v1/config":
				return map[string]any{"revision": 0, "config": c}
			case "/v1/guard":
				return map[string]any{"revision": 0, "policy": s.admin.state.Policy}
			case "/v1/fs-config":
				return map[string]any{"revision": 0, "overrides": map[string]string{}}
			default:
				return map[string]any{"revision": 0, "path": "autoload_configs/test.xml", "xml": "<include/>"}
			}
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, h, c := newAdminTestServer(t)
			var body any = map[string]any{}
			if tc.prepare != nil {
				body = tc.prepare(s, c)
			}
			wire, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:9080"+tc.path, bytes.NewReader(wire))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-RustSwitch-CSRF", s.adminToken)
			w := &adminSlowConfigWriter{header: make(http.Header), entered: make(chan struct{}), release: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(w, r) }()
			defer func() { close(w.release); <-done }()
			select {
			case <-w.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("处理器未进入网络响应")
			}
			if !s.admin.mu.TryLock() {
				t.Fatal("成功或错误响应仍持有管理锁")
			}
			s.admin.mu.Unlock()
			if w.status != tc.status {
				t.Fatalf("状态码 = %d，期望 %d", w.status, tc.status)
			}
		})
	}
}

// adminGatedHTTPWriter 包装真实HTTP连接的写入边界，使完整XML目录下载可确定地保持在途。
type adminGatedHTTPWriter struct {
	http.ResponseWriter
	gate *adminSlowConfigWriter
}

func (w adminGatedHTTPWriter) WriteHeader(status int) {
	w.gate.wait()
	w.ResponseWriter.WriteHeader(status)
}
func (w adminGatedHTTPWriter) Write(p []byte) (int, error) {
	w.gate.wait()
	return w.ResponseWriter.Write(p)
}

// TestAdminSlowFSHTTPKeepsStatusAndDrainResponsive 使用实际FS目录、状态与排空路由，避免替身处理器漏掉共享锁。
func TestAdminSlowFSHTTPKeepsStatusAndDrainResponsive(t *testing.T) {
	s, _, c := newAdminTestServer(t)
	var err error
	s.pool = &media.Pool{}
	s.journal, err = journal.Open(c.Journal.Path, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer s.journal.Close()
	s.started = time.Now()
	// 本回归只验证控制状态不被排空请求删除；真实SIP和媒体流程由端到端测试覆盖。
	s.Stats.Active.Store(2)
	s.Stats.Established.Store(1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.status)
	s.registerAdmin(mux)
	handler := s.protectAdmin(mux)
	gate := &adminSlowConfigWriter{entered: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1/fs-config" {
			w = adminGatedHTTPWriter{ResponseWriter: w, gate: gate}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	var release sync.Once
	defer release.Do(func() { close(gate.release) })
	client := &http.Client{Timeout: 3 * time.Second}
	finished := make(chan error, 1)
	go func() {
		response, err := client.Get(server.URL + "/v1/fs-config")
		if err != nil {
			finished <- err
			return
		}
		defer response.Body.Close()
		var value struct {
			Files []fsConfigFile `json:"files"`
		}
		err = json.NewDecoder(response.Body).Decode(&value)
		if err == nil && (response.StatusCode != 200 || len(value.Files) < 100) {
			err = fmt.Errorf("未返回完整官方XML目录：HTTP %d，文件 %d", response.StatusCode, len(value.Files))
		}
		finished <- err
	}()
	select {
	case <-gate.entered:
	case err := <-finished:
		t.Fatal("XML请求提前结束", err)
	case <-time.After(3 * time.Second):
		t.Fatal("XML请求未进入真实HTTP写入")
	}
	// 使用另一个真实连接，响应仍被拦住时必须可以读取状态并执行排空。
	controlClient := &http.Client{Timeout: time.Second}
	for _, operation := range []struct{ method, path string }{{"GET", "/v1/status"}, {"POST", "/v1/drain"}, {"GET", "/v1/status"}} {
		r, err := http.NewRequest(operation.method, server.URL+operation.path, bytes.NewBufferString("{}"))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-RustSwitch-CSRF", s.adminToken)
		response, err := controlClient.Do(r)
		if err != nil {
			t.Fatalf("慢XML响应阻碍 %s：%v", operation.path, err)
		}
		_, err = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("控制请求失败：%s HTTP %d %v", operation.path, response.StatusCode, err)
		}
	}
	if !s.draining.Load() || s.Stats.Active.Load() != 2 || s.Stats.Established.Load() != 1 {
		t.Fatal("排空未及时生效，或错误改写存量呼叫计数")
	}
	release.Do(func() { close(gate.release) })
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

// TestAdminConfigSnapshotOwnsSlices 验证锁外编码所用数组与运行配置、草稿以及另一快照独立。
func TestAdminConfigSnapshotOwnsSlices(t *testing.T) {
	s, _, _ := newAdminTestServer(t)
	s.admin.active.Media.CPUCores = []int{3}
	s.admin.state.Desired.Media.CPUCores = []int{4}
	s.admin.active.SIP.Stream = &config.SIPStream{MaxConnections: 128}
	s.admin.state.Desired.SIP.Stream = &config.SIPStream{MaxConnections: 256}
	s.admin.mu.Lock()
	snapshot := s.adminConfigLocked()
	s.admin.mu.Unlock()
	for _, key := range []string{"active", "desired"} {
		value := snapshot[key].(config.Config)
		value.SIP.TrustedNetworks[0] = "changed"
		value.Media.AllowedRemoteNetworks[0] = "changed"
		value.Media.CPUCores[0] = 999
		value.SIP.Stream.MaxConnections = 999
	}
	if s.admin.active.SIP.Stream.MaxConnections != 128 || s.admin.state.Desired.SIP.Stream.MaxConnections != 256 {
		t.Fatal("配置快照仍共享可变传输配置")
	}
	if s.admin.active.SIP.TrustedNetworks[0] == "changed" || s.admin.state.Desired.SIP.TrustedNetworks[0] == "changed" || s.admin.active.Media.AllowedRemoteNetworks[0] == "changed" || s.admin.state.Desired.Media.AllowedRemoteNetworks[0] == "changed" || s.admin.active.Media.CPUCores[0] != 3 || s.admin.state.Desired.Media.CPUCores[0] != 4 {
		t.Fatal("配置快照仍共享可变数组")
	}
	for _, cores := range [][]int{nil, {}} {
		value := config.Config{Media: config.Media{CPUCores: cores}}
		if (cloneAdminConfig(value).Media.CPUCores == nil) != (cores == nil) {
			t.Fatal("复制改变了nil与空数组的协议语义")
		}
	}
	// 未提交任何操作，读取与修改快照不应创建持久文件。
	if _, err := os.Stat(s.admin.path); !os.IsNotExist(err) {
		t.Fatal("读取快照意外修改持久状态", err)
	}
}

// TestControllerObservationCacheAndQueues 验证采样缓存、队列实际长度与可选线程指标的语义。
func TestControllerObservationCacheAndQueues(t *testing.T) {
	s := &Server{adminLoad: newAdminLoad(), normal: make(chan datagram, 4), critical: make(chan datagram, 8), results: make(chan mediaResult, 16)}
	s.normal <- datagram{}
	first, second := s.controllerSnapshot(), s.controllerSnapshot()
	if first.SampledAt != second.SampledAt || first.Goroutines == 0 || first.GOMAXPROCS == 0 {
		t.Fatal("资源缓存未生效或缺少有效Go运行时数据")
	}
	if first.NormalQueue != 1 || first.NormalCapacity != 4 || first.ResultsCapacity != 16 {
		t.Fatal("队列容量不是实际运行值")
	}
	if first.CPUCoresUsed != nil {
		t.Fatal("首轮采样不能伪造CPU使用率")
	}
	s.controllerMetrics(io.Discard)
}
