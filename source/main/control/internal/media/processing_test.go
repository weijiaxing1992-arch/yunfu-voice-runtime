package media

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"rustswitch/control/internal/config"
)

// init 只在本文件专用子进程模式运行真实JSON管道夹具，不改变其他测试已有的TestMain。
// 夹具不绑定媒体端口、不解码音频，不作为worker功能实现或端到端通过证据。
func init() {
	mode := os.Getenv("RUSTSWITCH_PROCESSING_MEDIA_HELPER")
	if mode == "" || len(os.Args) < 3 || os.Args[1] != "--worker-config" {
		return
	}
	var cfg WorkerConfig
	if json.Unmarshal([]byte(os.Args[2]), &cfg) != nil {
		os.Exit(2)
	}
	capabilities := []string{mode}
	if mode == "valid" {
		capabilities = []string{"unrelated_feature", "processed_g711_v1"}
	}
	if strings.HasPrefix(mode, "local_") {
		capabilities = []string{"processed_g711_v1", "processed_g711_local_v1"}
	}
	encoder, decoder := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	ready := Reply{OK: true, Type: "ready", ProtocolVersion: 1, WorkerID: cfg.WorkerID, PID: os.Getpid(), Capabilities: capabilities}
	if mode == "ready_nonzero_id" || mode == "ready_max_id" {
		ready.Capabilities = []string{"processed_g711_v1", "processed_g711_local_v1"}
		ready.ID = 1
		if mode == "ready_max_id" {
			ready.ID = ^uint64(0)
		}
	}
	if encoder.Encode(ready) != nil {
		os.Exit(2)
	}
	// 父测试有三秒上下文和Close，夹具另限制请求总量，避免错误测试无限驱动子进程。
	for count := 0; count < 128; count++ {
		var request Request
		if decoder.Decode(&request) != nil {
			os.Exit(0)
		}
		reply := Reply{ID: request.ID, OK: true, Type: "ack"}
		switch request.Op {
		case "allocate":
			reply.Type, reply.Session = "allocated", request.Session
			reply.ARTP, reply.ARTCP, reply.BRTP, reply.BRTCP = cfg.PortStart, cfg.PortStart+1, cfg.PortStart+2, cfg.PortStart+3
			if request.Processing != nil {
				reply.ProcessingVersion = processingVersion(1)
				if request.Processing.Topology == "local" && mode != "local_missing_topology" {
					reply.ProcessingTopology = "local"
					if mode == "local_wrong_topology" {
						reply.ProcessingTopology = "bridge"
					}
				}
			}
		case "stats":
			reply.Type = "stats"
			// 处理模式握手后仍须持续履行统计合同；缺字段不能依赖测试在首个采样周期前结束。
			reply.Stats = map[string]any{
				"active_calls": 0, "dtmf_send_packets": 0, "dtmf_send_errors": 0, "dtmf_send_cancelled_digits": 0, "dtmf_send_active": 0,
				"processed_active_calls": 0, "processed_decoded_frames": 0, "processed_decoded_samples": 0,
				"processed_encoded_frames": 0, "processed_encoded_samples": 0, "processed_plc_frames": 0,
				"processed_missing_frames": 0, "processed_cn_packets": 0, "processed_send_errors": 0,
				"processed_jitter_lost": 0, "processed_jitter_late": 0, "processed_jitter_reordered": 0,
				"processed_jitter_duplicates": 0, "processed_jitter_overflow": 0, "processed_playout_expired": 0,
				"processed_send_deadline_misses": 0, "processed_output_lateness_ns_max": 0,
				"processed_output_lateness_buckets": [8]uint64{},
				"processed_aux_expired":             0, "processed_aux_timeouts": 0,
				"processed_queue_reset_drops": 0, "processed_source_resets": 0,
			}
			if strings.HasPrefix(mode, "local_") && mode != "local_missing_stats" {
				for _, key := range []string{"processed_local_active_calls", "processed_local_consumed_frames", "processed_local_consumed_samples", "processed_local_nonzero_frames", "processed_local_energy_max"} {
					reply.Stats[key] = 0
				}
			}
		}
		if encoder.Encode(reply) != nil {
			os.Exit(0)
		}
		if request.Op == "shutdown" {
			os.Exit(0)
		}
	}
	os.Exit(2)
}

// TestProcessingPlanIsIndependentAndLegacyWireStaysUnchanged 防止共享默认计划被单次呼叫修改，并保持旧worker启动JSON不含新字段。
func TestProcessingPlanIsIndependentAndLegacyWireStaysUnchanged(t *testing.T) {
	first, second := G711ProcessingPlan(), G711ProcessingPlan()
	first.Mode, first.JitterTargetMS = "other", 99
	if second.Version != 1 || second.Mode != "g711" || second.JitterTargetMS != 40 || second.MaxDelayMS != 120 {
		t.Fatalf("工厂复用了可变全局计划：%+v", second)
	}
	for _, value := range []any{Request{Op: "allocate", Session: 1}, WorkerConfig{Processing: "g711"}} {
		wire, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wire, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["processing"]; exists {
			t.Fatalf("旧请求或worker启动配置被新增处理字段污染：%s", wire)
		}
	}
	wire, err := json.Marshal(Request{Op: "allocate", Session: 1, Processing: second})
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(wire, &decoded); err != nil || decoded.Processing == nil || *decoded.Processing != *second {
		t.Fatalf("处理计划JSON丢失或变更：%s %v", wire, err)
	}
}

// TestProcessingAllocationRequiresExplicitMatchingVersion 精确版本和分配变体同时满足才是处理模式的有效回执。
func TestProcessingAllocationRequiresExplicitMatchingVersion(t *testing.T) {
	worker := Worker{Config: WorkerConfig{PortStart: 20000, PortEnd: 20039}}
	request := Request{Op: "allocate", Session: 7, Processing: G711ProcessingPlan()}
	valid := Reply{OK: true, Type: "allocated", Session: 7, ARTP: 20000, ARTCP: 20001, BRTP: 20002, BRTCP: 20003}
	for _, version := range []*uint32{nil, processingVersion(0), processingVersion(2)} {
		reply := valid
		reply.ProcessingVersion = version
		if err := worker.validateReply(request, reply); err == nil {
			t.Fatalf("缺失或不同版本被当作真实处理分配：%+v", reply)
		}
	}
	valid.ProcessingVersion = processingVersion(1)
	if err := worker.validateReply(request, valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Reply){
		func(reply *Reply) { reply.Type = "ack" },
		func(reply *Reply) { reply.Session++ },
		func(reply *Reply) { reply.BRTCP++ },
	} {
		reply := valid
		mutate(&reply)
		if err := worker.validateReply(request, reply); err == nil {
			t.Fatal("只凭版本忽略了错误分配所有权", reply)
		}
	}
}

// processingVersion 返回测试独占的版本地址，避免多个变体意外共享可变指针。
func processingVersion(value uint32) *uint32 { return &value }

// TestRelayAllocationCannotSilentlyBecomeProcessed 新worker必须保留旧请求的relay行为，不能用额外版本字段默默切换处理模式。
func TestRelayAllocationCannotSilentlyBecomeProcessed(t *testing.T) {
	worker := Worker{Config: WorkerConfig{PortStart: 20000, PortEnd: 20039}}
	request := Request{Op: "allocate", Session: 7}
	reply := Reply{OK: true, Type: "allocated", Session: 7, ARTP: 20000, ARTCP: 20001, BRTP: 20002, BRTCP: 20003}
	if err := worker.validateReply(request, reply); err != nil {
		t.Fatal("旧relay分配被拒绝", err)
	}
	reply.ProcessingVersion = processingVersion(1)
	if err := worker.validateReply(request, reply); err == nil {
		t.Fatal("未请求处理计划却接受worker声称已经启用处理")
	}
}

// TestProcessingStartupRejectsLegacyWorkerBeforeHealthy 启动真实子进程管道，缺能力的旧worker不能获得健康状态或处理调用入口。
func TestProcessingStartupRejectsLegacyWorkerBeforeHealthy(t *testing.T) {
	t.Setenv("RUSTSWITCH_POOL_MEDIA_HELPER", "normal")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := Start(ctx, config.Config{Media: config.Media{Binary: executable, Processing: "g711", Workers: 1, PortStart: 20000, PortEnd: 20039}, Limits: config.Limits{MaxCalls: 2}})
	if p != nil {
		p.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "processed_g711_v1") {
		t.Fatalf("旧worker缺能力却被当成可接纳处理呼叫：pool=%v err=%v", p != nil, err)
	}
}

// TestProcessingLegacyAllocationReplyFailsActualRPC 通过实际子进程RPC复现旧worker忽略processing字段，错误必须返回且结束失配代际。
func TestProcessingLegacyAllocationReplyFailsActualRPC(t *testing.T) {
	p := faultPool(t, "normal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.Call(ctx, 0, Request{Op: "allocate", Session: 7, Processing: G711ProcessingPlan()}); err == nil {
		t.Fatal("旧worker忽略processing却被当作成功分配")
	}
	select {
	case failure := <-p.Failures:
		if failure.Generation != 1 {
			t.Fatal("错误处理版本没有结束正确代际", failure.Generation)
		}
	case <-ctx.Done():
		t.Fatal("处理版本错配没有触发代际失败")
	}
}

// TestProcessingHandshakeUsesExactCapabilityAndAllocationVersion 正向处理握手必须精确声明能力；近似名称和未来版本不能被前缀匹配误接受。
func TestProcessingHandshakeUsesExactCapabilityAndAllocationVersion(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"processed_g711_v2", "processed_g711_v1_extra", "valid"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("RUSTSWITCH_PROCESSING_MEDIA_HELPER", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := Start(ctx, config.Config{Media: config.Media{Binary: executable, Processing: "g711", Workers: 1, PortStart: 20000, PortEnd: 20039}, Limits: config.Limits{MaxCalls: 2}})
			if p != nil {
				defer p.Close()
			}
			if mode != "valid" {
				if err == nil || !strings.Contains(err.Error(), "processed_g711_v1") {
					t.Fatalf("相似能力声明被误接受：%s %v", mode, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !p.Workers[0].Healthy.Load() || p.Workers[0].Generation.Load() != 1 {
				t.Fatal("完整握手没有建立明确的健康代际")
			}
			proof := p.Workers[0].CapabilitySnapshot()
			if !proof.Healthy || proof.Generation != 1 || proof.PID <= 0 || len(proof.Capabilities) != 2 || proof.Capabilities[1] != "processed_g711_v1" {
				t.Fatalf("真实 ready 能力未与进程身份发布：%+v", proof)
			}
			proof.Capabilities[1] = "caller_mutated"
			if p.Workers[0].CapabilitySnapshot().Capabilities[1] != "processed_g711_v1" {
				t.Fatal("管理读者修改了握手证据")
			}
			reply, err := p.Call(ctx, 0, Request{Op: "allocate", Session: 7, Processing: G711ProcessingPlan()})
			if err != nil || reply.ProcessingVersion == nil || *reply.ProcessingVersion != 1 || reply.Session != 7 {
				t.Fatalf("精确能力与版本没有完成分配合同：%+v %v", reply, err)
			}
			if _, err := p.Call(ctx, 0, Request{Op: "release", Session: 7}); err != nil {
				t.Fatal(err)
			}
			// 等到监督器真实的一秒周期发布快照，而非主动RPC后把未校验的回复当作采样成功。
			poll := time.NewTicker(10 * time.Millisecond)
			defer poll.Stop()
			for {
				if snapshot := p.Workers[0].Snapshot(); len(snapshot) != 0 {
					if err := validateProcessingStats(snapshot, 2); err != nil {
						t.Fatal("监督器发布了不完整的处理统计", err)
					}
					if !p.Workers[0].Healthy.Load() || p.Workers[0].Generation.Load() != 1 {
						t.Fatal("真实采集后健康状态或代际丢失")
					}
					t.Log("处理模式完成真实周期统计采集，完整字段与首个健康代际均保持")
					break
				}
				select {
				case failure := <-p.Failures:
					t.Fatal("首次周期统计导致处理worker失败", failure)
				case <-ctx.Done():
					t.Fatal("未收到监督器的真实周期统计", ctx.Err())
				case <-poll.C:
				}
			}
		})
	}
}
