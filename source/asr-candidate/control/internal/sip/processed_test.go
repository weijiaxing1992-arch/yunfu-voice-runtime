package sip

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// processedDescription 解析真实SDP文本，避免手写已选择字段掩盖候选与静态PT规则。
func processedDescription(t *testing.T, payloads, attributes string) SDP {
	t.Helper()
	cfg := mediaConfig()
	cfg.Processing = "g711"
	value, err := ParseSDP([]byte(codecSDP(payloads, attributes)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestProcessedOfferKeepsDynamicAAndActuallyOffersBothStaticCodecs 验证B真实报价0/8，不把A动态PT写错或在渲染过程中修改A。
func TestProcessedOfferKeepsDynamicAAndActuallyOffersBothStaticCodecs(t *testing.T) {
	for _, example := range []struct{ payloads, attributes string }{
		{"8 101 103", ""},
		{"96 101 103", "a=rtpmap:96 PCMA/8000\r\n"},
		{"97 101 103", "a=rtpmap:97 PCMU/8000\r\n"},
	} {
		t.Run(example.payloads, func(t *testing.T) {
			a := processedDescription(t, example.payloads, example.attributes+"a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-9\r\na=rtpmap:103 CN/8000\r\na=ptime:20\r\n")
			before := RenderSDP(1, "127.0.0.1", 31000, 31001, a)
			wire := RenderProcessedOffer(1, "127.0.0.1", 32000, 32001, a)
			if !bytes.Contains(wire, []byte("m=audio 32000 RTP/AVP 0 8 101 103\r\n")) || !bytes.Contains(wire, []byte("a=rtpmap:0 PCMU/8000\r\n")) || !bytes.Contains(wire, []byte("a=rtpmap:8 PCMA/8000\r\n")) {
				t.Fatalf("B腿没有真实报价两种静态编码：%s", wire)
			}
			cfg := mediaConfig()
			cfg.Processing = "g711"
			b, err := ParseSDP(wire, cfg)
			if err != nil || b.Payload != 0 || b.Codec.Name != "PCMU" || b.DTMF == nil || *b.DTMF != 101 || b.CN == nil || *b.CN != 103 {
				t.Fatalf("B报价解析失败：%+v %v", b, err)
			}
			other := bytes.Replace(wire, []byte("RTP/AVP 0 8"), []byte("RTP/AVP 8 0"), 1)
			b, err = ParseSDP(other, cfg)
			if err != nil || b.Payload != 8 || b.Codec.Name != "PCMA" {
				t.Fatalf("PCMA候选无法实际选择：%+v %v", b, err)
			}
			if after := RenderSDP(1, "127.0.0.1", 31000, 31001, a); !bytes.Equal(before, after) {
				t.Fatal("生成B报价意外改变A腿合同")
			}
		})
	}
}

// TestProcessedOfferLegacyG711Metadata 兼容原SDP构造器的省略codec字段，不能生成PCMU/0的无效线路报价。
func TestProcessedOfferLegacyG711Metadata(t *testing.T) {
	a := SDP{Payload: 8, PTime: 20}
	wire := RenderProcessedOffer(1, "127.0.0.1", 32000, 32001, a)
	cfg := mediaConfig()
	cfg.Processing = "g711"
	if _, err := ParseSDP(wire, cfg); err != nil {
		t.Fatalf("旧G711元数据生成非法报价：%s；%v", wire, err)
	}
}

// TestProcessedSelectionRequiresActualTwentyMillisecondG711 处理模式选择后面的G711候选，relay仍保留原候选顺序和包时长。
func TestProcessedSelectionRequiresActualTwentyMillisecondG711(t *testing.T) {
	body := codecSDP("111 9 96", "a=rtpmap:111 opus/48000/2\r\na=rtpmap:96 PCMA/8000\r\na=ptime:20\r\n")
	cfg := mediaConfig()
	legacy, err := ParseSDP([]byte(body), cfg)
	if err != nil || legacy.Codec.Name != "OPUS" {
		t.Fatal(legacy, err)
	}
	cfg.Processing = "relay"
	relay, err := ParseSDP([]byte(body), cfg)
	if err != nil || !reflect.DeepEqual(legacy, relay) {
		t.Fatal("显式relay改变原协商行为", err)
	}
	cfg.Processing = "g711"
	processed, err := ParseSDP([]byte(body), cfg)
	if err != nil || processed.Payload != 96 || processed.Codec.Name != "PCMA" || processed.PTime != 20 {
		t.Fatalf("没有选择实际支持的G711候选：%+v %v", processed, err)
	}
	for _, ptime := range []string{"10", "30", "40", "50", "60"} {
		wire := []byte(strings.Replace(body, "a=ptime:20", "a=ptime:"+ptime, 1))
		if _, err := ParseSDP(wire, cfg); err == nil {
			t.Fatalf("首批固定20ms图接受ptime=%s", ptime)
		}
		legacyCfg := mediaConfig()
		if _, err := ParseSDP(wire, legacyCfg); err != nil {
			t.Fatalf("处理图限制污染旧透传ptime=%s：%v", ptime, err)
		}
	}
	invalid := strings.Replace(body, "a=rtpmap:96 PCMA/8000", "a=rtpmap:9 G722/16000\r\na=rtpmap:96 PCMA/8000", 1)
	if _, err := ParseSDP([]byte(invalid), cfg); err == nil {
		t.Fatal("跳过前置候选时也跳过了非法静态映射校验")
	}
}

// TestProcessedAnswerPreservesAAndIntersectsAuxiliaryOffers 验证跨编码的主PT独立，辅助能力只能取真实报价交集。
func TestProcessedAnswerPreservesAAndIntersectsAuxiliaryOffers(t *testing.T) {
	a := processedDescription(t, "96 101 13", "a=rtpmap:96 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-9\r\n")
	b := processedDescription(t, "0 101 13", "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 4-15\r\n")
	b.Peer.RTP = "127.0.0.1:34000"
	accepted, remote, err := NegotiateProcessedAnswer(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Payload != 96 || accepted.Codec != a.Codec || accepted.Peer != a.Peer || remote.Payload != 0 || remote.Codec.Name != "PCMU" || remote.Peer != b.Peer {
		t.Fatalf("混淆了A/B协商身份：A=%+v B=%+v", accepted, remote)
	}
	if accepted.DTMFEvents != "4-9" || remote.DTMFEvents != "4-9" || accepted.DTMF == nil || *accepted.DTMF != 101 || accepted.CN == nil || *accepted.CN != 13 {
		t.Fatalf("辅助格式没有按交集保留：A=%+v B=%+v", accepted, remote)
	}
	if a.DTMFEvents != "0-9" || b.DTMFEvents != "4-15" {
		t.Fatal("应答协商修改了原始报价对象")
	}
	if !bytes.Contains(RenderSDP(1, "127.0.0.1", 31000, 31001, accepted), []byte("a=rtpmap:96 PCMA/8000\r\n")) {
		t.Fatal("返回A的SDP丢失动态PCMA映射")
	}
}

// TestProcessedAnswerRejectsUnofferedPayloadsAndAuxiliaryFormats 只验证已经成功解析的SDP，不用解析错误代替应答报价校验。
func TestProcessedAnswerRejectsUnofferedPayloadsAndAuxiliaryFormats(t *testing.T) {
	a := processedDescription(t, "96 101 13", "a=rtpmap:96 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-9\r\n")
	for _, example := range []struct{ name, payloads, attributes string }{
		{"动态主PT未报价", "97", "a=rtpmap:97 PCMU/8000\r\n"},
		{"DTMF重映射未报价", "0 102", "a=rtpmap:102 telephone-event/8000\r\n"},
		{"DTMF时钟变更未报价", "0 101", "a=rtpmap:101 telephone-event/16000\r\n"},
		{"CN重映射未报价", "0 103", "a=rtpmap:103 CN/8000\r\n"},
	} {
		t.Run(example.name, func(t *testing.T) {
			b := processedDescription(t, example.payloads, example.attributes)
			if _, _, err := NegotiateProcessedAnswer(a, b); err == nil {
				t.Fatal("接受未报价的应答", b)
			}
		})
	}
	noAux := processedDescription(t, "96", "a=rtpmap:96 PCMU/8000\r\n")
	for _, b := range []SDP{
		processedDescription(t, "0 101", "a=rtpmap:101 telephone-event/8000\r\n"),
		processedDescription(t, "8 13", ""),
	} {
		if _, _, err := NegotiateProcessedAnswer(noAux, b); err == nil {
			t.Fatal("原报价无辅助格式却接受应答新增", b)
		}
	}
}

// TestProcessedAnswerNarrowingDoesNotDestroyOriginalOffer 早期应答收紧仅影响当前结果，最终协商必须仍可引用保存的原报价。
func TestProcessedAnswerNarrowingDoesNotDestroyOriginalOffer(t *testing.T) {
	a := processedDescription(t, "96 101 13", "a=rtpmap:96 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-9\r\n")
	early := processedDescription(t, "0", "")
	accepted, _, err := NegotiateProcessedAnswer(a, early)
	if err != nil || accepted.DTMF != nil || accepted.CN != nil {
		t.Fatal("早期应答未正确收紧", accepted, err)
	}
	final := processedDescription(t, "8 101 13", "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-5\r\n")
	accepted, _, err = NegotiateProcessedAnswer(a, final)
	if err != nil || accepted.DTMF == nil || accepted.CN == nil || accepted.DTMFEvents != "0-5" {
		t.Fatal("原报价不能用于最终协商", accepted, err)
	}
	emptyIntersection := processedDescription(t, "8 101", "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 10-15\r\n")
	accepted, remote, err := NegotiateProcessedAnswer(a, emptyIntersection)
	if err != nil || accepted.DTMF != nil || remote.DTMF != nil || accepted.DTMFEvents != "" || remote.DTMFClockRate != 0 {
		t.Fatal("空事件交集错误扩大成默认0-15", accepted, remote, err)
	}
}
