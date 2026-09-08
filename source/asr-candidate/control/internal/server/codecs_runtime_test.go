package server

import (
	"encoding/json"
	"rustswitch/control/internal/media"
	"testing"
)

// TestRuntimeLocalCodecReadinessIsIndependent本地能力不能借用双腿绿色，
// 混合旧worker、代次失效或目录关闭模式时必须按各自事实分别展示。
func TestRuntimeLocalCodecReadinessIsIndependent(t *testing.T) {
	base := media.CapabilitySnapshot{WorkerID: 0, PID: 123, Generation: 1, Healthy: true, Capabilities: []string{"processed_g711_v1"}}
	local := base
	local.Capabilities = []string{"processed_g711_v1", "processed_g711_local_v1"}
	second := local
	second.WorkerID, second.PID = 1, 456
	for _, tt := range []struct {
		name, mode                              string
		workers                                 []media.CapabilitySnapshot
		expected, capable                       int
		bridgeReady, localReady, localAvailable bool
	}{
		{"bridge_only", "g711", []media.CapabilitySnapshot{base}, 1, 0, true, false, false},
		{"local_current", "g711", []media.CapabilitySnapshot{local}, 1, 1, true, true, true},
		{"mixed_workers", "g711", []media.CapabilitySnapshot{base, second}, 2, 1, true, false, false},
		{"all_current", "g711", []media.CapabilitySnapshot{local, second}, 2, 2, true, true, true},
		{"missing_worker", "g711", []media.CapabilitySnapshot{local}, 2, 1, false, false, false},
		{"duplicate_worker", "g711", []media.CapabilitySnapshot{local, local}, 2, 2, false, false, false},
		{"relay_config", "relay", []media.CapabilitySnapshot{local}, 1, 1, false, false, true},
		{"empty_config", "", []media.CapabilitySnapshot{local}, 1, 1, false, false, true},
		{"unknown_config", "future", []media.CapabilitySnapshot{local}, 1, 1, false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeRuntimeCodecs(tt.mode, tt.expected, tt.workers)
			if got.Ready != tt.bridgeReady || got.LocalReady != tt.localReady || got.LocalAvailable != tt.localAvailable || got.LocalCapableWorkers != tt.capable || got.LocalCapability != "processed_g711_local_v1" {
				t.Fatal(got)
			}
			wire, err := json.Marshal(got)
			var fields map[string]any
			if err != nil || json.Unmarshal(wire, &fields) != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"local_capability", "local_available", "local_ready", "local_capable_workers"} {
				if _, exists := fields[key]; !exists {
					t.Fatal("未就绪时也必须明确序列化本地字段", key)
				}
			}
		})
	}
	for _, corrupt := range []func(*media.CapabilitySnapshot){
		func(w *media.CapabilitySnapshot) { w.Healthy = false },
		func(w *media.CapabilitySnapshot) { w.PID = 0 },
		func(w *media.CapabilitySnapshot) { w.Generation = 0 },
		func(w *media.CapabilitySnapshot) { w.WorkerID = 1 },
		func(w *media.CapabilitySnapshot) { w.Capabilities = []string{"processed_g711_local_v1"} },
		func(w *media.CapabilitySnapshot) {
			w.Capabilities = []string{"processed_g711_v1", "processed_g711_local_v1_extra"}
		},
	} {
		bad := local
		corrupt(&bad)
		if got := summarizeRuntimeCodecs("g711", 1, []media.CapabilitySnapshot{bad}); got.LocalReady || got.LocalAvailable {
			t.Fatal("无效或近似能力被当成本地就绪", got)
		}
	}
}

// TestRuntimeCodecReadinessRequiresEveryCurrentWorker 防止配置字符串、旧握手、重复分片或半池就绪被显示为实时可用。
func TestRuntimeCodecReadinessRequiresEveryCurrentWorker(t *testing.T) {
	proof := media.CapabilitySnapshot{WorkerID: 0, Generation: 1, PID: 123, Healthy: true, Capabilities: []string{"processed_g711_v1"}}
	for _, test := range []struct {
		name, mode       string
		expected         int
		workers          []media.CapabilitySnapshot
		ready, available bool
	}{
		{"current", "g711", 1, []media.CapabilitySnapshot{proof}, true, true},
		{"disabled", "relay", 1, []media.CapabilitySnapshot{proof}, false, true},
		{"legacy_default", "", 1, []media.CapabilitySnapshot{proof}, false, true},
		{"missing", "g711", 2, []media.CapabilitySnapshot{proof}, false, false},
		{"empty", "g711", 0, nil, false, false},
		{"duplicate", "g711", 2, []media.CapabilitySnapshot{proof, proof}, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := summarizeRuntimeCodecs(test.mode, test.expected, test.workers)
			if value.Ready != test.ready || value.Available != test.available {
				t.Fatalf("错误能力结论：%+v", value)
			}
		})
	}
	for _, corrupt := range []func(*media.CapabilitySnapshot){
		func(w *media.CapabilitySnapshot) { w.Healthy = false }, func(w *media.CapabilitySnapshot) { w.PID = 0 },
		func(w *media.CapabilitySnapshot) { w.Generation = 0 }, func(w *media.CapabilitySnapshot) { w.WorkerID = 2 },
		func(w *media.CapabilitySnapshot) { w.Capabilities = []string{"processed_g711_v1_extra"} },
	} {
		bad := proof
		corrupt(&bad)
		if value := summarizeRuntimeCodecs("g711", 1, []media.CapabilitySnapshot{bad}); value.Ready || value.Available {
			t.Fatalf("无效握手被接受：%+v", value)
		}
	}
}

// TestLiveCodecFormatsFollowActiveMode 不能让完整的压测预设被误当作 g711 图已支持 G722 或 Opus。
func TestLiveCodecFormatsFollowActiveMode(t *testing.T) {
	if len(liveCodecProfiles("relay")) != 14 {
		t.Fatal("旧模式目录被缩减")
	}
	profiles := liveCodecProfiles("g711")
	if len(profiles) != 2 {
		t.Fatal(profiles)
	}
	for _, profile := range profiles {
		if profile.Payload != 0 && profile.Payload != 8 {
			t.Fatal(profile)
		}
	}
	if len(liveCodecProfiles("future")) != 0 {
		t.Fatal("未知模式推断了支持格式")
	}
}
