package asr

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"rustswitch/control/internal/asr/wire"
	"rustswitch/control/internal/media"
)

const unitToken = "11223344556677889900aabbccddeeff"

// unitSource是纯状态测试输入，不代替实际Rust收音或供应商进程验收。
type unitSource struct {
	mu               sync.Mutex
	frames           chan media.RXFrame
	life             chan struct{}
	retired, unknown bool
	unsub, status    int
}

func newUnitSource() *unitSource {
	return &unitSource{frames: make(chan media.RXFrame, 16), life: make(chan struct{})}
}
func (u *unitSource) Read(ctx context.Context) (media.RXFrame, error) {
	select {
	case <-ctx.Done():
		return media.RXFrame{}, ctx.Err()
	case <-u.life:
		return media.RXFrame{}, media.ErrRXRetired
	case f := <-u.frames:
		return f, nil
	}
}
func (u *unitSource) Snapshot() media.RXSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return media.RXSnapshot{Session: 1, SubscriptionID: 7, Retired: u.retired}
}
func (u *unitSource) LifetimeDone() <-chan struct{} { return u.life }
func (u *unitSource) ConfirmedRetired() bool        { u.mu.Lock(); defer u.mu.Unlock(); return u.retired }
func (u *unitSource) Unsubscribe(context.Context) (media.Reply, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.unsub++
	if u.unknown {
		return media.Reply{}, media.ErrRXOutcomeUnknown
	}
	return media.Reply{OK: true, Type: "rx_state", Session: 1, SubscriptionID: 7, State: "stopped"}, nil
}
func (u *unitSource) Status(context.Context) (media.Reply, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status++
	if u.unknown {
		return media.Reply{}, media.ErrRXOutcomeUnknown
	}
	return media.Reply{OK: true, Type: "rx_state", Session: 1, SubscriptionID: 7, State: "stopped"}, nil
}

func unitFrame(t *testing.T, kind media.RXKind, seq uint64) media.RXFrame {
	t.Helper()
	now, domain, err := wire.ClockNow()
	if err != nil {
		t.Fatal(err)
	}
	f := media.RXFrame{Kind: kind, Session: 1, SubscriptionID: 7, Sequence: seq, SourceGeneration: 1, SourceSegment: 1, SampleRate: 8000, RTPClockRate: 8000, ClockDomain: domain, ObservationLowerBoundNS: now, ExpiresAtNS: now + 100_000_000, Flags: 15, SSRC: 17, RTPSequence: seq + 100, RTPTimestamp: (seq - 1) * 160, MediaTimeNS: (seq - 1) * frameDurationNS}
	if kind == media.RXSourceBoundary {
		f.Boundary = 1
		f.Flags |= 16
	}
	if kind == media.RXDecoded {
		f.KnownDurationTicks = 160
		f.SampleCount = 160
		f.BodyBytes = 320
		for i := range f.PCM {
			f.PCM[i] = int16(i*23 - 1200)
		}
	}
	return f
}

func startUnitStream(t *testing.T, conn net.Conn, u *unitSource) *Stream {
	t.Helper()
	s := newStream(context.Background(), Options{UUID: "unit", SubscriptionID: 7, SampleRate: 8000}, unitToken, conn)
	s.source = u
	s.state.State = "streaming"
	s.ioRunning = 3
	go s.writer()
	go s.reader()
	go s.lifecycle()
	t.Cleanup(func() {
		s.Cancel()
		u.mu.Lock()
		u.retired = true
		u.mu.Unlock()
		s.Reap(context.Background())
		select {
		case <-s.Closed():
		case <-time.After(2 * time.Second):
			t.Error("I/O未退出")
		}
	})
	return s
}

func unitMessage(t *testing.T, kind wire.Kind, v any) wire.Message {
	t.Helper()
	m, err := wire.JSONMessage(kind, v)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func unitSend(conn net.Conn, kind wire.Kind, v any) error {
	m, e := wire.JSONMessage(kind, v)
	if e != nil {
		return e
	}
	var b [wire.MaxWireSize]byte
	raw, e := wire.Encode(b[:], m)
	if e != nil {
		return e
	}
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	return writeAll(conn, raw)
}
func unitResult(f media.RXFrame, kind string, seq, revision uint64) wire.Result {
	return wire.Result{StreamToken: unitToken, ResultSeq: wire.U64(seq), UtteranceID: 1, Revision: wire.U64(revision), SourceGeneration: 1, SourceSegment: 1, Type: kind, Text: "测试识别", FirstEventSeq: wire.U64(f.Sequence), LastEventSeq: wire.U64(f.Sequence), StartMediaNS: wire.U64(f.MediaTimeNS), EndMediaNS: wire.U64(f.MediaTimeNS + frameDurationNS), Coverage: "complete"}
}

func TestStreamFinishFinalAndPhysicalCleanup(t *testing.T) {
	client, provider := net.Pipe()
	defer provider.Close()
	u := newUnitSource()
	s := startUnitStream(t, client, u)
	f := unitFrame(t, media.RXDecoded, 2)
	providerDone := make(chan error, 1)
	audioSeen := make(chan struct{})
	go func() {
		var b [wire.MaxWireSize]byte
		for {
			m, e := wire.Read(provider, &b)
			if e != nil {
				providerDone <- e
				return
			}
			switch m.Kind {
			case wire.KindAudio:
				close(audioSeen)
				if e = unitSend(provider, wire.KindResult, unitResult(f, "partial", 1, 1)); e != nil {
					providerDone <- e
					return
				}
			case wire.KindFinish:
				var finish wire.Finish
				if e = wire.ParseJSON(m.Body, &finish); e == nil {
					e = unitSend(provider, wire.KindResult, unitResult(f, "final", 2, 2))
				}
				if e == nil {
					e = unitSend(provider, wire.KindDone, finish)
				}
				providerDone <- e
				return
			}
		}
	}()
	u.frames <- unitFrame(t, media.RXSourceBoundary, 1)
	u.frames <- f
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-audioSeen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := s.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-providerDone; err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().ResourcesClosed {
		t.Fatal("未确证RX停止就回收")
	}
	s.Reap(ctx)
	select {
	case <-s.Closed():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if s.Snapshot().State != "finishing" {
		t.Fatalf("未消费结果提前完成: %+v", s.Snapshot())
	}
	for _, want := range []string{"partial", "final"} {
		e, err := s.Next(ctx)
		if err != nil || e.Type != want {
			t.Fatalf("%s: %+v %v", want, e, err)
		}
	}
	if _, err := s.Next(ctx); err != io.EOF {
		t.Fatalf("未正常EOF: %v", err)
	}
	v := s.Snapshot()
	if v.State != "completed" || v.AudioFrames != 1 || v.Samples != 160 || v.ProviderAcknowledgedSamples != 160 || !v.RXStopped || v.QueuedResults != 0 {
		t.Fatalf("计数/完成错误: %+v", v)
	}
}

func TestStreamUnknownCleanupRetainsIdentity(t *testing.T) {
	u := newUnitSource()
	u.unknown = true
	s := newStream(context.Background(), Options{SubscriptionID: 7, SampleRate: 8000}, unitToken, nil)
	s.source = u
	s.fail(ErrUnavailable)
	if s.Reap(context.Background()) || s.Reap(context.Background()) {
		t.Fatal("未知受理不能释放")
	}
	u.mu.Lock()
	if u.unsub != 1 || u.status != 1 {
		t.Errorf("请求应分次退订/状态: %d %d", u.unsub, u.status)
	}
	u.unknown = false
	u.mu.Unlock()
	if !s.Reap(context.Background()) {
		t.Fatal("实际停止后应释放")
	}
	if s.Snapshot().State != "failed" {
		t.Fatal("清理不能伪造识别成功")
	}
}

func TestStreamServiceCancellationIsNotRXRetirement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	u := newUnitSource()
	u.unknown = true
	s := newStream(ctx, Options{SubscriptionID: 7}, unitToken, nil)
	s.source = u
	cancel()
	s.Cancel()
	if s.Reap(context.Background()) {
		t.Fatal("服务取消不等于真实退休")
	}
	u.mu.Lock()
	u.retired = true
	u.mu.Unlock()
	if !s.Reap(context.Background()) {
		t.Fatal("真实退休后仍未释放")
	}
}

func TestStreamQueuedFinalRejectedAfterCallLifetime(t *testing.T) {
	u := newUnitSource()
	s := newStream(context.Background(), Options{SubscriptionID: 7}, unitToken, nil)
	s.source = u
	s.state.ProviderDone = true
	s.state.RXStopped = true
	s.state.TransportClosed = true
	s.results[0] = Event{Type: "final", Text: "旧通话"}
	s.resultCount = 1
	s.maybeClosedLocked()
	close(u.life)
	if _, err := s.Next(context.Background()); !errors.Is(err, ErrCancelled) {
		t.Fatalf("旧final被放行: %v", err)
	}
	if s.Snapshot().QueuedResults != 0 {
		t.Fatal("撤权后旧结果未清空")
	}
}

func TestStreamDoneRequiresExactPrefixAndFinal(t *testing.T) {
	for _, name := range []string{"before_finish", "wrong_count", "open_partial", "valid"} {
		t.Run(name, func(t *testing.T) {
			s := newStream(context.Background(), Options{}, unitToken, nil)
			defer s.cancel()
			s.finishWritten = true
			s.finishDeadline = time.Now().Add(time.Second)
			s.state.AudioFrames = 1
			s.state.Samples = 160
			s.state.Markers = 1
			s.state.LastEventSequence = 2
			done := s.finishValueLocked()
			switch name {
			case "before_finish":
				s.finishWritten = false
			case "wrong_count":
				done.Samples = 320
			case "open_partial":
				s.proof.utterance = 1
			}
			ok, err := s.accept(unitMessage(t, wire.KindDone, done))
			if name == "valid" {
				if err != nil || !ok {
					t.Fatal(err)
				}
			} else if err == nil || ok {
				t.Fatal("错误DONE被接受")
			}
		})
	}
}

func TestStreamPartialCoalescesButFinalOverflowFails(t *testing.T) {
	s := newStream(context.Background(), Options{}, unitToken, nil)
	defer s.cancel()
	for i := uint64(1); i < 100; i++ {
		if err := s.enqueueLocked(Event{Type: "partial", UtteranceID: 1, Revision: i}); err != nil {
			t.Fatal(err)
		}
	}
	if s.resultCount != 1 || s.state.PartialCoalesced != 98 {
		t.Fatal("partial无界增长")
	}
	for i := 1; i < resultLimit; i++ {
		if err := s.enqueueLocked(Event{Type: "final", UtteranceID: uint64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(s.enqueueLocked(Event{Type: "final", UtteranceID: 99}), ErrResultOverflow) {
		t.Fatal("final溢出应明确失败")
	}
}

// prefixFailConn模拟完整系统调用返回部分字节后报错，验证不能重发不确定的前缀。
type prefixFailConn struct {
	net.Conn
	calls    int
	deadline time.Time
}

func (c *prefixFailConn) Write(b []byte) (int, error)        { c.calls++; return 3, io.ErrClosedPipe }
func (c *prefixFailConn) SetWriteDeadline(d time.Time) error { c.deadline = d; return nil }
func TestStreamPartialWriteNeverReplayed(t *testing.T) {
	c := &prefixFailConn{}
	s := newStream(context.Background(), Options{}, unitToken, c)
	defer s.cancel()
	f := unitFrame(t, media.RXDecoded, 2)
	if err := s.writeObserved(make([]byte, 100), &f); !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatal(err)
	}
	if c.calls != 1 || s.Snapshot().UnknownWrites != 1 {
		t.Fatal("不确定帧被重试或计数遗漏")
	}
}

func TestStreamExpiredBeforeWriteConsumesNoBytes(t *testing.T) {
	c := &prefixFailConn{}
	s := newStream(context.Background(), Options{}, unitToken, c)
	defer s.cancel()
	f := unitFrame(t, media.RXDecoded, 2)
	f.ObservationLowerBoundNS -= 200_000_000
	f.ExpiresAtNS -= 200_000_000
	if err := s.writeObserved(make([]byte, 100), &f); !errors.Is(err, media.ErrRXExpired) {
		t.Fatal(err)
	}
	if c.calls != 0 || s.Snapshot().UnknownWrites != 0 || s.Snapshot().ExpiredFrames != 1 {
		t.Fatal("过期帧仍触发写入")
	}
}

func TestStreamInputEOFDoesNotFabricateFinish(t *testing.T) {
	client, provider := net.Pipe()
	defer provider.Close()
	u := newUnitSource()
	s := startUnitStream(t, client, u)
	f := unitFrame(t, media.RXEnd, 1)
	f.ObservationLowerBoundNS = 0
	f.ExpiresAtNS = 0
	u.frames <- f
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for s.Snapshot().Error == "" {
		select {
		case <-s.wake:
		case <-deadline.C:
			t.Fatal("未拒绝突然结束")
		}
	}
	v := s.Snapshot()
	if v.InputFinished || v.ProviderDone || v.State != "failed" {
		t.Fatalf("伪造正常结束: %+v", v)
	}
}

func TestStreamAcceptWaitsForInFlightCommit(t *testing.T) {
	s := newStream(context.Background(), Options{}, unitToken, nil)
	defer s.cancel()
	s.inFlight = true
	m := unitMessage(t, wire.KindResult, unitResult(unitFrame(t, media.RXDecoded, 2), "partial", 1, 1))
	if _, err := s.accept(m); !errors.Is(err, ErrBusy) {
		t.Fatal("在途响应必须等完整前缀")
	}
	if s.state.ResultsReceived != 0 || s.proof.resultSequence != 0 {
		t.Fatal("等待期间不应消耗协议序号")
	}
}
