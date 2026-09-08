//! 单worker独占的G.711图：双腿按原生PCM转码桥接，单腿消费入站PCM并编码本地播放。
//! 普通relay不进入本模块；本地能量节点不等于ASR/VAD，G.722/Opus实时图仍需后续接入。
use super::{
    jitter::{
        JitterBuffer, JitterConfig, JitterObservation, JitterObservationKind, JitterSourcePosition,
        Playout, PushOutcome,
    },
    protocol::{ProcessingPlan, ProcessingTopology, Stats},
    rtcp,
    rtp::PacketView,
    rtp_state::TxRtpState,
};
use crate::audio::{
    realtime::{DecodeResult, G711Codec},
    spec::{CodecSpec, MediaFormat},
    AudioFrameView, AudioPosition, DecodeInput, FrameOrigin, RtpPosition,
};
use anyhow::{ensure, Context, Result};
use std::time::{Duration, Instant, SystemTime};

#[cfg(test)]
#[path = "processed_tests.rs"]
mod tests;

#[path = "rx_observation.rs"]
mod rx_observation;
use rx_observation::ObservationQueue;
pub use rx_observation::{
    LocalObservation, LocalObservationData, LocalObservationKind, LocalObservationReason,
    LocalObservationStatus, LocalSourceBoundary, LocalSourcePosition, LOCAL_OBSERVATION_CAPACITY,
};

/// 接收回看128包之外，还保留辅助队列与事件交错余量；固定存储不随按键数量增长。
const DTMF_TIMESTAMP_HISTORY: usize = 256;
/// 接收预算必须在worker采集按键之前与图使用同一上限，防止先产生事件再拒绝报文。
pub const MAX_INPUT_PAYLOAD_BYTES: usize = 480;

#[derive(Clone, Copy)]
struct TimestampMapping {
    source_timestamp: u64,
    output_timestamp: u32,
}

/// 保存已发送事件的展开起点。同一事件的结束副本不能套用后来语音段的转换偏移。
struct DtmfTimestamps {
    entries: [TimestampMapping; DTMF_TIMESTAMP_HISTORY],
    source: Option<u32>,
    latest: Option<u64>,
    valid: usize,
    next: usize,
}

impl DtmfTimestamps {
    fn new() -> Self {
        Self {
            entries: [TimestampMapping {
                source_timestamp: 0,
                output_timestamp: 0,
            }; DTMF_TIMESTAMP_HISTORY],
            source: None,
            latest: None,
            valid: 0,
            next: 0,
        }
    }
    fn clear(&mut self) {
        // valid界定已初始化记录，重启无需清零整块历史，也不会再读上一来源的数据。
        self.source = None;
        self.latest = None;
        self.valid = 0;
        self.next = 0;
    }
    fn translate(&mut self, ssrc: u32, timestamp: u64, offset: u32) -> Option<u32> {
        if self.source != Some(ssrc) {
            self.clear();
            self.source = Some(ssrc);
        }
        if let Some(previous) = self.entries[..self.valid]
            .iter()
            .find(|entry| entry.source_timestamp == timestamp)
        {
            return Some(previous.output_timestamp);
        }
        // RTP窗口约束包序，不约束事件寿命。已经淘汰或从未见过的旧起点不能猜测映射再重放。
        if self.latest.is_some_and(|latest| timestamp < latest) {
            return None;
        }
        let translated = (timestamp as u32).wrapping_add(offset);
        self.entries[self.next] = TimestampMapping {
            source_timestamp: timestamp,
            output_timestamp: translated,
        };
        self.next = (self.next + 1) % DTMF_TIMESTAMP_HISTORY;
        self.valid = (self.valid + 1).min(DTMF_TIMESTAMP_HISTORY);
        self.latest = Some(timestamp);
        Some(translated)
    }
}

/// Direction编号代表输入腿：0是A输入/B输出，1是B输入/A输出。
struct Direction {
    input_payload: u8,
    output_payload: u8,
    jitter: JitterBuffer,
    decoder: G711Codec,
    encoder: G711Codec,
    tx: TxRtpState,
    /// 只存固定转换偏移；源重启时根据已有输出与单调时刻重新锚定。
    timestamp_offset: u32,
    /// RTP展开位置映射到同一呼叫的单调PCM时间；重启保留此前时间并重新建立来源锚点。
    position_anchor: Option<(u64, u64)>,
    last_pcm: Option<(u64, Instant)>,
    /// 即使新talkspurt重锚时间，同一DTMF事件的结束重传仍沿用其第一次转换的起点。
    dtmf_timestamps: Option<Box<DtmfTimestamps>>,
    pcm: [i16; 480],
    encoded: [u8; 480],
    cname: String,
}

/// 两方向存储在建呼阶段准备；每帧输出借用worker临时缓冲，不创建线程或等待控制面。
pub struct Graph {
    pub plan: ProcessingPlan,
    directions: [Direction; 2],
    a_codec: CodecSpec,
    a_payload: u8,
    connected: Option<(u8, CodecSpec, Option<u8>, MediaFormat)>,
    a_aux: (Option<u8>, Option<u8>),
    /// 本地TX时钟独立于入站源重启；从Allocate起以8k时钟连续推进。
    local_epoch: Option<(Instant, u32)>,
    local_audio_at: Option<Instant>,
    /// 只保存最新PCM的元数据和能量；样本借用方向0固定缓冲，无队列或跨线程引用。
    local_input: Option<(AudioPosition, FrameOrigin, u64)>,
    /// 仅订阅时分配；worker必须take，旧getter绝不承担上行发送职责。
    local_observation: Option<Box<ObservationQueue>>,
}

impl From<JitterSourcePosition> for LocalSourcePosition {
    fn from(value: JitterSourcePosition) -> Self {
        Self {
            ssrc: value.ssrc,
            expanded_timestamp: value.timestamp,
            rtp_sequence: value.sequence,
            generation: value.generation,
            segment: value.segment,
        }
    }
}

/// 只映射实际消费元数据；音频位置缺少锚点时保持None，不用0伪造CN/按键的PCM起点。
fn local_observation(meta: JitterObservation, now: Instant, input: &[u8]) -> LocalObservation {
    let (kind, reason) = match meta.kind {
        JitterObservationKind::Audio => (LocalObservationKind::Decoded, None),
        JitterObservationKind::Missing => (
            LocalObservationKind::Missing,
            Some(LocalObservationReason::MissingPacket),
        ),
        JitterObservationKind::ComfortNoise => (
            LocalObservationKind::ComfortNoise,
            meta.transmission_expired
                .then_some(LocalObservationReason::AuxiliaryDeadline),
        ),
        JitterObservationKind::AudioExpired => (
            LocalObservationKind::LocalExpired,
            Some(LocalObservationReason::AudioDeadline),
        ),
        JitterObservationKind::Auxiliary => (LocalObservationKind::Auxiliary, None),
        JitterObservationKind::AuxiliaryExpired => (
            LocalObservationKind::AuxiliaryExpired,
            Some(LocalObservationReason::AuxiliaryDeadline),
        ),
        JitterObservationKind::StaleComfortNoise => (
            LocalObservationKind::AuxiliaryExpired,
            Some(LocalObservationReason::StaleComfortNoise),
        ),
        JitterObservationKind::ToneTimeout => (
            if meta.suspended_after {
                LocalObservationKind::InactiveSuspended
            } else {
                LocalObservationKind::AuxiliaryExpired
            },
            Some(LocalObservationReason::ToneTimeout),
        ),
    };
    let mut value = LocalObservation::empty(now, meta.source.into(), kind);
    value.arrival = meta.arrival;
    value.deadline = Some(meta.deadline);
    value.reason = reason;
    value.boundary = meta.new_segment.then_some(LocalSourceBoundary::NewSegment);
    value.suspended_after = meta.suspended_after;
    value.cn_applied = meta.cn_applied;
    value.marker = meta.marker;
    value.duration_samples = meta.audio_frames.map(|frames| frames.saturating_mul(160));
    if kind == LocalObservationKind::ComfortNoise {
        if meta.payload_len == 0 || meta.payload_len > 480 || input[0] & 0x80 != 0 {
            // jitter已知SID与codec有效SID是两个事实；畸形SID不发布成有效舒适噪声。
            value.kind = LocalObservationKind::AuxiliaryExpired;
            value.reason = Some(LocalObservationReason::InvalidComfortNoise);
        } else {
            let mut bytes = [0u8; 480];
            bytes[..meta.payload_len].copy_from_slice(&input[..meta.payload_len]);
            value.data = LocalObservationData::ComfortNoise {
                bytes,
                len: meta.payload_len as u16,
            };
        }
    }
    value
}

/// 校验当前真正实现的图能力；不把已存在的其他编码透传能力当作实时codec可用。
fn validate(codec: &CodecSpec, dtmf: Option<u8>, format: &MediaFormat) -> Result<()> {
    ensure!(
        matches!(codec.name.as_str(), "PCMA" | "PCMU")
            && codec.sample_rate == 8000
            && codec.rtp_clock_rate == 8000
            && codec.channels == 1
            && codec.ptime_ms == 20
            && codec.fmtp.is_empty(),
        "processed graph requires G711 8k mono 20ms"
    );
    ensure!(
        dtmf.is_none() || format.dtmf_clock_rate.unwrap_or(8000) == 8000,
        "processed DTMF requires 8k clock"
    );
    Ok(())
}

/// SplitMix64仅生成随机播种后的RTP标识，不用于密钥或身份认证。
fn mix(mut x: u64) -> u64 {
    x = (x ^ (x >> 30)).wrapping_mul(0xbf58476d1ce4e5b9);
    x = (x ^ (x >> 27)).wrapping_mul(0x94d049bb133111eb);
    x ^ (x >> 31)
}

impl Direction {
    fn media_position(&self, timestamp: u64) -> Option<u64> {
        let (base, ns) = self.position_anchor?;
        timestamp
            .checked_sub(base)?
            .checked_mul(125_000)?
            .checked_add(ns)
    }
    /// 只在实际消费新段首帧时处理边界，不能在push时让旧缓冲套用新时间线。
    fn begin_segment(&mut self, timestamp: u64, now: Instant) {
        let _ = self.decoder.reset();
        // RTP表示来源采样位置。同相位连续来源暂停后恢复，发送迟到本身不能改写整段映射。
        // 仅已建立PCM锚点且新段确实改变160ticks采样相位，才允许按播放时刻重新定时。
        let source_phase_changed = self
            .position_anchor
            .is_some_and(|(base, _)| timestamp.abs_diff(base) % 160 != 0);
        let projected = self.position_anchor.and_then(|(base, ns)| {
            timestamp
                .checked_sub(base)?
                .checked_mul(125_000)?
                .checked_add(ns)
        });
        if self.last_pcm.is_some_and(|(last, _)| {
            projected.is_none_or(|ns| ns < last.saturating_add(20_000_000))
        }) {
            self.position_anchor = None;
        }
        let tx = self.tx.snapshot();
        if let Some(at) = tx.clock_at {
            let elapsed =
                (now.saturating_duration_since(at).as_nanos() * 8000 / 1_000_000_000) as u32;
            let current = tx.clock_timestamp.wrapping_add(elapsed.max(160));
            let proposed = (timestamp as u32).wrapping_add(self.timestamp_offset);
            // 已发送媒体越过新来源位置时仍须前向重锚；新采样相位也保留原有新段保障。
            // 同相位暂停不按wallclock强行前移，否则183ms来源停顿会使余下帧永久偏移504ticks。
            if proposed.wrapping_sub(tx.clock_timestamp) as i32 <= 0
                || (source_phase_changed && (proposed.wrapping_sub(current) as i32) < -160)
            {
                self.timestamp_offset = current.wrapping_sub(timestamp as u32);
                self.position_anchor = None;
            }
        }
    }

    fn new(
        payload: u8,
        codec: &CodecSpec,
        dtmf: Option<u8>,
        format: &MediaFormat,
        seed: u64,
    ) -> Result<Self> {
        let value = mix(seed);
        let jitter = JitterBuffer::new(JitterConfig {
            clock_rate: codec.rtp_clock_rate,
            packet_ticks: 160,
            audio_payload: payload,
            dtmf_payload: dtmf,
            cn_payload: format.cn_payload,
            max_payload_bytes: MAX_INPUT_PAYLOAD_BYTES,
            target_delay: Duration::from_millis(40),
            max_delay: Duration::from_millis(120),
            capacity: 8,
        })?;
        let ssrc = (value as u32).max(1);
        Ok(Self {
            input_payload: payload,
            output_payload: payload,
            jitter,
            decoder: G711Codec::new(&codec.name)?,
            encoder: G711Codec::new(&codec.name)?,
            tx: TxRtpState::new(ssrc, (value >> 32) as u16, codec.rtp_clock_rate)?,
            timestamp_offset: mix(value) as u32,
            position_anchor: None,
            last_pcm: None,
            // 未协商按键的媒体腿不承担历史空间；协商阶段一次分配，逐包处理只复用。
            dtmf_timestamps: dtmf.map(|_| Box::new(DtmfTimestamps::new())),
            pcm: [0; 480],
            encoded: [0; 480],
            cname: format!("{ssrc:08x}@rustswitch"),
        })
    }
}

impl Graph {
    /// 建呼即分配两个方向的上限资源，之后B腿接线仅替换未激活方向的状态。
    pub fn new(
        plan: ProcessingPlan,
        payload: u8,
        codec: &CodecSpec,
        dtmf: Option<u8>,
        format: &MediaFormat,
        seed: u64,
        now: Instant,
    ) -> Result<Self> {
        ensure!(
            plan.version == 1
                && plan.mode == "g711"
                && plan.jitter_target_ms == 40
                && plan.max_delay_ms == 120,
            "unsupported processing plan"
        );
        validate(codec, dtmf, format)?;
        let local = plan.topology == ProcessingTopology::Local;
        let mut directions = [
            Direction::new(payload, codec, dtmf, format, seed)?,
            Direction::new(payload, codec, dtmf, format, seed.wrapping_add(1))?,
        ];
        let local_epoch = local.then_some((now, directions[1].timestamp_offset));
        if let Some((at, timestamp)) = local_epoch {
            directions[1].tx.note_clock(timestamp, at);
        }
        Ok(Self {
            plan,
            directions,
            a_codec: codec.clone(),
            a_payload: payload,
            connected: None,
            a_aux: (dtmf, format.cn_payload),
            local_epoch,
            local_audio_at: None,
            local_input: None,
            local_observation: None,
        })
    }

    /// 接通后主音频参数不可变；辅助协商可收紧或恢复，均不重置RTP时间线。
    pub fn connect(
        &mut self,
        payload: u8,
        codec: &CodecSpec,
        dtmf: Option<u8>,
        format: &MediaFormat,
        stats: &mut Stats,
    ) -> Result<()> {
        ensure!(
            !self.is_local(),
            "local processed topology cannot connect a B leg"
        );
        validate(codec, dtmf, format)?;
        let desired = (payload, codec.clone(), dtmf, format.clone());
        if let Some(previous) = &self.connected {
            ensure!(
                previous.0 == payload && previous.1 == *codec,
                "processed audio plan cannot change after connect"
            );
            if *previous != desired {
                // 183可临时省略辅助格式，200仍按原报价恢复；只改辅助接纳集合，不重启主音频。
                for d in &mut self.directions {
                    let before = *d.jitter.stats();
                    d.jitter.set_aux_payloads(dtmf, format.cn_payload)?;
                    if dtmf.is_some() && d.dtmf_timestamps.is_none() {
                        d.dtmf_timestamps = Some(Box::new(DtmfTimestamps::new()));
                    }
                    accumulate_jitter(stats, &before, d.jitter.stats());
                }
                self.connected = Some(desired);
            }
            return Ok(());
        }
        // 两方向的网络身份来自原先随机播种；这里仍在建呼控制操作，不在音频热路径。
        let mut a_format = format.clone();
        a_format.codec = Some(self.a_codec.clone());
        let left_seed = self.directions[0].tx.ssrc() as u64;
        let right_seed = self.directions[1].tx.ssrc() as u64;
        let mut left = Direction::new(self.a_payload, &self.a_codec, dtmf, &a_format, left_seed)?;
        let mut right = Direction::new(payload, codec, dtmf, format, right_seed)?;
        left.output_payload = payload;
        left.encoder = G711Codec::new(&codec.name)?;
        right.output_payload = self.a_payload;
        right.encoder = G711Codec::new(&self.a_codec.name)?;
        self.directions = [left, right];
        self.connected = Some(desired);
        Ok(())
    }
    /// 按实际输入腿PT校验，不能用A腿PT检查B腿转码输入。
    pub fn input_payload(&self, direction: usize) -> u8 {
        self.directions[direction].input_payload
    }
    pub fn is_local(&self) -> bool {
        self.plan.topology == ProcessingTopology::Local
    }

    /// 停止旧播放后立即开始的新任务，也不能覆盖上一已发送20ms帧的采样区间。
    pub fn next_local_audio_at(&self, now: Instant) -> Instant {
        self.local_audio_at
            .map_or(now, |at| now.max(at + Duration::from_millis(20)))
    }

    /// 本地事件的起点由发送器计划时刻给定；结束副本不重新计算事件时间戳。
    pub fn local_timestamp(&self, at: Instant) -> Result<u32> {
        let (epoch, base) = self.local_epoch.context("not a local processed graph")?;
        Ok(base.wrapping_add(
            (at.saturating_duration_since(epoch).as_nanos() * 8000 / 1_000_000_000) as u32,
        ))
    }

    /// 暴露有界本地消费结果供同worker节点借用；CN/missing没有帧，不冒充静音PCM。
    pub fn local_input_frame(&self) -> Option<(AudioFrameView<'_>, u64)> {
        let (position, origin, energy) = self.local_input?;
        Some((
            AudioFrameView::new(&self.directions[0].pcm[..160], 8000, position, origin).ok()?,
            energy,
        ))
    }

    /// 只允许本地A输入订阅。重复true不重放、不清队列、不复活失败订阅。
    /// false显式撤销并释放队列；外部订阅ID/终止回执由worker传输层管理。
    pub fn enable_local_observation(&mut self, enabled: bool) -> Result<()> {
        ensure!(
            self.is_local(),
            "RX observation requires local processed topology"
        );
        if !enabled {
            self.local_observation = None;
        } else if self.local_observation.is_none() {
            self.local_observation = Some(Box::new(ObservationQueue::new()));
        }
        Ok(())
    }

    pub fn take_local_observation(&mut self) -> Option<LocalObservation> {
        self.local_observation.as_mut()?.take()
    }

    pub fn local_observation_status(&self) -> Option<LocalObservationStatus> {
        self.local_observation.as_ref().map(|queue| queue.status())
    }

    pub fn local_observation_storage_bytes(&self) -> usize {
        if self.local_observation.is_some() {
            std::mem::size_of::<ObservationQueue>()
        } else {
            0
        }
    }

    /// tone/WAV直接以原PCM进编码节点，不经过先G711编码再解码的伪接图。
    pub fn encode_local_pcm(
        &mut self,
        samples: &[i16],
        at: Instant,
        marker: bool,
        wire: &mut [u8],
        stats: &mut Stats,
    ) -> Result<usize> {
        let (epoch, _) = self.local_epoch.context("not a local processed graph")?;
        ensure!(
            samples.len() == 160 && wire.len() >= 172 && at >= epoch,
            "invalid local PCM frame or output buffer"
        );
        ensure!(
            self.local_audio_at
                .is_none_or(|previous| at >= previous + Duration::from_millis(20)),
            "local PCM frames overlap"
        );
        let timestamp = self.local_timestamp(at)?;
        let ns = u64::try_from(at.duration_since(epoch).as_nanos())?;
        let frame = AudioFrameView::new(
            samples,
            8000,
            AudioPosition::new(ns, None),
            FrameOrigin::SuppliedPcm,
        )?;
        let d = &mut self.directions[1];
        let len = d.encoder.encode_frame(&frame, &mut d.encoded)?;
        let len =
            d.tx.write_packet(d.output_payload, marker, timestamp, &d.encoded[..len], wire)?;
        self.local_audio_at = Some(at);
        stats.processed_encoded_frames += 1;
        stats.processed_encoded_samples += 160;
        Ok(len)
    }

    /// 主动电话事件共用本地TX的序号与SSRC；不进入音频codec或增加encoded计数。
    pub fn write_local_event(&mut self, packet: &[u8; 16], wire: &mut [u8]) -> Result<usize> {
        ensure!(
            self.is_local() && self.a_aux.0 == Some(packet[1] & 127),
            "local telephone-event was not negotiated"
        );
        let timestamp = u32::from_be_bytes(packet[4..8].try_into().unwrap());
        self.directions[1].tx.write_packet(
            packet[1] & 127,
            packet[1] & 128 != 0,
            timestamp,
            &packet[12..],
            wire,
        )
    }
    pub fn next_deadline(&self, direction: usize) -> Option<Instant> {
        self.directions[direction].jitter.next_deadline()
    }

    /// 源地址/包率/载荷长度由worker先校验；这里只推进该方向的RTP与有界重排状态。
    pub fn push(&mut self, direction: usize, bytes: &[u8], now: Instant, stats: &mut Stats) {
        if self.is_local() && direction != 0 {
            return;
        }
        let Some(packet) = PacketView::parse(bytes) else {
            return;
        };
        let d = &mut self.directions[direction];
        let before = *d.jitter.stats();
        let rx_before = *d.jitter.rx().stats();
        let source_before = d.jitter.rx().ssrc();
        let pushed = d.jitter.push(packet, now);
        let rx = d.jitter.rx().stats();
        let boundary = if rx.source_resets != rx_before.source_resets {
            Some(LocalSourceBoundary::SourceChanged)
        } else if rx.sequence_resets != rx_before.sequence_resets {
            Some(LocalSourceBoundary::SequenceRestart)
        } else if rx.timestamp_resets != rx_before.timestamp_resets {
            Some(LocalSourceBoundary::TimestampRestart)
        } else if source_before.is_none() && d.jitter.rx().ssrc().is_some() {
            Some(LocalSourceBoundary::InitialSource)
        } else {
            None
        };
        if rx.source_resets != rx_before.source_resets
            || rx.sequence_resets != rx_before.sequence_resets
            || rx.timestamp_resets != rx_before.timestamp_resets
        {
            self.local_input = None;
            let _ = d.decoder.reset();
            d.position_anchor = None;
            if let Some(history) = &mut d.dtmf_timestamps {
                history.clear();
            }
            let tx = d.tx.snapshot();
            if let Some(last) = tx.clock_at {
                let ticks =
                    (now.saturating_duration_since(last).as_nanos() * 8000 / 1_000_000_000) as u32;
                d.timestamp_offset = tx
                    .clock_timestamp
                    .wrapping_add(ticks.max(160))
                    .wrapping_sub(packet.timestamp);
            }
        }
        if let (Some(boundary), Some(queue), Some(source)) = (
            boundary,
            &mut self.local_observation,
            d.jitter.last_received(),
        ) {
            if queue.accepting() {
                let mut value = LocalObservation::empty(
                    now,
                    source.into(),
                    LocalObservationKind::SourceBoundary,
                );
                value.arrival = Some(now);
                value.boundary = Some(boundary);
                value.discarded_packets =
                    d.jitter.stats().queue_reset_drops - before.queue_reset_drops;
                queue.push(value);
            }
        }
        // 编码接收缓冲的资源拒绝不能在上行中伪装网络丢包；只记录RX真实接受的这一个包。
        if matches!(pushed, PushOutcome::Overflow | PushOutcome::Late)
            && d.jitter.rx().stats().accepted_packets > rx_before.accepted_packets
        {
            if let (Some(queue), Some(source)) =
                (&mut self.local_observation, d.jitter.last_received())
            {
                if queue.accepting() {
                    let audio = packet.payload_type == d.input_payload;
                    let mut value = LocalObservation::empty(
                        now,
                        source.into(),
                        if audio {
                            LocalObservationKind::LocalExpired
                        } else {
                            LocalObservationKind::AuxiliaryExpired
                        },
                    );
                    value.reason = Some(if pushed == PushOutcome::Overflow {
                        LocalObservationReason::JitterBufferLimit
                    } else {
                        LocalObservationReason::LateArrival
                    });
                    value.arrival = Some(now);
                    value.duration_samples = audio.then_some(160);
                    value.discarded_packets = 1;
                    value.media_time_ns = source.timestamp.and_then(|ts| d.media_position(ts));
                    queue.push(value);
                }
            }
        }
        accumulate_jitter(stats, &before, d.jitter.stats());
    }

    /// 一次只处理一个到期事件；缺失/过期绝不返回伪造静音包。
    pub fn advance(
        &mut self,
        direction: usize,
        now: Instant,
        wire: &mut [u8],
        stats: &mut Stats,
    ) -> Option<usize> {
        let local = self.is_local();
        if local && direction != 0 {
            return None;
        }
        let aux = if local {
            self.a_aux
        } else {
            self.connected
                .as_ref()
                .map_or((None, None), |x| (x.2, x.3.cn_payload))
        };
        let d = &mut self.directions[direction];
        let mut input = [0u8; 480];
        let before = *d.jitter.stats();
        let observing = local
            && self
                .local_observation
                .as_ref()
                .is_some_and(|queue| queue.accepting());
        let (event, meta) = if observing {
            d.jitter.pop_due_observed(now, &mut input)
        } else {
            (d.jitter.pop_due(now, &mut input), None)
        };
        accumulate_jitter(stats, &before, d.jitter.stats());
        let event = event?;
        let mut observed = meta.map(|meta| {
            let mut value = local_observation(meta, now, &input);
            value.media_time_ns = value
                .source
                .expanded_timestamp
                .and_then(|ts| d.media_position(ts));
            value
        });
        if local {
            self.local_input = None;
        }
        let (timestamp, marker, decoded) = match event {
            Playout::Audio {
                timestamp,
                len,
                marker,
                new_segment,
                ..
            } => {
                if new_segment {
                    d.begin_segment(timestamp, now);
                }
                let result =
                    d.decoder
                        .decode_into(DecodeInput::Packet(&input[..len]), 20, &mut d.pcm);
                if let Ok(DecodeResult::Pcm { samples, .. }) = &result {
                    stats.processed_decoded_frames += 1;
                    stats.processed_decoded_samples += *samples as u64;
                }
                (timestamp, marker, result)
            }
            Playout::Missing { timestamp, .. } => {
                let result = d
                    .decoder
                    .decode_into(DecodeInput::PacketLoss, 20, &mut d.pcm);
                if matches!(result, Ok(DecodeResult::Pcm { .. })) {
                    stats.processed_plc_frames += 1;
                } else {
                    stats.processed_missing_frames += 1;
                }
                (timestamp, false, result)
            }
            Playout::Aux {
                payload_type,
                timestamp,
                ssrc,
                len,
                marker,
                ..
            } => {
                if Some(payload_type) == aux.1 {
                    let _ = d.decoder.decode_into(
                        DecodeInput::ComfortNoise(&input[..len]),
                        20,
                        &mut d.pcm,
                    );
                    stats.processed_cn_packets += 1;
                }
                // 本地CN只结束旧PLC历史，DTMF已由worker采集；任何辅助输入都不得反射给呼叫者。
                if local {
                    if let Some(value) = observed {
                        self.local_observation.as_mut().unwrap().push(value);
                    }
                    return None;
                }
                let translated =
                    if aux.0 == Some(payload_type) {
                        let extended = d.jitter.rx().extend_timestamp(timestamp);
                        let Some(mapped) = d.dtmf_timestamps.as_mut().and_then(|history| {
                            history.translate(ssrc, extended, d.timestamp_offset)
                        }) else {
                            stats.processed_aux_expired += 1;
                            return None;
                        };
                        mapped
                    } else {
                        timestamp.wrapping_add(d.timestamp_offset)
                    };
                return d
                    .tx
                    .write_packet(payload_type, marker, translated, &input[..len], wire)
                    .ok();
            }
            Playout::Expired { skipped } => {
                if skipped > 0 {
                    let _ = d.decoder.reset();
                }
                if let Some(value) = observed {
                    self.local_observation.as_mut().unwrap().push(value);
                }
                return None;
            }
        };
        let Ok(DecodeResult::Pcm {
            samples,
            info,
            origin,
        }) = decoded
        else {
            if let Some(mut value) = observed {
                value.kind = LocalObservationKind::Missing;
                if decoded.is_err() {
                    value.reason = Some(LocalObservationReason::DecoderFailure);
                }
                self.local_observation.as_mut().unwrap().push(value);
            }
            return None;
        };
        let (base_rtp, base_ns) = *d.position_anchor.get_or_insert_with(|| {
            let ns = d.last_pcm.map_or(0, |(last_ns, last_at)| {
                last_ns.saturating_add(
                    now.saturating_duration_since(last_at)
                        .as_nanos()
                        // 重锚首帧不能与上一20ms PCM区间重叠，即使辅助流换相位后期限相距更短。
                        .max(20_000_000)
                        .min(u128::from(u64::MAX)) as u64,
                )
            });
            (timestamp, ns)
        });
        let media_ns = timestamp
            .checked_sub(base_rtp)?
            .checked_mul(125_000)?
            .checked_add(base_ns)?;
        let position = AudioPosition::new(
            media_ns,
            Some(RtpPosition::new(timestamp as u32, info.rtp_clock_rate).ok()?),
        );
        let frame =
            AudioFrameView::new(&d.pcm[..samples], info.sample_rate, position, origin).ok()?;
        if local {
            // 消费全部160个原生样本并保留真实来源；平方和不是VAD，PLC也不能标成真实收到。
            let energy = frame
                .samples()
                .iter()
                .map(|sample| {
                    let value = i64::from(*sample);
                    (value * value) as u64
                })
                .sum();
            self.local_input = Some((frame.position(), frame.origin(), energy));
            d.last_pcm = Some((media_ns, now));
            if let Some(mut value) = observed.take() {
                value.kind = if frame.origin() == FrameOrigin::HistoryConcealment {
                    LocalObservationKind::HistoryPlc
                } else {
                    LocalObservationKind::Decoded
                };
                value.media_time_ns = Some(media_ns);
                value.data = LocalObservationData::Pcm(d.pcm[..160].try_into().unwrap());
                self.local_observation.as_mut().unwrap().push(value);
            }
            let (consumed, energy) = self.local_input_frame()?;
            stats.processed_local_consumed_frames += 1;
            stats.processed_local_consumed_samples += consumed.valid_samples() as u64;
            stats.processed_local_nonzero_frames += u64::from(energy > 0);
            stats.processed_local_energy_max = stats.processed_local_energy_max.max(energy);
            return None;
        }
        let Ok(len) = d.encoder.encode_frame(&frame, &mut d.encoded) else {
            stats.processed_send_errors += 1;
            return None;
        };
        d.last_pcm = Some((media_ns, now));
        stats.processed_encoded_frames += 1;
        stats.processed_encoded_samples += samples as u64;
        d.tx.write_packet(
            d.output_payload,
            marker,
            (timestamp as u32).wrapping_add(d.timestamp_offset),
            &d.encoded[..len],
            wire,
        )
        .ok()
    }

    /// 包序在准备包时已消耗，这里只累计真实UDP接受结果，不因失败复用不同内容的序号。
    pub fn note_send(&mut self, direction: usize, packet: &[u8], now: Instant, accepted: bool) {
        let local = self.is_local();
        let d = &mut self.directions[direction];
        let tx = &mut d.tx;
        if accepted {
            let timestamp = u32::from_be_bytes(packet[4..8].try_into().unwrap());
            let first = tx.snapshot().sent_packets == 0;
            tx.note_sent(packet.len() - 12, timestamp, now);
            // 本地时钟从Allocate建立，独立于发送抖动和入站SSRC；所有生成源共享这一条线。
            if local {
                return;
            }
            if packet[1] & 127 == d.output_payload
                || self.connected.as_ref().and_then(|x| x.3.cn_payload) == Some(packet[1] & 127)
            {
                let clock = tx.snapshot();
                let current = clock.clock_at.map(|at| {
                    clock.clock_timestamp.wrapping_add(
                        (now.saturating_duration_since(at).as_nanos() * 8000 / 1_000_000_000)
                            as u32,
                    )
                });
                // RTP包携带的是采样起点，可比发送时刻早；迟到CN或普通发送抖动不能倒推SR时钟。
                if first || current.is_none_or(|value| timestamp.wrapping_sub(value) as i32 >= 0) {
                    tx.note_clock(timestamp, now);
                }
            } else if first {
                // 首包若是DTMF，event起点已在过去；用duration对齐当前媒体时刻而非反复使用恒定起点。
                let is_event = self.connected.as_ref().and_then(|x| x.2) == Some(packet[1] & 127);
                let ticks = if is_event && packet.len() >= 16 {
                    u32::from(u16::from_be_bytes([packet[14], packet[15]]))
                } else {
                    0
                };
                tx.note_clock(timestamp.wrapping_add(ticks), now);
            }
        } else {
            tx.note_failed();
        }
    }
    /// RTCP只更新本腿接收状态；不转发属于原端点SSRC的报告。
    pub fn observe_rtcp(
        &mut self,
        leg: usize,
        bytes: &[u8],
        now: Instant,
        stats: &mut Stats,
    ) -> Result<()> {
        let observation = rtcp::observe(bytes, self.directions[leg].jitter.rx_mut(), now)?;
        if observation.remote_bye {
            self.local_input = None;
            let before = *self.directions[leg].jitter.stats();
            let source = self.directions[leg].jitter.last_received();
            self.directions[leg].jitter.rx_mut().end_source();
            self.directions[leg].jitter.clear();
            self.directions[leg].decoder.reset()?;
            self.directions[leg].position_anchor = None;
            accumulate_jitter(stats, &before, self.directions[leg].jitter.stats());
            if leg == 0 {
                if let (Some(queue), Some(source)) = (&mut self.local_observation, source) {
                    if queue.accepting() {
                        let mut value = LocalObservation::empty(
                            now,
                            source.into(),
                            LocalObservationKind::SourceBoundary,
                        );
                        value.source.rtp_sequence = None;
                        value.boundary = Some(LocalSourceBoundary::SourceEnded);
                        value.discarded_packets = before.depth as u64;
                        value.suspended_after = true;
                        queue.push(value);
                    }
                }
            }
        }
        Ok(())
    }
    /// 向某腿报告时，TX来自反向图，RX来自该腿输入，保持源与包数对应。
    pub fn report(&mut self, leg: usize, now: Instant, bye: bool, out: &mut [u8]) -> Result<usize> {
        ensure!(
            !self.is_local() || leg == 0,
            "local RTCP supports A leg only"
        );
        let rx = self.directions[leg].jitter.rx_mut().report(now);
        let d = &self.directions[leg ^ 1];
        if bye {
            rtcp::write_bye(&d.tx, rx, now, SystemTime::now(), &d.cname, out)
        } else {
            rtcp::write_report(&d.tx, rx, now, SystemTime::now(), &d.cname, out)
        }
    }
}

/// 在每次真实push/playout的局部差量累加全局值；会话释放不抹除已发生的质量问题。
fn accumulate_jitter(
    stats: &mut Stats,
    before: &super::jitter::JitterStats,
    after: &super::jitter::JitterStats,
) {
    stats.processed_jitter_lost += after.lost - before.lost;
    stats.processed_jitter_late += after.late - before.late;
    stats.processed_jitter_reordered += after.reordered - before.reordered;
    stats.processed_jitter_duplicates += after.duplicates - before.duplicates;
    stats.processed_jitter_overflow += after.overflow - before.overflow;
    stats.processed_playout_expired += after.expired - before.expired;
    stats.processed_aux_expired += after.aux_expired - before.aux_expired;
    stats.processed_aux_timeouts += after.aux_timeouts - before.aux_timeouts;
    stats.processed_queue_reset_drops += after.queue_reset_drops - before.queue_reset_drops;
    stats.processed_source_resets += after.resets - before.resets;
    stats.invalid_packets += after.invalid - before.invalid;
}
