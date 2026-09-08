package asr

import (
	"encoding/base64"
	"encoding/binary"
	"math"

	"rustswitch/control/internal/asr/resample"
	"rustswitch/control/internal/asr/wire"
	"rustswitch/control/internal/media"
)

// frameEncoder只由送音协程持有；同率不创建滤波器，PLC/CN只输出元数据。
type frameEncoder struct {
	rate       uint32
	token      string
	tokenBytes [16]byte
	converter  *resample.Converter
	output     [320]int16
	pcm        [640]byte
	// 只决定 FIR 历史是否连续；来源和时间合法性由写前 provenance.note 预检。
	hasPCM                                bool
	generation, segment, mediaEnd, rtpEnd uint64
}

func newFrameEncoder(rate uint32, token string) *frameEncoder {
	e := &frameEncoder{rate: rate, token: token}
	e.tokenBytes, _ = wire.TokenBytes(token)
	if rate == 16000 {
		e.converter = resample.New()
	}
	return e
}

// encode不修改原始观察期限。调用者必须在转换后、每次实际写之前再次检查期限。
func (e *frameEncoder) encode(f *media.RXFrame, dst []byte) ([]byte, error) {
	if f == nil || e.rate != 8000 && e.rate != 16000 {
		return nil, ErrInvalid
	}
	if f.Kind != media.RXDecoded {
		m, err := markerFromFrame(e.token, f)
		if err != nil {
			return nil, err
		}
		// 过期 telephone-event 仍只是辅助包；只有实际 CN 生效或暂停才切断话音历史。
		pureAux := f.Kind == media.RXAuxiliary || f.Kind == media.RXAuxiliaryExpired && !f.CNApplied
		if !pureAux || f.Flags&64 != 0 {
			if e.converter != nil {
				e.converter.Reset()
			}
			e.hasPCM = false
		}
		message, err := wire.JSONMessage(wire.KindMarker, m)
		if err != nil {
			return nil, err
		}
		return wire.Encode(dst, message)
	}
	if f.SampleRate != 8000 || f.RTPClockRate != 8000 || f.SampleCount != 160 || f.BodyBytes != 320 || f.KnownDurationTicks != 160 || (f.Boundary != 0) != (f.Flags&16 != 0) || f.Boundary != 0 && f.Boundary != 5 || f.MediaTimeNS > math.MaxUint64-frameDurationNS || f.RTPTimestamp > math.MaxUint64-160 {
		return nil, ErrProtocol
	}
	if cap(dst) < wire.MaxWireSize {
		return nil, ErrInvalid
	}
	n := 160
	samples := f.PCM[:]
	var delay uint32
	if e.converter != nil {
		// 首帧 NewSegment 与 Decoded 共用一次观察；必须先清旧历史，再转换该首帧。
		if f.Boundary == 5 || e.hasPCM && (f.SourceGeneration != e.generation || f.SourceSegment != e.segment || f.MediaTimeNS != e.mediaEnd || f.RTPTimestamp != e.rtpEnd) {
			e.converter.Reset()
		}
		e.converter.Process(&f.PCM, &e.output)
		n, samples, delay = 320, e.output[:], resample.DelayNS
	}
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(e.pcm[i*2:], uint16(samples[i]))
	}
	a := wire.Audio{StreamToken: e.tokenBytes, EventSeq: f.Sequence, SourceGeneration: f.SourceGeneration, SourceSegment: f.SourceSegment, RTPSequence: f.RTPSequence, RTPTimestamp: f.RTPTimestamp, MediaNS: f.MediaTimeNS, LowerNS: f.ObservationLowerBoundNS, ExpiresNS: f.ExpiresAtNS, SSRC: f.SSRC, SampleRate: e.rate, RTPClockRate: f.RTPClockRate, FilterDelayNS: delay, Flags: f.Flags, ClockDomain: f.ClockDomain, SampleCount: uint16(n), Origin: 1, PCM: e.pcm[:n*2]}
	// 正文先放在前缀之后，wire支持同一固定缓冲中的重叠编码。
	body, err := wire.EncodeAudio(dst[8:cap(dst)], a)
	if err != nil {
		return nil, err
	}
	e.hasPCM, e.generation, e.segment, e.mediaEnd, e.rtpEnd = true, f.SourceGeneration, f.SourceSegment, f.MediaTimeNS+frameDurationNS, f.RTPTimestamp+160
	return wire.Encode(dst, wire.Message{Kind: wire.KindAudio, Body: body})
}

func markerFromFrame(token string, f *media.RXFrame) (wire.Marker, error) {
	if f.GoQueueAgeMS > math.MaxUint32 {
		return wire.Marker{}, ErrProtocol
	}
	m := wire.Marker{StreamToken: token, RXKind: uint8(f.Kind), EventSeq: wire.U64(f.Sequence), SourceGeneration: wire.U64(f.SourceGeneration), SourceSegment: wire.U64(f.SourceSegment), ClockDomain: f.ClockDomain, LowerNS: wire.U64(f.ObservationLowerBoundNS), ExpiresNS: wire.U64(f.ExpiresAtNS), Flags: f.Flags, SSRC: f.SSRC, RTPSequence: wire.U64(f.RTPSequence), RTPTimestamp: wire.U64(f.RTPTimestamp), MediaNS: wire.U64(f.MediaTimeNS), SampleRate: f.SampleRate, RTPClockRate: f.RTPClockRate, ObservationAgeMS: f.ObservationAgeMS, ArrivalAgeMS: f.ArrivalAgeMS, DeadlineLatenessMS: f.DeadlineLatenessMS, QueueAgeMS: uint32(f.GoQueueAgeMS), CNApplied: f.CNApplied, Reason: f.Reason, Boundary: f.Boundary, DiscardedPackets: wire.U64(f.DiscardedPackets)}
	// RXS2 没有时长有效位；128 是 RTP marker，真实 duration_ticks 非零才代表已知区间。
	if f.KnownDurationTicks != 0 {
		n := wire.U64(f.KnownDurationTicks)
		m.KnownDurationTicks = &n
	}
	if f.Kind == media.RXExportGap {
		m.Gap = &wire.Gap{First: wire.U64(f.GapFirst), Last: wire.U64(f.GapLast), Decoded: wire.U64(f.GapDecoded), PLC: wire.U64(f.GapPLC), Reasons: f.Reason}
	}
	if f.Kind == media.RXComfortNoise {
		if f.BodyBytes == 0 || int(f.BodyBytes) > len(f.SID) {
			return m, ErrProtocol
		}
		sid := base64.StdEncoding.EncodeToString(f.SID[:f.BodyBytes])
		m.SIDBase64 = &sid
	}
	return m, m.Validate()
}
