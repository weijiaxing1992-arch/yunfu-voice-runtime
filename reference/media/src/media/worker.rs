//! 独立进程中的 RTP/RTCP 转发引擎：控制读写线程与单线程媒体状态机分工。
//! 会话各占四个 UDP socket；正常路径复用接收缓冲，发送阻塞时才建立有界短队列。
//! 当前控制通道失联会终止 worker，不具备控制面重启后继续承载已有呼叫的接管能力。
use super::{
    control::ControlOutput,
    dtmf_sender::{LocalRtpClock, Sender as DtmfSender, MAX_SENDERS},
    file_loader::{FileLoader, LoadJob, LoadResult, LOAD_TIMEOUT},
    interaction::{DtmfEvent, DtmfTracker, EventJournal, Leg, Playback, MAX_PLAYBACKS},
    io::{ReceiveBatch, BATCH_SIZE, MAX_PACKET},
    pcm_transport::{self, Lane as PcmLane},
    pcm_turn::{TurnError, TurnQueue, TurnState},
    ports::PortPool,
    processed::{Graph, MAX_INPUT_PAYLOAD_BYTES},
    protocol::*,
    rtp,
    rx_export::{self, Lane as RxLane, Subscription as RxSubscription},
    scheduler::Scheduler,
};
use crate::{
    admission::TokenBucket,
    audio::spec::{event_mask, CodecSpec, MediaFormat},
};
use anyhow::{ensure, Context, Result};
use crossbeam_channel::{bounded, Receiver, Sender};
use mio::{net::UdpSocket, Events, Interest, Poll, Token, Waker};
use socket2::{Domain, Protocol, Socket, Type};
use std::{
    collections::{BTreeSet, HashMap, VecDeque},
    io::{self, BufRead, Read},
    net::{SocketAddr, SocketAddrV4},
    os::fd::AsFd,
    sync::{Arc, OnceLock},
    time::{Duration, Instant},
};

/// Token 0 只用于控制线程唤醒，不映射到媒体 socket。
const CONTROL: Token = Token(0);
/// 每个就绪 socket 一轮最多接收的包数，限制单个流长期占用事件循环。
const PACKETS_PER_TURN: usize = 32;
/// 读取、发送重试和冷却恢复共用 socket 轮次上限，批量到期也不能长期阻塞控制命令。
const SOCKETS_PER_TURN: usize = 128;
/// 每个发送方向的积压上限，防止拥塞时形成无界内存队列。
const MAX_SEND_QUEUE: usize = 8;
/// 暂时不可写时允许的短重试窗口；过期主动丢弃，避免无限积累语音延迟。
const SEND_DEADLINE: Duration = Duration::from_millis(5);
/// 一帧控制回复的整体写入上限；超时沿用控制失联终止 worker 的现有策略。
const CONTROL_WRITE_DEADLINE: Duration = Duration::from_secs(1);
/// 接收工作量至少允许每 socket 每秒千包，正常 G.711 与 RTCP 不受此额外保护影响。
const MIN_RECEIVE_WORK_RATE: u32 = 1000;

#[path = "worker_pcm.rs"]
mod pcm_runtime;
#[path = "worker_rx.rs"]
mod rx_runtime;

/// 区分真正读空、仍有数据和需要等待额度；暂停不能冒充 WouldBlock 丢掉边缘触发事件。
enum ReceiveState {
    Drained,
    Ready,
    Deferred(Instant),
}

/// 已复制的待发送报文；拥有自己的字节存储，不借用下一批会覆盖的接收缓冲。
struct PendingPacket {
    bytes: Vec<u8>,
    destination: SocketAddrV4,
    deadline: Instant,
}
/// 单个媒体会话的全部可变状态，生命周期由 Allocate 到 Release/worker 退出覆盖。
struct Session {
    /// 可选实时图由本worker独占；普通透传不创建codec/jitter存储。
    processed: Option<Box<Graph>>,
    /// 事件回传使用原控制会话编号，不依赖可复用端口推断通话身份。
    id: u64,
    /// 四个 socket 是否已按协商对端调用 connect；影响发送 API 的选择。
    connected: bool,
    /// 四端口块起点，释放时原样交回端口池。
    base: u16,
    /// 固定顺序：A-RTP、A-RTCP、B-RTP、B-RTCP。
    sockets: [UdpSocket; 4],
    /// 含会话代际的就绪标识，用于拒绝端口复用前遗留的事件。
    tokens: [usize; 4],
    /// A 腿在分配时确定；B 腿在 Connect 前可能尚未可用。
    a: Peer,
    b: Option<Peer>,
    payload: u8,
    /// 初始提议与最终采用的 DTMF 类型分开保存，便于重复分配与协商校验。
    offered_dtmf: Option<u8>,
    dtmf: Option<u8>,
    /// 已校验的主编码及最初报价；只在控制命令更新，不在收包热路径解析字符串。
    codec: CodecSpec,
    offered_format: MediaFormat,
    /// 已接纳的CN类型和DTMF事件掩码，辅助音频不会混入主编码解码。
    cn: Option<u8>,
    dtmf_mask: u32,
    /// telephone-event 的协商时钟独立于主音频；状态只在首个合法事件到达时创建。
    dtmf_clock: u32,
    dtmf_trackers: [Option<Box<DtmfTracker>>; 2],
    dtmf_due: [Option<Instant>; 2],
    /// 每会话只保留最后一项提示音；普通透传不创建音频缓冲或播放定时器。
    playback: Option<Box<Playback>>,
    /// 仅启用流式播放后分配固定队列；归属本worker，录音/ASR不能持有此缓冲。
    pcm_turn: Option<Box<TurnQueue>>,
    /// 独立A腿收音状态仅订阅后分配；下行turn和提示音不拥有这条队列。
    rx_subscription: Option<Box<RxSubscription>>,
    /// 仅主动本地发送时分配；释放会话即同时清除队列和输出时间线。
    dtmf_sender: Option<Box<DtmfSender>>,
    local_rtp: Option<Box<LocalRtpClock>>,
    /// 每个接收 socket 独立计量，RTP 与 RTCP 使用不同额度。
    limits: [TokenBucket; 4],
    /// 所有来源和未协商报文都支付读取额度；比媒体转发额度宽松，保护实际 CPU 工作量。
    receive_work: [TokenBucket; 4],
    /// 按输出 socket 排队；非空时后续包不能绕过旧包直接发送。
    pending: [VecDeque<PendingPacket>; 4],
    /// 内核累计丢包值的上次观测，用环绕差值累加，释放后随会话重置。
    last_kernel_drops: [u32; 4],
}

/// 事件循环独占的全局状态；控制线程只交换请求/响应，不直接读写会话。
struct Engine {
    /// 每个会话两条音频期限和一条RTCP期限；预留索引堆不逐帧创建树节点。
    media_timers: Scheduler,
    /// 每会话最多一项PCM期限，启动预分配索引堆，避免每音频帧创建树节点。
    pcm_timers: Scheduler,
    /// 数据报背压只暂停PCM输入，不占用电话控制和RTP接收的预算。
    pcm_lane: Option<PcmLane>,
    /// 上行发送与下行FD3使用不同socket，订阅调度不扫描未订阅会话。
    rx_lane: Option<RxLane>,
    rx_timers: Scheduler,
    /// 启动时从系统熵播种，建呼时生成互不相关的SSRC/初始包序。
    rtp_seed: u64,
    config: WorkerConfig,
    poll: Poll,
    ports: PortPool,
    /// 由端口块确定槽位，收包热路径不再查询会话 HashMap。
    sessions: Vec<Option<Session>>,
    /// 仅控制操作使用的会话编号到固定槽位映射。
    session_slots: HashMap<u64, usize>,
    /// 单调递增代际，溢出时明确报错而不是复用旧 Token。
    next_token: usize,
    /// 待处理可读事件；queued 按槽位记录已入队的代际，避免重复调度。
    ready: VecDeque<usize>,
    queued: Vec<usize>,
    /// 冷却到期时间有序集合；每个 socket 最多一项，释放时主动移除，避免代际更替积压。
    delayed: BTreeSet<(Instant, usize)>,
    delayed_by_slot: Vec<Option<(Instant, usize)>>,
    /// 有待发送数据的 socket 队列，与可读队列分开公平轮询。
    pending_tokens: VecDeque<usize>,
    pending_queued: Vec<usize>,
    /// 按键待结束与主动提示音分别有界调度，不逐轮扫描全部媒体会话。
    dtmf_timers: BTreeSet<(Instant, usize, usize)>,
    playback_timers: BTreeSet<(Instant, usize)>,
    /// 发送按键的绝对期限，每个活动发送器最多一个定时项。
    dtmf_send_timers: BTreeSet<(Instant, usize)>,
    dtmf_journal: EventJournal,
    /// 空根时为 None；开启后只有一个固定加载线程和两个有界队列。
    file_loader: Option<FileLoader>,
    stats: Stats,
}

/// telephone-event 每个事件四字节；只转发协商集合且保留结束位/时长原值。
fn valid_dtmf(bytes: &[u8], mask: u32) -> bool {
    !bytes.is_empty()
        && bytes.len() % 4 == 0
        && bytes
            .chunks_exact(4)
            .all(|event| event[0] <= 16 && (mask & (1 << event[0])) != 0 && event[1] & 0x40 == 0)
}

/// 校验配置和 fd 限额，启动控制 I/O 线程后进入媒体循环。
/// Linux CPU 绑定在创建控制线程之后执行，只约束当前媒体线程。
pub fn run(config: WorkerConfig) -> Result<()> {
    ensure!(
        config.port_start >= 1024
            && config.port_start % 2 == 0
            && config.port_end > config.port_start,
        "invalid port range"
    );
    let blocks = (usize::from(config.port_end) - usize::from(config.port_start) + 1) / 4;
    let ports = PortPool::excluding(
        config.port_start,
        config.port_end,
        Duration::from_millis(config.port_reuse_delay_ms),
        &config.excluded_port_blocks,
    )?;
    ensure!(
        config.max_calls > 0 && config.max_calls <= ports.free_count(),
        "invalid call capacity"
    );
    ensure!(
        (16_384..=1_048_576).contains(&config.receive_buffer_bytes),
        "invalid receive buffer"
    );
    ensure!(
        (100..=10_000).contains(&config.max_packets_per_second_per_leg),
        "invalid packet rate"
    );
    ensure!(
        !config.allowed_remote_networks.is_empty(),
        "media allowlist cannot be empty"
    );
    ensure!(config.port_reuse_delay_ms <= 60_000, "invalid quarantine");
    check_fd_limit(config.max_calls)?;

    let poll = Poll::new()?;
    let pcm_lane = PcmLane::from_environment(poll.registry())?;
    let rx_lane = RxLane::from_environment()?;
    let wake = Arc::new(Waker::new(poll.registry(), CONTROL)?);
    let file_loader = if config.playback_root.is_empty() {
        None
    } else {
        Some(FileLoader::start(&config.playback_root, Arc::clone(&wake))?)
    };
    let (commands_tx, commands_rx) = bounded::<Request>(128);
    let (replies_tx, replies_rx) = bounded::<Reply>(128);
    let reader_wake = Arc::clone(&wake);
    // 逐行读取有长度上限的 JSON；格式错误、缺少换行或 EOF 会关闭命令通道。
    // 有界通道满时只阻塞读取线程，不在这里直接修改媒体会话。
    std::thread::Builder::new()
        .name("control-reader".into())
        .spawn(move || {
            let stdin = io::stdin();
            let mut reader = stdin.lock();
            loop {
                let mut line = Vec::new();
                match (&mut reader)
                    .take(MAX_CONTROL_LINE as u64)
                    .read_until(b'\n', &mut line)
                {
                    Ok(0) | Err(_) => break,
                    Ok(_) if line.last() != Some(&b'\n') => break,
                    Ok(_) => match serde_json::from_slice(&line) {
                        Ok(request) => {
                            if commands_tx.send(request).is_err() {
                                break;
                            }
                            if reader_wake.wake().is_err() {
                                break;
                            }
                        }
                        Err(_) => break,
                    },
                }
            }
            drop(commands_tx);
            let _ = reader_wake.wake();
        })?;
    // 结束媒体循环后为剩余控制输出设置一次共享截止时间，不随回复队列长度累加。
    let shutdown_deadline = Arc::new(OnceLock::new());
    let writer_shutdown = Arc::clone(&shutdown_deadline);
    let writer_thread = std::thread::Builder::new()
        .name("control-writer".into())
        .spawn(move || {
            let stdout = io::stdout();
            let mut writer = match ControlOutput::new(stdout.as_fd(), CONTROL_WRITE_DEADLINE) {
                Ok(writer) => writer,
                Err(_) => std::process::exit(2),
            };
            for reply in replies_rx {
                let encoded = serde_json::to_vec(&reply).map(|mut bytes| {
                    bytes.push(b'\n');
                    bytes
                });
                if encoded.map_or(true, |bytes| {
                    writer
                        .write_frame(&bytes, writer_shutdown.get().copied())
                        .is_err()
                }) {
                    // 控制面已经不可达时终止进程，避免遗留失去管理的 UDP 监听。
                    // 这是当前生命周期策略，会影响此 worker 中的已有呼叫。
                    std::process::exit(2);
                }
            }
        })?;
    if let Some(core) = config.cpu_core {
        set_affinity(core)?;
    }
    let rtp_seed = media_seed()?;
    let mut capabilities = vec!["processed_g711_v1".into(), "processed_g711_local_v1".into()];
    if pcm_lane.is_some() {
        capabilities.push("pcm_turn_v1".into());
    }
    if rx_lane.as_ref().is_some_and(RxLane::available) {
        capabilities.push(rx_export::CAPABILITY.into());
    }
    replies_tx.send(Reply {
        id: 0,
        ok: true,
        result: Response::Ready {
            capabilities,
            protocol_version: PROTOCOL_VERSION,
            worker_id: config.worker_id,
            pid: std::process::id(),
        },
    })?;

    let max_calls = config.max_calls;
    let mut engine = Engine {
        media_timers: Scheduler::new(blocks * 3),
        pcm_timers: Scheduler::new(blocks),
        pcm_lane,
        rx_lane,
        rx_timers: Scheduler::new(blocks),
        rtp_seed,
        ports,
        config,
        poll,
        sessions: (0..blocks).map(|_| None).collect(),
        session_slots: HashMap::with_capacity(max_calls),
        next_token: 1,
        ready: VecDeque::new(),
        queued: vec![0; blocks * 4],
        delayed: BTreeSet::new(),
        delayed_by_slot: vec![None; blocks * 4],
        pending_tokens: VecDeque::new(),
        pending_queued: vec![0; blocks * 4],
        dtmf_timers: BTreeSet::new(),
        playback_timers: BTreeSet::new(),
        dtmf_send_timers: BTreeSet::new(),
        dtmf_journal: EventJournal::default(),
        file_loader,
        stats: Stats {
            socket_drop_counter_supported: cfg!(target_os = "linux"),
            ..Stats::default()
        },
    };
    // 循环结束先释放所有 UDP socket，再等待写线程的有限输出窗口；stdout 堵塞不能扣留端口。
    // 本版仍不提供可重连控制通道，超时属于明确的 worker 失联错误。
    let result = engine.event_loop(commands_rx, replies_tx);
    let _ = shutdown_deadline.set(Instant::now() + CONTROL_WRITE_DEADLINE);
    drop(engine);
    writer_thread
        .join()
        .map_err(|_| anyhow::anyhow!("control writer panicked"))?;
    result
}

/// 从 Token 提取固定 socket 偏移；0 是控制唤醒，不能参与媒体寻址。
/// 调用方保证 sockets 大于零；代际是否仍有效由 resolve 再检查。
fn token_offset(token: usize, sockets: usize) -> Option<usize> {
    token.checked_sub(1).map(|value| value % sockets)
}

impl Engine {
    /// 同时校验槽位仍有会话及代际相符，阻止旧就绪事件作用于复用端口的新会话。
    fn resolve(&self, token: usize) -> Option<(usize, usize)> {
        let offset = token_offset(token, self.sessions.len() * 4)?;
        let (slot, index) = (offset / 4, offset % 4);
        let call = self.sessions[slot].as_ref()?;
        (call.tokens[index] == token).then_some((slot, index))
    }

    /// 交错执行控制命令、短发送重试与可读 socket，所有循环均有工作量上限。
    /// 边缘触发就绪必须读取到 WouldBlock；一次未读完的 socket 会继续留在队列中。
    fn event_loop(&mut self, commands: Receiver<Request>, replies: Sender<Reply>) -> Result<()> {
        let mut events = Events::with_capacity(2048);
        let mut batch = ReceiveBatch::default();
        loop {
            for _ in 0..8 {
                match commands.try_recv() {
                    Ok(request) => {
                        let stop = matches!(request.command, Command::Shutdown);
                        let result = self.command(request.command);
                        let reply = match result {
                            Ok(result) => Reply {
                                id: request.id,
                                ok: true,
                                result,
                            },
                            Err(error) => Reply {
                                id: request.id,
                                ok: false,
                                result: Response::Error {
                                    message: error.to_string(),
                                },
                            },
                        };
                        // 回复队列耗尽属于当前协议的致命失联/积压状态，返回错误结束媒体循环。
                        // 这里不能改成阻塞发送，否则会让媒体线程等待控制面读取。
                        replies
                            .try_send(reply)
                            .context("control reply queue exhausted")?;
                        if stop {
                            return Ok(());
                        }
                    }
                    Err(crossbeam_channel::TryRecvError::Empty) => break,
                    Err(crossbeam_channel::TryRecvError::Disconnected) => return Ok(()),
                }
            }
            self.resume_reads(Instant::now())?;
            self.flush_pending();
            self.receive_pcm();
            if self.pcm_timers.first().is_some() {
                self.advance_pcm(Instant::now());
            }
            self.process_loaded_files();
            self.advance_processed();
            if self.rx_timers.first().is_some() {
                self.flush_rx();
            }
            // 正常透明桥没有主动交互时，不增加时钟读取或遍历会话的热路径成本。
            if !self.dtmf_timers.is_empty() {
                self.expire_dtmf(Instant::now());
            }
            if !self.playback_timers.is_empty() {
                self.advance_playbacks(Instant::now());
            }
            if !self.dtmf_send_timers.is_empty() {
                self.advance_dtmf_senders(Instant::now());
            }
            // 已有立即工作时不睡眠；仅有发送积压时用短轮询重试，完全空闲时允许更长等待。
            let mut wait = if !self.ready.is_empty() || !commands.is_empty() {
                Duration::ZERO
            } else if !self.pending_tokens.is_empty() {
                Duration::from_millis(1)
            } else {
                Duration::from_millis(100)
            };
            if let Some(lane) = &self.pcm_lane {
                if lane.has_pending() {
                    wait = wait.min(Duration::from_millis(1));
                } else if lane.readable {
                    wait = Duration::ZERO;
                }
            }
            if let Some(when) = self.pcm_timers.first() {
                wait = wait.min(when.saturating_duration_since(Instant::now()));
            }
            if let Some(when) = self.rx_timers.first() {
                // 全lane背压时只作一次短退避，不能每订阅重复空转同一socket。
                let when = self
                    .rx_lane
                    .as_ref()
                    .and_then(RxLane::retry_at)
                    .map_or(when, |retry| when.max(retry));
                wait = wait.min(when.saturating_duration_since(Instant::now()));
            }
            if let Some(&(when, _)) = self.delayed.first() {
                wait = wait.min(when.saturating_duration_since(Instant::now()));
            }
            if let Some(when) = self.media_timers.first() {
                wait = wait.min(when.saturating_duration_since(Instant::now()));
            }
            if let Some(&(when, _, _)) = self.dtmf_timers.first() {
                wait = wait.min(when.saturating_duration_since(Instant::now()));
            }
            if let Some(&(when, _)) = self.playback_timers.first() {
                wait = wait.min(when.saturating_duration_since(Instant::now()));
            }
            if let Some(&(when, _)) = self.dtmf_send_timers.first() {
                wait = wait.min(when.saturating_duration_since(Instant::now()));
            }
            match self.poll.poll(&mut events, Some(wait)) {
                Ok(()) => (),
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(e) => return Err(e.into()),
            }
            for event in &events {
                if event.token() == pcm_transport::TOKEN {
                    if let Some(lane) = &mut self.pcm_lane {
                        lane.readable = true;
                    }
                    continue;
                }
                let token = event.token().0;
                if let Some((slot, index)) = self.resolve(token) {
                    let offset = slot * 4 + index;
                    if self.queued[offset] != token {
                        self.queued[offset] = token;
                        self.ready.push_back(token);
                    }
                }
            }
            let turns = self.ready.len().min(SOCKETS_PER_TURN);
            // 每轮限制就绪 socket 数量，未处理完的和仍可读的项留给下一轮。
            for _ in 0..turns {
                let token = self.ready.pop_front().unwrap();
                match self.receive(token, &mut batch) {
                    ReceiveState::Ready => self.ready.push_back(token),
                    ReceiveState::Deferred(when) => self.defer_read(token, when)?,
                    ReceiveState::Drained => {
                        if let Some(offset) = token_offset(token, self.queued.len()) {
                            if self.queued[offset] == token {
                                self.queued[offset] = 0;
                            }
                        }
                    }
                }
            }
        }
    }

    /// 执行会话生命周期命令；仅 Allocate/Release 的现有语义支持对应幂等重试。
    fn command(&mut self, command: Command) -> Result<Response> {
        match command {
            Command::Allocate {
                processing,
                session,
                a,
                payload,
                dtmf_payload,
                format,
            } => {
                self.check_peer(&a)?;
                let codec = format.validate(payload, dtmf_payload)?;
                let initial_dtmf_mask = if dtmf_payload.is_some() {
                    event_mask(format.dtmf_events.as_deref().unwrap_or("0-15"))?
                } else {
                    0
                };
                let initial_dtmf_clock = format.dtmf_clock_rate.unwrap_or(8000);
                let initial_cn = format.cn_payload;
                if let Some(&slot) = self.session_slots.get(&session) {
                    // 重复分配必须逐项匹配原参数，不能用相同 ID 悄悄替换活跃会话。
                    let existing = self.sessions[slot].as_ref().unwrap();
                    ensure!(
                        existing.a == a
                            && existing.payload == payload
                            && existing.offered_dtmf == dtmf_payload
                            && existing.processed.as_ref().map(|g| &g.plan) == processing.as_ref()
                            && existing.offered_format == format,
                        "session already exists with different parameters"
                    );
                    return Ok(allocation_response(
                        session,
                        existing.base,
                        processing.is_some(),
                        existing.processed.as_ref().is_some_and(|g| g.is_local()),
                    ));
                }
                ensure!(
                    self.session_slots.len() < self.config.max_calls,
                    "worker call limit reached"
                );
                let now = Instant::now();
                // 资源/格式在端口发布之前校验并准备；创建失败不能返回半初始化会话。
                let processed = match processing {
                    Some(plan) => {
                        self.rtp_seed = self.rtp_seed.wrapping_add(0x9e3779b97f4a7c15);
                        Some(Box::new(Graph::new(
                            plan,
                            payload,
                            &codec,
                            dtmf_payload,
                            &format,
                            self.rtp_seed,
                            now,
                        )?))
                    }
                    None => None,
                };
                let is_processed = processed.is_some();
                let is_local = processed.as_ref().is_some_and(|g| g.is_local());
                let mut attempts = 0;
                let (base, mut sockets): (u16, [UdpSocket; 4]) = loop {
                    let base = self
                        .ports
                        .take(now)
                        .context("no free media port blocks (including quarantine)")?;
                    let sockets = (0..4)
                        .map(|offset| bind_socket(&self.config, base + offset))
                        .collect::<io::Result<Vec<_>>>();
                    match sockets {
                        Ok(s) => {
                            break (
                                base,
                                s.try_into()
                                    .map_err(|_| anyhow::anyhow!("invalid socket count"))?,
                            )
                        }
                        Err(error) => {
                            // 已创建的临时 socket 随错误结果析构；归还整个块并有限跳过占用端口。
                            self.ports.release(base, now);
                            attempts += 1;
                            if error.kind() != io::ErrorKind::AddrInUse || attempts >= 16 {
                                return Err(error.into());
                            }
                        }
                    }
                };
                let slot = usize::from(base - self.config.port_start) / 4;
                // 单腿Allocate即固定A对端；B端口仍保留原四端口ABI，但绝不创建B媒体来源。
                if is_local && self.config.connect_sockets {
                    for (socket, address) in sockets[..2].iter().zip([a.rtp, a.rtcp]) {
                        if let Err(error) = socket.connect(SocketAddr::V4(address)) {
                            self.ports.release(base, now);
                            return Err(error.into());
                        }
                    }
                }
                let generation = self.next_token;
                self.next_token = generation
                    .checked_add(1)
                    .context("token generation exhausted")?;
                let token_base = generation
                    .checked_mul(self.sessions.len() * 4)
                    .and_then(|n| n.checked_add(slot * 4 + 1))
                    .context("token space exhausted")?;
                ensure!(token_base < usize::MAX - 3, "token space exhausted");
                let mut tokens = [0; 4];
                for (index, socket) in sockets.iter_mut().enumerate() {
                    let token = token_base + index;
                    if let Err(error) =
                        self.poll
                            .registry()
                            .register(socket, Token(token), Interest::READABLE)
                    {
                        self.ports.release(base, now);
                        return Err(error.into());
                    }
                    tokens[index] = token;
                }
                let limits = std::array::from_fn(|index| {
                    // RTP 使用配置包率；RTCP 单独限制，避免控制报文流耗尽 worker 时间。
                    let rate = if index % 2 == 0 {
                        self.config.max_packets_per_second_per_leg
                    } else {
                        50
                    };
                    TokenBucket::new(rate, rate, now)
                });
                let receive_work = std::array::from_fn(|index| {
                    let media_rate = if index % 2 == 0 {
                        self.config.max_packets_per_second_per_leg
                    } else {
                        50
                    };
                    // 保留至少两倍原有一秒媒体突发额度；只限制远超合法负载的接收工作量。
                    let rate = media_rate.saturating_mul(2).max(MIN_RECEIVE_WORK_RATE);
                    TokenBucket::new(rate, rate, now)
                });
                self.session_slots.insert(session, slot);
                self.sessions[slot] = Some(Session {
                    processed,
                    id: session,
                    connected: is_local && self.config.connect_sockets,
                    base,
                    sockets,
                    tokens,
                    a,
                    b: None,
                    payload,
                    offered_dtmf: dtmf_payload,
                    dtmf: dtmf_payload,
                    codec,
                    offered_format: format,
                    cn: initial_cn,
                    dtmf_mask: initial_dtmf_mask,
                    dtmf_clock: initial_dtmf_clock,
                    dtmf_trackers: std::array::from_fn(|_| None),
                    dtmf_due: [None; 2],
                    playback: None,
                    pcm_turn: None,
                    rx_subscription: None,
                    dtmf_sender: None,
                    local_rtp: None,
                    limits,
                    receive_work,
                    pending: std::array::from_fn(|_| VecDeque::new()),
                    last_kernel_drops: [0; 4],
                });
                if is_processed {
                    self.stats.processed_active_calls += 1;
                }
                if is_local {
                    self.stats.processed_local_active_calls += 1;
                    self.media_timers.set(
                        slot * 3 + 2,
                        Some(now + Duration::from_millis(2500 + session % 5000)),
                    );
                }
                Ok(allocation_response(session, base, is_processed, is_local))
            }
            Command::Connect {
                session,
                b,
                payload,
                dtmf_payload,
                format,
            } => {
                self.check_peer(&b)?;
                let slot = *self
                    .session_slots
                    .get(&session)
                    .context("unknown media session")?;
                let call = self.sessions[slot].as_mut().unwrap();
                ensure!(
                    call.processed.as_ref().is_none_or(|g| !g.is_local()),
                    "local processed topology cannot connect a B leg"
                );
                ensure!(
                    call.local_rtp.is_none(),
                    "local generated RTP cannot become a transparent bridge"
                );
                ensure!(
                    dtmf_payload.is_none() || dtmf_payload == call.offered_dtmf,
                    "DTMF payload differs from offer"
                );
                // 旧版Connect的payload=0并不代表切换PCMA；只有显式codec时核对新PT。
                let negotiated_payload = if format.codec.is_some() {
                    payload.context("codec metadata requires payload")?
                } else {
                    call.payload
                };
                let negotiated_codec = format.validate(negotiated_payload, dtmf_payload)?;
                ensure!(
                    call.processed.is_some()
                        || (negotiated_payload == call.payload
                            && call.codec.same_stream(&negotiated_codec)),
                    "answer codec differs from offer"
                );
                ensure!(
                    format.cn_payload.is_none()
                        || (format.cn_payload == call.offered_format.cn_payload
                            && format.cn_clock_rate == call.offered_format.cn_clock_rate),
                    "CN differs from offer"
                );
                let dtmf_mask = if dtmf_payload.is_some() {
                    ensure!(
                        format.dtmf_clock_rate.unwrap_or(8000)
                            == call.offered_format.dtmf_clock_rate.unwrap_or(8000),
                        "DTMF clock differs from offer"
                    );
                    let accepted = event_mask(format.dtmf_events.as_deref().unwrap_or("0-15"))?;
                    let offered =
                        event_mask(call.offered_format.dtmf_events.as_deref().unwrap_or("0-15"))?;
                    ensure!(accepted & !offered == 0, "DTMF events exceed offer");
                    accepted
                } else {
                    0
                };
                if let Some(graph) = &mut call.processed {
                    ensure!(
                        call.b.as_ref().is_none_or(|previous| previous == &b),
                        "processed peer cannot change after connect"
                    );
                    graph.connect(
                        negotiated_payload,
                        &negotiated_codec,
                        dtmf_payload,
                        &format,
                        &mut self.stats,
                    )?;
                }
                if call.b.as_ref().is_some_and(|previous| previous != &b) {
                    // 协商目标变化后，旧目的地排队的包不能被发送到新目标。
                    for pending in &mut call.pending {
                        self.stats.peer_changed_drops += pending.len() as u64;
                        pending.clear();
                    }
                }
                if self.config.connect_sockets && (!call.connected || call.b.as_ref() != Some(&b)) {
                    // 已连接时 A 腿地址不变，只更新 B 腿；相同协商避免重复 connect。
                    // 本版逐 socket 更新，后续失败不回滚已成功更新的 socket；控制面负责错误清理。
                    for (index, (socket, address)) in call
                        .sockets
                        .iter()
                        .zip([call.a.rtp, call.a.rtcp, b.rtp, b.rtcp])
                        .enumerate()
                    {
                        if call.connected && index < 2 {
                            continue;
                        }
                        socket.connect(SocketAddr::V4(address))?;
                    }
                    call.connected = true;
                }
                let first_processed_connect = call.processed.is_some() && call.b.is_none();
                call.b = Some(b);
                call.dtmf = dtmf_payload;
                call.cn = format.cn_payload;
                call.dtmf_mask = dtmf_mask;
                call.dtmf_clock = format.dtmf_clock_rate.unwrap_or(8000);
                if first_processed_connect {
                    self.media_timers.set(
                        slot * 3 + 2,
                        Some(Instant::now() + Duration::from_millis(2500 + call.id % 5000)),
                    );
                }
                Ok(Response::Ack)
            }
            Command::Release { session } => {
                if let Some(slot) = self.session_slots.remove(&session) {
                    self.pcm_timers.remove(slot);
                    self.rx_timers.remove(slot);
                    let mut call = self.sessions[slot].take().unwrap();
                    self.finish_rx(&mut call);
                    if call.processed.is_some() {
                        self.stats.processed_active_calls -= 1;
                        if call.processed.as_ref().is_some_and(|g| g.is_local()) {
                            self.stats.processed_local_active_calls -= 1;
                        }
                        for key in slot * 3..slot * 3 + 3 {
                            self.media_timers.remove(key);
                        }
                        send_processed_reports(&mut call, Instant::now(), true, &mut self.stats);
                    }
                    for (leg, due) in call.dtmf_due.iter().enumerate() {
                        if let Some(when) = due {
                            self.dtmf_timers.remove(&(*when, slot, leg));
                        }
                    }
                    if let Some(playback) = &call.playback {
                        self.playback_timers.remove(&(playback.next, slot));
                    }
                    if let Some(sender) = &call.dtmf_sender {
                        if let Some(due) = sender.next {
                            self.dtmf_send_timers.remove(&(due, slot));
                        }
                        self.stats.dtmf_send_cancelled_digits += sender.status.queued_digits as u64;
                    }
                    for (index, socket) in call.sockets.iter_mut().enumerate() {
                        let _ = self.poll.registry().deregister(socket);
                        self.queued[slot * 4 + index] = 0;
                        if let Some(entry) = self.delayed_by_slot[slot * 4 + index].take() {
                            self.delayed.remove(&entry);
                        }
                        self.pending_queued[slot * 4 + index] = 0;
                    }
                    // Session 离开作用域时关闭所有 socket，端口块仍需经过隔离期再分配。
                    self.ports.release(call.base, Instant::now());
                }
                Ok(Response::Ack)
            }
            Command::DtmfSend {
                session,
                leg,
                digits,
                duration_ms,
            } => {
                let slot = *self
                    .session_slots
                    .get(&session)
                    .context("unknown media session")?;
                let call = self.sessions[slot].as_mut().unwrap();
                ensure!(
                    leg == Leg::A && call.b.is_none(),
                    "DTMF send supports local A leg only"
                );
                ensure!(
                    call.processed.as_ref().is_none_or(|g| g.is_local()),
                    "processed DTMF injection requires local topology"
                );
                ensure!(call.dtmf.is_some(), "telephone-event was not negotiated");
                ensure!(
                    call.dtmf_clock == call.codec.rtp_clock_rate,
                    "DTMF send requires the audio RTP clock"
                );
                ensure!(
                    call.dtmf_sender.as_ref().is_some_and(|s| s.next.is_some())
                        || self.dtmf_send_timers.len() < MAX_SENDERS,
                    "DTMF sender capacity exhausted"
                );
                let now = Instant::now();
                let sender = call
                    .dtmf_sender
                    .get_or_insert_with(|| Box::new(DtmfSender::new()));
                let previous = sender.next;
                sender.enqueue(&digits, duration_ms, call.dtmf_mask, now)?;
                if call.processed.is_none() && call.local_rtp.is_none() {
                    let (epoch, sequence, ssrc) = call
                        .playback
                        .as_ref()
                        .filter(|p| p.sent > 0)
                        .map(|p| p.rtp_origin())
                        .unwrap_or((
                            now,
                            0,
                            (call.id as u32).wrapping_mul(2654435761) ^ 0xd74f_193b,
                        ));
                    call.local_rtp = Some(Box::new(LocalRtpClock::new(
                        epoch,
                        sequence,
                        ssrc,
                        call.dtmf_clock,
                    )));
                }
                if let Some(due) = previous {
                    self.dtmf_send_timers.remove(&(due, slot));
                }
                if let Some(due) = sender.next {
                    self.dtmf_send_timers.insert((due, slot));
                }
                Ok(Response::DtmfSendState {
                    session,
                    status: sender.status.clone(),
                })
            }
            Command::DtmfSendStatus { session } => {
                let slot = *self
                    .session_slots
                    .get(&session)
                    .context("unknown media session")?;
                let call = self.sessions[slot].as_ref().unwrap();
                ensure!(call.b.is_none(), "DTMF send supports local A leg only");
                Ok(Response::DtmfSendState {
                    session,
                    status: call
                        .dtmf_sender
                        .as_ref()
                        .map(|s| s.status.clone())
                        .unwrap_or_else(|| DtmfSender::new().status),
                })
            }
            Command::Stats => {
                self.stats.rx_active_subscriptions = 0;
                self.stats.rx_queued_events = 0;
                self.stats.rx_observation_storage_bytes = 0;
                self.stats.rx_observation_failed = 0;
                for call in self.sessions.iter().flatten() {
                    if let Some(rx) = &call.rx_subscription {
                        self.stats.rx_active_subscriptions += usize::from(rx.active());
                        self.stats.rx_queued_events += rx.queued_events();
                    }
                    if let Some(graph) = &call.processed {
                        self.stats.rx_observation_storage_bytes +=
                            graph.local_observation_storage_bytes();
                        self.stats.rx_observation_failed +=
                            usize::from(graph.local_observation_status().is_some_and(|s| s.failed));
                    }
                }
                self.stats.dtmf_send_active = self.dtmf_send_timers.len();
                // 统计采样发生在同一媒体线程，避免读取到部分更新的会话状态。
                update_process_usage(&mut self.stats);
                self.ports.reclaim(Instant::now());
                self.stats.active_calls = self.session_slots.len();
                self.stats.connected_sockets = if self.config.connect_sockets {
                    self.sessions
                        .iter()
                        .flatten()
                        .filter(|call| call.connected)
                        .map(|call| {
                            if call.processed.as_ref().is_some_and(|g| g.is_local()) {
                                2
                            } else {
                                4
                            }
                        })
                        .sum()
                } else {
                    0
                };
                self.stats.available_blocks = self.ports.free_count();
                Ok(Response::Stats {
                    stats: Box::new(self.stats.clone()),
                })
            }
            Command::DtmfEvents { after_seq, limit } => {
                self.expire_dtmf(Instant::now());
                let (events, next_seq, oldest_seq, overflow) =
                    self.dtmf_journal.read(after_seq, limit)?;
                Ok(Response::DtmfEvents {
                    events,
                    next_seq,
                    oldest_seq,
                    overflow,
                })
            }
            Command::PlaybackStart {
                session,
                playback_id,
                leg,
                frequency_hz,
                duration_ms,
            } => {
                let slot = *self
                    .session_slots
                    .get(&session)
                    .context("unknown media session")?;
                let call = self.sessions[slot].as_mut().unwrap();
                ensure!(
                    call.pcm_turn
                        .as_ref()
                        .is_none_or(|turn| !pcm_runtime::active(turn)),
                    "PCM turn must be stopped before legacy playback"
                );
                ensure!(
                    call.processed.as_ref().is_none_or(|g| g.is_local())
                        && (call.processed.is_none() || leg == Leg::A),
                    "processed playback requires local A topology"
                );
                ensure!(
                    leg == Leg::A || call.b.is_some(),
                    "B-leg playback requires connected peer"
                );
                ensure!(
                    (call.codec.name == "PCMU" || call.codec.name == "PCMA")
                        && call.codec.sample_rate == 8000
                        && call.codec.rtp_clock_rate == 8000
                        && call.codec.channels == 1,
                    "playback supports G711 8k mono only"
                );
                if let Some(previous) = &call.playback {
                    if previous.id == playback_id {
                        ensure!(
                            previous.file.is_none()
                                && previous.leg == leg
                                && previous.frequency == frequency_hz
                                && previous.duration == duration_ms,
                            "playback id has different parameters"
                        );
                        return Ok(playback_response(session, previous));
                    }
                    ensure!(
                        previous.state != "running" && previous.state != "loading",
                        "playback already running or loading"
                    );
                    ensure!(playback_id > previous.id, "stale playback id");
                }
                ensure!(
                    self.playback_timers.len() < MAX_PLAYBACKS,
                    "worker playback limit reached"
                );
                let now = Instant::now();
                // 播放源独立于桥 SSRC；会话/任务混合生成实例内稳定标识，不作为身份认证凭据。
                let ssrc = (session as u32).wrapping_mul(0x9e3779b9)
                    ^ (playback_id as u32).rotate_left(13)
                    ^ 0x72736976;
                let start = call
                    .processed
                    .as_ref()
                    .map_or(now, |g| g.next_local_audio_at(now));
                let playback =
                    Playback::new(playback_id, leg, frequency_hz, duration_ms, start, ssrc)?;
                let out_index = if leg == Leg::A { 0 } else { 2 };
                let pending_before = call.pending[out_index].len();
                // 仅替代主音频和舒适噪声；已排队的 telephone-event 不能被播放命令清掉。
                call.pending[out_index].retain(|packet| {
                    rtp::payload_type(&packet.bytes)
                        .is_none_or(|pt| pt != call.payload && Some(pt) != call.cn)
                });
                self.stats.playback_replaced_packets +=
                    (pending_before - call.pending[out_index].len()) as u64;
                self.playback_timers.insert((start, slot));
                let reply = playback_response(session, &playback);
                call.playback = Some(Box::new(playback));
                Ok(reply)
            }
            Command::PlaybackFileStart {
                session,
                playback_id,
                leg,
                path,
            } => {
                super::file_loader::validate_path(&path)?;
                let loader = self
                    .file_loader
                    .as_ref()
                    .context("file playback disabled: playback_root is empty")?;
                let slot = *self
                    .session_slots
                    .get(&session)
                    .context("unknown media session")?;
                let call = self.sessions[slot].as_mut().unwrap();
                ensure!(
                    call.pcm_turn
                        .as_ref()
                        .is_none_or(|turn| !pcm_runtime::active(turn)),
                    "PCM turn must be stopped before legacy playback"
                );
                ensure!(
                    call.processed.as_ref().is_none_or(|g| g.is_local())
                        && (call.processed.is_none() || leg == Leg::A),
                    "processed playback requires local A topology"
                );
                ensure!(
                    leg == Leg::A || call.b.is_some(),
                    "B-leg playback requires connected peer"
                );
                ensure!(
                    (call.codec.name == "PCMU" || call.codec.name == "PCMA")
                        && call.codec.sample_rate == 8000
                        && call.codec.rtp_clock_rate == 8000
                        && call.codec.channels == 1,
                    "file playback supports G711 8k mono output only"
                );
                if let Some(previous) = &call.playback {
                    if previous.id == playback_id {
                        ensure!(
                            previous.leg == leg
                                && previous.file.as_ref().is_some_and(|file| file.path == path),
                            "playback id has different parameters"
                        );
                        return Ok(playback_response(session, previous));
                    }
                    ensure!(
                        previous.state != "loading" && previous.state != "running",
                        "playback already running or loading"
                    );
                    ensure!(playback_id > previous.id, "stale playback id");
                }
                ensure!(
                    self.playback_timers.len() < MAX_PLAYBACKS,
                    "worker playback limit reached"
                );
                let now = Instant::now();
                let deadline = now + LOAD_TIMEOUT;
                let ssrc = (session as u32).wrapping_mul(0x9e3779b9)
                    ^ (playback_id as u32).rotate_left(13)
                    ^ 0x72736976;
                let playback =
                    Playback::loading(playback_id, leg, path.clone(), now, deadline, ssrc)?;
                loader.submit(LoadJob {
                    slot,
                    token: call.tokens[0],
                    playback_id,
                    path,
                    cancelled: Arc::clone(&playback.file.as_ref().unwrap().cancelled),
                    deadline,
                })?;
                self.playback_timers.insert((deadline, slot));
                let reply = playback_response(session, &playback);
                call.playback = Some(Box::new(playback));
                Ok(reply)
            }
            Command::PlaybackStatus {
                session,
                playback_id,
            } => self.playback_command(session, playback_id, false),
            Command::PlaybackStop {
                session,
                playback_id,
            } => self.playback_command(session, playback_id, true),
            Command::PcmTurnBegin {
                session,
                turn_id,
                buffer_ms,
                prebuffer_ms,
            } => self.pcm_begin(session, turn_id, buffer_ms, prebuffer_ms),
            Command::PcmTurnEnd {
                session,
                turn_id,
                final_samples,
            } => self.pcm_end(session, turn_id, final_samples),
            Command::PcmTurnInterrupt {
                session,
                turn_id,
                fade_ms,
            } => self.pcm_interrupt(session, turn_id, fade_ms),
            Command::PcmTurnStatus { session, turn_id } => self.pcm_status(session, turn_id),
            Command::RxSubscribe {
                session,
                subscription_id,
            } => self.rx_subscribe(session, subscription_id),
            Command::RxUnsubscribe {
                session,
                subscription_id,
            } => self.rx_control(session, subscription_id, true),
            Command::RxStatus {
                session,
                subscription_id,
            } => self.rx_control(session, subscription_id, false),
            Command::Shutdown => Ok(Response::Ack),
        }
    }

    /// 查询只读状态；停止只能作用于相同任务，旧的延迟 stop 不能终止后来启动的新提示音。
    fn playback_command(&mut self, session: u64, playback_id: u64, stop: bool) -> Result<Response> {
        // 查询先推进已到期播放，避免同一循环中先回 running、随后才处理完成计时项。
        if !stop {
            self.advance_playbacks(Instant::now());
        }
        let slot = *self
            .session_slots
            .get(&session)
            .context("unknown media session")?;
        let playback = self.sessions[slot]
            .as_mut()
            .unwrap()
            .playback
            .as_mut()
            .context("no playback for session")?;
        ensure!(playback.id == playback_id, "playback id mismatch");
        if stop && (playback.state == "running" || playback.state == "loading") {
            self.playback_timers.remove(&(playback.next, slot));
            playback.finish("stopped");
        }
        Ok(playback_response(session, playback))
    }

    /// 最多消费一个有界结果队列，磁盘完成只唤醒事件循环，不在加载线程直接访问通话。
    fn process_loaded_files(&mut self) {
        for _ in 0..16 {
            let Some(result) = self
                .file_loader
                .as_ref()
                .and_then(|loader| loader.results.try_recv().ok())
            else {
                break;
            };
            self.accept_loaded_file(result);
        }
    }

    /// 四项身份/状态同时匹配才可开始：槽位仍有效、token 同代际、任务号相同且仍为 loading。
    fn accept_loaded_file(&mut self, result: LoadResult) {
        let Some(call) = self.sessions.get_mut(result.slot).and_then(Option::as_mut) else {
            return;
        };
        if call.tokens[0] != result.token {
            return;
        }
        let Some(playback) = call.playback.as_mut() else {
            return;
        };
        if playback.id != result.playback_id || playback.state != "loading" {
            return;
        }
        self.playback_timers.remove(&(playback.next, result.slot));
        let now = Instant::now();
        if now >= playback.next {
            playback.fail("file_load_timeout");
            return;
        }
        match result.result {
            Ok(samples) => {
                // 直到完整校验/解码完成才替代桥音频；排队中的电话事件仍保留。
                let index = if playback.leg == Leg::A { 0 } else { 2 };
                let before = call.pending[index].len();
                call.pending[index].retain(|packet| {
                    rtp::payload_type(&packet.bytes)
                        .is_none_or(|pt| pt != call.payload && Some(pt) != call.cn)
                });
                self.stats.playback_replaced_packets += (before - call.pending[index].len()) as u64;
                let start = call
                    .processed
                    .as_ref()
                    .map_or(now, |g| g.next_local_audio_at(now));
                playback.loaded(samples, start);
                self.playback_timers.insert((start, result.slot));
            }
            Err(message) => playback.fail(&message),
        }
    }

    /// 最多处理 128 个到期方向；每个方向最多 32 个未完成/去重记录，持续报文只更新一个计时项。
    fn expire_dtmf(&mut self, now: Instant) {
        for _ in 0..SOCKETS_PER_TURN {
            let Some(&(when, slot, leg)) = self.dtmf_timers.first() else {
                break;
            };
            if when > now {
                break;
            }
            self.dtmf_timers.pop_first();
            let Some(call) = self.sessions[slot].as_mut() else {
                continue;
            };
            if call.dtmf_due[leg] != Some(when) {
                continue;
            }
            call.dtmf_due[leg] = None;
            if let Some(tracker) = call.dtmf_trackers[leg].as_mut() {
                tracker.expire(now, &mut self.dtmf_journal);
                if let Some(next) = tracker.deadline() {
                    call.dtmf_due[leg] = Some(next);
                    self.dtmf_timers.insert((next, slot, leg));
                }
            }
        }
    }

    /// 每轮主动播放不超过 64 项，每任务只发送一帧；迟到或 UDP 阻塞直接记录失败，避免堆积音频。
    fn advance_playbacks(&mut self, now: Instant) {
        for _ in 0..MAX_PLAYBACKS {
            let Some(&(when, slot)) = self.playback_timers.first() else {
                break;
            };
            if when > now {
                break;
            }
            self.playback_timers.pop_first();
            let Some(call) = self.sessions[slot].as_mut() else {
                continue;
            };
            let Some(playback) = call.playback.as_mut() else {
                continue;
            };
            if playback.next != when {
                continue;
            }
            if playback.state == "loading" {
                playback.fail("file_load_timeout");
                continue;
            }
            if playback.state != "running" {
                continue;
            }
            if playback.sent == playback.total {
                playback.finish("completed");
                continue;
            }
            if let Some(graph) = call.processed.as_mut() {
                let mut pcm = [0i16; 160];
                let mut wire = [0u8; 172];
                let frame_at = Instant::now();
                if !playback.pcm_frame(frame_at, &mut pcm) {
                    record_output_lateness(
                        &mut self.stats,
                        frame_at.saturating_duration_since(when),
                    );
                    self.stats.send_expired += 1;
                    self.stats.processed_send_deadline_misses += 1;
                    continue;
                }
                let len = match graph.encode_local_pcm(
                    &pcm,
                    when,
                    playback.sent == 0,
                    &mut wire,
                    &mut self.stats,
                ) {
                    Ok(len) => len,
                    Err(_) => {
                        self.stats.processed_send_errors += 1;
                        playback.fail("processed_playback_encode_failed");
                        continue;
                    }
                };
                let send_at = Instant::now();
                let late = send_at.saturating_duration_since(when);
                record_output_lateness(&mut self.stats, late);
                let expired = late >= Duration::from_millis(20);
                let accepted = !expired
                    && matches!(send_packet(&call.sockets[0], &wire[..len], call.a.rtp, call.connected), Ok(n) if n == len);
                graph.note_send(1, &wire[..len], send_at, accepted);
                if accepted {
                    self.stats.tx_packets += 1;
                    self.stats.tx_bytes += len as u64;
                    playback.accepted();
                    self.playback_timers.insert((playback.next, slot));
                } else if expired {
                    self.stats.send_expired += 1;
                    self.stats.processed_send_deadline_misses += 1;
                    playback.fail("playback_schedule_late");
                } else {
                    self.stats.send_errors += 1;
                    self.stats.processed_send_errors += 1;
                    playback.fail("playback_udp_send_failed");
                }
                continue;
            }
            let (index, destination) = if playback.leg == Leg::A {
                (0, call.a.rtp)
            } else {
                (2, call.b.as_ref().unwrap().rtp)
            };
            let Some(bytes) =
                playback.packet(call.payload, call.codec.name == "PCMA", Instant::now())
            else {
                continue;
            };
            if let Some(clock) = &call.local_rtp {
                // 此分支只能是本地A腿；以播放器计划时刻计时，插入按键不会重置音频时间戳。
                clock.stamp(bytes, clock.timestamp(when));
            }
            match send_packet(&call.sockets[index], bytes, destination, call.connected) {
                Ok(172) => {
                    self.stats.tx_packets += 1;
                    self.stats.tx_bytes += 172;
                    playback.accepted();
                    if let Some(clock) = &mut call.local_rtp {
                        clock.accepted();
                    }
                    self.playback_timers.insert((playback.next, slot));
                }
                _ => {
                    self.stats.send_errors += 1;
                    playback.fail("playback_udp_send_failed");
                }
            }
        }
    }

    /// 与提示音交错逐包调度，不创建数字线程；UDP失败或迟到时保留明确失败状态。
    fn advance_dtmf_senders(&mut self, now: Instant) {
        for _ in 0..MAX_SENDERS {
            let Some(&(due, slot)) = self.dtmf_send_timers.first() else {
                break;
            };
            if due > now {
                break;
            }
            self.dtmf_send_timers.pop_first();
            let Some(call) = self.sessions[slot].as_mut() else {
                continue;
            };
            let Some(sender) = call.dtmf_sender.as_mut() else {
                continue;
            };
            if sender.next != Some(due) {
                continue;
            }
            if let Some(graph) = call.processed.as_mut() {
                let packet_at = Instant::now();
                let start = graph
                    .local_timestamp(due - Duration::from_millis(20))
                    .expect("本地发送器仅由local图受理");
                let Some(packet) = sender.packet_on_timeline(
                    call.dtmf.unwrap(),
                    call.dtmf_clock,
                    start,
                    packet_at,
                ) else {
                    record_output_lateness(
                        &mut self.stats,
                        packet_at.saturating_duration_since(due),
                    );
                    self.stats.send_expired += 1;
                    self.stats.processed_send_deadline_misses += 1;
                    self.stats.dtmf_send_errors += 1;
                    continue;
                };
                let mut wire = [0u8; 16];
                if graph.write_local_event(&packet, &mut wire).is_err() {
                    self.stats.processed_send_errors += 1;
                    self.stats.dtmf_send_errors += 1;
                    sender.fail("processed_dtmf_packet_failed");
                    continue;
                }
                let send_at = Instant::now();
                let late = send_at.saturating_duration_since(due);
                record_output_lateness(&mut self.stats, late);
                let expired = late >= Duration::from_millis(20);
                let accepted = !expired
                    && matches!(
                        send_packet(&call.sockets[0], &wire, call.a.rtp, call.connected),
                        Ok(16)
                    );
                graph.note_send(1, &wire, send_at, accepted);
                if accepted {
                    sender.accepted();
                    self.stats.tx_packets += 1;
                    self.stats.tx_bytes += 16;
                    self.stats.dtmf_send_packets += 1;
                } else {
                    self.stats.dtmf_send_errors += 1;
                    if expired {
                        self.stats.send_expired += 1;
                        self.stats.processed_send_deadline_misses += 1;
                        sender.fail("dtmf_send_scheduler_late");
                    } else {
                        self.stats.send_errors += 1;
                        self.stats.processed_send_errors += 1;
                        sender.fail("dtmf_udp_send_failed");
                    }
                }
                if let Some(next) = sender.next {
                    self.dtmf_send_timers.insert((next, slot));
                }
                continue;
            }
            let clock = call.local_rtp.as_mut().unwrap();
            let Some(packet) = sender.packet(call.dtmf.unwrap(), clock, Instant::now()) else {
                self.stats.dtmf_send_errors += 1;
                continue;
            };
            match send_packet(&call.sockets[0], &packet, call.a.rtp, call.connected) {
                Ok(16) => {
                    sender.accepted();
                    clock.accepted();
                    self.stats.tx_packets += 1;
                    self.stats.tx_bytes += 16;
                    self.stats.dtmf_send_packets += 1;
                }
                _ => {
                    self.stats.send_errors += 1;
                    self.stats.dtmf_send_errors += 1;
                    sender.fail("dtmf_udp_send_failed");
                }
            }
            if let Some(next) = sender.next {
                self.dtmf_send_timers.insert((next, slot));
            }
        }
    }

    /// 校验协商地址、网络白名单及本 worker 范围回环；不替代 SIP 认证或 SRTP 验证。
    /// 这里只认识本 worker 的端口范围，跨 worker 的规划约束仍由控制面负责。
    fn check_peer(&self, peer: &Peer) -> Result<()> {
        for address in [peer.rtp, peer.rtcp] {
            let ip = address.ip();
            ensure!(
                address.port() > 0
                    && !ip.is_unspecified()
                    && !ip.is_multicast()
                    && !ip.is_broadcast(),
                "invalid peer address"
            );
            ensure!(
                self.config
                    .allowed_remote_networks
                    .iter()
                    .any(|net| net.contains(&std::net::IpAddr::V4(*ip))),
                "peer outside media allowlist"
            );
            ensure!(
                !(ip == &self.config.bind_ip
                    && (self.config.port_start..=self.config.port_end).contains(&address.port())),
                "peer points into worker media range"
            );
        }
        ensure!(peer.rtp != peer.rtcp, "RTCP multiplexing is not supported");
        Ok(())
    }

    /// 将超额 socket 暂停到额度恢复，并撤销内核可读监听，避免新到数据仍反复唤醒 poll。
    /// queued 标记仍保留，已缓存的旧就绪事件不会把冷却 socket 提前插回。
    fn defer_read(&mut self, token: usize, when: Instant) -> Result<()> {
        let Some((slot, index)) = self.resolve(token) else {
            return Ok(());
        };
        let entry = (when, token);
        if self.delayed_by_slot[slot * 4 + index].is_none() {
            let call = self.sessions[slot].as_mut().unwrap();
            self.poll
                .registry()
                .deregister(&mut call.sockets[index])
                .context("cannot suspend flooded media socket")?;
        }
        if let Some(previous) = self.delayed_by_slot[slot * 4 + index].replace(entry) {
            self.delayed.remove(&previous);
        }
        self.delayed.insert(entry);
        Ok(())
    }

    /// 到期后重新注册监听并主动读取，而不是等待新边缘事件；每项只对应一个当前 socket。
    fn resume_reads(&mut self, now: Instant) -> Result<()> {
        for _ in 0..SOCKETS_PER_TURN {
            if !self.delayed.first().is_some_and(|(when, _)| *when <= now) {
                break;
            }
            let entry @ (_, token) = self.delayed.pop_first().unwrap();
            let Some(offset) = token_offset(token, self.queued.len()) else {
                continue;
            };
            if self.delayed_by_slot[offset] != Some(entry) {
                continue;
            }
            self.delayed_by_slot[offset] = None;
            if let Some((slot, index)) = self.resolve(token) {
                let call = self.sessions[slot].as_mut().unwrap();
                self.poll
                    .registry()
                    .register(&mut call.sockets[index], Token(token), Interest::READABLE)
                    .context("cannot resume media socket after receive cooldown")?;
            }
            if self.resolve(token).is_some() && self.queued[offset] == token {
                self.ready.push_back(token);
            }
        }
        Ok(())
    }

    /// 有界读取一个 socket 并转发；工作额度在来源校验前生效，错误来源不能免费占用 CPU。
    /// 冷却期间留包在内核，异常流可能触发内核丢包；不把未读取包伪记成已转发或已丢弃。
    fn receive(&mut self, token: usize, batch: &mut ReceiveBatch) -> ReceiveState {
        let Some((slot, index)) = self.resolve(token) else {
            return ReceiveState::Drained;
        };
        let call = self.sessions[slot].as_mut().unwrap();
        for _ in 0..PACKETS_PER_TURN / BATCH_SIZE {
            let now = Instant::now();
            let allowance = call.receive_work[index].available(now) as usize;
            if allowance == 0 {
                return ReceiveState::Deferred(now + call.receive_work[index].wait_for_token(now));
            }
            let calls_before = batch.syscalls;
            let received = batch.receive_limited(&call.sockets[index], allowance.min(BATCH_SIZE));
            self.stats.receive_syscalls += batch.syscalls - calls_before;
            let count = match received {
                Ok(count) => count,
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => return ReceiveState::Drained,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(_) => {
                    // 持续系统调用错误也消耗一个工作额度，避免单故障 fd 被立即反复重试。
                    call.receive_work[index].take(now);
                    self.stats.receive_errors += 1;
                    continue;
                }
            };
            // 接收容量不超过刚观察到的完整额度，因此按实际返回数量扣除一定成功。
            let consumed = call.receive_work[index].take_many(count as u32, now);
            debug_assert!(consumed);
            self.stats.receive_batches += 1;
            self.stats.max_receive_batch = self.stats.max_receive_batch.max(count);
            for received in &batch.packets[..count] {
                let (len, source, kernel_drops) =
                    (received.len, received.source, received.kernel_drops);
                self.stats.rx_packets += 1;
                self.stats.rx_bytes += len as u64;
                if let Some(drops) = kernel_drops {
                    // Linux 的计数是每 socket 累计 u32，使用环绕减法兼容计数器回绕。
                    self.stats.socket_rx_drops +=
                        u64::from(drops.wrapping_sub(call.last_kernel_drops[index]));
                    call.last_kernel_drops[index] = drops;
                }
                let expected = match index {
                    0 => call.a.rtp,
                    1 => call.a.rtcp,
                    2 => match &call.b {
                        Some(b) => b.rtp,
                        None => {
                            self.stats.peer_not_ready += 1;
                            continue;
                        }
                    },
                    _ => match &call.b {
                        Some(b) => b.rtcp,
                        None => {
                            self.stats.peer_not_ready += 1;
                            continue;
                        }
                    },
                };
                if source != SocketAddr::V4(expected) {
                    self.stats.source_rejected += 1;
                    continue;
                }
                let now = Instant::now();
                if !call.limits[index].take(now) {
                    self.stats.rate_limited += 1;
                    continue;
                }
                if len >= MAX_PACKET {
                    // 非 Linux 回退无法统一报告原始超长长度，因此容量边界也保守拒绝。
                    self.stats.invalid_packets += 1;
                    continue;
                }
                let packet = &received.bytes[..len];
                let valid = if index % 2 == 0 {
                    rtp::payload(packet).is_some_and(|(pt, bytes)| {
                        // 处理图辅助载荷同样受jitter上限约束；先完整拒绝，不能留下采号/去重副作用。
                        // 旧relay没有新增该限制，继续保留其已支持的原包转发合同。
                        if call.processed.is_some() && bytes.len() > MAX_INPUT_PAYLOAD_BYTES {
                            return false;
                        }
                        (pt == call
                            .processed
                            .as_ref()
                            .map_or(call.payload, |g| g.input_payload(index / 2))
                            && (call.processed.is_none() || bytes.len() == 160))
                            || (Some(pt) == call.dtmf && valid_dtmf(bytes, call.dtmf_mask))
                            || (Some(pt) == call.cn && !bytes.is_empty() && bytes[0] & 0x80 == 0)
                    })
                } else {
                    rtp::valid_rtcp(packet)
                };
                if !valid {
                    self.stats.invalid_packets += 1;
                    continue;
                }
                if index % 2 == 0 && Some(packet[1] & 0x7f) == call.dtmf {
                    if let Some((pt, bytes)) = rtp::payload(packet) {
                        if Some(pt) == call.dtmf {
                            // 已执行源地址、速率、完整头和协商事件集合检查，再采集；桥仍原样转发所有合法结束重传。
                            let leg_index = index / 2;
                            let tracker = call.dtmf_trackers[leg_index]
                                .get_or_insert_with(|| Box::new(DtmfTracker::new(now)));
                            let ssrc = u32::from_be_bytes(packet[8..12].try_into().unwrap());
                            let timestamp = u32::from_be_bytes(packet[4..8].try_into().unwrap());
                            for bytes in bytes.chunks_exact(4) {
                                let event = DtmfEvent {
                                    sequence: 0,
                                    session: call.id,
                                    leg: if index == 0 { Leg::A } else { Leg::B },
                                    kind: String::new(),
                                    digit: String::new(),
                                    event: bytes[0],
                                    duration_ticks: u16::from_be_bytes([bytes[2], bytes[3]]),
                                    clock_rate: call.dtmf_clock,
                                    ssrc,
                                    timestamp,
                                    reason: String::new(),
                                };
                                tracker.observe(
                                    event,
                                    bytes[1] & 0x80 != 0,
                                    now,
                                    &mut self.dtmf_journal,
                                );
                            }
                            if let Some(previous) = call.dtmf_due[leg_index].take() {
                                self.dtmf_timers.remove(&(previous, slot, leg_index));
                            }
                            if let Some(next) = tracker.deadline() {
                                call.dtmf_due[leg_index] = Some(next);
                                self.dtmf_timers.insert((next, slot, leg_index));
                            }
                        }
                    }
                }
                if let Some(graph) = call
                    .processed
                    .as_mut()
                    .filter(|g| g.is_local() || call.b.is_some())
                {
                    if index % 2 == 0 {
                        graph.push(index / 2, packet, now, &mut self.stats);
                        self.media_timers
                            .set(slot * 3 + index / 2, graph.next_deadline(index / 2));
                    } else {
                        if graph
                            .observe_rtcp(index / 2, packet, now, &mut self.stats)
                            .is_err()
                        {
                            self.stats.invalid_packets += 1;
                        }
                        self.media_timers
                            .set(slot * 3 + index / 2, graph.next_deadline(index / 2));
                    }
                    // 生成模式终结两个RTP/RTCP会话，绝不把旧SSRC的报告或编码包透传到另一腿。
                    rx_runtime::capture(call, slot, &mut self.rx_timers, now);
                    continue;
                }
                // 旧本地透传只有DTMF采集；单腿处理图已在上方消费，绝不因没有B腿而误计peer_not_ready。
                let Some(b) = &call.b else {
                    self.stats.peer_not_ready += 1;
                    continue;
                };
                let out_index = index ^ 2;
                if call.playback.as_ref().is_some_and(|p| {
                    p.state == "running" && (if p.leg == Leg::A { 0 } else { 2 }) == out_index
                }) && index % 2 == 0
                    && (packet[1] & 0x7f == call.payload || Some(packet[1] & 0x7f) == call.cn)
                {
                    self.stats.playback_replaced_packets += 1;
                    continue;
                }
                // 固定四槽位中异或 2 对应 A/B 腿互换，保留 RTP 与 RTCP 的奇偶位置。
                let destination = match out_index {
                    0 => call.a.rtp,
                    1 => call.a.rtcp,
                    2 => b.rtp,
                    _ => b.rtcp,
                };
                if call.pending[out_index].is_empty() {
                    match send_packet(
                        &call.sockets[out_index],
                        packet,
                        destination,
                        call.connected,
                    ) {
                        Ok(n) if n == len => {
                            self.stats.tx_packets += 1;
                            self.stats.tx_bytes += n as u64;
                            continue;
                        }
                        Err(e) if e.kind() == io::ErrorKind::WouldBlock => (),
                        _ => {
                            self.stats.send_errors += 1;
                            continue;
                        }
                    }
                }
                if call.pending[out_index].len() >= MAX_SEND_QUEUE {
                    self.stats.send_queue_drops += 1;
                    continue;
                }
                call.pending[out_index].push_back(PendingPacket {
                    // 只有暂时不可写或已有积压时复制数据，避免持有即将复用的批次缓冲。
                    bytes: packet.to_vec(),
                    destination,
                    deadline: now + SEND_DEADLINE,
                });
                let offset = slot * 4 + out_index;
                if self.pending_queued[offset] != call.tokens[out_index] {
                    self.pending_queued[offset] = call.tokens[out_index];
                    self.pending_tokens.push_back(call.tokens[out_index]);
                }
            }
            if batch.drained {
                return ReceiveState::Drained;
            }
        }
        // Mio 使用边缘触发就绪；达到本轮预算后仍保留调度，直到明确收到 WouldBlock。
        ReceiveState::Ready
    }

    /// 每轮至多128项处理工作；音频、RTCP和控制事件交错，释放后无遗留定时项。
    fn advance_processed(&mut self) {
        let mut wire = [0u8; 2048];
        for _ in 0..SOCKETS_PER_TURN {
            // 同批处理期间可能被OS抢占；每个期限项重读时钟，不能用批次起点放行过期音频。
            let now = Instant::now();
            let Some(key) = self.media_timers.pop_due(now) else {
                break;
            };
            let (slot, direction) = (key / 3, key % 3);
            let Some(call) = self.sessions[slot].as_mut() else {
                continue;
            };
            if direction == 2 {
                send_processed_reports(call, now, false, &mut self.stats);
                self.media_timers.set(
                    key,
                    Some(
                        now + Duration::from_millis(
                            2500 + (call.id.wrapping_add(self.stats.tx_packets)) % 5000,
                        ),
                    ),
                );
                continue;
            }
            let Some(graph) = call.processed.as_mut() else {
                continue;
            };
            let due = graph.next_deadline(direction).unwrap_or(now);
            if let Some(len) = graph.advance(direction, now, &mut wire, &mut self.stats) {
                let out_index = (direction ^ 1) * 2;
                let destination = if direction == 0 {
                    call.b.as_ref().unwrap().rtp
                } else {
                    call.a.rtp
                };
                // 编解码之后仍可能被抢占；发送前再次核对完整一帧期限，不追赶发送旧声音。
                let send_at = Instant::now();
                let lateness = send_at.saturating_duration_since(due);
                record_output_lateness(&mut self.stats, lateness);
                let expired = lateness >= Duration::from_millis(20);
                let accepted = !expired
                    && matches!(send_packet(&call.sockets[out_index], &wire[..len], destination, call.connected), Ok(n) if n == len);
                graph.note_send(direction, &wire[..len], send_at, accepted);
                if accepted {
                    self.stats.tx_packets += 1;
                    self.stats.tx_bytes += len as u64;
                } else if expired {
                    self.stats.send_expired += 1;
                    self.stats.processed_send_deadline_misses += 1;
                } else {
                    // 首批即时截止策略：WouldBlock也失败并丢弃，不拖延后续媒体或重用已消耗的包序。
                    self.stats.send_errors += 1;
                    self.stats.processed_send_errors += 1;
                }
            }
            self.media_timers.set(key, graph.next_deadline(direction));
            rx_runtime::capture(call, slot, &mut self.rx_timers, now);
        }
    }

    /// 公平轮询最多 128 个积压 socket，按原顺序重试并清理过期包。
    /// 当前通过短轮询而非 writable 监听重试，调度等待也计入五毫秒截止时间。
    fn flush_pending(&mut self) {
        for _ in 0..self.pending_tokens.len().min(SOCKETS_PER_TURN) {
            let token = self.pending_tokens.pop_front().unwrap();
            let Some((slot, index)) = self.resolve(token) else {
                continue;
            };
            let call = self.sessions[slot].as_mut().unwrap();
            while let Some(packet) = call.pending[index].front() {
                if packet.deadline <= Instant::now() {
                    call.pending[index].pop_front();
                    self.stats.send_expired += 1;
                    continue;
                }
                match send_packet(
                    &call.sockets[index],
                    &packet.bytes,
                    packet.destination,
                    call.connected,
                ) {
                    Ok(n) if n == packet.bytes.len() => {
                        self.stats.tx_packets += 1;
                        self.stats.tx_bytes += n as u64;
                    }
                    Err(e) if e.kind() == io::ErrorKind::WouldBlock => break,
                    _ => {
                        self.stats.send_errors += 1;
                    }
                }
                call.pending[index].pop_front();
            }
            if call.pending[index].is_empty() {
                self.pending_queued[slot * 4 + index] = 0;
            } else {
                self.pending_tokens.push_back(token);
            }
        }
    }
}

/// 只向各腿发送属于自身TX/RX身份的报告；单次失败不阻止BYE释放端口。
fn send_processed_reports(call: &mut Session, now: Instant, bye: bool, stats: &mut Stats) {
    let Some(graph) = &mut call.processed else {
        return;
    };
    if !graph.is_local() && call.b.is_none() {
        return;
    }
    let mut wire = [0u8; 2048];
    for (leg, destination) in [Some(call.a.rtcp), call.b.as_ref().map(|b| b.rtcp)]
        .into_iter()
        .enumerate()
    {
        let Some(destination) = destination else {
            continue;
        };
        match graph.report(leg, now, bye, &mut wire) {
            Ok(len) => {
                if matches!(send_packet(&call.sockets[leg*2+1], &wire[..len], destination, call.connected), Ok(n) if n == len)
                {
                    stats.tx_packets += 1;
                    stats.tx_bytes += len as u64;
                } else {
                    stats.send_errors += 1;
                    stats.processed_send_errors += 1;
                }
            }
            Err(_) => {
                stats.processed_send_errors += 1;
            }
        }
    }
}

/// 仅启动阶段读取系统随机源；实时RTP回调不访问磁盘、熵源或全局锁。
fn media_seed() -> Result<u64> {
    let mut bytes = [0u8; 8];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    Ok(u64::from_ne_bytes(bytes))
}

/// 固定桶观测无需分配/锁；分位数由验收器从这些真实区间计数计算，不能预填目标值。
fn record_output_lateness(stats: &mut Stats, delay: Duration) {
    let ns = delay.as_nanos().min(u128::from(u64::MAX)) as u64;
    const BOUNDS: [u64; 7] = [
        250_000, 500_000, 1_000_000, 2_000_000, 5_000_000, 10_000_000, 20_000_000,
    ];
    let bucket = BOUNDS.iter().position(|bound| ns <= *bound).unwrap_or(7);
    stats.processed_output_lateness_buckets[bucket] += 1;
    stats.processed_output_lateness_ns_max = stats.processed_output_lateness_ns_max.max(ns);
}

/// 按会话连接模式发送一个完整数据报；由调用者检查返回长度并处理短暂不可写。
/// 成功只代表操作系统接受了数据，不能当作对端已收到或网卡没有丢包的证据。
fn send_packet(
    socket: &UdpSocket,
    bytes: &[u8],
    destination: SocketAddrV4,
    connected: bool,
) -> io::Result<usize> {
    if connected {
        socket.send(bytes)
    } else {
        socket.send_to(bytes, SocketAddr::V4(destination))
    }
}

/// 把内部连续四端口块转为控制协议的具名字段，保持 A/B 腿与 RTP/RTCP 对应关系。
fn allocation_response(session: u64, base: u16, processed: bool, local: bool) -> Response {
    Response::Allocated {
        processing_topology: local.then_some(ProcessingTopology::Local),
        processing_version: processed.then_some(1),
        session,
        a_rtp: base,
        a_rtcp: base + 1,
        b_rtp: base + 2,
        b_rtcp: base + 3,
    }
}

/// 播放状态以不可变副本回复，不把内部音频缓冲或时钟对象跨控制线程共享。
fn playback_response(session: u64, playback: &Playback) -> Response {
    Response::PlaybackState {
        session,
        playback_id: playback.id,
        state: playback.state.into(),
        sent_packets: playback.sent,
        total_packets: playback.total,
        message: playback.message.clone(),
    }
}

/// 建立一个非阻塞 IPv4 UDP socket；任一步失败均由所有权析构关闭临时 fd。
/// Linux 额外要求能启用 socket 溢出观测，避免把“不支持观测”误报成零丢包。
fn bind_socket(config: &WorkerConfig, port: u16) -> io::Result<UdpSocket> {
    let socket = Socket::new(Domain::IPV4, Type::DGRAM, Some(Protocol::UDP))?;
    socket.set_nonblocking(true)?;
    socket.set_recv_buffer_size(config.receive_buffer_bytes)?;
    socket.bind(&SocketAddr::V4(SocketAddrV4::new(config.bind_ip, port)).into())?;
    #[cfg(target_os = "linux")]
    {
        use std::os::fd::AsRawFd;
        let enabled: libc::c_int = 1;
        // SAFETY: 安全依据：socket 的 fd 仍有效，enabled 的地址和长度匹配 SO_RXQ_OVFL
        // 所需的整型参数，同步调用期间该栈变量不会销毁。
        let status = unsafe {
            libc::setsockopt(
                socket.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_RXQ_OVFL,
                std::ptr::from_ref(&enabled).cast(),
                std::mem::size_of_val(&enabled) as libc::socklen_t,
            )
        };
        if status != 0 {
            return Err(io::Error::last_os_error());
        }
    }
    Ok(UdpSocket::from_std(socket.into()))
}

#[cfg(target_os = "linux")]
/// 仅修改调用线程的 CPU 亲和性；索引范围与系统调用失败都会显式返回。
fn set_affinity(core: usize) -> Result<()> {
    ensure!(core < libc::CPU_SETSIZE as usize, "CPU index out of range");
    // SAFETY: 安全依据：CPU 集合存储已经初始化，core 先校验在 CPU_SETSIZE 范围内；
    // pid 为 0 指定当前线程，传入尺寸与实际集合结构一致。
    unsafe {
        let mut set: libc::cpu_set_t = std::mem::zeroed();
        libc::CPU_ZERO(&mut set);
        libc::CPU_SET(core, &mut set);
        if libc::sched_setaffinity(0, std::mem::size_of_val(&set), &set) != 0 {
            return Err(io::Error::last_os_error().into());
        }
    }
    Ok(())
}

#[cfg(not(target_os = "linux"))]
/// 非 Linux 平台不静默忽略绑定要求，防止配置看似生效但实际没有绑定。
fn set_affinity(_: usize) -> Result<()> {
    anyhow::bail!("CPU affinity requires Linux")
}

/// 启动前检查四 fd/会话及少量额外 fd 的额度，不替控制面设置系统资源上限。
fn check_fd_limit(calls: usize) -> Result<()> {
    // SAFETY: 安全依据：输出指针指向有效 rlimit 结构，资源参数为已定义的 RLIMIT_NOFILE。
    let current = unsafe {
        let mut limit: libc::rlimit = std::mem::zeroed();
        if libc::getrlimit(libc::RLIMIT_NOFILE, &mut limit) != 0 {
            return Err(io::Error::last_os_error().into());
        }
        limit.rlim_cur
    };
    ensure!(
        current >= (calls * 4 + 32) as libc::rlim_t,
        "file descriptor limit {current} is too low: need at least {}",
        calls * 4 + 32
    );
    Ok(())
}

/// 采样整个进程的累计 CPU 时间和 RSS 峰值，统一 macOS/Linux 的内存计量单位。
fn update_process_usage(stats: &mut Stats) {
    // SAFETY: 安全依据：getrusage 的输出存储已初始化且大小正确，RUSAGE_SELF 只读取本进程统计。
    let usage = unsafe {
        let mut value: libc::rusage = std::mem::zeroed();
        if libc::getrusage(libc::RUSAGE_SELF, &mut value) != 0 {
            return;
        }
        value
    };
    stats.user_cpu_seconds =
        usage.ru_utime.tv_sec as f64 + usage.ru_utime.tv_usec as f64 / 1_000_000.0;
    stats.system_cpu_seconds =
        usage.ru_stime.tv_sec as f64 + usage.ru_stime.tv_usec as f64 / 1_000_000.0;
    #[cfg(target_os = "linux")]
    {
        stats.peak_resident_bytes = usage.ru_maxrss.max(0) as u64 * 1024;
    }
    #[cfg(not(target_os = "linux"))]
    {
        stats.peak_resident_bytes = usage.ru_maxrss.max(0) as u64;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn processed_oversized_dtmf_is_atomic_before_collection_while_relay_stays_compatible() {
        for topology in [
            None,
            Some(ProcessingTopology::Bridge),
            Some(ProcessingTopology::Local),
        ] {
            let mut engine = isolated_engine();
            let source = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
            let destination = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
            destination
                .set_read_timeout(Some(Duration::from_millis(500)))
                .unwrap();
            let source_rtcp = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
            let destination_rtcp = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
            let peer = |rtp: &std::net::UdpSocket, rtcp: &std::net::UdpSocket| Peer {
                rtp: rtp.local_addr().unwrap().to_string().parse().unwrap(),
                rtcp: rtcp.local_addr().unwrap().to_string().parse().unwrap(),
            };
            let call = engine.sessions[0].as_mut().unwrap();
            call.a = peer(&source, &source_rtcp);
            call.b = (topology != Some(ProcessingTopology::Local))
                .then(|| peer(&destination, &destination_rtcp));
            call.dtmf = Some(101);
            call.offered_dtmf = Some(101);
            call.dtmf_mask = 0xffff;
            let format = MediaFormat {
                codec: Some(call.codec.clone()),
                ..Default::default()
            };
            if let Some(topology) = topology {
                let mut graph = Graph::new(
                    ProcessingPlan {
                        topology,
                        version: 1,
                        mode: "g711".into(),
                        jitter_target_ms: 40,
                        max_delay_ms: 120,
                    },
                    0,
                    &call.codec,
                    Some(101),
                    &format,
                    42,
                    Instant::now(),
                )
                .unwrap();
                if topology == ProcessingTopology::Bridge {
                    graph
                        .connect(0, &call.codec, Some(101), &format, &mut engine.stats)
                        .unwrap();
                }
                call.processed = Some(Box::new(graph));
            }
            let address = call.sockets[0].local_addr().unwrap();
            let token = call.tokens[0];
            // 每块都是合法结束事件，合计484字节仅比图的480字节上限多一个事件。
            let mut oversized = vec![0x80, 101, 0, 1, 0, 0, 0, 160, 0, 0, 0, 42];
            for _ in 0..121 {
                oversized.extend_from_slice(&[1, 0x8a, 0, 160]);
            }
            source.send_to(&oversized, address).unwrap();
            receive_test_packet(&mut engine, token, &mut ReceiveBatch::default());
            let (events, _, _, _) = engine.dtmf_journal.read(0, 16).unwrap();
            if topology.is_none() {
                assert_eq!(engine.stats.invalid_packets, 0);
                assert_eq!(events.len(), 1);
                let mut received = [0; 1024];
                let len = destination.recv(&mut received).unwrap();
                assert_eq!(
                    &received[..len],
                    &oversized,
                    "旧relay不增加处理图的载荷限制"
                );
            } else {
                assert_eq!(engine.stats.invalid_packets, 1);
                assert!(
                    events.is_empty(),
                    "超出图预算的RTP不能先触发按键采集: {events:?}"
                );
                let call = engine.sessions[0].as_ref().unwrap();
                assert!(call.dtmf_trackers[0].is_none());
                assert!(call.processed.as_ref().unwrap().next_deadline(0).is_none());
                assert!(engine.dtmf_timers.is_empty());
                assert_eq!(engine.stats.tx_packets, 0);
                source.send_to(&oversized[..16], address).unwrap();
                receive_test_packet(&mut engine, token, &mut ReceiveBatch::default());
                let (events, _, _, _) = engine.dtmf_journal.read(0, 16).unwrap();
                assert_eq!(events.len(), 1, "拒绝大包不能污染同事件合法小包的去重状态");
                assert_eq!(events[0].digit, "1");
                assert_eq!(engine.stats.invalid_packets, 1);
                let mut at_limit = oversized[..12].to_vec();
                at_limit[2..4].copy_from_slice(&2u16.to_be_bytes());
                at_limit[4..8].copy_from_slice(&320u32.to_be_bytes());
                for _ in 0..120 {
                    at_limit.extend_from_slice(&[2, 0x8a, 0, 160]);
                }
                source.send_to(&at_limit, address).unwrap();
                receive_test_packet(&mut engine, token, &mut ReceiveBatch::default());
                let (events, _, _, _) = engine.dtmf_journal.read(0, 16).unwrap();
                assert_eq!(events.len(), 2, "恰好480字节仍属于现有图的合法上限");
                assert_eq!(events[1].digit, "2");
                assert_eq!(engine.stats.invalid_packets, 1);
            }
        }
    }

    /// 仅使用本轮测试保留的小端口块；真实Allocate负责绑定、A连接、拓扑回执和定时器。
    fn allocated_local_engine(
        connected: bool,
    ) -> (Engine, std::net::UdpSocket, std::net::UdpSocket) {
        static NEXT_BLOCK: std::sync::atomic::AtomicU16 = std::sync::atomic::AtomicU16::new(8500);
        let mut engine = isolated_engine();
        engine.sessions[0] = None;
        engine.session_slots.clear();
        let base = (0..100)
            .map(|_| NEXT_BLOCK.fetch_add(4, std::sync::atomic::Ordering::Relaxed))
            .take_while(|base| *base < 8900)
            .find(|base| {
                (0..4)
                    .map(|n| std::net::UdpSocket::bind(("127.0.0.1", base + n)))
                    .collect::<io::Result<Vec<_>>>()
                    .is_ok()
            })
            .expect("私有测试端口块不可用");
        engine.config.port_start = base;
        engine.config.port_end = base + 3;
        engine.config.connect_sockets = connected;
        engine.ports = PortPool::new(base, base + 3, Duration::ZERO);
        let a_rtp = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let a_rtcp = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        a_rtp.set_nonblocking(true).unwrap();
        a_rtcp.set_nonblocking(true).unwrap();
        let command = serde_json::json!({"op":"allocate","session":23,"a":{"rtp":a_rtp.local_addr().unwrap(),"rtcp":a_rtcp.local_addr().unwrap()},"payload":0,"dtmf_payload":101,
            "processing":{"version":1,"mode":"g711","topology":"local","jitter_target_ms":40,"max_delay_ms":120}});
        let reply = engine
            .command(serde_json::from_value(command.clone()).unwrap())
            .unwrap();
        let value = serde_json::to_value(reply).unwrap();
        assert_eq!(value["processing_topology"], "local");
        assert_eq!(value["processing_version"], 1);
        assert_eq!(
            serde_json::to_value(
                engine
                    .command(serde_json::from_value(command).unwrap())
                    .unwrap()
            )
            .unwrap(),
            value
        );
        assert_eq!(engine.stats.processed_local_active_calls, 1);
        assert!(engine.media_timers.first().is_some());
        let Response::Stats { stats } = engine.command(Command::Stats).unwrap() else {
            panic!()
        };
        assert_eq!(stats.connected_sockets, if connected { 2 } else { 0 });
        (engine, a_rtp, a_rtcp)
    }

    #[test]
    fn local_allocate_receives_pcm_without_echo_and_release_sends_only_a_rtcp_bye() {
        for connected in [false, true] {
            let (mut engine, a_rtp, a_rtcp) = allocated_local_engine(connected);
            let call = engine.sessions[0].as_ref().unwrap();
            let destination = call.sockets[0].local_addr().unwrap();
            let token = call.tokens[0];
            let b = Peer {
                rtp: "127.0.0.1:1".parse().unwrap(),
                rtcp: "127.0.0.1:2".parse().unwrap(),
            };
            assert!(engine
                .command(Command::Connect {
                    session: 23,
                    b,
                    payload: Some(0),
                    dtmf_payload: Some(101),
                    format: MediaFormat::default()
                })
                .is_err());
            assert!(engine.sessions[0].as_ref().unwrap().b.is_none());
            let mut packet = [0x55u8; 172];
            packet[..12].copy_from_slice(&[0x80, 0x80, 0, 1, 0, 0, 0, 0, 0, 0, 0, 42]);
            a_rtp.send_to(&packet, destination).unwrap();
            receive_test_packet(&mut engine, token, &mut ReceiveBatch::default());
            let graph = engine.sessions[0]
                .as_mut()
                .unwrap()
                .processed
                .as_mut()
                .unwrap();
            let due = graph.next_deadline(0).unwrap();
            assert!(graph
                .advance(0, due, &mut [0; 2048], &mut engine.stats)
                .is_none());
            assert_eq!(engine.stats.processed_local_consumed_frames, 1);
            assert!(engine.stats.processed_local_energy_max > 0);
            assert_eq!(engine.stats.peer_not_ready, 0);
            assert!(
                a_rtp.recv(&mut [0; 2048]).is_err(),
                "单腿收到的音频不得反射"
            );
            engine.command(Command::Release { session: 23 }).unwrap();
            assert_eq!(engine.stats.processed_local_active_calls, 0);
            assert_eq!(engine.stats.processed_active_calls, 0);
            assert!(engine.media_timers.first().is_none());
            let mut wire = [0; 2048];
            // Release 已将 RTCP 交给内核，但本地 UDP 接收仍可能晚于非阻塞读取。
            // 仅在测试接收端有界等待回执，仍严格校验 RR/BYE 内容与会话清理结果。
            a_rtcp.set_nonblocking(false).unwrap();
            a_rtcp
                .set_read_timeout(Some(Duration::from_millis(500)))
                .unwrap();
            let len = a_rtcp.recv(&mut wire).unwrap();
            assert_eq!(wire[1], 201, "没有生成RTP时报告RR而非伪造SR");
            assert_eq!(wire[len - 7], 203);
            assert_eq!(
                u32::from_be_bytes(wire[8..12].try_into().unwrap()),
                42,
                "RR报告块必须属于A输入源"
            );
            assert!(engine.sessions[0].is_none());
            assert!(engine.resolve(token).is_none());
        }
    }

    #[test]
    fn local_playback_stop_stale_file_and_release_leave_no_old_output() {
        let (mut engine, a_rtp, _a_rtcp) = allocated_local_engine(false);
        let now = Instant::now();
        engine
            .command(Command::PlaybackStart {
                session: 23,
                playback_id: 1,
                leg: Leg::A,
                frequency_hz: 1000,
                duration_ms: 100,
            })
            .unwrap();
        engine.advance_playbacks(Instant::now());
        let mut wire = [0; 2048];
        // UDP接受不等于对端队列已立即可读，等待单个预期包但不重发或重复推进播放器。
        a_rtp.set_nonblocking(false).unwrap();
        a_rtp
            .set_read_timeout(Some(Duration::from_millis(500)))
            .unwrap();
        let received = a_rtp.recv(&mut wire);
        a_rtp.set_nonblocking(true).unwrap();
        assert_eq!(
            received.unwrap_or_else(|error| panic!("播放帧未收到: {error}; {:?}", engine.stats)),
            172
        );
        assert_eq!(engine.stats.processed_encoded_frames, 1);
        assert_eq!(engine.stats.processed_decoded_frames, 0);
        assert_eq!(
            engine
                .stats
                .processed_output_lateness_buckets
                .iter()
                .sum::<u64>(),
            1
        );
        engine
            .command(Command::PlaybackStop {
                session: 23,
                playback_id: 1,
            })
            .unwrap();
        assert!(engine
            .command(Command::PlaybackStart {
                session: 23,
                playback_id: 1,
                leg: Leg::A,
                frequency_hz: 500,
                duration_ms: 100
            })
            .is_err());
        assert!(engine.playback_timers.is_empty());
        let call = engine.sessions[0].as_mut().unwrap();
        let old_token = call.tokens[0];
        call.playback = Some(Box::new(
            Playback::loading(2, Leg::A, "test.wav".into(), now, now + LOAD_TIMEOUT, 99).unwrap(),
        ));
        engine
            .command(Command::PlaybackStop {
                session: 23,
                playback_id: 2,
            })
            .unwrap();
        engine.accept_loaded_file(LoadResult {
            slot: 0,
            token: old_token,
            playback_id: 2,
            result: Ok(Arc::from([123i16; 160])),
        });
        assert!(engine.playback_timers.is_empty());
        engine.advance_playbacks(Instant::now() + Duration::from_secs(1));
        assert!(a_rtp.recv(&mut wire).is_err());
        engine
            .command(Command::DtmfSend {
                session: 23,
                leg: Leg::A,
                digits: "12".into(),
                duration_ms: 55,
            })
            .unwrap();
        assert!(
            engine.sessions[0].as_ref().unwrap().local_rtp.is_none(),
            "Graph使用自身TX，不分配第二套序号/SSRC"
        );
        engine.command(Command::Release { session: 23 }).unwrap();
        assert!(engine.dtmf_send_timers.is_empty());
        assert!(engine.playback_timers.is_empty());
        assert_eq!(engine.stats.dtmf_send_cancelled_digits, 2);
        engine.accept_loaded_file(LoadResult {
            slot: 0,
            token: old_token,
            playback_id: 2,
            result: Ok(Arc::from([999i16; 160])),
        });
        engine.advance_playbacks(Instant::now() + Duration::from_secs(2));
        engine.advance_dtmf_senders(Instant::now() + Duration::from_secs(2));
        assert!(a_rtp.recv(&mut wire).is_err());
    }

    #[test]
    fn rx_release_lane_failure_immediately_reaches_other_silent_subscription() {
        let mut engine = isolated_engine();
        engine.sessions[0].as_mut().unwrap().rx_subscription =
            Some(Box::new(RxSubscription::new(1, 1).unwrap()));
        let mut another = isolated_engine();
        let mut releasing = another.sessions[0].take().unwrap();
        releasing.id = 2;
        releasing.rx_subscription = Some(Box::new(RxSubscription::new(2, 2).unwrap()));
        let (socket, peer) = std::os::unix::net::UnixDatagram::pair().unwrap();
        engine.rx_lane = Some(RxLane::from_test_socket(socket));
        drop(peer); // 真实关闭消费者，Release里的首次写即检测到共享lane失联。
        assert!(engine.rx_timers.first().is_none());
        engine.finish_rx(&mut releasing);
        let Response::RxState { status, .. } = engine.rx_control(1, 1, false).unwrap() else {
            panic!("expected rx_state")
        };
        assert_eq!(status.state, rx_export::State::Failed);
        assert_eq!(status.error, "rx_lane_unavailable");
        assert!(engine.rx_timers.first().is_none());
        assert!(engine.command(Command::Stats).is_ok());
        assert!(engine.command(Command::Release { session: 1 }).is_ok());
        assert_eq!(engine.session_slots.len(), 0);
    }

    /// 仅绑定系统临时 UDP 端点，手工构造一个会话，避免访问主服务或固定媒体端口。
    fn isolated_engine() -> Engine {
        let config = WorkerConfig {
            playback_root: String::new(),
            connect_sockets: false,
            worker_id: 0,
            bind_ip: "127.0.0.1".parse().unwrap(),
            port_start: 1024,
            port_end: 1027,
            excluded_port_blocks: Vec::new(),
            max_calls: 1,
            receive_buffer_bytes: 65_536,
            port_reuse_delay_ms: 0,
            max_packets_per_second_per_leg: 100,
            allowed_remote_networks: vec!["127.0.0.0/8".parse().unwrap()],
            cpu_core: None,
        };
        let mut sockets: [UdpSocket; 4] = std::array::from_fn(|_| {
            let socket = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
            socket.set_nonblocking(true).unwrap();
            UdpSocket::from_std(socket)
        });
        let poll = Poll::new().unwrap();
        for (index, socket) in sockets.iter_mut().enumerate() {
            poll.registry()
                .register(socket, Token(5 + index), Interest::READABLE)
                .unwrap();
        }
        let now = Instant::now();
        let peer = |rtp, rtcp| Peer {
            rtp: SocketAddrV4::new(config.bind_ip, rtp),
            rtcp: SocketAddrV4::new(config.bind_ip, rtcp),
        };
        let call = Session {
            processed: None,
            id: 1,
            connected: false,
            base: 1024,
            sockets,
            tokens: [5, 6, 7, 8],
            a: peer(1, 2),
            b: Some(peer(3, 4)),
            payload: 0,
            offered_dtmf: None,
            dtmf: None,
            codec: CodecSpec::legacy(0).unwrap(),
            offered_format: MediaFormat::default(),
            cn: None,
            dtmf_mask: 0,
            dtmf_clock: 8000,
            dtmf_trackers: std::array::from_fn(|_| None),
            dtmf_due: [None; 2],
            playback: None,
            pcm_turn: None,
            rx_subscription: None,
            dtmf_sender: None,
            local_rtp: None,
            limits: std::array::from_fn(|_| TokenBucket::new(100, 100, now)),
            receive_work: std::array::from_fn(|_| TokenBucket::new(1000, 1000, now)),
            pending: std::array::from_fn(|_| VecDeque::new()),
            last_kernel_drops: [0; 4],
        };
        Engine {
            media_timers: Scheduler::new(3),
            pcm_timers: Scheduler::new(1),
            pcm_lane: None,
            rx_lane: None,
            rx_timers: Scheduler::new(1),
            rtp_seed: 123,
            config,
            poll,
            ports: PortPool::new(1024, 1027, Duration::ZERO),
            sessions: vec![Some(call)],
            session_slots: [(1, 0)].into_iter().collect(),
            next_token: 2,
            ready: VecDeque::new(),
            queued: vec![0; 4],
            delayed: BTreeSet::new(),
            delayed_by_slot: vec![None; 4],
            pending_tokens: VecDeque::new(),
            pending_queued: vec![0; 4],
            dtmf_timers: BTreeSet::new(),
            playback_timers: BTreeSet::new(),
            dtmf_send_timers: BTreeSet::new(),
            dtmf_journal: EventJournal::default(),
            file_loader: None,
            stats: Stats::default(),
        }
    }

    /// 测试需要推进真实就绪循环；send_to成功不保证数据已进入接收队列或Mio就绪状态。
    /// 单包等待最多500ms，保留失败诊断，不能靠无限重试掩盖实际转发失败。
    fn receive_test_packet(engine: &mut Engine, token: usize, batch: &mut ReceiveBatch) {
        let before = engine.stats.rx_packets;
        let deadline = Instant::now() + Duration::from_millis(500);
        let mut events = Events::with_capacity(8);
        while engine.stats.rx_packets == before && Instant::now() < deadline {
            engine
                .poll
                .poll(&mut events, Some(Duration::from_millis(5)))
                .unwrap();
            engine.receive(token, batch);
            engine.flush_pending();
        }
        assert_eq!(
            engine.stats.rx_packets,
            before + 1,
            "测试包未被读取: {:?}",
            engine.stats
        );
    }

    #[test]
    /// 新增编码在真实UDP路径双向原样转发，包含G729/G726，不能仅通过描述符检查就声称可用。
    fn negotiated_codecs_relay_real_packets_and_filter_auxiliary() {
        let mut engine = isolated_engine();
        let a = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let b = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        a.set_read_timeout(Some(Duration::from_millis(500)))
            .unwrap();
        b.set_read_timeout(Some(Duration::from_millis(500)))
            .unwrap();
        let a_address = match a.local_addr().unwrap() {
            SocketAddr::V4(x) => x,
            _ => unreachable!(),
        };
        let b_address = match b.local_addr().unwrap() {
            SocketAddr::V4(x) => x,
            _ => unreachable!(),
        };
        let targets = {
            let call = engine.sessions[0].as_mut().unwrap();
            call.a.rtp = a_address;
            call.b.as_mut().unwrap().rtp = b_address;
            call.payload = 110;
            call.dtmf = Some(101);
            call.dtmf_mask = 0xffff;
            call.cn = Some(13);
            [
                call.sockets[0].local_addr().unwrap(),
                call.sockets[2].local_addr().unwrap(),
            ]
        };
        let mut batch = ReceiveBatch::default();
        for name in [
            "G722",
            "OPUS",
            "G729",
            "G726-16",
            "G726-24",
            "G726-32",
            "G726-40",
            "AAL2-G726-32",
            "L16",
        ] {
            // codec内容已在控制路径校验；热路径只用最终载荷类型，保持默认零转码开销。
            let call = engine.sessions[0].as_mut().unwrap();
            call.codec.name = name.into();
            for (sender, receiver, target, token) in
                [(&a, &b, targets[0], 5), (&b, &a, targets[1], 7)]
            {
                let mut packet = vec![0x55; 172];
                packet[0] = 0x80;
                packet[1] = 110;
                sender.send_to(&packet, target).unwrap();
                receive_test_packet(&mut engine, token, &mut batch);
                let mut received = [0; 8192];
                let len = receiver.recv(&mut received).unwrap();
                assert_eq!(&received[..len], packet.as_slice());
            }
        }
        let mut packet = vec![0; 16];
        packet[0] = 0x80;
        packet[1] = 101;
        packet[12] = 16;
        a.send_to(&packet, targets[0]).unwrap();
        receive_test_packet(&mut engine, 5, &mut batch);
        assert_eq!(engine.stats.invalid_packets, 1, "未协商的事件16必须拒绝");
        packet[12] = 5;
        a.send_to(&packet, targets[0]).unwrap();
        receive_test_packet(&mut engine, 5, &mut batch);
        let mut received = [0; 8192];
        assert_eq!(b.recv(&mut received).unwrap(), 16);
        packet.truncate(13);
        packet[1] = 13;
        packet[12] = 37;
        a.send_to(&packet, targets[0]).unwrap();
        receive_test_packet(&mut engine, 5, &mut batch);
        assert_eq!(b.recv(&mut received).unwrap(), 13);
        packet[12] = 0x80;
        a.send_to(&packet, targets[0]).unwrap();
        receive_test_packet(&mut engine, 5, &mut batch);
        assert_eq!(engine.stats.invalid_packets, 2);
    }
    #[test]
    /// Connect的编码/辅助流越界在修改会话前失败；旧PCMA Connect载荷0保持历史兼容。
    fn connect_metadata_rejects_changes_and_legacy_pcma_works() {
        let mut engine = isolated_engine();
        {
            let call = engine.sessions[0].as_mut().unwrap();
            call.payload = 8;
            call.codec = CodecSpec::legacy(8).unwrap();
        }
        let legacy:Command=serde_json::from_value(serde_json::json!({"op":"connect","session":1,"payload":0,"b":{"rtp":"127.0.0.1:30000","rtcp":"127.0.0.1:30001"},"dtmf_payload":null})).unwrap();
        engine.command(legacy).unwrap();
        assert_eq!(engine.sessions[0].as_ref().unwrap().payload, 8);
        let bad:Command=serde_json::from_value(serde_json::json!({"op":"connect","session":1,"payload":9,"codec":{"name":"G722","sample_rate":16000,"rtp_clock_rate":8000,"channels":1,"ptime_ms":20,"fmtp":""},"b":{"rtp":"127.0.0.1:31000","rtcp":"127.0.0.1:31001"},"dtmf_payload":null})).unwrap();
        assert!(engine.command(bad).is_err());
        assert_eq!(
            engine.sessions[0]
                .as_ref()
                .unwrap()
                .b
                .as_ref()
                .unwrap()
                .rtp
                .port(),
            30000
        );
    }

    #[test]
    /// 播放切换不能清掉已积压的电话事件；释放同时撤销播放和未完成按键计时项。
    fn playback_start_keeps_queued_dtmf_and_release_clears_timers() {
        let mut engine = isolated_engine();
        let now = Instant::now();
        let call = engine.sessions[0].as_mut().unwrap();
        call.cn = Some(13);
        for payload in [0, 13, 101] {
            let mut bytes = vec![0; 16];
            bytes[0] = 0x80;
            bytes[1] = payload;
            call.pending[0].push_back(PendingPacket {
                bytes,
                destination: call.a.rtp,
                deadline: now + Duration::from_secs(1),
            });
        }
        engine
            .command(Command::PlaybackStart {
                session: 1,
                playback_id: 1,
                leg: Leg::A,
                frequency_hz: 1000,
                duration_ms: 200,
            })
            .unwrap();
        let call = engine.sessions[0].as_mut().unwrap();
        assert_eq!(call.pending[0].len(), 1);
        assert_eq!(rtp::payload_type(&call.pending[0][0].bytes), Some(101));
        assert_eq!(engine.stats.playback_replaced_packets, 2);
        assert_eq!(engine.playback_timers.len(), 1);
        let when = now + Duration::from_secs(2);
        call.dtmf_due[0] = Some(when);
        engine.dtmf_timers.insert((when, 0, 0));
        engine.command(Command::Release { session: 1 }).unwrap();
        assert!(engine.playback_timers.is_empty());
        assert!(engine.dtmf_timers.is_empty());
        assert!(engine.sessions[0].is_none());
    }

    #[test]
    /// 即使磁盘结果迟到，停止、旧代际和释放后的任务都不能重新变成正在播放。
    fn file_loading_stop_release_and_generation_reject_late_results() {
        let mut engine = isolated_engine();
        let now = Instant::now();
        let deadline = now + LOAD_TIMEOUT;
        let pending = Playback::loading(1, Leg::A, "tone.wav".into(), now, deadline, 7).unwrap();
        let cancelled = Arc::clone(&pending.file.as_ref().unwrap().cancelled);
        engine.sessions[0].as_mut().unwrap().playback = Some(Box::new(pending));
        engine.playback_timers.insert((deadline, 0));
        engine
            .command(Command::PlaybackStop {
                session: 1,
                playback_id: 1,
            })
            .unwrap();
        assert!(cancelled.load(std::sync::atomic::Ordering::Relaxed));
        engine.accept_loaded_file(LoadResult {
            slot: 0,
            token: 5,
            playback_id: 1,
            result: Ok(vec![1000; 160].into()),
        });
        assert_eq!(
            engine.sessions[0]
                .as_ref()
                .unwrap()
                .playback
                .as_ref()
                .unwrap()
                .state,
            "stopped"
        );
        assert!(engine.playback_timers.is_empty());
        let pending = Playback::loading(2, Leg::A, "tone.wav".into(), now, deadline, 7).unwrap();
        engine.sessions[0].as_mut().unwrap().playback = Some(Box::new(pending));
        engine.playback_timers.insert((deadline, 0));
        engine.accept_loaded_file(LoadResult {
            slot: 0,
            token: 9,
            playback_id: 2,
            result: Ok(vec![1000; 160].into()),
        });
        engine.accept_loaded_file(LoadResult {
            slot: 0,
            token: 5,
            playback_id: 1,
            result: Ok(vec![1000; 160].into()),
        });
        assert_eq!(
            engine.sessions[0]
                .as_ref()
                .unwrap()
                .playback
                .as_ref()
                .unwrap()
                .state,
            "loading"
        );
        engine.command(Command::Release { session: 1 }).unwrap();
        engine.accept_loaded_file(LoadResult {
            slot: 0,
            token: 5,
            playback_id: 2,
            result: Ok(vec![1000; 160].into()),
        });
        assert!(engine.sessions[0].is_none());
        assert!(engine.playback_timers.is_empty());
        assert_eq!(engine.stats.tx_packets, 0);
    }

    #[test]
    /// 文件截止由媒体循环独立执行，慢磁盘不能延长任务；成功只在完整结果到达后建立分母。
    fn file_loading_deadline_and_success_have_distinct_states() {
        let mut engine = isolated_engine();
        let now = Instant::now();
        let pending = Playback::loading(1, Leg::A, "tone.wav".into(), now, now, 7).unwrap();
        engine.sessions[0].as_mut().unwrap().playback = Some(Box::new(pending));
        engine.playback_timers.insert((now, 0));
        engine.advance_playbacks(now);
        let failed = engine.sessions[0]
            .as_ref()
            .unwrap()
            .playback
            .as_ref()
            .unwrap();
        assert_eq!((failed.state, failed.total), ("failed", 0));
        assert_eq!(failed.message, "file_load_timeout");
        let deadline = Instant::now() + LOAD_TIMEOUT;
        let pending =
            Playback::loading(2, Leg::A, "tone.wav".into(), Instant::now(), deadline, 8).unwrap();
        engine.sessions[0].as_mut().unwrap().playback = Some(Box::new(pending));
        engine.playback_timers.insert((deadline, 0));
        engine.accept_loaded_file(LoadResult {
            slot: 0,
            token: 5,
            playback_id: 2,
            result: Ok(vec![1000; 161].into()),
        });
        let ready = engine.sessions[0]
            .as_ref()
            .unwrap()
            .playback
            .as_ref()
            .unwrap();
        assert_eq!((ready.state, ready.total, ready.sent), ("running", 2, 0));
        assert_eq!(engine.playback_timers.len(), 1);
    }
    #[test]
    /// 错误来源也必须受接收工作量额度约束，不能绕过合法媒体的包率限制无限消耗事件循环。
    fn wrong_source_flood_has_bounded_receive_work() {
        let mut engine = isolated_engine();
        let destination = engine.sessions[0].as_ref().unwrap().sockets[0]
            .local_addr()
            .unwrap();
        let sender = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let mut batch = ReceiveBatch::default();
        let started = Instant::now();
        for _ in 0..96 {
            for _ in 0..PACKETS_PER_TURN {
                sender.send_to(&[0x80; 12], destination).unwrap();
            }
            let _ = engine.receive(5, &mut batch);
        }
        // 接收防护允许每秒至少 1000 包和同等突发，另允许一个批次的取整余量。
        let bound =
            1000 + (started.elapsed().as_secs_f64() * 1000.0).ceil() as u64 + BATCH_SIZE as u64;
        assert!(
            engine.stats.rx_packets <= bound,
            "错误来源绕过工作量限制：实际读取 {}，本轮上界 {bound}",
            engine.stats.rx_packets
        );
        assert_eq!(engine.stats.tx_packets, 0);
    }

    #[test]
    /// 冷却不丢失边缘触发可读状态；同一槽位反复暂停只能保留一个计时项，释放会主动移除。
    fn deferred_reads_resume_once_and_release_cancels_timer() {
        let mut engine = isolated_engine();
        let now = Instant::now();
        engine.queued[0] = 5;
        for offset in 1..=100 {
            engine
                .defer_read(5, now + Duration::from_millis(offset))
                .unwrap();
            assert_eq!(engine.delayed.len(), 1);
        }
        engine
            .resume_reads(now + Duration::from_millis(99))
            .unwrap();
        assert!(engine.ready.is_empty());
        engine
            .resume_reads(now + Duration::from_millis(100))
            .unwrap();
        assert_eq!(engine.ready.iter().copied().collect::<Vec<_>>(), vec![5]);
        engine.resume_reads(now + Duration::from_secs(1)).unwrap();
        assert_eq!(engine.ready.len(), 1);
        engine.ready.clear();
        engine.defer_read(5, now + Duration::from_secs(2)).unwrap();
        engine.command(Command::Release { session: 1 }).unwrap();
        assert!(engine.delayed.is_empty());
        assert!(engine.delayed_by_slot.iter().all(Option::is_none));
        engine.resume_reads(now + Duration::from_secs(3)).unwrap();
        assert!(engine.ready.is_empty());
    }

    #[test]
    /// 某个接收方向冷却时，另一方向仍能转发合法 RTP，控制统计与释放也不等待其额度。
    fn deferred_direction_does_not_block_media_or_commands() {
        let mut engine = isolated_engine();
        let a = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        a.set_read_timeout(Some(Duration::from_secs(1))).unwrap();
        let b = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let call = engine.sessions[0].as_mut().unwrap();
        call.a.rtp = match a.local_addr().unwrap() {
            SocketAddr::V4(address) => address,
            _ => unreachable!(),
        };
        call.b.as_mut().unwrap().rtp = match b.local_addr().unwrap() {
            SocketAddr::V4(address) => address,
            _ => unreachable!(),
        };
        let destination = call.sockets[2].local_addr().unwrap();
        engine.queued[0] = 5;
        engine
            .defer_read(5, Instant::now() + Duration::from_secs(1))
            .unwrap();
        let mut packet = [0u8; 172];
        packet[0] = 0x80;
        b.send_to(&packet, destination).unwrap();
        let mut batch = ReceiveBatch::default();
        let deadline = Instant::now() + Duration::from_secs(1);
        while engine.stats.tx_packets == 0 && Instant::now() < deadline {
            let _ = engine.receive(7, &mut batch);
            std::thread::yield_now();
        }
        let mut received = [0; 172];
        assert_eq!(a.recv(&mut received).unwrap(), packet.len());
        assert_eq!(received, packet);
        match engine.command(Command::Stats).unwrap() {
            Response::Stats { stats } => assert_eq!(stats.active_calls, 1),
            _ => unreachable!(),
        }
        engine.command(Command::Release { session: 1 }).unwrap();
        assert!(engine.session_slots.is_empty());
        assert!(engine.delayed.is_empty());
    }

    #[test]
    /// 冷却必须抑制内核可读通知，否则持续到包仍会让 poll 立即返回并造成 CPU 忙轮询。
    fn cooled_socket_does_not_wake_poll() {
        let mut engine = isolated_engine();
        let destination = engine.sessions[0].as_ref().unwrap().sockets[0]
            .local_addr()
            .unwrap();
        engine.queued[0] = 5;
        engine
            .defer_read(5, Instant::now() + Duration::from_secs(1))
            .unwrap();
        let sender = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        sender.send_to(&[0; 12], destination).unwrap();
        let mut events = Events::with_capacity(8);
        let started = Instant::now();
        engine
            .poll
            .poll(&mut events, Some(Duration::from_millis(20)))
            .unwrap();
        assert!(events.is_empty(), "冷却中的 socket 仍产生可读事件");
        assert!(started.elapsed() >= Duration::from_millis(15));
        // 到期后必须主动处理已经留在内核中的包，不依赖发送方再次制造就绪边缘。
        engine
            .resume_reads(Instant::now() + Duration::from_secs(2))
            .unwrap();
        let mut batch = ReceiveBatch::default();
        let _ = engine.receive(5, &mut batch);
        assert_eq!(engine.stats.rx_packets, 1);
    }
}
