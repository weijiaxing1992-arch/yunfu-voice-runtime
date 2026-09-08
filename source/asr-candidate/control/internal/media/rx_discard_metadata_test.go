package media

import (
	"encoding/binary"
	"testing"
)

// 独立wire反例覆盖Rust Push的资源拒收与观察终态继承，不冒充实际RTP端到端测试。
func TestRXDiscardMetadataFromActualProducerShapes(t *testing.T) {
	for _, tc := range []struct {
		name            string
		kind            RXKind
		reason          uint16
		count, duration uint64
		accept          bool
	}{
		{"audio_overflow", RXLocalExpired, 10, 1, 160, true},
		{"audio_late", RXLocalExpired, 11, 1, 160, true},
		{"aux_overflow", RXAuxiliaryExpired, 10, 1, 0, true},
		{"source_reset", RXSourceBoundary, 0, 42, 0, true},
		{"failed_inherits_reset", RXObservationFailed, 8, 42, 0, true},
		{"exhausted_inherits_drop", RXObservationFailed, 9, 1, 160, true},
		{"audio_wrong_duration", RXLocalExpired, 10, 1, 0, false},
		{"audio_wrong_reason", RXLocalExpired, 2, 1, 160, false},
		{"audio_multiple", RXLocalExpired, 10, 2, 160, false},
		{"aux_fake_duration", RXAuxiliaryExpired, 10, 1, 160, false},
		{"aux_late_unreachable", RXAuxiliaryExpired, 11, 1, 0, false},
		{"decoded_cannot_discard", RXDecoded, 0, 1, 160, false},
		{"cn_cannot_discard", RXComfortNoise, 0, 1, 0, false},
		{"wrong_terminal_reason", RXObservationFailed, 7, 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := rxWire(tc.kind, 1, 7, 1)
			binary.LittleEndian.PutUint16(b[124:], tc.reason)
			binary.LittleEndian.PutUint64(b[128:], tc.count)
			binary.LittleEndian.PutUint64(b[112:], tc.duration)
			if tc.name == "failed_inherits_reset" || tc.name == "source_reset" {
				binary.LittleEndian.PutUint16(b[6:], 16)
				b[148] = 2
			}
			f, err := decodeRXFrame(b)
			if (err == nil) != tc.accept {
				t.Fatalf("accept=%v err=%v", tc.accept, err)
			}
			if err == nil && (f.DiscardedPackets != tc.count || f.KnownDurationTicks != tc.duration) {
				t.Fatal("事实被丢弃")
			}
		})
	}
}

func TestRXObservationFailureRetainsBoundaryAndCNFacts(t *testing.T) {
	for _, reason := range []uint16{8, 9} {
		for boundary := uint8(1); boundary <= 6; boundary++ {
			t.Run(string(rune('0'+reason))+"_boundary_"+string(rune('0'+boundary)), func(t *testing.T) {
				b := rxWire(RXObservationFailed, 1, 7, 1)
				binary.LittleEndian.PutUint16(b[6:], 16)
				binary.LittleEndian.PutUint16(b[124:], reason)
				b[148] = boundary
				if boundary == 5 {
					binary.LittleEndian.PutUint64(b[112:], 160)
				}
				if _, err := decodeRXFrame(b); err != nil {
					t.Fatal(err)
				}
				if boundary == 6 {
					binary.LittleEndian.PutUint16(b[6:], 20)
					if _, err := decodeRXFrame(b); err == nil {
						t.Fatal("源结束不能声明已知RTP包序")
					}
				}
			})
		}
	}
	for _, reason := range []uint16{7, 8, 9} {
		t.Run("cn_"+string(rune('0'+reason)), func(t *testing.T) {
			b := rxWire(RXObservationFailed, 1, 7, 1)
			binary.LittleEndian.PutUint16(b[124:], reason)
			b[149] = 1
			f, err := decodeRXFrame(b)
			if (err == nil) != (reason == 8 || reason == 9) {
				t.Fatal(err)
			}
			if err == nil && (!f.CNApplied || f.BodyBytes != 0 || f.SampleCount != 0) {
				t.Fatal("终态不能携带伪造PCM/SID")
			}
		})
	}
}

func TestRXInheritedFailureRejectsImpossibleMetadataMix(t *testing.T) {
	for _, tc := range []struct {
		name            string
		count, duration uint64
		boundary        byte
		cn              bool
	}{
		{"many_audio_without_reset", 2, 160, 0, false}, {"count_with_cn", 1, 0, 0, true},
		{"reset_with_audio_duration", 2, 160, 2, false}, {"initial_source_clears_history", 1, 0, 1, false},
		{"segment_with_discard", 1, 160, 5, false}, {"segment_without_audio_duration", 0, 0, 5, false},
		{"cn_with_audio_duration", 0, 160, 0, true}, {"cn_with_source_boundary", 0, 0, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := rxWire(RXObservationFailed, 1, 7, 1)
			binary.LittleEndian.PutUint16(b[124:], 8)
			binary.LittleEndian.PutUint64(b[128:], tc.count)
			binary.LittleEndian.PutUint64(b[112:], tc.duration)
			b[148] = tc.boundary
			if tc.boundary != 0 {
				binary.LittleEndian.PutUint16(b[6:], 16)
			}
			if tc.cn {
				b[149] = 1
			}
			if _, err := decodeRXFrame(b); err == nil {
				t.Fatal("不可能来自同一原观察的字段组合被接受")
			}
		})
	}
}
