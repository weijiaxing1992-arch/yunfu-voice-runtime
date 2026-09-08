package server

import (
	"strconv"
	"strings"
	"time"

	"rustswitch/control/internal/esl"
)

// compatibilityChannelSnapshot 在通话主循环内生成单腿快照，沿用UUID索引的O(1)定位。
// 只输出真实持有的信息，不伪造FreeSWITCH的核心身份、codec状态或caller profile。
func (s *Server) compatibilityChannelSnapshot(arguments string) string {
	if len(arguments) > 128 {
		return "-ERR snapshot argument budget exceeded\n"
	}
	parts := strings.Fields(arguments)
	if len(parts) == 0 {
		return "-USAGE: <uuid> [format]\n"
	}
	entry := s.compatChannels[parts[0]]
	call, side := entry.call, entry.side
	if call == nil || call.Ended {
		return "-ERR No such channel!\n"
	}
	format := "txt"
	if len(parts) > 1 {
		format = strings.ToLower(parts[1])
	}
	if len(parts) > 2 {
		return "-ERR too many snapshot arguments\n"
	}
	// 原版未识别的格式走编码文本，不当成未知命令；XML/JSON格式名不区分大小写。
	if format != "txt" && format != "plain" && format != "json" && format != "xml" {
		format = "txt"
	}
	direction, callID, answered := "inbound", call.AID, call.AStatus == 200
	if side == 1 {
		direction, callID, answered = "outbound", call.BID, call.BAnswered
	}
	state, answerState := "CS_INIT", "ringing"
	if answered {
		state, answerState = "CS_EXECUTE", "answered"
	}
	headers := map[string]string{
		"Event-Name": "CHANNEL_DATA", "Event-Date-Timestamp": strconv.FormatInt(time.Now().UnixMicro(), 10),
		"Unique-ID": parts[0], "Call-Direction": direction, "Channel-State": state, "Answer-State": answerState,
		"Channel-Name":  "rustswitch/" + direction + "/" + callID,
		"variable_uuid": parts[0], "variable_sip_call_id": callID,
	}
	if !call.Local && call.compatUUIDs[1-side] != "" {
		headers["Other-Leg-Unique-ID"] = call.compatUUIDs[1-side]
	}
	for name, value := range call.compatVariables[side] {
		// 身份字段由当前通道拥有；任何内部意外同名变量也不能覆盖真实身份。
		if name != "uuid" && name != "sip_call_id" {
			headers["variable_"+name] = value
		}
	}
	value, err := esl.SerializeChannelData(headers, format)
	if err != nil {
		return "-ERR cannot serialize channel snapshot\n"
	}
	return value
}
