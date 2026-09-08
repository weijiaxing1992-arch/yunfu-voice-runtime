package server

import (
	"crypto/sha256"
	"fmt"
	"net/netip"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// handle 在唯一呼叫主循环中分派信令；先重放事务缓存，避免重传重复执行业务或消耗准入令牌。
func (s *Server) handle(d datagram) {
	m := d.message
	if m.TransportID != 0 && s.flow(m.TransportID) == nil {
		return
	}
	if m.Status > 0 {
		if s.handleRegistrationResponse(m, d.source) {
			return
		}
		s.responseB(m, d.source)
		return
	}
	if m.Method != "ACK" {
		if cached, ok := s.cache[m.Key()+"|"+transportKey(d.source, m.TransportID)]; ok {
			s.sendFlow(cached.wire, cached.source, cached.flow)
			return
		}
	}
	switch m.Method {
	case "OPTIONS":
		s.reply(d, 200, "OK", sip.Header{Name: "Allow", Value: "INVITE, ACK, CANCEL, BYE, OPTIONS, INFO"})
	case "INVITE":
		s.invite(d)
	case "ACK":
		s.ackA(d)
	case "CANCEL":
		s.cancelA(d)
	case "BYE":
		s.bye(d)
	case "INFO":
		s.info(d)
	default:
		s.reply(d, 405, "Method Not Allowed", sip.Header{Name: "Allow", Value: "INVITE, ACK, CANCEL, BYE, OPTIONS, INFO"})
	}
}

// invite 验证初始呼叫后预留媒体资源；所有峰值检查都只作用于新 INVITE，不阻塞已有通话。
func (s *Server) invite(d datagram) {
	m := d.message
	if id, ok := s.byDialog[m.CallID()]; ok {
		c := s.calls[id]
		if m.CallID() == c.AID && d.source == c.ASource && m.TransportID == c.ARequest.TransportID && m.Key() == c.ARequest.Key() {
			s.sendFlow(c.LastA, d.source, m.TransportID)
			return
		}
		if sip.Tag(m.Header("to")) != "" {
			s.reply(d, 501, "Re-INVITE Not Implemented")
		} else {
			s.reply(d, 482, "Merged Request")
		}
		return
	}
	if sip.Tag(m.Header("to")) != "" {
		s.reply(d, 481, "Call Does Not Exist")
		return
	}
	if supportedInitial(m) != nil {
		s.reply(d, 501, "Unsupported SDP or SIP Extension")
		return
	}
	hops, err := strconv.Atoi(m.Header("max-forwards"))
	if err != nil || hops < 0 {
		s.reply(d, 400, "Invalid Max-Forwards")
		return
	}
	if hops == 0 {
		s.reply(d, 483, "Too Many Hops")
		return
	}
	user, err := sip.User(m.URI)
	if err != nil {
		s.reply(d, 400, "Invalid Request URI")
		return
	}
	plan := s.dialplan.Match(s.Config.SIP.DialplanContext(), user)
	if s.dialplan != nil && plan == nil {
		s.reply(d, 404, "Not Found")
		return
	}
	local := plan != nil || slices.Contains(s.Config.SIP.LocalExtensions, user)
	if !local && s.Config.SIP.StreamOptions().UpstreamTransport != "udp" && s.flow(s.outgoingFlow()) == nil {
		s.reply(d, 503, "Upstream Transport Unavailable")
		return
	}
	contact, err := sip.URI(m.Header("contact"))
	if err != nil {
		s.reply(d, 400, "Contact Required")
		return
	}
	offer, err := sip.ParseSDP(m.Body, s.Config.Media)
	if err != nil {
		s.reply(d, 488, "Not Acceptable Here")
		return
	}
	if s.Config.Media.Processing == "g711" && offer.DTMF != nil && offer.DTMFClockRate != 8000 {
		// 两种处理拓扑都只接受G711同8k事件时钟，不能靠本地路由绕过协商限制。
		s.reply(d, 488, "Unsupported Processed Media Plan")
		return
	}
	// 退出、日志及启动硬容量仍独立生效；不在此处扣令牌，防止无可用分片时白白消耗额度。
	if s.draining.Load() || !s.journal.Healthy() || s.Stats.Active.Load() >= uint64(s.Config.Limits.MaxCalls) || len(s.calls)+len(s.cache) >= s.Config.Limits.MaxTransactions {
		s.rejectAdmission(d, "Service Unavailable", 0)
		return
	}
	worker := -1
	for i, w := range s.pool.Workers {
		if (!local || s.Config.Media.Processing != "g711" || w.SupportsLocalProcessing()) && w.Admission(time.Now()).Reason == "" && s.workerLoads[i] < w.Config.MaxCalls && (worker < 0 || s.workerLoads[i] < s.workerLoads[worker]) {
			worker = i
		}
	}
	if worker < 0 {
		s.rejectAdmission(d, "Media Capacity Unavailable", 0)
		return
	}
	// 先完成重传识别与媒体可用性检查，再原子检查并扣除一个新呼叫令牌。
	// 降低策略上限不会遍历或挂断存量；对端可按 Retry-After 发起后续新事务。
	if s.guard != nil {
		decision := s.guard.TryAdmit(s.Stats.Active.Load(), s.Stats.Established.Load(), time.Now())
		if !decision.Allowed {
			s.rejectAdmission(d, "Peak Protection", decision.RetryAfterSeconds)
			return
		}
	} else if !s.bucket.take(time.Now()) {
		// 保留未装配动态保护器的旧构造路径，避免测试或嵌入式调用绕过启动 CPS 限制。
		s.rejectAdmission(d, "Rate Limited", 1)
		return
	}
	s.nextID++
	n, _, _ := m.CSeq()
	c := &Call{BFlow: s.outgoingFlow(), ID: s.nextID, AID: m.CallID(), BID: randomID() + "@rustswitch", ARequest: m, ASource: d.source, ATag: randomID()[:16], BTag: randomID()[:16], AContact: contact, AInviteCSeq: n, ARemoteCSeq: n, BLocalCSeq: 1, BInviteCSeq: 1, Worker: worker, Generation: s.pool.Workers[worker].Generation.Load(), Offer: offer, Reserved: true, AllocatePending: true, Created: time.Now(), InviteBranch: "z9hG4bK" + randomID()}
	c.BURI = "sip:" + user + "@" + s.Config.SIP.Upstream
	c.BFrom = "<" + s.contact() + ">;tag=" + c.BTag
	c.BTo = "<" + c.BURI + ">"
	c.Local = local
	if s.Config.Media.Processing == "g711" && !local {
		// 只有桥接存在183/200两次B腿协商；本地始终沿用A腿原始Offer。
		frozen := offer
		c.ProcessedOffer = &frozen
	}
	c.Dialplan = plan
	if local {
		c.BFlow = 0
	}
	s.calls[c.ID] = c
	s.bindFlowCall(c)
	s.byDialog[c.AID] = c.ID
	if !local {
		s.byDialog[c.BID] = c.ID
	}
	s.workerLoads[worker]++
	s.Stats.Active.Add(1)
	s.Stats.Accepted.Add(1)
	s.answerA(c, 100, "Trying", nil)
	s.event(c, "call_admitted", "")
	// 先登记建立超时；同步入队拒绝会立即结束呼叫并取消它，不能在结束之后补建失效定时器。
	s.schedule("setup", c.ID, "", 0, time.Duration(s.Config.SIP.SetupTimeoutMS)*time.Millisecond)
	codec := offer.CodecMetadata()
	request := media.Request{Op: "allocate", Session: c.ID, A: &offer.Peer, Payload: offer.Payload, DTMFPayload: offer.DTMF, Codec: &codec, DTMFClockRate: offer.DTMFClockRate, DTMFEvents: offer.DTMFEvents, CNPayload: offer.CN, CNClockRate: offer.CNClockRate}
	if s.Config.Media.Processing == "g711" {
		request.Processing = media.G711ProcessingPlan()
		if local {
			request.Processing = media.G711LocalProcessingPlan()
		}
	}
	s.submit(c, "allocate", 0, request)
}

// rejectAdmission 对过载使用明确的 503 和重试建议；依旧缓存同一事务的回复，避免重传绕过限流。
func (s *Server) rejectAdmission(d datagram, reason string, retryAfter int) {
	if retryAfter < 1 {
		retryAfter = 1
		if s.guard != nil {
			retryAfter = s.guard.Snapshot(s.Stats.Active.Load(), s.Stats.Established.Load(), time.Now()).Policy.RetryAfterSeconds
		}
	}
	s.Stats.Rejected.Add(1)
	s.reply(d, 503, reason, sip.Header{Name: "Retry-After", Value: strconv.Itoa(retryAfter)})
}

// answerA 生成 A 腿响应并维护最终响应重传；已经提交的最终应答不能被后续峰值策略推迟或改写。
func (s *Server) answerA(c *Call, status int, reason string, body []byte) {
	if c.AStatus >= 200 {
		return
	}
	tag := c.ATag
	if status == 100 {
		tag = ""
	}
	contact := ""
	if status >= 180 && status < 300 {
		contact = s.flowContact(c.ARequest.TransportID)
	}
	c.LastA = sip.Response(c.ARequest, status, reason, tag, contact, c.ASource.String(), body)
	c.AStatus = status
	s.sendFlow(c.LastA, c.ASource, c.ARequest.TransportID)
	if status >= 200 {
		s.cancelTimer("setup", c.ID, "")
		c.UASPending = true
		c.UASVersion++
		c.UASInterval = 500 * time.Millisecond
		if c.ARequest.TransportID == 0 || status < 300 {
			s.schedule("uas_retry", c.ID, "", c.UASVersion, c.UASInterval)
		}
		s.schedule("uas_expire", c.ID, "", c.UASVersion, time.Duration(s.Config.SIP.AckTimeoutMS)*time.Millisecond)
		s.remember(c.ARequest.Key()+"|"+transportKey(c.ASource, c.ARequest.TransportID), c.LastA, c.ASource, c.ARequest.TransportID)
	}
}

// reject 终止已预留的失败呼叫，同时按 B 腿阶段发送 CANCEL 或 BYE 并启动资源释放。
func (s *Server) reject(c *Call, status int, reason string) {
	if c.AStatus < 200 {
		s.answerA(c, status, reason, nil)
	}
	if c.BAnswered {
		s.sendByeB(c)
	} else {
		s.sendCancelB(c)
	}
	s.finish(c, reason, true)
}

// onMedia 在主循环中合并异步媒体结果；等待中的释放保持资源预留，避免提前放开容量。
func (s *Server) onMedia(r mediaResult) {
	c, ok := s.calls[r.session]
	if !ok {
		return
	}
	switch r.operation {
	case "allocate":
		c.AllocatePending = false
		if r.err != nil {
			s.releaseReservation(c)
			if !c.Ended {
				s.reject(c, 503, "Media Allocation Failed")
			}
			return
		}
		if r.reply.Type != "allocated" || r.reply.Session != c.ID {
			s.releaseReservation(c)
			s.reject(c, 503, "Media Protocol Error")
			return
		}
		c.Allocated = true
		c.Allocation = r.reply
		if c.Ended {
			s.releaseMedia(c)
			return
		}
		if c.Dialplan != nil {
			s.startDialplan(c)
			return
		}
		if c.Local {
			// 本地路由在真实媒体分配成功后应答；等待A腿ACK后才允许read/playback，失败仍走正常回收。
			body := sip.RenderSDP(c.ID, s.Config.Media.AdvertiseIP, r.reply.ARTP, r.reply.ARTCP, c.Offer)
			s.answerA(c, 200, "OK", body)
			s.event(c, "call_answered", "")
			return
		}
		body := sip.RenderSDP(c.ID, s.Config.Media.AdvertiseIP, r.reply.BRTP, r.reply.BRTCP, c.Offer)
		if s.Config.Media.Processing == "g711" {
			body = sip.RenderProcessedOffer(c.ID, s.Config.Media.AdvertiseIP, r.reply.BRTP, r.reply.BRTCP, c.Offer)
		}
		wire := s.requestFlow(c.BFlow, "INVITE", c.BURI, c.InviteBranch, c.BFrom, c.BTo, c.BID, inviteCSeq(c), body)
		c.BInviteSent = true
		s.startTransaction(c, "INVITE", c.InviteBranch, wire, s.upstream(), c.BFlow)
	case "connect_early", "connect_answer":
		if r.version != c.ConnectInFlight {
			return
		}
		c.ConnectInFlight = 0
		if c.Ended {
			c.ConnectQueued = nil
			return
		}
		if next := c.ConnectQueued; next != nil {
			c.ConnectQueued = nil
			s.submitConnect(c, *next)
			return
		}
		if r.version != c.ConnectVersion {
			return
		}
		if r.err != nil {
			s.reject(c, 503, "Media Connect Failed")
			return
		}
		offer := c.Offer
		body := sip.RenderSDP(c.ID, s.Config.Media.AdvertiseIP, c.Allocation.ARTP, c.Allocation.ARTCP, offer)
		if r.operation == "connect_early" {
			s.answerA(c, 183, "Session Progress", body)
		} else {
			s.answerA(c, 200, "OK", body)
			s.event(c, "call_answered", "")
		}
	case "release":
		c.Releasing = false
		if r.err == nil || !s.pool.Workers[c.Worker].Healthy.Load() || s.pool.Workers[c.Worker].Generation.Load() != c.Generation {
			c.Allocated = false
			s.releaseReservation(c)
		} else {
			// 仍持有媒体所有权；退避并错开同批 BYE 重试，避免周期性调度尖峰。
			c.ReleaseRetries = min(c.ReleaseRetries+1, 5)
			delay := time.Duration(100<<min(c.ReleaseRetries-1, 4))*time.Millisecond + time.Duration(c.ID%97)*time.Millisecond
			s.schedule("release_retry", c.ID, "", 0, delay)
		}
	}
}

// responseB 校验 B 腿响应的来源、事务和对话关联，推进早期媒体、最终应答及迟到分支清理。
func (s *Server) responseB(m *sip.Message, source netip.AddrPort) {
	n, method, _ := m.CSeq()
	key := m.Branch() + "|" + method
	tx, exists := s.transactions[key]
	if exists {
		// 事务匹配同时校验远端传输地址和精确 CSeq，避免无关响应推进状态。
		if source != tx.destination || m.TransportID != tx.flow || m.CallID() != tx.callID || sip.Tag(m.Header("from")) != tx.fromTag || n != tx.cseq {
			return
		}
		if m.Status < 200 {
			tx.provisional = true
		} else {
			// 未选中分支的 BYE 完成后保留短期去重记录；关联检查必须先通过上面的来源及事务校验。
			if c := s.calls[tx.session]; c != nil {
				for _, cleanup := range c.ForkCleanups {
					if cleanup.key == key {
						cleanup.completed = true
						break
					}
				}
			}
			s.forgetFlowTransaction(key, tx.flow)
			delete(s.transactions, key)
			s.cancelTimer("out_retry", tx.session, key)
			s.cancelTimer("out_expire", tx.session, key)
		}
	}
	if method != "INVITE" || source != s.upstream() {
		return
	}
	id, ok := s.byDialog[m.CallID()]
	if !ok {
		return
	}
	c := s.calls[id]
	if s.replayInviteChallengeACK(c, m, source) {
		return
	}
	if m.TransportID != c.BFlow || m.CallID() != c.BID || m.Branch() != c.InviteBranch || n != inviteCSeq(c) || sip.Tag(m.Header("from")) != c.BTag {
		return
	}
	if m.Status < 200 {
		c.BProvisional = true
		if c.Ended || c.CancelWanted {
			s.sendCancelB(c)
			return
		}
		if c.BFinal {
			return
		}
		if m.Header("require") != "" {
			s.reject(c, 502, "Unsupported Upstream Extension")
			return
		}
		if m.Status >= 180 {
			if tag := sip.Tag(m.Header("to")); tag != "" {
				if c.BRemoteTag != "" && c.BRemoteTag != tag {
					s.reject(c, 502, "Forking Not Supported")
					return
				}
				c.BRemoteTag = tag
			}
			if len(m.Body) > 0 {
				s.connect(c, m, false)
			} else {
				s.answerA(c, m.Status, m.Reason, nil)
			}
		}
		return
	}
	if s.retryInviteAuthentication(c, m, tx) {
		return
	}
	c.BFinal = true
	if m.Status >= 300 {
		ack := s.inviteACK(c, c.BURI, c.InviteBranch, m.Header("to"))
		s.sendFlow(ack, s.upstream(), c.BFlow)
		if !c.Ended {
			status := m.Status
			reason := m.Reason
			if status < 400 {
				status = 502
				reason = "Upstream Redirect Not Supported"
			}
			s.reject(c, status, reason)
		}
		return
	}
	contact, e := sip.URI(m.Header("contact"))
	tag := sip.Tag(m.Header("to"))
	if e != nil || tag == "" {
		s.reject(c, 502, "Invalid Upstream Dialog")
		return
	}
	ack := s.inviteACK(c, contact, "z9hG4bK"+randomID(), m.Header("to"))
	s.sendFlow(ack, s.upstream(), c.BFlow)
	if c.BRemoteTag != "" && c.BRemoteTag != tag {
		// ACK 已按每份 200 发送；BYE 清理按远端分支去重，不把重传变成新的事务和定时器。
		s.cleanupUnselectedFork(c, tag, contact, m.Header("to"))
		return
	}
	if c.BAnswered {
		if c.Ended {
			s.sendByeB(c)
		}
		return
	}
	c.BAnswered = true
	c.BRemoteTag = tag
	c.BContact = contact
	c.BTo = m.Header("to")
	if c.Ended {
		s.sendByeB(c)
		return
	}
	if m.Header("record-route") != "" || m.Header("require") != "" {
		s.reject(c, 502, "Unsupported Upstream Extension")
		return
	}
	s.connect(c, m, true)
}

// maxForkCleanups 限制单通呼叫保留的未选中分支状态；本实现仍不提供完整的多目标 fork 路由。
const maxForkCleanups = 8

// cleanupUnselectedFork 在保护已选中对话的同时，尽力结束另一分支。
// 同分支重复 200 不延长去重期限、不重置 BYE 超时；超过记录上限时仅发无状态 BYE，接受其没有定时重试的明确限制。
func (s *Server) cleanupUnselectedFork(c *Call, tag, contact, to string) {
	if cleanup := c.ForkCleanups[tag]; cleanup != nil {
		if !cleanup.completed && s.transactions[cleanup.key] == nil {
			// 初次清理可能受全局事务额度限制而未登记；后续 200 仍可触发同一个 BYE 的无状态重发。
			s.sendFlow(cleanup.wire, s.upstream(), c.BFlow)
		}
		return
	}
	// 固定分支同时适用于有状态和降级清理；不同 Call-ID/远端标签不能共享一个 BYE 事务身份。
	digest := sha256.Sum256([]byte(c.BID + "\x00" + tag))
	branch := fmt.Sprintf("z9hG4bKfork-%x", digest[:16])
	wire := s.byeBRequest(c, contact, branch, to, inviteCSeq(c)+1)
	if len(c.ForkCleanups) >= maxForkCleanups {
		s.sendFlow(wire, s.upstream(), c.BFlow)
		if !c.ForkCleanupLimited {
			c.ForkCleanupLimited = true
			s.event(c, "fork_cleanup_capacity_exhausted", "stateless_cleanup_after_8_unselected_forks")
		}
		return
	}
	if c.ForkCleanups == nil {
		c.ForkCleanups = make(map[string]*forkCleanup)
	}
	key := branch + "|BYE"
	c.ForkCleanups[tag] = &forkCleanup{key: key, wire: wire}
	if s.transactions[key] == nil {
		s.startTransaction(c, "BYE", branch, wire, s.upstream(), c.BFlow)
	}
	s.schedule("fork_gc", c.ID, tag, 0, 32*time.Second)
}

// connect 校验远端 SDP 并异步更新媒体；这里只推进已接纳呼叫，不再次经过新呼叫峰值限流。
func (s *Server) connect(c *Call, m *sip.Message, answer bool) {
	if !strings.EqualFold(strings.TrimSpace(strings.Split(m.Header("content-type"), ";")[0]), "application/sdp") {
		s.reject(c, 502, "SDP Answer Required")
		return
	}
	remote, e := sip.ParseSDP(m.Body, s.Config.Media)
	if e == nil {
		if s.Config.Media.Processing == "g711" {
			var accepted sip.SDP
			original := c.Offer
			if c.ProcessedOffer != nil {
				original = *c.ProcessedOffer
			}
			accepted, remote, e = sip.NegotiateProcessedAnswer(original, remote)
			if e == nil {
				c.Offer = accepted
			}
		} else {
			remote, e = sip.NegotiateAnswer(c.Offer, remote)
		}
	}
	if e != nil {
		s.reject(c, 488, "Incompatible SDP Answer")
		return
	}
	// A 腿地址已经由 allocate 固定；只把双方确认的编码/辅助格式和接收偏好写回待答 SDP。
	aPeer := c.Offer.Peer
	if s.Config.Media.Processing != "g711" {
		c.Offer = remote
	}
	c.Offer.Peer = aPeer
	c.ConnectVersion++
	operation := "connect_early"
	if answer {
		operation = "connect_answer"
	}
	codec := remote.CodecMetadata()
	update := mediaConnectUpdate{operation: operation, version: c.ConnectVersion, request: media.Request{Op: "connect", Session: c.ID, B: &remote.Peer, Payload: remote.Payload, DTMFPayload: remote.DTMF, Codec: &codec, DTMFClockRate: remote.DTMFClockRate, DTMFEvents: remote.DTMFEvents, CNPayload: remote.CN, CNClockRate: remote.CNClockRate}}
	if c.ConnectInFlight != 0 {
		c.ConnectQueued = &update
		return
	}
	s.submitConnect(c, update)
}

// submitConnect 标记唯一在途协商后入队；同步入队失败也由 onMedia 清除标记。
func (s *Server) submitConnect(c *Call, update mediaConnectUpdate) {
	c.ConnectInFlight = update.version
	s.submit(c, update.operation, update.version, update.request)
}

// ackA 确认 A 腿最终响应；成功 ACK 立即从建立中转为已建立，并取消应答重传定时器。
func (s *Server) ackA(d datagram) {
	id, ok := s.byDialog[d.message.CallID()]
	if !ok {
		return
	}
	c := s.calls[id]
	n, _, _ := d.message.CSeq()
	if d.message.CallID() != c.AID || d.source != c.ASource || d.message.TransportID != c.ARequest.TransportID || sip.Tag(d.message.Header("from")) != sip.Tag(c.ARequest.Header("from")) || sip.Tag(d.message.Header("to")) != c.ATag || n != c.AInviteCSeq {
		return
	}
	if c.AStatus < 200 {
		return
	}
	if c.AStatus >= 300 && d.message.Branch() != c.ARequest.Branch() {
		return
	}
	if c.AAck {
		return
	}
	c.AAck = true
	s.cancelTimer("uas_retry", c.ID, "")
	s.cancelTimer("uas_expire", c.ID, "")
	c.UASPending = false
	if c.AStatus == 200 && !c.Ended {
		c.Established = true
		s.Stats.Established.Add(1)
		s.event(c, "call_established", "")
		s.schedule("max_duration", c.ID, "", 0, time.Duration(s.Config.SIP.MaxCallSeconds)*time.Second)
		s.applicationAnswered(c)
	}
}

// cancelA 只取消匹配的初始 INVITE；已发送最终应答时仅确认 CANCEL，不回退已建立对话。
func (s *Server) cancelA(d datagram) {
	id, ok := s.byDialog[d.message.CallID()]
	if !ok {
		s.reply(d, 481, "Call Does Not Exist")
		return
	}
	c := s.calls[id]
	n, _, _ := d.message.CSeq()
	if d.message.CallID() != c.AID || d.source != c.ASource || d.message.TransportID != c.ARequest.TransportID || d.message.Branch() != c.ARequest.Branch() || n != c.AInviteCSeq || d.message.Header("from") != c.ARequest.Header("from") || d.message.Header("to") != c.ARequest.Header("to") {
		s.reply(d, 481, "Call Does Not Exist")
		return
	}
	s.reply(d, 200, "OK")
	if c.AStatus >= 200 {
		return
	}
	s.answerA(c, 487, "Request Terminated", nil)
	s.sendCancelB(c)
	s.finish(c, "caller_cancelled", false)
}

// bye 验证方向、来源、标签和序号后结束双腿；清理消息不受新呼叫限额影响。
func (s *Server) bye(d datagram) {
	id, ok := s.byDialog[d.message.CallID()]
	if !ok {
		s.reply(d, 481, "Call Does Not Exist")
		return
	}
	c := s.calls[id]
	n, _, _ := d.message.CSeq()
	fromA := d.message.CallID() == c.AID
	if fromA {
		if d.source != c.ASource || d.message.TransportID != c.ARequest.TransportID || sip.Tag(d.message.Header("from")) != sip.Tag(c.ARequest.Header("from")) || sip.Tag(d.message.Header("to")) != c.ATag || n <= c.ARemoteCSeq {
			s.reply(d, 481, "Call Does Not Exist")
			return
		}
		if c.AStatus != 200 {
			s.reply(d, 481, "Dialog Not Established")
			return
		}
		c.ARemoteCSeq = n
	} else {
		if d.source != s.upstream() || d.message.TransportID != c.BFlow || sip.Tag(d.message.Header("from")) != c.BRemoteTag || sip.Tag(d.message.Header("to")) != c.BTag || n <= c.BRemoteCSeq || !c.BAnswered {
			s.reply(d, 481, "Call Does Not Exist")
			return
		}
		c.BRemoteCSeq = n
	}
	if d.message.Header("route") != "" {
		s.reply(d, 501, "Route Not Supported")
		return
	}
	s.reply(d, 200, "OK")
	if !c.Ended {
		if fromA {
			s.sendByeB(c)
		} else {
			if c.AStatus < 200 {
				s.answerA(c, 487, "Upstream Ended Call", nil)
			} else {
				s.sendByeA(c)
			}
		}
		s.finish(c, "normal_hangup", false)
	}
}

// sendCancelB 记录取消意图，并在收到临时响应后发送一次 CANCEL；迟到 200 由响应路径改用 BYE 清理。
func (s *Server) sendCancelB(c *Call) {
	if !c.BInviteSent || c.BFinal || c.BCancelSent {
		return
	}
	c.CancelWanted = true
	if !c.BProvisional {
		return
	}
	c.BCancelSent = true
	wire := s.requestFlow(c.BFlow, "CANCEL", c.BURI, c.InviteBranch, c.BFrom, "<"+c.BURI+">", c.BID, inviteCSeq(c), nil)
	s.startTransaction(c, "CANCEL", c.InviteBranch, wire, s.upstream(), c.BFlow)
}

// sendByeB 向已接通 B 腿发起可重传的 BYE；重复清理调用不会创建第二个同腿事务。
func (s *Server) sendByeB(c *Call) {
	if !c.BAnswered || c.BByeSent {
		return
	}
	c.BByeSent = true
	c.BLocalCSeq++
	branch := "z9hG4bK" + randomID()
	wire := s.byeBRequest(c, c.BContact, branch, c.BTo, c.BLocalCSeq)
	s.startTransaction(c, "BYE", branch, wire, s.upstream(), c.BFlow)
}

// sendByeA 向已经收到 200 应答的 A 腿发起 BYE，使用该方向独立递增的本地序号。
func (s *Server) sendByeA(c *Call) {
	if c.AStatus != 200 || c.AByeSent {
		return
	}
	c.AByeSent = true
	c.ALocalCSeq++
	branch := "z9hG4bK" + randomID()
	from := c.ARequest.Header("to") + ";tag=" + c.ATag
	to := c.ARequest.Header("from")
	wire := s.requestFlow(c.ARequest.TransportID, "BYE", c.AContact, branch, from, to, c.AID, c.ALocalCSeq, nil)
	s.startTransaction(c, "BYE", branch, wire, c.ASource, c.ARequest.TransportID)
}

// finish 只记录一次业务结束，并撤销长期定时器；媒体释放完成前仍保留 Active 资源计数。
func (s *Server) finish(c *Call, reason string, abnormal bool) {
	if !c.Ended {
		c.Ended = true
		s.retireCallPCM(c, false) // 先撤销授权，再提交release；撤销本身不证明Rust已经停止。
		s.retireCallRX(c, false)  // 先清本地未读音频并唤醒读者；不等待ASR或Rust退订回执。
		s.cancelCallApplications(c, reason)
		c.ConnectQueued = nil
		s.cancelTimer("setup", c.ID, "")
		s.cancelTimer("max_duration", c.ID, "")
		if c.Established {
			c.Established = false
			s.Stats.Established.Add(^uint64(0))
		}
		s.Stats.Completed.Add(1)
		if abnormal {
			s.Stats.Abnormal.Add(1)
		}
		s.event(c, "call_ended", reason)
		s.schedule("call_gc", c.ID, "", 0, 32*time.Second)
	}
	if c.Allocated {
		s.releaseMedia(c)
	} else if !c.AllocatePending {
		s.releaseReservation(c)
	}
}

// releaseMedia 合并同一通话的重复释放请求，失败后的重试由定时器触发。
func (s *Server) releaseMedia(c *Call) {
	if !c.Allocated || c.Releasing {
		return
	}
	c.Releasing = true
	s.submit(c, "release", 0, media.Request{Op: "release", Session: c.ID})
}

// releaseReservation 幂等减少分片负载和总资源预留数，保持准入计数与实际所有权一致。
func (s *Server) releaseReservation(c *Call) {
	s.retireCallPCM(c, true) // 已释放或原代次死亡，才确认不再拥有媒体TX。
	s.retireCallRX(c, true)  // 真实资源回收后退休上行清理句柄，旧身份不能接入新通话。
	// 到此处媒体已释放或原worker已终止，才允许挂机中的播放发布STOP和完成事件。
	s.applicationMediaReleased(c)
	s.cancelTimer("release_retry", c.ID, "")
	c.ReleaseRetries = 0
	if c.Reserved {
		c.Reserved = false
		s.workerLoads[c.Worker]--
		s.Stats.Active.Add(^uint64(0))
	}
}

// workerFailed 只清理失败进程这一代拥有的通话，保护其他分片及已经重启的新一代。
func (s *Server) workerFailed(f media.Failure) {
	for _, c := range s.calls {
		if c.Worker != f.Worker || c.Generation != f.Generation {
			continue
		}
		s.retireCallPCM(c, true)
		s.retireCallRX(c, true)
		c.Allocated = false
		c.AllocatePending = false
		c.Releasing = false
		s.releaseReservation(c)
		c.ConnectInFlight = 0
		c.ConnectQueued = nil
		if !c.Ended {
			if c.AStatus < 200 {
				s.answerA(c, 503, "Media Worker Failed", nil)
			} else {
				s.sendByeA(c)
			}
			// 已振铃但尚未接通的 B 腿必须取消，不能仅调用会直接返回的 sendByeB。
			if c.BAnswered {
				s.sendByeB(c)
			} else {
				s.sendCancelB(c)
			}
			s.finish(c, "media_worker_failed", true)
		}
	}
}

// startTransaction 登记出站请求及其重传/到期时间；事务容量独立于动态新呼叫峰值限额。
func (s *Server) startTransaction(c *Call, method, branch string, wire []byte, destination netip.AddrPort, flows ...uint64) {
	var flow uint64
	if len(flows) > 0 {
		flow = flows[0]
	}
	if len(s.transactions) >= s.Config.Limits.MaxTransactions {
		s.sendFlow(wire, destination, flow)
		s.event(c, "transaction_capacity_exhausted", method)
		return
	}
	key := branch + "|" + method
	s.nextVersion++
	version := s.nextVersion
	parsed, err := sip.Parse(wire)
	if err != nil {
		s.event(c, "internal_message_error", err.Error())
		return
	}
	n, _, _ := parsed.CSeq()
	tx := &transaction{flow: flow, wire: wire, destination: destination, session: c.ID, method: method, callID: parsed.CallID(), fromTag: sip.Tag(parsed.Header("from")), cseq: n, interval: 500 * time.Millisecond, version: version}
	s.transactions[key] = tx
	if refs := s.flowRefs(flow, true); refs != nil {
		refs.transactions[key] = struct{}{}
	}
	s.sendFlow(wire, destination, flow)
	if flow == 0 {
		s.schedule("out_retry", c.ID, key, version, tx.interval)
	}
	s.schedule("out_expire", c.ID, key, version, 32*time.Second)
}

// onTimer 处理匹配版本的到期动作，覆盖信令重传、建立超时、最大时长和资源回收。
func (s *Server) onTimer(t timerItem) {
	if s.registrationTimer(t) {
		return
	}
	if t.kind == "cache" {
		if value, ok := s.cache[t.key]; ok && value.version == t.version {
			if refs := s.flowRefs(value.flow, false); refs != nil {
				delete(refs.cache, t.key)
				s.pruneFlowRefs(value.flow)
			}
			delete(s.cache, t.key)
		}
		return
	}
	if t.kind == "out_retry" || t.kind == "out_expire" {
		tx, ok := s.transactions[t.key]
		if !ok || tx.version != t.version {
			return
		}
		if t.kind == "out_expire" {
			s.forgetFlowTransaction(t.key, tx.flow)
			delete(s.transactions, t.key)
			s.cancelTimer("out_retry", tx.session, t.key)
			if c := s.calls[tx.session]; c != nil && tx.method == "INVITE" && !c.Ended {
				s.reject(c, 408, "Upstream Timeout")
			}
			return
		}
		if tx.flow != 0 || tx.method == "INVITE" && tx.provisional {
			return
		}
		s.sendFlow(tx.wire, tx.destination, tx.flow)
		tx.interval *= 2
		if tx.method != "INVITE" && tx.interval > 4*time.Second {
			tx.interval = 4 * time.Second
		}
		s.schedule("out_retry", tx.session, t.key, tx.version, tx.interval)
		return
	}
	c, ok := s.calls[t.id]
	if !ok {
		return
	}
	switch t.kind {
	case "setup":
		if !c.Ended && c.AStatus < 200 {
			s.reject(c, 408, "Setup Timeout")
		}
	case "uas_retry":
		if c.UASVersion == t.version && !c.AAck && c.AStatus >= 200 {
			s.sendFlow(c.LastA, c.ASource, c.ARequest.TransportID)
			c.UASInterval = min(4*time.Second, c.UASInterval*2)
			s.schedule("uas_retry", c.ID, "", c.UASVersion, c.UASInterval)
		}
	case "uas_expire":
		if c.UASVersion == t.version && !c.AAck {
			c.UASPending = false
			s.cancelTimer("uas_retry", c.ID, "")
			c.UASVersion++
			if c.AStatus == 200 && !c.Ended {
				// 2xx已建立A侧对话；ACK超时也须用BYE通知该端，不能只释放本地资源留下对端悬挂。
				// RFC 3261 §13.3.1.4；沿用既有独立CSeq/事务重传与同腿去重。
				s.sendByeA(c)
				s.sendByeB(c)
				s.finish(c, "ack_timeout", true)
			}
		}
	case "max_duration":
		if !c.Ended {
			s.sendByeA(c)
			s.sendByeB(c)
			s.finish(c, "max_call_duration", false)
		}
	case "release_retry":
		if c.Ended {
			s.releaseMedia(c)
		}
	case "call_gc":
		if c.Reserved || c.AllocatePending || c.Releasing || c.UASPending {
			s.schedule("call_gc", c.ID, "", 0, time.Second)
			return
		}
		// 业务对象移除时同时撤销分支去重定时器；尚在途的 BYE 仍由独立事务的原有到期边界收敛。
		for tag := range c.ForkCleanups {
			s.cancelTimer("fork_gc", c.ID, tag)
		}
		c.ForkCleanups = nil
		delete(s.byDialog, c.AID)
		delete(s.byDialog, c.BID)
		s.forgetFlowCall(c)
		delete(s.calls, c.ID)
	case "fork_gc":
		delete(c.ForkCleanups, t.key)
		if len(c.ForkCleanups) == 0 {
			c.ForkCleanups = nil
		}
	}
}

// String 返回不包含用户号码及 SDP 的简要对象标识，供内部诊断使用。
func (c *Call) String() string { return fmt.Sprintf("call %d on worker %d", c.ID, c.Worker) }
