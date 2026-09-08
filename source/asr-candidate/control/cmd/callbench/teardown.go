// 本文件用固定工作者管理拆线，避免五千通话退出时同时创建五千个协程与定时器。
package main

import (
	"rustswitch/control/internal/sip"
	"sync"
	"time"
)

const teardownWorkerLimit = 64
const teardownRetryInterval = 500 * time.Millisecond

// hangupState 保留每路原有四次 BYE 尝试和独立截止时间，不因工作池排队减少请求或统计。
type hangupState struct {
	flow        *flow
	wire        []byte
	attempts    int
	nextAttempt time.Time
	complete    bool
}

// runTeardownWorkers 把固定通话集合均匀分片给最多64个工作者；每条状态只被一个工作者更新。
func runTeardownWorkers(flows []*flow, work func([]*flow)) {
	workers := min(teardownWorkerLimit, len(flows))
	var running sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		first, last := len(flows)*worker/workers, len(flows)*(worker+1)/workers
		running.Add(1)
		go func(assigned []*flow) {
			defer running.Done()
			work(assigned)
		}(flows[first:last])
	}
	running.Wait()
}

// teardownCalls 保留失败 INVITE 的清理和全部已建立通话的拆线，SIP 写入另设有界期限。
func (b *bench) teardownCalls() {
	accepted := make([]*flow, 0, len(b.flows))
	for _, f := range b.flows {
		if f.accepted.Load() {
			accepted = append(accepted, f)
		}
	}
	// 写入期限覆盖本阶段全部共用 caller 的请求；不可写时仍保留逐路失败结果，不能无限卡住。
	if err := b.caller.SetWriteDeadline(time.Now().Add(4 * time.Second)); err != nil {
		b.teardownFailed.Add(uint64(len(accepted)))
		return
	}
	for _, f := range b.flows {
		if f.abandoned.Load() {
			b.abandon(f)
		}
	}
	runTeardownWorkers(accepted, b.hangupGroup)
}

// hangupGroup 在一个工作者内轮询其分片的独立响应通道，只复用一个20ms检查定时器。
// 不逐路阻塞两秒，避免全部不应答时64工作者依次排队使五千路拆线拖到数分钟。
func (b *bench) hangupGroup(flows []*flow) {
	states := make([]hangupState, 0, len(flows))
	for _, f := range flows {
		ack, err := sip.Parse(f.ack.Load().([]byte))
		if err != nil {
			b.teardownFailed.Add(1)
			continue
		}
		wire := sip.Request("BYE", ack.URI, b.callerContact, "z9hG4bK"+identifier(), ack.Header("from"), ack.Header("to"), f.id, 2, nil)
		states = append(states, hangupState{flow: f, wire: wire})
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	send := func(wire []byte) time.Time {
		_, _ = b.caller.WriteToUDPAddrPort(wire, b.target)
		// 每路500ms从该次真实写入返回起算，不用本轮扫描起点缩短稍后请求的等待时间。
		return time.Now()
	}
	for b.advanceHangups(states, time.Now(), send) > 0 {
		<-ticker.C
	}
}

// advanceHangups 处理一轮到期与已到达响应；send 返回提交完成时刻，供确定性故障测试注入。
// 拒绝响应立即失败，四次请求各等待500ms后仍无响应才失败，每条状态只累计一次终态。
func (b *bench) advanceHangups(states []hangupState, now time.Time, send func([]byte) time.Time) int {
	pending := 0
	for i := range states {
		state := &states[i]
		if state.complete {
			continue
		}
		select {
		case ok := <-state.flow.byeAnswer:
			state.complete = true
			if !ok {
				b.teardownFailed.Add(1)
			}
			continue
		default:
		}
		if !now.Before(state.nextAttempt) {
			if state.attempts == 4 {
				state.complete = true
				b.teardownFailed.Add(1)
				continue
			}
			state.nextAttempt = send(state.wire).Add(teardownRetryInterval)
			state.attempts++
		}
		pending++
	}
	return pending
}
