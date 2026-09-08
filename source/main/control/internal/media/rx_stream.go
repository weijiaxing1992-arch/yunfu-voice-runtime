package media

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	rxCapability = "rx_g711_local_v2"
	rxHeaderSize = 168
	rxBodySize   = 480
	rxQueueSize  = 8
)

var (
	ErrRXUnavailable    = errors.New("RX channel unavailable")
	ErrRXRetired        = errors.New("RX subscription retired")
	ErrRXPending        = errors.New("RX subscription control pending")
	ErrRXSlowConsumer   = errors.New("RX consumer queue overflow; subscription failed locally")
	ErrRXExpired        = errors.New("RX observation delivery deadline reached; subscription failed locally")
	ErrRXClockInvalid   = errors.New("RX shared clock or immutable observation deadline invalid")
	ErrRXOutcomeUnknown = errors.New("RX control outcome unknown; retain handle and query or unsubscribe")
)

// RXKind区分真实解码、补偿、已知缺口及传输损失；不能把任何缺失自动解释成用户沉默。
type RXKind uint8

const (
	RXDecoded RXKind = iota + 1
	RXHistoryPLC
	RXMissing
	RXComfortNoise
	RXLocalExpired
	RXAuxiliaryExpired
	RXSourceBoundary
	RXInactiveSuspended
	RXObservationFailed
	RXExportGap
	RXEnd
	RXExportFailed
	RXAuxiliary
)

// RXFrame保留RXS2来源和媒体位置。正文固定数组由值复制，无每帧切片分配或借用接收缓冲。
// PCM只在Decoded/HistoryPLC时有效，SID只在ComfortNoise时有效；媒体时间不是UTC。
type RXFrame struct {
	// ReceivedAt只在当前Go进程内比较；GoQueueAgeMS是交付前本地停留时间，不是端到端延迟。
	ReceivedAt   time.Time `json:"-"`
	GoQueueAgeMS uint64    `json:"go_queue_age_ms"`
	// 下界来自Rust原始观察，ExpiresAtNS涵盖Rust/OS/Go全路径；收到、重试和SDK排队均不能续期。
	ClockDomain                                        uint16
	ObservationLowerBoundNS, ExpiresAtNS               uint64
	Kind                                               RXKind
	Flags                                              uint16
	Session, SubscriptionID, Sequence                  uint64
	SourceGeneration, SourceSegment                    uint64
	RTPTimestamp, RTPSequence, MediaTimeNS             uint64
	SSRC, SampleRate, RTPClockRate                     uint32
	BodyBytes, SampleCount                             uint16
	GapFirst, GapLast                                  uint64
	GapDecoded, GapPLC                                 uint32
	KnownDurationTicks                                 uint64
	ExportAgeMS                                        uint32
	Reason                                             uint16
	DiscardedPackets                                   uint64
	ObservationAgeMS, ArrivalAgeMS, DeadlineLatenessMS uint32
	Boundary                                           uint8
	CNApplied                                          bool
	PCM                                                [160]int16
	SID                                                [rxBodySize]byte
}

// RXSnapshot中的本地计数按RXS2记录计算（包含gap和终态）；Rust计数另由Status返回，二者不混算。
// 本地队列溢出时丢弃全部未读记录并永久失败，但保留句柄供JSON退订清理。
type RXSnapshot struct {
	Worker                                                             int
	Generation, Session, SubscriptionID                                uint64
	Pending, Retired                                                   bool
	ReceivedRecords, DeliveredRecords, DroppedRecords, ExportGapEvents uint64
	ReceivedSamples, DeliveredSamples, DroppedSamples                  uint64
	FreshnessFailures, ClockFailures                                   uint64
	QueuedRecords                                                      int
	Error                                                              string
}

// RXSubscription只持有不可变分片身份，没有Call指针或SIP授权；上层仍须校验ACK和通话权限。
// 接收与下行PCM/TX互斥无关。建议一个消费者调用Read保持业务处理顺序。
type RXSubscription struct {
	pool                                              *Pool
	lane                                              *rxLane
	worker                                            int
	generation, session, subscriptionID               uint64
	mu                                                sync.Mutex
	queue                                             [rxQueueSize]RXFrame
	head, count                                       int
	notify                                            chan struct{}
	readDone                                          chan struct{}
	readFinished                                      bool
	received, delivered, dropped, exportGapEvents     uint64
	receivedSamples, deliveredSamples, droppedSamples uint64
	freshnessFailures, clockFailures                  uint64
	lastSequence                                      uint64
	wireEnded                                         bool
	pending                                           bool
	retired                                           bool
	localError                                        error
	remote                                            Reply
}

func (h *RXSubscription) wakeLocked() {
	select {
	case h.notify <- struct{}{}:
	default:
	}
}
func (h *RXSubscription) endReadLocked() {
	if !h.readFinished {
		h.readFinished = true
		close(h.readDone)
	}
}
func (h *RXSubscription) failLocked(err error) {
	if h.localError == nil {
		h.localError = err
		if errors.Is(err, ErrRXExpired) {
			h.freshnessFailures++
		}
		if errors.Is(err, ErrRXClockInvalid) {
			h.clockFailures++
		}
	}
	for i := 0; i < h.count; i++ {
		h.droppedSamples += uint64(h.queue[(h.head+i)%rxQueueSize].SampleCount)
	}
	h.dropped += uint64(h.count)
	h.head, h.count = 0, 0
	h.endReadLocked()
	h.wakeLocked()
}
func (h *RXSubscription) retire(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.retired = true
	h.failLocked(err)
}

// RevokeRead在通话授权结束时立即撤销本地收音读取，清除PCM并唤醒阻塞消费者。
// 这不伪造Rust退订结果，也不退役worker身份；Status/Unsubscribe仍可完成真实资源清理。
func (h *RXSubscription) RevokeRead() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failLocked(ErrRXRetired)
}
func (h *RXSubscription) Retired() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.retired }
func (h *RXSubscription) Snapshot() RXSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := RXSnapshot{Worker: h.worker, Generation: h.generation, Session: h.session, SubscriptionID: h.subscriptionID, Pending: h.pending, Retired: h.retired, ReceivedRecords: h.received, DeliveredRecords: h.delivered, DroppedRecords: h.dropped, ExportGapEvents: h.exportGapEvents, QueuedRecords: h.count}
	r.ReceivedSamples, r.DeliveredSamples, r.DroppedSamples = h.receivedSamples, h.deliveredSamples, h.droppedSamples
	r.FreshnessFailures, r.ClockFailures = h.freshnessFailures, h.clockFailures
	if h.localError != nil {
		r.Error = h.localError.Error()
	}
	return r
}

// Read等待下一条真实观察。错误优先于残留音频，避免溢出后仍返回看似连续的正常音频。
// 终态记录先交付，然后返回EOF；调用者可用独立JSON Status核实Rust最终计数。
func (h *RXSubscription) Read(ctx context.Context) (RXFrame, error) {
	for {
		if err := ctx.Err(); err != nil {
			return RXFrame{}, err
		}
		h.mu.Lock()
		if h.localError != nil {
			err := h.localError
			h.mu.Unlock()
			return RXFrame{}, err
		}
		if h.count > 0 {
			r := h.queue[h.head]
			age := time.Since(r.ReceivedAt)
			err := h.lane.checkFresh(r)
			if err == nil && age >= 100*time.Millisecond {
				err = ErrRXExpired
			}
			if err != nil {
				h.failLocked(err)
				h.mu.Unlock()
				return RXFrame{}, err
			}
			r.GoQueueAgeMS = uint64(max(age, 0) / time.Millisecond)
			h.head = (h.head + 1) % rxQueueSize
			h.count--
			h.delivered++
			h.deliveredSamples += uint64(r.SampleCount)
			if r.Kind == RXExportFailed {
				h.localError = ErrRXUnavailable
			}
			h.mu.Unlock()
			return r, nil
		}
		ended := h.wireEnded
		h.mu.Unlock()
		if ended {
			return RXFrame{}, io.EOF
		}
		select {
		case <-h.notify:
		case <-h.readDone:
		case <-ctx.Done():
			return RXFrame{}, ctx.Err()
		}
	}
}

func (h *RXSubscription) Status(ctx context.Context) (Reply, error) {
	return h.control(ctx, "rx_status")
}
func (h *RXSubscription) Unsubscribe(ctx context.Context) (Reply, error) {
	return h.control(ctx, "rx_unsubscribe")
}
func (h *RXSubscription) control(ctx context.Context, op string) (Reply, error) {
	h.mu.Lock()
	if h.retired {
		h.mu.Unlock()
		return Reply{}, ErrRXRetired
	}
	if h.pending {
		h.mu.Unlock()
		return Reply{}, ErrRXPending
	}
	h.pending = true
	h.mu.Unlock()
	return h.submit(ctx, op, false, nil)
}

// submit使用固定worker控制协程的回调，无每订阅goroutine。执行前取消可证明未提交；
// 执行开始后取消返回未知结果，回调仍结算句柄，不丢掉可能已在Rust开始的订阅。
func (h *RXSubscription) submit(ctx context.Context, op string, candidate bool, previous *RXSubscription) (Reply, error) {
	state := &atomic.Uint32{}
	resultCh := make(chan result, 1)
	finish := func(r Reply, err error) {
		definite := state.Load() == 2 || (r.Type == "error" && !r.OK)
		h.mu.Lock()
		h.pending = false
		if err == nil {
			h.remote = r
		}
		h.mu.Unlock()
		if candidate && definite {
			h.lane.rollback(h, previous)
		} else if candidate && previous != nil {
			previous.retire(ErrRXRetired)
		}
		if err != nil && !definite {
			err = fmt.Errorf("%w: %v", ErrRXOutcomeUnknown, err)
		}
		resultCh <- result{reply: r, err: err}
	}
	request := Request{Op: op, Generation: h.generation, Session: h.session, SubscriptionID: h.subscriptionID}
	err := h.pool.enqueue(h.worker, job{ctx: ctx, request: request, callState: state, callback: finish})
	if err != nil {
		h.mu.Lock()
		h.pending = false
		h.mu.Unlock()
		if candidate {
			h.lane.rollback(h, previous)
		}
		return Reply{}, err
	}
	select {
	case r := <-resultCh:
		return r.reply, r.err
	case <-ctx.Done():
		if state.CompareAndSwap(0, 2) {
			// 队列任务仍由worker稍后回收，句柄现在即可从注册表安全撤销。
			if candidate {
				h.lane.rollback(h, previous)
			}
			return Reply{}, ctx.Err()
		}
		select {
		case r := <-resultCh:
			return r.reply, r.err
		default:
		}
		return Reply{}, fmt.Errorf("%w: %v", ErrRXOutcomeUnknown, ctx.Err())
	}
}

// SubscribeRX注册固定8槽后才入控制队列，防止Rust首帧早于JSON回执到达时丢失。
// 非nil句柄必须保留：包括取消/失联时可能已执行的订阅；同ID重试返回同一个句柄。
// 此入口仅是内部SDK，不能替代Server对本地通话、ACK和调用方权限的授权。
func (p *Pool) SubscribeRX(ctx context.Context, worker int, generation, session, subscriptionID uint64) (*RXSubscription, Reply, error) {
	if err := ctx.Err(); err != nil {
		return nil, Reply{}, err
	}
	if worker < 0 || worker >= len(p.Workers) || generation == 0 || session == 0 || subscriptionID == 0 {
		return nil, Reply{}, errors.New("invalid RX identity")
	}
	w := p.Workers[worker]
	w.submitMu.Lock()
	if !w.supportsRXLocked() || w.Generation.Load() != generation {
		w.submitMu.Unlock()
		return nil, Reply{}, ErrRXUnavailable
	}
	lane := w.rx
	lane.mu.Lock()
	w.submitMu.Unlock()
	if lane.failed {
		lane.mu.Unlock()
		return nil, Reply{}, ErrRXUnavailable
	}
	previous := lane.subscriptions[session]
	candidate := false
	var h *RXSubscription
	if previous != nil {
		previous.mu.Lock()
		if previous.subscriptionID == subscriptionID {
			h = previous
			if h.pending {
				previous.mu.Unlock()
				lane.mu.Unlock()
				return h, Reply{}, ErrRXPending
			}
			h.pending = true
			previous.mu.Unlock()
		} else {
			terminal := !previous.pending && (previous.remote.State == "stopped" || previous.remote.State == "failed")
			previous.mu.Unlock()
			if subscriptionID < previous.subscriptionID || !terminal {
				lane.mu.Unlock()
				return nil, Reply{}, errors.New("RX replacement requires higher ID and confirmed terminal subscription")
			}
		}
	}
	if h == nil {
		if previous == nil && len(lane.subscriptions) >= lane.maxSubscriptions {
			lane.mu.Unlock()
			return nil, Reply{}, ErrQueueFull
		}
		h = &RXSubscription{pool: p, lane: lane, worker: worker, generation: generation, session: session, subscriptionID: subscriptionID, notify: make(chan struct{}, 1), readDone: make(chan struct{}), pending: true}
		lane.subscriptions[session] = h
		candidate = true
	}
	lane.mu.Unlock()
	reply, err := h.submit(ctx, "rx_subscribe", candidate, previous)
	if h.Retired() {
		return nil, reply, err
	}
	if err == nil && previous != nil && previous != h {
		previous.retire(ErrRXRetired)
	}
	return h, reply, err
}

// rxLane按进程代次拥有独立FD；分发只查本lane固定身份表，不查询新worker的动态表。
type rxLane struct {
	mu               sync.Mutex
	conn             *net.UnixConn
	subscriptions    map[uint64]*RXSubscription
	maxSubscriptions int
	generation       uint64
	failed           bool
	done             chan struct{}
	once             sync.Once
	// 每lane时钟入口只供独立时钟边界测试替换；生产始终调用公开OS单调时钟。
	clockNow func() (uint64, error)
}

func (l *rxLane) checkFresh(f RXFrame) error {
	clock := l.clockNow
	if clock == nil {
		clock = rxClockNow
	}
	now, err := clock()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRXClockInvalid, err)
	}
	return checkRXFreshness(f, now)
}

func newRXLane(maxSubscriptions int) (*rxLane, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, err
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	parent, child := os.NewFile(uintptr(fds[0]), "rx-parent"), os.NewFile(uintptr(fds[1]), "rx-child")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		_ = child.Close()
		return nil, nil, errors.New("RX socketpair is not Unix datagram")
	}
	l := &rxLane{conn: unix, subscriptions: make(map[uint64]*RXSubscription), maxSubscriptions: maxSubscriptions, done: make(chan struct{})}
	go l.run()
	return l, child, nil
}
func (l *rxLane) close() {
	l.once.Do(func() { l.isolate(ErrRXRetired, true); _ = l.conn.Close() })
	<-l.done
}
func (l *rxLane) isolate(err error, retired bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failed = true
	for _, h := range l.subscriptions {
		h.mu.Lock()
		h.retired = h.retired || retired
		h.failLocked(err)
		h.mu.Unlock()
	}
}
func (l *rxLane) rollback(h, previous *RXSubscription) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.subscriptions[h.session] == h {
		if previous == nil {
			delete(l.subscriptions, h.session)
		} else {
			l.subscriptions[h.session] = previous
		}
	}
	h.retire(ErrRXRetired)
}
func (l *rxLane) release(session uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h := l.subscriptions[session]; h != nil {
		h.retire(ErrRXRetired)
		delete(l.subscriptions, session)
	}
}
func (l *rxLane) run() {
	defer close(l.done)
	var packet [rxHeaderSize + rxBodySize + 1]byte
	for {
		n, _, flags, _, err := l.conn.ReadMsgUnix(packet[:], nil)
		if err != nil {
			l.isolate(ErrRXUnavailable, false)
			return
		}
		if flags&syscall.MSG_TRUNC != 0 {
			l.isolate(errors.New("truncated RX datagram"), false)
			return
		}
		frame, err := decodeRXFrame(packet[:n])
		if err != nil {
			l.isolate(err, false)
			return
		}
		l.dispatch(frame)
	}
}
func (l *rxLane) dispatch(f RXFrame) {
	f.ReceivedAt = time.Now()
	l.mu.Lock()
	h := l.subscriptions[f.Session]
	if l.failed || h == nil || h.subscriptionID != f.SubscriptionID || h.generation != l.generation {
		l.mu.Unlock()
		return
	}
	h.mu.Lock()
	l.mu.Unlock()
	defer h.mu.Unlock()
	h.received++
	h.receivedSamples += uint64(f.SampleCount)
	dropIncoming := func() {
		h.dropped++
		h.droppedSamples += uint64(f.SampleCount)
	}
	if h.retired {
		dropIncoming()
		return
	}
	if err := h.checkSequence(f); err != nil {
		dropIncoming()
		h.failLocked(err)
		return
	}
	if f.Kind == RXExportGap {
		h.exportGapEvents += f.GapLast - f.GapFirst + 1
	}
	if f.Kind == RXEnd || f.Kind == RXExportFailed {
		h.wireEnded = true
		h.endReadLocked()
	}
	if h.localError != nil {
		dropIncoming()
		return
	}
	// 读取/解码/等待锁期间都可能被抢占，此处重新读取同域时钟，不使用ReceivedAt重锚。
	if err := l.checkFresh(f); err != nil {
		dropIncoming()
		h.failLocked(err)
		return
	}
	if h.count > 0 && f.ReceivedAt.Sub(h.queue[h.head].ReceivedAt) >= 100*time.Millisecond {
		dropIncoming()
		h.failLocked(ErrRXExpired)
		return
	}
	if h.count > 0 {
		if err := l.checkFresh(h.queue[h.head]); err != nil {
			dropIncoming()
			h.failLocked(err)
			return
		}
	}
	if h.count == rxQueueSize {
		dropIncoming()
		h.failLocked(ErrRXSlowConsumer)
		return
	}
	h.queue[(h.head+h.count)%rxQueueSize] = f
	h.count++
	h.wakeLocked()
}
func (h *RXSubscription) checkSequence(f RXFrame) error {
	if h.wireEnded {
		return errors.New("RX record after terminal")
	}
	switch f.Kind {
	case RXExportGap:
		if h.lastSequence == math.MaxUint64 || f.GapFirst != h.lastSequence+1 {
			return errors.New("RX gap does not account for next observation")
		}
		h.lastSequence = f.GapLast
	case RXEnd, RXExportFailed:
		if f.Sequence != h.lastSequence {
			return errors.New("RX terminal leaves unreported observation gap")
		}
	default:
		if h.lastSequence == math.MaxUint64 || f.Sequence != h.lastSequence+1 {
			return errors.New("RX observation sequence discontinuity")
		}
		h.lastSequence = f.Sequence
	}
	return nil
}

func (w *Worker) supportsRXLocked() bool {
	return w.Healthy.Load() && w.Generation.Load() > 0 && w.PID.Load() > 0 && w.rx != nil && slices.Contains(w.capabilities, rxCapability) && w.supportsLocalProcessingLocked()
}
func (w *Worker) SupportsRX() bool {
	w.submitMu.Lock()
	defer w.submitMu.Unlock()
	return w.supportsRXLocked()
}
func isRXControl(op string) bool {
	return op == "rx_subscribe" || op == "rx_status" || op == "rx_unsubscribe"
}
func validateRXRequest(r Request) error {
	if r.Generation == 0 || r.Session == 0 || r.SubscriptionID == 0 {
		return errors.New("RX requires explicit generation, session and subscription")
	}
	return nil
}

// 所有RX成功计数必须显式存在；零不是缺字段或null的替代品。本检查由控制管道解码调用。
func validateRXRaw(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"id", "ok", "type", "session", "subscription_id", "state", "produced_events", "submitted_events", "dropped_events", "queued_events", "produced_samples", "submitted_samples", "dropped_samples", "oldest_age_ms", "error"} {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return fmt.Errorf("missing RX state field: %s", key)
		}
	}
	return nil
}
func validateRXControlReply(req Request, r Reply) error {
	if r.Type != "rx_state" || r.Session != req.Session || r.SubscriptionID != req.SubscriptionID || !slices.Contains([]string{"active", "stopped", "failed"}, r.State) {
		return errors.New("invalid RX state identity")
	}
	if (r.State == "failed") != (r.Error != "") || !slices.Contains([]string{"", "invalid_observation", "sequence_exhausted", "sample_count_exhausted", "gap_metadata_discontinuity", "rx_lane_unavailable", "rx_observation_failed", "rx_clock_out_of_range"}, r.Error) {
		return errors.New("invalid RX error state")
	}
	if r.SubmittedEvents > r.ProducedEvents || r.DroppedEvents > r.ProducedEvents-r.SubmittedEvents || r.QueuedEvents != r.ProducedEvents-r.SubmittedEvents-r.DroppedEvents || r.QueuedEvents > rxQueueSize || (r.QueuedEvents == 0 && r.OldestAgeMS != 0) || (r.State != "active" && r.QueuedEvents != 0) {
		return errors.New("inconsistent RX event accounting")
	}
	if r.ProducedSamples%160 != 0 || r.SubmittedSamples%160 != 0 || r.DroppedSamples%160 != 0 || r.SubmittedSamples > r.ProducedSamples || r.DroppedSamples > r.ProducedSamples-r.SubmittedSamples {
		return errors.New("inconsistent RX sample accounting")
	}
	queuedSamples := r.ProducedSamples - r.SubmittedSamples - r.DroppedSamples
	if queuedSamples > r.QueuedEvents*160 || r.ProducedSamples/160 > r.ProducedEvents || r.SubmittedSamples/160 > r.SubmittedEvents || r.DroppedSamples/160 > r.DroppedEvents {
		return errors.New("RX samples exceed observed events")
	}
	if req.Op == "rx_unsubscribe" && r.State == "active" {
		return errors.New("RX unsubscribe did not stop")
	}
	return nil
}

// decodeRXFrame先验证完整datagram和保留字段，再解释元数据；不会补造缺失位置。
func decodeRXFrame(b []byte) (RXFrame, error) {
	bad := func() (RXFrame, error) { return RXFrame{}, errors.New("invalid RXS2 frame") }
	if len(b) < rxHeaderSize || len(b) > rxHeaderSize+rxBodySize || string(b[:4]) != "RXS2" || b[5] != 0 || binary.LittleEndian.Uint16(b[126:]) != 0 {
		return bad()
	}
	u16 := func(i int) uint16 { return binary.LittleEndian.Uint16(b[i:]) }
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(b[i:]) }
	u64 := func(i int) uint64 { return binary.LittleEndian.Uint64(b[i:]) }
	f := RXFrame{Kind: RXKind(b[4]), Flags: u16(6), Session: u64(8), SubscriptionID: u64(16), Sequence: u64(24), SourceGeneration: u64(32), SourceSegment: u64(40), RTPTimestamp: u64(48), RTPSequence: u64(56), MediaTimeNS: u64(64), SSRC: u32(72), SampleRate: u32(76), RTPClockRate: u32(80), BodyBytes: u16(84), SampleCount: u16(86), GapFirst: u64(88), GapLast: u64(96), GapDecoded: u32(104), GapPLC: u32(108), KnownDurationTicks: u64(112), ExportAgeMS: u32(120), Reason: u16(124), DiscardedPackets: u64(128), ObservationAgeMS: u32(136), ArrivalAgeMS: u32(140), DeadlineLatenessMS: u32(144), Boundary: b[148], CNApplied: b[149] == 1}
	f.ClockDomain, f.ObservationLowerBoundNS, f.ExpiresAtNS = u16(150), u64(152), u64(160)
	if f.Kind < RXDecoded || f.Kind > RXAuxiliary || f.Flags & ^uint16(0x3ff) != 0 || f.Session == 0 || f.SubscriptionID == 0 || f.SampleRate != 8000 || f.RTPClockRate != 8000 || len(b) != rxHeaderSize+int(f.BodyBytes) {
		return bad()
	}
	if !f.rxDeadlineValid() {
		return RXFrame{}, ErrRXClockInvalid
	}
	if b[149] > 1 || f.Boundary > 6 || (f.Flags&256 == 0 && f.ArrivalAgeMS != 0) || (f.Flags&512 == 0 && f.DeadlineLatenessMS != 0) {
		return bad()
	}
	if (f.Boundary != 0) != (f.Flags&16 != 0) || (f.Boundary != 0 && f.Boundary != 5 && f.Kind != RXSourceBoundary) || (f.Boundary == 6 && f.Flags&4 != 0) {
		return bad()
	}
	if f.DiscardedPackets != 0 && f.Kind != RXSourceBoundary {
		return bad()
	}
	if f.CNApplied && f.Kind != RXComfortNoise && f.Kind != RXAuxiliaryExpired {
		return bad()
	}
	if (f.Flags&1 == 0 && f.SSRC != 0) || (f.Flags&2 == 0 && f.RTPTimestamp != 0) || (f.Flags&4 == 0 && f.RTPSequence != 0) || (f.Flags&8 == 0 && f.MediaTimeNS != 0) {
		return bad()
	}
	switch f.Kind {
	case RXDecoded, RXHistoryPLC:
		if f.BodyBytes != 320 || f.SampleCount != 160 {
			return bad()
		}
		for i := range f.PCM {
			f.PCM[i] = int16(binary.LittleEndian.Uint16(b[rxHeaderSize+i*2:]))
		}
	case RXComfortNoise:
		if f.BodyBytes == 0 || f.SampleCount != 0 {
			return bad()
		}
		copy(f.SID[:], b[rxHeaderSize:])
	default:
		if f.BodyBytes != 0 || f.SampleCount != 0 {
			return bad()
		}
	}
	if f.Kind == RXExportGap || f.Kind == RXEnd || f.Kind == RXExportFailed {
		if f.DiscardedPackets != 0 || f.ObservationAgeMS != 0 || f.ArrivalAgeMS != 0 || f.DeadlineLatenessMS != 0 || f.Boundary != 0 || f.CNApplied {
			return bad()
		}
	}

	if f.Kind == RXExportGap {
		if f.GapFirst == 0 || f.GapLast < f.GapFirst || f.Sequence != f.GapLast || uint64(f.GapDecoded)+uint64(f.GapPLC) > f.GapLast-f.GapFirst+1 || f.Flags != 0 || f.SourceGeneration != 0 || f.SourceSegment != 0 || f.KnownDurationTicks != 0 || f.Reason == 0 || f.Reason & ^uint16(7) != 0 {
			return bad()
		}
	} else {
		if f.GapFirst != 0 || f.GapLast != 0 || f.GapDecoded != 0 || f.GapPLC != 0 {
			return bad()
		}
		if f.Kind == RXEnd || f.Kind == RXExportFailed {
			expectedReason := uint16(0)
			if f.Kind == RXExportFailed {
				expectedReason = 1
			}
			if f.Reason != expectedReason || f.Flags != 0 || f.SourceGeneration != 0 || f.SourceSegment != 0 || f.KnownDurationTicks != 0 {
				return bad()
			}
		} else if f.Sequence == 0 || f.Reason > 11 {
			return bad()
		}
	}
	return f, nil
}
