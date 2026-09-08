package server

import (
	"path/filepath"
	"rustswitch/control/internal/config"
	"testing"
)

// TestAdminDeploymentMediaAndXMLPathsReadOnly 配置表单不可改加载根；失败不落盘也不递增版本。
func TestAdminDeploymentMediaAndXMLPathsReadOnly(t *testing.T) {
	s, h, base := newAdminTestServer(t)
	for _, change := range []func(*config.Config){
		func(c *config.Config) { c.Media.PlaybackRoot = filepath.Join(t.TempDir(), "sounds") },
		func(c *config.Config) {
			c.SIP.Dialplan = &config.SIPDialplan{File: filepath.Join(t.TempDir(), "ivr.xml"), Context: "default"}
		},
	} {
		desired := cloneAdminConfig(base)
		change(&desired)
		w := adminRequest(t, h, "PUT", "/v1/config", s.adminToken, map[string]any{"revision": 1, "config": desired})
		if w.Code != 422 || s.admin.state.Revision != 1 {
			t.Fatalf("deployment input accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

// TestAdminDialplanSnapshotOwnsPointer 编辑草稿不应通过别名修改有效路由；相同值重新提交仍可比较。
func TestAdminDialplanSnapshotOwnsPointer(t *testing.T) {
	a := config.Config{SIP: config.SIP{Dialplan: &config.SIPDialplan{File: "/srv/ivr.xml", Context: "default"}}}
	b := cloneAdminConfig(a)
	if !sameDialplan(a.SIP.Dialplan, b.SIP.Dialplan) {
		t.Fatal("equal values differ")
	}
	b.SIP.Dialplan.Context = "changed"
	if a.SIP.Dialplan.Context != "default" || sameDialplan(a.SIP.Dialplan, b.SIP.Dialplan) {
		t.Fatal("snapshot shares live route")
	}
}
