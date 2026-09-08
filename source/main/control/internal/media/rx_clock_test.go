package media

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func rxFrameAt(t *testing.T, kind RXKind, session, id, seq, observed uint64) RXFrame {
	t.Helper()
	b := rxWire(kind, session, id, seq)
	if kind != RXExportGap && kind != RXEnd && kind != RXExportFailed {
		binary.LittleEndian.PutUint64(b[152:], observed)
		binary.LittleEndian.PutUint64(b[160:], observed+rxObservationBudgetNS)
	}
	f, err := decodeRXFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// 临界值用受控时钟检验纳秒不等式；真实OS滞留另有独立socket用例，不靠睡眠猜边界。
func TestRXAbsoluteDeadlineBoundaryAndClockFailure(t *testing.T) {
	const observed = uint64(123_000_000_000)
	for _, tc := range []struct {
		name string
		now  uint64
		want error
	}{
		{"before_deadline", observed + rxObservationBudgetNS - 1, nil},
		{"at_deadline", observed + rxObservationBudgetNS, ErrRXExpired},
		{"after_deadline", observed + rxObservationBudgetNS + 1, ErrRXExpired},
		{"future_observation", observed - 1, ErrRXClockInvalid},
		{"zero_clock", 0, ErrRXClockInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, l := rxFixture(t)
			h := rxRegister(p, l, 1, 1)
			l.clockNow = func() (uint64, error) { return tc.now, nil }
			l.dispatch(rxFrameAt(t, RXDecoded, 1, 1, 1, observed))
			f, err := h.Read(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("deadline: %v want %v", err, tc.want)
			}
			s := h.Snapshot()
			if tc.want == nil {
				if f.PCM[0] != -25000 || s.DeliveredSamples != 160 || s.DroppedSamples != 0 {
					t.Fatalf("fresh PCM/accounting: %+v", s)
				}
			} else if s.ReceivedSamples != 160 || s.DroppedSamples != 160 || s.DeliveredSamples != 0 || s.QueuedRecords != 0 {
				t.Fatalf("expired samples: %+v", s)
			}
		})
	}
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 1)
	l.clockNow = func() (uint64, error) { return 0, syscall.EIO }
	l.dispatch(rxFrameAt(t, RXDecoded, 1, 1, 1, observed))
	if _, err := h.Read(context.Background()); !errors.Is(err, ErrRXClockInvalid) {
		t.Fatal("clock failure accepted", err)
	}
	if s := h.Snapshot(); s.ClockFailures != 1 || s.FreshnessFailures != 0 || s.DroppedSamples != 160 {
		t.Fatal(s)
	}
}

func TestRXWireRejectsDeadlineRenewalAndClockDomain(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"v1":                  func(b []byte) { copy(b, "RXS1") },
		"other_domain":        func(b []byte) { binary.LittleEndian.PutUint16(b[150:], 3-rxClockDomain) },
		"no_domain":           func(b []byte) { binary.LittleEndian.PutUint16(b[150:], 0) },
		"missing_observation": func(b []byte) { binary.LittleEndian.PutUint64(b[152:], 0) },
		"missing_expiry":      func(b []byte) { binary.LittleEndian.PutUint64(b[160:], 0) },
		"renewed_budget":      func(b []byte) { binary.LittleEndian.PutUint64(b[160:], binary.LittleEndian.Uint64(b[160:])+1) },
		"overflow": func(b []byte) {
			binary.LittleEndian.PutUint64(b[152:], math.MaxUint64-rxObservationBudgetNS+1)
			binary.LittleEndian.PutUint64(b[160:], 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := rxWire(RXDecoded, 1, 1, 1)
			mutate(b)
			if _, err := decodeRXFrame(b); err == nil {
				t.Fatal("invalid deadline accepted")
			}
		})
	}
	for _, kind := range []RXKind{RXEnd, RXExportFailed} {
		b := rxWire(kind, 1, 1, 0)
		binary.LittleEndian.PutUint64(b[152:], 1)
		if _, err := decodeRXFrame(b); err == nil {
			t.Fatal("terminal invented observation")
		}
	}
}

// 各段都没有花满100ms，但同一帧从观察到Read已超总预算，不能按ReceivedAt重新开始。
func TestRXTotalBudgetAndSameIDRetryCannotFreshenPCM(t *testing.T) {
	const observed = uint64(123_000_000_000)
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 1)
	var now atomic.Uint64
	now.Store(observed + 70_000_000)
	l.clockNow = func() (uint64, error) { return now.Load(), nil }
	f := rxFrameAt(t, RXDecoded, 1, 1, 1, observed)
	l.dispatch(f)
	if h.Snapshot().QueuedRecords != 1 {
		t.Fatal("did not queue before total expiry")
	}
	done := make(chan error, 1)
	go func() {
		retry, _, err := p.SubscribeRX(context.Background(), 0, 1, 1, 1)
		if retry != h {
			done <- errors.New("retry replaced original handle")
			return
		}
		done <- err
	}()
	j := <-p.Workers[0].urgent
	rxFinish(j, rxState(1, 1), nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	retained := h.queue[h.head]
	h.mu.Unlock()
	if retained.ExpiresAtNS != f.ExpiresAtNS || retained.ObservationLowerBoundNS != f.ObservationLowerBoundNS {
		t.Fatal("same ID renewed deadline")
	}
	now.Store(observed + 110_000_000)
	if _, err := h.Read(context.Background()); !errors.Is(err, ErrRXExpired) {
		t.Fatal("partial-stage budgets summed", err)
	}
	if s := h.Snapshot(); s.DroppedSamples != 160 || s.FreshnessFailures != 1 || s.DeliveredRecords != 0 {
		t.Fatal(s)
	}
}

// 模拟序列化完成后、实际send之前被调度器暂停；wire携带原期限，不因晚发而变新。
func TestRXPreparedWireCannotRenewAfterSendPause(t *testing.T) {
	b := rxWire(RXDecoded, 1, 1, 1)
	oldDeadline := binary.LittleEndian.Uint64(b[160:])
	time.Sleep(110 * time.Millisecond)
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 1)
	f, err := decodeRXFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	l.dispatch(f)
	if _, err = h.Read(context.Background()); !errors.Is(err, ErrRXExpired) {
		t.Fatal("late prepared PCM delivered", err)
	}
	if f.ExpiresAtNS != oldDeadline {
		t.Fatal("decoder changed producer deadline")
	}
}

// 暂不启动唯一接收循环，让两帧真实驻留AF_UNIX内核socket 250ms；未填满8槽/未等1秒。
// 此用例生产者是Go wire夹具；实际Rust观察编码另由Go↔Rust测试证明。
func TestRXUnixSocketResidenceRejectsTwoOldFramesWithoutBackpressure(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*os.File{os.NewFile(uintptr(fds[0]), "rx-clock-test-parent"), os.NewFile(uintptr(fds[1]), "rx-clock-test-child")}
	conns := make([]net.Conn, 2)
	for i, file := range files {
		conns[i], err = net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	defer conns[1].Close()
	p, l := rxFixture(t)
	l.conn = conns[0].(*net.UnixConn)
	l.done = make(chan struct{})
	slow := rxRegister(p, l, 1, 1)
	fast := rxRegister(p, l, 2, 1)
	_ = conns[1].SetWriteDeadline(time.Now().Add(time.Second))
	writtenAt := time.Now()
	for seq := uint64(1); seq <= 2; seq++ {
		b := rxWire(RXDecoded, 1, 1, seq)
		n, e := conns[1].Write(b)
		if e != nil || n != len(b) {
			t.Fatalf("low-load write blocked: %d %v", n, e)
		}
	}
	if time.Since(writtenAt) >= 100*time.Millisecond {
		t.Fatal("fixture failed to place fresh frames in OS before pause")
	}
	time.Sleep(250 * time.Millisecond)
	if s := slow.Snapshot(); s.ReceivedRecords != 0 {
		t.Fatal("receiver ran during intended OS residence", s)
	}
	go l.run()
	defer l.close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := slow.Read(ctx); !errors.Is(err, ErrRXExpired) {
		t.Fatal("OS-resident old PCM delivered", err)
	}
	deadline := time.Now().Add(time.Second)
	for slow.Snapshot().ReceivedRecords != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s := slow.Snapshot(); s.ReceivedRecords != 2 || s.DroppedRecords != 2 || s.DroppedSamples != 320 || s.DeliveredSamples != 0 || s.FreshnessFailures != 1 {
		t.Fatal("OS expiry accounting", s)
	}
	b := rxWire(RXDecoded, 2, 1, 1)
	if _, err = conns[1].Write(b); err != nil {
		t.Fatal(err)
	}
	f, err := fast.Read(ctx)
	if err != nil || f.PCM[0] != -25000 || f.PCM[159] != int16(159*313-25000) {
		t.Fatalf("other stream affected: %+v %v", f, err)
	}
	if err := f.CheckFresh(); err != nil {
		t.Fatal("fresh consumer recheck", err)
	}
	if !p.Workers[0].Healthy.Load() {
		t.Fatal("RX expiry killed worker")
	}
}

// 记录当前平台公开时钟包装成本；真实容量须另在等配置Linux端到端压测，不能由ns/op外推。
func BenchmarkRXSharedClock(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := rxClockNow(); err != nil {
			b.Fatal(err)
		}
	}
}

// 授权撤销立即唤醒读者并清样本；远端句柄仍在，不能伪造退订完成或抛弃清理所有权。
func TestRXRevokeReadClearsSamplesAndKeepsControl(t *testing.T) {
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 1)
	f, err := decodeRXFrame(rxWire(RXDecoded, 1, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	l.dispatch(f)
	h.RevokeRead()
	h.RevokeRead()
	if _, err := h.Read(context.Background()); !errors.Is(err, ErrRXRetired) {
		t.Fatal("revoked read continued", err)
	}
	if s := h.Snapshot(); s.Retired || s.QueuedRecords != 0 || s.DroppedSamples != 160 || s.DroppedRecords != 1 {
		t.Fatal("bad revoke accounting", s)
	}
	if h.remote.State != "active" {
		t.Fatal("local revoke invented remote stop")
	}
	for _, op := range []string{"rx_status", "rx_unsubscribe"} {
		done := make(chan error, 1)
		go func() { _, err := h.control(context.Background(), op); done <- err }()
		j := <-p.Workers[0].urgent
		r := rxState(1, 1)
		if op == "rx_unsubscribe" {
			r.State = "stopped"
		}
		rxFinish(j, r, nil)
		if err := <-done; err != nil {
			t.Fatal("revoke discarded control ownership", err)
		}
	}
	// 无队列的阻塞Read由close(readDone)直接唤醒，无逐frame goroutine或AfterFunc。
	h2 := rxRegister(p, l, 2, 1)
	done := make(chan error, 1)
	go func() { _, err := h2.Read(context.Background()); done <- err }()
	h2.RevokeRead()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRXRetired) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("revoke did not wake read")
	}
}
