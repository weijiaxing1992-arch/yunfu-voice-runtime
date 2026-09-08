//! 本地终结媒体的有界 RFC 4733 发送器；不改写透明桥的 RTP 或 RTCP。
use anyhow::{ensure, Result};
use serde::{Deserialize, Serialize};
use std::{
    collections::VecDeque,
    time::{Duration, Instant},
};

/// 活动任务和数字队列分别限额，不随通话上限创建线程或逐路空定时器。
pub const MAX_SENDERS: usize = 64;
pub const MAX_DIGITS: usize = 32;
const STEP: Duration = Duration::from_millis(20);
const GAP: Duration = Duration::from_millis(100);

/// 仅在启用本地按键发送后分配；此后提示音沿用同一 SSRC 和包序。
pub struct LocalRtpClock {
    epoch: Instant,
    sequence: u16,
    ssrc: u32,
    clock: u32,
}
impl LocalRtpClock {
    pub fn new(epoch: Instant, sequence: u16, ssrc: u32, clock: u32) -> Self {
        Self {
            epoch,
            sequence,
            ssrc,
            clock,
        }
    }
    /// 使用协商 RTP 时钟，不从 PCM 采样率推断；差值转换后有意按 u32 回绕。
    pub fn timestamp(&self, when: Instant) -> u32 {
        (when.saturating_duration_since(self.epoch).as_nanos() * u128::from(self.clock)
            / 1_000_000_000) as u32
    }
    pub fn stamp(&self, packet: &mut [u8], timestamp: u32) {
        packet[2..4].copy_from_slice(&self.sequence.to_be_bytes());
        packet[4..8].copy_from_slice(&timestamp.to_be_bytes());
        packet[8..12].copy_from_slice(&self.ssrc.to_be_bytes());
    }
    /// 只有内核完整接受报文才消耗一个序号；失败不伪计远端接收。
    pub fn accepted(&mut self) {
        self.sequence = self.sequence.wrapping_add(1);
    }
}

/// 查询是媒体实际状态，accepted 表示入队，completed 表示三次结束包均已送交 UDP。
#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct SendState {
    pub state: String,
    pub accepted_digits: u64,
    pub completed_digits: u64,
    pub failed_digits: u64,
    pub queued_digits: usize,
    pub sent_packets: u64,
    pub last_error: String,
}
#[derive(Clone, Copy)]
struct Digit {
    event: u8,
    duration_ms: u32,
}
struct Active {
    digit: Digit,
    start: Instant,
    timestamp: u32,
    elapsed_ms: u32,
    ends: u8,
}
pub struct Sender {
    queue: VecDeque<Digit>,
    active: Option<Active>,
    pub next: Option<Instant>,
    pub status: SendState,
    /// 分开的API请求也保留数字间隔，不能通过重复短请求绕过发送节拍。
    available_at: Option<Instant>,
}
impl Sender {
    pub fn new() -> Self {
        Self {
            queue: VecDeque::with_capacity(MAX_DIGITS),
            active: None,
            next: None,
            status: SendState {
                state: "idle".into(),
                ..SendState::default()
            },
            available_at: None,
        }
    }
    /// 先验证整批和容量，再一次入队；非法字符、事件集合不足时不会发送前半串。
    pub fn enqueue(
        &mut self,
        digits: &str,
        duration_ms: u32,
        mask: u32,
        now: Instant,
    ) -> Result<()> {
        ensure!(
            !digits.is_empty() && digits.len() <= MAX_DIGITS,
            "DTMF requires 1..32 digits"
        );
        ensure!(
            (50..=1000).contains(&duration_ms),
            "DTMF duration must be 50..1000 ms"
        );
        ensure!(
            self.status.queued_digits + digits.len() <= MAX_DIGITS,
            "DTMF queue full"
        );
        let mut events = [0u8; MAX_DIGITS];
        for (index, digit) in digits.bytes().enumerate() {
            let event = b"0123456789*#ABCD"
                .iter()
                .position(|v| *v == digit)
                .ok_or_else(|| anyhow::anyhow!("unsupported DTMF digit"))?;
            ensure!(mask & (1 << event) != 0, "DTMF digit was not negotiated");
            events[index] = event as u8;
        }
        self.queue
            .extend(events[..digits.len()].iter().map(|event| Digit {
                event: *event,
                duration_ms,
            }));
        self.status.accepted_digits += digits.len() as u64;
        self.status.queued_digits += digits.len();
        self.status.state = "sending".into();
        if self.next.is_none() {
            self.next = Some(self.available_at.map_or(now, |due| due.max(now)) + STEP);
        }
        Ok(())
    }
    /// 每次调度只造一个小包；过期直接失败，禁止赶进度突发补发并伪装正常时长。
    pub fn packet(&mut self, payload: u8, clock: &LocalRtpClock, now: Instant) -> Option<[u8; 16]> {
        let start_timestamp = clock.timestamp(self.next? - STEP);
        let mut packet = self.packet_on_timeline(payload, clock.clock, start_timestamp, now)?;
        let timestamp = u32::from_be_bytes(packet[4..8].try_into().unwrap());
        clock.stamp(&mut packet, timestamp);
        Some(packet)
    }
    /// 新图仅借用事件载荷和恒定起点，RTP序号/SSRC由图统一分配；旧透传入口仍沿用LocalRtpClock。
    pub fn packet_on_timeline(
        &mut self,
        payload: u8,
        clock_rate: u32,
        start_timestamp: u32,
        now: Instant,
    ) -> Option<[u8; 16]> {
        let due = self.next?;
        if now.saturating_duration_since(due) >= STEP {
            self.fail("dtmf_send_scheduler_late");
            return None;
        }
        if self.active.is_none() {
            let digit = self.queue.pop_front()?;
            self.active = Some(Active {
                digit,
                start: due - STEP,
                timestamp: start_timestamp,
                elapsed_ms: 20,
                ends: 0,
            });
        }
        let active = self.active.as_ref().unwrap();
        let elapsed = active.elapsed_ms.min(active.digit.duration_ms);
        let ended = elapsed == active.digit.duration_ms;
        let ticks = (u64::from(elapsed) * u64::from(clock_rate) / 1000) as u16;
        let mut bytes = [0u8; 16];
        bytes[0] = 0x80;
        bytes[1] = payload
            | if active.elapsed_ms == 20 && active.ends == 0 {
                0x80
            } else {
                0
            };
        bytes[4..8].copy_from_slice(&active.timestamp.to_be_bytes());
        bytes[12] = active.digit.event;
        bytes[13] = 10 | if ended { 0x80 } else { 0 };
        bytes[14..16].copy_from_slice(&ticks.to_be_bytes());
        Some(bytes)
    }
    /// 三个结束包具有相同事件起点和最终 duration；相邻结束包仍使用新 RTP 序号。
    pub fn accepted(&mut self) {
        let active = self.active.as_mut().unwrap();
        self.status.sent_packets += 1;
        if active.elapsed_ms >= active.digit.duration_ms {
            active.ends += 1;
            if active.ends == 3 {
                self.status.completed_digits += 1;
                self.status.queued_digits -= 1;
                self.available_at = Some(
                    active.start + Duration::from_millis(u64::from(active.digit.duration_ms)) + GAP,
                );
                self.next = if self.queue.is_empty() {
                    None
                } else {
                    Some(
                        active.start
                            + Duration::from_millis(u64::from(active.digit.duration_ms))
                            + GAP
                            + STEP,
                    )
                };
                self.active = None;
                if self.next.is_none() {
                    self.status.state = "idle".into();
                }
                return;
            }
            self.next = Some(
                active.start
                    + Duration::from_millis(u64::from(active.digit.duration_ms))
                    + STEP * u32::from(active.ends),
            );
        } else {
            active.elapsed_ms = (active.elapsed_ms + 20).min(active.digit.duration_ms);
            self.next = Some(active.start + Duration::from_millis(u64::from(active.elapsed_ms)));
        }
    }
    /// 一次失效清掉全部剩余数字，累计失败数保留；新的显式请求可重新启动。
    pub fn fail(&mut self, message: &str) {
        self.status.failed_digits += self.status.queued_digits as u64;
        self.status.queued_digits = 0;
        self.status.last_error = message.into();
        self.status.state = "failed".into();
        self.queue.clear();
        self.active = None;
        self.next = None;
        self.available_at = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn admission_is_atomic_and_bounded() {
        let mut s = Sender::new();
        let now = Instant::now();
        assert!(s.enqueue("1X", 250, 0xffff, now).is_err());
        assert!(s.enqueue("12", 250, 2, now).is_err());
        assert_eq!(s.status.queued_digits, 0);
        s.enqueue(&"1".repeat(32), 250, 0xffff, now).unwrap();
        assert!(s.enqueue("2", 250, 0xffff, now).is_err());
        s.fail("test");
        assert_eq!(s.status.failed_digits, 32);
        assert!(s.next.is_none());
    }
    #[test]
    fn exact_duration_three_ends_and_sequence_wrap() {
        let start = Instant::now();
        let mut s = Sender::new();
        let mut clock = LocalRtpClock::new(start, u16::MAX, 33, 48000);
        s.enqueue("#", 55, 0xffff, start).unwrap();
        let mut durations = Vec::new();
        let mut ends = 0;
        let mut sequences = Vec::new();
        let mut instants = Vec::new();
        while let Some(due) = s.next {
            instants.push(due.duration_since(start).as_millis());
            let p = s.packet(110, &clock, due).unwrap();
            durations.push(u16::from_be_bytes([p[14], p[15]]));
            sequences.push(u16::from_be_bytes([p[2], p[3]]));
            assert_eq!(&p[4..8], &[0; 4]);
            ends += usize::from(p[13] & 0x80 != 0);
            s.accepted();
            clock.accepted();
        }
        assert_eq!(instants, vec![20, 40, 55, 75, 95]);
        assert_eq!(durations, vec![960, 1920, 2640, 2640, 2640]);
        assert_eq!(sequences, vec![65535, 0, 1, 2, 3]);
        assert_eq!(ends, 3);
        assert_eq!(s.status.completed_digits, 1);
    }

    #[test]
    fn late_tick_fails_instead_of_catching_up_in_same_turn() {
        let start = Instant::now();
        let mut s = Sender::new();
        let clock = LocalRtpClock::new(start, 0, 33, 8000);
        s.enqueue("1", 250, 0xffff, start).unwrap();
        assert!(s.packet(101, &clock, start + STEP).is_some());
        s.accepted();
        assert_eq!(s.next, Some(start + STEP * 2));
        assert!(s
            .packet(101, &clock, start + Duration::from_millis(79))
            .is_none());
        assert_eq!(s.status.failed_digits, 1);
        assert_eq!(s.status.sent_packets, 1);
        assert!(s.next.is_none());
    }
}
