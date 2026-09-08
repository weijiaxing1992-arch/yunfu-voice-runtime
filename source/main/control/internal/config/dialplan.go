package config

import (
	"errors"
	"path/filepath"
	"strings"
)

// SIPDialplan 是可选本地 XML 计划。只保存路径和 context；内容在启动时严格验证并冻结。
type SIPDialplan struct {
	File    string `json:"file"`              // 绝对普通文件路径，最多256KiB，不展开目录或读取网络。
	Context string `json:"context,omitempty"` // 默认default，须实际存在于该XML内。
}

func (s SIP) DialplanContext() string {
	if s.Dialplan == nil || s.Dialplan.Context == "" {
		return "default"
	}
	return s.Dialplan.Context
}
func (s SIP) validateDialplan() error {
	if s.Dialplan == nil {
		return nil
	}
	d := s.Dialplan
	if !filepath.IsAbs(d.File) || len(d.File) > 4096 || strings.ContainsAny(d.File, "\x00\r\n") {
		return errors.New("dialplan.file must be an absolute path of at most4096 bytes")
	}
	c := s.DialplanContext()
	if len(c) > 64 || strings.ContainsAny(c, "\x00\r\n\t /\\") {
		return errors.New("invalid dialplan context")
	}
	return nil
}
