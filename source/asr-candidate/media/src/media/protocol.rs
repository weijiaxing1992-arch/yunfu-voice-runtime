//! Go 控制面与 Rust worker 的逐行 JSON 契约，字段名属于项目内部协议。
//! 请求 ID 只关联响应，不等于会话版本；本协议不兼容 FreeSWITCH ESL 或模块 ABI。
use super::interaction::{DtmfEvent, Leg};
use crate::audio::spec::MediaFormat;
use ipnet::IpNet;
use serde::{Deserialize, Serialize};
use std::net::{Ipv4Addr, SocketAddrV4};

/// 启动握手声明的内部协议版本；字段语义变化时需要协调控制面版本。
pub const PROTOCOL_VERSION: u32 = 1;
/// 单条控制行的读取上限，限制错误输入占用；换行符也必须位于该上限内。
pub const MAX_CONTROL_LINE: usize = 16_384;

/// 本地终结与双腿桥接是不同图，未知值不能自动降级成旧透传。
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ProcessingTopology {
    #[default]
    Bridge,
    Local,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
/// 显式选择实时处理图；版本和延迟上限属于冻结的建呼合同，不能默默退回透传。
pub struct ProcessingPlan {
    /// 省略沿用已发布双腿合同；单腿必须显式声明并检查对应能力握手。
    #[serde(default)]
    pub topology: ProcessingTopology,
    pub version: u32,
    pub mode: String,
    pub jitter_target_ms: u16,
    pub max_delay_ms: u16,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
/// worker 启动配置，未知字段拒绝加载；不提供运行中自动热更新。
pub struct WorkerConfig {
    /// 固定本地文件播放根目录；空值关闭文件加载器，不为普通桥额外创建线程。
    #[serde(default)]
    pub playback_root: String,
    /// 是否固定连接协商后的 UDP 对端；缺省关闭，不实现 NAT 自动学习。
    #[serde(default)]
    pub connect_sockets: bool,
    /// 控制面分配的 worker 标识，随握手返回。
    pub worker_id: usize,
    /// 本 worker 的 IPv4 监听地址。
    pub bind_ip: Ipv4Addr,
    /// 包含两端点的范围；只有完整四端口块可分配。
    pub port_start: u16,
    pub port_end: u16,
    /// 不参与分配的完整四端口块起点；必须相对 port_start 四对齐且不重复。
    #[serde(default)]
    pub excluded_port_blocks: Vec<u16>,
    /// 同时媒体会话上限，不等于经过压力测试的承载能力。
    pub max_calls: usize,
    /// 每个 socket 请求的接收缓冲字节数；实际值可能被内核调整。
    pub receive_buffer_bytes: usize,
    /// 端口释放后的复用隔离毫秒数。
    pub port_reuse_delay_ms: u64,
    /// 每条 RTP 腿的包率和突发容量；RTCP 当前另固定为每秒 50 包。
    pub max_packets_per_second_per_leg: u32,
    /// 协商对端允许使用的网络；地址白名单不能替代媒体身份认证。
    pub allowed_remote_networks: Vec<IpNet>,
    /// 可选 Linux CPU 编号，只绑定事件循环线程；其他平台明确拒绝。
    pub cpu_core: Option<usize>,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
/// 单条呼叫腿的协商对端；当前 RTP/RTCP 必须使用不同的 IPv4 地址端口组合。
pub struct Peer {
    /// RTP 的预期源地址，同时作为向该腿发送报文的目的地。
    pub rtp: SocketAddrV4,
    /// RTCP 的预期源地址，同时作为向该腿发送报文的目的地。
    pub rtcp: SocketAddrV4,
}

#[derive(Debug, Deserialize, Serialize)]
/// 单条控制请求，命令字段直接展开到顶层 JSON。
pub struct Request {
    /// 单次操作的关联编号；本字段不自动实现命令去重和排序。
    pub id: u64,
    #[serde(flatten)]
    pub command: Command,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(tag = "op", rename_all = "snake_case")]
/// 媒体事件循环串行执行的操作，避免会话状态被多个线程并发修改。
pub enum Command {
    /// 分配四个监听端口；会话与参数相同的重复请求返回原分配。
    Allocate {
        /// 空值保留既有透传接口；处理模式必须核对握手能力及分配响应版本。
        #[serde(default)]
        processing: Option<ProcessingPlan>,
        /// 当前 worker 内唯一的控制面会话编号。
        session: u64,
        /// 已知的 A 腿对端。
        a: Peer,
        /// RTP载荷类型；动态编码须附codec元数据，本命令不启动转码。
        payload: u8,
        /// 可选 telephone-event 类型，必须位于动态范围 96–127。
        dtmf_payload: Option<u8>,
        /// 与Go共享的完整协商参数，展开后保持已有IPC字段位置。
        #[serde(flatten)]
        format: MediaFormat,
    },
    /// 设置 B 腿协商结果；当前命令不携带协商版本，不能自行消除过时更新。
    Connect {
        session: u64,
        /// 有codec元数据时核对PT；旧Connect中Go常发送0，缺元数据时保持原会话PT。
        #[serde(default)]
        payload: Option<u8>,
        /// 新 B 腿对端；目的地变化会清理旧的待发送数据。
        b: Peer,
        /// 与分配时的类型一致，或关闭 DTMF。
        dtmf_payload: Option<u8>,
        /// 与Go共享的完整协商参数，展开后保持已有IPC字段位置。
        #[serde(flatten)]
        format: MediaFormat,
    },
    /// 释放会话；不存在时也返回确认，允许控制面重试。
    Release { session: u64 },
    /// 拉取 worker 全局电话事件日志；游标溢出必须由业务收号显式处理。
    DtmfEvents {
        #[serde(default)]
        after_seq: u64,
        limit: usize,
    },
    /// 只向会话已有目标播放 G.711 单音；同任务同参数幂等，不接受任意文件或网络地址。
    PlaybackStart {
        session: u64,
        playback_id: u64,
        leg: Leg,
        frequency_hz: u16,
        duration_ms: u32,
    },
    /// 在配置根目录内有界异步加载相对 WAV，loading 不代表任何音频已经成功发送。
    PlaybackFileStart {
        session: u64,
        playback_id: u64,
        leg: Leg,
        path: String,
    },
    /// 查询最后一项播放；会话释放后不保留历史。
    PlaybackStatus { session: u64, playback_id: u64 },
    /// 幂等停止匹配编号的播放，不能用旧任务编号停止新播放。
    PlaybackStop { session: u64, playback_id: u64 },
    /// 创建或推进回复轮次；同编号同配置不复活已经停止的音频，PCM从独立FD输入。
    PcmTurnBegin {
        session: u64,
        turn_id: u64,
        buffer_ms: u16,
        prebuffer_ms: u16,
    },
    /// 标记该轮全部样本已入队；只有真实排空及最后20ms结束才返回completed。
    PcmTurnEnd {
        session: u64,
        turn_id: u64,
        final_samples: u64,
    },
    /// 立即关闭旧轮次输入；淡出只保留已收到的有限队首音频。
    PcmTurnInterrupt {
        session: u64,
        turn_id: u64,
        #[serde(default)]
        fade_ms: u16,
    },
    /// 当前轮次的真实接纳、提交UDP、取消及队列年龄，不表示终端听到了多少。
    PcmTurnStatus { session: u64, turn_id: u64 },
    /// 已分配本地A腿的原生收音订阅；与下行turn互不抢占。
    RxSubscribe { session: u64, subscription_id: u64 },
    /// 撤销后续观察并清有限队列；确认不等待ASR消费者读取终止通知。
    RxUnsubscribe { session: u64, subscription_id: u64 },
    /// 数据通道失联后仍可经独立JSON读取真实最终状态。
    RxStatus { session: u64, subscription_id: u64 },
    /// 读取统计快照，并回收隔离期已经到期的端口。
    Stats,
    /// 仅本地 A 腿；整批校验后入队，返回受理状态不代表远端收到。
    DtmfSend {
        session: u64,
        leg: Leg,
        digits: String,
        duration_ms: u32,
    },
    /// 最近真实发送状态及累计失败数；会话释放后不可查询旧任务。
    DtmfSendStatus { session: u64 },
    /// 确认后终止事件循环，不会等待活动呼叫自然结束。
    Shutdown,
}

#[derive(Debug, Serialize, Deserialize)]
/// 控制响应；ok 表示 worker 内操作结果，不表示对端接收或端到端质量已达标。
pub struct Reply {
    /// 请求关联编号，启动时主动发送的 Ready 使用 0。
    pub id: u64,
    pub ok: bool,
    #[serde(flatten)]
    pub result: Response,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
/// 用 type 字段区分握手、分配、确认、统计和错误，控制面须核对预期变体。
pub enum Response {
    /// 启动校验已完成；会话端口仍在每次 Allocate 时按需绑定。
    Ready {
        /// 新能力独立握手，不改变旧版透传请求的版本号。
        #[serde(default)]
        capabilities: Vec<String>,
        protocol_version: u32,
        worker_id: usize,
        /// 供控制面监管和故障定位的系统进程编号。
        pid: u32,
    },
    /// 四个端口严格对应两条呼叫腿；它们属于 worker 的监听地址。
    Allocated {
        /// 本地图单独确认拓扑，避免旧worker只返回版本号便被误认为已接入。
        #[serde(default, skip_serializing_if = "Option::is_none")]
        processing_topology: Option<ProcessingTopology>,
        /// 只有实际创建处理状态才返回1；省略表示原始透传模式。
        #[serde(default, skip_serializing_if = "Option::is_none")]
        processing_version: Option<u32>,
        session: u64,
        a_rtp: u16,
        a_rtcp: u16,
        b_rtp: u16,
        b_rtcp: u16,
    },
    /// 没有额外结果数据的操作确认。
    Ack,
    /// 扩展查询字段与原版 API 正文分离，不把异步入队当成实际发包完成。
    DtmfSendState {
        session: u64,
        #[serde(flatten)]
        status: super::dtmf_sender::SendState,
    },
    /// 当前进程累计计数与实时容量的快照；worker 重启后累计值从零开始。
    Stats { stats: Box<Stats> },
    /// 序号只覆盖本批真实事件，不能跳过未拉取的下一批。
    DtmfEvents {
        events: Vec<DtmfEvent>,
        next_seq: u64,
        oldest_seq: u64,
        overflow: bool,
    },
    /// 发送成功仅表示本机 UDP 接受，不代表终端声音质量已验证。
    PlaybackState {
        session: u64,
        playback_id: u64,
        state: String,
        sent_packets: u64,
        total_packets: u64,
        message: String,
    },
    PcmTurnState {
        session: u64,
        #[serde(flatten)]
        status: super::pcm_turn::TurnStatus,
    },
    RxState {
        session: u64,
        #[serde(flatten)]
        status: super::rx_export::Status,
    },
    /// 操作未完成的文本原因；控制面不可把错误消息当作成功结果解析。
    Error { message: String },
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
/// 观测字段同时包含进程累计量与即时量；收发计数不代表接收端一定收到数据。
pub struct Stats {
    /// 实际仍接受A腿观察的订阅数；和下行PCM turn独立。
    pub rx_active_subscriptions: usize,
    /// 当前各订阅待发观察总数，不含预留的缺口/终态摘要。
    pub rx_queued_events: usize,
    /// 订阅后实际分配的Graph观察队列字节数，未订阅图不计。
    pub rx_observation_storage_bytes: usize,
    /// Graph观察队列仍在终态的数量，不与网络丢包混淆。
    pub rx_observation_failed: usize,
    /// 实际拥有单腿终结图的呼叫，属于processed_active_calls的子集。
    pub processed_local_active_calls: usize,
    /// 本地消费节点接收的原生PCM帧，含有来源标记的有限PLC，不含CN或无PCM的missing。
    pub processed_local_consumed_frames: u64,
    pub processed_local_consumed_samples: u64,
    /// PCM含任意非零样本的帧数，不代表VAD或语音识别结论。
    pub processed_local_nonzero_frames: u64,
    /// 单个160样本帧平方和的历史最大，最大160*32768²，避免累计能量超出JS整数精度。
    pub processed_local_energy_max: u64,
    /// 已分配并实际拥有实时处理状态的呼叫；不等于SIP接通数。
    pub processed_active_calls: usize,
    /// 真实执行解码/编码的帧与样本；辅助包与缺失不计入解码音频。
    pub processed_decoded_frames: u64,
    pub processed_decoded_samples: u64,
    pub processed_encoded_frames: u64,
    pub processed_encoded_samples: u64,
    /// 有历史的有限PLC、无历史/耗尽的缺失以及真实CN分别记录。
    pub processed_plc_frames: u64,
    pub processed_missing_frames: u64,
    pub processed_cn_packets: u64,
    /// 实时生成的报文未被UDP接受；不会排无界重试队列。
    pub processed_send_errors: u64,
    /// 缺帧、迟到、重排、重复、溢出及调度过期分别计量。
    pub processed_jitter_lost: u64,
    pub processed_jitter_late: u64,
    pub processed_jitter_reordered: u64,
    pub processed_jitter_duplicates: u64,
    pub processed_jitter_overflow: u64,
    pub processed_playout_expired: u64,
    /// 构包后到发送前再次发现错过一帧期限，主动丢弃，区别于UDP错误或网络丢包。
    pub processed_send_deadline_misses: u64,
    /// 实际准备发送的RTP（不含RTCP）相对计划期限的最大迟到纳秒数。
    pub processed_output_lateness_ns_max: u64,
    /// 非累计区间桶：≤250us/500us/1ms/2ms/5ms/10ms/20ms/>20ms，包含期限拒发尝试。
    pub processed_output_lateness_buckets: [u64; 8],
    /// 辅助报文过期、未结束事件超时、重新协商/源重启丢弃队列、确认源重启均累计可查。
    pub processed_aux_expired: u64,
    pub processed_aux_timeouts: u64,
    pub processed_queue_reset_drops: u64,
    pub processed_source_resets: u64,
    /// 电话事件完整交给本机 UDP 的报文数，包括三次结束包。
    pub dtmf_send_packets: u64,
    /// 发送或调度失败次数；每次失败会撤销该发送器剩余队列。
    pub dtmf_send_errors: u64,
    /// 释放会话时尚未完整发送的数字数；不伪称它们已经发送。
    pub dtmf_send_cancelled_digits: u64,
    /// 仍有待发数字的本地发送器数量，上限64。
    pub dtmf_send_active: usize,
    /// 进程所有线程累计用户态 CPU 秒数。
    pub user_cpu_seconds: f64,
    /// 进程所有线程累计内核态 CPU 秒数。
    pub system_cpu_seconds: f64,
    /// 进程启动以来的常驻内存峰值，统一换算为字节。
    pub peak_resident_bytes: u64,
    /// 统计时已经连接对端的 socket 数量。
    pub connected_sockets: usize,
    /// 对端变更时清理发送队列而丢弃的包数。
    pub peer_changed_drops: u64,
    /// 当前已分配的媒体会话数，不限定 SIP 是否已经接通。
    pub active_calls: usize,
    /// 当前可尝试绑定的四端口块数，不含仍在隔离期的块。
    pub available_blocks: usize,
    /// 包括 WouldBlock 在内的接收调用总数。
    pub receive_syscalls: u64,
    /// 成功返回的接收批次数，可结合 rx_packets 估算实际批次大小。
    pub receive_batches: u64,
    /// 启动以来单次接收到的最大包数，不能替代平均批次指标。
    pub max_receive_batch: usize,
    /// 从 socket 读取的报文总数，包含之后被校验拒绝的报文。
    pub rx_packets: u64,
    /// 内核报告的数据报字节总数，Linux 上可包括超出缓冲的原始长度。
    pub rx_bytes: u64,
    /// 被发送调用完整接受的包数，不能证明已越过网卡或到达对端。
    pub tx_packets: u64,
    /// 与 tx_packets 对应的累计发送字节。
    pub tx_bytes: u64,
    /// 长度、RTP/RTCP 结构或协商负载类型校验失败的包数。
    pub invalid_packets: u64,
    /// 源地址不匹配的拒绝数；connected UDP 被内核先过滤的包不会计入。
    pub source_rejected: u64,
    /// B 腿尚未设置时到达、无法转发的包数。
    pub peer_not_ready: u64,
    /// 超出对应 RTP/RTCP 令牌桶额度的包数。
    pub rate_limited: u64,
    /// 非 WouldBlock 发送失败或异常发送长度次数。
    pub send_errors: u64,
    /// 排除 WouldBlock/Interrupted 的接收错误次数。
    pub receive_errors: u64,
    /// 发送队列达到固定长度后拒绝的新包数。
    pub send_queue_drops: u64,
    /// 重试前已经超过短发送期限的包数。
    pub send_expired: u64,
    /// 从 Linux SO_RXQ_OVFL 辅助字段累计得到的 socket 接收溢出增量。
    pub socket_rx_drops: u64,
    /// 平台是否支持上述计数；不支持时零值不能解释为不存在内核丢包。
    pub socket_drop_counter_supported: bool,
    /// 目的腿被提示音替代时，有意不转发的桥主音频及 CN 包；不与拥塞丢弃混淆。
    pub playback_replaced_packets: u64,
}
