package server

import (
	"context"
	"strings"
	"testing"
)

// TestSendDTMFGrammar 保持默认值与时长原值，非法批次不能静默过滤后只发送前缀。
func TestSendDTMFGrammar(t *testing.T) {
	for _, input := range []string{"", "u", "u ", "u 1 2", "u 1w", "u ~1", "u 1+2", "u a", "u 1@49", "u 1@1001", "u 1@55x", "u 1@+55", "u " + strings.Repeat("1", 33)} {
		if _, _, _, _, err := parseSendDTMF(input); err == nil {
			t.Fatalf("accepted unsupported input %q", input)
		}
	}
	for input, duration := range map[string]uint32{"u 0123456789*#ABCD": 250, "u #@55": 55, "u 1@1000": 1000} {
		uuid, digits, original, got, err := parseSendDTMF(input)
		if err != nil || uuid != "u" || digits == "" || original == "" || got != duration {
			t.Fatalf("%q got %s %d %v", input, digits, got, err)
		}
	}
}

// TestSendDTMFAdmissionNoSideEffects 用同一调用入口验证未建立/桥接/无通道拒绝，不需要真实外部线路。
func TestSendDTMFAdmissionNoSideEffects(t *testing.T) {
	s := &Server{compatChannels: map[string]compatibilityChannel{}}
	for _, test := range []struct {
		call *Call
		side int
		want string
	}{
		{nil, 0, "Cannot locate session"},
		{&Call{Local: false, Established: true, Allocated: true}, 0, "local A leg"},
		{&Call{Local: true, Established: true, Allocated: true}, 1, "local A leg"},
		{&Call{Local: true}, 0, "not established"},
	} {
		s.compatChannels["u"] = compatibilityChannel{call: test.call, side: test.side}
		r := compatibilityRequest{ctx: context.Background(), command: "uuid_send_dtmf", arguments: "u 1", reply: make(chan string, 1)}
		s.handleSendDTMF(r)
		if got := <-r.reply; !strings.Contains(got, test.want) {
			t.Fatalf("got %q want %q", got, test.want)
		}
	}
}
