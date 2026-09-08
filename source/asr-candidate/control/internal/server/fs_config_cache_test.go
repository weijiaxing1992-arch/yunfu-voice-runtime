package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFSCatalogCacheOwnsResponses 验证调用方修改响应/模板映射不能污染后续目录或官方模板。
func TestFSCatalogCacheOwnsResponses(t *testing.T) {
	var cache fsCatalogCache
	files := map[string]string{"directory/cache.xml": `<include><param name="x" value="old"/></include>`}
	first, err := cache.catalog(files)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Parameters[0].Value = "polluted"
	second, err := cache.catalog(files)
	if err != nil || second[0].Parameters[0].Value != "old" {
		t.Fatalf("响应污染缓存：%v %+v", err, second)
	}
	files["directory/cache.xml"] = `<include><!-- 偏移变化 --><param name="x" value="新值"/></include>`
	third, err := cache.catalog(files)
	if err != nil || third[0].Parameters[0].Value != "新值" {
		t.Fatalf("内容变化未重建：%v %+v", err, third)
	}
	files["directory/cache.xml"] = `<include><param name="x" value="a" value="b"/></include>`
	if _, err = cache.catalog(files); err == nil {
		t.Fatal("缓存掩盖了重复属性")
	}
	templates, err := baseFSFiles()
	if err != nil {
		t.Fatal(err)
	}
	original := templates["vars.xml"]
	templates["vars.xml"] = "polluted"
	again, err := baseFSFiles()
	if err != nil || again["vars.xml"] != original {
		t.Fatal("调用方污染官方模板")
	}
}

// TestFSCatalogCacheConcurrentContentChanges 验证并发目录构建不串用版本，缓存只保留一份目录。
func TestFSCatalogCacheConcurrentContentChanges(t *testing.T) {
	var cache fsCatalogCache
	var workers sync.WaitGroup
	for _, value := range []string{"甲", "乙", "丙", "丁"} {
		workers.Add(1)
		go func(value string) {
			defer workers.Done()
			for range 8 {
				files := map[string]string{"directory/cache.xml": `<include><param name="x" value="` + value + `"/></include>`}
				got, err := cache.catalog(files)
				if err != nil || got[0].Parameters[0].Value != value {
					t.Errorf("并发目录串用：%v %+v", err, got)
					return
				}
				got[0].Parameters[0].Value = "caller mutation"
			}
		}(value)
	}
	workers.Wait()
	for _, name := range []string{"a.xml", "b.xml", "c.xml"} {
		if _, err := cache.catalog(map[string]string{name: `<include/>`}); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 1 {
		t.Fatal("过往文件仍无限留在缓存")
	}
}

// TestFSPatchSelectsOnlyRequestedFiles 验证空修改保持字节、未知ID仍拒绝，并只修改目标文件。
func TestFSPatchSelectsOnlyRequestedFiles(t *testing.T) {
	files := map[string]string{"a.xml": `<include><param name="x" value="a"/></include>`, "b.xml": `<include><param name="y" value="b"/></include>`}
	same, err := patchFSParameters(files, nil)
	if err != nil || same["a.xml"] != files["a.xml"] || same["b.xml"] != files["b.xml"] {
		t.Fatal("空修改改变了内容", err)
	}
	same["a.xml"] = "caller mutation"
	if files["a.xml"] == same["a.xml"] {
		t.Fatal("空修改返回共享映射")
	}
	changed, err := patchFSParameters(files, map[string]string{"a.xml#0": `中文"&<`})
	if err != nil || changed["b.xml"] != files["b.xml"] {
		t.Fatal("无关文件被修改", err)
	}
	parsed, err := parseFSFile("a.xml", changed["a.xml"])
	if err != nil || parsed[0].Value != `中文"&<` {
		t.Fatal("转义破坏参数", err)
	}
	for _, id := range []string{"missing.xml#0", "a.xml#1", "a.xml#-1", "a.xml#00", "a.xml#0#x"} {
		if _, err := patchFSParameters(files, map[string]string{id: "x"}); err == nil {
			t.Fatalf("未知ID被接受：%s", id)
		}
	}
	if _, err := patchFSParameters(files, map[string]string{"a.xml#0": strings.Repeat("x", 16385)}); err == nil {
		t.Fatal("参数限长被绕过")
	}
}

// waitForCatalogBlock 等待处理器真实进入已锁住的缓存阶段，避免用睡眠猜测并发交错。
func waitForCatalogBlock(t *testing.T, handler string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stack := make([]byte, 1<<20)
		stack = stack[:runtime.Stack(stack, true)]
		for _, goroutine := range strings.Split(string(stack), "\n\n") {
			if strings.Contains(goroutine, "(*fsCatalogCache).catalog") && strings.Contains(goroutine, handler) {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("处理器没有进入目录构建阶段")
}

// TestFSCatalogBuildDoesNotHoldAdminLockAndRechecksRevision 用确定交错验证锁外计算与二次版本校验。
func TestFSCatalogBuildDoesNotHoldAdminLockAndRechecksRevision(t *testing.T) {
	for _, kind := range []string{"get", "parameters", "xml"} {
		t.Run(kind, func(t *testing.T) {
			s, h, _ := newAdminTestServer(t)
			original := `<include><param name="x" value="original"/></include>`
			s.admin.state.FSFiles = map[string]string{"directory/cache.xml": original}
			method, path, handler := "GET", "/v1/fs-config", "(*Server).getFSConfig"
			body := map[string]any{}
			if kind == "parameters" {
				method, handler = "PUT", "(*Server).putFSConfig"
				body = map[string]any{"revision": 1, "overrides": map[string]string{"directory/cache.xml#0": "attempt"}}
			} else if kind == "xml" {
				method, path, handler = "PUT", "/v1/fs-config/file", "(*Server).putFSFile"
				body = map[string]any{"revision": 1, "path": "directory/cache.xml", "xml": `<include><param name="x" value="attempt"/></include>`}
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(method, "http://127.0.0.1:9080"+path, bytes.NewReader(raw))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-RustSwitch-CSRF", s.adminToken)
			response, done := httptest.NewRecorder(), make(chan struct{})
			s.admin.catalog.mu.Lock()
			released := false
			defer func() {
				if !released {
					s.admin.catalog.mu.Unlock()
				}
				<-done
			}()
			go func() { defer close(done); h.ServeHTTP(response, request) }()
			waitForCatalogBlock(t, handler)
			if !s.admin.mu.TryLock() {
				t.Fatal("目录计算仍持有管理状态锁")
			}
			policy := s.admin.state.Policy
			s.admin.mu.Unlock()
			// 实际另一个管理写请求在目录被阻塞期间完成，并递增共享版本。
			update := adminRequest(t, h, "PUT", "/v1/guard", s.adminToken, map[string]any{"revision": 1, "policy": policy})
			if update.Code != 200 {
				t.Fatalf("并发策略提交失败：%d %s", update.Code, update.Body)
			}
			s.admin.catalog.mu.Unlock()
			released = true
			<-done
			if kind == "get" {
				var value struct {
					Revision uint64 `json:"revision"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || response.Code != 200 || value.Revision != 1 {
					t.Fatalf("读取丢失原快照版本：%d %s", response.Code, response.Body)
				}
			} else if response.Code != 409 {
				t.Fatalf("过时计算覆盖并发提交：%d %s", response.Code, response.Body)
			}
			if s.admin.state.Revision != 2 || s.admin.state.FSFiles["directory/cache.xml"] != original {
				t.Fatal("失败提交污染了草稿或版本")
			}
			assertFSParameterValue(t, h, s.adminToken, "directory/cache.xml", "original")
		})
	}
}

// assertFSParameterValue 经真实读取处理器核对当前草稿，不能把未提交候选的缓存当成已保存结果。
func assertFSParameterValue(t *testing.T, handler http.Handler, token, path, value string) {
	t.Helper()
	response := adminRequest(t, handler, "GET", "/v1/fs-config", token, nil)
	var got struct {
		Files []fsConfigFile `json:"files"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &got) != nil {
		t.Fatalf("读取当前目录失败：%d %s", response.Code, response.Body)
	}
	for _, file := range got.Files {
		if file.Path == path && len(file.Parameters) == 1 && file.Parameters[0].Value == value {
			return
		}
	}
	t.Fatal("读取目录混入未提交值或丢失字段")
}

// TestFSFailedCommitCannotLeakPreparedCatalog 验证持久化失败后不会通过缓存显示未提交的新值。
func TestFSFailedCommitCannotLeakPreparedCatalog(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	s.admin.state.FSFiles = map[string]string{"directory/cache.xml": `<include><param name="x" value="original"/></include>`}
	// 将目标设为已有目录，真实rename必须失败；只影响本测试的临时存储位置。
	s.admin.path = t.TempDir()
	response := adminRequest(t, h, "PUT", "/v1/fs-config", s.adminToken, map[string]any{"revision": 1, "overrides": map[string]string{"directory/cache.xml#0": "uncommitted"}})
	if response.Code != 500 || s.admin.state.Revision != 1 {
		t.Fatalf("失败提交改变版本：%d %s", response.Code, response.Body)
	}
	assertFSParameterValue(t, h, s.adminToken, "directory/cache.xml", "original")
}
