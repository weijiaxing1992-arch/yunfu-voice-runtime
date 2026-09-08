//! 有界 telephone-event 采集与 G.711 提示音状态；所有对象由媒体事件循环独占。
//! 只把真实收到且 E 位完成的事件交给收号；缺结束包、日志覆盖均明确保留失败证据。
use crate::{admission::TokenBucket, audio::g711};
use anyhow::{ensure, Result};
use serde::{Deserialize, Serialize};
use std::{
    collections::VecDeque,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::{Duration, Instant},
};

/// 每 worker 日志固定容量；应用拉取过慢会得到 overflow，不能把缺事件当作无输入。
pub const EVENT_CAPACITY: usize = 1024;
/// 每次最多 64 项，最坏字段长度仍须适配现有 16 KiB JSON 控制行。
pub const EVENT_BATCH: usize = 64;
/// 每方向最多记住 32 个近期事件；额外保存四个 SSRC 的时间戳水位拒绝过旧重传。
const RECENT_EVENTS: usize = 32;
/// 两秒没有持续事件包就标记不完整，既不自动补结束，也不解释为音频静默。
const EVENT_TIMEOUT: Duration = Duration::from_secs(2);

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "lowercase")]
/// 来源腿与播放目的腿均使用显式枚举，不把外部地址放入播放命令。
pub enum Leg {
    A,
    B,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
/// 完成按键或可确认的不完整输入；duration_ticks 必须与 clock_rate 配对换算。
pub struct DtmfEvent {
    /// worker 代次内全局序号，由日志追加时赋值。
    pub sequence: u64,
    /// 控制面分配的媒体会话号；不是端口号。
    pub session: u64,
    /// 已通过协商来源校验的输入方向。
    pub leg: Leg,
    /// digit 或 incomplete，后者必须令业务拒绝假完整输入。
    pub kind: String,
    /// 0–9、*、#、A–D；flash 和不完整事件为空。
    pub digit: String,
    /// 原始协商事件编号，范围 0–16。
    pub event: u8,
    /// 原始两字节时长，单位由 clock_rate 决定。
    pub duration_ticks: u16,
    /// telephone-event 自己的 SDP 时钟，不固定推断为主音频时钟。
    pub clock_rate: u32,
    /// 完整 RTP 头中读取的同步源编号，不提供密码学身份认证。
    pub ssrc: u32,
    /// 事件起点的 RTP 时间戳，持续和结束包保持同值。
    pub timestamp: u32,
    /// 仅不完整事件提供明确原因，正常完成为空。
    pub reason: String,
}

#[derive(Default)]
/// 日志归属 worker，释放会话不删除已产生的证据；业务必须用会话号及 worker 代次匹配。
pub struct EventJournal {
    events: VecDeque<DtmfEvent>,
    sequence: u64,
}
impl EventJournal {
    /// 在固定容量末尾追加，覆盖时通过 oldest_seq 给调用者可检测的序号缺口。
    pub fn push(&mut self, mut event: DtmfEvent) {
        // u64 每秒百万事件仍需五十多万年才会耗尽；耗尽时保持原有证据并停止分配新序号。
        let Some(next) = self.sequence.checked_add(1) else {
            return;
        };
        self.sequence = next;
        event.sequence = next;
        if self.events.len() == EVENT_CAPACITY {
            self.events.pop_front();
        }
        self.events.push_back(event);
    }
    /// 返回本批末尾游标；不能直接跳到全局最新序号而跳过剩余批次。
    pub fn read(&self, after: u64, limit: usize) -> Result<(Vec<DtmfEvent>, u64, u64, bool)> {
        ensure!(
            (1..=EVENT_BATCH).contains(&limit),
            "DTMF limit must be 1..64"
        );
        ensure!(
            after <= self.sequence,
            "DTMF cursor exceeds worker sequence"
        );
        let oldest = self
            .events
            .front()
            .map_or(self.sequence.saturating_add(1), |e| e.sequence);
        let events: Vec<_> = self
            .events
            .iter()
            .filter(|e| e.sequence > after)
            .take(limit)
            .cloned()
            .collect();
        let next = events.last().map_or(after, |e| e.sequence);
        Ok((events, next, oldest, after < oldest - 1))
    }
}

/// 一个事件身份不含包序号，因此 RFC 4733 的多份结束重传只形成一次完成通知。
struct Observed {
    event: DtmfEvent,
    done: bool,
    last_seen: Instant,
}
/// 同 SSRC 的旧时间戳不能在近期缓存覆盖后重新变成新按键；半空间比较兼容 u32 回绕。
struct Source {
    ssrc: u32,
    timestamp: u32,
}
/// 每方向独立且惰性创建；四个来源、32 项去重和每秒 50 次新事件均有硬边界。
pub struct DtmfTracker {
    recent: VecDeque<Observed>,
    sources: Vec<Source>,
    rate: TokenBucket,
    disabled: bool,
}
impl DtmfTracker {
    /// 空媒体会话不创建本状态；只有通过来源、PT 和事件集合校验的报文才触发创建。
    pub fn new(now: Instant) -> Self {
        Self {
            recent: VecDeque::new(),
            sources: Vec::new(),
            rate: TokenBucket::new(50, 50, now),
            disabled: false,
        }
    }
    /// 记录一个四字节事件。调用方已校验 RTP 长度、来源、协商 PT/clock/events 和保留位。
    pub fn observe(
        &mut self,
        mut event: DtmfEvent,
        ended: bool,
        now: Instant,
        journal: &mut EventJournal,
    ) {
        if self.disabled {
            return;
        }
        if let Some(previous) = self.recent.iter_mut().find(|x| {
            x.event.ssrc == event.ssrc
                && x.event.timestamp == event.timestamp
                && x.event.event == event.event
        }) {
            if previous.done || event.duration_ticks < previous.event.duration_ticks {
                return;
            }
            previous.last_seen = now;
            previous.event.duration_ticks = event.duration_ticks;
            if ended && event.duration_ticks > 0 {
                previous.done = true;
                complete(&mut event);
                journal.push(event);
            } else if event.duration_ticks == u16::MAX {
                // RFC 长事件分段尚未完整重组，明确报不完整而不是把下一段误报为第二次按键。
                previous.done = true;
                incomplete(&mut event, "segmented_event_unsupported");
                journal.push(event);
            }
            return;
        }
        if let Some(source) = self.sources.iter_mut().find(|s| s.ssrc == event.ssrc) {
            let delta = event.timestamp.wrapping_sub(source.timestamp);
            if delta >= (1 << 31) {
                return;
            }
            if delta > 0 {
                source.timestamp = event.timestamp;
            }
        } else {
            if self.sources.len() == 4 {
                incomplete(&mut event, "ssrc_limit");
                journal.push(event);
                self.disabled = true;
                return;
            }
            self.sources.push(Source {
                ssrc: event.ssrc,
                timestamp: event.timestamp,
            });
        }
        if !self.rate.take(now) {
            incomplete(&mut event, "event_rate_limit");
            journal.push(event);
            self.disabled = true;
            return;
        }
        // 同源下一次按键已开始而上次没有结束证据，先报告缺口，不能只交付新数字后假称完整收号。
        for previous in &mut self.recent {
            if !previous.done
                && previous.event.ssrc == event.ssrc
                && previous.event.timestamp != event.timestamp
            {
                previous.done = true;
                let mut lost = previous.event.clone();
                incomplete(&mut lost, "next_event_before_end");
                journal.push(lost);
            }
        }
        // 已看到饱和 duration 的同一事件在 timestamp+65535 继续，是未支持的长事件下一段。
        let segmented = self.recent.iter().any(|p| {
            p.event.ssrc == event.ssrc
                && p.event.event == event.event
                && p.event.duration_ticks == u16::MAX
                && event.timestamp.wrapping_sub(p.event.timestamp) == u32::from(u16::MAX)
        });
        if self.recent.len() == RECENT_EVENTS {
            if let Some(mut previous) = self.recent.pop_front() {
                if !previous.done {
                    incomplete(&mut previous.event, "pending_event_evicted");
                    journal.push(previous.event);
                }
            }
        }
        let done = ended && event.duration_ticks > 0;
        if segmented {
            incomplete(&mut event, "segmented_event_unsupported");
        } else if done {
            complete(&mut event);
            journal.push(event.clone());
        }
        self.recent.push_back(Observed {
            event,
            done: done || segmented,
            last_seen: now,
        });
    }
    /// 只遍历此方向至多 32 项；引擎按最近到期时间调度，不扫描所有静默呼叫。
    pub fn expire(&mut self, now: Instant, journal: &mut EventJournal) {
        for pending in &mut self.recent {
            if !pending.done && now.saturating_duration_since(pending.last_seen) >= EVENT_TIMEOUT {
                pending.done = true;
                let mut event = pending.event.clone();
                incomplete(&mut event, "end_packet_not_received");
                journal.push(event);
            }
        }
    }
    /// 引擎每个有待确认事件的方向最多保留一个计时项；结束或释放会主动撤销。
    pub fn deadline(&self) -> Option<Instant> {
        self.recent
            .iter()
            .filter(|e| !e.done)
            .map(|e| e.last_seen + EVENT_TIMEOUT)
            .min()
    }
}

/// 按 RFC 4733 编号映射；16 是 flash，保留事件但不能伪装成数字字符。
fn complete(event: &mut DtmfEvent) {
    event.kind = "digit".into();
    event.digit = b"0123456789*#ABCD"
        .get(usize::from(event.event))
        .map_or_else(String::new, |v| char::from(*v).to_string());
    event.reason.clear();
}
/// 不完整输入只暴露原因，不提供可能被 IVR 误执行的数字。
fn incomplete(event: &mut DtmfEvent, reason: &str) {
    event.kind = "incomplete".into();
    event.digit.clear();
    event.reason = reason.into();
}

/// 每个 worker 仅允许少量主动播放，透传万路不因此分配逐路定时器或声音缓冲。
pub const MAX_PLAYBACKS: usize = 64;
/// 单音播放的状态随会话释放；只保留最后一项，不建立无限历史队列。
pub struct Playback {
    /// 会话内递增任务号，禁止旧 start/stop 覆盖后来任务。
    pub id: u64,
    /// 提示音的接收腿，该腿原桥主音频在播放期间被替代。
    pub leg: Leg,
    /// 单频正弦频率，单位 Hz。
    pub frequency: u16,
    /// 请求时长，单位毫秒，不随实际成功包数缩小。
    pub duration: u32,
    /// running、completed、stopped 或 failed。
    pub state: &'static str,
    /// 发送或调度失败原因，不能用完成状态掩盖。
    pub message: String,
    /// 本机 UDP 已完整接受的报文数。
    pub sent: u64,
    /// 固定 20 毫秒分包的名义总数。
    pub total: u64,
    /// 下一帧或者最后一帧结束时刻，调度表每任务最多保留一项。
    pub next: Instant,
    /// 文件任务的相对路径、取消位和已装载 PCM；终态主动释放 PCM，避免每个已结束呼叫留存大文件。
    pub file: Option<FilePlayback>,
    started: Instant,
    sequence: u16,
    timestamp: u32,
    ssrc: u32,
    /// 一个 20 毫秒包的固定存储；只在命令和每帧生成时写入，无逐包堆分配。
    packet: [u8; 172],
}
/// 只有文件任务拥有本状态；不可变 PCM 在活跃播放和有界缓存之间共享。
pub struct FilePlayback {
    pub path: String,
    pub cancelled: Arc<AtomicBool>,
    samples: Option<Arc<[i16]>>,
}
impl Playback {
    /// 首次启用本地按键时接续当前播放器；此前已发音频的身份和序号不能重置。
    pub fn rtp_origin(&self) -> (Instant, u16, u32) {
        (self.started, self.sequence, self.ssrc)
    }
    /// 仅生成 8 kHz 单声道 G.711；播放不依赖外部文件、命令、网络或动态解码插件。
    pub fn new(
        id: u64,
        leg: Leg,
        frequency: u16,
        duration: u32,
        now: Instant,
        ssrc: u32,
    ) -> Result<Self> {
        ensure!(id != 0, "playback id must be nonzero");
        ensure!(
            (200..=2000).contains(&frequency),
            "tone frequency must be 200..2000 Hz"
        );
        ensure!(
            (20..=10000).contains(&duration) && duration % 20 == 0,
            "tone duration must be 20..10000 ms in 20 ms steps"
        );
        Ok(Self {
            id,
            leg,
            frequency,
            duration,
            state: "running",
            message: String::new(),
            sent: 0,
            total: u64::from(duration / 20),
            next: now,
            file: None,
            started: now,
            sequence: 0,
            timestamp: 0,
            ssrc,
            packet: [0; 172],
        })
    }
    /// 首次返回 loading 和零分母；路径已经由加载器入队前校验，媒体循环不打开文件。
    pub fn loading(
        id: u64,
        leg: Leg,
        path: String,
        now: Instant,
        deadline: Instant,
        ssrc: u32,
    ) -> Result<Self> {
        let mut playback = Self::new(id, leg, 1000, 20, now, ssrc)?;
        playback.frequency = 0;
        playback.duration = 0;
        playback.total = 0;
        playback.state = "loading";
        playback.next = deadline;
        playback.file = Some(FilePlayback {
            path,
            cancelled: Arc::new(AtomicBool::new(false)),
            samples: None,
        });
        Ok(playback)
    }
    /// 只有仍在 loading 的匹配任务可以接纳完整 PCM；最后不足一包的样本在发送时补数字静音。
    pub fn loaded(&mut self, samples: Arc<[i16]>, now: Instant) {
        debug_assert_eq!(self.state, "loading");
        self.total = samples.len().div_ceil(160) as u64;
        self.duration = (self.total * 20) as u32;
        self.file.as_mut().unwrap().samples = Some(samples);
        self.started = now;
        self.next = now;
        self.state = "running";
    }
    /// 两种输出共用实际PCM生成器，文件不足一帧的尾部才补零；不会重复编解码量化。
    fn sample(&self, index: usize) -> i16 {
        let sample = self.sent * 160 + index as u64;
        if let Some(file) = &self.file {
            file.samples
                .as_ref()
                .and_then(|samples| samples.get(sample as usize))
                .copied()
                .unwrap_or(0)
        } else {
            let wave = (2.0 * std::f64::consts::PI * f64::from(self.frequency) * sample as f64
                / 8000.0)
                .sin();
            (wave * 8192.0).round() as i16
        }
    }
    /// 时间迟到超过一帧就失败，禁止高速追赶或缩小分母后假报完整播放。
    fn frame_ready(&mut self, now: Instant) -> bool {
        if self.state != "running" || self.sent == self.total {
            return false;
        }
        if now.saturating_duration_since(self.next) > Duration::from_millis(20) {
            self.fail("playback_schedule_late");
            return false;
        }
        true
    }
    /// 本地处理图借用调用方固定160样本缓冲，保留原始tone/WAV PCM而非G711量化后的值。
    pub fn pcm_frame(&mut self, now: Instant, samples: &mut [i16; 160]) -> bool {
        if !self.frame_ready(now) {
            return false;
        }
        for (index, sample) in samples.iter_mut().enumerate() {
            *sample = self.sample(index);
        }
        true
    }
    /// 已发布透传播放入口仍直接编码，保持原协议、包体与任务进度。
    pub fn packet(&mut self, payload: u8, alaw: bool, now: Instant) -> Option<&mut [u8]> {
        if !self.frame_ready(now) {
            return None;
        }
        self.packet[0] = 0x80;
        self.packet[1] = payload | if self.sent == 0 { 0x80 } else { 0 };
        self.packet[2..4].copy_from_slice(&self.sequence.to_be_bytes());
        self.packet[4..8].copy_from_slice(&self.timestamp.to_be_bytes());
        self.packet[8..12].copy_from_slice(&self.ssrc.to_be_bytes());
        for i in 0..160 {
            self.packet[12 + i] = g711::encode(self.sample(i), alaw);
        }
        Some(&mut self.packet)
    }
    /// 只在一次完整 UDP 发送成功后推进序列；最后一帧仍保留其 20 毫秒播放时间。
    pub fn accepted(&mut self) {
        self.sent += 1;
        self.sequence = self.sequence.wrapping_add(1);
        self.timestamp = self.timestamp.wrapping_add(160);
        self.next = self.started + Duration::from_millis(self.sent * 20);
    }
    /// 停止或错误不会冒充完成；错误文本随状态查询返回。
    pub fn fail(&mut self, message: &str) {
        self.state = "failed";
        self.message = message.into();
        self.release_file();
    }
    /// 完成和停止都归还大 PCM；保留小状态供状态查询与相同编号重试。
    pub fn finish(&mut self, state: &'static str) {
        self.state = state;
        self.release_file();
    }
    /// 取消位阻止已在途磁盘结果迟到播放；缓存自己的引用仍受独立的固定预算约束。
    fn release_file(&mut self) {
        if let Some(file) = &mut self.file {
            file.cancelled.store(true, Ordering::Relaxed);
            file.samples = None;
        }
    }
}
impl Drop for Playback {
    /// 挂断、释放或任务替换也必须取消在途加载，不能依赖之后再收到一条 stop。
    fn drop(&mut self) {
        self.release_file();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::media::protocol::{Reply, Response, MAX_CONTROL_LINE};

    /// 构造经过调用方协议校验后的元数据，纯状态测试无需网络权限。
    fn event(timestamp: u32, duration: u16) -> DtmfEvent {
        DtmfEvent {
            sequence: 0,
            session: 42,
            leg: Leg::A,
            kind: String::new(),
            digit: String::new(),
            event: 5,
            duration_ticks: duration,
            clock_rate: 48000,
            ssrc: 17,
            timestamp,
            reason: String::new(),
        }
    }

    #[test]
    /// 持续包、乱序较短结束包和三十份完成重传只形成一份真实完成事件，独立时钟原样保留。
    fn completed_events_deduplicate_and_keep_negotiated_clock() {
        let now = Instant::now();
        let mut tracker = DtmfTracker::new(now);
        let mut journal = EventJournal::default();
        tracker.observe(event(100, 2400), false, now, &mut journal);
        tracker.observe(event(100, 1200), true, now, &mut journal);
        assert!(journal.read(0, 64).unwrap().0.is_empty());
        for _ in 0..32 {
            tracker.observe(event(100, 4800), true, now, &mut journal);
        }
        let (events, next, oldest, overflow) = journal.read(0, 64).unwrap();
        assert_eq!((events.len(), next, oldest, overflow), (1, 1, 1, false));
        assert_eq!(
            (
                &events[0].kind[..],
                &events[0].digit[..],
                events[0].duration_ticks,
                events[0].clock_rate
            ),
            ("digit", "5", 4800, 48000)
        );
        assert!(tracker.deadline().is_none());
    }

    #[test]
    /// 缺结束不能自动完成；到期只产生一次不完整，迟到结束不会再次驱动 IVR。
    fn missing_end_is_incomplete_and_new_event_exposes_prior_gap() {
        let now = Instant::now();
        let mut tracker = DtmfTracker::new(now);
        let mut journal = EventJournal::default();
        tracker.observe(event(100, 100), false, now, &mut journal);
        tracker.expire(now + EVENT_TIMEOUT, &mut journal);
        tracker.observe(event(100, 200), true, now + EVENT_TIMEOUT, &mut journal);
        tracker.expire(now + EVENT_TIMEOUT * 2, &mut journal);
        let events = journal.read(0, 64).unwrap().0;
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, "incomplete");
        assert_eq!(events[0].reason, "end_packet_not_received");
        assert!(events[0].digit.is_empty());
        tracker.observe(
            event(200, 100),
            false,
            now + EVENT_TIMEOUT * 2,
            &mut journal,
        );
        tracker.observe(event(300, 100), true, now + EVENT_TIMEOUT * 2, &mut journal);
        let events = journal.read(1, 64).unwrap().0;
        assert_eq!(events[0].reason, "next_event_before_end");
        assert_eq!(events[1].digit, "5");
    }

    #[test]
    /// 固定容量覆盖及批次推进都可检测，最坏 64 项 JSON 不超控制行上限。
    fn journal_overflow_cursor_and_wire_size_are_bounded() {
        let mut journal = EventJournal::default();
        for _ in 0..1100 {
            let mut e = event(u32::MAX, u16::MAX);
            e.session = u64::MAX;
            e.ssrc = u32::MAX;
            incomplete(&mut e, "segmented_event_unsupported");
            journal.push(e);
        }
        assert_eq!(journal.events.len(), EVENT_CAPACITY);
        let (mut events, next_seq, oldest_seq, overflow) = journal.read(0, 64).unwrap();
        assert_eq!((next_seq, oldest_seq, overflow), (140, 77, true));
        for e in &mut events {
            e.sequence = u64::MAX;
        }
        let reply = Reply {
            id: u64::MAX,
            ok: true,
            result: Response::DtmfEvents {
                events,
                next_seq: u64::MAX,
                oldest_seq: u64::MAX,
                overflow: true,
            },
        };
        assert!(serde_json::to_vec(&reply).unwrap().len() + 1 < MAX_CONTROL_LINE);
        assert_eq!(journal.read(140, 64).unwrap().0[0].sequence, 141);
        assert!(journal.read(1101, 64).is_err());
        assert!(journal.read(0, 65).is_err());
    }

    #[test]
    /// 去重缓存覆盖后旧时间戳仍不会变成新按键；时间戳回绕及有限合法 SSRC 更换保持可用。
    fn replay_watermark_wrap_and_source_limit() {
        let now = Instant::now();
        let mut tracker = DtmfTracker::new(now);
        let mut journal = EventJournal::default();
        for i in 0..40 {
            tracker.observe(
                event((u32::MAX - 20).wrapping_add(i), 100),
                true,
                now + Duration::from_millis(u64::from(i) * 40),
                &mut journal,
            );
        }
        let count = journal.sequence;
        tracker.observe(
            event(u32::MAX - 20, 100),
            true,
            now + Duration::from_secs(2),
            &mut journal,
        );
        assert_eq!(journal.sequence, count);
        assert_eq!(tracker.recent.len(), RECENT_EVENTS);
        for ssrc in 18..22 {
            let mut e = event(10, 100);
            e.ssrc = ssrc;
            tracker.observe(e, true, now + Duration::from_secs(2), &mut journal);
        }
        assert_eq!(tracker.sources.len(), 4);
        assert!(tracker.disabled);
        assert_eq!(journal.events.back().unwrap().reason, "ssrc_limit");
    }

    #[test]
    /// 高频新事件触发有界失败，不生成无限完成日志，桥 RTP 本身仍由 worker 独立转发。
    fn excessive_new_events_fail_collection_with_bounded_state() {
        let now = Instant::now();
        let mut tracker = DtmfTracker::new(now);
        let mut journal = EventJournal::default();
        for t in 0..1000 {
            tracker.observe(event(t, 100), true, now, &mut journal);
        }
        assert_eq!(journal.sequence, 51);
        assert_eq!(journal.events.back().unwrap().reason, "event_rate_limit");
        assert_eq!(tracker.recent.len(), RECENT_EVENTS);
    }

    #[test]
    /// 已明确分段的长按不应被报告成两个独立数字。
    fn segmented_long_event_does_not_invent_extra_digits() {
        let now = Instant::now();
        let mut tracker = DtmfTracker::new(now);
        let mut journal = EventJournal::default();
        tracker.observe(event(10, u16::MAX), false, now, &mut journal);
        tracker.observe(
            event(10 + u32::from(u16::MAX), 100),
            true,
            now,
            &mut journal,
        );
        assert!(journal.events.iter().all(|e| e.kind == "incomplete"));
        assert_eq!(journal.events.len(), 1);
    }

    #[test]
    /// 真正的 G.711 字节具有期望正弦幅度和时钟递增；两种压扩格式分别检查。
    fn generated_tone_has_g711_wave_and_rtp_clock() {
        for alaw in [false, true] {
            let now = Instant::now();
            let mut p = Playback::new(1, Leg::A, 1000, 40, now, 44).unwrap();
            let first = p
                .packet(if alaw { 8 } else { 0 }, alaw, now)
                .unwrap()
                .to_vec();
            assert_eq!(&first[..2], &[0x80, if alaw { 0x88 } else { 0x80 }]);
            assert!((i32::from(g711::decode(first[14], alaw)) - 8192).abs() < 400);
            assert!((i32::from(g711::decode(first[18], alaw)) + 8192).abs() < 400);
            p.accepted();
            let second = p
                .packet(
                    if alaw { 8 } else { 0 },
                    alaw,
                    now + Duration::from_millis(20),
                )
                .unwrap();
            assert_eq!(&second[2..4], &1u16.to_be_bytes());
            assert_eq!(&second[4..8], &160u32.to_be_bytes());
            assert_eq!(&first[12..], &second[12..]);
        }
    }

    #[test]
    /// 超出一帧的调度延误保留失败和原分母，不能追赶或返回 completed。
    fn playback_rejects_limits_and_fails_lateness_without_catchup() {
        let now = Instant::now();
        assert!(Playback::new(0, Leg::A, 1000, 20, now, 1).is_err());
        assert!(Playback::new(1, Leg::A, 1000, 21, now, 1).is_err());
        let mut p = Playback::new(1, Leg::A, 1000, 100, now, 1).unwrap();
        assert!(p
            .packet(0, false, now + Duration::from_millis(21))
            .is_none());
        assert_eq!((p.state, p.sent, p.total), ("failed", 0, 5));
    }

    #[test]
    /// 文件尾部不足一包只补编码静音；终态立即归还 PCM，不能随已结束通话无限留存文件。
    fn file_playback_pads_tail_and_releases_pcm_on_terminal_state() {
        for alaw in [false, true] {
            let now = Instant::now();
            let samples: Arc<[i16]> = vec![1000; 161].into();
            let mut p = Playback::loading(
                1,
                Leg::A,
                "voice.wav".into(),
                now,
                now + Duration::from_secs(2),
                7,
            )
            .unwrap();
            assert_eq!((p.state, p.total), ("loading", 0));
            p.loaded(Arc::clone(&samples), now);
            assert_eq!(p.total, 2);
            p.packet(if alaw { 8 } else { 0 }, alaw, now).unwrap();
            p.accepted();
            let packet = p
                .packet(
                    if alaw { 8 } else { 0 },
                    alaw,
                    now + Duration::from_millis(20),
                )
                .unwrap();
            assert_eq!(packet[12], g711::encode(1000, alaw));
            assert!(packet[13..]
                .iter()
                .all(|&byte| byte == g711::encode(0, alaw)));
            p.finish("stopped");
            assert_eq!(Arc::strong_count(&samples), 1);
            assert!(p.file.as_ref().unwrap().cancelled.load(Ordering::Relaxed));
        }
    }
}
