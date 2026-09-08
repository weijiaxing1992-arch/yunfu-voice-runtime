package config

import "testing"

// TestMediaProcessingModeIsExplicitAndLegacyDefaultPersists 明确接受空/relay/g711，错误模式不能被默认为透传而伪称处理。
func TestMediaProcessingModeIsExplicitAndLegacyDefaultPersists(t *testing.T) {
	original, err := Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "relay", "g711"} {
		cfg := original
		cfg.Media.Processing = mode
		if err := cfg.Validate(); err != nil {
			t.Fatalf("拒绝已声明模式%q：%v", mode, err)
		}
		if cfg.Media.Processing != mode {
			t.Fatal("校验擅自修改配置中的模式")
		}
	}
	for _, mode := range []string{"G711", "g711 ", "opus", "processed", "unknown"} {
		cfg := original
		cfg.Media.Processing = mode
		if err := cfg.Validate(); err == nil {
			t.Fatalf("接受未声明处理模式%q", mode)
		}
	}
}
