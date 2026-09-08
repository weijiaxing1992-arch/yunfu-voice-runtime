package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// benchmarkProcessingStatusWire 使用完整真实字段搭建HTTP夹具，统计发布时间由每次响应独立产生。
func benchmarkProcessingStatusWire(t *testing.T) map[string]any {
	t.Helper()
	stats := map[string]any{}
	for _, key := range []string{"active_calls", "rx_packets", "tx_packets", "socket_rx_drops", "send_errors", "send_queue_drops", "send_expired", "invalid_packets", "rate_limited", "peak_resident_bytes", "user_cpu_seconds", "system_cpu_seconds"} {
		stats[key] = float64(0)
	}
	stats["rx_packets"], stats["tx_packets"] = float64(123), float64(123)
	for _, key := range benchmarkProcessedCounters {
		stats[key] = float64(0)
	}
	stats["processed_output_lateness_buckets"] = []any{float64(0), float64(0), float64(0), float64(0), float64(0), float64(0), float64(0), float64(0)}
	return map[string]any{"active_calls": 0, "established_calls": 0, "journal_healthy": true, "journal_written_events": 3, "journal_synced_events": 3, "journal_lost_events": 0, "workers": []any{map[string]any{"id": 0, "pid": 123, "generation": 1, "healthy": true, "restarts": 0, "stats": stats, "admission": map[string]any{"sampled_at": time.Now().UTC()}}}}
}

func benchmarkDecodeStatus(t *testing.T, raw map[string]any) benchmarkStatus {
	t.Helper()
	wire, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var status benchmarkStatus
	if err := json.Unmarshal(wire, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestBenchmarkProcessingRequestIdentity 证明旧空值与显式relay幂等，但修改到g711不能复用任务。
func TestBenchmarkProcessingRequestIdentity(t *testing.T) {
	s, h, _ := newAdminTestServer(t)
	s.Config.Media.Processing = "g711"
	entered := make(chan *benchmarkTask, 1)
	s.benchmarks.runner = func(ctx context.Context, task *benchmarkTask) {
		entered <- task
		<-ctx.Done()
		s.benchmarks.finish(task, "stopped", nil, benchmarkReport{}, completeBenchmarkCleanup())
	}
	q := validBenchmarkRequest("processing-identity")
	first := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, q)
	if first.Code != 202 {
		t.Fatal(first.Body.String())
	}
	run := decodeBenchmarkRun(t, first)
	task := <-entered
	defer func() { task.cancel(); awaitBenchmark(t, task.done) }()
	if run.MediaProcessing != "relay" || task.run.MediaProcessing != "relay" {
		t.Fatal("旧请求继承了主服务g711")
	}
	q.MediaProcessing = "relay"
	if got := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, q); got.Code != 200 || decodeBenchmarkRun(t, got).ID != run.ID {
		t.Fatal("显式relay未复用旧请求")
	}
	q.MediaProcessing = "g711"
	if got := adminRequest(t, h, "POST", "/v1/tests", s.adminToken, q); got.Code != 409 {
		t.Fatal("模式改变复用了原幂等键")
	}
	if s.Config.Media.Processing != "g711" {
		t.Fatal("测试改写了主服务模式")
	}
}

// TestBenchmarkProcessingValidationAndPortBudget 不支持的codec必须在创建进程前失败，G711只预留32组端点。
func TestBenchmarkProcessingValidationAndPortBudget(t *testing.T) {
	for _, payload := range []int{0, 8, 9, 111, 18} {
		q := validBenchmarkRequest("g711-validation")
		q.MediaProcessing, q.Payload = "g711", payload
		if err := q.validate(); (err == nil) != (payload == 0 || payload == 8) {
			t.Fatalf("payload=%d err=%v", payload, err)
		}
	}
	q := validBenchmarkRequest("bad-mode")
	q.MediaProcessing = "G711"
	if q.validate() == nil {
		t.Fatal("未知模式未拒绝")
	}
	q.Mode, q.Concurrency, q.CPS, q.MediaProcessing = "pressure", 500, 500, "g711"
	if benchmarkSocketGroups(q) != 32 {
		t.Fatal("g711读取器预算未限制")
	}
	q.MediaProcessing = "relay"
	if benchmarkSocketGroups(q) != 128 {
		t.Fatal("relay预算被意外改变")
	}
}

// TestBenchmarkProcessingMissingStatsAreUnknown 遍历完整22字段与坏桶，HTTP成功不能补出一个零值样本。
func TestBenchmarkProcessingMissingStatsAreUnknown(t *testing.T) {
	q := validBenchmarkRequest("stats")
	q.MediaProcessing = "g711"
	keys := append(append([]string{}, benchmarkProcessedCounters...), "processed_output_lateness_buckets")
	for _, key := range keys {
		t.Run("missing_"+key, func(t *testing.T) {
			s := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t))
			delete(s.Workers[0].Stats, key)
			if _, err := s.checkedSample(time.Now(), q); err == nil {
				t.Fatal("缺失统计被当作零")
			}
		})
	}
	for _, mutate := range []func(*benchmarkStatus){
		func(s *benchmarkStatus) { s.Workers[0].Stats["processed_output_lateness_buckets"] = []any{float64(0)} },
		func(s *benchmarkStatus) { s.Workers[0].Stats["processed_decoded_frames"] = float64(1) },
		func(s *benchmarkStatus) { s.Workers[0].Stats["processed_active_calls"] = float64(2) },
		func(s *benchmarkStatus) { s.Workers[0].Stats["processed_output_lateness_buckets"].([]any)[0] = "0" },
		func(s *benchmarkStatus) { s.Workers[0].Stats["user_cpu_seconds"] = math.NaN() },
		func(s *benchmarkStatus) { delete(s.Workers[0].Stats, "active_calls") },
		func(s *benchmarkStatus) { s.Workers[0].Admission.SampledAt = time.Now().Add(-4 * time.Second) },
		func(s *benchmarkStatus) { s.Workers[0].Admission.SampledAt = time.Now().Add(2 * time.Second) },
		func(s *benchmarkStatus) { s.Workers = append(s.Workers, s.Workers[0]) },
		func(s *benchmarkStatus) { s.Workers[0].ID = 1 },
		func(s *benchmarkStatus) { other := s.Workers[0]; other.ID = 1; s.Workers = append(s.Workers, other) },
	} {
		s := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t))
		mutate(&s)
		if _, err := s.checkedSample(time.Now(), q); err == nil {
			t.Fatal("非法或过期统计未拒绝")
		}
	}
	s := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t))
	delete(s.Workers[0].Stats, "processed_active_calls")
	q.MediaProcessing = "relay"
	sample, err := s.checkedSample(time.Now(), q)
	if err != nil || sample.Processing != nil {
		t.Fatalf("relay被误标为转码: %+v %v", sample, err)
	}
}

// TestBenchmarkProcessingAggregation 区分最大延迟与累计计数，并拒绝跨worker的精度溢出。
func TestBenchmarkProcessingAggregation(t *testing.T) {
	q := validBenchmarkRequest("aggregate")
	q.MediaProcessing = "g711"
	s := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t))
	other := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t)).Workers[0]
	other.ID, other.PID = 1, 124
	s.Workers = append(s.Workers, other)
	for i := range s.Workers {
		s.Workers[i].Stats["processed_output_lateness_ns_max"] = float64((i + 1) * 100)
		s.Workers[i].Stats["processed_output_lateness_buckets"].([]any)[0] = float64(i + 1)
	}
	sample, err := s.checkedSample(time.Now(), q)
	if err != nil || sample.Processing["processed_output_lateness_ns_max"] != float64(200) || sample.Processing["processed_output_lateness_buckets"].([]any)[0] != float64(3) {
		t.Fatalf("聚合语义不符: %+v %v", sample, err)
	}
	s.Workers[0].Stats["rx_packets"] = float64(benchmarkMaxExactInteger)
	if _, err := s.checkedSample(time.Now(), q); err == nil {
		t.Fatal("总计数超过精确上限仍发布")
	}
}

// benchmarkValidGeneratorReport 按冻结合同构造三秒真实负载的报告夹具，不替代实际音频验收。
func benchmarkValidGeneratorReport(q benchmarkRequest) map[string]any {
	q = q.normalized()
	nominal := float64(q.Concurrency * q.DurationSeconds * 100)
	v := map[string]any{"passed": true, "media_processing": q.MediaProcessing, "media_server_bypassed": false, "requested_calls": float64(q.Concurrency), "established_calls": float64(q.Concurrency), "media_seconds": float64(q.DurationSeconds), "payload_type": float64(q.Payload), "ptime_ms": float64(20), "nominal_packets": nominal, "sent_packets": nominal, "received_unique_packets": nominal, "offered_load_ratio": float64(1)}
	for _, key := range []string{"unreceived_packets", "duplicate_packets", "invalid_packets", "reordered_packets", "generator_write_errors", "generator_reader_errors", "unexpected_byes", "teardown_failures", "unknown_rtp_packets", "underloaded_flow_directions", "unexpected_received_packets"} {
		v[key] = float64(0)
	}
	directions := []any{}
	for side := range 2 {
		directions = append(directions, map[string]any{"source_side": float64(side), "sent": nominal / 2, "received": nominal / 2, "flows_with_loss": float64(0), "underloaded_flows": float64(0), "minimum_sent_per_flow": float64(q.DurationSeconds * 50), "minimum_received_per_flow": float64(q.DurationSeconds * 50)})
	}
	v["directions"] = directions
	if q.MediaProcessing == "g711" {
		v["media_demux"] = "negotiated_source_and_socket"
		v["endpoint_socket_groups"], v["generator_receiver_workers"] = float64(benchmarkSocketGroups(q)), float64(benchmarkSocketGroups(q)*2)
		codecs := map[int]string{0: "PCMU", 8: "PCMA"}
		p := map[string]any{"oracle_version": "g711-pcm-v1", "codec_a": codecs[q.Payload], "codec_b": codecs[q.Payload^8], "payload_a": float64(q.Payload), "payload_b": float64(q.Payload ^ 8), "samples_per_frame": float64(160), "jitter_target_ms": float64(40), "receive_late_limit_ms": float64(80), "burst_gap_limit_ms": float64(5), "observed_packets": nominal, "content_verified_packets": nominal}
		for _, key := range []string{"content_mismatch_packets", "cross_flow_packets", "codec_mismatch_packets", "timestamp_errors", "sequence_errors", "identity_errors", "late_packets", "burst_packets", "generator_late_packets", "tail_plc_packets"} {
			p[key] = float64(0)
		}
		v["processed_media"] = p
	}
	return v
}

// TestBenchmarkProcessingReportContract 按两种A腿测试异律真值合同，逐字段缺失和假passed均必须失败。
func TestBenchmarkProcessingReportContract(t *testing.T) {
	q := validBenchmarkRequest("report")
	for _, mode := range []string{"relay", "g711"} {
		q.MediaProcessing = mode
		for _, payload := range []int{0, 8} {
			q.Payload = payload
			v := benchmarkValidGeneratorReport(q)
			if _, err := benchmarkValidateResult(q, v); err != nil {
				t.Fatal(err)
			}
			for key := range v {
				copy := benchmarkValidGeneratorReport(q)
				delete(copy, key)
				if _, err := benchmarkValidateResult(q, copy); err == nil {
					t.Fatalf("%s缺%s仍通过", mode, key)
				}
			}
			if mode == "g711" {
				for key := range v["processed_media"].(map[string]any) {
					copy := benchmarkValidGeneratorReport(q)
					delete(copy["processed_media"].(map[string]any), key)
					if _, err := benchmarkValidateResult(q, copy); err == nil {
						t.Fatalf("缺转码%s仍通过", key)
					}
				}
			}
		}
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["media_processing"] = "relay" },
		func(v map[string]any) {
			v["processed_media"].(map[string]any)["codec_b"] = v["processed_media"].(map[string]any)["codec_a"]
		},
		func(v map[string]any) {
			v["processed_media"].(map[string]any)["content_verified_packets"] = float64(299)
		},
		func(v map[string]any) { v["processed_media"].(map[string]any)["content_mismatch_packets"] = float64(1) },
		func(v map[string]any) { v["processed_media"].(map[string]any)["tail_plc_packets"] = float64(13) },
		func(v map[string]any) { v["directions"].([]any)[1].(map[string]any)["source_side"] = float64(0) },
		func(v map[string]any) {
			v["directions"].([]any)[1].(map[string]any)["minimum_received_per_flow"] = float64(146)
		},
		func(v map[string]any) { v["generator_reader_errors"] = float64(1) },
		func(v map[string]any) { v["offered_load_ratio"] = math.NaN() },
		func(v map[string]any) { v["offered_load_ratio"] = 0.99 },
		func(v map[string]any) { v["received_unique_packets"] = 300.5 },
		func(v map[string]any) { v["sent_packets"], v["received_unique_packets"] = float64(301), float64(301) },
	} {
		v := benchmarkValidGeneratorReport(q)
		mutate(v)
		if _, err := benchmarkValidateResult(q, v); err == nil {
			t.Fatal("假passed通过严格合同")
		}
	}
}

// TestBenchmarkProcessingFinalFreshness 将HTTP时间、worker时间和真实处理帧分别核验，不能复用旧缓存或透传计数。
func TestBenchmarkProcessingFinalFreshness(t *testing.T) {
	q := validBenchmarkRequest("final")
	q.MediaProcessing = "g711"
	baseline := func() benchmarkStatus {
		s := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t))
		for _, field := range []string{"processed_decoded_frames", "processed_encoded_frames"} {
			s.Workers[0].Stats[field] = float64(300)
		}
		for _, field := range []string{"processed_decoded_samples", "processed_encoded_samples"} {
			s.Workers[0].Stats[field] = float64(48000)
		}
		s.Workers[0].Stats["processed_output_lateness_buckets"].([]any)[0] = float64(300)
		return s
	}
	boundary := time.Now().Add(-time.Second)
	check := func(s benchmarkStatus) error {
		sample, err := s.checkedSample(time.Now(), q)
		if err != nil {
			return err
		}
		return benchmarkValidateFinal(q, &s, sample, 1, boundary, benchmarkResultSummary{Sent: 300, Received: 300})
	}
	if err := check(baseline()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*benchmarkStatus){
		func(s *benchmarkStatus) { s.Workers[0].Admission.SampledAt = boundary.Add(-time.Millisecond) },
		func(s *benchmarkStatus) { s.Workers[0].Restarts = 1 },
		func(s *benchmarkStatus) { s.Workers[0].Generation = 2 },
		func(s *benchmarkStatus) {
			s.Workers[0].Stats["processed_decoded_frames"], s.Workers[0].Stats["processed_decoded_samples"] = float64(0), float64(0)
		},
		func(s *benchmarkStatus) { s.Workers[0].Stats["processed_send_deadline_misses"] = float64(1) },
		func(s *benchmarkStatus) { s.JournalSynced-- },
		func(s *benchmarkStatus) { s.Established = 1 },
		func(s *benchmarkStatus) { s.Workers[0].Stats["send_errors"] = float64(1) },
	} {
		s := baseline()
		mutate(&s)
		if check(s) == nil {
			t.Fatal("未完成最终验收却通过")
		}
	}
}

// TestBenchmarkProcessingHTTPReadFailure 使用本地真实HTTP证明缺字段不追加假样本，修复后可读取真实零值。
func TestBenchmarkProcessingHTTPReadFailure(t *testing.T) {
	var complete atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := benchmarkProcessingStatusWire(t)
		if !complete.Load() {
			delete(status["workers"].([]any)[0].(map[string]any)["stats"].(map[string]any), "processed_decoded_frames")
		}
		_ = json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	m, task, _, evidence := benchmarkFinalFixture(nil)
	task.run.benchmarkRequest = validBenchmarkRequest("http")
	task.run.MediaProcessing = "g711"
	h := &benchmarkHTTP{base: server.URL, client: server.Client()}
	if _, err := m.observeFinal(context.Background(), task, h, evidence, "teardown_convergence"); err == nil || len(task.run.Samples) != 0 || evidence["final_status_sampled_at"] != nil {
		t.Fatal("缺统计HTTP响应发布假零样本")
	}
	complete.Store(true)
	if _, err := m.observeFinal(context.Background(), task, h, evidence, "teardown_convergence"); err != nil || len(task.run.Samples) != 1 || task.run.Samples[0].Processing == nil {
		t.Fatalf("完整统计未恢复读取: %v", err)
	}
}

// TestBenchmarkProcessingSharedReceiverBudget 覆盖单路、8路和大并发共享读取器预算，缺字段不能降为默认值。
func TestBenchmarkProcessingSharedReceiverBudget(t *testing.T) {
	for _, count := range []int{1, 8, 500} {
		q := validBenchmarkRequest("receiver-budget")
		q.MediaProcessing, q.Mode, q.Concurrency = "g711", "pressure", count
		v := benchmarkValidGeneratorReport(q)
		if _, err := benchmarkValidateResult(q, v); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"endpoint_socket_groups", "generator_receiver_workers"} {
			for _, bad := range []any{float64(0), float64(65), 1.5, math.NaN(), "2"} {
				v := benchmarkValidGeneratorReport(q)
				v[field] = bad
				if _, err := benchmarkValidateResult(q, v); err == nil {
					t.Fatalf("count=%d %s=%v 未拒绝", count, field, bad)
				}
			}
		}
	}
}

// TestBenchmarkStatusRequiredFields 控制面缺字段、null和类型错误不能被零值覆盖。
func TestBenchmarkStatusRequiredFields(t *testing.T) {
	for _, key := range []string{"active_calls", "established_calls", "journal_healthy", "journal_written_events", "journal_synced_events", "journal_lost_events", "workers"} {
		for _, mode := range []string{"missing", "null", "wrong_type"} {
			v := benchmarkProcessingStatusWire(t)
			switch mode {
			case "missing":
				delete(v, key)
			case "null":
				v[key] = nil
			default:
				v[key] = "0"
			}
			wire, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var status benchmarkStatus
			if err := json.Unmarshal(wire, &status); err == nil {
				t.Fatalf("%s %s 未拒绝", key, mode)
			}
		}
	}
}
