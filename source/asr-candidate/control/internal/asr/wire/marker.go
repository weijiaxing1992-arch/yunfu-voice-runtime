package wire

import (
	"encoding/base64"
	"errors"
)

func (m Marker) Validate() error {
	if err := validToken(m.StreamToken); err != nil {
		return err
	}
	if m.RXKind < 2 || m.RXKind > 13 || m.Flags & ^uint16(0x3ff) != 0 || m.SampleRate != 8000 || m.RTPClockRate != 8000 || m.Boundary > 6 {
		return errors.New("ASR1 MARKER 类型或格式错误")
	}
	if m.Flags&1 == 0 && m.SSRC != 0 || m.Flags&2 == 0 && m.RTPTimestamp != 0 || m.Flags&4 == 0 && m.RTPSequence != 0 || m.Flags&8 == 0 && m.MediaNS != 0 || m.Flags&256 == 0 && m.ArrivalAgeMS != 0 || m.Flags&512 == 0 && m.DeadlineLatenessMS != 0 {
		return errors.New("ASR1 未声明有效的 MARKER 位置不为零")
	}
	// 观察队列失败只改kind/reason并清正文，原边界、丢弃计数和CN事实仍有效。
	terminalInheritance := m.RXKind == 9 && (m.Reason == 8 || m.Reason == 9)
	if (m.Boundary != 0) != (m.Flags&16 != 0) || m.Boundary != 0 && m.Boundary != 5 && m.RXKind != 7 && !terminalInheritance || m.Boundary == 6 && m.Flags&4 != 0 {
		return errors.New("ASR1 MARKER 来源边界错误")
	}
	// 编码包迟到/拒收恰好丢弃一包；不把推入失败的辅助包误写为20ms静音。
	if !m.validDiscardedInheritance(terminalInheritance) || m.RXKind == 9 && !terminalInheritance {
		return errors.New("ASR1 MARKER 不允许继承该丢弃计数或终态原因")
	}
	if terminalInheritance {
		duration := U64(0)
		if m.KnownDurationTicks != nil {
			duration = *m.KnownDurationTicks
		}
		if m.Boundary == 5 && (duration != 160 || m.DiscardedPackets != 0 || m.CNApplied) || m.Boundary != 0 && m.Boundary != 5 && (duration != 0 || m.CNApplied) || m.CNApplied && (m.Boundary != 0 || duration != 0 || m.DiscardedPackets != 0) {
			return errors.New("ASR1 观察终态混合了不同原始观察事实")
		}
	}
	summary := m.RXKind == 10 || m.RXKind == 11 || m.RXKind == 12
	if err := validateDeadline(uint64(m.LowerNS), uint64(m.ExpiresNS), m.ClockDomain, summary); err != nil {
		return err
	}
	if summary {
		if m.Flags != 0 || m.SourceGeneration != 0 || m.SourceSegment != 0 || m.KnownDurationTicks != nil || m.DiscardedPackets != 0 || m.ObservationAgeMS != 0 || m.ArrivalAgeMS != 0 || m.DeadlineLatenessMS != 0 || m.Boundary != 0 || m.CNApplied {
			return errors.New("ASR1 摘要携带音频/来源位置")
		}
	} else if m.EventSeq == 0 || m.Reason > 11 {
		return errors.New("ASR1 MARKER 序号或原因错误")
	}
	if m.RXKind == 10 {
		if m.Gap == nil {
			return errors.New("ASR1 缺少 gap")
		}
		if err := m.Gap.Validate(); err != nil {
			return err
		}
		if m.Gap.Last != m.EventSeq || m.Reason != m.Gap.Reasons {
			return errors.New("ASR1 gap 摘要不一致")
		}
	} else if m.Gap != nil {
		return errors.New("ASR1 非 gap 携带 gap 字段")
	}
	if m.RXKind == 11 && m.Reason != 0 || m.RXKind == 12 && m.Reason != 1 {
		return errors.New("ASR1 结束摘要原因错误")
	}
	if m.RXKind == 2 && (m.KnownDurationTicks == nil || *m.KnownDurationTicks != 160) {
		return errors.New("ASR1 PLC 必须保留已知160tick")
	}
	if m.RXKind == 6 && m.KnownDurationTicks != nil {
		return errors.New("ASR1 辅助过期不能伪造20ms静默")
	}
	if m.RXKind == 4 {
		if m.SIDBase64 == nil || len(*m.SIDBase64) > 640 {
			return errors.New("ASR1 CN SID 缺失或过长")
		}
		var sid [480]byte
		n, err := base64.StdEncoding.Strict().Decode(sid[:], []byte(*m.SIDBase64))
		if err != nil || n == 0 || n > 480 || base64.StdEncoding.EncodeToString(sid[:n]) != *m.SIDBase64 {
			return errors.New("ASR1 CN SID 非规范base64")
		}
	} else if m.SIDBase64 != nil {
		return errors.New("ASR1 仅CN可携带SID")
	}
	// RXS2允许过期CN辅助转发记录保留cn_applied，不能丢掉这项原始语义。
	if m.CNApplied && m.RXKind != 4 && m.RXKind != 6 && !terminalInheritance {
		return errors.New("ASR1 cn_applied 类型错误")
	}
	return nil
}

// 只约束非零丢弃计数；正常playout deadline及CN过期仍可保留零计数。
func (m Marker) validDiscardedInheritance(terminal bool) bool {
	if m.DiscardedPackets == 0 {
		return true
	}
	duration := U64(0)
	if m.KnownDurationTicks != nil {
		duration = *m.KnownDurationTicks
	}
	if m.RXKind == 7 {
		return (m.Boundary == 2 || m.Boundary == 3 || m.Boundary == 4 || m.Boundary == 6) && m.Reason == 0 && duration == 0 && !m.CNApplied
	}
	if terminal {
		if m.CNApplied {
			return false
		}
		switch m.Boundary {
		case 2, 3, 4, 6:
			return duration == 0
		case 0:
			return m.DiscardedPackets == 1 && (duration == 0 || duration == 160)
		default:
			return false
		}
	}
	return m.DiscardedPackets == 1 && !m.CNApplied && (m.RXKind == 5 && (m.Reason == 10 || m.Reason == 11) && duration == 160 || m.RXKind == 6 && m.Reason == 10 && duration == 0)
}

func (m Marker) CheckFreshAt(now uint64, domain uint16) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if domain != m.ClockDomain || !validDomain(domain) {
		return errors.New("ASR1 MARKER 时钟域不匹配")
	}
	if m.RXKind == 10 || m.RXKind == 11 || m.RXKind == 12 {
		return nil
	}
	if now < uint64(m.LowerNS) || now >= uint64(m.ExpiresNS) {
		return errors.New("ASR1 MARKER 已过期或来自未来")
	}
	return nil
}
