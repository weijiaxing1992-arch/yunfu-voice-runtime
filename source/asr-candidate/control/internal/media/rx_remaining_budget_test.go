package media

import (
	"errors"
	"math"
	"testing"
	"time"
)

// 纯纳秒边界测试不启用socket，也不通过睡眠猜测恰好到期的结果。
func TestRXRemainingBudgetExactBoundaryAndNoRenewal(t *testing.T) {
	const observed uint64 = 123_000_000_000
	f := RXFrame{Kind: RXDecoded, ClockDomain: rxClockDomain, ObservationLowerBoundNS: observed, ExpiresAtNS: observed + rxObservationBudgetNS}
	f.ReceivedAt = time.Now().Add(time.Hour) // 即使本地接收时间被改晚，也不能刷新producer期限。
	original := f
	for _, tc := range []struct {
		name string
		now  uint64
		want time.Duration
		err  error
	}{
		{"initial", observed, 100 * time.Millisecond, nil},
		{"after_decode", observed + 40_000_000, 60 * time.Millisecond, nil},
		{"after_convert", observed + 70_000_000, 30 * time.Millisecond, nil},
		{"partial_write_retry", observed + 90_000_000, 10 * time.Millisecond, nil},
		{"last_nanosecond", observed + rxObservationBudgetNS - 1, time.Nanosecond, nil},
		{"exact_expiry", observed + rxObservationBudgetNS, 0, ErrRXExpired},
		{"expired", observed + rxObservationBudgetNS + 1, 0, ErrRXExpired},
		{"future_observation", observed - 1, 0, ErrRXClockInvalid},
		{"invalid_clock", 0, 0, ErrRXClockInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remainingRXBudget(f, tc.now)
			if got != tc.want || !errors.Is(err, tc.err) || f != original {
				t.Fatalf("剩余量/原期限改变：got=%v err=%v want=%v/%v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestRXRemainingBudgetSummaryNeverCreatesAudioBudget(t *testing.T) {
	for _, kind := range []RXKind{RXExportGap, RXEnd, RXExportFailed} {
		f := RXFrame{Kind: kind, ClockDomain: rxClockDomain}
		for _, now := range []uint64{1, 999_999_999, math.MaxUint64} {
			remaining, err := remainingRXBudget(f, now)
			if remaining != 0 || !errors.Is(err, ErrRXNoObservationBudget) {
				t.Fatalf("摘要生成了音频预算：kind=%v remaining=%v error=%v", kind, remaining, err)
			}
		}
		f.ExpiresAtNS = rxObservationBudgetNS
		if remaining, err := remainingRXBudget(f, 1); remaining != 0 || !errors.Is(err, ErrRXClockInvalid) {
			t.Fatal("摘要伪造期限未被拒绝", remaining, err)
		}
	}
}

func TestRXRemainingBudgetRejectsClockDomainAndOverflow(t *testing.T) {
	base := RXFrame{Kind: RXDecoded, ClockDomain: rxClockDomain, ObservationLowerBoundNS: 1_000_000_000, ExpiresAtNS: 1_100_000_000}
	for name, change := range map[string]func(*RXFrame){
		"other_domain":        func(f *RXFrame) { f.ClockDomain = 3 - rxClockDomain },
		"missing_observation": func(f *RXFrame) { f.ObservationLowerBoundNS = 0 },
		"renewed_expiry":      func(f *RXFrame) { f.ExpiresAtNS++ },
		"overflow": func(f *RXFrame) {
			f.ObservationLowerBoundNS = math.MaxUint64 - rxObservationBudgetNS + 1
			f.ExpiresAtNS = 0
		},
		"unknown_kind": func(f *RXFrame) { f.Kind = 255 },
		"zero_kind":    func(f *RXFrame) { f.Kind = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			f := base
			change(&f)
			if remaining, err := remainingRXBudget(f, base.ObservationLowerBoundNS); remaining != 0 || !errors.Is(err, ErrRXClockInvalid) {
				t.Fatal("畸形期限取得预算", remaining, err)
			}
		})
	}
	last := base
	last.ObservationLowerBoundNS, last.ExpiresAtNS = math.MaxUint64-rxObservationBudgetNS, math.MaxUint64
	if remaining, err := remainingRXBudget(last, last.ObservationLowerBoundNS); err != nil || remaining != 100*time.Millisecond {
		t.Fatal("最后一个合法u64期限被错误溢出", remaining, err)
	}
}

// 公开方法确实使用当前OS共享时钟；其剩余量随时间减少，重复调用不重锚。
func TestRXRemainingBudgetPublicClockDecreases(t *testing.T) {
	now, err := rxClockNow()
	if err != nil {
		t.Fatal(err)
	}
	f := RXFrame{Kind: RXDecoded, ClockDomain: rxClockDomain, ObservationLowerBoundNS: now, ExpiresAtNS: now + rxObservationBudgetNS}
	first, err := f.RemainingBudget()
	if err != nil || first <= 0 || first > 100*time.Millisecond {
		t.Fatal(first, err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := f.RemainingBudget()
	if err != nil || second <= 0 || second >= first {
		t.Fatal("当前时钟没有扣减同一预算", first, second, err)
	}
	old := f
	old.ObservationLowerBoundNS -= 200_000_000
	old.ExpiresAtNS -= 200_000_000
	if remaining, err := old.RemainingBudget(); remaining != 0 || !errors.Is(err, ErrRXExpired) {
		t.Fatal(remaining, err)
	}
	if n := testing.AllocsPerRun(100, func() { _, _ = remainingRXBudget(f, now) }); n != 0 {
		t.Fatal("纯期限计算发生堆分配", n)
	}
}
