// 本文件在媒体窗口前冻结发送计划，避免每轮查找连接、分配包体或竞争共享 UDP 写锁。
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

const mediaPacketInterval = 20 * time.Millisecond

// mediaSendBatch 的所有报文必须共用 writer 对应的同一个 fd；不能跨 fd 做 sendmmsg。
type mediaSendBatch struct {
	writer  packetWriter
	packets []mediaDatagram
}

// mediaSendPlan 的相位列表和包体只由对应发送工作者使用；空相位不启动定时等待。
type mediaSendPlan struct {
	groups  [][]mediaSendBatch
	writers []packetWriter
	worker  int
	workers int
}

// endpointFlowCounts 按实际端点计算共享规模，独占连接只分配单包描述符和接收缓冲。
func (b *bench) endpointFlowCounts() map[*net.UDPConn]int {
	counts := map[*net.UDPConn]int{}
	for _, f := range b.flows {
		if f.accepted.Load() {
			counts[f.sockets[0]]++
			counts[f.sockets[2]]++
		}
	}
	return counts
}

// prepareSendPlans 根据实际共享连接分配唯一工作者，兼容组数不能被工作者数整除的情况。
// writerFactory 仅用于准备阶段的真实适配或单元测试，不在每包热路径进行函数注入。
func (b *bench) prepareSendPlans(workers int, writerFactory func(*net.UDPConn, bool, int) (packetWriter, error)) ([]mediaSendPlan, error) {
	slots := 1
	if b.spread {
		slots = b.phaseSlots
	}
	plans := make([]mediaSendPlan, workers)
	// 两端连接分别登记，任何实际 fd 只能归一个发送者；建立阶段已经冻结 sockets。
	endpointFlows := b.endpointFlowCounts()
	owners := map[*net.UDPConn]int{}
	writers := map[*net.UDPConn]packetWriter{}
	indexes := make([][]map[*net.UDPConn]int, workers)
	for worker := range plans {
		plans[worker] = mediaSendPlan{groups: make([][]mediaSendBatch, slots), worker: worker, workers: workers}
		indexes[worker] = make([]map[*net.UDPConn]int, slots)
		for phase := range indexes[worker] {
			indexes[worker][phase] = map[*net.UDPConn]int{}
		}
	}
	frame := b.audioProfile().Frame()
	for _, f := range b.flows {
		if !f.accepted.Load() {
			continue
		}
		worker, ok := owners[f.sockets[0]]
		if !ok {
			worker = f.index % workers
		}
		phase := (f.index / workers) % slots
		// 固定相位给收端计算原始发包期限；不把实际迟到发送重锚成新的合格负载。
		f.phaseOffset = phasePlannedTime(time.Time{}, 0, phase, slots, worker, workers, b.spread).Sub(time.Time{})
		for side := 0; side < 2; side++ {
			conn := f.sockets[side*2]
			if previous, exists := owners[conn]; exists && previous != worker {
				return nil, fmt.Errorf("shared UDP endpoints have conflicting sender ownership")
			}
			owners[conn] = worker
			writer := writers[conn]
			if writer == nil {
				var err error
				writer, err = writerFactory(conn, b.connectedMedia, min(endpointFlows[conn], mediaBatchCapacity))
				if err != nil {
					return nil, err
				}
				writers[conn] = writer
				plans[worker].writers = append(plans[worker].writers, writer)
			}
			packet := make([]byte, 12+len(frame))
			packet[0], packet[1] = 0x80, b.sideProfile(side).Payload
			copy(packet[12:], frame)
			binary.BigEndian.PutUint32(packet[8:12], uint32(f.index*2+side+1))
			groups := plans[worker].groups[phase]
			index, exists := indexes[worker][phase][conn]
			if !exists || len(groups[index].packets) == mediaBatchCapacity {
				index = len(groups)
				groups = append(groups, mediaSendBatch{writer: writer, packets: make([]mediaDatagram, 0, mediaBatchCapacity)})
				indexes[worker][phase][conn] = index
			}
			groups[index].packets = append(groups[index].packets, mediaDatagram{flow: f, side: side, data: packet, destination: f.mediaDestinations[side]})
			plans[worker].groups[phase] = groups
		}
	}
	return plans, nil
}

// phasePlannedTime 以完整 20ms 周期计算相位，避免无法整除的槽数逐次截断后累积漂移。
// 工作者偏移只在 spread 模式使用；同步突发模式仍保留原有同时发包的负载定义。
func phasePlannedTime(start time.Time, cycle, phase, slots, worker, workers int, spread bool) time.Time {
	offset := time.Duration(cycle)*mediaPacketInterval + time.Duration(phase)*mediaPacketInterval/time.Duration(slots)
	if spread {
		offset += time.Duration(worker) * mediaPacketInterval / time.Duration(slots*workers)
	}
	return start.Add(offset)
}

// pacedPhaseTime 限制迟到追赶的瞬时速度：相邻实际相位至少保留名义间隔的一半。
// 不删除相位、不修改标称包量，也不延长总窗口；能力不足仍会因提供率不足而失败。
func pacedPhaseTime(planned, previousPlanned, previousActual time.Time) time.Time {
	if previousActual.IsZero() {
		return planned
	}
	minimum := previousActual.Add(planned.Sub(previousPlanned) / 2)
	if minimum.After(planned) {
		return minimum
	}
	return planned
}

// sendPlannedRTP 执行冻结计划。每个方向拥有固定包体、发送者及计数，过程中不锁通话对象。
func (b *bench) sendPlannedRTP(plan mediaSendPlan, start, deadline time.Time) {
	defer func() {
		var count uint64
		for _, writer := range plan.writers {
			count += writer.calls()
		}
		b.sendCalls.Add(count)
	}()
	if len(plan.writers) == 0 {
		return
	}
	step := b.audioProfile().TimestampStep()
	var previousPlanned, previousActual time.Time
	cycles := int(b.duration / mediaPacketInterval)
	for cycle := 0; cycle < cycles; cycle++ {
		ordinal := uint32(cycle + 1)
		for phase, batches := range plan.groups {
			if len(batches) == 0 {
				continue
			}
			planned := phasePlannedTime(start, cycle, phase, len(plan.groups), plan.worker, plan.workers, b.spread)
			next := planned
			if b.spread {
				next = pacedPhaseTime(planned, previousPlanned, previousActual)
			}
			if !next.Before(deadline) {
				return
			}
			if wait := time.Until(next); wait > 0 {
				time.Sleep(wait)
			}
			now := time.Now()
			if !now.Before(deadline) {
				return
			}
			if now.Sub(planned) > 2*time.Millisecond {
				b.lateTicks.Add(1)
			}
			previousPlanned, previousActual = planned, now
			for i := range batches {
				// 一次有限批次之后重新检查总窗口，避免迟到时仍把整个大相位发到截止时间外。
				if !time.Now().Before(deadline) {
					return
				}
				batch := &batches[i]
				for j := range batch.packets {
					datagram := &batch.packets[j]
					packet := datagram.data
					binary.BigEndian.PutUint16(packet[2:4], uint16(ordinal))
					binary.BigEndian.PutUint32(packet[4:8], ordinal*step)
					if b.mediaProcessing == "g711" {
						fillProcessedAudio(packet[12:], datagram.flow.index, datagram.side, ordinal, b.sideProfile(datagram.side).Payload)
						if time.Since(planned) >= mediaPacketInterval {
							datagram.flow.generatorLate[datagram.side]++
						}
					}
				}
				if b.mediaProcessing == "g711" {
					transmitPacketsObserved(batch.writer, batch.packets, planned, start)
				} else {
					transmitPackets(batch.writer, batch.packets)
				}
			}
		}
	}
}
