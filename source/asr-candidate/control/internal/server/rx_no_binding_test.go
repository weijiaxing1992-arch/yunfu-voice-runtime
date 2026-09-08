package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"rustswitch/control/internal/asr"
	"rustswitch/control/internal/media"
)

// rxResolvedNilTransport 模拟真实Pool在请求已取消、尚未产生资源时返回nil；不进行网络或伪造成功媒体回执。
type rxResolvedNilTransport struct{ entered chan struct{} }

func (f rxResolvedNilTransport) subscribe(ctx context.Context, _ int, _, _, _ uint64) (rxSubscription, media.Reply, error) {
	close(f.entered)
	<-ctx.Done()
	return nil, media.Reply{}, ctx.Err()
}

// TestRXNoBindingActualCompletionRetiresCancelledUnknown 回归审查发现的未知额度永久占用：实际nil结果必须收敛旧清理身份。
func TestRXNoBindingActualCompletionRetiresCancelledUnknown(t *testing.T) {
	s, c, _, _ := rxSDKFixture(t)
	f := rxResolvedNilTransport{entered: make(chan struct{})}
	s.rx.transport = f
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan rxSubscribeResult, 1)
	go func() {
		h, r, err := s.SubscribeRX(ctx, c.compatUUIDs[0], 1)
		returned <- rxSubscribeResult{handle: h, reply: r, err: err}
	}()
	r := <-s.rx.requests
	s.handleRXSubscribe(r)
	<-f.entered
	cancel()
	result := <-returned
	if result.handle == nil || !errors.Is(result.err, ErrRXSubscribeUnknown) {
		t.Fatal("没有进入已发布unknown路径", result)
	}
	rxSDKProcess(t, s)
	h := result.handle
	source := &asrRXSource{handle: h}
	if h.binding.Load() != nil || !h.noBindingConfirmed.Load() || !source.ConfirmedRetired() || !source.Snapshot().Retired || source.Snapshot().Pending {
		t.Fatal("完整nil结果没有收敛实际无资源证据")
	}
	if s.rx.pending != 0 || c.rxPending != nil || s.ctx.Err() != nil {
		t.Fatal("收敛依赖服务关闭或遗漏控制额度")
	}
	// 已退休句柄仍然拒绝媒体控制；新证明只用于资源归还，不能重新开放旧订阅。
	if _, err := source.Unsubscribe(context.Background()); !errors.Is(err, ErrRXHandleRetired) {
		t.Fatal(err)
	}
}

// rxCancelAtPublishedContext 在前置参数检查后立即报告取消，确定覆盖“已发布句柄、尚未入job队列”的分支。
type rxCancelAtPublishedContext struct {
	context.Context
	checks atomic.Int32
}

func (c *rxCancelAtPublishedContext) Err() error {
	if c.checks.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

// TestRXNoBindingKnownUnsubmittedBranches 覆盖真实代码的发布后取消和job队列拒绝；两者没有媒体执行者可再取得任务。
func TestRXNoBindingKnownUnsubmittedBranches(t *testing.T) {
	for _, name := range []string{"published_then_cancelled", "job_queue_full"} {
		t.Run(name, func(t *testing.T) {
			s, c, _, proof := rxSDKFixture(t)
			// 独立新服务不启动worker，仅使明确拒绝分支确定可重现；原夹具worker由原cleanup正常退出。
			s.rx = &rxService{ctx: s.ctx, jobs: make(chan rxSubscribeJob, 1), workerSnapshot: func(int) (media.CapabilitySnapshot, bool) { return *proof, true }}
			r := rxSDKRequest(c, 1)
			if name == "published_then_cancelled" {
				r.ctx = &rxCancelAtPublishedContext{Context: context.Background()}
			} else {
				s.rx.jobs <- rxSubscribeJob{}
			}
			s.handleRXSubscribe(r)
			result := <-r.reply
			h := r.published.Load()
			if h == nil || result.err == nil || h.binding.Load() != nil || !h.noBindingConfirmed.Load() {
				t.Fatal("明确未提交路径没有资源证明")
			}
			source := &asrRXSource{handle: h}
			if !source.ConfirmedRetired() || source.Snapshot().Pending || s.rx.pending != 0 || c.rxPending != nil {
				t.Fatal("未提交仍占资源/控制额度")
			}
		})
	}
}

// TestRXNoBindingCancellationAloneNeverProvesRetirement 普通撤权、服务取消以及尚无回执必须保持未知；迟到真实绑定也不构成退休。
func TestRXNoBindingCancellationAloneNeverProvesRetirement(t *testing.T) {
	for _, name := range []string{"ordinary_retire", "service_cancel", "late_live_binding"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := &RXHandle{service: ctx, session: 1, subscriptionID: 1}
			h.initLifetime()
			defer h.endLifetime()
			if name == "service_cancel" {
				cancel()
			} else {
				h.retire()
			}
			source := &asrRXSource{handle: h}
			if source.ConfirmedRetired() || source.Snapshot().Retired || !source.Snapshot().Pending {
				t.Fatal("无回执的撤权被错当退休")
			}
			if name == "late_live_binding" {
				sub := newRXSDKFakeSub(1, 1)
				h.resolveBinding(sub)
				if source.ConfirmedRetired() || h.noBindingConfirmed.Load() {
					t.Fatal("迟到活动绑定被错当无资源")
				}
				sub.retired.Store(true)
				if !source.ConfirmedRetired() {
					t.Fatal("底层真实退休未被观察")
				}
			} else {
				h.resolveBinding(nil)
				if !source.ConfirmedRetired() || source.Snapshot().Pending {
					t.Fatal("迟到nil完成结果仍未收敛")
				}
			}
		})
	}
}

// TestRXNoBindingManagerReleasesOnlyAfterActualResolution 将真实Source退休证据接入管理器清理，unknown先占槽、确证后归还。
func TestRXNoBindingManagerReleasesOnlyAfterActualResolution(t *testing.T) {
	h := &RXHandle{service: context.Background(), session: 1, subscriptionID: 1}
	h.initLifetime()
	defer h.endLifetime()
	h.revoke()
	source := &asrRXSource{handle: h}
	life := newASRManagerFake()
	life.reap = func(context.Context) bool {
		if source.ConfirmedRetired() {
			life.confirmClose()
			return true
		}
		return false
	}
	m := asrManagerFixture(t, 1, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		return asrManagedStream{life: life}, ErrRXSubscribeUnknown
	})
	if _, err := m.startStream(context.Background(), "resolved", 1); !errors.Is(err, ErrRXSubscribeUnknown) {
		t.Fatal(err)
	}
	m.collectOne()
	if m.snapshot().Slots != 1 {
		t.Fatal("尚无真实回执提前归还")
	}
	h.retire()
	m.collectOne()
	if m.snapshot().Slots != 1 {
		t.Fatal("本地撤权提前归还")
	}
	h.resolveBinding(nil)
	m.collectOne()
	if m.snapshot().Slots != 0 || !channelClosed(life.Closed()) {
		t.Fatal("真实nil结果仍然泄漏ASR额度")
	}
}
