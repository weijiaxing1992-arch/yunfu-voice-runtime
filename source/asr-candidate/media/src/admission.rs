//! 每条媒体流独占的令牌桶；不共享锁，也不决定全局呼叫准入。
use std::time::{Duration, Instant};

/// 根据单调时间补充令牌，容量限制突发量；一次通过消耗一个报文令牌。
pub struct TokenBucket {
    /// 允许带小数的当前余额，避免高频小时间差补充时丢失额度。
    tokens: f64,
    /// 最大累计令牌数；长时间空闲不能积累无限突发额度。
    capacity: f64,
    /// 每秒补充的令牌数。
    rate: f64,
    /// 上次计量时刻，由调用方传入，便于使用统一时钟并进行确定性测试。
    last: Instant,
}
impl TokenBucket {
    /// 创建满额令牌桶；参数校验由配置入口负责，时间与后续 take 调用保持同一时钟。
    pub fn new(rate: u32, capacity: u32, now: Instant) -> Self {
        Self {
            tokens: capacity.into(),
            capacity: capacity.into(),
            rate: rate.into(),
            last: now,
        }
    }
    /// 补充额度并尝试消费一个令牌；时间未前进时不补充，余额不足时保留小数余额。
    pub fn take(&mut self, now: Instant) -> bool {
        self.take_many(1, now)
    }
    /// 批次读取之后按实际包数扣除额度；调用方先用 available 限制一次系统调用的容量。
    pub fn take_many(&mut self, count: u32, now: Instant) -> bool {
        self.refill(now);
        if self.tokens < f64::from(count) {
            false
        } else {
            self.tokens -= f64::from(count);
            true
        }
    }
    /// 只读取当前完整令牌数，不提前预扣可能没有收到的数据报。
    pub fn available(&mut self, now: Instant) -> u32 {
        self.refill(now);
        self.tokens as u32
    }
    /// 计算至少一个令牌可用前的等待时间，供事件循环排入有界延迟队列。
    /// 调用者保证 rate 大于零；向上留出一纳秒，避免浮点舍入导致零时长忙轮询。
    pub fn wait_for_token(&mut self, now: Instant) -> Duration {
        self.refill(now);
        if self.tokens >= 1.0 {
            Duration::ZERO
        } else {
            Duration::from_secs_f64((1.0 - self.tokens) / self.rate)
                .saturating_add(Duration::from_nanos(1))
        }
    }
    /// 统一补充逻辑；媒体循环只传入单调前进的时刻，不为细分批次重复实现浮点计量。
    fn refill(&mut self, now: Instant) {
        // available 后按相同采样时刻 take_many 不可能产生新额度；直接复用余额，
        // 省去重复浮点换算，同时不移动时钟、不增加突发额度或改变扣费语义。
        if now == self.last {
            return;
        }
        self.tokens = (self.tokens
            + now.saturating_duration_since(self.last).as_secs_f64() * self.rate)
            .min(self.capacity);
        self.last = now;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;
    #[test]
    /// 验证初始突发、按秒补充与长期空闲后的容量上限。
    fn rate_limit_and_refill_are_bounded() {
        let now = Instant::now();
        let mut bucket = TokenBucket::new(10, 2, now);
        assert!(bucket.take(now));
        assert!(bucket.take(now));
        assert!(!bucket.take(now));
        assert!(bucket.take(now + Duration::from_millis(100)));
        assert!(!bucket.take(now + Duration::from_millis(100)));
        let later = now + Duration::from_secs(100);
        assert!(bucket.take(later));
        assert!(bucket.take(later));
        assert!(!bucket.take(later));
    }

    #[test]
    /// 批次额度按实际数量扣除；额度不足不透支，等待到一个令牌恢复后才恢复读取。
    fn batched_allowance_has_a_precise_resume_time() {
        let now = Instant::now();
        let mut bucket = TokenBucket::new(1000, 8, now);
        assert_eq!(bucket.available(now), 8);
        assert!(bucket.take_many(8, now));
        assert!(!bucket.take_many(1, now));
        assert_eq!(bucket.available(now), 0);
        let wait = bucket.wait_for_token(now);
        assert!(wait >= Duration::from_millis(1));
        assert_eq!(bucket.available(now + wait), 1);
    }

    #[test]
    /// 相同now的观察和扣费不补令牌；时间推进后的分数余额与下一枚令牌边界仍精确。
    fn same_time_refill_does_not_restore_spent_tokens() {
        let now = Instant::now();
        let mut bucket = TokenBucket::new(100, 4, now);
        for expected in (1..=4).rev() {
            assert_eq!(bucket.available(now), expected);
            assert!(bucket.take_many(1, now));
        }
        for _ in 0..100 {
            assert_eq!(bucket.available(now), 0);
            assert!(!bucket.take(now));
        }
        let halfway = now + Duration::from_millis(5);
        assert_eq!(bucket.available(halfway), 0);
        assert!(!bucket.take(halfway));
        assert!(bucket.take(now + Duration::from_millis(10)));
        assert!(!bucket.take(now + Duration::from_millis(10)));
        assert_eq!(bucket.available(now + Duration::from_secs(10)), 4);
    }
}
