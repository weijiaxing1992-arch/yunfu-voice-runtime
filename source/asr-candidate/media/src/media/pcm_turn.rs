//! 单个媒体工作线程独占的 PCM 轮次队列；只接收原生 8kHz、单声道、20ms 的有符号样本。
//! 固定存储不创建逐帧对象；入队只代表接收，只有 UDP 完整发送成功才能提交已发送样本。
use serde::{Deserialize, Serialize};
use std::{
    fmt,
    time::{Duration, Instant},
};

/// 本队列不负责重采样或声道混合；上游必须显式转换后再提交。
pub const SAMPLE_RATE: u32 = 8_000;
pub const FRAME_SAMPLES: usize = 160;
pub const FRAME_MS: u32 = 20;
pub const MAX_FRAMES: usize = 50;
pub const MAX_BATCH_FRAMES: usize = 5;
const STEP: Duration = Duration::from_millis(FRAME_MS as u64);

/// 固定错误枚举可直接映射二进制回执，不为拒绝请求分配字符串。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TurnError {
    InvalidArgument,
    StaleTurn,
    Closed,
    OffsetMismatch,
    QueueFull,
}

impl fmt::Display for TurnError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(match self {
            Self::InvalidArgument => "invalid_argument",
            Self::StaleTurn => "stale_turn",
            Self::Closed => "closed",
            Self::OffsetMismatch => "offset_mismatch",
            Self::QueueFull => "queue_full",
        })
    }
}
impl std::error::Error for TurnError {}
pub type TurnResult<T> = Result<T, TurnError>;

/// 结束和中断均关闭生产入口，排空期间也不能追加旧轮次音频。
#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum TurnState {
    Idle,
    Buffering,
    Playing,
    Draining,
    Stopping,
    Completed,
    Stopped,
    Failed,
}
impl TurnState {
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Idle => "idle",
            Self::Buffering => "buffering",
            Self::Playing => "playing",
            Self::Draining => "draining",
            Self::Stopping => "stopping",
            Self::Completed => "completed",
            Self::Stopped => "stopped",
            Self::Failed => "failed",
        }
    }
}

/// 全部计数属于当前轮次；任何状态均满足 accepted = sent + queued + discarded。
#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
pub struct TurnStatus {
    pub turn_id: u64,
    pub state: TurnState,
    pub accepted_samples: u64,
    pub sent_samples: u64,
    pub discarded_samples: u64,
    pub queued_samples: u64,
    pub queued_bytes: u64,
    pub queued_ms: u32,
    pub oldest_age_ms: u64,
    pub buffer_ms: u16,
    pub prebuffer_ms: u16,
    /// 仅失败改变错误码；普通拒绝请求不会污染原轮次状态。
    pub error: String,
}

/// 对象本身约 17KiB，调用方可在首次启用时一次性装箱；本类型不使用 Vec、Arc 或内部锁。
pub struct TurnQueue {
    samples: [[i16; FRAME_SAMPLES]; MAX_FRAMES],
    enqueued: [Option<Instant>; MAX_FRAMES],
    head: usize,
    len: usize,
    turn_id: u64,
    buffer_frames: usize,
    prebuffer_frames: usize,
    state: TurnState,
    accepted_samples: u64,
    sent_samples: u64,
    discarded_samples: u64,
    error: &'static str,
    began: Option<Instant>,
    started: Option<Instant>,
    last_accepted: Option<Instant>,
    next: Option<Instant>,
}
impl Default for TurnQueue {
    fn default() -> Self {
        Self::new()
    }
}
impl TurnQueue {
    pub fn new() -> Self {
        Self {
            samples: [[0; FRAME_SAMPLES]; MAX_FRAMES],
            enqueued: [None; MAX_FRAMES],
            head: 0,
            len: 0,
            turn_id: 0,
            buffer_frames: 0,
            prebuffer_frames: 0,
            state: TurnState::Idle,
            accepted_samples: 0,
            sent_samples: 0,
            discarded_samples: 0,
            error: "",
            began: None,
            started: None,
            last_accepted: None,
            next: None,
        }
    }

    /// 返回 true 才会创建新轮次；相同配置的相同 ID 在终态也只是幂等查询，不能复活。
    pub fn validate_begin(
        &self,
        turn_id: u64,
        buffer_ms: u32,
        prebuffer_ms: u32,
    ) -> TurnResult<bool> {
        if turn_id == 0
            || !(FRAME_MS..=1_000).contains(&buffer_ms)
            || buffer_ms % FRAME_MS != 0
            || !(FRAME_MS..=buffer_ms).contains(&prebuffer_ms)
            || prebuffer_ms % FRAME_MS != 0
        {
            return Err(TurnError::InvalidArgument);
        }
        if turn_id < self.turn_id {
            return Err(TurnError::StaleTurn);
        }
        if turn_id == self.turn_id {
            if self.buffer_frames != (buffer_ms / FRAME_MS) as usize
                || self.prebuffer_frames != (prebuffer_ms / FRAME_MS) as usize
            {
                return Err(TurnError::InvalidArgument);
            }
            return Ok(false);
        }
        Ok(true)
    }

    /// 所有校验完成后才替换旧队列；旧音频的物理槽可保留，但长度清零后永远不能被读取。
    pub fn begin(
        &mut self,
        turn_id: u64,
        buffer_ms: u32,
        prebuffer_ms: u32,
        now: Instant,
    ) -> TurnResult<bool> {
        if !self.validate_begin(turn_id, buffer_ms, prebuffer_ms)? {
            return Ok(false);
        }
        self.head = 0;
        self.len = 0;
        self.enqueued.fill(None);
        self.turn_id = turn_id;
        self.buffer_frames = (buffer_ms / FRAME_MS) as usize;
        self.prebuffer_frames = (prebuffer_ms / FRAME_MS) as usize;
        self.state = TurnState::Buffering;
        self.accepted_samples = 0;
        self.sent_samples = 0;
        self.discarded_samples = 0;
        self.error = "";
        self.began = Some(now);
        self.started = None;
        self.last_accepted = None;
        self.next = None;
        Ok(true)
    }

    fn check_turn(&self, turn_id: u64) -> TurnResult<()> {
        if turn_id == 0 {
            return Err(TurnError::InvalidArgument);
        }
        if turn_id != self.turn_id {
            return Err(TurnError::StaleTurn);
        }
        Ok(())
    }

    /// 验证整批偏移、长度、累积溢出和容量；任何失败都不会写入前半批或推进偏移。
    pub fn validate_push(
        &self,
        turn_id: u64,
        offset_samples: u64,
        sample_count: usize,
    ) -> TurnResult<()> {
        self.check_turn(turn_id)?;
        if !matches!(self.state, TurnState::Buffering | TurnState::Playing) {
            return Err(TurnError::Closed);
        }
        if sample_count == 0
            || sample_count > FRAME_SAMPLES * MAX_BATCH_FRAMES
            || sample_count % FRAME_SAMPLES != 0
            || self
                .accepted_samples
                .checked_add(sample_count as u64)
                .is_none()
        {
            return Err(TurnError::InvalidArgument);
        }
        if offset_samples != self.accepted_samples {
            return Err(TurnError::OffsetMismatch);
        }
        if self.len + sample_count / FRAME_SAMPLES > self.buffer_frames {
            return Err(TurnError::QueueFull);
        }
        Ok(())
    }

    /// 输入切片只在调用期间借用，返回后生产方可立即复用；每批至多复制 800 个样本。
    pub fn push(
        &mut self,
        turn_id: u64,
        offset_samples: u64,
        samples: &[i16],
        now: Instant,
    ) -> TurnResult<()> {
        self.validate_push(turn_id, offset_samples, samples.len())?;
        let was_empty = self.len == 0;
        for frame in samples.chunks_exact(FRAME_SAMPLES) {
            let slot = (self.head + self.len) % MAX_FRAMES;
            self.samples[slot].copy_from_slice(frame);
            self.enqueued[slot] = Some(now);
            self.len += 1;
        }
        self.accepted_samples += samples.len() as u64;
        if self.state == TurnState::Buffering && self.len >= self.prebuffer_frames {
            self.state = TurnState::Playing;
            self.started = Some(now);
            self.next = Some(now);
        } else if self.state == TurnState::Playing && was_empty {
            // 供音中断不补发空白，也不在恢复后赶发过期时间格；缺口上限由工作线程验收。
            self.next = Some(self.next.unwrap_or(now).max(now));
        }
        Ok(())
    }

    /// 提前结束不足预缓冲的一轮也允许排空；最终样本数不一致时整个状态保持不变。
    pub fn end(&mut self, turn_id: u64, final_samples: u64, now: Instant) -> TurnResult<()> {
        self.check_turn(turn_id)?;
        if final_samples != self.accepted_samples {
            return Err(TurnError::OffsetMismatch);
        }
        if matches!(self.state, TurnState::Draining | TurnState::Completed) {
            return Ok(());
        }
        if !matches!(self.state, TurnState::Buffering | TurnState::Playing) {
            return Err(TurnError::Closed);
        }
        self.state = TurnState::Draining;
        if self.len > 0 && self.started.is_none() {
            self.started = Some(now);
            self.next = Some(now);
        }
        self.advance(now);
        Ok(())
    }

    /// 中断立即关闭写入口；只淡出尚未发送的队头，不重复刚发送的样本，也不等待未来数据。
    pub fn interrupt(&mut self, turn_id: u64, fade_ms: u32, now: Instant) -> TurnResult<()> {
        self.check_turn(turn_id)?;
        if !matches!(fade_ms, 0 | 20 | 40) {
            return Err(TurnError::InvalidArgument);
        }
        if matches!(
            self.state,
            TurnState::Stopped | TurnState::Completed | TurnState::Failed
        ) {
            return Ok(());
        }
        // 非零重复请求保持原曲线，包含换成另一非零长度；零长度始终可升级为立即停止。
        if self.state == TurnState::Stopping && fade_ms != 0 {
            return Ok(());
        }
        let keep = self.len.min((fade_ms / FRAME_MS) as usize);
        self.discard_tail(keep);
        if keep == 0 {
            self.state = TurnState::Stopped;
            self.next = None;
            return Ok(());
        }
        let total = keep * FRAME_SAMPLES;
        for index in 0..keep {
            let slot = (self.head + index) % MAX_FRAMES;
            for (sample_index, sample) in self.samples[slot].iter_mut().enumerate() {
                let weight = total - 1 - (index * FRAME_SAMPLES + sample_index);
                // 最大乘积小于 32768×320；整数向零取整，首样本不突增，末样本精确归零。
                *sample = (i32::from(*sample) * weight as i32 / (total - 1) as i32) as i16;
            }
        }
        self.state = TurnState::Stopping;
        if self.started.is_none() {
            self.started = Some(now);
        }
        // 淡出仍消费原有样本位置；控制命令晚到不能让连续音频的RTP时钟逐帧漂移。
        // 只有此前尚未达到预缓冲、没有发送计划的队列，才为淡出建立首个时刻。
        self.next = Some(self.next.unwrap_or(now));
        Ok(())
    }

    fn discard_tail(&mut self, keep: usize) {
        self.discarded_samples += ((self.len - keep) * FRAME_SAMPLES) as u64;
        for index in keep..self.len {
            self.enqueued[(self.head + index) % MAX_FRAMES] = None;
        }
        self.len = keep;
    }

    /// 期限只指真实待发送帧或结束尾音；播放中的空队列没有定时器，避免空转忙循环。
    pub fn deadline(&self) -> Option<Instant> {
        if self.len > 0 {
            return self.next;
        }
        if matches!(self.state, TurnState::Draining | TurnState::Stopping) {
            return self.last_accepted.and_then(|at| at.checked_add(STEP));
        }
        None
    }

    /// 新轮次首帧可与图中其他播放源协调；只提高期限，绝不通过回拨时刻追赶旧音频。
    pub fn align_deadline(&mut self, earliest: Instant) {
        if self.len > 0 {
            if let Some(next) = &mut self.next {
                *next = (*next).max(earliest);
            }
        }
    }

    /// 取头帧不消费队列；借用失效后才能提交、替换或中断轮次，禁止保留跨异步任务的裸引用。
    pub fn front_frame(&self) -> Option<&[i16; FRAME_SAMPLES]> {
        if self.len == 0 || self.next.is_none() {
            return None;
        }
        Some(&self.samples[self.head])
    }

    /// 仅在 UDP 完整接受当前帧后调用；失败、WouldBlock、只构包都不允许伪记已发送。
    pub fn accepted(&mut self, now: Instant) -> TurnResult<()> {
        if self.front_frame().is_none() {
            return Err(TurnError::Closed);
        }
        let due = self.next.ok_or(TurnError::Closed)?;
        if now < due {
            return Err(TurnError::InvalidArgument);
        }
        let next = due.checked_add(STEP).ok_or(TurnError::InvalidArgument)?;
        now.checked_add(STEP).ok_or(TurnError::InvalidArgument)?;
        self.enqueued[self.head] = None;
        self.head = (self.head + 1) % MAX_FRAMES;
        self.len -= 1;
        self.sent_samples += FRAME_SAMPLES as u64;
        self.last_accepted = Some(now);
        // 连续样本严格沿原计划推进20ms；实际发送迟到不能逐帧改变RTP采样时间线。
        // 工作线程负责拒绝超期并限制每轮调度预算，不能借此在一个回调里追赶多帧。
        self.next = Some(next);
        Ok(())
    }

    /// 最后帧提交之后仍保留实际 20ms 音频区间；空 end 没有待播样本，可立即完成。
    pub fn advance(&mut self, now: Instant) {
        if self.len != 0 || !matches!(self.state, TurnState::Draining | TurnState::Stopping) {
            return;
        }
        if self
            .last_accepted
            .is_some_and(|at| now.saturating_duration_since(at) < STEP)
        {
            return;
        }
        self.state = if self.state == TurnState::Stopping {
            TurnState::Stopped
        } else {
            TurnState::Completed
        };
        self.next = None;
    }

    /// 调度、供音超时和发送错误由工作线程选择稳定错误码；清理后旧生产者仍然被拒绝。
    pub fn fail(&mut self, error: &'static str) {
        if matches!(
            self.state,
            TurnState::Idle | TurnState::Completed | TurnState::Stopped | TurnState::Failed
        ) {
            return;
        }
        self.discard_tail(0);
        self.next = None;
        self.state = TurnState::Failed;
        self.error = error;
    }

    pub fn began_at(&self) -> Option<Instant> {
        self.began
    }
    pub fn last_accepted_at(&self) -> Option<Instant> {
        self.last_accepted
    }

    /// 年龄从实际入队时刻计算；这里只拷贝小型标量，不序列化样本、不进行堆分配。
    pub fn status(&self, now: Instant) -> TurnStatus {
        let queued_samples = (self.len * FRAME_SAMPLES) as u64;
        TurnStatus {
            turn_id: self.turn_id,
            state: self.state,
            accepted_samples: self.accepted_samples,
            sent_samples: self.sent_samples,
            discarded_samples: self.discarded_samples,
            queued_samples,
            queued_bytes: queued_samples * 2,
            queued_ms: self.len as u32 * FRAME_MS,
            oldest_age_ms: if self.len == 0 {
                0
            } else {
                self.enqueued[self.head].map_or(0, |at| {
                    now.saturating_duration_since(at)
                        .as_millis()
                        .min(u128::from(u64::MAX)) as u64
                })
            },
            buffer_ms: (self.buffer_frames as u32 * FRAME_MS) as u16,
            prebuffer_ms: (self.prebuffer_frames as u32 * FRAME_MS) as u16,
            error: self.error.to_owned(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn accounting(queue: &TurnQueue, now: Instant) {
        let state = queue.status(now);
        assert_eq!(
            state.accepted_samples,
            state.sent_samples + state.queued_samples + state.discarded_samples
        );
        assert_eq!(state.queued_bytes, state.queued_samples * 2);
        assert_eq!(
            u64::from(state.queued_ms) * u64::from(SAMPLE_RATE),
            state.queued_samples * 1_000
        );
        assert!(state.queued_samples <= (MAX_FRAMES * FRAME_SAMPLES) as u64);
    }

    #[test]
    fn configuration_identity_and_replacement_are_atomic() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        for (id, buffer, prebuffer) in [
            (0, 20, 20),
            (1, 0, 20),
            (1, 1_020, 20),
            (1, 21, 20),
            (1, 40, 0),
            (1, 40, 60),
            (1, 40, 21),
        ] {
            assert_eq!(
                queue.begin(id, buffer, prebuffer, now),
                Err(TurnError::InvalidArgument)
            );
            assert_eq!(queue.status(now).state, TurnState::Idle);
        }
        assert_eq!(queue.begin(7, 60, 40, now), Ok(true));
        queue.push(7, 0, &[301; FRAME_SAMPLES], now).unwrap();
        let before = queue.status(now);
        assert_eq!(queue.begin(6, 60, 40, now), Err(TurnError::StaleTurn));
        assert_eq!(queue.begin(7, 80, 40, now), Err(TurnError::InvalidArgument));
        assert_eq!(
            queue.begin(8, 80, 100, now),
            Err(TurnError::InvalidArgument)
        );
        assert_eq!(queue.begin(7, 60, 40, now + STEP), Ok(false));
        assert_eq!(queue.status(now), before);
        assert_eq!(queue.began_at(), Some(now));
        assert_eq!(queue.begin(8, 20, 20, now + STEP), Ok(true));
        assert!(queue.front_frame().is_none());
        assert_eq!(
            queue.push(7, FRAME_SAMPLES as u64, &[77; FRAME_SAMPLES], now),
            Err(TurnError::StaleTurn)
        );
        queue.push(8, 0, &[902; FRAME_SAMPLES], now + STEP).unwrap();
        assert_eq!(queue.front_frame(), Some(&[902; FRAME_SAMPLES]));
        accounting(&queue, now + STEP);
    }

    #[test]
    fn whole_batches_reject_without_changing_audio_offset_or_deadline() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 60, 20, now).unwrap();
        queue.push(1, 0, &[123; FRAME_SAMPLES * 2], now).unwrap();
        let before = queue.status(now);
        let deadline = queue.deadline();
        for (id, offset, count, error) in [
            (2, 320, 160, TurnError::StaleTurn),
            (1, 160, 160, TurnError::OffsetMismatch),
            (1, 320, 0, TurnError::InvalidArgument),
            (1, 320, 159, TurnError::InvalidArgument),
            (1, 320, 960, TurnError::InvalidArgument),
            (1, 320, 320, TurnError::QueueFull),
        ] {
            let input = [999; FRAME_SAMPLES * 6];
            assert_eq!(
                queue.push(id, offset, &input[..count], now + STEP),
                Err(error)
            );
            assert_eq!(queue.status(now), before);
            assert_eq!(queue.front_frame(), Some(&[123; FRAME_SAMPLES]));
            assert_eq!(queue.deadline(), deadline);
        }
        // 正常追加仍不修改已有队头期限；工作线程必须能看到真实迟到，不能被生产者掩盖。
        queue
            .push(1, 320, &[234; FRAME_SAMPLES], now + STEP)
            .unwrap();
        assert_eq!(queue.deadline(), Some(now));
        queue.accepted(now).unwrap();
        queue.accepted(now + STEP).unwrap();
        assert_eq!(queue.front_frame(), Some(&[234; FRAME_SAMPLES]));
        accounting(&queue, now + STEP);
    }

    #[test]
    fn prebuffer_alignment_drain_and_real_final_interval() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 100, 60, now).unwrap();
        queue.push(1, 0, &[777; FRAME_SAMPLES], now).unwrap();
        assert_eq!(queue.status(now).state, TurnState::Buffering);
        assert!(queue.front_frame().is_none());
        assert!(queue.deadline().is_none());
        assert_eq!(queue.end(1, 0, now), Err(TurnError::OffsetMismatch));
        queue.end(1, FRAME_SAMPLES as u64, now).unwrap();
        assert_eq!(queue.state, TurnState::Draining);
        queue.align_deadline(now + STEP);
        queue.align_deadline(now);
        assert_eq!(queue.deadline(), Some(now + STEP));
        assert_eq!(queue.accepted(now), Err(TurnError::InvalidArgument));
        assert_eq!(
            queue.push(1, 160, &[9; FRAME_SAMPLES], now),
            Err(TurnError::Closed)
        );
        let sent_at = now + STEP + Duration::from_millis(3);
        queue.accepted(sent_at).unwrap();
        assert_eq!(queue.state, TurnState::Draining);
        assert_eq!(queue.deadline(), Some(sent_at + STEP));
        queue.advance(sent_at + STEP - Duration::from_nanos(1));
        assert_eq!(queue.state, TurnState::Draining);
        queue.advance(sent_at + STEP);
        assert_eq!(queue.state, TurnState::Completed);
        assert!(queue.front_frame().is_none());
        assert!(queue.deadline().is_none());
        assert_eq!(queue.begin(1, 100, 60, now), Ok(false));
        queue.end(1, 160, now).unwrap();
        assert_eq!(queue.state, TurnState::Completed);
        accounting(&queue, sent_at + STEP);
    }

    #[test]
    fn empty_end_and_ending_after_open_underflow_do_not_replay() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 20, 20, now).unwrap();
        queue.end(1, 0, now).unwrap();
        assert_eq!(queue.state, TurnState::Completed);
        assert!(queue.deadline().is_none());
        queue.begin(2, 20, 20, now).unwrap();
        queue.push(2, 0, &[5; FRAME_SAMPLES], now).unwrap();
        queue.accepted(now).unwrap();
        assert_eq!(queue.state, TurnState::Playing);
        assert!(queue.deadline().is_none());
        assert!(queue.front_frame().is_none());
        queue.end(2, 160, now + Duration::from_millis(8)).unwrap();
        assert_eq!(queue.deadline(), Some(now + STEP));
        queue.advance(now + STEP);
        assert_eq!(queue.state, TurnState::Completed);
        queue.begin(3, 20, 20, now).unwrap();
        queue.push(3, 0, &[7; FRAME_SAMPLES], now).unwrap();
        queue.accepted(now).unwrap();
        queue.end(3, 160, now + STEP * 2).unwrap();
        assert_eq!(queue.state, TurnState::Completed);
    }

    #[test]
    fn underflow_has_no_timer_and_recovery_never_catches_up_old_slots() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 60, 40, now).unwrap();
        queue.push(1, 0, &[31; FRAME_SAMPLES], now).unwrap();
        assert!(queue.started.is_none());
        queue
            .push(1, 160, &[32; FRAME_SAMPLES], now + STEP)
            .unwrap();
        assert_eq!(queue.started, Some(now + STEP));
        queue.accepted(now + STEP).unwrap();
        queue.accepted(now + STEP * 2).unwrap();
        assert!(queue.deadline().is_none());
        assert!(queue.front_frame().is_none());
        queue
            .push(1, 320, &[33; FRAME_SAMPLES], now + STEP * 4)
            .unwrap();
        assert_eq!(queue.deadline(), Some(now + STEP * 4));
        assert_eq!(queue.front_frame(), Some(&[33; FRAME_SAMPLES]));
        assert_eq!(queue.last_accepted_at(), Some(now + STEP * 2));
        assert_eq!(queue.started, Some(now + STEP));
        assert_eq!(queue.status(now + STEP * 5).oldest_age_ms, 20);
        accounting(&queue, now + STEP * 5);
    }

    #[test]
    fn minor_send_lateness_preserves_sample_timeline_but_real_underflow_can_resume_later() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 60, 40, now).unwrap();
        queue.push(1, 0, &[51; FRAME_SAMPLES * 2], now).unwrap();
        // 已在队的连续音频晚发2ms，下一帧仍对应原始20ms样本位置，不能漂移成22ms。
        queue.accepted(now + Duration::from_millis(2)).unwrap();
        assert_eq!(queue.deadline(), Some(now + STEP));
        queue
            .accepted(now + STEP + Duration::from_millis(2))
            .unwrap();
        assert!(queue.deadline().is_none());
        assert_eq!(queue.next, Some(now + STEP * 2));
        // 真正耗尽队列后直到70ms才补帧，允许留下明确供音缺口，不伪造40/60ms静音。
        let resumed = now + Duration::from_millis(70);
        queue
            .push(1, 320, &[52; FRAME_SAMPLES * 2], resumed)
            .unwrap();
        assert_eq!(queue.deadline(), Some(resumed));
        queue.accepted(resumed + Duration::from_millis(2)).unwrap();
        assert_eq!(queue.deadline(), Some(resumed + STEP));
        queue.end(1, 640, resumed + STEP).unwrap();
        let last = resumed + STEP + Duration::from_millis(2);
        queue.accepted(last).unwrap();
        assert_eq!(queue.deadline(), Some(last + STEP));
        queue.advance(last + STEP - Duration::from_nanos(1));
        assert_eq!(queue.state, TurnState::Draining);
        queue.advance(last + STEP);
        assert_eq!(queue.state, TurnState::Completed);
        accounting(&queue, last + STEP);
    }

    #[test]
    fn fade_command_two_ms_after_due_keeps_original_sample_positions() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 60, 20, now).unwrap();
        queue.push(1, 0, &[3190; FRAME_SAMPLES * 3], now).unwrap();
        queue.accepted(now).unwrap();
        let original = queue.deadline().unwrap();
        queue
            .interrupt(1, 40, original + Duration::from_millis(2))
            .unwrap();
        assert_eq!(queue.deadline(), Some(original));
        assert_eq!(
            original.duration_since(now).as_millis() * u128::from(SAMPLE_RATE) / 1_000,
            160
        );
        assert_eq!(queue.front_frame().unwrap()[0], 3190);
        queue.accepted(original + Duration::from_millis(2)).unwrap();
        let next = queue.deadline().unwrap();
        assert_eq!(next, original + STEP);
        assert_eq!(
            next.duration_since(original).as_millis() * u128::from(SAMPLE_RATE) / 1_000,
            160
        );
        let remaining = *queue.front_frame().unwrap();
        queue
            .interrupt(1, 40, next + Duration::from_millis(2))
            .unwrap();
        assert_eq!(queue.deadline(), Some(next));
        assert_eq!(queue.front_frame(), Some(&remaining));
        assert_eq!(remaining[159], 0);
        queue.accepted(next + Duration::from_millis(2)).unwrap();
        queue.advance(next + STEP + Duration::from_millis(1));
        assert_eq!(queue.state, TurnState::Stopping);
        queue.advance(next + STEP + Duration::from_millis(2));
        assert_eq!(queue.state, TurnState::Stopped);
        accounting(&queue, next + STEP);
    }

    #[test]
    fn fade_uses_only_unsent_samples_and_repeat_interrupt_is_idempotent() {
        let now = Instant::now();
        for fade_ms in [20, 40] {
            let mut queue = TurnQueue::new();
            queue.begin(1, 100, 20, now).unwrap();
            let input = std::array::from_fn::<_, { FRAME_SAMPLES * 5 }, _>(|i| {
                if i % 2 == 0 {
                    i16::MIN
                } else {
                    i16::MAX
                }
            });
            queue.push(1, 0, &input, now).unwrap();
            queue.accepted(now).unwrap();
            queue.interrupt(1, fade_ms, now).unwrap();
            let count = (fade_ms / FRAME_MS) as usize;
            assert_eq!(
                queue.status(now).discarded_samples,
                ((4 - count) * FRAME_SAMPLES) as u64
            );
            let total = count * FRAME_SAMPLES;
            for frame in 0..count {
                let actual = *queue.front_frame().unwrap();
                for (index, sample) in actual.iter().enumerate() {
                    let ordinal = frame * FRAME_SAMPLES + index;
                    let source = i64::from(input[FRAME_SAMPLES + ordinal]);
                    let expected = source * (total - 1 - ordinal) as i64 / (total - 1) as i64;
                    assert_eq!(i64::from(*sample), expected);
                }
                queue.interrupt(1, fade_ms, now).unwrap();
                queue
                    .interrupt(1, if fade_ms == 20 { 40 } else { 20 }, now)
                    .unwrap();
                assert_eq!(queue.front_frame(), Some(&actual));
                assert_eq!(
                    queue.push(1, 800, &[1; FRAME_SAMPLES], now),
                    Err(TurnError::Closed)
                );
                queue.accepted(now + STEP * (frame as u32 + 1)).unwrap();
                accounting(&queue, now);
            }
            let last = now + STEP * count as u32;
            assert_eq!(queue.state, TurnState::Stopping);
            queue.advance(last + STEP - Duration::from_nanos(1));
            assert_eq!(queue.state, TurnState::Stopping);
            queue.advance(last + STEP);
            assert_eq!(queue.state, TurnState::Stopped);
            assert_eq!(queue.sent_samples, ((count + 1) * FRAME_SAMPLES) as u64);
        }
    }

    #[test]
    fn interruption_can_escalate_to_zero_and_never_waits_for_future_samples() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 100, 100, now).unwrap();
        queue.push(1, 0, &[20; FRAME_SAMPLES], now).unwrap();
        queue.interrupt(1, 40, now).unwrap();
        assert_eq!(queue.status(now).queued_samples, 160);
        assert_eq!(queue.front_frame().unwrap()[159], 0);
        assert_eq!(queue.front_frame().unwrap()[0], 20);
        queue.interrupt(1, 0, now).unwrap();
        assert_eq!(queue.state, TurnState::Stopped);
        assert_eq!(queue.status(now).discarded_samples, 160);
        assert!(queue.deadline().is_none());
        assert!(queue.front_frame().is_none());
        queue.interrupt(1, 40, now).unwrap();
        assert_eq!(queue.state, TurnState::Stopped);
        queue.begin(2, 20, 20, now).unwrap();
        queue.interrupt(2, 40, now).unwrap();
        assert_eq!(queue.state, TurnState::Stopped);
        accounting(&queue, now);
    }

    #[test]
    fn failure_discards_once_and_invalid_controls_leave_live_turn_unchanged() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 60, 20, now).unwrap();
        queue.push(1, 0, &[77; FRAME_SAMPLES * 3], now).unwrap();
        queue.accepted(now).unwrap();
        let before = queue.status(now);
        assert_eq!(queue.interrupt(1, 30, now), Err(TurnError::InvalidArgument));
        assert_eq!(queue.interrupt(2, 0, now), Err(TurnError::StaleTurn));
        assert_eq!(queue.end(0, 480, now), Err(TurnError::InvalidArgument));
        assert_eq!(queue.status(now), before);
        queue.fail("pcm_send_failed");
        queue.fail("different_error");
        queue.interrupt(1, 0, now).unwrap();
        assert_eq!(queue.state, TurnState::Failed);
        assert_eq!(queue.status(now).error, "pcm_send_failed");
        assert_eq!(queue.status(now).discarded_samples, 320);
        assert_eq!(
            queue.push(1, 480, &[9; FRAME_SAMPLES], now),
            Err(TurnError::Closed)
        );
        assert!(queue.front_frame().is_none());
        assert!(queue.deadline().is_none());
        accounting(&queue, now);
    }

    #[test]
    fn fixed_ring_reuses_all_slots_without_cross_frame_or_prior_turn_leaks() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 1_000, 1_000, now).unwrap();
        let mut offset = 0u64;
        for _ in 0..10 {
            let frame = std::array::from_fn::<_, { FRAME_SAMPLES * 5 }, _>(|i| {
                ((offset as usize + i) % 30_001) as i16
            });
            queue.push(1, offset, &frame, now).unwrap();
            offset += frame.len() as u64;
        }
        assert_eq!(queue.status(now).queued_ms, 1_000);
        assert_eq!(
            queue.push(1, offset, &[0; FRAME_SAMPLES], now),
            Err(TurnError::QueueFull)
        );
        for index in 0..700 {
            let due = now + STEP * index;
            let frame = queue.front_frame().unwrap();
            for (sample, value) in frame.iter().enumerate() {
                assert_eq!(
                    *value,
                    ((index as usize * FRAME_SAMPLES + sample) % 30_001) as i16
                );
            }
            queue.accepted(due).unwrap();
            let next = std::array::from_fn::<_, FRAME_SAMPLES, _>(|i| {
                ((offset as usize + i) % 30_001) as i16
            });
            queue.push(1, offset, &next, due).unwrap();
            offset += FRAME_SAMPLES as u64;
            accounting(&queue, due);
        }
        queue.interrupt(1, 0, now).unwrap();
        accounting(&queue, now);
        queue.begin(2, 20, 20, now).unwrap();
        queue.push(2, 0, &[i16::MIN; FRAME_SAMPLES], now).unwrap();
        assert_eq!(queue.front_frame(), Some(&[i16::MIN; FRAME_SAMPLES]));
    }

    #[test]
    fn cumulative_overflow_is_rejected_before_any_mutation_and_status_is_explicit() {
        let now = Instant::now();
        let mut queue = TurnQueue::new();
        queue.begin(1, 20, 20, now).unwrap();
        queue.accepted_samples = u64::MAX - u64::MAX % FRAME_SAMPLES as u64;
        queue.sent_samples = queue.accepted_samples;
        let before = queue.status(now);
        assert_eq!(
            queue.push(1, queue.accepted_samples, &[0; FRAME_SAMPLES], now),
            Err(TurnError::InvalidArgument)
        );
        assert_eq!(queue.status(now), before);
        let json = serde_json::to_value(before).unwrap();
        assert_eq!(json["state"], "buffering");
        assert_eq!(json["error"], "");
        assert_eq!(json["buffer_ms"], 20);
        assert_eq!(json["prebuffer_ms"], 20);
        assert_eq!(TurnState::Stopping.as_str(), "stopping");
        assert_eq!(TurnError::QueueFull.to_string(), "queue_full");
        accounting(&queue, now);
    }
}
