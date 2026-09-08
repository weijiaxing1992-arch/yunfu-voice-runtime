//! 单个 worker 独占的媒体端口池；释放后延迟复用，降低迟到 UDP 包串入新会话的概率。
//! 隔离期不能替代 SRTP 认证，端口池本身也不会检查操作系统中的实际占用。
use anyhow::{ensure, Result};
use std::{
    collections::{HashSet, VecDeque},
    time::{Duration, Instant},
};

/// 一个块依次包含 A-RTP、A-RTCP、B-RTP、B-RTCP；不同 worker 的范围由控制面分开。
pub struct PortPool {
    /// 可以立即尝试绑定的四端口块起点，按队列顺序轮换。
    free: VecDeque<u16>,
    /// 已释放但仍处于隔离期的块；要求调用方按单调时刻顺序释放。
    retired: VecDeque<(Instant, u16)>,
    /// 释放到再次可分配之间的固定等待时间。
    delay: Duration,
}
impl PortPool {
    /// 只收录范围内完整的四端口块；尾部不足四个端口时直接忽略。
    pub fn new(start: u16, end: u16, delay: Duration) -> Self {
        Self::excluding(start, end, delay, &[]).expect("无排除块的端口池构造不会失败")
    }
    /// 排除操作者保留的完整块；只接受相对 start 四对齐、范围内且不重复的起点。
    /// 不把排除块放入空闲或隔离队列，因此全部容量统计自然扣除这些块。
    pub fn excluding(start: u16, end: u16, delay: Duration, excluded: &[u16]) -> Result<Self> {
        let mut unique = HashSet::with_capacity(excluded.len());
        for &port in excluded {
            ensure!(
                port >= start && u32::from(port) + 3 <= u32::from(end) && (port - start) % 4 == 0,
                "invalid excluded port block: {port}"
            );
            ensure!(unique.insert(port), "duplicate excluded port block: {port}");
        }
        let free = (u32::from(start)..=u32::from(end))
            .step_by(4)
            .filter(|p| p + 3 <= u32::from(end) && !unique.contains(&(*p as u16)))
            .map(|p| p as u16)
            .collect();
        Ok(Self {
            free,
            retired: VecDeque::new(),
            delay,
        })
    }
    /// 回收已经到期的块后取出一个候选；实际绑定失败时调用方须把它归还。
    pub fn take(&mut self, now: Instant) -> Option<u16> {
        self.reclaim(now);
        self.free.pop_front()
    }
    /// 按释放顺序回收已到期的块，不扫描尚未到期的队列尾部。
    pub fn reclaim(&mut self, now: Instant) {
        while self.retired.front().is_some_and(|(when, _)| *when <= now) {
            self.free.push_back(self.retired.pop_front().unwrap().1);
        }
    }
    /// 进入隔离队列；调用方必须保证该块属于本池且没有重复释放。
    pub fn release(&mut self, port: u16, now: Instant) {
        self.retired.push_back((now + self.delay, port));
    }
    /// 读取当前空闲数；不会隐式推进时钟，统计前应先调用 reclaim。
    pub fn free_count(&self) -> usize {
        self.free.len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    /// 排除块永不分配，边界、相对对齐和重复输入都必须显式失败。
    fn exclusions_are_validated_and_reduce_capacity() {
        let now = Instant::now();
        let mut pool = PortPool::excluding(1026, 1041, Duration::ZERO, &[1030, 1038]).unwrap();
        assert_eq!(pool.free_count(), 2);
        assert_eq!(pool.take(now), Some(1026));
        assert_eq!(pool.take(now), Some(1034));
        assert_eq!(pool.take(now), None);
        for invalid in [vec![1024], vec![1028], vec![1042], vec![1030, 1030]] {
            assert!(PortPool::excluding(1026, 1041, Duration::ZERO, &invalid).is_err());
        }
    }
    #[test]
    /// 验证尾部零散端口不被分配，已释放块必须等隔离期结束后才可复用。
    fn port_quarantine_and_incomplete_blocks() {
        let now = Instant::now();
        let mut pool = PortPool::new(20000, 20005, Duration::from_secs(2));
        assert_eq!(pool.take(now), Some(20000));
        assert_eq!(pool.take(now), None);
        pool.release(20000, now);
        assert_eq!(pool.take(now + Duration::from_secs(1)), None);
        assert_eq!(pool.take(now + Duration::from_secs(2)), Some(20000));
    }
}
