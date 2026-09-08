// Package sip 提供有界 UDP/TCP/TLS SIP 报文与 SDP 编解码，不代表完整 SIP 或 FreeSWITCH 协议实现。
// 协议代码与呼叫控制器分离，便于后续接入 Sofia-SIP/PJSIP，同时保持 RTP 在 Rust 层处理。
package sip

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxMessageSize 限制单条 SIP 报文长度，防止解析超大输入导致资源膨胀。
const MaxMessageSize = 16384

// Header 保留头部名称和值；解析后的 Name 已归一化，Value 保持协议文本。
type Header struct{ Name, Value string }

// Message 统一表示 SIP 请求与响应，Status 大于零表示响应，否则使用 Method 和 URI。
type Message struct {
	TransportID uint64   // 仅由本机可信传输层写入的连接代次；解析网络输入始终为零，零表示 UDP。
	Method, URI string   // 请求方法和原始请求 URI；响应时为空。
	Status      int      // 响应状态码；请求时为零。
	Reason      string   // 响应原因短语。
	Headers     []Header // 按出现顺序保存的头部，可保留多条 Via 等合法重复项。
	Body        []byte   // 独立复制的消息体，不持有接收缓冲。
}

// canonical 将头部名称转为小写，并把支持的紧凑形式归并为同一个比较键。
func canonical(name string) string {
	name = strings.ToLower(name)
	switch name {
	case "v":
		return "via"
	case "f":
		return "from"
	case "t":
		return "to"
	case "i":
		return "call-id"
	case "m":
		return "contact"
	case "l":
		return "content-length"
	case "c":
		return "content-type"
	}
	return name
}

// Header 返回指定名称第一次出现的值；不存在时返回空字符串。
func (m *Message) Header(name string) string {
	name = canonical(name)
	for _, h := range m.Headers {
		if h.Name == name {
			return h.Value
		}
	}
	return ""
}

// Values 按报文顺序返回同名头部的全部值，供 Via 链等多值语义使用。
func (m *Message) Values(name string) []string {
	name = canonical(name)
	var out []string
	for _, h := range m.Headers {
		if h.Name == name {
			out = append(out, h.Value)
		}
	}
	return out
}

// Parse 校验完整报文的起始行、头部、事务标识和长度边界，拒绝有歧义的重复单值头部。
// 当前要求 RFC 3261 风格的 UDP/TCP/TLS Via 分支与 From 标签；接收者另校验真实传输类型。
func Parse(packet []byte) (*Message, error) {
	if len(packet) == 0 || len(packet) > MaxMessageSize || bytes.IndexByte(packet, 0) >= 0 {
		return nil, errors.New("invalid message size or NUL")
	}
	split := bytes.Index(packet, []byte("\r\n\r\n"))
	if split < 0 {
		return nil, errors.New("missing SIP header terminator")
	}
	lines := strings.Split(string(packet[:split]), "\r\n")
	if len(lines) < 2 || len(lines) > 128 {
		return nil, errors.New("invalid SIP header count")
	}
	m := &Message{}
	first := strings.SplitN(lines[0], " ", 3)
	if len(first) != 3 {
		return nil, errors.New("invalid start line")
	}
	if first[0] == "SIP/2.0" {
		status, e := strconv.Atoi(first[1])
		if e != nil || status < 100 || status > 699 {
			return nil, errors.New("invalid SIP status")
		}
		m.Status = status
		m.Reason = first[2]
	} else {
		if first[2] != "SIP/2.0" || !token(first[0]) || len(first[1]) > 1024 || strings.ContainsAny(first[1], "\r\n\t ") {
			return nil, errors.New("invalid request line")
		}
		m.Method = first[0]
		m.URI = first[1]
	}
	// 紧凑头部在重复检查前归一化，避免 Content-Length 与 l 同时出现形成两套长度解释。
	singles := map[string]bool{"from": true, "to": true, "call-id": true, "cseq": true, "content-length": true, "content-type": true, "max-forwards": true}
	seen := make(map[string]bool)
	for _, line := range lines[1:] {
		if len(line) > 4096 || strings.ContainsAny(line, "\r\n") {
			return nil, errors.New("invalid header line")
		}
		// 合法折行接续上一头部；起始位置和展开后长度仍需独立限制。
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(m.Headers) == 0 {
				return nil, errors.New("invalid header continuation")
			}
			last := &m.Headers[len(m.Headers)-1]
			last.Value += " " + strings.TrimSpace(line)
			if len(last.Value) > 4096 {
				return nil, errors.New("header too long")
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || !token(key) {
			return nil, errors.New("invalid header name")
		}
		key = canonical(key)
		if singles[key] && seen[key] {
			return nil, errors.New("duplicate singleton header")
		}
		seen[key] = true
		value = strings.TrimSpace(value)
		for _, c := range value {
			if c < 32 && c != '\t' || c == 127 {
				return nil, errors.New("control character in header")
			}
		}
		m.Headers = append(m.Headers, Header{key, value})
	}
	for _, name := range []string{"via", "from", "to", "call-id", "cseq"} {
		if m.Header(name) == "" {
			return nil, fmt.Errorf("missing %s", name)
		}
	}
	if len(m.Header("call-id")) > 256 || strings.ContainsAny(m.Header("call-id"), " \t") {
		return nil, errors.New("invalid Call-ID")
	}
	cseq, method, err := m.CSeq()
	if err != nil || cseq > 2147483647 || m.Method != "" && method != m.Method {
		return nil, errors.New("invalid CSeq")
	}
	if m.Transport() == "" || !strings.HasPrefix(m.Branch(), "z9hG4bK") {
		return nil, errors.New("requires RFC3261 UDP/TCP/TLS Via branch")
	}
	if Tag(m.Header("from")) == "" {
		return nil, errors.New("From tag required")
	}
	// UDP 数据报天然界定消息结尾；提供 Content-Length 时必须与剩余字节完全一致。
	body := packet[split+4:]
	if text := m.Header("content-length"); text != "" {
		n, e := strconv.Atoi(text)
		if e != nil || n < 0 || n != len(body) {
			return nil, errors.New("Content-Length mismatch")
		}
	}
	m.Body = append([]byte(nil), body...)
	return m, nil
}

// token 检查 SIP token 字符集，不允许空值、分隔符和控制字符混入方法或标识。
func token(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-.!%*_+`'~", c)) {
			return false
		}
	}
	return true
}

// CSeq 解析序列号与方法，按当前 RFC 3261 序号边界限制为三十一位非负整数范围。
func (m *Message) CSeq() (uint32, string, error) {
	parts := strings.Fields(m.Header("cseq"))
	if len(parts) != 2 || !token(parts[1]) {
		return 0, "", errors.New("bad CSeq")
	}
	n, e := strconv.ParseUint(parts[0], 10, 31)
	return uint32(n), parts[1], e
}

// parameter 提取分号后的名称值参数；这是当前已支持头部的简单参数解析器。
func parameter(value, name string) string {
	for _, part := range strings.Split(value, ";")[1:] {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(key, name) {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

// Tag 读取并验证标签；地址含尖括号时仅检查括号后的头部参数。
func Tag(value string) string {
	if i := strings.LastIndex(value, ">"); i >= 0 {
		value = value[i+1:]
	}
	v := parameter(";"+strings.TrimPrefix(value, ";"), "tag")
	if len(v) > 128 || !token(v) {
		return ""
	}
	return v
}

// Branch 只读取最上层 Via 的 branch，逗号后的代理链不参与当前事务身份。
func (m *Message) Branch() string {
	top, _, _ := strings.Cut(m.Header("via"), ",")
	return parameter(top, "branch")
}

// CallID 返回报文中的对话关联标识。
func (m *Message) CallID() string { return m.Header("call-id") }

// Key 组合本原型的事务关联字段；传输来源由调用者另外拼入缓存键并校验。
func (m *Message) Key() string {
	n, method, _ := m.CSeq()
	return m.CallID() + "|" + Tag(m.Header("from")) + "|" + m.Branch() + "|" + strconv.FormatUint(uint64(n), 10) + "|" + method
}

// URI 提取本原型支持的 sip: 地址并拒绝换行等注入字符，不承担完整 URI 语法或路由解析。
func URI(value string) (string, error) {
	value = strings.TrimSpace(value)
	if start := strings.IndexByte(value, '<'); start >= 0 {
		end := strings.IndexByte(value[start+1:], '>')
		if end < 0 {
			return "", errors.New("invalid name-addr")
		}
		value = value[start+1 : start+1+end]
	} else {
		value, _, _ = strings.Cut(value, ";tag=")
	}
	if !strings.HasPrefix(value, "sip:") || len(value) > 1024 || strings.ContainsAny(value, "\r\n\t <>,\"") {
		return "", errors.New("unsupported SIP URI")
	}
	return value, nil
}

// User 提取目标用户并限制可拨号字符；当前不处理百分号转义、用户密码或复杂 URI 参数。
func User(uri string) (string, error) {
	u, e := URI(uri)
	if e != nil {
		return "", e
	}
	u = strings.TrimPrefix(u, "sip:")
	u, _, _ = strings.Cut(u, "@")
	u, _, _ = strings.Cut(u, ";")
	if len(u) == 0 || len(u) > 64 {
		return "", errors.New("invalid dialed user")
	}
	for _, c := range u {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("+_.-", c)) {
			return "", errors.New("unsupported dialed user")
		}
	}
	return u, nil
}

// Response 复制事务必需头部、补充本地标签与实际来源信息，并按字节数生成 Content-Length。
// extra 等生成参数由控制器提供，调用者须保证它们不含可注入新头部的换行。
func Response(request *Message, status int, reason, tag, contact, source string, body []byte, extra ...Header) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", status, reason)
	for index, via := range request.Values("via") {
		if index == 0 {
			via = receivedVia(via, source)
		}
		fmt.Fprintf(&b, "Via: %s\r\n", via)
	}
	to := request.Header("to")
	if tag != "" && Tag(to) == "" {
		to += ";tag=" + tag
	}
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nCall-ID: %s\r\nCSeq: %s\r\n", request.Header("from"), to, request.CallID(), request.Header("cseq"))
	if contact != "" {
		fmt.Fprintf(&b, "Contact: <%s>\r\n", contact)
	}
	b.WriteString("Server: RustSwitch/0.1\r\n")
	for _, h := range extra {
		fmt.Fprintf(&b, "%s: %s\r\n", h.Name, h.Value)
	}
	if len(body) > 0 {
		b.WriteString("Content-Type: application/sdp\r\n")
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.Write(body)
	return []byte(b.String())
}

// receivedVia 以实际数据报来源更新最上层 Via 的 received/rport，保留后续代理链。
func receivedVia(via, source string) string {
	// 传输边界已限定 IPv4，因此这里可按地址和端口拆分来源。
	top, rest, comma := strings.Cut(via, ",")
	host, port, ok := strings.Cut(source, ":")
	if !ok {
		return via
	}
	parts := strings.Split(top, ";")
	out := []string{parts[0]}
	rport := false
	for _, p := range parts[1:] {
		key, _, _ := strings.Cut(strings.TrimSpace(p), "=")
		if strings.EqualFold(key, "rport") {
			rport = true
			continue
		}
		if strings.EqualFold(key, "received") {
			continue
		}
		out = append(out, p)
	}
	out = append(out, "received="+host)
	if rport {
		out = append(out, "rport="+port)
	}
	result := strings.Join(out, ";")
	if comma {
		result += "," + rest
	}
	return result
}

// Transport 读取最上层 Via 声明的协议；接收者仍必须与真实 socket 协议比较。
func (m *Message) Transport() string {
	via := strings.ToUpper(m.Header("via"))
	for _, name := range []string{"UDP", "TCP", "TLS"} {
		if strings.HasPrefix(via, "SIP/2.0/"+name+" ") {
			return strings.ToLower(name)
		}
	}
	return ""
}

// Request 生成当前受限 UDP 提供器的请求；分支、标签、CSeq 和远端目标由呼叫状态机维护。
// 此函数只负责编码，不实现事务重传、路由集、鉴权或字符串净化。
func Request(method, uri, advertise, branch, from, to, callID string, cseq uint32, body []byte) []byte {
	return RequestTransport("udp", method, uri, advertise, branch, from, to, callID, cseq, body)
}

// RequestTransport 按真实传输生成 Via 与 Contact；不负责路由、鉴权或参数净化。
func RequestTransport(transport, method, uri, advertise, branch, from, to, callID string, cseq uint32, body []byte, extra ...Header) []byte {
	var b strings.Builder
	contact := "sip:rustswitch@" + advertise
	if transport != "udp" {
		contact += ";transport=" + transport
	}
	fmt.Fprintf(&b, "%s %s SIP/2.0\r\nVia: SIP/2.0/%s %s;branch=%s;rport\r\nMax-Forwards: 70\r\nFrom: %s\r\nTo: %s\r\nCall-ID: %s\r\nCSeq: %d %s\r\n", method, uri, strings.ToUpper(transport), advertise, branch, from, to, callID, cseq, method)
	contactProvided := false
	for _, header := range extra {
		// 附加值只由本机状态机生成；仍拒绝换行，避免未来调用者破坏报文边界。
		if !token(header.Name) || strings.ContainsAny(header.Value, "\r\n\x00") {
			return nil
		}
		contactProvided = contactProvided || strings.EqualFold(header.Name, "contact")
	}
	if !contactProvided {
		fmt.Fprintf(&b, "Contact: <%s>\r\n", contact)
	}
	b.WriteString("User-Agent: RustSwitch/0.1\r\n")
	for _, header := range extra {
		fmt.Fprintf(&b, "%s: %s\r\n", header.Name, header.Value)
	}
	if len(body) > 0 {
		b.WriteString("Content-Type: application/sdp\r\n")
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	b.Write(body)
	return []byte(b.String())
}
