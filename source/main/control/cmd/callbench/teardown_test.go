// 本文件验证拆线工作者上限与逐路重试，不启动服务或改变媒体验收窗口。
package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestTeardownPoolCapsWorkersAndVisitsEveryCall 确认五千通话只启动64个工作者且每条恰好处理一次。
func TestTeardownPoolCapsWorkersAndVisitsEveryCall(t *testing.T) {
	flows := make([]*flow, 5000)
	seen := make([]int, len(flows))
	for i := range flows {
		flows[i] = &flow{index: i}
	}
	started := make(chan struct{}, teardownWorkerLimit)
	release := make(chan struct{})
	done := make(chan struct{})
	var active, peak atomic.Int32
	go func() {
		runTeardownWorkers(flows, func(group []*flow) {
			current := active.Add(1)
			for old := peak.Load(); current > old; old = peak.Load() {
				if peak.CompareAndSwap(old, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			for _, f := range group {
				seen[f.index]++
			}
			active.Add(-1)
		})
		close(done)
	}()
	for i := 0; i < teardownWorkerLimit; i++ {
		<-started
	}
	close(release)
	<-done
	if peak.Load() != teardownWorkerLimit || active.Load() != 0 {
		t.Fatal("拆线工作者数量无界或退出不完整")
	}
	for _, visits := range seen {
		if visits != 1 {
			t.Fatal("工作池遗漏或重复处理通话")
		}
	}
}

// TestTeardownRoundsKeepPerCallRequestsAndTimeouts 验证成功、拒绝、四次均超时彼此独立。
func TestTeardownRoundsKeepPerCallRequestsAndTimeouts(t *testing.T) {
	states := make([]hangupState, 3)
	for i := range states {
		states[i] = hangupState{flow: &flow{byeAnswer: make(chan bool, 1)}, wire: []byte{byte(i)}}
	}
	b := &bench{}
	now := time.Unix(0, 0)
	var attempts [3]int
	send := func(wire []byte) time.Time { attempts[wire[0]]++; return now }
	if b.advanceHangups(states, now, send) != 3 {
		t.Fatal("首轮遗漏请求")
	}
	states[0].flow.byeAnswer <- true
	states[1].flow.byeAnswer <- false
	// 499ms尚不能重发；两个已响应的通话此后不再进入重试。
	now = now.Add(499 * time.Millisecond)
	if b.advanceHangups(states, now, send) != 1 || attempts != [3]int{1, 1, 1} {
		t.Fatal("响应归属错误或未保留每路500ms等待")
	}
	now = now.Add(time.Millisecond)
	for round := 0; round < 3; round++ {
		if b.advanceHangups(states, now, send) != 1 {
			t.Fatal("未满四次请求即提前结束")
		}
		now = now.Add(teardownRetryInterval)
	}
	if b.advanceHangups(states, now, send) != 0 || attempts != [3]int{1, 1, 4} || b.teardownFailed.Load() != 2 {
		t.Fatal("逐路完整请求/超时/失败统计未保留")
	}
	b.advanceHangups(states, now, send)
	if b.teardownFailed.Load() != 2 {
		t.Fatal("终态失败被重复累计")
	}
}
