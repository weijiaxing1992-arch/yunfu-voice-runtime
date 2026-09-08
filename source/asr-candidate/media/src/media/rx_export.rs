//! A腿原生收音的有界导出队列和专用FD4；慢消费不占用RTP/下行PCM的资源。
//! 每个音频观察只入队一次；丢弃先记连续序号缺口，不把发送成功当成ASR已消费。
use anyhow::{ensure, Context, Result};
use serde::{Deserialize, Serialize};
use std::{
    collections::VecDeque,
    io,
    os::fd::FromRawFd,
    time::{Duration, Instant},
};

#[path = "rx_clock.rs"]
mod clock;

pub const HEADER: usize = 168;
pub const MAX_BODY: usize = 480;
pub const MAX_WIRE: usize = HEADER + MAX_BODY;
pub const CAPACITY: usize = 8;
pub const MAX_AGE: Duration = Duration::from_millis(100);
const STALL: Duration = Duration::from_secs(1);
pub const CAPABILITY: &str = "rx_g711_local_v2";

/// 数值为候选RXS2合同；真实观察类型由Graph映射，不能根据能量自行判断静默。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum Kind {
    Decoded = 1,
    HistoryPlc = 2,
    Missing = 3,
    ComfortNoise = 4,
    LocalExpired = 5,
    AuxiliaryExpired = 6,
    SourceBoundary = 7,
    InactiveSuspended = 8,
    ObservationFailed = 9,
    ExportGap = 10,
    End = 11,
    ExportFailed = 12,
    Auxiliary = 13,
}

/// 热路径只复制固定数组；只有body_len限定的有效前缀会被写入socket。
#[derive(Clone)]
pub struct Record {
    pub kind: Kind,
    pub flags: u16,
    pub source_generation: u64,
    pub source_segment: u64,
    pub rtp_timestamp: u64,
    pub rtp_sequence: u64,
    pub media_time_ns: u64,
    pub ssrc: u32,
    pub samples: u16,
    pub duration_ticks: u64,
    pub reason: u16,
    pub discarded_packets: u64,
    pub observed_at: Option<Instant>,
    pub arrival: Option<Instant>,
    pub deadline: Option<Instant>,
    pub boundary: u8,
    pub cn_applied: bool,
    pub body: [u8; MAX_BODY],
    pub body_len: usize,
}
impl Record {
    pub fn empty(kind: Kind) -> Self {
        Self {
            kind,
            flags: 0,
            source_generation: 0,
            source_segment: 0,
            rtp_timestamp: 0,
            rtp_sequence: 0,
            media_time_ns: 0,
            ssrc: 0,
            samples: 0,
            duration_ticks: 0,
            reason: 0,
            discarded_packets: 0,
            observed_at: None,
            arrival: None,
            deadline: None,
            boundary: 0,
            cn_applied: false,
            body: [0; MAX_BODY],
            body_len: 0,
        }
    }
    fn write(
        &self,
        session: u64,
        subscription: u64,
        sequence: u64,
        age: u32,
        now: Instant,
        out: &mut [u8; MAX_WIRE],
    ) -> usize {
        out[..HEADER].fill(0);
        out[..4].copy_from_slice(b"RXS2");
        out[4] = self.kind as u8;
        put16(out, 6, self.flags);
        for (at, value) in [
            (8, session),
            (16, subscription),
            (24, sequence),
            (32, self.source_generation),
            (40, self.source_segment),
            (48, self.rtp_timestamp),
            (56, self.rtp_sequence),
            (64, self.media_time_ns),
            (112, self.duration_ticks),
        ] {
            put64(out, at, value);
        }
        put32(out, 72, self.ssrc);
        put32(out, 76, 8000);
        put32(out, 80, 8000);
        put16(out, 84, self.body_len as u16);
        put16(out, 86, self.samples);
        put32(out, 120, age);
        put16(out, 124, self.reason);
        put64(out, 128, self.discarded_packets);
        for (at, when) in [
            (136, self.observed_at),
            (140, self.arrival),
            (144, self.deadline),
        ] {
            put32(
                out,
                at,
                when.map_or(0, |when| {
                    now.saturating_duration_since(when)
                        .as_millis()
                        .min(u128::from(u32::MAX)) as u32
                }),
            );
        }
        out[148] = self.boundary;
        out[149] = u8::from(self.cn_applied);
        put16(out, 150, clock::DOMAIN);
        out[HEADER..HEADER + self.body_len].copy_from_slice(&self.body[..self.body_len]);
        HEADER + self.body_len
    }
}
fn put16(out: &mut [u8], at: usize, v: u16) {
    out[at..at + 2].copy_from_slice(&v.to_le_bytes());
}
fn put32(out: &mut [u8], at: usize, v: u32) {
    out[at..at + 4].copy_from_slice(&v.to_le_bytes());
}
fn put64(out: &mut [u8], at: usize, v: u64) {
    out[at..at + 8].copy_from_slice(&v.to_le_bytes());
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum State {
    Active,
    Stopped,
    Failed,
}

/// 独立控制快照保留最终状态；即使数据通道失联也能查询，不等待终止数据报的确认。
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Status {
    pub subscription_id: u64,
    pub state: State,
    pub produced_events: u64,
    pub submitted_events: u64,
    pub dropped_events: u64,
    pub queued_events: u64,
    pub produced_samples: u64,
    pub submitted_samples: u64,
    pub dropped_samples: u64,
    pub oldest_age_ms: u64,
    pub error: String,
}
struct Queued {
    record: Record,
    sequence: u64,
    at: Instant,
    observation_lower_ns: u64,
    expires_ns: u64,
}
#[derive(Clone, Copy)]
struct Gap {
    first: u64,
    last: u64,
    decoded: u32,
    plc: u32,
    reasons: u16,
}

/// 固定8槽和独立缺口/终止摘要；单会话只有一个订阅，旧ID不复活。
pub struct Subscription {
    session: u64,
    status: Status,
    queue: VecDeque<Queued>,
    gap: Option<Gap>,
    terminal: Option<Kind>,
    clock: clock::ClockAnchor,
}
#[derive(Clone, Copy)]
pub enum Pending {
    Gap,
    Event,
    Terminal,
}
impl Subscription {
    pub fn new(session: u64, id: u64) -> Result<Self> {
        Ok(Self {
            session,
            status: Status {
                subscription_id: id,
                state: State::Active,
                produced_events: 0,
                submitted_events: 0,
                dropped_events: 0,
                queued_events: 0,
                produced_samples: 0,
                submitted_samples: 0,
                dropped_samples: 0,
                oldest_age_ms: 0,
                error: String::new(),
            },
            queue: VecDeque::with_capacity(CAPACITY),
            gap: None,
            terminal: None,
            clock: clock::ClockAnchor::new()?,
        })
    }
    pub fn id(&self) -> u64 {
        self.status.subscription_id
    }
    pub fn active(&self) -> bool {
        self.status.state == State::Active
    }
    pub fn pending(&self) -> bool {
        !self.queue.is_empty() || self.gap.is_some() || self.terminal.is_some()
    }
    pub fn queued_events(&self) -> usize {
        self.queue.len()
    }
    /// 编码后复核原观察年龄；检查与系统调用之间仍可能被抢占，Go必须再核验共享期限。
    pub fn still_fresh(&mut self, pending: Pending, now: Instant) -> bool {
        if matches!(pending, Pending::Event)
            && self
                .queue
                .front()
                .is_some_and(|q| now.saturating_duration_since(q.at) >= MAX_AGE)
        {
            self.expire(now);
            return false;
        }
        true
    }
    pub fn status(&self, now: Instant) -> Status {
        let mut state = self.status.clone();
        state.queued_events = self.queue.len() as u64;
        state.oldest_age_ms = self.queue.front().map_or(0, |q| {
            now.saturating_duration_since(q.at)
                .as_millis()
                .min(u128::from(u64::MAX)) as u64
        });
        state
    }
    /// 从头丢弃才能形成严格连续缺口；无法表达的异常宁可失败，不伪造连续音频。
    fn drop_front(&mut self, reason: u16) {
        let Some(q) = self.queue.pop_front() else {
            return;
        };
        self.status.dropped_events += 1;
        self.status.dropped_samples += u64::from(q.record.samples);
        let decoded = u32::from(q.record.kind == Kind::Decoded);
        let plc = u32::from(q.record.kind == Kind::HistoryPlc);
        match &mut self.gap {
            Some(g) if g.last.checked_add(1) == Some(q.sequence) => {
                g.last = q.sequence;
                g.decoded += decoded;
                g.plc += plc;
                g.reasons |= reason;
            }
            None => {
                self.gap = Some(Gap {
                    first: q.sequence,
                    last: q.sequence,
                    decoded,
                    plc,
                    reasons: reason,
                })
            }
            Some(_) => {
                self.status.state = State::Failed;
                self.status.error = "gap_metadata_discontinuity".into();
                self.terminal = Some(Kind::ExportFailed);
            }
        }
    }
    pub fn expire(&mut self, now: Instant) {
        while self
            .queue
            .front()
            .is_some_and(|q| now.saturating_duration_since(q.at) >= MAX_AGE)
        {
            self.drop_front(2);
        }
    }
    pub fn push(&mut self, record: Record, now: Instant) {
        if !self.active() {
            return;
        }
        // Graph提供的固定格式也作边界检查，防止未来编码扩展意外越过线协议上限。
        let valid = record.body_len <= MAX_BODY
            && record.flags & !0x3ff == 0
            && match record.kind {
                Kind::Decoded | Kind::HistoryPlc => record.samples == 160 && record.body_len == 320,
                Kind::ComfortNoise => record.samples == 0 && record.body_len > 0,
                Kind::ExportGap | Kind::End | Kind::ExportFailed => false,
                _ => record.samples == 0 && record.body_len == 0,
            };
        if !valid {
            self.fail("invalid_observation");
            return;
        }
        // 用真实观察时刻建立一次不可续期的跨进程期限；重试或恢复读取不能重锚。
        let observed_at = record.observed_at.unwrap_or(now);
        let Some(observation_lower_ns) = self.clock.observation_lower_ns(observed_at) else {
            self.fail("rx_clock_out_of_range");
            return;
        };
        let Some(expires_ns) = observation_lower_ns.checked_add(MAX_AGE.as_nanos() as u64) else {
            self.fail("rx_clock_out_of_range");
            return;
        };
        let Some(sequence) = self.status.produced_events.checked_add(1) else {
            self.fail("sequence_exhausted");
            return;
        };
        let Some(samples) = self
            .status
            .produced_samples
            .checked_add(u64::from(record.samples))
        else {
            self.fail("sample_count_exhausted");
            return;
        };
        self.expire(now);
        if self.queue.len() == CAPACITY {
            self.drop_front(1);
        }
        if !self.active() {
            return;
        }
        self.status.produced_events = sequence;
        self.status.produced_samples = samples;
        self.queue.push_back(Queued {
            record,
            sequence,
            at: observed_at,
            observation_lower_ns,
            expires_ns,
        });
    }
    pub fn stop(&mut self) {
        if !self.active() {
            return;
        }
        self.status.state = State::Stopped;
        while !self.queue.is_empty() {
            self.drop_front(4);
        }
        if self.status.state == State::Stopped {
            self.terminal = Some(Kind::End);
        }
    }
    pub fn fail(&mut self, reason: &str) {
        // 失败原因冻结；后续状态查询不能把首次故障替换成一次普通退订。
        if self.status.state != State::Active {
            return;
        }
        self.status.state = State::Failed;
        self.status.error = reason.into();
        while !self.queue.is_empty() {
            self.drop_front(4);
        }
        self.terminal = Some(Kind::ExportFailed);
    }
    pub fn prepare(&mut self, now: Instant, out: &mut [u8; MAX_WIRE]) -> Option<(usize, Pending)> {
        self.expire(now);
        if let Some(g) = self.gap {
            let mut r = Record::empty(Kind::ExportGap);
            r.reason = g.reasons;
            let n = r.write(self.session, self.id(), g.last, 0, now, out);
            put64(out, 88, g.first);
            put64(out, 96, g.last);
            put32(out, 104, g.decoded);
            put32(out, 108, g.plc);
            return Some((n, Pending::Gap));
        }
        if let Some(q) = self.queue.front() {
            let age = now
                .saturating_duration_since(q.at)
                .as_millis()
                .min(u128::from(u32::MAX)) as u32;
            let n = q
                .record
                .write(self.session, self.id(), q.sequence, age, now, out);
            put64(out, 152, q.observation_lower_ns);
            put64(out, 160, q.expires_ns);
            return Some((n, Pending::Event));
        }
        self.terminal.map(|kind| {
            let mut r = Record::empty(kind);
            r.reason = u16::from(kind == Kind::ExportFailed);
            (
                r.write(
                    self.session,
                    self.id(),
                    self.status.produced_events,
                    0,
                    now,
                    out,
                ),
                Pending::Terminal,
            )
        })
    }
    /// 仅完整数据报实际写入内核后调用；WouldBlock必须保留原记录和顺序。
    pub fn submitted(&mut self, pending: Pending) {
        match pending {
            Pending::Gap => self.gap = None,
            Pending::Event => {
                if let Some(q) = self.queue.pop_front() {
                    self.status.submitted_events += 1;
                    self.status.submitted_samples += u64::from(q.record.samples);
                }
            }
            Pending::Terminal => self.terminal = None,
        }
    }
}

/// 独立上行socket仅由媒体事件循环拥有。一次真实背压后全lane按1ms退避，避免N路忙等。
pub struct Lane {
    socket: std::os::unix::net::UnixDatagram,
    blocked_since: Option<Instant>,
    retry_at: Option<Instant>,
    failed: bool,
}
pub enum Send {
    Submitted,
    Blocked,
    Failed,
}
impl Lane {
    #[cfg(test)]
    pub(crate) fn from_test_socket(socket: std::os::unix::net::UnixDatagram) -> Self {
        socket.set_nonblocking(true).unwrap();
        Self {
            socket,
            blocked_since: None,
            retry_at: None,
            failed: false,
        }
    }
    pub fn from_environment() -> Result<Option<Self>> {
        let Some(value) = std::env::var_os("RUSTSWITCH_RX_FD") else {
            return Ok(None);
        };
        ensure!(value == "4", "RX descriptor must be inherited FD4");
        let mut kind: libc::c_int = 0;
        let mut length = std::mem::size_of_val(&kind) as libc::socklen_t;
        // SAFETY: 传入的整数及长度地址有效，只验证尚未接管的FD4。
        ensure!(
            // SAFETY: kind和length为有效整数地址，getsockopt只读取FD4的socket类型。
            unsafe {
                libc::getsockopt(
                    4,
                    libc::SOL_SOCKET,
                    libc::SO_TYPE,
                    (&mut kind as *mut libc::c_int).cast(),
                    &mut length,
                )
            } == 0
                && kind == libc::SOCK_DGRAM,
            "RX descriptor is not datagram"
        );
        // SAFETY: sockaddr_storage为C值，零初始化有效，长度准确。
        let mut address: libc::sockaddr_storage = unsafe { std::mem::zeroed() };
        let mut size = std::mem::size_of_val(&address) as libc::socklen_t;
        // SAFETY: 缓冲区地址/容量均有效，getsockname不会取得FD所有权。
        ensure!(
            // SAFETY: address缓冲与size容量变量有效，getsockname只填写本地socket地址。
            unsafe {
                libc::getsockname(
                    4,
                    (&mut address as *mut libc::sockaddr_storage).cast(),
                    &mut size,
                )
            } == 0
                && i32::from(address.ss_family) == libc::AF_UNIX,
            "RX descriptor is not Unix domain"
        );
        // SAFETY: FD4由父进程单独传入且未被其他Rust对象接管，由本对象析构关闭。
        let socket = unsafe { std::os::unix::net::UnixDatagram::from_raw_fd(4) };
        socket
            .peer_addr()
            .context("RX descriptor must have connected peer")?;
        socket.set_nonblocking(true)?;
        Ok(Some(Self {
            socket,
            blocked_since: None,
            retry_at: None,
            // 未支持的同机时钟域只禁用上行，仍由本对象关闭继承FD，不虚报能力。
            failed: clock::DOMAIN == 0,
        }))
    }
    pub fn available(&self) -> bool {
        !self.failed
    }
    pub fn retry_at(&self) -> Option<Instant> {
        self.retry_at
    }
    pub fn send(&mut self, bytes: &[u8], now: Instant) -> Send {
        if self.failed {
            return Send::Failed;
        }
        if self
            .blocked_since
            .is_some_and(|at| now.saturating_duration_since(at) >= STALL)
        {
            self.failed = true;
            return Send::Failed;
        }
        if self.retry_at.is_some_and(|at| now < at) {
            return Send::Blocked;
        }
        match self.socket.send(bytes) {
            Ok(n) if n == bytes.len() => {
                self.blocked_since = None;
                self.retry_at = None;
                Send::Submitted
            }
            Err(e)
                if e.kind() == io::ErrorKind::WouldBlock
                    || e.kind() == io::ErrorKind::Interrupted
                    || e.raw_os_error() == Some(libc::ENOBUFS) =>
            {
                self.blocked_since.get_or_insert(now);
                self.retry_at = Some(now + Duration::from_millis(1));
                Send::Blocked
            }
            _ => {
                self.failed = true;
                Send::Failed
            }
        }
    }
}

#[cfg(test)]
#[path = "rx_export_tests.rs"]
mod tests;
