package server

import (
	"rustswitch/control/internal/dialplan"
	"rustswitch/control/internal/esl"
	"strings"
	"testing"
	"time"
)

// TestSleepPreservesQueueAndHasNoSixtySecondCutoff 长等待只占一个堆项，按期限真实推进后续变量。
func TestSleepPreservesQueueAndHasNoSixtySecondCutoff(t *testing.T) {
	s, c, events := applicationFixture(t)
	a := c.compatUUIDs[0]
	start := time.Now()
	if applicationAdmit(s, a, "sleep", "120000", false) != "+OK" || applicationAdmit(s, a, "set", "after=done", true) != "+OK" {
		t.Fatal("sleep queue refused")
	}
	if len(s.applications.waiting) != 1 {
		t.Fatal("sleep timer missing")
	}
	s.advanceApplications(start.Add(61 * time.Second))
	if c.compatVariables[0]["after"] != "" || len(*events) != 1 {
		t.Fatal("sleep incorrectly completed at60seconds")
	}
	s.advanceApplications(start.Add(121 * time.Second))
	if c.compatVariables[0]["after"] != "done" || s.applications.jobs != 0 || len(s.applications.waiting) != 0 {
		t.Fatal("sleep failed to finish ordered queue")
	}
	if (*events)[1].name != "CHANNEL_EXECUTE_COMPLETE" || (*events)[1].headers["Application"] != "sleep" {
		t.Fatal("set ran before sleep completion")
	}
}

// TestParkWaitsForActualHangupWithoutTimer park不按60秒伪完成；挂断取消排队副作用并释放额度。
func TestParkWaitsForActualHangupWithoutTimer(t *testing.T) {
	s, c, events := applicationFixture(t)
	a := c.compatUUIDs[0]
	if applicationAdmit(s, a, "park", "", false) != "+OK" || applicationAdmit(s, a, "set", "after=bad", true) != "+OK" {
		t.Fatal("park queue refused")
	}
	s.advanceApplications(time.Now().Add(time.Hour))
	if len(*events) != 2 || (*events)[1].name != "CHANNEL_PARK" || s.applications.jobs != 2 || len(s.applications.waiting) != 0 {
		t.Fatal("park became timed success or created polling timers")
	}
	c.Ended = true
	s.cancelCallApplications(c, "normal_hangup")
	if c.compatVariables[0]["after"] != "" || s.applications.jobs != 0 || len(s.applications.lanes) != 0 {
		t.Fatal("park cancellation leaked or executed queued state")
	}
}

// TestDialplanActionCursorHonorsSharedBudget XML逐条取动作仍计入ESL总预算，不一次复制整份动作表。
func TestDialplanActionCursorHonorsSharedBudget(t *testing.T) {
	s, c, events := applicationFixture(t)
	a := c.compatUUIDs[0]
	xml := `<context name="default"><extension name="ivr"><condition field="destination_number" expression="^7101$"><action application="answer"/><action application="set" data="customer=中文客户"/><action application="sleep" data="50"/><action application="set" data="after=done"/><action application="park"/></condition></extension></context>`
	p, err := dialplan.Parse([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	c.Dialplan = p.Match("default", "7101")
	s.startDialplan(c)
	if c.compatVariables[0]["customer"] != "中文客户" || c.compatVariables[0]["after"] != "" || s.applications.jobs != 1 || len(s.applications.lanes[a].queue) != 0 {
		t.Fatal("XML order or lazy accounting broken")
	}
	s.advanceApplications(time.Now().Add(100 * time.Millisecond))
	if c.compatVariables[0]["after"] != "done" || s.applications.jobs != 1 || s.applications.lanes[a].active.request.Application != "park" || len(s.applications.waiting) != 0 {
		t.Fatal("XML did not enter real park")
	}
	if len(*events) != 10 || (*events)[9].name != "CHANNEL_PARK" {
		t.Fatal("missing precise start/complete events", len(*events))
	}
	c.Ended = true
	s.cancelCallApplications(c, "test cleanup")
}

// TestBasicApplicationsValidateBeforeAdmission 错误参数与不支持路径明确拒绝，不接受负数或脚本展开。
func TestBasicApplicationsValidateBeforeAdmission(t *testing.T) {
	for _, in := range [][2]string{{"sleep", "-1"}, {"sleep", "3600001"}, {"sleep", ""}, {"sleep", "20ms"}, {"answer", "is_conference"}, {"park", "x"}, {"hangup", "USER_BUSY"}, {"playback", "../secret.wav"}, {"playback", "http://host/a.wav"}, {"playback", "/tmp/a.wav"}, {"playback", "tone_stream://%(21,0,440)"}} {
		if _, err := parseApplication(esl.ExecuteRequest{Application: in[0], Arguments: in[1]}); err == nil {
			t.Fatal("bad input accepted", in)
		}
	}
	for _, in := range [][2]string{{"sleep", "0"}, {"sleep", "3600000"}, {"answer", ""}, {"park", ""}, {"hangup", "NORMAL_CLEARING"}, {"playback", "menu/a.wav"}, {"read", "1 4 prompt.wav digits 1000 #"}} {
		if _, err := parseApplication(esl.ExecuteRequest{Application: in[0], Arguments: in[1]}); err != nil {
			t.Fatal(in, err)
		}
	}
	s, c, _ := applicationFixture(t)
	if got := applicationAdmit(s, c.compatUUIDs[0], "playback", "menu.wav", false); !strings.HasPrefix(got, "-ERR file playback root") {
		t.Fatal("file accepted without configured root", got)
	}
}
