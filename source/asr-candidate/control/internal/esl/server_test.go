package esl

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testPeer struct {
	conn   net.Conn
	reader *bufio.Reader
}

// startTestServer 每个测试绑定独立临时端口，避免使用主服务及现网凭据。
func startTestServer(t *testing.T, edit func(*Options)) *Server {
	t.Helper()
	o := Options{Listen: "127.0.0.1:0", Password: "test-only-secret", API: func(_ context.Context, command, args string) string {
		if command == "echo" {
			return args
		}
		return "-ERR " + command + " Command not found!\n"
	}}
	if edit != nil {
		edit(&o)
	}
	s, err := Listen(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func dialPeer(t *testing.T, s *Server, authenticate bool) *testPeer {
	t.Helper()
	conn, err := net.DialTimeout("tcp", s.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p := &testPeer{conn, bufio.NewReader(conn)}
	t.Cleanup(func() { conn.Close() })
	if f := p.read(t); f.Get("Content-Type") != "auth/request" {
		t.Fatalf("bad greeting %#v", f)
	}
	if authenticate {
		p.send(t, "auth test-only-secret\n\n")
		if f := p.read(t); f.Get("Reply-Text") != "+OK accepted" {
			t.Fatalf("bad auth %#v", f)
		}
	}
	return p
}
func (p *testPeer) send(t *testing.T, s string) {
	t.Helper()
	p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := p.conn.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}
func (p *testPeer) read(t *testing.T) Frame {
	t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	f, err := ReadFrame(p.reader, false, 64*1024, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestAuthenticationAndPipeline 验证逐字节认证、CRLF、中文及同连接连续请求。
func TestAuthenticationAndPipeline(t *testing.T) {
	s := startTestServer(t, nil)
	p := dialPeer(t, s, false)
	for _, b := range []byte("auth test-only-secret\r\n\r\n") {
		p.send(t, string([]byte{b}))
	}
	if p.read(t).Get("Reply-Text") != "+OK accepted" {
		t.Fatal("auth failed")
	}
	p.send(t, "api echo 中文☃  a \n\napi unknown\n\napi echo final\n\n")
	for _, want := range []string{"中文☃  a", "-ERR unknown Command not found!\n", "final"} {
		f := p.read(t)
		if f.Get("Content-Type") != "api/response" || string(f.Body) != want {
			t.Fatalf("got %q want %q", f.Body, want)
		}
	}
	p.send(t, "exit\n\n")
	if p.read(t).Get("Reply-Text") != "+OK bye" {
		t.Fatal("missing final reply")
	}
}

// TestUnauthenticatedCannotExecute 未认证连接不得进入业务处理器，也不得泄露密码。
func TestUnauthenticatedCannotExecute(t *testing.T) {
	for _, command := range []string{"auth wrong", "api echo secret", "auth"} {
		t.Run(command, func(t *testing.T) {
			var called atomic.Int64
			s := startTestServer(t, func(o *Options) { o.API = func(context.Context, string, string) string { called.Add(1); return "BAD" } })
			p := dialPeer(t, s, false)
			p.send(t, command+"\n\n")
			want := "-ERR command not found"
			if command == "auth wrong" {
				want = "-ERR invalid"
			}
			if f := p.read(t); f.Get("Reply-Text") != want {
				t.Fatalf("bad rejection %#v", f)
			}
			if command == "auth wrong" {
				f := p.read(t)
				if f.Get("Content-Type") != "text/disconnect-notice" || !strings.Contains(string(f.Body), "Disconnected, goodbye.") {
					t.Fatalf("missing disconnect notice %#v", f)
				}
				p.conn.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := p.reader.ReadByte(); err == nil {
					t.Fatal("invalid password socket not closed")
				}
			} else {
				// 非 auth 命令只被拒绝，原连接仍可在原认证时限内完成身份认证。
				p.send(t, "AUTH test-only-secret\n\n")
				if p.read(t).Get("Reply-Text") != "+OK accepted" {
					t.Fatal("valid later auth rejected")
				}
			}
			if called.Load() != 0 {
				t.Fatal("unauthenticated API executed")
			}
		})
	}
}

// TestBackgroundJobCorrelation 保留重复关联号的两次执行，检查原始正文长度和 JSON/纯文本事件。
func TestBackgroundJobCorrelation(t *testing.T) {
	for _, format := range []string{"plain", "json"} {
		t.Run(format, func(t *testing.T) {
			s := startTestServer(t, nil)
			p := dialPeer(t, s, true)
			p.send(t, "event "+format+" BACKGROUND_JOB\n\n")
			if p.read(t).Get("Reply-Text") != "+OK event listener enabled "+format {
				t.Fatal("subscription failed")
			}
			const id = "00000000-0000-4000-8000-000000000001"
			for i := 0; i < 2; i++ {
				p.send(t, "bgapi echo 中文☃\nJob-UUID: "+id+"\n\n")
				ack := p.read(t)
				if ack.Get("Reply-Text") != "+OK Job-UUID: "+id || ack.Get("Job-UUID") != id {
					t.Fatalf("bad job ack %#v", ack)
				}
				f := p.read(t)
				if f.Get("Content-Type") != "text/event-"+format {
					t.Fatalf("bad event %#v", f)
				}
				if format == "json" {
					var h map[string]string
					if err := json.Unmarshal(f.Body, &h); err != nil {
						t.Fatal(err)
					}
					if h["Job-UUID"] != id || h["_body"] != "中文☃" {
						t.Fatalf("bad job %#v", h)
					}
				} else {
					inner, err := ReadFrame(bufio.NewReader(strings.NewReader(string(f.Body))), false, 65536, 1024)
					if err != nil || inner.Get("Job-UUID") != id || string(inner.Body) != "中文☃" {
						t.Fatalf("bad inner event %#v %v", inner, err)
					}
				}
			}
		})
	}
}

// TestSubscriptionIsolationAndFilters 验证两个连接的订阅隔离及负过滤优先级。
func TestSubscriptionIsolationAndFilters(t *testing.T) {
	s := startTestServer(t, nil)
	a := dialPeer(t, s, true)
	b := dialPeer(t, s, true)
	a.send(t, "event plain CHANNEL_CREATE\n\nfilter Unique-ID target\n\nfilter Hangup-Cause -NORMAL_CLEARING\n\n")
	for i := 0; i < 3; i++ {
		a.read(t)
	}
	b.send(t, "event json BACKGROUND_JOB\n\n")
	b.read(t)
	s.Publish("CHANNEL_CREATE", map[string]string{"Unique-ID": "other"}, nil)
	s.Publish("CHANNEL_CREATE", map[string]string{"Unique-ID": "target", "Hangup-Cause": "NORMAL_CLEARING"}, nil)
	s.Publish("CHANNEL_CREATE", map[string]string{"Unique-ID": "target", "Channel-Name": "中文/ a"}, nil)
	f := a.read(t)
	inner, err := ReadFrame(bufio.NewReader(strings.NewReader(string(f.Body))), false, 65536, 1024)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := url.QueryUnescape(inner.Get("Channel-Name"))
	if decoded != "中文/ a" {
		t.Fatalf("incorrect filtered event %q", f.Body)
	}
	b.send(t, "api echo isolated\n\n")
	if f := b.read(t); string(f.Body) != "isolated" {
		t.Fatal("unsubscribed connection received event")
	}
	a.send(t, "noevents\n\n")
	if a.read(t).Get("Reply-Text") != "+OK no longer listening for events" {
		t.Fatal("noevents failed")
	}
	s.Publish("CHANNEL_CREATE", map[string]string{"Unique-ID": "target"}, nil)
	a.send(t, "api echo stopped\n\n")
	if string(a.read(t).Body) != "stopped" {
		t.Fatal("event delivered after unsubscribe")
	}
}

// TestResourceLimitsAndFrameTimeout 验证满连接、慢速残帧和错误配置会被收敛。
func TestResourceLimitsAndFrameTimeout(t *testing.T) {
	s := startTestServer(t, func(o *Options) {
		o.MaxConnections = 1
		o.FrameTimeout = 40 * time.Millisecond
		o.AuthTimeout = 200 * time.Millisecond
	})
	p := dialPeer(t, s, true)
	other, err := net.DialTimeout("tcp", s.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := other.Read(one[:]); err == nil {
		t.Fatal("connection limit ignored")
	}
	p.send(t, "api ")
	p.conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := p.reader.ReadByte(); err == nil {
		t.Fatal("incomplete frame not timed out")
	}
	if _, err := Listen(context.Background(), Options{Listen: "0.0.0.0:0", Password: "x", API: func(context.Context, string, string) string { return "" }}); err == nil {
		t.Fatal("remote ESL unexpectedly enabled")
	}
}

// TestQueueSaturationAndShutdown 验证后台并行度不随任务数增长，关停取消进行中的回调。
func TestQueueSaturationAndShutdown(t *testing.T) {
	var active, peak atomic.Int64
	entered := make(chan struct{}, 1)
	s := startTestServer(t, func(o *Options) {
		o.JobWorkers = 1
		o.QueueSize = 4
		o.API = func(ctx context.Context, command, args string) string {
			n := active.Add(1)
			peak.CompareAndSwap(0, n)
			defer active.Add(-1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return "-ERR cancelled\n"
		}
	})
	p := dialPeer(t, s, true)
	p.send(t, "bgapi wait\n\n")
	p.read(t)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("job not started")
	}
	for i := 0; i < 4; i++ {
		p.send(t, "bgapi wait\n\n")
		if !strings.HasPrefix(p.read(t).Get("Reply-Text"), "+OK") {
			t.Fatal("bounded queue rejected too soon")
		}
	}
	p.send(t, "bgapi wait\n\n")
	if p.read(t).Get("Reply-Text") != "-ERR background queue full" {
		t.Fatal("queue overcommitted")
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown failed to cancel workers")
	}
	if peak.Load() != 1 || active.Load() != 0 {
		t.Fatalf("workers leaked active=%d peak=%d", active.Load(), peak.Load())
	}
}

// TestConcurrentSubscribers 验证并发订阅、事件广播、退出和关闭不会发生数据竞争或死锁。
func TestConcurrentSubscribers(t *testing.T) {
	s := startTestServer(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		p := dialPeer(t, s, true)
		p.send(t, "event json CHANNEL_CREATE\n\n")
		p.read(t)
		wg.Add(1)
		go func(p *testPeer) {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				s.Publish("CHANNEL_CREATE", map[string]string{"Unique-ID": fmt.Sprint(j)}, nil)
			}
			p.conn.Close()
		}(p)
	}
	wg.Wait()
	s.Close()
}

// TestCommandCaseAndFormatPersistence 对齐原版命令/格式忽略大小写和省略格式时沿用前次选择。
func TestCommandCaseAndFormatPersistence(t *testing.T) {
	s := startTestServer(t, nil)
	p := dialPeer(t, s, false)
	p.send(t, "AuTh test-only-secret\n\nApI echo case-preserved\n\nEvEnT JsOn background_job\n\nEVENT CHANNEL_CREATE\n\n")
	if p.read(t).Get("Reply-Text") != "+OK accepted" {
		t.Fatal("mixed case auth")
	}
	if string(p.read(t).Body) != "case-preserved" {
		t.Fatal("mixed case API")
	}
	for range 2 {
		if p.read(t).Get("Reply-Text") != "+OK event listener enabled json" {
			t.Fatal("event format reset")
		}
	}
	p.send(t, "NIXEVENT ALL\n\nNOEVENTS\n\nNOEVENTS\n\n")
	for _, want := range []string{"+OK events nixed", "+OK no longer listening for events", "-ERR not listening for events"} {
		if got := p.read(t).Get("Reply-Text"); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

// TestFilterDeleteAllAndEmptyValue 通过真实事件交付核对删除全部及空值删除同名头，而非只验成功文本。
func TestFilterDeleteAllAndEmptyValue(t *testing.T) {
	for _, clearing := range []string{"FiLtEr DeLeTe ALL", "filter add Unique-ID "} {
		t.Run(clearing, func(t *testing.T) {
			s := startTestServer(t, nil)
			p := dialPeer(t, s, true)
			p.send(t, "event json CHANNEL_CREATE\n\nfilter Unique-ID blocked\n\n"+clearing+"\n\n")
			for range 3 {
				if !strings.HasPrefix(p.read(t).Get("Reply-Text"), "+OK") {
					t.Fatal("filter command rejected")
				}
			}
			s.Publish("CHANNEL_CREATE", map[string]string{"Unique-ID": "delivered"}, nil)
			f := p.read(t)
			var h map[string]string
			if err := json.Unmarshal(f.Body, &h); err != nil || h["Unique-ID"] != "delivered" {
				t.Fatalf("clear did not take effect %s %v", f.Body, err)
			}
		})
	}
}

// TestAuthenticationDeadlineDoesNotRenew 反复未认证命令不能延长握手总时限。
func TestAuthenticationDeadlineDoesNotRenew(t *testing.T) {
	s := startTestServer(t, func(o *Options) { o.AuthTimeout = 80 * time.Millisecond })
	p := dialPeer(t, s, false)
	started := time.Now()
	for i := 0; i < 100; i++ {
		p.conn.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := p.conn.Write([]byte("api echo no\n\n")); err != nil {
			break
		}
		p.conn.SetReadDeadline(time.Now().Add(time.Second))
		_, err := ReadFrame(p.reader, false, 65536, 1024)
		if err != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("auth deadline renewed: %s", elapsed)
	}
}

// TestConnectionCreditWaitsForBothWorkers 模拟退出中的慢回调，防止写协程先结束就让新连接无限进入。
func TestConnectionCreditWaitsForBothWorkers(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	s := startTestServer(t, func(o *Options) {
		o.MaxConnections = 1
		o.API = func(ctx context.Context, command, args string) string {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return "done"
		}
	})
	// 注册在服务 Close 之后，确保测试失败时也先释放夹具回调，不让清理无限等待。
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	p := dialPeer(t, s, true)
	p.send(t, "api wait\n\n")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("API did not enter")
	}
	s.mu.Lock()
	for c := range s.clients {
		c.close()
	}
	s.mu.Unlock()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("client close did not cancel API")
	}
	other, err := net.DialTimeout("tcp", s.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	other.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	_, err = other.Read(one[:])
	other.Close()
	if err == nil {
		t.Fatal("connection credit released before callback terminated")
	}
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		remaining := len(s.clients)
		s.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connection credit leaked")
		}
		time.Sleep(time.Millisecond)
	}
	_ = dialPeer(t, s, true)
}

// TestParentCancellationClosesListener 父上下文取消应自行关闭入口及工作组，无需外部补一次 Close。
func TestParentCancellationClosesListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Listen(ctx, Options{Listen: "127.0.0.1:0", Password: "test-only-secret", API: func(context.Context, string, string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = dialPeer(t, s, true)
	cancel()
	select {
	case <-s.acceptDone:
	case <-time.After(time.Second):
		t.Fatal("listener survived parent cancel")
	}
	conn, err := net.DialTimeout("tcp", s.Addr().String(), 50*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("cancelled listener accepted")
	}
	if _, err := Listen(ctx, Options{Listen: "127.0.0.1:0", Password: "x", API: func(context.Context, string, string) string { return "" }}); err == nil {
		t.Fatal("cancelled parent created server")
	}
}

// TestOutputByteBudget 有界帧数之外还限制累计字节，并发溢出只记录同一连接的一次中断。
func TestOutputByteBudget(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{}
	c := &client{server: s, conn: a, out: make(chan Frame, 128), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	f := Frame{Body: []byte(strings.Repeat("x", maxQueuedBytes/2))}
	if !c.enqueue(f) {
		t.Fatal("first bounded frame rejected")
	}
	if c.enqueue(f) {
		t.Fatal("byte budget ignored")
	}
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() { defer group.Done(); c.closeWithDrop(true) }()
	}
	group.Wait()
	if s.DroppedClients() != 1 || c.queuedBytes.Load() > maxQueuedBytes {
		t.Fatal("drop accounting or memory bound failed")
	}
}

// TestNoEventsFlushesQueuedEvents 在写协程尚未开始前积压事件，再取消订阅；只允许收到 noevents 回复。
func TestNoEventsFlushesQueuedEvents(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, clients: make(map[*client]struct{}), options: Options{WriteTimeout: time.Second}}
	c := &client{server: s, conn: a, out: make(chan Frame, 8), done: make(chan struct{}), ctx: ctx, cancel: cancel, authed: true, eventsEnabled: true, events: map[string]bool{"CHANNEL_CREATE": true}}
	s.clients[c] = struct{}{}
	c.remaining.Store(1)
	s.wg.Add(1)
	for range 4 {
		if !c.enqueue(Frame{Headers: []Header{{"Content-Type", "text/event-plain"}}, isEvent: true, eventGeneration: 0}) {
			t.Fatal("fixture enqueue failed")
		}
	}
	if !c.command(Frame{Command: "noevents"}) {
		t.Fatal("noevents closed healthy client")
	}
	go c.write()
	b.SetReadDeadline(time.Now().Add(time.Second))
	f, err := ReadFrame(bufio.NewReader(b), false, 65536, 1024)
	c.close()
	s.wg.Wait()
	if err != nil || f.Get("Reply-Text") != "+OK no longer listening for events" {
		t.Fatalf("stale event escaped flush: %#v %v", f, err)
	}
	if c.queuedBytes.Load() != 0 {
		t.Fatal("flushed queue byte accounting leaked")
	}
}
