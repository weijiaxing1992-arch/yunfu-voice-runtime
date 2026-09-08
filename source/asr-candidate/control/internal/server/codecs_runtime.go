package server

import (
	"slices"
	"time"

	"rustswitch/control/internal/codecprofile"
	"rustswitch/control/internal/media"
)

// runtimeCodecStatus 将启动模式与实际 worker 握手证据一起公开，库探测成功不能代替实时图就绪。
type runtimeCodecStatus struct {
	Processing          string                     `json:"processing"` // 生效配置，独立于待重启草稿以及测试任务模式。
	Enabled             bool                       `json:"enabled"`    // 只有显式 g711 才接入真实音频图。
	Available           bool                       `json:"available"`  // 所有预期 worker 均健康并声明同一能力；缺分片时为 false。
	Ready               bool                       `json:"ready"`      // 已启用且当前所有分片就绪，不等于业务或容量验收。
	State               string                     `json:"state"`
	ObservedAt          string                     `json:"observed_at"`
	LocalCapability     string                     `json:"local_capability"` // 本地A腿须额外声明的能力；不混入原桥接就绪。
	LocalAvailable      bool                       `json:"local_available"`  // 全部预期worker同时具有桥接和本地能力。
	LocalReady          bool                       `json:"local_ready"`      // 已启用g711且本地能力全部就绪。
	LocalCapableWorkers int                        `json:"local_capable_workers"`
	Capability          string                     `json:"capability"`
	ExpectedWorkers     int                        `json:"expected_workers"`
	HealthyWorkers      int                        `json:"healthy_workers"`
	CapableWorkers      int                        `json:"capable_workers"`
	Workers             []media.CapabilitySnapshot `json:"workers"`
	SampleRate          int                        `json:"sample_rate"`
	FrameMS             int                        `json:"frame_ms"`
	JitterTargetMS      int                        `json:"jitter_target_ms"`
	MaxDelayMS          int                        `json:"max_delay_ms"`
	Scope               string                     `json:"scope"`
}

// runtimeCodecs 每次请求重新读取握手状态，避免缓存使重启失败的进程继续显示绿色。
func (s *Server) runtimeCodecs() runtimeCodecStatus {
	workers := []media.CapabilitySnapshot{}
	if s.pool != nil {
		for _, worker := range s.pool.Workers {
			workers = append(workers, worker.CapabilitySnapshot())
		}
	}
	return summarizeRuntimeCodecs(s.Config.Media.Processing, s.Config.Media.Workers, workers)
}

// summarizeRuntimeCodecs 对混用旧二进制、缺 worker 和不完整代次证据保持未就绪；不推断能力。
func summarizeRuntimeCodecs(processing string, expected int, workers []media.CapabilitySnapshot) runtimeCodecStatus {
	if processing == "" {
		processing = "relay"
	}
	value := runtimeCodecStatus{Processing: processing, Enabled: processing == "g711", State: "disabled", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Capability: "processed_g711_v1", LocalCapability: "processed_g711_local_v1", ExpectedWorkers: expected, Workers: workers, SampleRate: 8000, FrameMS: 20, JitterTargetMS: 40, MaxDelayMS: 120,
		Scope: "G.711 PCMA/PCMU 双腿桥接，8 kHz 单声道、20 ms；真实解码、抖动缓冲、PCM 与重新编码。本地A腿处理、有限播放和按键由local_ready独立标示。当前图不支持 G.722/Opus 实时转码、ASR/TTS 或录音。握手就绪不是通话质量或容量认证。"}
	seen := make(map[int]bool, len(workers))
	identitiesValid := true
	for _, worker := range workers {
		if worker.WorkerID < 0 || worker.WorkerID >= expected || seen[worker.WorkerID] {
			identitiesValid = false
		}
		seen[worker.WorkerID] = true
		if worker.Healthy && worker.Generation > 0 && worker.PID > 0 {
			value.HealthyWorkers++
			if slices.Contains(worker.Capabilities, value.Capability) {
				value.CapableWorkers++
				if slices.Contains(worker.Capabilities, value.LocalCapability) {
					value.LocalCapableWorkers++
				}
			}
		}
	}
	value.Available = identitiesValid && expected > 0 && len(workers) == expected && value.CapableWorkers == expected
	value.Ready = value.Enabled && value.Available
	value.LocalAvailable = value.Available && value.LocalCapableWorkers == expected
	value.LocalReady = value.Enabled && value.LocalAvailable
	if value.Enabled {
		value.State = "unavailable"
		if value.Ready {
			value.State = "ready"
		}
	} else if processing != "relay" {
		value.State = "unavailable"
	}
	return value
}

// liveCodecProfiles 保留完整测试预设目录，同时单列当前生效模式允许的实时通话格式。
func liveCodecProfiles(processing string) []codecprofile.Profile {
	profiles := codecprofile.List()
	if processing == "relay" {
		return profiles
	}
	out := []codecprofile.Profile{}
	if processing == "g711" {
		for _, profile := range profiles {
			if profile.Payload == 0 || profile.Payload == 8 {
				out = append(out, profile)
			}
		}
	}
	return out
}
