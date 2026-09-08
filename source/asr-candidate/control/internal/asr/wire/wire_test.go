package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"
)

const testToken = "123456789abcdef0123456789abcdef0"
const testNow uint64 = 10_000_000_000

func testHello() Hello {
	return Hello{Protocol: "ASR1", StreamToken: testToken, UUID: "fixture-01", SubscriptionID: 19, SourceRate: 8000, TargetRate: 8000, Channels: 1, Format: "s16le", FrameMS: 20, ClockDomain: 2, PLCPolicy: "metadata", GapPolicy: "explicit"}
}
func testAudio(rate uint32) Audio {
	token, _ := TokenBytes(testToken)
	pcm := make([]byte, rate/50*2)
	for i := 0; i < len(pcm)/2; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16((i*499+274)%60001-30000)))
	}
	a := Audio{StreamToken: token, EventSeq: 2, SourceGeneration: 1, SourceSegment: 1, RTPSequence: 65538, RTPTimestamp: 4294967616, LowerNS: testNow, ExpiresNS: testNow + LifetimeNS, SSRC: 0x12345678, SampleRate: rate, RTPClockRate: 8000, Flags: 15, ClockDomain: 2, SampleCount: uint16(rate / 50), Origin: 1, PCM: pcm}
	if rate == 16000 {
		a.FilterDelayNS = 3937500
	}
	return a
}
func testMarker() Marker {
	return Marker{StreamToken: testToken, RXKind: 7, EventSeq: 1, SourceGeneration: 1, SourceSegment: 1, ClockDomain: 2, LowerNS: U64(testNow), ExpiresNS: U64(testNow + LifetimeNS), Flags: 16, SampleRate: 8000, RTPClockRate: 8000, Boundary: 1}
}

func TestAudioGoldenOffsetsAndPCM(t *testing.T) {
	a := testAudio(8000)
	var buf [760]byte
	body, err := EncodeAudio(buf[:], a)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 432 || binary.LittleEndian.Uint64(body[16:]) != 2 || binary.LittleEndian.Uint64(body[40:]) != 65538 || binary.LittleEndian.Uint64(body[48:]) != 4294967616 || binary.LittleEndian.Uint64(body[64:]) != testNow || binary.LittleEndian.Uint64(body[72:]) != testNow+LifetimeNS || binary.LittleEndian.Uint16(body[104:]) != 320 {
		t.Fatal("固定头偏移不匹配")
	}
	want := []int16{-29726, -29227, -28728, -28229}
	for i, v := range want {
		if int16(binary.LittleEndian.Uint16(body[112+i*2:])) != v {
			t.Fatal("独立非静音样本不匹配")
		}
	}
	out, err := ParseAudio(body)
	if err != nil || out.EventSeq != a.EventSeq || !bytes.Equal(out.PCM, a.PCM) {
		t.Fatalf("解析失败: %+v %v", out, err)
	}
	whole, err := Encode(buf[:], Message{KindAudio, body})
	if err != nil || len(whole) != 440 || binary.LittleEndian.Uint32(whole) != 436 {
		t.Fatal("同一缓冲重叠组装失败", err)
	}
	var read [MaxWireSize]byte
	m, err := Read(bytes.NewReader(whole), &read)
	if err != nil {
		t.Fatal(err)
	}
	out, err = ParseAudio(m.Body)
	if err != nil || !bytes.Equal(out.PCM, a.PCM) {
		t.Fatal("重叠组装损坏正文", err)
	}
}

func TestAudio16kFormatAndNoAllocation(t *testing.T) {
	a := testAudio(16000)
	var buf [760]byte
	if a.RTPClockRate != 8000 || a.SampleCount != 320 {
		t.Fatal("格式错误")
	}
	body, err := EncodeAudio(buf[:], a)
	if err != nil || len(body) != 752 {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(100, func() {
		body, err := EncodeAudio(buf[:], a)
		if err != nil {
			panic(err)
		}
		if _, err = ParseAudio(body); err != nil {
			panic(err)
		}
	}); n != 0 {
		t.Fatalf("二进制帧路径分配 %v", n)
	}
}

func TestAudioRejectsMalformedFields(t *testing.T) {
	cases := map[string]func(*Audio){"origin": func(a *Audio) { a.Origin = 2 }, "flags": func(a *Audio) { a.Flags = 0 }, "reserved_flags": func(a *Audio) { a.Flags |= 1024 }, "token": func(a *Audio) { a.StreamToken = [16]byte{} }, "samples": func(a *Audio) { a.SampleCount = 159 }, "body": func(a *Audio) { a.PCM = a.PCM[:318] }, "rate": func(a *Audio) { a.RTPClockRate = 16000 }, "delay": func(a *Audio) { a.FilterDelayNS = 1 }, "clock": func(a *Audio) { a.ClockDomain = 3 }, "renew": func(a *Audio) { a.ExpiresNS++ }, "lower_overflow": func(a *Audio) { a.LowerNS = math.MaxUint64 }, "media_overflow": func(a *Audio) { a.MediaNS = math.MaxUint64 }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			a := testAudio(8000)
			change(&a)
			if a.Validate() == nil {
				t.Fatal("非法帧被接受")
			}
		})
	}
	var buf [760]byte
	body, _ := EncodeAudio(buf[:], testAudio(8000))
	body[106] = 1
	if _, err := ParseAudio(body); err == nil {
		t.Fatal("保留位未拒绝")
	}
}

func TestFreshnessExactBoundaries(t *testing.T) {
	a := testAudio(8000)
	if err := a.CheckFreshAt(a.ExpiresNS-1, 2); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		now    uint64
		domain uint16
	}{{testNow - 1, 2}, {a.ExpiresNS, 2}, {testNow, 1}, {testNow, 0}} {
		if a.CheckFreshAt(x.now, x.domain) == nil {
			t.Fatal("非法时钟通过", x)
		}
	}
	a.LowerNS = math.MaxUint64 - LifetimeNS
	a.ExpiresNS = math.MaxUint64
	if err := a.CheckFreshAt(math.MaxUint64-1, 2); err != nil {
		t.Fatal("最后合法u64期限被错误拒绝", err)
	}
}

type oneByteReader struct{ data []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}
func TestReadFragmentationAndTwoMessages(t *testing.T) {
	m, _ := JSONMessage(KindHello, testHello())
	var buf [MaxWireSize]byte
	raw, _ := Encode(buf[:], m)
	both := append(append([]byte{}, raw...), raw...)
	r := &oneByteReader{both}
	var dst [MaxWireSize]byte
	for i := 0; i < 2; i++ {
		got, err := Read(r, &dst)
		if err != nil || got.Kind != KindHello {
			t.Fatal(err)
		}
		var h Hello
		if err := ParseJSON(got.Body, &h); err != nil || h != testHello() {
			t.Fatal("HELLO错乱", err)
		}
	}
	if _, err := Read(r, &dst); err != io.EOF {
		t.Fatal(err)
	}
}

func TestReadRejectsLengthBeforeBodyAndTruncatedEOF(t *testing.T) {
	var dst [MaxWireSize]byte
	for _, n := range []uint32{0, 3, 8193, math.MaxUint32} {
		var prefix [4]byte
		binary.LittleEndian.PutUint32(prefix[:], n)
		if _, err := Read(bytes.NewReader(prefix[:]), &dst); err == nil || err == io.EOF || err == io.ErrUnexpectedEOF {
			t.Fatal("未先验长度", n, err)
		}
	}
	m, _ := JSONMessage(KindHello, testHello())
	var b [MaxWireSize]byte
	raw, _ := Encode(b[:], m)
	for n := 1; n < len(raw); n++ {
		if _, err := Read(bytes.NewReader(raw[:n]), &dst); err == nil {
			t.Fatal("半帧被接受", n)
		}
	}
}

func TestReadRejectsPrefixAndShortEncodeBuffer(t *testing.T) {
	var dst [MaxWireSize]byte
	for _, p := range [][]byte{{4, 0, 0, 0, 0, 1, 0, 0}, {4, 0, 0, 0, 12, 1, 0, 0}, {4, 0, 0, 0, 1, 2, 0, 0}, {4, 0, 0, 0, 1, 1, 1, 0}} {
		if _, err := Read(bytes.NewReader(p), &dst); err == nil {
			t.Fatal("非法前缀")
		}
	}
	if _, err := Encode(nil, Message{KindHello, []byte("{}")}); err != io.ErrShortBuffer {
		t.Fatal(err)
	}
	if _, err := EncodeAudio(nil, testAudio(8000)); err != io.ErrShortBuffer {
		t.Fatal(err)
	}
	if _, err := Encode(dst[:], Message{KindHello, make([]byte, 8189)}); err == nil {
		t.Fatal("超长正文")
	}
}

func TestJSONExactFieldsTypesAndNull(t *testing.T) {
	m, _ := JSONMessage(KindHello, testHello())
	var base map[string]any
	if err := json.Unmarshal(m.Body, &base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"source_rate", "channels", "stream_token", "uuid", "subscription_id"} {
		t.Run(name+"_null", func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range base {
				copy[k] = v
			}
			copy[name] = nil
			raw, _ := json.Marshal(copy)
			var h Hello
			if ParseJSON(raw, &h) == nil {
				t.Fatal("null 被接受")
			}
		})
	}
	for _, change := range []func(map[string]any){func(m map[string]any) { delete(m, "uuid") }, func(m map[string]any) { m["extra"] = 1 }, func(m map[string]any) { m["channels"] = true }, func(m map[string]any) { m["subscription_id"] = 19 }, func(m map[string]any) { m["subscription_id"] = "019" }, func(m map[string]any) { m["target_rate"] = 48000 }} {
		copy := map[string]any{}
		for k, v := range base {
			copy[k] = v
		}
		change(copy)
		raw, _ := json.Marshal(copy)
		var h Hello
		if ParseJSON(raw, &h) == nil {
			t.Fatal("非法字段被接受", string(raw))
		}
	}
}

func TestJSONDuplicateTrailingInvalidUnicode(t *testing.T) {
	m, _ := JSONMessage(KindHello, testHello())
	raw := string(m.Body)
	for _, bad := range []string{strings.Replace(raw, `"protocol":"ASR1"`, `"protocol":"ASR1","protocol":"ASR1"`, 1), raw + "{}", strings.Replace(raw, `"uuid":"fixture-01"`, `"uuid":"\ud800"`, 1), strings.Replace(raw, `"uuid":"fixture-01"`, `"uuid":"\udc00"`, 1), strings.Replace(raw, `"uuid":"fixture-01"`, "\"uuid\":\"\xff\"", 1), "null", "[]", "{\"x\":NaN}"} {
		var h Hello
		if ParseJSON([]byte(bad), &h) == nil {
			t.Fatal("宽松JSON通过", bad)
		}
	}
	good := strings.Replace(raw, `"uuid":"fixture-01"`, `"uuid":"\ud83d\ude00"`, 1)
	var h Hello
	if err := ParseJSON([]byte(good), &h); err != nil {
		t.Fatal("合法代理对被拒绝", err)
	}
}

func TestU64CanonicalAndToken(t *testing.T) {
	for _, raw := range []string{`1`, `true`, `null`, `""`, `"01"`, `"+1"`, `"-1"`, `"1.0"`, `"18446744073709551616"`, `"\u0031"`} {
		var n U64
		if json.Unmarshal([]byte(raw), &n) == nil {
			t.Fatal("u64误接收", raw)
		}
	}
	var n U64
	if err := json.Unmarshal([]byte(`"18446744073709551615"`), &n); err != nil || uint64(n) != math.MaxUint64 {
		t.Fatal(err)
	}
	for _, s := range []string{strings.Repeat("0", 32), strings.ToUpper(testToken), testToken[:31]} {
		if _, err := TokenBytes(s); err == nil {
			t.Fatal("token误接收")
		}
	}
}

func TestMarkerPreservesCNAndExpiredCNAuxiliary(t *testing.T) {
	m := testMarker()
	m.RXKind = 4
	m.Boundary = 0
	m.Flags = 0
	sid := "NRAg"
	m.SIDBase64 = &sid
	m.CNApplied = true
	msg, err := JSONMessage(KindMarker, m)
	if err != nil {
		t.Fatal(err)
	}
	var out Marker
	if err := ParseJSON(msg.Body, &out); err != nil || out.SIDBase64 == nil || *out.SIDBase64 != sid {
		t.Fatal(err)
	}
	m.RXKind = 6
	m.SIDBase64 = nil
	if err := m.Validate(); err != nil {
		t.Fatal("原RXS2过期CN元数据丢失", err)
	}
	duration := U64(160)
	m.KnownDurationTicks = &duration
	if m.Validate() == nil {
		t.Fatal("辅助过期不应伪造20ms")
	}
}

func TestMarkerPLCDurationAndPositionRules(t *testing.T) {
	m := testMarker()
	m.RXKind = 2
	m.Flags = 0
	m.Boundary = 0
	if m.Validate() == nil {
		t.Fatal("PLC缺少已知时长")
	}
	d := U64(160)
	m.KnownDurationTicks = &d
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Marker){func(m *Marker) { m.RTPSequence = 1 }, func(m *Marker) { m.ArrivalAgeMS = 1 }, func(m *Marker) { m.DiscardedPackets = 1 }, func(m *Marker) { m.Flags = 16 }, func(m *Marker) { m.Reason = 12 }} {
		copy := m
		change(&copy)
		if copy.Validate() == nil {
			t.Fatal("非法原始位置通过")
		}
	}
}

func TestGapSummaryCountersAndNestedJSON(t *testing.T) {
	m := Marker{StreamToken: testToken, RXKind: 10, EventSeq: 6, ClockDomain: 2, SampleRate: 8000, RTPClockRate: 8000, Reason: 3, Gap: &Gap{First: 2, Last: 6, Decoded: 2, PLC: 1, Reasons: 3}}
	msg, err := JSONMessage(KindMarker, m)
	if err != nil {
		t.Fatal(err)
	}
	var got Marker
	if err := ParseJSON(msg.Body, &got); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Marker){func(m *Marker) { m.LowerNS = 1 }, func(m *Marker) { m.SourceGeneration = 1 }, func(m *Marker) { m.Flags = 8 }, func(m *Marker) { m.Reason = 2 }, func(m *Marker) { m.EventSeq = 7 }} {
		copy := m
		change(&copy)
		if copy.Validate() == nil {
			t.Fatal("摘要伪音频通过")
		}
	}
	bad := bytes.Replace(msg.Body, []byte(`"first":"2"`), []byte(`"first":"2","first":"2"`), 1)
	if ParseJSON(bad, &got) == nil {
		t.Fatal("嵌套重复字段通过")
	}
	for _, g := range []Gap{{First: 2, Last: 1, Reasons: 1}, {First: 1, Last: 3, Decoded: 3, PLC: 1, Reasons: 1}, {First: 1, Last: math.MaxUint64, Decoded: math.MaxUint64, PLC: 1, Reasons: 1}} {
		if g.Validate() == nil {
			t.Fatal("gap越界通过")
		}
	}
}

func TestFinishAndResultEnvelopeValidation(t *testing.T) {
	f := Finish{StreamToken: testToken, LastEventSeq: 3, AudioFrames: 2, Samples: 320, Markers: 1}
	m, err := JSONMessage(KindFinish, f)
	if err != nil {
		t.Fatal(err)
	}
	var out Finish
	if err := ParseJSON(m.Body, &out); err != nil || out != f {
		t.Fatal(err)
	}
	f.Samples = 319
	if f.Validate() == nil {
		t.Fatal("样本计数非完整帧")
	}
	r := Result{StreamToken: testToken, ResultSeq: 1, UtteranceID: 1, Revision: 1, SourceGeneration: 1, SourceSegment: 1, Type: "partial", Text: "模拟", FirstEventSeq: 2, LastEventSeq: 3, EndMediaNS: 40000000, Coverage: "complete"}
	if _, err := JSONMessage(KindResult, r); err != nil {
		t.Fatal(err)
	}
	r.Text = strings.Repeat("x", 4097)
	if r.Validate() == nil {
		t.Fatal("结果文本超限")
	}
	r.Text = "ok"
	r.Revision = 0
	if r.Validate() == nil {
		t.Fatal("零修订通过")
	}
}

func TestMessageKindTypeMismatchAndErrorCode(t *testing.T) {
	if _, err := JSONMessage(KindAudio, testAudio(8000)); err == nil {
		t.Fatal("PCM进入JSON")
	}
	if _, err := JSONMessage(KindResult, testHello()); err == nil {
		t.Fatal("错误JSON类型")
	}
	for _, code := range []string{"", "A", "bad-code", strings.Repeat("a", 129)} {
		if (Error{testToken, code}).Validate() == nil {
			t.Fatal("错误code通过")
		}
	}
	var e Error
	for _, raw := range []string{`{"stream_token":"` + testToken + `","code":null}`, `{"stream_token":"` + testToken + `","code":""}`} {
		if ParseJSON([]byte(raw), &e) == nil {
			t.Fatal("null/空ERROR通过")
		}
	}
}

func TestPublicClockSameDomainMonotonic(t *testing.T) {
	now, domain, err := ClockNow()
	if err != nil {
		t.Fatal(err)
	}
	later, next, err := ClockNow()
	if err != nil || now == 0 || later < now || domain != next || !validDomain(domain) {
		t.Fatal("共享时钟异常", now, later, domain, next, err)
	}
}
