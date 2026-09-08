package server

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync/atomic"

	"rustswitch/control/internal/media"
)

const pcmBeginLimit = 64 // 授权等待、在途begin和结果分别有界，不按音频帧驱动呼叫主循环。

var (
	ErrPCMHandleBusy    = errors.New("PCM handle busy")
	ErrPCMHandleClosed  = errors.New("PCM input closed; query or interrupt the turn")
	ErrPCMHandleRetired = errors.New("PCM handle retired")
	ErrPCMNotEligible   = errors.New("PCM requires an ACKed local processed G711 A leg")
	ErrPCMStaleTurn     = errors.New("PCM turn is older than the current turn")
	ErrPCMInvalid       = errors.New("invalid PCM begin arguments")
	ErrPCMBeginUnknown  = errors.New("PCM begin outcome unknown; query the same turn before retrying")
)

// pcmTransport是现有Pool的窄接口；测试可注入确定的暂停/错误，生产始终使用真实Pool。
type pcmTransport interface {
	Call(context.Context, int, media.Request) (media.Reply, error)
	Submit(context.Context, int, media.Request, func(media.Reply, error)) error
	PushPCM(context.Context, int, uint64, uint64, uint64, uint64, []int16) (media.PCMReply, error)
}

// PCMHandle冻结一次已授权媒体身份，不持有Call。所有音频操作在调用者线程直接进入独立媒体队列。
// 输入关闭与媒体终态分别记录；Go撤销不能被当作Rust已停止或终端已听到。
type PCMHandle struct {
	uuid                  string
	worker                int
	generation, session   uint64
	turnID                uint64
	bufferMS, prebufferMS uint16
	transport             pcmTransport
	service               context.Context
	committed             atomic.Bool
	inputClosed           atomic.Bool
	retired               atomic.Bool
	inflight              atomic.Bool // Push/End互斥，Interrupt不等待此标记。
	terminal              atomic.Bool // 仅真实成功的终态回执能够释放TX所有权。
}

func (h *PCMHandle) UUID() string   { return h.uuid }
func (h *PCMHandle) TurnID() uint64 { return h.turnID }

// Retired供外部有界注册表清理。普通interrupt不退休，仍可跨连接Status对账。
func (h *PCMHandle) Retired() bool {
	return h == nil || h.retired.Load() || h.service == nil || h.service.Err() != nil
}

func (h *PCMHandle) retire() {
	if h != nil {
		h.inputClosed.Store(true)
		h.retired.Store(true)
	}
}

func (h *PCMHandle) usable() error {
	if h.Retired() || h.transport == nil {
		return ErrPCMHandleRetired
	}
	if !h.committed.Load() {
		return ErrPCMHandleBusy
	}
	return nil
}

func (h *PCMHandle) request(op string) media.Request {
	return media.Request{Op: op, Generation: h.generation, Session: h.session, TurnID: h.turnID}
}

// observe只接收Pool已严格验证的真实回复。失败/撤销不自行填造stopped或completed。
func (h *PCMHandle) observe(reply media.Reply, err error) {
	if err != nil || !reply.OK {
		h.inputClosed.Store(true)
		return
	}
	if reply.Session != h.session || reply.TurnID != h.turnID || reply.Type != "pcm_turn_state" {
		h.inputClosed.Store(true)
		return
	}
	if reply.State == "completed" || reply.State == "stopped" || reply.State == "failed" {
		h.inputClosed.Store(true)
		h.terminal.Store(true)
	}
}

// Push每轮最多一个在途批次；入队与音频发送均不读取呼叫映射，也不经过普通信令队列。
func (h *PCMHandle) Push(ctx context.Context, offset uint64, samples []int16) (media.PCMReply, error) {
	if len(samples) == 0 || len(samples) > 800 || len(samples)%160 != 0 || offset%160 != 0 || offset > math.MaxUint64-uint64(len(samples)) {
		return media.PCMReply{}, ErrPCMInvalid
	}
	if err := h.usable(); err != nil {
		return media.PCMReply{}, err
	}
	if h.inputClosed.Load() {
		return media.PCMReply{}, ErrPCMHandleClosed
	}
	if !h.inflight.CompareAndSwap(false, true) {
		return media.PCMReply{}, ErrPCMHandleBusy
	}
	defer h.inflight.Store(false)
	if err := h.usable(); err != nil {
		return media.PCMReply{}, err
	}
	if h.inputClosed.Load() {
		return media.PCMReply{}, ErrPCMHandleClosed
	}
	reply, err := h.transport.PushPCM(ctx, h.worker, h.generation, h.session, h.turnID, offset, samples)
	// 确定的整批参数/偏移/容量拒绝可恢复，不能把正常背压变成永久关闭。
	var rejection *media.PCMRejection
	recoverable := errors.Is(err, media.ErrQueueFull) || (errors.As(err, &rejection) && (rejection.Code == 1 || rejection.Code == 7 || rejection.Code == 8))
	if (err != nil || reply.Code != 0) && !recoverable {
		h.inputClosed.Store(true)
	}
	return reply, err
}

// End取得同一个inflight标记后才提交JSON，不能超越仍排在二进制通道中的Push。
// draining不是终态，调用方必须继续Status；不为每通话创建轮询goroutine。
func (h *PCMHandle) End(ctx context.Context, finalSamples uint64) (media.Reply, error) {
	if finalSamples%160 != 0 {
		return media.Reply{}, ErrPCMInvalid
	}
	if err := h.usable(); err != nil {
		return media.Reply{}, err
	}
	if !h.inflight.CompareAndSwap(false, true) {
		return media.Reply{}, ErrPCMHandleBusy
	}
	defer h.inflight.Store(false)
	if err := h.usable(); err != nil {
		return media.Reply{}, err
	}
	request := h.request("pcm_turn_end")
	request.FinalSamples = finalSamples
	reply, err := h.transport.Call(ctx, h.worker, request)
	if reply.OK || (!errors.Is(err, media.ErrQueueFull) && reply.Type != "error") {
		// 成功结束或在途结果未知才关闭；确定final offset拒绝保留原输入，不会复位并发Interrupt。
		h.inputClosed.Store(true)
		h.observe(reply, err)
	}
	return reply, err
}

// Interrupt先关闭后续输入，独立JSON控制不等待数据在途；已提交UDP的包无法撤回。
func (h *PCMHandle) Interrupt(ctx context.Context, fadeMS uint16) (media.Reply, error) {
	if err := h.usable(); err != nil {
		return media.Reply{}, err
	}
	if fadeMS != 0 && fadeMS != 20 && fadeMS != 40 {
		return media.Reply{}, ErrPCMInvalid
	}
	h.inputClosed.Store(true)
	request := h.request("pcm_turn_interrupt")
	request.FadeMS = fadeMS
	reply, err := h.transport.Call(ctx, h.worker, request)
	h.observe(reply, err)
	return reply, err
}

func (h *PCMHandle) Status(ctx context.Context) (media.Reply, error) {
	if err := h.usable(); err != nil {
		return media.Reply{}, err
	}
	reply, err := h.transport.Call(ctx, h.worker, h.request("pcm_turn_status"))
	h.observe(reply, err)
	return reply, err
}

// abandon只清理已受理但无法交付的begin；提交失败保留未知TX归属，下一次同轮Begin可查询控制。
func (h *PCMHandle) abandon() {
	h.inputClosed.Store(true)
	if h.Retired() || h.transport == nil || !h.committed.Load() {
		return
	}
	err := h.transport.Submit(h.service, h.worker, h.request("pcm_turn_interrupt"), func(reply media.Reply, err error) { h.observe(reply, err) })
	if err != nil {
		h.inputClosed.Store(true)
	}
}

type pcmBeginRequest struct {
	ctx                   context.Context
	uuid                  string
	turnID                uint64
	bufferMS, prebufferMS uint16
	reply                 chan pcmBeginResult
	delivery              atomic.Uint32             // 0等待，1调用者放弃，2主循环已取得交付权；防取消与交付之间丢失所有者。
	published             atomic.Pointer[PCMHandle] // Submit前发布；取消认领后为nil严格表示尚未提交。
}

type pcmBeginResult struct {
	request *pcmBeginRequest
	handle  *PCMHandle
	reply   media.Reply
	err     error
}

type pcmService struct {
	ctx            context.Context
	transport      pcmTransport
	workerSnapshot func(int) (media.CapabilitySnapshot, bool)
	requests       chan *pcmBeginRequest
	results        chan pcmBeginResult
	pending        int // 仅主循环访问，最多64，确保固定worker回调不会堵在满结果队列上。
}

func (s *Server) initPCM() {
	e := &pcmService{ctx: s.ctx, transport: s.pool, requests: make(chan *pcmBeginRequest, pcmBeginLimit), results: make(chan pcmBeginResult, pcmBeginLimit)}
	e.workerSnapshot = func(worker int) (media.CapabilitySnapshot, bool) {
		if s.pool == nil || worker < 0 || worker >= len(s.pool.Workers) {
			return media.CapabilitySnapshot{}, false
		}
		return s.pool.Workers[worker].CapabilitySnapshot(), true
	}
	s.pcm = e
}

// BeginPCM是SDK授权入口；普通音频批次不经过此队列。相同轮次同配置幂等返回同一指针。
func (s *Server) BeginPCM(ctx context.Context, uuid string, turnID uint64, bufferMS, prebufferMS uint16) (*PCMHandle, media.Reply, error) {
	if uuid == "" || len(uuid) > 128 || strings.ContainsAny(uuid, " \t\r\n") || turnID == 0 || bufferMS < 20 || bufferMS > 1000 || bufferMS%20 != 0 || prebufferMS < 20 || prebufferMS > bufferMS || prebufferMS%20 != 0 {
		return nil, media.Reply{}, ErrPCMInvalid
	}
	e := s.pcm
	if e == nil || e.ctx == nil || e.ctx.Err() != nil {
		return nil, media.Reply{}, ErrPCMHandleRetired
	}
	if err := ctx.Err(); err != nil {
		return nil, media.Reply{}, err
	}
	r := &pcmBeginRequest{ctx: ctx, uuid: uuid, turnID: turnID, bufferMS: bufferMS, prebufferMS: prebufferMS, reply: make(chan pcmBeginResult, 1)}
	select {
	case e.requests <- r:
	case <-ctx.Done():
		return nil, media.Reply{}, ctx.Err()
	default:
		return nil, media.Reply{}, ErrPCMHandleBusy
	}
	select {
	case result := <-r.reply:
		if e.ctx.Err() != nil {
			if result.handle != nil {
				result.handle.retire()
			}
			return nil, media.Reply{}, ErrPCMHandleRetired
		}
		if ctx.Err() != nil && result.handle != nil {
			result.handle.abandon()
			return result.handle, result.reply, ErrPCMBeginUnknown
		}
		return result.handle, result.reply, result.err
	case <-ctx.Done():
		if !r.delivery.CompareAndSwap(0, 1) {
			// 主循环已经认领交付，只等待它写入单槽结果，不等待任何媒体RPC。
			result := <-r.reply
			if result.handle != nil {
				result.handle.abandon()
				return result.handle, result.reply, ErrPCMBeginUnknown
			}
			return nil, result.reply, result.err // 已取得明确负回复，不再伪称执行未知。
		}
		if h := r.published.Load(); h != nil {
			h.abandon() // pending仅关闭输入；实际Submit回执到来后再清理。
			return h, media.Reply{}, ErrPCMBeginUnknown
		}
		return nil, media.Reply{}, ctx.Err() // 发布之后还会检查delivery，因此nil表示确定未提交。
	case <-e.ctx.Done():
		r.delivery.CompareAndSwap(0, 1)
		return nil, media.Reply{}, ErrPCMHandleRetired
	}
}

func deliverPCMBegin(r *pcmBeginRequest, result pcmBeginResult) bool {
	if !r.delivery.CompareAndSwap(0, 2) {
		return false
	}
	r.reply <- result
	return true
}

func (s *Server) pcmEligible(uuid string) (*Call, error) {
	entry := s.compatChannels[uuid]
	c := entry.call
	if c == nil || entry.side != 0 || !c.Local || !c.AAck || !c.Established || !c.Allocated || c.Releasing || c.Ended || s.Config.Media.Processing != "g711" || c.Allocation.ProcessingTopology != "local" || c.Allocation.ProcessingVersion == nil || *c.Allocation.ProcessingVersion != 1 {
		return nil, ErrPCMNotEligible
	}
	codec := c.Offer.Codec
	if (codec.Name != "PCMU" && codec.Name != "PCMA") || codec.SampleRate != 8000 || codec.RTPClockRate != 8000 || codec.Channels != 1 || codec.PTimeMS != 20 || c.Offer.PTime != 20 {
		return nil, ErrPCMNotEligible
	}
	proof, ok := s.pcm.workerSnapshot(c.Worker)
	if !ok || !proof.Healthy || proof.PID <= 0 || proof.WorkerID != c.Worker || proof.Generation != c.Generation || c.Generation == 0 || !slices.Contains(proof.Capabilities, "processed_g711_v1") || !slices.Contains(proof.Capabilities, "processed_g711_local_v1") || !slices.Contains(proof.Capabilities, "pcm_turn_v1") {
		return nil, ErrPCMNotEligible
	}
	return c, nil
}

// pcmLegacyTXBusy扫描至多一个腿的8项已受理应用，防止新stream抢占正在播放或已保留的提示音。
func (s *Server) pcmLegacyTXBusy(uuid string) bool {
	if s.applications == nil {
		return false
	}
	lane := s.applications.lanes[uuid]
	if lane == nil {
		return false
	}
	if j := lane.active; j != nil && j.hasPlayback() && (j.mediaInflight || (j.playbackState != "completed" && j.playbackState != "stopped" && j.playbackState != "failed")) {
		return true
	}
	for _, j := range lane.queue {
		if j.hasPlayback() {
			return true
		}
	}
	return false
}

// pcmOwnsTX保守保留未知结果的归属；只有实际终态、代次死亡或release确认才能释放。
func (s *Server) pcmOwnsTX(c *Call) bool {
	return c != nil && (c.pcmPending != nil || (c.pcmHandle != nil && !c.pcmHandle.terminal.Load()))
}

func (s *Server) handlePCMBegin(r *pcmBeginRequest) {
	e := s.pcm
	fail := func(err error) { deliverPCMBegin(r, pcmBeginResult{err: err}) }
	if r.ctx.Err() != nil || r.delivery.Load() != 0 {
		fail(r.ctx.Err())
		return
	}
	c, err := s.pcmEligible(r.uuid)
	if err != nil {
		fail(err)
		return
	}
	if e.pending >= pcmBeginLimit || c.pcmPending != nil || s.pcmLegacyTXBusy(r.uuid) {
		fail(ErrPCMHandleBusy)
		return
	}
	old := c.pcmHandle
	if old != nil && r.turnID < old.turnID {
		fail(ErrPCMStaleTurn)
		return
	}
	if old != nil && r.turnID == old.turnID && (r.bufferMS != old.bufferMS || r.prebufferMS != old.prebufferMS) {
		fail(ErrPCMInvalid)
		return
	}
	h := old
	if h == nil || h.turnID != r.turnID {
		h = &PCMHandle{uuid: r.uuid, worker: c.Worker, generation: c.Generation, session: c.ID, turnID: r.turnID, bufferMS: r.bufferMS, prebufferMS: r.prebufferMS, transport: e.transport, service: e.ctx}
	}
	// 顺序不可交换：先发布、再复查取消认领、最后Submit；取消返回nil时绝不会出现后续媒体副作用。
	r.published.Store(h)
	if r.delivery.Load() != 0 || r.ctx.Err() != nil {
		if h != old {
			h.retire()
		}
		fail(r.ctx.Err())
		return
	}
	request := h.request("pcm_turn_begin")
	request.BufferMS, request.PrebufferMS = r.bufferMS, r.prebufferMS
	c.pcmPending = h
	e.pending++
	err = e.transport.Submit(e.ctx, c.Worker, request, func(reply media.Reply, err error) {
		select {
		case e.results <- pcmBeginResult{request: r, handle: h, reply: reply, err: err}:
		case <-e.ctx.Done():
			h.retire()
		}
	})
	if err != nil {
		c.pcmPending = nil
		e.pending--
		if h != old {
			h.retire()
		}
		fail(err)
		return
	}
	if old != nil && old != h {
		old.inputClosed.Store(true)
	}
}

func (s *Server) handlePCMBeginResult(result pcmBeginResult) {
	e, h, r := s.pcm, result.handle, result.request
	e.pending--
	c := s.calls[h.session]
	if c == nil || c.pcmPending != h {
		h.retire()
		deliverPCMBegin(r, pcmBeginResult{err: ErrPCMHandleRetired})
		return
	}
	c.pcmPending = nil
	if result.err != nil || !result.reply.OK {
		h.inputClosed.Store(true)
		knownRejection := (!result.reply.OK && result.reply.Type == "error") || errors.Is(result.err, media.ErrQueueFull)
		if !knownRejection && !h.Retired() && !c.Ended && c.Allocated && c.Worker == h.worker && c.Generation == h.generation {
			// 新turn可能已经生效；保存封闭的恢复句柄，不让WAV抢占未知TX，也不丢失新turn的控制地址。
			if c.pcmHandle != nil && c.pcmHandle != h {
				c.pcmHandle.retire()
			}
			c.pcmHandle = h
			h.committed.Store(true) // 仅Status/Interrupt可用；inputClosed仍为true，并不声称begin成功。
			h.terminal.Store(false) // 未知回执不能沿用更早的完成缓存来释放当前TX预留。
			deliverPCMBegin(r, pcmBeginResult{handle: h, reply: result.reply, err: ErrPCMBeginUnknown})
			return
		}
		if c.pcmHandle != h {
			h.retire()
		} // 明确负回复的候选能被外部有界注册表回收。
		if result.err == nil {
			result.err = errors.New(result.reply.Message)
		}
		deliverPCMBegin(r, pcmBeginResult{reply: result.reply, err: result.err})
		return
	}
	if eligible, err := s.pcmEligible(h.uuid); err != nil || eligible != c || h.Retired() {
		h.retire()
		deliverPCMBegin(r, pcmBeginResult{err: ErrPCMHandleRetired})
		return
	}
	if c.pcmHandle != nil && c.pcmHandle != h {
		c.pcmHandle.retire()
	}
	c.pcmHandle = h
	h.committed.Store(true)
	h.observe(result.reply, nil)
	if r.ctx.Err() != nil || !deliverPCMBegin(r, pcmBeginResult{handle: h, reply: result.reply}) {
		h.abandon()
	}
}

// retireCallPCM只撤销Go调用权限；actualRelease单独记录媒体确已无资源，不构造播放完成事件。
func (s *Server) retireCallPCM(c *Call, actualRelease bool) {
	if c == nil {
		return
	}
	for _, h := range []*PCMHandle{c.pcmHandle, c.pcmPending} {
		if h != nil {
			h.retire()
			if actualRelease {
				h.terminal.Store(true)
			}
		}
	}
}

// closePCM在Run退出后由Close调用，Call映射仍只属于这一条生命周期线程。
func (s *Server) closePCM() {
	for _, c := range s.calls {
		s.retireCallPCM(c, false)
	}
}
