package asr

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"rustswitch/control/internal/asr/wire"
	"rustswitch/control/internal/media"
)

// 本文件由独立审查者编写，只用内存输入与假Conn证明已发现的交错边界，不代替真实SIP/RTP/供应商验收。

// TestReviewDoneAbsoluteDeadlineWithoutLifecycle 不启动生命周期协程，模拟其未获调度，迟到DONE也必须按原截止拒绝。
func TestReviewDoneAbsoluteDeadlineWithoutLifecycle(t *testing.T) {
	for name, offset := range map[string]time.Duration{"已过期": -time.Second, "临界已到": 0, "期限内": time.Hour} {
		t.Run(name, func(t *testing.T) {
			s := newStream(context.Background(), Options{SubscriptionID: 7, SampleRate: 8000}, unitToken, nil)
			defer s.cancel()
			s.finishWritten = true
			s.finishDeadline = time.Now().Add(offset)
			s.state.State = "finishing"
			done, err := s.accept(unitMessage(t, wire.KindDone, s.finishValueLocked()))
			if offset <= 0 {
				if done || !errors.Is(err, ErrProviderTimeout) || s.Snapshot().ProviderDone || s.Snapshot().ProviderAcknowledgedSamples != 0 {
					t.Fatalf("生命周期未调度时迟到DONE被认作成功：done=%v err=%v state=%+v", done, err, s.Snapshot())
				}
			} else if !done || err != nil || !s.Snapshot().ProviderDone {
				t.Fatalf("合法期限内DONE被拒绝：%v %v", done, err)
			}
		})
	}
}

// reviewProgressConn 每次只写一个字节；可添加真实短延迟，但没有socket或其他进程。
// 所有收到的Deadline都记录，不能靠最终错误碰巧正确就忽略续期错误。
type reviewProgressConn struct {
	net.Conn
	deadlines      []time.Time
	delay          time.Duration
	written, calls int
}

func (c *reviewProgressConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return nil
}
func (c *reviewProgressConn) Write(b []byte) (int, error) {
	c.calls++
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	if len(c.deadlines) == 0 || !time.Now().Before(c.deadlines[len(c.deadlines)-1]) {
		return 0, os.ErrDeadlineExceeded
	}
	if len(b) == 0 {
		return 0, nil
	}
	c.written++
	return 1, nil
}

// TestReviewSummaryPartialWritesCannotRenewBudget 同一无观察到期摘要的每次部分写必须共享同一个20ms绝对上限。
func TestReviewSummaryPartialWritesCannotRenewBudget(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(map[bool]string{false: "少量快速进度", true: "慢进度超过总期限"}[slow], func(t *testing.T) {
			c := &reviewProgressConn{}
			if slow {
				c.delay = 4 * time.Millisecond
			}
			s := newStream(context.Background(), Options{}, unitToken, c)
			defer s.cancel()
			f := unitFrame(t, media.RXExportGap, 1)
			f.ObservationLowerBoundNS, f.ExpiresAtNS = 0, 0
			if _, err := f.RemainingBudget(); !errors.Is(err, media.ErrRXNoObservationBudget) {
				t.Fatal(err)
			}
			err := s.writeObserved(make([]byte, 12), &f)
			if len(c.deadlines) < 2 {
				t.Fatalf("未实际执行部分进度重试：%d，%v", len(c.deadlines), err)
			}
			for _, deadline := range c.deadlines[1:] {
				if !deadline.Equal(c.deadlines[0]) {
					t.Fatalf("同一摘要部分写续期：初始=%v 后续=%v", c.deadlines[0], deadline)
				}
			}
			if slow {
				if !errors.Is(err, ErrSubmissionUnknown) || c.written >= 12 || s.Snapshot().UnknownWrites != 1 {
					t.Fatalf("慢进度绕过总预算或错误重播：written=%d err=%v state=%+v", c.written, err, s.Snapshot())
				}
			} else if err != nil || c.written != 12 || s.Snapshot().UnknownWrites != 0 {
				t.Fatalf("预算内完整提交失败：written=%d err=%v", c.written, err)
			}
		})
	}
}

// reviewReplySource 通过窄Source契约返回语义矛盾的nil-error回复，检验清理层不会只认state字面值。
type reviewReplySource struct {
	*unitSource
	reply                   media.Reply
	unsubCalls, statusCalls int
}

func (u *reviewReplySource) Unsubscribe(context.Context) (media.Reply, error) {
	u.unsubCalls++
	return u.reply, nil
}
func (u *reviewReplySource) Status(context.Context) (media.Reply, error) {
	u.statusCalls++
	return u.reply, nil
}

// TestReviewReapRejectsNegativeAndWrongSession 负回复或另一会话的stopped不能回收；后续真实匹配终态才释放。
func TestReviewReapRejectsNegativeAndWrongSession(t *testing.T) {
	valid := media.Reply{OK: true, Type: "rx_state", Session: 1, SubscriptionID: 7, State: "stopped"}
	for name, mutate := range map[string]func(*media.Reply){
		"负OK":  func(r *media.Reply) { r.OK = false },
		"错误会话": func(r *media.Reply) { r.Session = 9 },
		"空会话":  func(r *media.Reply) { r.Session = 0 },
		"错误订阅": func(r *media.Reply) { r.SubscriptionID = 8 },
		"错误类型": func(r *media.Reply) { r.Type = "error" },
	} {
		t.Run(name, func(t *testing.T) {
			u := &reviewReplySource{unitSource: newUnitSource(), reply: valid}
			mutate(&u.reply)
			s := newStream(context.Background(), Options{SubscriptionID: 7, SampleRate: 8000}, unitToken, nil)
			s.source = u
			s.fail(ErrUnavailable)
			for range 2 {
				if s.Reap(context.Background()) || s.Snapshot().RXStopped || s.Snapshot().ResourcesClosed || !s.Snapshot().CleanupPending {
					t.Fatalf("矛盾停止回执释放了未知资源：%+v", s.Snapshot())
				}
				select {
				case <-s.Closed():
					t.Fatal("错误回执关闭了资源证明信号")
				default:
				}
			}
			if u.unsubCalls != 1 || u.statusCalls != 1 {
				t.Fatal("每次Reap未保持一个RPC或未知重查顺序")
			}
			u.reply = valid
			if !s.Reap(context.Background()) || !s.Snapshot().ResourcesClosed || s.Snapshot().State != "failed" {
				t.Fatal("真实停止没有收敛，或清理伪造识别成功")
			}
		})
	}
}

// reviewLifetimeSource 在第二次寿命检查时安排显式Cancel，确定地落在Next弹出结果之后。
// 这是Source测试实现，不向生产增加调度钩子，也不关闭仍然有效的通话寿命。
type reviewLifetimeSource struct {
	*unitSource
	mu       sync.Mutex
	checks   int
	onSecond func()
}

func (u *reviewLifetimeSource) LifetimeDone() <-chan struct{} {
	u.mu.Lock()
	u.checks++
	n := u.checks
	u.mu.Unlock()
	if n == 2 && u.onSecond != nil {
		u.onSecond()
	}
	return u.life
}

// TestReviewCancelBetweenResultPopAndDelivery 不论资源是否已关闭，显式Cancel均拒绝尚未真正交付的final。
func TestReviewCancelBetweenResultPopAndDelivery(t *testing.T) {
	for _, resourcesClosed := range []bool{false, true} {
		t.Run(map[bool]string{false: "仍有媒体资源", true: "I/O关闭但待交付"}[resourcesClosed], func(t *testing.T) {
			u := &reviewLifetimeSource{unitSource: newUnitSource()}
			s := newStream(context.Background(), Options{SubscriptionID: 7, SampleRate: 8000}, unitToken, nil)
			s.source = u
			s.state.State = "finishing"
			s.state.ProviderDone = true
			s.state.RXStopped, s.state.TransportClosed = resourcesClosed, resourcesClosed
			s.results[0] = Event{Type: "final", Text: "待交付旧结果"}
			s.resultCount = 1
			s.maybeClosedLocked()
			u.onSecond = s.Cancel
			e, err := s.Next(context.Background())
			if !errors.Is(err, ErrCancelled) || e != (Event{}) || s.Snapshot().ResultsDelivered != 0 || s.Snapshot().QueuedResults != 0 {
				t.Fatalf("Cancel后仍交付或计为交付：event=%+v err=%v state=%+v", e, err, s.Snapshot())
			}
			select {
			case <-u.life:
				t.Fatal("测试错误地用挂机代替显式Cancel")
			default:
			}
			if u.checks < 2 {
				t.Fatal("没有覆盖弹出后的交错")
			}
		})
	}
}

// TestReviewFinishStopsNewWriteReservation 直接测试writer真实的同锁预留方法，不引入只为测试存在的生产钩子。
func TestReviewFinishStopsNewWriteReservation(t *testing.T) {
	for _, priorInFlight := range []bool{false, true} {
		t.Run(map[bool]string{false: "Finish先于新预留", true: "Finish保留已有在途"}[priorInFlight], func(t *testing.T) {
			s := newStream(context.Background(), Options{SubscriptionID: 7, SampleRate: 8000}, unitToken, nil)
			defer s.cancel()
			s.inFlight = priorInFlight
			s.state.State = "streaming"
			s.state.AudioFrames, s.state.Samples = 3, 480
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := s.Finish(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			proof := s.proof
			invalidate, finish, err := s.reserveObserved(ptrReviewFrame(unitFrame(t, media.RXSourceBoundary, 1)))
			if err != nil || !finish || invalidate || s.inFlight != priorInFlight || s.proof != proof || s.Snapshot().AudioFrames != 3 || s.Snapshot().Samples != 480 {
				t.Fatalf("Finish后创建了新写预留或改变已在途前缀：finish=%v invalidate=%v err=%v inFlight=%v", finish, invalidate, err, s.inFlight)
			}
		})
	}
}

func ptrReviewFrame(f media.RXFrame) *media.RXFrame { return &f }

// TestReviewCompletionWaitsForActualResultDelivery 在结果出队和交付间触发同一完成裁决，不能提前completed。
func TestReviewCompletionWaitsForActualResultDelivery(t *testing.T) {
	u := &reviewLifetimeSource{unitSource: newUnitSource()}
	s := newStream(context.Background(), Options{SubscriptionID: 7, SampleRate: 8000}, unitToken, nil)
	defer s.cancel()
	s.source = u
	s.state.State = "finishing"
	s.state.ProviderDone, s.state.RXStopped, s.state.TransportClosed = true, true, true
	s.results[0] = Event{Type: "final", Text: "待最终交付"}
	s.resultCount = 1
	s.maybeClosedLocked()
	u.onSecond = func() {
		// 模拟在该交错点到达的ioDone/Reap完成裁决；Source寿命接口本身不关闭通话。
		s.mu.Lock()
		s.maybeClosedLocked()
		if !s.deliveryPending || s.state.State == "completed" || s.state.ResultsDelivered != 0 {
			t.Errorf("后置交付前出现完成证明：pending=%v state=%+v", s.deliveryPending, s.state)
		}
		s.mu.Unlock()
	}
	e, err := s.Next(context.Background())
	if err != nil || e.Type != "final" || s.Snapshot().State != "completed" || s.Snapshot().ResultsDelivered != 1 || s.deliveryPending {
		t.Fatalf("最终交付没有完成：event=%+v err=%v state=%+v", e, err, s.Snapshot())
	}
	s.Cancel()
	if s.Snapshot().State != "completed" || s.Snapshot().Error != "" || s.Snapshot().ResultsDelivered != 1 {
		t.Fatal("交付完成后的Cancel改写了历史完成证明")
	}
}
