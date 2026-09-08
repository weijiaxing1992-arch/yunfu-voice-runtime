package server

import (
	"rustswitch/control/internal/media"
	"strings"
	"time"
)

// applicationLifecycleEvent 仅输出本实现拥有的真实单腿信息。四种生命周期事件在
// FreeSWITCH 1.11.3中并无顶层Application-UUID；以同通道EXECUTE/COMPLETE区间关联。
func (s *Server) applicationLifecycleEvent(lane *applicationLane, j *applicationJob, name string, extra map[string]string) {
	if s.compatEvents == nil {
		return
	}
	c := lane.call
	direction, callID := "inbound", c.AID
	if lane.side == 1 {
		direction, callID = "outbound", c.BID
	}
	answer := "answered"
	if c.Ended {
		answer = "hangup"
	}
	h := map[string]string{
		"Unique-ID": lane.uuid, "Channel-State": "CS_EXECUTE", "Answer-State": answer,
		"Call-Direction": direction, "Channel-Name": "rustswitch/" + direction + "/" + callID,
	}
	for key, value := range c.compatVariables[lane.side] {
		h["variable_"+key] = value
	}
	// 身份和当前执行数据归运行时所有，同名用户变量不能覆盖真实关联。
	h["variable_uuid"], h["variable_sip_call_id"] = lane.uuid, callID
	h["variable_current_application"] = j.request.Application
	h["variable_current_application_data"] = j.request.Arguments
	if !c.Local && c.compatUUIDs[1-lane.side] != "" {
		h["Other-Leg-Unique-ID"] = c.compatUUIDs[1-lane.side]
	}
	for key, value := range extra {
		h[key] = value
	}
	s.compatEvents(name, h, nil)
}

// parkLifecycle 在真正占用执行槽时进入，在该槽真正退出时离开；失败受理没有PARK。
func (s *Server) parkLifecycle(lane *applicationLane, j *applicationJob, leaving bool) {
	if j.request.Application != "park" || !j.started {
		return
	}
	if leaving {
		if j.parkEntered && !j.parkLeft {
			j.parkLeft = true
			s.applicationLifecycleEvent(lane, j, "CHANNEL_UNPARK", nil)
		}
	} else if !j.parkEntered {
		j.parkEntered = true
		s.applicationLifecycleEvent(lane, j, "CHANNEL_PARK", nil)
	}
}

// playbackLifecycleHeaders 保留受限应用实际提交的路径，read取提示音而不是整条read参数。
// WAV路径是playback_root内相对名称；不假装与原版sounds_dir的绝对路径相同。
func playbackLifecycleHeaders(j *applicationJob) map[string]string {
	path := j.filePath
	h := make(map[string]string, 3)
	if j.tone != nil {
		path = j.request.Arguments
		if j.read != nil {
			// read语法已经严格按空白分词验证，提示音固定第三项。
			parts := strings.Fields(j.request.Arguments)
			if len(parts) >= 3 {
				path = parts[2]
			}
		}
		h["Playback-File-Type"] = "tone_stream"
	}
	h["Playback-File-Path"] = path
	return h
}

// observePlaybackLifecycle 依据媒体进程回执发事件。loading/文件缺失不算开始；
// 短音频可能首次查询已completed，仍由完整包计数证据依次补发START与STOP。
func (s *Server) observePlaybackLifecycle(lane *applicationLane, j *applicationJob, reply media.Reply) {
	valid := reply.TotalPackets > 0 && reply.SentPackets <= reply.TotalPackets
	started := valid && (reply.State == "running" || reply.State == "stopped" || reply.State == "failed" || reply.State == "completed" && reply.SentPackets == reply.TotalPackets)
	if started && !j.playbackStarted {
		j.playbackStarted = true
		s.applicationLifecycleEvent(lane, j, "PLAYBACK_START", playbackLifecycleHeaders(j))
	}
	switch reply.State {
	case "completed":
		if valid && reply.SentPackets == reply.TotalPackets {
			s.playbackLifecycleStop(lane, j, "done")
		}
	case "stopped":
		status := "break"
		if j.endedPending {
			status = "done"
		}
		s.playbackLifecycleStop(lane, j, status)
	case "failed":
		// 原版除SWITCH_STATUS_BREAK外都使用done；done不承诺播放成功。
		s.playbackLifecycleStop(lane, j, "done")
	}
}

func (s *Server) playbackLifecycleStop(lane *applicationLane, j *applicationJob, status string) {
	if !j.playbackStarted || j.playbackStopped {
		return
	}
	j.playbackStopped = true
	h := playbackLifecycleHeaders(j)
	h["Playback-Status"] = status
	s.applicationLifecycleEvent(lane, j, "PLAYBACK_STOP", h)
}

// applicationMediaReleased 只在通话已经结束且媒体所有权确定释放后推进；失败重试不会调用。
// 仍在返回途中的播放RPC保留原有有界槽位，直到回执到达，避免START晚于COMPLETE。
func (s *Server) applicationMediaReleased(c *Call) {
	if c == nil || !c.Ended || s.applications == nil {
		return
	}
	for _, uuid := range c.compatUUIDs {
		lane := s.applications.lanes[uuid]
		if lane != nil && lane.active != nil && lane.active.endedPending {
			lane.active.releaseConfirmed = true
			s.finishReleasedApplication(lane)
		}
	}
}

func (s *Server) finishReleasedApplication(lane *applicationLane) {
	j := lane.active
	if j == nil || !j.endedPending || !j.releaseConfirmed || j.mediaInflight {
		return
	}
	s.playbackLifecycleStop(lane, j, "done")
	s.completeApplication(lane, j.finishResponse)
	delete(s.applications.lanes, lane.uuid)
}

// applicationPlaybackRetry 100/200/400/800/1000ms退避并按播放编号错峰；不延长硬期限。
// 主循环每次最多推进256个堆项，每腿最多一个在途RPC，避免故障下任务一起高频重试。
func applicationPlaybackRetry(j *applicationJob, now time.Time) {
	delay := min(100*time.Millisecond*time.Duration(1<<min(j.playbackRetries, 4)), time.Second)
	j.playbackRetries = min(j.playbackRetries+1, 5)
	j.playbackDue = now.Add(delay + time.Duration(j.playbackID%37)*time.Millisecond)
}
