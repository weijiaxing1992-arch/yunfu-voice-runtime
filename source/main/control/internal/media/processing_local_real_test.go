package media

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"rustswitch/control/internal/config"
)

// realLocalMediaBinary沿用已有真实媒体测试的候选路径环境名；
// 缺少程序或使用旧二进制必须真实失败，不跳过，也不回退到JSON测试替身。
func realLocalMediaBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("RUSTSWITCH_REAL_MEDIA")
	if path == "" {
		path = "../../../bin/rustswitch-media"
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("真实Rust候选不可执行，请设置RUSTSWITCH_REAL_MEDIA：%s %v", path, err)
	}
	return path
}

func realLocalBinaryHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// TestLocalProcessingRealRustHandshakeAndPoolLifecycle只验证实际启动边界及零负载生命周期。
// 先留存真实Ready原帧和正常shutdown，再经生产Pool核验同一程序的能力、统计与回收。
// 此测试不绑定媒体端口，不证明播放质量、ASR/TTS、原版兼容或并发容量。
func TestLocalProcessingRealRustHandshakeAndPoolLifecycle(t *testing.T) {
	binary := realLocalMediaBinary(t)
	before := realLocalBinaryHash(t, binary)
	cfg, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Media.Binary, cfg.Media.Processing, cfg.Media.PlaybackRoot = binary, "g711", ""
	cfg.Media.Workers, cfg.Media.PortStart, cfg.Media.PortEnd = 2, 42000, 42007
	cfg.Media.CPUCores, cfg.Media.ExcludedPortBlocks = nil, nil
	cfg.Limits.MaxCalls = 2
	wcfg := WorkerConfig{WorkerID: 37, BindIP: cfg.Media.BindIP, PortStart: 42000, PortEnd: 42003, MaxCalls: 1, ReceiveBufferBytes: cfg.Media.ReceiveBufferBytes, MaxPacketsPerSecondPerLeg: cfg.Media.MaxPacketsPerSecondPerLeg, AllowedRemoteNetworks: cfg.Media.AllowedRemoteNetworks}
	encoded, err := json.Marshal(wcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("wire_ready_and_clean_shutdown", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "--worker-config", string(encoded))
		cmd.Stderr = os.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waited := false
		defer func() {
			_ = stdin.Close()
			if !waited {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 16384)
		read := func() Reply {
			t.Helper()
			if !scanner.Scan() {
				t.Fatalf("真实Rust没有完整有界回复：%v ctx=%v", scanner.Err(), ctx.Err())
			}
			wire := append([]byte(nil), scanner.Bytes()...)
			t.Logf("actual_rust_ipc=%s", wire)
			var r Reply
			if json.Unmarshal(wire, &r) != nil {
				t.Fatal("真实回复不是JSON", string(wire))
			}
			return r
		}
		ready := read()
		if !ready.OK || ready.Type != "ready" || ready.ID != 0 || ready.ProtocolVersion != 1 || ready.WorkerID != 37 || ready.PID != cmd.Process.Pid || !slices.Contains(ready.Capabilities, "processed_g711_v1") || !slices.Contains(ready.Capabilities, "processed_g711_local_v1") {
			t.Fatalf("真实Ready与Go合同不符：%+v pid=%d", ready, cmd.Process.Pid)
		}
		if err = json.NewEncoder(stdin).Encode(Request{ID: 1, Op: "shutdown"}); err != nil {
			t.Fatal(err)
		}
		ack := read()
		if !ack.OK || ack.ID != 1 || ack.Type != "ack" {
			t.Fatal("实际shutdown未确认", ack)
		}
		_ = stdin.Close()
		if scanner.Scan() || scanner.Err() != nil {
			t.Fatal("shutdown后多余报文或读取失败", scanner.Text(), scanner.Err())
		}
		err = cmd.Wait()
		waited = true
		if err != nil || ctx.Err() != nil {
			t.Fatal("真实Rust未正常退出", err, ctx.Err())
		}
	})
	t.Run("production_pool_stats_and_close", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		p, err := Start(ctx, cfg)
		if err != nil {
			t.Fatal("生产Go监督器未接受真实Rust握手", err)
		}
		defer p.Close()
		pids := map[int64]bool{}
		for i, w := range p.Workers {
			proof := w.CapabilitySnapshot()
			if proof.WorkerID != i || proof.Generation != 1 || proof.PID <= 0 || pids[proof.PID] || !proof.Healthy || !w.SupportsLocalProcessing() {
				t.Fatal("实际进程身份或独立能力不完整", proof)
			}
			pids[proof.PID] = true
			stats, err := p.Call(ctx, i, Request{Op: "stats"})
			if err != nil {
				t.Fatal("真实统计未通过Go版本合同", err)
			}
			if err = validateProcessingStats(stats.Stats, 1); err != nil {
				t.Fatal(err)
			}
			if err = validateLocalProcessingStats(stats.Stats); err != nil {
				t.Fatal(err)
			}
			for key, value := range stats.Stats {
				if !strings.HasPrefix(key, "processed_") {
					continue
				}
				if key == "processed_output_lateness_buckets" {
					for _, count := range value.([]any) {
						if count != float64(0) {
							t.Fatal("无负载桶不是零", value)
						}
					}
				} else if value != float64(0) {
					t.Fatal("新进程无负载却有处理计数", key, value)
				}
			}
			wire, _ := json.Marshal(map[string]any{"identity": proof, "stats": stats.Stats})
			t.Logf("actual_pool_worker=%s", wire)
		}
		closed := make(chan struct{})
		go func() { p.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("Pool.Close未有界退出")
		}
		for _, w := range p.Workers {
			if w.Healthy.Load() || w.PID.Load() != 0 || len(w.CapabilitySnapshot().Capabilities) != 0 {
				t.Fatal("Close后仍发布旧身份或能力")
			}
		}
		for pid := range pids {
			if err := syscall.Kill(int(pid), 0); err != syscall.ESRCH {
				t.Fatalf("Pool.Close后真实worker仍存在 pid=%d err=%v", pid, err)
			}
		}
		if _, err := p.Call(context.Background(), 0, Request{Op: "stats"}); err == nil {
			t.Fatal("已关闭池仍接受命令")
		}
	})
	if after := realLocalBinaryHash(t, binary); after != before {
		t.Fatal("实际测试过程中候选二进制变化", before, after)
	}
	t.Logf("actual_rust_binary=%s sha256=%s", binary, before)
}
