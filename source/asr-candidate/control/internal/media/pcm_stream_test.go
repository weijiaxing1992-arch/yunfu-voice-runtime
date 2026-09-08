package media

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"rustswitch/control/internal/config"
)

// init只提供独立子进程IPC夹具，验证FD继承和调度；它不编码RTP，不计为真实媒体通过。
func init() {
	mode := os.Getenv("RUSTSWITCH_PCM_MEDIA_HELPER")
	if mode == "" || len(os.Args) < 3 || os.Args[1] != "--worker-config" {
		return
	}
	var cfg WorkerConfig
	if json.Unmarshal([]byte(os.Args[2]), &cfg) != nil || os.Getenv("RUSTSWITCH_PCM_FD") != "3" {
		os.Exit(2)
	}
	file := os.NewFile(3, "pcm-child")
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		os.Exit(3)
	}
	defer conn.Close()
	caps := []string{}
	if mode == "supported" {
		caps = []string{pcmCapability}
	}
	encoder, decoder := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	if encoder.Encode(Reply{OK: true, Type: "ready", ProtocolVersion: 1, WorkerID: cfg.WorkerID, PID: os.Getpid(), Capabilities: caps}) != nil {
		os.Exit(4)
	}
	go func() {
		var wire [1645]byte
		for count := 0; count < 32; count++ {
			n, err := conn.Read(wire[:])
			if err != nil {
				return
			}
			if n < 364 || string(wire[:4]) != "RSP1" || n != 44+int(binary.LittleEndian.Uint16(wire[40:42]))*2 {
				os.Exit(5)
			}
			j := &pcmJob{session: binary.LittleEndian.Uint64(wire[16:24]), turnID: binary.LittleEndian.Uint64(wire[24:32]), offset: binary.LittleEndian.Uint64(wire[32:40]), samples: make([]int16, int(binary.LittleEndian.Uint16(wire[40:42])))}
			reply := pcmTestWire(binary.LittleEndian.Uint64(wire[8:16]), j)
			if _, err := conn.Write(reply); err != nil {
				return
			}
		}
	}()
	for count := 0; count < 128; count++ {
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			os.Exit(0)
		}
		var request Request
		if json.Unmarshal(raw, &request) != nil {
			os.Exit(7)
		}
		reply := Reply{OK: true, ID: request.ID, Type: "ack"}
		if isPCMControl(request.Op) {
			reply = pcmTestState(request.Session, request.TurnID)
			reply.ID = request.ID
			if request.Op == "pcm_turn_end" {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				if _, exists := fields["final_samples"]; !exists {
					os.Exit(8) // 和Rust必填字段一致，空end不能靠Go解码零值掩盖缺失。
				}
				reply.State = "completed"
			}
		}
		if request.Op == "stats" {
			reply.Type = "stats"
			reply.Stats = map[string]any{"dtmf_send_packets": 0, "dtmf_send_errors": 0, "dtmf_send_cancelled_digits": 0, "dtmf_send_active": 0}
		}
		if encoder.Encode(reply) != nil || request.Op == "shutdown" {
			os.Exit(0)
		}
	}
	os.Exit(6)
}

func pcmTestState(session, turn uint64) Reply {
	return Reply{OK: true, Type: "pcm_turn_state", Session: session, TurnID: turn, State: "buffering", BufferMS: 200, PrebufferMS: 40}
}

// pcmTestWire生成协议夹具字节；真实实现仍须与Rust和RTP端到端独立核验。
func pcmTestWire(id uint64, j *pcmJob) []byte {
	wire := make([]byte, pcmReplySize)
	copy(wire, "RSR1")
	accepted := j.offset + uint64(len(j.samples))
	for i, value := range []uint64{id, j.session, j.turnID, j.turnID, accepted, 0, accepted, 0, 0} {
		binary.LittleEndian.PutUint64(wire[8+i*8:16+i*8], value)
	}
	wire[80] = 1
	return wire
}

func TestPCMControlStrictContract(t *testing.T) {
	w := &Worker{}
	r := Request{Op: "pcm_turn_begin", Session: 7, TurnID: math.MaxUint64, BufferMS: 200, PrebufferMS: 40}
	valid := pcmTestState(r.Session, r.TurnID)
	if err := validatePCMRequest(r); err != nil {
		t.Fatal(err)
	}
	if err := w.validateReply(r, valid); err != nil {
		t.Fatal(err)
	}
	emptyEnd := Request{Op: "pcm_turn_end", Session: 7, TurnID: r.TurnID, FinalSamples: 0}
	emptyState := valid
	emptyState.State = "completed"
	if err := w.validateReply(emptyEnd, emptyState); err != nil {
		t.Fatalf("合法空轮次end被拒绝：%v", err)
	}
	for name, mutate := range map[string]func(*Reply){
		"wrong_turn": func(r *Reply) { r.TurnID = 1 }, "wrong_session": func(r *Reply) { r.Session++ },
		"idle": func(r *Reply) { r.State = "idle" }, "unknown_state": func(r *Reply) { r.State = "ready" },
		"missing_failure_reason": func(r *Reply) { r.State = "failed" }, "buffer_change": func(r *Reply) { r.BufferMS = 220 },
		"partial_frame": func(r *Reply) { r.AcceptedSamples = 1 },
		"overflow_conservation": func(r *Reply) {
			r.AcceptedSamples = 160
			r.SentSamples = math.MaxUint64 - 159
			r.DiscardedSamples = 320
		},
		"fake_empty_age": func(r *Reply) { r.OldestAgeMS = 1 },
		"wrong_bytes":    func(r *Reply) { r.AcceptedSamples = 160; r.QueuedSamples = 160; r.QueuedMS = 20; r.QueuedBytes = 319 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if err := w.validateReply(r, bad); err == nil {
				t.Fatalf("坏PCM回执通过：%+v", bad)
			}
		})
	}
	for _, bad := range []Request{{Op: "pcm_turn_begin", Session: 7, TurnID: 1}, {Op: "pcm_turn_begin", Session: 7, TurnID: 1, BufferMS: 1001, PrebufferMS: 20}, {Op: "pcm_turn_begin", Session: 7, TurnID: 1, BufferMS: 20, PrebufferMS: 40}, {Op: "pcm_turn_status", Session: 7}, {Op: "pcm_turn_end", Session: 7, TurnID: 1, FinalSamples: 161}, {Op: "pcm_turn_interrupt", Session: 7, TurnID: 1, FadeMS: 60}} {
		if err := validatePCMRequest(bad); err == nil {
			t.Fatalf("坏PCM请求通过：%+v", bad)
		}
	}
	if err := w.validateReply(r, Reply{OK: false, Type: "error", Message: "unknown_turn"}); err != nil {
		t.Fatalf("确定业务拒绝不应终止进程：%v", err)
	}
	wire, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"accepted_samples", "sent_samples", "discarded_samples", "queued_samples", "queued_bytes", "queued_ms", "oldest_age_ms", "buffer_ms", "prebuffer_ms", "error"} {
		saved := fields[key]
		for _, missing := range []bool{true, false} {
			if missing {
				delete(fields, key)
			} else {
				fields[key] = json.RawMessage("null")
			}
			broken, _ := json.Marshal(fields)
			var reply Reply
			if json.Unmarshal(broken, &reply) == nil {
				t.Fatalf("必填字段%s缺失/null被补成成功零值", key)
			}
		}
		fields[key] = saved
	}
}

func TestPCMBinaryReplyStrictIdentityAndAccounting(t *testing.T) {
	j := &pcmJob{session: 5, turnID: math.MaxUint64, samples: make([]int16, 160)}
	valid := pcmTestWire(9, j)
	if _, err := decodePCMReply(valid, 9, j); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 6, 7, 8, 16, 24, 32, 40, 48, 56, 64, 80, 81, 95} {
		t.Run("mutate_"+strconv.Itoa(offset), func(t *testing.T) {
			bad := append([]byte(nil), valid...)
			bad[offset] ^= 0xff
			if _, err := decodePCMReply(bad, 9, j); err == nil {
				t.Fatalf("损坏偏移%d被接受", offset)
			}
		})
	}
	for _, size := range []int{0, 4, 95, 97} {
		if _, err := decodePCMReply(make([]byte, size), 9, j); err == nil {
			t.Fatalf("错误长度%d被接受", size)
		}
	}
	stale := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(stale[4:6], 5)
	binary.LittleEndian.PutUint64(stale[32:40], 6)
	if r, err := decodePCMReply(stale, 9, j); err != nil || r.CurrentTurnID != 6 || r.TurnID != j.turnID || r.Error != "stale_turn" {
		t.Fatalf("旧轮次拒绝必须保留真实当前轮次：%+v %v", r, err)
	}
}

func newPCMTestPool(t *testing.T) (*Pool, *pcmLane, net.Conn) {
	t.Helper()
	lane, child, err := newPCMLane()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.FileConn(child)
	_ = child.Close()
	if err != nil {
		lane.close()
		t.Fatal(err)
	}
	w := &Worker{pcm: lane, capabilities: []string{pcmCapability}, urgent: make(chan job, 64), jobs: make(chan job, 64)}
	w.Healthy.Store(true)
	w.Generation.Store(1)
	w.PID.Store(int64(os.Getpid()))
	p := &Pool{Workers: []*Worker{w}}
	t.Cleanup(func() { _ = peer.Close(); lane.close() })
	return p, lane, peer
}

func readPCMTestRequest(t *testing.T, peer net.Conn) ([]byte, *pcmJob, uint64) {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	wire := make([]byte, 1645)
	n, err := peer.Read(wire)
	if err != nil {
		t.Fatal(err)
	}
	wire = wire[:n]
	if n < 364 || string(wire[:4]) != "RSP1" || wire[4] != 0 || wire[5] != 0 || wire[6] != 0 || wire[7] != 0 || wire[42] != 0 || wire[43] != 0 {
		t.Fatalf("真实写出请求头错误：%x", wire)
	}
	count := int(binary.LittleEndian.Uint16(wire[40:42]))
	if count%160 != 0 || count > pcmMaxSamples || n != 44+count*2 {
		t.Fatalf("请求包长度或样本数错误：%d %d", n, count)
	}
	j := &pcmJob{session: binary.LittleEndian.Uint64(wire[16:24]), turnID: binary.LittleEndian.Uint64(wire[24:32]), offset: binary.LittleEndian.Uint64(wire[32:40]), samples: make([]int16, count)}
	for i := range j.samples {
		j.samples[i] = int16(binary.LittleEndian.Uint16(wire[44+i*2 : 46+i*2]))
	}
	return wire, j, binary.LittleEndian.Uint64(wire[8:16])
}

func pushPCMTestAsync(p *Pool, ctx context.Context, offset uint64, samples []int16) <-chan pcmResult {
	done := make(chan pcmResult, 1)
	go func() { r, err := p.PushPCM(ctx, 0, 1, 7, 9, offset, samples); done <- pcmResult{r, err} }()
	return done
}

func waitPCMQueue(t *testing.T, lane *pcmLane, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(lane.jobs) != count && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(lane.jobs) != count {
		t.Fatalf("队列长度=%d，期望%d", len(lane.jobs), count)
	}
}

func TestPCMTransportCopiesQueuedSamplesAndRejectsStaleGeneration(t *testing.T) {
	p, lane, peer := newPCMTestPool(t)
	first := pushPCMTestAsync(p, context.Background(), 0, make([]int16, 160))
	_, request, id := readPCMTestRequest(t, peer)
	samples := make([]int16, 800)
	for i := range samples {
		samples[i] = int16(i*71 - 30000)
	}
	second := pushPCMTestAsync(p, context.Background(), 160, samples)
	waitPCMQueue(t, lane, 1)
	lane.mu.Lock() // 与入队复制建立明确同步，测试不能制造调用者并发改写的未定义用法。
	lane.mu.Unlock()
	for i := range samples {
		samples[i] = 0
	}
	_, _ = peer.Write(pcmTestWire(id, request))
	if result := <-first; result.err != nil {
		t.Fatal(result.err)
	}
	_, copied, next := readPCMTestRequest(t, peer)
	for i, sample := range copied.samples {
		if sample != int16(i*71-30000) {
			t.Fatalf("样本%d未独占：%d", i, sample)
		}
	}
	if next != id+1 || copied.offset != 160 {
		t.Fatalf("序号/偏移错误：%d %+v", next, copied)
	}
	_, _ = peer.Write(pcmTestWire(next, copied))
	if result := <-second; result.err != nil || result.reply.AcceptedSamples != 960 {
		t.Fatalf("%+v", result)
	}
	if _, err := p.PushPCM(context.Background(), 0, 2, 7, 9, 960, samples); !errors.Is(err, ErrPCMUnavailable) {
		t.Fatalf("旧/未知代次未拒绝：%v", err)
	}
	for _, size := range []int{0, 159, 161, 801} {
		if _, err := p.PushPCM(context.Background(), 0, 1, 7, 9, 960, make([]int16, size)); err == nil {
			t.Fatalf("非法样本%d被接受", size)
		}
	}
}

func TestPCMQueuedCancellationDoesNotWriteAndInflightCancellationIsUnknown(t *testing.T) {
	p, lane, peer := newPCMTestPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := pushPCMTestAsync(p, ctx, 0, make([]int16, 160))
	_, request, id := readPCMTestRequest(t, peer)
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queued := pushPCMTestAsync(p, queuedCtx, 160, make([]int16, 160))
	waitPCMQueue(t, lane, 1)
	cancelQueued()
	if result := <-queued; !errors.Is(result.err, context.Canceled) || errors.Is(result.err, ErrPCMOutcomeUnknown) {
		t.Fatalf("排队取消结果错误：%v", result.err)
	}
	cancel()
	if result := <-first; !errors.Is(result.err, ErrPCMOutcomeUnknown) {
		t.Fatalf("在途取消伪称未受理：%v", result.err)
	}
	_, _ = peer.Write(pcmTestWire(id, request))
	third := pushPCMTestAsync(p, context.Background(), 160, make([]int16, 160))
	_, actual, actualID := readPCMTestRequest(t, peer)
	if actualID != id+1 {
		t.Fatalf("取消的排队任务仍写出：%d -> %d", id, actualID)
	}
	_, _ = peer.Write(pcmTestWire(actualID, actual))
	if result := <-third; result.err != nil {
		t.Fatalf("正确消费迟到回复后后续任务失败：%v", result.err)
	}
}

func TestPCMQueueBoundAndCloseDrain(t *testing.T) {
	p, lane, peer := newPCMTestPool(t)
	first := pushPCMTestAsync(p, context.Background(), 0, make([]int16, 160))
	readPCMTestRequest(t, peer)
	waiters := make([]<-chan pcmResult, 0, pcmQueueSize)
	for i := 0; i < pcmQueueSize; i++ {
		waiters = append(waiters, pushPCMTestAsync(p, context.Background(), 160, make([]int16, 160)))
		waitPCMQueue(t, lane, i+1)
	}
	if _, err := p.PushPCM(context.Background(), 0, 1, 7, 9, 160, make([]int16, 160)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("64等待+1在途上限没有生效：%v", err)
	}
	samples := make([]int16, 160)
	if allocations := testing.AllocsPerRun(20, func() {
		_, err := p.PushPCM(context.Background(), 0, 1, 7, 9, 160, samples)
		if !errors.Is(err, ErrQueueFull) {
			panic("queue is not full")
		}
	}); allocations != 0 {
		t.Fatalf("满队列拒绝仍创建样本/任务分配：%g", allocations)
	}
	lane.close()
	if result := <-first; !errors.Is(result.err, ErrPCMOutcomeUnknown) {
		t.Fatalf("关闭在途任务必须结果未知：%v", result.err)
	}
	for _, waiter := range waiters {
		if result := <-waiter; !errors.Is(result.err, ErrPCMUnavailable) {
			t.Fatalf("未发任务未确定拒绝：%v", result.err)
		}
	}
	if _, err := p.PushPCM(context.Background(), 0, 1, 7, 9, 160, make([]int16, 160)); !errors.Is(err, ErrPCMUnavailable) {
		t.Fatalf("关闭后仍可写：%v", err)
	}
	if err := p.Submit(context.Background(), 0, Request{Op: "pcm_turn_status", Generation: 1, Session: 7, TurnID: 9}, func(Reply, error) {}); err != nil {
		t.Fatalf("数据失败不应阻止JSON对账：%v", err)
	}
	if err := p.Submit(context.Background(), 0, Request{Op: "pcm_turn_status", Session: 7, TurnID: 9}, func(Reply, error) {}); err == nil {
		t.Fatal("新PCM控制命令没有显式代次却进入了当前进程")
	}
}

// pcmRealTestPorts仅预留自有四端口块，不占用主服务媒体范围；分配前归还临时预留。
func pcmRealTestPorts(t *testing.T) (int, func()) {
	t.Helper()
	for attempt := 0; attempt < 64; attempt++ {
		first, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		base := first.LocalAddr().(*net.UDPAddr).Port
		if base < 40000 || base%2 != 0 || base > 65532 {
			_ = first.Close()
			continue
		}
		reserved := []*net.UDPConn{first}
		for offset := 1; offset < 4; offset++ {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: base + offset})
			if err != nil {
				break
			}
			reserved = append(reserved, conn)
		}
		closeAll := func() {
			for _, conn := range reserved {
				_ = conn.Close()
			}
		}
		if len(reserved) == 4 {
			return base, closeAll
		}
		closeAll()
	}
	t.Fatal("无法取得隔离四端口块")
	return 0, func() {}
}

// TestRealPCMTurnPoolAndDatagram验证真实FD3+JSON+G711 TX完整路径，不以IPC夹具冒充媒体。
// 需要RUSTSWITCH_REAL_MEDIA指向具有pcm_turn_v1的候选；缺能力、缺程序、无声音均失败而非skip。
func TestRealPCMTurnPoolAndDatagram(t *testing.T) {
	binaryPath := realLocalMediaBinary(t)
	before := realLocalBinaryHash(t, binaryPath)
	base, releasePorts := pcmRealTestPorts(t)
	defer releasePorts()
	rtp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer rtp.Close()
	rtcp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer rtcp.Close()
	cfg, err := config.Load("../../../config/local.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Media.Binary, cfg.Media.Processing, cfg.Media.PlaybackRoot = binaryPath, "g711", ""
	cfg.Media.Workers, cfg.Media.BindIP, cfg.Media.PortStart, cfg.Media.PortEnd = 1, "127.0.0.1", base, base+3
	cfg.Media.CPUCores, cfg.Media.ExcludedPortBlocks, cfg.Media.AllowedRemoteNetworks = nil, nil, []string{"127.0.0.0/8"}
	cfg.Limits.MaxCalls = 1
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	pool, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	w := pool.Workers[0]
	if !w.SupportsPCMTurn() || !w.SupportsLocalProcessing() {
		t.Fatalf("真实候选缺少新双通道能力：%+v", w.CapabilitySnapshot())
	}
	identity, _ := json.Marshal(w.CapabilitySnapshot())
	t.Logf("actual_pcm_worker=%s binary_sha256=%s", identity, before)
	call := func(request Request) Reply {
		t.Helper()
		request.Generation = w.Generation.Load()
		reply, err := pool.Call(ctx, 0, request)
		if err != nil {
			t.Fatalf("实际%s失败：%+v %v", request.Op, reply, err)
		}
		encoded, _ := json.Marshal(reply)
		t.Logf("decoded_pcm_control op=%s reply=%s", request.Op, encoded)
		return reply
	}
	releasePorts()
	allocated := call(Request{Op: "allocate", Session: 7, Processing: G711LocalProcessingPlan(), A: &Peer{RTP: rtp.LocalAddr().String(), RTCP: rtcp.LocalAddr().String()}, Payload: 0, Codec: &CodecSpec{Name: "PCMU", SampleRate: 8000, RTPClockRate: 8000, Channels: 1, PTimeMS: 20}})
	call(Request{Op: "pcm_turn_begin", Session: 7, TurnID: 1, BufferMS: 200, PrebufferMS: 40})
	empty := call(Request{Op: "pcm_turn_end", Session: 7, TurnID: 1, FinalSamples: 0})
	if empty.State != "completed" || empty.AcceptedSamples != 0 {
		t.Fatalf("真实空end未完成：%+v", empty)
	}
	call(Request{Op: "pcm_turn_begin", Session: 7, TurnID: 2, BufferMS: 200, PrebufferMS: 40})
	samples := make([]int16, 320)
	for i := range samples {
		if (i+i/160)%2 == 0 {
			samples[i] = 1000
		} else {
			samples[i] = -1000
		}
	}
	accepted, err := pool.PushPCM(ctx, 0, w.Generation.Load(), 7, 2, 0, samples)
	if err != nil || accepted.AcceptedSamples != 320 || accepted.Code != 0 {
		t.Fatalf("实际二进制PCM没有原子受理：%+v %v", accepted, err)
	}
	encoded, _ := json.Marshal(accepted)
	t.Logf("decoded_pcm_datagram_reply=%s", encoded)
	call(Request{Op: "pcm_turn_end", Session: 7, TurnID: 2, FinalSamples: 320})
	var previous []byte
	for frame := 0; frame < 2; frame++ {
		_ = rtp.SetReadDeadline(time.Now().Add(time.Second))
		wire := make([]byte, 2048)
		n, source, err := rtp.ReadFromUDP(wire)
		if err != nil {
			t.Fatalf("真实PCM音频帧%d缺失：%v", frame, err)
		}
		wire = wire[:n]
		t.Logf("actual_pcm_rtp source=%s hex=%x", source, wire)
		if n != 172 || wire[0] != 0x80 || wire[1]&0x7f != 0 || source.Port != allocated.ARTP || !source.IP.Equal(net.IPv4(127, 0, 0, 1)) || binary.BigEndian.Uint32(wire[8:12]) == 0 {
			t.Fatalf("实际RTP格式/来源错误：%x %s", wire, source)
		}
		for i, value := range wire[12:] {
			want := byte(0xce) // G.711 μ-law的独立已知向量：+1000 -> CE，-1000 -> 4E。
			if samples[frame*160+i] < 0 {
				want = 0x4e
			}
			if value != want {
				t.Fatalf("真实PCM编码内容错误 frame=%d sample=%d got=%x want=%x", frame, i, value, want)
			}
		}
		if previous != nil && (binary.BigEndian.Uint16(wire[2:4])-binary.BigEndian.Uint16(previous[2:4]) != 1 || binary.BigEndian.Uint32(wire[4:8])-binary.BigEndian.Uint32(previous[4:8]) != 160 || string(wire[8:12]) != string(previous[8:12])) {
			t.Fatal("真实两帧RTP身份/序号/时间戳不连续")
		}
		previous = wire
	}
	var status Reply
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status = call(Request{Op: "pcm_turn_status", Session: 7, TurnID: 2})
		if status.State == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status.State != "completed" || status.SentSamples != 320 || status.AcceptedSamples != 320 || status.DiscardedSamples != 0 || status.QueuedSamples != 0 {
		t.Fatalf("真实输出与状态不守恒：%+v", status)
	}
	call(Request{Op: "release", Session: 7})
	stats := call(Request{Op: "stats"})
	if stats.Stats["processed_active_calls"] != float64(0) || stats.Stats["processed_local_active_calls"] != float64(0) {
		t.Fatal("真实媒体释放后活跃计数未归零", stats.Stats)
	}
	_ = rtp.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	if n, _, err := rtp.ReadFromUDP(make([]byte, 2048)); err == nil || n != 0 {
		t.Fatal("实际完成/释放后仍有额外RTP", n, err)
	}
	pool.Close()
	if w.Healthy.Load() || w.PID.Load() != 0 || w.SupportsPCMTurn() {
		t.Fatal("真实进程关闭后残留可写状态")
	}
	if after := realLocalBinaryHash(t, binaryPath); after != before {
		t.Fatal("实际测试期间二进制变化", before, after)
	}
	t.Log(fmt.Sprintf("actual_pcm_summary frames=2 samples=320 empty_end=true released=true binary=%s sha256=%s", binaryPath, before))
}

func TestPCMTimeoutAndMalformedReplyIsolateOnlyDataLane(t *testing.T) {
	for _, mode := range []string{"timeout", "oversized", "wrong_request"} {
		t.Run(mode, func(t *testing.T) {
			p, lane, peer := newPCMTestPool(t)
			lane.timeout = 40 * time.Millisecond // 入队之前冻结测试期限，生产固定一秒。
			pending := pushPCMTestAsync(p, context.Background(), 0, make([]int16, 160))
			_, j, id := readPCMTestRequest(t, peer)
			if mode != "timeout" {
				wire := pcmTestWire(id, j)
				if mode == "oversized" {
					wire = append(wire, 0)
				} else {
					wire[8]++
				}
				_, _ = peer.Write(wire)
			}
			if result := <-pending; !errors.Is(result.err, ErrPCMOutcomeUnknown) {
				t.Fatalf("未知结果被掩盖：%v", result.err)
			}
			<-lane.done
			if !p.Workers[0].Healthy.Load() || !p.Workers[0].SupportsPCMTurn() {
				t.Fatal("数据失联错误关闭了正常控制能力")
			}
			if _, err := p.PushPCM(context.Background(), 0, 1, 7, 9, 160, make([]int16, 160)); !errors.Is(err, ErrPCMUnavailable) {
				t.Fatalf("隔离后仍允许盲目重发：%v", err)
			}
		})
	}
}

func TestPCMBusinessRejectionKeepsDataChannelUsable(t *testing.T) {
	p, _, peer := newPCMTestPool(t)
	pending := pushPCMTestAsync(p, context.Background(), 0, make([]int16, 160))
	_, j, id := readPCMTestRequest(t, peer)
	wire := pcmTestWire(id, j)
	binary.LittleEndian.PutUint16(wire[4:6], 8)
	_, _ = peer.Write(wire)
	result := <-pending
	var rejected *PCMRejection
	if !errors.As(result.err, &rejected) || rejected.Code != 8 || result.reply.Error != "queue_full" {
		t.Fatalf("拒绝没有分类：%+v", result)
	}
	next := pushPCMTestAsync(p, context.Background(), 0, make([]int16, 160))
	_, j, id = readPCMTestRequest(t, peer)
	_, _ = peer.Write(pcmTestWire(id, j))
	if result := <-next; result.err != nil {
		t.Fatal(result.err)
	}
}

func TestPCMInheritedDescriptorAndHandshakeGate(t *testing.T) {
	for _, mode := range []string{"supported", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("RUSTSWITCH_PCM_MEDIA_HELPER", mode)
			binaryPath, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{Media: config.Media{Binary: binaryPath, Workers: 1, PortStart: 22000, PortEnd: 22003}, Limits: config.Limits{MaxCalls: 1}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pool, err := Start(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			worker := pool.Workers[0]
			if worker.SupportsPCMTurn() != (mode == "supported") {
				t.Fatal("能力与实际握手不符")
			}
			request := Request{Op: "pcm_turn_begin", Generation: worker.Generation.Load(), Session: 7, TurnID: 9, BufferMS: 200, PrebufferMS: 40}
			_, beginErr := pool.Call(ctx, 0, request)
			if mode == "supported" {
				// 真实管道验证0样本end字段存在；新轮次再测试二进制数据，不向已结束轮次写入。
				ended, endErr := pool.Call(ctx, 0, Request{Op: "pcm_turn_end", Generation: worker.Generation.Load(), Session: 7, TurnID: 9, FinalSamples: 0})
				if endErr != nil || ended.State != "completed" {
					t.Fatalf("空end管道失败：%+v %v", ended, endErr)
				}
				request.TurnID = 10
				if _, err := pool.Call(ctx, 0, request); err != nil {
					t.Fatal(err)
				}
			}
			reply, pushErr := pool.PushPCM(ctx, 0, worker.Generation.Load(), 7, request.TurnID, 0, make([]int16, 160))
			if mode == "legacy" {
				if !errors.Is(beginErr, ErrPCMUnavailable) || !errors.Is(pushErr, ErrPCMUnavailable) {
					t.Fatalf("旧worker被隐式开放：%v %v", beginErr, pushErr)
				}
			} else if beginErr != nil || pushErr != nil || reply.AcceptedSamples != 160 {
				t.Fatalf("实际FD3请求失败：%+v %v %v", reply, beginErr, pushErr)
			}
			pool.Close()
			if worker.Healthy.Load() || worker.PID.Load() != 0 || worker.SupportsPCMTurn() {
				t.Fatal("Close后残留健康身份或数据能力")
			}
		})
	}
}
