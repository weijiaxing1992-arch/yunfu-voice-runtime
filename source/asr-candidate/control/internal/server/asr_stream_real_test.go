package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"rustswitch/control/internal/asr"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
)

// asrSIPObserved 只装饰真实Server RX Source作有界证据记录，不注入音频、不替换ACK授权或媒体清理。
type asrSIPObserved struct {
	asr.Source
	trace  *rxSIPTrace
	mu     sync.Mutex
	frames []media.RXFrame
}

func (o *asrSIPObserved) Read(ctx context.Context) (media.RXFrame, error) {
	f, err := o.Source.Read(ctx)
	if err != nil {
		o.trace.record("actual_rx_read_error", err.Error())
		return f, err
	}
	o.mu.Lock()
	if len(o.frames) >= 256 {
		o.mu.Unlock()
		return media.RXFrame{}, errors.New("真实ASR RX证据超过256条预算")
	}
	o.frames = append(o.frames, f)
	o.mu.Unlock()
	domain, now, clockErr := rxSIPClock()
	if clockErr != nil {
		return media.RXFrame{}, clockErr
	}
	o.trace.record("actual_rx_sdk_frame", map[string]any{"frame": f, "capture_clock_ns": now, "capture_clock_domain": domain})
	return f, nil
}
func (o *asrSIPObserved) records() []media.RXFrame {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]media.RXFrame(nil), o.frames...)
}

// asrSIPInput 以独立G.711码字模式构造RTP；端口来自既有4000..4799预留器，不占主媒体范围。
func asrSIPInput(payload uint8, index int) []byte {
	p := make([]byte, 172)
	p[0], p[1] = 0x80, payload
	if index == 0 {
		p[1] |= 0x80
	}
	binary.BigEndian.PutUint16(p[2:], uint16(65532+index))
	binary.BigEndian.PutUint32(p[4:], uint32(uint64(0xfffffc00)+uint64(index)*160))
	binary.BigEndian.PutUint32(p[8:], 0x7a123456)
	for i := 0; i < 160; i++ {
		p[12+i] = byte(index*47 + i*13 + 31)
	}
	return p
}

func asrSIPSendBatch(t *testing.T, trace *rxSIPTrace, rtp *net.UDPConn, target *net.UDPAddr, payload uint8, startIndex, count int, inputs map[uint16][]byte) {
	t.Helper()
	start := time.Now()
	for n := 0; n < count; n++ {
		due := start.Add(time.Duration(n) * 20 * time.Millisecond)
		if delay := time.Until(due); delay > 0 {
			time.Sleep(delay)
		}
		late := time.Since(due)
		trace.record("generator_deadline", map[string]any{"index": startIndex + n, "late_ns": late.Nanoseconds()})
		if late >= 20*time.Millisecond {
			t.Fatal("真实发生器迟到一帧，不能追赶伪造通过")
		}
		p := asrSIPInput(payload, startIndex+n)
		inputs[binary.BigEndian.Uint16(p[2:])] = p
		rxSIPSend(t, trace, rtp, target, p, "rtp_send")
	}
}

// asrSIPFiles 绑定实际测试、控制嵌入资源、依赖、配置与Rust源码；证据及可执行文件放在目录外，不自引用。
func asrSIPFiles(project string) (map[string]string, error) {
	result := map[string]string{}
	for _, root := range []string{"control", "media/src", "config"} {
		err := filepath.WalkDir(filepath.Join(project, root), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("源码中不接受符号链接：%s", path)
			}
			rel, err := filepath.Rel(project, path)
			if err != nil {
				return err
			}
			sum, err := rxSIPHash(path)
			if err != nil {
				return err
			}
			result[filepath.ToSlash(rel)] = sum
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, p := range []string{"media/Cargo.toml", "media/Cargo.lock"} {
		sum, err := rxSIPHash(filepath.Join(project, p))
		if err != nil {
			return nil, err
		}
		result[p] = sum
	}
	return result, nil
}

func asrSIPNewJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, werr := f.Write(append(raw, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	if err = errors.Join(werr, serr, cerr); err != nil {
		t.Fatal(err)
	}
}

// TestRealASRServerSIPPCMAndCleanup 使用真实SIP/Rust/ASR1独立进程；mock仅返回PCM摘要，不能据此声称识别模型质量。
func TestRealASRServerSIPPCMAndCleanup(t *testing.T) {
	mediaBin, mockBin, output := os.Getenv("RUSTSWITCH_REAL_MEDIA"), os.Getenv("RUSTSWITCH_ASR_MOCK_BIN"), os.Getenv("RUSTSWITCH_ASR_SIP_OUTPUT")
	if mockBin == "" && output == "" {
		t.Skip("未显式提供真实Rust、独立ASR模拟进程及新原证据目录")
	}
	if mediaBin == "" || mockBin == "" || output == "" || !filepath.IsAbs(mediaBin) || !filepath.IsAbs(mockBin) || !filepath.IsAbs(output) {
		t.Fatal("真实ASR SIP验收需要三个绝对路径环境变量")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal("验收输出根必须为不存在的新目录：", err)
	}
	project, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	before, err := asrSIPFiles(project)
	if err != nil {
		t.Fatal(err)
	}
	asrSIPNewJSON(t, filepath.Join(output, "source-before.json"), before)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaries := map[string]string{}
	for _, path := range []string{mediaBin, mockBin, self} {
		sum, err := rxSIPHash(path)
		if err != nil {
			t.Fatal(err)
		}
		binaries[path] = sum
	}
	defer func() {
		after, err := asrSIPFiles(project)
		if err != nil {
			t.Error(err)
		}
		asrSIPNewJSON(t, filepath.Join(output, "source-after.json"), after)
		if !reflect.DeepEqual(before, after) {
			t.Error("实际SIP/ASR验收期间源码或嵌入资源变化")
		}
		for path, sum := range binaries {
			after, err := rxSIPHash(path)
			if err != nil || after != sum {
				t.Error("实际二进制身份变化：", path, err)
			}
		}
		asrSIPNewJSON(t, filepath.Join(output, "suite-receipt.json"), map[string]any{"schema": "asr-sip-rust-v1", "binaries": binaries, "source_unchanged": reflect.DeepEqual(before, after), "failed": t.Failed(), "recognition_model": false, "main_service_touched": false, "scope": "真实隔离SIP、Rust RX、Server.StartASR与独立Unix代理；不证明物理Linux容量或真实识别供应商"})
	}()
	for _, tc := range []struct {
		name            string
		payload         uint8
		rate            uint32
		connected, late bool
	}{
		{"pcmu_8k_udp", 0, 8000, false, false}, {"pcma_8k_connected", 8, 8000, true, false},
		{"pcmu_16k_udp", 0, 16000, false, false}, {"pcma_16k_connected", 8, 16000, true, false},
		{"pcmu_8k_late_subscription", 0, 8000, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(output, tc.name)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(filepath.Join(dir, "wire.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			trace := &rxSIPTrace{file: file, started: time.Now()}
			defer trace.finish(t)
			trace.record("identity", map[string]any{"test": t.Name(), "binaries": binaries, "sample_rate": tc.rate, "payload": tc.payload, "connected": tc.connected, "late_subscription": tc.late, "recognition_model": false})
			// /tmp短目录同时适用于Linux和Darwin；这里只删除本测试创建的空目录及mock自己清理后的socket。
			private, err := os.MkdirTemp("/tmp", "asr-sip-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(private)
			socket, providerDir := filepath.Join(private, "provider.sock"), filepath.Join(dir, "provider")
			processLog, err := os.OpenFile(filepath.Join(dir, "provider-process.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer processLog.Close()
			cmd := exec.Command(mockBin, "--listen", socket, "--artifacts", providerDir, "--max-connections", "1", "--max-total-connections", "2")
			cmd.Stdout, cmd.Stderr = processLog, processLog
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			providerWait := make(chan error, 1)
			go func() { providerWait <- cmd.Wait() }()
			defer func() {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				forced := false
				var waitErr error
				select {
				case waitErr = <-providerWait:
				case <-time.After(3 * time.Second):
					forced = true
					_ = cmd.Process.Kill()
					waitErr = <-providerWait
					t.Error("仅本测试拥有的mock未按SIGTERM退出")
				}
				_, socketErr := os.Lstat(socket)
				removed := errors.Is(socketErr, os.ErrNotExist)
				trace.record("provider_cleanup", map[string]any{"pid": cmd.Process.Pid, "forced": forced, "wait_error": fmt.Sprint(waitErr), "socket_removed": removed})
				if waitErr != nil || !removed {
					t.Error("mock没有正常完整清理：", waitErr, socketErr)
				}
			}()
			rxSIPWait(t, 3*time.Second, func() bool { _, err := os.Stat(filepath.Join(providerDir, "ready.json")); return err == nil }, "独立ASR进程没有就绪")
			base, sockets, adminReservation := rxSIPReserve(t)
			defer func() {
				for _, c := range sockets {
					_ = c.Close()
				}
				_ = adminReservation.Close()
			}()
			cfg, err := config.Load("../../../config/local.json")
			if err != nil {
				t.Fatal(err)
			}
			cfg.SourcePath = ""
			cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
			cfg.SIP.Listen = fmt.Sprintf("127.0.0.1:%d", base+4)
			cfg.SIP.Advertise = cfg.SIP.Listen
			cfg.SIP.Upstream = fmt.Sprintf("127.0.0.1:%d", base+9)
			cfg.SIP.Stream, cfg.SIP.Registration, cfg.SIP.TrunkAuth, cfg.SIP.Dialplan = nil, nil, nil, nil
			cfg.SIP.LocalExtensions = []string{"1000"}
			cfg.SIP.SetupTimeoutMS, cfg.SIP.AckTimeoutMS = 3000, 3000
			cfg.SIP.MaxCallSeconds = 60
			cfg.Admin.Listen = fmt.Sprintf("127.0.0.1:%d", base+10)
			cfg.PCMStream = nil
			cfg.ASRStream = &config.ASRStream{SocketPath: socket, SampleRate: tc.rate, MaxStreams: 1}
			cfg.Media.Binary, cfg.Media.Processing, cfg.Media.PlaybackRoot = mediaBin, "g711", ""
			cfg.Media.Workers, cfg.Media.PortStart, cfg.Media.PortEnd = 1, base, base+3
			cfg.Media.CPUCores, cfg.Media.ExcludedPortBlocks = nil, nil
			cfg.Media.PortReuseDelayMS, cfg.Media.AdaptiveAdmission, cfg.Media.ConnectSockets = 0, false, tc.connected
			cfg.Media.MaxPacketsPerSecondPerLeg = 1000
			cfg.Limits.MaxCalls, cfg.Limits.CallsPerSecond, cfg.Limits.BurstCalls, cfg.Limits.MaxTransactions = 1, 2, 2, 32
			if err = cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			trace.record("actual_config", cfg)
			for _, c := range sockets[:5] {
				_ = c.Close()
			}
			_ = adminReservation.Close()
			service, cancelService := context.WithCancel(context.Background())
			s, err := New(service, cfg)
			if err != nil {
				cancelService()
				t.Fatal(err)
			}
			created := make(chan string, 4)
			s.SetCompatibilityEvents(func(name string, h map[string]string, body []byte) {
				trace.record("actual_esl_event", map[string]any{"name": name, "headers": h, "body_hex": hex.EncodeToString(body)})
				if name == "CHANNEL_CREATE" {
					select {
					case created <- h["Unique-ID"]:
					default:
					}
				}
			})
			var observed *asrSIPObserved
			originalAcquire := s.asr.acquire
			s.asr.acquire = func(ctx context.Context, uuid string, id uint64) (asr.Source, error) {
				source, err := originalAcquire(ctx, uuid, id)
				trace.record("actual_rx_acquire", map[string]any{"uuid": uuid, "subscription_id": id, "error": fmt.Sprint(err), "has_source": source != nil})
				if source == nil {
					return nil, err
				}
				observed = &asrSIPObserved{Source: source, trace: trace}
				return observed, err
			}
			stop, runDone := make(chan struct{}), make(chan error, 1)
			go func() { runDone <- s.Run(stop) }()
			runFinished, normalShutdown := false, false
			var stream *asr.Stream
			defer func() {
				if stream != nil {
					stream.Cancel()
				}
				cancelService()
				if !runFinished {
					select {
					case err := <-runDone:
						trace.record("failure_run_exit", fmt.Sprint(err))
					case <-time.After(5 * time.Second):
						t.Error("自有Server.Run未退出")
					}
				}
				s.Close()
				trace.record("server_cleanup", map[string]any{"normal_drain": normalShutdown, "asr": s.ASRSnapshot(), "active_calls": s.Stats.Active.Load(), "established": s.Stats.Established.Load(), "timers": s.Stats.Timers.Load(), "pool_close": "existing supervisor Kill+Wait; not Rust zero-exit evidence", "test_failed": t.Failed()})
				if !normalShutdown || s.ASRSnapshot().Slots != 0 {
					t.Error("未完成正常SIP/ASR资源清理")
				}
			}()
			capability := s.pool.Workers[0].CapabilitySnapshot()
			trace.record("actual_media_ready", capability)
			if !capability.Healthy || !s.pool.Workers[0].SupportsRX() {
				t.Fatal("真实候选没有就绪RXS2能力")
			}
			caller, stranger, rtp := sockets[5], sockets[6], sockets[7]
			target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: base + 4}
			mediaTarget := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: base}
			callID := fmt.Sprintf("asr-real-%d-%s", os.Getpid(), tc.name)
			from, to := "<sip:caller@local>;tag=asr-caller", "<sip:1000@local>"
			uri := "sip:1000@" + cfg.SIP.Listen
			rxSIPSend(t, trace, caller, target, sip.Request("INVITE", uri, caller.LocalAddr().String(), "z9hG4bK-asr-invite", from, to, callID, 1, rxSIPSDP(tc.payload, base+7, base+8)), "sip_send")
			answer := rxSIPResponse(t, trace, caller, target, callID, "INVITE")
			if answer.Status != 200 || !strings.Contains(string(answer.Body), fmt.Sprintf("m=audio %d RTP/AVP %d", base, tc.payload)) || !strings.Contains(string(answer.Body), "c=IN IP4 127.0.0.1") {
				t.Fatal("本地INVITE/实际媒体SDP不符")
			}
			var uuid string
			select {
			case uuid = <-created:
			case <-time.After(time.Second):
				t.Fatal("没有实际通话UUID")
			}
			if len(uuid) != 36 {
				t.Fatal("实际UUID格式不符")
			}
			rxSIPSend(t, trace, stranger, target, sip.Request("ACK", uri, stranger.LocalAddr().String(), "z9hG4bK-asr-wrong-ack", from, answer.Header("to"), callID, 1, nil), "sip_wrong_source_ack")
			barrier := callID + "-barrier"
			rxSIPSend(t, trace, stranger, target, sip.Request("OPTIONS", uri, stranger.LocalAddr().String(), "z9hG4bK-asr-barrier", from, to, barrier, 1, nil), "sip_send")
			if rxSIPResponse(t, trace, stranger, target, barrier, "OPTIONS").Status != 200 {
				t.Fatal("错误ACK屏障失败")
			}
			startCtx, startCancel := context.WithTimeout(service, 3*time.Second)
			bad, badErr := s.StartASR(startCtx, uuid, 1)
			startCancel()
			trace.record("wrong_ack_asr_start", map[string]any{"error": fmt.Sprint(badErr), "has_stream": bad != nil, "asr": s.ASRSnapshot()})
			if bad != nil || !errors.Is(badErr, ErrRXNotEligible) || s.Stats.Established.Load() != 0 || s.ASRSnapshot().Slots != 0 {
				if bad != nil {
					bad.Cancel()
				}
				t.Fatal("错误ACK实际开放了ASR或丢失清理额度：", badErr)
			}
			rxSIPWait(t, time.Second, func() bool {
				_, err := os.Stat(filepath.Join(providerDir, "connection-0001", "receipt.json"))
				return err == nil
			}, "拒绝ACK的握手连接没有关闭证据")
			rejectedWire := asrSIPMessages(t, filepath.Join(providerDir, "connection-0001", "inbound.bin"))
			if len(rejectedWire) != 1 || rejectedWire[0].kind != 1 {
				t.Fatal("错误ACK的真实连接发送了HELLO以外的数据")
			}
			rxSIPSend(t, trace, caller, target, sip.Request("ACK", uri, caller.LocalAddr().String(), "z9hG4bK-asr-correct-ack", from, answer.Header("to"), callID, 1, nil), "sip_send")
			rxSIPWait(t, time.Second, func() bool { return s.Stats.Established.Load() == 1 }, "正确ACK没有实际建立通话")
			inputs := map[uint16][]byte{}
			preFrames, startIndex := 0, 0
			if tc.late {
				preFrames, startIndex = 4, 20
				asrSIPSendBatch(t, trace, rtp, mediaTarget, tc.payload, 0, preFrames, inputs)
				rxSIPWait(t, time.Second, func() bool {
					ctx, cancel := context.WithTimeout(service, 200*time.Millisecond)
					defer cancel()
					r, err := s.pool.Call(ctx, 0, media.Request{Op: "stats", Generation: capability.Generation})
					if err != nil || !r.OK {
						return false
					}
					return rxSIPStat(t, r.Stats, "processed_decoded_frames") == uint64(preFrames) && rxSIPStat(t, r.Stats, "processed_plc_frames") >= 6
				}, "晚订阅前真实媒体没有消费预先输入并结束有限PLC")
				// 真实状态已观察到六帧有限PLC；多等一个播放周期，让挂起边界完成在旧订阅之外。
				time.Sleep(40 * time.Millisecond)
				trace.record("late_subscription_precondition", map[string]any{"decoded_before_subscribe": preFrames, "minimum_plc_before_subscribe": 6, "next_input_index": startIndex})
				// 预先送音仍保留原始RTP证据，但不能成为ASR期望输入，避免重放旧帧也能匹配oracle。
				inputs = map[uint16][]byte{}
			}
			startCtx, startCancel = context.WithTimeout(service, 3*time.Second)
			stream, err = s.StartASR(startCtx, uuid, 1)
			startCancel()
			if err != nil || stream == nil || observed == nil {
				t.Fatal("正确ACK后的真实ASR链起建失败：", err)
			}
			trace.record("actual_asr_start", map[string]any{"snapshot": stream.Snapshot(), "rx": observed.Snapshot(), "manager": s.ASRSnapshot()})
			rxContext, rxCancel := context.WithTimeout(service, time.Second)
			rxState, rxErr := observed.Status(rxContext)
			rxCancel()
			trace.record("actual_rust_rx_status", map[string]any{"reply": rxState, "error": fmt.Sprint(rxErr)})
			if rxErr != nil || !rxState.OK || rxState.Type != "rx_state" || rxState.State != "active" || rxState.SubscriptionID != 1 || rxState.Session != observed.Snapshot().Session {
				t.Fatal("真实Rust没有确认同一活动RX订阅：", rxErr)
			}
			const frames = 8
			asrSIPSendBatch(t, trace, rtp, mediaTarget, tc.payload, startIndex, frames, inputs)
			rxSIPWait(t, 2*time.Second, func() bool { return stream.Snapshot().AudioFrames >= frames || stream.Snapshot().Error != "" }, "真实8帧未被ASR完整提交")
			if st := stream.Snapshot(); st.AudioFrames != frames || st.Error != "" {
				t.Fatalf("真实音频未完整提交：%+v", st)
			}
			finishCtx, finishCancel := context.WithTimeout(service, 3*time.Second)
			defer finishCancel()
			if err = stream.Finish(finishCtx); err != nil {
				t.Fatal("真实FINISH未写完：", err)
			}
			var events []asr.Event
			for len(events) < 32 {
				event, err := stream.Next(finishCtx)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal("真实识别代理结果失败：", err)
				}
				events = append(events, event)
				trace.record("actual_asr_result", event)
			}
			st := stream.Snapshot()
			if st.State != "completed" || !st.ProviderDone || !st.ResourcesClosed || st.Samples != uint64(frames)*uint64(tc.rate/50) || st.ProviderAcknowledgedSamples != st.Samples {
				t.Fatalf("实际结果/媒体清理未完成：%+v", st)
			}
			rxSIPWait(t, time.Second, func() bool { return s.ASRSnapshot().Slots == 0 }, "实际Closed没有归还ASR额度")
			providerConnection := filepath.Join(providerDir, "connection-0002")
			rxSIPWait(t, time.Second, func() bool { _, err := os.Stat(filepath.Join(providerConnection, "receipt.json")); return err == nil }, "供应商成功连接没有原始收据")
			asrSIPVerifyProvider(t, trace, providerConnection, observed.records(), inputs, events, tc.payload, tc.rate, tc.late, frames)
			rxSIPSend(t, trace, caller, target, sip.Request("BYE", uri, caller.LocalAddr().String(), "z9hG4bK-asr-bye", from, answer.Header("to"), callID, 2, nil), "sip_send")
			if rxSIPResponse(t, trace, caller, target, callID, "BYE").Status != 200 {
				t.Fatal("真实BYE没有正常应答")
			}
			rxSIPWait(t, 2*time.Second, func() bool { return s.Stats.Active.Load() == 0 && s.Stats.Established.Load() == 0 }, "BYE未释放实际通话资源")
			checkCtx, checkCancel := context.WithTimeout(service, 2*time.Second)
			defer checkCancel()
			stats, err := s.pool.Call(checkCtx, 0, media.Request{Op: "stats", Generation: capability.Generation})
			trace.record("fresh_media_stats", map[string]any{"reply": stats, "error": fmt.Sprint(err)})
			if err != nil || !stats.OK {
				t.Fatal("BYE后新鲜媒体统计不可用：", err)
			}
			for _, key := range []string{"active_calls", "processed_active_calls", "processed_local_active_calls", "rx_active_subscriptions", "rx_queued_events", "rx_observation_storage_bytes", "rx_observation_failed"} {
				if rxSIPStat(t, stats.Stats, key) != 0 {
					t.Fatal("真实资源未归零：", key)
				}
			}
			if rxSIPStat(t, stats.Stats, "processed_decoded_frames") != uint64(frames+preFrames) {
				t.Fatal("实际解码帧数与全部输入不符")
			}
			if value := s.CompatibilityAPI(checkCtx, "uuid_exists", uuid); strings.TrimSpace(value) != "false" {
				t.Fatal("BYE后UUID仍存在")
			}
			var rebound []*net.UDPConn
			for port := base; port < base+4; port++ {
				c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
				if err != nil {
					for _, owned := range rebound {
						_ = owned.Close()
					}
					t.Fatal("实际端口无法复绑：", err)
				}
				rebound = append(rebound, c)
			}
			for _, owned := range rebound {
				_ = owned.Close()
			}
			trace.record("media_ports_rebound", []int{base, base + 1, base + 2, base + 3})
			rxSIPWait(t, 35*time.Second, func() bool { return s.Stats.Timers.Load() == 0 }, "真实32秒SIP缓存/GC未归零")
			trace.record("actual_final_zero", map[string]any{"active_calls": s.Stats.Active.Load(), "established": s.Stats.Established.Load(), "timers": s.Stats.Timers.Load(), "asr": s.ASRSnapshot(), "completed_asr": st})
			close(stop)
			select {
			case err := <-runDone:
				runFinished = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("实际Server未正常排空")
			}
			normalShutdown = true
			t.Logf("真实SIP/Rust/ASR代理完成：payload=%d rate=%d connected=%t late=%t；8帧逐样本核验、FINISH/DONE、BYE与真实资源清理", tc.payload, tc.rate, tc.connected, tc.late)
		})
	}
}

// asrSIPMessage 由独立偏移解析器读取原始ASR1文件，避免使用待测编码/解码函数生成期望。
type asrSIPMessage struct {
	kind byte
	body []byte
}

func asrSIPMessages(t *testing.T, path string) []asrSIPMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 4<<20 {
		t.Fatal("原始ASR1证据不可用/超限：", err)
	}
	var result []asrSIPMessage
	for len(raw) > 0 {
		if len(raw) < 8 {
			t.Fatal("ASR1原始帧截断")
		}
		n := int(binary.LittleEndian.Uint32(raw))
		if n < 4 || n > 8192 || n+4 > len(raw) || raw[5] != 1 || raw[6] != 0 || raw[7] != 0 {
			t.Fatal("ASR1原始头不符")
		}
		result = append(result, asrSIPMessage{raw[4], raw[8 : n+4]})
		raw = raw[n+4:]
		if len(result) > 512 {
			t.Fatal("ASR1消息数量超限")
		}
	}
	return result
}

// asrSIPFIR 使用冻结Rust参考系数作完整插零直接卷积，独立于生产Go的两相/环形Converter。
func asrSIPFIR(t *testing.T, input []int16) []byte {
	t.Helper()
	raw, err := os.ReadFile("../asr/resample/testdata/coefficients.f64le")
	if err != nil || len(raw) != 127*8 || fmt.Sprintf("%x", sha256.Sum256(raw)) != "e7d108779e82bc025af440ac3f2fbf4434972f93298b4bdfc79c56dc50795f01" {
		t.Fatal("独立FIR参考系数身份错误：", err)
	}
	output := make([]byte, len(input)*4)
	for n := 0; n < len(input)*2; n++ {
		var value float64
		for tap := 0; tap < 127 && tap <= n; tap++ {
			if (n-tap)%2 == 0 {
				value += math.Float64frombits(binary.LittleEndian.Uint64(raw[tap*8:])) * float64(input[(n-tap)/2]) * 2
			}
		}
		sample := int16(math.Max(-32768, math.Min(32767, math.Round(value))))
		binary.LittleEndian.PutUint16(output[n*2:], uint16(sample))
	}
	return output
}

type asrSIPPrefix struct {
	Token   string `json:"stream_token"`
	Last    string `json:"last_event_seq"`
	Frames  string `json:"audio_frames"`
	Samples string `json:"samples"`
	Markers string `json:"markers"`
}

func asrSIPVerifyProvider(t *testing.T, trace *rxSIPTrace, dir string, records []media.RXFrame, inputs map[uint16][]byte, events []asr.Event, payload uint8, rate uint32, late bool, wantFrames int) {
	t.Helper()
	if len(records) == 0 {
		t.Fatal("没有实际RX原记录")
	}
	if late && records[0].Kind != media.RXDecoded {
		t.Fatalf("晚订阅没有确定覆盖首Decoded：kind=%v", records[0].Kind)
	}
	if late {
		for _, f := range records {
			if f.Kind == media.RXSourceBoundary {
				t.Fatal("晚订阅重放了旧来源Boundary")
			}
		}
	}
	bySeq := map[uint64]media.RXFrame{}
	expected := map[uint64][]byte{}
	var history []int16
	var previous *media.RXFrame
	for i := range records {
		f := records[i]
		if _, exists := bySeq[f.Sequence]; exists {
			t.Fatal("实际RX序号重复")
		}
		bySeq[f.Sequence] = f
		if f.Kind != media.RXDecoded {
			pureAux := f.Kind == media.RXAuxiliary || f.Kind == media.RXAuxiliaryExpired && !f.CNApplied
			if !pureAux || f.Flags&64 != 0 {
				history = nil
				previous = nil
			}
			continue
		}
		p := inputs[uint16(f.RTPSequence)]
		if len(p) != 172 || uint32(f.RTPTimestamp) != binary.BigEndian.Uint32(p[4:]) || f.SSRC != binary.BigEndian.Uint32(p[8:]) || f.SampleCount != 160 || f.BodyBytes != 320 {
			t.Fatal("真实RX帧不能对应原始RTP")
		}
		// 内部展开从一个完整周期起步，以容纳首包之前的乱序值；不是线上RTCP的去种子计数。
		// 本组输入下标0..27，按固定初值独立核验跨回绕；只比低位会漏掉SDK周期错误。
		index := uint64(uint16(binary.BigEndian.Uint16(p[2:]) - uint16(65532)))
		if index > 27 || f.RTPSequence != (1<<16)+65532+index || f.RTPTimestamp != (1<<32)+0xfffffc00+index*160 {
			t.Fatalf("完整RTP展开位置不匹配：index=%d sequence=%d timestamp=%d", index, f.RTPSequence, f.RTPTimestamp)
		}
		native := make([]int16, 160)
		for n := range native {
			native[n] = rxSIPG711(payload, p[12+n])
			if native[n] != f.PCM[n] {
				t.Fatalf("独立G711逐样本不符：seq=%d sample=%d got=%d want=%d", f.Sequence, n, f.PCM[n], native[n])
			}
		}
		if previous != nil && (f.SourceGeneration != previous.SourceGeneration || f.SourceSegment != previous.SourceSegment || f.MediaTimeNS != previous.MediaTimeNS+20_000_000 || f.RTPTimestamp != previous.RTPTimestamp+160) || f.Boundary == 5 {
			history = nil
		}
		if rate == 8000 {
			b := make([]byte, 320)
			for n, x := range native {
				binary.LittleEndian.PutUint16(b[n*2:], uint16(x))
			}
			expected[f.Sequence] = b
		} else {
			history = append(history, native...)
			all := asrSIPFIR(t, history)
			expected[f.Sequence] = append([]byte(nil), all[len(all)-640:]...)
		}
		copy := f
		previous = &copy
	}
	var pcm []byte
	audio := map[uint64][]byte{}
	var finish asrSIPPrefix
	finishCount, markerCount := 0, 0
	var token string
	for _, m := range asrSIPMessages(t, filepath.Join(dir, "inbound.bin")) {
		switch m.kind {
		case 1:
			var hello map[string]any
			if json.Unmarshal(m.body, &hello) != nil {
				t.Fatal("HELLO JSON错误")
			}
			token, _ = hello["stream_token"].(string)
		case 3:
			if len(m.body) < 112 {
				t.Fatal("AUDIO头截断")
			}
			u64 := func(offset int) uint64 { return binary.LittleEndian.Uint64(m.body[offset:]) }
			seq := u64(16)
			f, ok := bySeq[seq]
			if !ok || f.Kind != media.RXDecoded {
				t.Fatal("AUDIO没有真实Decoded来源")
			}
			if u64(24) != f.SourceGeneration || u64(32) != f.SourceSegment || u64(40) != f.RTPSequence || u64(48) != f.RTPTimestamp || u64(56) != f.MediaTimeNS || u64(64) != f.ObservationLowerBoundNS || u64(72) != f.ExpiresAtNS || hex.EncodeToString(m.body[:16]) != token {
				t.Fatal("AUDIO改写了真实来源/原始到期值")
			}
			if binary.LittleEndian.Uint32(m.body[84:]) != rate || binary.LittleEndian.Uint32(m.body[88:]) != 8000 || binary.LittleEndian.Uint16(m.body[100:]) != uint16(rate/50) || binary.LittleEndian.Uint16(m.body[98:]) != f.ClockDomain {
				t.Fatal("AUDIO采样率/时钟/样本格式不符")
			}
			delay := uint32(0)
			if rate == 16000 {
				delay = 3_937_500
			}
			if binary.LittleEndian.Uint32(m.body[80:]) != f.SSRC || binary.LittleEndian.Uint32(m.body[92:]) != delay || binary.LittleEndian.Uint16(m.body[96:]) != f.Flags || binary.LittleEndian.Uint16(m.body[102:]) != 1 || binary.LittleEndian.Uint16(m.body[104:]) != uint16(rate/50*2) || !bytes.Equal(m.body[106:112], make([]byte, 6)) {
				t.Fatal("AUDIO来源标记/滤波延迟/保留位不符")
			}
			if !bytes.Equal(m.body[112:], expected[seq]) {
				t.Fatalf("独立PCM/FIR逐样本不符：event=%d", seq)
			}
			if _, exists := audio[seq]; exists {
				t.Fatal("供应商重放AUDIO")
			}
			audio[seq] = append([]byte(nil), m.body[112:]...)
			pcm = append(pcm, m.body[112:]...)
		case 4:
			markerCount++
		case 5:
			finishCount++
			if json.Unmarshal(m.body, &finish) != nil {
				t.Fatal("FINISH JSON错误")
			}
		default:
			t.Fatalf("本次短成功流出现非预期入站kind=%d", m.kind)
		}
	}
	if len(audio) != wantFrames || finishCount != 1 || finish.Token != token || finish.Frames != strconv.Itoa(wantFrames) || finish.Samples != strconv.Itoa(wantFrames*int(rate/50)) || finish.Markers != strconv.Itoa(markerCount) {
		t.Fatal("完整提交前缀/帧数/样本数错误")
	}
	consumed, err := os.ReadFile(filepath.Join(dir, "consumed.pcm"))
	if err != nil || !bytes.Equal(consumed, pcm) {
		t.Fatal("供应商实际消费PCM不等于全部提交PCM：", err)
	}
	doneCount := 0
	for _, m := range asrSIPMessages(t, filepath.Join(dir, "outbound.bin")) {
		if m.kind == 7 {
			var done asrSIPPrefix
			if json.Unmarshal(m.body, &done) != nil || done != finish {
				t.Fatal("DONE没有精确回显实际前缀")
			}
			doneCount++
		}
	}
	if doneCount != 1 {
		t.Fatal("实际线缆缺少唯一DONE")
	}
	partial, final := 0, 0
	for _, event := range events {
		if event.Type != "partial" && event.Type != "final" {
			continue
		}
		if event.LastEventSequence < event.FirstEventSequence || event.LastEventSequence-event.FirstEventSequence >= 256 {
			t.Fatal("结果覆盖范围超出本用例原始RX预算")
		}
		var covered []byte
		for seq := event.FirstEventSequence; seq <= event.LastEventSequence; seq++ {
			if f, ok := bySeq[seq]; ok && f.SourceGeneration == event.SourceGeneration && f.SourceSegment == event.SourceSegment {
				covered = append(covered, audio[seq]...)
			}
		}
		want := "mock-sha256:" + fmt.Sprintf("%x", sha256.Sum256(covered))
		if len(covered) == 0 || event.Text != want {
			t.Fatal("实际交付结果摘要不匹配其PCM覆盖范围")
		}
		if event.Type == "partial" {
			partial++
		} else {
			final++
		}
	}
	if partial == 0 || final == 0 {
		t.Fatal("实际链没有partial和final")
	}
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(data) > 4<<20 {
		t.Fatal("供应商事件证据缺失/超限")
	}
	checked := map[uint64]bool{}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e map[string]any
		if json.Unmarshal(line, &e) != nil {
			t.Fatal("供应商事件JSON错误")
		}
		if e["stage"] != "audio-consumed" {
			continue
		}
		parse := func(key string) uint64 {
			text, ok := e[key].(string)
			if !ok {
				t.Fatal("消费时钟不是原始十进制串：", key)
			}
			v, err := strconv.ParseUint(text, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		seq := parse("event_seq")
		f, ok := bySeq[seq]
		if !ok || checked[seq] || parse("source_generation") != f.SourceGeneration || parse("source_segment") != f.SourceSegment || e["clock_domain"] != float64(f.ClockDomain) || e["samples"] != float64(rate/50) || parse("lower_ns") != f.ObservationLowerBoundNS || parse("expires_ns") != f.ExpiresAtNS || parse("checked_clock_ns") < f.ObservationLowerBoundNS || parse("checked_clock_ns") >= f.ExpiresAtNS {
			t.Fatal("供应商没有按原共享到期值消费")
		}
		checked[seq] = true
	}
	if len(checked) != wantFrames {
		t.Fatal("供应商完整消费时钟证明不足")
	}
	var receipt struct {
		State            string            `json:"state"`
		Error            string            `json:"error"`
		Counters         asrSIPPrefix      `json:"counters"`
		ConsumedBytes    uint64            `json:"consumed_pcm_bytes"`
		Closed           bool              `json:"connection_closed"`
		RecognitionModel bool              `json:"recognition_model"`
		Files            map[string]string `json:"files"`
	}
	rawReceipt, readErr := os.ReadFile(filepath.Join(dir, "receipt.json"))
	if readErr != nil || json.Unmarshal(rawReceipt, &receipt) != nil || receipt.State != "completed" || receipt.Error != "" || !receipt.Closed || receipt.RecognitionModel || receipt.Counters != finish || receipt.ConsumedBytes != uint64(len(consumed)) {
		t.Fatal("供应商实际连接收据不支持完成结论：", readErr)
	}
	for _, name := range []string{"inbound.bin", "outbound.bin", "consumed.pcm", "events.jsonl"} {
		sum, hashErr := rxSIPHash(filepath.Join(dir, name))
		if hashErr != nil || receipt.Files[name] != sum {
			t.Fatal("供应商收据文件指纹不匹配：", name, hashErr)
		}
	}
	trace.record("independent_pcm_verification", map[string]any{"decoded_frames": len(audio), "consumed_samples": len(consumed) / 2, "consumed_pcm_sha256": fmt.Sprintf("%x", sha256.Sum256(consumed)), "partial_events": partial, "final_events": final, "finish_done": finish, "provider_freshness_checks": len(checked), "late_first_decoded_no_boundary": late, "oracle": "independent G711 law + full zero-insertion convolution with SHA-bound Rust reference coefficients; not production Go Converter"})
}
