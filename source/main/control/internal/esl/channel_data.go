package esl

import (
	"errors"
	"sort"
	"strings"
)

// SerializeChannelData 复用事件值编码生成 uuid_dump 的正文；它不会发布 CHANNEL_DATA 事件。
// 原版 plain 指方括号包裹的原值，与 event plain 的百分号编码含义不同，不能混用。
// 当前只支持单值头；64KiB通道变量最多扩展到约384KiB，输出始终有界。
func SerializeChannelData(headers map[string]string, format string) (string, error) {
	if len(headers) > 160 {
		return "", errors.New("channel snapshot header budget exceeded")
	}
	bytes := 0
	for key, value := range headers {
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return "", errors.New("invalid channel snapshot header")
		}
		bytes += len(key) + len(value)
	}
	if bytes > 80*1024 {
		return "", errors.New("channel snapshot budget exceeded")
	}
	if format == "json" || format == "txt" || format == "xml" {
		encoding := "plain"
		if format == "json" || format == "xml" {
			encoding = format
		}
		frame, err := serializeEvent(event{headers: headers}, encoding)
		if err != nil {
			return "", err
		}
		return string(frame.Body), nil
	}
	if format != "plain" {
		return "", errors.New("unsupported snapshot format")
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output strings.Builder
	for _, key := range keys {
		output.WriteString(key)
		output.WriteString(": [")
		output.WriteString(headers[key])
		output.WriteString("]\n")
	}
	output.WriteByte('\n')
	return output.String(), nil
}
