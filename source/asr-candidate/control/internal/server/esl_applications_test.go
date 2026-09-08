package server

import (
	"context"
	"rustswitch/control/internal/esl"
	"rustswitch/control/internal/media"
	"strings"
	"testing"
	"time"
)

type applicationTestEvent struct {
	name    string
	headers map[string]string
}

func applicationFixture(t *testing.T) (*Server, *Call, *[]applicationTestEvent) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := new([]applicationTestEvent)
	s := &Server{ctx: ctx, cancel: cancel, calls: make(map[uint64]*Call), compatEvents: func(name string, headers map[string]string, _ []byte) {
		*events = append(*events, applicationTestEvent{name, headers})
	}}
	s.initApplications()
	s.applications.workers = make([]applicationCursor, 1)
	s.applications.workers[0].generation = 1
	pt := uint8(101)
	c := &Call{ID: 1, Worker: 0, Generation: 1, Established: true, Allocated: true}
	c.Offer.DTMF = &pt
	s.calls[1] = c
	s.compatibilityEvent(c, "call_admitted", "")
	*events = nil
	return s, c, events
}
func applicationAdmit(s *Server, uuid, app, args string, lock bool) string {
	r := applicationRequest{ctx: context.Background(), value: esl.ExecuteRequest{UUID: uuid, Application: app, Arguments: args, EventUUID: compatibilityUUID(), EventLock: lock}, reply: make(chan string, 1)}
	s.handleApplicationRequest(r)
	return <-r.reply
}

// TestApplicationSetUnsetHasActualStateAndOrderedEvents 完成事件必须出现在真实变量修改之后，两腿空间独立。
func TestApplicationSetUnsetHasActualStateAndOrderedEvents(t *testing.T) {
	s, c, events := applicationFixture(t)
	a, b := c.compatUUIDs[0], c.compatUUIDs[1]
	if got := applicationAdmit(s, a, "set", "customer=中文客户", false); got != "+OK" {
		t.Fatal(got)
	}
	if len(*events) != 2 || (*events)[0].name != "CHANNEL_EXECUTE" || (*events)[1].name != "CHANNEL_EXECUTE_COMPLETE" || (*events)[1].headers["variable_customer"] != "中文客户" {
		t.Fatal("missing actual mutation evidence", *events)
	}
	if s.compatibilityChannelCommand("uuid_getvar", b+" customer") != "_undef_" {
		t.Fatal("cross-leg variable mutation")
	}
	if got := applicationAdmit(s, a, "unset", "customer", false); got != "+OK" || s.compatibilityChannelCommand("uuid_getvar", a+" customer") != "_undef_" {
		t.Fatal("unset did not remove variable")
	}
	if s.applications.jobs != 0 || s.applications.bytes != 0 || len(s.applications.lanes) != 0 {
		t.Fatal("immediate application leaked admission")
	}
}

// TestApplicationReadSerializesQueuedCommandsAndLegDigits 真正收满或终止后才执行后续应用，B腿按键不进入A腿结果。
func TestApplicationReadSerializesQueuedCommandsAndLegDigits(t *testing.T) {
	s, c, _ := applicationFixture(t)
	a := c.compatUUIDs[0]
	if applicationAdmit(s, a, "read", "1 4 silence digits 2000 #", false) != "+OK" {
		t.Fatal("read admission failed")
	}
	if !strings.HasPrefix(applicationAdmit(s, a, "set", "after=unsafe", false), "-ERR") {
		t.Fatal("unsupported concurrent execution accepted")
	}
	if applicationAdmit(s, a, "set", "after=done", true) != "+OK" {
		t.Fatal("serial queue rejected")
	}
	s.applicationDTMF(c, 1, "9", 800, 8000, "RTP")
	s.applicationDTMF(c, 0, "1", 800, 8000, "RTP")
	s.applicationDTMF(c, 0, "2", 800, 8000, "RTP")
	if s.compatibilityChannelCommand("uuid_getvar", a+" after") != "_undef_" {
		t.Fatal("queued set ran during read")
	}
	s.applicationDTMF(c, 0, "#", 800, 8000, "RTP")
	for key, want := range map[string]string{"digits": "12", "read_result": "success", "read_terminator_used": "#", "after": "done"} {
		if c.compatVariables[0][key] != want {
			t.Fatalf("%s=%q want %q", key, c.compatVariables[0][key], want)
		}
	}
	if len(s.applications.waiting) != 0 || s.applications.jobs != 0 {
		t.Fatal("completed read leaked timer or task")
	}
}

// TestApplicationReadTimeoutAndIncompleteAreDifferentResultPaths 不完整DTMF明确失败，不能被误解释为用户沉默。
func TestApplicationReadTimeoutAndIncompleteAreDifferentResultPaths(t *testing.T) {
	for _, kind := range []string{"timeout_with_digits", "too_few", "incomplete", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			s, c, events := applicationFixture(t)
			a := c.compatUUIDs[0]
			if applicationAdmit(s, a, "read", "1 4 silence digits 1000 # 50", false) != "+OK" {
				t.Fatal("read rejected")
			}
			if kind == "timeout_with_digits" {
				s.applicationDTMF(c, 0, "4", 800, 8000, "RTP")
			}
			switch kind {
			case "incomplete":
				s.handleApplicationDigits(applicationMediaResult{worker: 0, generation: 1, reply: media.Reply{OK: true, Type: "dtmf_events", NextSeq: 1, Events: []media.DTMFEvent{{Sequence: 1, Session: 1, Leg: "a", Kind: "incomplete", Reason: "missing end"}}}})
			case "overflow":
				s.handleApplicationDigits(applicationMediaResult{worker: 0, generation: 1, reply: media.Reply{OK: true, Type: "dtmf_events", Overflow: true}})
			default:
				s.advanceApplications(time.Now().Add(2 * time.Second))
			}
			want := "failure"
			if kind == "timeout_with_digits" {
				want = "timeout"
			}
			if c.compatVariables[0]["read_result"] != want {
				t.Fatal(c.compatVariables[0])
			}
			last := (*events)[len(*events)-1]
			if last.name != "CHANNEL_EXECUTE_COMPLETE" {
				t.Fatal("failure left application pending")
			}
			if (kind == "incomplete" || kind == "overflow") && !strings.HasPrefix(last.headers["Application-Response"], "-ERR") {
				t.Fatal("media failure hidden as silence")
			}
		})
	}
}

// TestApplicationQueueCancellationAndHeapAreBounded 多次按键更新同一个堆项，挂断释放活跃和已排队任务。
func TestApplicationQueueCancellationAndHeapAreBounded(t *testing.T) {
	s, c, events := applicationFixture(t)
	a := c.compatUUIDs[0]
	if applicationAdmit(s, a, "read", "1 64 silence digits 60000 none", false) != "+OK" {
		t.Fatal("read rejected")
	}
	for i := 0; i < 20; i++ {
		s.applicationDTMF(c, 0, "1", 800, 8000, "RTP")
		if len(s.applications.waiting) != 1 {
			t.Fatal("digit reset grew timer heap")
		}
	}
	for i := 0; i < applicationQueuePerLeg-1; i++ {
		if applicationAdmit(s, a, "set", "queued=value", true) != "+OK" {
			t.Fatal("queue below limit rejected")
		}
	}
	if !strings.HasPrefix(applicationAdmit(s, a, "set", "overflow=value", true), "-ERR") {
		t.Fatal("queue limit bypassed")
	}
	c.Ended = true
	s.cancelCallApplications(c, "remote hangup")
	if len(s.applications.lanes) != 0 || len(s.applications.waiting) != 0 || s.applications.jobs != 0 || s.applications.bytes != 0 {
		t.Fatal("hangup left application resources")
	}
	if _, ok := c.compatVariables[0]["queued"]; ok {
		t.Fatal("cancelled queued action executed")
	}
	completed := 0
	for _, event := range *events {
		if event.name == "CHANNEL_EXECUTE_COMPLETE" {
			completed++
		}
	}
	if completed != applicationQueuePerLeg {
		t.Fatal("accepted tasks missing cancellation outcome", completed)
	}
}

// TestApplicationUnsupportedScopeAndCancelledAdmissionNoEffects 配置范围之外直接拒绝，不产生命令或假成功事件。
func TestApplicationUnsupportedScopeAndCancelledAdmissionNoEffects(t *testing.T) {
	s, c, events := applicationFixture(t)
	a := c.compatUUIDs[0]
	for _, input := range [][2]string{{"answer", "is_conference"}, {"park", "unsupported"}, {"read", "1 2 /etc/passwd result"}, {"read", "1 65 silence result"}, {"read", "1 2 silence result 1000 +#"}, {"playback", "tone_stream://%(100000,0,440)"}, {"set", "uuid=bad"}, {"set", "customer=${system(danger)}"}} {
		if !strings.HasPrefix(applicationAdmit(s, a, input[0], input[1], false), "-ERR") {
			t.Fatal("unsupported action accepted", input)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := applicationRequest{ctx: ctx, value: esl.ExecuteRequest{UUID: a, Application: "set", Arguments: "cancelled=yes"}, reply: make(chan string, 1)}
	s.handleApplicationRequest(r)
	if !strings.HasPrefix(<-r.reply, "-ERR") || len(*events) != 0 || len(c.compatVariables[0]) != 0 {
		t.Fatal("invalid/cancelled request produced effects")
	}
}

// TestApplicationDTMFCursorCannotReapplyOldDigit 重复、过期进程代次与空事件均不能再次形成有效收号。
func TestApplicationDTMFCursorCannotReapplyOldDigit(t *testing.T) {
	s, c, _ := applicationFixture(t)
	a := c.compatUUIDs[0]
	applicationAdmit(s, a, "read", "1 3 silence digits", false)
	r := applicationMediaResult{worker: 0, generation: 1, reply: media.Reply{OK: true, Type: "dtmf_events", NextSeq: 1, Events: []media.DTMFEvent{{Sequence: 1, Session: 1, Leg: "a", Kind: "digit", Digit: "5", DurationTicks: 800, ClockRate: 8000}}}}
	s.handleApplicationDigits(r)
	if s.applications.lanes[a].active.digits != "5" {
		t.Fatal("valid journal digit missing")
	}
	r.generation = 0
	s.handleApplicationDigits(r)
	if s.applications.lanes[a].active.digits != "5" {
		t.Fatal("old media generation affected collector")
	}
	r.generation = 1
	s.handleApplicationDigits(r)
	if c.compatVariables[0]["read_result"] != "failure" || s.applications.jobs != 0 {
		t.Fatal("duplicate journal entry accepted")
	}
}

// TestApplicationMixedCaseINFOWithoutSDPKeepsRTPOut 验证模式判定与SIP入口一致；
// 无telephone-event的实际通道可用INFO收号，旧RTP失效状态与迟到RTP不能影响独立输入。
func TestApplicationMixedCaseINFOWithoutSDPKeepsRTPOut(t *testing.T) {
	s, c, events := applicationFixture(t)
	a := c.compatUUIDs[0]
	c.Offer.DTMF = nil
	if applicationAdmit(s, a, "set", "dtmf_type=iNfO", false) != "+OK" {
		t.Fatal("mixed-case INFO mode was not stored")
	}
	s.disableApplicationDigits(c, 0, "previous RTP input incomplete")
	if got := applicationAdmit(s, a, "read", "1 4 silence digits 1000 #", false); got != "+OK" {
		t.Fatal("INFO read incorrectly required SDP telephone-event or healthy RTP", got)
	}
	*events = nil
	s.handleApplicationDigits(applicationMediaResult{worker: 0, generation: 1, reply: media.Reply{OK: true, Type: "dtmf_events", NextSeq: 2, Events: []media.DTMFEvent{
		{Sequence: 1, Session: c.ID, Leg: "a", Kind: "digit", Digit: "9", DurationTicks: 800, ClockRate: 8000},
		{Sequence: 2, Session: c.ID, Leg: "a", Kind: "incomplete", Reason: "missing end"},
	}}})
	s.applicationDTMF(c, 0, "8", 800, 8000, "RTP")
	if s.applications.lanes[a].active.digits != "" || len(*events) != 0 {
		t.Fatal("RTP input or failure entered INFO collector", *events)
	}
	s.applicationDTMF(c, 0, "1", 800, 8000, "INFO")
	s.applicationDTMF(c, 0, "#", 800, 8000, "INFO")
	if c.compatVariables[0]["digits"] != "1" || c.compatVariables[0]["read_result"] != "success" || c.compatVariables[0]["read_terminator_used"] != "#" || s.applications.jobs != 0 {
		t.Fatal("INFO result was lost or mixed with RTP", c.compatVariables[0])
	}
	count := 0
	for _, event := range *events {
		if event.name == "DTMF" {
			count++
			if event.headers["DTMF-Source"] != "INFO" {
				t.Fatal("RTP event leaked")
			}
		}
	}
	if count != 2 {
		t.Fatal("INFO digit event count", count)
	}
	applicationAdmit(s, a, "unset", "dtmf_type", false)
	pt := uint8(101)
	c.Offer.DTMF = &pt
	if !strings.HasPrefix(applicationAdmit(s, a, "read", "1 4 silence digits", false), "-ERR DTMF collection disabled") {
		t.Fatal("switching back to RTP erased persistent input failure")
	}
}
