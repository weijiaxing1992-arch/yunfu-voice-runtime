package sip

import (
	"errors"
	"strings"
)

// RenderProcessedOffer 独立向B腿提供真实图支持的两个编码；A腿原始PT和编码保持其自己的合同。
func RenderProcessedOffer(id uint64, ip string, rtp, rtcp int, a SDP) []byte {
	b := a
	b.Codec = a.CodecMetadata()
	b.Payload = 0
	b.Codec.Name = "PCMU"
	b.Codec.FMTP = ""
	body := string(RenderSDP(id, ip, rtp, rtcp, b))
	// RenderSDP只产生一条m=audio，精确在主PT后加入PCMA及其映射。
	body = strings.Replace(body, " RTP/AVP 0", " RTP/AVP 0 8", 1)
	body += "a=rtpmap:8 PCMA/8000\r\n"
	return []byte(body)
}

// NegotiateProcessedAnswer 仅允许应答B腿实际报价的PT0/8，不允许任意动态重映射或额外编解码器。
// 辅助载荷仍核对原报价；B腿拒绝辅助功能时，同时收紧给A腿的应答。
func NegotiateProcessedAnswer(a, b SDP) (SDP, SDP, error) {
	if b.PTime != 20 || !((b.Payload == 0 && b.CodecMetadata().Name == "PCMU") || (b.Payload == 8 && b.CodecMetadata().Name == "PCMA")) {
		return SDP{}, SDP{}, errors.New("answer is outside processed G711 offer")
	}
	template := a
	template.Payload = b.Payload
	template.Codec = b.CodecMetadata()
	negotiated, err := NegotiateAnswer(template, b)
	if err != nil {
		return SDP{}, SDP{}, err
	}
	a.DTMF, a.DTMFClockRate, a.DTMFEvents = negotiated.DTMF, negotiated.DTMFClockRate, negotiated.DTMFEvents
	a.CN, a.CNClockRate = negotiated.CN, negotiated.CNClockRate
	return a, negotiated, nil
}
