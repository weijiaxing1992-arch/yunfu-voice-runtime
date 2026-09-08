package server

import (
	"errors"
	"reflect"
	"rustswitch/control/internal/media"
	"testing"
	"time"
)

func lifecycleNames(events []applicationTestEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.name
	}
	return names
}

// TestPlaybackLifecycleStateEvidence 不把loading、无效包计数或缺文件当成真实播放。
func TestPlaybackLifecycleStateEvidence(t *testing.T) {
	for _, state := range []string{"loading", "failed", "completed_invalid", "completed", "running_stopped", "running_failed"} {
		t.Run(state, func(t *testing.T) {
			s, _, lane, events := armedPlayback(t, false)
			j := lane.active
			j.filePath, j.tone = "menu/prompt.wav", nil
			r := media.Reply{State: state}
			switch state {
			case "completed_invalid":
				r.State, r.SentPackets, r.TotalPackets = "completed", 1, 10
			case "completed":
				r.SentPackets, r.TotalPackets = 10, 10
			case "running_stopped", "running_failed":
				r.State, r.TotalPackets = "running", 10
				s.observePlaybackLifecycle(lane, j, r)
				s.observePlaybackLifecycle(lane, j, r) // 重复状态不重复START。
				r.State = "stopped"
				if state == "running_failed" {
					r.State = "failed"
				}
			}
			s.observePlaybackLifecycle(lane, j, r)
			s.observePlaybackLifecycle(lane, j, r)
			if state == "loading" || state == "failed" || state == "completed_invalid" {
				if len(*events) != 0 {
					t.Fatal("unproven media emitted events", *events)
				}
				return
			}
			if !reflect.DeepEqual(lifecycleNames(*events), []string{"PLAYBACK_START", "PLAYBACK_STOP"}) {
				t.Fatal(*events)
			}
			want := "done"
			if state == "running_stopped" {
				want = "break"
			}
			if (*events)[1].headers["Playback-Status"] != want || (*events)[0].headers["Playback-File-Path"] != "menu/prompt.wav" {
				t.Fatal(*events)
			}
		})
	}
}

// TestPlaybackLifecycleHangupWaitsForReleaseAndInflight 捕获两条回复队列乱序、重复释放及迟到开始。
func TestPlaybackLifecycleHangupWaitsForReleaseAndInflight(t *testing.T) {
	for _, order := range []string{"reply_first", "release_first", "failed_start"} {
		t.Run(order, func(t *testing.T) {
			s, c, lane, events := armedPlayback(t, false)
			lane.active.playbackState, lane.active.mediaInflight = "starting", true
			c.Ended = true
			s.cancelCallApplications(c, "normal_hangup")
			if len(*events) != 0 || s.applications.jobs != 1 || len(s.applications.waiting) != 0 {
				t.Fatal("premature cancellation", *events)
			}
			r := applicationMediaResult{uuid: lane.uuid, generation: c.Generation, playbackID: 1, op: "playback_start", reply: media.Reply{OK: true, Type: "playback_state", PlaybackID: 1, State: "running", TotalPackets: 100}}
			if order == "release_first" {
				s.applicationMediaReleased(c)
				if len(*events) != 0 || s.applications.jobs != 1 {
					t.Fatal("release overtook pending start")
				}
			}
			if order == "failed_start" {
				r.err = errors.New("worker terminated")
			}
			s.handleApplicationMedia(r)
			if order != "release_first" && (s.applications.jobs != 1 || len(*events) > 1) {
				t.Fatal("completion before release")
			}
			s.applicationMediaReleased(c)
			s.applicationMediaReleased(c)
			s.handleApplicationMedia(r)
			want := []string{"PLAYBACK_START", "PLAYBACK_STOP", "CHANNEL_EXECUTE_COMPLETE"}
			if order == "failed_start" {
				want = []string{"CHANNEL_EXECUTE_COMPLETE"}
			}
			if !reflect.DeepEqual(lifecycleNames(*events), want) {
				t.Fatal(*events)
			}
			if s.applications.jobs != 0 || s.applications.bytes != 0 || len(s.applications.lanes) != 0 {
				t.Fatal("cancelled task leaked budget")
			}
			if order != "failed_start" && ((*events)[1].headers["Answer-State"] != "hangup" || (*events)[1].headers["Playback-Status"] != "done") {
				t.Fatal(*events)
			}
		})
	}
}

// TestLifecycleLegIdentityAndReadPrompt 当前执行的B腿身份不可借用A腿；原版没有的App UUID不添加。
func TestLifecycleLegIdentityAndReadPrompt(t *testing.T) {
	s, c, lane, events := armedPlayback(t, true)
	c.AID, c.BID = "caller", "callee"
	lane.side, lane.uuid = 1, c.compatUUIDs[1]
	c.compatVariables[1] = map[string]string{"uuid": "forged", "current_application": "forged", "custom": "中文"}
	s.observePlaybackLifecycle(lane, lane.active, media.Reply{State: "running", TotalPackets: 100})
	h := (*events)[0].headers
	if h["Unique-ID"] != lane.uuid || h["Other-Leg-Unique-ID"] != c.compatUUIDs[0] || h["Call-Direction"] != "outbound" || h["variable_sip_call_id"] != "callee" || h["variable_uuid"] != lane.uuid || h["variable_current_application"] != "read" || h["Playback-File-Path"] != "tone_stream://%(2000,0,440)" || h["variable_custom"] != "中文" {
		t.Fatal(h)
	}
	if _, ok := h["Application-UUID"]; ok {
		t.Fatal("invented application header")
	}
	if _, ok := h["Application"]; ok {
		t.Fatal("invented application header")
	}
}

// TestParkLifecycleOrderAndNoDuplicates 实际挂机先离开park再完成；重复取消不能重发事件或扣预算。
func TestParkLifecycleOrderAndNoDuplicates(t *testing.T) {
	s, c, events := applicationFixture(t)
	c.Local = true
	uuid := c.compatUUIDs[0]
	if applicationAdmit(s, uuid, "park", "", false) != "+OK" {
		t.Fatal("park rejected")
	}
	lane := s.applications.lanes[uuid]
	s.parkLifecycle(lane, lane.active, false)
	c.Ended = true
	s.cancelCallApplications(c, "normal_hangup")
	s.cancelCallApplications(c, "normal_hangup")
	if !reflect.DeepEqual(lifecycleNames(*events), []string{"CHANNEL_EXECUTE", "CHANNEL_PARK", "CHANNEL_UNPARK", "CHANNEL_EXECUTE_COMPLETE"}) {
		t.Fatal(*events)
	}
	if (*events)[1].headers["Answer-State"] != "answered" || (*events)[2].headers["Answer-State"] != "hangup" || (*events)[3].headers["Application-Response"] != "_none_" {
		t.Fatal(*events)
	}
	if _, ok := (*events)[1].headers["Other-Leg-Unique-ID"]; ok {
		t.Fatal("local channel invented B leg")
	}
	if s.applications.jobs != 0 || s.applications.bytes != 0 {
		t.Fatal("park budget leaked")
	}
}

// TestPlaybackLifecycleRPCFailureBackoff 失联不伪造STOP；重试堆节点/期限/计数都有界且播放ID错峰。
func TestPlaybackLifecycleRPCFailureBackoff(t *testing.T) {
	s, _, lane, events := armedPlayback(t, false)
	j := lane.active
	deadline := j.hardDeadline
	s.observePlaybackLifecycle(lane, j, media.Reply{State: "running", TotalPackets: 100})
	for i := 0; i < 12; i++ {
		before := time.Now()
		s.handleApplicationMedia(applicationMediaResult{uuid: lane.uuid, generation: lane.call.Generation, playbackID: 1, op: "playback_status", err: errors.New("RPC timeout")})
		if len(*events) != 1 || s.applications.jobs != 1 || len(s.applications.waiting) != 1 || !j.hardDeadline.Equal(deadline) || !j.stopWanted {
			t.Fatal("unbounded or premature failed completion")
		}
		if j.playbackDue.Before(before.Add(100*time.Millisecond)) || j.playbackDue.After(time.Now().Add(1036*time.Millisecond)) {
			t.Fatal("retry bounds", j.playbackDue.Sub(before))
		}
	}
	if j.playbackRetries != 5 {
		t.Fatal("retry counter overflow")
	}
	// 错代次/播放ID不能误关当前播放，也不消耗它的在途标志。
	j.mediaInflight = true
	s.handleApplicationMedia(applicationMediaResult{uuid: lane.uuid, generation: 999, playbackID: 1})
	if !j.mediaInflight || len(*events) != 1 {
		t.Fatal("stale result mutated current playback")
	}
}

// TestPlaybackLifecycleReleaseFailureRetainsOwnership release失败保持任务和预算；仅成功确认停止才出栈。
func TestPlaybackLifecycleReleaseFailureRetainsOwnership(t *testing.T) {
	s, c, lane, events := armedPlayback(t, false)
	worker := &media.Worker{}
	worker.Healthy.Store(true)
	worker.Generation.Store(c.Generation)
	s.pool = &media.Pool{Workers: []*media.Worker{worker}}
	s.observePlaybackLifecycle(lane, lane.active, media.Reply{State: "running", TotalPackets: 100})
	c.Ended = true
	s.cancelCallApplications(c, "normal_hangup")
	s.onMedia(mediaResult{session: c.ID, operation: "release", err: errors.New("release timeout")})
	if len(*events) != 1 || s.applications.jobs != 1 || !c.Allocated || len(s.timers) != 1 {
		t.Fatal("failed release claimed stop")
	}
	s.onMedia(mediaResult{session: c.ID, operation: "release"})
	if !reflect.DeepEqual(lifecycleNames(*events), []string{"PLAYBACK_START", "PLAYBACK_STOP", "CHANNEL_EXECUTE_COMPLETE"}) || s.applications.jobs != 0 || s.applications.bytes != 0 || len(s.applications.lanes) != 0 || c.Allocated {
		t.Fatal("confirmed release leaked", *events)
	}
}
