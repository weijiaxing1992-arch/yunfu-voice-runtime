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

// benchmarkFinalTransport 只替换HTTP传输，不创建监听端口、子进程或真实通话。
type benchmarkFinalTransport func(*http.Request) (*http.Response, error)

func (f benchmarkFinalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// benchmarkFinalFixture 构造拥有独立证据映射的单任务，保留与生产相同的数值类型。
func benchmarkFinalFixture(transport benchmarkFinalTransport) (*benchmarkManager, *benchmarkTask, *benchmarkHTTP, map[string]any) {
	m := &benchmarkManager{}
	task := &benchmarkTask{created: time.Now(), run: benchmarkRun{Samples: []benchmarkSample{}}}
	h := &benchmarkHTTP{base: "http://127.0.0.1:1", client: &http.Client{Transport: transport}}
	evidence := map[string]any{"peak_sampled_active_calls": uint64(0), "peak_sampled_established_calls": uint64(0), "status_sampling_errors": 0}
	return m, task, h, evidence
}

// TestBenchmarkFinalObservationFresh 验证失败/收敛阶段均只能发布本次读取的状态与响应时刻。
func TestBenchmarkFinalObservationFresh(t *testing.T) {
	for _, phase := range []string{"generator_exit_failure", "teardown_convergence"} {
		t.Run(phase, func(t *testing.T) {
			called := 0
			started := time.Now()
			m, task, h, evidence := benchmarkFinalFixture(func(r *http.Request) (*http.Response, error) {
				called++
				if r.Method != "GET" || r.URL.Path != "/v1/status" {
					t.Fatalf("意外请求: %s %s", r.Method, r.URL.Path)
				}
				deadline, ok := r.Context().Deadline()
				if !ok || deadline.After(time.Now().Add(1500*time.Millisecond)) {
					t.Fatal("最终证据读取没有1.5秒上限")
				}
				wire, err := json.Marshal(benchmarkProcessingStatusWire(t))
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(wire))), Header: make(http.Header)}, nil
			})
			status, err := m.observeFinal(context.Background(), task, h, evidence, phase)
			if err != nil || status == nil || len(status.Workers) != 1 || called != 1 {
				t.Fatalf("最终读取未成功: %v %+v", err, status)
			}
			at, err := time.Parse(time.RFC3339Nano, evidence["final_status_sampled_at"].(string))
			if err != nil || at.Before(started) || evidence["final_status_phase"] != phase || evidence["final_status_sampling_error"] != nil {
				t.Fatalf("最终阶段/时刻错误: %+v", evidence)
			}
			if len(task.run.Samples) != 1 || task.run.Samples[0].RXPackets != 123 || evidence["status_sampling_errors"] != 0 {
				t.Fatalf("真实样本未保留: %+v %+v", task.run.Samples, evidence)
			}
		})
	}
}

// TestBenchmarkFinalObservationFailureDoesNotReuseOldState 失败清空本次最终时刻，不伪造零样本或复用此前成功状态。
func TestBenchmarkFinalObservationFailureDoesNotReuseOldState(t *testing.T) {
	m, task, h, evidence := benchmarkFinalFixture(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("controlled read failure")
	})
	evidence["final_status_sampled_at"] = "2000-01-01T00:00:00Z"
	evidence["final_status_sampling_error"] = "old error"
	status, err := m.observeFinal(context.Background(), task, h, evidence, "generator_exit_failure")
	if err == nil || status != nil || evidence["final_status_sampled_at"] != nil || evidence["final_status_sampling_error"] == nil {
		t.Fatalf("失败复用了旧最终状态: %+v %v", evidence, err)
	}
	if len(task.run.Samples) != 0 || evidence["status_sampling_errors"] != 1 {
		t.Fatalf("失败产生假样本或遗漏错误计数: %+v", evidence)
	}
}

// TestBenchmarkFinalObservationPreservesParentDeadline 即使父任务即将到期，补证据也不能获得新的独立总预算。
func TestBenchmarkFinalObservationPreservesParentDeadline(t *testing.T) {
	parentDeadline := time.Now().Add(25 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()
	m, task, h, evidence := benchmarkFinalFixture(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || !deadline.Equal(parentDeadline) {
			t.Fatalf("原总期限被延长: %v -> %v", parentDeadline, deadline)
		}
		return nil, context.DeadlineExceeded
	})
	status, err := m.observeFinal(ctx, task, h, evidence, "generator_exit_failure")
	if !errors.Is(err, context.DeadlineExceeded) || status != nil || evidence["status_sampling_errors"] != 1 {
		t.Fatalf("到期没有按采样失败保留: %+v %v", evidence, err)
	}
}
