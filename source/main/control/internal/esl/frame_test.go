package esl

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestFrameEverySplit 覆盖头和中文正文每个字节边界，并验证粘连帧不会被吞掉。
func TestFrameEverySplit(t *testing.T) {
	wire := []byte("sendmsg x\r\nContent-Length: 9\r\nX-Test: yes\r\n\r\n中文abcapi echo next\n\n")
	for split := 0; split <= len(wire); split++ {
		r := bufio.NewReader(io.MultiReader(bytes.NewReader(wire[:split]), bytes.NewReader(wire[split:])))
		f, err := ReadFrame(r, true, 1024, 1024)
		if err != nil || f.Command != "sendmsg x" || string(f.Body) != "中文abc" || f.Get("x-test") != "yes" {
			t.Fatalf("split %d: %#v %v", split, f, err)
		}
		next, err := ReadFrame(r, true, 1024, 1024)
		if err != nil || next.Command != "api echo next" {
			t.Fatalf("next split %d: %#v %v", split, next, err)
		}
	}
}

// TestFrameRejectsAmbiguousLength 防止重复长度、符号长度和溢出造成帧错位或无限分配。
func TestFrameRejectsAmbiguousLength(t *testing.T) {
	for name, wire := range map[string]string{
		"duplicate":     "api x\nContent-Length: 1\ncontent-length: 1\n\nx",
		"negative":      "api x\nContent-Length: -1\n\n",
		"plus":          "api x\nContent-Length: +1\n\nx",
		"empty":         "api x\nContent-Length: \n\n",
		"overflow":      "api x\nContent-Length: 999999999999999999\n\n",
		"limit":         "api x\nContent-Length: 1025\n\n",
		"truncated":     "api x\nContent-Length: 3\n\nx",
		"embedded_cr":   "api x\rx\n\n",
		"nul":           "api x\x00\n\n",
		"bad_header":    "api x\nContent Length: 0\n\n",
		"leading_lines": strings.Repeat("\n", 1025) + "api x\n\n",
		"long_line":     "api " + strings.Repeat("x", 1025) + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadFrame(bufio.NewReaderSize(strings.NewReader(wire), 16), true, 1024, 1024); err == nil {
				t.Fatal("invalid frame accepted")
			}
		})
	}
}

// TestResponseBinaryBody 验证正文可包含空行、零字节和类似下一帧的文本。
func TestResponseBinaryBody(t *testing.T) {
	body := []byte("a\x00\n\nContent-Type: fake\n中文")
	wire, err := Encode(Frame{Headers: []Header{{"Content-Type", "api/response"}}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(bufio.NewReader(bytes.NewReader(wire)), false, 1024, 1024)
	if err != nil || !bytes.Equal(f.Body, body) {
		t.Fatalf("binary body mismatch %v", err)
	}
}

// TestOutgoingHeaderInjection 输出编码器拒绝能伪造另一条响应的头内容。
func TestOutgoingHeaderInjection(t *testing.T) {
	for _, f := range []Frame{{Command: "api x\napi y"}, {Headers: []Header{{"X", "a\nb"}}}, {Headers: []Header{{"Content-Length", "3"}}, Body: []byte("xx")}, {Headers: []Header{{"Content-Length", "0"}, {"content-length", "0"}}}} {
		if _, err := Encode(f); err == nil {
			t.Fatal("injected or inconsistent frame accepted")
		}
	}
}

// FuzzReadFrame 对任意字节输入验证解析器有界退出，不建立网络连接或执行业务命令。
func FuzzReadFrame(f *testing.F) {
	for _, seed := range []string{"api echo 中文\n\n", "auth x\r\n\r\n", "api x\nContent-Length: 3\n\nabc", "\n\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 4096 {
			return
		}
		_, _ = ReadFrame(bufio.NewReader(bytes.NewReader(b)), true, 1024, 2048)
	})
}
