package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"rustswitch/control/internal/media"
)

// parseSendDTMF 仅实现原版的digits[@milliseconds]子集，不静默忽略未知字符或截断长串。
// 默认250ms对应原版默认2000个8k时钟刻度；原版w/W/+分段/~标志及变量覆盖未实现。
func parseSendDTMF(arguments string) (uuid, digits, original string, duration uint32, err error) {
	parts := strings.Split(arguments, " ")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		err = errors.New("-USAGE: <uuid> <dtmf_data>\n")
		return
	}
	if len(parts) != 2 || len(parts[0]) > 128 {
		err = errors.New("-ERR unsupported DTMF arguments\n")
		return
	}
	uuid, original, duration = parts[0], parts[1], 250
	digits = original
	if before, after, found := strings.Cut(original, "@"); found {
		digits = before
		if len(after) == 0 || len(after) > 4 || strings.Trim(after, "0123456789") != "" {
			err = errors.New("-ERR DTMF duration must be 50..1000 ms\n")
			return
		}
		value, e := strconv.ParseUint(after, 10, 32)
		if e != nil || value < 50 || value > 1000 {
			err = errors.New("-ERR DTMF duration must be 50..1000 ms\n")
			return
		}
		duration = uint32(value)
	}
	if len(digits) < 1 || len(digits) > 32 || strings.Trim(digits, "0123456789*#ABCD") != "" {
		err = errors.New("-ERR DTMF requires 1..32 digits from 0-9*#A-D\n")
	}
	return
}

// handleSendDTMF 在呼叫主循环验证归属后异步提交；固定worker回调仅写单次结果，不访问呼叫映射。
// API的+OK表示媒体队列受理；真正完成或失败由uuid_send_dtmf_status扩展查询和媒体统计证明。
func (s *Server) handleSendDTMF(request compatibilityRequest) {
	respond := func(value string) {
		select {
		case request.reply <- value:
		default:
		}
	}
	var uuid, digits, original string
	var duration uint32
	if request.command == "uuid_send_dtmf" {
		var err error
		uuid, digits, original, duration, err = parseSendDTMF(request.arguments)
		if err != nil {
			respond(err.Error())
			return
		}
	} else {
		uuid = request.arguments
		if uuid == "" || len(uuid) > 128 || strings.ContainsAny(uuid, " \t\r\n") {
			respond("-USAGE: <uuid>\n")
			return
		}
	}
	entry := s.compatChannels[uuid]
	c := entry.call
	if c == nil || c.Ended {
		respond("-ERR Cannot locate session!\n")
		return
	}
	if !c.Local || entry.side != 0 {
		respond("-ERR DTMF send supports local A leg only\n")
		return
	}
	if !c.Established || !c.Allocated || c.Releasing {
		respond("-ERR channel media is not established\n")
		return
	}
	op := "dtmf_send_status"
	if request.command == "uuid_send_dtmf" {
		mode := c.compatVariables[0]["dtmf_type"]
		if mode != "" && !strings.EqualFold(mode, "rfc2833") {
			respond("-ERR DTMF send requires rfc2833 mode\n")
			return
		}
		if c.Offer.DTMF == nil || c.Offer.DTMFClockRate != c.Offer.Codec.RTPClockRate {
			respond("-ERR telephone-event with the audio RTP clock was not negotiated\n")
			return
		}
		op = "dtmf_send"
	}
	if s.pool == nil {
		respond("-ERR media unavailable\n")
		return
	}
	command := media.Request{Generation: c.Generation, Op: op, Session: c.ID, Leg: "a", Digits: digits, DurationMS: duration}
	err := s.pool.Submit(request.ctx, c.Worker, command, func(reply media.Reply, err error) {
		if err != nil {
			respond("-ERR DTMF media operation failed: " + err.Error() + "\n")
			return
		}
		if !reply.OK {
			respond("-ERR " + reply.Message + "\n")
			return
		}
		if op == "dtmf_send" {
			respond(fmt.Sprintf("+OK %s sent DTMF %s.\n", uuid, original))
			return
		}
		// 这是RustSwitch扩展状态，不冒充FreeSWITCH存在同名API；不导出媒体进程/私有路径。
		body, err := json.Marshal(map[string]any{"uuid": uuid, "state": reply.State, "accepted_digits": reply.AcceptedDigits, "completed_digits": reply.CompletedDigits, "failed_digits": reply.FailedDigits, "queued_digits": reply.QueuedDigits, "sent_packets": reply.SentPackets, "last_error": reply.LastError})
		if err != nil {
			respond("-ERR cannot encode DTMF state\n")
			return
		}
		respond(string(body))
	})
	if err != nil {
		respond("-ERR DTMF command was not queued: " + err.Error() + "\n")
	}
}
