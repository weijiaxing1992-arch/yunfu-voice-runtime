package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"

	"rustswitch/control/internal/asr/wire"
)

// provider 是单一实际连接的有限状态；摘要仅证明传输内容，不是语音识别。
type provider struct {
	state                                           string
	hello                                           wire.Hello
	domain                                          uint16
	lastSeq, audioFrames, samples, markers          uint64
	generation, segment                             uint64
	minimumMedia, lastRTPSequence, lastRTPTimestamp uint64
	hasAudio, suspended, discontinuous              bool
	haveRTP, segmentPCM, ended                      bool
	// 首个Decoded之前的暂停仍属于真实来源；保留身份以约束后续恢复，不建立音频范围。
	initialPauseGeneration, initialPauseSegment  uint64
	resultSeq, utterance, revision               uint64
	firstAudio, lastAudio, firstMedia, lastMedia uint64
	digest                                       hash.Hash
}

func newProvider(domain uint16) *provider {
	return &provider{state: "starting", domain: domain, digest: sha256.New()}
}

func (p *provider) counters() wire.Finish {
	return wire.Finish{StreamToken: p.hello.StreamToken, LastEventSeq: wire.U64(p.lastSeq), AudioFrames: wire.U64(p.audioFrames), Samples: wire.U64(p.samples), Markers: wire.U64(p.markers)}
}

func checkedAdd(a, b uint64) (uint64, error) {
	if b > math.MaxUint64-a {
		return 0, errors.New("ASR1 计数溢出")
	}
	return a + b, nil
}

func (p *provider) resetUtterance() {
	p.firstAudio, p.firstMedia, p.revision = 0, 0, 0
	p.digest.Reset()
}
func (p *provider) response(kind string) (wire.Message, error) {
	if p.firstAudio == 0 {
		return wire.Message{}, errors.New("ASR1 无真实音频不能创建转录")
	}
	var err error
	p.resultSeq, err = checkedAdd(p.resultSeq, 1)
	if err != nil {
		return wire.Message{}, err
	}
	p.revision, err = checkedAdd(p.revision, 1)
	if err != nil {
		return wire.Message{}, err
	}
	coverage := "complete"
	if p.discontinuous {
		coverage = "discontinuous"
	}
	return wire.JSONMessage(wire.KindResult, wire.Result{StreamToken: p.hello.StreamToken, ResultSeq: wire.U64(p.resultSeq), UtteranceID: wire.U64(p.utterance), Revision: wire.U64(p.revision), SourceGeneration: wire.U64(p.generation), SourceSegment: wire.U64(p.segment), Type: kind, Text: "mock-sha256:" + hex.EncodeToString(p.digest.Sum(nil)), FirstEventSeq: wire.U64(p.firstAudio), LastEventSeq: wire.U64(p.lastAudio), StartMediaNS: wire.U64(p.firstMedia), EndMediaNS: wire.U64(p.lastMedia), Coverage: coverage})
}

// accept 在完整消息已读取且故障暂停结束后调用；now必须是该检查点的共享时钟。
// 任意协议失败都会锁定终态，不允许在同一连接修正后继续播放旧PCM。
func (p *provider) accept(m wire.Message, now uint64) (out []wire.Message, err error) {
	defer func() {
		if err != nil {
			p.state = "failed"
		}
	}()
	if p.state == "failed" || p.state == "completed" || p.state == "cancelled" {
		return nil, errors.New("ASR1 供应商流已终止")
	}
	if p.state == "starting" {
		if m.Kind != wire.KindHello {
			return nil, errors.New("ASR1 首消息必须HELLO")
		}
		if err = wire.ParseJSON(m.Body, &p.hello); err != nil {
			return nil, err
		}
		if p.hello.ClockDomain != p.domain {
			return nil, errors.New("ASR1 模拟进程共享时钟域不匹配")
		}
		p.state = "streaming"
		reply, e := wire.JSONMessage(wire.KindReady, p.hello)
		if e != nil {
			return nil, e
		}
		return []wire.Message{reply}, nil
	}
	switch m.Kind {
	case wire.KindAudio:
		if p.state != "streaming" || p.ended {
			return nil, errors.New("ASR1 音频输入已停止或等待新来源")
		}
		a, e := wire.ParseAudio(m.Body)
		if e != nil {
			return nil, e
		}
		if e = a.CheckFreshAt(now, p.domain); e != nil {
			return nil, e
		}
		token, _ := wire.TokenBytes(p.hello.StreamToken)
		if a.StreamToken != token || a.SampleRate != p.hello.TargetRate {
			return nil, errors.New("ASR1 音频连接身份或格式不一致")
		}
		// 中途订阅不会重放Rust来源边界；首个完整Decoded可据真实正身份初始化，不能伪造kind7。
		if p.generation == 0 {
			if p.suspended && (a.SourceGeneration != p.initialPauseGeneration || a.Flags&16 == 0 || a.SourceSegment <= p.initialPauseSegment) {
				return nil, errors.New("ASR1 初始暂停需要同来源更大语音段恢复")
			}
			p.generation, p.segment = a.SourceGeneration, a.SourceSegment
			p.hasAudio, p.haveRTP, p.segmentPCM = false, false, false
			p.suspended = false
			p.initialPauseGeneration, p.initialPauseSegment = 0, 0
			p.lastRTPSequence, p.lastRTPTimestamp = 0, 0
		}
		if p.lastSeq == math.MaxUint64 || a.EventSeq != p.lastSeq+1 || a.SourceGeneration != p.generation {
			return nil, errors.New("ASR1 音频事件序号或来源边界缺失")
		}
		// Rust的新语音段可直接随Decoded携flags16，无需额外kind7；同generation包序仍不能倒退。
		if a.Flags&16 != 0 {
			if a.SourceSegment < p.segment || a.SourceSegment == p.segment && (p.segmentPCM || p.suspended) {
				return nil, errors.New("ASR1 非法重复或倒退的新语音段")
			}
			if a.SourceSegment != p.segment {
				p.resetUtterance()
			}
			p.segment = a.SourceSegment
			p.hasAudio, p.segmentPCM, p.suspended = false, false, false
			p.lastRTPTimestamp = 0
		} else if a.SourceSegment != p.segment || p.suspended {
			return nil, errors.New("ASR1 缺少恢复语音段边界")
		}
		if a.MediaNS < p.minimumMedia || p.haveRTP && a.RTPSequence <= p.lastRTPSequence || p.hasAudio && a.RTPTimestamp <= p.lastRTPTimestamp {
			return nil, errors.New("ASR1 同来源媒体位置倒退")
		}
		if p.hasAudio && a.MediaNS != p.minimumMedia {
			p.discontinuous = true
		}
		if p.firstAudio == 0 {
			p.utterance, e = checkedAdd(p.utterance, 1)
			if e != nil {
				return nil, e
			}
			p.firstAudio, p.firstMedia = a.EventSeq, a.MediaNS
		}
		p.audioFrames, e = checkedAdd(p.audioFrames, 1)
		if e != nil {
			return nil, e
		}
		p.samples, e = checkedAdd(p.samples, uint64(a.SampleCount))
		if e != nil {
			return nil, e
		}
		p.lastSeq, p.lastAudio, p.lastMedia = a.EventSeq, a.EventSeq, a.MediaNS+20_000_000
		p.minimumMedia = p.lastMedia
		p.lastRTPSequence, p.lastRTPTimestamp = a.RTPSequence, a.RTPTimestamp
		p.hasAudio, p.haveRTP, p.segmentPCM = true, true, true
		_, _ = p.digest.Write(a.PCM)
		if p.audioFrames%2 == 0 {
			reply, e := p.response("partial")
			if e != nil {
				return nil, e
			}
			return []wire.Message{reply}, nil
		}
		return nil, nil
	case wire.KindMarker:
		if p.state != "streaming" {
			return nil, errors.New("ASR1 marker 输入已停止")
		}
		var mark wire.Marker
		if e := wire.ParseJSON(m.Body, &mark); e != nil {
			return nil, e
		}
		if e := mark.CheckFreshAt(now, p.domain); e != nil {
			return nil, e
		}
		if mark.StreamToken != p.hello.StreamToken {
			return nil, errors.New("ASR1 marker 身份错误")
		}
		summary := mark.RXKind == 10 || mark.RXKind == 11 || mark.RXKind == 12
		if mark.RXKind == 10 {
			if p.lastSeq == math.MaxUint64 || uint64(mark.Gap.First) != p.lastSeq+1 {
				return nil, errors.New("ASR1 gap 不能消失或重叠")
			}
		} else if summary {
			if uint64(mark.EventSeq) != p.lastSeq {
				return nil, errors.New("ASR1 结束摘要前缀不一致")
			}
		} else if p.lastSeq == math.MaxUint64 || uint64(mark.EventSeq) != p.lastSeq+1 {
			return nil, errors.New("ASR1 marker 序号未连续")
		}
		if mark.RXKind == 7 {
			g, s := uint64(mark.SourceGeneration), uint64(mark.SourceSegment)
			if mark.Boundary == 6 {
				if g == 0 || p.generation != 0 && (g != p.generation || s < p.segment) || p.generation == 0 && p.suspended && (g != p.initialPauseGeneration || s < p.initialPauseSegment) {
					return nil, errors.New("ASR1 源结束必须匹配当前来源")
				}
				if p.generation == 0 {
					p.generation, p.segment = g, s
				} // 仅记结束身份，不建立PCM范围或utterance。
				p.suspended = true
				p.ended = true
				p.resetUtterance()
			} else {
				if mark.Boundary < 1 || mark.Boundary > 4 || g == 0 || g <= p.generation || p.generation == 0 && p.suspended && g <= p.initialPauseGeneration {
					return nil, errors.New("ASR1 必须为新generation来源边界")
				}
				p.generation, p.segment = g, s // CN/DTMF先到时segment=0合法。
				p.hasAudio, p.suspended, p.discontinuous, p.haveRTP, p.segmentPCM = false, false, false, false, false
				p.ended = false
				p.initialPauseGeneration, p.initialPauseSegment = 0, 0
				p.lastRTPSequence, p.lastRTPTimestamp = 0, 0
				p.resetUtterance() // 会话媒体时间下界不随源切换清零。
			}
		} else if !summary && p.generation != 0 && uint64(mark.SourceGeneration) != p.generation {
			return nil, errors.New("ASR1 marker 来源不一致")
		}
		if !summary && mark.RXKind != 7 && p.generation == 0 && p.suspended && uint64(mark.SourceGeneration) != p.initialPauseGeneration {
			return nil, errors.New("ASR1 初始暂停不能被其他来源元数据替换")
		}
		// 首段解码失败后Rust仍在Missing上携带真实NewSegment，恢复身份不等于消费语音。
		if mark.Boundary == 5 {
			g, s := uint64(mark.SourceGeneration), uint64(mark.SourceSegment)
			if p.ended || mark.RXKind != 3 || s == 0 || p.generation == 0 && p.suspended && (g != p.initialPauseGeneration || s <= p.initialPauseSegment) || p.generation != 0 && (s < p.segment || s == p.segment && (p.segmentPCM || p.suspended)) {
				return nil, errors.New("ASR1 Missing恢复语音段边界无效")
			}
			if p.generation == 0 || s != p.segment {
				p.resetUtterance()
				p.generation, p.segment = g, s
				p.hasAudio, p.segmentPCM, p.suspended = false, false, false
				p.lastRTPTimestamp = 0
				p.initialPauseGeneration, p.initialPauseSegment = 0, 0
			}
		}
		// 旧辅助槽和首帧过期可带不同segment；它们不能悄悄改变当前音频来源。
		if mark.RXKind == 2 {
			if p.generation != 0 && uint64(mark.SourceSegment) != p.segment {
				return nil, errors.New("ASR1 PLC必须属于当前语音段")
			}
			p.segmentPCM = true
			if mark.Flags&8 != 0 {
				if uint64(mark.MediaNS) < p.minimumMedia || uint64(mark.MediaNS) > math.MaxUint64-20_000_000 {
					return nil, errors.New("ASR1 PLC媒体位置倒退或溢出")
				}
				p.minimumMedia = uint64(mark.MediaNS) + 20_000_000
			}
			if mark.Flags&2 != 0 {
				if p.hasAudio && uint64(mark.RTPTimestamp) <= p.lastRTPTimestamp {
					return nil, errors.New("ASR1 PLC时间戳倒退")
				}
				p.hasAudio = true
				p.lastRTPTimestamp = uint64(mark.RTPTimestamp)
			}
		}
		if mark.RXKind == 3 && mark.KnownDurationTicks != nil && mark.Reason != 10 && mark.Reason != 11 {
			duration := uint64(*mark.KnownDurationTicks)
			if duration > math.MaxUint64/125_000 {
				return nil, errors.New("ASR1 Missing已知时长溢出")
			}
			if mark.Flags&8 != 0 {
				end, e := checkedAdd(uint64(mark.MediaNS), duration*125_000)
				if e != nil {
					return nil, e
				}
				p.minimumMedia = max(p.minimumMedia, end)
			}
			if mark.Flags&2 != 0 && uint64(mark.SourceSegment) == p.segment && duration != 0 {
				end, e := checkedAdd(uint64(mark.RTPTimestamp), duration)
				if e != nil {
					return nil, e
				}
				// 旧字段表示“最后已用tick”，所以后续完整包必须严格大于end-1。
				p.lastRTPTimestamp, p.hasAudio = max(p.lastRTPTimestamp, end-1), true
			}
		}
		if mark.RXKind != 7 && mark.RXKind != 13 && !(mark.RXKind == 6 && !mark.CNApplied) {
			p.discontinuous = true
		}
		if mark.RXKind == 8 || mark.Flags&64 != 0 {
			if p.generation == 0 && (!p.suspended || uint64(mark.SourceSegment) > p.initialPauseSegment) {
				p.initialPauseGeneration, p.initialPauseSegment = uint64(mark.SourceGeneration), uint64(mark.SourceSegment)
			}
			p.suspended = true
			p.resetUtterance()
		}
		p.lastSeq = uint64(mark.EventSeq)
		p.markers, err = checkedAdd(p.markers, 1)
		if err != nil {
			return nil, err
		}
		if mark.RXKind == 9 || mark.RXKind == 12 {
			return nil, errors.New("ASR1 RX观察失败，不能生成正常final")
		}
		if mark.RXKind == 11 {
			p.state = "input_ended"
		}
		return nil, nil
	case wire.KindFinish:
		var finish wire.Finish
		if e := wire.ParseJSON(m.Body, &finish); e != nil {
			return nil, e
		}
		if finish != p.counters() {
			return nil, fmt.Errorf("ASR1 FINISH不等于实际消费前缀: got=%+v want=%+v", finish, p.counters())
		}
		if p.firstAudio != 0 {
			final, e := p.response("final")
			if e != nil {
				return nil, e
			}
			out = append(out, final)
		}
		done, e := wire.JSONMessage(wire.KindDone, p.counters())
		if e != nil {
			return nil, e
		}
		p.state = "completed"
		return append(out, done), nil
	case wire.KindCancel:
		var c wire.Cancel
		if e := wire.ParseJSON(m.Body, &c); e != nil {
			return nil, e
		}
		if c.StreamToken != p.hello.StreamToken {
			return nil, errors.New("ASR1 CANCEL token错误")
		}
		p.state = "cancelled"
		return nil, nil
	case wire.KindPing:
		var ping wire.Ping
		if e := wire.ParseJSON(m.Body, &ping); e != nil {
			return nil, e
		}
		if ping.StreamToken != p.hello.StreamToken {
			return nil, errors.New("ASR1 PING token错误")
		}
		pong, e := wire.JSONMessage(wire.KindPong, ping)
		if e != nil {
			return nil, e
		}
		return []wire.Message{pong}, nil
	default:
		return nil, errors.New("ASR1 客户端消息类型错误")
	}
}

func (p *provider) eof() error {
	if p.state != "completed" && p.state != "cancelled" {
		return errors.New("ASR1 无DONE的EOF不是完成")
	}
	return nil
}
