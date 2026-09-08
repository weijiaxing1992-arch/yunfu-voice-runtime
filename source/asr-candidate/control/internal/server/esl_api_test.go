package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestCompatibilityLocalChannelLifecycleOwnsOnlyA 验证本地 IVR 没有虚构的 B 腿，并在挂机后清理唯一通道。
func TestCompatibilityLocalChannelLifecycleOwnsOnlyA(t *testing.T) {
	var emitted []map[string]string
	s := &Server{compatEvents: func(_ string, h map[string]string, _ []byte) { emitted = append(emitted, h) }}
	c := &Call{Local: true, AID: "local-caller", BID: "unused-internal-id"}
	s.compatibilityEvent(c, "call_admitted", "")
	a := c.compatUUIDs[0]
	if len(a) != 36 || c.compatUUIDs[1] != "" || len(emitted) != 1 || len(s.compatChannels) != 1 {
		t.Fatal("local call created a phantom outbound channel")
	}
	if _, exists := emitted[0]["Other-Leg-Unique-ID"]; exists {
		t.Fatal("local event advertised an absent other leg")
	}
	if s.compatibilityChannelCommand("uuid_getvar", a+" sip_call_id") != c.AID {
		t.Fatal("local channel identity was not queryable")
	}
	s.compatibilityEvent(c, "call_answered", "")
	c.Ended = true
	s.compatibilityEvent(c, "call_ended", "normal_hangup")
	if len(emitted) != 3 || len(s.compatChannels) != 0 || s.compatibilityChannelCommand("uuid_exists", a) != "false" {
		t.Fatal("local channel lifecycle leaked or duplicated a channel")
	}
}

// TestCompatibilityChannelIdentityAndVariables 验证两腿 UUID、SIP Call-ID 和变量空间独立，结束后索引消失。
func TestCompatibilityChannelIdentityAndVariables(t *testing.T) {
	var emitted []map[string]string
	s := &Server{compatEvents: func(name string, h map[string]string, body []byte) { emitted = append(emitted, h) }}
	c := &Call{AID: "caller-call-id", BID: "callee-call-id"}
	s.compatibilityEvent(c, "call_admitted", "")
	a, b := c.compatUUIDs[0], c.compatUUIDs[1]
	if a == b || len(a) != 36 || len(b) != 36 || len(emitted) != 2 || emitted[0]["Other-Leg-Unique-ID"] != b {
		t.Fatal("channel identities are not independent")
	}
	for _, step := range [][3]string{{"uuid_exists", a, "true"}, {"uuid_getvar", a + " sip_call_id", c.AID}, {"uuid_getvar", b + " sip_call_id", c.BID}, {"uuid_setvar", a + " test 中文  a ", "+OK\n"}, {"uuid_getvar", a + " test", "中文  a "}, {"uuid_getvar", b + " test", "_undef_"}, {"uuid_setvar", a + " test", "+OK\n"}, {"uuid_getvar", a + " test", "_undef_"}, {"uuid_getvar", "missing test", "-ERR No such channel!\n"}} {
		if got := s.compatibilityChannelCommand(step[0], step[1]); got != step[2] {
			t.Fatalf("%s %s = %q want %q", step[0], step[1], got, step[2])
		}
	}
	s.compatibilityEvent(c, "call_answered", "")
	if c.compatUUIDs != [2]string{a, b} {
		t.Fatal("identity changed at answer")
	}
	c.Ended = true
	s.compatibilityEvent(c, "call_ended", "normal_hangup")
	if len(s.compatChannels) != 0 || s.compatibilityChannelCommand("uuid_exists", a) != "false" {
		t.Fatal("ended channel retained")
	}
	if emitted[len(emitted)-1]["Hangup-Cause"] != "NORMAL_CLEARING" {
		t.Fatal("real end reason missing")
	}
}

// TestCompatibilityInventoryReflectsExecutableDispatch 每个公开目录入口必须真实分派，不返回未知命令；不把目录存在当作语义全兼容。
func TestCompatibilityInventoryReflectsExecutableDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{ctx: ctx, compatRequests: make(chan compatibilityRequest, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-s.compatRequests:
				s.handleCompatibilityRequest(r)
			}
		}
	}()
	defer func() { cancel(); <-done }()
	var inventory struct {
		Count int `json:"row_count"`
		Rows  []struct {
			Name           string `json:"name"`
			Description    string `json:"description"`
			Implementation string `json:"ikey"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(s.CompatibilityAPI(ctx, "show", "api as json")), &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Count != 14 || len(inventory.Rows) != 14 {
		t.Fatal("unexpected inventory count")
	}
	seenDTMF := map[string]bool{}
	for _, row := range inventory.Rows {
		if row.Name == "uuid_send_dtmf" || row.Name == "uuid_send_dtmf_status" {
			seenDTMF[row.Name] = true
		}
		// 自有状态查询必须标明产品扩展，不能在目录中冒充原版同名API。
		if row.Name == "uuid_send_dtmf_status" && !strings.HasPrefix(row.Description, "RustSwitch ") {
			t.Fatal("extension lost product identity")
		}
		arguments := ""
		if row.Name == "show" {
			arguments = "api as json"
		}
		got := s.CompatibilityAPI(ctx, row.Name, arguments)
		if strings.Contains(got, "Command not found!") || row.Implementation != "rustswitch" {
			t.Fatalf("invented API entry %s: %q", row.Name, got)
		}
	}
	if len(seenDTMF) != 2 {
		t.Fatal("DTMF entry or product extension missing")
	}
}

// TestCompatibilityVariableBoundsAndCancelledMutation 防止控制接口耗尽内存或执行已取消的写入。
func TestCompatibilityVariableBoundsAndCancelledMutation(t *testing.T) {
	s := &Server{compatEvents: func(string, map[string]string, []byte) {}}
	c := &Call{}
	s.compatibilityEvent(c, "call_admitted", "")
	a := c.compatUUIDs[0]
	if got := s.compatibilityChannelCommand("uuid_setvar", a+" large "+strings.Repeat("x", 65536)); !strings.HasPrefix(got, "-ERR") {
		t.Fatal("variable budget ignored")
	}
	if got := s.compatibilityChannelCommand("uuid_setvar", a+" uuid overwrite"); !strings.HasPrefix(got, "-ERR") {
		t.Fatal("protected identity overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.handleCompatibilityRequest(compatibilityRequest{ctx: ctx, command: "uuid_setvar", arguments: a + " cancelled yes", reply: make(chan string, 1)})
	if s.compatibilityChannelCommand("uuid_getvar", a+" cancelled") != "_undef_" {
		t.Fatal("cancelled queued mutation executed")
	}
}

// TestCompatibilityAPINeverImpersonatesFreeSWITCH 版本和不支持命令如实返回，不能靠固定成功字符串伪装兼容。
func TestCompatibilityAPINeverImpersonatesFreeSWITCH(t *testing.T) {
	s := &Server{}
	if version := s.CompatibilityAPI(context.Background(), "version", ""); !strings.HasPrefix(version, "RustSwitch Version") {
		t.Fatalf("dishonest product identity %q", version)
	}
	if got := s.CompatibilityAPI(context.Background(), "conference", "x list"); got != "-ERR conference Command not found!\n" {
		t.Fatalf("unsupported feature accepted: %q", got)
	}
}
