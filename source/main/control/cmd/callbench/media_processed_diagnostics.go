// 本文件保留有界首错和写入时间区间，辅助归因而不改变既有PCM/时间轴/负载验收结果。
package main

import (
	"encoding/base64"
	"encoding/binary"
	"net"
	"net/netip"
	"sort"
	"time"
)

const processedDiagnosticLimit = 32

// 每个已协商接收方向只保留第一错误及上一包头，不生成无界日志、不每包分配。
// ContentValid包括PCM正确但时间轴失败的观察包，绝不加入received或抵消missing。
type processedReceiveDiagnostic struct {
	ContentValid, ContentValidTimestampErrors, ContentValidLate uint64
	first                                                       processedReceiveError
	previousTimestamp, previousOrdinal                          uint32
	previousAtNS                                                uint64
	previousValid                                               bool
}

type processedReceiveError struct {
	Reason               string `json:"reason"`
	CallIndex            int    `json:"call_index"`
	ReceiveLeg           string `json:"receive_leg"`
	SourceAddress        string `json:"source_address"`
	ObservedAtNS         uint64 `json:"observed_at_ns"`
	TimingKnown          bool   `json:"timing_known"`
	ReceiveDelayNS       uint64 `json:"receive_delay_ns"`
	WordValid            bool   `json:"word_valid"`
	ContentValid         bool   `json:"content_valid"`
	FrameOrdinal         uint32 `json:"frame_ordinal"`
	DecodedCallIndex     int    `json:"decoded_call_index"`
	DecodedSourceSide    int    `json:"decoded_source_side"`
	RTPSequence          uint16 `json:"rtp_sequence"`
	RTPTimestamp         uint32 `json:"rtp_timestamp"`
	TimestampBase        uint32 `json:"timestamp_base"`
	RTPSSRC              uint32 `json:"rtp_ssrc"`
	PreviousValid        bool   `json:"previous_valid"`
	PreviousTimestamp    uint32 `json:"previous_timestamp"`
	PreviousFrameOrdinal uint32 `json:"previous_frame_ordinal"`
	PreviousObservedAtNS uint64 `json:"previous_observed_at_ns"`
	PacketBytes          int    `json:"packet_bytes"`
	PacketBase64         string `json:"packet_base64"`
	raw                  [172]byte
	at                   time.Time
}

// 成功写入仅说明UDP API接受；批量调用内各包精确内核提交时刻未知，只能给前后边界。
type processedWriteDiagnostic struct {
	AttemptLate, ReturnLate, MaxCallNS, MaxReturnDelayNS uint64
	first                                                processedWriteError
}

type processedWriteError struct {
	CallIndex       int    `json:"call_index"`
	SendLeg         string `json:"send_leg"`
	FrameOrdinal    uint32 `json:"frame_ordinal"`
	PlannedAtNS     uint64 `json:"planned_at_ns"`
	WriteBeginAtNS  uint64 `json:"write_begin_at_ns"`
	WriteReturnAtNS uint64 `json:"write_return_at_ns"`
	SubmittedBytes  int    `json:"submitted_bytes"`
	at              time.Time
}

func processedLeg(side int) string {
	if side == 0 {
		return "a"
	}
	return "b"
}

func nonnegativeProcessedNS(d time.Duration) uint64 {
	if d < 0 {
		return 0
	}
	return uint64(d)
}

// recordProcessedRTP按协商来源定位；全160样本只算一次，即使时间戳先失败也留下独立内容结果。
func (b *bench) recordProcessedRTP(conn *net.UDPConn, side int, source netip.AddrPort, packet []byte, now time.Time) {
	f := b.processedSources[processedEndpointKey{conn, side, source}]
	if f == nil {
		b.unknownRTP.Add(1)
		return
	}
	state := &f.processed[side]
	d := &state.diagnostic
	var word uint64
	var wordOK, contentValid, timingKnown bool
	var receiveDelayNS, atNS uint64
	var ts uint32
	var seq uint16
	if len(packet) == 172 && packet[0] == 0x80 && packet[1]&127 == b.sideProfile(side).Payload {
		word, wordOK = decodeProcessedWord(packet[12:], b.sideProfile(side).Payload)
		if wordOK {
			// 身份和内容同时成立才计入独立有效PCM观察，不接受串音或范围外标签。
			ordinal := uint32(word >> 15 & 0x3ffff)
			if int(word>>34&0x3fff) == f.index && int(word>>33&1) == side^1 && ordinal > 0 && ordinal <= uint32(b.duration/mediaPacketInterval) {
				contentValid = verifyProcessedSamples(packet[12:], word, b.sideProfile(side^1).Payload, b.sideProfile(side).Payload)
			}
		}
	}
	if len(packet) >= 12 {
		ts = binary.BigEndian.Uint32(packet[4:8])
		seq = binary.BigEndian.Uint16(packet[2:4])
	}
	if started := b.mediaStarted.Load(); started != nil {
		atNS = nonnegativeProcessedNS(now.Sub(*started))
		if contentValid {
			planned := started.Add(f.phaseOffset + time.Duration(uint32(word>>15&0x3ffff)-1)*mediaPacketInterval)
			timingKnown = true
			receiveDelayNS = nonnegativeProcessedNS(now.Sub(planned))
			if now.Sub(planned) > processedReceiveLimit {
				d.ContentValidLate++
			}
		}
	}
	if contentValid {
		d.ContentValid++
	}
	beforeInvalid, beforeDuplicate := f.invalid[side], f.duplicate[side]
	beforeTimestamp, beforeSequence := state.TimestampErrors, state.SequenceErrors
	beforeIdentity, beforeCodec := state.IdentityErrors, state.CodecMismatchPackets
	beforeContent, beforeCross := state.ContentMismatchPackets, state.CrossFlowPackets
	beforeLate, beforeBurst := state.LatePackets, state.BurstPackets
	b.recordProcessedPacket(f, side, packet, now, word, wordOK, contentValid)
	if contentValid && state.TimestampErrors > beforeTimestamp {
		d.ContentValidTimestampErrors++
	}
	if d.first.Reason == "" && (f.invalid[side] > beforeInvalid || f.duplicate[side] > beforeDuplicate || state.LatePackets > beforeLate || state.BurstPackets > beforeBurst) {
		reason := "validation_failure"
		switch {
		case state.TimestampErrors > beforeTimestamp:
			reason = "timestamp"
		case state.IdentityErrors > beforeIdentity:
			reason = "identity"
		case state.CodecMismatchPackets > beforeCodec:
			reason = "codec"
		case state.CrossFlowPackets > beforeCross:
			reason = "cross_flow"
		case state.ContentMismatchPackets > beforeContent:
			reason = "content"
		case state.SequenceErrors > beforeSequence:
			reason = "sequence"
		case f.duplicate[side] > beforeDuplicate:
			reason = "duplicate"
		case state.LatePackets > beforeLate:
			reason = "late"
		case state.BurstPackets > beforeBurst:
			reason = "burst"
		}
		// 固定原报文数组先存；地址字符串与base64只在所有读者停止后的汇总阶段产生。
		d.first = processedReceiveError{Reason: reason, CallIndex: f.index, ReceiveLeg: processedLeg(side), ObservedAtNS: atNS,
			TimingKnown: timingKnown, ReceiveDelayNS: receiveDelayNS, WordValid: wordOK, ContentValid: contentValid,
			FrameOrdinal: uint32(word >> 15 & 0x3ffff), DecodedCallIndex: int(word >> 34 & 0x3fff), DecodedSourceSide: int(word >> 33 & 1),
			RTPSequence: seq, RTPTimestamp: ts, TimestampBase: state.timestampBase,
			PreviousValid: d.previousValid, PreviousTimestamp: d.previousTimestamp, PreviousFrameOrdinal: d.previousOrdinal, PreviousObservedAtNS: d.previousAtNS,
			PacketBytes: len(packet), at: now}
		if len(packet) >= 12 {
			d.first.RTPSSRC = binary.BigEndian.Uint32(packet[8:12])
		}
		copy(d.first.raw[:], packet)
	}
	d.previousTimestamp, d.previousOrdinal, d.previousAtNS, d.previousValid = ts, uint32(word>>15&0x3ffff), atNS, wordOK
}

// 单独记录成功前缀的写入前后区间；失败/短写由原有write_errors保留，不冒充提交。
func recordProcessedWrite(p *mediaDatagram, planned, started, before, after time.Time) {
	d := &p.flow.processedWrites[p.side]
	if before.Sub(planned) >= mediaPacketInterval {
		d.AttemptLate++
	}
	if after.Sub(planned) >= mediaPacketInterval {
		d.ReturnLate++
	}
	d.MaxCallNS = max(d.MaxCallNS, nonnegativeProcessedNS(after.Sub(before)))
	d.MaxReturnDelayNS = max(d.MaxReturnDelayNS, nonnegativeProcessedNS(after.Sub(planned)))
	if d.first.SubmittedBytes == 0 && after.Sub(planned) >= mediaPacketInterval {
		d.first = processedWriteError{CallIndex: p.flow.index, SendLeg: processedLeg(p.side),
			FrameOrdinal: binary.BigEndian.Uint32(p.data[4:8]) / 160,
			PlannedAtNS:  nonnegativeProcessedNS(planned.Sub(started)), WriteBeginAtNS: nonnegativeProcessedNS(before.Sub(started)),
			WriteReturnAtNS: nonnegativeProcessedNS(after.Sub(started)), SubmittedBytes: len(p.data), at: after}
	}
}

// 仅在发送者和接收者全部停止后汇总；两种样本各最多32，按真实观察时间排序并明确省略数。
func (b *bench) processedDiagnosticsSummary() map[string]any {
	var content, timestampContent, lateContent, attemptLate, returnLate, maxCall, maxReturn uint64
	errors := make([]processedReceiveError, 0, processedDiagnosticLimit)
	writes := make([]processedWriteError, 0, processedDiagnosticLimit)
	var receiverFlows, senderFlows int
	for _, f := range b.flows {
		for side := 0; side < 2; side++ {
			d := &f.processed[side].diagnostic
			content += d.ContentValid
			timestampContent += d.ContentValidTimestampErrors
			lateContent += d.ContentValidLate
			if d.first.Reason != "" {
				receiverFlows++
				sample := d.first
				// 固定32项插入保留最早观察，不能按flow顺序隐藏早先错误；非热路径允许格式化。
				index := sort.Search(len(errors), func(i int) bool { return errors[i].at.After(sample.at) })
				if index < processedDiagnosticLimit {
					if len(errors) < processedDiagnosticLimit {
						errors = append(errors, processedReceiveError{})
					}
					copy(errors[index+1:], errors[index:len(errors)-1])
					sample.SourceAddress = f.mediaDestinations[side].String()
					sample.PacketBase64 = base64.StdEncoding.EncodeToString(sample.raw[:min(sample.PacketBytes, len(sample.raw))])
					errors[index] = sample
				}
			}
			w := &f.processedWrites[side]
			attemptLate += w.AttemptLate
			returnLate += w.ReturnLate
			maxCall = max(maxCall, w.MaxCallNS)
			maxReturn = max(maxReturn, w.MaxReturnDelayNS)
			if w.first.SubmittedBytes != 0 {
				senderFlows++
				index := sort.Search(len(writes), func(i int) bool { return writes[i].at.After(w.first.at) })
				if index < processedDiagnosticLimit {
					if len(writes) < processedDiagnosticLimit {
						writes = append(writes, processedWriteError{})
					}
					copy(writes[index+1:], writes[index:len(writes)-1])
					writes[index] = w.first
				}
			}
		}
	}
	return map[string]any{"version": "g711-observation-v1", "sample_limit": processedDiagnosticLimit,
		"observed_content_valid_packets": content, "timestamp_error_content_valid_packets": timestampContent, "observed_content_late_packets": lateContent,
		"write_attempt_late_packets": attemptLate, "write_return_late_packets": returnLate, "max_write_call_ns": maxCall, "max_write_return_delay_ns": maxReturn,
		"receiver_error_flows": receiverFlows, "sender_late_flows": senderFlows,
		"truncated_receiver_flows": receiverFlows - len(errors), "truncated_sender_flows": senderFlows - len(writes),
		"first_receive_errors": errors, "first_late_writes": writes}
}
