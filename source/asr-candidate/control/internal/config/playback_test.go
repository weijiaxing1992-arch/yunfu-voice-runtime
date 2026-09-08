package config

import "testing"

// TestPlaybackRootShape 只验证配置路径形状，读取权限和目录存在性由媒体启动时验证。
func TestPlaybackRootShape(t *testing.T) {
	base, err := Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"", "/srv/rustswitch/prompts"} {
		c := base
		c.Media.PlaybackRoot = root
		if err := c.Validate(); err != nil {
			t.Fatalf("拒绝有效根 %q: %v", root, err)
		}
	}
	for _, root := range []string{"/", "prompts", "/srv/../etc", "/srv//prompts", "/srv/prompts/", "/srv/voice\nsecret", "/srv/voice\x00"} {
		c := base
		c.Media.PlaybackRoot = root
		if c.Validate() == nil {
			t.Fatalf("接受无效文件根 %q", root)
		}
	}
}
