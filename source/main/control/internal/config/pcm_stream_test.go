package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPCMStreamDefaultsPreserveDisabledAndOwnValues 防止读取默认值时隐式开监听或修改共享的启动配置。
func TestPCMStreamDefaultsPreserveDisabledAndOwnValues(t *testing.T) {
	c := Config{Limits: Limits{MaxCalls: 10000}}
	if c.PCMStreamOptions() != (PCMStream{}) || c.PCMStream != nil {
		t.Fatal("缺省入口没有保持关闭")
	}
	c.PCMStream = &PCMStream{SocketPath: "/run/rustswitch/pcm.sock"}
	options := c.PCMStreamOptions()
	if options.MaxConnections != 32 || options.MaxStreams != 10000 {
		t.Fatalf("默认预算不正确：%+v", options)
	}
	options.SocketPath, options.MaxConnections, options.MaxStreams = "/changed.sock", 2, 1
	if *c.PCMStream != (PCMStream{SocketPath: "/run/rustswitch/pcm.sock"}) {
		t.Fatal("有效选项与原配置共享了可变状态")
	}
}

// TestPCMStreamValidationBoundaries 验证启动硬预算和路径的字节边界；不需要路径存在，也不访问文件系统。
func TestPCMStreamValidationBoundaries(t *testing.T) {
	original, err := Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, option := range map[string]PCMStream{
		"默认预算":    {SocketPath: "/not-created/pcm.sock"},
		"最低预算":    {SocketPath: "/run/pcm.sock", MaxConnections: 2, MaxStreams: 1},
		"最高预算":    {SocketPath: "/run/pcm.sock", MaxConnections: 64, MaxStreams: original.Limits.MaxCalls},
		"百字节英文路径": {SocketPath: "/" + strings.Repeat("a", 99)},
		"百字节中文路径": {SocketPath: "/" + strings.Repeat("中", 33)},
	} {
		t.Run(name, func(t *testing.T) {
			c := original
			c.PCMStream = &option
			before := option
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			if option != before {
				t.Fatal("验证修改了原始配置")
			}
		})
	}
	for name, option := range map[string]PCMStream{
		"空对象不是关闭":  {},
		"相对路径":     {SocketPath: "pcm.sock"},
		"根目录":      {SocketPath: "/"},
		"父级折叠":     {SocketPath: "/run/../pcm.sock"},
		"重复分隔":     {SocketPath: "/run//pcm.sock"},
		"尾部分隔":     {SocketPath: "/run/pcm.sock/"},
		"NUL":      {SocketPath: "/run/pcm\x00.sock"},
		"换行":       {SocketPath: "/run/pcm\n.sock"},
		"回车":       {SocketPath: "/run/pcm\r.sock"},
		"英文超过百字节":  {SocketPath: "/" + strings.Repeat("a", 100)},
		"中文超过百字节":  {SocketPath: "/" + strings.Repeat("中", 34)},
		"负连接数":     {SocketPath: "/run/pcm.sock", MaxConnections: -2},
		"仅一条连接":    {SocketPath: "/run/pcm.sock", MaxConnections: 1},
		"奇数连接":     {SocketPath: "/run/pcm.sock", MaxConnections: 33},
		"超额连接":     {SocketPath: "/run/pcm.sock", MaxConnections: 66},
		"负句柄数":     {SocketPath: "/run/pcm.sock", MaxStreams: -1},
		"句柄超过通话预算": {SocketPath: "/run/pcm.sock", MaxStreams: original.Limits.MaxCalls + 1},
	} {
		t.Run(name, func(t *testing.T) {
			c := original
			c.PCMStream = &option
			if c.Validate() == nil {
				t.Fatal("接受了非法入口配置")
			}
		})
	}
}

// TestPCMStreamLoadRejectsUnknownAndMalformedOptions 使用真实配置解码器检查未知字段、非整数与开关语义。
func TestPCMStreamLoadRejectsUnknownAndMalformedOptions(t *testing.T) {
	original, err := os.ReadFile("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"省略保持关闭": "", "空值保持关闭": "null",
		"明确启用":   `{"socket_path":"/not-created/pcm.sock"}`,
		"未知字段":   `{"socket_path":"/run/pcm.sock","frame_timeout_ms":1}`,
		"非整数连接数": `{"socket_path":"/run/pcm.sock","max_connections":2.5}`,
		"对象没有路径": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(original, &object); err != nil {
				t.Fatal(err)
			}
			if raw != "" {
				object["pcm_stream"] = json.RawMessage(raw)
			}
			wire, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, wire, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			valid := name == "省略保持关闭" || name == "空值保持关闭" || name == "明确启用"
			if valid != (err == nil) {
				t.Fatalf("配置接受状态错误：%v", err)
			}
			if valid && (c.PCMStream == nil) != (name != "明确启用") {
				t.Fatal("配置解码改变了入口开关")
			}
		})
	}
}
