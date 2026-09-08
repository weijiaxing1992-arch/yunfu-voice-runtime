package esl

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStandardEventSubscriptionsDoNotRejectOrdinaryClient 验证客户端混合订阅不被无关事件名阻断。
// 只有测试明确Publish的事件才可到达，订阅与取消不会伪造业务生命周期。
func TestStandardEventSubscriptionsDoNotRejectOrdinaryClient(t *testing.T) {
	s := startTestServer(t, nil)
	p := dialPeer(t, s, true)
	p.send(t, "event json CHANNEL_CREATE CHANNEL_PARK CHANNEL_UNPARK PLAYBACK_START PLAYBACK_STOP\n\n")
	if got := p.read(t).Get("Reply-Text"); got != "+OK event listener enabled json" {
		t.Fatal(got)
	}
	s.Publish("CHANNEL_CREATE", map[string]string{"Unique-ID": "actual-call"}, nil)
	frame := p.read(t)
	var event map[string]string
	if json.Unmarshal(frame.Body, &event) != nil || event["Event-Name"] != "CHANNEL_CREATE" || event["Unique-ID"] != "actual-call" {
		t.Fatalf("fabricated subscription event: %s", frame.Body)
	}
	p.send(t, "nixevent PLAYBACK_START PLAYBACK_STOP\n\nevent json UNKNOWN_RUSTSWITCH_EVENT\n\n")
	if p.read(t).Get("Reply-Text") != "+OK events nixed" {
		t.Fatal("unsubscribe failed")
	}
	if !strings.HasPrefix(p.read(t).Get("Reply-Text"), "-ERR") {
		t.Fatal("unknown event accepted")
	}
	// 循环检查全部固定普通名称，确保ALL/nixevent展开基于同一有界目录。
	seen := map[string]bool{}
	for _, name := range supportedEvents {
		if seen[name] || name == "ALL" || name == "CUSTOM" {
			t.Fatal("invalid standard registry")
		}
		seen[name] = true
		p.send(t, "event json "+name+"\n\n")
		if p.read(t).Get("Reply-Text") != "+OK event listener enabled json" {
			t.Fatal(name)
		}
	}
	if len(seen) != 92 {
		t.Fatal("incomplete fixed registry")
	}
}
