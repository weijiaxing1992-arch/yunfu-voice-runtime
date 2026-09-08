package asr

import (
	"math"
	"rustswitch/control/internal/asr/wire"
	"rustswitch/control/internal/media"
)

// audioSpan 只保存已完整写入的连续 Decoded 范围；辅助序号单独隔开范围，不冒充语音缺失。
type audioSpan struct {
	first, last, start, end uint64
	broken                  bool
}
type provenance struct {
	spans                                                              [provenanceLimit]audioSpan
	count                                                              int
	generation, segment, lastEvent, lastFinal                          uint64
	lastMediaEnd, lastRTPEnd, lastRTPSequence                          uint64
	hasMedia, hasRTPEnd, hasRTPSequence, segmentPCM, suspended, broken bool
	ended                                                              bool
	// 首次Decoded之前也可能已经观察到暂停。这里只保留恢复所需的来源身份，不建立PCM范围。
	initialPauseGeneration, initialPauseSegment          uint64
	resultSequence, utterance, revision, closedUtterance uint64
}

func sourceCompare(ag, as, bg, bs uint64) int {
	if ag < bg || ag == bg && as < bs {
		return -1
	}
	if ag == bg && as == bs {
		return 0
	}
	return 1
}

// note 由短状态锁保护。错误不推进任何前缀；预检使用调用者副本，完整写后才提交真实对象。
// 返回 true 表示需要撤销旧来源 partial；调用者必须在应用新对象前对旧对象 invalidate。
func (p *provenance) note(f *media.RXFrame) (bool, error) {
	if f == nil {
		return false, ErrProtocol
	}
	next := *p
	invalidates, err := next.noteChecked(f)
	if err == nil {
		*p = next
	}
	return invalidates, err
}

func (p *provenance) noteChecked(f *media.RXFrame) (bool, error) {
	if f.Kind < media.RXDecoded || f.Kind > media.RXAuxiliary {
		return false, ErrProtocol
	}
	if f.Kind == media.RXExportGap {
		if p.lastEvent == math.MaxUint64 || f.GapFirst != p.lastEvent+1 || f.GapLast < f.GapFirst || f.Sequence != f.GapLast || uint64(f.GapDecoded)+uint64(f.GapPLC) > f.GapLast-f.GapFirst+1 {
			return false, ErrProtocol
		}
		p.lastEvent, p.broken = f.Sequence, true
		return false, nil
	}
	if f.Kind == media.RXEnd || f.Kind == media.RXExportFailed {
		if f.Sequence != p.lastEvent {
			return false, ErrProtocol
		}
		return false, nil
	}
	if p.lastEvent == math.MaxUint64 || f.Sequence != p.lastEvent+1 || f.SourceGeneration == 0 || (f.Boundary != 0) != (f.Flags&16 != 0) {
		return false, ErrProtocol
	}
	p.lastEvent = f.Sequence
	if f.Kind == media.RXObservationFailed {
		return false, ErrInputFailed
	}
	if f.Kind == media.RXSourceBoundary {
		switch f.Boundary {
		case 1, 2, 3, 4:
			// 源初始可仅有 CN/DTMF，segment=0 是真实未建立音频段，不能要求为正。
			if f.SourceGeneration <= p.generation || p.generation == 0 && p.suspended && f.SourceGeneration <= p.initialPauseGeneration {
				return false, ErrProtocol
			}
			p.changeSource(f.SourceGeneration, f.SourceSegment)
			return true, nil
		case 6:
			// RTCP BYE 报告被结束的旧来源，不会凭空递增 generation/segment。
			// last_received 可已指向尚未消费的新段；结束它不等于将那一段登记为已送音。
			if p.generation == 0 {
				if p.suspended && (f.SourceGeneration != p.initialPauseGeneration || f.SourceSegment < p.initialPauseSegment) {
					return false, ErrProtocol
				}
				p.generation, p.segment = f.SourceGeneration, f.SourceSegment
			}
			if f.SourceGeneration != p.generation || f.SourceSegment < p.segment || f.Flags&4 != 0 {
				return false, ErrProtocol
			}
			p.suspended, p.broken = true, true
			p.ended = true
			return true, nil
		default:
			return false, ErrProtocol
		}
	}
	// 观察订阅可在已通话之后开启；Rust 不重放来源边界，也不重置已播放段。
	// 首个完整Decoded可直接建立初始来源；真实Missing/NewSegment也可先建立恢复边界，
	// 但仍没有PCM范围。已观察暂停时，必须证明同代次更大段号的真实NewSegment，不能被首帧抹掉。
	if p.generation == 0 && p.suspended && f.SourceGeneration != p.initialPauseGeneration {
		return false, ErrProtocol
	}
	if p.generation == 0 && (f.Kind == media.RXDecoded || f.Kind == media.RXMissing && f.Boundary == 5) {
		if f.SourceSegment == 0 {
			return false, ErrProtocol
		}
		if p.suspended && (f.Boundary != 5 || f.SourceSegment <= p.initialPauseSegment) {
			return false, ErrProtocol
		}
		p.changeSource(f.SourceGeneration, f.SourceSegment)
	}
	if p.generation != 0 && f.SourceGeneration != p.generation {
		return false, ErrProtocol
	}
	invalidates := false
	if f.Boundary == 5 {
		// Rust 在真正消费首段音频时附带 NewSegment，而不是额外发送 kind7。
		// 解码失败时同一观察可能转为 Missing，边界事实仍必须保留。
		if p.ended || f.Kind != media.RXDecoded && f.Kind != media.RXMissing || f.SourceSegment == 0 || f.SourceSegment < p.segment || f.SourceSegment == p.segment && (p.segmentPCM || p.suspended) {
			return false, ErrProtocol
		}
		if p.generation != 0 && f.SourceSegment != p.segment {
			p.changeSource(f.SourceGeneration, f.SourceSegment)
			invalidates = true
		}
	} else if f.Boundary != 0 {
		return false, ErrProtocol
	}
	if f.Kind == media.RXDecoded {
		if f.SourceSegment == 0 || f.SourceSegment != p.segment || p.suspended || f.Flags&15 != 15 || f.KnownDurationTicks != 160 || f.SampleRate != 8000 || f.RTPClockRate != 8000 || f.SampleCount != 160 || f.BodyBytes != 320 {
			return false, ErrProtocol
		}
		if p.hasRTPSequence && f.RTPSequence <= p.lastRTPSequence {
			return false, ErrProtocol
		}
		if f.MediaTimeNS > math.MaxUint64-frameDurationNS || f.RTPTimestamp > math.MaxUint64-160 || p.hasMedia && f.MediaTimeNS < p.lastMediaEnd || p.hasRTPEnd && f.RTPTimestamp < p.lastRTPEnd {
			return false, ErrProtocol
		}
		broken := p.broken || p.hasMedia && f.MediaTimeNS != p.lastMediaEnd || p.hasRTPEnd && f.RTPTimestamp != p.lastRTPEnd
		if p.count > 0 && !broken && f.Sequence == p.spans[p.count-1].last+1 && f.MediaTimeNS == p.spans[p.count-1].end {
			p.spans[p.count-1].last, p.spans[p.count-1].end = f.Sequence, f.MediaTimeNS+frameDurationNS
		} else {
			if p.count == provenanceLimit {
				return false, ErrProvenanceOverflow
			}
			p.spans[p.count] = audioSpan{first: f.Sequence, last: f.Sequence, start: f.MediaTimeNS, end: f.MediaTimeNS + frameDurationNS, broken: broken}
			p.count++
		}
		p.lastMediaEnd, p.lastRTPEnd, p.lastRTPSequence = f.MediaTimeNS+frameDurationNS, f.RTPTimestamp+160, f.RTPSequence
		p.hasMedia, p.hasRTPEnd, p.hasRTPSequence, p.segmentPCM, p.broken = true, true, true, true, false
	} else {
		// 辅助包保留自己的旧/新槽段号；它不能替换当前音频来源，也不代表一帧丢失。
		pureAux := f.Kind == media.RXAuxiliary || f.Kind == media.RXAuxiliaryExpired && !f.CNApplied
		if !pureAux {
			p.broken = true
		}
		if f.Kind == media.RXHistoryPLC || f.Kind == media.RXMissing || f.Kind == media.RXLocalExpired {
			if f.Kind == media.RXHistoryPLC && f.KnownDurationTicks != 160 {
				return false, ErrProtocol
			}
			// Push 的拒收/迟到仅描述编码包；它不推进 playout，不能让早到的未来包重写时间下界。
			if f.Reason != 10 && f.Reason != 11 {
				if err := p.noteKnownInterval(f); err != nil {
					return false, err
				}
			}
			if f.Kind == media.RXHistoryPLC && f.SourceSegment == p.segment {
				p.segmentPCM = true
			}
		} else if f.Kind == media.RXComfortNoise || f.Kind == media.RXAuxiliaryExpired && f.CNApplied {
			// CN 没有已知长度；最多记录它实际声明的位置，不换算成 160tick 静音。
			if f.Flags&8 != 0 && (!p.hasMedia || f.MediaTimeNS > p.lastMediaEnd) {
				p.lastMediaEnd, p.hasMedia = f.MediaTimeNS, true
			}
			if f.SourceSegment == p.segment && f.Flags&2 != 0 && (!p.hasRTPEnd || f.RTPTimestamp > p.lastRTPEnd) {
				p.lastRTPEnd, p.hasRTPEnd = f.RTPTimestamp, true
			}
		}
	}
	if f.Kind == media.RXInactiveSuspended || f.Flags&64 != 0 {
		if p.generation == 0 && (!p.suspended || f.SourceSegment > p.initialPauseSegment) {
			p.initialPauseGeneration, p.initialPauseSegment = f.SourceGeneration, f.SourceSegment
		}
		p.suspended, p.broken, invalidates = true, true, true
	}
	return invalidates, nil
}

// changeSource 只在真实边界调用。Rust last_pcm 跨来源保留，媒体时间高水位不能清零。
func (p *provenance) changeSource(generation, segment uint64) {
	if generation != p.generation {
		p.lastRTPSequence, p.hasRTPSequence = 0, false
	}
	p.generation, p.segment = generation, segment
	clear(p.spans[:])
	p.count, p.lastFinal = 0, 0
	p.lastRTPEnd, p.hasRTPEnd, p.segmentPCM, p.suspended, p.broken = 0, false, false, false, false
	p.ended = false
	p.initialPauseGeneration, p.initialPauseSegment = 0, 0
}

func (p *provenance) noteKnownInterval(f *media.RXFrame) error {
	if f.KnownDurationTicks == 0 {
		return nil
	}
	if f.KnownDurationTicks > math.MaxUint64/125_000 {
		return ErrProtocol
	}
	duration := f.KnownDurationTicks * 125_000
	if f.Flags&8 != 0 {
		if f.MediaTimeNS > math.MaxUint64-duration {
			return ErrProtocol
		}
		end := f.MediaTimeNS + duration
		if !p.hasMedia || end > p.lastMediaEnd {
			p.lastMediaEnd, p.hasMedia = end, true
		}
	}
	if f.SourceSegment == p.segment && f.Flags&2 != 0 {
		if f.RTPTimestamp > math.MaxUint64-f.KnownDurationTicks {
			return ErrProtocol
		}
		end := f.RTPTimestamp + f.KnownDurationTicks
		if !p.hasRTPEnd || end > p.lastRTPEnd {
			p.lastRTPEnd, p.hasRTPEnd = end, true
		}
	}
	return nil
}

func (p *provenance) invalidate() (Event, bool) {
	if p.utterance <= p.closedUtterance {
		return Event{}, false
	}
	p.closedUtterance = p.utterance
	return Event{Type: "partial_invalidated", UtteranceID: p.utterance, SourceGeneration: p.generation, SourceSegment: p.segment}, true
}

// result 仅接受已提交 Decoded 的精确首尾，不把 marker、PLC 或丢弃摘要算作识别输入。
func (p *provenance) result(r wire.Result) (Event, bool, error) {
	if err := r.Validate(); err != nil {
		return Event{}, false, ErrProtocol
	}
	if p.resultSequence == math.MaxUint64 || uint64(r.ResultSeq) != p.resultSequence+1 || uint64(r.LastEventSeq) > p.lastEvent {
		return Event{}, false, ErrProtocol
	}
	cmp := sourceCompare(uint64(r.SourceGeneration), uint64(r.SourceSegment), p.generation, p.segment)
	if cmp > 0 {
		return Event{}, false, ErrProtocol
	}
	if cmp < 0 || p.suspended {
		p.resultSequence = uint64(r.ResultSeq)
		return Event{}, true, nil
	}
	u, v := uint64(r.UtteranceID), uint64(r.Revision)
	if u <= p.closedUtterance || u < p.utterance || u == p.utterance && v <= p.revision || u != p.utterance && p.utterance != p.closedUtterance {
		return Event{}, false, ErrProtocol
	}
	first, last := uint64(r.FirstEventSeq), uint64(r.LastEventSeq)
	a, b := -1, -1
	for i := 0; i < p.count; i++ {
		span := p.spans[i]
		if first >= span.first && first <= span.last {
			a = i
		}
		if last >= span.first && last <= span.last {
			b = i
		}
	}
	if a < 0 || b < a || first <= p.lastFinal {
		return Event{}, false, ErrProtocol
	}
	if uint64(r.StartMediaNS) != p.spans[a].start+(first-p.spans[a].first)*frameDurationNS || uint64(r.EndMediaNS) != p.spans[b].start+(last-p.spans[b].first+1)*frameDurationNS {
		return Event{}, false, ErrProtocol
	}
	for i := a + 1; i <= b; i++ {
		if p.spans[i].broken && r.Coverage != "discontinuous" {
			return Event{}, false, ErrProtocol
		}
	}
	p.resultSequence, p.utterance, p.revision = uint64(r.ResultSeq), u, v
	if r.Type == "final" {
		p.closedUtterance, p.lastFinal = u, last
		n := 0
		for i := 0; i < p.count; i++ {
			span := p.spans[i]
			if span.last <= last {
				continue
			}
			if span.first <= last {
				span.start += (last + 1 - span.first) * frameDurationNS
				span.first = last + 1
			}
			p.spans[n] = span
			n++
		}
		clear(p.spans[n:])
		p.count = n
	}
	return Event{Type: r.Type, Text: r.Text, Coverage: r.Coverage, ResultSequence: uint64(r.ResultSeq), UtteranceID: u, Revision: v, SourceGeneration: uint64(r.SourceGeneration), SourceSegment: uint64(r.SourceSegment), FirstEventSequence: first, LastEventSequence: last, StartMediaNS: uint64(r.StartMediaNS), EndMediaNS: uint64(r.EndMediaNS)}, false, nil
}
