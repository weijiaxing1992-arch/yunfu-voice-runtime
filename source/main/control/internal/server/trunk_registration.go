package server

import (
	"net/netip"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/sip"
	"strconv"
	"strings"
	"time"
)

// SIPRegistrationStatus 是只含状态的只读快照；不含账户、密码、挑战或认证头。
type SIPRegistrationStatus struct {
	Enabled   bool   `json:"enabled"`               // 是否配置了上游注册客户端。
	State     string `json:"state"`                 // disabled/registering/registered/retrying/unregistered。
	Status    int    `json:"last_status,omitempty"` // 最近匹配的上游最终响应状态码。
	Error     string `json:"error,omitempty"`       // 本机固定错误类别，绝不反射上游正文。
	ExpiresAt string `json:"expires_at,omitempty"`  // 实际绑定到期时间；并非承诺可达性。
}

// registrationState 只属于 Run 主循环，最多一条在途请求和三个可替换定时器。
type registrationState struct {
	options     config.SIPRegistration // 已应用默认值的冻结配置。
	callID, tag string                 // 整个注册生命周期保持相同身份。
	branch      string                 // 每次新请求产生新分支，重传复用。
	cseq        uint32                 // 认证、刷新和注销都递增。
	flow        uint64                 // 固定本次真实连接，响应不跨重连代次。
	contact     string                 // 当前实际发布的唯一 Contact URI。
	wire        []byte                 // 只用于当前事务重传，最多16KiB。
	interval    time.Duration          // UDP 非 INVITE T1/T2 重传节奏。
	deadline    time.Time              // 单轮含全部挑战的固定总期限。
	sentAt      time.Time              // 当前新请求首次发送时刻，计算保守的剩余绑定时间。
	expiresAt   time.Time              // 最近确认绑定的到期点。
	version     uint64                 // 过滤被替换的旧重传/超时。
	requested   int                    // 当前期望有效期；423 可有界调高。
	pending     bool                   // 当前轮尚未结束；阻止停机过早返回。
	closing     bool                   // 停机只注销一次，不再进入周期注册。
	adjusted    bool                   // 单轮只接受一次有效的 Min-Expires 调整。
	failures    uint8                  // 有界指数退避，不无限增长。
	auth        trunkAuthentication    // 本注册对象的两个挑战槽及 nonce 计数。
}

// RegistrationStatus 供管理线程读取原子快照，不读取注册主循环的可变字段。
func (s *Server) RegistrationStatus() SIPRegistrationStatus {
	if value := s.trunkSnapshot.Load(); value != nil {
		return *value
	}
	return SIPRegistrationStatus{State: "disabled"}
}

// registration 返回可选的唯一状态，禁用功能时保持常数级空检查。
func (s *Server) registration() *registrationState {
	if s.trunk == nil {
		return nil
	}
	return s.trunk.register
}

// publishRegistration 不输出上游 Reason/Contact/挑战；HTTP 和日志无法取得派生认证数据。
func (s *Server) publishRegistration(state string, status int, reason string) {
	r := s.registration()
	if r == nil {
		return
	}
	value := &SIPRegistrationStatus{Enabled: true, State: state, Status: status, Error: reason}
	if !r.expiresAt.IsZero() && time.Now().Before(r.expiresAt) {
		value.ExpiresAt = r.expiresAt.UTC().Format(time.RFC3339Nano)
	}
	s.trunkSnapshot.Store(value)
	s.event(nil, "trunk_registration_"+state, reason)
}

// startRegistration 在接收器启动后发起第一轮；固定上游不要求任何新增工作线程。
func (s *Server) startRegistration() {
	if s.registration() != nil {
		s.beginRegistration(false)
	}
}

// stopRegistration 在退出排空开始时注销，最多等待三秒；不因认证挑战重置期限。
func (s *Server) stopRegistration() {
	r := s.registration()
	if r == nil || r.closing {
		return
	}
	s.beginRegistration(true)
}

// registrationPending 与活动通话/事务一起限制正常退出；强制上下文取消仍立即结束。
func (s *Server) registrationPending() bool { r := s.registration(); return r != nil && r.pending }

// beginRegistration 取消上一轮计时并开一个固定期限的操作，保留同服务器的 nonce 计数。
func (s *Server) beginRegistration(closing bool) {
	r := s.registration()
	if r == nil || r.closing {
		return
	}
	for _, kind := range []string{"register_retry", "register_expire", "register_next"} {
		s.cancelTimer(kind, 0, "")
	}
	r.closing, r.pending, r.adjusted = closing, true, false
	r.auth.attempts = 0
	timeout := time.Duration(r.options.TimeoutMS) * time.Millisecond
	if closing {
		timeout = 3 * time.Second
	}
	r.deadline = time.Now().Add(timeout)
	s.sendRegistration()
}

// sendRegistration 每个新请求递增 CSeq、生成新分支；可靠传输不创建重传计时器。
func (s *Server) sendRegistration() {
	r := s.registration()
	if r == nil || !r.pending {
		return
	}
	if time.Now().After(r.deadline) {
		s.registrationFailed(408, "timeout", 0)
		return
	}
	flow := s.outgoingFlow()
	if s.Config.SIP.StreamOptions().UpstreamTransport != "udp" && s.flow(flow) == nil {
		s.registrationFailed(503, "transport_unavailable", 0)
		return
	}
	if r.cseq >= 2147483646 {
		s.registrationFailed(500, "sequence_exhausted", 3600)
		return
	}
	r.flow, r.branch = flow, "z9hG4bK"+randomID()
	r.cseq++
	user, _, _ := strings.Cut(strings.TrimPrefix(r.options.AOR, "sip:"), "@")
	kind, address := s.flowAdvertise(flow)
	r.contact = "sip:" + user + "@" + address
	if kind != "udp" {
		r.contact += ";transport=" + kind
	}
	expires := r.requested
	if r.closing {
		expires = 0
	}
	headers, err := s.trunkAuthorization(&r.auth, "REGISTER", r.options.RegistrarURI)
	if err != nil {
		s.registrationFailed(502, "authentication_failed", 0)
		return
	}
	headers = append(headers, sip.Header{Name: "Contact", Value: "<" + r.contact + ">;expires=" + strconv.Itoa(expires)}, sip.Header{Name: "Expires", Value: strconv.Itoa(expires)})
	r.wire = s.requestFlow(flow, "REGISTER", r.options.RegistrarURI, r.branch, "<"+r.options.AOR+">;tag="+r.tag, "<"+r.options.AOR+">", r.callID, r.cseq, nil, headers...)
	if len(r.wire) == 0 || len(r.wire) > sip.MaxMessageSize {
		s.registrationFailed(502, "request_too_large", 0)
		return
	}
	r.version++
	r.interval = 500 * time.Millisecond
	r.sentAt = time.Now()
	s.cancelTimer("register_retry", 0, "")
	s.sendFlow(r.wire, s.upstream(), flow)
	if flow == 0 {
		s.schedule("register_retry", 0, "", r.version, r.interval)
	}
	s.schedule("register_expire", 0, "", r.version, time.Until(r.deadline))
	s.publishRegistration("registering", 0, "")
}

// handleRegistrationResponse 消费 REGISTER 响应；完整身份不符时不影响任何状态或期限。
func (s *Server) handleRegistrationResponse(m *sip.Message, source netip.AddrPort) bool {
	r := s.registration()
	if r == nil {
		return false
	}
	n, method, _ := m.CSeq()
	if method != "REGISTER" {
		return false
	}
	if r == nil || !r.pending || source != s.upstream() || m.TransportID != r.flow || m.CallID() != r.callID || m.Branch() != r.branch || n != r.cseq || sip.Tag(m.Header("from")) != r.tag {
		return true
	}
	to, toErr := sip.URI(m.Header("to"))
	from, fromErr := sip.URI(m.Header("from"))
	if toErr != nil || fromErr != nil || to != r.options.AOR || from != r.options.AOR {
		return true
	}
	if m.Status < 200 {
		r.interval = 4 * time.Second
		return true
	}
	s.cancelTimer("register_retry", 0, "")
	if m.Status == 401 || m.Status == 407 {
		if s.acceptTrunkChallenge(&r.auth, m) {
			s.sendRegistration()
		} else {
			s.registrationFailed(m.Status, "authentication_failed", 0)
		}
		return true
	}
	if m.Status == 423 && !r.closing && !r.adjusted {
		minimum, err := strconv.Atoi(m.Header("min-expires"))
		if len(m.Values("min-expires")) == 1 && err == nil && minimum > r.requested && minimum <= 86400 {
			r.requested, r.adjusted = minimum, true
			s.sendRegistration()
			return true
		}
	}
	if m.Status >= 200 && m.Status < 300 {
		expires, valid := registrationExpires(m, r.contact, r.closing)
		if !valid || r.closing && expires != 0 || !r.closing && expires == 0 {
			s.registrationFailed(502, "invalid_binding_response", 0)
			return true
		}
		if !r.closing && !r.sentAt.Add(time.Duration(expires)*time.Second).After(time.Now()) {
			s.registrationFailed(502, "expired_binding_response", 0)
			return true
		}
		r.pending, r.failures = false, 0
		r.wire = nil
		s.cancelTimer("register_expire", 0, "")
		if r.closing {
			r.expiresAt = time.Time{}
			s.publishRegistration("unregistered", m.Status, "")
			return true
		}
		r.expiresAt = r.sentAt.Add(time.Duration(expires) * time.Second)
		s.publishRegistration("registered", m.Status, "")
		// 在80%有效期刷新，最短800ms，负载与有效期有关而不受报文分片数量影响。
		s.schedule("register_next", 0, "", r.version, time.Until(r.expiresAt)*4/5)
		return true
	}
	retry, _ := strconv.Atoi(strings.TrimSpace(strings.Split(m.Header("retry-after"), ";")[0]))
	s.registrationFailed(m.Status, "registration_rejected", retry)
	return true
}

// registrationExpires 仅接受响应中属于本 Contact 的绑定，不能把其他用户的 expires 当作成功。
// 当前生成的是简单 name-addr；复杂 Contact 显示名/URI 参数组合仍按有限范围明确拒绝。
func registrationExpires(m *sip.Message, contact string, closing bool) (int, bool) {
	values := m.Values("contact")
	if closing && len(values) == 0 {
		return 0, true
	}
	found, seconds := false, 0
	for _, field := range values {
		for _, value := range strings.Split(field, ",") {
			value = strings.TrimSpace(value)
			left, right := strings.Index(value, "<"), strings.Index(value, ">")
			if left < 0 || right <= left {
				return 0, false
			}
			if value[left+1:right] != contact {
				continue
			}
			if found {
				return 0, false
			}
			found = true
			text := ""
			hasExpires := false
			for _, part := range strings.Split(value[right+1:], ";") {
				key, v, ok := strings.Cut(strings.TrimSpace(part), "=")
				if strings.EqualFold(key, "expires") {
					if !ok || hasExpires {
						return 0, false
					}
					text, hasExpires = strings.TrimSpace(v), true
				}
			}
			if !hasExpires {
				if len(m.Values("expires")) != 1 {
					return 0, false
				}
				text = m.Header("expires")
			}
			var err error
			seconds, err = strconv.Atoi(text)
			if err != nil || seconds < 0 || seconds > 86400 {
				return 0, false
			}
		}
	}
	// 注销成功回复可能只列出账户的其他绑定，本 Contact 不在其中即已删除。
	if closing && !found {
		return 0, true
	}
	return seconds, found
}

// registrationFailed 保存固定错误并有界退避；关闭中的失败立即完成排空，不重新注册。
func (s *Server) registrationFailed(status int, reason string, retryAfter int) {
	r := s.registration()
	if r == nil {
		return
	}
	r.pending, r.wire = false, nil
	s.cancelTimer("register_retry", 0, "")
	s.cancelTimer("register_expire", 0, "")
	if r.closing {
		s.publishRegistration("unregister_failed", status, reason)
		return
	}
	r.failures = min(r.failures+1, 6)
	delay := min(3600, r.options.RetrySeconds*(1<<min(r.failures-1, 5)))
	delay = max(delay, min(max(retryAfter, 0), 3600))
	// 清理失败 nonce，下一轮先取得新挑战，防止错误密码造成高频预认证循环。
	r.auth = trunkAuthentication{}
	s.publishRegistration("retrying", status, reason)
	s.schedule("register_next", 0, "", r.version, time.Duration(delay)*time.Second)
}

// registrationFlowClosed 不扫描通话；注册占用的唯一连接关闭时转换成下一轮退避。
func (s *Server) registrationFlowClosed(id uint64) {
	r := s.registration()
	if r == nil || r.flow != id {
		return
	}
	if r.closing && !r.pending {
		return
	}
	r.expiresAt = time.Time{}
	s.cancelTimer("register_next", 0, "")
	s.registrationFailed(503, "transport_closed", 0)
}

// registrationTimer 复用主循环最小堆，任何到期动作都不启动网络线程或阻塞等待响应。
func (s *Server) registrationTimer(t timerItem) bool {
	if s.registration() == nil {
		return false
	}
	if !strings.HasPrefix(t.kind, "register_") {
		return false
	}
	r := s.registration()
	if r == nil || t.version != r.version {
		return true
	}
	switch t.kind {
	case "register_next":
		if !r.closing {
			s.beginRegistration(false)
		}
	case "register_expire":
		if r.pending {
			s.registrationFailed(408, "timeout", 0)
		}
	case "register_retry":
		if r.pending && r.flow == 0 {
			s.sendFlow(r.wire, s.upstream(), 0)
			r.interval = min(4*time.Second, r.interval*2)
			s.schedule("register_retry", 0, "", r.version, r.interval)
		}
	}
	return true
}
