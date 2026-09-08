//! 上行观察在所属媒体worker内消费；所有网络写均非阻塞且单轮有预算。
use super::*;
use crate::media::processed::{
    LocalObservation, LocalObservationKind as K, LocalObservationReason as R,
    LocalSourceBoundary as B,
};
use crate::media::rx_export::{Kind, Record, Send, MAX_WIRE};

/// 显式映射候选wire枚举，不依赖Rust枚举布局，也不把没有位置的事件补成零位置。
fn encode_observation(value: LocalObservation) -> Record {
    let kind = match value.kind {
        K::Decoded => Kind::Decoded,
        K::HistoryPlc => Kind::HistoryPlc,
        K::Missing => Kind::Missing,
        K::ComfortNoise => Kind::ComfortNoise,
        K::LocalExpired => Kind::LocalExpired,
        K::Auxiliary => Kind::Auxiliary,
        K::AuxiliaryExpired => Kind::AuxiliaryExpired,
        K::SourceBoundary => Kind::SourceBoundary,
        K::InactiveSuspended => Kind::InactiveSuspended,
        K::Failed => Kind::ObservationFailed,
    };
    let mut r = Record::empty(kind);
    r.source_generation = value.source.generation;
    r.source_segment = value.source.segment;
    if let Some(x) = value.source.ssrc {
        r.flags |= 1;
        r.ssrc = x;
    }
    if let Some(x) = value.source.expanded_timestamp {
        r.flags |= 2;
        r.rtp_timestamp = x;
    }
    if let Some(x) = value.source.rtp_sequence {
        r.flags |= 4;
        r.rtp_sequence = x;
    }
    if let Some(x) = value.media_time_ns {
        r.flags |= 8;
        r.media_time_ns = x;
    }
    r.boundary = match value.boundary {
        None => 0,
        Some(B::InitialSource) => 1,
        Some(B::SourceChanged) => 2,
        Some(B::SequenceRestart) => 3,
        Some(B::TimestampRestart) => 4,
        Some(B::NewSegment) => 5,
        Some(B::SourceEnded) => 6,
    };
    if value.boundary.is_some() {
        r.flags |= 16;
    }
    if value.kind == K::ComfortNoise && value.reason == Some(R::AuxiliaryDeadline) {
        r.flags |= 32;
    }
    if value.suspended_after {
        r.flags |= 64;
    }
    if value.marker {
        r.flags |= 128;
    }
    if value.arrival.is_some() {
        r.flags |= 256;
    }
    if value.deadline.is_some() {
        r.flags |= 512;
    }
    r.reason = match value.reason {
        None => 0,
        Some(R::MissingPacket) => 1,
        Some(R::AudioDeadline) => 2,
        Some(R::AuxiliaryDeadline) => 3,
        Some(R::StaleComfortNoise) => 4,
        Some(R::InvalidComfortNoise) => 5,
        Some(R::ToneTimeout) => 6,
        Some(R::DecoderFailure) => 7,
        Some(R::ObservationOverflow) => 8,
        Some(R::ObservationSequenceExhausted) => 9,
        Some(R::JitterBufferLimit) => 10,
        Some(R::LateArrival) => 11,
    };
    r.discarded_packets = value.discarded_packets;
    r.observed_at = Some(value.observed_at);
    r.arrival = value.arrival;
    r.deadline = value.deadline;
    r.cn_applied = value.cn_applied;
    // 当前合同两者均8k；按已知真实样本区间映射，不给CN/DTMF凭空填160ticks。
    r.duration_ticks = value.duration_samples.unwrap_or(0);
    if !value.samples().is_empty() {
        r.samples = value.samples().len() as u16;
        r.body_len = usize::from(r.samples) * 2;
        for (sample, pair) in value
            .samples()
            .iter()
            .zip(r.body[..r.body_len].chunks_exact_mut(2))
        {
            pair.copy_from_slice(&sample.to_le_bytes());
        }
    } else if !value.sid().is_empty() {
        r.body_len = value.sid().len();
        r.body[..r.body_len].copy_from_slice(value.sid());
    }
    r
}

/// push、playout和RTCP BYE后同轮取走新观察；不在空轮询中重复读取最近PCM getter。
pub(super) fn capture(call: &mut Session, slot: usize, timers: &mut Scheduler, now: Instant) {
    let Some(rx) = &mut call.rx_subscription else {
        return;
    };
    let Some(graph) = &mut call.processed else {
        return;
    };
    // 终态只由第一次终止路径安排发送；后续RTP不能重新调度已隔离lane的失败摘要。
    if !rx.active() {
        return;
    }
    if rx.active() {
        // Graph固定8槽加一个无PCM失败终态，单轮最多消费这个已知上限。
        for _ in 0..(crate::media::processed::LOCAL_OBSERVATION_CAPACITY + 1) {
            let Some(value) = graph.take_local_observation() else {
                break;
            };
            let failed = value.kind == K::Failed;
            rx.push(encode_observation(value), now);
            if failed {
                rx.fail("rx_observation_failed");
                break;
            }
        }
    }
    if !rx.active() {
        let _ = graph.enable_local_observation(false);
    }
    if rx.pending() {
        timers.set(slot, Some(now));
    }
}

impl Engine {
    pub(super) fn rx_subscribe(&mut self, session: u64, id: u64) -> Result<Response> {
        ensure!(
            session != 0 && id != 0,
            "RX session/subscription must be nonzero"
        );
        let slot = *self
            .session_slots
            .get(&session)
            .context("unknown media session")?;
        let call = self.sessions[slot].as_mut().unwrap();
        if let Some(old) = &call.rx_subscription {
            if old.id() == id {
                return Ok(Response::RxState {
                    session,
                    status: old.status(Instant::now()),
                });
            }
            ensure!(id > old.id(), "stale RX subscription id");
            ensure!(!old.active(), "previous RX subscription must be stopped");
        }
        ensure!(
            self.rx_lane.as_ref().is_some_and(RxLane::available),
            "RX lane unavailable"
        );
        ensure!(call.b.is_none(), "RX requires local A leg");
        let graph = call
            .processed
            .as_mut()
            .context("RX requires processed G711 local graph")?;
        ensure!(graph.is_local(), "RX requires local topology");
        // 时钟校准失败只拒绝本次订阅，在修改观察图之前完成，避免残留无人消费的队列。
        let subscription = RxSubscription::new(session, id)?;
        // 新ID有独立观察序列；旧终态已由控制面确认，不能复用原观察队列或旧音频。
        graph.enable_local_observation(false)?;
        graph.enable_local_observation(true)?;
        self.rx_timers.remove(slot);
        call.rx_subscription = Some(Box::new(subscription));
        Ok(Response::RxState {
            session,
            status: call
                .rx_subscription
                .as_ref()
                .unwrap()
                .status(Instant::now()),
        })
    }

    pub(super) fn rx_control(&mut self, session: u64, id: u64, stop: bool) -> Result<Response> {
        ensure!(
            session != 0 && id != 0,
            "RX session/subscription must be nonzero"
        );
        let slot = *self
            .session_slots
            .get(&session)
            .context("unknown media session")?;
        let call = self.sessions[slot].as_mut().unwrap();
        let rx = call
            .rx_subscription
            .as_mut()
            .context("no RX subscription")?;
        ensure!(rx.id() == id, "RX subscription id mismatch");
        let now = Instant::now();
        if stop {
            rx.stop();
            if let Some(graph) = &mut call.processed {
                graph.enable_local_observation(false)?;
            }
        } else {
            rx.expire(now);
        }
        if rx.pending() && self.rx_lane.as_ref().is_some_and(RxLane::available) {
            self.rx_timers.set(slot, Some(now));
        }
        Ok(Response::RxState {
            session,
            status: rx.status(now),
        })
    }

    /// 每轮总计最多128次报文尝试，允许同一订阅再次到期；全lane遇阻统一退避，控制不等待。
    pub(super) fn flush_rx(&mut self) {
        let Some(lane) = &self.rx_lane else { return };
        if lane.retry_at().is_some_and(|at| Instant::now() < at) {
            return;
        }
        let mut wire = [0; MAX_WIRE];
        let mut lane_failed = false;
        for _ in 0..SOCKETS_PER_TURN {
            let now = Instant::now();
            let Some(slot) = self.rx_timers.pop_due(now) else {
                break;
            };
            let Some(rx) = self.sessions[slot]
                .as_mut()
                .and_then(|c| c.rx_subscription.as_mut())
            else {
                continue;
            };
            let Some((len, pending)) = rx.prepare(now, &mut wire) else {
                continue;
            };
            let send_at = Instant::now();
            if !rx.still_fresh(pending, send_at) {
                self.rx_timers.set(slot, Some(send_at));
                continue;
            }
            match self.rx_lane.as_mut().unwrap().send(&wire[..len], send_at) {
                Send::Submitted => {
                    rx.submitted(pending);
                    if rx.pending() {
                        self.rx_timers.set(slot, Some(Instant::now()));
                    }
                }
                Send::Blocked => {
                    self.rx_timers
                        .set(slot, Some(now + Duration::from_millis(1)));
                    break;
                }
                Send::Failed => {
                    lane_failed = true;
                    break;
                }
            }
        }
        if lane_failed {
            self.isolate_rx_lane();
        }
    }

    /// 所有FD4失败路径共用收敛入口，静默订阅无需等到下一包RTP才得知失联。
    fn isolate_rx_lane(&mut self) {
        // 仅故障一次遍历固定会话槽，撤销上行与调度；RTP/TX/DTMF/Release照常继续。
        for (slot, call) in self.sessions.iter_mut().enumerate() {
            self.rx_timers.remove(slot);
            if let Some(call) = call {
                if let Some(rx) = &mut call.rx_subscription {
                    rx.fail("rx_lane_unavailable");
                }
                if let Some(graph) = &mut call.processed {
                    let _ = graph.enable_local_observation(false);
                }
            }
        }
    }

    /// Release不等对端读取；最多尝试缺口与终态两条，真实资源回收以JSON确认/代次失联为准。
    pub(super) fn finish_rx(&mut self, call: &mut Session) {
        let Some(rx) = &mut call.rx_subscription else {
            return;
        };
        if let Some(graph) = &mut call.processed {
            let _ = graph.enable_local_observation(false);
        }
        rx.stop();
        let Some(lane) = &mut self.rx_lane else {
            return;
        };
        let mut wire = [0; MAX_WIRE];
        let mut failed = false;
        for _ in 0..2 {
            let Some((len, pending)) = rx.prepare(Instant::now(), &mut wire) else {
                break;
            };
            match lane.send(&wire[..len], Instant::now()) {
                Send::Submitted => rx.submitted(pending),
                Send::Blocked => break,
                Send::Failed => {
                    failed = true;
                    break;
                }
            }
        }
        if failed {
            self.isolate_rx_lane();
        }
    }
}
