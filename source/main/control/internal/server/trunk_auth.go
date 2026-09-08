package server

import (
	"errors"
	"io"
	"net/netip"
	"os"
	"rustswitch/control/internal/sip"
	"syscall"
)

// sipTrunk 的密码只存在于本进程私有内存，不进入 Config、管理 JSON 或事件报文。
type sipTrunk struct {
	password []byte             // 启动时读取、Close 时尽力清零；不通过日志格式化此结构。
	register *registrationState // 可选、唯一的上游注册操作。
}

// digestSlot 分开保留服务器与代理挑战；同 nonce 的请求计数只在新请求时递增。
type digestSlot struct {
	challenge sip.DigestChallenge // 已匹配账户域和支持算法的挑战。
	cnonce    string              // 每个 nonce 使用独立随机值，跨通话不共享。
	nc        uint32              // 已发送新请求数量，重传不增加。
}

// trunkAuthentication 每轮操作最多四次挑战；固定两槽，不因恶意 realm 增长字典。
type trunkAuthentication struct {
	slots    [2]*digestSlot // 0 为 Authorization；1 为 Proxy-Authorization。
	attempts uint8          // 本轮已接受挑战次数。
}

// inviteAuthentication 只在真正遇到认证挑战时分配；无认证的纯 UDP 路径不保存额外报文。
type inviteAuthentication struct {
	auth        trunkAuthentication  // 本通电话独立认证状态。
	lastHeaders []sip.Header         // 最后一次 INVITE 的凭据，ACK 必须原样携带。
	oldACKs     []inviteChallengeACK // 最多四条旧挑战的 ACK，重复挑战不能重复呼叫。
}

// inviteChallengeACK 保存已结束 INVITE 事务的身份及回复，随 Call 原有回收期限释放。
type inviteChallengeACK struct {
	branch, to string // 必须匹配原最终响应，不能跨分支重放。
	cseq       uint32 // 原事务序号。
	status     int    // 401 或 407。
	wire       []byte // 固定 ACK；重传不重新执行 Digest。
}

// initTrunk 在启动媒体之前读取私有凭据；拒绝软链接、非普通文件、共享权限及过大文件。
func (s *Server) initTrunk() error {
	if s.Config.SIP.TrunkAuth == nil && s.Config.SIP.Registration == nil {
		return nil
	}
	state := &sipTrunk{}
	if auth := s.Config.SIP.TrunkAuth; auth != nil {
		f, err := os.OpenFile(auth.PasswordFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return errors.New("cannot open private SIP trunk password file")
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4098 {
			f.Close()
			return errors.New("SIP trunk password file must be private, regular and bounded")
		}
		secret, err := io.ReadAll(io.LimitReader(f, 4099))
		f.Close()
		if err != nil || len(secret) > 4098 {
			clear(secret)
			return errors.New("cannot read SIP trunk password file")
		}
		// 只允许常见文本文件末尾的一组换行，不擅自修剪密码中的空格。
		if len(secret) > 0 && secret[len(secret)-1] == '\n' {
			secret[len(secret)-1] = 0
			secret = secret[:len(secret)-1]
			if len(secret) > 0 && secret[len(secret)-1] == '\r' {
				secret[len(secret)-1] = 0
				secret = secret[:len(secret)-1]
			}
		}
		if len(secret) < 1 || len(secret) > 4096 {
			clear(secret)
			return errors.New("invalid SIP trunk password length")
		}
		for _, b := range secret {
			if b < 32 || b == 127 {
				clear(secret)
				return errors.New("SIP trunk password contains control character")
			}
		}
		state.password = secret
	}
	if s.Config.SIP.Registration != nil {
		o := s.Config.SIP.RegistrationOptions()
		state.register = &registrationState{options: o, callID: randomID() + "@rustswitch-register", tag: randomID()[:16], requested: o.ExpiresSeconds}
	}
	s.trunk = state
	return nil
}

// clearTrunkCredentials 仅在 Run 退出后调用，避免与认证计算并发写同一密码缓冲。
func (s *Server) clearTrunkCredentials() {
	if s.trunk != nil {
		clear(s.trunk.password)
	}
}

// acceptTrunkChallenge 只在调用者完成固定上游与完整事务校验后使用，不缓存其他来源的凭据请求。
func (s *Server) acceptTrunkChallenge(state *trunkAuthentication, response *sip.Message) bool {
	credentials := s.Config.SIP.TrunkAuth
	if credentials == nil || s.trunk == nil || len(s.trunk.password) == 0 || state.attempts >= 4 {
		return false
	}
	index, name := 0, "www-authenticate"
	if response.Status == 407 {
		index, name = 1, "proxy-authenticate"
	} else if response.Status != 401 {
		return false
	}
	values := response.Values(name)
	if len(values) == 0 || len(values) > 8 {
		return false
	}
	for _, value := range values {
		challenge, err := sip.ParseDigestChallenge(value)
		if err != nil || credentials.Realm != "" && challenge.Realm != credentials.Realm {
			continue
		}
		old := state.slots[index]
		if old != nil && (!challenge.Stale || old.challenge.Nonce == challenge.Nonce) {
			return false
		}
		state.slots[index] = &digestSlot{challenge: challenge, cnonce: randomID()}
		state.attempts++
		return true
	}
	return false
}

// trunkAuthorization 为同一次新请求同时计算代理/服务器凭据，保存 wire 后重传只复用 wire。
func (s *Server) trunkAuthorization(state *trunkAuthentication, method, uri string) ([]sip.Header, error) {
	var headers []sip.Header
	for i, slot := range state.slots {
		if slot == nil {
			continue
		}
		if slot.nc == ^uint32(0) || s.trunk == nil || s.Config.SIP.TrunkAuth == nil {
			return nil, errors.New("SIP authentication state unavailable")
		}
		slot.nc++
		value, err := slot.challenge.Authorization(s.Config.SIP.TrunkAuth.Username, string(s.trunk.password), method, uri, slot.cnonce, slot.nc)
		if err != nil {
			return nil, err
		}
		name := "Authorization"
		if i == 1 {
			name = "Proxy-Authorization"
		}
		headers = append(headers, sip.Header{Name: name, Value: value})
	}
	return headers, nil
}

// inviteCSeq 兼容旧内部测试夹具的零值；生产 Call 明确从序号一开始。
func inviteCSeq(c *Call) uint32 {
	if c.BInviteCSeq == 0 {
		return 1
	}
	return c.BInviteCSeq
}

// inviteACK 保留已认证 INVITE 的原凭据，ACK 不增加 nonce 计数也不接受新的认证挑战。
func (s *Server) inviteACK(c *Call, uri, branch, to string) []byte {
	var headers []sip.Header
	if c.authentication != nil {
		headers = c.authentication.lastHeaders
	}
	return s.requestFlow(c.BFlow, "ACK", uri, branch, c.BFrom, to, c.BID, inviteCSeq(c), nil, headers...)
}

// byeBRequest 为已经认证的对话附上同保护域的新BYE摘要，方法/URI及nonce计数均重新计算。
// 对话内重新挑战仍属于后续扩展；这里不把缺少凭据的BYE伪装成已获服务器认证。
func (s *Server) byeBRequest(c *Call, uri, branch, to string, cseq uint32) []byte {
	var headers []sip.Header
	if c.authentication != nil {
		var err error
		headers, err = s.trunkAuthorization(&c.authentication.auth, "BYE", uri)
		if err != nil {
			s.event(c, "upstream_bye_authentication_error", "credential_state_unavailable")
			return nil
		}
	}
	return s.requestFlow(c.BFlow, "BYE", uri, branch, c.BFrom, to, c.BID, cseq, nil, headers...)
}

// replayInviteChallengeACK 处理旧认证事务的重传；必须先匹配真实连接、来源和完整身份。
func (s *Server) replayInviteChallengeACK(c *Call, m *sip.Message, source netip.AddrPort) bool {
	if c.authentication == nil || source != s.upstream() || m.TransportID != c.BFlow || m.CallID() != c.BID || sip.Tag(m.Header("from")) != c.BTag {
		return false
	}
	n, method, _ := m.CSeq()
	if method != "INVITE" {
		return false
	}
	for _, old := range c.authentication.oldACKs {
		if old.branch == m.Branch() && old.cseq == n && old.status == m.Status && old.to == m.Header("to") {
			s.sendFlow(old.wire, source, c.BFlow)
			return true
		}
	}
	return false
}

// retryInviteAuthentication 在非成功最终响应后先结束旧事务，再有界创建新 INVITE。
// 不重置原始 setup 定时器；取消、资源结束、恶意挑战或错误密码均不产生无限重拨。
func (s *Server) retryInviteAuthentication(c *Call, m *sip.Message, tx *transaction) bool {
	if m.Status != 401 && m.Status != 407 {
		return false
	}
	ack := s.inviteACK(c, c.BURI, c.InviteBranch, m.Header("to"))
	s.sendFlow(ack, s.upstream(), c.BFlow)
	if c.Ended || c.CancelWanted || c.BAnswered {
		return true
	}
	if c.authentication == nil {
		c.authentication = &inviteAuthentication{}
	}
	a := c.authentication
	if len(a.oldACKs) >= 4 || tx == nil || !s.acceptTrunkChallenge(&a.auth, m) {
		c.BFinal = true
		s.reject(c, 502, "Upstream Authentication Failed")
		return true
	}
	request, err := sip.Parse(tx.wire)
	if err != nil || inviteCSeq(c) >= 2147483646 {
		c.BFinal = true
		s.reject(c, 502, "Upstream Authentication Failed")
		return true
	}
	headers, err := s.trunkAuthorization(&a.auth, "INVITE", c.BURI)
	if err != nil {
		c.BFinal = true
		s.reject(c, 502, "Upstream Authentication Failed")
		return true
	}
	a.oldACKs = append(a.oldACKs, inviteChallengeACK{branch: c.InviteBranch, to: m.Header("to"), cseq: inviteCSeq(c), status: m.Status, wire: ack})
	c.BInviteCSeq = inviteCSeq(c) + 1
	c.BLocalCSeq = c.BInviteCSeq
	c.InviteBranch = "z9hG4bK" + randomID()
	c.BProvisional, c.BFinal, c.BCancelSent = false, false, false
	c.BRemoteTag = ""
	c.BTo = "<" + c.BURI + ">"
	wire := s.requestFlow(c.BFlow, "INVITE", c.BURI, c.InviteBranch, c.BFrom, c.BTo, c.BID, c.BInviteCSeq, request.Body, headers...)
	if len(wire) == 0 || len(wire) > sip.MaxMessageSize {
		c.BFinal = true
		s.reject(c, 502, "Upstream Authentication Failed")
		return true
	}
	a.lastHeaders = headers
	s.startTransaction(c, "INVITE", c.InviteBranch, wire, s.upstream(), c.BFlow)
	return true
}
