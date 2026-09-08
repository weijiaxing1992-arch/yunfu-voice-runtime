package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rustswitch/control/internal/asr"
	"rustswitch/control/internal/config"
)

// 本文件使用明确的假供应商生命周期，只证明管理器并发、配额与清理顺序；不提供 SIP/RTP/网络通过证据。
type asrManagerFake struct {
	mu          sync.Mutex
	state       asr.Snapshot
	closed      chan struct{}
	once        sync.Once
	reap        func(context.Context) bool
	cancelCalls atomic.Int32
	reapCalls   atomic.Int32
}

func newASRManagerFake() *asrManagerFake {
	return &asrManagerFake{state: asr.Snapshot{State: "streaming"}, closed: make(chan struct{})}
}
func (f *asrManagerFake) Cancel() {
	f.cancelCalls.Add(1)
	f.mu.Lock()
	f.state.State = "cancelled"
	f.state.CleanupPending = true
	f.mu.Unlock()
}
func (f *asrManagerFake) Reap(ctx context.Context) bool {
	f.reapCalls.Add(1)
	if f.reap != nil {
		return f.reap(ctx)
	}
	return channelClosed(f.closed)
}
func (f *asrManagerFake) Closed() <-chan struct{} { return f.closed }
func (f *asrManagerFake) Snapshot() asr.Snapshot  { f.mu.Lock(); defer f.mu.Unlock(); return f.state }
func (f *asrManagerFake) confirmClose() {
	f.mu.Lock()
	f.state.ResourcesClosed = true
	f.state.CleanupPending = false
	f.mu.Unlock()
	f.once.Do(func() { close(f.closed) })
}

func asrManagerFixture(t *testing.T, limit int, starter asrStarter) *asrManager {
	t.Helper()
	m := newASRManager(context.Background(), config.ASRStream{SocketPath: "/not-created/provider.sock", SampleRate: 8000, MaxStreams: limit}, starter)
	m.acquire = func(context.Context, string, uint64) (asr.Source, error) { return nil, ErrRXNotEligible }
	t.Cleanup(m.beginClose)
	return m
}

func asrAwait(t *testing.T, condition func() bool) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(until) {
			t.Fatal("有界等待没有达到预期状态")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestASRManagerDisabledAndIdentity 在起建前拒绝非法身份/已取消请求；任何拒绝都不创建流或占额度。
func TestASRManagerDisabledAndIdentity(t *testing.T) {
	s := &Server{}
	if h, err := s.StartASR(context.Background(), "call", 1); h != nil || !errors.Is(err, ErrASRDisabled) {
		t.Fatal(h, err)
	}
	if got := s.ASRSnapshot(); got != (ASRStatus{}) {
		t.Fatal(got)
	}
	var calls atomic.Int32
	m := asrManagerFixture(t, 1, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		calls.Add(1)
		return asrManagedStream{}, ErrRXNotEligible
	})
	for name, uuid := range map[string]string{"空": "", "超长": strings.Repeat("a", 129), "空格": "a b", "TAB": "a\tb", "换行": "a\nb", "回车": "a\rb", "NUL": "a\x00b", "非UTF8": "a\xff"} {
		t.Run(name, func(t *testing.T) {
			if _, err := m.startStream(context.Background(), uuid, 1); !errors.Is(err, ErrASRInvalid) {
				t.Fatal(err)
			}
		})
	}
	if _, err := m.startStream(context.Background(), "call", 0); !errors.Is(err, ErrASRInvalid) {
		t.Fatal(err)
	}
	if _, err := m.startStream(nil, "call", 1); !errors.Is(err, ErrASRInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.startStream(ctx, "call", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 || m.snapshot().Slots != 0 {
		t.Fatal("前置拒绝触发了启动或预留")
	}
}

// TestASRManagerStartingReservesBothUUIDAndCapacity 包含被供应商握手阻塞的起建流，满额与相同UUID必须立即拒绝。
func TestASRManagerStartingReservesBothUUIDAndCapacity(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	m := asrManagerFixture(t, 1, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		close(entered)
		<-release
		return asrManagedStream{}, asr.ErrUnavailable
	})
	result := make(chan error, 1)
	go func() { _, err := m.startStream(context.Background(), "a", 1); result <- err }()
	<-entered
	if s := m.snapshot(); s.Slots != 1 || s.Starting != 1 || s.Active != 0 {
		t.Fatal(s)
	}
	if _, err := m.startStream(context.Background(), "a", 2); !errors.Is(err, ErrASRBusy) {
		t.Fatal(err)
	}
	if _, err := m.startStream(context.Background(), "b", 1); !errors.Is(err, ErrASRCapacity) {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := <-result; !errors.Is(err, asr.ErrUnavailable) {
		t.Fatal(err)
	}
	if s := m.snapshot(); s.Slots != 0 || s.RejectedCapacity != 1 || s.RejectedUUID != 1 {
		t.Fatal(s)
	}
}

// TestASRManagerUnknownRetainsQuota nil与非nil的失败必须区别对待；Reap的布尔返回不能伪造Closed证明。
func TestASRManagerUnknownRetainsQuota(t *testing.T) {
	f := newASRManagerFake()
	f.reap = func(context.Context) bool { return true }
	m := asrManagerFixture(t, 1, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		return asrManagedStream{life: f}, ErrRXSubscribeUnknown
	})
	got, err := m.startStream(context.Background(), "a", 1)
	if got.life != f || !errors.Is(err, ErrRXSubscribeUnknown) || f.cancelCalls.Load() != 1 {
		t.Fatal(got, err)
	}
	if s := m.snapshot(); s.Slots != 1 || s.CleanupPending != 1 || s.Starting != 0 {
		t.Fatal(s)
	}
	for i := 0; i < 3; i++ {
		m.collectOne()
		if m.snapshot().Slots != 1 {
			t.Fatal("未知清理释放了额度")
		}
	}
	if f.reapCalls.Load() != 3 {
		t.Fatal("每次清理没有保持单次调用")
	}
	if _, err := m.startStream(context.Background(), "a", 2); !errors.Is(err, ErrASRBusy) {
		t.Fatal(err)
	}
	if _, err := m.startStream(context.Background(), "b", 2); !errors.Is(err, ErrASRCapacity) {
		t.Fatal(err)
	}
	f.confirmClose()
	m.collectOne()
	if m.snapshot().Slots != 0 {
		t.Fatal("真实关闭未归还额度")
	}
}

// TestASRManagerActiveScanBudget 大量活跃流不会每个消耗一次百毫秒清理节拍，也不会一次扫描无限槽。
func TestASRManagerActiveScanBudget(t *testing.T) {
	var streams []*asrManagerFake
	m := asrManagerFixture(t, 130, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		f := newASRManagerFake()
		streams = append(streams, f)
		return asrManagedStream{life: f}, nil
	})
	for i := range 130 {
		if _, err := m.startStream(context.Background(), fmt.Sprintf("scan-%d", i), 1); err != nil {
			t.Fatal(err)
		}
	}
	last := streams[129]
	last.Cancel()
	last.reap = func(context.Context) bool { last.confirmClose(); return true }
	m.collectOne()
	if m.cursor != 64 || last.reapCalls.Load() != 0 {
		t.Fatal("第一批超出六十四槽扫描预算")
	}
	m.collectOne()
	if m.cursor != 128 || last.reapCalls.Load() != 0 {
		t.Fatal("第二批没有从原游标继续")
	}
	m.collectOne()
	if last.reapCalls.Load() != 1 || m.snapshot().Slots != 129 {
		t.Fatal("活跃槽拖延了末尾失败流")
	}
	for _, f := range streams[:129] {
		if f.reapCalls.Load() != 0 {
			t.Fatal("扫描活跃流触发了媒体RPC")
		}
	}
}

// TestASRManagerActiveAndFinishingCollection 正常音频期不轮询/退订媒体，结束期才清理，并保留待交付最终结果的区别。
func TestASRManagerActiveAndFinishingCollection(t *testing.T) {
	f := newASRManagerFake()
	m := asrManagerFixture(t, 1, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		return asrManagedStream{life: f}, nil
	})
	if _, err := m.startStream(context.Background(), "a", 1); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		m.collectOne()
	}
	if f.reapCalls.Load() != 0 || m.snapshot().Active != 1 {
		t.Fatal("活跃流被主动清理")
	}
	f.mu.Lock()
	f.state.State = "finishing"
	f.mu.Unlock()
	m.collectOne()
	if f.reapCalls.Load() != 1 || m.snapshot().Active != 1 {
		t.Fatal("结束期没有进入有界清理")
	}
	f.confirmClose()
	m.collectOne()
	if m.snapshot().Slots != 0 {
		t.Fatal("关闭网络/RX后仍错误占资源额度")
	}
	if f.Snapshot().State != "finishing" {
		t.Fatal("管理器把待final交付改成识别完成")
	}
}

// TestASRManagerCancellationAfterAcceptedSource 启动刚返回时调用方已取消，非nil所有者必须关闭网络但保留清理身份。
func TestASRManagerCancellationAfterAcceptedSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newASRManagerFake()
	m := asrManagerFixture(t, 1, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		cancel()
		return asrManagedStream{life: f}, nil
	})
	got, err := m.startStream(ctx, "a", 1)
	if got.life != f || !errors.Is(err, context.Canceled) || f.cancelCalls.Load() != 1 || m.snapshot().CleanupPending != 1 {
		t.Fatal(got, err, m.snapshot())
	}
}

// TestASRManagerFrozenOptionsAndAcquire 仅复用正确UUID/订阅授权；路径与输出率来自冻结副本，不能由每通话变更。
func TestASRManagerFrozenOptionsAndAcquire(t *testing.T) {
	var options asr.Options
	var acquired bool
	m := asrManagerFixture(t, 1, func(ctx, service context.Context, o asr.Options, a asr.Acquire) (asrManagedStream, error) {
		options = o
		_, err := a(ctx)
		if err != ErrRXNotEligible {
			t.Fatal(err)
		}
		return asrManagedStream{}, err
	})
	m.acquire = func(ctx context.Context, uuid string, id uint64) (asr.Source, error) {
		if uuid != "中文呼叫" || id != 99 || ctx.Err() != nil {
			t.Fatal(uuid, id)
		}
		acquired = true
		return nil, ErrRXNotEligible
	}
	_, err := m.startStream(context.Background(), "中文呼叫", 99)
	if !errors.Is(err, ErrRXNotEligible) || !acquired || options != (asr.Options{SocketPath: "/not-created/provider.sock", UUID: "中文呼叫", SubscriptionID: 99, SampleRate: 8000}) {
		t.Fatal(options, err)
	}
}

// TestASRManagerConcurrentAdmission 多个调用者并发争用同一硬预算时，获批数不得超过起建/活动共享上限。
func TestASRManagerConcurrentAdmission(t *testing.T) {
	const limit = 8
	var accepted atomic.Int32
	var all sync.Mutex
	var streams []*asrManagerFake
	m := asrManagerFixture(t, limit, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		f := newASRManagerFake()
		all.Lock()
		streams = append(streams, f)
		all.Unlock()
		accepted.Add(1)
		return asrManagedStream{life: f}, nil
	})
	var wg sync.WaitGroup
	for i := range 96 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.startStream(context.Background(), fmt.Sprintf("call-%d", i), 1)
			if err != nil && !errors.Is(err, ErrASRCapacity) {
				t.Errorf("意外拒绝：%v", err)
			}
			_ = m.snapshot()
		}()
	}
	wg.Wait()
	if accepted.Load() != limit || m.snapshot().Slots != limit || m.snapshot().RejectedCapacity != 96-limit {
		t.Fatal(accepted.Load(), m.snapshot())
	}
	for _, f := range streams {
		f.confirmClose()
	}
	for range limit {
		m.collectOne()
	}
	if m.snapshot().Slots != 0 {
		t.Fatal(m.snapshot())
	}
}

// TestASRManagerTwoPhaseClose 验证网络取消不等待未知RX；只有池真实关闭后才释放并加入固定清理执行者。
func TestASRManagerTwoPhaseClose(t *testing.T) {
	f := newASRManagerFake()
	m := asrManagerFixture(t, 1, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		return asrManagedStream{life: f}, nil
	})
	f.reap = func(ctx context.Context) bool {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > asrCleanupTimeout {
			t.Error("清理没有单次截止")
		}
		if m.poolClosed.Load() {
			f.confirmClose()
			return true
		}
		return false
	}
	m.startWorkers()
	if _, err := m.startStream(context.Background(), "a", 1); err != nil {
		t.Fatal(err)
	}
	m.beginClose()
	asrAwait(t, func() bool { return f.reapCalls.Load() > 0 })
	if f.cancelCalls.Load() == 0 || m.poolClosed.Load() || m.snapshot().Slots != 1 || channelClosed(f.closed) {
		t.Fatal("关闭第一阶段错误释放了未知资源")
	}
	if _, err := m.startStream(context.Background(), "b", 1); !errors.Is(err, ErrASRClosed) {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.afterPoolClose(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("池关闭后固定清理执行者未退出")
	}
	if !m.poolClosed.Load() || m.snapshot().Slots != 0 || !channelClosed(f.closed) {
		t.Fatal("关闭第二阶段未回收")
	}
}

// TestASRManagerCloseWhileStartReturnsUnknown 起建与服务关闭交错，迟到的非nil身份仍归清理器，不能重新授权。
func TestASRManagerCloseWhileStartReturnsUnknown(t *testing.T) {
	entered := make(chan struct{})
	f := newASRManagerFake()
	m := asrManagerFixture(t, 1, func(ctx, service context.Context, _ asr.Options, _ asr.Acquire) (asrManagedStream, error) {
		close(entered)
		<-service.Done()
		return asrManagedStream{life: f}, ErrRXSubscribeUnknown
	})
	f.reap = func(context.Context) bool {
		if m.poolClosed.Load() {
			f.confirmClose()
			return true
		}
		return false
	}
	m.startWorkers()
	result := make(chan error, 1)
	go func() { _, err := m.startStream(context.Background(), "a", 1); result <- err }()
	<-entered
	m.beginClose()
	if err := <-result; !errors.Is(err, ErrRXSubscribeUnknown) {
		t.Fatal(err)
	}
	if f.cancelCalls.Load() == 0 || m.snapshot().Slots != 1 {
		t.Fatal("迟到未知受理失去清理所有者")
	}
	done := make(chan struct{})
	go func() { m.afterPoolClose(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("迟到流没有收敛")
	}
}

// TestASRManagerBoundedCleanupAndFairRetry 慢RPC仅占四个固定执行者之一，同一流不重入且失败可轮转到其他槽。
func TestASRManagerBoundedCleanupAndFairRetry(t *testing.T) {
	const count = 12
	var active, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var mu sync.Mutex
	var streams []*asrManagerFake
	m := asrManagerFixture(t, count, func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error) {
		f := newASRManagerFake()
		var own atomic.Int32
		f.reap = func(ctx context.Context) bool {
			if own.Add(1) != 1 {
				t.Error("同一流清理重入")
			}
			defer own.Add(-1)
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			select {
			case <-release:
				f.confirmClose()
				return true
			case <-ctx.Done():
				return false
			}
		}
		mu.Lock()
		streams = append(streams, f)
		mu.Unlock()
		return asrManagedStream{life: f}, ErrRXSubscribeUnknown
	})
	for i := range count {
		if _, err := m.startStream(context.Background(), fmt.Sprintf("a-%d", i), 1); !errors.Is(err, ErrRXSubscribeUnknown) {
			t.Fatal(err)
		}
	}
	m.startWorkers()
	asrAwait(t, func() bool { return peak.Load() == asrCleanupWorkers })
	if m.snapshot().Slots != count {
		t.Fatal("慢清理提前释放槽")
	}
	once.Do(func() { close(release) })
	asrAwait(t, func() bool { return m.snapshot().Slots == 0 })
	if peak.Load() > asrCleanupWorkers {
		t.Fatal("清理执行者随流增长")
	}
	m.beginClose()
	m.afterPoolClose()
	for _, f := range streams {
		if f.reapCalls.Load() == 0 || !channelClosed(f.closed) {
			t.Fatal("公平轮转遗漏槽")
		}
	}
}

// TestASRSourceRetirementEvidence 重点区分 Go 服务撤权、未知绑定与底层媒体实际退休，防取消时谎报资源已回收。
func TestASRSourceRetirementEvidence(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("bound-%v", bound), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := &RXHandle{uuid: "a", session: 9, subscriptionID: 3, service: ctx}
			h.initLifetime()
			var poolClosed atomic.Bool
			source := &asrRXSource{handle: h, poolClosed: &poolClosed}
			sub := newRXSDKFakeSub(9, 3)
			if bound {
				h.bind(sub)
			}
			cancel()
			h.retire()
			if !h.Retired() || !channelClosed(source.LifetimeDone()) {
				t.Fatal("服务撤权没有生效")
			}
			if source.ConfirmedRetired() || source.Snapshot().Retired {
				t.Fatal("把服务取消/句柄撤权当成真实RX退休")
			}
			if !bound {
				h.bind(sub)
				if source.ConfirmedRetired() {
					t.Fatal("迟到绑定直接变成退休")
				}
			}
			sub.retired.Store(true)
			if !source.ConfirmedRetired() {
				t.Fatal("遗漏实际底层退休")
			}
			sub.retired.Store(false)
			poolClosed.Store(true)
			if !source.ConfirmedRetired() {
				t.Fatal("遗漏实际全池关闭")
			}
		})
	}
	t.Run("全池关闭收敛未绑定", func(t *testing.T) {
		h := &RXHandle{session: 9, subscriptionID: 3, service: context.Background()}
		var closed atomic.Bool
		s := &asrRXSource{handle: h, poolClosed: &closed}
		if s.ConfirmedRetired() {
			t.Fatal("未绑定没有池关闭证据")
		}
		closed.Store(true)
		if !s.ConfirmedRetired() || !s.Snapshot().Retired {
			t.Fatal("实际全池关闭未收敛未知绑定")
		}
	})
}

// TestASRSourceUnsubscribeDoesNotEndLifetime 主动输入结束仍允许final；通话挂断才撤销结果生命周期。
func TestASRSourceUnsubscribeDoesNotEndLifetime(t *testing.T) {
	h := &RXHandle{session: 9, subscriptionID: 3, service: context.Background()}
	h.initLifetime()
	defer h.endLifetime()
	h.bind(newRXSDKFakeSub(9, 3))
	source := &asrRXSource{handle: h}
	r, err := source.Unsubscribe(context.Background())
	if err != nil || r.State != "stopped" {
		t.Fatal(r, err)
	}
	if channelClosed(source.LifetimeDone()) || source.ConfirmedRetired() {
		t.Fatal("正常结束输入错误撤销结果或伪造退休")
	}
	h.endLifetime()
	if !channelClosed(source.LifetimeDone()) {
		t.Fatal("挂机没有立即撤销结果")
	}
}
