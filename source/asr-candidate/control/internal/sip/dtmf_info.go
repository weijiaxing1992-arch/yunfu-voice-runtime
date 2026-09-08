package sip

import (
	"errors"
	"mime"
	"strconv"
	"strings"
)

// ParseDTMFInfo 解析常用 INFO application/dtmf-relay 子集。只接受一个 Signal 和显式 Duration，
// 避免含糊输入被 atoi 静默变成按键0。Duration 为毫秒，与 RFC4733 的 RTP 时钟单位分开。
// 本函数不实现 application/dtmf、Nortel 格式、音内检测或发送端INFO生成。
func ParseDTMFInfo(contentType string, body []byte) (string, uint32, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/dtmf-relay") {
		return "", 0, errors.New("unsupported INFO content type")
	}
	if len(body) == 0 || len(body) > 256 || strings.ContainsRune(string(body), 0) {
		return "", 0, errors.New("invalid DTMF INFO body length")
	}
	fields := make(map[string]string, 2)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		name, value = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)
		if !found || (name != "signal" && name != "duration") || value == "" {
			return "", 0, errors.New("invalid DTMF INFO field")
		}
		if _, exists := fields[name]; exists {
			return "", 0, errors.New("duplicate DTMF INFO field")
		}
		fields[name] = value
	}
	digit := fields["signal"]
	if len(digit) != 1 || !strings.Contains("0123456789*#ABCD", digit) {
		// Sofia 常见数字事件编号也支持10..15；负数、小数、隐式0及字母小写明确拒绝。
		index, e := strconv.ParseUint(digit, 10, 8)
		if e != nil || index < 10 || index > 15 || strconv.FormatUint(index, 10) != digit {
			return "", 0, errors.New("invalid DTMF INFO signal")
		}
		digit = string("0123456789*#ABCD"[index])
	}
	duration, err := strconv.ParseUint(fields["duration"], 10, 32)
	if err != nil || duration < 20 || duration > 8191 {
		return "", 0, errors.New("DTMF INFO duration must be 20..8191 milliseconds")
	}
	return digit, uint32(duration), nil
}
