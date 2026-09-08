package sip

import (
	"strings"
	"testing"
)

// inviteFixture 提供完整初始请求样本，包含事务分支、来源标签和明确空消息体。
const inviteFixture = "INVITE sip:100@127.0.0.1 SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:5000;branch=z9hG4bKtest;rport\r\nFrom: <sip:a@local>;tag=alice\r\nTo: <sip:100@local>\r\nCall-ID: test-call\r\nCSeq: 1 INVITE\r\nMax-Forwards: 70\r\nContact: <sip:a@127.0.0.1:5000>\r\nContent-Length: 0\r\n\r\n"

// TestParseAndResponse 验证请求身份读取及响应标签、实际来源端口的编码。
func TestParseAndResponse(t *testing.T) {
	m, e := Parse([]byte(inviteFixture))
	if e != nil {
		t.Fatal(e)
	}
	if m.Branch() != "z9hG4bKtest" || Tag(m.Header("from")) != "alice" {
		t.Fatal("wrong dialog identifiers")
	}
	reply := Response(m, 200, "OK", "localtag", "sip:server@127.0.0.1:5060", "127.0.0.2:4444", nil)
	response, e := Parse(reply)
	if e != nil {
		t.Fatal(e)
	}
	if Tag(response.Header("to")) != "localtag" || !strings.Contains(response.Header("via"), "rport=4444") {
		t.Fatal(string(reply))
	}
}

// TestRejectAmbiguousFraming 覆盖重复长度、长度不符、方法冲突和控制字符等报文歧义。
func TestRejectAmbiguousFraming(t *testing.T) {
	cases := []string{
		strings.Replace(inviteFixture, "Content-Length: 0", "Content-Length: 0\r\nl: 0", 1),
		strings.Replace(inviteFixture, "Content-Length: 0", "Content-Length: 1", 1),
		strings.Replace(inviteFixture, "CSeq: 1 INVITE", "CSeq: 1 BYE", 1),
		inviteFixture + "smuggled",
		strings.Replace(inviteFixture, "From:", "Bad\x00From:", 1),
	}
	for _, text := range cases {
		if _, err := Parse([]byte(text)); err == nil {
			t.Fatal("accepted ambiguous SIP framing")
		}
	}
}

// TestCompactHeadersAndFolding 验证紧凑名称和合法折行在解析后保留一致标签。
func TestCompactHeadersAndFolding(t *testing.T) {
	text := strings.ReplaceAll(inviteFixture, "Via:", "v:")
	text = strings.Replace(text, "From: <sip:a@local>;tag=alice", "f: <sip:a@local>;\r\n tag=alice", 1)
	m, e := Parse([]byte(text))
	if e != nil {
		t.Fatal(e)
	}
	if Tag(m.Header("from")) != "alice" {
		t.Fatal("folded tag")
	}
}

// TestInvalidURI 确认当前不支持的协议、截断地址和头部注入文本被拒绝。
func TestInvalidURI(t *testing.T) {
	for _, text := range []string{"sips:x@host", "sip:a@host\r\nX: yes", "<sip:bad"} {
		if _, e := URI(text); e == nil {
			t.Fatal(text)
		}
	}
}

// FuzzParse 用有效及残缺报文作种子，检查任意输入不会导致解析器崩溃。
func FuzzParse(f *testing.F) {
	f.Add([]byte(inviteFixture))
	f.Add([]byte("\r\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = Parse(data) })
}
