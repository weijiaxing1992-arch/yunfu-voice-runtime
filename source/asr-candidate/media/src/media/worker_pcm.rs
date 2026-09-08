//! PCM流的生命周期和有界调度；只由Engine的媒体事件循环调用，不等待外部TTS。
use super::*;

const FIRST_AUDIO_TIMEOUT: Duration = Duration::from_secs(2);
const AUDIO_GAP_TIMEOUT: Duration = Duration::from_millis(100);
const FRAME: Duration = Duration::from_millis(20);

pub(super) fn active(turn: &TurnQueue) -> bool {
    matches!(
        turn.status(Instant::now()).state,
        TurnState::Buffering | TurnState::Playing | TurnState::Draining | TurnState::Stopping
    )
}

/// 等首个可播帧和播放中断供分开计时；未到预缓冲不会建立每20ms空转的定时器。
fn deadline(turn: &TurnQueue) -> Option<Instant> {
    if let Some(at) = turn.deadline() {
        return Some(at);
    }
    match turn.status(Instant::now()).state {
        TurnState::Buffering => turn.began_at().map(|at| at + FIRST_AUDIO_TIMEOUT),
        TurnState::Playing => turn
            .last_accepted_at()
            .map(|at| at + FRAME + AUDIO_GAP_TIMEOUT),
        _ => None,
    }
}

/// 供音超时不依赖生产者停止发请求；迟到的push/end不能把已经过期的旧轮次救活。
fn expire_input(turn: &mut TurnQueue, now: Instant) {
    let state = turn.status(now);
    if state.state == TurnState::Buffering
        && turn
            .began_at()
            .is_some_and(|at| now.saturating_duration_since(at) >= FIRST_AUDIO_TIMEOUT)
    {
        turn.fail("pcm_first_audio_timeout");
    } else if state.state == TurnState::Playing
        && state.queued_samples == 0
        && turn
            .last_accepted_at()
            .is_some_and(|at| now.saturating_duration_since(at) >= FRAME + AUDIO_GAP_TIMEOUT)
    {
        turn.fail("pcm_audio_gap_timeout");
    }
}

fn reply(session: u64, turn: &TurnQueue, now: Instant) -> Response {
    Response::PcmTurnState {
        session,
        status: turn.status(now),
    }
}

impl Engine {
    fn local_pcm_slot(&self, session: u64) -> Result<usize> {
        let slot = *self
            .session_slots
            .get(&session)
            .context("unknown media session")?;
        let call = self.sessions[slot].as_ref().unwrap();
        ensure!(
            call.processed.as_ref().is_some_and(|g| g.is_local())
                && call.codec.sample_rate == super::super::pcm_turn::SAMPLE_RATE,
            "PCM stream requires local processed G711 topology"
        );
        Ok(slot)
    }

    pub(super) fn pcm_begin(
        &mut self,
        session: u64,
        turn_id: u64,
        buffer_ms: u16,
        prebuffer_ms: u16,
    ) -> Result<Response> {
        let slot = self.local_pcm_slot(session)?;
        let call = self.sessions[slot].as_mut().unwrap();
        // 先完整验证，任何非法新轮次都不能取消正在播放的合法音频。
        let is_new = if let Some(turn) = &call.pcm_turn {
            turn.validate_begin(turn_id, u32::from(buffer_ms), u32::from(prebuffer_ms))?
        } else {
            TurnQueue::new().validate_begin(
                turn_id,
                u32::from(buffer_ms),
                u32::from(prebuffer_ms),
            )?
        };
        let now = Instant::now();
        if !is_new {
            let turn = call.pcm_turn.as_mut().unwrap();
            expire_input(turn, now);
            turn.advance(now);
            self.pcm_timers.set(slot, deadline(turn));
            return Ok(reply(session, turn, now));
        }
        ensure!(self.pcm_lane.is_some(), "PCM data channel unavailable");
        ensure!(
            call.playback
                .as_ref()
                .is_none_or(|p| !matches!(p.state, "running" | "loading")),
            "legacy playback must be stopped before PCM turn"
        );
        let turn = call
            .pcm_turn
            .get_or_insert_with(|| Box::new(TurnQueue::new()));
        turn.begin(turn_id, u32::from(buffer_ms), u32::from(prebuffer_ms), now)?;
        self.pcm_timers.set(slot, deadline(turn));
        Ok(reply(session, turn, now))
    }

    pub(super) fn pcm_end(
        &mut self,
        session: u64,
        turn_id: u64,
        final_samples: u64,
    ) -> Result<Response> {
        let slot = self.local_pcm_slot(session)?;
        let call = self.sessions[slot].as_mut().unwrap();
        let turn = call.pcm_turn.as_mut().context("unknown PCM turn")?;
        let previous = turn.deadline();
        let now = Instant::now();
        ensure!(turn.status(now).turn_id == turn_id, "stale PCM turn");
        ensure!(
            turn.status(now).accepted_samples == final_samples,
            "PCM final sample offset mismatch"
        );
        expire_input(turn, now);
        turn.end(turn_id, final_samples, now)?;
        if previous.is_none() {
            turn.align_deadline(call.processed.as_ref().unwrap().next_local_audio_at(now));
        }
        self.pcm_timers.set(slot, deadline(turn));
        Ok(reply(session, turn, now))
    }

    pub(super) fn pcm_interrupt(
        &mut self,
        session: u64,
        turn_id: u64,
        fade_ms: u16,
    ) -> Result<Response> {
        let slot = self.local_pcm_slot(session)?;
        let call = self.sessions[slot].as_mut().unwrap();
        let turn = call.pcm_turn.as_mut().context("unknown PCM turn")?;
        let now = Instant::now();
        ensure!(
            turn_id > 0 && turn.status(now).turn_id == turn_id,
            "stale PCM turn"
        );
        ensure!(matches!(fade_ms, 0 | 20 | 40), "invalid PCM fade duration");
        expire_input(turn, now);
        let previous_deadline = turn.deadline();
        if turn.front_frame().is_some()
            && turn
                .deadline()
                .is_some_and(|due| now.saturating_duration_since(due) >= FRAME)
        {
            // 淡出不能把已经超期的旧PCM重新定时成合法音频。
            turn.fail("pcm_schedule_late");
            record_output_lateness(
                &mut self.stats,
                now.saturating_duration_since(previous_deadline.unwrap()),
            );
            self.stats.send_expired += 1;
            self.stats.processed_send_deadline_misses += 1;
        }
        let previous_state = turn.status(now).state;
        turn.interrupt(turn_id, u32::from(fade_ms), now)?;
        // 重复淡出只查询原动作，不能一再把同一包推迟到未来来逃避迟到检查。
        if previous_state != TurnState::Stopping && previous_deadline.is_none() {
            turn.align_deadline(call.processed.as_ref().unwrap().next_local_audio_at(now));
        }
        self.pcm_timers.set(slot, deadline(turn));
        Ok(reply(session, turn, now))
    }

    pub(super) fn pcm_status(&mut self, session: u64, turn_id: u64) -> Result<Response> {
        let slot = self.local_pcm_slot(session)?;
        let turn = self.sessions[slot]
            .as_mut()
            .unwrap()
            .pcm_turn
            .as_mut()
            .context("unknown PCM turn")?;
        let now = Instant::now();
        ensure!(turn.status(now).turn_id == turn_id, "stale PCM turn");
        // 状态查询可确认尾帧已结束，但不借查询执行发送或推迟挂断/打断的顺序。
        turn.advance(now);
        expire_input(turn, now);
        self.pcm_timers.set(slot, deadline(turn));
        Ok(reply(session, turn, now))
    }

    /// 同一轮至多处理32批已到达PCM；数据供给洪峰不能饿死控制、RTP与播放期限。
    pub(super) fn receive_pcm(&mut self) {
        let Some(mut lane) = self.pcm_lane.take() else {
            return;
        };
        if lane.flush().is_err() {
            return;
        }
        for _ in 0..32 {
            let input = match lane.receive() {
                Ok(Some(input)) => input,
                Ok(None) => break,
                Err(_) => return,
            };
            let (code, status) = self.push_pcm(&input);
            if lane
                .respond(pcm_transport::reply(&input, code, status.as_ref()))
                .is_err()
            {
                return;
            }
        }
        self.pcm_lane = Some(lane);
    }

    fn push_pcm(
        &mut self,
        input: &pcm_transport::Push,
    ) -> (u16, Option<super::super::pcm_turn::TurnStatus>) {
        if !input.valid {
            return (1, None);
        }
        let Some(&slot) = self.session_slots.get(&input.session) else {
            return (2, None);
        };
        let call = self.sessions[slot].as_mut().unwrap();
        let Some(graph) = call.processed.as_ref().filter(|g| g.is_local()) else {
            return (3, None);
        };
        let Some(turn) = call.pcm_turn.as_mut() else {
            return (4, None);
        };
        let now = Instant::now();
        let previous = turn.deadline();
        if turn.status(now).turn_id == input.turn_id {
            expire_input(turn, now);
        }
        let code = match turn.push(
            input.turn_id,
            input.offset,
            &input.samples[..input.count],
            now,
        ) {
            Ok(()) => 0,
            Err(TurnError::InvalidArgument) => 1,
            Err(TurnError::StaleTurn) => 5,
            Err(TurnError::Closed) => 6,
            Err(TurnError::OffsetMismatch) => 7,
            Err(TurnError::QueueFull) => 8,
        };
        if code == 0 {
            if previous.is_none() {
                turn.align_deadline(graph.next_local_audio_at(now));
            }
            self.pcm_timers.set(slot, deadline(turn));
        }
        (code, Some(turn.status(now)))
    }

    /// 每轮最多128个到期会话，每个只发一帧，发送过程中没有锁、等待或队列扩容。
    pub(super) fn advance_pcm(&mut self, now: Instant) {
        for _ in 0..SOCKETS_PER_TURN {
            let Some(slot) = self.pcm_timers.pop_due(now) else {
                break;
            };
            let Some(call) = self.sessions[slot].as_mut() else {
                continue;
            };
            let Some(turn) = call.pcm_turn.as_mut() else {
                continue;
            };
            turn.advance(now);
            let snapshot = turn.status(now);
            if snapshot.state == TurnState::Buffering {
                if turn
                    .began_at()
                    .is_some_and(|at| now.saturating_duration_since(at) >= FIRST_AUDIO_TIMEOUT)
                {
                    turn.fail("pcm_first_audio_timeout");
                }
            } else if snapshot.state == TurnState::Playing && snapshot.queued_samples == 0 {
                if turn.last_accepted_at().is_some_and(|at| {
                    now.saturating_duration_since(at) >= FRAME + AUDIO_GAP_TIMEOUT
                }) {
                    turn.fail("pcm_audio_gap_timeout");
                }
            } else if let (Some(due), Some(samples)) =
                (turn.deadline(), turn.front_frame().copied())
            {
                let send_at = Instant::now();
                if send_at < due {
                    self.pcm_timers.set(slot, Some(due));
                    continue;
                }
                let late = send_at.saturating_duration_since(due);
                if late >= FRAME {
                    record_output_lateness(&mut self.stats, late);
                    self.stats.send_expired += 1;
                    self.stats.processed_send_deadline_misses += 1;
                    turn.fail("pcm_schedule_late");
                } else {
                    let graph = call.processed.as_mut().unwrap();
                    let mut wire = [0u8; 172];
                    match graph.encode_local_pcm(
                        &samples,
                        due,
                        snapshot.sent_samples == 0,
                        &mut wire,
                        &mut self.stats,
                    ) {
                        Ok(len) => {
                            // 单写线程内最后确认轮次；控制中断后不可能沿用上轮取出的待发送帧。
                            let ready_at = Instant::now();
                            record_output_lateness(
                                &mut self.stats,
                                ready_at.saturating_duration_since(due),
                            );
                            let expired = ready_at.saturating_duration_since(due) >= FRAME;
                            let same_turn = turn.status(ready_at).turn_id == snapshot.turn_id;
                            let accepted = !expired
                                && same_turn
                                && matches!(
                                send_packet(&call.sockets[0], &wire[..len], call.a.rtp, call.connected), Ok(n) if n == len);
                            let accepted_at = Instant::now();
                            graph.note_send(1, &wire[..len], accepted_at, accepted);
                            if accepted {
                                self.stats.tx_packets += 1;
                                self.stats.tx_bytes += len as u64;
                                if turn.accepted(accepted_at).is_err() {
                                    turn.fail("pcm_commit_failed");
                                } else if accepted_at.saturating_duration_since(due) >= FRAME {
                                    // 已经完整返回成功的包如实计sent；只取消尚未发送的剩余内容。
                                    self.stats.processed_send_deadline_misses += 1;
                                    turn.fail("pcm_send_return_late");
                                }
                            } else if expired {
                                self.stats.send_expired += 1;
                                self.stats.processed_send_deadline_misses += 1;
                                turn.fail("pcm_schedule_late");
                            } else {
                                self.stats.send_errors += 1;
                                self.stats.processed_send_errors += 1;
                                turn.fail("pcm_udp_send_failed");
                            }
                        }
                        Err(_) => {
                            record_output_lateness(
                                &mut self.stats,
                                Instant::now().saturating_duration_since(due),
                            );
                            self.stats.processed_send_errors += 1;
                            turn.fail("pcm_encode_failed");
                        }
                    }
                }
            }
            self.pcm_timers.set(slot, deadline(turn));
        }
    }
}
