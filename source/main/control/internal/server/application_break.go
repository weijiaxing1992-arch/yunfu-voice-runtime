package server

import (
	"errors"
	"strings"
	"time"
)

// playbackTerminators 使用原版默认*、none/any或字面按键集；逐次按键读变量，运行中设置可生效。
func playbackTerminators(variables map[string]string) (string, error) {
	value, present := variables["playback_terminators"]
	if !present {
		return "*", nil
	}
	if strings.EqualFold(value, "none") {
		return "", nil
	}
	if strings.EqualFold(value, "any") {
		return "1234567890*#", nil
	}
	if len(value) > 16 {
		return "", errors.New("unsupported playback terminators")
	}
	for _, digit := range value {
		if !strings.ContainsRune("0123456789*#ABCD", digit) {
			return "", errors.New("unsupported playback terminators")
		}
	}
	return value, nil
}

// terminatePlaybackDigit 先写实际终止键，再等待媒体stop确认，不能仅凭DTMF或+OK发布完成。
func (s *Server) terminatePlaybackDigit(lane *applicationLane, digit string, now time.Time) {
	j := lane.active
	if j == nil || j.request.Application != "playback" || j.finishWanted {
		return
	}
	terminators, err := playbackTerminators(lane.call.compatVariables[lane.side])
	if err != nil {
		s.finishApplication(lane, "-ERR "+err.Error(), now)
		return
	}
	if !strings.Contains(terminators, digit) {
		return
	}
	if err = applicationVariable(lane, "playback_terminator_used", digit, false); err != nil {
		s.finishApplication(lane, "-ERR "+err.Error(), now)
		return
	}
	s.finishApplication(lane, "FILE PLAYED", now)
}

// compatibilityBreakApplication 仅在通话主循环执行；API受理后应用仍等真实媒体确认。
// all/both/无活动播放器不在当前子集，不能伪称清空队列或打断任意业务。
func (s *Server) compatibilityBreakApplication(arguments string) string {
	parts := strings.Fields(arguments)
	if len(parts) == 0 {
		return "-USAGE: <uuid> [all]\n"
	}
	if len(parts) != 1 {
		return "-ERR unsupported break mode\n"
	}
	entry := s.compatChannels[parts[0]]
	if entry.call == nil || entry.call.Ended {
		return "-ERR No such channel!\n"
	}
	if s.applications == nil {
		return "-ERR no active playback or read\n"
	}
	lane := s.applications.lanes[parts[0]]
	if lane == nil || lane.active == nil {
		return "-ERR no active playback or read\n"
	}
	j := lane.active
	if j.read != nil {
		if j.playbackID == 0 || (j.playbackState != "starting" && j.playbackState != "loading" && j.playbackState != "running" && !j.mediaInflight) {
			return "-ERR read has no active prompt\n"
		}
		// 原版read中的break只中断提示音，仍等待正常收号期限；不能马上伪造成功或清除已收数字。
		j.stopWanted = true
		if !j.mediaInflight {
			if err := s.submitApplicationMedia(lane, "playback_stop"); err != nil {
				applicationPlaybackRetry(j, time.Now())
			}
		}
		s.scheduleApplication(lane)
		return "+OK\n"
	}
	if j.request.Application != "playback" {
		return "-ERR break supports active playback or read prompt only\n"
	}
	if !j.finishWanted {
		s.finishApplication(lane, "FILE PLAYED", time.Now())
	}
	return "+OK\n"
}

// failPlayback 保留原版NOTFOUND正文，同时保持XML的失败停止语义，不把该文本当成普通完成。
func (s *Server) failPlayback(lane *applicationLane, message string, now time.Time) {
	j := lane.active
	if j.read != nil {
		_ = applicationVariable(lane, "read_result", "failure", false)
	}
	if j.fromDialplan {
		lane.planFailed = true
	}
	response := "-ERR " + message
	if j.request.Application == "playback" && j.filePath != "" && message == "file_load_failed: file_not_found" {
		response = "FILE NOT FOUND"
	}
	s.finishApplication(lane, response, now)
}
