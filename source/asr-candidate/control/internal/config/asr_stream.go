package config

import (
	"errors"
	"path/filepath"
	"strings"
)

// ASRStream 是本次部署冻结的识别代理配置；空指针关闭功能，空对象不代表关闭。
// Runtime 只连接该 Unix socket，不拥有、创建或删除供应商的目录与 socket。
type ASRStream struct {
	SocketPath string `json:"socket_path"`           // 本机私有代理的规范绝对路径，最多一百字节。
	SampleRate uint32 `json:"sample_rate,omitempty"` // 8000 原生或 16000 按需转换；零使用 8000。
	MaxStreams int    `json:"max_streams,omitempty"` // 含起建和未知清理的额度；零使用 min(64, max_calls)。
}

// ASRStreamOptions 返回独立有效值，不修改草稿，也不将省略配置隐式启用。
func (c Config) ASRStreamOptions() ASRStream {
	if c.ASRStream == nil {
		return ASRStream{}
	}
	o := *c.ASRStream
	if o.SampleRate == 0 {
		o.SampleRate = 8000
	}
	if o.MaxStreams == 0 {
		o.MaxStreams = min(64, c.Limits.MaxCalls)
	}
	return o
}

// validateASRStream 只验证静态路径和硬预算，不触碰外部路径；同 UID 和私有目录由实际连接层复核。
func (c Config) validateASRStream() error {
	if c.ASRStream == nil {
		return nil
	}
	o := c.ASRStreamOptions()
	p := o.SocketPath
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == string(filepath.Separator) || len(p) > 100 || strings.ContainsAny(p, "\x00\r\n") {
		return errors.New("asr_stream.socket_path must be a clean absolute socket path of at most 100 bytes")
	}
	if o.SampleRate != 8000 && o.SampleRate != 16000 {
		return errors.New("asr_stream.sample_rate must be 8000 or 16000, or zero for default")
	}
	if o.MaxStreams < 1 || o.MaxStreams > c.Limits.MaxCalls {
		return errors.New("asr_stream.max_streams must be between 1 and limits.max_calls, or zero for default")
	}
	if c.PCMStream != nil && c.PCMStream.SocketPath == p {
		return errors.New("ASR provider socket and PCM input socket must use distinct paths")
	}
	return nil
}
