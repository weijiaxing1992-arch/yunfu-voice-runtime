package media

import (
	"errors"
	"fmt"
	"math"
	"time"

	"golang.org/x/sys/unix"
)

const rxObservationBudgetNS uint64 = 100_000_000

// 摘要没有producer观察时刻。调用者须按独立控制消息预算处理，不能给摘要或旧PCM续发100ms。
var ErrRXNoObservationBudget = errors.New("RX summary has no observation delivery budget")

// rxClockNow使用公开OS包装获取可跨同机进程比较的纳秒，不读取time.Time的私有单调布局。
// x/sys在Linux不需要CGO；Darwin由其官方生成的系统库trampoline调用clock_gettime。
func rxClockNow() (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(rxClockID, &ts); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRXClockInvalid, err)
	}
	if ts.Sec < 0 || ts.Nsec < 0 || ts.Nsec >= 1_000_000_000 || uint64(ts.Sec) > (math.MaxUint64-uint64(ts.Nsec))/1_000_000_000 {
		return 0, ErrRXClockInvalid
	}
	now := uint64(ts.Sec)*1_000_000_000 + uint64(ts.Nsec)
	if now == 0 {
		return 0, ErrRXClockInvalid
	}
	return now, nil
}

func (f RXFrame) isSummary() bool {
	return f.Kind == RXExportGap || f.Kind == RXEnd || f.Kind == RXExportFailed
}

// rxDeadlineValid只验证不可变wire合同；当前时刻必须在每个实际交付检查点重新读取。
func (f RXFrame) rxDeadlineValid() bool {
	if f.ClockDomain != rxClockDomain {
		return false
	}
	if f.isSummary() {
		return f.ObservationLowerBoundNS == 0 && f.ExpiresAtNS == 0
	}
	return f.ObservationLowerBoundNS != 0 && f.ObservationLowerBoundNS <= math.MaxUint64-rxObservationBudgetNS && f.ExpiresAtNS == f.ObservationLowerBoundNS+rxObservationBudgetNS
}

// checkRXFreshness不更新期限。未来ASR适配器入队/写供应商连接时应再次检查同一frame。
// 保证发生在检查点；业务线程之后任意暂停，不能靠一次Read替代后续使用点的校验。
func checkRXFreshness(f RXFrame, now uint64) error {
	if !f.rxDeadlineValid() || now == 0 || (!f.isSummary() && now < f.ObservationLowerBoundNS) {
		return ErrRXClockInvalid
	}
	if !f.isSummary() && now >= f.ExpiresAtNS {
		return ErrRXExpired
	}
	return nil
}

// CheckFresh在当前进程实际使用原生RX观察前复核共享期限，不把历史gap/终态当成音频。
func (f RXFrame) CheckFresh() error {
	now, err := rxClockNow()
	if err != nil {
		return err
	}
	return checkRXFreshness(f, now)
}

// remainingRXBudget复用不可变RXS2期限，用整数纳秒先验证再相减，避免未来时刻/溢出续期。
// 保留纯函数边界便于测试恰好100ms与1ns；生产始终从公开共享时钟取得当前时刻。
func remainingRXBudget(f RXFrame, now uint64) (time.Duration, error) {
	if f.Kind < RXDecoded || f.Kind > RXAuxiliary {
		return 0, ErrRXClockInvalid
	}
	if err := checkRXFreshness(f, now); err != nil {
		return 0, err
	}
	if f.isSummary() {
		return 0, ErrRXNoObservationBudget
	}
	return time.Duration(f.ExpiresAtNS - now), nil
}

// RemainingBudget返回原始expires-now，不改写帧，也不从SDK接收或当前重试重算100ms。
// 每次实际Write/部分写重试前都应重新调用；检查后仍可能抢占，接收方仍须复核原期限。
// Gap/End/ExportFailed摘要返回0及ErrRXNoObservationBudget，不能作为新音频写入预算。
func (f RXFrame) RemainingBudget() (time.Duration, error) {
	now, err := rxClockNow()
	if err != nil {
		return 0, err
	}
	return remainingRXBudget(f, now)
}
