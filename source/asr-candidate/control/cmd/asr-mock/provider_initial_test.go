package main

import (
	"fmt"
	"testing"

	"rustswitch/control/internal/asr/wire"
)

// 初始元数据不是可以丢掉的前缀：暂停事实先于订阅首个Decoded也必须限制恢复。
func TestProviderInitialPauseRequiresRealNewSegment(t *testing.T) {
	for _, kind := range []uint8{2, 3, 8} {
		for _, scenario := range []string{"bare", "same", "backward", "bare_generation", "new_segment", "missing_new_segment"} {
			t.Run(fmt.Sprintf("kind%d/%s", kind, scenario), func(t *testing.T) {
				p := newProvider(2)
				mustAccept(t, p, message(t, wire.KindHello, fixtureHello(8000)))
				pause := fixtureMarker(kind, 1)
				pause.SourceGeneration, pause.SourceSegment, pause.Flags = 7, 9, 64
				mustAccept(t, p, message(t, wire.KindMarker, pause))
				if p.generation != 0 || p.firstAudio != 0 || p.samples != 0 || !p.suspended {
					t.Fatal("暂停元数据不能成为实际音频来源")
				}
				a := fixtureAudio(2, 8000)
				a.SourceGeneration, a.SourceSegment, a.MediaNS = 7, 9, 20_000_000
				switch scenario {
				case "same":
					a.Flags |= 16
				case "backward":
					a.Flags |= 16
					a.SourceSegment = 8
				case "bare_generation":
					a.SourceGeneration = 8
				case "new_segment":
					a.Flags |= 16
					a.SourceSegment = 10
				case "missing_new_segment":
					m := fixtureMarker(3, 2)
					m.SourceGeneration, m.SourceSegment = 7, 10
					m.Boundary, m.Flags, m.MediaNS = 5, 16|8, 20_000_000
					duration := wire.U64(160)
					m.KnownDurationTicks = &duration
					mustAccept(t, p, message(t, wire.KindMarker, m))
					if p.generation != 7 || p.segment != 10 || p.samples != 0 || p.firstAudio != 0 || p.minimumMedia != 40_000_000 {
						t.Fatal("真实Missing恢复边界须记录身份及时间下界，但不能创建PCM范围")
					}
					a.EventSeq, a.SourceSegment, a.MediaNS = 3, 10, 40_000_000
				}
				_, err := p.accept(audioMessage(t, a), fixtureNow)
				positive := scenario == "new_segment" || scenario == "missing_new_segment"
				if positive != (err == nil) {
					t.Fatalf("恢复规则错误 positive=%v err=%v", positive, err)
				}
				if positive && (p.samples != 160 || p.suspended) {
					t.Fatal("合法恢复未消费实际首帧")
				}
			})
		}
	}
}

func TestProviderInitialPauseCannotBeReplacedByOldBoundary(t *testing.T) {
	for _, boundary := range []uint8{1, 6} {
		p := newProvider(2)
		mustAccept(t, p, message(t, wire.KindHello, fixtureHello(8000)))
		m := fixtureMarker(8, 1)
		m.SourceGeneration, m.SourceSegment, m.Flags = 7, 9, 64
		mustAccept(t, p, message(t, wire.KindMarker, m))
		m = fixtureMarker(7, 2)
		m.Boundary = boundary
		if _, err := p.accept(message(t, wire.KindMarker, m), fixtureNow); err == nil {
			t.Fatal("初始暂停来源被无关旧边界替换")
		}
	}
}
