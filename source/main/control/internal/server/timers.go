package server

import (
	"container/heap"
	"time"
)

// timerKey 为同一个业务定时器提供稳定身份，重排时直接替换而不积累失效条目。
type timerKey struct {
	kind string // 超时、重传或垃圾回收等定时器类别。
	id   uint64 // 所属呼叫编号；全局缓存使用零。
	key  string // 事务键或缓存键，用于区分同一通话的多个任务。
}

// timerItem 同时保存到期时刻和堆位置，支持按身份及时取消。
type timerItem struct {
	when    time.Time // 绝对到期时间，使用单调时钟比较。
	kind    string    // 定时器类别。
	id      uint64    // 所属呼叫编号。
	key     string    // 事务或缓存标识。
	version uint64    // 业务对象版本，防止旧超时影响新状态。
	index   int       // 当前最小堆位置，移除后为负一。
}

// identity 返回供索引表使用的唯一身份。
func (t *timerItem) identity() timerKey { return timerKey{t.kind, t.id, t.key} }

// timerHeap 是按最早到期排序的最小堆，仅由呼叫主循环访问。
type timerHeap []*timerItem

// Len 满足标准库堆接口，返回当前活跃定时器数量。
func (h timerHeap) Len() int { return len(h) }

// Less 满足标准库堆接口，将较早到期的条目排在前面。
func (h timerHeap) Less(i, j int) bool { return h[i].when.Before(h[j].when) }

// Swap 同步修改两个条目的位置索引，保证按身份取消时能找到正确条目。
func (h timerHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }

// Push 在堆尾加入新条目，后续排序由标准库完成。
func (h *timerHeap) Push(x any) {
	value := x.(*timerItem)
	value.index = len(*h)
	*h = append(*h, value)
}

// Pop 清空移除位置的指针，避免底层切片长时间持有已取消对象。
func (h *timerHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	value.index = -1
	return value
}

// schedule 新增或替换定时器；同一身份反复刷新只占一个堆条目。
func (s *Server) schedule(kind string, id uint64, key string, version uint64, delay time.Duration) {
	if s.timerIndex == nil {
		s.timerIndex = make(map[timerKey]*timerItem)
	}
	identity := timerKey{kind, id, key}
	when := time.Now().Add(delay)
	if existing := s.timerIndex[identity]; existing != nil {
		existing.when = when
		existing.version = version
		heap.Fix(&s.timers, existing.index)
		return
	}
	item := &timerItem{when: when, kind: kind, id: id, key: key, version: version}
	s.timerIndex[identity] = item
	heap.Push(&s.timers, item)
	s.Stats.Timers.Store(uint64(len(s.timers)))
}

// cancelTimer 即时移除指定身份，防止高周转呼叫留下长达数小时的失效时长定时器。
func (s *Server) cancelTimer(kind string, id uint64, key string) {
	identity := timerKey{kind, id, key}
	if existing := s.timerIndex[identity]; existing != nil {
		heap.Remove(&s.timers, existing.index)
		delete(s.timerIndex, identity)
		s.Stats.Timers.Store(uint64(len(s.timers)))
	}
}

// expireTimers 每轮最多处理 1024 个到期动作，让信令和媒体控制结果仍有机会进入主循环。
func (s *Server) expireTimers() {
	now := time.Now()
	for count := 0; count < 1024 && len(s.timers) > 0 && !s.timers[0].when.After(now); count++ {
		item := heap.Pop(&s.timers).(*timerItem)
		delete(s.timerIndex, item.identity())
		s.Stats.Timers.Store(uint64(len(s.timers)))
		s.onTimer(*item)
	}
}

// bucket 是未装配 AdmissionGuard 时保留的静态令牌桶，只能由呼叫主循环访问。
type bucket struct {
	tokens, capacity, rate float64   // 余额、最大余额及每秒补充速率。
	last                   time.Time // 上次补充时刻。
}

// newBucket 建立一个初始填满的静态令牌桶。
func newBucket(rate, capacity int) *bucket {
	return &bucket{float64(capacity), float64(capacity), float64(rate), time.Now()}
}

// take 结算经过时间并尝试消费一通新呼叫的额度；余额不足时直接拒绝而不排队。
func (b *bucket) take(now time.Time) bool {
	b.tokens = min(b.capacity, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
