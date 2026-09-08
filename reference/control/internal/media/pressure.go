package media

import (
	"time"
)

// Admission 是发布后不可变的压力快照；监督协程负责更新，呼叫准入只读取它的值。
type Admission struct {
	Reason    string    `json:"reason"`         // 空值表示本次压力检查允许新呼叫。
	CPUCores  float64   `json:"cpu_cores_used"` // CPU 时间增量除以墙钟间隔，单位为占用核心数。
	SampledAt time.Time `json:"sampled_at"`     // 采样时刻，用于检测观测停滞。
}

// pressureTracker 持有单分片的历史差分及恢复计数，只由该分片监督协程访问。
type pressureTracker struct {
	previousTime  time.Time // 上一次有效观察时刻。
	previousCPU   float64   // 上次累计用户态及内核态 CPU 秒数。
	previousDrops float64   // 上次累计收包丢失与发送失败计数。
	reason        string    // 当前保持中的拒绝原因。
	clearSamples  int       // 连续处于恢复区间的采样数量。
}

// number 读取 JSON 解码产生的浮点计数；字段缺失或类型不符时按零处理。
func number(stats map[string]any, key string) float64 {
	value, _ := stats[key].(float64)
	return value
}

// update 对累计指标做差分，发生丢包、CPU 过高或积压时关闭新准入，连续三个低压采样才恢复。
func (p *pressureTracker) update(now time.Time, stats map[string]any, queued int, high, low float64) Admission {
	cpu := number(stats, "user_cpu_seconds") + number(stats, "system_cpu_seconds")
	drops := number(stats, "socket_rx_drops") + number(stats, "send_queue_drops") + number(stats, "send_expired") + number(stats, "send_errors")
	sample := Admission{SampledAt: now}
	// 首次采样和 CPU 累计值回退时只建立基线，避免把进程重置误算成负负载。
	if !p.previousTime.IsZero() && cpu >= p.previousCPU {
		elapsed := now.Sub(p.previousTime).Seconds()
		if elapsed > 0 {
			sample.CPUCores = (cpu - p.previousCPU) / elapsed
		}
		switch {
		case drops > p.previousDrops:
			p.reason = "media_drops"
			p.clearSamples = 0
		case sample.CPUCores >= high:
			p.reason = "media_cpu"
			p.clearSamples = 0
		case queued >= 32:
			p.reason = "control_backlog"
			p.clearSamples = 0
		case p.reason != "" && sample.CPUCores <= low && queued < 8:
			p.clearSamples++
			if p.clearSamples >= 3 {
				p.reason = ""
				p.clearSamples = 0
			}
		default:
			p.clearSamples = 0
		}
	}
	p.previousTime = now
	p.previousCPU = cpu
	p.previousDrops = drops
	sample.Reason = p.reason
	return sample
}

// Admission 合并已发布的压力、即时队列积压和采样新鲜度；关闭自适应仍检查进程健康。
func (w *Worker) Admission(now time.Time) Admission {
	result := Admission{}
	if sample := w.admission.Load(); sample != nil {
		result = *sample
	}
	if !w.Healthy.Load() {
		result.Reason = "worker_unavailable"
		return result
	}
	if !w.adaptiveAdmission {
		result.Reason = ""
		return result
	}
	if len(w.jobs)+len(w.urgent) >= 32 {
		result.Reason = "control_backlog"
	}
	if !result.SampledAt.IsZero() && now.Sub(result.SampledAt) > 3*time.Second {
		result.Reason = "stale_media_stats"
	}
	return result
}
