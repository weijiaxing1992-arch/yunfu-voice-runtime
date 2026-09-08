// 这些测试验证发生器判定本身，成功不计为产品转码或并发容量通过。
package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/sip"
	"testing"
	"time"
)

// 测试端独立按最近PCM码字做有损转换；错误仅改PT、标签保留但其他样本错误必须被拒绝。
func oracleTargetByte(pcm int, payload uint8) byte {
	best, out := 1<<30, byte(0)
	for code := 0; code < 256; code++ {
		if distance := absProcessed(decodeProcessedSample(byte(code), payload) - pcm); distance < best {
			best, out = distance, byte(code)
		}
	}
	return out
}

func processedFixture(t *testing.T) (*bench, *flow, *net.UDPConn, netip.AddrPort, time.Time) {
	t.Helper()
	conn := &net.UDPConn{}
	source := netip.MustParseAddrPort("127.0.0.1:12000")
	f := &flow{index: 3}
	f.accepted.Store(true)
	f.sockets[0] = conn
	f.sockets[2] = &net.UDPConn{}
	f.mediaDestinations = [2]netip.AddrPort{source, netip.MustParseAddrPort("127.0.0.1:12002")}
	f.seen[0] = make([]byte, 32)
	f.seen[1] = make([]byte, 32)
	b := &bench{mediaProcessing: "g711", packetType: 0, duration: time.Second, flows: []*flow{f}}
	if err := b.prepareProcessedSources(); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1700000000, 0)
	b.mediaStarted.Store(&start)
	return b, f, conn, source, start
}

func processedTestPacket(call, sourceSide int, ordinal uint32, targetPT uint8, sequence uint16) []byte {
	p := make([]byte, 172)
	p[0], p[1] = 0x80, targetPT
	binary.BigEndian.PutUint16(p[2:4], sequence)
	binary.BigEndian.PutUint32(p[4:8], 0x91420000+ordinal*160)
	binary.BigEndian.PutUint32(p[8:12], 0x99887766)
	fillProcessedAudio(p[12:], call, sourceSide, ordinal, targetPT^8)
	for i, code := range p[12:] {
		p[12+i] = oracleTargetByte(decodeProcessedSample(code, targetPT^8), targetPT)
	}
	return p
}

func TestProcessedOracleKnownCompandingAndAllSamples(t *testing.T) {
	for _, v := range []struct {
		pt, code uint8
		pcm      int
	}{{0, 0xff, 0}, {0, 0x80, 32124}, {0, 0, -32124}, {8, 0xd5, 8}, {8, 0x55, -8}, {8, 0xaa, 32256}, {8, 0x2a, -32256}} {
		if got := decodeProcessedSample(v.code, v.pt); got != v.pcm {
			t.Fatalf("已知压扩码字错误: %+v got=%d", v, got)
		}
	}
	for _, pt := range []uint8{0, 8} {
		for _, call := range []int{0, 17, 9999} {
			for _, ordinal := range []uint32{1, 65536, 180000} {
				p := processedTestPacket(call, 1, ordinal, pt, 40000)
				word, ok := decodeProcessedWord(p[12:], pt)
				if !ok || word != processedWord(call, 1, ordinal) || !verifyProcessedSamples(p[12:], word, pt^8, pt) {
					t.Fatal("合法异律量化未通过")
				}
				// 只保留16样本标签，不校验后面144样本会让此负例错误通过。
				p[171] = oracleTargetByte(-decodeProcessedSample(p[171], pt), pt)
				if verifyProcessedSamples(p[12:], word, pt^8, pt) {
					t.Fatal("非标签样本被篡改仍通过")
				}
			}
		}
	}
	a, c := make([]byte, 160), make([]byte, 160)
	fillProcessedAudio(a, 3, 0, 10, 0)
	fillProcessedAudio(c, 3, 1, 10, 0)
	if bytes.Equal(a, c) {
		t.Fatal("两个方向内容相同")
	}
	fillProcessedAudio(c, 4, 0, 10, 0)
	if bytes.Equal(a, c) {
		t.Fatal("不同通话内容相同")
	}
	fillProcessedAudio(c, 3, 0, 11, 0)
	if bytes.Equal(a, c) {
		t.Fatal("连续帧内容相同")
	}
}

func TestProcessedSourceDemuxAndFullContent(t *testing.T) {
	b, f, conn, source, start := processedFixture(t)
	p := processedTestPacket(3, 1, 1, 0, 45000)
	b.recordProcessedRTP(conn, 0, source, p, start.Add(40*time.Millisecond))
	if f.received[0] != 1 || f.invalid[0] != 0 {
		t.Fatalf("有效真实转码未计入: %+v", f.processed[0])
	}
	// 同一共享socket但不同来源必须查另一个flow，不能靠不含呼叫身份的新SSRC猜测。
	other := &flow{index: 4}
	other.accepted.Store(true)
	other.sockets[0] = conn
	other.sockets[2] = &net.UDPConn{}
	other.mediaDestinations = [2]netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:12004"), netip.MustParseAddrPort("127.0.0.1:12006")}
	other.seen[0] = make([]byte, 32)
	b.flows = append(b.flows, other)
	if err := b.prepareProcessedSources(); err != nil {
		t.Fatal(err)
	}
	b.recordProcessedRTP(conn, 0, other.mediaDestinations[0], processedTestPacket(4, 1, 1, 0, 50000), start.Add(40*time.Millisecond))
	if other.received[0] != 1 {
		t.Fatal("共享端点按协商来源未能定位不同呼叫")
	}
	b.recordProcessedRTP(conn, 0, netip.MustParseAddrPort("127.0.0.1:19999"), p, start.Add(40*time.Millisecond))
	if b.unknownRTP.Load() != 1 {
		t.Fatal("未知源误计入正确流")
	}
	b.recordProcessedRTP(conn, 0, source, processedTestPacket(4, 1, 2, 0, 45001), start.Add(60*time.Millisecond))
	if f.processed[0].CrossFlowPackets != 1 {
		t.Fatal("串音未明确计数")
	}
}

func TestProcessedRejectsRelayCorruptionDuplicateLateAndBurst(t *testing.T) {
	for _, name := range []string{"relay", "payload_only", "codec", "body", "duplicate", "late", "burst", "timestamp", "sequence"} {
		t.Run(name, func(t *testing.T) {
			b, f, conn, source, start := processedFixture(t)
			p := processedTestPacket(3, 1, 1, 0, 45000)
			b.recordProcessedRTP(conn, 0, source, p, start.Add(40*time.Millisecond))
			p = processedTestPacket(3, 1, 2, 0, 45001)
			at := start.Add(60 * time.Millisecond)
			switch name {
			case "relay":
				binary.BigEndian.PutUint32(p[8:12], 8)
			case "payload_only":
				fillProcessedAudio(p[12:], 3, 1, 2, 8)
			case "codec":
				p[1] = 8
			case "body":
				p[100] ^= 0x80
			case "duplicate":
				p = processedTestPacket(3, 1, 1, 0, 45000)
			case "late":
				at = start.Add(101 * time.Millisecond)
			case "burst":
				at = start.Add(42 * time.Millisecond)
			case "timestamp":
				binary.BigEndian.PutUint32(p[4:8], 0x91420000+321)
			case "sequence":
				binary.BigEndian.PutUint16(p[2:4], 45002)
			}
			b.recordProcessedRTP(conn, 0, source, p, at)
			_, ok := b.processedSummary()
			if ok && f.duplicate[0] == 0 && f.invalid[0] == 0 {
				t.Fatal("坏样本仍判定可通过")
			}
		})
	}
}

func TestProcessedPLCDoesNotMaskLostFrameAndTailIsBounded(t *testing.T) {
	b, f, conn, source, start := processedFixture(t)
	last := processedTestPacket(3, 1, 50, 0, 60000)
	b.recordProcessedRTP(conn, 0, source, last, start.Add(1020*time.Millisecond))
	// 中途只收到最后一帧不可能凭尾包填足负载，原始源帧仍只有一包。
	for lost := uint32(1); lost <= 6; lost++ {
		p := processedTestPacket(3, 1, 50, 0, 60000+uint16(lost))
		binary.BigEndian.PutUint32(p[4:8], 0x91420000+(50+lost)*160)
		sourceBytes := make([]byte, 160)
		fillProcessedAudio(sourceBytes, 3, 1, 50, 8)
		for i, c := range sourceBytes {
			pcm := decodeProcessedSample(c, 8)
			for j := uint32(0); j < lost; j++ {
				pcm = pcm * 3 / 4
			}
			p[12+i] = oracleTargetByte(pcm, 0)
		}
		b.recordProcessedRTP(conn, 0, source, p, start.Add(time.Duration(51+lost)*20*time.Millisecond))
	}
	if f.received[0] != 1 || f.processed[0].TailPLCPackets != 6 {
		t.Fatalf("PLC冒充真实收包或丢失: %+v", f.processed[0])
	}
	if sufficientDirectionLoad(50, f.received[0], 50) {
		t.Fatal("一帧加PLC误通过完整负载")
	}
	p := processedTestPacket(3, 1, 50, 0, 60007)
	binary.BigEndian.PutUint32(p[4:8], 0x91420000+57*160)
	b.recordProcessedRTP(conn, 0, source, p, start.Add(1160*time.Millisecond))
	if f.invalid[0] == 0 {
		t.Fatal("超过6帧尾部仍被接受")
	}
}

func TestProcessedOptionsAndNegotiatedSourceCollision(t *testing.T) {
	for _, bad := range []struct {
		mode              string
		pt, calls, groups int
		connected, direct bool
	}{{"fake", 0, 10, 0, false, false}, {"g711", 9, 10, 0, false, false}, {"g711", 0, 10000, 33, false, false}, {"g711", 0, 10000, 0, true, false}, {"g711", 0, 10, 0, false, true}} {
		if validateProcessingOptions(bad.mode, bad.pt, bad.calls, bad.groups, bad.connected, bad.direct) == nil {
			t.Fatalf("非法范围接受 %+v", bad)
		}
	}
	if err := validateProcessingOptions("g711", 8, 10000, 32, false, false); err != nil {
		t.Fatal(err)
	}
	b, f, _, _, _ := processedFixture(t)
	b.flows = append(b.flows, f)
	if b.prepareProcessedSources() == nil {
		t.Fatal("重复协商源覆盖另一呼叫")
	}
}

func TestProcessedSDPSelectsOnlyActuallyOfferedOppositeLaw(t *testing.T) {
	for _, pt := range []uint8{0, 8} {
		b := &bench{mediaProcessing: "g711", packetType: pt, mediaConfig: config.Media{AllowedRemoteNetworks: []string{"127.0.0.0/8"}}}
		a := sip.SDP{Payload: pt, Codec: b.sideProfile(0).Spec(), PTime: 20}
		offer := sip.RenderProcessedOffer(1, "127.0.0.1", 7200, 7201, a)
		got, err := b.parseUASOffer(offer)
		if err != nil || got.Payload != pt^8 {
			t.Fatalf("实际双律报价未能选择B律: pt=%d got=%+v err=%v", pt, got, err)
		}
		if _, err = b.parseUASOffer(sip.RenderSDP(1, "127.0.0.1", 7200, 7201, a)); err == nil {
			t.Fatal("没有另一律的relay报价被假装转码")
		}
		if b.validateProcessedSDP(got, 0) == nil {
			t.Fatal("A腿接受了B腿不同编码")
		}
	}
}

func TestProcessedTenThousandCallsHaveBoundedEndpointsAndNoMissingPlans(t *testing.T) {
	b := &bench{mediaProcessing: "g711", duration: time.Second, spread: true, phaseSlots: 20}
	groups := make([][4]*net.UDPConn, 32)
	for i := range groups {
		for side := range groups[i] {
			groups[i][side] = &net.UDPConn{}
		}
	}
	for i := 0; i < 10000; i++ {
		f := &flow{index: i, sockets: groups[i%32]}
		f.accepted.Store(true)
		b.flows = append(b.flows, f)
	}
	plans, err := b.prepareSendPlans(4, func(*net.UDPConn, bool, int) (packetWriter, error) { return &resultWriter{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	var count int
	seen := make([][2]int, 10000)
	for _, plan := range plans {
		for _, group := range plan.groups {
			for _, batch := range group {
				for _, p := range batch.packets {
					count++
					seen[p.flow.index][p.side]++
					if len(p.data) != 172 || p.data[1] != uint8(p.side*8) {
						t.Fatal("两个方向律或帧长错误")
					}
				}
			}
		}
	}
	if count != 20000 || len(b.endpointFlowCounts()) != 64 {
		t.Fatalf("降载或增加每路reader: packets=%d readers=%d", count, len(b.endpointFlowCounts()))
	}
	for i, sides := range seen {
		if sides != [2]int{1, 1} {
			t.Fatalf("第%d路漏方向", i)
		}
	}
}
