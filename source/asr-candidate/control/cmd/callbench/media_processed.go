// 本文件是发生器独立PCM内容oracle；不调用服务端编解码实现，不用保留SSRC证明转码。
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"rustswitch/control/internal/codecprofile"
	"rustswitch/control/internal/sip"
	"strings"
	"time"
)

const (
	processedMaxSocketGroups = 32
	processedReceiveLimit    = 80 * time.Millisecond
	processedBurstGap        = 5 * time.Millisecond
	processedSamples         = 160
	processedMagic           = uint64(0xc31e)
)

// 端点+协商来源才是处理后RTP的通话归属；SSRC属于服务端重新生成的发送流。
type processedEndpointKey struct {
	conn   *net.UDPConn
	side   int
	source netip.AddrPort
}

// 所有计数只由该端点读者修改，最终停止读者后汇总，避免每包原子与锁开销。
type processedReceiveState struct {
	ObservedPackets        uint64 `json:"observed_packets"`
	ContentVerifiedPackets uint64 `json:"content_verified_packets"`
	ContentMismatchPackets uint64 `json:"content_mismatch_packets"`
	CrossFlowPackets       uint64 `json:"cross_flow_packets"`
	CodecMismatchPackets   uint64 `json:"codec_mismatch_packets"`
	TimestampErrors        uint64 `json:"timestamp_errors"`
	SequenceErrors         uint64 `json:"sequence_errors"`
	IdentityErrors         uint64 `json:"identity_errors"`
	LatePackets            uint64 `json:"late_packets"`
	BurstPackets           uint64 `json:"burst_packets"`
	TailPLCPackets         uint64 `json:"tail_plc_packets"`
	initialized            bool
	ssrc, timestampBase    uint32
	lastSequence           uint16
	lastArrival            time.Time
	lastPosition           uint32
	diagnostic             processedReceiveDiagnostic
}

var processedProfiles = func() [2]codecprofile.Profile {
	u, _ := codecprofile.Lookup(0)
	a, _ := codecprofile.Lookup(8)
	return [2]codecprofile.Profile{u, a}
}()

// 独立标量G711展开公式，只在初始化256码字表时执行；不依赖生产C/Rust库。
func decodeProcessedSample(code byte, payload uint8) int {
	if payload == 0 {
		u := ^code
		value := ((int(u&15) << 3) + 132) << ((u >> 4) & 7)
		value -= 132
		if u&128 != 0 {
			return -value
		}
		return value
	}
	a := code ^ 0x55
	value := int(a&15) << 4
	segment := (a >> 4) & 7
	if segment == 0 {
		value += 8
	} else {
		value += 264
		value <<= segment - 1
	}
	if a&128 == 0 {
		return -value
	}
	return value
}

// 16个相隔2048的PCM电平携带完整内容，跨律量化的容许误差固定512，不能接受错误的电平。
// 循环波形预分配在只读表，热路径只复制144样本和生成16样本身份，避免每包分配或随机源调用。
var processedOracle = func() struct {
	decoded [2][256]int
	encoded [2][16]byte
	body    [2][1168]byte
	nibbles [1168]byte
} {
	var o struct {
		decoded [2][256]int
		encoded [2][16]byte
		body    [2][1168]byte
		nibbles [1168]byte
	}
	for law, payload := range []uint8{0, 8} {
		for code := 0; code < 256; code++ {
			o.decoded[law][code] = decodeProcessedSample(byte(code), payload)
		}
		for nibble := 0; nibble < 16; nibble++ {
			target := (nibble*2 - 15) * 1024
			best := 1 << 30
			for code, pcm := range o.decoded[law] {
				if distance := absProcessed(pcm - target); distance < best {
					best = distance
					o.encoded[law][nibble] = byte(code)
				}
			}
		}
	}
	for index := 0; index < 1024; index++ {
		o.nibbles[index] = byte(processedMix(uint64(index)+0x51a7) & 15)
	}
	copy(o.nibbles[1024:], o.nibbles[:144])
	for law := range o.body {
		for index, nibble := range o.nibbles {
			o.body[law][index] = o.encoded[law][nibble]
		}
	}
	return o
}()

func absProcessed(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func processedLaw(payload uint8) int {
	if payload == 8 {
		return 1
	}
	return 0
}
func processedMix(n uint64) uint64 {
	n ^= n >> 30
	n *= 0xbf58476d1ce4e5b9
	n ^= n >> 27
	n *= 0x94d049bb133111eb
	return n ^ (n >> 31)
}

// 64位元数据：固定magic16、call14、方向1、frame18、校验15；3600秒180000帧不会回绕。
func processedWord(call, side int, ordinal uint32) uint64 {
	word := processedMagic<<48 | uint64(call)<<34 | uint64(side)<<33 | uint64(ordinal)<<15
	return word | (processedMix(word) & 0x7fff)
}

func fillProcessedAudio(body []byte, call, side int, ordinal uint32, payload uint8) {
	word := processedWord(call, side, ordinal)
	law := processedLaw(payload)
	for index := 0; index < 16; index++ {
		body[index] = processedOracle.encoded[law][byte(word>>uint(60-index*4))&15]
	}
	offset := int(processedMix(word) & 1023)
	copy(body[16:160], processedOracle.body[law][offset:offset+144])
}

func (b *bench) sideProfile(side int) codecprofile.Profile {
	if b.mediaProcessing != "g711" {
		return b.audioProfile()
	}
	law := processedLaw(b.packetType)
	if side == 1 {
		law ^= 1
	}
	return processedProfiles[law]
}

func validateProcessingOptions(mode string, payload, calls, groups int, connected, direct bool) error {
	if mode != "relay" && mode != "g711" {
		return fmt.Errorf("media-processing must be relay or g711")
	}
	if mode == "relay" {
		return nil
	}
	if payload != 0 && payload != 8 {
		return fmt.Errorf("g711 processing requires payload 0 or 8 at 20ms")
	}
	if direct {
		return fmt.Errorf("g711 processing cannot bypass the media server")
	}
	if groups > processedMaxSocketGroups {
		return fmt.Errorf("g711 socket groups must not exceed 32 (64 RTP readers); calls are unchanged")
	}
	if connected && (groups != 0 || calls > processedMaxSocketGroups) {
		return fmt.Errorf("g711 connected media requires dedicated endpoints and at most 32 calls; larger tests require shared endpoints")
	}
	return nil
}

func (b *bench) validateProcessedSDP(s sip.SDP, side int) error {
	p := b.sideProfile(side)
	c := s.CodecMetadata()
	if s.Payload != p.Payload || c.Name != p.Name || c.SampleRate != 8000 || c.RTPClockRate != 8000 || c.Channels != 1 || s.PTime != 20 {
		return fmt.Errorf("processed leg %d expected %s/PT%d/8000 mono/20ms; received %s/PT%d/%dms", side, p.Name, p.Payload, c.Name, s.Payload, s.PTime)
	}
	return nil
}

// ParseSDP选择首个主编码；被叫必须从真实报价中选择另一律，不能凭载荷号凭空宣告报价存在。
func (b *bench) parseUASOffer(body []byte) (sip.SDP, error) {
	if b.mediaProcessing != "g711" {
		return sip.ParseSDP(body, b.mediaConfig)
	}
	if len(body) > 8192 {
		return sip.SDP{}, fmt.Errorf("offer too large")
	}
	wanted := fmt.Sprint(b.sideProfile(1).Payload)
	lines := strings.Split(string(body), "\n")
	found := false
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "m=audio" {
			for _, pt := range fields[3:] {
				if pt == wanted {
					found = true
				}
			}
			if !found {
				return sip.SDP{}, fmt.Errorf("upstream did not offer required opposite G711 law")
			}
			// 只调整候选优先级，保留原有PT和属性给严格解析器核验，不能制造孤立rtpmap。
			ordered := append([]string(nil), fields[:3]...)
			ordered = append(ordered, wanted)
			for _, pt := range fields[3:] {
				if pt != wanted {
					ordered = append(ordered, pt)
				}
			}
			lines[i] = strings.Join(ordered, " ") + "\r"
		}
	}
	if !found {
		return sip.SDP{}, fmt.Errorf("no audio offer")
	}
	s, err := sip.ParseSDP([]byte(strings.Join(lines, "\n")), b.mediaConfig)
	if err == nil {
		err = b.validateProcessedSDP(s, 1)
	}
	return s, err
}

// 同一接收端点若出现重复协商源就直接拒绝准备，不能让最后写入覆盖另一通呼叫。
func (b *bench) prepareProcessedSources() error {
	if b.mediaProcessing != "g711" {
		return nil
	}
	b.processedSources = make(map[processedEndpointKey]*flow, len(b.flows)*2)
	for _, f := range b.flows {
		if f.accepted.Load() {
			for side := 0; side < 2; side++ {
				key := processedEndpointKey{f.sockets[side*2], side, f.mediaDestinations[side]}
				if !key.source.IsValid() {
					return fmt.Errorf("processed call %d has no negotiated media destination", f.index)
				}
				if b.processedSources[key] != nil {
					return fmt.Errorf("processed calls share the same negotiated media source")
				}
				b.processedSources[key] = f
			}
		}
	}
	return nil
}

// decodeProcessedWord先用目标律真实展开PCM，再解析有界电平编码；错误PT重写不能通过此步骤。
func decodeProcessedWord(body []byte, payload uint8) (uint64, bool) {
	if len(body) != 160 {
		return 0, false
	}
	law := processedLaw(payload)
	var word uint64
	for _, code := range body[:16] {
		pcm := processedOracle.decoded[law][code]
		nibble := (pcm + 16384) / 2048
		if nibble < 0 || nibble > 15 || absProcessed(pcm-(nibble*2-15)*1024) > 512 {
			return 0, false
		}
		word = word<<4 | uint64(nibble)
	}
	return word, word>>48 == processedMagic && word&0x7fff == processedMix(word&^0x7fff)&0x7fff
}

// 每个样本都必须接近来源律实际解码后的电平，固定512误差覆盖这组电平的有损G711量化。
func verifyProcessedSamples(body []byte, word uint64, sourcePayload, targetPayload uint8) bool {
	source, target := processedLaw(sourcePayload), processedLaw(targetPayload)
	offset := int(processedMix(word) & 1023)
	for index, code := range body {
		nibble := byte(0)
		if index < 16 {
			nibble = byte(word>>uint(60-index*4)) & 15
		} else {
			nibble = processedOracle.nibbles[offset+index-16]
		}
		expected := processedOracle.decoded[source][processedOracle.encoded[source][nibble]]
		if absProcessed(processedOracle.decoded[target][code]-expected) > 512 {
			return false
		}
	}
	return true
}

// 首批图的尾部PLC是每帧3/4的有限历史衰减；核对全部样本，不能把任意额外数据藏进尾包额度。
func verifyProcessedTail(body []byte, word uint64, sourcePayload, targetPayload uint8, losses uint32) bool {
	source, target := processedLaw(sourcePayload), processedLaw(targetPayload)
	offset := int(processedMix(word) & 1023)
	for index, code := range body {
		var nibble byte
		if index < 16 {
			nibble = byte(word>>uint(60-index*4)) & 15
		} else {
			nibble = processedOracle.nibbles[offset+index-16]
		}
		expected := processedOracle.decoded[source][processedOracle.encoded[source][nibble]]
		for lost := uint32(0); lost < losses; lost++ {
			expected = expected * 3 / 4
		}
		if absProcessed(processedOracle.decoded[target][code]-expected) > 512 {
			return false
		}
	}
	return true
}

// recordProcessedPacket保持原有通过门槛；外层诊断复用同一完整PCM校验结果，不改时钟基准。
func (b *bench) recordProcessedPacket(f *flow, side int, packet []byte, now time.Time, word uint64, wordOK, contentValid bool) {
	state := &f.processed[side]
	state.ObservedPackets++
	if len(packet) != 172 || packet[0] != 0x80 {
		f.invalid[side]++
		state.ContentMismatchPackets++
		return
	}
	if packet[1]&127 != b.sideProfile(side).Payload {
		f.invalid[side]++
		state.CodecMismatchPackets++
		return
	}
	ssrc := binary.BigEndian.Uint32(packet[8:12])
	ts := binary.BigEndian.Uint32(packet[4:8])
	seq := binary.BigEndian.Uint16(packet[2:4])
	call := int((word >> 34) & 0x3fff)
	direction := int((word >> 33) & 1)
	ordinal := uint32((word >> 15) & 0x3ffff)
	nominal := uint32(b.duration / mediaPacketInterval)
	if !state.initialized {
		if !wordOK || ordinal < 1 || ordinal > nominal {
			f.invalid[side]++
			state.ContentMismatchPackets++
			return
		}
		if call != f.index || direction != (side^1) {
			f.invalid[side]++
			state.CrossFlowPackets++
			return
		}
		state.initialized = true
		state.ssrc = ssrc
		state.timestampBase = ts - ordinal*160
		state.lastSequence = seq - 1
		// 随机的新序列号可恰好等于输入序号，不能以1/65536概率误杀万路测试；SSRC和完整转码内容共同证明终结。
		if ssrc == 0 || ssrc == uint32(f.index*2+(side^1)+1) {
			state.IdentityErrors++
			f.invalid[side]++
		}
	}
	if ssrc != state.ssrc {
		state.IdentityErrors++
		f.invalid[side]++
		return
	}
	if seq != state.lastSequence+1 {
		state.SequenceErrors++
		f.invalid[side]++
	}
	state.lastSequence = seq
	if !state.lastArrival.IsZero() && now.Sub(state.lastArrival) < processedBurstGap {
		state.BurstPackets++
	}
	state.lastArrival = now
	delta := ts - state.timestampBase
	position := delta / 160
	if delta%160 != 0 || position == 0 {
		state.TimestampErrors++
		f.invalid[side]++
		return
	}
	if position < state.lastPosition {
		state.TimestampErrors++
		f.reordered[side]++
		f.invalid[side]++
		return
	}
	state.lastPosition = position
	// 输入结束后的有限PLC拥有未来时间位置，仅单列最多6包；不算真实内容、不冲抵丢失。
	if position > nominal {
		if position <= nominal+6 && state.TailPLCPackets < 6 {
			if !verifyProcessedTail(packet[12:], processedWord(f.index, side^1, nominal), b.sideProfile(side^1).Payload, b.sideProfile(side).Payload, position-nominal) {
				state.ContentMismatchPackets++
				f.invalid[side]++
				return
			}
			state.TailPLCPackets++
			return
		}
		state.TimestampErrors++
		f.invalid[side]++
		return
	}
	if !wordOK {
		state.ContentMismatchPackets++
		f.invalid[side]++
		return
	}
	if call != f.index || direction != side^1 {
		state.CrossFlowPackets++
		f.invalid[side]++
		return
	}
	if ordinal != position || ordinal < 1 || ordinal > nominal {
		state.TimestampErrors++
		state.ContentMismatchPackets++
		f.invalid[side]++
		return
	}
	if !contentValid {
		state.ContentMismatchPackets++
		f.invalid[side]++
		return
	}
	if started := b.mediaStarted.Load(); started != nil {
		planned := started.Add(f.phaseOffset + time.Duration(ordinal-1)*mediaPacketInterval)
		if now.Sub(planned) > processedReceiveLimit {
			state.LatePackets++
		}
	} else {
		state.TimestampErrors++
		f.invalid[side]++
		return
	}
	index, mask := ordinal/8, byte(1<<(ordinal%8))
	if int(index) >= len(f.seen[side]) {
		state.TimestampErrors++
		f.invalid[side]++
		return
	}
	if f.seen[side][index]&mask != 0 {
		f.duplicate[side]++
		return
	}
	f.seen[side][index] |= mask
	f.received[side]++
	state.ContentVerifiedPackets++
	if ordinal < f.largest[side] {
		f.reordered[side]++
	}
	if ordinal > f.largest[side] {
		f.largest[side] = ordinal
	}
}

// 汇总必须在readers.Wait之后，所有JSON字段为真实非负整数；不存在未采样补零的外部指标。
func (b *bench) processedSummary() (map[string]any, bool) {
	if b.mediaProcessing != "g711" {
		return nil, true
	}
	var total processedReceiveState
	var generatorLate uint64
	for _, f := range b.flows {
		for side := 0; side < 2; side++ {
			s := f.processed[side]
			total.ObservedPackets += s.ObservedPackets
			total.ContentVerifiedPackets += s.ContentVerifiedPackets
			total.ContentMismatchPackets += s.ContentMismatchPackets
			total.CrossFlowPackets += s.CrossFlowPackets
			total.CodecMismatchPackets += s.CodecMismatchPackets
			total.TimestampErrors += s.TimestampErrors
			total.SequenceErrors += s.SequenceErrors
			total.IdentityErrors += s.IdentityErrors
			total.LatePackets += s.LatePackets
			total.BurstPackets += s.BurstPackets
			total.TailPLCPackets += s.TailPLCPackets
			generatorLate += f.generatorLate[side]
		}
	}
	p := map[string]any{"oracle_version": "g711-pcm-v1", "codec_a": b.sideProfile(0).Name, "codec_b": b.sideProfile(1).Name, "payload_a": b.sideProfile(0).Payload, "payload_b": b.sideProfile(1).Payload, "samples_per_frame": 160, "jitter_target_ms": 40, "receive_late_limit_ms": 80, "burst_gap_limit_ms": 5, "observed_packets": total.ObservedPackets, "content_verified_packets": total.ContentVerifiedPackets, "content_mismatch_packets": total.ContentMismatchPackets, "cross_flow_packets": total.CrossFlowPackets, "codec_mismatch_packets": total.CodecMismatchPackets, "timestamp_errors": total.TimestampErrors, "sequence_errors": total.SequenceErrors, "identity_errors": total.IdentityErrors, "late_packets": total.LatePackets, "burst_packets": total.BurstPackets, "tail_plc_packets": total.TailPLCPackets, "generator_late_packets": generatorLate}
	ok := total.ContentMismatchPackets == 0 && total.CrossFlowPackets == 0 && total.CodecMismatchPackets == 0 && total.TimestampErrors == 0 && total.SequenceErrors == 0 && total.IdentityErrors == 0 && total.LatePackets == 0 && total.BurstPackets == 0 && generatorLate == 0
	return p, ok
}
