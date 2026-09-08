package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rustswitch/control/internal/media"
)

const (
	rxSubscribeLimit = 64 // 请求、在途和结果均有独立上限，不能按音频帧进入主循环。
	rxControlWorkers = 4  // 全服务固定执行协程，不随通话或订阅数量增长。
	rxControlTimeout = 3 * time.Second
)

var (
	ErrRXNotEligible       = errors.New("RX requires an ACKed local processed G711 A leg")
	ErrRXInvalid           = errors.New("invalid RX subscription identity")
	ErrRXHandleBusy        = errors.New("RX handle or authorization queue busy")
	ErrRXHandleClosed      = errors.New("RX read authorization revoked; query or unsubscribe for cleanup")
	ErrRXHandleRetired     = errors.New("RX handle retired")
	ErrRXSubscribeUnknown  = errors.New("RX subscribe outcome unknown; retain handle and query or unsubscribe")
	ErrRXStaleSubscription = errors.New("RX replacement requires a higher ID and confirmed remote terminal state")
)

// rxSubscription只抽取接收SDK；生产适配器始终使用Pool句柄，测试可确定暂停而不伪造SIP证据。
type rxSubscription interface {
	Read(context.Context) (media.RXFrame, error)
	Status(context.Context) (media.Reply, error)
	Unsubscribe(context.Context) (media.Reply, error)
	Snapshot() media.RXSnapshot
	Retired() bool
	RevokeRead()
}

type rxTransport interface {
	subscribe(context.Context, int, uint64, uint64, uint64) (rxSubscription, media.Reply, error)
}

type rxPoolTransport struct{ pool *media.Pool }

func (p rxPoolTransport) subscribe(ctx context.Context, worker int, generation, session, subscription uint64) (rxSubscription, media.Reply, error) {
	h, r, err := p.pool.SubscribeRX(ctx, worker, generation, session, subscription)
	if h == nil {
		return nil, r, err
	}
	return h, r, err
}

type rxBinding struct{ subscription rxSubscription }

// RXHandle冻结ACK授权时的身份，不持有Call或全局通话映射。取消读权和远端终态是不同事实。
// 未知受理结果也保留此句柄，待实际媒体绑定到达后可Status/Unsubscribe，不复活已关闭读权。
type RXHandle struct {
	uuid                                string
	worker                              int
	generation, session, subscriptionID uint64
	service                             context.Context
	binding                             atomic.Pointer[rxBinding]
	last                                atomic.Pointer[media.Reply]
	authorized                          atomic.Bool
	readClosed                          atomic.Bool
	retired                             atomic.Bool
	terminal                            atomic.Bool
	reading                             atomic.Bool
	controlling                         atomic.Bool
}

func (h *RXHandle) UUID() string           { return h.uuid }
func (h *RXHandle) SubscriptionID() uint64 { return h.subscriptionID }
func (h *RXHandle) Retired() bool {
	if h == nil || h.retired.Load() || h.service == nil || h.service.Err() != nil {
		return true
	}
	b := h.binding.Load()
	return b != nil && b.subscription.Retired()
}

// revoke只关闭本地读取并立即唤醒等待者；不向外谎报Rust已经停止，也不丢掉退订地址。
func (h *RXHandle) revoke() {
	if h == nil {
		return
	}
	h.readClosed.Store(true)
	if b := h.binding.Load(); b != nil {
		b.subscription.RevokeRead()
	}
}
func (h *RXHandle) retire() {
	if h != nil {
		h.retired.Store(true)
		h.revoke()
	}
}
func (h *RXHandle) bind(sub rxSubscription) {
	if sub == nil {
		return
	}
	h.binding.Store(&rxBinding{subscription: sub})
	// 与revoke并发时至少一方看到另一方的写入；迟到绑定无法留下可读的旧队列。
	if h.readClosed.Load() || h.Retired() {
		sub.RevokeRead()
	}
}
func (h *RXHandle) usable() (rxSubscription, error) {
	if h.Retired() {
		return nil, ErrRXHandleRetired
	}
	b := h.binding.Load()
	if b == nil {
		return nil, ErrRXHandleBusy
	}
	return b.subscription, nil
}
func (h *RXHandle) observe(r media.Reply, err error) {
	if err != nil || !r.OK || r.Type != "rx_state" || r.Session != h.session || r.SubscriptionID != h.subscriptionID {
		return
	}
	copy := r
	h.last.Store(&copy)
	if r.State == "stopped" || r.State == "failed" {
		h.terminal.Store(true)
		h.revoke()
	}
}

// Read每次仅直接调用媒体SDK；不创建context、回调、协程，也不占用SIP主循环或TX所有权。
// 结束先撤读权，底层RevokeRead负责唤醒；返回前再次检查，拒绝与撤销同时取出的旧音频。
func (h *RXHandle) Read(ctx context.Context) (media.RXFrame, error) {
	sub, err := h.usable()
	if err != nil {
		return media.RXFrame{}, err
	}
	if h.readClosed.Load() {
		return media.RXFrame{}, ErrRXHandleClosed
	}
	if !h.authorized.Load() {
		return media.RXFrame{}, ErrRXHandleBusy
	}
	if !h.reading.CompareAndSwap(false, true) {
		return media.RXFrame{}, ErrRXHandleBusy
	}
	defer h.reading.Store(false)
	f, err := sub.Read(ctx)
	if h.Retired() {
		return media.RXFrame{}, ErrRXHandleRetired
	}
	if h.readClosed.Load() {
		return media.RXFrame{}, ErrRXHandleClosed
	}
	if err == nil {
		// SDK读取之后也可能发生抢占，Server交付点复核同一不可续期期限。
		if freshErr := f.CheckFresh(); freshErr != nil {
			h.revoke()
			return media.RXFrame{}, freshErr
		}
	}
	// 共享时钟检查期间也可能发生挂断，最后再检查授权；检查之后的调用者使用仍需自检。
	if h.Retired() {
		return media.RXFrame{}, ErrRXHandleRetired
	}
	if h.readClosed.Load() {
		return media.RXFrame{}, ErrRXHandleClosed
	}
	return f, err
}

// Status和Unsubscribe在调用者协程执行，有界媒体控制队列承接RPC；不经过Server通话映射。
func (h *RXHandle) Status(ctx context.Context) (media.Reply, error) { return h.control(ctx, false) }
func (h *RXHandle) Unsubscribe(ctx context.Context) (media.Reply, error) {
	h.revoke()
	return h.control(ctx, true)
}
func (h *RXHandle) control(ctx context.Context, unsubscribe bool) (media.Reply, error) {
	sub, err := h.usable()
	if err != nil {
		return media.Reply{}, err
	}
	if !h.controlling.CompareAndSwap(false, true) {
		return media.Reply{}, ErrRXHandleBusy
	}
	defer h.controlling.Store(false)
	var r media.Reply
	if unsubscribe {
		r, err = sub.Unsubscribe(ctx)
	} else {
		r, err = sub.Status(ctx)
	}
	h.observe(r, err)
	return r, err
}
func (h *RXHandle) Snapshot() media.RXSnapshot {
	if b := h.binding.Load(); b != nil {
		return b.subscription.Snapshot()
	}
	return media.RXSnapshot{Worker: h.worker, Generation: h.generation, Session: h.session, SubscriptionID: h.subscriptionID, Pending: true, Retired: h.Retired()}
}

type rxSubscribeRequest struct {
	ctx            context.Context
	uuid           string
	subscriptionID uint64
	reply          chan rxSubscribeResult
	delivery       atomic.Uint32 // 0等待、1调用者取消、2主循环认领交付；避免取消使已受理句柄无主。
	published      atomic.Pointer[RXHandle]
}
type rxSubscribeResult struct {
	request *rxSubscribeRequest
	handle  *RXHandle
	reply   media.Reply
	err     error
}
type rxSubscribeJob struct {
	request *rxSubscribeRequest
	handle  *RXHandle
}

type rxService struct {
	ctx            context.Context
	transport      rxTransport
	workerSnapshot func(int) (media.CapabilitySnapshot, bool)
	requests       chan *rxSubscribeRequest
	jobs           chan rxSubscribeJob
	results        chan rxSubscribeResult
	pending        int // 仅Server主循环修改；64个预留覆盖全部job与尚未处理的result。
	workers        sync.WaitGroup
}

func (s *Server) initRX() {
	e := &rxService{ctx: s.ctx, transport: rxPoolTransport{s.pool}, requests: make(chan *rxSubscribeRequest, rxSubscribeLimit), jobs: make(chan rxSubscribeJob, rxSubscribeLimit), results: make(chan rxSubscribeResult, rxSubscribeLimit)}
	e.workerSnapshot = func(worker int) (media.CapabilitySnapshot, bool) {
		if s.pool == nil || worker < 0 || worker >= len(s.pool.Workers) {
			return media.CapabilitySnapshot{}, false
		}
		return s.pool.Workers[worker].CapabilitySnapshot(), true
	}
	s.rx = e
	e.startWorkers()
}
func (e *rxService) startWorkers() {
	for range rxControlWorkers {
		e.workers.Add(1)
		go func() {
			defer e.workers.Done()
			for {
				select {
				case <-e.ctx.Done():
					return
				case job := <-e.jobs:
					h, r := job.handle, job.request
					// 每次订阅控制仅创建一次取消桥接，绝不在逐音频帧路径创建这些对象。
					ctx, cancel := context.WithTimeout(e.ctx, rxControlTimeout)
					stop := context.AfterFunc(r.ctx, cancel)
					if r.ctx.Err() != nil {
						cancel()
					}
					sub, reply, err := e.transport.subscribe(ctx, h.worker, h.generation, h.session, h.subscriptionID)
					stop()
					cancel()
					h.bind(sub)
					result := rxSubscribeResult{request: r, handle: h, reply: reply, err: err}
					select {
					case e.results <- result:
					case <-e.ctx.Done():
						h.retire()
						return
					}
				}
			}
		}()
	}
}

// SubscribeRX只发起一次授权控制。数据由返回句柄独立读取；同ID重试不产生新流或重开读权。
func (s *Server) SubscribeRX(ctx context.Context, uuid string, subscriptionID uint64) (*RXHandle, media.Reply, error) {
	if uuid == "" || len(uuid) > 128 || strings.ContainsAny(uuid, " \t\r\n") || subscriptionID == 0 {
		return nil, media.Reply{}, ErrRXInvalid
	}
	e := s.rx
	if e == nil || e.ctx == nil || e.ctx.Err() != nil {
		return nil, media.Reply{}, ErrRXHandleRetired
	}
	if err := ctx.Err(); err != nil {
		return nil, media.Reply{}, err
	}
	r := &rxSubscribeRequest{ctx: ctx, uuid: uuid, subscriptionID: subscriptionID, reply: make(chan rxSubscribeResult, 1)}
	select {
	case e.requests <- r:
	case <-ctx.Done():
		return nil, media.Reply{}, ctx.Err()
	default:
		return nil, media.Reply{}, ErrRXHandleBusy
	}
	select {
	case result := <-r.reply:
		if e.ctx.Err() != nil {
			if result.handle != nil {
				result.handle.retire()
			}
			return result.handle, result.reply, ErrRXHandleRetired
		}
		if result.handle != nil && ctx.Err() != nil {
			result.handle.revoke()
			return result.handle, result.reply, ErrRXSubscribeUnknown
		}
		return result.handle, result.reply, result.err
	case <-ctx.Done():
		if !r.delivery.CompareAndSwap(0, 1) {
			// 已取得交付权只等待单槽写入，媒体RPC早已结束，不等待网络。
			result := <-r.reply
			if result.handle != nil {
				result.handle.revoke()
				return result.handle, result.reply, ErrRXSubscribeUnknown
			}
			return nil, result.reply, result.err
		}
		if h := r.published.Load(); h != nil {
			h.revoke()
			return h, media.Reply{}, ErrRXSubscribeUnknown
		}
		return nil, media.Reply{}, ctx.Err()
	case <-e.ctx.Done():
		r.delivery.CompareAndSwap(0, 1)
		if h := r.published.Load(); h != nil {
			h.retire()
			return h, media.Reply{}, ErrRXHandleRetired
		}
		return nil, media.Reply{}, ErrRXHandleRetired
	}
}
func deliverRXSubscribe(r *rxSubscribeRequest, result rxSubscribeResult) bool {
	if !r.delivery.CompareAndSwap(0, 2) {
		return false
	}
	r.reply <- result
	return true
}

func (s *Server) rxEligible(uuid string) (*Call, error) {
	entry := s.compatChannels[uuid]
	c := entry.call
	if c == nil || entry.side != 0 || !c.Local || !c.AAck || !c.Established || !c.Allocated || c.Releasing || c.Ended || s.Config.Media.Processing != "g711" || c.Allocation.ProcessingTopology != "local" || c.Allocation.ProcessingVersion == nil || *c.Allocation.ProcessingVersion != 1 {
		return nil, ErrRXNotEligible
	}
	codec := c.Offer.Codec
	if (codec.Name != "PCMU" && codec.Name != "PCMA") || codec.SampleRate != 8000 || codec.RTPClockRate != 8000 || codec.Channels != 1 || codec.PTimeMS != 20 || c.Offer.PTime != 20 {
		return nil, ErrRXNotEligible
	}
	p, ok := s.rx.workerSnapshot(c.Worker)
	if !ok || !p.Healthy || p.PID <= 0 || p.WorkerID != c.Worker || p.Generation != c.Generation || c.Generation == 0 || !slices.Contains(p.Capabilities, "processed_g711_v1") || !slices.Contains(p.Capabilities, "processed_g711_local_v1") || !slices.Contains(p.Capabilities, "rx_g711_local_v2") {
		return nil, ErrRXNotEligible
	}
	return c, nil
}

func (s *Server) handleRXSubscribe(r *rxSubscribeRequest) {
	e := s.rx
	fail := func(err error) { deliverRXSubscribe(r, rxSubscribeResult{err: err}) }
	if r.ctx.Err() != nil || r.delivery.Load() != 0 {
		fail(r.ctx.Err())
		return
	}
	c, err := s.rxEligible(r.uuid)
	if err != nil {
		fail(err)
		return
	}
	if c.rxPending != nil {
		fail(ErrRXHandleBusy)
		return
	}
	old := c.rxHandle
	if old != nil && r.subscriptionID == old.subscriptionID {
		result := rxSubscribeResult{handle: old}
		if p := old.last.Load(); p != nil {
			result.reply = *p
		} else {
			result.err = ErrRXSubscribeUnknown
		}
		if old.Retired() {
			result.err = ErrRXHandleRetired
		}
		// 同ID返回原句柄，包括已退订/已关闭读权的历史，不再次提交rx_subscribe。
		r.published.Store(old)
		if !deliverRXSubscribe(r, result) {
			old.revoke()
		}
		return
	}
	if old != nil && (r.subscriptionID < old.subscriptionID || !old.terminal.Load()) {
		fail(ErrRXStaleSubscription)
		return
	}
	if e.pending >= rxSubscribeLimit {
		fail(ErrRXHandleBusy)
		return
	}
	h := &RXHandle{uuid: r.uuid, worker: c.Worker, generation: c.Generation, session: c.ID, subscriptionID: r.subscriptionID, service: e.ctx}
	r.published.Store(h)
	if r.delivery.Load() != 0 || r.ctx.Err() != nil {
		h.retire()
		fail(r.ctx.Err())
		return
	}
	c.rxPending = h
	e.pending++
	// 主循环永不等待：pending预留保证队列有界，防御性满队列分支也回滚全部所有权。
	select {
	case e.jobs <- rxSubscribeJob{r, h}:
	default:
		c.rxPending = nil
		e.pending--
		h.retire()
		fail(ErrRXHandleBusy)
	}
}
func (s *Server) handleRXSubscribeResult(result rxSubscribeResult) {
	e, h, r := s.rx, result.handle, result.request
	e.pending--
	c := s.calls[h.session]
	if c == nil || c.rxPending != h {
		h.retire()
		deliverRXSubscribe(r, rxSubscribeResult{handle: h, err: ErrRXHandleRetired})
		return
	}
	c.rxPending = nil
	sub := h.binding.Load()
	if sub == nil {
		h.retire()
		if result.err == nil {
			result.err = ErrRXHandleRetired
		}
		deliverRXSubscribe(r, rxSubscribeResult{reply: result.reply, err: result.err})
		return
	}
	// Pool本来就严格核验回执，此处仍冻结Server交付边界，防适配器返回另一流的成功。
	if result.err == nil && result.reply.OK && (result.reply.Type != "rx_state" || result.reply.Session != h.session || result.reply.SubscriptionID != h.subscriptionID || !slices.Contains([]string{"active", "stopped", "failed"}, result.reply.State)) {
		result.err = ErrRXSubscribeUnknown
	}
	h.observe(result.reply, result.err)
	// 底层可能受理后回执失联；保存封闭的清理句柄，但绝不能凭存在句柄就授权读音频。
	if result.err != nil || !result.reply.OK {
		h.revoke()
		if c.rxHandle != nil && c.rxHandle != h {
			c.rxHandle.retire()
		}
		c.rxHandle = h
		if c.Ended || !c.Allocated || c.Releasing || c.Worker != h.worker || c.Generation != h.generation {
			h.revoke()
		}
		if h.Retired() {
			result.err = ErrRXHandleRetired
		} else {
			result.err = ErrRXSubscribeUnknown
		}
		deliverRXSubscribe(r, rxSubscribeResult{handle: h, reply: result.reply, err: result.err})
		return
	}
	eligible, err := s.rxEligible(h.uuid)
	if err != nil || eligible != c || h.Retired() {
		h.revoke()
		if c.rxHandle != nil && c.rxHandle != h {
			c.rxHandle.retire()
		}
		c.rxHandle = h
		deliverRXSubscribe(r, rxSubscribeResult{handle: h, reply: result.reply, err: ErrRXHandleClosed})
		return
	}
	if c.rxHandle != nil && c.rxHandle != h {
		c.rxHandle.retire()
	}
	c.rxHandle = h
	if !h.readClosed.Load() && result.reply.State == "active" {
		h.authorized.Store(true)
	}
	if r.ctx.Err() != nil || !deliverRXSubscribe(r, rxSubscribeResult{handle: h, reply: result.reply}) {
		h.revoke()
	}
}

// finish首先撤销收音授权；实际Release或匹配代次死亡时退休所有控制句柄，不改动PCM/TX归属。
func (s *Server) retireCallRX(c *Call, actualRelease bool) {
	if c == nil {
		return
	}
	for _, h := range []*RXHandle{c.rxHandle, c.rxPending} {
		if h != nil {
			if actualRelease {
				h.retire()
			} else {
				h.revoke()
			}
		}
	}
}
func (s *Server) closeRX() {
	for _, c := range s.calls {
		s.retireCallRX(c, true)
	}
}
