// Package codecprofile 保存可重复的 RTP 传输测试格式；它不是编解码实现或音质认证。
package codecprofile

import (
	"bytes"
	"rustswitch/control/internal/media"
)

// Profile 把测试载荷号、音频采样率和 RTP 时钟分别描述；动态 PT 仅为本测试约定。
type Profile struct {
	Payload      uint8  `json:"payload"`
	Name         string `json:"name"`
	SampleRate   uint32 `json:"sample_rate"`
	RTPClockRate uint32 `json:"rtp_clock_rate"`
	Channels     uint8  `json:"channels"`
	PTimeMS      uint16 `json:"ptime_ms"`
	FMTP         string `json:"fmtp"`
	Validation   string `json:"validation"` // 明确区分字节传输验证与实际音频解码。
}

// List 每次返回独立列表；同编码透传不表示已经装载对应的转码器。
func List() []Profile {
	const transport = "验证协商、RTP时钟和双向原样字节；不代表转码或听感认证"
	return []Profile{
		{0, "PCMU", 8000, 8000, 1, 20, "", transport},
		{8, "PCMA", 8000, 8000, 1, 20, "", transport},
		{9, "G722", 16000, 8000, 1, 20, "", transport},
		{111, "OPUS", 48000, 48000, 2, 20, "stereo=0;sprop-stereo=0", transport},
		{18, "G729", 8000, 8000, 1, 20, "annexb=no", transport},
		{110, "G726-32", 8000, 8000, 1, 20, "", transport},
		{112, "G726-16", 8000, 8000, 1, 20, "", transport},
		{113, "G726-24", 8000, 8000, 1, 20, "", transport},
		{114, "G726-40", 8000, 8000, 1, 20, "", transport},
		{119, "L16", 16000, 16000, 1, 20, "", transport},
		{120, "AAL2-G726-32", 8000, 8000, 1, 20, "", transport},
		{121, "AAL2-G726-16", 8000, 8000, 1, 20, "", transport},
		{122, "AAL2-G726-24", 8000, 8000, 1, 20, "", transport},
		{123, "AAL2-G726-40", 8000, 8000, 1, 20, "", transport},
	}
}

// Lookup 严格匹配预置测试格式，不根据任意动态载荷号猜测编码。
func Lookup(payload uint8) (Profile, bool) {
	for _, profile := range List() {
		if profile.Payload == payload {
			return profile, true
		}
	}
	return Profile{}, false
}

// TestPayloads 用于管理能力和服务端请求校验，前端不能绕过此列表自行宣告支持。
func TestPayloads() []int {
	profiles := List()
	result := make([]int, len(profiles))
	for i, p := range profiles {
		result[i] = int(p.Payload)
	}
	return result
}

// Spec 返回线上媒体元数据；L16 是网络大端 PCM，不是内部 S16LE 音频帧。
func (p Profile) Spec() media.CodecSpec {
	return media.CodecSpec{Name: p.Name, SampleRate: p.SampleRate, RTPClockRate: p.RTPClockRate, Channels: p.Channels, PTimeMS: p.PTimeMS, FMTP: p.FMTP}
}

// TimestampStep 按独立 RTP 时钟计算每个测试包的时间戳增量：G722 为160，Opus为960。
func (p Profile) TimestampStep() uint32 { return p.RTPClockRate * uint32(p.PTimeMS) / 1000 }

// Frame 返回固定传输向量，生成器核对字节一致性；不把任意压缩码字称为静音或语音质量证明。
// Opus 的三字节包是20ms单声道静音编码；其他压缩格式另由原生音频自检验证解码器。
func (p Profile) Frame() []byte {
	switch p.Name {
	case "PCMU":
		return bytes.Repeat([]byte{0xff}, 160)
	case "PCMA":
		return bytes.Repeat([]byte{0xd5}, 160)
	case "G722":
		return bytes.Repeat([]byte{0xfa}, 160)
	case "OPUS":
		return []byte{0xf8, 0xff, 0xfe}
	case "G729":
		return make([]byte, 20)
	case "G726-16", "AAL2-G726-16":
		return make([]byte, 40)
	case "G726-24", "AAL2-G726-24":
		return make([]byte, 60)
	case "G726-32", "AAL2-G726-32":
		return make([]byte, 80)
	case "G726-40", "AAL2-G726-40":
		return make([]byte, 100)
	case "L16":
		return make([]byte, 640)
	default:
		return nil
	}
}
