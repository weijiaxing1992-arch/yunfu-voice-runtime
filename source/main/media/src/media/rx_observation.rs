//! 本地A腿一次性消费观察；此队列不拥有网络、ASR或媒体发送权限。
use std::time::Instant;

pub const LOCAL_OBSERVATION_CAPACITY: usize = 8;

/// 来源位置完全来自接收状态和实际jitter槽；缺失包没有可证明的RTP包序。
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LocalSourcePosition {
    pub ssrc: Option<u32>,
    pub expanded_timestamp: Option<u64>,
    pub rtp_sequence: Option<u64>,
    pub generation: u64,
    pub segment: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LocalObservationKind {
    Decoded,
    HistoryPlc,
    Missing,
    ComfortNoise,
    LocalExpired,
    Auxiliary,
    AuxiliaryExpired,
    SourceBoundary,
    InactiveSuspended,
    Failed,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LocalSourceBoundary {
    InitialSource,
    SourceChanged,
    SequenceRestart,
    TimestampRestart,
    SourceEnded,
    NewSegment,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LocalObservationReason {
    MissingPacket,
    AudioDeadline,
    JitterBufferLimit,
    LateArrival,
    AuxiliaryDeadline,
    StaleComfortNoise,
    InvalidComfortNoise,
    ToneTimeout,
    DecoderFailure,
    ObservationOverflow,
    ObservationSequenceExhausted,
}

/// 联合存储避免每个槽同时承担PCM和SID数组；PCM只有完整160样本。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LocalObservationData {
    None,
    Pcm([i16; 160]),
    ComfortNoise { bytes: [u8; 480], len: u16 },
}

/// 每条记录只可take一次。Instant仅在该worker内可比，不能直接当跨进程时间戳。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct LocalObservation {
    pub sequence: u64,
    pub observed_at: Instant,
    pub source: LocalSourcePosition,
    pub media_time_ns: Option<u64>,
    pub arrival: Option<Instant>,
    pub deadline: Option<Instant>,
    pub kind: LocalObservationKind,
    pub reason: Option<LocalObservationReason>,
    pub boundary: Option<LocalSourceBoundary>,
    /// 本条实际消费导致有限尾部暂停；不重复产生第二个PCM或空白帧。
    pub suspended_after: bool,
    /// 即使辅助转发已过期，实际CN仍可能改变jitter抑制状态。
    pub cn_applied: bool,
    pub marker: bool,
    pub rtp_clock_rate: u32,
    pub sample_rate: u32,
    pub duration_samples: Option<u64>,
    /// 已确认源重启清掉的编码包数量，不推测其中有多少音频样本。
    pub discarded_packets: u64,
    pub data: LocalObservationData,
}

impl LocalObservation {
    pub(super) fn empty(
        observed_at: Instant,
        source: LocalSourcePosition,
        kind: LocalObservationKind,
    ) -> Self {
        Self {
            sequence: 0,
            observed_at,
            source,
            media_time_ns: None,
            arrival: None,
            deadline: None,
            kind,
            reason: None,
            boundary: None,
            suspended_after: false,
            cn_applied: false,
            marker: false,
            rtp_clock_rate: 8000,
            sample_rate: 8000,
            duration_samples: None,
            discarded_packets: 0,
            data: LocalObservationData::None,
        }
    }

    pub fn samples(&self) -> &[i16] {
        match &self.data {
            LocalObservationData::Pcm(samples) => samples,
            _ => &[],
        }
    }

    pub fn sid(&self) -> &[u8] {
        match &self.data {
            LocalObservationData::ComfortNoise { bytes, len } => &bytes[..usize::from(*len)],
            _ => &[],
        }
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LocalObservationStatus {
    /// 包含首次无法入队的观察；失败后本订阅不再接收新事件。
    pub produced: u64,
    /// 已由worker取走的条数，不代表socket接受或ASR消费。
    pub taken: u64,
    pub queued: usize,
    pub peak_queued: usize,
    pub failed: bool,
    pub failure: Option<LocalObservationReason>,
    pub last_sequence: u64,
}

/// 正常槽只有8项；第9项只保留无PCM的失败终态，队满不会静默覆盖旧记录。
pub(super) struct ObservationQueue {
    slots: [Option<LocalObservation>; LOCAL_OBSERVATION_CAPACITY],
    head: usize,
    len: usize,
    terminal: Option<LocalObservation>,
    status: LocalObservationStatus,
}

impl ObservationQueue {
    pub(super) fn new() -> Self {
        Self {
            slots: [None; LOCAL_OBSERVATION_CAPACITY],
            head: 0,
            len: 0,
            terminal: None,
            status: LocalObservationStatus::default(),
        }
    }

    pub(super) fn accepting(&self) -> bool {
        !self.status.failed
    }

    pub(super) fn push(&mut self, mut value: LocalObservation) {
        if self.status.failed {
            return;
        }
        let next = self.status.last_sequence.checked_add(1);
        self.status.produced = self.status.produced.saturating_add(1);
        value.sequence = next.unwrap_or(u64::MAX);
        self.status.last_sequence = value.sequence;
        if self.len == LOCAL_OBSERVATION_CAPACITY || next.is_none_or(|value| value == u64::MAX) {
            let reason = if next.is_none_or(|value| value == u64::MAX) {
                LocalObservationReason::ObservationSequenceExhausted
            } else {
                LocalObservationReason::ObservationOverflow
            };
            value.kind = LocalObservationKind::Failed;
            value.reason = Some(reason);
            value.data = LocalObservationData::None;
            self.status.failed = true;
            self.status.failure = Some(reason);
            self.terminal = Some(value);
            self.status.peak_queued = self.status.peak_queued.max(self.len + 1);
            return;
        }
        self.slots[(self.head + self.len) % LOCAL_OBSERVATION_CAPACITY] = Some(value);
        self.len += 1;
        self.status.peak_queued = self.status.peak_queued.max(self.len);
    }

    pub(super) fn take(&mut self) -> Option<LocalObservation> {
        let value = if self.len > 0 {
            let value = self.slots[self.head].take();
            self.head = (self.head + 1) % LOCAL_OBSERVATION_CAPACITY;
            self.len -= 1;
            value
        } else {
            self.terminal.take()
        };
        if value.is_some() {
            self.status.taken = self.status.taken.saturating_add(1);
        }
        value
    }

    pub(super) fn status(&self) -> LocalObservationStatus {
        LocalObservationStatus {
            queued: self.len + usize::from(self.terminal.is_some()),
            ..self.status
        }
    }
}
