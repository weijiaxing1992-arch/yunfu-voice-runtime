// 实时处理合同与旧透传请求并存；音频始终留在Rust事件循环内。
package media

import (
	"errors"
	"math"
)

// ProcessingPlan 在Allocate阶段固定版本和缓冲边界，不提供在途图迁移。
type ProcessingPlan struct {
	Version        uint32 `json:"version"`            // 1表示真实G711处理图；拓扑由Topology独立确定。
	Topology       string `json:"topology,omitempty"` // 空或bridge保留双腿；local是独立能力的本地A腿。
	Mode           string `json:"mode"`               // 当前只实现g711；不能隐式退回relay。
	JitterTargetMS uint16 `json:"jitter_target_ms"`   // 首批固定40ms。
	MaxDelayMS     uint16 `json:"max_delay_ms"`       // 首批固定120ms上限。
}

// G711ProcessingPlan 返回独立值，调用者不能通过全局指针更改已有呼叫合同。
func G711ProcessingPlan() *ProcessingPlan {
	return &ProcessingPlan{Version: 1, Mode: "g711", JitterTargetMS: 40, MaxDelayMS: 120}
}

// G711LocalProcessingPlan沿用媒体时间与缓冲边界，但必须明确请求独立的本地拓扑。
func G711LocalProcessingPlan() *ProcessingPlan {
	plan := G711ProcessingPlan()
	plan.Topology = "local"
	return plan
}

// validateProcessingPlan只接受已经交付的固定合同；不能将未知拓扑解释成旧桥接。
func validateProcessingPlan(plan *ProcessingPlan) error {
	if plan == nil {
		return nil
	}
	if plan.Version != 1 || plan.Mode != "g711" || plan.JitterTargetMS != 40 || plan.MaxDelayMS != 120 || (plan.Topology != "" && plan.Topology != "bridge" && plan.Topology != "local") {
		return errors.New("unsupported G711 processing plan")
	}
	return nil
}

// SupportsLocalProcessing在同一代次短锁内检查真实能力；不分配切片，也不等待任何IPC。
func (w *Worker) SupportsLocalProcessing() bool {
	w.submitMu.Lock()
	defer w.submitMu.Unlock()
	return w.Healthy.Load() && w.Generation.Load() > 0 && w.PID.Load() > 0 && w.supportsLocalProcessingLocked()
}

// 调用者必须持submitMu，避免健康检查后跨代次使用上一worker残留的能力。
func (w *Worker) supportsLocalProcessingLocked() bool {
	bridge, local := false, false
	for _, capability := range w.capabilities {
		bridge = bridge || capability == "processed_g711_v1"
		local = local || capability == "processed_g711_local_v1"
	}
	return bridge && local
}

// ValidateProcessingStats 供独立测试验收复用媒体监督器的同一合同，避免缺字段在另一层变成零。
func ValidateProcessingStats(stats map[string]any, maxCalls int) error {
	return validateProcessingStats(stats, maxCalls)
}

// validateProcessingStats 要求真实启用图的进程提供完整整数计数；缺字段不能在管理页伪显示为零。
func validateProcessingStats(stats map[string]any, maxCalls int) error {
	for _, name := range []string{"processed_active_calls", "processed_decoded_frames", "processed_decoded_samples", "processed_encoded_frames", "processed_encoded_samples", "processed_plc_frames", "processed_missing_frames", "processed_cn_packets", "processed_send_errors", "processed_jitter_lost", "processed_jitter_late", "processed_jitter_reordered", "processed_jitter_duplicates", "processed_jitter_overflow", "processed_playout_expired", "processed_send_deadline_misses", "processed_output_lateness_ns_max", "processed_aux_expired", "processed_aux_timeouts", "processed_queue_reset_drops", "processed_source_resets"} {
		value, ok := stats[name].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value != math.Trunc(value) || value > 9007199254740991 || (name == "processed_active_calls" && value > float64(maxCalls)) {
			return errors.New("invalid processed media stats: " + name)
		}
	}
	if stats["processed_decoded_samples"].(float64) != stats["processed_decoded_frames"].(float64)*160 || stats["processed_encoded_samples"].(float64) != stats["processed_encoded_frames"].(float64)*160 {
		return errors.New("processed media sample/frame counters disagree")
	}
	// JSON数组长度与每个计数都要核验，不能接受缺桶、字符串或无穷值再由页面补零。
	buckets, ok := stats["processed_output_lateness_buckets"].([]any)
	if !ok || len(buckets) != 8 {
		return errors.New("invalid processed output lateness buckets")
	}
	var total float64
	for _, raw := range buckets {
		v, valid := raw.(float64)
		if !valid || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v != math.Trunc(v) || v > 9007199254740991 {
			return errors.New("invalid processed output lateness bucket")
		}
		total += v
	}
	if total < stats["processed_encoded_frames"].(float64) || stats["processed_send_deadline_misses"].(float64) > total || (total == 0 && stats["processed_output_lateness_ns_max"].(float64) != 0) {
		return errors.New("processed output lateness counters disagree")
	}
	return nil
}

// validateLocalProcessingStats只在worker声明独立本地能力时附加核验；旧桥接统计合同保持原样。
// 消费帧包含有限PLC，但播放编码是独立TX，因此不能套用桥接的encoded=decoded+plc关系。
func validateLocalProcessingStats(stats map[string]any) error {
	for _, name := range []string{"processed_local_active_calls", "processed_local_consumed_frames", "processed_local_consumed_samples", "processed_local_nonzero_frames", "processed_local_energy_max"} {
		value, ok := stats[name].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value != math.Trunc(value) || value > 9007199254740991 {
			return errors.New("invalid local processed media stats: " + name)
		}
	}
	active, frames := stats["processed_local_active_calls"].(float64), stats["processed_local_consumed_frames"].(float64)
	samples, nonzero, energy := stats["processed_local_consumed_samples"].(float64), stats["processed_local_nonzero_frames"].(float64), stats["processed_local_energy_max"].(float64)
	// 这里先经过基础处理统计校验；局部计数只能是整个worker计数的子集。
	if active > stats["processed_active_calls"].(float64) || frames > stats["processed_decoded_frames"].(float64)+stats["processed_plc_frames"].(float64) || samples != frames*160 || nonzero > frames || energy > 171798691840 || ((nonzero == 0) != (energy == 0)) {
		return errors.New("local processed media counters disagree")
	}
	return nil
}
