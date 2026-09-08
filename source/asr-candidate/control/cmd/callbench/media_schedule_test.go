// 本文件验证发生器调度和负载分母，不启动服务，也不把模拟时钟结果当作真实吞吐证明。
package main

import (
	"net"
	"testing"
	"time"
)

// TestSenderPlanOwnsSharedSocketsAndPreservesFiveThousandCalls 覆盖组数不能整除发送者数的写锁争用根因。
func TestSenderPlanOwnsSharedSocketsAndPreservesFiveThousandCalls(t *testing.T) {
	b := &bench{spread: true, phaseSlots: 20, duration: 10 * time.Second}
	groups := make([][4]*net.UDPConn, 128)
	for i := range groups {
		for side := range groups[i] {
			groups[i][side] = &net.UDPConn{}
		}
	}
	for i := 0; i < 5000; i++ {
		f := &flow{index: i, sockets: groups[i%len(groups)]}
		f.accepted.Store(true)
		b.flows = append(b.flows, f)
	}
	writerSockets := map[packetWriter]*net.UDPConn{}
	for _, workers := range []int{3, 4, 8} {
		plans, err := b.prepareSendPlans(workers, func(conn *net.UDPConn, _ bool, capacity int) (packetWriter, error) {
			if capacity < 1 || capacity > mediaBatchCapacity {
				t.Fatal("描述符容量越界")
			}
			w := &resultWriter{}
			writerSockets[w] = conn
			return w, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		owners := map[*net.UDPConn]int{}
		seen := make([][2]int, 5000)
		packets, batches := 0, 0
		for worker, plan := range plans {
			for _, group := range plan.groups {
				for _, batch := range group {
					batches++
					if len(batch.packets) == 0 || len(batch.packets) > mediaBatchCapacity {
						t.Fatal("批次不满足有界非空约束")
					}
					conn := writerSockets[batch.writer]
					if previous, ok := owners[conn]; ok && previous != worker {
						t.Fatal("一个共享socket被多个发送者拥有")
					}
					owners[conn] = worker
					for _, packet := range batch.packets {
						if packet.flow.sockets[packet.side*2] != conn {
							t.Fatal("错误地跨fd合并sendmmsg")
						}
						seen[packet.flow.index][packet.side]++
						packets++
					}
				}
			}
		}
		for i, sides := range seen {
			if sides != [2]int{1, 1} {
				t.Fatalf("呼叫 %d 的双向包被遗漏或重复: %v", i, sides)
			}
		}
		if packets*int(b.duration/mediaPacketInterval) != 5_000_000 || len(owners) != 256 {
			t.Fatalf("标称五百万包或共享端点数量被改变: %d / %d", packets, len(owners))
		}
		t.Logf("workers=%d 每20ms计划包=%d 同fd批次=%d 相比逐包调用上界减少 %.2fx", workers, packets, batches, float64(packets)/float64(batches))
	}
}

// TestPhaseScheduleBoundsCatchupWithoutDroppingOrdinals 防止长时间迟到后连续无间隔回放旧相位。
func TestPhaseScheduleBoundsCatchupWithoutDroppingOrdinals(t *testing.T) {
	start := time.Unix(0, 0)
	previousPlanned := phasePlannedTime(start, 0, 0, 20, 0, 8, true)
	previousActual := previousPlanned.Add(12 * time.Millisecond)
	for phase := 1; phase < 20; phase++ {
		planned := phasePlannedTime(start, 0, phase, 20, 0, 8, true)
		actual := pacedPhaseTime(planned, previousPlanned, previousActual)
		if actual.Before(planned) || actual.Sub(previousActual) < 500*time.Microsecond {
			t.Fatalf("迟到追赶违反名义时间或半相位间隔: phase=%d gap=%s", phase, actual.Sub(previousActual))
		}
		previousPlanned, previousActual = planned, actual
	}
	// 8个工作者分散在同一毫秒相位内部，不能共用完全相同的起始时刻。
	for worker := 1; worker < 8; worker++ {
		before := phasePlannedTime(start, 0, 0, 20, worker-1, 8, true)
		after := phasePlannedTime(start, 0, 0, 20, worker, 8, true)
		if after.Sub(before) != 125*time.Microsecond {
			t.Fatal("工作者起始相位未分散")
		}
	}
}

// TestPhaseScheduleProvidesAllNominalPacketsAndNoDrift 覆盖单路低负载和不能整除20ms的相位槽数。
func TestPhaseScheduleProvidesAllNominalPacketsAndNoDrift(t *testing.T) {
	start := time.Unix(0, 0)
	deadline := start.Add(10 * time.Second)
	for _, slots := range []int{1, 20, 333, 1000} {
		for _, worker := range []int{0, 7} {
			for _, phase := range []int{0, slots - 1} {
				var previousPlanned, previousActual time.Time
				for cycle := 0; cycle < 500; cycle++ {
					planned := phasePlannedTime(start, cycle, phase, slots, worker, 8, true)
					actual := pacedPhaseTime(planned, previousPlanned, previousActual)
					if actual != planned || !actual.Before(deadline) {
						t.Fatalf("正常调度少发/漂移: slots=%d worker=%d phase=%d cycle=%d", slots, worker, phase, cycle)
					}
					previousPlanned, previousActual = planned, actual
				}
			}
		}
	}
}
