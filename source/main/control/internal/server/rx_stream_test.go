package server

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"rustswitch/control/internal/esl"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
)

// 夹具只证明Server授权/竞态，不打开SIP或RTP网络，真实媒体证据必须另跑端到端用例。
type rxSDKFakeSub struct {
	session, id  uint64
	mu           sync.Mutex
	state        string
	revoked      chan struct{}
	once         sync.Once
	retired      atomic.Bool
	reads        atomic.Uint64
	controls     atomic.Uint64
	frames       chan media.RXFrame
	entered      chan struct{}
	bypassRevoke bool
}

func newRXSDKFakeSub(session, id uint64) *rxSDKFakeSub {
	return &rxSDKFakeSub{session: session, id: id, state: "active", revoked: make(chan struct{}), frames: make(chan media.RXFrame, 8), entered: make(chan struct{}, 1)}
}
func (f *rxSDKFakeSub) reply() media.Reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return media.Reply{OK: true, Type: "rx_state", Session: f.session, SubscriptionID: f.id, State: f.state}
}
func (f *rxSDKFakeSub) Read(ctx context.Context) (media.RXFrame, error) {
	f.reads.Add(1)
	select {
	case f.entered <- struct{}{}:
	default:
	}
	if f.bypassRevoke {
		select {
		case x := <-f.frames:
			return x, nil
		case <-ctx.Done():
			return media.RXFrame{}, ctx.Err()
		}
	}
	select {
	case <-f.revoked:
		return media.RXFrame{}, media.ErrRXRetired
	case x := <-f.frames:
		return x, nil
	case <-ctx.Done():
		return media.RXFrame{}, ctx.Err()
	}
}
func (f *rxSDKFakeSub) Status(context.Context) (media.Reply, error) {
	f.controls.Add(1)
	return f.reply(), nil
}
func (f *rxSDKFakeSub) Unsubscribe(context.Context) (media.Reply, error) {
	f.controls.Add(1)
	f.mu.Lock()
	f.state = "stopped"
	f.mu.Unlock()
	return f.reply(), nil
}
func (f *rxSDKFakeSub) Snapshot() media.RXSnapshot {
	return media.RXSnapshot{Session: f.session, SubscriptionID: f.id, Retired: f.retired.Load()}
}
func (f *rxSDKFakeSub) Retired() bool { return f.retired.Load() }
func (f *rxSDKFakeSub) RevokeRead()   { f.once.Do(func() { close(f.revoked) }) }

type rxSDKAttempt struct {
	sub      *rxSDKFakeSub
	complete chan rxSubscribeResult
}
type rxSDKFake struct {
	entered chan rxSDKAttempt
	calls   atomic.Uint64
}

func (f *rxSDKFake) subscribe(ctx context.Context, _ int, _ uint64, session, id uint64) (rxSubscription, media.Reply, error) {
	f.calls.Add(1)
	sub := newRXSDKFakeSub(session, id)
	a := rxSDKAttempt{sub: sub, complete: make(chan rxSubscribeResult, 1)}
	select {
	case f.entered <- a:
	case <-ctx.Done():
		return nil, media.Reply{}, ctx.Err()
	}
	select {
	case r := <-a.complete:
		return sub, r.reply, r.err
	case <-ctx.Done():
		return sub, media.Reply{}, media.ErrRXOutcomeUnknown
	}
}
func rxSDKFixture(t *testing.T) (*Server, *Call, *rxSDKFake, *media.CapabilitySnapshot) {
	t.Helper()
	s, c, _, _ := pcmSDKFixture(t)
	p := &media.CapabilitySnapshot{WorkerID: 0, Generation: 1, PID: 123, Healthy: true, Capabilities: []string{"processed_g711_v1", "processed_g711_local_v1", "rx_g711_local_v2"}}
	f := &rxSDKFake{entered: make(chan rxSDKAttempt, rxSubscribeLimit)}
	e := &rxService{ctx: s.ctx, transport: f, requests: make(chan *rxSubscribeRequest, rxSubscribeLimit), jobs: make(chan rxSubscribeJob, rxSubscribeLimit), results: make(chan rxSubscribeResult, rxSubscribeLimit), workerSnapshot: func(int) (media.CapabilitySnapshot, bool) { return *p, true }}
	s.rx = e
	e.startWorkers()
	t.Cleanup(func() {
		s.cancel()
		done := make(chan struct{})
		go func() { e.workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("固定RX控制协程未退出")
		}
	})
	return s, c, f, p
}
func rxSDKRequest(c *Call, id uint64) *rxSubscribeRequest {
	return &rxSubscribeRequest{ctx: context.Background(), uuid: c.compatUUIDs[0], subscriptionID: id, reply: make(chan rxSubscribeResult, 1)}
}
func rxSDKAttemptWait(t *testing.T, f *rxSDKFake) rxSDKAttempt {
	t.Helper()
	select {
	case a := <-f.entered:
		return a
	case <-time.After(time.Second):
		t.Fatal("订阅未进入固定执行worker")
		return rxSDKAttempt{}
	}
}
func rxSDKProcess(t *testing.T, s *Server) {
	t.Helper()
	select {
	case r := <-s.rx.results:
		s.handleRXSubscribeResult(r)
	case <-time.After(time.Second):
		t.Fatal("没有订阅控制结果")
	}
}
func rxSDKBegin(t *testing.T, s *Server, c *Call, f *rxSDKFake, id uint64) (*RXHandle, *rxSDKFakeSub) {
	t.Helper()
	r := rxSDKRequest(c, id)
	s.handleRXSubscribe(r)
	a := rxSDKAttemptWait(t, f)
	a.complete <- rxSubscribeResult{reply: a.sub.reply()}
	rxSDKProcess(t, s)
	x := <-r.reply
	if x.err != nil || x.handle == nil {
		t.Fatalf("订阅授权失败：%+v", x)
	}
	return x.handle, a.sub
}

// 构造当前公开OS时钟的真实有限期限，避免通过关闭CheckFresh掩盖Server交付期限。
func rxSDKFrame(t *testing.T, expired bool) media.RXFrame {
	t.Helper()
	clock, domain := int32(1), uint16(1) // Linux CLOCK_MONOTONIC。
	if runtime.GOOS == "darwin" {
		clock, domain = 8, 2
	} // Darwin公开CLOCK_UPTIME_RAW常量为8，与Rust Instant相同。
	var ts unix.Timespec
	if err := unix.ClockGettime(clock, &ts); err != nil {
		t.Fatal(err)
	}
	now := uint64(ts.Sec)*1_000_000_000 + uint64(ts.Nsec)
	if expired {
		now -= 200_000_000
	}
	f := media.RXFrame{Kind: media.RXDecoded, ClockDomain: domain, ObservationLowerBoundNS: now, ExpiresAtNS: now + 100_000_000, SampleCount: 160, Session: 1, SubscriptionID: 1}
	for i := range f.PCM {
		f.PCM[i] = int16(i*7 - 400)
	}
	return f
}

func TestRXSDKRejectsNonACKLocalCodecAndWorkerIdentity(t *testing.T) {
	cases := map[string]func(*Server, *Call, *media.CapabilitySnapshot){
		"no_ack":          func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.AAck = false },
		"not_established": func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Established = false },
		"bridge":          func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Local = false },
		"unallocated":     func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Allocated = false },
		"releasing":       func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Releasing = true },
		"ended":           func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Ended = true },
		"relay":           func(s *Server, _ *Call, _ *media.CapabilitySnapshot) { s.Config.Media.Processing = "relay" },
		"topology":        func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Allocation.ProcessingTopology = "bridge" },
		"version_missing": func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Allocation.ProcessingVersion = nil },
		"version_wrong": func(_ *Server, c *Call, _ *media.CapabilitySnapshot) {
			v := uint32(2)
			c.Allocation.ProcessingVersion = &v
		},
		"codec":           func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.Name = "G722" },
		"rate":            func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.SampleRate = 16000 },
		"clock":           func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.RTPClockRate = 48000 },
		"channels":        func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.Channels = 2 },
		"frame":           func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.PTimeMS = 40 },
		"sdp_frame":       func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.PTime = 40 },
		"healthy":         func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.Healthy = false },
		"pid":             func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.PID = 0 },
		"worker":          func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.WorkerID = 1 },
		"generation":      func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.Generation = 2 },
		"generation_zero": func(_ *Server, c *Call, p *media.CapabilitySnapshot) { c.Generation = 0; p.Generation = 0 },
		"no_rx":           func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.Capabilities = p.Capabilities[:2] },
		"old_rx":          func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.Capabilities[2] = "rx_g711_local_v1" },
		"no_local": func(_ *Server, _ *Call, p *media.CapabilitySnapshot) {
			p.Capabilities = []string{"processed_g711_v1", "rx_g711_local_v2"}
		},
		"b_leg": func(s *Server, c *Call, _ *media.CapabilitySnapshot) {
			s.compatChannels[c.compatUUIDs[0]] = compatibilityChannel{call: c, side: 1}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, c, f, p := rxSDKFixture(t)
			change(s, c, p)
			r := rxSDKRequest(c, 1)
			s.handleRXSubscribe(r)
			x := <-r.reply
			if !errors.Is(x.err, ErrRXNotEligible) || f.calls.Load() != 0 || s.rx.pending != 0 || c.rxPending != nil {
				t.Fatalf("非法资格产生了媒体副作用：%+v", x)
			}
		})
	}
}

func TestRXSDKWrongACKCannotAuthorizeButExactACKCan(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	c.AAck = false
	c.Established = false
	c.AID = "rx-ack"
	c.ATag = "answer"
	c.AStatus = 200
	c.AInviteCSeq = 7
	c.ASource = netip.MustParseAddrPort("127.0.0.1:2222")
	invite, _ := sip.Parse(sip.Request("INVITE", "sip:1000@local", c.ASource.String(), "z9hG4bK-invite", "<sip:a@local>;tag=caller", "<sip:1000@local>", c.AID, 7, nil))
	c.ARequest = invite
	s.byDialog = map[string]uint64{c.AID: c.ID}
	for _, which := range []string{"source", "from_tag", "to_tag", "cseq", "flow"} {
		from, to, seq := invite.Header("from"), "<sip:1000@local>;tag=answer", uint32(7)
		source := c.ASource
		switch which {
		case "source":
			source = netip.MustParseAddrPort("127.0.0.1:2223")
		case "from_tag":
			from = "<sip:a@local>;tag=other"
		case "to_tag":
			to = "<sip:1000@local>;tag=other"
		case "cseq":
			seq = 8
		}
		ack, _ := sip.Parse(sip.Request("ACK", "sip:1000@local", c.ASource.String(), "z9hG4bK-ack", from, to, c.AID, seq, nil))
		if which == "flow" {
			ack.TransportID = 9
		}
		s.ackA(datagram{message: ack, source: source})
		r := rxSDKRequest(c, 1)
		s.handleRXSubscribe(r)
		if x := <-r.reply; !errors.Is(x.err, ErrRXNotEligible) || c.AAck || c.Established {
			t.Fatalf("错误ACK(%s)获得授权：%+v", which, x)
		}
	}
	j, err := journal.Open(filepath.Join(t.TempDir(), "events.jsonl"), 16)
	if err != nil {
		t.Fatal(err)
	}
	s.journal = j
	t.Cleanup(func() { j.Close() })
	ack, _ := sip.Parse(sip.Request("ACK", "sip:1000@local", c.ASource.String(), "z9hG4bK-ack", invite.Header("from"), "<sip:1000@local>;tag=answer", c.AID, 7, nil))
	s.ackA(datagram{message: ack, source: c.ASource})
	h, _ := rxSDKBegin(t, s, c, f, 1)
	if !c.AAck || !c.Established || !h.authorized.Load() {
		t.Fatal("正确ACK未授权")
	}
}

func TestRXSDKReadIndependentOfTXAndCallMap(t *testing.T) {
	for _, app := range []string{"park", "read", "playback"} {
		t.Run(app, func(t *testing.T) {
			s, c, f, _ := rxSDKFixture(t)
			if app == "playback" {
				j, _ := parseApplication(esl.ExecuteRequest{Application: "playback", Arguments: "tone_stream://%(1000,0,440)"})
				j.playbackState = "running"
				s.applications.lanes[c.compatUUIDs[0]] = &applicationLane{call: c, uuid: c.compatUUIDs[0], active: j, heapIndex: -1}
			} else {
				args := ""
				if app == "read" {
					args = "1 4 silence digits 2000 #"
				}
				if got := applicationAdmit(s, c.compatUUIDs[0], app, args, true); got != "+OK" {
					t.Fatal(got)
				}
			}
			c.pcmHandle = &PCMHandle{service: s.ctx}
			c.pcmPending = &PCMHandle{service: s.ctx}
			before := c.pcmHandle
			h, sub := rxSDKBegin(t, s, c, f, 1)
			delete(s.calls, c.ID)
			delete(s.compatChannels, c.compatUUIDs[0])
			frame := rxSDKFrame(t, false)
			sub.frames <- frame
			got, err := h.Read(context.Background())
			if err != nil || got.PCM != frame.PCM || c.pcmHandle != before || !s.pcmOwnsTX(c) {
				t.Fatalf("RX读取依赖TX或全局映射：%v", err)
			}
			if h.UUID() != c.compatUUIDs[0] || h.SubscriptionID() != 1 || h.Snapshot().Session != c.ID {
				t.Fatal("冻结身份错误")
			}
		})
	}
}

func TestRXSDKSameIDNeverRevivesAndHigherIDNeedsTerminal(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	h, _ := rxSDKBegin(t, s, c, f, 1)
	r := rxSDKRequest(c, 1)
	s.handleRXSubscribe(r)
	if x := <-r.reply; x.handle != h || x.err != nil || f.calls.Load() != 1 {
		t.Fatal("同ID不幂等", x)
	}
	r = rxSDKRequest(c, 2)
	s.handleRXSubscribe(r)
	if x := <-r.reply; !errors.Is(x.err, ErrRXStaleSubscription) {
		t.Fatal("活动订阅被替换", x)
	}
	if _, err := h.Unsubscribe(context.Background()); err != nil {
		t.Fatal(err)
	}
	r = rxSDKRequest(c, 1)
	s.handleRXSubscribe(r)
	x := <-r.reply
	if x.handle != h || x.reply.State != "stopped" || f.calls.Load() != 1 {
		t.Fatal("同ID改变终态", x)
	}
	if _, err := h.Read(context.Background()); !errors.Is(err, ErrRXHandleClosed) {
		t.Fatal("同ID复活读权", err)
	}
	next, _ := rxSDKBegin(t, s, c, f, 2)
	if next == h || !h.Retired() {
		t.Fatal("换ID未退休旧句柄")
	}
}

func TestRXSDKCancellationBeforePublicationHasNoMediaJob(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan rxSubscribeResult, 1)
	go func() {
		h, r, err := s.SubscribeRX(ctx, c.compatUUIDs[0], 1)
		done <- rxSubscribeResult{handle: h, reply: r, err: err}
	}()
	r := <-s.rx.requests
	cancel()
	x := <-done
	s.handleRXSubscribe(r)
	if x.handle != nil || !errors.Is(x.err, context.Canceled) || f.calls.Load() != 0 || s.rx.pending != 0 {
		t.Fatal("取消后才提交", x)
	}
}

func TestRXSDKUnknownCancellationRetainsClosedCleanupHandle(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan rxSubscribeResult, 1)
	go func() {
		h, r, err := s.SubscribeRX(ctx, c.compatUUIDs[0], 1)
		done <- rxSubscribeResult{handle: h, reply: r, err: err}
	}()
	r := <-s.rx.requests
	s.handleRXSubscribe(r)
	a := rxSDKAttemptWait(t, f)
	cancel()
	x := <-done
	if x.handle == nil || !errors.Is(x.err, ErrRXSubscribeUnknown) {
		t.Fatal("未知结果丢失清理句柄", x)
	}
	rxSDKProcess(t, s)
	retry := rxSDKRequest(c, 1)
	s.handleRXSubscribe(retry)
	again := <-retry.reply
	if again.handle != x.handle || !errors.Is(again.err, ErrRXSubscribeUnknown) || f.calls.Load() != 1 {
		t.Fatal("未知结果重试创建新流", again)
	}
	if _, err := again.handle.Read(context.Background()); !errors.Is(err, ErrRXHandleClosed) {
		t.Fatal("未知订阅仍可读取", err)
	}
	select {
	case <-a.sub.revoked:
	default:
		t.Fatal("迟到绑定未补撤销")
	}
	if reply, err := again.handle.Status(context.Background()); err != nil || reply.State != "active" {
		t.Fatal("丢失真实状态查询权", reply, err)
	}
	if reply, err := again.handle.Unsubscribe(context.Background()); err != nil || reply.State != "stopped" {
		t.Fatal("丢失退订权", reply, err)
	}
}

func TestRXSDKPendingACKRecheckAndHangupCannotRevive(t *testing.T) {
	for _, kind := range []string{"ended", "generation", "ack", "released"} {
		t.Run(kind, func(t *testing.T) {
			s, c, f, p := rxSDKFixture(t)
			r := rxSDKRequest(c, 1)
			s.handleRXSubscribe(r)
			a := rxSDKAttemptWait(t, f)
			switch kind {
			case "ended":
				c.Ended = true
				s.retireCallRX(c, false)
			case "generation":
				p.Generation++
			case "ack":
				c.AAck = false
			case "released":
				c.Allocated = false
				s.retireCallRX(c, true)
			}
			a.complete <- rxSubscribeResult{reply: a.sub.reply()}
			rxSDKProcess(t, s)
			x := <-r.reply
			if x.handle == nil || x.handle.authorized.Load() || (!errors.Is(x.err, ErrRXHandleClosed) && !errors.Is(x.err, ErrRXHandleRetired)) {
				t.Fatal("迟到回执复活通话", x)
			}
			select {
			case <-a.sub.revoked:
			default:
				t.Fatal("旧队列未撤销")
			}
			if s.rx.pending != 0 || c.rxPending != nil {
				t.Fatal("在途额度泄漏")
			}
		})
	}
}

func TestRXSDKRevokeWakesReaderAndRetainsActualCleanup(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	h, sub := rxSDKBegin(t, s, c, f, 1)
	done := make(chan error, 1)
	go func() { _, err := h.Read(context.Background()); done <- err }()
	<-sub.entered
	s.retireCallRX(c, false)
	select {
	case err := <-done:
		if !errors.Is(err, ErrRXHandleClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("挂断未唤醒Read")
	}
	if h.Retired() || h.terminal.Load() {
		t.Fatal("撤读权伪造远端释放")
	}
	if r, err := h.Status(context.Background()); err != nil || r.State != "active" {
		t.Fatal("结束后无法对账", r, err)
	}
	s.retireCallRX(c, true)
	if !h.Retired() {
		t.Fatal("真实释放未退休")
	}
	if _, err := h.Status(context.Background()); !errors.Is(err, ErrRXHandleRetired) {
		t.Fatal("释放后身份仍可用", err)
	}
}

func TestRXSDKLateReadAndExpiredFrameRejectedAtServerBoundary(t *testing.T) {
	t.Run("revoke_after_sdk_dequeue", func(t *testing.T) {
		s, c, f, _ := rxSDKFixture(t)
		h, sub := rxSDKBegin(t, s, c, f, 1)
		sub.bypassRevoke = true
		done := make(chan error, 1)
		go func() { _, err := h.Read(context.Background()); done <- err }()
		<-sub.entered
		s.retireCallRX(c, false)
		sub.frames <- rxSDKFrame(t, false)
		if err := <-done; !errors.Is(err, ErrRXHandleClosed) {
			t.Fatal("迟到帧穿越挂断", err)
		}
	})
	t.Run("sdk_check_is_not_server_check", func(t *testing.T) {
		s, c, f, _ := rxSDKFixture(t)
		h, sub := rxSDKBegin(t, s, c, f, 1)
		sub.frames <- rxSDKFrame(t, true)
		if _, err := h.Read(context.Background()); !errors.Is(err, media.ErrRXExpired) {
			t.Fatal("Server未复核不可续期期限", err)
		}
		if !h.readClosed.Load() || h.terminal.Load() {
			t.Fatal("过期未封闭或伪造远端终态")
		}
	})
}

func TestRXSDKWrongSuccessfulReplyNeverAuthorizes(t *testing.T) {
	for _, kind := range []string{"session", "subscription", "type", "state"} {
		t.Run(kind, func(t *testing.T) {
			s, c, f, _ := rxSDKFixture(t)
			r := rxSDKRequest(c, 1)
			s.handleRXSubscribe(r)
			a := rxSDKAttemptWait(t, f)
			reply := a.sub.reply()
			switch kind {
			case "session":
				reply.Session++
			case "subscription":
				reply.SubscriptionID++
			case "type":
				reply.Type = "allocated"
			case "state":
				reply.State = "playing"
			}
			a.complete <- rxSubscribeResult{reply: reply}
			rxSDKProcess(t, s)
			x := <-r.reply
			if x.handle == nil || x.handle.authorized.Load() || !errors.Is(x.err, ErrRXSubscribeUnknown) {
				t.Fatal("错误流成功回执授权", x)
			}
		})
	}
}

func TestRXSDKQueueAndInflightBudgetsAreBounded(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	for range rxSubscribeLimit {
		s.rx.requests <- rxSDKRequest(c, 1)
	}
	if _, _, err := s.SubscribeRX(context.Background(), c.compatUUIDs[0], 1); !errors.Is(err, ErrRXHandleBusy) {
		t.Fatal("授权请求队列无界", err)
	}
	s.rx.pending = rxSubscribeLimit
	r := rxSDKRequest(c, 1)
	s.handleRXSubscribe(r)
	if x := <-r.reply; !errors.Is(x.err, ErrRXHandleBusy) || f.calls.Load() != 0 || c.rxPending != nil {
		t.Fatal("在途上限失效", x)
	}
	s.rx.pending = 0
	if _, _, err := s.SubscribeRX(context.Background(), c.compatUUIDs[0], 0); !errors.Is(err, ErrRXInvalid) {
		t.Fatal("零订阅ID受理", err)
	}
}

func TestRXSDKCloseRetiresHandlesAndJoinsFixedWorkers(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	h, _ := rxSDKBegin(t, s, c, f, 1)
	s.closeRX()
	s.cancel()
	s.rx.workers.Wait()
	if !h.Retired() {
		t.Fatal("关闭后句柄未退休")
	}
	if _, _, err := s.SubscribeRX(context.Background(), c.compatUUIDs[0], 2); !errors.Is(err, ErrRXHandleRetired) {
		t.Fatal("关闭后仍受理", err)
	}
}

func TestRXSDKWorkerFailureOnlyRetiresMatchingGeneration(t *testing.T) {
	s, c, f, p := rxSDKFixture(t)
	first, _ := rxSDKBegin(t, s, c, f, 1)
	other := *c
	other.ID = 2
	other.Generation = 2
	other.compatUUIDs = [2]string{"rx-second-generation", ""}
	other.rxHandle = nil
	other.rxPending = nil
	s.calls[other.ID] = &other
	s.compatChannels[other.compatUUIDs[0]] = compatibilityChannel{call: &other, side: 0}
	p.Generation = 2
	second, _ := rxSDKBegin(t, s, &other, f, 1)
	c.Ended = true // 已结束但尚占资源，检验原代次死亡清理；不触发真实网络BYE。
	s.workerFailed(media.Failure{Worker: 0, Generation: 1})
	if !first.Retired() || second.Retired() || !other.Allocated || c.Allocated {
		t.Fatal("worker失联跨代清理或遗漏旧代次")
	}
}

func TestRXSDKCancelAfterDeliveryClaimStillClosesPublishedRead(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	h, _ := rxSDKBegin(t, s, c, f, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan rxSubscribeResult, 1)
	go func() {
		handle, reply, err := s.SubscribeRX(ctx, c.compatUUIDs[0], 1)
		done <- rxSubscribeResult{handle: handle, reply: reply, err: err}
	}()
	r := <-s.rx.requests
	r.published.Store(h)
	if !r.delivery.CompareAndSwap(0, 2) {
		t.Fatal("无法构造已认领交付")
	}
	cancel()
	r.reply <- rxSubscribeResult{handle: h, reply: *h.last.Load()}
	x := <-done
	if x.handle != h || !errors.Is(x.err, ErrRXSubscribeUnknown) || !h.readClosed.Load() || h.Retired() {
		t.Fatal("交付取消竞态丢失清理权或保留可读权", x)
	}
	if _, err := h.Unsubscribe(context.Background()); err != nil {
		t.Fatal("取消后无法实际退订", err)
	}
}

func TestRXSDKBlockedControlUsesOnlyFixedWorkersAndPreservesBudget(t *testing.T) {
	s, c, f, _ := rxSDKFixture(t)
	for i := 0; i < rxSubscribeLimit; i++ {
		call := *c
		call.ID = uint64(i + 10)
		call.rxHandle = nil
		call.rxPending = nil
		call.compatUUIDs = [2]string{compatibilityUUID(), ""}
		s.calls[call.ID] = &call
		s.compatChannels[call.compatUUIDs[0]] = compatibilityChannel{call: &call, side: 0}
		s.handleRXSubscribe(rxSDKRequest(&call, 1))
	}
	for range rxControlWorkers {
		rxSDKAttemptWait(t, f)
	}
	if f.calls.Load() != rxControlWorkers || s.rx.pending != rxSubscribeLimit || len(s.rx.jobs) != rxSubscribeLimit-rxControlWorkers {
		t.Fatal("控制慢请求增加协程或突破固定预算", f.calls.Load(), s.rx.pending, len(s.rx.jobs))
	}
	r := rxSDKRequest(c, 1)
	s.handleRXSubscribe(r)
	if x := <-r.reply; !errors.Is(x.err, ErrRXHandleBusy) {
		t.Fatal("第65个在途被接受", x)
	}
	// 后台RPC仍阻塞时，主循环已经完成所有授权/拒绝；服务取消必须唤醒四个执行者。
	s.cancel()
	s.rx.workers.Wait()
}
