package config

import "testing"

// TestLocalExtensionsAreExplicitAndBounded 本地自动应答必须精确配置，不能以正则或通配符意外接通整条线路。
func TestLocalExtensionsAreExplicitAndBounded(t *testing.T) {
	for _, values := range [][]string{nil, {}, {"1000", "+861234"}} {
		if err := validateLocalExtensions(values); err != nil {
			t.Fatal(values, err)
		}
	}
	for _, values := range [][]string{{""}, {"1000", "1000"}, {"^.*$"}, {"*123#"}, {"1000 "}, {"sip:1000@host"}, {"123456789012345678901234567890123"}, make([]string, 257)} {
		if err := validateLocalExtensions(values); err == nil {
			t.Fatal("接受了错误路由", values)
		}
	}
}
