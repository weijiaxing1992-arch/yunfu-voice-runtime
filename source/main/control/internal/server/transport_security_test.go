package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/sip"
	"testing"
	"time"
)

// transportOnlyServer 创建真实信令监听与串行控制循环，专用于不需要媒体的传输资源测试。
func transportOnlyServer(t *testing.T, options config.SIPStream, networks []string) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, cancel: cancel, normal: make(chan datagram, 16), critical: make(chan datagram, 16), calls: make(map[uint64]*Call), byDialog: make(map[string]uint64), transactions: make(map[string]*transaction), cache: make(map[string]cacheEntry), timerIndex: make(map[timerKey]*timerItem), Config: config.Config{SIP: config.SIP{Listen: "127.0.0.1:65000", Advertise: "127.0.0.1:65000", Upstream: "127.0.0.1:65001", TrustedNetworks: networks, Stream: &options}, Limits: config.Limits{MaxTransactions: 64}}}
	if err := s.startTransports(); err != nil {
		cancel()
		s.closeTransports()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case d := <-s.normal:
				s.handle(d)
			case d := <-s.critical:
				s.handle(d)
			case id := <-s.streamClosed:
				s.streamEnded(id)
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done; s.closeTransports() })
	return s
}

// TestSIPStreamOriginalConnectionAndCacheIdentity 同事务文本经两条真实连接发送，各自响应必须回到原连接。
func TestSIPStreamOriginalConnectionAndCacheIdentity(t *testing.T) {
	address := freeTransportAddress(t)
	s := transportOnlyServer(t, config.SIPStream{TCPListen: address, MaxConnections: 4}, []string{"127.0.0.0/8"})
	first, err := net.Dial("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.Dial("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	request := sip.RequestTransport("tcp", "OPTIONS", "sip:test@"+address, "127.0.0.1:5060", "z9hG4bK-shared", "<sip:caller@127.0.0.1>;tag=shared", "<sip:test@127.0.0.1>", "same-transaction", 1, nil)
	for _, conn := range []net.Conn{first, second, first, second} {
		if _, err = conn.Write(request); err != nil {
			t.Fatal(err)
		}
		response := readTransportMessage(t, conn, bufio.NewReaderSize(conn, 4098))
		if response.Status != 200 {
			t.Fatal("响应未经发起事务的原连接返回")
		}
	}
	if s.Stats.SendErrors.Load() != 0 {
		t.Fatal("流式响应被发送到 UDP 或已关闭连接")
	}
	if transportKey(netip.MustParseAddrPort("127.0.0.1:5060"), 1) == transportKey(netip.MustParseAddrPort("127.0.0.1:5060"), 2) {
		t.Fatal("同地址重连代次发生冲突")
	}
}

// TestSIPStreamRejectsTransportSpoofing 来自 TCP 的 UDP Via 不能被接受并改用 UDP 回包。
func TestSIPStreamRejectsTransportSpoofing(t *testing.T) {
	address := freeTransportAddress(t)
	s := transportOnlyServer(t, config.SIPStream{TCPListen: address}, []string{"127.0.0.0/8"})
	conn, err := net.Dial("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wire := sip.Request("OPTIONS", "sip:test@"+address, "127.0.0.1:5060", "z9hG4bK-spoof", "<sip:a@127.0.0.1>;tag=a", "<sip:b@127.0.0.1>", "spoof", 1, nil)
	if _, err = conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err = conn.Read(buffer); err == nil {
		t.Fatal("接受 TCP 上伪造的 UDP Via")
	}
	waitTransportCondition(t, func() bool { return s.Stats.Malformed.Load() == 1 })
}

// TestSIPStreamSlowlorisHasAbsoluteDeadline 零散字节无法反复刷新整帧期限，关闭后会归还连接预算。
func TestSIPStreamSlowlorisHasAbsoluteDeadline(t *testing.T) {
	address := freeTransportAddress(t)
	s := transportOnlyServer(t, config.SIPStream{TCPListen: address, FrameTimeoutMS: 100, MaxConnections: 1}, []string{"127.0.0.0/8"})
	conn, err := net.Dial("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	_, _ = conn.Write([]byte("I"))
	for range 6 {
		time.Sleep(25 * time.Millisecond)
		_, _ = conn.Write([]byte("N"))
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buffer := make([]byte, 1)
	if _, err = conn.Read(buffer); err == nil {
		t.Fatal("慢速半帧仍保持连接")
	}
	if time.Since(start) > time.Second {
		t.Fatal("绝对整帧期限未生效")
	}
	waitTransportCondition(t, func() bool { return len(s.streams.slots) == 0 })
}

// TestSIPStreamConnectionBudgetAndACL 握手前的连接额度和真实来源白名单均必须生效。
func TestSIPStreamConnectionBudgetAndACL(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		address := freeTransportAddress(t)
		s := transportOnlyServer(t, config.SIPStream{TCPListen: address, MaxConnections: 1}, []string{"127.0.0.0/8"})
		first, err := net.Dial("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		waitTransportCondition(t, func() bool { return len(s.streams.slots) == 1 })
		second, err := net.Dial("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		_ = second.SetReadDeadline(time.Now().Add(time.Second))
		if _, err = second.Read(make([]byte, 1)); err == nil {
			t.Fatal("突破可靠连接硬上限")
		}
		waitTransportCondition(t, func() bool { return s.Stats.QueueDrops.Load() == 1 })
	})
	t.Run("acl", func(t *testing.T) {
		address := freeTransportAddress(t)
		s := transportOnlyServer(t, config.SIPStream{TCPListen: address}, []string{"192.0.2.0/24"})
		conn, err := net.Dial("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err = conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("接受白名单之外的真实来源")
		}
		waitTransportCondition(t, func() bool { return s.Stats.Untrusted.Load() == 1 })
	})
}

// TestSIPTLSUpstreamRejectsUntrustedAndWrongName 上游证书校验失败必须拒绝启动，不允许自动降级明文。
func TestSIPTLSUpstreamRejectsUntrustedAndWrongName(t *testing.T) {
	pair, _, certPath, _ := transportCertificate(t)
	for _, name := range []string{"untrusted_ca", "wrong_name"} {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				secured := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = secured.HandshakeContext(ctx)
			}()
			o := config.SIPStream{UpstreamTransport: "tls", TLSServerName: "localhost", FrameTimeoutMS: 1000}
			if name == "wrong_name" {
				o.TLSCAFile = certPath
				o.TLSServerName = "invalid.example"
			}
			ctx, cancel := context.WithCancel(context.Background())
			s := &Server{ctx: ctx, cancel: cancel, Config: config.Config{SIP: config.SIP{Upstream: listener.Addr().String(), Stream: &o}}}
			err = s.startTransports()
			cancel()
			s.closeTransports()
			<-done
			if err == nil {
				t.Fatal("不可信或主机名不符的 TLS 上游被接受")
			}
		})
	}
}

// TestSIPStreamWriteDeadline 真实阻塞写必须按整条消息期限结束，不能永久堵塞发送协程。
func TestSIPStreamWriteDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	left, right := net.Pipe()
	defer right.Close()
	s := &Server{ctx: ctx, streamClosed: make(chan uint64, 1)}
	manager := &sipStreams{owner: s, options: config.SIPStream{WriteTimeoutMS: 30}, flows: make(map[uint64]*sipFlow), slots: make(chan struct{}, 1)}
	flow := &sipFlow{id: 1, conn: left, send: make(chan []byte, 16), done: make(chan struct{})}
	manager.flows[1] = flow
	manager.slots <- struct{}{}
	manager.wg.Add(1)
	go manager.write(flow)
	flow.send <- []byte("blocked")
	select {
	case <-flow.done:
	case <-time.After(time.Second):
		t.Fatal("慢接收方导致无界写阻塞")
	}
	manager.wg.Wait()
	if s.Stats.SendErrors.Load() != 1 || len(manager.slots) != 0 {
		t.Fatal("写失败未计数或未释放连接预算")
	}
}

// TestSIPReliableTransactionIdentityAndTimers 验证可靠请求不重传，且相同地址的新连接不能推进旧事务。
func TestSIPReliableTransactionIdentityAndTimers(t *testing.T) {
	peer := netip.MustParseAddrPort("127.0.0.1:65001")
	s := &Server{Config: config.Config{SIP: config.SIP{Advertise: "127.0.0.1:65000", Upstream: peer.String(), AckTimeoutMS: 3000}, Limits: config.Limits{MaxTransactions: 64}}, calls: make(map[uint64]*Call), byDialog: make(map[string]uint64), transactions: make(map[string]*transaction), cache: make(map[string]cacheEntry), timerIndex: make(map[timerKey]*timerItem)}
	flow := &sipFlow{id: 1, kind: "tcp", peer: peer, send: make(chan []byte, 16)}
	s.streams = &sipStreams{flows: map[uint64]*sipFlow{1: flow}}
	wire := sip.RequestTransport("tcp", "BYE", "sip:b@127.0.0.1", "127.0.0.1:65000", "z9hG4bK-reliable", "<sip:a@127.0.0.1>;tag=a", "<sip:b@127.0.0.1>;tag=b", "reliable", 2, nil)
	c := &Call{ID: 1}
	s.startTransaction(c, "BYE", "z9hG4bK-reliable", wire, peer, 1)
	if len(s.transactions) != 1 {
		t.Fatal("可靠请求未建立事务")
	}
	for key := range s.timerIndex {
		if key.kind == "out_retry" {
			t.Fatal("可靠请求错误开启重传定时器")
		}
	}
	request, err := sip.Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	response, err := sip.Parse(sip.Response(request, 200, "OK", "", "", peer.String(), nil))
	if err != nil {
		t.Fatal(err)
	}
	response.TransportID = 2
	s.responseB(response, peer)
	if len(s.transactions) != 1 {
		t.Fatal("相同远端地址的新连接推进了旧事务")
	}
	response.TransportID = 1
	s.responseB(response, peer)
	if len(s.transactions) != 0 {
		t.Fatal("真实原连接的成功响应未结束事务")
	}
	for _, status := range []int{200, 486} {
		request, err := sip.Parse(sip.RequestTransport("tcp", "INVITE", "sip:b@127.0.0.1", "127.0.0.1:65001", "z9hG4bK-final", "<sip:a@127.0.0.1>;tag=a", "<sip:b@127.0.0.1>", "final", 1, nil))
		if err != nil {
			t.Fatal(err)
		}
		request.TransportID = 1
		call := &Call{ID: uint64(status), ARequest: request, ASource: peer, ATag: "server"}
		s.answerA(call, status, "Final", nil)
		retry, expiry := false, false
		for key := range s.timerIndex {
			if key.id == call.ID {
				retry = retry || key.kind == "uas_retry"
				expiry = expiry || key.kind == "uas_expire"
			}
		}
		if !expiry || retry != (status == 200) {
			t.Fatalf("%d 最终响应定时器不符合可靠传输/2xx端到端规则", status)
		}
	}
}

// TestSIPFlowReferencesBoundDisconnectWork 断线只能访问自己的归属索引，不能扫描或修改其他连接的资源。
func TestSIPFlowReferencesBoundDisconnectWork(t *testing.T) {
	peer := netip.MustParseAddrPort("127.0.0.1:65001")
	s := &Server{calls: make(map[uint64]*Call), transactions: make(map[string]*transaction), cache: make(map[string]cacheEntry), timerIndex: make(map[timerKey]*timerItem)}
	owned := &Call{ID: 1, ARequest: &sip.Message{TransportID: 1}, BFlow: 2, Ended: true, UASPending: true}
	s.calls[1] = owned
	s.bindFlowCall(owned)
	// 未归属目标连接的哨兵没有 ARequest；全局扫描会错误访问它，准确暴露不必要的遍历路径。
	for id := uint64(2); id <= 5001; id++ {
		s.calls[id] = &Call{ID: id}
	}
	s.transactions["owned"] = &transaction{flow: 1, session: 1}
	s.flowRefs(1, true).transactions["owned"] = struct{}{}
	s.transactions["other"] = &transaction{flow: 2, session: 1}
	s.flowRefs(2, true).transactions["other"] = struct{}{}
	s.cache["owned"] = cacheEntry{flow: 1, source: peer}
	s.flowRefs(1, true).cache["owned"] = struct{}{}
	s.cache["other"] = cacheEntry{flow: 2, source: peer}
	s.flowRefs(2, true).cache["other"] = struct{}{}
	s.streamEnded(1)
	if s.transactions["owned"] != nil || s.transactions["other"] == nil || len(s.cache) != 1 || owned.UASPending {
		t.Fatal("归属连接清理错误影响其他连接或留下 ACK 状态")
	}
	if s.flowRefs(1, false) != nil || s.flowRefs(2, false) == nil {
		t.Fatal("归属索引未按连接隔离清理")
	}
	s.streamEnded(99999) // 空短连接应立即返回，不能触碰五千个无关通话。
}

// TestSIPFlowReferencesReleaseAcrossTurnover 长期连接周转一万次后不能积累历史通话引用。
func TestSIPFlowReferencesReleaseAcrossTurnover(t *testing.T) {
	s := &Server{}
	for id := uint64(1); id <= 10000; id++ {
		c := &Call{ID: id, ARequest: &sip.Message{TransportID: 1}, BFlow: 2}
		s.bindFlowCall(c)
		if len(s.flowRefs(1, false).calls) != 1 || len(s.flowRefs(2, false).calls) != 1 {
			t.Fatal("长期连接积累已结束通话")
		}
		s.forgetFlowCall(c)
		if len(s.flowReferences) != 0 {
			t.Fatal("最后一个资源回收后空索引未释放")
		}
	}
}
