package esl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ExecuteRequest 是入站 sendmsg 的已校验单次应用请求；受理只表示进入业务队列。
// 应用完成或失败必须由真实通道事件说明，协议层不能自行制造执行成功。
type ExecuteRequest struct {
	UUID          string // 已存在的通道 UUID，不能使用 SIP Call-ID 代替。
	Application   string // 原版应用名称，实际支持集合由业务引擎决定。
	Arguments     string // UTF-8 参数，正文按 Content-Length 读取后原样交付。
	EventUUID     string // 对应原版 event-uuid 和完成事件 Application-UUID。
	EventUUIDName string // 可选关联名称，不参与通道寻址。
	EventLock     bool   // 请求在当前应用完成后串行执行，不支持抢占执行。
}

// parseExecuteMessage 对照 switch_ivr_parse_event 的 execute 分支，保留真实受支持语义。
// 多循环、hold-bleg、优先级抢占和任意 call-command 尚未实现，不能悄悄忽略。
func parseExecuteMessage(f Frame, target string) (ExecuteRequest, error) {
	r := ExecuteRequest{UUID: strings.TrimSpace(target)}
	seen := make(map[string]bool, len(f.Headers))
	for _, h := range f.Headers {
		name := strings.ToLower(h.Name)
		if seen[name] {
			return r, errors.New("duplicate sendmsg header")
		}
		seen[name] = true
		switch name {
		case "call-command", "execute-app-name", "execute-app-arg", "event-uuid", "event-uuid-name", "event-lock", "async", "loops", "session-id", "content-type", "content-length":
		default:
			return r, fmt.Errorf("unsupported sendmsg header: %s", name)
		}
	}
	if r.UUID == "" {
		r.UUID = f.Get("session-id")
	} else if id := f.Get("session-id"); id != "" && id != r.UUID {
		return r, errors.New("conflicting session id")
	}
	if len(r.UUID) != 36 || strings.ContainsAny(r.UUID, " \t\r\n\x00") {
		return r, errors.New("invalid session id")
	}
	if f.Get("call-command") != "execute" {
		return r, errors.New("only call-command execute is supported")
	}
	if loops := f.Get("loops"); loops != "" && loops != "1" {
		return r, errors.New("only one application execution is supported")
	}
	r.Application, r.Arguments = f.Get("execute-app-name"), f.Get("execute-app-arg")
	if r.Application == "" || len(r.Application) > 64 || strings.ContainsAny(r.Application, " \t\r\n\x00") {
		return r, errors.New("invalid application name")
	}
	if len(f.Body) > 0 {
		if !strings.EqualFold(f.Get("Content-Type"), "text/plain") || r.Arguments != "" {
			return r, errors.New("sendmsg body requires text/plain and no argument header")
		}
		r.Arguments = string(f.Body)
	}
	if len(r.Arguments) > 4096 || !utf8.ValidString(r.Arguments) || strings.ContainsRune(r.Arguments, 0) {
		return r, errors.New("application arguments exceed UTF-8 or size limits")
	}
	var err error
	r.EventLock, err = consoleExecuteFlag(f.Get("event-lock"))
	if err != nil {
		return r, err
	}
	if _, err = consoleExecuteFlag(f.Get("async")); err != nil {
		return r, err
	}
	r.EventUUID, r.EventUUIDName = f.Get("event-uuid"), f.Get("event-uuid-name")
	if r.EventUUID == "" {
		r.EventUUID = newUUID()
	}
	if len(r.EventUUID) != 36 || len(r.EventUUIDName) > 128 || strings.ContainsAny(r.EventUUID+r.EventUUIDName, "\r\n\x00") {
		return r, errors.New("invalid application event identity")
	}
	return r, nil
}

// executeMessage 等待有界受理结果，实际等待按键期间不占用 ESL 网络读线程。
func (c *client) executeMessage(f Frame, target string) {
	r, err := parseExecuteMessage(f, target)
	if err != nil {
		c.enqueue(reply("-ERR " + err.Error()))
		return
	}
	if c.server.options.Execute == nil {
		c.enqueue(reply("-ERR application execution is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.server.options.CommandTimeout)
	defer cancel()
	c.enqueue(reply(c.server.options.Execute(ctx, r)))
}
