package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"rustswitch/control/internal/esl"
	"rustswitch/control/internal/media"
)

// pcmSDKFake仅是并发与生命周期故障夹具，不打开媒体socket，也不算实际RTP证据。
type pcmSDKFake struct {
	mu          sync.Mutex
	commands    []media.Request
	pending     []pcmSDKSubmission
	pushes      int
	pushEntered chan struct{}
	pushRelease chan struct{}
	pushErr     error
	callErr     error
	callReject  bool
	callState   string
	submitErr   error
}
type pcmSDKSubmission struct {
	request  media.Request
	callback func(media.Reply, error)
}

func pcmSDKReply(r media.Request, state string) media.Reply {
	return media.Reply{OK: true, Type: "pcm_turn_state", Session: r.Session, TurnID: r.TurnID, State: state, BufferMS: 200, PrebufferMS: 40}
}
func (f *pcmSDKFake) Call(ctx context.Context, _ int, r media.Request) (media.Reply, error) {
	f.mu.Lock()
	f.commands = append(f.commands, r)
	state, err, reject := f.callState, f.callErr, f.callReject
	f.mu.Unlock()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if reject {
		return media.Reply{Type: "error", Message: "PCM final sample offset mismatch"}, err
	}
	if state == "" {
		state = "playing"
	}
	return pcmSDKReply(r, state), err
}
func (f *pcmSDKFake) Submit(_ context.Context, _ int, r media.Request, cb func(media.Reply, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submitErr != nil {
		return f.submitErr
	}
	f.commands = append(f.commands, r)
	f.pending = append(f.pending, pcmSDKSubmission{r, cb})
	return nil
}
func (f *pcmSDKFake) PushPCM(ctx context.Context, _ int, _, session, turnID, offset uint64, samples []int16) (media.PCMReply, error) {
	f.mu.Lock()
	f.pushes++
	entered, release, err := f.pushEntered, f.pushRelease, f.pushErr
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return media.PCMReply{}, media.ErrPCMOutcomeUnknown
		}
	}
	return media.PCMReply{Session: session, TurnID: turnID, CurrentTurnID: turnID, State: "playing", AcceptedSamples: offset + uint64(len(samples))}, err
}
func (f *pcmSDKFake) complete(t *testing.T, state string, err error) media.Request {
	t.Helper()
	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		t.Fatal("没有已提交的媒体操作")
		return media.Request{}
	}
	x := f.pending[0]
	f.pending = f.pending[1:]
	f.mu.Unlock()
	reply := pcmSDKReply(x.request, state)
	if err != nil {
		reply = media.Reply{Type: "error", Message: err.Error()}
	}
	x.callback(reply, err)
	return x.request
}

// completeUnknown模拟已提交但丢失完整回执；它和上面的明确type:error业务拒绝是两种不同证据。
func (f *pcmSDKFake) completeUnknown(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		t.Fatal("没有在途begin")
		return
	}
	x := f.pending[0]
	f.pending = f.pending[1:]
	f.mu.Unlock()
	x.callback(media.Reply{}, media.ErrPCMOutcomeUnknown)
}

func pcmSDKFixture(t *testing.T) (*Server, *Call, *pcmSDKFake, *media.CapabilitySnapshot) {
	t.Helper()
	s, c, _ := applicationFixture(t)
	c.Local, c.AAck = true, true
	c.Offer.PTime = 20
	c.Offer.Codec = media.CodecSpec{Name: "PCMU", SampleRate: 8000, RTPClockRate: 8000, Channels: 1, PTimeMS: 20}
	version := uint32(1)
	c.Allocation = media.Reply{ProcessingTopology: "local", ProcessingVersion: &version}
	s.Config.Media.Processing = "g711"
	f := &pcmSDKFake{}
	proof := &media.CapabilitySnapshot{WorkerID: 0, Generation: 1, PID: 123, Healthy: true, Capabilities: []string{"processed_g711_v1", "processed_g711_local_v1", "pcm_turn_v1"}}
	s.pcm = &pcmService{ctx: s.ctx, transport: f, requests: make(chan *pcmBeginRequest, pcmBeginLimit), results: make(chan pcmBeginResult, pcmBeginLimit), workerSnapshot: func(int) (media.CapabilitySnapshot, bool) { return *proof, true }}
	return s, c, f, proof
}
func pcmSDKBeginRequest(c *Call, turn uint64) *pcmBeginRequest {
	return &pcmBeginRequest{ctx: context.Background(), uuid: c.compatUUIDs[0], turnID: turn, bufferMS: 200, prebufferMS: 40, reply: make(chan pcmBeginResult, 1)}
}
func pcmSDKBegin(t *testing.T, s *Server, c *Call, f *pcmSDKFake, turn uint64) *PCMHandle {
	t.Helper()
	r := pcmSDKBeginRequest(c, turn)
	s.handlePCMBegin(r)
	f.complete(t, "buffering", nil)
	s.handlePCMBeginResult(<-s.pcm.results)
	result := <-r.reply
	if result.err != nil || result.handle == nil {
		t.Fatalf("授权失败：%+v", result)
	}
	return result.handle
}

func TestPCMSDKAdmissionRequiresExactLocalACKAndWorkerIdentity(t *testing.T) {
	for name, change := range map[string]func(*Server, *Call, *media.CapabilitySnapshot){
		"no_ack":           func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.AAck = false },
		"not_established":  func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Established = false },
		"bridge":           func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Local = false },
		"released":         func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Allocated = false },
		"releasing":        func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Releasing = true },
		"ended":            func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Ended = true },
		"relay":            func(s *Server, _ *Call, _ *media.CapabilitySnapshot) { s.Config.Media.Processing = "relay" },
		"wrong_topology":   func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Allocation.ProcessingTopology = "bridge" },
		"missing_version":  func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Allocation.ProcessingVersion = nil },
		"wrong_codec":      func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.Name = "G722" },
		"wrong_rate":       func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.SampleRate = 16000 },
		"wrong_clock":      func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.RTPClockRate = 48000 },
		"stereo":           func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.Channels = 2 },
		"wrong_frame":      func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.Codec.PTimeMS = 40 },
		"wrong_sdp_frame":  func(_ *Server, c *Call, _ *media.CapabilitySnapshot) { c.Offer.PTime = 40 },
		"unhealthy":        func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.Healthy = false },
		"missing_pid":      func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.PID = 0 },
		"wrong_worker":     func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.WorkerID = 1 },
		"wrong_generation": func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.Generation = 2 },
		"zero_generation":  func(_ *Server, c *Call, p *media.CapabilitySnapshot) { c.Generation = 0; p.Generation = 0 },
		"missing_pcm_cap":  func(_ *Server, _ *Call, p *media.CapabilitySnapshot) { p.Capabilities = p.Capabilities[:2] },
		"b_leg": func(s *Server, c *Call, _ *media.CapabilitySnapshot) {
			s.compatChannels[c.compatUUIDs[0]] = compatibilityChannel{call: c, side: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, c, f, p := pcmSDKFixture(t)
			change(s, c, p)
			r := pcmSDKBeginRequest(c, 1)
			s.handlePCMBegin(r)
			result := <-r.reply
			if !errors.Is(result.err, ErrPCMNotEligible) || len(f.pending) != 0 || c.pcmPending != nil {
				t.Fatalf("不合法通道获得实际媒体副作用：%+v", result)
			}
		})
	}
}

func TestPCMSDKBeginIdempotentAndReplacementRetiresOnlyAfterAcknowledgement(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	first := pcmSDKBegin(t, s, c, f, 1)
	if first.UUID() != c.compatUUIDs[0] || first.TurnID() != 1 || first.Retired() {
		t.Fatal("返回身份错误")
	}
	second := pcmSDKBegin(t, s, c, f, 1)
	if first != second {
		t.Fatal("幂等begin替换了句柄指针")
	}
	r := pcmSDKBeginRequest(c, 2)
	s.handlePCMBegin(r)
	if first.Retired() || !first.inputClosed.Load() || !s.pcmOwnsTX(c) {
		t.Fatal("媒体确认前错误退休或释放TX")
	}
	f.complete(t, "buffering", nil)
	s.handlePCMBeginResult(<-s.pcm.results)
	next := <-r.reply
	if next.err != nil || next.handle == first || !first.Retired() || next.handle.Retired() {
		t.Fatal("新媒体轮次确认未正确替换")
	}
	if _, err := first.Push(context.Background(), 0, make([]int16, 160)); !errors.Is(err, ErrPCMHandleRetired) {
		t.Fatal("被替换句柄仍可写", err)
	}
	old := pcmSDKBeginRequest(c, 1)
	s.handlePCMBegin(old)
	if result := <-old.reply; !errors.Is(result.err, ErrPCMStaleTurn) {
		t.Fatal("接受旧轮次", result)
	}
	bad := pcmSDKBeginRequest(c, 2)
	bad.bufferMS = 220
	s.handlePCMBegin(bad)
	if result := <-bad.reply; !errors.Is(result.err, ErrPCMInvalid) {
		t.Fatal("同轮修改参数复活流", result)
	}
}

func TestPCMSDKPushEndOrderingAndInterruptDoesNotWaitForData(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	h := pcmSDKBegin(t, s, c, f, 1)
	f.pushEntered = make(chan struct{}, 1)
	f.pushRelease = make(chan struct{})
	f.callState = "stopping"
	done := make(chan error, 1)
	go func() { _, err := h.Push(context.Background(), 0, make([]int16, 160)); done <- err }()
	<-f.pushEntered
	if _, err := h.End(context.Background(), 160); !errors.Is(err, ErrPCMHandleBusy) {
		t.Fatal("end超越在途PCM", err)
	}
	if _, err := h.Push(context.Background(), 160, make([]int16, 160)); !errors.Is(err, ErrPCMHandleBusy) {
		t.Fatal("同轮无界并发", err)
	}
	if _, err := h.Interrupt(context.Background(), 20); err != nil {
		t.Fatal("interrupt等待了数据", err)
	}
	if h.Retired() || !s.pcmOwnsTX(c) {
		t.Fatal("stopping被当作媒体已停止/退休")
	}
	close(f.pushRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := h.Push(context.Background(), 160, make([]int16, 160)); !errors.Is(err, ErrPCMHandleClosed) {
		t.Fatal("interrupt后输入被恢复", err)
	}
	f.mu.Lock()
	f.callState = "stopped"
	f.mu.Unlock()
	if _, err := h.Status(context.Background()); err != nil || s.pcmOwnsTX(c) || h.Retired() {
		t.Fatal("真实终态后未释放TX，或失去查询权限", err)
	}
}

func TestPCMSDKBackpressureAndWrongEndRemainRecoverable(t *testing.T) {
	for _, failure := range []error{media.ErrQueueFull, &media.PCMRejection{Code: 1, Name: "invalid_packet"}, &media.PCMRejection{Code: 7, Name: "offset_mismatch"}, &media.PCMRejection{Code: 8, Name: "queue_full"}} {
		t.Run(failure.Error(), func(t *testing.T) {
			s, c, f, _ := pcmSDKFixture(t)
			h := pcmSDKBegin(t, s, c, f, 1)
			f.pushErr = failure
			if _, err := h.Push(context.Background(), 0, make([]int16, 160)); err == nil || h.inputClosed.Load() {
				t.Fatal("确定整批拒绝关闭了输入", err)
			}
			f.pushErr = nil
			if _, err := h.Push(context.Background(), 0, make([]int16, 160)); err != nil {
				t.Fatal("背压后正确偏移不能继续", err)
			}
			f.callReject = true
			f.callErr = errors.New("PCM final sample offset mismatch")
			if _, err := h.End(context.Background(), 320); err == nil || h.inputClosed.Load() {
				t.Fatal("错误end永久关闭输入", err)
			}
			f.callReject = false
			f.callErr = nil
			if _, err := h.Push(context.Background(), 160, make([]int16, 160)); err != nil {
				t.Fatal("错误end后不能补齐", err)
			}
		})
	}
	s, c, f, _ := pcmSDKFixture(t)
	h := pcmSDKBegin(t, s, c, f, 1)
	f.pushErr = media.ErrPCMOutcomeUnknown
	if _, err := h.Push(context.Background(), 0, make([]int16, 160)); !errors.Is(err, media.ErrPCMOutcomeUnknown) || !h.inputClosed.Load() || !s.pcmOwnsTX(c) {
		t.Fatal("未知结果被当作可重发或已停止", err)
	}
	f.pushErr = nil
	if _, err := h.Push(context.Background(), 0, make([]int16, 160)); !errors.Is(err, ErrPCMHandleClosed) {
		t.Fatal("未知结果后盲重发", err)
	}
}

func TestPCMSDKMutualExclusionKeepsParkAndSilentReadAvailable(t *testing.T) {
	for _, app := range []string{"park", "read"} {
		t.Run(app, func(t *testing.T) {
			s, c, f, _ := pcmSDKFixture(t)
			args := ""
			if app == "read" {
				args = "1 4 silence digits 2000 #"
			}
			if got := applicationAdmit(s, c.compatUUIDs[0], app, args, true); got != "+OK" {
				t.Fatal(got)
			}
			h := pcmSDKBegin(t, s, c, f, 1)
			if got := applicationAdmit(s, c.compatUUIDs[0], "playback", "tone_stream://%(1000,0,440)", true); !strings.HasPrefix(got, "-ERR PCM") {
				t.Fatal("正在PCM仍受理tone", got)
			}
			if got := applicationAdmit(s, c.compatUUIDs[0], "read", "1 4 prompt.wav digits 2000 #", true); !strings.HasPrefix(got, "-ERR PCM") {
				t.Fatal("正在PCM仍受理read提示", got)
			}
			if !s.pcmOwnsTX(c) || h.Retired() {
				t.Fatal("silent应用改变PCM归属")
			}
		})
	}
	s, c, f, _ := pcmSDKFixture(t)
	j, _ := parseApplication(esl.ExecuteRequest{Application: "playback", Arguments: "tone_stream://%(1000,0,440)"})
	j.playbackState = "running"
	s.applications.lanes[c.compatUUIDs[0]] = &applicationLane{call: c, uuid: c.compatUUIDs[0], active: j, heapIndex: -1}
	r := pcmSDKBeginRequest(c, 1)
	s.handlePCMBegin(r)
	if result := <-r.reply; !errors.Is(result.err, ErrPCMHandleBusy) || len(f.pending) != 0 {
		t.Fatal("抢占旧tone", result)
	}
	j.playbackState = "completed"
	h := pcmSDKBegin(t, s, c, f, 1)
	if h == nil {
		t.Fatal("已完成提示未释放TX")
	}
}

func TestPCMSDKCancellationAfterActualBeginHasNoWritableOrphan(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan pcmBeginResult, 1)
	go func() {
		h, reply, err := s.BeginPCM(ctx, c.compatUUIDs[0], 1, 200, 40)
		done <- pcmBeginResult{handle: h, reply: reply, err: err}
	}()
	r := <-s.pcm.requests
	s.handlePCMBegin(r)
	cancel()
	result := <-done
	if !errors.Is(result.err, ErrPCMBeginUnknown) || result.handle == nil || !result.handle.inputClosed.Load() {
		t.Fatal("可能已提交的取消没有可恢复封闭句柄", result)
	}
	if _, err := result.handle.Status(context.Background()); !errors.Is(err, ErrPCMHandleBusy) || result.handle.Retired() {
		t.Fatal("pending句柄错误退休或可以伪查询", err)
	}
	f.complete(t, "buffering", nil)
	s.handlePCMBeginResult(<-s.pcm.results)
	h := c.pcmHandle
	if h == nil || h != result.handle || !h.inputClosed.Load() || !s.pcmOwnsTX(c) || s.pcm.pending != 0 {
		t.Fatal("丢失实际媒体归属")
	}
	if request := f.complete(t, "stopped", nil); request.Op != "pcm_turn_interrupt" || request.TurnID != 1 {
		t.Fatal("取消后没有真实清理操作", request)
	}
	if s.pcmOwnsTX(c) || h.Retired() {
		t.Fatal("清理终态未保留可对账句柄")
	}
}

func TestPCMSDKPendingBeginCannotReviveFinishedCall(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	r := pcmSDKBeginRequest(c, 1)
	s.handlePCMBegin(r)
	c.Ended = true
	s.retireCallPCM(c, false)
	f.complete(t, "buffering", nil)
	s.handlePCMBeginResult(<-s.pcm.results)
	result := <-r.reply
	if !errors.Is(result.err, ErrPCMHandleRetired) || result.handle != nil || c.pcmHandle != nil || s.pcm.pending != 0 {
		t.Fatal("挂机后迟到begin复活了句柄", result)
	}
}

func TestPCMSDKQueueBudgetAndRetirementDoNotFakeMediaTerminal(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	h := pcmSDKBegin(t, s, c, f, 1)
	for i := 0; i < pcmBeginLimit; i++ {
		s.pcm.requests <- pcmSDKBeginRequest(c, 2)
	}
	if _, _, err := s.BeginPCM(context.Background(), c.compatUUIDs[0], 2, 200, 40); !errors.Is(err, ErrPCMHandleBusy) {
		t.Fatal("授权队列未封顶", err)
	}
	if _, _, err := s.BeginPCM(context.Background(), c.compatUUIDs[0], 0, 200, 40); !errors.Is(err, ErrPCMInvalid) {
		t.Fatal("零turn被接受", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 32; n++ {
				h.Retired()
				_, _ = h.Status(context.Background())
			}
		}()
	}
	s.retireCallPCM(c, false)
	wg.Wait()
	if !h.Retired() || h.terminal.Load() || !s.pcmOwnsTX(c) {
		t.Fatal("Go撤销被当作媒体已停止")
	}
	s.retireCallPCM(c, true)
	if s.pcmOwnsTX(c) {
		t.Fatal("真实释放后仍有TX归属")
	}
	if _, err := h.Push(context.Background(), 0, make([]int16, 160)); !errors.Is(err, ErrPCMHandleRetired) {
		t.Fatal("已退休身份仍可写", err)
	}
}

func TestPCMSDKCancellationAfterDeliveryClaimStillClosesOrphan(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	h := pcmSDKBegin(t, s, c, f, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := s.BeginPCM(ctx, c.compatUUIDs[0], 1, 200, 40); done <- err }()
	r := <-s.pcm.requests
	if !r.delivery.CompareAndSwap(0, 2) {
		t.Fatal("未取得交付权")
	}
	cancel()
	// 交付已经归主循环，取消线程必须等待这一槽结果后清理，不能直接遗忘句柄。
	select {
	case err := <-done:
		t.Fatal("还未交付就结束", err)
	case <-time.After(5 * time.Millisecond):
	}
	r.reply <- pcmBeginResult{handle: h, reply: pcmSDKReply(h.request("pcm_turn_begin"), "buffering")}
	if err := <-done; !errors.Is(err, ErrPCMBeginUnknown) || !h.inputClosed.Load() {
		t.Fatal("交付竞态产生可写孤儿", err)
	}
	if request := f.complete(t, "stopped", nil); request.Op != "pcm_turn_interrupt" {
		t.Fatal(request)
	}
}

func TestPCMSDKFailedReplacementPreservesOldControlAndTerminalBeginCannotResurrect(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	h := pcmSDKBegin(t, s, c, f, 1)
	f.submitErr = media.ErrQueueFull
	r := pcmSDKBeginRequest(c, 2)
	s.handlePCMBegin(r)
	if result := <-r.reply; !errors.Is(result.err, media.ErrQueueFull) || h.inputClosed.Load() || h.Retired() {
		t.Fatal("未入媒体队列的拒绝改变了旧轮次", result)
	}
	f.submitErr = nil
	r = pcmSDKBeginRequest(c, 2)
	s.handlePCMBegin(r)
	f.complete(t, "", errors.New("media begin rejected"))
	s.handlePCMBeginResult(<-s.pcm.results)
	if result := <-r.reply; result.err == nil || result.handle != nil || c.pcmHandle != h || h.Retired() || !h.inputClosed.Load() || !s.pcmOwnsTX(c) {
		t.Fatal("失败替换丢失旧控制身份或假释放TX", result)
	}
	f.callState = "stopped"
	if _, err := h.Interrupt(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	r = pcmSDKBeginRequest(c, 1)
	s.handlePCMBegin(r)
	f.complete(t, "stopped", nil)
	s.handlePCMBeginResult(<-s.pcm.results)
	result := <-r.reply
	if result.err != nil || result.handle != h || !h.inputClosed.Load() || !h.terminal.Load() {
		t.Fatal("同轮幂等begin复活已停止输入", result)
	}
}

func TestPCMSDKDelayedLegacyPlaybackRechecksTXAndActiveStreamAllowsSilentRead(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	pcmSDKBegin(t, s, c, f, 1)
	if got := applicationAdmit(s, c.compatUUIDs[0], "read", "1 4 silence digits 2000 #", false); got != "+OK" {
		t.Fatal("stream误拦不占TX的silent read", got)
	}
	// 构造在之前阶段已排队的媒体应用，穿过真正startNextApplication检查其没有提交播放。
	lane := s.applications.lanes[c.compatUUIDs[0]]
	s.completeApplication(lane, "_none_")
	j, err := parseApplication(esl.ExecuteRequest{UUID: lane.uuid, Application: "playback", Arguments: "tone_stream://%(1000,0,440)"})
	if err != nil {
		t.Fatal(err)
	}
	lane.queue = append(lane.queue, j)
	s.applications.jobs++
	s.applications.bytes += j.bytes
	s.startNextApplication(lane, time.Now())
	if j.playbackID != 0 || j.mediaInflight || lane.active != nil || s.applications.jobs != 0 || s.applications.bytes != 0 {
		t.Fatal("延迟应用绕过TX所有权或泄漏应用预算")
	}
	for _, request := range f.commands {
		if request.Op == "playback_start" || request.Op == "playback_file_start" {
			t.Fatal("冲突播放进入真实媒体队列")
		}
	}
}

func TestPCMSDKRecoverableRejectionCannotUndoConcurrentInterrupt(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	h := pcmSDKBegin(t, s, c, f, 1)
	f.pushErr = &media.PCMRejection{Code: 8, Name: "queue_full"}
	f.pushEntered = make(chan struct{}, 1)
	f.pushRelease = make(chan struct{})
	f.callState = "stopping"
	done := make(chan error, 1)
	go func() { _, err := h.Push(context.Background(), 0, make([]int16, 160)); done <- err }()
	<-f.pushEntered
	if _, err := h.Interrupt(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	close(f.pushRelease)
	if err := <-done; err == nil {
		t.Fatal("故障夹具未产生拒绝")
	}
	if !h.inputClosed.Load() || h.Retired() {
		t.Fatal("可恢复失败错误复位了并发interrupt或使句柄失去查询能力")
	}
}

// 丢失begin回执与明确拒绝不同：实际Rust可能已经切换轮次，必须能定位并停止新turn。
func TestPCMSDKUnknownBeginRetainsRecoverableOwner(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "first"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			s, c, f, _ := pcmSDKFixture(t)
			var old *PCMHandle
			turn := uint64(1)
			if replacement {
				old = pcmSDKBegin(t, s, c, f, 1)
				turn = 2
			}
			r := pcmSDKBeginRequest(c, turn)
			s.handlePCMBegin(r)
			candidate := c.pcmPending
			f.completeUnknown(t)
			s.handlePCMBeginResult(<-s.pcm.results)
			result := <-r.reply
			if !errors.Is(result.err, ErrPCMBeginUnknown) || result.handle != candidate || result.reply.OK || result.reply.Type != "" {
				t.Fatal("未知回执没有保留实际错误及恢复身份", result)
			}
			if c.pcmHandle != candidate || c.pcmPending != nil || s.pcm.pending != 0 || candidate.Retired() || !candidate.committed.Load() || !candidate.inputClosed.Load() || candidate.terminal.Load() || !s.pcmOwnsTX(c) {
				t.Fatal("未知begin释放了TX或遗失新轮次")
			}
			if old != nil && !old.Retired() {
				t.Fatal("可能已被Rust替换的旧句柄仍可操作")
			}
			if got := applicationAdmit(s, c.compatUUIDs[0], "playback", "prompt.wav", true); !strings.HasPrefix(got, "-ERR PCM") {
				t.Fatal("未知stream期间受理WAV", got)
			}
			if _, err := candidate.Push(context.Background(), 0, make([]int16, 160)); !errors.Is(err, ErrPCMHandleClosed) || f.pushes != 0 {
				t.Fatal("未知回执后继续推送", err)
			}
			if _, err := candidate.Status(context.Background()); err != nil || !s.pcmOwnsTX(c) {
				t.Fatal("恢复句柄不能查询或playing被当终态", err)
			}
			// 同轮begin用于恢复身份，真实确认也不能把封闭输入重开。
			if recovered := pcmSDKBegin(t, s, c, f, turn); recovered != candidate || !recovered.inputClosed.Load() {
				t.Fatal("同轮恢复生成新句柄或重开输入")
			}
			f.callState = "stopping"
			if _, err := candidate.Interrupt(context.Background(), 20); err != nil || !s.pcmOwnsTX(c) {
				t.Fatal("stopping提前释放所有权", err)
			}
			f.callState = "stopped"
			if _, err := candidate.Status(context.Background()); err != nil || s.pcmOwnsTX(c) || candidate.Retired() {
				t.Fatal("实际停止后没有释放TX并保留查询", err)
			}
		})
	}
}

func TestPCMSDKCancelledBeforePublicationCannotSubmit(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan pcmBeginResult, 1)
	go func() {
		h, reply, err := s.BeginPCM(ctx, c.compatUUIDs[0], 1, 200, 40)
		done <- pcmBeginResult{handle: h, reply: reply, err: err}
	}()
	r := <-s.pcm.requests
	cancel()
	result := <-done
	if result.handle != nil || !errors.Is(result.err, context.Canceled) {
		t.Fatal("未发布取消被当成可能已提交", result)
	}
	s.handlePCMBegin(r)
	if len(f.pending) != 0 || len(f.commands) != 0 || s.pcm.pending != 0 || c.pcmPending != nil || c.pcmHandle != nil || r.published.Load() != nil {
		t.Fatal("返回nil后仍产生媒体请求或资源占用")
	}
}

func TestPCMSDKCancelledPendingKnownRejectionRetiresCandidate(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "first"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			s, c, f, _ := pcmSDKFixture(t)
			var old *PCMHandle
			turn := uint64(1)
			if replacement {
				old = pcmSDKBegin(t, s, c, f, 1)
				turn = 2
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan pcmBeginResult, 1)
			go func() {
				h, reply, err := s.BeginPCM(ctx, c.compatUUIDs[0], turn, 200, 40)
				done <- pcmBeginResult{handle: h, reply: reply, err: err}
			}()
			r := <-s.pcm.requests
			s.handlePCMBegin(r)
			candidate := c.pcmPending
			cancel()
			result := <-done
			if result.handle != candidate || result.handle == nil || !errors.Is(result.err, ErrPCMBeginUnknown) || candidate.Retired() {
				t.Fatal("在途取消丢失可恢复资源身份", result)
			}
			f.complete(t, "", errors.New("explicit begin rejection"))
			s.handlePCMBeginResult(<-s.pcm.results)
			if !candidate.Retired() || c.pcmHandle != old || c.pcmPending != nil || s.pcm.pending != 0 {
				t.Fatal("明确拒绝未回收候选或改变旧所有权")
			}
			if old != nil {
				if old.Retired() || !s.pcmOwnsTX(c) {
					t.Fatal("拒绝替换使旧轮失控")
				}
				if _, err := old.Status(context.Background()); err != nil {
					t.Fatal("旧轮无法查询", err)
				}
			} else if s.pcmOwnsTX(c) {
				t.Fatal("确定未产生媒体却泄漏TX预留")
			}
		})
	}
}

// 两个原子操作之间的取消不应产生nil句柄但已经Submit的状态；128轮并发实际走公开入口。
func TestPCMSDKPublicationCancellationRaceNeverLosesSubmittedHandle(t *testing.T) {
	for i := 0; i < 128; i++ {
		s, c, f, _ := pcmSDKFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan pcmBeginResult, 1)
		go func() {
			h, reply, err := s.BeginPCM(ctx, c.compatUUIDs[0], 1, 200, 40)
			done <- pcmBeginResult{handle: h, reply: reply, err: err}
		}()
		r := <-s.pcm.requests
		cancelDone := make(chan struct{})
		go func() { cancel(); close(cancelDone) }()
		s.handlePCMBegin(r)
		<-cancelDone
		result := <-done
		if len(f.pending) == 0 {
			if s.pcm.pending != 0 || c.pcmPending != nil || c.pcmHandle != nil {
				t.Fatal("未提交却留下主循环占位", i)
			}
			if result.handle != nil && !result.handle.Retired() {
				t.Fatal("未提交候选无法被外部预算清理", i)
			}
			continue
		}
		if result.handle == nil || result.handle != c.pcmPending || result.handle != r.published.Load() || !errors.Is(result.err, ErrPCMBeginUnknown) || !result.handle.inputClosed.Load() {
			t.Fatal("已提交取消丢失恢复句柄", i, result)
		}
		f.completeUnknown(t)
		s.handlePCMBeginResult(<-s.pcm.results)
		if c.pcmHandle != result.handle || result.handle.Retired() || !s.pcmOwnsTX(c) || s.pcm.pending != 0 {
			t.Fatal("迟到未知回执释放了TX或替换恢复句柄", i)
		}
		s.retireCallPCM(c, true)
	}
}

func TestPCMSDKUnknownSameTurnDoesNotReuseTerminalProof(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	h := pcmSDKBegin(t, s, c, f, 1)
	f.callState = "stopped"
	if _, err := h.Interrupt(context.Background(), 0); err != nil || s.pcmOwnsTX(c) {
		t.Fatal("停止夹具无效", err)
	}
	r := pcmSDKBeginRequest(c, 1)
	s.handlePCMBegin(r)
	f.completeUnknown(t)
	s.handlePCMBeginResult(<-s.pcm.results)
	result := <-r.reply
	if result.handle != h || !errors.Is(result.err, ErrPCMBeginUnknown) || h.terminal.Load() || !s.pcmOwnsTX(c) || !h.inputClosed.Load() {
		t.Fatal("未知回执复用了旧终态释放TX", result)
	}
	if _, err := h.Status(context.Background()); err != nil || s.pcmOwnsTX(c) {
		t.Fatal("新鲜终态回执未释放TX", err)
	}
}
