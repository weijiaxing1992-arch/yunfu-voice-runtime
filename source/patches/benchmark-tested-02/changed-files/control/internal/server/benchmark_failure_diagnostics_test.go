package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// benchmarkFailureResponse只返回内存HTTP响应，不开启监听端口或向任何实例发送请求。
func benchmarkFailureResponse(t *testing.T, times ...time.Time) *http.Response {
	t.Helper()
	wire := benchmarkProcessingStatusWire(t)
	first := wire["workers"].([]any)[0].(map[string]any)
	workers := make([]any, len(times))
	for i, at := range times {
		worker := map[string]any{}
		for key, value := range first {
			worker[key] = value
		}
		worker["id"], worker["pid"] = i, 100+i
		worker["admission"] = map[string]any{"sampled_at": at}
		workers[i] = worker
	}
	wire["workers"] = workers
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}
}

// TestBenchmarkFailureFinalWaitsForEveryWorker覆盖旧缓存、仅部分分片刷新和最终完整覆盖，诊断成功不能清除失败。
func TestBenchmarkFailureFinalWaitsForEveryWorker(t *testing.T) {
	exited := time.Now().Add(-100 * time.Millisecond)
	old, fresh := exited.Add(-time.Millisecond), exited.Add(time.Millisecond)
	attempts := 0
	m, task, h, evidence := benchmarkFinalFixture(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/v1/status" {
			t.Fatalf("补采样越过只读状态边界: %s %s", r.Method, r.URL.Path)
		}
		attempts++
		switch attempts {
		case 1:
			return benchmarkFailureResponse(t, old, old), nil
		case 2:
			return benchmarkFailureResponse(t, fresh, old), nil
		default:
			return benchmarkFailureResponse(t, fresh, fresh), nil
		}
	})
	task.run.Status, task.run.Error = "failed", "发生器缺包"
	task.run.Failure = &benchmarkFailure{Stage: "media", Code: "execution_failed", Message: task.run.Error}
	started := time.Now()
	status := m.observeFailureFinal(context.Background(), task, h, evidence, "generator_exit_failure", 2, exited)
	deadline, err := time.Parse(time.RFC3339Nano, evidence["final_status_sampling_deadline"].(string))
	if err != nil || deadline.After(started.Add(5*time.Second+50*time.Millisecond)) {
		t.Fatalf("补采样总预算超过5秒: %v %v", deadline, err)
	}
	if status == nil || attempts != 3 || len(task.run.Samples) != 3 || evidence["worker_stats_after_generator_exit"] != true || evidence["final_status_sampling_outcome"] != "fresh" || evidence["final_status_freshness_error"] != nil {
		t.Fatalf("未等待全部分片刷新: attempts=%d evidence=%+v", attempts, evidence)
	}
	if task.run.Status != "failed" || task.run.Error != "发生器缺包" || task.run.Failure.Message != "发生器缺包" {
		t.Fatalf("采样成功修改了原始失败: %+v", task.run)
	}
}

// TestBenchmarkFailureFinalCancelRetainsLastSample证明取消唤醒阻塞读取、保留上一份统计且不锁住任务查询。
func TestBenchmarkFailureFinalCancelRetainsLastSample(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exited := time.Now()
	attempts := 0
	blocked := make(chan struct{})
	m, task, h, evidence := benchmarkFinalFixture(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return benchmarkFailureResponse(t, exited.Add(-time.Millisecond)), nil
		}
		close(blocked)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	task.run.Status, task.run.Error = "failed", "原始发生器失败"
	done := make(chan *benchmarkStatus, 1)
	go func() { done <- m.observeFailureFinal(ctx, task, h, evidence, "generator_exit_failure", 1, exited) }()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("未进入第二次可取消读取")
	}
	// 模拟查询接口的持锁快照；网络请求尚未返回时应直接取得第一次成功样本。
	queried := make(chan benchmarkRun, 1)
	go func() { m.mu.Lock(); value := m.snapshotLocked(task, true); m.mu.Unlock(); queried <- value }()
	select {
	case value := <-queried:
		if len(value.Samples) != 1 || value.Samples[0].RXPackets != 123 {
			t.Fatalf("并发查询没有保留样本: %+v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("补采样阻塞了任务查询锁")
	}
	cancel()
	select {
	case status := <-done:
		if status == nil || len(task.run.Samples) != 1 || evidence["final_status_sampled_at"] == nil || evidence["final_status_sampling_outcome"] != "cancelled" || evidence["worker_stats_after_generator_exit"] != false || evidence["final_status_sampling_error"] == nil {
			t.Fatalf("取消丢掉旧样本或误称新鲜: %+v %+v", status, evidence)
		}
		if task.run.Status != "failed" || task.run.Error != "原始发生器失败" {
			t.Fatal("取消补采样改写原始失败")
		}
	case <-time.After(time.Second):
		t.Fatal("取消没有及时结束补采样")
	}
}

// TestBenchmarkFailureFinalParentDeadline包含重试等待和HTTP自身阻塞两种到期路径，不授予额外预算。
func TestBenchmarkFailureFinalParentDeadline(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry_wait", true: "http_read"}[block], func(t *testing.T) {
			exited := time.Now()
			parentDeadline := time.Now().Add(30 * time.Millisecond)
			ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
			defer cancel()
			calls := 0
			m, task, h, evidence := benchmarkFinalFixture(func(r *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := r.Context().Deadline()
				if !ok || !deadline.Equal(parentDeadline) {
					t.Fatalf("HTTP延长父期限: %v", deadline)
				}
				if block {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return benchmarkFailureResponse(t, exited.Add(-time.Millisecond)), nil
			})
			start := time.Now()
			status := m.observeFailureFinal(ctx, task, h, evidence, "generator_report_failure", 1, exited)
			if time.Since(start) > time.Second || calls != 1 || evidence["final_status_sampling_outcome"] != "deadline_exceeded" || evidence["worker_stats_after_generator_exit"] != false {
				t.Fatalf("未遵守任务总期限: calls=%d %+v", calls, evidence)
			}
			if (status == nil) != block || (evidence["final_status_sampled_at"] == nil) != block {
				t.Fatalf("最后样本保留边界错误: %+v %+v", status, evidence)
			}
		})
	}
}

// TestBenchmarkFailureFinalCancelledBeforeStart证明已取消的任务完全不再发请求。
func TestBenchmarkFailureFinalCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, task, h, evidence := benchmarkFinalFixture(func(*http.Request) (*http.Response, error) {
		t.Fatal("取消后仍发出HTTP请求")
		return nil, errors.New("unexpected request")
	})
	status := m.observeFailureFinal(ctx, task, h, evidence, "generator_exit_failure", 1, time.Now())
	if status != nil || evidence["final_status_sampling_attempts"] != 0 || evidence["final_status_sampling_outcome"] != "cancelled" {
		t.Fatalf("预先取消未保持未知: %+v", evidence)
	}
}

// TestBenchmarkFailureFinalRecoversReadError保留错误次数而不把一次暂时读取失败当成无媒体数据。
func TestBenchmarkFailureFinalRecoversReadError(t *testing.T) {
	exited := time.Now().Add(-time.Second)
	attempts := 0
	m, task, h, evidence := benchmarkFinalFixture(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("temporary read failure")
		}
		return benchmarkFailureResponse(t, time.Now()), nil
	})
	status := m.observeFailureFinal(context.Background(), task, h, evidence, "generator_exit_failure", 1, exited)
	if status == nil || attempts != 2 || len(task.run.Samples) != 1 || evidence["status_sampling_errors"] != 1 || evidence["worker_stats_after_generator_exit"] != true {
		t.Fatalf("读取恢复边界错误: %+v %+v", status, evidence)
	}
}

// TestBenchmarkFailureFinalWorkerCoverage每个ID、PID、代次与严格越过退出时刻均须满足，缺一分片不能算完整。
func TestBenchmarkFailureFinalWorkerCoverage(t *testing.T) {
	exited := time.Now().Add(-time.Second)
	base := func() benchmarkStatus {
		status := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t))
		other := status.Workers[0]
		other.ID, other.PID = 1, other.PID+1
		status.Workers = append(status.Workers, other)
		return status
	}
	valid := base()
	if err := benchmarkWorkerStatsAfterExit(&valid, 2, exited); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*benchmarkStatus){
		"missing_worker": func(s *benchmarkStatus) { s.Workers = s.Workers[:1] },
		"duplicate_id":   func(s *benchmarkStatus) { s.Workers[1].ID = 0 },
		"duplicate_pid":  func(s *benchmarkStatus) { s.Workers[1].PID = s.Workers[0].PID },
		"wrong_id":       func(s *benchmarkStatus) { s.Workers[1].ID = 2 },
		"zero_pid":       func(s *benchmarkStatus) { s.Workers[1].PID = 0 },
		"generation":     func(s *benchmarkStatus) { s.Workers[1].Generation = 2 },
		"restart":        func(s *benchmarkStatus) { s.Workers[1].Restarts = 1 },
		"equal_boundary": func(s *benchmarkStatus) { s.Workers[1].Admission.SampledAt = exited },
		"older":          func(s *benchmarkStatus) { s.Workers[1].Admission.SampledAt = exited.Add(-time.Millisecond) },
		"missing_time":   func(s *benchmarkStatus) { s.Workers[1].Admission.SampledAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			status := base()
			change(&status)
			if benchmarkWorkerStatsAfterExit(&status, 2, exited) == nil {
				t.Fatal("错误覆盖仍声称全部新鲜")
			}
		})
	}
}

// TestBenchmarkSocketDropCoverage全支持/部分支持/旧版缺失/错误类型均按实际范围发布，非零计数不丢失。
func TestBenchmarkSocketDropCoverage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flags     []any
		supported int
	}{
		{"all_supported", []any{true, true}, 2},
		{"partial", []any{true, false}, 1},
		{"unsupported", []any{false, false}, 0},
		{"legacy_missing", []any{nil, nil}, 0},
		{"partial_legacy", []any{true, nil}, 1},
		{"wrong_types", []any{"true", float64(1)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t))
			other := benchmarkDecodeStatus(t, benchmarkProcessingStatusWire(t)).Workers[0]
			other.ID, other.PID = 1, 124
			status.Workers = append(status.Workers, other)
			for i, flag := range tc.flags {
				if flag != nil {
					status.Workers[i].Stats["socket_drop_counter_supported"] = flag
				}
			}
			status.Workers[0].Stats["socket_rx_drops"] = float64(7)
			sample, err := status.checkedSample(time.Now(), validBenchmarkRequest("drop-coverage"))
			if err != nil || sample.SocketRXDrops != 7 || sample.SocketDropCounterSupportedWorkers != tc.supported || sample.SocketDropCounterSupported != (tc.supported == 2) {
				t.Fatalf("支持范围或已有非零计数丢失: %+v %v", sample, err)
			}
			raw, err := json.Marshal(sample)
			if err != nil || !strings.Contains(string(raw), `"socket_drop_counter_supported":`) || !strings.Contains(string(raw), `"socket_drop_counter_supported_workers":`) {
				t.Fatalf("报告未包含明确支持字段: %s %v", raw, err)
			}
		})
	}
}
