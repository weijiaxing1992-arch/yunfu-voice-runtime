package server

import (
	"errors"
	"fmt"
	"rustswitch/control/internal/dialplan"
	"rustswitch/control/internal/esl"
	"rustswitch/control/internal/sip"
	"strings"
	"time"
)

// initDialplan 在创建任何监听或媒体进程之前验证完整快照，未知动作不会在来话时才悄悄跳过。
func (s *Server) initDialplan() error {
	if s.Config.SIP.Dialplan == nil {
		return nil
	}
	p, err := dialplan.Load(s.Config.SIP.Dialplan.File)
	if err != nil {
		return fmt.Errorf("dialplan: %w", err)
	}
	if !p.HasContext(s.Config.SIP.DialplanContext()) {
		return errors.New("configured dialplan context does not exist")
	}
	if err = p.Visit(func(a dialplan.Action) error {
		j, err := parseApplication(esl.ExecuteRequest{Application: a.Application, Arguments: a.Data})
		if err == nil && j.filePath != "" && s.Config.Media.PlaybackRoot == "" {
			return errors.New("XML file playback requires media.playback_root")
		}
		return err
	}); err != nil {
		return err
	}
	s.dialplan = p
	return nil
}

// startDialplan 共享只读动作表，每通话仅保留位置和一个已受理动作，不把64动作复制成无限队列。
func (s *Server) startDialplan(c *Call) {
	if c.Dialplan == nil || c.Ended {
		return
	}
	uuid := c.compatUUIDs[0]
	lane := &applicationLane{uuid: uuid, call: c, side: 0, heapIndex: -1, plan: c.Dialplan}
	s.applications.lanes[uuid] = lane
	s.startNextApplication(lane, time.Now())
}

// pullDialplanAction 与ESL使用同一任务和字节预算；过载时真实结束此通道而非跳过动作。
func (s *Server) pullDialplanAction(lane *applicationLane) bool {
	if lane.planFailed || lane.plan != nil && lane.planIndex >= len(lane.plan.Actions) {
		s.hangupApplication(lane.call)
		return false
	}
	if lane.plan == nil {
		return false
	}
	a := lane.plan.Actions[lane.planIndex]
	request := esl.ExecuteRequest{UUID: lane.uuid, Application: a.Application, Arguments: a.Data, EventUUID: compatibilityUUID(), EventLock: true}
	j, err := parseApplication(request)
	if err != nil || s.applications.jobs >= s.applications.maxJobs || s.applications.bytes+j.bytes > applicationByteBudget {
		s.hangupApplication(lane.call)
		return false
	}
	j.fromDialplan = true
	lane.planIndex++
	lane.queue = append(lane.queue, j)
	s.applications.jobs++
	s.applications.bytes += j.bytes
	return true
}

// answerLocalApplication 使用真实已分配A腿SDP应答，最终ACK与原信令期限仍由对话状态机管理。
func (s *Server) answerLocalApplication(c *Call) bool {
	if !c.Local || !c.Allocated || c.Ended || c.Releasing || c.AStatus >= 300 {
		return false
	}
	if c.AStatus < 200 {
		body := sip.RenderSDP(c.ID, s.Config.Media.AdvertiseIP, c.Allocation.ARTP, c.Allocation.ARTCP, c.Offer)
		s.answerA(c, 200, "OK", body)
		s.event(c, "call_answered", "")
	}
	return true
}

// applicationAnswered 仅在匹配真实ACK后完成正在等待的answer，重复ACK不会推进第二遍计划。
func (s *Server) applicationAnswered(c *Call) {
	if s.applications == nil {
		return
	}
	lane := s.applications.lanes[c.compatUUIDs[0]]
	if lane != nil && lane.active != nil && lane.active.request.Application == "answer" {
		s.completeApplication(lane, "_none_")
		s.startNextApplication(lane, time.Now())
	}
}

// hangupApplication 请求实际SIP拆线并进入统一释放，媒体释放异步确认而非仅删除应用记录。
func (s *Server) hangupApplication(c *Call) {
	if c.Ended {
		return
	}
	if c.AStatus < 200 {
		s.answerA(c, 487, "Request Terminated", nil)
	} else {
		s.sendByeA(c)
	}
	if c.BAnswered {
		s.sendByeB(c)
	} else {
		c.CancelWanted = true
		s.sendCancelB(c)
	}
	s.finish(c, "normal_hangup", false)
}

// applicationReady 保证排队期间通道状态变化仍被重新校验，不让早先受理绕过真实媒体条件。
func (s *Server) applicationReady(lane *applicationLane, j *applicationJob) error {
	c := lane.call
	if c.Ended {
		return errors.New("channel ended")
	}
	if j.filePath != "" && s.Config.Media.PlaybackRoot == "" {
		return errors.New("file playback root is not configured")
	}
	app := j.request.Application
	if app == "answer" {
		if c.Established {
			return nil
		}
		if c.Local && c.Allocated && !c.Releasing && lane.side == 0 {
			return nil
		}
		return errors.New("answer requires established channel or allocated local A leg")
	}
	if j.read != nil || j.hasPlayback() || app == "sleep" || app == "park" {
		if !c.Established || !c.Allocated || c.Releasing {
			return errors.New("application requires established media")
		}
	}
	if j.read != nil && !strings.EqualFold(c.compatVariables[lane.side]["dtmf_type"], "info") {
		if c.Offer.DTMF == nil {
			return errors.New("telephone-event was not negotiated")
		}
		if reason := s.applications.disabledDigits[c.ID][lane.side]; reason != "" {
			return errors.New("DTMF collection disabled: " + reason)
		}
	}
	return nil
}
