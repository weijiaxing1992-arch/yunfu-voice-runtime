package asr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"rustswitch/control/internal/asr/wire"
)

// Stream每条流只有三个固定I/O协程；RX清理由Server的固定执行者调用Reap。
// mu只保护短状态变更，绝不持锁等待供应商、SIP控制或业务消费。
type Stream struct {
	mu                                                     sync.Mutex
	opts                                                   Options
	token                                                  string
	conn                                                   net.Conn
	source                                                 Source
	ctx                                                    context.Context
	cancel                                                 context.CancelFunc
	inputCtx                                               context.Context
	stopInput                                              context.CancelFunc
	closed, finishSent                                     chan struct{}
	wake, lifecycleWake, commitWake                        chan struct{}
	state                                                  Snapshot
	err                                                    error
	proof                                                  provenance
	results                                                [resultLimit]Event
	resultCount                                            int
	deliveryPending                                        bool
	finishRequested, finishWritten, inFlight, queryCleanup bool
	finishDeadline, pongDeadline                           time.Time
	pingNonce                                              uint64
	ioRunning                                              int
	reading, reaping                                       atomic.Bool
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func newStream(service context.Context, opts Options, token string, conn net.Conn) *Stream {
	ctx, cancel := context.WithCancel(service)
	in, stop := context.WithCancel(ctx)
	return &Stream{opts: opts, token: token, conn: conn, ctx: ctx, cancel: cancel, inputCtx: in, stopInput: stop, closed: make(chan struct{}), finishSent: make(chan struct{}), wake: make(chan struct{}, 1), lifecycleWake: make(chan struct{}, 1), commitWake: make(chan struct{}, 1), state: Snapshot{State: "starting", SampleRate: opts.SampleRate}}
}

// Start先确认独立代理身份与固定格式，再申请真实RX；错误伴随非nil句柄时仍须清理。
func Start(ctx context.Context, service context.Context, opts Options, acquire Acquire) (*Stream, error) {
	if ctx == nil || service == nil || acquire == nil || !filepath.IsAbs(opts.SocketPath) || filepath.Clean(opts.SocketPath) != opts.SocketPath || len(opts.SocketPath) > 100 {
		return nil, ErrInvalid
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, ErrUnavailable
	}
	_, domain, err := wire.ClockNow()
	if err != nil {
		return nil, ErrUnavailable
	}
	hello := wire.Hello{Protocol: "ASR1", StreamToken: hex.EncodeToString(token[:]), UUID: opts.UUID, SubscriptionID: wire.U64(opts.SubscriptionID), SourceRate: 8000, TargetRate: opts.SampleRate, Channels: 1, Format: "s16le", FrameMS: 20, ClockDomain: domain, PLCPolicy: "metadata", GapPolicy: "explicit"}
	if hello.Validate() != nil {
		return nil, ErrInvalid
	}
	dir, before, err := privateSocket(opts.SocketPath)
	if err != nil {
		return nil, err
	}
	handshake, cancelHandshake := context.WithTimeout(ctx, handshakeTimeout)
	stopService := context.AfterFunc(service, cancelHandshake)
	defer func() { stopService(); cancelHandshake() }()
	connRaw, err := (&net.Dialer{}).DialContext(handshake, "unix", opts.SocketPath)
	if err != nil {
		return nil, ErrUnavailable
	}
	conn, ok := connRaw.(*net.UnixConn)
	if !ok {
		_ = connRaw.Close()
		return nil, ErrUnavailable
	}
	stopSocket := context.AfterFunc(handshake, func() { _ = conn.Close() })
	defer stopSocket()
	afterDir, after, err := privateSocket(opts.SocketPath)
	uid, peerErr := wire.PeerUID(conn)
	if err != nil || peerErr != nil || uid != uint32(os.Getuid()) || !os.SameFile(dir, afterDir) || !os.SameFile(before, after) {
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	deadline, _ := handshake.Deadline()
	_ = conn.SetDeadline(deadline)
	var buf [wire.MaxWireSize]byte
	m, err := wire.JSONMessage(wire.KindHello, hello)
	if err == nil {
		var b []byte
		b, err = wire.Encode(buf[:], m)
		if err == nil {
			err = writeAll(conn, b)
		}
	}
	if err != nil {
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	m, err = wire.Read(conn, &buf)
	var ready wire.Hello
	if err != nil || m.Kind != wire.KindReady || wire.ParseJSON(m.Body, &ready) != nil || ready != hello {
		_ = conn.Close()
		return nil, ErrProtocol
	}
	// 握手截止时间到此结束。Acquire有独立的一秒控制预算；取消仍能关闭在建连接。
	if !stopSocket() || handshake.Err() != nil || service.Err() != nil || !time.Now().Before(deadline) {
		_ = conn.Close()
		return nil, ErrCancelled
	}
	_ = conn.SetDeadline(time.Time{})
	s := newStream(service, opts, hello.StreamToken, conn)
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, acquireTimeout)
	acquireDeadline, _ := acquireCtx.Deadline()
	stopAcquire := context.AfterFunc(service, cancelAcquire)
	source, acquireErr := acquire(acquireCtx)
	if acquireErr == nil && !time.Now().Before(acquireDeadline) {
		acquireErr = ErrProviderTimeout
	}
	stopAcquire()
	cancelAcquire()
	s.source = source
	if acquireErr != nil || source == nil || source.LifetimeDone() == nil || service.Err() != nil || ctx.Err() != nil {
		if acquireErr == nil {
			acquireErr = ErrCancelled
		}
		s.fail(acquireErr)
		if source == nil {
			s.mu.Lock()
			s.state.RXStopped = true
			s.maybeClosedLocked()
			s.mu.Unlock()
			return nil, acquireErr
		}
		return s, acquireErr
	}
	select {
	case <-source.LifetimeDone():
		s.fail(ErrCancelled)
		return s, ErrCancelled
	default:
	}
	s.mu.Lock()
	s.state.State = "streaming"
	s.ioRunning = 3
	s.mu.Unlock()
	go s.writer()
	go s.reader()
	go s.lifecycle()
	return s, nil
}

// 私有目录和实际peer凭据共同限制本机适配器；客户端从不删除供应商socket。
func privateSocket(path string) (os.FileInfo, os.FileInfo, error) {
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	sock, err := os.Lstat(path)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	owner := func(info os.FileInfo) bool {
		st, ok := info.Sys().(*syscall.Stat_t)
		return ok && st.Uid == uint32(os.Getuid())
	}
	if !dir.IsDir() || dir.Mode().Perm() != 0700 || !owner(dir) || sock.Mode()&os.ModeSocket == 0 || sock.Mode()&os.ModeSymlink != 0 || !owner(sock) {
		return nil, nil, ErrUnavailable
	}
	return dir, sock, nil
}

func writeAll(conn net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := conn.Write(b)
		if n < 0 || n > len(b) {
			return ErrProtocol
		}
		b = b[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func (s *Stream) fail(err error) {
	if err == nil {
		err = ErrProtocol
	}
	s.mu.Lock()
	s.failLocked(err)
	s.mu.Unlock()
	s.stopIO()
}

// failLocked让截止时间与终态在同一锁内裁决；实际关闭连接始终在锁外。
func (s *Stream) failLocked(err error) {
	if s.err == nil && s.state.State != "completed" {
		s.err = err
		s.state.Error = err.Error()
		s.state.State = "failed"
		if errors.Is(err, ErrCancelled) {
			s.state.State = "cancelled"
		}
		s.state.StaleResults += uint64(s.resultCount)
		clear(s.results[:])
		s.resultCount = 0
		s.deliveryPending = false
	}
	s.state.CleanupPending = !s.state.RXStopped
}

func (s *Stream) stopIO() {
	s.stopInput()
	s.cancel()
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.mu.Lock()
	s.state.TransportClosed = true
	s.maybeClosedLocked()
	s.mu.Unlock()
	signal(s.wake)
	signal(s.lifecycleWake)
	signal(s.commitWake)
}

func (s *Stream) ioDone() {
	s.mu.Lock()
	s.ioRunning--
	s.maybeClosedLocked()
	s.mu.Unlock()
	signal(s.wake)
}

func (s *Stream) maybeClosedLocked() {
	if s.ioRunning == 0 && s.state.TransportClosed && s.state.RXStopped && !s.state.ResourcesClosed {
		s.state.ResourcesClosed = true
		s.state.CleanupPending = false
		close(s.closed)
	}
	if s.err == nil && s.state.ResourcesClosed && s.state.ProviderDone && s.resultCount == 0 && !s.deliveryPending {
		s.state.State = "completed"
	}
}

// Finish只等待FINISH完整写入；最终识别结果继续由Next消费，避免等待未消费结果死锁。
func (s *Stream) Finish(ctx context.Context) error {
	if s == nil || ctx == nil {
		return ErrInvalid
	}
	s.mu.Lock()
	err := s.err
	if err == nil && !s.finishRequested {
		s.finishRequested = true
		s.state.State = "finishing"
		s.state.CleanupPending = true
		s.finishDeadline = time.Now().Add(finishTimeout)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.stopInput()
	signal(s.lifecycleWake)
	select {
	case <-s.finishSent:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.finishWritten {
			return nil
		}
		if s.err != nil {
			return s.err
		}
		return ErrCancelled
	}
}

func (s *Stream) Cancel() {
	if s != nil {
		s.fail(ErrCancelled)
	}
}
func (s *Stream) Closed() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.closed
}

func (s *Stream) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{State: "failed", Error: ErrInvalid.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state
	v.QueuedResults = s.resultCount
	v.ProvenanceRanges = s.proof.count
	return v
}

// Next限一个同时消费方。已完成I/O的句柄仍会在交付前直接核对原通话生命周期。
func (s *Stream) Next(ctx context.Context) (Event, error) {
	if s == nil || ctx == nil {
		return Event{}, ErrInvalid
	}
	if !s.reading.CompareAndSwap(false, true) {
		return Event{}, ErrBusy
	}
	defer s.reading.Store(false)
	for {
		if s.source != nil {
			select {
			case <-s.source.LifetimeDone():
				s.fail(ErrCancelled)
				return Event{}, ErrCancelled
			default:
			}
		}
		s.mu.Lock()
		if s.err != nil {
			err := s.err
			s.mu.Unlock()
			return Event{}, err
		}
		if s.resultCount > 0 {
			e := s.results[0]
			copy(s.results[:], s.results[1:s.resultCount])
			s.resultCount--
			s.deliveryPending = true
			s.results[s.resultCount] = Event{}
			s.mu.Unlock()
			// 后置检查覆盖队列取出期间的撤权；不把此前一次检查当原子通话保证。
			select {
			case <-s.source.LifetimeDone():
				s.fail(ErrCancelled)
				return Event{}, ErrCancelled
			default:
			}
			s.mu.Lock()
			if s.err != nil {
				err := s.err
				s.mu.Unlock()
				return Event{}, err
			}
			select {
			case <-s.source.LifetimeDone():
				s.mu.Unlock()
				s.fail(ErrCancelled)
				return Event{}, ErrCancelled
			default:
			}
			s.state.ResultsDelivered++
			s.deliveryPending = false
			s.maybeClosedLocked()
			s.mu.Unlock()
			return e, nil
		}
		complete := s.state.State == "completed"
		s.mu.Unlock()
		if complete {
			return Event{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-s.wake:
		case <-s.source.LifetimeDone():
			s.fail(ErrCancelled)
		}
	}
}

// Reap每次最多一次有界控制请求；unknown仍占槽，不能把ctx取消当Rust已停止。
func (s *Stream) Reap(ctx context.Context) bool {
	if s == nil || ctx == nil {
		return false
	}
	if !s.reaping.CompareAndSwap(false, true) {
		return s.Snapshot().ResourcesClosed
	}
	defer s.reaping.Store(false)
	s.mu.Lock()
	need := (s.finishRequested || s.err != nil) && !s.state.RXStopped
	query := s.queryCleanup
	closed := s.state.ResourcesClosed
	s.mu.Unlock()
	if closed || !need {
		return closed
	}
	stopped := s.source == nil || s.source.ConfirmedRetired()
	if !stopped {
		request, cancel := context.WithTimeout(ctx, acquireTimeout)
		var state string
		var err error
		if query {
			r, e := s.source.Status(request)
			err = e
			state = r.State
			if e == nil && (!r.OK || r.Type != "rx_state" || r.SubscriptionID != s.opts.SubscriptionID || r.Session == 0 || r.Session != s.source.Snapshot().Session) {
				err = ErrProtocol
			}
		} else {
			r, e := s.source.Unsubscribe(request)
			err = e
			state = r.State
			if e == nil && (!r.OK || r.Type != "rx_state" || r.SubscriptionID != s.opts.SubscriptionID || r.Session == 0 || r.Session != s.source.Snapshot().Session) {
				err = ErrProtocol
			}
		}
		cancel()
		stopped = err == nil && (state == "stopped" || state == "failed") || s.source.ConfirmedRetired()
		s.mu.Lock()
		s.queryCleanup = err != nil
		s.mu.Unlock()
	}
	s.mu.Lock()
	if stopped {
		s.state.RXStopped = true
		s.state.CleanupPending = false
	}
	s.maybeClosedLocked()
	closed = s.state.ResourcesClosed
	s.mu.Unlock()
	signal(s.wake)
	return closed
}
