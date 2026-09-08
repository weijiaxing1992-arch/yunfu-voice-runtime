package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestChannelSnapshotTracksLegStateAndLifetime 验证快照属于真实通道，未接听/接听/挂机状态按实际字段推进。
func TestChannelSnapshotTracksLegStateAndLifetime(t *testing.T) {
	s := &Server{}
	c := &Call{AID: "caller", BID: "callee"}
	s.compatibilityEvent(c, "call_admitted", "")
	a, b := c.compatUUIDs[0], c.compatUUIDs[1]
	read := func(uuid string) map[string]string {
		t.Helper()
		var value map[string]string
		if err := json.Unmarshal([]byte(s.compatibilityChannelCommand("uuid_dump", uuid+" JSON")), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := read(a)
	if first["Event-Name"] != "CHANNEL_DATA" || first["Answer-State"] != "ringing" || first["Channel-State"] != "CS_INIT" || first["Other-Leg-Unique-ID"] != b {
		t.Fatal(first)
	}
	s.compatibilityChannelCommand("uuid_setvar", a+" customer 中文 值%20+")
	c.AStatus, c.BAnswered = 200, true
	for side, uuid := range []string{a, b} {
		value := read(uuid)
		if value["Unique-ID"] != uuid || value["variable_uuid"] != uuid || value["Answer-State"] != "answered" || value["Channel-State"] != "CS_EXECUTE" {
			t.Fatal(value)
		}
		if (side == 0 && value["variable_customer"] != "中文 值%20+") || (side == 1 && value["variable_customer"] != "") {
			t.Fatal("snapshot crossed leg variables")
		}
	}
	c.Ended = true
	for _, uuid := range []string{a, b} {
		if got := s.compatibilityChannelCommand("uuid_dump", uuid+" json"); got != "-ERR No such channel!\n" {
			t.Fatal("ended channel leaked", got)
		}
	}
	s.compatibilityEvent(c, "call_ended", "normal_hangup")
	if len(s.compatChannels) != 0 {
		t.Fatal("ended index retained")
	}
}

// TestChannelSnapshotEncodingAndBudgets 对照原版txt保留现有大写%HH，plain原值；超限拒绝不会改通道。
func TestChannelSnapshotEncodingAndBudgets(t *testing.T) {
	s := &Server{}
	c := &Call{Local: true, AID: "caller", AStatus: 200}
	s.compatibilityEvent(c, "call_admitted", "")
	a := c.compatUUIDs[0]
	s.compatibilityChannelCommand("uuid_setvar", a+" customer 中文 %20+&<")
	txt := s.compatibilityChannelCommand("uuid_dump", a)
	plain := s.compatibilityChannelCommand("uuid_dump", a+" plain")
	if !strings.Contains(txt, "variable_customer: %E4%B8%AD%E6%96%87%20%20%2B%26%3C\n") || !strings.Contains(plain, "variable_customer: [中文 %20+&<]\n") {
		t.Fatal("wrong text encoding", txt, plain)
	}
	if strings.Contains(plain, "Other-Leg-Unique-ID") {
		t.Fatal("local snapshot invented B leg")
	}
	for _, args := range []string{a + " json extra", a + strings.Repeat(" ", 130)} {
		if got := s.compatibilityChannelCommand("uuid_dump", args); !strings.HasPrefix(got, "-ERR") {
			t.Fatal("unsupported snapshot accepted", args, got)
		}
	}
	if got := s.compatibilityChannelCommand("uuid_dump", a+" arbitrary"); !strings.Contains(got, "Event-Name: CHANNEL_DATA\n") {
		t.Fatal("unknown format did not fall back to encoded text", got)
	}
	if got := s.compatibilityChannelCommand("uuid_dump", a+" XML"); !strings.Contains(got, "<Event-Name>CHANNEL_DATA</Event-Name>") {
		t.Fatal("XML format was not encoded", got)
	}
	if got := s.compatibilityChannelCommand("uuid_dump", ""); got != "-USAGE: <uuid> [format]\n" {
		t.Fatal(got)
	}
	if got := s.compatibilityChannelCommand("uuid_dump", "absent json"); got != "-ERR No such channel!\n" {
		t.Fatal(got)
	}
	c.compatVariables[0]["large"] = strings.Repeat("x", 80*1024)
	if got := s.compatibilityChannelCommand("uuid_dump", a+" json"); !strings.HasPrefix(got, "-ERR") || c.Ended {
		t.Fatal("budget exhaustion corrupted call", got)
	}
}

// TestChannelSnapshotCancelledQueueDoesNotRun 取消尚未执行的快照，不消耗主循环编码预算或产生事件。
func TestChannelSnapshotCancelledQueueDoesNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	replies := make(chan string, 1)
	s := &Server{}
	s.handleCompatibilityRequest(compatibilityRequest{ctx: ctx, command: "uuid_dump", arguments: "missing json", reply: replies})
	if len(replies) != 0 {
		t.Fatal("cancelled snapshot executed")
	}
}
