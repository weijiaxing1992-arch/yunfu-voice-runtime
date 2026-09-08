// Package esl 实现有界的入站事件套接字子集；业务处理器独立提供，未知命令不会虚报成功。
package esl

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Header 保留序列和重复字段，查找时按协议头名称忽略大小写。
type Header struct{ Name, Value string }

// Frame 包含命令首行、头和精确字节正文；命令参数不会做URL解码。
type Frame struct {
	Command string
	Headers []Header
	Body    []byte
	// eventGeneration 仅用于丢弃 noevents 之前尚未发送的事件；不序列化进协议。
	isEvent         bool
	eventGeneration uint64
}

// Get 返回第一个同名头字段，供Content-Type和Job-UUID等单值字段使用。
func (f Frame) Get(name string) string {
	for _, h := range f.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// ReadFrame 从共享缓冲器读取恰好一帧，粘连的下一帧留给下一次调用。
// 响应没有命令首行；请求首行由request参数明确选择，避免把正文或头误当命令。
func ReadFrame(r *bufio.Reader, request bool, maxHeader, maxBody int) (Frame, error) {
	f := Frame{}
	if maxHeader < 1 || maxBody < 0 {
		return f, errors.New("invalid ESL frame limits")
	}
	used, first, length, lengthSeen := 0, true, 0, false
	for {
		var line []byte
		for {
			part, err := r.ReadSlice('\n')
			used += len(part)
			if used > maxHeader {
				return f, errors.New("ESL headers exceed limit")
			}
			line = append(line, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				if used > 0 && err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return f, err
			}
			break
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.IndexByte(line, 0) >= 0 || bytes.IndexByte(line, '\r') >= 0 {
			return f, errors.New("invalid ESL header character")
		}
		if len(line) == 0 {
			if first {
				continue
			} // LF/CRLF空行保持对齐；累计头字节预算不会被重置。
			break
		}
		if first && request {
			f.Command = string(line)
			first = false
			continue
		}
		first = false
		name, value, ok := strings.Cut(string(line), ":")
		if !ok || name == "" || len(f.Headers) >= 256 {
			return f, errors.New("invalid ESL header")
		}
		for _, c := range name {
			if c <= 32 || c >= 127 || c == ':' {
				return f, errors.New("invalid ESL header name")
			}
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Content-Length") {
			if lengthSeen || value == "" {
				return f, errors.New("duplicate or empty ESL length")
			}
			for _, c := range value {
				if c < '0' || c > '9' {
					return f, errors.New("invalid ESL length")
				}
			}
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil || n > uint64(maxBody) {
				return f, errors.New("ESL body exceeds limit")
			}
			length, lengthSeen = int(n), true
		}
		f.Headers = append(f.Headers, Header{name, value})
	}
	f.Body = make([]byte, length)
	if _, err := io.ReadFull(r, f.Body); err != nil {
		return f, err
	}
	return f, nil
}

// Encode 统一输出LF分隔，正文长度按字节计算；头注入及重复长度直接拒绝。
func Encode(f Frame) ([]byte, error) {
	var b bytes.Buffer
	if f.Command != "" {
		if strings.ContainsAny(f.Command, "\r\n\x00") {
			return nil, errors.New("invalid ESL command")
		}
		b.WriteString(f.Command)
		b.WriteByte('\n')
	}
	lengthSeen := false
	for _, h := range f.Headers {
		if h.Name == "" || strings.ContainsAny(h.Name, ": \t\r\n\x00") || strings.ContainsAny(h.Value, "\r\n\x00") {
			return nil, errors.New("invalid outgoing ESL header")
		}
		if strings.EqualFold(h.Name, "Content-Length") {
			if lengthSeen || h.Value != strconv.Itoa(len(f.Body)) {
				return nil, errors.New("outgoing ESL length mismatch")
			}
			lengthSeen = true
		}
		fmt.Fprintf(&b, "%s: %s\n", h.Name, h.Value)
	}
	if len(f.Body) > 0 && !lengthSeen {
		fmt.Fprintf(&b, "Content-Length: %d\n", len(f.Body))
	}
	b.WriteByte('\n')
	b.Write(f.Body)
	return b.Bytes(), nil
}
