package media

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"rustswitch/control/internal/config"
	"testing"
	"time"
)

// TestMain 为管道故障测试提供不绑定媒体端口的独立替身进程，其他调用仍运行标准测试。
func TestMain(m *testing.M) {
	mode := os.Getenv("RUSTSWITCH_POOL_MEDIA_HELPER")
	if len(os.Args) > 2 && os.Args[1] == "--worker-config" && mode != "" {
		var cfg WorkerConfig
		if json.Unmarshal([]byte(os.Args[2]), &cfg) != nil {
			os.Exit(2)
		}
		encoder, decoder := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
		_ = encoder.Encode(Reply{OK: true, Type: "ready", ProtocolVersion: 1, WorkerID: cfg.WorkerID, PID: os.Getpid()})
		for {
			var request Request
			if decoder.Decode(&request) != nil {
				os.Exit(0)
			}
			reply := Reply{ID: request.ID, OK: true, Type: "ack"}
			switch request.Op {
			case "allocate":
				reply.Type = "allocated"
				reply.Session = request.Session
				reply.ARTP = cfg.PortStart
				reply.ARTCP = cfg.PortStart + 1
				reply.BRTP = cfg.PortStart + 2
				reply.BRTCP = cfg.PortStart + 3
			case "stats":
				reply.Type = "stats"
				reply.Stats = map[string]any{"active_calls": 0, "dtmf_send_packets": 0, "dtmf_send_errors": 0, "dtmf_send_cancelled_digits": 0, "dtmf_send_active": 0}
			}
			if request.Op != "stats" {
				switch mode {
				case "slow_allocate":
					if request.Op == "allocate" {
						_ = os.WriteFile(os.Getenv("RUSTSWITCH_POOL_ACCEPTED_MARKER"), []byte("accepted"), 0600)
						time.Sleep(200 * time.Millisecond)
					}
				case "wrong_type":
					reply.Type = "stats"
				case "wrong_session":
					reply.Session++
				case "wrong_ports":
					reply.BRTCP = cfg.PortEnd + 1
				case "release_error":
					reply.OK = false
					reply.Type = "error"
					reply.Message = "simulated release failure"
				case "hang":
					time.Sleep(time.Hour)
				}
			}
			if encoder.Encode(reply) != nil {
				os.Exit(0)
			}
		}
	}
	os.Exit(m.Run())
}

// faultPool 为每个测试创建独立的进程、管道和取消上下文，退出时只回收该测试所拥有的进程。
func faultPool(t *testing.T, mode string) *Pool {
	t.Helper()
	t.Setenv("RUSTSWITCH_POOL_MEDIA_HELPER", mode)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), config.Config{Media: config.Media{Binary: executable, Workers: 1, PortStart: 20000, PortEnd: 20199}, Limits: config.Limits{MaxCalls: 10}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// TestPoolRejectsMalformedSuccessfulReplies 复现 OK=true 时错误响应变体、会话或端口仍被当成成功的问题。
func TestPoolRejectsMalformedSuccessfulReplies(t *testing.T) {
	for _, mode := range []string{"wrong_type", "wrong_session", "wrong_ports"} {
		t.Run(mode, func(t *testing.T) {
			p := faultPool(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := p.Call(ctx, 0, Request{Op: "allocate", Session: 9}); err == nil {
				t.Fatalf("%s 协议错配被当成成功", mode)
			}
		})
	}
}

// TestPoolReleaseFailureEndsGeneration 复现媒体拒绝幂等释放后仍保持健康，导致通道无限重试而不回收的问题。
func TestPoolReleaseFailureEndsGeneration(t *testing.T) {
	p := faultPool(t, "release_error")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.Call(ctx, 0, Request{Op: "release", Session: 9}); err == nil {
		t.Fatal("释放失败未返回错误")
	}
	select {
	case failure := <-p.Failures:
		if failure.Generation != 1 {
			t.Fatalf("错误代次=%d", failure.Generation)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("释放契约失效后媒体仍保持健康，通道会无限重试")
	}
}

// TestPoolSubmissionQueueBoundAndClose 验证万次提交仍受固定队列约束，池退出不会留下等待中的控制任务。
func TestPoolSubmissionQueueBoundAndClose(t *testing.T) {
	p := faultPool(t, "hang")
	completed := make(chan error, 256)
	accepted := 0
	before := runtime.NumGoroutine()
	for i := 0; i < 10000; i++ {
		err := p.Submit(context.Background(), 0, Request{Op: "connect", Session: 1}, func(_ Reply, err error) { completed <- err })
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrQueueFull) {
			t.Fatal(err)
		}
	}
	if accepted > 65 || accepted < 64 {
		t.Fatalf("队列没有保持64+1在途上限：%d", accepted)
	}
	if growth := runtime.NumGoroutine() - before; growth > 4 {
		t.Fatalf("排队增加%d个goroutine", growth)
	}
	p.Close()
	if len(completed) != accepted {
		t.Fatalf("池退出只回收%d/%d任务", len(completed), accepted)
	}
	if err := p.Submit(context.Background(), 0, Request{Op: "release", Session: 1}, func(Reply, error) {}); err == nil {
		t.Fatal("池关闭后仍接受任务")
	}
}

// TestPoolHungCommandHasBoundedLifetime 检查不回响应的媒体进程会在原有一秒边界结束代次。
func TestPoolHungCommandHasBoundedLifetime(t *testing.T) {
	p := faultPool(t, "hang")
	started := time.Now()
	_, err := p.Call(context.Background(), 0, Request{Op: "connect", Session: 1})
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("未确认有界超时：%v duration=%v", err, time.Since(started))
	}
	select {
	case <-p.Failures:
	case <-time.After(time.Second):
		t.Fatal("超时未触发代次清理")
	}
}

// TestPoolCancellationKeepsInFlightAllocationOutcome 复现在途分配已经执行、调用者取消却丢弃资源确认的竞态。
// 命令进入媒体之后只等待原有一秒 RPC 边界内的确定结果，不把已分配资源当成从未执行。
func TestPoolCancellationKeepsInFlightAllocationOutcome(t *testing.T) {
	marker := t.TempDir() + "/accepted"
	t.Setenv("RUSTSWITCH_POOL_ACCEPTED_MARKER", marker)
	p := faultPool(t, "slow_allocate")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan result, 1)
	go func() { reply, err := p.Call(ctx, 0, Request{Op: "allocate", Session: 9}); done <- result{reply, err} }()
	for until := time.Now().Add(time.Second); ; {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("媒体未收到分配")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case outcome := <-done:
		if outcome.err != nil || !outcome.reply.OK || outcome.reply.Session != 9 {
			t.Fatalf("丢弃了在途资源确认：%+v err=%v", outcome.reply, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("在途结果等待无界")
	}
}
