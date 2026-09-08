package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"rustswitch/control/internal/config"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/media"
)

// 实际HTTP处理器输出必须包含有效ASR额度，不能仅单测一个未接入总览的函数。
func TestASRStatusAndMetricsExposeBoundedResources(t *testing.T) {
	s, _, _ := newAdminTestServer(t)
	s.pool = &media.Pool{}
	s.journal = &journal.Journal{}
	m := newASRManager(context.Background(), config.ASRStream{SocketPath: "/run/private-secret.sock", MaxStreams: 4}, nil)
	defer m.cancel()
	s.asr = m
	m.byUUID["secret-uuid"] = &asrEntry{uuid: "secret-uuid", slot: 0, starting: true}
	m.entries[0] = m.byUUID["secret-uuid"]
	m.rejectedCapacity.Store(3)
	m.rejectedUUID.Store(2)
	w := httptest.NewRecorder()
	s.status(w, httptest.NewRequest("GET", "/v1/status", nil))
	var body struct {
		ASR ASRStatus `json:"asr_stream"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.ASR.Enabled || body.ASR.Slots != 1 || body.ASR.Starting != 1 || body.ASR.RejectedCapacity != 3 {
		t.Fatalf("总览遗漏ASR: %+v", body)
	}
	metrics := httptest.NewRecorder()
	s.metrics(metrics, httptest.NewRequest("GET", "/metrics", nil))
	for _, line := range []string{"rustswitch_asr_enabled 1\n", "rustswitch_asr_slots 1\n", "rustswitch_asr_starting 1\n", "rustswitch_asr_rejected_capacity_total 3\n"} {
		if !strings.Contains(metrics.Body.String(), line) {
			t.Errorf("缺少指标 %s", line)
		}
	}
	for _, raw := range []string{w.Body.String(), metrics.Body.String()} {
		if strings.Contains(raw, "secret-uuid") || strings.Contains(raw, "private-secret.sock") {
			t.Fatal("泄漏私有身份/路径")
		}
	}
	// 只用于上述原子/快照测试，不启动固定清理器；返回fixture前恢复禁用防止Cleanup等待。
	s.asr = nil
}

func TestASRDisabledStatusIsExplicit(t *testing.T) {
	s, _, _ := newAdminTestServer(t)
	if s.ASRSnapshot() != (ASRStatus{}) {
		t.Fatal("省略配置不能隐式启用")
	}
	w := httptest.NewRecorder()
	s.asrMetrics(w)
	if !strings.Contains(w.Body.String(), "rustswitch_asr_enabled 0\n") {
		t.Fatal("禁用状态未显式输出")
	}
}
