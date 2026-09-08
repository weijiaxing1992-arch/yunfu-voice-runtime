package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// compatibilityRequest 让ESL业务操作在通话主循环内执行，不从网络协程直接访问通话映射。
type compatibilityRequest struct {
	ctx                context.Context
	command, arguments string
	reply              chan string
}

// compatibilityChannel 通过稳定 UUID 直接定位通话腿，避免每个控制命令扫描全部通话。
type compatibilityChannel struct {
	call *Call
	side int
}

// compatibilityUUID 生成标准随机UUID；同一通话的两条腿拥有独立身份，不能使用媒体槽位充当UUID。
func compatibilityUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	value[6] = value[6]&15 | 64
	value[8] = value[8]&63 | 128
	s := hex.EncodeToString(value[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

// SetCompatibilityEvents 仅能在Run开始前安装发布器；默认nil保持未启用ESL时的媒体路径。
func (s *Server) SetCompatibilityEvents(publish func(string, map[string]string, []byte)) {
	s.compatEvents = publish
}

// CompatibilityAPI 为入站ESL提供实际业务处理；版本如实返回RustSwitch，不能伪装原版身份。
func (s *Server) CompatibilityAPI(ctx context.Context, command, arguments string) string {
	switch command {
	case "echo":
		return arguments
	case "create_uuid":
		return compatibilityUUID()
	case "version":
		return "RustSwitch Version 0.3.0 (FreeSWITCH compatibility subset)\n"
	case "status":
		return fmt.Sprintf("RustSwitch 0.3.0\nUP %d seconds\n%d active calls\n%d established calls\n", int(time.Since(s.started).Seconds()), s.Stats.Active.Load(), s.Stats.Established.Load())
	case "show":
		return compatibilityAPIInventory(arguments)
	case "uuid_exists", "uuid_getvar", "uuid_setvar", "uuid_setvar_multi", "uuid_break", "uuid_kill", "uuid_dump", "uuid_send_dtmf", "uuid_send_dtmf_status":
		request := compatibilityRequest{ctx: ctx, command: command, arguments: arguments, reply: make(chan string, 1)}
		select {
		case s.compatRequests <- request:
		case <-ctx.Done():
			return "-ERR operation cancelled\n"
		default:
			return "-ERR command queue full\n"
		}
		select {
		case value := <-request.reply:
			return value
		case <-ctx.Done():
			return "-ERR operation cancelled\n"
		case <-s.ctx.Done():
			return "-ERR server stopped\n"
		}
	default:
		return fmt.Sprintf("-ERR %s Command not found!\n", command)
	}
}

// compatibilityAPIInventory 通过真实控制入口暴露有限 API 目录，供迁移工具检查入口覆盖。
// ikey 明确标注自有实现，不伪装加载了 mod_commands；未实现的 show 子命令直接失败。
func compatibilityAPIInventory(arguments string) string {
	if arguments != "api as json" {
		return "-ERR unsupported show query; available: show api as json\n"
	}
	type row struct {
		Name           string `json:"name"`
		Description    string `json:"description"`
		Syntax         string `json:"syntax"`
		Implementation string `json:"ikey"`
	}
	rows := []row{
		{"create_uuid", "Generate a UUID", "", "rustswitch"},
		{"echo", "Echo", "<data>", "rustswitch"},
		{"show", "List implemented APIs", "api as json", "rustswitch"},
		{"status", "RustSwitch current status", "", "rustswitch"},
		{"uuid_break", "Stop active playback or read", "<uuid>", "rustswitch"},
		{"uuid_dump", "Inspect a channel snapshot", "<uuid> [format]", "rustswitch"},
		{"uuid_exists", "Check if a uuid exists", "<uuid>", "rustswitch"},
		{"uuid_getvar", "Get a variable from a channel", "<uuid> <var>", "rustswitch"},
		{"uuid_kill", "Kill a channel with normal clearing", "<uuid> [NORMAL_CLEARING]", "rustswitch"},
		{"uuid_setvar", "Set a basic variable", "<uuid> <var> [value]", "rustswitch"},
		{"uuid_send_dtmf", "Queue telephone events on a local A leg", "<uuid> <digits>[@milliseconds]", "rustswitch"},
		{"uuid_send_dtmf_status", "RustSwitch local telephone event send status", "<uuid>", "rustswitch"},
		{"uuid_setvar_multi", "Set multiple basic variables", "<uuid> <var>=<value>;<var>=<value>...", "rustswitch"},
		{"version", "RustSwitch product identity", "", "rustswitch"},
	}
	value, err := json.Marshal(struct {
		Count int   `json:"row_count"`
		Rows  []row `json:"rows"`
	}{len(rows), rows})
	if err != nil {
		return "-ERR cannot encode API inventory\n"
	}
	return string(value)
}

// handleCompatibilityRequest 与SIP状态机串行，已取消且尚未执行的操作不会产生业务副作用。
func (s *Server) handleCompatibilityRequest(request compatibilityRequest) {
	if request.ctx.Err() != nil {
		return
	}
	if request.command == "uuid_send_dtmf" || request.command == "uuid_send_dtmf_status" {
		s.handleSendDTMF(request)
		return
	}
	value := s.compatibilityChannelCommand(request.command, request.arguments)
	select {
	case request.reply <- value:
	default:
	}
}

// compatibilityChannelCommand 仅承诺基本变量与正常挂机子集，未实现数组、展开和全原因码映射。
func (s *Server) compatibilityChannelCommand(command, arguments string) string {
	if command == "uuid_dump" {
		return s.compatibilityChannelSnapshot(arguments)
	}
	if command == "uuid_break" {
		return s.compatibilityBreakApplication(arguments)
	}
	if command == "uuid_setvar_multi" {
		return s.compatibilitySetVariables(arguments)
	}
	parts := strings.SplitN(arguments, " ", 3)
	uuid := parts[0]
	if command == "uuid_kill" && uuid == "" {
		return "-USAGE: <uuid> [cause]\n"
	}
	if command == "uuid_getvar" && (len(parts) < 2 || uuid == "") {
		return "-USAGE: <uuid> <var>\n"
	}
	if command == "uuid_setvar" && (len(parts) < 2 || uuid == "") {
		return "-USAGE: <uuid> <var> [value]\n"
	}
	entry := s.compatChannels[uuid]
	channel, side := entry.call, entry.side
	if channel != nil && channel.Ended {
		channel = nil
	}
	if command == "uuid_exists" {
		return strconv.FormatBool(channel != nil && arguments == uuid)
	}
	if channel == nil {
		return "-ERR No such channel!\n"
	}
	switch command {
	case "uuid_getvar":
		if parts[1] == "uuid" {
			return uuid
		}
		if parts[1] == "sip_call_id" {
			if side == 0 {
				return channel.AID
			}
			return channel.BID
		}
		if value, ok := channel.compatVariables[side][parts[1]]; ok {
			return value
		}
		return "_undef_"
	case "uuid_setvar":
		name := parts[1]
		if name == "" {
			return "-ERR No variable specified\n"
		}
		if name == "uuid" || name == "sip_call_id" || strings.ContainsAny(name, "[]\r\n") {
			return "-ERR protected or unsupported variable\n"
		}
		if len(parts) < 3 {
			delete(channel.compatVariables[side], name)
			return "+OK\n"
		}
		if channel.compatVariables[side] == nil {
			channel.compatVariables[side] = make(map[string]string)
		}
		vars := channel.compatVariables[side]
		size := len(name) + len(parts[2])
		for key, value := range vars {
			if key != name {
				size += len(key) + len(value)
			}
		}
		if _, exists := vars[name]; !exists && len(vars) >= 128 || size > 65536 {
			return "-ERR variable budget exceeded\n"
		}
		vars[name] = parts[2]
		return "+OK\n"
	case "uuid_kill":
		if len(parts) > 1 && parts[1] != "" && parts[1] != "NORMAL_CLEARING" {
			return "-ERR unsupported hangup cause\n"
		}
		if channel.AStatus < 200 {
			s.answerA(channel, 487, "Request Terminated", nil)
		} else {
			s.sendByeA(channel)
		}
		if channel.BAnswered {
			s.sendByeB(channel)
		} else {
			channel.CancelWanted = true
			s.sendCancelB(channel)
		}
		s.finish(channel, "normal_hangup", false)
		return "+OK\n"
	}
	return "-ERR unsupported channel command\n"
}

// compatibilityEvent 独立维护真实通道身份；即使未启用ESL，XML应用仍有稳定通道和变量空间。
// 发布器只影响对外事件发送，完整原版事件字段仍需逐项验证。
func (s *Server) compatibilityEvent(call *Call, kind, reason string) {
	if call == nil {
		return
	}
	name, state := "", ""
	switch kind {
	case "call_admitted":
		name, state = "CHANNEL_CREATE", "CS_INIT"
	case "call_answered":
		name, state = "CHANNEL_ANSWER", "CS_EXECUTE"
	case "call_ended":
		name, state = "CHANNEL_HANGUP", "CS_HANGUP"
	default:
		return
	}
	legs := 2
	if call.Local {
		legs = 1
	}
	for side := range legs {
		if call.compatUUIDs[side] == "" {
			call.compatUUIDs[side] = compatibilityUUID()
		}
	}
	if s.compatChannels == nil {
		s.compatChannels = make(map[string]compatibilityChannel)
	}
	for side := range legs {
		if kind == "call_ended" {
			delete(s.compatChannels, call.compatUUIDs[side])
		} else {
			s.compatChannels[call.compatUUIDs[side]] = compatibilityChannel{call: call, side: side}
		}
	}
	if s.compatEvents == nil {
		return
	}
	for side := range legs {
		direction, callID := "inbound", call.AID
		if side == 1 {
			direction, callID = "outbound", call.BID
		}
		headers := map[string]string{"Unique-ID": call.compatUUIDs[side], "Other-Leg-Unique-ID": call.compatUUIDs[1-side], "Call-Direction": direction,
			"Channel-State": state, "Channel-Name": "rustswitch/" + direction + "/" + callID, "variable_uuid": call.compatUUIDs[side], "variable_sip_call_id": callID}
		if call.Local {
			delete(headers, "Other-Leg-Unique-ID")
		}
		if kind == "call_ended" {
			if reason == "normal_hangup" {
				headers["Hangup-Cause"] = "NORMAL_CLEARING"
			} else {
				headers["Hangup-Cause"] = "NORMAL_UNSPECIFIED"
			}
			headers["variable_rustswitch_end_reason"] = reason
		}
		for key, value := range call.compatVariables[side] {
			headers["variable_"+key] = value
		}
		s.compatEvents(name, headers, nil)
	}
}
