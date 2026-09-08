package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"rustswitch/control/internal/config"
	"strings"
	"testing"
)

// newAdminTestServer 只组装管理层，不启动 SIP 和 Rust 进程，便于隔离配置事务失败。
func newAdminTestServer(t *testing.T) (*Server, http.Handler, config.Config) {
	t.Helper()
	c, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Journal.Path = filepath.Join(t.TempDir(), "events.jsonl")
	store, err := openAdminStore(c)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewAdmissionGuard(c.Limits, store.state.Policy)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Config: c, admin: store, guard: guard, adminToken: "test-only-token"}
	mux := http.NewServeMux()
	s.registerAdmin(mux)
	return s, s.protectAdmin(mux), c
}

// adminRequest 模拟同源页面；传空 token 用于验证未授权的跨站写入被拒绝。
func adminRequest(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		wire, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(wire)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:9080"+path, reader)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-RustSwitch-CSRF", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestAdminGuardPersistenceAndConflict 验证在线限额、版本冲突、重启恢复及降低阈值不结束存量。
func TestAdminGuardPersistenceAndConflict(t *testing.T) {
	s, h, base := newAdminTestServer(t)
	s.Stats.Active.Store(70)
	s.Stats.Established.Store(60)
	policy := s.admin.state.Policy
	policy.MaxActiveCalls = 50
	w := adminRequest(t, h, "PUT", "/v1/guard", s.adminToken, map[string]any{"revision": 1, "policy": policy})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.guardSnapshot().Policy.MaxActiveCalls != 50 || s.Stats.Active.Load() != 70 {
		t.Fatal("更新未生效或修改了存量呼叫")
	}
	if s.admin.state.Revision != 2 {
		t.Fatal("版本没有递增")
	}
	w = adminRequest(t, h, "PUT", "/v1/guard", s.adminToken, map[string]any{"revision": 1, "policy": policy})
	if w.Code != 409 {
		t.Fatal("旧页面覆盖了新配置", w.Code)
	}
	reopened, err := openAdminStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.state.Policy.MaxActiveCalls != 50 || reopened.state.Revision != 2 {
		t.Fatal("重启未恢复策略")
	}
	stat, err := os.Stat(s.admin.path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatal("管理草稿未使用私有权限")
	}
}

// TestAdminBaseChangePreservesProtection 防止调整无关启动字段时静默放宽用户已经收紧的策略。
func TestAdminBaseChangePreservesProtection(t *testing.T) {
	s, h, base := newAdminTestServer(t)
	policy := s.admin.state.Policy
	policy.MaxActiveCalls = 50
	policy.MaxEstablishingCalls = 30
	policy.CallsPerSecond = 2
	policy.BurstCalls = 5
	policy.SoftLimitRatio = .7
	policy.RetryAfterSeconds = 9
	w := adminRequest(t, h, "PUT", "/v1/guard", s.adminToken, map[string]any{"revision": 1, "policy": policy})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	base.SIP.MaxCallSeconds++
	reopened, err := openAdminStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.state.Policy != policy || reopened.active.SIP.MaxCallSeconds != base.SIP.MaxCallSeconds {
		t.Fatal("无关启动配置改动取消了已保存的保护", reopened.state.Policy)
	}
	// 容量降低只夹紧超出新的硬边界的字段，不能把 CPS=2 等更严值抬到新默认值。
	base.Limits.MaxCalls = 20
	base.Limits.CallsPerSecond = 10
	base.Limits.BurstCalls = 3
	reopened, err = openAdminStore(base)
	if err != nil {
		t.Fatal(err)
	}
	want := policy
	want.MaxActiveCalls, want.MaxEstablishingCalls, want.BurstCalls = 20, 20, 3
	if reopened.state.Policy != want || reopened.state.Revision != 3 {
		t.Fatal("新容量边界没有安全收紧策略", reopened.state.Policy)
	}
}

// TestAdminSaveFailureDoesNotApply 验证磁盘失败不会发布新策略或增加已提交版本。
func TestAdminSaveFailureDoesNotApply(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	block := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(block, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.admin.path = filepath.Join(block, "cannot-write.json")
	policy := s.admin.state.Policy
	policy.MaxActiveCalls = 50
	w := adminRequest(t, h, "PUT", "/v1/guard", s.adminToken, map[string]any{"revision": 1, "policy": policy})
	if w.Code != 500 || s.admin.state.Revision != 1 || s.guardSnapshot().Policy.MaxActiveCalls != 100 {
		t.Fatal("保存失败却改变了运行策略", w.Code)
	}
}

// TestAdminProtection 验证本机页面可访问，跨站来源、伪造主机与缺少令牌的写入被拒绝。
func TestAdminProtection(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	if w := adminRequest(t, h, "GET", "/", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "RustSwitch") {
		t.Fatal("页面资源不可访问", w.Code)
	}
	if w := adminRequest(t, h, "POST", "/v1/drain", "", map[string]any{}); w.Code != 403 || w.Header().Get("X-RustSwitch-CSRF-Refresh") != "required" || s.draining.Load() {
		t.Fatal("缺少令牌仍能排空")
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:9080/v1/drain", strings.NewReader("{}"))
	r.Header.Set("Origin", "http://other.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-RustSwitch-CSRF", s.adminToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("X-RustSwitch-CSRF-Refresh") != "" {
		t.Fatal("接受了跨站修改")
	}
	r = httptest.NewRequest("GET", "http://attacker.example/v1/config", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("接受了非回环 Host")
	}
	if w = adminRequest(t, h, "POST", "/v1/drain", s.adminToken, map[string]any{}); w.Code != 200 || !s.draining.Load() {
		t.Fatal("排空失败")
	}
	if w = adminRequest(t, h, "POST", "/v1/resume", s.adminToken, map[string]any{}); w.Code != 200 || s.draining.Load() {
		t.Fatal("恢复接入失败")
	}
	s.stopping.Store(true)
	s.draining.Store(true)
	if w = adminRequest(t, h, "POST", "/v1/resume", s.adminToken, map[string]any{}); w.Code != 409 || !s.draining.Load() {
		t.Fatal("退出时被网页恢复")
	}
}

// TestAdminConfigurationWaitsForRestart 验证基础配置只落盘，重启前有效值不改变。
func TestAdminConfigurationWaitsForRestart(t *testing.T) {
	s, h, base := newAdminTestServer(t)
	desired := base
	desired.SIP.MaxCallSeconds = 7200
	w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": desired})
	if w.Code != 200 || s.Config.SIP.MaxCallSeconds != 3600 || !s.admin.restartRequiredLocked() {
		t.Fatal(w.Code, w.Body.String())
	}
	reopened, err := openAdminStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.active.SIP.MaxCallSeconds != 7200 || reopened.restartRequiredLocked() {
		t.Fatal("重启没有采用待生效值")
	}
	desired.Media.Binary = "/tmp/untrusted-program"
	if w = adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 2, "config": desired}); w.Code != 422 {
		t.Fatal("页面修改了可执行程序路径")
	}
}

// TestFSXMLPatchAndExport 验证官方所有模板可解析，属性转义、注释保留以及导出目录安全。
func TestFSXMLPatchAndExport(t *testing.T) {
	base, err := baseFSFiles()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := fsCatalog(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 189 {
		t.Fatal("参考文件目录数量发生变化", len(catalog))
	}
	count := 0
	for _, file := range catalog {
		count += len(file.Parameters)
	}
	if count < 875 {
		t.Fatal("参数目录遗漏参考参数", count)
	}
	files := map[string]string{"directory/test.xml": `<include><!-- 保留原注释 --><param name="password" value="old"/><variable name='caller' value='x'/><X-PRE-PROCESS cmd="set" data="domain=old"/></include>`}
	updated, err := patchFSParameters(files, map[string]string{"directory/test.xml#0": `中文"'&<x>`, "directory/test.xml#1": "new", "directory/test.xml#2": "$${domain}"})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := parseFSFile("directory/test.xml", updated["directory/test.xml"])
	if err != nil {
		t.Fatal(err)
	}
	if fields[0].Value != `中文"'&<x>` || fields[1].Value != "new" || fields[2].Value != "$${domain}" || !strings.Contains(updated["directory/test.xml"], "<!-- 保留原注释 -->") {
		t.Fatal("属性替换破坏内容或注释")
	}
	if _, err = patchFSParameters(files, map[string]string{"bad#0": "x"}); err == nil {
		t.Fatal("不存在的参数被忽略")
	}
	for _, test := range []struct{ name, xml string }{{"../escape.xml", "<x/>"}, {"/absolute.xml", "<x/>"}, {"x.xml", "<!DOCTYPE x><x/>"}, {"x.xml", "<x>"}, {"x.xml", "<x/>stray"}} {
		if _, err = parseFSFile(test.name, test.xml); err == nil {
			t.Fatal("危险或无效 XML 被接受", test)
		}
	}
	w := httptest.NewRecorder()
	if err = exportFSConfig(w, updated, 3); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range archive.File {
		if file.Name == "conf/directory/test.xml" {
			found = true
		}
		if strings.Contains(file.Name, "..") {
			t.Fatal("ZIP 出现目录穿越")
		}
	}
	if !found {
		t.Fatal("导出缺少配置文件")
	}
	t.Logf("已验证 %d 份 XML、%d 个可编辑字段", len(catalog), count)
}

// TestFSConfigSaveAndReload 验证网页参数编辑与高级 XML 共用版本并可在重启后恢复。
func TestFSConfigSaveAndReload(t *testing.T) {
	s, h, base := newAdminTestServer(t)
	w := adminRequest(t, h, "PUT", "/v1/fs-config/file", s.adminToken, map[string]any{"revision": 1, "path": "directory/new.xml", "xml": `<include><user id="3000"><params><param name="password" value="test-only"/></params></user></include>`})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = adminRequest(t, h, "PUT", "/v1/fs-config", s.adminToken, map[string]any{"revision": 2, "overrides": map[string]string{"directory/new.xml#0": "updated-test"}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	reopened, err := openAdminStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reopened.state.FSFiles["directory/new.xml"], "updated-test") {
		t.Fatal("XML 草稿未恢复")
	}
	if s.admin.restartRequiredLocked() {
		t.Fatal("仅导出 XML 被误计为重启配置")
	}
}
