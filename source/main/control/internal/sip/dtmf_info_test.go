package sip

import (
	"fmt"
	"strings"
	"testing"
)

// TestDTMFInfoExplicitSignals 核对全部16键和Sofia常见数字事件编号，并验证毫秒没有误当RTP采样数。
func TestDTMFInfoExplicitSignals(t *testing.T) {
	for index, digit := range "0123456789*#ABCD" {
		for _, wire := range []string{fmt.Sprintf("Signal=%c\r\nDuration=100\r\n", digit), fmt.Sprintf("sIgNaL= %d\ndUrAtIoN= 100", index)} {
			got, ms, err := ParseDTMFInfo("Application/DTMF-Relay; charset=utf-8", []byte(wire))
			if err != nil || got != string(digit) || ms != 100 {
				t.Fatalf("%q: %q %d %v", wire, got, ms, err)
			}
		}
	}
}

// TestDTMFInfoRejectsAmbiguity 非法或重复输入不能产生按键；缺少Duration也不假设已验证的原版默认值。
func TestDTMFInfoRejectsAmbiguity(t *testing.T) {
	for _, body := range []string{"", "Signal=foo\nDuration=100", "Signal=16\nDuration=100", "Signal=-1\nDuration=100", "Signal=01\nDuration=100", "Signal=a\nDuration=100", "Signal=1\nDuration=0", "Signal=1\nDuration=19", "Signal=1\nDuration=8192", "Signal=1\nDuration=9999999999999", "Signal=1\nDuration=1.5", "Signal=1", "Duration=100", "Signal=1\nSignal=2\nDuration=100", "Signal=1\nDuration=100\nDuration=200", "Signal=1\nDuration=100\nExtra=x", "Signal=1\x00\nDuration=100", strings.Repeat("x", 257)} {
		if _, _, err := ParseDTMFInfo("application/dtmf-relay", []byte(body)); err == nil {
			t.Fatalf("错误接受%q", body)
		}
	}
	if _, _, err := ParseDTMFInfo("application/dtmf", []byte("Signal=1\nDuration=100")); err == nil {
		t.Fatal("隐式接受其他INFO格式")
	}
}
