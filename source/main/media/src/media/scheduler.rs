//! 按固定会话槽位索引的最小堆；建worker时预留全部存储，每帧更新不分配树节点。
use std::time::Instant;

/// 一个key最多一个期限。remove在释放时立即删项，不保留等待将来过滤的旧代际对象。
pub struct Scheduler {
    heap: Vec<(Instant, usize)>,
    positions: Vec<Option<usize>>,
}
impl Scheduler {
    /// 容量为会话方向数上限；key由已有分配槽位推导而非不受限外部ID。
    pub fn new(capacity: usize) -> Self {
        Self {
            heap: Vec::with_capacity(capacity),
            positions: vec![None; capacity],
        }
    }
    /// 最近期限供mio计算睡眠，不扫描所有会话。
    pub fn first(&self) -> Option<Instant> {
        self.heap.first().map(|x| x.0)
    }
    /// 删除当前项后重新定位，所有交换同步维护反向索引。
    pub fn set(&mut self, key: usize, when: Option<Instant>) {
        self.remove(key);
        if let Some(when) = when {
            let index = self.heap.len();
            self.heap.push((when, key));
            self.positions[key] = Some(index);
            self.up(index);
        }
    }
    /// 仅交付真正到期项；调用方另设每轮工作预算，防止大批到期饿死控制面。
    pub fn pop_due(&mut self, now: Instant) -> Option<usize> {
        let &(when, key) = self.heap.first()?;
        if when > now {
            return None;
        }
        self.remove(key);
        Some(key)
    }
    /// 生命周期清理后立即归还堆容量；允许重复调用。
    pub fn remove(&mut self, key: usize) {
        let Some(i) = self.positions[key].take() else {
            return;
        };
        self.heap.swap_remove(i);
        if i < self.heap.len() {
            self.positions[self.heap[i].1] = Some(i);
            if i > 0 && self.heap[i] < self.heap[(i - 1) / 2] {
                self.up(i);
            } else {
                self.down(i);
            }
        }
    }
    fn swap(&mut self, a: usize, b: usize) {
        self.heap.swap(a, b);
        self.positions[self.heap[a].1] = Some(a);
        self.positions[self.heap[b].1] = Some(b);
    }
    fn up(&mut self, mut i: usize) {
        while i > 0 {
            let p = (i - 1) / 2;
            if self.heap[p] <= self.heap[i] {
                break;
            }
            self.swap(i, p);
            i = p;
        }
    }
    fn down(&mut self, mut i: usize) {
        loop {
            let left = i * 2 + 1;
            if left >= self.heap.len() {
                break;
            }
            let right = left + 1;
            let child = if right < self.heap.len() && self.heap[right] < self.heap[left] {
                right
            } else {
                left
            };
            if self.heap[i] <= self.heap[child] {
                break;
            }
            self.swap(i, child);
            i = child;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;
    #[test]
    /// 反复改期、移除与复用槽位保持堆有序，且内存容量不随帧数增长。
    fn updates_cancellation_and_reuse_stay_bounded() {
        let mut s = Scheduler::new(64);
        let t = Instant::now();
        let cap = s.heap.capacity();
        for round in 0..100 {
            for key in (0..64).rev() {
                s.set(
                    key,
                    Some(t + Duration::from_millis(((key + round) % 64) as u64)),
                );
            }
            s.remove(13);
            s.set(13, Some(t));
            let mut last = t;
            while let Some(next) = s.first() {
                assert!(next >= last);
                last = next;
                s.pop_due(next).unwrap();
            }
            assert!(s.positions.iter().all(Option::is_none));
            assert_eq!(s.heap.capacity(), cap);
        }
    }
}
