package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"rustswitch/control/internal/asr"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/media"
)

const (
	asrCleanupWorkers = 4           // 固定清理执行者，不随流数量创建清理协程。
	asrCleanupTimeout = time.Second // 每次最多一个有界媒体退订或状态查询。
	asrCleanupTick    = 100 * time.Millisecond
	asrScanBudget     = 64 // 每次认领最多扫描六十四槽，避免大配置拖住注册表短锁。
)

var (
	ErrASRDisabled = errors.New("ASR stream adapter is disabled")
	ErrASRClosed   = errors.New("ASR stream adapter is stopping")
	ErrASRInvalid  = errors.New("invalid ASR call or subscription identity")
	ErrASRCapacity = errors.New("ASR stream capacity exhausted; starting and cleanup streams retain quota")
	ErrASRBusy     = errors.New("ASR call already owns a starting, active or cleanup stream")
)

// ASRStatus 是低基数资源快照；Active 包含仍等待 final 的有效流，不代表已经识别成功。
// Slots = Starting + Active + CleanupPending；已经关闭但尚未被清理器扫到的槽仍保守占位。
type ASRStatus struct {
	Enabled          bool   `json:"enabled"`
	Stopping         bool   `json:"stopping"`
	MaxStreams       int    `json:"max_streams"`
	Slots            int    `json:"slots"`
	Starting         int    `json:"starting"`
	Active           int    `json:"active"`
	CleanupPending   int    `json:"cleanup_pending"`
	RejectedCapacity uint64 `json:"rejected_capacity"`
	RejectedUUID     uint64 `json:"rejected_uuid"`
}

// asrLifecycle 仅用于管理器的清理边界；生产值始终为实际 asr.Stream，单元测试使用明确的假执行者。
type asrLifecycle interface {
	Cancel()
	Reap(context.Context) bool
	Closed() <-chan struct{}
	Snapshot() asr.Snapshot
}

type asrManagedStream struct {
	stream *asr.Stream // 仅生产启动器赋值；内部单元测试不将假生命周期冒充网络流。
	life   asrLifecycle
}

type asrStarter func(context.Context, context.Context, asr.Options, asr.Acquire) (asrManagedStream, error)

// asrEntry 从预留直到真实资源关闭始终保持同一槽身份；替换 UUID 不能越过未知清理。
type asrEntry struct {
	uuid     string
	slot     int
	starting bool
	cleaning bool
	unknown  bool
	managed  asrManagedStream
}

// asrManager 在短锁下管理固定槽和 UUID 索引。供应商连接、订阅、结果和清理 RPC 从不持注册表锁。
// 服务取消只结束 ASR 网络；poolClosed 必须在 Pool.Close 确实返回后设置，才能证明未知 RX 不再存活。
type asrManager struct {
	options          config.ASRStream
	ctx              context.Context
	cancel           context.CancelFunc
	start            asrStarter
	acquire          func(context.Context, string, uint64) (asr.Source, error)
	mu               sync.Mutex
	byUUID           map[string]*asrEntry
	entries          []*asrEntry
	free             []int
	cursor           int
	closing          bool
	starts           sync.WaitGroup
	workers          sync.WaitGroup
	wake             chan struct{}
	poolClosed       atomic.Bool
	rejectedCapacity atomic.Uint64
	rejectedUUID     atomic.Uint64
}

func (s *Server) initASR() {
	if s.Config.ASRStream == nil {
		return
	}
	o := s.Config.ASRStreamOptions()
	m := newASRManager(s.ctx, o, func(ctx, service context.Context, options asr.Options, acquire asr.Acquire) (asrManagedStream, error) {
		stream, err := asr.Start(ctx, service, options, acquire)
		if stream == nil {
			return asrManagedStream{}, err
		}
		return asrManagedStream{stream: stream, life: stream}, err
	})
	m.acquire = func(ctx context.Context, uuid string, subscriptionID uint64) (asr.Source, error) {
		h, _, err := s.SubscribeRX(ctx, uuid, subscriptionID)
		if h == nil {
			return nil, err
		}
		return &asrRXSource{handle: h, poolClosed: &m.poolClosed}, err
	}
	s.asr = m
	m.startWorkers()
}

func newASRManager(parent context.Context, options config.ASRStream, starter asrStarter) *asrManager {
	ctx, cancel := context.WithCancel(parent)
	m := &asrManager{options: options, ctx: ctx, cancel: cancel, start: starter,
		byUUID: make(map[string]*asrEntry), entries: make([]*asrEntry, options.MaxStreams),
		free: make([]int, options.MaxStreams), wake: make(chan struct{}, asrCleanupWorkers)}
	for i := range m.free {
		m.free[i] = options.MaxStreams - 1 - i
	}
	return m
}

// StartASR 是可信控制面的内部 SDK；固定配置决定代理路径与格式，调用者只能指定通话与订阅身份。
// 起建使用调用方协程和有限期限，实际音频不经过 SIP 主循环。非 nil 返回值在错误时仍必须保留。
func (s *Server) StartASR(ctx context.Context, uuid string, subscriptionID uint64) (*asr.Stream, error) {
	if s.asr == nil {
		return nil, ErrASRDisabled
	}
	managed, err := s.asr.startStream(ctx, uuid, subscriptionID)
	return managed.stream, err
}

func validASRIdentity(uuid string, subscriptionID uint64) bool {
	return uuid != "" && len(uuid) <= 128 && utf8.ValidString(uuid) && !strings.ContainsAny(uuid, "\x00 \t\r\n") && subscriptionID != 0
}

func (m *asrManager) startStream(ctx context.Context, uuid string, subscriptionID uint64) (asrManagedStream, error) {
	if ctx == nil || !validASRIdentity(uuid, subscriptionID) {
		return asrManagedStream{}, ErrASRInvalid
	}
	if err := ctx.Err(); err != nil {
		return asrManagedStream{}, err
	}
	m.mu.Lock()
	if m.closing || m.ctx.Err() != nil {
		m.mu.Unlock()
		return asrManagedStream{}, ErrASRClosed
	}
	if m.byUUID[uuid] != nil {
		m.mu.Unlock()
		m.rejectedUUID.Add(1)
		return asrManagedStream{}, ErrASRBusy
	}
	if len(m.free) == 0 {
		m.mu.Unlock()
		m.rejectedCapacity.Add(1)
		return asrManagedStream{}, ErrASRCapacity
	}
	last := len(m.free) - 1
	e := &asrEntry{uuid: uuid, slot: m.free[last], starting: true}
	m.free = m.free[:last]
	m.entries[e.slot], m.byUUID[uuid] = e, e
	// Add 与关闭排他，关闭开始后不能在 Wait 同时新增启动任务。
	m.starts.Add(1)
	m.mu.Unlock()
	defer m.starts.Done()
	options := asr.Options{SocketPath: m.options.SocketPath, UUID: uuid, SubscriptionID: subscriptionID, SampleRate: m.options.SampleRate}
	managed, err := m.start(ctx, m.ctx, options, func(acquireContext context.Context) (asr.Source, error) {
		return m.acquire(acquireContext, uuid, subscriptionID)
	})
	if managed.life == nil && err == nil {
		err = asr.ErrUnavailable // 防御错误启动器的空成功，不能让调用方把 nil 当成已建立识别流。
	}
	m.mu.Lock()
	e.starting, e.managed = false, managed
	if err == nil && (m.closing || m.ctx.Err() != nil) {
		err = ErrASRClosed
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	e.unknown = err != nil && managed.life != nil
	if managed.life == nil {
		// 契约中 nil 严格表示没有网络/RX遗留，只有这一种启动失败可立即归还额度。
		m.releaseLocked(e)
	}
	m.mu.Unlock()
	if err != nil && managed.life != nil {
		managed.life.Cancel()
	}
	m.notify()
	return managed, err
}

func (m *asrManager) releaseLocked(e *asrEntry) {
	if m.entries[e.slot] != e || m.byUUID[e.uuid] != e {
		return
	}
	m.entries[e.slot] = nil
	delete(m.byUUID, e.uuid)
	m.free = append(m.free, e.slot)
}

func (m *asrManager) notify() {
	for range asrCleanupWorkers {
		select {
		case m.wake <- struct{}{}:
		default:
			return
		}
	}
}

func (m *asrManager) startWorkers() {
	for range asrCleanupWorkers {
		m.workers.Add(1)
		go func() {
			defer m.workers.Done()
			ticker := time.NewTicker(asrCleanupTick)
			defer ticker.Stop()
			for {
				if m.collectOne() {
					return
				}
				select {
				case <-ticker.C:
				case <-m.wake:
				}
			}
		}()
	}
}

// collectOne 每次最多检查六十四槽、执行一次 Reap；活跃槽只做短快照，不能让大量活跃流把失败流延后数分钟。
// 清理 RPC 失败会结束本次批次，下一节拍轮转到其他槽；不会无限重试同一流或按流增加协程。
func (m *asrManager) collectOne() bool {
	for range min(asrScanBudget, len(m.entries)) {
		m.mu.Lock()
		if m.closing && len(m.byUUID) == 0 {
			m.mu.Unlock()
			return true
		}
		entry := m.entries[m.cursor]
		m.cursor = (m.cursor + 1) % len(m.entries)
		if entry == nil || entry.starting || entry.cleaning || entry.managed.life == nil {
			m.mu.Unlock()
			continue
		}
		entry.cleaning = true
		life, unknown, closing := entry.managed.life, entry.unknown, m.closing
		m.mu.Unlock()
		closed := channelClosed(life.Closed())
		reaped := false
		if !closed {
			snapshot := life.Snapshot()
			if unknown || closing || snapshot.CleanupPending || snapshot.State == "finishing" || snapshot.State == "failed" || snapshot.State == "cancelled" {
				ctx, cancel := context.WithTimeout(context.Background(), asrCleanupTimeout)
				life.Reap(ctx)
				cancel()
				closed, reaped = channelClosed(life.Closed()), true
			}
		}
		m.mu.Lock()
		entry.cleaning = false
		if closed {
			// 仅真实 Closed 信号能够归还槽，单独 Reap=true 或缓存 Snapshot 不能替代此证明。
			m.releaseLocked(entry)
		}
		done := m.closing && len(m.byUUID) == 0
		m.mu.Unlock()
		if closed {
			m.notify() // 已收敛的槽让清理者继续工作，不让大量关闭流受每百毫秒四个的固定节拍限制。
		}
		if done || closed || reaped {
			return done
		}
	}
	return false
}

func channelClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// ASRSnapshot 不读取 Call 映射，也不暴露 UUID、token、路径或全文；计数不是识别质量指标。
func (s *Server) ASRSnapshot() ASRStatus {
	if s.asr == nil {
		return ASRStatus{}
	}
	return s.asr.snapshot()
}

func (m *asrManager) snapshot() ASRStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := ASRStatus{Enabled: true, Stopping: m.closing || m.ctx.Err() != nil, MaxStreams: m.options.MaxStreams, Slots: len(m.byUUID), RejectedCapacity: m.rejectedCapacity.Load(), RejectedUUID: m.rejectedUUID.Load()}
	for _, e := range m.entries {
		if e == nil {
			continue
		}
		if e.starting {
			status.Starting++
		} else if e.unknown || m.closing || e.managed.life.Snapshot().CleanupPending || channelClosed(e.managed.life.Closed()) {
			status.CleanupPending++
		} else {
			status.Active++
		}
	}
	return status
}

// beginClose 只撤销新建权限并关闭 ASR 网络，不等待 RX 停止，避免阻止随后必须执行的 Pool.Close。
func (m *asrManager) beginClose() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closing = true
	m.cancel()
	streams := make([]asrLifecycle, 0, len(m.byUUID))
	for _, e := range m.entries {
		if e != nil && !e.starting && e.managed.life != nil {
			streams = append(streams, e.managed.life)
		}
	}
	m.mu.Unlock()
	for _, stream := range streams {
		stream.Cancel()
	}
	m.notify()
}

// afterPoolClose 只允许在真实 Pool.Close 返回后调用；确认全池无存活订阅，再等待固定清理者退出。
func (m *asrManager) afterPoolClose() {
	if m == nil {
		return
	}
	m.poolClosed.Store(true)
	m.notify()
	m.starts.Wait()
	m.notify()
	m.workers.Wait()
}

// asrRXSource 冻结订阅句柄与实际池关闭证据；所有音频/控制复用已有 RX SDK，不读取 Server.calls。
type asrRXSource struct {
	handle     *RXHandle
	poolClosed *atomic.Bool
}

func (s *asrRXSource) Read(ctx context.Context) (media.RXFrame, error) { return s.handle.Read(ctx) }
func (s *asrRXSource) Unsubscribe(ctx context.Context) (media.Reply, error) {
	return s.handle.Unsubscribe(ctx)
}
func (s *asrRXSource) Status(ctx context.Context) (media.Reply, error) { return s.handle.Status(ctx) }
func (s *asrRXSource) LifetimeDone() <-chan struct{}                   { return s.handle.LifetimeDone() }

func (s *asrRXSource) Snapshot() media.RXSnapshot {
	if b := s.handle.binding.Load(); b != nil {
		return b.subscription.Snapshot()
	}
	// RXHandle.Snapshot 的服务撤权提示不能用作未知绑定的实际退休证据。
	confirmed := s.handle.noBindingConfirmed.Load() || s.poolClosed != nil && s.poolClosed.Load()
	return media.RXSnapshot{Worker: s.handle.worker, Generation: s.handle.generation, Session: s.handle.session, SubscriptionID: s.handle.subscriptionID, Pending: !confirmed, Retired: confirmed}
}

func (s *asrRXSource) ConfirmedRetired() bool {
	if s.poolClosed != nil && s.poolClosed.Load() {
		return true
	}
	b := s.handle.binding.Load()
	if b != nil {
		return b.subscription.Snapshot().Retired
	}
	return s.handle.noBindingConfirmed.Load()
}

var _ asr.Source = (*asrRXSource)(nil)
