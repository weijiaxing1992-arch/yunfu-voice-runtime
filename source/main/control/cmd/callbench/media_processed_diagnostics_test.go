// 本文件只验证独立发生器的错误归因；不把模拟包/模拟时钟结果当产品或容量通过。
package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestProcessedDiagnosticsDistinguishGapFromPersistentClockShift(t *testing.T) {
	for _, offset := range []uint32{0, 504} {
		t.Run(map[uint32]string{0: "source_gap", 504: "clock_reanchor"}[offset], func(t *testing.T) {
			b, f, conn, source, start := processedFixture(t)
			b.recordProcessedRTP(conn, 0, source, processedTestPacket(3, 1, 1, 0, 45000), start.Add(40*time.Millisecond))
			// 不发第2帧，后续RTP seq连续但源时间戳按真实第3..10帧跳格。
			for ordinal := uint32(3); ordinal <= 10; ordinal++ {
				p := processedTestPacket(3, 1, ordinal, 0, uint16(44999+ordinal-1))
				binary.BigEndian.PutUint32(p[4:8], binary.BigEndian.Uint32(p[4:8])+offset)
				b.recordProcessedRTP(conn, 0, source, p, start.Add(time.Duration(ordinal+1)*20*time.Millisecond))
			}
			s := &f.processed[0]
			if s.SequenceErrors != 0 || s.diagnostic.ContentValid != 9 {
				t.Fatalf("序号/独立PCM观察错误: %+v", s)
			}
			if offset == 0 {
				if f.received[0] != 9 || s.TimestampErrors != 0 || s.diagnostic.first.Reason != "" {
					t.Fatal("合法源时间格缺口被误判成时钟偏移")
				}
			} else {
				if f.received[0] != 1 || s.TimestampErrors != 8 || s.diagnostic.ContentValidTimestampErrors != 8 {
					t.Fatal("持续时钟偏移被接收计数或重锚掩盖")
				}
				sample := s.diagnostic.first
				if sample.Reason != "timestamp" || sample.FrameOrdinal != 3 || sample.PreviousFrameOrdinal != 1 || sample.TimestampBase != 0x91420000 || !sample.ContentValid {
					t.Fatalf("首错缺少原时钟/上一包证据: %+v", sample)
				}
				_, passed := b.processedSummary()
				if passed {
					t.Fatal("独立PCM计数不能把时间轴错误改绿")
				}
			}
		})
	}
}

func TestProcessedDiagnosticsObserveLateAndCorruptionDespiteClockFailure(t *testing.T) {
	b, f, conn, source, start := processedFixture(t)
	b.recordProcessedRTP(conn, 0, source, processedTestPacket(3, 1, 1, 0, 45000), start.Add(40*time.Millisecond))
	p := processedTestPacket(3, 1, 2, 0, 45001)
	binary.BigEndian.PutUint32(p[4:8], 0x91420000+321)
	b.recordProcessedRTP(conn, 0, source, p, start.Add(101*time.Millisecond))
	// 同时含时戳错误和完整PCM；旧late口径不变，新字段补充被前序拒绝遮住的迟到。
	s := &f.processed[0]
	if s.LatePackets != 0 || s.diagnostic.ContentValidLate != 1 || s.diagnostic.ContentValidTimestampErrors != 1 || f.received[0] != 1 {
		t.Fatal("时戳/迟到/成功计数未独立")
	}
	p = processedTestPacket(3, 1, 3, 0, 45002)
	binary.BigEndian.PutUint32(p[4:8], 0x91420000+481)
	p[171] ^= 0x80
	b.recordProcessedRTP(conn, 0, source, p, start.Add(121*time.Millisecond))
	if s.diagnostic.ContentValid != 2 || s.diagnostic.ContentValidLate != 1 || s.TimestampErrors != 2 {
		t.Fatal("只保留标签的损坏音频误计独立完整PCM")
	}
}

func TestProcessedDiagnosticsBoundRawEvidenceAndRetainEarliest(t *testing.T) {
	b, f, conn, source, start := processedFixture(t)
	b.recordProcessedRTP(conn, 0, source, processedTestPacket(3, 1, 1, 0, 45000), start.Add(40*time.Millisecond))
	p := processedTestPacket(3, 1, 2, 0, 45001)
	binary.BigEndian.PutUint32(p[4:8], 0x91420000+321)
	b.recordProcessedRTP(conn, 0, source, p, start.Add(60*time.Millisecond))
	// 反向遍历100方向，证明保留的是最早32项而非最先遍历到的32个flow。
	b.flows = nil
	for i := 99; i >= 0; i-- {
		other := &flow{index: i, mediaDestinations: f.mediaDestinations}
		other.processed[0].diagnostic = f.processed[0].diagnostic
		other.processed[0].diagnostic.first.CallIndex = i
		other.processed[0].diagnostic.first.at = start.Add(time.Duration(i) * time.Millisecond)
		other.processed[0].diagnostic.first.ObservedAtNS = uint64(i) * 1_000_000
		b.flows = append(b.flows, other)
	}
	report := b.processedDiagnosticsSummary()
	samples := report["first_receive_errors"].([]processedReceiveError)
	if len(samples) != 32 || report["receiver_error_flows"] != 100 || report["truncated_receiver_flows"] != 68 {
		t.Fatal("首错样本预算或省略数错误")
	}
	for i, sample := range samples {
		raw, err := base64.StdEncoding.DecodeString(sample.PacketBase64)
		if err != nil || len(raw) != 172 || sample.CallIndex != i || binary.BigEndian.Uint32(raw[4:8]) != sample.RTPTimestamp || sample.SourceAddress != source.String() {
			t.Fatalf("第%d项原包/身份不一致", i)
		}
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatal(err)
	}
}

func TestProcessedWriteObservationIsAnIntervalAndDoesNotChangeAcceptance(t *testing.T) {
	start := time.Unix(1700000000, 0)
	f := &flow{index: 5}
	p := mediaDatagram{flow: f, side: 1, data: processedTestPacket(5, 1, 2, 0, 45001)}
	binary.BigEndian.PutUint32(p.data[4:8], 2*160)
	planned := start.Add(20 * time.Millisecond)
	// API开始未迟到，返回已迟到；不能把整个区间误称确定的内核发送迟到。
	recordProcessedWrite(&p, planned, start, start.Add(25*time.Millisecond), start.Add(45*time.Millisecond))
	d := &f.processedWrites[1]
	if d.AttemptLate != 0 || d.ReturnLate != 1 || d.MaxCallNS != uint64(20*time.Millisecond) || f.generatorLate[1] != 0 {
		t.Fatal("写入区间被误当确定提交时刻或污染既有判定")
	}
	// 第二次确实在期限后才开始；首样本不可被覆盖，累计数仍保留。
	p.data = processedTestPacket(5, 1, 3, 0, 45002)
	binary.BigEndian.PutUint32(p.data[4:8], 3*160)
	recordProcessedWrite(&p, start.Add(40*time.Millisecond), start, start.Add(63*time.Millisecond), start.Add(64*time.Millisecond))
	if d.AttemptLate != 1 || d.ReturnLate != 2 || d.first.FrameOrdinal != 2 {
		t.Fatal("写入迟到未累计或首证据被覆盖")
	}
	// 测试发送packet应是source时戳，不借用随机输出基准。
	binary.BigEndian.PutUint32(p.data[4:8], 3*160)
	fresh := &flow{index: 5}
	p.flow = fresh
	recordProcessedWrite(&p, start.Add(40*time.Millisecond), start, start.Add(63*time.Millisecond), start.Add(64*time.Millisecond))
	if fresh.processedWrites[1].first.FrameOrdinal != 3 {
		t.Fatal("发送帧编号错误")
	}
}

func TestProcessedWriteDiagnosticsOnlyRecordSuccessfulPrefix(t *testing.T) {
	packets := []mediaDatagram{{flow: &flow{}, data: make([]byte, 172)}, {flow: &flow{}, data: make([]byte, 172)}}
	w := &resultWriter{results: []batchResult{{lengths: []int{172}, err: errors.New("bounded write failure")}}}
	planned := time.Now().Add(-30 * time.Millisecond)
	transmitPacketsObserved(w, packets, planned, planned.Add(-time.Second))
	if packets[0].flow.processedWrites[0].AttemptLate != 1 || packets[1].flow.processedWrites[0].AttemptLate != 0 || packets[0].flow.sent[0] != 1 || packets[1].flow.writeErrors[0] != 1 || w.count != 1 {
		t.Fatal("失败后缀被诊断伪装成实际提交或重试前缀")
	}
}

func TestProcessedDiagnosticHotPathDoesNotAllocate(t *testing.T) {
	b, f, conn, source, start := processedFixture(t)
	p := processedTestPacket(3, 1, 1, 0, 45000)
	allocations := testing.AllocsPerRun(100, func() {
		f.processed[0] = processedReceiveState{}
		f.seen[0][0] = 0
		b.recordProcessedRTP(conn, 0, source, p, start.Add(40*time.Millisecond))
	})
	if allocations != 0 {
		t.Fatalf("正确包热路径每包分配: %v", allocations)
	}
	allocations = testing.AllocsPerRun(100, func() {
		f.processed[0] = processedReceiveState{}
		f.seen[0][0] = 0
		b.recordProcessedRTP(conn, 0, source, p, start.Add(100*time.Millisecond))
	})
	if allocations != 0 {
		t.Fatalf("首错记录每包分配: %v", allocations)
	}
}
