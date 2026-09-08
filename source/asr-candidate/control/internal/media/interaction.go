package media

import (
	"errors"
	"math"
)

// validateDTMFSendStats 对Rust实际发送观测做类型和固定容量检查，JSON不会静默丢弃新增字段。
func validateDTMFSendStats(stats map[string]any) error {
	for _, name := range []string{"dtmf_send_packets", "dtmf_send_errors", "dtmf_send_cancelled_digits", "dtmf_send_active"} {
		value, ok := stats[name].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value != math.Trunc(value) || (name == "dtmf_send_active" && value > 64) {
			return errors.New("invalid media DTMF send stats: " + name)
		}
	}
	return nil
}

// validateDTMFSendReply 拒绝互相矛盾的计数；不能让伪造的OK掩盖数字丢失或队列越界。
func validateDTMFSendReply(request Request, reply Reply) error {
	if reply.Type != "dtmf_send_state" || reply.Session != request.Session || reply.QueuedDigits > 32 || reply.CompletedDigits > reply.SentPackets/5 || reply.CompletedDigits > reply.AcceptedDigits || reply.FailedDigits > reply.AcceptedDigits-reply.CompletedDigits || uint64(reply.QueuedDigits) != reply.AcceptedDigits-reply.CompletedDigits-reply.FailedDigits {
		return errors.New("invalid media DTMF send counters")
	}
	if (reply.State != "idle" && reply.State != "sending" && reply.State != "failed") || (reply.State == "sending") != (reply.QueuedDigits > 0) || (reply.State == "failed" && reply.LastError == "") || (request.Op == "dtmf_send" && (reply.State != "sending" || uint32(len(request.Digits)) > reply.QueuedDigits)) {
		return errors.New("invalid media DTMF send state")
	}
	return nil
}

// validateDTMFReply 验证有界游标协议，坏媒体回复不能悄悄丢失按键或把重复事件再次执行。
// Overflow 是合法业务证据而非协议错误，由主循环令进行中的收号操作明确失败。
func validateDTMFReply(request Request, reply Reply) error {
	if reply.Type != "dtmf_events" || request.Limit < 1 || request.Limit > 64 || len(reply.Events) > request.Limit || reply.NextSeq < request.AfterSeq || reply.OldestSeq == 0 {
		return errors.New("invalid media DTMF reply")
	}
	if reply.Overflow != (request.AfterSeq < reply.OldestSeq-1) {
		return errors.New("invalid media DTMF overflow")
	}
	previous := request.AfterSeq
	for _, event := range reply.Events {
		if event.Sequence <= previous || event.Sequence < reply.OldestSeq || event.Sequence > reply.NextSeq || (event.Leg != "a" && event.Leg != "b") || event.Event > 16 {
			return errors.New("invalid media DTMF event")
		}
		expected := previous + 1
		if previous < reply.OldestSeq-1 {
			expected = reply.OldestSeq
		}
		if event.Sequence != expected {
			return errors.New("media DTMF sequence gap")
		}
		if event.ClockRate != 8000 && event.ClockRate != 16000 && event.ClockRate != 32000 && event.ClockRate != 44100 && event.ClockRate != 48000 {
			return errors.New("invalid media DTMF clock")
		}
		if event.Kind != "digit" && event.Kind != "incomplete" {
			return errors.New("invalid media DTMF kind")
		}
		if event.Kind == "digit" {
			const digits = "0123456789*#ABCD"
			if event.DurationTicks == 0 || event.Reason != "" || (event.Event < 16 && event.Digit != digits[event.Event:event.Event+1]) || (event.Event == 16 && event.Digit != "") {
				return errors.New("invalid completed media DTMF event")
			}
		} else if event.Reason == "" || event.Digit != "" {
			return errors.New("invalid incomplete media DTMF event")
		}
		previous = event.Sequence
	}
	if previous != reply.NextSeq {
		return errors.New("media DTMF cursor skips events")
	}
	return nil
}
