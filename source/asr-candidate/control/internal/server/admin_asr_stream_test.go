package server

import (
	"os"
	"rustswitch/control/internal/config"
	"testing"
)

// TestAdminASRStreamSnapshotOwnsPointer 防止锁外配置快照修改正在生效或待启动的私有音频入口。
func TestAdminASRStreamSnapshotOwnsPointer(t *testing.T) {
	s, _, _ := newAdminTestServer(t)
	s.admin.active.ASRStream = &config.ASRStream{SocketPath: "/run/active.sock", SampleRate: 8000}
	s.admin.state.Desired.ASRStream = &config.ASRStream{SocketPath: "/run/desired.sock", SampleRate: 16000}
	s.admin.mu.Lock()
	snapshot := s.adminConfigLocked()
	s.admin.mu.Unlock()
	for _, key := range []string{"active", "desired"} {
		value := snapshot[key].(config.Config)
		value.ASRStream.SocketPath = "/changed.sock"
		value.ASRStream.SampleRate = 16000
		value.ASRStream.MaxStreams = 1
	}
	if *s.admin.active.ASRStream != (config.ASRStream{SocketPath: "/run/active.sock", SampleRate: 8000}) || *s.admin.state.Desired.ASRStream != (config.ASRStream{SocketPath: "/run/desired.sock", SampleRate: 16000}) {
		t.Fatal("快照仍与有效配置或草稿共享可变对象")
	}
	if cloneAdminConfig(config.Config{}).ASRStream != nil {
		t.Fatal("克隆把省略入口变成启用对象")
	}
}

// TestAdminASRStreamDeploymentReadOnly 通过实际管理处理链验证禁用、启用、路径和预算改变均失败且不落盘。
func TestAdminASRStreamDeploymentReadOnly(t *testing.T) {
	for name, mutate := range map[string]func(*config.Config){
		"禁用":   func(c *config.Config) { c.ASRStream = nil },
		"路径":   func(c *config.Config) { c.ASRStream.SocketPath = "/run/changed.sock" },
		"采样率":  func(c *config.Config) { c.ASRStream.SampleRate = 16000 },
		"句柄预算": func(c *config.Config) { c.ASRStream.MaxStreams = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			s, h, base := newAdminTestServer(t)
			base.ASRStream = &config.ASRStream{SocketPath: "/run/asr.sock"}
			s.admin.active = cloneAdminConfig(base)
			s.admin.state.Desired = cloneAdminConfig(base)
			desired := cloneAdminConfig(base)
			mutate(&desired)
			w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": desired})
			if w.Code != 422 || s.admin.state.Revision != 1 || !sameASRStream(s.admin.active.ASRStream, base.ASRStream) || !sameASRStream(s.admin.state.Desired.ASRStream, base.ASRStream) {
				t.Fatalf("管理请求改变了部署入口：%d %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(s.admin.path); !os.IsNotExist(err) {
				t.Fatalf("拒绝操作仍创建了持久文件：%v", err)
			}
		})
	}
	t.Run("启用", func(t *testing.T) {
		s, h, base := newAdminTestServer(t)
		base.ASRStream = &config.ASRStream{SocketPath: "/run/asr.sock"}
		w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": base})
		if w.Code != 422 || s.admin.state.Revision != 1 || s.admin.state.Desired.ASRStream != nil {
			t.Fatalf("管理请求擅自启用了入口：%d %s", w.Code, w.Body.String())
		}
		if _, err := os.Stat(s.admin.path); !os.IsNotExist(err) {
			t.Fatalf("启用失败仍创建了持久文件：%v", err)
		}
	})
}

// TestAdminASRStreamUnchangedCanSave 验证按值相同的独立对象不被当成部署变更，并保留零预算的原始表示。
func TestAdminASRStreamUnchangedCanSave(t *testing.T) {
	s, h, base := newAdminTestServer(t)
	base.ASRStream = &config.ASRStream{SocketPath: "/run/asr.sock"}
	s.admin.active = cloneAdminConfig(base)
	s.admin.state.Desired = cloneAdminConfig(base)
	desired := cloneAdminConfig(base)
	w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": desired})
	if w.Code != 200 || s.admin.state.Revision != 2 || !sameASRStream(s.admin.state.Desired.ASRStream, base.ASRStream) {
		t.Fatalf("相同配置保存失败或被改写默认值：%d %s", w.Code, w.Body.String())
	}
}
