package asr

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"rustswitch/control/internal/asr/resample"
	"rustswitch/control/internal/media"
)

// 本层启动独立代理并走真实Unix字节；RX输入仍是明确的测试Source，不冒充SIP/Rust验收。
func TestASRRealIndependentProvider(t *testing.T) {
	bin, output := os.Getenv("RUSTSWITCH_ASR_MOCK_BIN"), os.Getenv("RUSTSWITCH_ASR_REAL_OUTPUT")
	if bin == "" || output == "" {
		t.Skip("需显式独立代理二进制与新证据目录")
	}
	if !filepath.IsAbs(bin) || !filepath.IsAbs(output) {
		t.Fatal("必须绝对路径")
	}
	for _, tc := range []struct {
		name, fault string
		rate        uint32
		gate        bool
	}{
		{"native_8k", "none", 8000, false}, {"resampled_16k", "none", 16000, false}, {"midstream_first_decoded_8k", "none", 8000, false},
		{"wrong_done", "wrong-done", 8000, false}, {"missing_done", "close-before-done", 8000, false},
		{"wrong_token", "wrong-token", 8000, false}, {"duplicate_final", "duplicate-final", 8000, false},
		{"expired_consumption", "consume-pause", 8000, false}, {"hangup_late_final", "none", 8000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(output, tc.name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			// 两个平台均有短路径/tmp；Darwin专用/private/tmp会让Linux真实验收在建目录时失败。
			private, err := os.MkdirTemp("/tmp", "asr-real-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(private)
			socket := filepath.Join(private, "provider.sock")
			artifacts := filepath.Join(dir, "provider")
			args := []string{"--listen", socket, "--artifacts", artifacts, "--max-connections", "1", "--max-total-connections", "1", "--fault", tc.fault, "--delay-ms", "200"}
			if tc.gate {
				args = append(args, "--final-gate")
			}
			log, err := os.OpenFile(filepath.Join(dir, "process.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(bin, args...)
			cmd.Stdout = log
			cmd.Stderr = log
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			var stream *Stream
			u := newUnitSource()
			var events []Event
			var frames []media.RXFrame
			var before Snapshot
			defer func() {
				if stream != nil {
					stream.Cancel()
					u.mu.Lock()
					u.retired = true
					u.mu.Unlock()
					stream.Reap(context.Background())
					select {
					case <-stream.Closed():
					case <-time.After(2 * time.Second):
						t.Error("客户端资源未退出")
					}
				}
				_ = cmd.Process.Signal(syscall.SIGTERM)
				forced := false
				var waitErr error
				select {
				case waitErr = <-wait:
				case <-time.After(3 * time.Second):
					forced = true
					_ = cmd.Process.Kill()
					waitErr = <-wait
					t.Error("自有代理未按时退出")
				}
				_, socketErr := os.Lstat(socket)
				if !errors.Is(socketErr, os.ErrNotExist) {
					t.Errorf("自有socket未清理: %v", socketErr)
				}
				if waitErr != nil {
					t.Errorf("代理进程退出异常: %v", waitErr)
				}
				receipt := map[string]any{"schema": "asr-real-client-v1", "case": tc.name, "provider_pid": cmd.Process.Pid, "provider_wait_error": fmt.Sprint(waitErr), "forced": forced, "socket_removed": errors.Is(socketErr, os.ErrNotExist), "sample_rate": tc.rate, "rx_source": "unit_source_not_rust", "recognition_model": false, "snapshot_before_cleanup": before, "events": events, "input_frames": frames, "failed": t.Failed()}
				raw, _ := json.MarshalIndent(receipt, "", "  ")
				if e := os.WriteFile(filepath.Join(dir, "receipt.json"), raw, 0600); e != nil {
					t.Error(e)
				}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, e := os.Stat(filepath.Join(artifacts, "ready.json")); e == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("代理未就绪")
				}
				time.Sleep(5 * time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err = Start(ctx, context.Background(), Options{SocketPath: socket, UUID: tc.name, SubscriptionID: 7, SampleRate: tc.rate}, func(context.Context) (Source, error) { return u, nil })
			if err != nil {
				t.Fatal(err)
			}
			first := uint64(2)
			if tc.name != "midstream_first_decoded_8k" {
				boundary := unitFrame(t, media.RXSourceBoundary, 1)
				u.frames <- boundary
				frames = append(frames, boundary)
			} else {
				first = 1
			}
			var expected []byte
			converter := resample.New()
			for i := first; i < first+4; i++ {
				f := unitFrame(t, media.RXDecoded, i)
				for j := range f.PCM {
					f.PCM[j] += int16(i * 31)
				}
				frames = append(frames, f)
				u.frames <- f
				if tc.rate == 8000 {
					for _, v := range f.PCM {
						expected = binary.LittleEndian.AppendUint16(expected, uint16(v))
					}
				} else {
					var out [320]int16
					converter.Process(&f.PCM, &out)
					for _, v := range out {
						expected = binary.LittleEndian.AppendUint16(expected, uint16(v))
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err = stream.Finish(ctx); err != nil && (tc.fault == "none" || tc.fault == "duplicate-final" || tc.fault == "wrong-done" || tc.fault == "close-before-done") {
				t.Fatal(err)
			}
			if tc.gate {
				for {
					raw, _ := os.ReadFile(filepath.Join(artifacts, "connection-0001", "events.jsonl"))
					if strings.Contains(string(raw), "waiting-final") {
						break
					}
					if ctx.Err() != nil {
						t.Fatal("未到final等待窗口")
					}
					time.Sleep(5 * time.Millisecond)
				}
				close(u.life)
				if err = os.WriteFile(filepath.Join(artifacts, "final.release"), []byte("release"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stream.Reap(ctx)
			var nextErr error
			for {
				e, eerr := stream.Next(ctx)
				if eerr != nil {
					nextErr = eerr
					break
				}
				events = append(events, e)
			}
			before = stream.Snapshot()
			if tc.fault == "none" && !tc.gate {
				if nextErr != io.EOF || before.State != "completed" || before.Samples != uint64(tc.rate/50*4) || before.ProviderAcknowledgedSamples != before.Samples {
					t.Fatalf("未正常完成: %v %+v", nextErr, before)
				}
				hash := sha256.Sum256(expected)
				want := "mock-sha256:" + hex.EncodeToString(hash[:])
				final := false
				for _, e := range events {
					if e.Type == "final" {
						final = true
						if e.Text != want {
							t.Fatalf("代理实际PCM摘要错误: %s != %s", e.Text, want)
						}
					}
				}
				if !final {
					t.Fatal("没有final")
				}
			} else if nextErr == nil || nextErr == io.EOF || before.ProviderDone {
				t.Fatalf("故障被伪造为成功: %v %+v", nextErr, before)
			}
			if tc.gate && len(events) != 0 {
				t.Fatal("挂机后仍交付旧结果")
			}
			if tc.gate {
				// 等待独立代理真的尝试迟到final并结算；不能让清理SIGTERM抢先替代目标故障。
				for {
					if _, e := os.Stat(filepath.Join(artifacts, "connection-0001", "receipt.json")); e == nil {
						break
					}
					if ctx.Err() != nil {
						t.Fatal("独立代理未结算迟到final写入")
					}
					time.Sleep(5 * time.Millisecond)
				}
				raw, e := os.ReadFile(filepath.Join(artifacts, "connection-0001", "events.jsonl"))
				if e != nil || !strings.Contains(string(raw), "final-write-attempt") {
					t.Fatalf("未保存实际迟到final写尝试: %v", e)
				}
			}
		})
	}
}
