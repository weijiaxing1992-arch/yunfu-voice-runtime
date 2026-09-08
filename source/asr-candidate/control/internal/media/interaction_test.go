package media

import (
	"encoding/json"
	"testing"
)

// TestDTMFCursorContract 拒绝假连续游标和错误事件；合法日志覆盖仍交给业务报告输入丢失。
func TestDTMFCursorContract(t *testing.T) {
	request := Request{Op: "dtmf_events", AfterSeq: 10, Limit: 64}
	event := DTMFEvent{Sequence: 11, Session: 7, Leg: "a", Kind: "digit", Digit: "5", Event: 5, DurationTicks: 4800, ClockRate: 48000, SSRC: 42, Timestamp: 100}
	valid := func() Reply {
		return Reply{Type: "dtmf_events", OK: true, Events: []DTMFEvent{event}, NextSeq: 11, OldestSeq: 1}
	}
	worker := Worker{}
	if err := worker.validateReply(request, valid()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Reply){
		"跳过未交付事件": func(r *Reply) { r.NextSeq = 12 },
		"重复序号":    func(r *Reply) { r.Events[0].Sequence = 10 },
		"隐瞒覆盖":    func(r *Reply) { r.OldestSeq = 12 },
		"乱报覆盖":    func(r *Reply) { r.Overflow = true },
		"错误时钟":    func(r *Reply) { r.Events[0].ClockRate = 16001 },
		"错误腿":     func(r *Reply) { r.Events[0].Leg = "both" },
		"数字不匹配":   func(r *Reply) { r.Events[0].Digit = "6" },
		"未完成伪造数字": func(r *Reply) { r.Events[0].Kind = "incomplete"; r.Events[0].Reason = "end_packet_not_received" },
	} {
		t.Run(name, func(t *testing.T) {
			reply := valid()
			mutate(&reply)
			if worker.validateReply(request, reply) == nil {
				t.Fatal("错误回复未被拒绝")
			}
		})
	}
	covered := valid()
	covered.OldestSeq = 20
	covered.NextSeq = 20
	covered.Events[0].Sequence = 20
	covered.Overflow = true
	if err := worker.validateReply(request, covered); err != nil {
		t.Fatal(err)
	}
	incomplete := valid()
	incomplete.Events[0].Kind = "incomplete"
	incomplete.Events[0].Digit = ""
	incomplete.Events[0].Reason = "end_packet_not_received"
	if err := worker.validateReply(request, incomplete); err != nil {
		t.Fatal(err)
	}
}

// TestInteractionWireFields 保持内部 JSON 字段稳定，并核对播放任务的响应所有权。
func TestInteractionWireFields(t *testing.T) {
	request := Request{Op: "playback_start", Session: 7, PlaybackID: 9, Leg: "a", FrequencyHz: 1000, DurationMS: 200}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"playback_id": float64(9), "leg": "a", "frequency_hz": float64(1000), "duration_ms": float64(200)} {
		if fields[key] != value {
			t.Fatalf("字段 %s: %v", key, fields[key])
		}
	}
	reply := Reply{OK: true, Type: "playback_state", Session: 7, PlaybackID: 9, State: "completed", SentPackets: 10, TotalPackets: 10}
	worker := Worker{}
	if err := worker.validateReply(request, reply); err != nil {
		t.Fatal(err)
	}
	reply.PlaybackID = 8
	if worker.validateReply(request, reply) == nil {
		t.Fatal("旧播放任务被误接受")
	}
	reply.PlaybackID = 9
	reply.State = "silent"
	if worker.validateReply(request, reply) == nil {
		t.Fatal("未知播放状态被误接受")
	}
}

// TestFilePlaybackLoadingContract 加载只是受理；零分母不能被伪报为已完成播放。
func TestFilePlaybackLoadingContract(t *testing.T) {
	w := Worker{}
	request := Request{Op: "playback_file_start", Session: 7, PlaybackID: 10, FilePath: "menu/welcome.wav", Leg: "a"}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["path"] != "menu/welcome.wav" {
		t.Fatal("WAV相对路径没有传递")
	}
	reply := Reply{OK: true, Type: "playback_state", Session: 7, PlaybackID: 10, State: "loading"}
	if err := w.validateReply(request, reply); err != nil {
		t.Fatal(err)
	}
	reply.State = "completed"
	if w.validateReply(request, reply) == nil {
		t.Fatal("未加载零包被当作完成")
	}
	reply.State = "stopped"
	if err := w.validateReply(request, reply); err != nil {
		t.Fatal(err)
	}
	reply.State = "failed"
	reply.Message = "file_load_timeout"
	if err := w.validateReply(request, reply); err != nil {
		t.Fatal(err)
	}
	reply.State = "running"
	reply.TotalPackets = 1500
	if err := w.validateReply(request, reply); err != nil {
		t.Fatal(err)
	}
	reply.TotalPackets = 1501
	if w.validateReply(request, reply) == nil {
		t.Fatal("超过30秒硬上限")
	}
}
