package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/sip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// sipStreams 管理有界可靠连接；共享表只保存连接，不读取主循环私有呼叫状态。
type sipStreams struct {
	owner     *Server             // 所有关闭事件提交回呼叫主循环。
	options   config.SIPStream    // 已填充默认值的冻结配置。
	mu        sync.RWMutex        // 只保护连接表及编号分配，不覆盖网络读写。
	flows     map[uint64]*sipFlow // 连接编号永不复用，隔离相同远端地址的重连。
	nextID    uint64              // 每个控制进程最多创建 2^64-1 个不同连接。
	slots     chan struct{}       // 握手、等待关闭事件和已建立连接共同占用预算。
	listeners []net.Listener      // 启动完成后不再变更。
	upstream  atomic.Uint64       // 当前固定上游连接；零表示尚不可用。
	clientTLS *tls.Config         // 上游 TLS 必须校验链和名称，无跳过校验配置。
	wg        sync.WaitGroup      // 等待接收、发送、握手及重连协程退出。
}

// sipFlow 的发送队列最多十六条完整消息；主循环不会等待网络写阻塞。
type sipFlow struct {
	id   uint64         // 真实连接的不可复用身份。
	kind string         // tcp 或 tls，必须与最上层 Via 一致。
	peer netip.AddrPort // socket 报告的远端地址。
	conn net.Conn       // 单读单写的真实可靠传输。
	send chan []byte    // 每条消息独立复制，避免调用方缓冲被覆盖。
	done chan struct{}  // 任一方向失败立即关闭整个连接。
	once sync.Once      // 两个方向只产生一次关闭事件。
}

// startTransports 按需启动额外传输；固定上游的首次 TLS 校验失败直接拒绝启动。
func (s *Server) startTransports() error {
	o := s.Config.SIP.StreamOptions()
	if o.TCPListen == "" && o.TLSListen == "" && o.UpstreamTransport == "udp" {
		return nil
	}
	t := &sipStreams{owner: s, options: o, flows: make(map[uint64]*sipFlow), slots: make(chan struct{}, o.MaxConnections)}
	s.streams = t
	s.streamClosed = make(chan uint64, o.MaxConnections)
	var serverTLS *tls.Config
	if o.TLSListen != "" {
		cert, err := tls.LoadX509KeyPair(o.TLSCertFile, o.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("SIP TLS certificate: %w", err)
		}
		serverTLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	if o.UpstreamTransport == "tls" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if o.TLSCAFile != "" {
			pem, e := os.ReadFile(o.TLSCAFile)
			if e != nil {
				return fmt.Errorf("SIP upstream CA: %w", e)
			}
			if !roots.AppendCertsFromPEM(pem) {
				return errors.New("SIP upstream CA contains no certificates")
			}
		}
		name := o.TLSServerName
		if name == "" {
			name = s.upstream().Addr().String()
		}
		t.clientTLS = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: name}
	}
	for _, item := range []struct{ address, kind string }{{o.TCPListen, "tcp"}, {o.TLSListen, "tls"}} {
		if item.address == "" {
			continue
		}
		listener, err := net.Listen("tcp4", item.address)
		if err != nil {
			return err
		}
		t.listeners = append(t.listeners, listener)
		t.wg.Add(1)
		go t.accept(listener, item.kind, serverTLS)
	}
	if o.UpstreamTransport != "udp" {
		flow, err := t.dialUpstream()
		if err != nil {
			return fmt.Errorf("SIP %s upstream: %w", o.UpstreamTransport, err)
		}
		t.upstream.Store(flow.id)
		t.wg.Add(1)
		go t.reconnect(flow)
	}
	return nil
}

// accept 先校验真实远端及连接额度，再执行有期限握手；不为被拒绝连接创建协程。
func (t *sipStreams) accept(listener net.Listener, kind string, serverTLS *tls.Config) {
	defer t.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		peer, err := netip.ParseAddrPort(conn.RemoteAddr().String())
		if err != nil || !config.Allowed(peer.Addr().Unmap(), t.owner.Config.SIP.TrustedNetworks) {
			t.owner.Stats.Untrusted.Add(1)
			_ = conn.Close()
			continue
		}
		select {
		case t.slots <- struct{}{}:
		default:
			t.owner.Stats.QueueDrops.Add(1)
			_ = conn.Close()
			continue
		}
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			if kind == "tls" {
				secured := tls.Server(conn, serverTLS)
				ctx, cancel := context.WithTimeout(t.owner.ctx, time.Duration(t.options.FrameTimeoutMS)*time.Millisecond)
				err := secured.HandshakeContext(ctx)
				cancel()
				if err != nil {
					_ = conn.Close()
					<-t.slots
					return
				}
				conn = secured
			}
			t.add(conn, kind)
		}()
	}
}

// dialUpstream 使用固定的一条连接，不会因每通呼叫创建额外拨号任务。
func (t *sipStreams) dialUpstream() (*sipFlow, error) {
	select {
	case t.slots <- struct{}{}:
	case <-t.owner.ctx.Done():
		return nil, t.owner.ctx.Err()
	default:
		return nil, errors.New("SIP connection budget exhausted")
	}
	ctx, cancel := context.WithTimeout(t.owner.ctx, time.Duration(t.options.FrameTimeoutMS)*time.Millisecond)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", t.owner.Config.SIP.Upstream)
	if err == nil && t.options.UpstreamTransport == "tls" {
		secured := tls.Client(conn, t.clientTLS)
		err = secured.HandshakeContext(ctx)
		if err != nil {
			_ = conn.Close()
		} else {
			conn = secured
		}
	}
	if err != nil {
		<-t.slots
		return nil, err
	}
	return t.add(conn, t.options.UpstreamTransport), nil
}

// reconnect 仅在上一连接结束后有界退避重连；旧呼叫绝不迁移到新连接身份。
func (t *sipStreams) reconnect(flow *sipFlow) {
	defer t.wg.Done()
	for {
		select {
		case <-flow.done:
		case <-t.owner.ctx.Done():
			return
		}
		t.upstream.CompareAndSwap(flow.id, 0)
		for {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-timer.C:
			case <-t.owner.ctx.Done():
				timer.Stop()
				return
			}
			next, err := t.dialUpstream()
			if err == nil {
				flow = next
				t.upstream.Store(flow.id)
				break
			}
		}
	}
}

// add 在已有预算内登记连接并启动固定两个方向的处理协程。
func (t *sipStreams) add(conn net.Conn, kind string) *sipFlow {
	peer := netip.MustParseAddrPort(conn.RemoteAddr().String())
	peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
	t.mu.Lock()
	t.nextID++
	f := &sipFlow{id: t.nextID, kind: kind, peer: peer, conn: conn, send: make(chan []byte, 16), done: make(chan struct{})}
	t.flows[f.id] = f
	t.mu.Unlock()
	t.wg.Add(2)
	go t.read(f)
	go t.write(f)
	return f
}

// closeFlow 先关闭 socket 唤醒另一方向，再通过有界队列通知主循环释放受影响通话。
func (t *sipStreams) closeFlow(f *sipFlow) {
	f.once.Do(func() {
		closeSIPConn(f.conn)
		close(f.done)
		t.mu.Lock()
		delete(t.flows, f.id)
		t.mu.Unlock()
		select {
		case t.owner.streamClosed <- f.id:
		case <-t.owner.ctx.Done():
		}
		<-t.slots
	})
}

// read 以首字节为分界分别设置空闲期限与完整帧期限，分片到达不会延长绝对期限。
func (t *sipStreams) read(f *sipFlow) {
	defer t.wg.Done()
	defer t.closeFlow(f)
	reader := bufio.NewReaderSize(f.conn, 4098)
	for {
		_ = f.conn.SetReadDeadline(time.Now().Add(time.Duration(t.options.IdleTimeoutMS) * time.Millisecond))
		if _, err := reader.Peek(1); err != nil {
			return
		}
		_ = f.conn.SetReadDeadline(time.Now().Add(time.Duration(t.options.FrameTimeoutMS) * time.Millisecond))
		wire, err := sip.ReadStreamFrame(reader)
		if err != nil {
			t.owner.Stats.Malformed.Add(1)
			return
		}
		message, err := sip.Parse(wire)
		if err != nil || message.Transport() != f.kind {
			t.owner.Stats.Malformed.Add(1)
			return
		}
		message.TransportID = f.id
		t.owner.Stats.Incoming.Add(1)
		if !t.owner.enqueue(datagram{message, f.peer}) {
			return
		}
	}
}

// write 单次设定完整消息期限并处理短写；任何失败终止连接，不在半条消息后继续下一条。
func (t *sipStreams) write(f *sipFlow) {
	defer t.wg.Done()
	defer t.closeFlow(f)
	for {
		select {
		case wire := <-f.send:
			_ = f.conn.SetWriteDeadline(time.Now().Add(time.Duration(t.options.WriteTimeoutMS) * time.Millisecond))
			for len(wire) > 0 {
				n, err := f.conn.Write(wire)
				if err != nil || n <= 0 {
					t.owner.Stats.SendErrors.Add(1)
					return
				}
				wire = wire[n:]
			}
		case <-f.done:
			return
		case <-t.owner.ctx.Done():
			return
		}
	}
}

// closeTransports 在取消服务上下文后关闭监听和连接，等待全部有界处理协程退出。
func (s *Server) closeTransports() {
	if s.streams == nil {
		return
	}
	t := s.streams
	for _, listener := range t.listeners {
		_ = listener.Close()
	}
	t.mu.RLock()
	flows := make([]*sipFlow, 0, len(t.flows))
	for _, f := range t.flows {
		flows = append(flows, f)
	}
	t.mu.RUnlock()
	for _, f := range flows {
		t.closeFlow(f)
	}
	t.wg.Wait()
}

// flow 返回仍然有效的连接；不存在时不可降级到 UDP 或同地址新连接。
func (s *Server) flow(id uint64) *sipFlow {
	if id == 0 || s.streams == nil {
		return nil
	}
	s.streams.mu.RLock()
	f := s.streams.flows[id]
	s.streams.mu.RUnlock()
	return f
}

// sendFlow 保持原 UDP 路径；可靠连接只做有界队列提交，满队列直接断开并回收呼叫。
func (s *Server) sendFlow(wire []byte, destination netip.AddrPort, id uint64) {
	if id == 0 {
		s.send(wire, destination)
		return
	}
	if len(wire) == 0 {
		return
	}
	f := s.flow(id)
	if f == nil || f.peer != destination || len(wire) > sip.MaxMessageSize {
		s.Stats.SendErrors.Add(1)
		return
	}
	select {
	case f.send <- append([]byte(nil), wire...):
	default:
		s.Stats.SendErrors.Add(1)
		// Close 不等待主循环处理关闭事件；读写协程负责唯一的状态通知和预算释放。
		closeSIPConn(f.conn)
	}
}

// transportKey 为缓存增加连接代次，保持既有 UDP 键格式兼容。
func transportKey(source netip.AddrPort, id uint64) string {
	if id == 0 {
		return source.String()
	}
	return source.String() + "#" + strconv.FormatUint(id, 10)
}

// outgoingFlow 为固定上游读取当前连接；调用者在新呼叫准入时将它固定到 Call。
func (s *Server) outgoingFlow() uint64 {
	if s.streams == nil {
		return 0
	}
	return s.streams.upstream.Load()
}

// flowContact 按传输监听端口宣告当前腿的 Contact，不把 TLS 信令标记成安全媒体。
func (s *Server) flowContact(id uint64) string {
	kind, address := s.flowAdvertise(id)
	contact := "sip:rustswitch@" + address
	if kind != "udp" {
		contact += ";transport=" + kind
	}
	return contact
}

// flowAdvertise 保留显式宣告 IP；额外监听使用其实际端口，固定上游沿用该传输监听端口。
func (s *Server) flowAdvertise(id uint64) (string, string) {
	if id == 0 {
		return "udp", s.Config.SIP.Advertise
	}
	kind := "tcp"
	if f := s.flow(id); f != nil {
		kind = f.kind
	} else if s.Config.SIP.StreamOptions().UpstreamTransport == "tls" {
		kind = "tls"
	}
	listen := s.Config.SIP.StreamOptions().TCPListen
	if kind == "tls" {
		listen = s.Config.SIP.StreamOptions().TLSListen
	}
	advertise := s.Config.SIP.Advertise
	if listen != "" {
		a := netip.MustParseAddrPort(advertise)
		port := netip.MustParseAddrPort(listen).Port()
		advertise = netip.AddrPortFrom(a.Addr(), port).String()
	}
	return kind, advertise
}

// requestFlow 让编码、发送和事务状态共享同一个真实传输身份。
func (s *Server) requestFlow(id uint64, method, uri, branch, from, to, callID string, cseq uint32, body []byte, extra ...sip.Header) []byte {
	kind, advertise := s.flowAdvertise(id)
	return sip.RequestTransport(kind, method, uri, advertise, branch, from, to, callID, cseq, body, extra...)
}

// streamEnded 仅在主循环回收断开的连接所拥有的事务、缓存和通话，其他连接不受影响。
func (s *Server) streamEnded(id uint64) {
	s.registrationFlowClosed(id)
	refs := s.flowRefs(id, false)
	if refs == nil {
		return
	}
	delete(s.flowReferences, id)
	for key := range refs.transactions {
		if tx := s.transactions[key]; tx != nil && tx.flow == id {
			delete(s.transactions, key)
			s.cancelTimer("out_retry", tx.session, key)
			s.cancelTimer("out_expire", tx.session, key)
		}
	}
	for key := range refs.cache {
		if cached, ok := s.cache[key]; ok && cached.flow == id {
			delete(s.cache, key)
			s.cancelTimer("cache", 0, key)
		}
	}
	for callID := range refs.calls {
		c := s.calls[callID]
		if c == nil {
			continue
		}
		if c.ARequest.TransportID != id && c.BFlow != id {
			continue
		}
		if !c.Ended {
			if c.ARequest.TransportID == id {
				if c.BAnswered {
					s.sendByeB(c)
				} else {
					s.sendCancelB(c)
				}
			} else if c.AStatus < 200 {
				s.answerA(c, 503, "Upstream Transport Closed", nil)
			} else {
				s.sendByeA(c)
			}
			s.finish(c, "sip_transport_closed", true)
		}
		if c.ARequest.TransportID == id {
			c.UASPending = false
			c.UASVersion++
			s.cancelTimer("uas_retry", c.ID, "")
			s.cancelTimer("uas_expire", c.ID, "")
		}
	}
}

// closeSIPConn 在异常、背压或停机时直接关闭底层 socket，不执行可能等待五秒的 TLS close_notify。
// 此处不追加加密报文，避免慢对端把呼叫主循环拖入 TLS 的同步关闭路径。
func closeSIPConn(conn net.Conn) {
	if secured, ok := conn.(*tls.Conn); ok {
		_ = secured.NetConn().Close()
		return
	}
	_ = conn.Close()
}

// sipFlowReferences 只引用尚存资源，结束和定时过期时同步移除，长连接周转不会积累历史对象。
type sipFlowReferences struct {
	calls        map[uint64]struct{} // 该连接涉及的当前保留通话。
	transactions map[string]struct{} // 该连接拥有的当前出站事务。
	cache        map[string]struct{} // 该连接拥有的尚未过期响应缓存。
}

// flowRefs 对 UDP 不建索引，默认关闭可靠传输时不增加每通呼叫的映射成本。
func (s *Server) flowRefs(id uint64, create bool) *sipFlowReferences {
	if id == 0 {
		return nil
	}
	refs := s.flowReferences[id]
	if refs == nil && create {
		if s.flowReferences == nil {
			s.flowReferences = make(map[uint64]*sipFlowReferences)
		}
		refs = &sipFlowReferences{calls: make(map[uint64]struct{}), transactions: make(map[string]struct{}), cache: make(map[string]struct{})}
		s.flowReferences[id] = refs
	}
	return refs
}

// pruneFlowRefs 在最后一个资源结束时即时归还空索引，防止大量短连接形成残留。
func (s *Server) pruneFlowRefs(id uint64) {
	if refs := s.flowRefs(id, false); refs != nil && len(refs.calls)+len(refs.transactions)+len(refs.cache) == 0 {
		delete(s.flowReferences, id)
	}
}

// bindFlowCall 在准入后的主循环登记双腿，断线只遍历该连接涉及的通话。
func (s *Server) bindFlowCall(c *Call) {
	for _, id := range []uint64{c.ARequest.TransportID, c.BFlow} {
		if refs := s.flowRefs(id, true); refs != nil {
			refs.calls[c.ID] = struct{}{}
		}
	}
}

// forgetFlowCall 与通话垃圾回收同步删除归属引用，不延长媒体或信令状态寿命。
func (s *Server) forgetFlowCall(c *Call) {
	var a uint64
	if c.ARequest != nil {
		a = c.ARequest.TransportID
	}
	for _, id := range []uint64{a, c.BFlow} {
		if refs := s.flowRefs(id, false); refs != nil {
			delete(refs.calls, c.ID)
			s.pruneFlowRefs(id)
		}
	}
}

// forgetFlowTransaction 在响应或到期时移除归属引用，重传缓存不会形成永久索引。
func (s *Server) forgetFlowTransaction(key string, id uint64) {
	if refs := s.flowRefs(id, false); refs != nil {
		delete(refs.transactions, key)
		s.pruneFlowRefs(id)
	}
}
