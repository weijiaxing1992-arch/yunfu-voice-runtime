package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
	"strings"
	"testing"
	"time"
)

// TestMain 给生命周期测试提供不绑定端口的媒体进程替身；正常测试仍走 testing 的标准入口。
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "--worker-config" && os.Getenv("RUSTSWITCH_LIFECYCLE_MEDIA_HELPER") == "1" {
		var cfg media.WorkerConfig
		if json.Unmarshal([]byte(os.Args[2]), &cfg) != nil {
			os.Exit(2)
		}
		encoder, decoder := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
		_ = encoder.Encode(media.Reply{OK: true, Type: "ready", ProtocolVersion: 1, WorkerID: cfg.WorkerID, PID: os.Getpid()})
		for {
			var request media.Request
			if decoder.Decode(&request) != nil {
				os.Exit(0)
			}
			if request.Op == "connect" {
				f, err := os.OpenFile(os.Getenv("RUSTSWITCH_LIFECYCLE_MEDIA_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					os.Exit(3)
				}
				_, _ = fmt.Fprintln(f, request.B.RTP)
				_ = f.Close()
			}
			reply := media.Reply{ID: request.ID, OK: true, Type: "ack"}
			if request.Op == "stats" {
				reply.Type = "stats"
				reply.Stats = map[string]any{"active_calls": 0}
			}
			if request.Op == "allocate" {
				reply.Type = "allocated"
				reply.Session = request.Session
				reply.ARTP = cfg.PortStart
				reply.ARTCP = cfg.PortStart + 1
				reply.BRTP = cfg.PortStart + 2
				reply.BRTCP = cfg.PortStart + 3
			}
			if encoder.Encode(reply) != nil {
				os.Exit(0)
			}
		}
	}
	os.Exit(m.Run())
}

// lifecyclePool 启动当前测试二进制作为媒体替身，仅通过私有管道交互，不占用现有 SIP 或管理监听。
func lifecyclePool(t *testing.T) (*media.Pool, string) {
	t.Helper()
	t.Setenv("RUSTSWITCH_LIFECYCLE_MEDIA_HELPER", "1")
	trace := t.TempDir() + "/commands"
	t.Setenv("RUSTSWITCH_LIFECYCLE_MEDIA_TRACE", trace)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{Media: config.Media{Binary: executable, Workers: 1, PortStart: 20000, PortEnd: 20199}, Limits: config.Limits{MaxCalls: 10}}
	p, err := media.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, trace
}

// TestMediaSubmissionPreservesOrderAndBoundsGoroutines 复现早期/最终 SDP 更新被 goroutine 调度颠倒的问题。
// 不依赖 UDP 或墙钟压测：单调度线程中连续提交，再核对媒体进程实际看到的全部目标顺序。
func TestMediaSubmissionPreservesOrderAndBoundsGoroutines(t *testing.T) {
	p, trace := lifecyclePool(t)
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{ctx: ctx, pool: p, results: make(chan mediaResult, 128)}
	c := &Call{ID: 1, Worker: 0, Generation: p.Workers[0].Generation.Load()}
	before := runtime.NumGoroutine()
	for i := 0; i < 32; i++ {
		s.submit(c, "connect_early", uint64(i+1), media.Request{Op: "connect", Session: 1, B: &media.Peer{RTP: fmt.Sprintf("127.0.0.1:%d", 30000+i*2), RTCP: fmt.Sprintf("127.0.0.1:%d", 30001+i*2)}})
	}
	growth := runtime.NumGoroutine() - before
	for range 32 {
		select {
		case result := <-s.results:
			if result.err != nil {
				t.Fatal(result.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("媒体结果未返回")
		}
	}
	wire, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(wire))
	if len(lines) != 32 {
		t.Fatalf("收到%d条命令", len(lines))
	}
	for i, line := range lines {
		want := fmt.Sprintf("127.0.0.1:%d", 30000+i*2)
		if line != want {
			t.Errorf("第%d条实际媒体目标=%s，需要=%s", i, line, want)
			break
		}
	}
	if growth > 4 {
		t.Errorf("32条IPC额外创建%d个goroutine，提交成本仍随消息数量增长", growth)
	}
}

// TestConnectUpdatesCoalesceAndKeepLatestFinalTarget 证明重复 183 不制造无限任务，200 的最终目标一定最后应用。
func TestConnectUpdatesCoalesceAndKeepLatestFinalTarget(t *testing.T) {
	p, trace := lifecyclePool(t)
	log, err := journal.Open(t.TempDir()+"/events", 64)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Call{ID: 1, Worker: 0, Generation: p.Workers[0].Generation.Load(), AStatus: 200, Offer: sip.SDP{Payload: 0, PTime: 20}}
	s := &Server{ctx: ctx, pool: p, journal: log, results: make(chan mediaResult, 128), calls: map[uint64]*Call{1: c}, Config: config.Config{Media: config.Media{AdvertiseIP: "127.0.0.1", AllowedRemoteNetworks: []string{"127.0.0.0/8"}}}}
	for i := 0; i < 1000; i++ {
		message := &sip.Message{Headers: []sip.Header{{Name: "content-type", Value: "application/sdp"}}, Body: sip.RenderSDP(1, "127.0.0.1", 30000+i*2, 30001+i*2, c.Offer)}
		s.connect(c, message, i == 999)
	}
	if c.ConnectInFlight != 1 || c.ConnectQueued == nil || c.ConnectQueued.version != 1000 || c.ConnectQueued.operation != "connect_answer" {
		t.Fatalf("未合并到最后一次协商：%+v", c)
	}
	for range 2 {
		select {
		case result := <-s.results:
			s.onMedia(result)
		case <-time.After(3 * time.Second):
			t.Fatal("合并后的媒体结果未返回")
		}
	}
	if c.ConnectInFlight != 0 || c.ConnectQueued != nil {
		t.Fatal("完成后仍保留连接任务")
	}
	wire, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(wire))
	if len(lines) != 2 || lines[0] != "127.0.0.1:30000" || lines[1] != "127.0.0.1:31998" {
		t.Fatalf("实际执行序列=%v", lines)
	}
}

// TestEndedCallDoesNotApplyQueuedConnect 检查取消或媒体故障之后的旧结果不会再把待更新目标提交到媒体。
func TestEndedCallDoesNotApplyQueuedConnect(t *testing.T) {
	c := &Call{ID: 1, Ended: true, ConnectInFlight: 1, ConnectVersion: 2, ConnectQueued: &mediaConnectUpdate{version: 2}}
	s := &Server{calls: map[uint64]*Call{1: c}}
	s.onMedia(mediaResult{session: 1, operation: "connect_early", version: 1})
	if c.ConnectInFlight != 0 || c.ConnectQueued != nil {
		t.Fatal("已结束呼叫仍保留待处理连接")
	}
}

// TestReleaseBackoffKeepsReservationAndCancelsAfterRelease 验证拥塞时不提前释放容量，重试有上限并及时取消。
func TestReleaseBackoffKeepsReservationAndCancelsAfterRelease(t *testing.T) {
	w := &media.Worker{}
	w.Healthy.Store(true)
	w.Generation.Store(1)
	c := &Call{ID: 17, Worker: 0, Generation: 1, Ended: true, Allocated: true, Reserved: true}
	s := &Server{pool: &media.Pool{Workers: []*media.Worker{w}}, calls: map[uint64]*Call{17: c}, workerLoads: []int{1}}
	s.Stats.Active.Store(1)
	for range 20 {
		s.onMedia(mediaResult{session: 17, operation: "release", err: media.ErrQueueFull})
	}
	if !c.Reserved || s.Stats.Active.Load() != 1 || len(s.timers) != 1 || c.ReleaseRetries != 5 {
		t.Fatalf("背压导致预留或重试越界：%+v timers=%d", c, len(s.timers))
	}
	if wait := time.Until(s.timers[0].when); wait < 1500*time.Millisecond || wait > 2*time.Second {
		t.Fatalf("退避时间越界：%v", wait)
	}
	s.onMedia(mediaResult{session: 17, operation: "release", reply: media.Reply{OK: true, Type: "ack"}})
	if c.Reserved || c.Allocated || s.Stats.Active.Load() != 0 || len(s.timers) != 0 {
		t.Fatal("成功释放没有清空预留与重试")
	}
}
