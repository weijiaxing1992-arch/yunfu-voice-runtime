package asr

import (
	"errors"
	"math"
	"rustswitch/control/internal/asr/wire"
	"rustswitch/control/internal/media"
	"testing"
)

const layerToken = "123456789abcdef0123456789abcdef0"

// 本层使用按 Rust worker_rx 实际字段语义制作的离线记录，不冒充真实网络采集。
func layerDecoded(seq, generation, segment, at uint64) media.RXFrame {
	f := media.RXFrame{Kind: media.RXDecoded, Sequence: seq, SourceGeneration: generation, SourceSegment: segment, Flags: 15, SSRC: 1234, RTPSequence: 65536 + seq, RTPTimestamp: 4294967296 + at/125000, MediaTimeNS: at, SampleRate: 8000, RTPClockRate: 8000, BodyBytes: 320, SampleCount: 160, KnownDurationTicks: 160, ClockDomain: 2, ObservationLowerBoundNS: 1000000, ExpiresAtNS: 101000000}
	for i := range f.PCM {
		f.PCM[i] = int16(i*137 - 10000)
	}
	return f
}
func layerBoundary(seq, generation, segment uint64, boundary uint8) media.RXFrame {
	f := layerDecoded(seq, generation, segment, 0)
	f.Kind = media.RXSourceBoundary
	f.BodyBytes = 0
	f.SampleCount = 0
	f.KnownDurationTicks = 0
	f.Flags = 7 | 16
	f.Boundary = boundary
	if boundary == 6 {
		f.Flags = f.Flags&^4 | 64
		f.RTPSequence = 0
	}
	return f
}
func layerMarker(kind media.RXKind, seq, at uint64) media.RXFrame {
	f := layerDecoded(seq, 1, 1, at)
	f.Kind = kind
	if kind == media.RXHistoryPLC {
		f.Flags &^= 4
		f.RTPSequence = 0
		f.Reason = 1
		return f
	}
	f.BodyBytes = 0
	f.SampleCount = 0
	f.KnownDurationTicks = 0
	if kind == media.RXMissing || kind == media.RXLocalExpired {
		f.KnownDurationTicks = 160
		f.Reason = 1
	}
	if kind == media.RXComfortNoise {
		f.BodyBytes = 2
		f.SID[0] = 20
		f.SID[1] = 5
		f.CNApplied = true
	}
	if kind == media.RXAuxiliaryExpired {
		f.Reason = 3
	}
	return f
}
func layerNote(t *testing.T, p *provenance, f media.RXFrame) bool {
	t.Helper()
	changed, err := p.note(&f)
	if err != nil {
		t.Fatalf("kind=%d seq=%d note: %v", f.Kind, f.Sequence, err)
	}
	return changed
}
func layerEstablished(t *testing.T) provenance {
	var p provenance
	layerNote(t, &p, layerBoundary(1, 1, 1, 1))
	f := layerDecoded(2, 1, 1, 0)
	f.Boundary = 5
	f.Flags |= 16
	layerNote(t, &p, f)
	return p
}
func layerResult(seq, utterance, revision, first, last, start, end uint64) wire.Result {
	return wire.Result{StreamToken: layerToken, ResultSeq: wire.U64(seq), UtteranceID: wire.U64(utterance), Revision: wire.U64(revision), SourceGeneration: 1, SourceSegment: 1, Type: "partial", Text: "独立离线来源检查", FirstEventSeq: wire.U64(first), LastEventSeq: wire.U64(last), StartMediaNS: wire.U64(start), EndMediaNS: wire.U64(end), Coverage: "complete"}
}
func layerRejectNote(t *testing.T, p *provenance, f media.RXFrame, want error) {
	t.Helper()
	before := *p
	if _, err := p.note(&f); !errors.Is(err, want) {
		t.Fatalf("note error=%v want=%v", err, want)
	}
	if *p != before {
		t.Fatal("拒绝记录修改了实际已提交前缀")
	}
}
func layerRejectResult(t *testing.T, p *provenance, r wire.Result) {
	t.Helper()
	before := *p
	if _, _, err := p.result(r); err == nil {
		t.Fatal("非法结果未拒绝")
	}
	if *p != before {
		t.Fatal("拒绝结果修改了来源或最终状态")
	}
}

func TestProvenanceRealInitialAndNewSegmentBoundaries(t *testing.T) {
	for _, initialSegment := range []uint64{0, 1} {
		t.Run(string(rune('0'+initialSegment)), func(t *testing.T) {
			var p provenance
			layerNote(t, &p, layerBoundary(1, 1, initialSegment, 1))
			seq := uint64(2)
			if initialSegment == 0 {
				aux := layerMarker(media.RXAuxiliary, seq, 0)
				aux.SourceSegment = 0
				layerNote(t, &p, aux)
				seq++
			}
			f := layerDecoded(seq, 1, 1, 0)
			f.Flags |= 16
			f.Boundary = 5
			layerNote(t, &p, f)
			if p.segment != 1 || p.count != 1 || !p.segmentPCM {
				t.Fatal("首个音频段没有建立")
			}
			f.Sequence++
			f.RTPSequence++
			f.MediaTimeNS = frameDurationNS
			f.RTPTimestamp += 160
			layerRejectNote(t, &p, f, ErrProtocol) // 同段的第二个PCM不可重放NewSegment。
		})
	}
}

func TestProvenanceSourceEndedAndSuspendedAfterInvalidateOldPartial(t *testing.T) {
	for _, kind := range []media.RXKind{media.RXSourceBoundary, media.RXHistoryPLC, media.RXMissing, media.RXInactiveSuspended} {
		t.Run(string(rune('A'+kind)), func(t *testing.T) {
			p := layerEstablished(t)
			r := layerResult(1, 1, 1, 2, 2, 0, frameDurationNS)
			if _, _, err := p.result(r); err != nil {
				t.Fatal(err)
			}
			f := layerMarker(kind, 3, frameDurationNS)
			if kind == media.RXSourceBoundary {
				f = layerBoundary(3, 1, 1, 6)
			} else {
				f.Flags |= 64
			}
			preview := p
			changed, err := preview.note(&f)
			if err != nil || !changed {
				t.Fatalf("suspension: %v %v", changed, err)
			}
			invalidated, ok := p.invalidate()
			if !ok || invalidated.SourceGeneration != 1 || invalidated.SourceSegment != 1 {
				t.Fatal("未按旧来源撤销partial")
			}
			layerNote(t, &p, f)
			if !p.suspended {
				t.Fatal("暂停位没有生效")
			}
			if _, ok := p.invalidate(); ok {
				t.Fatal("重复发出partial_invalidated")
			}
			late := layerResult(2, 1, 2, 2, 2, 0, frameDurationNS)
			late.Type = "final"
			if e, stale, err := p.result(late); err != nil || !stale || e.Type != "" {
				t.Fatalf("旧final被交付: %+v %v %v", e, stale, err)
			}
			next := layerDecoded(4, 1, 1, 2*frameDurationNS)
			layerRejectNote(t, &p, next, ErrProtocol)
			next.SourceSegment = 2
			next.Flags |= 16
			next.Boundary = 5
			if kind == media.RXSourceBoundary { // BYE之后先要新generation边界，旧源不能自行复活。
				layerRejectNote(t, &p, next, ErrProtocol)
				layerNote(t, &p, layerBoundary(4, 2, 1, 1))
				next = layerDecoded(5, 2, 1, 2*frameDurationNS)
				next.Boundary = 5
				next.Flags |= 16
				layerNote(t, &p, next)
				return
			}
			layerNote(t, &p, next)
			if p.suspended || p.segment != 2 {
				t.Fatal("真实新段未恢复")
			}
		})
	}
}

func TestProvenanceNewSegmentPreservesMediaAndPacketHighWater(t *testing.T) {
	p := layerEstablished(t)
	r := layerResult(1, 1, 1, 2, 2, 0, frameDurationNS)
	if _, _, err := p.result(r); err != nil {
		t.Fatal(err)
	}
	next := layerDecoded(3, 1, 3, frameDurationNS)
	next.Boundary = 5
	next.Flags |= 16
	next.RTPTimestamp -= 80
	preview := p
	if changed, err := preview.note(&next); err != nil || !changed {
		t.Fatalf("新段: %v %v", changed, err)
	}
	old, _ := p.invalidate()
	if old.SourceSegment != 1 {
		t.Fatal("失效通知用了新来源")
	}
	layerNote(t, &p, next)
	if p.segment != 3 || p.count != 1 || p.spans[0].first != 3 {
		t.Fatal("新来源未释放旧区间")
	}
	backwards := layerDecoded(4, 1, 4, 0)
	backwards.Boundary = 5
	backwards.Flags |= 16
	layerRejectNote(t, &p, backwards, ErrProtocol)
	backwards.MediaTimeNS = 2 * frameDurationNS
	backwards.RTPSequence = p.lastRTPSequence - 65536
	layerRejectNote(t, &p, backwards, ErrProtocol)
	// 源代次必须有单独的真实kind7，不能只靠Decoded中的generation变大。
	backwards.RTPSequence = p.lastRTPSequence + 1
	backwards.SourceGeneration = 2
	layerRejectNote(t, &p, backwards, ErrProtocol)
}

func TestProvenanceGenerationResetAllowsRTPRestartButNotMediaRewind(t *testing.T) {
	for _, boundary := range []uint8{1, 2, 3, 4} {
		t.Run(string(rune('0'+boundary)), func(t *testing.T) {
			p := layerEstablished(t)
			layerNote(t, &p, layerBoundary(3, 2, 1, boundary))
			f := layerDecoded(4, 2, 1, 0)
			f.Flags |= 16
			f.Boundary = 5
			f.RTPSequence = 1
			f.RTPTimestamp = 0
			layerRejectNote(t, &p, f, ErrProtocol)
			f.MediaTimeNS = frameDurationNS
			layerNote(t, &p, f)
		})
	}
}

func TestProvenanceMarkersRetainTimeHighWater(t *testing.T) {
	for _, kind := range []media.RXKind{media.RXComfortNoise, media.RXHistoryPLC, media.RXMissing, media.RXLocalExpired, media.RXExportGap} {
		t.Run(string(rune('A'+kind)), func(t *testing.T) {
			p := layerEstablished(t)
			f := layerMarker(kind, 3, frameDurationNS)
			if kind == media.RXExportGap {
				f = media.RXFrame{Kind: kind, Sequence: 3, GapFirst: 3, GapLast: 3, GapDecoded: 1}
			}
			layerNote(t, &p, f)
			for _, which := range []string{"media", "timestamp", "sequence"} {
				bad := layerDecoded(4, 1, 1, 3*frameDurationNS)
				switch which {
				case "media":
					bad.MediaTimeNS = 0
				case "timestamp":
					bad.RTPTimestamp = p.lastRTPEnd - 1
				case "sequence":
					bad.RTPSequence = p.lastRTPSequence - 65536
				}
				layerRejectNote(t, &p, bad, ErrProtocol)
			}
			good := layerDecoded(4, 1, 1, 3*frameDurationNS)
			layerNote(t, &p, good)
			r := layerResult(1, 1, 1, 2, 4, 0, 4*frameDurationNS)
			layerRejectResult(t, &p, r)
			r.Coverage = "discontinuous"
			if _, _, err := p.result(r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProvenanceAuxiliarySequenceIsNotAudioGap(t *testing.T) {
	for _, kind := range []media.RXKind{media.RXAuxiliary, media.RXAuxiliaryExpired} {
		t.Run(string(rune('A'+kind)), func(t *testing.T) {
			p := layerEstablished(t)
			aux := layerMarker(kind, 3, 999*frameDurationNS)
			aux.SourceSegment = 0
			layerNote(t, &p, aux)
			layerNote(t, &p, layerDecoded(4, 1, 1, frameDurationNS))
			if p.count != 2 || p.spans[1].broken {
				t.Fatal("辅助事件被当成音频缺口")
			}
			r := layerResult(1, 1, 1, 2, 4, 0, 2*frameDurationNS)
			if _, _, err := p.result(r); err != nil {
				t.Fatal(err)
			}
			r.ResultSeq = 2
			r.Revision = 2
			r.FirstEventSeq = 3
			layerRejectResult(t, &p, r)
		})
	}
}

func TestProvenanceEarlyExpiredNewSegmentDoesNotPrematurelyBind(t *testing.T) {
	p := layerEstablished(t)
	expired := layerMarker(media.RXLocalExpired, 3, frameDurationNS)
	expired.SourceSegment = 2
	expired.Reason = 2
	layerNote(t, &p, expired)
	if p.segment != 1 {
		t.Fatal("过期首帧提前切换了当前来源")
	}
	next := layerDecoded(4, 1, 2, 2*frameDurationNS)
	next.Boundary = 5
	next.Flags |= 16
	layerNote(t, &p, next)
	if p.segment != 2 {
		t.Fatal("实际消费新段时没有切换")
	}
}

func TestProvenanceKnownIntervalsAndOverflow(t *testing.T) {
	p := layerEstablished(t)
	expired := layerMarker(media.RXLocalExpired, 3, frameDurationNS)
	expired.KnownDurationTicks = 480
	layerNote(t, &p, expired)
	bad := layerDecoded(4, 1, 1, 2*frameDurationNS)
	layerRejectNote(t, &p, bad, ErrProtocol)
	good := layerDecoded(4, 1, 1, 4*frameDurationNS)
	layerNote(t, &p, good)
	huge := layerMarker(media.RXLocalExpired, 5, 0)
	huge.KnownDurationTicks = math.MaxUint64
	layerRejectNote(t, &p, huge, ErrProtocol)
	for _, reason := range []uint16{10, 11} {
		q := layerEstablished(t)
		f := layerMarker(media.RXLocalExpired, 3, 999*frameDurationNS)
		f.Reason = reason
		layerNote(t, &q, f)
		layerNote(t, &q, layerDecoded(4, 1, 1, frameDurationNS))
	}
}

func TestProvenanceInvalidAndExhaustedEventsDoNotCommit(t *testing.T) {
	p := layerEstablished(t)
	cases := []media.RXFrame{layerDecoded(4, 1, 1, frameDurationNS), {Kind: media.RXKind(99), Sequence: 3}, {Kind: media.RXExportGap, GapFirst: 4, GapLast: 4, Sequence: 4}, {Kind: media.RXExportGap, GapFirst: 3, GapLast: 3, Sequence: 3, GapDecoded: 2}, {Kind: media.RXEnd, Sequence: 3}}
	for _, f := range cases {
		layerRejectNote(t, &p, f, ErrProtocol)
	}
	p.lastEvent = math.MaxUint64
	layerRejectNote(t, &p, media.RXFrame{Kind: media.RXExportGap, GapFirst: 0, GapLast: 0}, ErrProtocol)
	p = layerEstablished(t)
	fail := layerMarker(media.RXObservationFailed, 3, 0)
	layerRejectNote(t, &p, fail, ErrInputFailed)
}

func TestProvenanceBoundedRangesAndFinalReleasesPrefix(t *testing.T) {
	var p provenance
	layerNote(t, &p, layerBoundary(1, 1, 1, 1))
	for i := uint64(0); i < 64; i++ {
		layerNote(t, &p, layerDecoded(2+2*i, 1, 1, i*frameDurationNS))
		if i < 63 {
			layerNote(t, &p, layerMarker(media.RXAuxiliary, 3+2*i, i*frameDurationNS))
		}
	}
	if p.count != 64 {
		t.Fatalf("区间数=%d", p.count)
	}
	layerNote(t, &p, layerMarker(media.RXAuxiliary, 129, 64*frameDurationNS))
	f := layerDecoded(130, 1, 1, 64*frameDurationNS)
	layerRejectNote(t, &p, f, ErrProvenanceOverflow)
	r := layerResult(1, 1, 1, 2, 64, 0, 32*frameDurationNS)
	r.Type = "final"
	if _, _, err := p.result(r); err != nil {
		t.Fatal(err)
	}
	if p.count != 32 || p.spans[0].first != 66 {
		t.Fatalf("final没有裁掉完成前缀: %+v", p.spans[0])
	}
	layerNote(t, &p, f)
	// 连续合并的范围中间结束也要精确移动媒体起点。
	q := layerEstablished(t)
	layerNote(t, &q, layerDecoded(3, 1, 1, frameDurationNS))
	layerNote(t, &q, layerDecoded(4, 1, 1, 2*frameDurationNS))
	r = layerResult(1, 1, 1, 2, 3, 0, 2*frameDurationNS)
	r.Type = "final"
	if _, _, err := q.result(r); err != nil {
		t.Fatal(err)
	}
	if q.count != 1 || q.spans[0].first != 4 || q.spans[0].start != 2*frameDurationNS {
		t.Fatal("范围内裁剪错误")
	}
	r = layerResult(2, 2, 1, 4, 4, 2*frameDurationNS, 3*frameDurationNS)
	r.Type = "final"
	if _, _, err := q.result(r); err != nil {
		t.Fatal(err)
	}
	if q.count != 0 || q.spans != [provenanceLimit]audioSpan{} {
		t.Fatal("完成后仍保留历史证明")
	}
}

func TestProvenanceResultsValidateActualRangesRevisionsAndFinality(t *testing.T) {
	p := layerEstablished(t)
	layerNote(t, &p, layerDecoded(3, 1, 1, frameDurationNS))
	good := layerResult(1, 1, 1, 2, 3, 0, 2*frameDurationNS)
	for _, change := range []func(*wire.Result){func(r *wire.Result) { r.ResultSeq = 2 }, func(r *wire.Result) { r.SourceGeneration = 2 }, func(r *wire.Result) { r.FirstEventSeq = 1 }, func(r *wire.Result) { r.LastEventSeq = 4 }, func(r *wire.Result) { r.StartMediaNS = 1 }, func(r *wire.Result) { r.EndMediaNS-- }} {
		bad := good
		change(&bad)
		layerRejectResult(t, &p, bad)
	}
	if _, _, err := p.result(good); err != nil {
		t.Fatal(err)
	}
	duplicate := good
	duplicate.ResultSeq = 2
	layerRejectResult(t, &p, duplicate)
	other := duplicate
	other.UtteranceID = 2
	other.Revision = 1
	layerRejectResult(t, &p, other)
	good.ResultSeq = 2
	good.Revision = 2
	if _, _, err := p.result(good); err != nil {
		t.Fatal(err)
	}
	good.ResultSeq = 3
	good.Revision = 3
	good.Type = "final"
	if _, _, err := p.result(good); err != nil {
		t.Fatal(err)
	}
	good.ResultSeq = 4
	good.Revision = 4
	layerRejectResult(t, &p, good)
	other = good
	other.UtteranceID = 2
	layerRejectResult(t, &p, other)
}

func TestProvenanceNoAudioAndFutureSourceCannotInventResult(t *testing.T) {
	var p provenance
	layerNote(t, &p, layerBoundary(1, 1, 1, 1))
	layerNote(t, &p, layerMarker(media.RXAuxiliary, 2, 0))
	r := layerResult(1, 1, 1, 2, 2, 0, frameDurationNS)
	layerRejectResult(t, &p, r)
	q := layerEstablished(t)
	layerNote(t, &q, layerBoundary(3, 2, 1, 2))
	r.SourceGeneration = 2
	r.FirstEventSeq = 3
	r.LastEventSeq = 3
	layerRejectResult(t, &q, r)
}

func TestProvenanceNoteAndResultsUseNoHeap(t *testing.T) {
	p := layerEstablished(t)
	frame := layerDecoded(3, 1, 1, frameDurationNS)
	if allocs := testing.AllocsPerRun(1000, func() {
		copy := p
		if _, err := copy.note(&frame); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("note allocs=%g", allocs)
	}
	r := layerResult(1, 1, 1, 2, 2, 0, frameDurationNS)
	if allocs := testing.AllocsPerRun(1000, func() {
		copy := p
		if _, _, err := copy.result(r); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("result allocs=%g", allocs)
	}
}

// enable_local_observation 仅建立队列，接入正在通话的来源时没有合成的边界或历史重放。
func TestProvenanceSubscribeDuringExistingAudioNeedsNoSyntheticBoundary(t *testing.T) {
	var p provenance
	first := layerDecoded(1, 7, 4, 999*frameDurationNS)
	changed := layerNote(t, &p, first)
	if changed || p.generation != 7 || p.segment != 4 || p.count != 1 || p.spans[0].first != 1 {
		t.Fatal("任意时点订阅没有从真实首帧建立来源")
	}
	next := layerDecoded(2, 8, 1, 1000*frameDurationNS)
	layerRejectNote(t, &p, next, ErrProtocol)
}

func TestProvenanceInitialMarkersNeverAuthorizeResultsOrClearKnownTime(t *testing.T) {
	for _, kind := range []media.RXKind{media.RXHistoryPLC, media.RXComfortNoise, media.RXAuxiliary, media.RXAuxiliaryExpired, media.RXMissing} {
		t.Run(string(rune('A'+kind)), func(t *testing.T) {
			var p provenance
			first := layerMarker(kind, 1, frameDurationNS)
			first.SourceGeneration = 7
			first.SourceSegment = 4
			layerNote(t, &p, first)
			if p.generation != 0 || p.count != 0 || p.utterance != 0 || p.lastEvent != 1 {
				t.Fatal("初始marker冒充了真实音频来源范围")
			}
			r := layerResult(1, 1, 1, 1, 1, frameDurationNS, 2*frameDurationNS)
			r.SourceGeneration = 7
			r.SourceSegment = 4
			layerRejectResult(t, &p, r)
			next := layerDecoded(2, 7, 4, 2*frameDurationNS)
			if kind == media.RXHistoryPLC || kind == media.RXMissing || kind == media.RXComfortNoise {
				old := next
				old.MediaTimeNS = 0
				layerRejectNote(t, &p, old, ErrProtocol)
			}
			layerNote(t, &p, next)
			if p.count != 1 || p.spans[0].first != 2 || p.generation != 7 || p.segment != 4 {
				t.Fatal("初始marker之后真实音频未建立")
			}
		})
	}
}

func TestProvenanceSourceEndedMayReferenceQueuedNewSegment(t *testing.T) {
	p := layerEstablished(t)
	r := layerResult(1, 1, 1, 2, 2, 0, frameDurationNS)
	if _, _, err := p.result(r); err != nil {
		t.Fatal(err)
	}
	ended := layerBoundary(3, 1, 2, 6)
	preview := p
	if changed, err := preview.note(&ended); err != nil || !changed {
		t.Fatalf("待播段BYE: %v %v", changed, err)
	}
	old, _ := p.invalidate()
	if old.SourceSegment != 1 {
		t.Fatal("结束通知冒用待播段的身份撤销partial")
	}
	layerNote(t, &p, ended)
	if p.segment != 1 || !p.ended || !p.suspended {
		t.Fatal("结束通知错误登记了待播音频段")
	}
	next := layerDecoded(4, 1, 3, frameDurationNS)
	next.Boundary = 5
	next.Flags |= 16
	layerRejectNote(t, &p, next, ErrProtocol)
}

func TestProvenanceFirstObservationCanEndExistingSourceWithoutAudio(t *testing.T) {
	var p provenance
	ended := layerBoundary(1, 7, 4, 6)
	layerNote(t, &p, ended)
	if !p.ended || !p.suspended || p.count != 0 || p.utterance != 0 {
		t.Fatal("首条结束标记伪造了音频来源范围")
	}
	frame := layerDecoded(2, 7, 5, 0)
	frame.Boundary = 5
	frame.Flags |= 16
	layerRejectNote(t, &p, frame, ErrProtocol)
	layerNote(t, &p, layerBoundary(2, 8, 1, 1))
	frame = layerDecoded(3, 8, 1, 0)
	frame.Boundary = 5
	frame.Flags |= 16
	layerNote(t, &p, frame)
	if p.ended || p.suspended || p.count != 1 {
		t.Fatal("真实新代次没有恢复")
	}
}

// 首前缀只有暂停元数据时，首Decoded不能悄悄抹掉暂停；这是应拒绝输入的负例。
func TestProvenanceInitialPauseRequiresActualNewSegment(t *testing.T) {
	for _, kind := range []media.RXKind{media.RXHistoryPLC, media.RXMissing, media.RXInactiveSuspended} {
		t.Run(string(rune('A'+kind)), func(t *testing.T) {
			for _, candidate := range []string{"no_boundary", "same_segment_boundary", "backward_segment_boundary"} {
				t.Run(candidate, func(t *testing.T) {
					var p provenance
					prefix := layerMarker(kind, 1, frameDurationNS)
					prefix.SourceGeneration = 7
					prefix.SourceSegment = 4
					prefix.Flags |= 64
					layerNote(t, &p, prefix)
					if !p.suspended || p.generation != 0 || p.count != 0 {
						t.Fatal("前置条件不成立")
					}
					next := layerDecoded(2, 7, 4, 2*frameDurationNS)
					if candidate != "no_boundary" {
						next.Flags |= 16
						next.Boundary = 5
					}
					if candidate == "backward_segment_boundary" {
						next.SourceSegment = 3
					}
					before := p
					_, err := p.note(&next)
					if err == nil {
						t.Fatalf("BUG: initial suspended prefix kind=%d accepted %s; after suspended=%v generation=%d segment=%d ranges=%d", kind, candidate, p.suspended, p.generation, p.segment, p.count)
					}
					if p != before {
						t.Fatal("拒绝不得推进实际前缀")
					}
				})
			}
		})
	}
}

func TestProvenanceInitialResumeBoundaryMayArriveOnMissing(t *testing.T) {
	for _, boundaryKind := range []media.RXKind{media.RXDecoded, media.RXMissing} {
		t.Run(string(rune('A'+boundaryKind)), func(t *testing.T) {
			var p provenance
			prefix := layerMarker(media.RXHistoryPLC, 1, frameDurationNS)
			prefix.SourceGeneration = 7
			prefix.SourceSegment = 4
			prefix.Flags |= 64
			layerNote(t, &p, prefix)
			resume := layerDecoded(2, 7, 5, 2*frameDurationNS)
			resume.Flags |= 16
			resume.Boundary = 5
			if boundaryKind == media.RXMissing {
				resume.Kind = media.RXMissing
				resume.BodyBytes = 0
				resume.SampleCount = 0
				resume.Reason = 7
			}
			layerNote(t, &p, resume)
			if p.suspended {
				t.Fatal("真实NewSegment未解除暂停")
			}
			next := layerDecoded(3, 7, 5, 3*frameDurationNS)
			layerNote(t, &p, next)
		})
	}
}

func TestProvenanceInitialPauseCannotChangeGenerationWithoutBoundary(t *testing.T) {
	var p provenance
	prefix := layerMarker(media.RXMissing, 1, frameDurationNS)
	prefix.SourceGeneration = 7
	prefix.SourceSegment = 4
	prefix.Flags |= 64
	layerNote(t, &p, prefix)
	next := layerDecoded(2, 8, 1, 2*frameDurationNS)
	next.Flags |= 16
	next.Boundary = 5
	layerRejectNote(t, &p, next, ErrProtocol)
	same := layerBoundary(2, 7, 5, 1)
	layerRejectNote(t, &p, same, ErrProtocol)
	layerNote(t, &p, layerBoundary(2, 8, 1, 1))
	next.Sequence = 3
	layerNote(t, &p, next)
	if p.suspended || p.generation != 8 || p.count != 1 {
		t.Fatal("新代次恢复边界没有生效")
	}
}
