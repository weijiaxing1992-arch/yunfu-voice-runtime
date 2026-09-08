package config

import (
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// SIPTrunkAuth 只面向固定上游的 Digest 客户端；不提供终端 REGISTER 账户数据库。
type SIPTrunkAuth struct {
	Username     string `json:"username"`        // Digest 账户名，可与注册 AOR 用户不同。
	PasswordFile string `json:"password_file"`   // 绝对路径、私有普通密码文件；启动时读取一次。
	Realm        string `json:"realm,omitempty"` // 非空时只接受精确匹配的认证域。
}

// SIPRegistration 将逻辑 SIP 身份与实际固定上游 IP/端口分开；不执行 DNS 或重定向。
type SIPRegistration struct {
	AOR            string `json:"aor"`                       // 例如 sip:account@carrier.example；禁止 URI 密码和查询参数。
	RegistrarURI   string `json:"registrar_uri"`             // 例如 sip:carrier.example，网络目的地仍为 sip.upstream。
	ExpiresSeconds int    `json:"expires_seconds,omitempty"` // 请求有效期，默认 300 秒，允许 30..86400。
	RetrySeconds   int    `json:"retry_seconds,omitempty"`   // 失败退避基数，默认 30 秒，允许 1..3600。
	TimeoutMS      int    `json:"timeout_ms,omitempty"`      // 单轮含认证重试的总期限，默认 8000 毫秒。
}

// RegistrationOptions 返回独立副本，不把默认值写回管理草稿。
func (s SIP) RegistrationOptions() SIPRegistration {
	var value SIPRegistration
	if s.Registration != nil {
		value = *s.Registration
	}
	if value.ExpiresSeconds == 0 {
		value.ExpiresSeconds = 300
	}
	if value.RetrySeconds == 0 {
		value.RetrySeconds = 30
	}
	if value.TimeoutMS == 0 {
		value.TimeoutMS = 8000
	}
	return value
}

// validTrunkURI 只接受当前固定中继明确支持的简单 URI，域名仅作为 SIP 身份使用。
func validTrunkURI(value string, withUser bool) bool {
	if len(value) > 384 || !strings.HasPrefix(value, "sip:") || strings.ContainsAny(value, ";?%/\\<>\" \t\r\n") {
		return false
	}
	host := strings.TrimPrefix(value, "sip:")
	if withUser {
		user, remaining, ok := strings.Cut(host, "@")
		if !ok || len(user) == 0 || len(user) > 64 {
			return false
		}
		for _, c := range user {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("+_.-", c)) {
				return false
			}
		}
		host = remaining
	}
	if strings.Contains(host, "@") {
		return false
	}
	if strings.Contains(host, ":") {
		name, port, err := net.SplitHostPort(host)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
		host = name
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// validateTrunk 只检查静态边界，不在管理请求里读密码文件。
func (s SIP) validateTrunk() error {
	if auth := s.TrunkAuth; auth != nil {
		if len(auth.Username) == 0 || len(auth.Username) > 128 || len(auth.Realm) > 256 || !filepath.IsAbs(auth.PasswordFile) || len(auth.PasswordFile) > 4096 || strings.ContainsAny(auth.Username, ":\r\n\t") {
			return errors.New("invalid SIP trunk authentication configuration")
		}
		for _, value := range []string{auth.Username, auth.Realm, auth.PasswordFile} {
			for _, c := range value {
				if c < 32 || c == 127 {
					return errors.New("SIP trunk control character rejected")
				}
			}
		}
	}
	if s.Registration != nil {
		o := s.RegistrationOptions()
		if !validTrunkURI(o.AOR, true) || !validTrunkURI(o.RegistrarURI, false) || o.ExpiresSeconds < 30 || o.ExpiresSeconds > 86400 || o.RetrySeconds < 1 || o.RetrySeconds > 3600 || o.TimeoutMS < 500 || o.TimeoutMS > 32000 {
			return errors.New("invalid SIP registration configuration")
		}
		// 可靠上游即使断连后也要有可回呼的 Contact 监听，不能宣告不存在的 TCP/TLS 端口。
		stream := s.StreamOptions()
		if stream.UpstreamTransport == "tcp" && stream.TCPListen == "" || stream.UpstreamTransport == "tls" && stream.TLSListen == "" {
			return errors.New("SIP registration requires matching reliable transport listener")
		}
	}
	return nil
}
