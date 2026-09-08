package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rustswitch/control/internal/config"
	"rustswitch/control/internal/media"
)

// 测试帧按公开字节偏移独立构造，不调用生产解析器生成请求。
func endpointHeader(op byte, id uint64, mode uint16) []byte {
	b := make([]byte, 64)
	copy(b, "RVA1")
	b[4] = op
	binary.LittleEndian.PutUint64(b[8:16], id)
	binary.LittleEndian.PutUint16(b[52:54], mode)
	return b
}

// 满载请求不能把后台分片回收变成每请求全表扫描，数据查找不应随MaxStreams放大工作量。
func TestPCMEndpointFullRegistryDefersRetirementToBoundedCollector(t *testing.T) {
	e := &pcmEndpoint{byUUID: make(map[string]*pcmEntry), byToken: make(map[[16]byte]*pcmEntry), entries: make([]*pcmEntry, 10000), free: make([]int, 0, 10000)}
	for i := range e.entries {
		h := &PCMHandle{}
		h.retired.Store(true)
		entry := &pcmEntry{uuid: fmt.Sprint(i), slot: i, handle: h}
		binary.LittleEndian.PutUint64(entry.token[:8], uint64(i+1))
		e.entries[i], e.byUUID[entry.uuid], e.byToken[entry.token] = entry, entry, entry
	}
	for i := 0; i < 100; i++ {
		result := e.begin(context.Background(), pcmWireRequest{turn: 1, arg1: 200, arg2: 40}, []byte("new-call"))
		if result.code != 7 || len(e.byUUID) != 10000 {
			t.Fatalf("full admission bypassed collector budget: code=%d entries=%d", result.code, len(e.byUUID))
		}
	}
	e.collectLocked(256)
	if len(e.free) != 256 || len(e.byUUID) != 9744 || len(e.byToken) != 9744 {
		t.Fatal("bounded collector did not reclaim exactly its allotted retired streams")
	}
}

// 控制队列和Rust音频缓冲满都对外返回背压；偏移错误与未知结果不能混成同一重试语义。
func TestPCMEndpointBackpressureAndUnknownErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code uint16
	}{
		{"control_queue", media.ErrQueueFull, 5},
		{"audio_queue", &media.PCMRejection{Code: 8, Name: "queue_full"}, 5},
		{"offset", &media.PCMRejection{Code: 7, Name: "offset"}, 3},
		{"shape", &media.PCMRejection{Code: 1, Name: "invalid"}, 3},
		{"unknown_audio", media.ErrPCMOutcomeUnknown, 6},
		{"unknown_begin", ErrPCMBeginUnknown, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := pcmWireError(tc.err)
			if result.code != tc.code || result.message == "" {
				t.Fatalf("wrong recovery classification: %+v", result)
			}
		})
	}
}

// 故障夹具暂停真实SDK提交回调，验证未知结果仍占预算且有可恢复token；不计为RTP验收。
func TestPCMEndpointUnknownBeginKeepsBudgetAndForgetRequiresRealTerminal(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	e := &pcmEndpoint{server: s, byUUID: make(map[string]*pcmEntry), byToken: make(map[[16]byte]*pcmEntry), entries: make([]*pcmEntry, 1), free: []int{0}}
	completed := make(chan pcmWireResult, 1)
	go func() {
		completed <- e.begin(context.Background(), pcmWireRequest{turn: 1, arg1: 200, arg2: 40}, []byte(c.compatUUIDs[0]))
	}()
	request := <-s.pcm.requests
	s.handlePCMBegin(request)
	f.mu.Lock()
	pending := f.pending[0]
	f.pending = f.pending[1:]
	f.mu.Unlock()
	pending.callback(media.Reply{}, media.ErrPCMOutcomeUnknown)
	s.handlePCMBeginResult(<-s.pcm.results)
	result := <-completed
	if result.code != 6 || result.token == [16]byte{} || len(e.free) != 0 || !s.pcmOwnsTX(c) {
		t.Fatalf("unknown begin lost token/budget/TX ownership: %+v", result)
	}
	if value := e.begin(context.Background(), pcmWireRequest{turn: 1, arg1: 200, arg2: 40}, []byte("another-call")); value.code != 7 {
		t.Fatalf("unknown stream released capacity: %+v", value)
	}
	var samples [800]int16
	r := pcmWireRequest{op: pcmStatus, token: result.token, turn: 1}
	if status := e.execute(context.Background(), r, nil, &samples); status.code != 0 || status.state != "playing" {
		t.Fatalf("unknown begin cannot be queried: %+v", status)
	}
	f.callErr = context.DeadlineExceeded
	r.op = pcmForget
	if value := e.execute(context.Background(), r, nil, &samples); value.code != 6 || len(e.free) != 0 {
		t.Fatalf("unknown stop discarded token/budget: %+v", value)
	}
	f.callErr, f.callState = nil, "stopped"
	if value := e.execute(context.Background(), r, nil, &samples); value.code != 0 || len(e.free) != 1 || len(e.byToken) != 0 || s.pcmOwnsTX(c) {
		t.Fatalf("confirmed stop failed to release reservation: %+v", value)
	}
}

func TestPCMEndpointCancelledSubmittedBeginPublishesRecoverableToken(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	e := &pcmEndpoint{server: s, byUUID: make(map[string]*pcmEntry), byToken: make(map[[16]byte]*pcmEntry), entries: make([]*pcmEntry, 1), free: []int{0}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan pcmWireResult, 1)
	go func() {
		completed <- e.begin(ctx, pcmWireRequest{turn: 1, arg1: 200, arg2: 40}, []byte(c.compatUUIDs[0]))
	}()
	request := <-s.pcm.requests
	s.handlePCMBegin(request)
	cancel()
	result := <-completed
	if result.code != 6 || result.token == [16]byte{} || len(e.free) != 0 {
		t.Fatalf("cancelled submitted begin lost reservation: %+v", result)
	}
	var samples [800]int16
	if value := e.execute(context.Background(), pcmWireRequest{op: pcmStatus, token: result.token, turn: 1}, nil, &samples); value.code != 5 {
		t.Fatalf("pending token must report busy until actual disposition: %+v", value)
	}
	f.complete(t, "buffering", nil)
	s.handlePCMBeginResult(<-s.pcm.results)
	if !s.pcmOwnsTX(c) || !c.pcmHandle.inputClosed.Load() {
		t.Fatal("abandoned begin allowed audio or lost uncertain TX ownership")
	}
	s.retireCallPCM(c, true)
	e.collectLocked(256)
	if len(e.free) != 1 || len(e.byToken) != 0 {
		t.Fatal("ended call left an orphaned pending stream reservation")
	}
}

// 候选替换被取消且最终明确拒绝时，旧轮仍可能播放，不能让GC释放旧预算或丢掉旧控制令牌。
func TestPCMEndpointRejectedPendingReplacementRestoresOldOwner(t *testing.T) {
	s, c, f, _ := pcmSDKFixture(t)
	old := pcmSDKBegin(t, s, c, f, 1)
	oldToken := [16]byte{1}
	entry := &pcmEntry{uuid: c.compatUUIDs[0], slot: 0, handle: old, token: oldToken}
	e := &pcmEndpoint{server: s, byUUID: map[string]*pcmEntry{entry.uuid: entry}, byToken: map[[16]byte]*pcmEntry{oldToken: entry}, entries: []*pcmEntry{entry}, free: make([]int, 0, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan pcmWireResult, 1)
	go func() { completed <- e.begin(ctx, pcmWireRequest{turn: 2, arg1: 200, arg2: 40}, []byte(entry.uuid)) }()
	request := <-s.pcm.requests
	s.handlePCMBegin(request)
	cancel()
	result := <-completed
	if result.code != 6 || result.token == oldToken || result.token == [16]byte{} || len(e.byToken) != 2 || len(e.free) != 0 {
		t.Fatalf("unknown replacement lost either owner: %+v", result)
	}
	var samples [800]int16
	requestWire := pcmWireRequest{op: pcmStatus, token: oldToken, turn: 1}
	if value := e.execute(context.Background(), requestWire, nil, &samples); value.code != 0 {
		t.Fatalf("previous owner unavailable during pending replacement: %+v", value)
	}
	requestWire.op = pcmForget
	if value := e.execute(context.Background(), requestWire, nil, &samples); value.code != 5 || len(e.free) != 0 {
		t.Fatalf("old forget bypassed pending new owner: %+v", value)
	}
	f.complete(t, "", errors.New("deterministic begin rejection"))
	s.handlePCMBeginResult(<-s.pcm.results)
	e.collectLocked(256)
	if len(e.free) != 0 || len(e.byToken) != 1 || entry.handle != old || entry.token != oldToken || entry.previous != nil || !s.pcmOwnsTX(c) {
		t.Fatal("rejected replacement retired/released the still active prior owner")
	}
	f.callState = "stopped"
	if value := e.execute(context.Background(), requestWire, nil, &samples); value.code != 0 || len(e.free) != 1 {
		t.Fatalf("restored owner cannot stop and release: %+v", value)
	}
}

func endpointReply(t *testing.T, c net.Conn, id uint64) (uint16, []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 96)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatal(err)
	}
	if string(b[:4]) != "RVR1" || binary.LittleEndian.Uint64(b[8:16]) != id {
		t.Fatalf("bad reply identity %x", b)
	}
	n := binary.LittleEndian.Uint16(b[92:94])
	if n > 512 {
		t.Fatalf("unbounded reply %d", n)
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(c, msg); err != nil {
		t.Fatal(err)
	}
	if b[94] != 0 || b[95] != 0 {
		t.Fatal("reserved reply bits")
	}
	return binary.LittleEndian.Uint16(b[6:8]), b
}

func endpointFixture(t *testing.T) (*Server, string) {
	t.Helper()
	// Unix路径在macOS有严格字节上限，使用本测试拥有的短私有目录。
	dir, err := os.MkdirTemp("/tmp", "rva-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Server{ctx: ctx, Config: config.Config{Limits: config.Limits{MaxCalls: 4}, PCMStream: &config.PCMStream{SocketPath: filepath.Join(dir, "a.sock"), MaxConnections: 4, MaxStreams: 4}}}
	if err = s.startPCMTransport(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.closePCMTransport)
	return s, dir
}

func endpointConnect(t *testing.T, s *Server, mode uint16) *net.UnixConn {
	t.Helper()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: s.Config.PCMStream.SocketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err = c.Write(endpointHeader(0, 1, mode)); err != nil {
		t.Fatal(err)
	}
	if code, _ := endpointReply(t, c, 1); code != 0 {
		t.Fatalf("hello code %d", code)
	}
	return c
}

func TestPCMEndpointClassesAndMalformedFrames(t *testing.T) {
	s, _ := endpointFixture(t)
	d1 := endpointConnect(t, s, 2)
	d2 := endpointConnect(t, s, 2)
	// 数据类已满也必须留控制类配额，不能被慢音频连接占满全部槽位。
	d3, err := net.Dial("unix", s.Config.PCMStream.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d3.Close()
	d3.Write(endpointHeader(0, 1, 2))
	if code, _ := endpointReply(t, d3, 1); code != 7 {
		t.Fatalf("data class limit code %d", code)
	}
	c := endpointConnect(t, s, 1)
	c.Write(endpointHeader(7, 2, 0))
	if code, _ := endpointReply(t, c, 2); code != 0 {
		t.Fatal(code)
	}
	// 在数据连接发送控制命令返回明确错误，连接仍可继续PING。
	r := endpointHeader(5, 2, 0)
	r[16] = 1
	r[32] = 1
	d1.Write(r)
	if code, _ := endpointReply(t, d1, 2); code != 9 {
		t.Fatal(code)
	}
	d1.Write(endpointHeader(7, 3, 0))
	if code, _ := endpointReply(t, d1, 3); code != 0 {
		t.Fatal(code)
	}
	// 相同request_id禁止重放；非法保留位也不能被忽略。
	d1.Write(endpointHeader(7, 3, 0))
	if code, _ := endpointReply(t, d1, 3); code != 1 {
		t.Fatal(code)
	}
	r = endpointHeader(7, 2, 0)
	r[63] = 1
	d2.Write(r)
	if code, _ := endpointReply(t, d2, 2); code != 1 {
		t.Fatal(code)
	}
	var one [1]byte
	if _, err = d2.Read(one[:]); err != io.EOF {
		t.Fatalf("invalid frame not closed: %v", err)
	}
}

func TestPCMEndpointFrameDeadlineAndCloseWakeReader(t *testing.T) {
	s, _ := endpointFixture(t)
	c := endpointConnect(t, s, 1)
	start := time.Now()
	c.Write([]byte{'R'})
	// 首字节之后的帧只有一秒总期限，完整帧不来必须断开，不能等30秒空闲期限。
	c.SetReadDeadline(start.Add(1800 * time.Millisecond))
	var b [1]byte
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatalf("slow frame remained open: %v", err)
	}
	if time.Since(start) < 800*time.Millisecond {
		t.Fatal("frame deadline ended prematurely")
	}
	d := endpointConnect(t, s, 2)
	start = time.Now()
	s.closePCMTransport()
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("shutdown waited for idle read deadline")
	}
	d.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := d.Read(b[:]); err != io.EOF {
		t.Fatal(err)
	}
	if _, err := os.Lstat(s.Config.PCMStream.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("owned socket not removed: %v", err)
	}
}

func TestPCMEndpointPrivateDirectoryAndPathOwnership(t *testing.T) {
	s, dir := endpointFixture(t)
	info, err := os.Lstat(s.Config.PCMStream.SocketPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", info, err)
	}
	o := s.Config.PCMStreamOptions()
	if _, err := newPCMEndpoint(s, o); err == nil {
		t.Fatal("existing listener path was replaced")
	}
	old := o.SocketPath + ".moved"
	if err := os.Rename(o.SocketPath, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.SocketPath, []byte("another owner"), 0600); err != nil {
		t.Fatal(err)
	}
	s.closePCMTransport()
	if raw, err := os.ReadFile(o.SocketPath); err != nil || string(raw) != "another owner" {
		t.Fatalf("shutdown deleted unrelated file: %q %v", raw, err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	o.SocketPath = filepath.Join(dir, "b.sock")
	if _, err := newPCMEndpoint(s, o); err == nil {
		t.Fatal("public parent accepted")
	}
}

func TestPCMEndpointHeaderLimitsAndExactCounts(t *testing.T) {
	s, _ := endpointFixture(t)
	c := endpointConnect(t, s, 1)
	r := endpointHeader(1, 2, 200)
	r[32] = 1
	binary.LittleEndian.PutUint16(r[54:56], 40)
	binary.LittleEndian.PutUint32(r[48:52], 1601)
	c.Write(r) // 不发送body；必须在分配/等待1601字节前拒绝。
	if code, _ := endpointReply(t, c, 2); code != 1 {
		t.Fatal(code)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	result := pcmWireResult{turn: 1<<63 + 7, accepted: 8000, sent: 160, queued: 320, discarded: 7520, age: 12, buffer: 1000, prebuffer: 40, state: "playing"}
	var wire [pcmWireReply + pcmWireMessage]byte
	done := make(chan error, 1)
	go func() { done <- writePCMResult(a, pcmWireRequest{op: 5, id: 4}, result, &wire) }()
	_, raw := endpointReply(t, b, 4)
	if binary.LittleEndian.Uint64(raw[32:40]) != result.turn || binary.LittleEndian.Uint64(raw[40:48]) != 8000 || binary.LittleEndian.Uint32(raw[80:84]) != 640 || binary.LittleEndian.Uint32(raw[84:88]) != 40 {
		t.Fatalf("numeric precision/units lost: %x", raw)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
