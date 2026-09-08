//! 单方向RTP接收状态与发送身份；音频时钟独立于占用包序的辅助负载。
use super::rtp::PacketView;
use anyhow::{ensure, Result};
use std::time::{Duration, Instant};

const SEQ_SEED: u64 = 1 << 16;
const TS_SEED: u64 = 1 << 32;
const REORDER_WINDOW: u64 = 128;
const MAX_DROPOUT: i32 = 3000;

/// 重启是需清除解码历史的显式事件，不能把新源接到旧滤波器历史。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ResetReason {
    SourceChanged,
    SequenceRestart,
    TimestampRestart,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RxOutcome {
    Accepted {
        sequence: u64,
        timestamp: Option<u64>,
        reordered: bool,
        reset: Option<ResetReason>,
    },
    Duplicate,
    TooOld,
    Probation,
}
/// 累计计数不因源重启清零；RTCP本源区间计数另行维护。
#[derive(Clone, Copy, Debug, Default)]
pub struct RxStats {
    pub accepted_packets: u64,
    pub duplicates: u64,
    pub reordered: u64,
    pub too_old: u64,
    pub probation: u64,
    pub source_resets: u64,
    pub sequence_resets: u64,
    pub timestamp_resets: u64,
    pub audio_jitter_ticks: f64,
    pub report_jitter_ticks: f64,
}
#[derive(Clone, Copy, Debug, Default)]
pub struct ReceiverReport {
    pub ssrc: u32,
    pub fraction_lost: u8,
    pub cumulative_lost: i32,
    pub extended_highest_sequence: u32,
    pub jitter: u32,
    pub last_sr: u32,
    pub delay_since_last_sr: u32,
}
#[derive(Clone, Copy)]
struct Candidate {
    ssrc: u32,
    sequence: u16,
    timestamp: u32,
    audio: bool,
    at: Instant,
    reason: ResetReason,
}

/// 单SSRC与一个替换候选，恶意多SSRC不会扩张映射；128位滑动窗精确去重。
pub struct RxRtpState {
    clock: u32,
    source: Option<u32>,
    restart_after_bye: bool,
    highest: u64,
    base: u64,
    seen: u128,
    audio_timestamp: Option<u64>,
    audio_arrival: Option<(Instant, u64)>,
    report_arrival: Option<(Instant, u64)>,
    candidate: Option<Candidate>,
    received: u64,
    expected_prior: u64,
    received_prior: u64,
    last_sr: Option<(u32, Instant)>,
    stats: RxStats,
}
impl RxRtpState {
    pub fn new(clock_rate: u32) -> Result<Self> {
        ensure!((1000..=192000).contains(&clock_rate), "invalid RTP clock");
        Ok(Self {
            clock: clock_rate,
            source: None,
            restart_after_bye: false,
            highest: 0,
            base: 0,
            seen: 0,
            audio_timestamp: None,
            audio_arrival: None,
            report_arrival: None,
            candidate: None,
            received: 0,
            expected_prior: 0,
            received_prior: 0,
            last_sr: None,
            stats: RxStats::default(),
        })
    }
    pub fn ssrc(&self) -> Option<u32> {
        self.source
    }
    /// 已校验BYE结束当前报告身份；累计统计保留，下一源首包显式要求清除解码与TX旧锚点。
    pub fn end_source(&mut self) {
        if self.source.is_some() {
            self.restart_after_bye = true;
        }
        self.source = None;
        self.last_sr = None;
        self.candidate = None;
        self.audio_timestamp = None;
        self.audio_arrival = None;
        self.report_arrival = None;
    }
    pub fn stats(&self) -> &RxStats {
        &self.stats
    }
    /// 给辅助负载做同钟展开时只读音频锚点；不使恒定DTMF时间戳污染音频jitter。
    pub fn extend_timestamp(&self, raw: u32) -> u64 {
        self.audio_timestamp
            .map_or(TS_SEED + u64::from(raw), |last| extend32(raw, last))
    }
    fn initialize(&mut self, packet: PacketView<'_>) {
        self.source = Some(packet.ssrc);
        self.highest = SEQ_SEED + u64::from(packet.sequence);
        self.base = self.highest;
        self.seen = 0;
        self.audio_timestamp = None;
        self.audio_arrival = None;
        self.report_arrival = None;
        self.received = 0;
        self.expected_prior = 0;
        self.received_prior = 0;
        self.last_sr = None;
        self.stats.audio_jitter_ticks = 0.0;
        self.stats.report_jitter_ticks = 0.0;
        self.candidate = None;
    }
    /// 异常跳变只有第二个同源顺序包才确认；单个突跳不能立即重置正在播放的时间线。
    fn confirm_restart(
        &mut self,
        packet: PacketView<'_>,
        audio: bool,
        at: Instant,
        reason: ResetReason,
    ) -> bool {
        let confirmed = self.candidate.is_some_and(|c| {
            c.ssrc == packet.ssrc
                && c.reason == reason
                && packet.sequence == c.sequence.wrapping_add(1)
                && at.saturating_duration_since(c.at) <= Duration::from_millis(500)
                && (!audio
                    || !c.audio
                    || (packet.timestamp.wrapping_sub(c.timestamp) as i32 >= 0
                        && packet.timestamp.wrapping_sub(c.timestamp) <= self.clock))
        });
        if confirmed {
            self.initialize(packet);
            return true;
        }
        self.candidate = Some(Candidate {
            ssrc: packet.ssrc,
            sequence: packet.sequence,
            timestamp: packet.timestamp,
            audio,
            at,
            reason,
        });
        self.stats.probation += 1;
        false
    }
    /// audio由已协商的载荷分类给出；包序覆盖全部负载，时间戳/jitter只取主音频。
    pub fn observe(&mut self, packet: PacketView<'_>, audio: bool, at: Instant) -> RxOutcome {
        let mut reset = None;
        if self.source.is_none() {
            self.initialize(packet);
            if self.restart_after_bye {
                self.restart_after_bye = false;
                self.stats.source_resets += 1;
                reset = Some(ResetReason::SourceChanged);
            }
        } else if self.source != Some(packet.ssrc) {
            if !self.confirm_restart(packet, audio, at, ResetReason::SourceChanged) {
                return RxOutcome::Probation;
            }
            self.stats.source_resets += 1;
            reset = Some(ResetReason::SourceChanged);
        }
        let delta = i32::from(packet.sequence.wrapping_sub(self.highest as u16) as i16);
        if delta >= MAX_DROPOUT {
            if !self.confirm_restart(packet, audio, at, ResetReason::SequenceRestart) {
                return RxOutcome::Probation;
            }
            self.stats.sequence_resets += 1;
            reset = Some(ResetReason::SequenceRestart);
        } else if delta < -(REORDER_WINDOW as i32 - 1) {
            // 超出窗口的旧包不得重置；只有与当前源明显不连续且下一包顺序一致才可重启。
            if !self.confirm_restart(packet, audio, at, ResetReason::SequenceRestart) {
                self.stats.too_old += 1;
                return RxOutcome::TooOld;
            }
            self.stats.sequence_resets += 1;
            reset = Some(ResetReason::SequenceRestart);
        }
        let mut sequence = extend16(packet.sequence, self.highest);
        let mut timestamp = audio.then(|| self.extend_timestamp(packet.timestamp));
        if audio && sequence > self.highest {
            if let Some((previous_at, previous_ts)) = self.audio_arrival {
                let expected =
                    at.saturating_duration_since(previous_at).as_secs_f64() * f64::from(self.clock);
                let delta = timestamp.unwrap() as i128 - i128::from(previous_ts);
                // 留出实际无包时间，正常长静音不作为重启；负向突跳或偏离墙上到达超过2秒需要确认。
                if delta < 0 || (delta as f64 - expected).abs() > f64::from(self.clock) * 2.0 {
                    if !self.confirm_restart(packet, audio, at, ResetReason::TimestampRestart) {
                        return RxOutcome::Probation;
                    }
                    self.stats.timestamp_resets += 1;
                    reset = Some(ResetReason::TimestampRestart);
                    sequence = self.highest;
                    timestamp = Some(self.extend_timestamp(packet.timestamp));
                }
            }
        }
        self.received += 1;
        // RR抖动按实际主音频到达顺序更新，包含重排和重复；不能套用播放端只取顺序包的指标。
        // 辅助负载固定事件起点不代表当前音频采样时间，因而不混入此主音频时钟估计。
        if let Some(ts) = timestamp {
            if let Some((previous_at, previous_ts)) = self.report_arrival {
                let spacing =
                    at.saturating_duration_since(previous_at).as_secs_f64() * f64::from(self.clock);
                let distance = (spacing - (i128::from(ts) - i128::from(previous_ts)) as f64).abs();
                self.stats.report_jitter_ticks +=
                    (distance - self.stats.report_jitter_ticks) / 16.0;
            }
            self.report_arrival = Some((at, ts));
        }
        let reordered = sequence < self.highest;
        if sequence > self.highest {
            let shift = sequence - self.highest;
            self.seen = if shift >= REORDER_WINDOW {
                0
            } else {
                self.seen << shift
            };
            self.highest = sequence;
        }
        let behind = self.highest - sequence;
        if behind >= REORDER_WINDOW {
            self.stats.too_old += 1;
            return RxOutcome::TooOld;
        }
        let bit = 1u128 << behind;
        if self.seen & bit != 0 {
            self.stats.duplicates += 1;
            return RxOutcome::Duplicate;
        }
        self.seen |= bit;
        self.stats.accepted_packets += 1;
        if reordered {
            self.stats.reordered += 1;
        }
        if let Some(timestamp) = timestamp {
            // 顺序音频样本用于播放jitter，重排包另计；辅助时钟和恒定DTMF起点不参与。
            if !reordered {
                if let Some((last_at, last_ts)) = self.audio_arrival {
                    let spacing =
                        at.saturating_duration_since(last_at).as_secs_f64() * f64::from(self.clock);
                    let delta = (spacing - (timestamp as i128 - i128::from(last_ts)) as f64).abs();
                    self.stats.audio_jitter_ticks += (delta - self.stats.audio_jitter_ticks) / 16.0;
                }
                self.audio_arrival = Some((at, timestamp));
                self.audio_timestamp = Some(timestamp);
            }
        }
        self.candidate = None;
        RxOutcome::Accepted {
            sequence,
            timestamp,
            reordered,
            reset,
        }
    }
    pub fn note_sender_report(&mut self, ssrc: u32, ntp: u64, at: Instant) {
        if self.source == Some(ssrc) {
            self.last_sr = Some(((ntp >> 16) as u32, at));
        }
    }
    /// RR的丢失覆盖RTP包序（含辅助包），与音频Missing计数严格分开；重复可产生负累计loss。
    pub fn report(&mut self, now: Instant) -> Option<ReceiverReport> {
        let ssrc = self.source?;
        let expected = self.highest - self.base + 1;
        let expected_interval = expected.saturating_sub(self.expected_prior);
        let received_interval = self.received.saturating_sub(self.received_prior);
        let lost = expected as i128 - self.received as i128;
        let fraction = if expected_interval == 0 || received_interval >= expected_interval {
            0
        } else {
            ((expected_interval - received_interval) * 256 / expected_interval).min(255) as u8
        };
        self.expected_prior = expected;
        self.received_prior = self.received;
        let (last_sr, delay) = self.last_sr.map_or((0, 0), |(lsr, at)| {
            (
                lsr,
                (now.saturating_duration_since(at).as_secs_f64() * 65536.0).min(u32::MAX as f64)
                    as u32,
            )
        });
        Some(ReceiverReport {
            ssrc,
            fraction_lost: fraction,
            cumulative_lost: lost.clamp(-8388608, 8388607) as i32,
            extended_highest_sequence: self.highest.wrapping_sub(SEQ_SEED) as u32,
            jitter: self.stats.report_jitter_ticks.min(u32::MAX as f64) as u32,
            last_sr,
            delay_since_last_sr: delay,
        })
    }
}
fn extend16(raw: u16, last: u64) -> u64 {
    (i128::from(last) + i128::from(raw.wrapping_sub(last as u16) as i16)) as u64
}
fn extend32(raw: u32, last: u64) -> u64 {
    (i128::from(last) + i128::from(raw.wrapping_sub(last as u32) as i32)) as u64
}

#[derive(Clone, Copy, Debug, Default)]
pub struct TxSnapshot {
    pub ssrc: u32,
    pub next_sequence: u16,
    pub prepared_packets: u64,
    pub sent_packets: u64,
    pub sent_octets: u64,
    pub failed_packets: u64,
    pub last_timestamp: u32,
    pub last_sent_at: Option<Instant>,
    pub clock_rate: u32,
    /// 媒体时钟基准与最后网络包分离；DTMF结束副本不会把SR时间戳拉回事件起点。
    pub clock_timestamp: u32,
    pub clock_at: Option<Instant>,
}
pub struct TxRtpState {
    value: TxSnapshot,
}
impl TxRtpState {
    pub fn new(ssrc: u32, sequence: u16, clock_rate: u32) -> Result<Self> {
        ensure!(
            (1000..=192000).contains(&clock_rate),
            "invalid TX RTP clock"
        );
        Ok(Self {
            value: TxSnapshot {
                ssrc,
                next_sequence: sequence,
                clock_rate,
                ..TxSnapshot::default()
            },
        })
    }
    pub fn ssrc(&self) -> u32 {
        self.value.ssrc
    }
    pub fn snapshot(&self) -> TxSnapshot {
        self.value
    }
    /// 提供者拥有待发包；准备后序号已经消耗，重试必须使用原字节，发送失败不能复用序号给新内容。
    pub fn write_packet(
        &mut self,
        pt: u8,
        marker: bool,
        timestamp: u32,
        payload: &[u8],
        out: &mut [u8],
    ) -> Result<usize> {
        ensure!(
            pt < 128 && out.len() >= 12 + payload.len(),
            "invalid RTP output buffer or payload type"
        );
        out[0] = 0x80;
        out[1] = pt | if marker { 0x80 } else { 0 };
        out[2..4].copy_from_slice(&self.value.next_sequence.to_be_bytes());
        out[4..8].copy_from_slice(&timestamp.to_be_bytes());
        out[8..12].copy_from_slice(&self.value.ssrc.to_be_bytes());
        out[12..12 + payload.len()].copy_from_slice(payload);
        self.value.next_sequence = self.value.next_sequence.wrapping_add(1);
        self.value.prepared_packets += 1;
        Ok(12 + payload.len())
    }
    /// 只在UDP完整接受后调用；报表字节只计负载，不含RTP头或填充。
    pub fn note_sent(&mut self, payload_bytes: usize, timestamp: u32, now: Instant) {
        self.value.sent_packets += 1;
        self.value.sent_octets += payload_bytes as u64;
        self.value.last_timestamp = timestamp;
        self.value.last_sent_at = Some(now);
        if self.value.clock_at.is_none() {
            self.note_clock(timestamp, now);
        }
    }
    /// 调用方以真正音频/CN采样时间或首个辅助事件的进度建立连续SR时钟。
    pub fn note_clock(&mut self, timestamp: u32, now: Instant) {
        self.value.clock_timestamp = timestamp;
        self.value.clock_at = Some(now);
    }
    pub fn note_failed(&mut self) {
        self.value.failed_packets += 1;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn p(seq: u16, ts: u32, ssrc: u32) -> [u8; 13] {
        let mut p = [0u8; 13];
        p[0] = 0x80;
        p[2..4].copy_from_slice(&seq.to_be_bytes());
        p[4..8].copy_from_slice(&ts.to_be_bytes());
        p[8..12].copy_from_slice(&ssrc.to_be_bytes());
        p
    }
    #[test]
    fn wraps_reordering_duplicates_and_rr_are_independent() {
        let mut r = RxRtpState::new(8000).unwrap();
        let now = Instant::now();
        for (i, seq, ts) in [
            (0, 65534, 0xffff_ff00),
            (20, 0, 64),
            (25, 65535, 0xffff_ffa0),
        ] {
            assert!(matches!(
                r.observe(
                    PacketView::parse(&p(seq, ts, 1)).unwrap(),
                    true,
                    now + Duration::from_millis(i)
                ),
                RxOutcome::Accepted { .. }
            ));
        }
        assert_eq!(
            r.observe(
                PacketView::parse(&p(65535, 0xffff_ffa0, 1)).unwrap(),
                true,
                now + Duration::from_millis(26)
            ),
            RxOutcome::Duplicate
        );
        assert_eq!(r.stats().reordered, 1);
        let report = r.report(now).unwrap();
        assert_eq!(report.extended_highest_sequence, 65536);
        assert_eq!(report.cumulative_lost, -1);
    }
    #[test]
    fn bye_stops_old_reports_and_marks_next_source_for_reanchoring() {
        let now = Instant::now();
        let mut r = RxRtpState::new(8000).unwrap();
        r.observe(PacketView::parse(&p(1, 8000, 1)).unwrap(), true, now);
        r.end_source();
        assert!(r.report(now).is_none());
        assert_eq!(r.stats().accepted_packets, 1);
        assert!(matches!(
            r.observe(PacketView::parse(&p(1, 0, 2)).unwrap(), true, now),
            RxOutcome::Accepted {
                reset: Some(ResetReason::SourceChanged),
                ..
            }
        ));
        assert_eq!(r.stats().accepted_packets, 2);
        assert_eq!(r.stats().source_resets, 1);
    }
    #[test]
    fn sequence_restart_requires_second_packet_and_bounds_source_candidates() {
        let now = Instant::now();
        let mut r = RxRtpState::new(8000).unwrap();
        r.observe(PacketView::parse(&p(10, 0, 1)).unwrap(), true, now);
        assert_eq!(
            r.observe(
                PacketView::parse(&p(5000, 160, 1)).unwrap(),
                true,
                now + Duration::from_millis(20)
            ),
            RxOutcome::Probation
        );
        assert!(matches!(
            r.observe(
                PacketView::parse(&p(5001, 320, 1)).unwrap(),
                true,
                now + Duration::from_millis(40)
            ),
            RxOutcome::Accepted {
                reset: Some(ResetReason::SequenceRestart),
                ..
            }
        ));
        for source in 2..1000 {
            assert_eq!(
                r.observe(
                    PacketView::parse(&p(1, 0, source)).unwrap(),
                    true,
                    now + Duration::from_millis(60)
                ),
                RxOutcome::Probation
            );
        }
        assert_eq!(r.ssrc(), Some(1));
        assert_eq!(r.stats().source_resets, 0);
        assert_eq!(r.stats().sequence_resets, 1);
    }
    #[test]
    fn rr_jitter_uses_reordered_arrival_while_playout_metric_stays_in_order() {
        let now = Instant::now();
        let mut r = RxRtpState::new(8000).unwrap();
        r.observe(PacketView::parse(&p(1, 0, 1)).unwrap(), true, now);
        r.observe(
            PacketView::parse(&p(3, 320, 1)).unwrap(),
            true,
            now + Duration::from_millis(40),
        );
        r.observe(
            PacketView::parse(&p(2, 160, 1)).unwrap(),
            true,
            now + Duration::from_millis(45),
        );
        assert_eq!(r.stats().audio_jitter_ticks, 0.0);
        assert_eq!(r.stats().report_jitter_ticks, 12.5);
        assert_eq!(r.report(now).unwrap().jitter, 12);
    }
    #[test]
    fn source_and_timestamp_need_confirmed_restart() {
        let mut r = RxRtpState::new(8000).unwrap();
        let now = Instant::now();
        r.observe(PacketView::parse(&p(1, 0, 1)).unwrap(), true, now);
        assert_eq!(
            r.observe(PacketView::parse(&p(1, 0, 2)).unwrap(), true, now),
            RxOutcome::Probation
        );
        assert!(matches!(
            r.observe(
                PacketView::parse(&p(2, 160, 2)).unwrap(),
                true,
                now + Duration::from_millis(20)
            ),
            RxOutcome::Accepted {
                reset: Some(ResetReason::SourceChanged),
                ..
            }
        ));
        assert_eq!(
            r.observe(
                PacketView::parse(&p(3, 100000, 2)).unwrap(),
                true,
                now + Duration::from_millis(40)
            ),
            RxOutcome::Probation
        );
        assert!(matches!(
            r.observe(
                PacketView::parse(&p(4, 100160, 2)).unwrap(),
                true,
                now + Duration::from_millis(60)
            ),
            RxOutcome::Accepted {
                reset: Some(ResetReason::TimestampRestart),
                ..
            }
        ));
    }
    #[test]
    fn auxiliary_timestamp_does_not_change_audio_jitter() {
        let now = Instant::now();
        let mut r = RxRtpState::new(48000).unwrap();
        r.observe(PacketView::parse(&p(1, 96000, 1)).unwrap(), true, now);
        r.observe(
            PacketView::parse(&p(2, 800, 1)).unwrap(),
            false,
            now + Duration::from_millis(10),
        );
        r.observe(
            PacketView::parse(&p(3, 96960, 1)).unwrap(),
            true,
            now + Duration::from_millis(20),
        );
        assert_eq!(r.stats().audio_jitter_ticks, 0.0);
        assert_eq!(r.report(now).unwrap().cumulative_lost, 0);
    }
    #[test]
    fn tx_does_not_reuse_failed_sequence_and_short_output_is_atomic() {
        let mut tx = TxRtpState::new(7, 65535, 8000).unwrap();
        let mut out = [0u8; 20];
        assert!(tx.write_packet(0, false, 100, &[1; 10], &mut out).is_err());
        assert_eq!(tx.snapshot().next_sequence, 65535);
        tx.write_packet(0, false, 100, &[1], &mut out).unwrap();
        tx.note_failed();
        tx.write_packet(0, false, 260, &[2], &mut out).unwrap();
        assert_eq!(&out[2..4], &[0, 0]);
        tx.note_sent(1, 260, Instant::now());
        assert_eq!(tx.snapshot().sent_octets, 1);
        assert_eq!(tx.snapshot().prepared_packets, 2);
    }
}
