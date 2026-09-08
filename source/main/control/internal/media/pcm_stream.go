package media

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
	pcmCapability = "pcm_turn_v1"
	pcmMaxSamples = 800 // 每批最多100毫秒8kHz mono S16LE，且只能包含完整20毫秒帧。
	pcmQueueSize  = 64  // 每worker最多64批等待及1批在途，不按通话创建数据goroutine。
	pcmReplySize  = 96
)

var (
	ErrPCMUnavailable    = errors.New("PCM data channel unavailable")
	ErrPCMOutcomeUnknown = errors.New("PCM push outcome unknown; query turn status before any retry")
)

// PCMReply保留数据通道原始请求/当前轮次和受理结果；成功仅代表Rust原子入队，不代表远端听到。
type PCMReply struct {
	Code             uint16 `json:"code"`
	Error            string `json:"error"`
	RequestID        uint64 `json:"request_id"`
	Session          uint64 `json:"session"`
	TurnID           uint64 `json:"turn_id"`
	CurrentTurnID    uint64 `json:"current_turn_id"`
	State            string `json:"state"`
	AcceptedSamples  uint64 `json:"accepted_samples"`
	SentSamples      uint64 `json:"sent_samples"`
	QueuedSamples    uint64 `json:"queued_samples"`
	DiscardedSamples uint64 `json:"discarded_samples"`
	OldestAgeMS      uint64 `json:"oldest_age_ms"`
}

// PCMRejection是确定的整批拒绝，和写出后超时的未知结果不同；所有失败都不会自动重新发送。
type PCMRejection struct {
	Code uint16
	Name string
}

func (e *PCMRejection) Error() string { return "PCM push rejected: " + e.Name }

type pcmResult struct {
	reply PCMReply
	err   error
}

// pcmJob的样本副本只归队列所有，调用者返回后修改原切片不会更改正在发送的数据。
type pcmJob struct {
	ctx                     context.Context
	session, turnID, offset uint64
	samples                 []int16
	result                  chan pcmResult
	state                   atomic.Uint32 // 0排队，1在途，2排队时取消；交付通道始终有一个空槽。
}

// pcmLane与一次serve生命周期绑定；数据故障只隔离该lane，JSON status/release仍然可用。
type pcmLane struct {
	mu      sync.Mutex // 封闭停止排空与提交之间的竞态；只覆盖入队，不等待socket。
	conn    *net.UnixConn
	jobs    chan *pcmJob
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	failed  atomic.Bool
	timeout time.Duration
	packet  [44 + pcmMaxSamples*2]byte // 数据协程独占固定收发缓冲，避免每批为两份wire另行分配。
	reply   [pcmReplySize + 1]byte
}

// newPCMLane创建无文件系统路径的私有Unix datagram socketpair；只有子进程继承对端FD。
func newPCMLane() (*pcmLane, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, err
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	parent, child := os.NewFile(uintptr(fds[0]), "pcm-parent"), os.NewFile(uintptr(fds[1]), "pcm-child")
	conn, err := net.FileConn(parent)
	_ = parent.Close() // net.FileConn已复制描述符，原始FD不再持有所有权。
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		_ = child.Close()
		return nil, nil, errors.New("PCM socketpair is not Unix datagram")
	}
	lane := &pcmLane{conn: unix, jobs: make(chan *pcmJob, pcmQueueSize), stop: make(chan struct{}), done: make(chan struct{}), timeout: time.Second}
	go lane.run()
	return lane, child, nil
}

func (l *pcmLane) close() {
	l.once.Do(func() {
		l.mu.Lock()
		l.failed.Store(true)
		close(l.stop)
		l.mu.Unlock()
		_ = l.conn.Close() // 立即唤醒在途读写，不等待一秒RPC期限。
	})
	<-l.done
}

// supportsPCMTurnLocked不看数据lane故障标记：故障后仍必须允许从JSON查询实际状态和停止旧轮次。
func (w *Worker) supportsPCMTurnLocked() bool {
	return w.Healthy.Load() && w.Generation.Load() > 0 && w.PID.Load() > 0 &&
		w.pcm != nil && slices.Contains(w.capabilities, pcmCapability)
}

// SupportsPCMTurn只报告同代次真实握手能力；它不证明某个通话可播放或数据通道仍可写。
func (w *Worker) SupportsPCMTurn() bool {
	w.submitMu.Lock()
	defer w.submitMu.Unlock()
	return w.supportsPCMTurnLocked()
}

// PushPCM只接收本地8kHz mono S16LE整帧。调用前必须已通过begin；Rust还会重新核对session/turn/offset。
// ctx在入队后取消可能发生在实际受理之后，此时明确返回ErrPCMOutcomeUnknown，不推断“未执行”。
func (p *Pool) PushPCM(ctx context.Context, worker int, generation, session, turnID, offset uint64, samples []int16) (PCMReply, error) {
	if err := ctx.Err(); err != nil {
		return PCMReply{}, err
	}
	if generation == 0 || session == 0 || turnID == 0 || len(samples) == 0 || len(samples) > pcmMaxSamples || len(samples)%160 != 0 || offset%160 != 0 || offset > math.MaxUint64-uint64(len(samples)) {
		return PCMReply{}, errors.New("invalid PCM batch identity, offset or sample count")
	}
	if worker < 0 || worker >= len(p.Workers) {
		return PCMReply{}, errors.New("invalid worker")
	}
	w := p.Workers[worker]
	w.submitMu.Lock()
	if p.ctx != nil && p.ctx.Err() != nil {
		w.submitMu.Unlock()
		return PCMReply{}, p.ctx.Err()
	}
	if !w.supportsPCMTurnLocked() || generation != w.Generation.Load() || w.pcm.failed.Load() {
		w.submitMu.Unlock()
		return PCMReply{}, ErrPCMUnavailable
	}
	lane := w.pcm
	lane.mu.Lock()
	if lane.failed.Load() {
		lane.mu.Unlock()
		w.submitMu.Unlock()
		return PCMReply{}, ErrPCMUnavailable
	}
	if len(lane.jobs) >= cap(lane.jobs) {
		// 消费者只会减少队列；所有生产者共用短锁，满载拒绝在分配样本副本/结果通道之前完成。
		lane.mu.Unlock()
		w.submitMu.Unlock()
		return PCMReply{}, ErrQueueFull
	}
	// 锁内仅复制最多1600字节并非阻塞入队，不把用户切片暴露给后台线程。
	j := &pcmJob{ctx: ctx, session: session, turnID: turnID, offset: offset, samples: append([]int16(nil), samples...), result: make(chan pcmResult, 1)}
	select {
	case lane.jobs <- j:
		lane.mu.Unlock()
		w.submitMu.Unlock()
	case <-ctx.Done():
		lane.mu.Unlock()
		w.submitMu.Unlock()
		return PCMReply{}, ctx.Err()
	default:
		lane.mu.Unlock()
		w.submitMu.Unlock()
		return PCMReply{}, ErrQueueFull
	}
	select {
	case r := <-j.result:
		return r.reply, r.err
	case <-ctx.Done():
		if j.state.CompareAndSwap(0, 2) {
			return PCMReply{}, ctx.Err()
		}
		// 不撤销已经写给Rust的批次；固定数据线程仍收取对应回复或隔离超时通道。
		return PCMReply{}, fmt.Errorf("%w: %v", ErrPCMOutcomeUnknown, ctx.Err())
	}
}

func (l *pcmLane) run() {
	defer close(l.done)
	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.failed.Store(true)
		_ = l.conn.Close()
		for {
			select {
			case j := <-l.jobs:
				j.result <- pcmResult{err: ErrPCMUnavailable}
			default:
				return
			}
		}
	}()
	var sequence uint64
	for {
		select {
		case <-l.stop:
			return
		case j := <-l.jobs:
			if l.failed.Load() {
				j.result <- pcmResult{err: ErrPCMUnavailable}
				return
			}
			if !j.state.CompareAndSwap(0, 1) || j.ctx.Err() != nil {
				j.result <- pcmResult{err: j.ctx.Err()}
				continue
			}
			if sequence == math.MaxUint64 {
				j.result <- pcmResult{err: ErrPCMUnavailable}
				return // 序号绝不回绕后与旧datagram匹配。
			}
			sequence++
			reply, err, fatal := l.exchange(sequence, j)
			j.result <- pcmResult{reply, err}
			if fatal {
				return
			}
		}
	}
}

func (l *pcmLane) exchange(id uint64, j *pcmJob) (PCMReply, error, bool) {
	packet := l.packet[:]
	copy(packet[:4], "RSP1")
	binary.LittleEndian.PutUint64(packet[8:16], id)
	binary.LittleEndian.PutUint64(packet[16:24], j.session)
	binary.LittleEndian.PutUint64(packet[24:32], j.turnID)
	binary.LittleEndian.PutUint64(packet[32:40], j.offset)
	binary.LittleEndian.PutUint16(packet[40:42], uint16(len(j.samples)))
	for i, sample := range j.samples {
		binary.LittleEndian.PutUint16(packet[44+i*2:46+i*2], uint16(sample))
	}
	if err := l.conn.SetDeadline(time.Now().Add(l.timeout)); err != nil {
		return PCMReply{}, fmt.Errorf("%w: %v", ErrPCMUnavailable, err), true
	}
	length := 44 + len(j.samples)*2
	if n, err := l.conn.Write(packet[:length]); err != nil || n != length {
		return PCMReply{}, fmt.Errorf("%w: datagram write %d/%d: %v", ErrPCMOutcomeUnknown, n, length, err), true
	}
	// 多留一字节检测超长回复，ReadMsg的MSG_TRUNC同时防止更长datagram被误当合法96字节。
	wire := l.reply[:]
	n, _, flags, _, err := l.conn.ReadMsgUnix(wire, nil)
	if err != nil {
		return PCMReply{}, fmt.Errorf("%w: %v", ErrPCMOutcomeUnknown, err), true
	}
	if n != pcmReplySize || flags&syscall.MSG_TRUNC != 0 {
		return PCMReply{}, fmt.Errorf("%w: invalid PCM reply length", ErrPCMOutcomeUnknown), true
	}
	reply, err := decodePCMReply(wire[:n], id, j)
	if err != nil {
		return reply, fmt.Errorf("%w: %v", ErrPCMOutcomeUnknown, err), true
	}
	if reply.Code != 0 {
		return reply, &PCMRejection{Code: reply.Code, Name: reply.Error}, false
	}
	return reply, nil, false
}

var pcmStates = [...]string{"idle", "buffering", "playing", "draining", "stopping", "completed", "stopped", "failed"}
var pcmCodes = [...]string{"", "invalid_packet", "unknown_session", "unsupported", "unknown_turn", "stale_turn", "closed", "offset_mismatch", "queue_full", "unavailable"}

func decodePCMReply(wire []byte, id uint64, j *pcmJob) (PCMReply, error) {
	if len(wire) != pcmReplySize || string(wire[:4]) != "RSR1" || wire[6] != 0 || wire[7] != 0 {
		return PCMReply{}, errors.New("invalid PCM reply header")
	}
	for _, b := range wire[81:] {
		if b != 0 {
			return PCMReply{}, errors.New("invalid PCM reply reserved bytes")
		}
	}
	code, state := binary.LittleEndian.Uint16(wire[4:6]), int(wire[80])
	if int(code) >= len(pcmCodes) || state >= len(pcmStates) {
		return PCMReply{}, errors.New("unknown PCM reply code or state")
	}
	r := PCMReply{Code: code, Error: pcmCodes[code], State: pcmStates[state]}
	values := []*uint64{&r.RequestID, &r.Session, &r.TurnID, &r.CurrentTurnID, &r.AcceptedSamples, &r.SentSamples, &r.QueuedSamples, &r.DiscardedSamples, &r.OldestAgeMS}
	for i, value := range values {
		*value = binary.LittleEndian.Uint64(wire[8+i*8 : 16+i*8])
	}
	if r.RequestID != id || r.Session != j.session || r.TurnID != j.turnID || (r.Code == 0 && (r.CurrentTurnID != j.turnID || r.AcceptedSamples != j.offset+uint64(len(j.samples)))) {
		return r, errors.New("PCM reply identity or atomic batch size mismatch")
	}
	if err := validatePCMCounters(r.AcceptedSamples, r.SentSamples, r.QueuedSamples, r.DiscardedSamples, 8000, r.OldestAgeMS); err != nil {
		return r, err
	}
	if (r.State == "idle" && (r.Code == 0 || r.CurrentTurnID != 0 || r.AcceptedSamples != 0)) || (r.State != "idle" && r.CurrentTurnID == 0) || ((r.State == "completed" || r.State == "stopped" || r.State == "failed") && r.QueuedSamples != 0) {
		return r, errors.New("inconsistent PCM terminal state")
	}
	return r, nil
}

func isPCMControl(op string) bool {
	return op == "pcm_turn_begin" || op == "pcm_turn_end" || op == "pcm_turn_interrupt" || op == "pcm_turn_status"
}

func validatePCMRequest(r Request) error {
	if r.Session == 0 || r.TurnID == 0 {
		return errors.New("PCM session and turn must be positive")
	}
	if r.Op == "pcm_turn_begin" && (r.BufferMS < 20 || r.BufferMS > 1000 || r.BufferMS%20 != 0 || r.PrebufferMS < 20 || r.PrebufferMS > r.BufferMS || r.PrebufferMS%20 != 0) {
		return errors.New("invalid PCM buffer or prebuffer")
	}
	if r.Op == "pcm_turn_end" && r.FinalSamples%160 != 0 {
		return errors.New("PCM final samples must contain whole frames")
	}
	if r.Op == "pcm_turn_interrupt" && r.FadeMS != 0 && r.FadeMS != 20 && r.FadeMS != 40 {
		return errors.New("invalid PCM fade duration")
	}
	return nil
}

// validatePCMCounters先减后比，避免恶意u64相加溢出后伪造样本守恒。
func validatePCMCounters(accepted, sent, queued, discarded, capacity, age uint64) error {
	if accepted%160 != 0 || sent%160 != 0 || queued%160 != 0 || discarded%160 != 0 || queued > capacity || sent > accepted || queued > accepted-sent || discarded != accepted-sent-queued || (queued == 0 && age != 0) {
		return errors.New("inconsistent PCM sample accounting")
	}
	return nil
}

func validatePCMControlReply(request Request, r Reply) error {
	if r.Type != "pcm_turn_state" || r.Session != request.Session || r.TurnID != request.TurnID || r.BufferMS < 20 || r.BufferMS > 1000 || r.BufferMS%20 != 0 || r.PrebufferMS < 20 || r.PrebufferMS > r.BufferMS || r.PrebufferMS%20 != 0 {
		return errors.New("invalid PCM state identity or buffer")
	}
	if !slices.Contains(pcmStates[1:], r.State) || ((r.State == "completed" || r.State == "stopped" || r.State == "failed") && r.QueuedSamples != 0) || (r.State == "failed" && r.Error == "") {
		return errors.New("invalid PCM control state")
	}
	if err := validatePCMCounters(r.AcceptedSamples, r.SentSamples, r.QueuedSamples, r.DiscardedSamples, uint64(r.BufferMS)*8, r.OldestAgeMS); err != nil {
		return err
	}
	if r.QueuedBytes != r.QueuedSamples*2 || r.QueuedMS != r.QueuedSamples/8 || (request.Op == "pcm_turn_begin" && (r.BufferMS != request.BufferMS || r.PrebufferMS != request.PrebufferMS)) {
		return errors.New("PCM byte, duration or frozen buffer mismatch")
	}
	if request.Op == "pcm_turn_end" && r.AcceptedSamples != request.FinalSamples {
		return errors.New("PCM final sample count mismatch")
	}
	return nil
}

// UnmarshalJSON只对新增PCM成功回执附加字段存在性检查，旧协议保持原有解码行为。
// 显式零值是证据；缺失/null不能被Go零值悄悄解释成“队列为空且一切正常”。
func (r *Reply) UnmarshalJSON(data []byte) error {
	type plain Reply
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Type == "pcm_turn_state" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		for _, key := range []string{"id", "ok", "type", "session", "turn_id", "state", "accepted_samples", "sent_samples", "discarded_samples", "queued_samples", "queued_bytes", "queued_ms", "oldest_age_ms", "buffer_ms", "prebuffer_ms", "error"} {
			value, exists := fields[key]
			if !exists || string(value) == "null" {
				return fmt.Errorf("missing PCM state field: %s", key)
			}
		}
	}
	*r = Reply(decoded)
	return nil
}
