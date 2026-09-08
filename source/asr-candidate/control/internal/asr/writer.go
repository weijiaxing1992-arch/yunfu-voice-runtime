package asr

import (
	"context"
	"errors"
	"io"
	"time"

	"rustswitch/control/internal/asr/wire"
	"rustswitch/control/internal/media"
)

func (s *Stream) writer() {
	defer s.ioDone()
	encoder := newFrameEncoder(s.opts.SampleRate, s.token)
	var buf [wire.MaxWireSize]byte
	// 每十秒复用一个读预算，不能每20ms创建context/timer或辅助协程。
	window := newReadWindow(s.inputCtx)
	defer window.close()
	for {
		f, err := s.source.Read(window.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			finish := s.finishRequested
			s.mu.Unlock()
			if finish {
				s.writeFinish(buf[:])
				return
			}
			if errors.Is(err, context.DeadlineExceeded) && s.inputCtx.Err() == nil {
				if err = s.writePing(buf[:]); err != nil {
					s.fail(err)
					return
				}
				window.renew(s.inputCtx)
				continue
			}
			if errors.Is(err, media.ErrRXExpired) {
				s.mu.Lock()
				s.state.ExpiredFrames++
				s.mu.Unlock()
			}
			s.fail(ErrInputFailed)
			return
		}
		// Finish到达时停止提交刚读出的新帧；不把已在途的完整前缀重新发送。
		s.mu.Lock()
		finish := s.finishRequested
		s.mu.Unlock()
		if finish {
			s.writeFinish(buf[:])
			return
		}
		if f.SubscriptionID != s.opts.SubscriptionID {
			s.fail(ErrProtocol)
			return
		}
		if f.Kind == media.RXEnd || f.Kind == media.RXExportFailed || f.Kind == media.RXObservationFailed {
			s.fail(ErrInputFailed)
			return
		}
		if err = f.CheckFresh(); err != nil {
			s.expired(err)
			return
		}
		b, err := encoder.encode(&f, buf[:])
		if err != nil {
			s.fail(err)
			return
		}
		if err = f.CheckFresh(); err != nil {
			s.expired(err)
			return
		}
		invalidate, finish, err := s.reserveObserved(&f)
		if finish {
			s.writeFinish(buf[:])
			return
		}
		if err != nil {
			s.fail(err)
			return
		}
		err = s.writeObserved(b, &f)
		s.mu.Lock()
		if err == nil && s.err == nil {
			if invalidate {
				err = s.invalidateLocked()
			}
			if err == nil {
				_, err = s.proof.note(&f)
			}
			if err == nil {
				s.state.LastEventSequence = f.Sequence
				if f.Kind == media.RXDecoded {
					s.state.AudioFrames++
					s.state.Samples += uint64(s.opts.SampleRate / 50)
				} else {
					s.state.Markers++
				}
			}
		}
		s.inFlight = false
		s.mu.Unlock()
		signal(s.commitWake)
		signal(s.wake)
		if err != nil {
			s.fail(err)
			return
		}
		if s.ctx.Err() != nil {
			return
		}
	}
}

// readWindow集中拥有当前十秒读预算；更新先释放旧预算，退出只需释放当前预算。
// 将所有权保存在固定对象，避免重赋值局部cancel后容易漏掉某条return路径。
type readWindow struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func newReadWindow(parent context.Context) *readWindow {
	ctx, cancel := context.WithTimeout(parent, pingInterval)
	return &readWindow{ctx: ctx, cancel: cancel}
}
func (w *readWindow) close() { w.cancel() }
func (w *readWindow) renew(parent context.Context) {
	w.cancel()
	w.ctx, w.cancel = context.WithTimeout(parent, pingInterval)
}

// reserveObserved是停止意图与新写入的共同裁决点，不允许转换期间到达的Finish被越过。
func (s *Stream) reserveObserved(f *media.RXFrame) (invalidate, finish bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, false, s.err
	}
	if s.finishRequested {
		return false, true, nil
	}
	if s.inFlight {
		return false, false, ErrBusy
	}
	preview := s.proof
	invalidate, err = preview.note(f)
	if err == nil {
		s.inFlight = true
	}
	return invalidate, false, err
}

func (s *Stream) expired(err error) {
	if errors.Is(err, media.ErrRXExpired) {
		s.mu.Lock()
		s.state.ExpiredFrames++
		s.mu.Unlock()
	}
	s.fail(err)
}

func (s *Stream) writeObserved(b []byte, f *media.RXFrame) error {
	written := 0
	controlDeadline := time.Now().Add(20 * time.Millisecond)
	for written < len(b) {
		// 先取Go本地时刻再求原始剩余预算，防止两次取时之间抢占而延长截止。
		now := time.Now()
		remaining, err := f.RemainingBudget()
		if errors.Is(err, media.ErrRXNoObservationBudget) {
			remaining = controlDeadline.Sub(now)
			err = nil
			if remaining <= 0 {
				err = ErrProviderTimeout
			}
		}
		if err != nil {
			if errors.Is(err, media.ErrRXExpired) {
				s.mu.Lock()
				s.state.ExpiredFrames++
				s.mu.Unlock()
			}
			if written > 0 {
				return s.unknownWrite()
			}
			return err
		}
		if remaining > 20*time.Millisecond {
			remaining = 20 * time.Millisecond
		}
		if err = s.conn.SetWriteDeadline(now.Add(remaining)); err != nil {
			if written > 0 {
				return s.unknownWrite()
			}
			return err
		}
		n, err := s.conn.Write(b[written:])
		if n < 0 || n > len(b)-written {
			return s.unknownWrite()
		}
		written += n
		if err != nil || n == 0 {
			if written > 0 {
				return s.unknownWrite()
			}
			if err != nil {
				return err
			}
			return io.ErrNoProgress
		}
	}
	return nil
}

func (s *Stream) unknownWrite() error {
	s.mu.Lock()
	s.state.UnknownWrites++
	s.mu.Unlock()
	return ErrSubmissionUnknown
}

func (s *Stream) controlWrite(kind wire.Kind, value any, buf []byte) error {
	m, err := wire.JSONMessage(kind, value)
	if err != nil {
		return ErrProtocol
	}
	b, err := wire.Encode(buf, m)
	if err != nil {
		return ErrProtocol
	}
	if err = s.conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		return err
	}
	// 控制消息也不重发不确定前缀；关闭连接由调用者完成。
	written := 0
	for written < len(b) {
		n, e := s.conn.Write(b[written:])
		if n < 0 || n > len(b)-written {
			return s.unknownWrite()
		}
		written += n
		if e != nil || n == 0 {
			if written > 0 {
				return s.unknownWrite()
			}
			if e != nil {
				return e
			}
			return io.ErrNoProgress
		}
	}
	return nil
}

func (s *Stream) finishValueLocked() wire.Finish {
	return wire.Finish{StreamToken: s.token, LastEventSeq: wire.U64(s.state.LastEventSequence), AudioFrames: wire.U64(s.state.AudioFrames), Samples: wire.U64(s.state.Samples), Markers: wire.U64(s.state.Markers)}
}

func (s *Stream) writeFinish(buf []byte) {
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return
	}
	s.inFlight = true
	value := s.finishValueLocked()
	s.mu.Unlock()
	err := s.controlWrite(wire.KindFinish, value, buf)
	s.mu.Lock()
	if err == nil && s.err == nil {
		s.finishWritten = true
		s.state.InputFinished = true
		s.state.CleanupPending = !s.state.RXStopped
		close(s.finishSent)
	}
	s.inFlight = false
	s.mu.Unlock()
	signal(s.commitWake)
	signal(s.wake)
	if err != nil {
		s.fail(err)
	}
}

func (s *Stream) writePing(buf []byte) error {
	s.mu.Lock()
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return err
	}
	if !s.pongDeadline.IsZero() {
		s.mu.Unlock()
		return ErrProviderTimeout
	}
	s.pingNonce++
	nonce := s.pingNonce
	s.pongDeadline = time.Now().Add(pongTimeout)
	s.mu.Unlock()
	signal(s.lifecycleWake)
	return s.controlWrite(wire.KindPing, wire.Ping{StreamToken: s.token, Nonce: wire.U64(nonce)}, buf)
}
