package sip

import (
	"rustswitch/control/internal/config"
	"strings"
	"testing"
)

// mediaConfig 为 SDP 测试提供独立端口区间与回环媒体白名单。
func mediaConfig() config.Media {
	return config.Media{BindIP: "127.0.0.1", AdvertiseIP: "127.0.0.1", PortStart: 20000, PortEnd: 21999, AllowedRemoteNetworks: []string{"127.0.0.0/8"}}
}

// sdpFixture 提供 G.711 双候选、电话事件和二十毫秒分包的单音频流样本。
const sdpFixture = "v=0\r\no=x 1 1 IN IP4 127.0.0.1\r\ns=x\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0 8 101\r\na=rtpmap:101 telephone-event/8000\r\na=ptime:20\r\n"

// TestSDPRoundTripAndMediaConnection 验证编解码选择、RTCP 默认端口、描述重建和媒体级地址覆盖。
func TestSDPRoundTripAndMediaConnection(t *testing.T) {
	value, e := ParseSDP([]byte(sdpFixture), mediaConfig())
	if e != nil {
		t.Fatal(e)
	}
	if value.Payload != 0 || value.DTMF == nil || value.Peer.RTCP != "127.0.0.1:30001" {
		t.Fatalf("%+v", value)
	}
	rendered := RenderSDP(1, "127.0.0.1", 31000, 31001, value)
	round, e := ParseSDP(rendered, mediaConfig())
	if e != nil || round.Peer.RTP != "127.0.0.1:31000" {
		t.Fatal(round, e)
	}
	text := sdpFixture + "c=IN IP4 127.0.0.2\r\n"
	changed, e := ParseSDP([]byte(text), mediaConfig())
	if e != nil || changed.Peer.RTP != "127.0.0.2:30000" {
		t.Fatal(changed, e)
	}
}

// TestSDPRejectsUnsupportedAndReflectedMedia 覆盖自反地址、白名单越界和未实现协议特性。
func TestSDPRejectsUnsupportedAndReflectedMedia(t *testing.T) {
	cases := []string{
		strings.Replace(sdpFixture, "audio 30000", "audio 20000", 1),
		strings.ReplaceAll(sdpFixture, "127.0.0.1", "8.8.8.8"),
		sdpFixture + "m=video 30002 RTP/AVP 96\r\n",
		sdpFixture + "a=rtpmap:0 opus/48000/2\r\n",
		sdpFixture + "a=rtcp-mux\r\n",
		sdpFixture + "a=sendonly\r\n",
		strings.Replace(sdpFixture, "audio 30000", "audio 65535", 1),
	}
	for _, text := range cases {
		if _, e := ParseSDP([]byte(text), mediaConfig()); e == nil {
			t.Fatal("accepted unsupported or unsafe SDP")
		}
	}
}

// FuzzSDP 检查随机媒体描述不会使有限输入解析器崩溃。
func FuzzSDP(f *testing.F) {
	f.Add([]byte(sdpFixture))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = ParseSDP(data, mediaConfig()) })
}

// TestRTP65535RequiresExplicitRTCP 验证最高 RTP 端口在显式给出其他 RTCP 端口后可接受。
func TestRTP65535RequiresExplicitRTCP(t *testing.T) {
	text := strings.Replace(sdpFixture, "audio 30000", "audio 65535", 1) + "a=rtcp:30001 IN IP4 127.0.0.1\r\n"
	if _, e := ParseSDP([]byte(text), mediaConfig()); e != nil {
		t.Fatal(e)
	}
}

// codecSDP 构造单音频流，保留测试所需的原始 PT 与属性，不通过渲染器掩盖输入问题。
func codecSDP(payloads, attributes string) string {
	return "v=0\r\no=x 1 1 IN IP4 127.0.0.1\r\ns=x\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP " + payloads + "\r\n" + attributes
}

// TestAudioCodecMappingsRoundTrip 核对真实线路编码身份及重建结果，尤其 G722 双时钟和 Opus 声道声明。
func TestAudioCodecMappingsRoundTrip(t *testing.T) {
	cases := []struct {
		payloads, attributes, name string
		payload                    uint8
		sample, clock              uint32
		channels                   uint8
	}{
		{"0", "", "PCMU", 0, 8000, 8000, 1},
		{"8", "", "PCMA", 8, 8000, 8000, 1},
		{"9", "", "G722", 9, 16000, 8000, 1},
		{"111", "a=rtpmap:111 opus/48000/2\r\na=fmtp:111 useinbandfec=1;minptime=10;stereo=0\r\n", "OPUS", 111, 48000, 48000, 2},
		{"96", "a=rtpmap:96 PCMU/8000\r\n", "PCMU", 96, 8000, 8000, 1},
		{"97", "a=rtpmap:97 PCMA/8000\r\n", "PCMA", 97, 8000, 8000, 1},
		{"98", "a=rtpmap:98 G722/8000\r\n", "G722", 98, 16000, 8000, 1},
		{"18", "a=fmtp:18 annexb=no\r\n", "G729", 18, 8000, 8000, 1},
		{"99", "a=rtpmap:99 G729A/8000\r\n", "G729", 99, 8000, 8000, 1},
		{"100", "a=rtpmap:100 G726-16/8000\r\n", "G726-16", 100, 8000, 8000, 1},
		{"100", "a=rtpmap:100 G726-24/8000\r\n", "G726-24", 100, 8000, 8000, 1},
		{"100", "a=rtpmap:100 G726-32/8000\r\n", "G726-32", 100, 8000, 8000, 1},
		{"100", "a=rtpmap:100 G726-40/8000\r\n", "G726-40", 100, 8000, 8000, 1},
		{"100", "a=rtpmap:100 AAL2-G726-16/8000\r\n", "AAL2-G726-16", 100, 8000, 8000, 1},
		{"100", "a=rtpmap:100 AAL2-G726-24/8000\r\n", "AAL2-G726-24", 100, 8000, 8000, 1},
		{"100", "a=rtpmap:100 AAL2-G726-32/8000\r\n", "AAL2-G726-32", 100, 8000, 8000, 1},
		{"100", "a=rtpmap:100 AAL2-G726-40/8000\r\n", "AAL2-G726-40", 100, 8000, 8000, 1},
		{"10", "", "L16", 10, 44100, 44100, 2},
		{"11", "", "L16", 11, 44100, 44100, 1},
		{"110", "a=rtpmap:110 L16/16000/1\r\n", "L16", 110, 16000, 16000, 1},
		{"110", "a=rtpmap:110 L16/48000/2\r\n", "L16", 110, 48000, 48000, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.payloads, func(t *testing.T) {
			value, err := ParseSDP([]byte(codecSDP(tc.payloads, tc.attributes)), mediaConfig())
			if err != nil {
				t.Fatal(err)
			}
			if value.Payload != tc.payload || value.Codec.Name != tc.name || value.Codec.SampleRate != tc.sample || value.Codec.RTPClockRate != tc.clock || value.Codec.Channels != tc.channels || value.Codec.PTimeMS != 20 {
				t.Fatalf("unexpected mapping: %+v", value)
			}
			round, err := ParseSDP(RenderSDP(1, "127.0.0.1", 31000, 31001, value), mediaConfig())
			if err != nil {
				t.Fatal(err)
			}
			if round.Codec != value.Codec || round.Payload != value.Payload {
				t.Fatalf("round trip changed codec: %+v -> %+v", value, round)
			}
		})
	}
}

// TestCodecMappingAndFMTPRejections 确认格式约束不能被忽略、编号不能重用，内部 PCM 名称不能伪装成线路编码。
func TestCodecMappingAndFMTPRejections(t *testing.T) {
	cases := []struct{ name, payloads, attributes string }{
		{"重复主载荷", "0 0", ""},
		{"未映射动态载荷", "111", ""},
		{"静态时钟冲突", "9", "a=rtpmap:9 G722/16000\r\n"},
		{"静态编码冲突", "0", "a=rtpmap:0 Opus/48000/2\r\n"},
		{"静态声道冲突", "10", "a=rtpmap:10 L16/44100/1\r\n"},
		{"保留编号被重用", "35", "a=rtpmap:35 Opus/48000/2\r\n"},
		{"Opus声明单声道", "111", "a=rtpmap:111 Opus/48000/1\r\n"},
		{"Opus错误时钟", "111", "a=rtpmap:111 Opus/16000/2\r\n"},
		{"重复格式参数", "111", "a=rtpmap:111 Opus/48000/2\r\na=fmtp:111 stereo=0;stereo=1\r\n"},
		{"未知格式参数", "111", "a=rtpmap:111 Opus/48000/2\r\na=fmtp:111 imaginary=1\r\n"},
		{"无效格式参数", "111", "a=rtpmap:111 Opus/48000/2\r\na=fmtp:111 useinbandfec=2\r\n"},
		{"重复fmtp属性", "0", "a=fmtp:0 x=1\r\na=fmtp:0 y=1\r\n"},
		{"无归属映射", "0", "a=rtpmap:111 Opus/48000/2\r\n"},
		{"无归属参数", "0", "a=fmtp:111 stereo=0\r\n"},
		{"G711不支持的参数", "0", "a=fmtp:0 mode=other\r\n"},
		{"G729错误AnnexB", "18", "a=fmtp:18 annexb=1\r\n"},
		{"G726打包参数不能猜测", "100", "a=rtpmap:100 G726-32/8000\r\na=fmtp:100 packing=aal2\r\n"},
		{"S16LE非线路L16", "110", "a=rtpmap:110 S16LE/16000/1\r\n"},
		{"L16声道越界", "110", "a=rtpmap:110 L16/48000/3\r\n"},
		{"L16分包越界", "110", "a=rtpmap:110 L16/48000/2\r\na=ptime:60\r\n"},
		{"最大分包约束", "0", "a=ptime:20\r\na=maxptime:10\r\n"},
		{"重复分包属性", "0", "a=ptime:20\r\na=ptime:30\r\n"},
		{"事件反向范围", "0 101", "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 16-0\r\n"},
		{"事件越界", "0 101", "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-256\r\n"},
		{"AMR未实现", "96", "a=rtpmap:96 AMR/8000\r\na=fmtp:96 octet-align=1\r\n"},
		{"EVS未实现", "96", "a=rtpmap:96 EVS/16000\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSDP([]byte(codecSDP(tc.payloads, tc.attributes)), mediaConfig()); err == nil {
				t.Fatal("accepted invalid or unsupported codec description")
			}
		})
	}
}

// TestAuxiliaryPayloadClockAndEvents 检查事件时钟独立传递、同钟候选优先和事件集合不被扩大。
func TestAuxiliaryPayloadClockAndEvents(t *testing.T) {
	text := codecSDP("111 101 102 13 103", "a=rtpmap:111 opus/48000/2\r\na=rtpmap:101 telephone-event/8000\r\na=rtpmap:102 telephone-event/48000\r\na=fmtp:102 1-3,5,16,32-35\r\na=rtpmap:103 CN/48000\r\n")
	value, err := ParseSDP([]byte(text), mediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if value.DTMF == nil || *value.DTMF != 102 || value.DTMFClockRate != 48000 || value.DTMFEvents != "1-3,5,16" || value.CN == nil || *value.CN != 103 || value.CNClockRate != 48000 {
		t.Fatalf("wrong auxiliary negotiation: %+v", value)
	}
	round, err := ParseSDP(RenderSDP(1, "127.0.0.1", 31000, 31001, value), mediaConfig())
	if err != nil || round.DTMFEvents != value.DTMFEvents || round.DTMFClockRate != 48000 || round.CNClockRate != 48000 {
		t.Fatal(round, err)
	}
	value, err = ParseSDP([]byte(codecSDP("111 101 13", "a=rtpmap:111 opus/48000/2\r\na=rtpmap:101 telephone-event/8000\r\n")), mediaConfig())
	if err != nil || value.DTMFClockRate != 8000 || value.DTMFEvents != "0-15" || value.CN != nil {
		t.Fatal(value, err)
	}
	// 未实现的辅事件不妨碍主音频；它们不会被渲染成默认 DTMF 集合。
	value, err = ParseSDP([]byte(codecSDP("0 101", "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 32-35\r\n")), mediaConfig())
	if err != nil || value.DTMF != nil {
		t.Fatal(value, err)
	}
}

// TestAnswerNegotiationRejectsImplicitTransforms 核对动态 PT、时钟和打包格式的变化不会伪接受为透传。
func TestAnswerNegotiationRejectsImplicitTransforms(t *testing.T) {
	parse := func(payloads, attrs string) SDP {
		t.Helper()
		s, e := ParseSDP([]byte(codecSDP(payloads, attrs)), mediaConfig())
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	opus := parse("111 101", "a=rtpmap:111 opus/48000/2\r\na=fmtp:111 stereo=1;useinbandfec=1\r\na=rtpmap:101 telephone-event/48000\r\na=fmtp:101 0-16\r\n")
	answer := parse("111 101", "a=rtpmap:111 opus/48000/2\r\na=fmtp:111 stereo=0;useinbandfec=0\r\na=rtpmap:101 telephone-event/48000\r\na=fmtp:101 0-9\r\n")
	negotiated, err := NegotiateAnswer(opus, answer)
	if err != nil || negotiated.Codec.FMTP != "stereo=0;useinbandfec=0" || negotiated.DTMFEvents != "0-9" {
		t.Fatal(negotiated, err)
	}
	changed := answer
	changed.Payload = 112
	if _, err = NegotiateAnswer(opus, changed); err == nil {
		t.Fatal("accepted unimplemented PT remapping")
	}
	changed = answer
	changed.DTMFClockRate = 8000
	if _, err = NegotiateAnswer(opus, changed); err == nil {
		t.Fatal("accepted changed event clock")
	}
	g726 := parse("100", "a=rtpmap:100 G726-32/8000\r\n")
	aal2 := parse("100", "a=rtpmap:100 AAL2-G726-32/8000\r\n")
	if _, err = NegotiateAnswer(g726, aal2); err == nil {
		t.Fatal("accepted different G726 packing")
	}
	yes := parse("18", "")
	no := parse("18", "a=fmtp:18 annexb=no\r\n")
	if _, err = NegotiateAnswer(yes, no); err != nil {
		t.Fatal(err)
	}
	if _, err = NegotiateAnswer(no, yes); err == nil {
		t.Fatal("answer enabled disabled Annex B")
	}
	if _, err = NegotiateAnswer(parse("0", ""), parse("8", "")); err == nil {
		t.Fatal("accepted implicit G711 transcoding")
	}
}

// TestUnsupportedPacketDurationOnlySkipsThatCandidate 验证某个已知编码不支持当前分包间隔时，不连带拒绝其他可用主编码。
// 无法实现的候选应跳过；重复编号、静态映射冲突及非法 fmtp 仍是整个描述的错误。
func TestUnsupportedPacketDurationOnlySkipsThatCandidate(t *testing.T) {
	for _, payloads := range []string{"0 110", "110 0"} {
		t.Run(payloads, func(t *testing.T) {
			body := codecSDP(payloads, "a=rtpmap:110 L16/48000/2\r\na=ptime:40\r\n")
			value, err := ParseSDP([]byte(body), mediaConfig())
			if err != nil {
				t.Fatalf("可用 PCMU/40ms 被未选中 L16 候选连带拒绝：%v", err)
			}
			if value.Payload != 0 || value.Codec.Name != "PCMU" || value.PTime != 40 {
				t.Fatalf("未选择可用编码：%+v", value)
			}
		})
	}
	// 即使某候选分包不适用，其声明本身也不能含冲突或未知格式参数。
	invalid := codecSDP("0 110", "a=rtpmap:110 L16/48000/2\r\na=fmtp:110 byte-order=little\r\na=ptime:40\r\n")
	if _, err := ParseSDP([]byte(invalid), mediaConfig()); err == nil {
		t.Fatal("候选跳过掩盖了非法 L16 参数")
	}
	// 没有任何可用候选时仍明确拒绝，不能忽略分包约束并接受 L16。
	unsupported := codecSDP("110", "a=rtpmap:110 L16/48000/2\r\na=ptime:40\r\n")
	if _, err := ParseSDP([]byte(unsupported), mediaConfig()); err == nil {
		t.Fatal("接受了本实现无法转发的 L16 分包")
	}
}
