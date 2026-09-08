// 隔离测试必须分别证明请求模式、发生器媒体内容和实际worker处理计数，不能由单个passed字段代替。
package server

import (
	"errors"
	"fmt"
	"math"
	"rustswitch/control/internal/media"
	"time"
)

// benchmarkMaxExactInteger 保持浏览器和JSON浮点解码中的整数证据精确，累加溢出时拒绝发布。
const benchmarkMaxExactInteger = 9007199254740991

var benchmarkProcessedCounters = []string{
	"processed_active_calls", "processed_decoded_frames", "processed_decoded_samples", "processed_encoded_frames", "processed_encoded_samples",
	"processed_plc_frames", "processed_missing_frames", "processed_cn_packets", "processed_send_errors", "processed_jitter_lost", "processed_jitter_late",
	"processed_jitter_reordered", "processed_jitter_duplicates", "processed_jitter_overflow", "processed_playout_expired", "processed_send_deadline_misses",
	"processed_output_lateness_ns_max", "processed_aux_expired", "processed_aux_timeouts", "processed_queue_reset_drops", "processed_source_resets",
}

// benchmarkNumber 区分缺字段、非法类型和真实零值；计数必须为可精确表达的非负整数。
func benchmarkNumber(values map[string]any, name string, integer bool) (float64, error) {
	v, ok := values[name].(float64)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > benchmarkMaxExactInteger || integer && v != math.Trunc(v) {
		return 0, fmt.Errorf("测试证据字段 %s 缺失或不是有效非负数", name)
	}
	return v, nil
}

// benchmarkAdd 拒绝累计溢出，不截断计数或把异常当作零。
func benchmarkAdd(a, b float64) (float64, error) {
	if b > benchmarkMaxExactInteger-a {
		return 0, errors.New("测试统计累计超过JSON精确整数上限")
	}
	return a + b, nil
}

// checkedSample 只发布完整且近期的worker统计；冷启动尚无首个统计时明确读取失败。
func (s benchmarkStatus) checkedSample(start time.Time, q benchmarkRequest) (benchmarkSample, error) {
	q = q.normalized()
	now := time.Now()
	v := benchmarkSample{At: now.UTC().Format(time.RFC3339Nano), ElapsedSeconds: now.Sub(start).Seconds(), ActiveCalls: s.Active, EstablishedCalls: s.Established, WorkerCount: len(s.Workers), Controller: s.Controller, MediaProcessing: q.MediaProcessing, WorkerStatsSampledAt: []time.Time{}}
	if len(s.Workers) == 0 {
		return v, errors.New("测试实例尚未提供媒体worker统计")
	}
	if q.MediaProcessing == "g711" {
		v.Processing = map[string]any{}
		for _, key := range benchmarkProcessedCounters {
			v.Processing[key] = float64(0)
		}
		v.Processing["processed_output_lateness_buckets"] = make([]any, 8)
		for i := range 8 {
			v.Processing["processed_output_lateness_buckets"].([]any)[i] = float64(0)
		}
	}
	ids := map[int]bool{}
	pids := map[int]bool{}
	for _, w := range s.Workers {
		if w.ID < 0 || w.ID >= len(s.Workers) || ids[w.ID] || w.PID <= 0 || pids[w.PID] || w.Generation == 0 {
			return v, errors.New("测试媒体worker身份缺失或重复")
		}
		ids[w.ID] = true
		pids[w.PID] = true
		at := w.Admission.SampledAt
		if at.IsZero() || now.Sub(at) > 3*time.Second || at.After(now.Add(time.Second)) {
			return v, fmt.Errorf("测试媒体worker %d 统计缺失、过期或时钟异常", w.ID)
		}
		v.WorkerStatsSampledAt = append(v.WorkerStatsSampledAt, at)
		// 只有明确的布尔true表示该分片提供socket丢弃计数；缺失/null/错误类型均不扩大覆盖。
		if supported, ok := w.Stats["socket_drop_counter_supported"].(bool); ok && supported {
			v.SocketDropCounterSupportedWorkers++
		}
		if w.Healthy {
			v.HealthyWorkers++
		}
		for key, target := range map[string]*uint64{"active_calls": &v.MediaActiveCalls, "rx_packets": &v.RXPackets, "tx_packets": &v.TXPackets, "socket_rx_drops": &v.SocketRXDrops, "send_errors": &v.SendErrors, "send_queue_drops": &v.SendQueueDrops, "send_expired": &v.SendExpired, "invalid_packets": &v.InvalidPackets, "rate_limited": &v.RateLimited, "peak_resident_bytes": &v.PeakResidentBytes} {
			number, err := benchmarkNumber(w.Stats, key, true)
			if err != nil {
				return v, fmt.Errorf("worker %d: %w", w.ID, err)
			}
			total, err := benchmarkAdd(float64(*target), number)
			if err != nil {
				return v, err
			}
			*target = uint64(total)
		}
		for key, target := range map[string]*float64{"user_cpu_seconds": &v.UserCPUSeconds, "system_cpu_seconds": &v.SystemCPUSeconds} {
			number, err := benchmarkNumber(w.Stats, key, false)
			if err != nil {
				return v, fmt.Errorf("worker %d: %w", w.ID, err)
			}
			*target, err = benchmarkAdd(*target, number)
			if err != nil {
				return v, err
			}
		}
		if q.MediaProcessing != "g711" {
			continue
		}
		if err := media.ValidateProcessingStats(w.Stats, q.Concurrency); err != nil {
			return v, fmt.Errorf("worker %d g711处理证据不完整: %w", w.ID, err)
		}
		for _, key := range benchmarkProcessedCounters {
			a, b := v.Processing[key].(float64), w.Stats[key].(float64)
			if key == "processed_output_lateness_ns_max" {
				v.Processing[key] = max(a, b)
			} else {
				total, err := benchmarkAdd(a, b)
				if err != nil {
					return v, err
				}
				v.Processing[key] = total
			}
		}
		buckets := v.Processing["processed_output_lateness_buckets"].([]any)
		for i, raw := range w.Stats["processed_output_lateness_buckets"].([]any) {
			total, err := benchmarkAdd(buckets[i].(float64), raw.(float64))
			if err != nil {
				return v, err
			}
			buckets[i] = total
		}
	}
	if v.Processing != nil {
		if err := media.ValidateProcessingStats(v.Processing, q.Concurrency); err != nil {
			return v, fmt.Errorf("汇总g711处理统计不一致: %w", err)
		}
	}
	v.SocketDropCounterSupported = v.SocketDropCounterSupportedWorkers == len(s.Workers)
	return v, nil
}

// benchmarkResultSummary 只在完整报告合同核验通过后交给最终worker计数对照。
type benchmarkResultSummary struct {
	Sent     uint64
	Received uint64
	TailPLC  uint64
}

// benchmarkValidateResult 保留发生器原有98%逐方向负载与零缺包要求，并核验模式与双腿PCM真值。
func benchmarkValidateResult(q benchmarkRequest, values map[string]any) (benchmarkResultSummary, error) {
	q = q.normalized()
	var result benchmarkResultSummary
	if passed, ok := values["passed"].(bool); !ok || !passed {
		return result, errors.New("发生器报告未通过媒体校验")
	}
	if values["media_processing"] != q.MediaProcessing {
		return result, errors.New("发生器报告未证明请求的media_processing，禁止用透传结果替代转码")
	}
	if bypass, ok := values["media_server_bypassed"].(bool); !ok || bypass {
		return result, errors.New("发生器报告没有证明媒体经过独立测试服务")
	}
	for key, expected := range map[string]int{"requested_calls": q.Concurrency, "established_calls": q.Concurrency, "media_seconds": q.DurationSeconds, "payload_type": q.Payload, "ptime_ms": 20, "nominal_packets": q.Concurrency * q.DurationSeconds * 100} {
		v, err := benchmarkNumber(values, key, true)
		if err != nil || v != float64(expected) {
			return result, fmt.Errorf("发生器报告 %s 与请求不一致或缺失", key)
		}
	}
	for _, key := range []string{"unreceived_packets", "duplicate_packets", "invalid_packets", "reordered_packets", "generator_write_errors", "generator_reader_errors", "unexpected_byes", "teardown_failures", "unknown_rtp_packets", "underloaded_flow_directions", "unexpected_received_packets"} {
		v, err := benchmarkNumber(values, key, true)
		if err != nil || v != 0 {
			return result, fmt.Errorf("发生器报告 %s 缺失或不为零", key)
		}
	}
	sent, err := benchmarkNumber(values, "sent_packets", true)
	if err != nil {
		return result, err
	}
	received, err := benchmarkNumber(values, "received_unique_packets", true)
	if err != nil {
		return result, err
	}
	nominal := float64(q.Concurrency * q.DurationSeconds * 100)
	if sent == 0 || sent != received || sent > nominal || sent*100 < nominal*98 {
		return result, errors.New("发生器未完成目标负载和完整收包")
	}
	ratio, err := benchmarkNumber(values, "offered_load_ratio", false)
	if err != nil || math.Abs(ratio-sent/nominal) > 1e-12 {
		return result, errors.New("发生器负载比例与真实发送数不一致")
	}
	result.Sent, result.Received = uint64(sent), uint64(received)
	directions, ok := values["directions"].([]any)
	if !ok || len(directions) != 2 {
		return result, errors.New("发生器缺少双方向负载证据")
	}
	seen := map[int]bool{}
	var totalSent, totalReceived float64
	for _, raw := range directions {
		d, ok := raw.(map[string]any)
		if !ok {
			return result, errors.New("发生器方向证据格式无效")
		}
		side, err := benchmarkNumber(d, "source_side", true)
		if err != nil || side > 1 || seen[int(side)] {
			return result, errors.New("发生器方向身份缺失或重复")
		}
		seen[int(side)] = true
		for _, key := range []string{"flows_with_loss", "underloaded_flows"} {
			v, err := benchmarkNumber(d, key, true)
			if err != nil || v != 0 {
				return result, fmt.Errorf("发生器逐方向 %s 未通过", key)
			}
		}
		ds, err := benchmarkNumber(d, "sent", true)
		if err != nil {
			return result, err
		}
		dr, err := benchmarkNumber(d, "received", true)
		if err != nil {
			return result, err
		}
		if ds == 0 || ds != dr || ds*100 < float64(q.Concurrency*q.DurationSeconds*50)*98 {
			return result, errors.New("发生器双方向存在空载或缺包")
		}
		for _, key := range []string{"minimum_sent_per_flow", "minimum_received_per_flow"} {
			v, err := benchmarkNumber(d, key, true)
			if err != nil || v*100 < float64(q.DurationSeconds*50)*98 {
				return result, errors.New("发生器逐通逐方向未达到98%负载")
			}
		}
		totalSent += ds
		totalReceived += dr
	}
	if totalSent != sent || totalReceived != received {
		return result, errors.New("发生器方向计数与总量不一致")
	}
	if q.MediaProcessing == "relay" {
		return result, nil
	}
	if values["media_demux"] != "negotiated_source_and_socket" {
		return result, errors.New("g711发生器没有使用协商后的来源分流")
	}
	for key, expected := range map[string]int{"endpoint_socket_groups": benchmarkSocketGroups(q), "generator_receiver_workers": benchmarkSocketGroups(q) * 2} {
		v, err := benchmarkNumber(values, key, true)
		if err != nil || v != float64(expected) {
			return result, fmt.Errorf("g711发生器 %s 与有界端点预算不一致", key)
		}
	}
	p, ok := values["processed_media"].(map[string]any)
	if !ok || p["oracle_version"] != "g711-pcm-v1" {
		return result, errors.New("g711发生器缺少PCM独立真值校验")
	}
	codecs := map[int]string{0: "PCMU", 8: "PCMA"}
	if p["codec_a"] != codecs[q.Payload] || p["codec_b"] != codecs[q.Payload^8] {
		return result, errors.New("g711报告双腿编码不符合异律转码请求")
	}
	for key, expected := range map[string]int{"payload_a": q.Payload, "payload_b": q.Payload ^ 8, "samples_per_frame": 160, "jitter_target_ms": 40, "receive_late_limit_ms": 80, "burst_gap_limit_ms": 5} {
		v, err := benchmarkNumber(p, key, true)
		if err != nil || v != float64(expected) {
			return result, fmt.Errorf("g711报告 %s 合同不符", key)
		}
	}
	for _, key := range []string{"content_mismatch_packets", "cross_flow_packets", "codec_mismatch_packets", "timestamp_errors", "sequence_errors", "identity_errors", "late_packets", "burst_packets", "generator_late_packets"} {
		v, err := benchmarkNumber(p, key, true)
		if err != nil || v != 0 {
			return result, fmt.Errorf("g711发生器 %s 缺失或校验失败", key)
		}
	}
	verified, err := benchmarkNumber(p, "content_verified_packets", true)
	if err != nil || verified != received {
		return result, errors.New("g711完整PCM内容验证数与收包数不一致")
	}
	tail, err := benchmarkNumber(p, "tail_plc_packets", true)
	if err != nil || tail > float64(q.Concurrency*12) {
		return result, errors.New("g711尾部PLC超出每通双向120ms上限")
	}
	observed, err := benchmarkNumber(p, "observed_packets", true)
	if err != nil || observed != received+tail {
		return result, errors.New("g711接收观察数与内容/尾部PLC计数不一致")
	}
	result.TailPLC = uint64(tail)
	return result, nil
}

// benchmarkProcessingComplete 在拆线后的新统计中交叉验证真实处理帧，辅助尾部PLC不得充当有效音频。
func benchmarkProcessingComplete(q benchmarkRequest, sample benchmarkSample, result benchmarkResultSummary) error {
	if q.normalized().MediaProcessing != "g711" {
		return nil
	}
	p := sample.Processing
	if p == nil {
		return errors.New("最终g711处理统计缺失")
	}
	for _, key := range []string{"processed_active_calls", "processed_send_errors", "processed_jitter_late", "processed_jitter_duplicates", "processed_jitter_overflow", "processed_playout_expired", "processed_send_deadline_misses", "processed_aux_expired", "processed_aux_timeouts", "processed_queue_reset_drops", "processed_source_resets", "processed_cn_packets"} {
		if p[key].(float64) != 0 {
			return fmt.Errorf("最终g711统计 %s 不为零", key)
		}
	}
	decoded, encoded, plc := uint64(p["processed_decoded_frames"].(float64)), uint64(p["processed_encoded_frames"].(float64)), uint64(p["processed_plc_frames"].(float64))
	if decoded != result.Sent || encoded < result.Received || encoded != decoded+plc || plc > uint64(q.Concurrency*12) || plc < result.TailPLC {
		return errors.New("worker真实解码/编码/尾部PLC计数与发生器完整收包证据不一致")
	}
	return nil
}

// benchmarkValidateFinal 要求拆线后的同代worker统计，零状态必须来自完整采样而非JSON默认值。
func benchmarkValidateFinal(q benchmarkRequest, status *benchmarkStatus, sample benchmarkSample, expectedWorkers int, notBefore time.Time, result benchmarkResultSummary) error {
	for _, worker := range status.Workers {
		if !worker.Admission.SampledAt.After(notBefore) || worker.Restarts != 0 || worker.Generation != 1 {
			return errors.New("worker统计尚未越过发生器退出时刻，或测试期间进程发生重启")
		}
	}
	if sample.ActiveCalls != 0 || sample.EstablishedCalls != 0 || sample.MediaActiveCalls != 0 || sample.HealthyWorkers != sample.WorkerCount || sample.WorkerCount != expectedWorkers || sample.SocketRXDrops != 0 || sample.SendErrors != 0 || sample.SendQueueDrops != 0 || sample.SendExpired != 0 || sample.InvalidPackets != 0 || sample.RateLimited != 0 || !status.JournalHealthy || status.JournalLost != 0 || status.JournalSynced != status.JournalWritten {
		return errors.New("拆线后资源、媒体健康、丢包计数或事件落盘未通过检查")
	}
	return benchmarkProcessingComplete(q, sample, result)
}
