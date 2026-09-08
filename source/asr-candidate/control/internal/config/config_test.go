package config

import (
	"testing"
)

// TestExampleAndPortHeadroom 验证示例可加载，并拒绝隔离期端口不足和非回环管理监听。
func TestExampleAndPortHeadroom(t *testing.T) {
	c, e := Load("../../../config/local.json")
	if e != nil {
		t.Fatal(e)
	}
	c.Limits.MaxCalls = 500
	if c.Validate() == nil {
		t.Fatal("accepted capacity with no quarantine headroom")
	}
	c, e = Load("../../../config/local.json")
	if e != nil {
		t.Fatal(e)
	}
	c.Admin.Listen = "0.0.0.0:9080"
	if c.Validate() == nil {
		t.Fatal("exposed unauthenticated admin interface")
	}
}

// TestExcludedMediaBlocks 验证排除块的对齐、唯一性和有效容量，不把有洞范围按全部端口计算。
func TestExcludedMediaBlocks(t *testing.T) {
	original, err := Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, blocks := range [][]int{{19996}, {20001}, {21998}, {20000, 20000}} {
		c := original
		c.Media.ExcludedPortBlocks = blocks
		if c.Validate() == nil {
			t.Fatalf("接受无效排除块：%v", blocks)
		}
	}
	c := original
	c.Media.ExcludedPortBlocks = []int{20000, 20004}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Limits.MaxCalls = 459
	if c.Validate() == nil {
		t.Fatal("有效块扣除后不足隔离余量仍被接受")
	}
}

// TestSIPStreamConfiguration 确认旧配置零值兼容，并拒绝无界连接、无期限流和不安全的传输名称。
func TestSIPStreamConfiguration(t *testing.T) {
	original, err := Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	defaults := original.SIP.StreamOptions()
	if defaults.UpstreamTransport != "udp" || defaults.MaxConnections != 256 {
		t.Fatal("旧配置未保留 UDP 默认行为")
	}
	for name, option := range map[string]SIPStream{
		"unknown_transport": {UpstreamTransport: "wss"}, "connection_budget": {MaxConnections: 4097},
		"frame_timeout": {FrameTimeoutMS: 1}, "idle_timeout": {IdleTimeoutMS: -1}, "write_timeout": {WriteTimeoutMS: 6000},
		"tls_without_key": {TLSListen: "127.0.0.1:5061"}, "listener_collision": {TCPListen: "127.0.0.1:5062", TLSListen: "127.0.0.1:5062", TLSCertFile: "a", TLSKeyFile: "b"},
	} {
		t.Run(name, func(t *testing.T) {
			c := original
			c.SIP.Stream = &option
			if c.Validate() == nil {
				t.Fatal("接受无效可靠传输配置")
			}
		})
	}
	c := original
	c.SIP.Stream = &SIPStream{TCPListen: "127.0.0.1:5062", UpstreamTransport: "tcp"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
