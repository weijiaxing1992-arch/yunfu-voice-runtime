package media

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"rustswitch/control/internal/config"
)

// rxPauseCase只包含自有测试端口和候选路径；不读取或控制9080主服务的进程。
type rxPauseCase struct {
	Base             int
	RTP, RTCP        string
	Payload          uint8
	Connected        bool
	Media, Artifacts string
}

type rxPauseReady struct {
	HelperPID  int                `json:"helper_pid"`
	Worker     CapabilitySnapshot `json:"worker"`
	Allocated  Reply              `json:"allocated"`
	Subscribed Reply              `json:"subscribed"`
	SDK        RXSnapshot         `json:"sdk"`
}

type rxPauseResult struct {
	Passed       bool               `json:"passed"`
	ReadError    string             `json:"read_error"`
	ReadClockNS  uint64             `json:"read_clock_ns"`
	Remote       Reply              `json:"remote"`
	SDK          RXSnapshot         `json:"sdk"`
	Worker       CapabilitySnapshot `json:"worker_after"`
	Stopped      Reply              `json:"stopped"`
	AfterRelease map[string]any     `json:"after_release"`
	CleanupError string             `json:"cleanup_error"`
}

// rxPauseJSON保留实际记录，不以最终绿色布尔替代原始状态、PID和时钟。
func rxPauseJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestRXReceiverPauseOwnedHelper只由父测试显式创建；普通go test执行到此直接返回。
// helper控制/信令同时被SIGSTOP，因此本测试不把暂停期间的控制延迟当成通过。
func TestRXReceiverPauseOwnedHelper(t *testing.T) {
	if os.Getenv("RUSTSWITCH_RX_PAUSE_OWNED_HELPER") != "1" {
		return
	}
	var c rxPauseCase
	if err := json.Unmarshal([]byte(os.Getenv("RUSTSWITCH_RX_PAUSE_CASE")), &c); err != nil {
		t.Fatal(err)
	}
	control := os.NewFile(3, "rx-pause-results")
	defer control.Close()
	fdSender := os.NewFile(4, "rx-pause-fd-transfer")
	defer fdSender.Close()
	enc := json.NewEncoder(control)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cfg, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Media.Binary, cfg.Media.Processing, cfg.Media.PlaybackRoot = c.Media, "g711", ""
	cfg.Media.Workers, cfg.Media.BindIP, cfg.Media.PortStart, cfg.Media.PortEnd = 1, "127.0.0.1", c.Base, c.Base+3
	cfg.Media.CPUCores, cfg.Media.ExcludedPortBlocks, cfg.Media.AllowedRemoteNetworks = nil, nil, []string{"127.0.0.0/8"}
	cfg.Media.ConnectSockets, cfg.Limits.MaxCalls = c.Connected, 1
	p, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	codec := "PCMU"
	if c.Payload == 8 {
		codec = "PCMA"
	}
	allocated, err := p.Call(ctx, 0, Request{Op: "allocate", Generation: 1, Session: 73, Processing: G711LocalProcessingPlan(), A: &Peer{RTP: c.RTP, RTCP: c.RTCP}, Payload: c.Payload, Codec: &CodecSpec{Name: codec, SampleRate: 8000, RTPClockRate: 8000, Channels: 1, PTimeMS: 20}})
	if err != nil {
		t.Fatal(err)
	}
	// EOF、父测试失败和正常结束都走真实Release；从不等待FD4被消费。
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_, _ = p.Call(cleanupCtx, 0, Request{Op: "release", Generation: 1, Session: 73})
	}()
	h, subscribed, err := p.SubscribeRX(ctx, 0, 1, 73, 1)
	if err != nil {
		t.Fatal(err)
	}
	ready := rxPauseReady{HelperPID: os.Getpid(), Worker: p.Workers[0].CapabilitySnapshot(), Allocated: allocated, Subscribed: subscribed, SDK: h.Snapshot()}
	if !ready.Worker.Healthy || ready.Worker.Generation != 1 || ready.SDK.ReceivedRecords != 0 {
		t.Fatalf("unexpected initial identity: %+v", ready)
	}
	// SCM_RIGHTS只复制FD引用；父只用MSG_PEEK读取，不用FileConn改变该共享open-file-description模式。
	raw, err := h.lane.conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sendErr error
	if err = raw.Control(func(fd uintptr) {
		sendErr = unix.Sendmsg(int(fdSender.Fd()), []byte("rustswitch-rx-pause-v1"), unix.UnixRights(int(fd)), nil, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	rxPauseJSON(t, filepath.Join(c.Artifacts, "helper-ready.json"), ready)
	if err = enc.Encode(ready); err != nil {
		t.Fatal(err)
	}
	// SIGSTOP期间本goroutine、SDK接收goroutine与整个Go helper都停止，Rust子进程继续运行。
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || scanner.Text() != "inspect" {
		return
	}
	result := rxPauseResult{}
	_, readErr := h.Read(ctx)
	if readErr != nil {
		result.ReadError = readErr.Error()
	}
	result.ReadClockNS, _ = rxClockNow()
	deadline := time.Now().Add(time.Second)
	for {
		result.Remote, err = h.Status(ctx)
		if err != nil {
			break
		}
		result.SDK = h.Snapshot()
		if result.Remote.SubmittedSamples > 0 && result.SDK.ReceivedSamples == result.Remote.SubmittedSamples && result.SDK.ReceivedRecords >= result.Remote.SubmittedEvents {
			break
		}
		if time.Now().After(deadline) {
			err = errors.New("actual submitted records were not fully received after resume")
			break
		}
		time.Sleep(time.Millisecond)
	}
	result.Worker = p.Workers[0].CapabilitySnapshot()
	result.Passed = errors.Is(readErr, ErrRXExpired) && err == nil && result.Remote.State == "active" && result.Remote.Error == "" && result.Remote.DroppedEvents == 0 && result.Remote.QueuedEvents == 0 && result.Remote.SubmittedSamples >= 320 && result.Remote.ProducedSamples == result.Remote.SubmittedSamples && result.SDK.DeliveredRecords == 0 && result.SDK.DeliveredSamples == 0 && result.SDK.DroppedSamples == result.Remote.SubmittedSamples && result.SDK.QueuedRecords == 0 && result.SDK.FreshnessFailures == 1 && result.SDK.ClockFailures == 0 && result.Worker.PID == ready.Worker.PID && result.Worker.Generation == ready.Worker.Generation && result.Worker.Healthy
	if err != nil {
		result.CleanupError = err.Error()
	}
	result.Stopped, err = h.Unsubscribe(ctx)
	if err != nil {
		result.Passed = false
		result.CleanupError = err.Error()
	}
	if _, err = p.Call(ctx, 0, Request{Op: "release", Generation: 1, Session: 73}); err != nil {
		result.Passed = false
		result.CleanupError = err.Error()
	}
	stats, err := p.Call(ctx, 0, Request{Op: "stats", Generation: 1})
	if err != nil {
		result.Passed = false
		result.CleanupError = err.Error()
	} else {
		result.AfterRelease = stats.Stats
	}
	if result.Stopped.State != "stopped" || !h.Retired() || result.AfterRelease["active_calls"] != float64(0) || result.AfterRelease["processed_local_active_calls"] != float64(0) {
		result.Passed = false
	}
	rxPauseJSON(t, filepath.Join(c.Artifacts, "helper-result.json"), result)
	if err = enc.Encode(result); err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatalf("actual RXS2 pause failed: %+v", result)
	}
}

// rxPauseProcessState检查精确自有PID的父进程/进程组/停止状态，不按模糊进程名发信号。
func rxPauseProcessState(pid int) (string, error) {
	out, err := exec.Command("/bin/ps", "-p", fmt.Sprint(pid), "-o", "pid=,ppid=,pgid=,stat=").Output()
	return strings.TrimSpace(string(out)), err
}

// TestRealRXReceiverPauseRejectsOSResidentPCM以两个真实RTP包验证Rust→OS→实际Go SDK，不使用生产hook。
func TestRealRXReceiverPauseRejectsOSResidentPCM(t *testing.T) {
	if os.Getenv("RUSTSWITCH_REAL_MEDIA") == "" {
		t.Fatal("真实暂停门禁必须指定RUSTSWITCH_REAL_MEDIA")
	}
	media := realLocalMediaBinary(t)
	mediaBefore := realLocalBinaryHash(t, media)
	artifactRoot := os.Getenv("RUSTSWITCH_RX_PAUSE_ARTIFACTS")
	if artifactRoot == "" {
		artifactRoot = t.TempDir()
	}
	if err := os.MkdirAll(artifactRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for _, connected := range []bool{false, true} {
		for _, payload := range []uint8{0, 8} {
			t.Run(fmt.Sprintf("payload_%d_connected_%t", payload, connected), func(t *testing.T) {
				artifacts := filepath.Join(artifactRoot, fmt.Sprintf("payload_%d_connected_%t", payload, connected))
				if err := os.Mkdir(artifacts, 0700); err != nil {
					t.Fatal(err)
				}
				base, releasePorts, rtp, rtcp := rxRealIsolatedPorts(t)
				defer releasePorts()
				defer rtp.Close()
				defer rtcp.Close()
				c := rxPauseCase{Base: base, RTP: rtp.LocalAddr().String(), RTCP: rtcp.LocalAddr().String(), Payload: payload, Connected: connected, Media: media, Artifacts: artifacts}
				encoded, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				controlRead, controlWrite, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer controlRead.Close()
				defer controlWrite.Close()
				transfer, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
				if err != nil {
					t.Fatal(err)
				}
				unix.CloseOnExec(transfer[0])
				unix.CloseOnExec(transfer[1])
				transferParent, transferChild := os.NewFile(uintptr(transfer[0]), "pause-fd-parent"), os.NewFile(uintptr(transfer[1]), "pause-fd-child")
				defer transferChild.Close()
				transferConn, err := net.FileConn(transferParent)
				_ = transferParent.Close()
				if err != nil {
					t.Fatal(err)
				}
				defer transferConn.Close()
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(executable, "-test.run=^TestRXReceiverPauseOwnedHelper$", "-test.v")
				cmd.Env = append(os.Environ(), "RUSTSWITCH_RX_PAUSE_OWNED_HELPER=1", "RUSTSWITCH_RX_PAUSE_CASE="+string(encoded))
				cmd.ExtraFiles = []*os.File{controlWrite, transferChild}
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				logFile, err := os.OpenFile(filepath.Join(artifacts, "helper.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer logFile.Close()
				cmd.Stdout, cmd.Stderr = logFile, logFile
				releasePorts()
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				_ = controlWrite.Close()
				_ = transferChild.Close()
				waitDone := make(chan struct{})
				var waitErr error
				go func() { waitErr = cmd.Wait(); close(waitDone) }()
				var forced atomic.Bool
				var cleanOnce sync.Once
				cleanup := func() {
					cleanOnce.Do(func() {
						// 必须先恢复精确helper；EOF使其正常Release/Pool.Close，故障注入不留下停止的Go进程。
						_ = cmd.Process.Signal(syscall.SIGCONT)
						_ = stdin.Close()
						select {
						case <-waitDone:
							return
						case <-time.After(2 * time.Second):
						}
						forced.Store(true)
						// helper在启动时创建自己的进程组，Rust继承该组；仅最后兜底终止这个自有组。
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGCONT)
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
						select {
						case <-waitDone:
							return
						case <-time.After(time.Second):
						}
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						<-waitDone
					})
				}
				defer cleanup()
				// watchdog运行在未暂停的父测试，任何断言/管道失败均有独立恢复清理上限。
				watchdog := time.AfterFunc(5*time.Second, cleanup)
				defer watchdog.Stop()
				_ = controlRead.SetReadDeadline(time.Now().Add(3 * time.Second))
				dec := json.NewDecoder(controlRead)
				var ready rxPauseReady
				if err = dec.Decode(&ready); err != nil {
					t.Fatal("owned helper not ready", err)
				}
				if ready.HelperPID != cmd.Process.Pid || ready.HelperPID <= 1 || ready.Worker.PID <= 1 || ready.Worker.PID == int64(ready.HelperPID) {
					t.Fatal("invalid owned process identity", ready)
				}
				beforeHelper, err := rxPauseProcessState(ready.HelperPID)
				if err != nil {
					t.Fatal(err)
				}
				beforeWorker, err := rxPauseProcessState(int(ready.Worker.PID))
				if err != nil {
					t.Fatal(err)
				}
				var wpid, wppid, wpgid int
				var wstate string
				if _, err = fmt.Sscan(beforeWorker, &wpid, &wppid, &wpgid, &wstate); err != nil || wppid != ready.HelperPID || wpgid != ready.HelperPID {
					t.Fatal("worker is not owned helper child", beforeWorker, err)
				}
				_ = transferConn.SetReadDeadline(time.Now().Add(time.Second))
				var body [64]byte
				var oob [128]byte
				n, oobn, flags, _, err := transferConn.(*net.UnixConn).ReadMsgUnix(body[:], oob[:])
				if err != nil || flags&syscall.MSG_CTRUNC != 0 || string(body[:n]) != "rustswitch-rx-pause-v1" {
					t.Fatal("missing owned FD transfer", err)
				}
				messages, err := unix.ParseSocketControlMessage(oob[:oobn])
				if err != nil {
					t.Fatal(err)
				}
				var descriptors []int
				for _, message := range messages {
					rights, e := unix.ParseUnixRights(&message)
					if e != nil {
						t.Fatal(e)
					}
					descriptors = append(descriptors, rights...)
				}
				defer func() {
					for _, fd := range descriptors {
						_ = unix.Close(fd)
					}
				}()
				if len(descriptors) != 1 {
					t.Fatal("expected exactly one duplicated RX FD")
				}
				peekFD := descriptors[0]
				unix.CloseOnExec(peekFD)
				originalFlags, err := unix.FcntlInt(uintptr(peekFD), unix.F_GETFL, 0)
				if err != nil || originalFlags&unix.O_NONBLOCK == 0 {
					t.Fatal("unexpected RX FD mode", err)
				}
				if err = cmd.Process.Signal(syscall.SIGSTOP); err != nil {
					t.Fatal(err)
				}
				stopStarted := time.Now()
				stopNS, _ := rxClockNow()
				var stopped string
				for time.Since(stopStarted) < time.Second {
					stopped, err = rxPauseProcessState(ready.HelperPID)
					if err != nil {
						t.Fatal(err)
					}
					fields := strings.Fields(stopped)
					if len(fields) == 4 && strings.Contains(fields[3], "T") {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if fields := strings.Fields(stopped); len(fields) != 4 || !strings.Contains(fields[3], "T") {
					t.Fatal("owned helper did not actually stop", stopped)
				}
				target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ready.Allocated.ARTP}
				for index := 0; index < 2; index++ {
					packet := make([]byte, 172)
					packet[0] = 0x80
					packet[1] = payload
					if index == 0 {
						packet[1] |= 0x80
					}
					binary.BigEndian.PutUint16(packet[2:], uint16(900+index))
					binary.BigEndian.PutUint32(packet[4:], uint32(32000+index*160))
					binary.BigEndian.PutUint32(packet[8:], 0x71230999)
					for i := 0; i < 160; i++ {
						packet[12+i] = byte(31 + index*47 + i*13)
					}
					if err = os.WriteFile(filepath.Join(artifacts, fmt.Sprintf("input-%d.rtp", index)), packet, 0600); err != nil {
						t.Fatal(err)
					}
					if _, err = rtp.WriteToUDP(packet, target); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(250 * time.Millisecond)
				var wire [648]byte
				n, _, _, _, err = unix.Recvmsg(peekFD, wire[:], nil, unix.MSG_PEEK|unix.MSG_DONTWAIT)
				if err != nil {
					t.Fatal("no actual RXS2 in OS while Go stopped", err)
				}
				peekNS, _ := rxClockNow()
				frame, err := decodeRXFrame(wire[:n])
				if err != nil {
					t.Fatal(err)
				}
				if frame.isSummary() || frame.Session != 73 || frame.SubscriptionID != 1 || peekNS < frame.ExpiresAtNS {
					t.Fatal("peek did not prove expired real observation", frame.Kind)
				}
				if err = os.WriteFile(filepath.Join(artifacts, "os-head.rxs2"), wire[:n], 0600); err != nil {
					t.Fatal(err)
				}
				workerDuring, err := rxPauseProcessState(int(ready.Worker.PID))
				if err != nil {
					t.Fatal(err)
				}
				if fields := strings.Fields(workerDuring); len(fields) != 4 || strings.Contains(fields[3], "T") {
					t.Fatal("Rust was paused with Go", workerDuring)
				}
				flagsAfter, err := unix.FcntlInt(uintptr(peekFD), unix.F_GETFL, 0)
				if err != nil || flagsAfter != originalFlags {
					t.Fatal("peeking changed RX FD mode", err)
				}
				resumeNS, _ := rxClockNow()
				if err = cmd.Process.Signal(syscall.SIGCONT); err != nil {
					t.Fatal(err)
				}
				rxPauseJSON(t, filepath.Join(artifacts, "parent-observation.json"), map[string]any{"helper_before": beforeHelper, "worker_before": beforeWorker, "helper_stopped": stopped, "worker_during_stop": workerDuring, "stop_ns": stopNS, "peek_ns": peekNS, "resume_ns": resumeNS, "paused_ns": resumeNS - stopNS, "peek_kind": frame.Kind, "peek_scope": "only actual OS queue head, MSG_PEEK did not consume it", "peek_frame": frame, "fd_flags_before": originalFlags, "fd_flags_after": flagsAfter, "input_packets": 2, "target": target.String()})
				if resumeNS-stopNS >= 900_000_000 {
					t.Fatal("pause exceeded low-stall scenario budget")
				}
				if _, err = fmt.Fprintln(stdin, "inspect"); err != nil {
					t.Fatal(err)
				}
				_ = controlRead.SetReadDeadline(time.Now().Add(2 * time.Second))
				var result rxPauseResult
				if err = dec.Decode(&result); err != nil {
					t.Fatal("missing actual SDK result", err)
				}
				cleanup()
				if forced.Load() || waitErr != nil || !result.Passed {
					t.Fatalf("pause/cleanup failed forced=%v wait=%v result=%+v", forced.Load(), waitErr, result)
				}
				t.Logf("actual_receiver_pause helper_pid=%d worker_pid=%d kind=%d pause_ns=%d submitted_samples=%d dropped_samples=%d delivered_samples=%d artifacts=%s", ready.HelperPID, ready.Worker.PID, frame.Kind, resumeNS-stopNS, result.Remote.SubmittedSamples, result.SDK.DroppedSamples, result.SDK.DeliveredSamples, artifacts)
			})
		}
	}
	if realLocalBinaryHash(t, media) != mediaBefore {
		t.Fatal("Rust candidate changed during actual pause test")
	}
}
