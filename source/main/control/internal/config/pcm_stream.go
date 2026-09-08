package config

import (
	"errors"
	"path/filepath"
	"strings"
)

// PCMStream 定义显式启用的本机流式音频入口；省略整个对象才关闭监听。
// 路径与资源预算由部署配置冻结；帧读取和回写期限固定为一秒，空闲期限固定为三十秒。
type PCMStream struct {
	SocketPath     string `json:"socket_path"`               // 私有 Unix socket 的规范绝对路径，最多一百字节。
	MaxConnections int    `json:"max_connections,omitempty"` // 零值使用三十二；有效额度必须是二至六十四之间的偶数。
	MaxStreams     int    `json:"max_streams,omitempty"`     // 零值使用max_calls；所有连接共用UUID流槽上限，未知替换每槽最多保留新旧两个令牌。
}

// PCMStreamOptions 返回独立有效值，既不改变持久化配置，也不将缺省对象隐式启用。
func (c Config) PCMStreamOptions() PCMStream {
	if c.PCMStream == nil {
		return PCMStream{}
	}
	options := *c.PCMStream
	if options.MaxConnections == 0 {
		options.MaxConnections = 32
	}
	if options.MaxStreams == 0 {
		options.MaxStreams = c.Limits.MaxCalls
	}
	return options
}

// validatePCMStream 只做字符串和数值校验，配置检查不得创建目录、删除旧 socket 或读取部署文件。
func (c Config) validatePCMStream() error {
	if c.PCMStream == nil {
		return nil
	}
	options := c.PCMStreamOptions()
	path := options.SocketPath
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) || len(path) > 100 || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("pcm_stream.socket_path must be a clean absolute socket path of at most 100 bytes")
	}
	if options.MaxConnections < 2 || options.MaxConnections > 64 || options.MaxConnections%2 != 0 {
		return errors.New("pcm_stream.max_connections must be even and between 2 and 64, or zero for default")
	}
	if options.MaxStreams < 1 || options.MaxStreams > c.Limits.MaxCalls {
		return errors.New("pcm_stream.max_streams must be between 1 and limits.max_calls, or zero for default")
	}
	return nil
}
