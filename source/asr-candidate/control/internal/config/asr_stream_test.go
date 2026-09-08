package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestASRStreamDefaults 固定部署默认值不修改原对象；省略/空指针严格保持关闭。
func TestASRStreamDefaults(t *testing.T) {
	c := Config{Limits: Limits{MaxCalls: 10000}}
	if c.ASRStreamOptions() != (ASRStream{}) || c.ASRStream != nil {
		t.Fatal("默认配置启用了识别代理")
	}
	c.ASRStream = &ASRStream{SocketPath: "/not-created/asr.sock"}
	o := c.ASRStreamOptions()
	if o.MaxStreams != 64 || o.SampleRate != 8000 {
		t.Fatalf("识别默认格式或额度错误：%+v", o)
	}
	o.SocketPath, o.SampleRate, o.MaxStreams = "/changed.sock", 16000, 1
	if *c.ASRStream != (ASRStream{SocketPath: "/not-created/asr.sock"}) {
		t.Fatal("有效选项改变了配置原值")
	}
	c.Limits.MaxCalls = 8
	if c.ASRStreamOptions().MaxStreams != 8 {
		t.Fatal("默认额度超过本次部署的通话硬预算")
	}
}

// TestASRStreamValidation 验证路径、格式和含起建/未知清理的额度；静态校验不能创建供应商路径。
func TestASRStreamValidation(t *testing.T) {
	base, err := Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]ASRStream{
		"默认":     {SocketPath: "/not-created/asr.sock"},
		"原生最少一流": {SocketPath: "/not-created/asr.sock", SampleRate: 8000, MaxStreams: 1},
		"转换最大额度": {SocketPath: "/not-created/asr.sock", SampleRate: 16000, MaxStreams: base.Limits.MaxCalls},
		"英文百字节":  {SocketPath: "/" + strings.Repeat("a", 99)},
		"中文百字节":  {SocketPath: "/" + strings.Repeat("中", 33)},
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			c.ASRStream = &o
			before := o
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			if o != before {
				t.Fatal("静态验证改变了部署原值")
			}
		})
	}
	for name, o := range map[string]ASRStream{
		"空对象": {}, "相对路径": {SocketPath: "asr.sock"}, "根目录": {SocketPath: "/"},
		"父目录折叠": {SocketPath: "/run/../asr.sock"}, "重复分隔": {SocketPath: "/run//asr.sock"},
		"尾部分隔": {SocketPath: "/run/asr.sock/"}, "NUL": {SocketPath: "/run/asr\x00.sock"},
		"换行": {SocketPath: "/run/asr\n.sock"}, "回车": {SocketPath: "/run/asr\r.sock"},
		"英文超过百字节": {SocketPath: "/" + strings.Repeat("a", 100)}, "中文超过百字节": {SocketPath: "/" + strings.Repeat("中", 34)},
		"不支持采样率": {SocketPath: "/run/asr.sock", SampleRate: 48000},
		"负额度":    {SocketPath: "/run/asr.sock", MaxStreams: -1},
		"超过通话上限": {SocketPath: "/run/asr.sock", MaxStreams: base.Limits.MaxCalls + 1},
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			c.ASRStream = &o
			if c.Validate() == nil {
				t.Fatal("非法识别配置被接受")
			}
		})
	}
	t.Run("供应商不能占用PCM入口", func(t *testing.T) {
		c := base
		c.PCMStream = &PCMStream{SocketPath: "/run/shared.sock"}
		c.ASRStream = &ASRStream{SocketPath: "/run/shared.sock"}
		if c.Validate() == nil {
			t.Fatal("两种协议使用了同一路径")
		}
	})
	t.Run("验证不创建或删除路径", func(t *testing.T) {
		c := base
		path := filepath.Join(t.TempDir(), "provider.sock")
		c.ASRStream = &ASRStream{SocketPath: path}
		// TempDir 的长路径可能超过 Unix 地址预算，用未创建固定路径证明纯静态可接受性。
		if len(path) > 100 {
			path = "/not-created/asr.sock"
			c.ASRStream.SocketPath = path
		}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("验证触碰了外部路径：%v", err)
		}
	})
}

// TestASRStreamLoad 经过实际 JSON 配置解码器，拒绝未知字段、溢出、负/小数采样率，不自动启用。
func TestASRStreamLoad(t *testing.T) {
	raw, err := os.ReadFile("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"省略": "", "空值": "null", "有效": `{"socket_path":"/not-created/asr.sock","sample_rate":16000}`,
		"未知字段":  `{"socket_path":"/not-created/asr.sock","provider_url":"https://example.invalid"}`,
		"非整数额度": `{"socket_path":"/not-created/asr.sock","max_streams":1.5}`,
		"负采样率":  `{"socket_path":"/not-created/asr.sock","sample_rate":-1}`,
		"小数采样率": `{"socket_path":"/not-created/asr.sock","sample_rate":8000.5}`,
		"采样率溢出": `{"socket_path":"/not-created/asr.sock","sample_rate":4294967296}`,
		"数组":    `[]`, "空对象": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			if value != "" {
				object["asr_stream"] = json.RawMessage(value)
			}
			data, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			valid := name == "省略" || name == "空值" || name == "有效"
			if valid != (err == nil) {
				t.Fatalf("接受状态错误：%v", err)
			}
			if valid && (c.ASRStream != nil) != (name == "有效") {
				t.Fatal("解码改变了启用状态")
			}
		})
	}
}
