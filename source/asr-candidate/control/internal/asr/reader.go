package asr

import (
	"errors"
	"net"
	"time"

	"rustswitch/control/internal/asr/wire"
)

// 帧首字节前可等待心跳，收到首字节后只有一次1秒总预算，半帧进度不能续期。
type messageReader struct {
	conn    net.Conn
	started bool
}

func (r *messageReader) Read(b []byte) (int, error) {
	n, err := r.conn.Read(b)
	if n > 0 && !r.started {
		r.started = true
		if e := r.conn.SetReadDeadline(time.Now().Add(frameTimeout)); e != nil && err == nil {
			err = e
		}
	}
	return n, err
}

func (s *Stream) reader() {
	defer s.ioDone()
	var buf [wire.MaxWireSize]byte
	for {
		if err := s.conn.SetReadDeadline(time.Now().Add(pingInterval + pongTimeout)); err != nil {
			s.fail(err)
			return
		}
		r := messageReader{conn: s.conn}
		m, err := wire.Read(&r, &buf)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				s.fail(ErrProviderTimeout)
			} else {
				s.fail(ErrProtocol)
			}
			return
		}
		// 代理可能比writer本地记账更快；同一状态锁下检查并接收，避免检查后又开始写入。
		var done bool
		for {
			if s.ctx.Err() != nil {
				return
			}
			done, err = s.accept(m)
			if !errors.Is(err, ErrBusy) {
				break
			}
			select {
			case <-s.commitWake:
			case <-s.ctx.Done():
				return
			}
		}
		if err != nil {
			s.fail(err)
			return
		}
		if done {
			s.stopIO()
			return
		}
	}
}

func (s *Stream) accept(m wire.Message) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if s.inFlight {
		return false, ErrBusy
	}
	if s.state.ProviderDone {
		return false, ErrProtocol
	}
	switch m.Kind {
	case wire.KindResult:
		var r wire.Result
		if wire.ParseJSON(m.Body, &r) != nil || r.StreamToken != s.token {
			return false, ErrProtocol
		}
		s.state.ResultsReceived++
		e, stale, err := s.proof.result(r)
		if err != nil {
			return false, err
		}
		if stale {
			s.state.StaleResults++
			return false, nil
		}
		if err = s.enqueueLocked(e); err != nil {
			return false, err
		}
		signal(s.wake)
	case wire.KindDone:
		var done wire.Finish
		if s.finishDeadline.IsZero() || !time.Now().Before(s.finishDeadline) {
			return false, ErrProviderTimeout
		}
		if wire.ParseJSON(m.Body, &done) != nil || !s.finishWritten || done != s.finishValueLocked() || s.proof.utterance != s.proof.closedUtterance {
			return false, ErrProtocol
		}
		s.state.ProviderDone = true
		s.state.ProviderAcknowledgedSamples = uint64(done.Samples)
		s.state.State = "finishing"
		return true, nil
	case wire.KindError:
		var e wire.Error
		if wire.ParseJSON(m.Body, &e) != nil || e.StreamToken != s.token {
			return false, ErrProtocol
		}
		// 供应商code不混入指标标签，也不允许任意文本错误复活当前流。
		return false, ErrUnavailable
	case wire.KindPong:
		var p wire.Ping
		if wire.ParseJSON(m.Body, &p) != nil || p.StreamToken != s.token || s.pongDeadline.IsZero() || uint64(p.Nonce) != s.pingNonce || !time.Now().Before(s.pongDeadline) {
			return false, ErrProtocol
		}
		s.pongDeadline = time.Time{}
		signal(s.lifecycleWake)
	default:
		return false, ErrProtocol
	}
	return false, nil
}

func (s *Stream) enqueueLocked(e Event) error {
	if e.Type == "partial" {
		for i := 0; i < s.resultCount; i++ {
			if s.results[i].Type == "partial" && s.results[i].UtteranceID == e.UtteranceID {
				s.results[i] = e
				s.state.PartialCoalesced++
				return nil
			}
		}
	}
	if s.resultCount == resultLimit {
		return ErrResultOverflow
	}
	s.results[s.resultCount] = e
	s.resultCount++
	return nil
}

func (s *Stream) invalidateLocked() error {
	e, invalid := s.proof.invalidate()
	n := 0
	for i := 0; i < s.resultCount; i++ {
		if s.results[i].Type != "partial" {
			s.results[n] = s.results[i]
			n++
		}
	}
	clear(s.results[n:])
	s.resultCount = n
	if invalid {
		s.state.PartialInvalidated++
		return s.enqueueLocked(e)
	}
	return nil
}

func (s *Stream) lifecycle() {
	defer s.ioDone()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		s.mu.Lock()
		deadline := s.finishDeadline
		if !s.pongDeadline.IsZero() && (deadline.IsZero() || s.pongDeadline.Before(deadline)) {
			deadline = s.pongDeadline
		}
		s.mu.Unlock()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer.Reset(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case <-s.source.LifetimeDone():
			s.fail(ErrCancelled)
			return
		case <-s.ctx.Done():
			s.mu.Lock()
			done := s.state.ProviderDone || s.err != nil
			s.mu.Unlock()
			if !done {
				s.fail(ErrCancelled)
			}
			return
		case <-s.lifecycleWake:
		case <-timeout:
			// 与同时到达的PONG/DONE重新在同一状态锁下裁决，避免旧timer误杀已结算状态。
			s.mu.Lock()
			now := time.Now()
			expired := !s.state.ProviderDone && ((!s.finishDeadline.IsZero() && !now.Before(s.finishDeadline)) || (!s.pongDeadline.IsZero() && !now.Before(s.pongDeadline)))
			if expired {
				s.failLocked(ErrProviderTimeout)
			}
			s.mu.Unlock()
			if expired {
				s.stopIO()
				return
			}
		}
	}
}
