package server

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

func (j *applicationJob) hasPlayback() bool { return j.tone != nil || j.filePath != "" }

// parseApplicationPlayback 接受已有单频tone或root内规范相对WAV；真实文件与codec检查仍归Rust。
func parseApplicationPlayback(value string) (*applicationTone, string, error) {
	if strings.HasPrefix(value, "tone_stream://") {
		tone, err := parseApplicationTone(value)
		return tone, "", err
	}
	if len(value) == 0 || len(value) > 240 || !utf8.ValidString(value) || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\\:\x00\r\n\t") || !strings.HasSuffix(strings.ToLower(value), ".wav") {
		return nil, "", errors.New("playback requires bounded tone_stream or relative WAV path")
	}
	parts := strings.Split(value, "/")
	if len(parts) > 8 {
		return nil, "", errors.New("WAV path cannot exceed8 components")
	}
	for _, part := range parts {
		if part == "." || part == ".." || part == "" || len(part) > 128 {
			return nil, "", errors.New("invalid relative WAV path")
		}
	}
	return nil, value, nil
}
