package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rustswitch/control/internal/asr/wire"
)

const fixtureToken = "123456789abcdef0123456789abcdef0"
const fixtureNow uint64 = 10_000_000_000

func fixtureHello(rate uint32) wire.Hello {
	return wire.Hello{Protocol: "ASR1", StreamToken: fixtureToken, UUID: "asr-mock-unit", SubscriptionID: 19, SourceRate: 8000, TargetRate: rate, Channels: 1, Format: "s16le", FrameMS: 20, ClockDomain: 2, PLCPolicy: "metadata", GapPolicy: "explicit"}
}
func fixtureMarker(kind uint8, seq uint64) wire.Marker {
	m := wire.Marker{StreamToken: fixtureToken, RXKind: kind, EventSeq: wire.U64(seq), SourceGeneration: 1, SourceSegment: 1, ClockDomain: 2, LowerNS: wire.U64(fixtureNow), ExpiresNS: wire.U64(fixtureNow + wire.LifetimeNS), SampleRate: 8000, RTPClockRate: 8000}
	if kind == 7 {
		m.Flags = 16
		m.Boundary = 1
	}
	if kind == 2 {
		d := wire.U64(160)
		m.KnownDurationTicks = &d
	}
	if kind == 4 {
		s := "NRAg"
		m.SIDBase64 = &s
		m.CNApplied = true
	}
	if kind == 10 || kind == 11 || kind == 12 {
		m.SourceGeneration = 0
		m.SourceSegment = 0
		m.LowerNS = 0
		m.ExpiresNS = 0
	}
	if kind == 12 {
		m.Reason = 1
	}
	if kind == 9 {
		m.Reason = 8 // 真实观察队列失败只使用overflow/sequence exhausted，不能伪造reason0。
	}
	return m
}
func fixtureAudio(seq uint64, rate uint32) wire.Audio {
	token, _ := wire.TokenBytes(fixtureToken)
	pcm := make([]byte, rate/50*2)
	for i := 0; i < len(pcm)/2; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(i*127-17000)))
	}
	a := wire.Audio{StreamToken: token, EventSeq: seq, SourceGeneration: 1, SourceSegment: 1, RTPSequence: 65536 + seq, RTPTimestamp: 4294967296 + seq*160, MediaNS: (seq - 2) * 20_000_000, LowerNS: fixtureNow, ExpiresNS: fixtureNow + wire.LifetimeNS, SSRC: 17, SampleRate: rate, RTPClockRate: 8000, Flags: 15, ClockDomain: 2, SampleCount: uint16(rate / 50), Origin: 1, PCM: pcm}
	if rate == 16000 {
		a.FilterDelayNS = 3937500
	}
	return a
}
func message(t *testing.T, kind wire.Kind, v any) wire.Message {
	t.Helper()
	m, err := wire.JSONMessage(kind, v)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func audioMessage(t *testing.T, a wire.Audio) wire.Message {
	t.Helper()
	b := make([]byte, 752)
	body, err := wire.EncodeAudio(b, a)
	if err != nil {
		t.Fatal(err)
	}
	return wire.Message{Kind: wire.KindAudio, Body: body}
}
func prepared(t *testing.T, rate uint32) *provider {
	t.Helper()
	p := newProvider(2)
	if _, err := p.accept(message(t, wire.KindHello, fixtureHello(rate)), fixtureNow); err != nil {
		t.Fatal(err)
	}
	if _, err := p.accept(message(t, wire.KindMarker, fixtureMarker(7, 1)), fixtureNow); err != nil {
		t.Fatal(err)
	}
	return p
}
func mustAccept(t *testing.T, p *provider, m wire.Message) []wire.Message {
	t.Helper()
	out, err := p.accept(m, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProviderRealBytesPartialFinalAndExactPrefix(t *testing.T) {
	for _, rate := range []uint32{8000, 16000} {
		t.Run(map[uint32]string{8000: "native8k", 16000: "format16k"}[rate], func(t *testing.T) {
			p := prepared(t, rate)
			a, b := fixtureAudio(2, rate), fixtureAudio(3, rate)
			if out := mustAccept(t, p, audioMessage(t, a)); len(out) != 0 {
				t.Fatal("提前partial")
			}
			out := mustAccept(t, p, audioMessage(t, b))
			var partial wire.Result
			if len(out) != 1 || wire.ParseJSON(out[0].Body, &partial) != nil || partial.Type != "partial" || p.state != "streaming" {
				t.Fatal("上传中没有合法partial")
			}
			sum := sha256.Sum256(append(append([]byte{}, a.PCM...), b.PCM...))
			if partial.Text != "mock-sha256:"+hex.EncodeToString(sum[:]) {
				t.Fatal("不是实际PCM摘要")
			}
			out = mustAccept(t, p, message(t, wire.KindFinish, p.counters()))
			if len(out) != 2 || out[1].Kind != wire.KindDone {
				t.Fatal("缺少final/DONE")
			}
			var final wire.Result
			var done wire.Finish
			if wire.ParseJSON(out[0].Body, &final) != nil || wire.ParseJSON(out[1].Body, &done) != nil || final.Revision != 2 || final.Text != partial.Text || done != p.counters() {
				t.Fatal("final或实际前缀错误")
			}
			if err := p.eof(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderMarkersNeverBecomeSpeechOrSilence(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	for i, kind := range []uint8{2, 3, 4, 5, 6, 13} {
		mustAccept(t, p, message(t, wire.KindMarker, fixtureMarker(kind, uint64(i+3))))
		if p.samples != 160 || p.audioFrames != 1 {
			t.Fatal("marker伪造了音频")
		}
	}
	a := fixtureAudio(9, 8000)
	out := mustAccept(t, p, audioMessage(t, a))
	var r wire.Result
	if wire.ParseJSON(out[0].Body, &r) != nil || r.Coverage != "discontinuous" {
		t.Fatal("缺口被压缩为完整音频")
	}
}

func TestProviderAuxiliarySequenceKeepsContinuousAudio(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	mustAccept(t, p, message(t, wire.KindMarker, fixtureMarker(13, 3)))
	a := fixtureAudio(4, 8000)
	a.MediaNS = 20_000_000
	out := mustAccept(t, p, audioMessage(t, a))
	var r wire.Result
	if wire.ParseJSON(out[0].Body, &r) != nil || r.Coverage != "complete" {
		t.Fatal("DTMF序号被误当音频缺口")
	}
}

func TestProviderZeroAudioAndEndAreNotInventedFinal(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, message(t, wire.KindMarker, fixtureMarker(4, 2)))
	mustAccept(t, p, message(t, wire.KindMarker, fixtureMarker(11, 2)))
	if p.eof() == nil {
		t.Fatal("End误当DONE")
	}
	out := mustAccept(t, p, message(t, wire.KindFinish, p.counters()))
	if len(out) != 1 || out[0].Kind != wire.KindDone || p.samples != 0 {
		t.Fatal("捏造空final")
	}
}

func TestProviderStaleCompleteFrameDoesNotConsume(t *testing.T) {
	p := prepared(t, 8000)
	if _, err := p.accept(audioMessage(t, fixtureAudio(2, 8000)), fixtureNow+120_000_000); err == nil {
		t.Fatal("过期帧被消费")
	}
	if p.samples != 0 || p.audioFrames != 0 || p.state != "failed" {
		t.Fatal("过期输入改变消费计数")
	}
	if _, err := p.accept(message(t, wire.KindFinish, p.counters()), fixtureNow); err == nil {
		t.Fatal("失败流复活")
	}
}

func TestProviderSameSourceMediaAndExpandedRTPDoNotReverseAfterCN(t *testing.T) {
	for _, change := range []func(*wire.Audio){func(a *wire.Audio) { a.MediaNS = 0 }, func(a *wire.Audio) { a.RTPSequence = 4 }, func(a *wire.Audio) { a.RTPTimestamp = 160 }} {
		p := prepared(t, 8000)
		mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
		mustAccept(t, p, message(t, wire.KindMarker, fixtureMarker(4, 3)))
		a := fixtureAudio(4, 8000)
		change(&a)
		if _, err := p.accept(audioMessage(t, a), fixtureNow); err == nil {
			t.Fatal("CN清理掩盖了倒退")
		}
	}
}

func TestProviderMidCallFirstDecodedNeedsNoReplayedBoundary(t *testing.T) {
	for _, flag := range []uint16{15, 31} {
		t.Run(map[uint16]string{15: "without_new_segment", 31: "with_new_segment"}[flag], func(t *testing.T) {
			p := newProvider(2)
			mustAccept(t, p, message(t, wire.KindHello, fixtureHello(8000)))
			a := fixtureAudio(2, 8000)
			a.EventSeq = 1
			a.SourceGeneration = 7
			a.SourceSegment = 9
			a.Flags = flag
			mustAccept(t, p, audioMessage(t, a))
			if p.generation != 7 || p.segment != 9 || p.markers != 0 || p.samples != 160 {
				t.Fatal("中途订阅伪造边界或未初始化真实音频")
			}
		})
	}
}

func TestProviderInitialMarkersKeepBoundsButCreateNoAudioSource(t *testing.T) {
	p := newProvider(2)
	mustAccept(t, p, message(t, wire.KindHello, fixtureHello(8000)))
	plc := fixtureMarker(2, 1)
	plc.SourceGeneration = 7
	plc.SourceSegment = 9
	plc.Flags = 8
	plc.MediaNS = 0
	mustAccept(t, p, message(t, wire.KindMarker, plc))
	if p.generation != 0 || p.samples != 0 || p.firstAudio != 0 || p.minimumMedia != 20_000_000 {
		t.Fatal("初始PLC建立了伪音频源或丢失下界")
	}
	a := fixtureAudio(2, 8000)
	a.SourceGeneration = 7
	a.SourceSegment = 9
	a.MediaNS = 20_000_000
	mustAccept(t, p, audioMessage(t, a))
	if p.samples != 160 || p.generation != 7 {
		t.Fatal("首个实际Decoded未建立来源")
	}
}

func TestProviderSourceEndedMayNamePendingHigherSegmentOnlyToEnd(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	end := fixtureMarker(7, 3)
	end.Boundary = 6
	end.SourceSegment = 4
	mustAccept(t, p, message(t, wire.KindMarker, end))
	if !p.ended || p.segment != 1 || p.firstAudio != 0 {
		t.Fatal("SourceEnded把待播段注册成了新音频源")
	}
}

func TestProviderFirstObservationEndedOnlyBlocksOldGeneration(t *testing.T) {
	p := newProvider(2)
	mustAccept(t, p, message(t, wire.KindHello, fixtureHello(8000)))
	end := fixtureMarker(7, 1)
	end.Boundary = 6
	end.SourceGeneration = 9
	end.SourceSegment = 7
	mustAccept(t, p, message(t, wire.KindMarker, end))
	if !p.ended || p.samples != 0 || p.firstAudio != 0 {
		t.Fatal("首次源结束被伪造成音频")
	}
	a := fixtureAudio(2, 8000)
	a.SourceGeneration = 9
	a.SourceSegment = 8
	a.Flags |= 16
	if _, err := p.accept(audioMessage(t, a), fixtureNow); err == nil {
		t.Fatal("结束的初始generation复活")
	}
}

func TestProviderSegmentZeroThenFirstDecodedNewSegment(t *testing.T) {
	p := newProvider(2)
	mustAccept(t, p, message(t, wire.KindHello, fixtureHello(8000)))
	boundary := fixtureMarker(7, 1)
	boundary.SourceSegment = 0
	mustAccept(t, p, message(t, wire.KindMarker, boundary))
	cn := fixtureMarker(4, 2)
	cn.SourceSegment = 0
	mustAccept(t, p, message(t, wire.KindMarker, cn))
	a := fixtureAudio(3, 8000)
	a.Flags |= 16
	a.MediaNS = 0
	mustAccept(t, p, audioMessage(t, a))
	if p.segment != 1 || p.samples != 160 {
		t.Fatal("真实首段被拒绝")
	}
}

func TestProviderInitialSameSegmentNewFlagAndLaterResume(t *testing.T) {
	p := prepared(t, 8000)
	a := fixtureAudio(2, 8000)
	a.Flags |= 16
	mustAccept(t, p, audioMessage(t, a))
	missing := fixtureMarker(3, 3)
	missing.Flags = 64
	mustAccept(t, p, message(t, wire.KindMarker, missing))
	if !p.suspended || p.firstAudio != 0 {
		t.Fatal("suspended_after未撤销开放utterance")
	}
	next := fixtureAudio(4, 8000)
	next.Flags |= 16
	next.SourceSegment = 3
	next.RTPTimestamp = 160
	mustAccept(t, p, audioMessage(t, next))
	if p.suspended || p.segment != 3 || p.utterance != 2 {
		t.Fatal("NewSegment未恢复或延续旧utterance")
	}
}

func TestProviderSourceEndedUsesCurrentTupleAndGlobalMediaRemains(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	end := fixtureMarker(7, 3)
	end.Boundary = 6
	mustAccept(t, p, message(t, wire.KindMarker, end))
	if !p.suspended {
		t.Fatal("SourceEnded未挂起")
	}
	next := fixtureMarker(7, 4)
	next.SourceGeneration = 2
	next.SourceSegment = 0
	next.Boundary = 2
	mustAccept(t, p, message(t, wire.KindMarker, next))
	a := fixtureAudio(5, 8000)
	a.SourceGeneration = 2
	a.SourceSegment = 1
	a.Flags |= 16
	a.MediaNS = 0
	if _, err := p.accept(audioMessage(t, a), fixtureNow); err == nil {
		t.Fatal("新源重置了Rust会话媒体下界")
	}
}

func TestProviderEndedGenerationCannotResumeAsOrdinarySuspension(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	end := fixtureMarker(7, 3)
	end.Boundary = 6
	mustAccept(t, p, message(t, wire.KindMarker, end))
	a := fixtureAudio(4, 8000)
	a.SourceSegment = 2
	a.Flags |= 16
	if _, err := p.accept(audioMessage(t, a), fixtureNow); err == nil {
		t.Fatal("已结束generation不能仅凭更高语音段恢复")
	}
}

func TestProviderExpiredAuxiliaryWithoutCNAppliedIsNotAudioGap(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	mustAccept(t, p, message(t, wire.KindMarker, fixtureMarker(6, 3)))
	a := fixtureAudio(4, 8000)
	a.MediaNS = 20_000_000
	out := mustAccept(t, p, audioMessage(t, a))
	var result wire.Result
	if wire.ParseJSON(out[0].Body, &result) != nil || result.Coverage != "complete" {
		t.Fatal("未知辅助期限错误宣告音频缺口")
	}
}

func TestProviderCannotSkipGenerationAndOldAuxiliaryCannotChangeSource(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	expired := fixtureMarker(5, 3)
	expired.SourceSegment = 5
	mustAccept(t, p, message(t, wire.KindMarker, expired))
	if p.segment != 1 {
		t.Fatal("过期首帧擅自切段")
	}
	aux := fixtureMarker(13, 4)
	aux.SourceSegment = 0
	mustAccept(t, p, message(t, wire.KindMarker, aux))
	if p.segment != 1 {
		t.Fatal("旧辅助槽擅自换段")
	}
	a := fixtureAudio(5, 8000)
	a.SourceGeneration = 2
	a.Flags |= 16
	if _, err := p.accept(audioMessage(t, a), fixtureNow); err == nil {
		t.Fatal("仅Decoded跳过generation边界")
	}
}

func TestProviderPLCKeepsMediaLowerBoundWithoutSubmittingPCM(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	plc := fixtureMarker(2, 3)
	plc.Flags = 8
	plc.MediaNS = 20_000_000
	mustAccept(t, p, message(t, wire.KindMarker, plc))
	a := fixtureAudio(4, 8000)
	a.MediaNS = 20_000_000
	if _, err := p.accept(audioMessage(t, a), fixtureNow); err == nil {
		t.Fatal("Decoded与PLC区间重叠")
	}
	if p.samples != 160 {
		t.Fatal("PLC当真实音频上传")
	}
}

func TestProviderGapMustMatchAndNeverNormalizesTime(t *testing.T) {
	p := prepared(t, 8000)
	g := fixtureMarker(10, 5)
	g.Gap = &wire.Gap{First: 2, Last: 5, Decoded: 2, PLC: 1, Reasons: 3}
	g.Reason = 3
	mustAccept(t, p, message(t, wire.KindMarker, g))
	a := fixtureAudio(6, 8000)
	a.MediaNS = 900_000_000
	mustAccept(t, p, audioMessage(t, a))
	if p.firstMedia != 900_000_000 || p.samples != 160 {
		t.Fatal("gap被补静音或压缩")
	}
	p = prepared(t, 8000)
	g.Gap.First = 3
	if _, err := p.accept(message(t, wire.KindMarker, g), fixtureNow); err == nil {
		t.Fatal("gap不接续却通过")
	}
}

func TestProviderWrongFinishCancelAndObservationFailure(t *testing.T) {
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, fixtureAudio(2, 8000)))
	f := p.counters()
	f.AudioFrames = 0
	f.Samples = 0
	if _, err := p.accept(message(t, wire.KindFinish, f), fixtureNow); err == nil {
		t.Fatal("FINISH虚报消费前缀")
	}
	p = prepared(t, 8000)
	mustAccept(t, p, message(t, wire.KindCancel, wire.Cancel{StreamToken: fixtureToken, Reason: "cancelled"}))
	if p.state != "cancelled" || p.eof() != nil {
		t.Fatal("取消状态错误")
	}
	for _, kind := range []uint8{9, 12} {
		p = prepared(t, 8000)
		seq := uint64(2)
		if kind == 12 {
			seq = 1
		}
		if _, err := p.accept(message(t, wire.KindMarker, fixtureMarker(kind, seq)), fixtureNow); err == nil || p.state != "failed" {
			t.Fatal("观察失败假正常结束")
		}
	}
}

func TestProviderRejectsInputBeforeHelloAndTerminalReuse(t *testing.T) {
	p := newProvider(2)
	if _, err := p.accept(audioMessage(t, fixtureAudio(2, 8000)), fixtureNow); err == nil {
		t.Fatal("首消息非HELLO")
	}
	if _, err := p.accept(message(t, wire.KindHello, fixtureHello(8000)), fixtureNow); err == nil {
		t.Fatal("错误后复活")
	}
}

func TestMockOptionsRejectExistingOrNonPrivatePaths(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "asr-mock-unit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	o := options{listen: filepath.Join(dir, "asr.sock"), artifacts: filepath.Join(dir, "evidence"), fault: "none", maxConnections: 2, maxTotal: 8, maxBytes: 65536, perConnectionBytes: 32768, faultAfter: 1, delay: 150 * time.Millisecond}
	if err := o.validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.listen, []byte("owner"), 0600); err != nil {
		t.Fatal(err)
	}
	if o.validate() == nil {
		t.Fatal("删除既存路径")
	}
	if b, _ := os.ReadFile(o.listen); string(b) != "owner" {
		t.Fatal("其他对象被修改")
	}
	os.Remove(o.listen)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if o.validate() == nil {
		t.Fatal("非私有路径通过")
	}
	os.Chmod(dir, 0700)
	o.maxConnections = 65
	if o.validate() == nil {
		t.Fatal("容量越界")
	}
}

func TestEvidenceIsBoundedAndKeepsRawPCM(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "one")
	budget := &evidenceBudget{maximum: 65536}
	e, err := newEvidence(dir, nil, budget, 32768)
	if err != nil {
		t.Fatal(err)
	}
	a := fixtureAudio(2, 8000)
	if err := e.savePCM(a.PCM); err != nil {
		t.Fatal(err)
	}
	if err := e.event("offline-unit", map[string]any{"network_started": false}); err != nil {
		t.Fatal(err)
	}
	p := prepared(t, 8000)
	mustAccept(t, p, audioMessage(t, a))
	if err := e.finish(p, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "consumed.pcm"))
	if err != nil || !bytes.Equal(raw, a.PCM) {
		t.Fatal("样本原件不匹配", err)
	}
	if _, err := newEvidence(dir, nil, budget, 32768); err == nil {
		t.Fatal("覆盖旧目录")
	}
	if err := budget.reserve(65536); !errors.Is(err, errEvidence) {
		t.Fatal("总预算无界")
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
func TestWriteAllRejectsZeroProgressAndMutationsRemainExplicit(t *testing.T) {
	if err := writeAll(zeroWriter{}, []byte("x")); err != io.ErrNoProgress {
		t.Fatal(err)
	}
	f := wire.Finish{StreamToken: fixtureToken, LastEventSeq: 2, AudioFrames: 1, Samples: 160, Markers: 1}
	m := message(t, wire.KindDone, f)
	bad, err := mutateJSON(m, "samples", wire.U64(161))
	if err != nil {
		t.Fatal(err)
	}
	var result wire.Finish
	if wire.ParseJSON(bad.Body, &result) == nil {
		t.Fatal("故障没有真正改变线协议")
	}
}
