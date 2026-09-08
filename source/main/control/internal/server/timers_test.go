package server

import (
	"container/heap"
	"rustswitch/control/internal/journal"
	"testing"
	"time"
)

// TestTimerReplacementRemovalAndOrder 验证重复调度只保留一个条目，取消操作不会破坏最早到期顺序。
func TestTimerReplacementRemovalAndOrder(t *testing.T) {
	s := &Server{}
	for i := 0; i < 10000; i++ {
		s.schedule("same", 1, "", uint64(i), time.Hour)
	}
	if len(s.timers) != 1 {
		t.Fatal("replacements accumulated stale timers")
	}
	for i := 0; i < 100; i++ {
		s.schedule("different", uint64(i), "", 0, time.Duration(i)*time.Second)
	}
	for i := 0; i < 100; i += 2 {
		s.cancelTimer("different", uint64(i), "")
	}
	var last time.Time
	for len(s.timers) > 0 {
		item := heap.Pop(&s.timers).(*timerItem)
		if item.when.Before(last) {
			t.Fatal("cancellation corrupted heap order")
		}
		last = item.when
	}
}

// TestFinishedCallsDoNotRetainLongDurationTimers 确认已结束呼叫立即移除建立与最长时长定时器，仅保留短期回收任务。
func TestFinishedCallsDoNotRetainLongDurationTimers(t *testing.T) {
	log, e := journal.Open(t.TempDir()+"/events", 4096)
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	s := &Server{journal: log}
	for i := uint64(1); i <= 1000; i++ {
		s.schedule("max_duration", i, "", 0, 24*time.Hour)
		s.schedule("setup", i, "", 0, 32*time.Second)
		s.finish(&Call{ID: i}, "test", false)
	}
	if len(s.timers) != 1000 {
		t.Fatalf("long-lived obsolete timers retained: %d", len(s.timers))
	}
	for _, timer := range s.timers {
		if timer.kind != "call_gc" {
			t.Fatalf("retained %s", timer.kind)
		}
	}
}
