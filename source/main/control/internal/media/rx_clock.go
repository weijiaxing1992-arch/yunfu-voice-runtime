package media

import (
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

const rxObservationBudgetNS uint64 = 100_000_000

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
