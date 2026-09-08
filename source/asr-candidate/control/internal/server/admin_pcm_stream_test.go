package server

import (
	"os"
	"rustswitch/control/internal/config"
	"testing"
)

// TestAdminPCMStreamSnapshotOwnsPointer 防止锁外配置快照修改正在生效或待启动的私有音频入口。
func TestAdminPCMStreamSnapshotOwnsPointer(t *testing.T) {
	s, _, _ := newAdminTestServer(t)
	s.admin.active.PCMStream = &config.PCMStream{SocketPath: "/run/active.sock", MaxConnections: 32}
	s.admin.state.Desired.PCMStream = &config.PCMStream{SocketPath: "/run/desired.sock", MaxConnections: 16}
	s.admin.mu.Lock()
	snapshot := s.adminConfigLocked()
	s.admin.mu.Unlock()
	for _, key := range []string{"active", "desired"} {
		value := snapshot[key].(config.Config)
		value.PCMStream.SocketPath = "/changed.sock"
		value.PCMStream.MaxConnections = 64
		value.PCMStream.MaxStreams = 1
	}
	if *s.admin.active.PCMStream != (config.PCMStream{SocketPath: "/run/active.sock", MaxConnections: 32}) || *s.admin.state.Desired.PCMStream != (config.PCMStream{SocketPath: "/run/desired.sock", MaxConnections: 16}) {
		t.Fatal("快照仍与有效配置或草稿共享可变对象")
	}
	if cloneAdminConfig(config.Config{}).PCMStream != nil {
		t.Fatal("克隆把省略入口变成启用对象")
	}
}

// TestAdminPCMStreamDeploymentReadOnly 通过实际管理处理链验证禁用、启用、路径和预算改变均失败且不落盘。
func TestAdminPCMStreamDeploymentReadOnly(t *testing.T) {
	for name, mutate := range map[string]func(*config.Config){
		"禁用":   func(c *config.Config) { c.PCMStream = nil },
		"路径":   func(c *config.Config) { c.PCMStream.SocketPath = "/run/changed.sock" },
		"连接预算": func(c *config.Config) { c.PCMStream.MaxConnections = 2 },
		"句柄预算": func(c *config.Config) { c.PCMStream.MaxStreams = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			s, h, base := newAdminTestServer(t)
			base.PCMStream = &config.PCMStream{SocketPath: "/run/pcm.sock"}
			s.admin.active = cloneAdminConfig(base)
			s.admin.state.Desired = cloneAdminConfig(base)
			desired := cloneAdminConfig(base)
			mutate(&desired)
			w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": desired})
			if w.Code != 422 || s.admin.state.Revision != 1 || !samePCMStream(s.admin.active.PCMStream, base.PCMStream) || !samePCMStream(s.admin.state.Desired.PCMStream, base.PCMStream) {
				t.Fatalf("管理请求改变了部署入口：%d %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(s.admin.path); !os.IsNotExist(err) {
				t.Fatalf("拒绝操作仍创建了持久文件：%v", err)
			}
		})
	}
	t.Run("启用", func(t *testing.T) {
		s, h, base := newAdminTestServer(t)
		base.PCMStream = &config.PCMStream{SocketPath: "/run/pcm.sock"}
		w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": base})
		if w.Code != 422 || s.admin.state.Revision != 1 || s.admin.state.Desired.PCMStream != nil {
			t.Fatalf("管理请求擅自启用了入口：%d %s", w.Code, w.Body.String())
		}
		if _, err := os.Stat(s.admin.path); !os.IsNotExist(err) {
			t.Fatalf("启用失败仍创建了持久文件：%v", err)
		}
	})
}

// TestAdminPCMStreamUnchangedCanSave 验证按值相同的独立对象不被当成部署变更，并保留零预算的原始表示。
func TestAdminPCMStreamUnchangedCanSave(t *testing.T) {
	s, h, base := newAdminTestServer(t)
	base.PCMStream = &config.PCMStream{SocketPath: "/run/pcm.sock"}
	s.admin.active = cloneAdminConfig(base)
	s.admin.state.Desired = cloneAdminConfig(base)
	desired := cloneAdminConfig(base)
	w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": desired})
	if w.Code != 200 || s.admin.state.Revision != 2 || !samePCMStream(s.admin.state.Desired.PCMStream, base.PCMStream) {
		t.Fatalf("相同配置保存失败或被改写默认值：%d %s", w.Code, w.Body.String())
	}
}
