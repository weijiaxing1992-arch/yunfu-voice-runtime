//! 解码前的固定容量编码包缓冲：按音频时间戳排程，辅助包不制造音频序号空洞。
use super::{
    rtp::PacketView,
    rtp_state::{RxOutcome, RxRtpState},
};
use anyhow::{ensure, Result};
use std::time::{Duration, Instant};

pub const MAX_SLOTS: usize = 16;
pub const MAX_PAYLOAD_BYTES: usize = 480;
#[derive(Clone, Copy, Debug)]
pub struct JitterConfig {
    pub clock_rate: u32,
    pub packet_ticks: u32,
    pub audio_payload: u8,
    pub dtmf_payload: Option<u8>,
    pub cn_payload: Option<u8>,
    pub max_payload_bytes: usize,
    pub target_delay: Duration,
    pub max_delay: Duration,
    pub capacity: usize,
}
#[derive(Clone, Copy, Debug, Default)]
pub struct JitterStats {
    pub accepted: u64,
    pub played: u64,
    pub lost: u64,
    pub late: u64,
    pub reordered: u64,
    pub duplicates: u64,
    pub overflow: u64,
    pub expired: u64,
    pub invalid: u64,
    pub resets: u64,
    pub aux_packets: u64,
    pub aux_timeouts: u64,
    pub inactive_suspends: u64,
    pub aux_expired: u64,
    pub queue_reset_drops: u64,
    pub depth: usize,
    pub peak_depth: usize,
    pub last_delay_ns: u64,
    pub peak_delay_ns: u64,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PushOutcome {
    Accepted,
    Reordered,
    Duplicate,
    Late,
    Overflow,
    Probation,
    Reset,
    Invalid,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Playout {
    Audio {
        timestamp: u64,
        ssrc: u32,
        len: usize,
        marker: bool,
        new_segment: bool,
    },
    Missing {
        timestamp: u64,
        ssrc: u32,
    },
    Aux {
        payload_type: u8,
        timestamp: u32,
        ssrc: u32,
        len: usize,
        marker: bool,
    },
    Expired {
        skipped: u64,
    },
}
#[derive(Clone, Copy)]
struct Slot {
    audio: bool,
    sequence: u64,
    segment: u64,
    timestamp: u64,
    raw_timestamp: u32,
    ssrc: u32,
    payload_type: u8,
    marker: bool,
    len: usize,
    arrival: Instant,
    due: Instant,
}

/// 只读观察元数据；不修改原Playout返回值或排程规则，也不按音频格点推算包序。
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct JitterSourcePosition {
    pub ssrc: Option<u32>,
    pub timestamp: Option<u64>,
    pub sequence: Option<u64>,
    pub generation: u64,
    pub segment: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum JitterObservationKind {
    Audio,
    Missing,
    ComfortNoise,
    AudioExpired,
    Auxiliary,
    AuxiliaryExpired,
    StaleComfortNoise,
    ToneTimeout,
}

#[derive(Clone, Copy, Debug)]
pub struct JitterObservation {
    pub kind: JitterObservationKind,
    pub source: JitterSourcePosition,
    pub arrival: Option<Instant>,
    pub deadline: Instant,
    pub new_segment: bool,
    pub suspended_after: bool,
    pub cn_applied: bool,
    pub transmission_expired: bool,
    pub audio_frames: Option<u64>,
    pub payload_len: usize,
    pub marker: bool,
}
#[derive(Clone, Copy)]
struct ToneHold {
    start: u64,
    through: u64,
    ended: bool,
    timeout: Instant,
    seen_events: u32,
    ended_events: u32,
    durations: [u16; 17],
    timed_out: bool,
}
#[derive(Clone, Copy)]
enum Action {
    Audio(usize, u64, Instant),
    Missing(u64, Instant),
    Aux(usize, Instant),
    ToneTimeout(Instant),
}
impl Action {
    fn due(self) -> Instant {
        match self {
            Self::Audio(_, _, at)
            | Self::Missing(_, at)
            | Self::Aux(_, at)
            | Self::ToneTimeout(at) => at,
        }
    }
}

/// 元数据与负载各只在建呼时分配一次；双向会话必须分别持有实例，不能共享序号或锚点。
pub struct JitterBuffer {
    config: JitterConfig,
    period: Duration,
    max_ticks: u64,
    rx: RxRtpState,
    slots: Box<[Option<Slot>]>,
    payloads: Box<[u8]>,
    anchor: Option<(u64, Instant)>,
    cursor: Option<u64>,
    last_played: Option<u64>,
    cn_start: Option<u64>,
    tone: Option<ToneHold>,
    inactive: bool,
    current_segment: u64,
    played_segment: Option<u64>,
    resume_pending: bool,
    last_received: Option<JitterSourcePosition>,
    stats: JitterStats,
}
impl JitterBuffer {
    pub fn new(config: JitterConfig) -> Result<Self> {
        ensure!(
            (1000..=192000).contains(&config.clock_rate) && config.packet_ticks > 0,
            "invalid jitter RTP clock/frame"
        );
        let ns = u64::from(config.packet_ticks) * 1_000_000_000 / u64::from(config.clock_rate);
        ensure!(
            (1_000_000..=120_000_000).contains(&ns),
            "jitter packet duration out of range"
        );
        let period = Duration::from_nanos(ns);
        ensure!(
            config.target_delay >= period
                && config.target_delay <= config.max_delay
                && config.max_delay <= Duration::from_millis(120),
            "invalid jitter delay bounds"
        );
        let required = config.max_delay.as_nanos().div_ceil(period.as_nanos()) + 1;
        ensure!(
            config.capacity >= required as usize && config.capacity <= MAX_SLOTS,
            "jitter slot capacity cannot cover configured delay"
        );
        ensure!(
            (1..=MAX_PAYLOAD_BYTES).contains(&config.max_payload_bytes),
            "invalid jitter payload bound"
        );
        validate_payloads(config.audio_payload, config.dtmf_payload, config.cn_payload)?;
        Ok(Self {
            config,
            period,
            max_ticks: (config.max_delay.as_nanos() * u128::from(config.clock_rate) / 1_000_000_000)
                as u64,
            rx: RxRtpState::new(config.clock_rate)?,
            slots: vec![None; config.capacity].into_boxed_slice(),
            payloads: vec![0; config.capacity * config.max_payload_bytes].into_boxed_slice(),
            anchor: None,
            cursor: None,
            last_played: None,
            cn_start: None,
            tone: None,
            inactive: false,
            current_segment: 0,
            played_segment: None,
            resume_pending: false,
            last_received: None,
            stats: JitterStats::default(),
        })
    }
    pub fn stats(&self) -> &JitterStats {
        &self.stats
    }
    pub fn rx(&self) -> &RxRtpState {
        &self.rx
    }
    pub fn rx_mut(&mut self) -> &mut RxRtpState {
        &mut self.rx
    }
    /// 最近一次通过RX去重/源重启确认的真实来源，不代表该包一定进入了音频缓冲。
    pub fn last_received(&self) -> Option<JitterSourcePosition> {
        self.last_received
    }
    pub fn capacity(&self) -> usize {
        self.config.capacity
    }
    pub fn storage_bytes(&self) -> usize {
        self.payloads.len() + self.slots.len() * std::mem::size_of::<Option<Slot>>()
    }
    /// 协商更新只移除不再允许的辅助包；不重置音频历史，不进行热路径分配。
    pub fn set_aux_payloads(&mut self, dtmf: Option<u8>, cn: Option<u8>) -> Result<()> {
        validate_payloads(self.config.audio_payload, dtmf, cn)?;
        for slot in &mut self.slots {
            if slot.is_some_and(|s| {
                !s.audio
                    && !(Some(s.payload_type) == self.config.dtmf_payload
                        && self.config.dtmf_payload == dtmf)
                    && !(Some(s.payload_type) == self.config.cn_payload
                        && self.config.cn_payload == cn)
            }) {
                *slot = None;
                self.stats.depth -= 1;
                self.stats.queue_reset_drops += 1;
            }
        }
        if self.config.cn_payload != cn {
            self.cn_start = None;
        }
        if self.config.dtmf_payload != dtmf {
            self.tone = None;
        }
        self.config.cn_payload = cn;
        self.config.dtmf_payload = dtmf;
        Ok(())
    }
    /// 清除播放队列与时间线但保留实际RX累计统计；已确认源重启也走同一路径。
    pub fn clear(&mut self) {
        self.stats.queue_reset_drops += self.stats.depth as u64;
        self.stats.depth = 0;
        self.slots.fill(None);
        self.anchor = None;
        self.cursor = None;
        self.last_played = None;
        self.cn_start = None;
        self.tone = None;
        self.inactive = false;
        self.current_segment = 0;
        self.played_segment = None;
        self.resume_pending = false;
        self.last_received = None;
    }
    fn deadline(&self, ts: u64) -> Instant {
        let (anchor, at) = self.anchor.expect("audio anchor exists");
        let ticks = ts.abs_diff(anchor);
        let delay = Duration::from_nanos(
            (u128::from(ticks) * 1_000_000_000 / u128::from(self.config.clock_rate))
                .min(u128::from(u64::MAX)) as u64,
        );
        if ts >= anchor {
            at + delay
        } else {
            at.checked_sub(delay).unwrap_or(at)
        }
    }
    pub fn push(&mut self, packet: PacketView<'_>, arrival: Instant) -> PushOutcome {
        let audio = packet.payload_type == self.config.audio_payload;
        let dtmf = Some(packet.payload_type) == self.config.dtmf_payload;
        let cn = Some(packet.payload_type) == self.config.cn_payload;
        if packet.payload.is_empty()
            || packet.payload.len() > self.config.max_payload_bytes
            || (!audio && !dtmf && !cn)
            || (dtmf
                && (packet.payload.len() % 4 != 0
                    || packet
                        .payload
                        .chunks_exact(4)
                        .any(|e| e[0] > 16 || e[1] & 0x40 != 0)))
        {
            self.stats.invalid += 1;
            return PushOutcome::Invalid;
        }
        let (sequence, timestamp, reordered, reset) = match self.rx.observe(packet, audio, arrival)
        {
            RxOutcome::Accepted {
                sequence,
                timestamp,
                reordered,
                reset,
            } => (sequence, timestamp, reordered, reset.is_some()),
            RxOutcome::Duplicate => {
                self.stats.duplicates += 1;
                return PushOutcome::Duplicate;
            }
            RxOutcome::TooOld => {
                self.stats.late += 1;
                return PushOutcome::Late;
            }
            RxOutcome::Probation => return PushOutcome::Probation,
        };
        if reset {
            self.clear();
            self.stats.resets += 1;
        }
        if reordered {
            self.stats.reordered += 1;
        }
        let ts = timestamp.unwrap_or_else(|| self.rx.extend_timestamp(packet.timestamp));
        self.last_received = Some(JitterSourcePosition {
            ssrc: Some(packet.ssrc),
            timestamp: Some(ts),
            sequence: Some(sequence),
            generation: self.stats.resets.saturating_add(1),
            segment: self.current_segment,
        });
        // 先保证整包能入队，再修改播放锚点；满队列不能留下只有状态而没有首帧的假新段。
        let Some(index) = self.slots.iter().position(Option::is_none) else {
            self.stats.overflow += 1;
            return PushOutcome::Overflow;
        };
        let due = if audio {
            if self
                .slots
                .iter()
                .flatten()
                .any(|s| s.audio && s.timestamp == ts)
            {
                self.stats.duplicates += 1;
                return PushOutcome::Duplicate;
            }
            if let Some((anchor, _)) = self.anchor {
                let new_segment = !self.resume_pending
                    && !reordered
                    && self.after_silence(ts)
                    && (self.cursor.is_none_or(|cursor| ts >= cursor)
                        || (packet.marker && self.last_played.is_some_and(|last| ts > last)));
                if new_segment {
                    // 新talkspurt的起始相位可以改变；每包样本数约束仍由协商codec保持。
                    // 队列内旧段真实音频保留原deadline，边界只在新段实际出队时交给图。
                    self.anchor = Some((ts, arrival + self.config.target_delay));
                    if !self
                        .slots
                        .iter()
                        .flatten()
                        .any(|s| s.audio && s.timestamp < ts)
                    {
                        self.cursor = Some(ts);
                    }
                    self.inactive = false;
                    self.current_segment = self.current_segment.wrapping_add(1);
                    self.resume_pending = true;
                } else {
                    // 连续活跃段仍拒绝不匹配帧格点的时间戳，不能把畸形包当作新段。
                    if ts.abs_diff(anchor) % u64::from(self.config.packet_ticks) != 0 {
                        self.stats.invalid += 1;
                        return PushOutcome::Invalid;
                    }
                    if self.cursor.is_some_and(|cursor| ts < cursor) {
                        if self.last_played.is_some() || self.deadline(ts) < arrival {
                            self.stats.late += 1;
                            return PushOutcome::Late;
                        }
                        self.cursor = Some(ts);
                    }
                }
            } else {
                self.anchor = Some((ts, arrival + self.config.target_delay));
                self.cursor = Some(ts);
                self.current_segment = self.current_segment.wrapping_add(1);
            }
            let due = self.deadline(ts);
            if due < arrival {
                self.stats.late += 1;
                return PushOutcome::Late;
            }
            if due.saturating_duration_since(arrival) > self.config.max_delay {
                self.stats.overflow += 1;
                return PushOutcome::Overflow;
            }
            due
        } else {
            arrival + self.config.target_delay
        };
        let start = index * self.config.max_payload_bytes;
        self.payloads[start..start + packet.payload.len()].copy_from_slice(packet.payload);
        self.slots[index] = Some(Slot {
            audio,
            sequence,
            segment: self.current_segment,
            timestamp: ts,
            raw_timestamp: packet.timestamp,
            ssrc: packet.ssrc,
            payload_type: packet.payload_type,
            marker: packet.marker,
            len: packet.payload.len(),
            arrival,
            due,
        });
        self.last_received.as_mut().unwrap().segment = self.current_segment;
        self.stats.accepted += 1;
        self.stats.depth += 1;
        self.stats.peak_depth = self.stats.peak_depth.max(self.stats.depth);
        if reset {
            PushOutcome::Reset
        } else if reordered {
            PushOutcome::Reordered
        } else {
            PushOutcome::Accepted
        }
    }
    /// 明确暂停才允许改变帧相位；活跃音频间混入DTMF不构成新语音段。
    fn after_silence(&self, ts: u64) -> bool {
        let after_last = |start| self.last_played.is_none_or(|last| last < start);
        self.inactive
            || self.cn_start.is_some_and(|start| ts > start)
            || self
                .tone
                .is_some_and(|tone| tone.ended && ts >= tone.through && after_last(tone.start))
            || self.slots.iter().enumerate().any(|(index, slot)| {
                slot.is_some_and(|s| {
                    if s.audio || s.timestamp > ts || !after_last(s.timestamp) {
                        return false;
                    }
                    if Some(s.payload_type) == self.config.cn_payload {
                        return ts > s.timestamp;
                    }
                    if Some(s.payload_type) == self.config.dtmf_payload {
                        let begin = index * self.config.max_payload_bytes;
                        return self.payloads[begin..begin + s.len]
                            .chunks_exact(4)
                            .all(|e| {
                                e[1] & 0x80 != 0
                                    && ts
                                        >= s.timestamp + u64::from(u16::from_be_bytes([e[2], e[3]]))
                            });
                    }
                    false
                })
            })
    }
    fn earliest_audio(&self, cursor: u64) -> Option<(usize, Slot)> {
        self.slots
            .iter()
            .enumerate()
            .filter_map(|(i, s)| {
                s.filter(|s| s.audio && s.timestamp >= cursor)
                    .map(|s| (i, s))
            })
            .min_by_key(|(_, s)| s.timestamp)
    }
    fn suppressed(&self, ts: u64) -> bool {
        self.cn_start.is_some_and(|start|ts>=start) || self.tone.is_some_and(|t|!t.timed_out&&ts>=t.start&&(!t.ended||ts<t.through))
            // 已收到的SID/事件足以证明此时间格并非音频丢包；不等辅助转发定时才抑制PLC。
            || self.slots.iter().enumerate().any(|(index,slot)|slot.is_some_and(|s| {
                if s.audio || s.timestamp>ts {return false;}
                if Some(s.payload_type)==self.config.cn_payload {return self.last_played.is_none_or(|last|s.timestamp>last);}
                if Some(s.payload_type)==self.config.dtmf_payload {
                    let start=index*self.config.max_payload_bytes;
                    return self.payloads[start..start+s.len].chunks_exact(4).any(|e|e[1]&0x80==0 || ts<s.timestamp+u64::from(u16::from_be_bytes([e[2],e[3]])));
                }
                false
            }))
    }
    fn action(&self) -> Option<Action> {
        let aux = self
            .slots
            .iter()
            .enumerate()
            .filter_map(|(i, s)| s.filter(|s| !s.audio).map(|s| Action::Aux(i, s.due)))
            .min_by_key(|a| a.due());
        let audio = self.cursor.and_then(|ts| {
            let queued = self.earliest_audio(ts);
            if let Some((i, s)) = queued {
                if s.timestamp == ts {
                    return Some(Action::Audio(i, ts, s.due));
                }
            }
            let exhausted = self
                .last_played
                .is_some_and(|last| ts > last.saturating_add(self.max_ticks));
            if self.suppressed(ts) || exhausted {
                return queued.map(|(i, s)| Action::Audio(i, s.timestamp, s.due));
            }
            self.last_played
                .map(|_| Action::Missing(ts, self.deadline(ts)))
        });
        let timeout = self
            .tone
            .filter(|t| !t.ended && !t.timed_out)
            .map(|t| Action::ToneTimeout(t.timeout));
        [aux, audio, timeout]
            .into_iter()
            .flatten()
            .min_by_key(|a| a.due())
    }
    pub fn next_deadline(&self) -> Option<Instant> {
        self.action().map(Action::due)
    }
    fn remove(&mut self, index: usize) -> Slot {
        self.stats.depth -= 1;
        self.slots[index].take().expect("selected jitter slot")
    }
    /// 只有订阅者走此包装层；消费仍调用原pop_due一次，不新建定时器或重新安排过期包。
    /// 辅助过期和音频过期明确分类，CN的状态变化不会因转发过期而消失。
    pub fn pop_due_observed(
        &mut self,
        now: Instant,
        out: &mut [u8],
    ) -> (Option<Playout>, Option<JitterObservation>) {
        let Some(action) = self.action() else {
            return (None, None);
        };
        if action.due() > now || out.len() < self.config.max_payload_bytes {
            return (None, None);
        }
        let slot = match action {
            Action::Audio(index, _, _) | Action::Aux(index, _) => self.slots[index],
            _ => None,
        };
        let cn = slot.is_some_and(|s| Some(s.payload_type) == self.config.cn_payload);
        let stale_cn = cn
            && slot.is_some_and(|s| {
                self.last_played.is_some_and(|last| s.timestamp <= last)
                    || self
                        .slots
                        .iter()
                        .flatten()
                        .any(|audio| audio.audio && audio.timestamp == s.timestamp)
            });
        let was_inactive = self.inactive;
        let source = slot.map_or_else(
            || JitterSourcePosition {
                ssrc: self.rx.ssrc(),
                timestamp: match action {
                    Action::Missing(ts, _) => Some(ts),
                    Action::ToneTimeout(_) => self.cursor.or(self.tone.map(|tone| tone.through)),
                    _ => None,
                },
                sequence: None,
                generation: self
                    .stats
                    .resets
                    .saturating_add(u64::from(self.rx.ssrc().is_some())),
                segment: self.played_segment.unwrap_or(self.current_segment),
            },
            |s| JitterSourcePosition {
                ssrc: Some(s.ssrc),
                timestamp: Some(s.timestamp),
                sequence: Some(s.sequence),
                generation: self.stats.resets.saturating_add(1),
                segment: s.segment,
            },
        );
        let event = self.pop_due(now, out);
        let Some(event) = event else {
            return (None, None);
        };
        let (kind, audio_frames, new_segment) = match event {
            Playout::Audio { new_segment, .. } => {
                (JitterObservationKind::Audio, Some(1), new_segment)
            }
            Playout::Missing { .. } => (JitterObservationKind::Missing, Some(1), false),
            Playout::Aux { .. } if cn => (JitterObservationKind::ComfortNoise, None, false),
            Playout::Aux { .. } => (JitterObservationKind::Auxiliary, None, false),
            Playout::Expired { skipped } if skipped > 0 => {
                (JitterObservationKind::AudioExpired, Some(skipped), false)
            }
            Playout::Expired { .. } if matches!(action, Action::ToneTimeout(_)) => {
                (JitterObservationKind::ToneTimeout, None, false)
            }
            Playout::Expired { .. } if stale_cn => {
                (JitterObservationKind::StaleComfortNoise, None, false)
            }
            Playout::Expired { .. } if cn => (JitterObservationKind::ComfortNoise, None, false),
            Playout::Expired { .. } => (JitterObservationKind::AuxiliaryExpired, None, false),
        };
        let observation = JitterObservation {
            kind,
            source,
            arrival: slot.map(|s| s.arrival),
            deadline: action.due(),
            new_segment,
            suspended_after: !was_inactive && self.inactive,
            cn_applied: cn && !stale_cn,
            transmission_expired: matches!(event, Playout::Expired { .. }),
            audio_frames,
            payload_len: slot.map_or(0, |s| s.len),
            marker: slot.is_some_and(|s| s.marker),
        };
        (Some(event), Some(observation))
    }
    /// 每次返回至多一帧；本地定时迟到丢弃已过期时间格，不把本机调度滞后计为网络丢失。
    pub fn pop_due(&mut self, now: Instant, out: &mut [u8]) -> Option<Playout> {
        // 缓冲不足时不得消费任何包，调用方必须提供配置的最大负载长度。
        if out.len() < self.config.max_payload_bytes {
            return None;
        }
        let action = self.action()?;
        if action.due() > now {
            return None;
        }
        match action {
            Action::ToneTimeout(_) => {
                // 结束活动等待，但保留单组事件水位；迟到旧包不能在超时后再次倒退起点/时长。
                self.tone.as_mut().unwrap().timed_out = true;
                self.stats.aux_timeouts += 1;
                if let Some(ts) = self.cursor {
                    let due = self.deadline(ts);
                    let skipped =
                        now.saturating_duration_since(due).as_nanos() / self.period.as_nanos();
                    self.cursor = Some(
                        ts.saturating_add(skipped as u64 * u64::from(self.config.packet_ticks)),
                    );
                }
                self.update_inactive();
                Some(Playout::Expired { skipped: 0 })
            }
            Action::Aux(index, due) => {
                let s = self.remove(index);
                // 同时间位的真实语音也优先于SID。检查必须在出队时重做：CN排队期间可能已被语音越过。
                // 不能再向下游返回Aux，否则旧静默信号会清掉刚解码的语音和PLC历史。
                if Some(s.payload_type) == self.config.cn_payload
                    && (self.last_played.is_some_and(|last| s.timestamp <= last)
                        || self
                            .slots
                            .iter()
                            .flatten()
                            .any(|audio| audio.audio && audio.timestamp == s.timestamp))
                {
                    self.stats.late += 1;
                    self.stats.aux_expired += 1;
                    return Some(Playout::Expired { skipped: 0 });
                }
                let start = index * self.config.max_payload_bytes;
                out[..s.len].copy_from_slice(&self.payloads[start..start + s.len]);
                if Some(s.payload_type) == self.config.cn_payload {
                    self.cn_start = Some(s.timestamp);
                } else if Some(s.payload_type) == self.config.dtmf_payload {
                    self.observe_tone(s.timestamp, due, &out[..s.len]);
                }
                // 迟到一帧就放弃辅助转发，不能集中补发结束副本；已收到的CN/DTMF语义仍有效且有界。
                if now.saturating_duration_since(due) >= self.period {
                    self.stats.aux_expired += 1;
                    return Some(Playout::Expired { skipped: 0 });
                }
                self.stats.aux_packets += 1;
                self.note_delay(now, s.arrival);
                Some(Playout::Aux {
                    payload_type: s.payload_type,
                    timestamp: s.raw_timestamp,
                    ssrc: s.ssrc,
                    len: s.len,
                    marker: s.marker,
                })
            }
            Action::Audio(_, ts, due) | Action::Missing(ts, due)
                if now.saturating_duration_since(due) >= self.period =>
            {
                let skipped =
                    (now.saturating_duration_since(due).as_nanos() / self.period.as_nanos()) as u64;
                let cursor =
                    ts.saturating_add(skipped.saturating_mul(u64::from(self.config.packet_ticks)));
                for slot in &mut self.slots {
                    if slot.is_some_and(|s| s.audio && s.timestamp < cursor) {
                        *slot = None;
                        self.stats.depth -= 1;
                    }
                }
                self.cursor = Some(cursor);
                self.stats.expired += skipped;
                // 本机调度跳过时间格不是远端静默证据；内核中仍可能积压连续语音。
                // 保留原anchor，之后drain到的过期包必须Late，不能重新排到arrival+target。
                Some(Playout::Expired { skipped })
            }
            Action::Audio(index, ts, _) => {
                let s = self.remove(index);
                let start = index * self.config.max_payload_bytes;
                out[..s.len].copy_from_slice(&self.payloads[start..start + s.len]);
                self.cursor = Some(ts + u64::from(self.config.packet_ticks));
                self.last_played = Some(ts);
                self.inactive = false;
                if self.cn_start.is_some_and(|start| ts >= start) {
                    self.cn_start = None;
                }
                // 段编号随槽保存，旧队列先播放、首新帧过期等情形也不会提前消费或丢失边界。
                let new_segment = self.played_segment != Some(s.segment);
                self.played_segment = Some(s.segment);
                if s.segment == self.current_segment {
                    self.resume_pending = false;
                }
                self.stats.played += 1;
                self.note_delay(now, s.arrival);
                Some(Playout::Audio {
                    new_segment,
                    timestamp: ts,
                    ssrc: s.ssrc,
                    len: s.len,
                    marker: s.marker,
                })
            }
            Action::Missing(ts, _) => {
                self.cursor = Some(ts + u64::from(self.config.packet_ticks));
                self.stats.lost += 1;
                self.update_inactive();
                Some(Playout::Missing {
                    timestamp: ts,
                    ssrc: self.rx.ssrc().unwrap_or(0),
                })
            }
        }
    }
    /// 只保留最新事件起点，旧结束副本仍可转发，但不得覆盖更新事件的静默抑制状态。
    fn observe_tone(&mut self, start: u64, due: Instant, bytes: &[u8]) {
        if self.tone.is_some_and(|tone| start < tone.start) {
            return;
        }
        if self.tone.is_none_or(|tone| start > tone.start) {
            self.tone = Some(ToneHold {
                start,
                through: start,
                ended: false,
                timeout: due + self.config.max_delay,
                seen_events: 0,
                ended_events: 0,
                durations: [0; 17],
                timed_out: false,
            });
        }
        let tone = self.tone.as_mut().unwrap();
        let mut progressing = false;
        for event in bytes.chunks_exact(4) {
            let index = usize::from(event[0]);
            let bit = 1u32 << index;
            let duration = u16::from_be_bytes([event[2], event[3]]);
            let unseen = tone.seen_events & bit == 0;
            // 每个事件自己的duration与E位单调；一个多事件包的最后四字节不能覆盖其他事件。
            if event[1] & 0x80 == 0
                && tone.ended_events & bit == 0
                && (unseen || duration > tone.durations[index])
            {
                progressing = true;
            }
            tone.seen_events |= bit;
            tone.durations[index] = tone.durations[index].max(duration);
            if event[1] & 0x80 != 0 {
                tone.ended_events |= bit;
            }
            tone.through = tone.through.max(start + u64::from(tone.durations[index]));
        }
        tone.ended = tone.seen_events == tone.ended_events;
        // 旧重复包不刷新活动截止时间；只有仍未结束事件的真实进度才延长有限持有。
        if progressing {
            tone.timeout = due + self.config.max_delay;
            tone.timed_out = false;
        }
    }
    fn update_inactive(&mut self) {
        if !self.inactive
            && self
                .cursor
                .zip(self.last_played)
                .is_some_and(|(next, last)| next > last.saturating_add(self.max_ticks))
            && self.earliest_audio(self.cursor.unwrap()).is_none()
        {
            self.inactive = true;
            self.stats.inactive_suspends += 1;
        }
    }
    fn note_delay(&mut self, now: Instant, arrival: Instant) {
        let ns = now
            .saturating_duration_since(arrival)
            .as_nanos()
            .min(u128::from(u64::MAX)) as u64;
        self.stats.last_delay_ns = ns;
        self.stats.peak_delay_ns = self.stats.peak_delay_ns.max(ns);
    }
}
fn validate_payloads(audio: u8, dtmf: Option<u8>, cn: Option<u8>) -> Result<()> {
    ensure!(
        audio < 128
            && dtmf.is_none_or(|pt| pt < 128 && pt != audio)
            && cn.is_none_or(|pt| pt < 128 && pt != audio)
            && (dtmf.is_none() || dtmf != cn),
        "jitter payload types must be distinct"
    );
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn cfg() -> JitterConfig {
        JitterConfig {
            clock_rate: 8000,
            packet_ticks: 160,
            audio_payload: 0,
            dtmf_payload: Some(101),
            cn_payload: Some(13),
            max_payload_bytes: 480,
            target_delay: Duration::from_millis(40),
            max_delay: Duration::from_millis(120),
            capacity: 8,
        }
    }
    fn packet(seq: u16, ts: u32, pt: u8, data: &[u8]) -> Vec<u8> {
        let mut p = vec![0; 12 + data.len()];
        p[0] = 0x80;
        p[1] = pt;
        p[2..4].copy_from_slice(&seq.to_be_bytes());
        p[4..8].copy_from_slice(&ts.to_be_bytes());
        p[8..12].copy_from_slice(&42u32.to_be_bytes());
        p[12..].copy_from_slice(data);
        p
    }
    fn push(
        j: &mut JitterBuffer,
        t: Instant,
        seq: u16,
        ts: u32,
        pt: u8,
        data: &[u8],
    ) -> PushOutcome {
        j.push(PacketView::parse(&packet(seq, ts, pt, data)).unwrap(), t)
    }
    #[test]
    fn reordered_audio_wraps_without_auxiliary_sequence_loss() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 65534, 0xffff_ff00, 0, &[1]);
        push(&mut j, t + Duration::from_millis(20), 0, 64, 0, &[3]);
        assert_eq!(
            push(
                &mut j,
                t + Duration::from_millis(25),
                65535,
                0xffff_ffa0,
                0,
                &[2]
            ),
            PushOutcome::Reordered
        );
        for (ms, value) in [(40, 1), (60, 2), (80, 3)] {
            assert!(matches!(
                j.pop_due(t + Duration::from_millis(ms), &mut out),
                Some(Playout::Audio { .. })
            ));
            assert_eq!(out[0], value);
        }
        assert_eq!(j.stats.lost, 0);
        assert_eq!(j.stats.reordered, 1);
    }
    #[test]
    fn duplicates_late_and_six_frame_tail_are_bounded() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 10, 0, 0, &[1]);
        assert_eq!(push(&mut j, t, 10, 0, 0, &[1]), PushOutcome::Duplicate);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(40), &mut out),
            Some(Playout::Audio { .. })
        ));
        for ms in [60, 80, 100, 120, 140, 160] {
            assert!(matches!(
                j.pop_due(t + Duration::from_millis(ms), &mut out),
                Some(Playout::Missing { .. })
            ));
        }
        assert_eq!(j.next_deadline(), None);
        assert_eq!(j.stats.lost, 6);
        assert_eq!(j.stats.inactive_suspends, 1);
        assert_eq!(
            push(&mut j, t + Duration::from_millis(170), 11, 160, 0, &[2]),
            PushOutcome::Late
        );
        assert_eq!(
            push(&mut j, t + Duration::from_millis(1000), 12, 8000, 0, &[3]),
            PushOutcome::Accepted
        );
        assert_eq!(j.next_deadline(), Some(t + Duration::from_millis(1040)));
    }
    #[test]
    fn cn_suppresses_only_following_missing_and_keeps_queued_audio() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(&mut j, t + Duration::from_millis(20), 2, 160, 13, &[30]);
        push(&mut j, t + Duration::from_millis(60), 3, 480, 0, &[2]);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(40), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(60), &mut out),
            Some(Playout::Aux {
                payload_type: 13,
                ..
            })
        ));
        assert_eq!(j.next_deadline(), Some(t + Duration::from_millis(100)));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(100), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert_eq!(out[0], 2);
        assert_eq!(j.stats.lost, 0);
    }
    #[test]
    fn audio_and_dtmf_share_sequence_but_not_loss_slots() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(
            &mut j,
            t + Duration::from_millis(10),
            2,
            80,
            101,
            &[1, 0, 0, 160],
        );
        push(&mut j, t + Duration::from_millis(20), 3, 160, 0, &[2]);
        push(
            &mut j,
            t + Duration::from_millis(30),
            4,
            80,
            101,
            &[1, 128, 1, 144],
        );
        push(&mut j, t + Duration::from_millis(40), 5, 320, 0, &[3]);
        for (ms, audio) in [(40, true), (50, false), (60, true), (70, false), (80, true)] {
            let result = j.pop_due(t + Duration::from_millis(ms), &mut out);
            assert_eq!(matches!(result, Some(Playout::Audio { .. })), audio);
            assert!(result.is_some());
        }
        assert_eq!(j.stats.played, 3);
        assert_eq!(j.stats.aux_packets, 2);
        assert_eq!(j.stats.lost, 0);
        assert_eq!(j.rx.report(t).unwrap().cumulative_lost, 0);
    }
    #[test]
    fn generic_clock_frame_and_payload_storage_bounds() {
        let mut config = cfg();
        config.clock_rate = 48000;
        config.packet_ticks = 480;
        config.capacity = 13;
        let mut j = JitterBuffer::new(config).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        assert_eq!(push(&mut j, t, 1, 0, 0, &[1; 480]), PushOutcome::Accepted);
        assert_eq!(push(&mut j, t, 2, 480, 0, &[1; 481]), PushOutcome::Invalid);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(40), &mut out),
            Some(Playout::Audio { len: 480, .. })
        ));
        for i in 1..=12 {
            assert!(matches!(
                j.pop_due(t + Duration::from_millis(40 + i * 10), &mut out),
                Some(Playout::Missing { .. })
            ));
        }
        assert_eq!(j.stats.lost, 12);
        assert_eq!(j.next_deadline(), None);
    }
    #[test]
    fn auxiliary_only_timeout_and_negotiation_are_bounded() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 101, &[1, 0, 0, 160]);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(40), &mut out),
            Some(Playout::Aux { .. })
        ));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(160), &mut out),
            Some(Playout::Expired { skipped: 0 })
        ));
        assert_eq!(j.next_deadline(), None);
        assert_eq!(j.stats.aux_timeouts, 1);
        push(
            &mut j,
            t + Duration::from_millis(170),
            2,
            100,
            101,
            &[1, 128, 0, 160],
        );
        j.set_aux_payloads(None, Some(13)).unwrap();
        assert_eq!(j.stats.depth, 0);
        assert!(j.set_aux_payloads(Some(0), None).is_err());
    }
    #[test]
    fn queued_auxiliary_suppresses_missing_before_its_arrival_relative_deadline() {
        for pt in [13, 101] {
            let mut j = JitterBuffer::new(cfg()).unwrap();
            let t = Instant::now();
            let mut out = [0; 480];
            push(&mut j, t, 1, 0, 0, &[1]);
            let data = if pt == 13 {
                &[30][..]
            } else {
                &[1, 0, 0, 160][..]
            };
            push(&mut j, t + Duration::from_micros(20200), 2, 160, pt, data);
            assert!(matches!(
                j.pop_due(t + Duration::from_millis(40), &mut out),
                Some(Playout::Audio { .. })
            ));
            assert_eq!(j.pop_due(t + Duration::from_millis(60), &mut out), None);
            assert!(matches!(
                j.pop_due(t + Duration::from_micros(60200), &mut out),
                Some(Playout::Aux { .. })
            ));
            assert_eq!(j.stats.lost, 0);
        }
    }
    #[test]
    fn old_cn_does_not_pause_newer_audio() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(&mut j, t + Duration::from_millis(20), 2, 160, 0, &[2]);
        j.pop_due(t + Duration::from_millis(40), &mut out);
        j.pop_due(t + Duration::from_millis(60), &mut out);
        push(&mut j, t + Duration::from_millis(61), 3, 0, 13, &[30]);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(80), &mut out),
            Some(Playout::Missing { .. })
        ));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(100), &mut out),
            Some(Playout::Missing { .. })
        ));
        assert_eq!(
            j.pop_due(t + Duration::from_millis(101), &mut out),
            Some(Playout::Expired { skipped: 0 })
        );
        assert_eq!(out[0], 2);
        assert_eq!(j.stats.aux_packets, 0);
        assert_eq!(j.stats.late, 1);
        assert_eq!(j.stats.aux_expired, 1);
        assert_eq!(j.rx.stats().accepted_packets, 3);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(120), &mut out),
            Some(Playout::Missing { .. })
        ));
    }
    #[test]
    fn queued_cn_is_discarded_when_real_voice_takes_its_position() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(&mut j, t + Duration::from_micros(20200), 2, 160, 13, &[30]);
        push(&mut j, t + Duration::from_micros(20500), 3, 160, 0, &[2]);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(40), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(60), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert_eq!(
            j.pop_due(t + Duration::from_micros(60200), &mut out),
            Some(Playout::Expired { skipped: 0 })
        );
        assert_eq!(out[0], 2);
        assert_eq!(j.stats.aux_packets, 0);
        assert_eq!(j.stats.aux_expired, 1);
        assert_eq!(j.next_deadline(), Some(t + Duration::from_millis(80)));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(80), &mut out),
            Some(Playout::Missing { .. })
        ));
        assert_eq!(j.stats.played, 2);
        assert_eq!(j.rx.stats().accepted_packets, 3);
    }
    #[test]
    fn same_deadline_cn_cannot_precede_voice_at_the_same_timestamp() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(&mut j, t + Duration::from_millis(20), 2, 160, 13, &[30]);
        push(&mut j, t + Duration::from_micros(20500), 3, 160, 0, &[2]);
        j.pop_due(t + Duration::from_millis(40), &mut out);
        assert_eq!(
            j.pop_due(t + Duration::from_millis(60), &mut out),
            Some(Playout::Expired { skipped: 0 })
        );
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(60), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert_eq!(out[0], 2);
        assert_eq!(j.stats.aux_packets, 0);
        assert_eq!(j.stats.played, 2);
    }
    #[test]
    fn expired_auxiliary_is_not_burst_forwarded_but_still_prevents_false_loss() {
        for pt in [13, 101] {
            let mut j = JitterBuffer::new(cfg()).unwrap();
            let t = Instant::now();
            let mut out = [0; 480];
            push(&mut j, t, 1, 0, 0, &[1]);
            let data = if pt == 13 {
                &[30][..]
            } else {
                &[1, 0, 0, 160][..]
            };
            push(&mut j, t + Duration::from_millis(20), 2, 160, pt, data);
            j.pop_due(t + Duration::from_millis(40), &mut out);
            assert_eq!(
                j.pop_due(t + Duration::from_millis(80), &mut out),
                Some(Playout::Expired { skipped: 0 })
            );
            assert_eq!(j.stats.aux_packets, 0);
            assert_eq!(j.stats.aux_expired, 1);
            assert_eq!(j.stats.lost, 0);
            assert_eq!(j.pop_due(t + Duration::from_millis(80), &mut out), None);
            if pt == 13 {
                assert_eq!(j.next_deadline(), None);
            } else {
                assert_eq!(j.next_deadline(), Some(t + Duration::from_millis(180)));
            }
        }
    }
    #[test]
    fn changing_auxiliary_role_cannot_reinterpret_old_payload_and_keeps_audio() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(&mut j, t + Duration::from_millis(10), 2, 80, 13, &[30]);
        let deadline = j.next_deadline();
        j.set_aux_payloads(Some(13), Some(101)).unwrap();
        assert_eq!(j.stats.depth, 1);
        assert_eq!(j.next_deadline(), deadline);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(40), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert_eq!(j.stats.resets, 0);
        assert_eq!(j.rx.ssrc(), Some(42));
    }
    #[test]
    fn late_scheduler_drops_old_frames_without_catchup_burst() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        for i in 0..4 {
            push(
                &mut j,
                t + Duration::from_millis(i * 20),
                i as u16,
                (i * 160) as u32,
                0,
                &[i as u8],
            );
        }
        assert_eq!(
            j.pop_due(t + Duration::from_millis(99), &mut out),
            Some(Playout::Expired { skipped: 2 })
        );
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(99), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert_eq!(out[0], 2);
        assert_eq!(j.pop_due(t + Duration::from_millis(99), &mut out), None);
        assert_eq!(j.stats.lost, 0);
        assert_eq!(j.stats.expired, 2);
    }
    #[test]
    fn cn_resume_changes_phase_and_marks_only_first_output_of_new_segment() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(&mut j, t + Duration::from_millis(20), 2, 160, 13, &[30]);
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(40), &mut out),
            Some(Playout::Audio {
                new_segment: true,
                ..
            })
        ));
        j.pop_due(t + Duration::from_millis(60), &mut out);
        for (ms, seq, ts) in [(80, 3, 600), (100, 4, 760), (120, 5, 920)] {
            assert_eq!(
                push(
                    &mut j,
                    t + Duration::from_millis(ms),
                    seq,
                    ts,
                    0,
                    &[seq as u8]
                ),
                PushOutcome::Accepted
            );
        }
        for (ms, ts, boundary) in [(120, 600, true), (140, 760, false), (160, 920, false)] {
            assert!(
                matches!(j.pop_due(t+Duration::from_millis(ms),&mut out),Some(Playout::Audio{timestamp,new_segment,..}) if timestamp as u32==ts && new_segment==boundary)
            );
        }
        assert_eq!(j.stats.invalid, 0);
        assert_eq!(j.stats.lost, 0);
    }
    #[test]
    fn inactive_marked_resume_may_follow_predicted_plc_but_old_packets_stay_late() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        j.pop_due(t + Duration::from_millis(40), &mut out);
        for ms in [60, 80, 100, 120, 140, 160] {
            j.pop_due(t + Duration::from_millis(ms), &mut out);
        }
        assert_eq!(
            push(&mut j, t + Duration::from_millis(170), 2, 160, 0, &[2]),
            PushOutcome::Late
        );
        let mut p = packet(3, 600, 0, &[3]);
        p[1] |= 0x80;
        assert_eq!(
            j.push(
                PacketView::parse(&p).unwrap(),
                t + Duration::from_millis(200)
            ),
            PushOutcome::Accepted
        );
        assert_eq!(
            push(&mut j, t + Duration::from_millis(220), 4, 760, 0, &[4]),
            PushOutcome::Accepted
        );
        assert!(
            matches!(j.pop_due(t+Duration::from_millis(240),&mut out),Some(Playout::Audio{timestamp,new_segment:true,..}) if timestamp as u32==600)
        );
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(260), &mut out),
            Some(Playout::Audio {
                new_segment: false,
                ..
            })
        ));
        assert_eq!(j.stats.late, 1);
    }
    #[test]
    fn new_segment_preserves_old_queued_voice_and_survives_first_frame_expiry() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(&mut j, t + Duration::from_millis(20), 2, 160, 13, &[30]);
        push(&mut j, t + Duration::from_millis(80), 3, 600, 0, &[3]);
        push(&mut j, t + Duration::from_millis(100), 4, 760, 0, &[4]);
        assert!(
            matches!(j.pop_due(t+Duration::from_millis(40),&mut out),Some(Playout::Audio{timestamp,new_segment:true,..}) if timestamp as u32==0)
        );
        j.pop_due(t + Duration::from_millis(60), &mut out);
        assert_eq!(
            j.pop_due(t + Duration::from_millis(140), &mut out),
            Some(Playout::Expired { skipped: 1 })
        );
        assert!(
            matches!(j.pop_due(t+Duration::from_millis(140),&mut out),Some(Playout::Audio{timestamp,new_segment:true,..}) if timestamp as u32==760)
        );
        assert_eq!(j.stats.played, 2);
    }
    #[test]
    fn active_audio_cannot_change_phase_without_known_silence() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        j.pop_due(t + Duration::from_millis(40), &mut out);
        let mut p = packet(2, 170, 0, &[2]);
        p[1] |= 0x80;
        assert_eq!(
            j.push(
                PacketView::parse(&p).unwrap(),
                t + Duration::from_millis(45)
            ),
            PushOutcome::Invalid
        );
        assert_eq!(j.stats.invalid, 1);
    }
    #[test]
    fn scheduler_expiration_does_not_reanchor_kernel_backlog_as_silence() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        for step in 0..=12u16 {
            let at = t + Duration::from_millis(u64::from(step) * 20);
            if step <= 10 {
                push(&mut j, at, step, u32::from(step) * 160, 0, &[step as u8]);
            }
            if step >= 2 {
                assert!(matches!(
                    j.pop_due(at, &mut out),
                    Some(Playout::Audio { .. })
                ));
            }
        }
        let now = t + Duration::from_millis(381);
        assert_eq!(
            j.pop_due(now, &mut out),
            Some(Playout::Expired { skipped: 6 })
        );
        assert_eq!(j.stats.inactive_suspends, 0);
        for frame in 11..=19u16 {
            assert_eq!(
                push(
                    &mut j,
                    now,
                    frame,
                    u32::from(frame) * 160,
                    0,
                    &[frame as u8]
                ),
                if frame <= 17 {
                    PushOutcome::Late
                } else {
                    PushOutcome::Accepted
                }
            );
        }
        assert_eq!(j.next_deadline(), Some(t + Duration::from_millis(400)));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(400), &mut out),
            Some(Playout::Audio {
                new_segment: false,
                ..
            })
        ));
        assert_eq!(out[0], 18);
        assert_eq!(j.stats.lost, 0);
    }
    #[test]
    fn late_previous_dtmf_end_cannot_cancel_new_event_hold() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 0, 0, &[1]);
        push(
            &mut j,
            t + Duration::from_millis(20),
            2,
            160,
            101,
            &[1, 0, 0, 160],
        );
        push(
            &mut j,
            t + Duration::from_millis(40),
            3,
            160,
            101,
            &[1, 128, 1, 144],
        );
        j.pop_due(t + Duration::from_millis(40), &mut out);
        j.pop_due(t + Duration::from_millis(60), &mut out);
        j.pop_due(t + Duration::from_millis(80), &mut out);
        let mut p = packet(6, 600, 0, &[6]);
        p[1] |= 0x80;
        j.push(
            PacketView::parse(&p).unwrap(),
            t + Duration::from_millis(200),
        );
        push(
            &mut j,
            t + Duration::from_millis(220),
            7,
            760,
            101,
            &[2, 0, 0, 160],
        );
        push(
            &mut j,
            t + Duration::from_millis(225),
            5,
            160,
            101,
            &[1, 128, 1, 144],
        );
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(240), &mut out),
            Some(Playout::Audio { .. })
        ));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(260), &mut out),
            Some(Playout::Aux { timestamp: 760, .. })
        ));
        assert!(matches!(
            j.pop_due(t + Duration::from_millis(265), &mut out),
            Some(Playout::Aux { timestamp: 160, .. })
        ));
        assert_eq!(j.tone.unwrap().start as u32, 760);
        assert!(!j.tone.unwrap().ended);
        assert_eq!(j.pop_due(t + Duration::from_millis(266), &mut out), None);
        assert_eq!(j.stats.lost, 0);
    }
    #[test]
    fn dtmf_multiple_blocks_keep_individual_duration_and_sticky_end() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 1000, 101, &[1, 128, 0, 100, 2, 0, 0, 200]);
        j.pop_due(t + Duration::from_millis(40), &mut out);
        assert!(!j.tone.unwrap().ended);
        assert_eq!(j.tone.unwrap().through as u32, 1200);
        push(
            &mut j,
            t + Duration::from_millis(20),
            3,
            1000,
            101,
            &[2, 128, 0, 220],
        );
        j.pop_due(t + Duration::from_millis(60), &mut out);
        assert!(j.tone.unwrap().ended);
        assert_eq!(j.tone.unwrap().through as u32, 1220);
        push(
            &mut j,
            t + Duration::from_millis(25),
            2,
            1000,
            101,
            &[2, 0, 0, 180, 1, 0, 0, 50],
        );
        j.pop_due(t + Duration::from_millis(65), &mut out);
        let hold = j.tone.unwrap();
        assert!(hold.ended);
        assert_eq!(hold.through as u32, 1220);
        assert_eq!(hold.durations[1], 100);
        assert_eq!(hold.durations[2], 220);
        assert_eq!(j.next_deadline(), None);
        assert_eq!(
            push(
                &mut j,
                t + Duration::from_millis(30),
                4,
                1000,
                101,
                &[17, 0, 0, 160]
            ),
            PushOutcome::Invalid
        );
    }
    #[test]
    fn dtmf_timeout_keeps_watermark_and_old_repeats_cannot_restart_wait() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let mut out = [0; 480];
        push(&mut j, t, 1, 1000, 101, &[1, 0, 0, 100]);
        j.pop_due(t + Duration::from_millis(40), &mut out);
        assert_eq!(
            j.pop_due(t + Duration::from_millis(160), &mut out),
            Some(Playout::Expired { skipped: 0 })
        );
        push(
            &mut j,
            t + Duration::from_millis(170),
            2,
            900,
            101,
            &[1, 128, 0, 100],
        );
        j.pop_due(t + Duration::from_millis(210), &mut out);
        push(
            &mut j,
            t + Duration::from_millis(180),
            3,
            1000,
            101,
            &[1, 0, 0, 50],
        );
        j.pop_due(t + Duration::from_millis(220), &mut out);
        assert_eq!(j.tone.unwrap().start as u32, 1000);
        assert_eq!(j.tone.unwrap().through as u32, 1100);
        assert!(j.tone.unwrap().timed_out);
        assert_eq!(j.next_deadline(), None);
        push(
            &mut j,
            t + Duration::from_millis(200),
            4,
            1000,
            101,
            &[1, 0, 0, 200],
        );
        j.pop_due(t + Duration::from_millis(240), &mut out);
        assert!(!j.tone.unwrap().timed_out);
        assert_eq!(j.tone.unwrap().through as u32, 1200);
        assert_eq!(j.next_deadline(), Some(t + Duration::from_millis(360)));
    }
    #[test]
    fn memory_overflow_short_output_and_source_reset_are_explicit() {
        let mut j = JitterBuffer::new(cfg()).unwrap();
        let t = Instant::now();
        let bytes = j.storage_bytes();
        for i in 0..8 {
            assert_eq!(
                push(&mut j, t, i, i as u32 * 160, 101, &[1, 0, 0, 160]),
                PushOutcome::Accepted
            );
        }
        assert_eq!(
            push(&mut j, t, 8, 1280, 101, &[1, 0, 0, 160]),
            PushOutcome::Overflow
        );
        assert_eq!(j.storage_bytes(), bytes);
        assert_eq!(j.stats.peak_depth, 8);
        assert_eq!(j.pop_due(t + Duration::from_millis(40), &mut [0; 4]), None);
        assert_eq!(j.stats.depth, 8);
        let mut p = packet(1, 0, 0, &[4]);
        p[8..12].copy_from_slice(&99u32.to_be_bytes());
        assert_eq!(
            j.push(PacketView::parse(&p).unwrap(), t),
            PushOutcome::Probation
        );
        p[2..4].copy_from_slice(&2u16.to_be_bytes());
        p[4..8].copy_from_slice(&160u32.to_be_bytes());
        assert_eq!(
            j.push(
                PacketView::parse(&p).unwrap(),
                t + Duration::from_millis(20)
            ),
            PushOutcome::Reset
        );
        assert_eq!(j.stats.depth, 1);
        assert_eq!(j.stats.resets, 1);
        let mut invalid = cfg();
        invalid.capacity = 6;
        assert!(JitterBuffer::new(invalid).is_err());
        invalid = cfg();
        invalid.max_payload_bytes = 481;
        assert!(JitterBuffer::new(invalid).is_err());
        let mut wide = cfg();
        wide.clock_rate = 48000;
        wide.packet_ticks = 960;
        assert!(JitterBuffer::new(wide).is_ok());
    }
}
