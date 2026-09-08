package sip

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// ReadStreamFrame 从可靠字节流提取一条完整 SIP 消息，保留同一次读取中的后续粘包。
// 调用者设置首字节及整帧绝对期限；这里限制头部、正文及连续保活行，避免无界分配。
func ReadStreamFrame(reader *bufio.Reader) ([]byte, error) {
	wire := make([]byte, 0, 1024)
	lines := 0
	keepalive := 0
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return nil, err
		}
		if len(line) > 4098 || len(line) < 2 || !bytes.HasSuffix(line, []byte("\r\n")) {
			return nil, errors.New("invalid SIP stream header line")
		}
		if len(wire) == 0 && bytes.Equal(line, []byte("\r\n")) {
			keepalive++
			if keepalive > 8 {
				return nil, errors.New("excessive SIP keepalive prefix")
			}
			continue
		}
		if len(wire)+len(line) > MaxMessageSize {
			return nil, errors.New("SIP stream header too large")
		}
		wire = append(wire, line...)
		lines++
		if lines > 129 {
			return nil, errors.New("too many SIP stream headers")
		}
		if len(line) == 2 {
			break
		}
	}
	// 与完整消息解析器相同地展开折行并识别紧凑 l，绝不接受两套长度解释。
	var headers []string
	for _, line := range strings.Split(string(wire), "\r\n")[1:] {
		if line == "" {
			break
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(headers) == 0 {
				return nil, errors.New("invalid stream header continuation")
			}
			headers[len(headers)-1] += " " + strings.TrimSpace(line)
		} else {
			headers = append(headers, line)
		}
	}
	length := -1
	for _, line := range headers {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errors.New("invalid SIP stream header")
		}
		if canonical(strings.TrimSpace(name)) != "content-length" {
			continue
		}
		if length >= 0 {
			return nil, errors.New("duplicate stream Content-Length")
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("empty stream Content-Length")
		}
		for _, v := range value {
			if v < '0' || v > '9' {
				return nil, errors.New("invalid stream Content-Length")
			}
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || n > uint64(MaxMessageSize-len(wire)) {
			return nil, errors.New("SIP stream body too large")
		}
		length = int(n)
	}
	if length < 0 {
		return nil, errors.New("SIP stream requires Content-Length")
	}
	end := len(wire)
	wire = append(wire, make([]byte, length)...)
	if _, err := io.ReadFull(reader, wire[end:]); err != nil {
		return nil, err
	}
	return wire, nil
}
