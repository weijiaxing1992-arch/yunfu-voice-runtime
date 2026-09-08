package media

import (
	"testing"
	"time"
)

// TestAdmissionHysteresisAndPacketDrops 验证 CPU 拒绝、连续低压恢复窗口以及丢包即时收紧。
func TestAdmissionHysteresisAndPacketDrops(t *testing.T) {
	now := time.Now()
	p := pressureTracker{}
	// 使用显式采样时刻和累计值，避免测试依赖真实负载或墙钟等待。
	sample := func(second int, cpu, drops float64) Admission {
		return p.update(now.Add(time.Duration(second)*time.Second), map[string]any{"user_cpu_seconds": cpu, "socket_rx_drops": drops}, 0, .85, .65)
	}
	sample(0, 0, 0)
	if v := sample(1, .9, 0); v.Reason != "media_cpu" {
		t.Fatalf("missing overload: %+v", v)
	}
	if v := sample(2, 1, 0); v.Reason == "" {
		t.Fatal("released admission without recovery window")
	}
	sample(3, 1.1, 0)
	if v := sample(4, 1.2, 0); v.Reason != "" {
		t.Fatal("did not recover after three healthy samples")
	}
	if v := sample(5, 1.3, 1); v.Reason != "media_drops" {
		t.Fatal("kernel drops did not close admission")
	}
}

// TestAdmissionRejectsStaleStatsAndControlBacklog 验证过期统计、队列积压和自适应开关的边界。
func TestAdmissionRejectsStaleStatsAndControlBacklog(t *testing.T) {
	w := Worker{adaptiveAdmission: true, jobs: make(chan job, 64), urgent: make(chan job, 64)}
	now := time.Now()
	w.Healthy.Store(true)
	w.admission.Store(&Admission{SampledAt: now.Add(-4 * time.Second)})
	if w.Admission(now).Reason != "stale_media_stats" {
		t.Fatal("stale worker admitted calls")
	}
	w.admission.Store(&Admission{SampledAt: now})
	for i := 0; i < 32; i++ {
		w.jobs <- job{}
	}
	if w.Admission(now).Reason != "control_backlog" {
		t.Fatal("backlog admitted calls")
	}
	w.adaptiveAdmission = false
	if w.Admission(now).Reason != "" {
		t.Fatal("disabled policy still active")
	}
}
