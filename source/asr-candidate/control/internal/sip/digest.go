package sip

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// DigestChallenge 保存一条已验证的 RFC 3261/8760 挑战，不包含账户密码。
// 仅实现 auth 与历史无 qop 模式；auth-int、AKA、userhash 不会退化为错误的认证正文。
type DigestChallenge struct {
	Realm, Nonce, Opaque string // 服务器提供的有限字符串，生成头部时始终转义。
	Algorithm, QOP       string // 已规范化的算法及选定的 qop。
	Stale                bool   // 仅 stale=true 可在已认证后替换同类别挑战。
}

// DigestParameters 解析单条 Digest 参数，支持引号和转义；拒绝重复参数、控制字符及不完整引号。
func DigestParameters(value string) (map[string]string, error) {
	fail := errors.New("invalid Digest parameters")
	if len(value) > 4096 || len(value) < 7 || !strings.EqualFold(value[:6], "Digest") || value[6] != ' ' && value[6] != '\t' {
		return nil, fail
	}
	for _, r := range value {
		if r < 32 && r != '\t' || r == 127 {
			return nil, fail
		}
	}
	rest := strings.TrimSpace(value[7:])
	out := make(map[string]string)
	for len(rest) > 0 {
		if len(out) >= 24 {
			return nil, fail
		}
		key, after, ok := strings.Cut(rest, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !ok || !token(key) {
			return nil, fail
		}
		if _, exists := out[key]; exists {
			return nil, fail
		}
		rest = strings.TrimLeft(after, " \t")
		var decoded string
		if strings.HasPrefix(rest, "\"") {
			var b strings.Builder
			closed := false
			for i := 1; i < len(rest); i++ {
				switch rest[i] {
				case '\\':
					i++
					if i >= len(rest) {
						return nil, fail
					}
					b.WriteByte(rest[i])
				case '"':
					rest = strings.TrimLeft(rest[i+1:], " \t")
					closed = true
				default:
					b.WriteByte(rest[i])
				}
				if closed {
					break
				}
			}
			if !closed {
				return nil, fail
			}
			decoded = b.String()
		} else {
			var comma bool
			decoded, rest, comma = strings.Cut(rest, ",")
			decoded = strings.TrimSpace(decoded)
			if !token(decoded) {
				return nil, fail
			}
			if comma {
				rest = "," + rest
			}
		}
		out[key] = decoded
		if rest == "" {
			break
		}
		if rest[0] != ',' {
			return nil, fail
		}
		rest = strings.TrimSpace(rest[1:])
		if rest == "" {
			return nil, fail
		}
	}
	if len(out) == 0 {
		return nil, fail
	}
	return out, nil
}

// ParseDigestChallenge 按有界支持集合筛选挑战；未知算法不会错误地按 MD5 计算。
func ParseDigestChallenge(value string) (DigestChallenge, error) {
	p, err := DigestParameters(value)
	if err != nil {
		return DigestChallenge{}, err
	}
	c := DigestChallenge{Realm: p["realm"], Nonce: p["nonce"], Opaque: p["opaque"], Algorithm: strings.ToUpper(p["algorithm"])}
	if c.Algorithm == "" {
		c.Algorithm = "MD5"
	}
	if c.Nonce == "" || len(c.Nonce) > 1024 || len(c.Realm) > 256 || len(c.Opaque) > 1024 {
		return c, errors.New("invalid Digest challenge bounds")
	}
	if _, exists := p["realm"]; !exists {
		return c, errors.New("Digest realm required")
	}
	switch c.Algorithm {
	case "MD5", "MD5-SESS", "SHA-256", "SHA-256-SESS", "SHA-512-256", "SHA-512-256-SESS":
	default:
		return c, errors.New("unsupported Digest algorithm")
	}
	if value, exists := p["qop"]; exists {
		for _, option := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(option), "auth") {
				c.QOP = "auth"
			}
		}
		if c.QOP == "" {
			return c, errors.New("unsupported Digest qop")
		}
	}
	if p["charset"] != "" && !strings.EqualFold(p["charset"], "UTF-8") || p["userhash"] != "" && !strings.EqualFold(p["userhash"], "false") {
		return c, errors.New("unsupported Digest charset or userhash")
	}
	if p["stale"] != "" && !strings.EqualFold(p["stale"], "true") && !strings.EqualFold(p["stale"], "false") {
		return c, errors.New("invalid Digest stale")
	}
	c.Stale = strings.EqualFold(p["stale"], "true")
	return c, nil
}

// digestHash 返回 RFC 定义的小写十六进制；MD5 仅用于明确选择的传统中继互通。
func digestHash(algorithm, text string) string {
	data := []byte(text)
	switch strings.TrimSuffix(algorithm, "-SESS") {
	case "SHA-256":
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	case "SHA-512-256":
		sum := sha512.Sum512_256(data)
		return hex.EncodeToString(sum[:])
	default:
		sum := md5.Sum(data)
		return hex.EncodeToString(sum[:])
	}
}

// digestQuoted 只供已经拒绝控制字符的输入使用，防止引号或反斜线改变参数边界。
func digestQuoted(value string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(value) + "\""
}

// Authorization 计算单次请求的认证值；同一次事务重传必须复用生成的完整报文，不能递增 nc。
// cnonce 由拥有者通过密码学随机源生成；错误只报告类别，绝不包含密码或派生摘要。
func (c DigestChallenge) Authorization(username, password, method, uri, cnonce string, nc uint32) (string, error) {
	c.Algorithm = strings.ToUpper(c.Algorithm)
	if len(username) == 0 || len(username) > 128 || len(password) == 0 || len(password) > 4096 || len(uri) > 1024 || !token(method) || len(cnonce) < 8 || len(cnonce) > 128 || nc == 0 {
		return "", errors.New("invalid Digest credential bounds")
	}
	for _, value := range []string{username, password, uri, cnonce, c.Realm, c.Nonce, c.Opaque} {
		for _, r := range value {
			if r < 32 || r == 127 {
				return "", errors.New("Digest control character rejected")
			}
		}
	}
	if strings.Contains(username, ":") {
		return "", errors.New("Digest username colon rejected")
	}
	// 导出类型仍需二次校验，防止调用者绕过 Parse 后触发未知算法的默认分支。
	if _, err := ParseDigestChallenge("Digest realm=" + digestQuoted(c.Realm) + ", nonce=" + digestQuoted(c.Nonce) + ", algorithm=" + c.Algorithm); err != nil || c.QOP != "" && c.QOP != "auth" {
		return "", errors.New("invalid Digest challenge")
	}
	ha1 := digestHash(c.Algorithm, username+":"+c.Realm+":"+password)
	if strings.HasSuffix(c.Algorithm, "-SESS") {
		ha1 = digestHash(c.Algorithm, ha1+":"+c.Nonce+":"+cnonce)
	}
	ha2 := digestHash(c.Algorithm, method+":"+uri)
	nonceCount := fmt.Sprintf("%08x", nc)
	input := ha1 + ":" + c.Nonce + ":" + ha2
	if c.QOP != "" {
		input = ha1 + ":" + c.Nonce + ":" + nonceCount + ":" + cnonce + ":" + c.QOP + ":" + ha2
	}
	result := "Digest username=" + digestQuoted(username) + ", realm=" + digestQuoted(c.Realm) + ", nonce=" + digestQuoted(c.Nonce) + ", uri=" + digestQuoted(uri) + ", response=" + digestQuoted(digestHash(c.Algorithm, input)) + ", algorithm=" + c.Algorithm
	if c.Opaque != "" {
		result += ", opaque=" + digestQuoted(c.Opaque)
	}
	if c.QOP != "" {
		result += ", qop=auth, nc=" + nonceCount + ", cnonce=" + digestQuoted(cnonce)
	} else if strings.HasSuffix(c.Algorithm, "-SESS") {
		result += ", cnonce=" + digestQuoted(cnonce)
	}
	return result, nil
}
