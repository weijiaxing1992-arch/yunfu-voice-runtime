// Package wire 实现私有本机 ASR1 协议，不是云厂商 API 或识别模型。
package wire

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const (
	MaxLength              = 8192
	MaxWireSize            = MaxLength + 4
	AudioHeaderSize        = 112
	LifetimeNS      uint64 = 100_000_000
)

type Kind uint8

const (
	KindHello Kind = iota + 1
	KindReady
	KindAudio
	KindMarker
	KindFinish
	KindResult
	KindDone
	KindError
	KindCancel
	KindPing
	KindPong
)

// Message.Body 借用读取缓冲；下一次 Read 前必须消费或明确复制。
type Message struct {
	Kind Kind
	Body []byte
}

// U64 用规范十进制 JSON 字符串保持跨语言完整精度。
type U64 uint64

func (n U64) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatUint(uint64(n), 10) + `"`), nil
}
func (n *U64) UnmarshalJSON(raw []byte) error {
	if n == nil || len(raw) < 3 || len(raw) > 22 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return errors.New("ASR1 u64 必须为十进制字符串")
	}
	s := raw[1 : len(raw)-1]
	if len(s) > 1 && s[0] == '0' {
		return errors.New("ASR1 u64 不接受前导零")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return errors.New("ASR1 u64 非规范十进制")
		}
	}
	v, err := strconv.ParseUint(string(s), 10, 64)
	if err != nil {
		return fmt.Errorf("ASR1 u64: %w", err)
	}
	*n = U64(v)
	return nil
}

// TokenBytes 严格接受非零、小写、32位十六进制连接身份。
func TokenBytes(s string) ([16]byte, error) {
	var out [16]byte
	if len(s) != 32 {
		return out, errors.New("ASR1 token 长度错误")
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return out, errors.New("ASR1 token 不是小写十六进制")
		}
	}
	_, err := hex.Decode(out[:], []byte(s))
	if err != nil {
		return out, err
	}
	if out == [16]byte{} {
		return out, errors.New("ASR1 token 不能为零")
	}
	return out, nil
}

func validToken(s string) error          { _, err := TokenBytes(s); return err }
func validText(s string, limit int) bool { return utf8.ValidString(s) && len(s) <= limit }
func validDomain(n uint16) bool          { return n == 1 || n == 2 }

type Hello struct {
	Protocol       string `json:"protocol"`
	StreamToken    string `json:"stream_token"`
	UUID           string `json:"uuid"`
	SubscriptionID U64    `json:"subscription_id"`
	SourceRate     uint32 `json:"source_rate"`
	TargetRate     uint32 `json:"target_rate"`
	Channels       uint16 `json:"channels"`
	Format         string `json:"format"`
	FrameMS        uint16 `json:"frame_ms"`
	ClockDomain    uint16 `json:"clock_domain"`
	PLCPolicy      string `json:"plc_policy"`
	GapPolicy      string `json:"gap_policy"`
}

func (h Hello) Validate() error {
	if err := validToken(h.StreamToken); err != nil {
		return err
	}
	if h.Protocol != "ASR1" || h.SubscriptionID == 0 || h.UUID == "" || !validText(h.UUID, 128) {
		return errors.New("ASR1 HELLO 身份错误")
	}
	for _, r := range h.UUID {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("ASR1 UUID 含空白或控制字符")
		}
	}
	if h.SourceRate != 8000 || (h.TargetRate != 8000 && h.TargetRate != 16000) || h.Channels != 1 || h.Format != "s16le" || h.FrameMS != 20 || !validDomain(h.ClockDomain) || h.PLCPolicy != "metadata" || h.GapPolicy != "explicit" {
		return errors.New("ASR1 HELLO 能力不受支持")
	}
	return nil
}

// Audio 是真实 Decoded 的二进制头；PCM 不能编码到 JSON。
type Audio struct {
	StreamToken                                                                                       [16]byte
	EventSeq, SourceGeneration, SourceSegment, RTPSequence, RTPTimestamp, MediaNS, LowerNS, ExpiresNS uint64
	SSRC, SampleRate, RTPClockRate, FilterDelayNS                                                     uint32
	Flags, ClockDomain, SampleCount, Origin                                                           uint16
	PCM                                                                                               []byte
}

func (a Audio) Validate() error {
	if a.StreamToken == [16]byte{} || a.EventSeq == 0 || a.SourceGeneration == 0 || a.SourceSegment == 0 {
		return errors.New("ASR1 AUDIO 身份错误")
	}
	if a.MediaNS > math.MaxUint64-20_000_000 {
		return errors.New("ASR1 AUDIO 媒体终点溢出")
	}
	if a.SampleRate != 8000 && a.SampleRate != 16000 {
		return errors.New("ASR1 AUDIO 采样率不支持")
	}
	if a.RTPClockRate != 8000 || uint32(a.SampleCount) != a.SampleRate/50 || len(a.PCM) != int(a.SampleCount)*2 || a.Origin != 1 || a.Flags&15 != 15 || a.Flags & ^uint16(0x3ff) != 0 {
		return errors.New("ASR1 AUDIO 格式或来源错误")
	}
	if (a.SampleRate == 8000 && a.FilterDelayNS != 0) || (a.SampleRate == 16000 && a.FilterDelayNS != 3_937_500) {
		return errors.New("ASR1 AUDIO 滤波延迟错误")
	}
	return validateDeadline(a.LowerNS, a.ExpiresNS, a.ClockDomain, false)
}
func validateDeadline(lower, expiry uint64, domain uint16, summary bool) error {
	if !validDomain(domain) {
		return errors.New("ASR1 不支持时钟域")
	}
	if summary {
		if lower != 0 || expiry != 0 {
			return errors.New("ASR1 摘要不得声明音频期限")
		}
		return nil
	}
	if lower == 0 || lower > math.MaxUint64-LifetimeNS || expiry != lower+LifetimeNS {
		return errors.New("ASR1 原始期限错误")
	}
	return nil
}

// CheckFreshAt 仅检查此检查点；不能把检查和后续 syscall 视为原子操作。
func (a Audio) CheckFreshAt(now uint64, domain uint16) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if domain != a.ClockDomain || !validDomain(domain) {
		return errors.New("ASR1 时钟域不匹配")
	}
	if now < a.LowerNS || now >= a.ExpiresNS {
		return errors.New("ASR1 AUDIO 未到观察下界或已经过期")
	}
	return nil
}

type Gap struct {
	First   U64    `json:"first"`
	Last    U64    `json:"last"`
	Decoded U64    `json:"decoded"`
	PLC     U64    `json:"plc"`
	Reasons uint16 `json:"reasons"`
}

func (g Gap) Validate() error {
	if g.First == 0 || g.Last < g.First || g.Reasons < 1 || g.Reasons > 7 {
		return errors.New("ASR1 gap 范围或原因错误")
	}
	n := uint64(g.Last-g.First) + 1
	if uint64(g.Decoded) > n || uint64(g.PLC) > n-uint64(g.Decoded) {
		return errors.New("ASR1 gap 丢弃计数越界")
	}
	return nil
}

type Marker struct {
	StreamToken        string  `json:"stream_token"`
	RXKind             uint8   `json:"rx_kind"`
	EventSeq           U64     `json:"event_seq"`
	SourceGeneration   U64     `json:"source_generation"`
	SourceSegment      U64     `json:"source_segment"`
	ClockDomain        uint16  `json:"clock_domain"`
	LowerNS            U64     `json:"lower_ns"`
	ExpiresNS          U64     `json:"expires_ns"`
	Flags              uint16  `json:"flags"`
	SSRC               uint32  `json:"ssrc"`
	RTPSequence        U64     `json:"rtp_sequence"`
	RTPTimestamp       U64     `json:"rtp_timestamp"`
	MediaNS            U64     `json:"media_ns"`
	SampleRate         uint32  `json:"sample_rate"`
	RTPClockRate       uint32  `json:"rtp_clock_rate"`
	ObservationAgeMS   uint32  `json:"observation_age_ms"`
	ArrivalAgeMS       uint32  `json:"arrival_age_ms"`
	DeadlineLatenessMS uint32  `json:"deadline_lateness_ms"`
	QueueAgeMS         uint32  `json:"queue_age_ms"`
	KnownDurationTicks *U64    `json:"known_duration_ticks"`
	Gap                *Gap    `json:"gap"`
	SIDBase64          *string `json:"sid_b64"`
	CNApplied          bool    `json:"cn_applied"`
	Reason             uint16  `json:"reason"`
	Boundary           uint8   `json:"boundary"`
	DiscardedPackets   U64     `json:"discarded_packets"`
}

type Finish struct {
	StreamToken  string `json:"stream_token"`
	LastEventSeq U64    `json:"last_event_seq"`
	AudioFrames  U64    `json:"audio_frames"`
	Samples      U64    `json:"samples"`
	Markers      U64    `json:"markers"`
}

func (f Finish) Validate() error {
	if err := validToken(f.StreamToken); err != nil {
		return err
	}
	if (f.AudioFrames == 0) != (f.Samples == 0) || f.AudioFrames > f.LastEventSeq || f.Markers > f.LastEventSeq+1 && f.LastEventSeq != U64(math.MaxUint64) {
		return errors.New("ASR1 FINISH 计数错误")
	}
	if f.AudioFrames != 0 && (f.Samples%f.AudioFrames != 0 || f.Samples/f.AudioFrames != 160 && f.Samples/f.AudioFrames != 320) {
		return errors.New("ASR1 FINISH 样本格式计数错误")
	}
	return nil
}

type Result struct {
	StreamToken      string `json:"stream_token"`
	ResultSeq        U64    `json:"result_seq"`
	UtteranceID      U64    `json:"utterance_id"`
	Revision         U64    `json:"revision"`
	SourceGeneration U64    `json:"source_generation"`
	SourceSegment    U64    `json:"source_segment"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	FirstEventSeq    U64    `json:"first_event_seq"`
	LastEventSeq     U64    `json:"last_event_seq"`
	StartMediaNS     U64    `json:"start_media_ns"`
	EndMediaNS       U64    `json:"end_media_ns"`
	Coverage         string `json:"coverage"`
}

func (r Result) Validate() error {
	if err := validToken(r.StreamToken); err != nil {
		return err
	}
	if r.ResultSeq == 0 || r.UtteranceID == 0 || r.Revision == 0 || r.SourceGeneration == 0 || r.SourceSegment == 0 || r.FirstEventSeq == 0 || r.LastEventSeq < r.FirstEventSeq || r.StartMediaNS >= r.EndMediaNS || (r.Type != "partial" && r.Type != "final") || (r.Coverage != "complete" && r.Coverage != "discontinuous") || !validText(r.Text, 4096) {
		return errors.New("ASR1 RESULT 内容错误")
	}
	return nil
}

type Error struct {
	StreamToken string `json:"stream_token"`
	Code        string `json:"code"`
}

func (e Error) Validate() error {
	if err := validToken(e.StreamToken); err != nil {
		return err
	}
	if len(e.Code) == 0 || len(e.Code) > 128 || e.Code[0] < 'a' || e.Code[0] > 'z' {
		return errors.New("ASR1 ERROR code 错误")
	}
	for _, c := range e.Code {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return errors.New("ASR1 ERROR code 错误")
		}
	}
	return nil
}

type Ping struct {
	StreamToken string `json:"stream_token"`
	Nonce       U64    `json:"nonce"`
}

func (p Ping) Validate() error { return validToken(p.StreamToken) }

type Cancel struct {
	StreamToken string `json:"stream_token"`
	Reason      string `json:"reason"`
}

func (c Cancel) Validate() error {
	if err := validToken(c.StreamToken); err != nil {
		return err
	}
	if !validText(c.Reason, 128) {
		return errors.New("ASR1 CANCEL 原因过长")
	}
	return nil
}
