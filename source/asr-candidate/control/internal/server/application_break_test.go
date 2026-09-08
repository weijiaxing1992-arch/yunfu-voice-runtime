package server

import (
	"rustswitch/control/internal/esl"
	"rustswitch/control/internal/media"
	"strings"
	"testing"
	"time"
)

// armedPlayback 构造已收到running的单应用；测试停止确认前后的状态边界，不把模拟结果算产品媒体验收。
func armedPlayback(t *testing.T, read bool) (*Server, *Call, *applicationLane, *[]applicationTestEvent) {
	t.Helper()
	s, c, events := applicationFixture(t)
	request := esl.ExecuteRequest{UUID: c.compatUUIDs[0], Application: "playback", Arguments: "tone_stream://%(2000,0,440)", EventUUID: compatibilityUUID()}
	if read {
		request.Application = "read"
		request.Arguments = "1 3 tone_stream://%(2000,0,440) digits 2000 #"
	}
	j, err := parseApplication(request)
	if err != nil {
		t.Fatal(err)
	}
	j.playbackID = 1
	j.playbackState = "running"
	j.started = true
	j.hardDeadline = time.Now().Add(time.Minute)
	lane := &applicationLane{uuid: c.compatUUIDs[0], call: c, active: j, heapIndex: -1}
	s.applications.lanes[lane.uuid] = lane
	s.applications.jobs = 1
	s.applications.bytes = j.bytes
	return s, c, lane, events
}
func stoppedPlayback(s *Server, lane *applicationLane) {
	s.handleApplicationMedia(applicationMediaResult{uuid: lane.uuid, generation: lane.call.Generation, playbackID: 1, op: "playback_stop", reply: media.Reply{OK: true, Type: "playback_state", PlaybackID: 1, Session: lane.call.ID, State: "stopped", SentPackets: 2, TotalPackets: 100}})
}

// TestPlaybackBreakWaitsForMediaAndDoesNotHangup 命令受理不先完成；只有目标媒体确认stopped后发布FILE PLAYED。
func TestPlaybackBreakWaitsForMediaAndDoesNotHangup(t *testing.T) {
	s, c, lane, events := armedPlayback(t, false)
	if got := s.compatibilityBreakApplication(lane.uuid); got != "+OK\n" {
		t.Fatal(got)
	}
	if len(*events) != 0 || s.applications.jobs != 1 || !lane.active.stopWanted {
		t.Fatal("break falsely completed before media confirmation")
	}
	stoppedPlayback(s, lane)
	if s.applications.jobs != 0 || c.Ended || len(*events) != 3 || (*events)[0].name != "PLAYBACK_START" || (*events)[1].name != "PLAYBACK_STOP" || (*events)[2].headers["Application-Response"] != "FILE PLAYED" {
		t.Fatal("break completion incorrect", *events)
	}
	if !strings.HasPrefix(s.compatibilityBreakApplication(lane.uuid+" all"), "-ERR") || !strings.HasPrefix(s.compatibilityBreakApplication(lane.uuid), "-ERR") {
		t.Fatal("unsupported break scope accepted")
	}
}

// TestReadBreakStopsOnlyPromptAndRetainsDigits 原版read被break后仍按收号期限报告failure或timeout。
func TestReadBreakStopsOnlyPromptAndRetainsDigits(t *testing.T) {
	for _, digits := range []string{"", "1"} {
		t.Run(digits, func(t *testing.T) {
			s, c, lane, events := armedPlayback(t, true)
			lane.active.digits = digits
			if s.compatibilityBreakApplication(lane.uuid) != "+OK\n" {
				t.Fatal("read prompt break refused")
			}
			stoppedPlayback(s, lane)
			if len(*events) != 2 || (*events)[1].name != "PLAYBACK_STOP" || lane.active == nil || lane.active.digits != digits || lane.active.digitDeadline.IsZero() {
				t.Fatal("read completed instead of continuing collection")
			}
			s.advanceApplications(time.Now().Add(3 * time.Second))
			want := "failure"
			if digits != "" {
				want = "timeout"
			}
			if c.compatVariables[0]["read_result"] != want || c.compatVariables[0]["digits"] != digits || (*events)[2].headers["Application-Response"] != "_none_" {
				t.Fatal("read break result incorrect", c.compatVariables[0])
			}
		})
	}
}

// TestPlaybackTerminatorsAndMissingFile 不同终止模式都保留实际变量；缺文件正文与资源失败区分。
func TestPlaybackTerminatorsAndMissingFile(t *testing.T) {
	for _, mode := range []string{"default", "none", "#", "AnY"} {
		t.Run(mode, func(t *testing.T) {
			s, c, lane, events := armedPlayback(t, false)
			if mode != "default" {
				c.compatVariables[0] = map[string]string{"playback_terminators": mode}
			}
			digit := "*"
			if mode == "#" {
				digit = "#"
			}
			s.applicationDTMF(c, 0, digit, 480, 8000, "RTP")
			if mode == "none" {
				if lane.active.stopWanted {
					t.Fatal("none terminated playback")
				}
				return
			}
			if c.compatVariables[0]["playback_terminator_used"] != digit || !lane.active.stopWanted {
				t.Fatal("terminator not recorded")
			}
			for _, e := range *events {
				if e.name == "CHANNEL_EXECUTE_COMPLETE" {
					t.Fatal("completion before stop confirmation")
				}
			}
			stoppedPlayback(s, lane)
			if (*events)[len(*events)-1].headers["Application-Response"] != "FILE PLAYED" {
				t.Fatal("terminated playback reported failure")
			}
		})
	}
	s, _, lane, events := armedPlayback(t, false)
	lane.active.filePath = "missing.wav"
	lane.active.playbackState = "failed"
	lane.active.fromDialplan = false
	s.failPlayback(lane, "file_load_failed: file_not_found", time.Now())
	if (*events)[0].headers["Application-Response"] != "FILE NOT FOUND" || s.applications.jobs != 0 {
		t.Fatal("missing file hidden", *events)
	}
}
