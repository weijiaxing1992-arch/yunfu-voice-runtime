// Package media 通过有界 JSON 控制管道管理 Rust 进程；主动 PCM 使用独立的有界二进制通道。
package media

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"rustswitch/control/internal/config"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Peer 保存某一腿的两个远端地址；当前协议使用独立 RTP/RTCP 端口及 IPv4。
type Peer struct {
	RTP  string `json:"rtp"`  // RTP 目标地址与端口。
	RTCP string `json:"rtcp"` // RTCP 目标地址与端口。
}

// CodecSpec 描述 RTP 上实际使用的编码格式；不表示已经装载对应转码器。
// SampleRate 是编码的名义 PCM 采样率，RTPClockRate 是时间戳时钟；G.722 两者不同。
// Channels 保留 SDP 的声明（Opus 固定为 2），不能据此推断每个 Opus 包都含双声道。
type CodecSpec struct {
	Name         string `json:"name"`           // 规范名称；G726 与 AAL2-G726 的打包位序不可互换。
	SampleRate   uint32 `json:"sample_rate"`    // 名义 PCM 采样率，单位 Hz；不描述内部归一化采样格式。
	RTPClockRate uint32 `json:"rtp_clock_rate"` // RTP 时间戳每秒刻度。
	Channels     uint8  `json:"channels"`       // SDP 声道数；L16 必须是网络大端有符号 PCM。
	PTimeMS      uint16 `json:"ptime_ms"`       // SDP 中接收方期望的分包间隔，单位毫秒。
	FMTP         string `json:"fmtp"`           // 已校验并规范化的格式参数，空字符串表示标准默认值。
}

// Request 是控制进程到媒体进程的一条命令；JSON 字段必须与 Rust 端的消息定义同步。
type Request struct {
	Processing     *ProcessingPlan `json:"processing,omitempty"`      // 建呼时冻结的处理合同；只有已握手能力的worker可接纳。
	Generation     uint64          `json:"-"`                         // 仅供 Go 检查进程代次，不编码到媒体命令。
	ID             uint64          `json:"id"`                        // 当前进程内的请求序号，用于关联回复。
	Op             string          `json:"op"`                        // 会话、统计、事件拉取或有界提示音操作。
	Session        uint64          `json:"session,omitempty"`         // 控制面分配的通话资源编号。
	A              *Peer           `json:"a,omitempty"`               // 分配阶段提供的 A 腿地址。
	B              *Peer           `json:"b,omitempty"`               // 连接阶段提供的 B 腿地址。
	Payload        uint8           `json:"payload"`                   // 已协商的音频 RTP 载荷类型。
	DTMFPayload    *uint8          `json:"dtmf_payload"`              // 电话事件载荷；nil 编码为 null，明确取消此前配置。
	Codec          *CodecSpec      `json:"codec,omitempty"`           // 新协商显式传递编码；省略仅兼容原有 PT 0/8 命令。
	DTMFClockRate  uint32          `json:"dtmf_clock_rate,omitempty"` // 电话事件自己的时间戳时钟，不固定推断成 8000。
	DTMFEvents     string          `json:"dtmf_events,omitempty"`     // 已协商事件集合，例如 0-15；不能扩大终端声明范围。
	CNPayload      *uint8          `json:"cn_payload"`                // 可选 RFC 3389 舒适噪声载荷；null 取消此前配置。
	CNClockRate    uint32          `json:"cn_clock_rate,omitempty"`   // 舒适噪声的独立时钟，须与所选主编码匹配。
	AfterSeq       uint64          `json:"after_seq,omitempty"`       // worker 代次内的 DTMF 游标，重启后必须重新建立基线。
	Limit          int             `json:"limit,omitempty"`           // 每次最多读取 64 个事件，保持控制行和业务处理有界。
	PlaybackID     uint64          `json:"playback_id,omitempty"`     // 会话内非零播放编号；同编号同参数支持重试。
	Leg            string          `json:"leg,omitempty"`             // 播放目的腿 a 或 b，不接受任意网络目的地。
	FrequencyHz    uint16          `json:"frequency_hz,omitempty"`    // 原生单频提示音，限定 200–2000 Hz。
	DurationMS     uint32          `json:"duration_ms,omitempty"`     // 提示音时长，20–10000 毫秒且须为 20 的倍数。
	FilePath       string          `json:"path,omitempty"`            // playback_root 内相对 WAV；禁止绝对地址、上级目录或 URL。
	Digits         string          `json:"digits,omitempty"`          // 本地A腿待发DTMF，限定1–32个协商数字；时长复用DurationMS。
	SubscriptionID uint64          `json:"subscription_id,omitempty"` // RX独立订阅身份，不借用下行轮次或TX所有权。
	TurnID         uint64          `json:"turn_id,omitempty"`         // 调用方提供的非零轮次；不跨会话或进程代次复用。
	BufferMS       uint16          `json:"buffer_ms,omitempty"`       // PCM缓冲20–1000毫秒，20的整数倍；begin必须明确提供。
	PrebufferMS    uint16          `json:"prebuffer_ms,omitempty"`    // 首播缓冲20毫秒至BufferMS，20的整数倍。
	FinalSamples   uint64          `json:"final_samples"`             // end固定最终受理样本数；0也必须编码，避免空轮次无法结束。
	FadeMS         uint16          `json:"fade_ms,omitempty"`         // interrupt淡出只允许0、20、40毫秒。
}

// DTMFEvent 是真实 RTP telephone-event 的完成或不完整证据；不代表带内音频识别。
// Sequence 只在一个 worker 代次内递增；Kind=incomplete 不能作为数字或静音返回。
type DTMFEvent struct {
	Sequence      uint64 `json:"sequence"`       // worker 全局顺序，按读取结果推进游标。
	Session       uint64 `json:"session"`        // 分配时的媒体会话编号。
	Leg           string `json:"leg"`            // 输入来源腿 a 或 b。
	Kind          string `json:"kind"`           // digit 为完整事件；incomplete 为无法确认的输入。
	Digit         string `json:"digit"`          // 0–9、*、#、A–D；事件 16 为 flash，保留空字符串。
	Event         uint8  `json:"event"`          // 原始 RFC 4733 事件编号，不扩展协商集合。
	DurationTicks uint16 `json:"duration_ticks"` // 原始持续时钟刻度，不能固定按 8 kHz 换算。
	ClockRate     uint32 `json:"clock_rate"`     // 本事件协商的独立时钟。
	SSRC          uint32 `json:"ssrc"`           // 已通过源地址校验的 RTP 同步源。
	Timestamp     uint32 `json:"timestamp"`      // 事件起点 RTP 时间戳。
	Reason        string `json:"reason"`         // 不完整事件的原因；正常完成为空。
}

// Reply 表示握手、操作确认或统计响应；OK 表示命令结果，不表示 SIP 呼叫已经建立。
type Reply struct {
	Capabilities       []string       `json:"capabilities"`                  // 启动时声明的精确能力集合。
	ProcessingTopology string         `json:"processing_topology,omitempty"` // 本地分配必须明确local；旧bridge/relay不添加此字段。
	ProcessingVersion  *uint32        `json:"processing_version"`            // 分配实际处理状态的版本证据，不能用OK替代。
	ID                 uint64         `json:"id"`                            // 回显控制请求序号。
	OK                 bool           `json:"ok"`                            // 媒体命令是否成功。
	Type               string         `json:"type"`                          // ready、allocated 等响应类别。
	Message            string         `json:"message"`                       // 失败时的诊断信息。
	ProtocolVersion    int            `json:"protocol_version"`              // 握手时的内部管道协议版本。
	WorkerID           int            `json:"worker_id"`                     // 媒体分片编号。
	PID                int            `json:"pid"`                           // 媒体进程报告的进程号。
	Session            uint64         `json:"session"`                       // 此响应对应的通话资源编号。
	ARTP               int            `json:"a_rtp"`                         // A 腿本地 RTP 接收端口。
	ARTCP              int            `json:"a_rtcp"`                        // A 腿本地 RTCP 接收端口。
	BRTP               int            `json:"b_rtp"`                         // B 腿本地 RTP 接收端口。
	BRTCP              int            `json:"b_rtcp"`                        // B 腿本地 RTCP 接收端口。
	Stats              map[string]any `json:"stats"`                         // 当前媒体统计；发布后只读。
	Events             []DTMFEvent    `json:"events"`                        // 有界按键批次，空批次不能证明来话音频静默。
	NextSeq            uint64         `json:"next_seq"`                      // 仅推进到本批次实际交付末尾，避免跳过尚未取出的事件。
	OldestSeq          uint64         `json:"oldest_seq"`                    // 环形日志仍保留的最早事件编号。
	Overflow           bool           `json:"overflow"`                      // 游标已落在日志保留区之前，业务收号必须明确失败。
	PlaybackID         uint64         `json:"playback_id"`                   // 播放响应核对请求所属任务。
	State              string         `json:"state"`                         // running、completed、stopped 或 failed。
	SentPackets        uint64         `json:"sent_packets"`                  // 提示音被本机 UDP 发送接受的包数，不证明远端播放。
	TotalPackets       uint64         `json:"total_packets"`                 // 提示音标称 20 毫秒包数，失败不缩小此分母。
	AcceptedDigits     uint64         `json:"accepted_digits"`               // 当前会话DTMF累计受理数，受理不代表完成。
	CompletedDigits    uint64         `json:"completed_digits"`              // 三个结束包均已交给UDP的数字数。
	FailedDigits       uint64         `json:"failed_digits"`                 // 调度或UDP失败导致取消的数字数。
	QueuedDigits       uint32         `json:"queued_digits"`                 // 包含当前数字的有界队列长度，上限32。
	LastError          string         `json:"last_error"`                    // 最近一次发送失败；后续受理不抹除历史错误。
	SubscriptionID     uint64         `json:"subscription_id"`               // RX成功回执必须精确对应所查询订阅。
	ProducedEvents     uint64         `json:"produced_events"`               // Rust产生的真实观察；gap及终态不进入此计数。
	SubmittedEvents    uint64         `json:"submitted_events"`              // 已交给Unix socket的观察，不代表ASR已处理。
	DroppedEvents      uint64         `json:"dropped_events"`                // Rust出口丢弃的观察，通过RXS2 gap明确说明。
	QueuedEvents       uint64         `json:"queued_events"`                 // Rust每订阅最多8个等待观察。
	ProducedSamples    uint64         `json:"produced_samples"`              // Decoded/HistoryPLC产生的原生8k样本累计数。
	SubmittedSamples   uint64         `json:"submitted_samples"`             // 已提交Unix socket的PCM样本。
	DroppedSamples     uint64         `json:"dropped_samples"`               // 出口丢弃的PCM样本；CN/边界无样本。
	TurnID             uint64         `json:"turn_id"`                       // PCM状态必须回显所查询的轮次，不返回其他轮次冒充成功。
	AcceptedSamples    uint64         `json:"accepted_samples"`              // 已原子受理的PCM样本数；受理不代表发送或远端听到。
	SentSamples        uint64         `json:"sent_samples"`                  // 已提交本机UDP的样本数。
	DiscardedSamples   uint64         `json:"discarded_samples"`             // 被停止、过期或失败撤销的样本数。
	QueuedSamples      uint64         `json:"queued_samples"`                // 仍归媒体队列所有的样本数。
	QueuedBytes        uint64         `json:"queued_bytes"`                  // S16LE队列字节数，严格等于QueuedSamples的两倍。
	QueuedMS           uint64         `json:"queued_ms"`                     // 8kHz队列时长，严格等于QueuedSamples除以8。
	OldestAgeMS        uint64         `json:"oldest_age_ms"`                 // 最早待发PCM的实际年龄；空队列为零。
	BufferMS           uint16         `json:"buffer_ms"`                     // 当前轮次已冻结的缓冲容量。
	PrebufferMS        uint16         `json:"prebuffer_ms"`                  // 当前轮次已冻结的首播门槛。
	Error              string         `json:"error"`                         // PCM失败静态原因码，不能当作自由文本命令。
}

// WorkerConfig 是启动单个媒体进程时冻结的配置，每个进程拥有互不重叠的端口分区。
type WorkerConfig struct {
	Processing                string   `json:"-"`                              // 仅Go监督器使用；不发送给旧worker的启动配置。
	PlaybackRoot              string   `json:"playback_root,omitempty"`        // 冻结的本地提示文件目录；空值不创建文件加载线程。
	ExcludedPortBlocks        []int    `json:"excluded_port_blocks,omitempty"` // 本分片禁止分配的四端口块起点；省略保持旧协议行为。
	ConnectSockets            bool     `json:"connect_sockets"`                // 是否对媒体 UDP socket 设置固定远端。
	WorkerID                  int      `json:"worker_id"`                      // 从零开始的分片编号。
	BindIP                    string   `json:"bind_ip"`                        // 本地媒体绑定地址。
	PortStart                 int      `json:"port_start"`                     // 本分片起始端口，包含边界。
	PortEnd                   int      `json:"port_end"`                       // 本分片结束端口，包含边界。
	MaxCalls                  int      `json:"max_calls"`                      // 本分片同时分配的媒体会话硬上限。
	ReceiveBufferBytes        int      `json:"receive_buffer_bytes"`           // 每个 socket 请求的接收缓冲区大小。
	PortReuseDelayMS          int      `json:"port_reuse_delay_ms"`            // 回收后端口隔离期，避免旧报文串入新呼叫。
	MaxPacketsPerSecondPerLeg int      `json:"max_packets_per_second_per_leg"` // 每腿允许处理的包速率预算。
	AllowedRemoteNetworks     []string `json:"allowed_remote_networks"`        // 允许媒体目的地址所属的网段，构造后只读。
	CPUCore                   *int     `json:"cpu_core"`                       // Linux 可选绑核编号；nil 表示不指定。
}

// result 将媒体回复和管道错误合并为一次交付给等待者的结果。
type result struct {
	reply Reply // 解码后的回复；失败时可能为零值。
	err   error // 排队、进程或命令层面的错误。
}

// job 是进入分片控制队列的任务；结果通道留一个位置，调用者取消后监督线程仍能返回结果。
type job struct {
	ctx       context.Context    // 提交者的取消上下文。
	request   Request            // 入队时的命令值及进程代次。
	result    chan result        // 本任务独占的单次结果通道。
	callback  func(Reply, error) // 非等待型完成入口，由固定分片协程调用，不额外创建 goroutine。
	callState *atomic.Uint32     // 同步调用的取消仲裁：0排队、1已进入执行、2已在排队阶段取消。
}

// finish 交付一次完整结果；回调只投递到上层有界结果队列，不能直接修改呼叫状态。
func (j job) finish(reply Reply, err error) {
	if j.callback != nil {
		j.callback(reply, err)
		return
	}
	j.result <- result{reply: reply, err: err}
}

// ErrQueueFull 表示请求尚未进入媒体执行队列，可以安全退避，不等同于在途执行失败。
var ErrQueueFull = errors.New("media control queue full")

// Failure 通知呼叫主循环清理指定进程代次的通话，避免波及重启后的新资源。
type Failure struct {
	Worker     int    // 失败分片编号。
	Generation uint64 // 已终止的进程代次。
	Err        error  // 终止原因。
}

// Worker 保存稳定的分片身份和线程安全的观测状态；其监督协程串行处理实际管道命令。
type Worker struct {
	submitMu          sync.Mutex                // 串行化入队与健康代次切换，防止清空队列后插入无人处理的任务。
	Config            WorkerConfig              // 启动时冻结的分片配置。
	jobs              chan job                  // 有界新分配队列。
	urgent            chan job                  // 有界连接和释放队列，优先服务已有通话。
	adaptiveAdmission bool                      // 是否启用媒体压力准入。
	cpuHigh, cpuLow   float64                   // CPU 拒绝和恢复阈值，形成迟滞区间。
	admission         atomic.Pointer[Admission] // 最近发布的不可变压力采样。
	Healthy           atomic.Bool               // 当前进程是否完成握手且仍可处理命令。
	Generation        atomic.Uint64             // 每次成功握手递增的进程代次。
	PID               atomic.Int64              // 当前进程号；无进程时为零。
	Restarts          atomic.Uint64             // 已触发的重启次数。
	snapshot          atomic.Value              // 最近媒体统计映射；发布后不可修改。
	capabilities      []string                  // 真实 ready 握手中的能力；只由 submitMu 保护，与进程代次一同发布。
	rx                *rxLane                   // 当前代次独占上行接收通道；submitMu保护身份发布与撤销。
	pcm               *pcmLane                  // 当前代次独占的数据通道；submitMu保护发布与撤销。
}

// CapabilitySnapshot 是某一时刻的进程握手证据；不能代表通话质量或整机容量通过验收。
type CapabilitySnapshot struct {
	WorkerID     int      `json:"worker_id"`
	Generation   uint64   `json:"generation"`
	PID          int64    `json:"pid"`
	Healthy      bool     `json:"healthy"`
	Capabilities []string `json:"capabilities"`
}

// CapabilitySnapshot 在短锁内取得同一代次的健康状态与能力，返回独立切片避免外部修改握手证据。
// 该读取仅发生在管理请求中，不给 Rust 的每帧处理增加锁或分配。
func (w *Worker) CapabilitySnapshot() CapabilitySnapshot {
	w.submitMu.Lock()
	defer w.submitMu.Unlock()
	result := CapabilitySnapshot{WorkerID: w.Config.WorkerID, Generation: w.Generation.Load(), PID: w.PID.Load(), Healthy: w.Healthy.Load(), Capabilities: []string{}}
	if result.Healthy && result.Generation > 0 && result.PID > 0 {
		result.Capabilities = append(result.Capabilities, w.capabilities...)
	} else {
		result.Healthy = false
	}
	return result
}

// Snapshot 读取最近发布的统计映射；返回值共享底层对象，调用者必须只读。
func (w *Worker) Snapshot() map[string]any {
	v := w.snapshot.Load()
	if v == nil {
		return map[string]any{}
	}
	return v.(map[string]any)
}

// Pool 管理所有媒体分片的生命周期，负责将进程故障交给呼叫主循环。
type Pool struct {
	ctx      context.Context    // 池关闭后唤醒使用独立上下文的同步调用者。
	Workers  []*Worker          // 启动后固定的分片列表。
	Failures chan Failure       // 有界进程故障通知队列。
	cancel   context.CancelFunc // 同时取消所有监督协程。
	wg       sync.WaitGroup     // 等待全部子进程监督循环退出。
}

// Start 按四端口会话块划分资源，等待每个分片完成首次握手后才返回可用进程池。
func Start(parent context.Context, c config.Config) (*Pool, error) {
	ctx, cancel := context.WithCancel(parent)
	p := &Pool{ctx: ctx, cancel: cancel, Failures: make(chan Failure, c.Media.Workers*4)}
	blocks := (c.Media.PortEnd - c.Media.PortStart + 1) / 4
	offset := 0
	// 余数优先分配给前面的分片，端口块和会话硬上限分别均分。
	for id := 0; id < c.Media.Workers; id++ {
		count := blocks / c.Media.Workers
		if id < blocks%c.Media.Workers {
			count++
		}
		maxCalls := c.Limits.MaxCalls / c.Media.Workers
		if id < c.Limits.MaxCalls%c.Media.Workers {
			maxCalls++
		}
		wc := WorkerConfig{Processing: c.Media.Processing, PlaybackRoot: c.Media.PlaybackRoot, ConnectSockets: c.Media.ConnectSockets, WorkerID: id, BindIP: c.Media.BindIP, PortStart: c.Media.PortStart + offset*4, PortEnd: c.Media.PortStart + (offset+count)*4 - 1, MaxCalls: maxCalls, ReceiveBufferBytes: c.Media.ReceiveBufferBytes, PortReuseDelayMS: c.Media.PortReuseDelayMS, MaxPacketsPerSecondPerLeg: c.Media.MaxPacketsPerSecondPerLeg, AllowedRemoteNetworks: c.Media.AllowedRemoteNetworks}
		// 配置已经校验块起点和对齐；每个 worker 只接收自己区间内的排除项，分片边界保持稳定。
		for _, base := range c.Media.ExcludedPortBlocks {
			if base >= wc.PortStart && base+3 <= wc.PortEnd {
				wc.ExcludedPortBlocks = append(wc.ExcludedPortBlocks, base)
			}
		}
		if len(c.Media.CPUCores) > 0 {
			v := c.Media.CPUCores[id]
			wc.CPUCore = &v
		}
		offset += count
		w := &Worker{Config: wc, jobs: make(chan job, 64), urgent: make(chan job, 64), adaptiveAdmission: c.Media.AdaptiveAdmission, cpuHigh: c.Media.AdmissionCPUHigh, cpuLow: c.Media.AdmissionCPULow}
		p.Workers = append(p.Workers, w)
	}
	started := make(chan error, len(p.Workers))
	for _, w := range p.Workers {
		p.wg.Add(1)
		go func(w *Worker) { defer p.wg.Done(); p.supervise(ctx, c.Media.Binary, w, started) }(w)
	}
	for range p.Workers {
		if err := <-started; err != nil {
			p.Close()
			return nil, err
		}
	}
	return p, nil
}

// Close 取消池上下文并等待监督协程退出，确保不遗留子进程。
func (p *Pool) Close() { p.cancel(); p.wg.Wait() }

// Call 保留同步等待入口；控制服务使用 Submit，在主循环顺序内直接入队。
// 取消可以撤销未执行命令；命令一旦在途，必须在 RPC 有界期限内交付确定结果，避免丢失分配所有权。
func (p *Pool) Call(ctx context.Context, worker int, request Request) (Reply, error) {
	j := job{ctx: ctx, request: request, result: make(chan result, 1), callState: &atomic.Uint32{}}
	if err := p.enqueue(worker, j); err != nil {
		return Reply{}, err
	}
	var closed <-chan struct{}
	if p.ctx != nil {
		closed = p.ctx.Done()
	}
	select {
	case r := <-j.result:
		return r.reply, r.err
	case <-ctx.Done():
		if j.callState.CompareAndSwap(0, 2) {
			return Reply{}, ctx.Err()
		}
		// 执行已经开始，丢弃成功确认会让调用者永远不知道需要释放哪份资源。
		select {
		case r := <-j.result:
			return r.reply, r.err
		case <-closed:
			return Reply{}, p.ctx.Err()
		}
	case <-closed:
		return Reply{}, p.ctx.Err()
	}
}

// Submit 在调用线程内尝试入队，成功后由固定分片协程回调；失败时不会调用回调。
// 同一调用线程的同优先级命令维持 FIFO；队列满立即返回，不创建隐形等待 goroutine。
func (p *Pool) Submit(ctx context.Context, worker int, request Request, callback func(Reply, error)) error {
	if callback == nil {
		return errors.New("media completion callback required")
	}
	return p.enqueue(worker, job{ctx: ctx, request: request, callback: callback})
}

// enqueue 在健康代次锁内非阻塞入队，不持锁等待管道或调用者。
func (p *Pool) enqueue(worker int, j job) error {
	ctx, request := j.ctx, j.request
	if err := ctx.Err(); err != nil {
		return err
	}
	if worker < 0 || worker >= len(p.Workers) {
		return errors.New("invalid worker")
	}
	w := p.Workers[worker]
	w.submitMu.Lock()
	defer w.submitMu.Unlock()
	if p.ctx != nil && p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	if !w.Healthy.Load() {
		return errors.New("media worker unavailable")
	}
	if (isPCMControl(request.Op) || isRXControl(request.Op)) && request.Generation == 0 {
		return errors.New("stream control requires explicit media generation")
	}
	if request.Generation == 0 {
		request.Generation = w.Generation.Load()
	}
	if request.Generation != w.Generation.Load() {
		return errors.New("stale media generation")
	}
	if isPCMControl(request.Op) {
		if !w.supportsPCMTurnLocked() {
			return ErrPCMUnavailable
		}
		if err := validatePCMRequest(request); err != nil {
			return err
		}
	}
	if isRXControl(request.Op) {
		if !w.supportsRXLocked() {
			return ErrRXUnavailable
		}
		if err := validateRXRequest(request); err != nil {
			return err
		}
	}
	if err := validateProcessingPlan(request.Processing); err != nil {
		return err
	}
	if request.Processing != nil {
		if request.Op != "allocate" {
			return errors.New("processing plan is only valid for allocation")
		}
		if request.Processing.Topology == "local" && (w.Generation.Load() == 0 || w.PID.Load() <= 0 || !w.supportsLocalProcessingLocked()) {
			return errors.New("media worker lacks processed_g711_local_v1 capability")
		}
		// 固定处理计划的值归队列所有，调用方之后改指针不会改写已受理请求。
		frozen := *request.Processing
		request.Processing = &frozen
	}
	j.request = request
	queue := w.jobs
	if request.Op != "allocate" {
		queue = w.urgent
	}
	select {
	case queue <- j:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrQueueFull
	}
}

// supervise 维护一个分片的重启循环，在重新使用端口前通知故障并清空旧代次队列。
func (p *Pool) supervise(ctx context.Context, binary string, w *Worker, started chan<- error) {
	first := true
	for {
		if ctx.Err() != nil {
			if first {
				started <- ctx.Err()
			}
			return
		}
		err := p.serve(ctx, binary, w, func(err error) {
			if first {
				started <- err
				first = false
			}
		})
		w.submitMu.Lock()
		w.Healthy.Store(false)
		w.submitMu.Unlock()
		w.PID.Store(0)
		// 新进程代次开始接收任务前，明确拒绝所有仍留在队列中的旧命令。
		for {
			select {
			case j := <-w.urgent:
				j.finish(Reply{}, errors.New("media generation ended"))
			case j := <-w.jobs:
				j.finish(Reply{}, errors.New("media generation ended"))
			default:
				goto drained
			}
		}
	drained:
		if ctx.Err() != nil {
			return
		}
		generation := w.Generation.Load()
		select {
		case p.Failures <- Failure{Worker: w.Config.WorkerID, Generation: generation, Err: err}:
		case <-ctx.Done():
			return
		}
		slog.Error("media worker stopped", "worker", w.Config.WorkerID, "generation", generation, "error", err)
		w.Restarts.Add(1)
		// 按配置等待旧报文过期；至少等待一秒，避免立即复用整个失败分片的端口。
		delay := time.Duration(w.Config.PortReuseDelayMS) * time.Millisecond
		if delay < time.Second {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// validateReply 检查成功变体及分配所有权，不能仅凭 OK=true 释放预留或把错误端口写入 SDP。
// 合法业务拒绝由调用者处理；响应结构错配属于当前进程代次的协议故障。
func (w *Worker) validateReply(request Request, reply Reply) error {
	if !reply.OK {
		if reply.Type != "error" {
			return errors.New("invalid media error reply")
		}
		return nil
	}
	switch request.Op {
	case "allocate":
		if request.Processing == nil && reply.ProcessingTopology != "" {
			return errors.New("media worker enabled unrequested topology")
		}
		if request.Processing != nil {
			local := request.Processing.Topology == "local"
			if (local && reply.ProcessingTopology != "local") || (!local && reply.ProcessingTopology != "" && reply.ProcessingTopology != "bridge") {
				return errors.New("media worker did not allocate requested processing topology")
			}
		}
		if request.Processing == nil && reply.ProcessingVersion != nil {
			return errors.New("media worker enabled unrequested processing")
		}
		if request.Processing != nil && (reply.ProcessingVersion == nil || *reply.ProcessingVersion != request.Processing.Version) {
			return errors.New("media worker did not allocate requested processing plan")
		}
		if reply.Type != "allocated" || reply.Session != request.Session || reply.ARTP < w.Config.PortStart || reply.ARTP%2 != 0 || reply.ARTCP != reply.ARTP+1 || reply.BRTP != reply.ARTP+2 || reply.BRTCP != reply.ARTP+3 || reply.BRTCP > w.Config.PortEnd {
			return errors.New("invalid media allocation reply")
		}
	case "connect", "release", "shutdown":
		if reply.Type != "ack" {
			return errors.New("invalid media acknowledgement")
		}
	case "stats":
		if reply.Type != "stats" || reply.Stats == nil {
			return errors.New("invalid media stats reply")
		}
		if err := validateDTMFSendStats(reply.Stats); err != nil {
			return err
		}
		if w.Config.Processing == "g711" {
			if err := validateProcessingStats(reply.Stats, w.Config.MaxCalls); err != nil {
				return err
			}
			if w.SupportsLocalProcessing() {
				if err := validateLocalProcessingStats(reply.Stats); err != nil {
					return err
				}
			}
		}
	case "dtmf_events":
		if err := validateDTMFReply(request, reply); err != nil {
			return err
		}
	case "dtmf_send", "dtmf_send_status":
		if err := validateDTMFSendReply(request, reply); err != nil {
			return err
		}
	case "rx_subscribe", "rx_status", "rx_unsubscribe":
		return validateRXControlReply(request, reply)
	case "pcm_turn_begin", "pcm_turn_end", "pcm_turn_interrupt", "pcm_turn_status":
		return validatePCMControlReply(request, reply)
	case "playback_start", "playback_file_start", "playback_status", "playback_stop":
		if reply.Type != "playback_state" || reply.Session != request.Session || reply.PlaybackID != request.PlaybackID || reply.TotalPackets > 1500 || reply.SentPackets > reply.TotalPackets {
			return errors.New("invalid media playback reply")
		}
		switch reply.State {
		case "loading", "running", "completed", "stopped", "failed":
		default:
			return errors.New("invalid media playback state")
		}
		if (reply.State == "loading" && (reply.SentPackets != 0 || reply.TotalPackets != 0)) || ((reply.State == "running" || reply.State == "completed") && reply.TotalPackets == 0) {
			return errors.New("invalid media playback loading state")
		}
		if (reply.State == "completed" && reply.SentPackets != reply.TotalPackets) || (reply.State == "failed" && reply.Message == "") {
			return errors.New("inconsistent media playback result")
		}
	default:
		return errors.New("unknown media command")
	}
	return nil
}

// serve 启动并管理一个媒体进程代次，完成版本握手后串行发送命令和采集统计。
func (p *Pool) serve(ctx context.Context, binary string, w *Worker, onStart func(error)) error {
	encoded, _ := json.Marshal(w.Config)
	cmd := exec.CommandContext(ctx, binary, "--worker-config", string(encoded))
	cmd.Stderr = os.Stderr
	pcm, pcmChild, err := newPCMLane()
	if err != nil {
		onStart(err)
		return err
	}
	rx, rxChild, err := newRXLane(w.Config.MaxCalls)
	if err != nil {
		pcmChild.Close()
		pcm.close()
		onStart(err)
		return err
	}
	defer rxChild.Close()
	defer rx.close()
	// FD3下行保持原协议，FD4上行独立。旧worker可忽略新增FD，不经能力握手绝不开放入口。
	cmd.ExtraFiles = []*os.File{pcmChild, rxChild}
	cmd.Env = append(os.Environ(), "RUSTSWITCH_PCM_FD=3", "RUSTSWITCH_RX_FD=4")
	defer pcmChild.Close()
	defer pcm.close()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		onStart(err)
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		onStart(err)
		return err
	}
	if err = cmd.Start(); err != nil {
		onStart(err)
		return err
	}
	_ = rxChild.Close()
	_ = pcmChild.Close() // 父进程不能保留对端，否则子进程退出后的通道所有权会含混。
	done := make(chan struct{})
	incoming := make(chan result, 1)
	// 每行只解码一条有限长度 JSON；解析错误或 EOF 将终止当前进程代次。
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 16384)
		for scanner.Scan() {
			var reply Reply
			err := json.Unmarshal(scanner.Bytes(), &reply)
			if err == nil && reply.Type == "rx_state" {
				err = validateRXRaw(scanner.Bytes())
			}
			select {
			case incoming <- result{reply, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case incoming <- result{err: err}:
		case <-done:
		}
	}()
	defer func() {
		w.submitMu.Lock()
		w.Healthy.Store(false)
		w.capabilities = nil
		w.pcm = nil
		w.rx = nil
		w.submitMu.Unlock()
		rx.close()  // 撤销旧代次全部上行句柄，唤醒Read；不把旧FD数据交给新进程。
		pcm.close() // 先停止数据及拒绝排队任务，再等待子进程退出。
		close(done)
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	var ready result
	select {
	case ready = <-incoming:
	case <-time.After(5 * time.Second):
		err = errors.New("media startup timeout")
		onStart(err)
		return err
	case <-ctx.Done():
		onStart(ctx.Err())
		return ctx.Err()
	}
	if ready.err != nil || !ready.reply.OK || ready.reply.ID != 0 || ready.reply.Type != "ready" || ready.reply.ProtocolVersion != 1 || ready.reply.WorkerID != w.Config.WorkerID || ready.reply.PID != cmd.Process.Pid {
		err = fmt.Errorf("invalid media handshake: %v", ready.err)
		onStart(err)
		return err
	}
	if w.Config.Processing == "g711" && !slices.Contains(ready.reply.Capabilities, "processed_g711_v1") {
		err = errors.New("media worker lacks processed_g711_v1 capability")
		onStart(err)
		return err
	}
	w.submitMu.Lock()
	rx.mu.Lock()
	rx.generation = w.Generation.Add(1)
	rx.mu.Unlock()
	w.capabilities = append([]string(nil), ready.reply.Capabilities...)
	w.pcm = pcm
	w.rx = rx
	w.PID.Store(int64(ready.reply.PID))
	w.snapshot.Store(map[string]any{})
	w.admission.Store(&Admission{SampledAt: time.Now()})
	pressure := pressureTracker{}
	w.Healthy.Store(true)
	w.submitMu.Unlock()
	onStart(nil)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	encoder := json.NewEncoder(stdin)
	// 每个分片只复用一个 RPC 计时器；正常高频命令不创建定时器回调 goroutine。
	rpcTimer := time.NewTimer(time.Hour)
	rpcTimer.Stop()
	defer rpcTimer.Stop()
	var sequence uint64
	// rpc 串行分配请求序号，只接受对应回复；当前协议不支持多个同时在途的管道命令。
	rpc := func(request Request) (Reply, error) {
		sequence++
		request.ID = sequence
		// 管道写和回复等待共用一秒期限；未知执行结果会结束当前代次，不能只丢弃回复后继续复用资源。
		deadline := time.Now().Add(time.Second)
		if e := stdin.(*os.File).SetWriteDeadline(deadline); e != nil {
			return Reply{}, e
		}
		rpcTimer.Reset(time.Until(deadline))
		defer func() {
			if !rpcTimer.Stop() {
				select {
				case <-rpcTimer.C:
				default:
				}
			}
		}()
		if e := encoder.Encode(request); e != nil {
			return Reply{}, e
		}
		select {
		case r := <-incoming:
			if r.err != nil {
				return Reply{}, r.err
			}
			if r.reply.ID != sequence {
				return Reply{}, errors.New("media reply sequence mismatch")
			}
			if e := w.validateReply(request, r.reply); e != nil {
				return Reply{}, e
			}
			if request.Op == "release" && r.reply.OK {
				rx.release(request.Session)
			}
			return r.reply, nil
		case <-rpcTimer.C:
			return Reply{}, errors.New("media command timeout")
		case <-ctx.Done():
			return Reply{}, ctx.Err()
		}
	}
	// handleJob 执行前再校验取消和进程代次；命令业务拒绝不等同于管道失效。
	handleJob := func(j job) error {
		if j.callState != nil && !j.callState.CompareAndSwap(0, 1) {
			j.finish(Reply{}, j.ctx.Err())
			return nil
		}
		if j.ctx.Err() != nil {
			j.finish(Reply{}, j.ctx.Err())
			return nil
		}
		if j.request.Generation != w.Generation.Load() {
			j.finish(Reply{}, errors.New("stale media generation"))
			return nil
		}
		reply, e := rpc(j.request)
		if e != nil {
			j.finish(Reply{}, e)
			return e
		}
		if !reply.OK {
			err := errors.New(reply.Message)
			j.finish(reply, err)
			// release 必须幂等成功；明确拒绝表示资源所有权契约已失效，不能保持健康并永久重试。
			if j.request.Op == "release" {
				return fmt.Errorf("media release contract failed: %w", err)
			}
		} else {
			j.finish(reply, nil)
		}
		return nil
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 每轮优先处理最多八个已有通话任务，为应答和释放保留推进机会。
		for i := 0; i < 8; i++ {
			select {
			case j := <-w.urgent:
				if e := handleJob(j); e != nil {
					return e
				}
			default:
				goto selected
			}
		}
	selected:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-incoming:
			if r.err != nil {
				return r.err
			}
			return errors.New("unsolicited media reply")
		case <-ticker.C:
			reply, e := rpc(Request{Op: "stats"})
			if e != nil {
				return e
			}
			if !reply.OK || reply.Type != "stats" {
				return errors.New("invalid stats reply")
			}
			w.snapshot.Store(reply.Stats)
			sample := pressure.update(time.Now(), reply.Stats, len(w.jobs)+len(w.urgent), w.cpuHigh, w.cpuLow)
			w.admission.Store(&sample)
		case j := <-w.urgent:
			if e := handleJob(j); e != nil {
				return e
			}
		case j := <-w.jobs:
			if e := handleJob(j); e != nil {
				return e
			}
		}
	}
}
