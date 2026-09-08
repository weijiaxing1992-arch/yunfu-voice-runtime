package sip

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

// splitReader 每次只返回一个字节，复现 TCP 在任意字节边界拆分的输入。
type splitReader struct{ source io.Reader }

func (r splitReader) Read(p []byte) (int, error) { return r.source.Read(p[:min(1, len(p))]) }

// TestStreamFramesFragmentAndCoalesce 验证多条粘包、逐字节分片、正文 CRLF 及紧凑长度不会串包。
func TestStreamFramesFragmentAndCoalesce(t *testing.T) {
	first := RequestTransport("tcp", "INVITE", "sip:100@127.0.0.1", "127.0.0.1:5060", "z9hG4bK-one", "<sip:a@127.0.0.1>;tag=one", "<sip:b@127.0.0.1>", "one", 1, []byte("a\r\n\r\nb"))
	second := bytes.ReplaceAll(RequestTransport("tls", "OPTIONS", "sip:100@127.0.0.1", "127.0.0.1:5061", "z9hG4bK-two", "<sip:a@127.0.0.1>;tag=two", "<sip:b@127.0.0.1>", "two", 1, nil), []byte("Content-Length:"), []byte("l:"))
	for _, fragment := range []bool{false, true} {
		t.Run(map[bool]string{false: "coalesced", true: "byte_fragmented"}[fragment], func(t *testing.T) {
			var source io.Reader = bytes.NewReader(append(append([]byte("\r\n"), first...), second...))
			if fragment {
				source = splitReader{source}
			}
			reader := bufio.NewReaderSize(source, 4098)
			for _, want := range [][]byte{first, second} {
				got, err := ReadStreamFrame(reader)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("帧边界丢失：%v", err)
				}
				parsed, err := Parse(got)
				if err != nil || parsed.TransportID != 0 {
					t.Fatalf("解析失败或网络输入伪造本机连接身份：%v", err)
				}
			}
			if _, err := ReadStreamFrame(reader); err != io.EOF {
				t.Fatalf("尾部应已完整消费：%v", err)
			}
		})
	}
}

// TestStreamFrameRejectsAmbiguousLengths 验证歧义、缺失、超长、截断与注入类输入均失败关闭。
func TestStreamFrameRejectsAmbiguousLengths(t *testing.T) {
	for name, wire := range map[string]string{
		"missing":         "OPTIONS sip:x SIP/2.0\r\nVia: x\r\n\r\n",
		"duplicate":       "OPTIONS sip:x SIP/2.0\r\nContent-Length: 0\r\nl: 0\r\n\r\n",
		"negative":        "OPTIONS sip:x SIP/2.0\r\nContent-Length: -1\r\n\r\n",
		"plus":            "OPTIONS sip:x SIP/2.0\r\nContent-Length: +1\r\n\r\na",
		"huge":            "OPTIONS sip:x SIP/2.0\r\nContent-Length: 999999999999999999\r\n\r\n",
		"truncated":       "OPTIONS sip:x SIP/2.0\r\nContent-Length: 10\r\n\r\na",
		"bare_lf":         "OPTIONS sip:x SIP/2.0\nContent-Length: 0\n\n",
		"line_limit":      "OPTIONS sip:x SIP/2.0\r\nX: " + strings.Repeat("a", 4100) + "\r\nContent-Length: 0\r\n\r\n",
		"keepalive_limit": strings.Repeat("\r\n", 9),
		"folded_length":   "OPTIONS sip:x SIP/2.0\r\nContent-Length: 1\r\n 2\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadStreamFrame(bufio.NewReaderSize(strings.NewReader(wire), 4098)); err == nil {
				t.Fatal("接受有歧义或无界流式消息")
			}
		})
	}
}

// FuzzStreamFraming 复用同一有限输入验证分帧不会越界、无界分配或消费超过消息上限。
func FuzzStreamFraming(f *testing.F) {
	f.Add([]byte("OPTIONS sip:x SIP/2.0\r\nContent-Length: 0\r\n\r\n"))
	f.Add([]byte("\r\n\r\n"))
	f.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > MaxMessageSize*2 {
			return
		}
		frame, err := ReadStreamFrame(bufio.NewReaderSize(bytes.NewReader(wire), 4098))
		if err == nil && len(frame) > MaxMessageSize {
			t.Fatal("分帧突破资源上限")
		}
	})
}
