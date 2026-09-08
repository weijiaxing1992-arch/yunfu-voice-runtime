package media

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"rustswitch/control/internal/config"
)

// TestLocalProcessingAdmissionFreezesPlanAndRejectsStaleCapabilities覆盖真正入队入口，
// 缺能力、上一代次或未知拓扑不能消耗队列；已受理计划不能被调用方改成桥接。
func TestLocalProcessingAdmissionFreezesPlanAndRejectsStaleCapabilities(t *testing.T) {
	w := &Worker{jobs: make(chan job, 2), urgent: make(chan job, 2), capabilities: []string{"processed_g711_v1", "processed_g711_local_v1"}}
	w.Healthy.Store(true)
	w.Generation.Store(7)
	w.PID.Store(42)
	p := &Pool{Workers: []*Worker{w}}
	for _, caps := range [][]string{nil, {"processed_g711_v1"}, {"processed_g711_local_v1"}, {"processed_g711_v1", "processed_g711_local_v1_extra"}} {
		w.capabilities = caps
		if w.SupportsLocalProcessing() || p.Submit(context.Background(), 0, Request{Op: "allocate", Session: 1, Processing: G711LocalProcessingPlan()}, func(Reply, error) {}) == nil || len(w.jobs) != 0 {
			t.Fatal("不完整能力却受理本地拓扑", caps)
		}
	}
	w.capabilities = []string{"processed_g711_v1", "processed_g711_local_v1"}
	for _, topology := range []string{"Local", "unknown", " local"} {
		plan := G711LocalProcessingPlan()
		plan.Topology = topology
		if p.Submit(context.Background(), 0, Request{Op: "allocate", Processing: plan}, func(Reply, error) {}) == nil {
			t.Fatal("未知拓扑进入RPC", topology)
		}
	}
	if p.Submit(context.Background(), 0, Request{Op: "allocate", Generation: 6, Processing: G711LocalProcessingPlan()}, func(Reply, error) {}) == nil {
		t.Fatal("旧代次利用新进程能力")
	}
	w.Healthy.Store(false)
	if w.SupportsLocalProcessing() || p.Submit(context.Background(), 0, Request{Op: "allocate", Processing: G711LocalProcessingPlan()}, func(Reply, error) {}) == nil {
		t.Fatal("失效进程保留本地准入")
	}
	w.Healthy.Store(true)
	w.PID.Store(0)
	if w.SupportsLocalProcessing() || p.Submit(context.Background(), 0, Request{Op: "allocate", Processing: G711LocalProcessingPlan()}, func(Reply, error) {}) == nil {
		t.Fatal("没有真实进程身份却接受本地请求")
	}
	w.PID.Store(42)
	w.Generation.Store(0)
	if w.SupportsLocalProcessing() || p.Submit(context.Background(), 0, Request{Op: "allocate", Processing: G711LocalProcessingPlan()}, func(Reply, error) {}) == nil {
		t.Fatal("没有有效代次却接受本地请求")
	}
	w.Generation.Store(7)
	plan := G711LocalProcessingPlan()
	if err := p.Submit(context.Background(), 0, Request{Op: "allocate", Session: 9, Processing: plan}, func(Reply, error) {}); err != nil {
		t.Fatal(err)
	}
	plan.Topology, plan.Version = "bridge", 2
	queued := <-w.jobs
	if queued.request.Generation != 7 || queued.request.Processing.Topology != "local" || queued.request.Processing.Version != 1 {
		t.Fatal("调用者改变了已受理的合同", queued.request)
	}
	// 显式bridge与省略topology共享旧合同，relay请求完全不新增processing字段。
	for _, plan := range []*ProcessingPlan{nil, G711ProcessingPlan()} {
		wire, err := json.Marshal(Request{Op: "allocate", Processing: plan})
		if err != nil || strings.Contains(string(wire), "topology") {
			t.Fatal("旧wire出现拓扑字段", string(wire), err)
		}
	}
}

// TestLocalProcessingReplyCannotChangeRequestedTopology覆盖三个既有/新增拓扑之间的边界，
// 不能用相同version掩盖local变bridge，也不能为未请求处理的relay附加拓扑。
func TestLocalProcessingReplyCannotChangeRequestedTopology(t *testing.T) {
	w := &Worker{Config: WorkerConfig{PortStart: 20000, PortEnd: 20039}}
	for _, requested := range []string{"relay", "", "bridge", "local"} {
		var plan *ProcessingPlan
		if requested != "relay" {
			plan = G711ProcessingPlan()
			plan.Topology = requested
		}
		for _, replied := range []string{"", "bridge", "local", "LOCAL", "unknown"} {
			reply := Reply{OK: true, Type: "allocated", Session: 3, ARTP: 20000, ARTCP: 20001, BRTP: 20002, BRTCP: 20003, ProcessingTopology: replied}
			if plan != nil {
				reply.ProcessingVersion = processingVersion(1)
			}
			want := requested == "relay" && replied == "" || requested == "local" && replied == "local" || (requested == "" || requested == "bridge") && (replied == "" || replied == "bridge")
			err := w.validateReply(Request{Op: "allocate", Session: 3, Processing: plan}, reply)
			if (err == nil) != want {
				t.Fatalf("request=%q reply=%q err=%v", requested, replied, err)
			}
		}
	}
}

// localProcessingPool运行真正子进程JSON管道，限定总请求数与期限；它只证明控制合同，不证明音频。
func localProcessingPool(t *testing.T, mode string) *Pool {
	t.Helper()
	t.Setenv("RUSTSWITCH_PROCESSING_MEDIA_HELPER", mode)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	p, err := Start(ctx, config.Config{Media: config.Media{Binary: executable, Processing: "g711", Workers: 1, PortStart: 20000, PortEnd: 20039}, Limits: config.Limits{MaxCalls: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// TestLocalProcessingActualRPCRequiresExactTopology防止只看version=1便把桥接分配当作本地处理。
func TestLocalProcessingActualRPCRequiresExactTopology(t *testing.T) {
	for _, mode := range []string{"local_valid", "local_missing_topology", "local_wrong_topology", "valid"} {
		t.Run(mode, func(t *testing.T) {
			p := localProcessingPool(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			reply, err := p.Call(ctx, 0, Request{Op: "allocate", Session: 5, Processing: G711LocalProcessingPlan()})
			if mode == "local_valid" {
				if err != nil || reply.ProcessingTopology != "local" || reply.ProcessingVersion == nil || *reply.ProcessingVersion != 1 || !p.Workers[0].SupportsLocalProcessing() {
					t.Fatal("完整本地握手没有完成实际分配", reply, err)
				}
				if _, err = p.Call(ctx, 0, Request{Op: "release", Session: 5}); err != nil {
					t.Fatal(err)
				}
				// 同一新worker仍能履行未声明local的旧桥接wire。
				reply, err = p.Call(ctx, 0, Request{Op: "allocate", Session: 6, Processing: G711ProcessingPlan()})
				if err != nil || reply.ProcessingTopology != "" {
					t.Fatal("新能力改变旧桥接回执", reply, err)
				}
				return
			}
			if err == nil {
				t.Fatal("缺失本地能力或回执仍成功", reply)
			}
			if mode == "valid" {
				if !p.Workers[0].Healthy.Load() || !strings.Contains(err.Error(), "processed_g711_local_v1") {
					t.Fatal("旧桥接worker应在入队前明确拒绝并保留桥接健康", err)
				}
				if _, err = p.Call(ctx, 0, Request{Op: "allocate", Session: 6, Processing: G711ProcessingPlan()}); err != nil {
					t.Fatal("本地拒绝破坏旧桥接", err)
				}
				return
			}
			select {
			case failure := <-p.Failures:
				if failure.Generation != 1 {
					t.Fatal("拓扑失配没有终止所属代次", failure)
				}
			case <-ctx.Done():
				t.Fatal("拓扑不明时没有隔离媒体进程")
			}
		})
	}
}

// TestLocalProcessingStatsCannotHideMissingOrImpossibleCounters区分本地消费与播放，
// 并实际驱动监督器stats RPC证明新cap不能只握手后缺少采样合同。
func TestLocalProcessingStatsCannotHideMissingOrImpossibleCounters(t *testing.T) {
	p := localProcessingPool(t, "local_valid")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := p.Call(ctx, 0, Request{Op: "stats"})
	if err != nil {
		t.Fatal(err)
	}
	stats := r.Stats
	stats["processed_active_calls"], stats["processed_local_active_calls"] = float64(1), float64(1)
	stats["processed_decoded_frames"], stats["processed_decoded_samples"], stats["processed_plc_frames"] = float64(2), float64(320), float64(1)
	stats["processed_local_consumed_frames"], stats["processed_local_consumed_samples"] = float64(3), float64(480)
	stats["processed_local_nonzero_frames"], stats["processed_local_energy_max"] = float64(2), float64(171798691840)
	// RX实际消费三帧、TX尚未播放零帧是合法本地事实。
	if err := p.Workers[0].validateReply(Request{Op: "stats"}, Reply{OK: true, Type: "stats", Stats: stats}); err != nil {
		t.Fatal("把本地独立RX/TX误当成桥接转码守恒", err)
	}
	for _, key := range []string{"processed_local_active_calls", "processed_local_consumed_frames", "processed_local_consumed_samples", "processed_local_nonzero_frames", "processed_local_energy_max"} {
		original := stats[key]
		for _, bad := range []any{nil, "0", float64(-1), 0.5, math.NaN(), math.Inf(1)} {
			stats[key] = bad
			if err := validateLocalProcessingStats(stats); err == nil {
				t.Fatal("无效本地字段当成零", key, bad)
			}
		}
		stats[key] = original
	}
	for key, bad := range map[string]float64{"processed_local_active_calls": 2, "processed_local_consumed_frames": 4, "processed_local_consumed_samples": 479, "processed_local_nonzero_frames": 4, "processed_local_energy_max": 171798691841} {
		old := stats[key]
		stats[key] = bad
		if validateLocalProcessingStats(stats) == nil {
			t.Fatal("物理不可能的统计被接受", key)
		}
		stats[key] = old
	}
}

// TestLocalProcessingMissingStatsFailsActualRPC新握手后的缺字段不是“尚无负载”，必须终止不履约代次。
func TestLocalProcessingMissingStatsFailsActualRPC(t *testing.T) {
	p := localProcessingPool(t, "local_missing_stats")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.Call(ctx, 0, Request{Op: "stats"}); err == nil {
		t.Fatal("新能力缺少本地采样字段仍发布成功")
	}
	select {
	case <-p.Failures:
	case <-ctx.Done():
		t.Fatal("缺字段后未隔离不履约worker")
	}
}

// TestProcessingStartupRejectsNonzeroReadyID启动通知不是任何请求的回复，ID只能为零。
// 同时具备正确PID、worker、版本与两项能力也不能使非零ID的通知获得健康状态。
func TestProcessingStartupRejectsNonzeroReadyID(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ready_nonzero_id", "ready_max_id"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("RUSTSWITCH_PROCESSING_MEDIA_HELPER", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			p, err := Start(ctx, config.Config{Media: config.Media{Binary: executable, Processing: "g711", Workers: 1, PortStart: 20000, PortEnd: 20039}, Limits: config.Limits{MaxCalls: 2}})
			if p != nil {
				p.Close()
				t.Fatal("错误ready ID暴露了可用进程池")
			}
			if err == nil || !strings.Contains(err.Error(), "invalid media handshake") {
				t.Fatal("非零启动ID未被精确拒绝", err)
			}
		})
	}
}
