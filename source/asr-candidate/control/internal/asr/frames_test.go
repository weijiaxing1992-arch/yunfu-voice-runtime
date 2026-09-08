package asr

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"math"
	"rustswitch/control/internal/asr/resample"
	"rustswitch/control/internal/asr/wire"
	"rustswitch/control/internal/media"
	"testing"
)

func layerAudio(t *testing.T, e *frameEncoder, f media.RXFrame) wire.Audio {
	t.Helper()
	var buffer [wire.MaxWireSize]byte
	raw, err := e.encode(&f, buffer[:])
	if err != nil {
		t.Fatal(err)
	}
	var parsed [wire.MaxWireSize]byte
	message, err := wire.Read(bytes.NewReader(raw), &parsed)
	if err != nil || message.Kind != wire.KindAudio {
		t.Fatalf("audio: %v %+v", err, message)
	}
	a, err := wire.ParseAudio(message.Body)
	if err != nil {
		t.Fatal(err)
	}
	// 测试复制离线结果；生产方法使用调用方固定空间。
	a.PCM = append([]byte(nil), a.PCM...)
	return a
}

func TestFramesMarkerKnownDurationUsesTicksNotRTPMarker(t *testing.T) {
	for _, flag := range []uint16{0, 128} {
		t.Run(string(rune('A'+flag)), func(t *testing.T) {
			plc := layerMarker(media.RXHistoryPLC, 3, frameDurationNS)
			plc.Flags |= flag
			m, err := markerFromFrame(layerToken, &plc)
			if err != nil || m.KnownDurationTicks == nil || *m.KnownDurationTicks != 160 {
				t.Fatalf("PLC真实时长丢失: %+v %v", m, err)
			}
			aux := layerMarker(media.RXAuxiliaryExpired, 4, frameDurationNS)
			aux.Flags |= flag
			m, err = markerFromFrame(layerToken, &aux)
			if err != nil || m.KnownDurationTicks != nil {
				t.Fatalf("RTP marker虚构了辅助包时长: %+v %v", m, err)
			}
		})
	}
}

func TestFramesMarkersPreserveCNGapAndMetadata(t *testing.T) {
	cn := layerMarker(media.RXComfortNoise, 3, frameDurationNS)
	cn.Flags |= 256 | 512
	cn.ObservationAgeMS = 3
	cn.ArrivalAgeMS = 8
	cn.DeadlineLatenessMS = 2
	cn.GoQueueAgeMS = 1
	m, err := markerFromFrame(layerToken, &cn)
	if err != nil {
		t.Fatal(err)
	}
	if m.SIDBase64 == nil || *m.SIDBase64 != base64.StdEncoding.EncodeToString(cn.SID[:2]) || !m.CNApplied || m.KnownDurationTicks != nil || m.LowerNS != wire.U64(cn.ObservationLowerBoundNS) || m.ExpiresNS != wire.U64(cn.ExpiresAtNS) || m.ObservationAgeMS != 3 || m.ArrivalAgeMS != 8 || m.DeadlineLatenessMS != 2 || m.QueueAgeMS != 1 {
		t.Fatalf("CN元数据不保真: %+v", m)
	}
	gap := media.RXFrame{Kind: media.RXExportGap, Sequence: 8, GapFirst: 4, GapLast: 8, GapDecoded: 2, GapPLC: 1, Reason: 3, SampleRate: 8000, RTPClockRate: 8000, ClockDomain: 2}
	m, err = markerFromFrame(layerToken, &gap)
	if err != nil || m.Gap == nil || m.Gap.First != 4 || m.Gap.Last != 8 || m.Gap.Decoded != 2 || m.Gap.PLC != 1 || m.Gap.Reasons != 3 || m.KnownDurationTicks != nil || m.LowerNS != 0 || m.ExpiresNS != 0 {
		t.Fatalf("gap被补造时长/期限: %+v %v", m, err)
	}
}

func TestFramesEightKHzPassThroughPreservesSamplesAndDeadline(t *testing.T) {
	e := newFrameEncoder(8000, layerToken)
	if e.converter != nil {
		t.Fatal("同率路径不应创建转换器")
	}
	f := layerDecoded(2, 1, 1, 0)
	f.Flags |= 16
	f.Boundary = 5
	a := layerAudio(t, e, f)
	if a.SampleRate != 8000 || a.SampleCount != 160 || a.FilterDelayNS != 0 || a.LowerNS != f.ObservationLowerBoundNS || a.ExpiresNS != f.ExpiresAtNS || a.Flags != f.Flags || a.EventSeq != f.Sequence || a.SourceGeneration != f.SourceGeneration || a.SourceSegment != f.SourceSegment || a.RTPSequence != f.RTPSequence || a.RTPTimestamp != f.RTPTimestamp || a.MediaNS != f.MediaTimeNS || a.SSRC != f.SSRC {
		t.Fatalf("8k来源字段变化: %+v", a)
	}
	for i, sample := range f.PCM {
		if int16(binary.LittleEndian.Uint16(a.PCM[i*2:])) != sample {
			t.Fatalf("同率样本 %d 改变", i)
		}
	}
}

func TestFramesSixteenKHzPreservesDerivationAndContinuousHistory(t *testing.T) {
	e := newFrameEncoder(16000, layerToken)
	oracle := resample.New()
	for frame := uint64(0); frame < 3; frame++ {
		f := layerDecoded(frame+2, 1, 1, frame*frameDurationNS)
		var want [320]int16
		oracle.Process(&f.PCM, &want)
		a := layerAudio(t, e, f)
		if a.SampleCount != 320 || a.FilterDelayNS != resample.DelayNS || a.RTPClockRate != 8000 || a.LowerNS != f.ObservationLowerBoundNS || a.ExpiresNS != f.ExpiresAtNS || a.MediaNS != f.MediaTimeNS {
			t.Fatalf("16k更改了原始期限/位置: %+v", a)
		}
		for i, sample := range want {
			if int16(binary.LittleEndian.Uint16(a.PCM[i*2:])) != sample {
				t.Fatalf("连续重采样 %d/%d 失配", frame, i)
			}
		}
	}
}

func TestFramesDiscontinuitiesResetFIRBeforeNextDecoded(t *testing.T) {
	kinds := []media.RXKind{media.RXHistoryPLC, media.RXMissing, media.RXComfortNoise, media.RXLocalExpired, media.RXInactiveSuspended, media.RXSourceBoundary, media.RXExportGap}
	for _, kind := range kinds {
		t.Run(string(rune('A'+kind)), func(t *testing.T) {
			e := newFrameEncoder(16000, layerToken)
			first := layerDecoded(2, 1, 1, 0)
			for i := range first.PCM {
				first.PCM[i] = 12000
			}
			layerAudio(t, e, first)
			marker := layerMarker(kind, 3, frameDurationNS)
			if kind == media.RXSourceBoundary {
				marker = layerBoundary(3, 2, 1, 2)
			}
			if kind == media.RXExportGap {
				marker = media.RXFrame{Kind: kind, Sequence: 3, GapFirst: 3, GapLast: 3, Reason: 1, SampleRate: 8000, RTPClockRate: 8000, ClockDomain: 2}
			}
			var buffer [wire.MaxWireSize]byte
			raw, err := e.encode(&marker, buffer[:])
			if err != nil {
				t.Fatal(err)
			}
			if raw[4] != byte(wire.KindMarker) {
				t.Fatal("非Decoded上传了音频")
			}
			next := layerDecoded(4, 1, 1, 2*frameDurationNS)
			next.PCM = [160]int16{}
			a := layerAudio(t, e, next)
			for i, sample := range a.PCM {
				if sample != 0 {
					t.Fatalf("边界后有旧FIR字节 %d=%d", i, sample)
				}
			}
		})
	}
	for _, condition := range []string{"new_segment", "forward_media", "forward_rtp"} {
		t.Run(condition, func(t *testing.T) {
			e := newFrameEncoder(16000, layerToken)
			first := layerDecoded(2, 1, 1, 0)
			for i := range first.PCM {
				first.PCM[i] = 12000
			}
			layerAudio(t, e, first)
			next := layerDecoded(3, 1, 1, frameDurationNS)
			next.PCM = [160]int16{}
			switch condition {
			case "new_segment":
				next.SourceSegment = 2
				next.Boundary = 5
				next.Flags |= 16
			case "forward_media":
				next.MediaTimeNS += frameDurationNS
			case "forward_rtp":
				next.RTPTimestamp += 160
			}
			a := layerAudio(t, e, next)
			for _, b := range a.PCM {
				if b != 0 {
					t.Fatal("首个新段/前跳音频借用了旧FIR")
				}
			}
		})
	}
}

func TestFramesAuxiliaryDoesNotClearContinuousFIR(t *testing.T) {
	for _, kind := range []media.RXKind{media.RXAuxiliary, media.RXAuxiliaryExpired} {
		t.Run(string(rune('A'+kind)), func(t *testing.T) {
			e := newFrameEncoder(16000, layerToken)
			first := layerDecoded(2, 1, 1, 0)
			for i := range first.PCM {
				first.PCM[i] = 12000
			}
			layerAudio(t, e, first)
			aux := layerMarker(kind, 3, 0)
			var buffer [wire.MaxWireSize]byte
			if _, err := e.encode(&aux, buffer[:]); err != nil {
				t.Fatal(err)
			}
			next := layerDecoded(4, 1, 1, frameDurationNS)
			next.PCM = [160]int16{}
			a := layerAudio(t, e, next)
			if bytes.Equal(a.PCM, make([]byte, len(a.PCM))) {
				t.Fatal("辅助事件误清除了连续话音历史")
			}
		})
	}
}

func TestFramesInvalidMetadataAndBodiesFail(t *testing.T) {
	e := newFrameEncoder(16000, layerToken)
	base := layerDecoded(2, 1, 1, 0)
	for _, change := range []func(*media.RXFrame){func(f *media.RXFrame) { f.SampleCount = 159 }, func(f *media.RXFrame) { f.BodyBytes = 318 }, func(f *media.RXFrame) { f.KnownDurationTicks = 0 }, func(f *media.RXFrame) { f.RTPClockRate = 16000 }, func(f *media.RXFrame) { f.Boundary = 5 }, func(f *media.RXFrame) { f.Boundary = 2; f.Flags |= 16 }, func(f *media.RXFrame) { f.ExpiresAtNS++ }, func(f *media.RXFrame) { f.MediaTimeNS = math.MaxUint64 }, func(f *media.RXFrame) { f.Flags &^= 1 }} {
		f := base
		change(&f)
		var buffer [wire.MaxWireSize]byte
		if _, err := e.encode(&f, buffer[:]); err == nil {
			t.Fatal("非法音频元数据被接受")
		}
	}
	for _, count := range []uint16{0, 481} {
		cn := layerMarker(media.RXComfortNoise, 3, 0)
		cn.BodyBytes = count
		if _, err := markerFromFrame(layerToken, &cn); err == nil {
			t.Fatal("CN SID长度未拒绝")
		}
	}
	bad := layerMarker(media.RXAuxiliary, 3, 0)
	bad.GoQueueAgeMS = math.MaxUint32 + 1
	if _, err := markerFromFrame(layerToken, &bad); err == nil {
		t.Fatal("队列年龄被截断")
	}
	var short [320]byte
	if _, err := e.encode(&base, short[:]); err == nil {
		t.Fatal("音频编码自动扩展固定缓冲")
	}
}

func TestFramesAudioPathAllocatesZero(t *testing.T) {
	for _, rate := range []uint32{8000, 16000} {
		t.Run(string(rune('A'+rate/8000)), func(t *testing.T) {
			e := newFrameEncoder(rate, layerToken)
			f := layerDecoded(2, 1, 1, 0)
			var buffer [wire.MaxWireSize]byte
			if count := testing.AllocsPerRun(1000, func() {
				if _, err := e.encode(&f, buffer[:]); err != nil {
					panic(err)
				}
			}); count != 0 {
				t.Fatalf("音频热路径发生堆分配: %g", count)
			}
		})
	}
}
