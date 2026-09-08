package wire

import (
	"encoding/binary"
	"errors"
	"io"
)

func validKind(k Kind) bool { return k >= KindHello && k <= KindPong }

// Read 使用调用方固定缓冲，读正文前验证长度/前缀；半帧EOF不能当成功结束。
func Read(r io.Reader, buf *[MaxWireSize]byte) (Message, error) {
	if buf == nil {
		return Message{}, errors.New("ASR1 读取缓冲为空")
	}
	if _, err := io.ReadFull(r, buf[:4]); err != nil {
		return Message{}, err
	}
	n := binary.LittleEndian.Uint32(buf[:4])
	if n < 4 || n > MaxLength {
		return Message{}, errors.New("ASR1 length 越界")
	}
	if _, err := io.ReadFull(r, buf[4:8]); err != nil {
		return Message{}, err
	}
	k := Kind(buf[4])
	if !validKind(k) || buf[5] != 1 || binary.LittleEndian.Uint16(buf[6:8]) != 0 {
		return Message{}, errors.New("ASR1 前缀版本/类型/保留位错误")
	}
	if _, err := io.ReadFull(r, buf[8:4+int(n)]); err != nil {
		return Message{}, err
	}
	return Message{Kind: k, Body: buf[8 : 4+int(n)]}, nil
}

// Encode 只使用给定容量，不扩展缓冲；返回含前缀完整消息。
func Encode(dst []byte, m Message) ([]byte, error) {
	if !validKind(m.Kind) || len(m.Body) > MaxLength-4 {
		return nil, errors.New("ASR1 消息类型或大小错误")
	}
	n := len(m.Body) + 8
	if cap(dst) < n {
		return nil, io.ErrShortBuffer
	}
	dst = dst[:n]
	// 先移动正文，支持调用方在同一固定数组内组装消息而不破坏重叠内容。
	copy(dst[8:], m.Body)
	binary.LittleEndian.PutUint32(dst, uint32(n-4))
	dst[4] = byte(m.Kind)
	dst[5] = 1
	dst[6] = 0
	dst[7] = 0
	return dst, nil
}

func EncodeAudio(dst []byte, a Audio) ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	n := AudioHeaderSize + len(a.PCM)
	if cap(dst) < n {
		return nil, io.ErrShortBuffer
	}
	dst = dst[:n]
	copy(dst[AudioHeaderSize:], a.PCM)
	clear(dst[:AudioHeaderSize])
	copy(dst[:16], a.StreamToken[:])
	u64 := []uint64{a.EventSeq, a.SourceGeneration, a.SourceSegment, a.RTPSequence, a.RTPTimestamp, a.MediaNS, a.LowerNS, a.ExpiresNS}
	for i, v := range u64 {
		binary.LittleEndian.PutUint64(dst[16+i*8:], v)
	}
	u32 := []uint32{a.SSRC, a.SampleRate, a.RTPClockRate, a.FilterDelayNS}
	for i, v := range u32 {
		binary.LittleEndian.PutUint32(dst[80+i*4:], v)
	}
	u16 := []uint16{a.Flags, a.ClockDomain, a.SampleCount, a.Origin, uint16(len(a.PCM))}
	for i, v := range u16 {
		binary.LittleEndian.PutUint16(dst[96+i*2:], v)
	}
	return dst, nil
}

// ParseAudio 借用PCM正文，不为每帧创建样本切片或猜测格式。
func ParseAudio(raw []byte) (Audio, error) {
	if len(raw) < AudioHeaderSize || len(raw) > AudioHeaderSize+640 {
		return Audio{}, errors.New("ASR1 AUDIO 长度错误")
	}
	for _, b := range raw[106:112] {
		if b != 0 {
			return Audio{}, errors.New("ASR1 AUDIO 保留位错误")
		}
	}
	u64 := func(i int) uint64 { return binary.LittleEndian.Uint64(raw[i:]) }
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(raw[i:]) }
	u16 := func(i int) uint16 { return binary.LittleEndian.Uint16(raw[i:]) }
	if int(u16(104)) != len(raw)-AudioHeaderSize {
		return Audio{}, errors.New("ASR1 AUDIO 正文计数错误")
	}
	a := Audio{EventSeq: u64(16), SourceGeneration: u64(24), SourceSegment: u64(32), RTPSequence: u64(40), RTPTimestamp: u64(48), MediaNS: u64(56), LowerNS: u64(64), ExpiresNS: u64(72), SSRC: u32(80), SampleRate: u32(84), RTPClockRate: u32(88), FilterDelayNS: u32(92), Flags: u16(96), ClockDomain: u16(98), SampleCount: u16(100), Origin: u16(102), PCM: raw[AudioHeaderSize:]}
	copy(a.StreamToken[:], raw[:16])
	if err := a.Validate(); err != nil {
		return Audio{}, err
	}
	return a, nil
}
