package media

import "testing"

// TestDTMFSendReplyRejectsFalseCompletion 不能通过伪造计数/跨会话回复让未发数字变为完成。
func TestDTMFSendReplyRejectsFalseCompletion(t *testing.T) {
	request := Request{Op: "dtmf_send_status", Session: 9}
	valid := Reply{Type: "dtmf_send_state", Session: 9, State: "idle", AcceptedDigits: 3, CompletedDigits: 3, SentPackets: 15}
	if err := validateDTMFSendReply(request, valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Reply){
		func(r *Reply) { r.Session++ },
		func(r *Reply) { r.SentPackets = 0 }, func(r *Reply) { r.QueuedDigits = 33 },
		func(r *Reply) { r.CompletedDigits = 4 }, func(r *Reply) { r.FailedDigits = 1 },
		func(r *Reply) { r.State = "sending" }, func(r *Reply) { r.State = "failed" },
	} {
		bad := valid
		change(&bad)
		if validateDTMFSendReply(request, bad) == nil {
			t.Fatalf("invalid state accepted %+v", bad)
		}
	}
}

// TestDTMFSendAcceptanceRequiresWholeBatch 防止媒体只受理一个数字却让32数字请求返回OK。
func TestDTMFSendAcceptanceRequiresWholeBatch(t *testing.T) {
	request := Request{Op: "dtmf_send", Session: 1, Digits: "123"}
	reply := Reply{Type: "dtmf_send_state", Session: 1, State: "sending", AcceptedDigits: 1, QueuedDigits: 1}
	if validateDTMFSendReply(request, reply) == nil {
		t.Fatal("partial acceptance passed")
	}
}

// TestDTMFSendStatsRequiresBoundedObservations 防止省略和超限指标掩盖活动发送器泄漏。
func TestDTMFSendStatsRequiresBoundedObservations(t *testing.T) {
	values := map[string]any{"dtmf_send_packets": float64(0), "dtmf_send_errors": float64(0), "dtmf_send_cancelled_digits": float64(0), "dtmf_send_active": float64(64)}
	if err := validateDTMFSendStats(values); err != nil {
		t.Fatal(err)
	}
	values["dtmf_send_active"] = float64(65)
	if validateDTMFSendStats(values) == nil {
		t.Fatal("excess senders accepted")
	}
	delete(values, "dtmf_send_active")
	if validateDTMFSendStats(values) == nil {
		t.Fatal("missing observation accepted")
	}
}
