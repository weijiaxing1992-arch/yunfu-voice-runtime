package server

import (
	"mime"
	"rustswitch/control/internal/sip"
	"strings"
)

// info 接收已建立对话的按键INFO。真实源、可靠连接、双腿标签及递增CSeq共同确认身份，
// 通道显式设置dtmf_type=info后才交给业务收号。该模式不表示实现全部INFO包或透传任意正文。
func (s *Server) info(d datagram) {
	m := d.message
	c := s.calls[s.byDialog[m.CallID()]]
	if c == nil || c.Ended || !c.Established {
		s.reply(d, 481, "Call Does Not Exist")
		return
	}
	n, _, _ := m.CSeq()
	side := 0
	if m.CallID() == c.AID {
		if d.source != c.ASource || m.TransportID != c.ARequest.TransportID || sip.Tag(m.Header("from")) != sip.Tag(c.ARequest.Header("from")) || sip.Tag(m.Header("to")) != c.ATag || n <= c.ARemoteCSeq {
			s.reply(d, 481, "Call Does Not Exist")
			return
		}
		c.ARemoteCSeq = n
	} else {
		side = 1
		if m.CallID() != c.BID || d.source != s.upstream() || m.TransportID != c.BFlow || sip.Tag(m.Header("from")) != c.BRemoteTag || sip.Tag(m.Header("to")) != c.BTag || n <= c.BRemoteCSeq {
			s.reply(d, 481, "Call Does Not Exist")
			return
		}
		c.BRemoteCSeq = n
	}
	if m.Header("route") != "" || m.Header("require") != "" || m.Header("info-package") != "" {
		s.reply(d, 501, "Unsupported INFO Extension")
		return
	}
	if !strings.EqualFold(c.compatVariables[side]["dtmf_type"], "info") {
		s.reply(d, 488, "INFO DTMF Not Enabled")
		return
	}
	contentType, _, err := mime.ParseMediaType(m.Header("content-type"))
	if err != nil || !strings.EqualFold(contentType, "application/dtmf-relay") {
		s.reply(d, 415, "Unsupported Media Type", sip.Header{Name: "Accept", Value: "application/dtmf-relay"})
		return
	}
	digit, durationMS, err := sip.ParseDTMFInfo(m.Header("content-type"), m.Body)
	if err != nil {
		s.reply(d, 400, "Malformed DTMF INFO")
		return
	}
	// handle在进入此函数前先重放事务缓存；相同INFO重传只返回原200，不再次推入DTMF应用队列。
	s.applicationDTMF(c, side, digit, durationMS*8, 8000, "INFO")
	s.reply(d, 200, "OK")
}
