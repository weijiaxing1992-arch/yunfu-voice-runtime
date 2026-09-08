package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"rustswitch/control/internal/media"
)

func expectRXLifetime(t *testing.T, done <-chan struct{}, ended bool) {
	t.Helper()
	if done == nil {
		t.Fatal("生命期信号不能是nil")
	}
	select {
	case <-done:
		if !ended {
			t.Fatal("输入结束被错误当成通话结束")
		}
	default:
		if ended {
			t.Fatal("真实授权结束后生命期未及时关闭")
		}
	}
}

// 主动结束输入之后结果窗口仍存在；通话挂断钩子必须先于媒体Release关闭此窗口。
// 本用例是实际Server方法的纯单元验证，不创建SIP/RTP或供应商连接。
func TestRXLifetimeUnsubscribeKeepsFinalWindowUntilCallEnds(t *testing.T) {
	s, c, transport, _ := rxSDKFixture(t)
	h, _ := rxSDKBegin(t, s, c, transport, 1)
	done := h.LifetimeDone()
	expectRXLifetime(t, done, false)
	if reply, err := h.Unsubscribe(context.Background()); err != nil || reply.State != "stopped" {
		t.Fatal(reply, err)
	}
	expectRXLifetime(t, done, false)
	if h.Retired() || !h.terminal.Load() {
		t.Fatal("退订状态与通话授权混淆")
	}
	if _, err := h.Read(context.Background()); !errors.Is(err, ErrRXHandleClosed) {
		t.Fatal("退订仍能继续收音", err)
	}
	if reply, err := h.Status(context.Background()); err != nil || reply.State != "stopped" {
		t.Fatal("清理查询丢失", reply, err)
	}
	// 这一钩子由真实Server.finish在通话结束时调用，未来ASR Finish不调用它。
	s.retireCallRX(c, false)
	expectRXLifetime(t, done, true)
	if h.LifetimeDone() != done || h.Retired() {
		t.Fatal("挂断提前伪造Release或更换生命期")
	}
	if _, err := h.Status(context.Background()); err != nil {
		t.Fatal("挂断后清理身份丢失", err)
	}
	s.retireCallRX(c, true)
	expectRXLifetime(t, done, true)
}

func TestRXLifetimeReplacementClosesOnlyOldHandle(t *testing.T) {
	s, c, transport, _ := rxSDKFixture(t)
	old, _ := rxSDKBegin(t, s, c, transport, 1)
	done := old.LifetimeDone()
	if _, err := old.Unsubscribe(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectRXLifetime(t, done, false)
	next, _ := rxSDKBegin(t, s, c, transport, 2)
	expectRXLifetime(t, done, true)
	expectRXLifetime(t, next.LifetimeDone(), false)
	late := newRXSDKFakeSub(c.ID, 1)
	old.bind(late)
	expectRXLifetime(t, old.LifetimeDone(), true)
	select {
	case <-late.revoked:
	default:
		t.Fatal("替换后的迟到绑定复活读权")
	}
	if old.LifetimeDone() != done {
		t.Fatal("旧句柄创建新生命期")
	}
}

func TestRXLifetimeServiceCancellationAndClose(t *testing.T) {
	for _, method := range []string{"context", "close_rx"} {
		t.Run(method, func(t *testing.T) {
			s, c, transport, _ := rxSDKFixture(t)
			h, _ := rxSDKBegin(t, s, c, transport, 1)
			done := h.LifetimeDone()
			if method == "context" {
				s.cancel()
			} else {
				s.closeRX()
			}
			expectRXLifetime(t, done, true)
			if h.LifetimeDone() != done {
				t.Fatal("关闭后替换信号")
			}
		})
	}
	var absent *RXHandle
	expectRXLifetime(t, absent.LifetimeDone(), true)
	empty := &RXHandle{}
	expectRXLifetime(t, empty.LifetimeDone(), true)
}

func TestRXLifetimeWorkerFailureIsGenerationScoped(t *testing.T) {
	s, c, transport, proof := rxSDKFixture(t)
	old, _ := rxSDKBegin(t, s, c, transport, 1)
	done := old.LifetimeDone()
	other := *c
	other.ID, other.Generation = 2, 2
	other.compatUUIDs = [2]string{"lifetime-new-generation", ""}
	other.rxHandle, other.rxPending = nil, nil
	s.calls[other.ID] = &other
	s.compatChannels[other.compatUUIDs[0]] = compatibilityChannel{call: &other, side: 0}
	proof.Generation = 2
	next, _ := rxSDKBegin(t, s, &other, transport, 1)
	// 已结束但资源未释放的旧通话，调用真实代次故障处理；不生成任何网络BYE。
	c.Ended = true
	s.workerFailed(media.Failure{Worker: 0, Generation: 1})
	expectRXLifetime(t, done, true)
	expectRXLifetime(t, next.LifetimeDone(), false)
	if !old.Retired() || next.Retired() {
		t.Fatal("故障跨代影响了新句柄")
	}
}

func TestRXLifetimeUnknownAndLateReplyCannotRevive(t *testing.T) {
	for _, failure := range []string{"hangup", "generation", "service"} {
		t.Run(failure, func(t *testing.T) {
			s, c, transport, worker := rxSDKFixture(t)
			r := rxSDKRequest(c, 1)
			s.handleRXSubscribe(r)
			a := rxSDKAttemptWait(t, transport)
			h := r.published.Load()
			done := h.LifetimeDone()
			expectRXLifetime(t, done, false)
			switch failure {
			case "hangup":
				c.Ended = true
				s.retireCallRX(c, false)
			case "generation":
				worker.Generation++
			case "service":
				s.cancel()
			}
			if failure == "service" {
				s.rx.workers.Wait()
				expectRXLifetime(t, done, true)
				return
			}
			a.complete <- rxSubscribeResult{reply: a.sub.reply(), err: media.ErrRXOutcomeUnknown}
			rxSDKProcess(t, s)
			result := <-r.reply
			if result.handle != h || result.err == nil || h.authorized.Load() {
				t.Fatal("未知回执获得了授权", result)
			}
			expectRXLifetime(t, done, true)
			if h.LifetimeDone() != done {
				t.Fatal("迟到绑定创建新的生命期")
			}
			select {
			case <-a.sub.revoked:
			default:
				t.Fatal("迟到绑定没有撤销读权")
			}
			if reply, err := h.Status(context.Background()); err != nil || reply.State != "active" {
				t.Fatal("未知结果丢失清理地址", reply, err)
			}
		})
	}
}

// 并发订阅取消/通话退休/迟到绑定，所有消费者必须看到同一关闭信号，不访问Call map。
func TestRXLifetimeConcurrentAccessRetireAndLateBind(t *testing.T) {
	for round := 0; round < 64; round++ {
		ctx, cancel := context.WithCancel(context.Background())
		h := &RXHandle{service: ctx}
		sub := newRXSDKFakeSub(1, 1)
		start := make(chan struct{})
		seen := make(chan (<-chan struct{}), 16)
		var workers sync.WaitGroup
		for i := 0; i < 16; i++ {
			workers.Add(1)
			go func() { defer workers.Done(); <-start; seen <- h.LifetimeDone() }()
		}
		workers.Add(3)
		go func() { defer workers.Done(); <-start; h.retire() }()
		go func() { defer workers.Done(); <-start; h.bind(sub) }()
		go func() { defer workers.Done(); <-start; h.revoke(); cancel() }()
		close(start)
		workers.Wait()
		close(seen)
		for done := range seen {
			if done != h.LifetimeDone() {
				t.Fatal("并发生命期信号发生替换")
			}
			expectRXLifetime(t, done, true)
		}
		select {
		case <-sub.revoked:
		default:
			t.Fatal("退休竞态遗留可读绑定")
		}
	}
}

func TestRXLifetimeBlockedWaitWakesOnCallEnd(t *testing.T) {
	s, c, transport, _ := rxSDKFixture(t)
	h, _ := rxSDKBegin(t, s, c, transport, 1)
	done := h.LifetimeDone()
	woke := make(chan struct{})
	go func() { <-done; close(woke) }()
	if _, err := h.Unsubscribe(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-woke:
		t.Fatal("正常结束输入唤醒了通话结束监听")
	default:
	}
	s.retireCallRX(c, false)
	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("通话结束没有唤醒等待结果的适配器")
	}
	if n := testing.AllocsPerRun(100, func() { _ = h.LifetimeDone() }); n != 0 {
		t.Fatal("查询生命期重复分配", n)
	}
}
