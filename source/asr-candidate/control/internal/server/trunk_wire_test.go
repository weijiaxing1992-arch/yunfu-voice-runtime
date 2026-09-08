package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/sip"
	"strings"
	"testing"
	"time"
)

// trunkWire 统一测试对端的真实 UDP/TCP/TLS 输入；不替代生产传输或媒体。
type trunkWire struct {
	conn   net.Conn
	udp    *net.UDPConn
	reader *bufio.Reader
	peer   netip.AddrPort
}

// read 给每条线缆消息设外部期限，真实帧由生产解析器读取。
func (w *trunkWire) read(t *testing.T) *sip.Message {
	t.Helper()
	if w.udp == nil {
		return readTransportMessage(t, w.conn, w.reader)
	}
	_ = w.udp.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, sip.MaxMessageSize)
	n, peer, err := w.udp.ReadFromUDPAddrPort(buffer)
	if err != nil {
		t.Fatal(err)
	}
	w.peer = peer
	m, err := sip.Parse(buffer[:n])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// write 只向刚才观察到的固定测试对端回包，不选择真实业务服务器。
func (w *trunkWire) write(t *testing.T, packet []byte) {
	t.Helper()
	var err error
	if w.udp != nil {
		_, err = w.udp.WriteToUDPAddrPort(packet, w.peer)
	} else {
		_, err = w.conn.Write(packet)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// checkTrunkDigest 独立按RFC公式检查真实请求正文，不能用同一个Authorization生成器自证。
func checkTrunkDigest(t *testing.T, m *sip.Message, name, nonce, algorithm string) {
	t.Helper()
	p, err := sip.DigestParameters(m.Header(name))
	if err != nil {
		t.Fatal("缺少可解析的线缆认证")
	}
	if p["username"] != "line" || p["realm"] != "carrier" || p["uri"] != m.URI || p["nonce"] != nonce || p["algorithm"] != algorithm || p["qop"] != "auth" || p["nc"] == "00000000" || p["cnonce"] == "" {
		t.Fatal("线缆认证字段与请求身份不同")
	}
	hash := func(value string) string {
		switch algorithm {
		case "SHA-256":
			v := sha256.Sum256([]byte(value))
			return hex.EncodeToString(v[:])
		case "SHA-512-256":
			v := sha512.Sum512_256([]byte(value))
			return hex.EncodeToString(v[:])
		default:
			v := md5.Sum([]byte(value))
			return hex.EncodeToString(v[:])
		}
	}
	a1 := hash("line:carrier:TEST_ONLY_PASSWORD")
	a2 := hash(m.Method + ":" + m.URI)
	if p["response"] != hash(strings.Join([]string{a1, nonce, p["nc"], p["cnonce"], "auth", a2}, ":")) {
		t.Fatal("真实线缆Digest摘要校验失败")
	}
}

// TestTrunkRegistrationAndInviteRealRustMedia 覆盖注册401/407、刷新、认证呼叫、真实双向RTP及停机注销。
// 缺少Rust程序明确跳过；不能用测试媒体替身或仅OPTIONS探针冒充通过。
func TestTrunkRegistrationAndInviteRealRustMedia(t *testing.T) {
	binaryPath := os.Getenv("RUSTSWITCH_REAL_MEDIA")
	if binaryPath == "" {
		binaryPath = "../../../bin/rustswitch-media"
	}
	binaryPath, err := filepath.Abs(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(binaryPath); err != nil {
		t.Skip("真实Rust媒体未构建，中继媒体验证未执行")
	}
	for _, kind := range []string{"udp", "tcp", "tls"} {
		t.Run(kind, func(t *testing.T) {
			mediaPorts, endpoints, signals := isolatedTransportUDP(t), isolatedTransportUDP(t), isolatedTransportUDP(t)
			pair, _, certFile, keyFile := transportCertificate(t)
			peer := &trunkWire{udp: signals[1]}
			upstream := signals[1].LocalAddr().String()
			var listener net.Listener
			accepted := make(chan net.Conn, 1)
			if kind != "udp" {
				listener, err = net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				upstream = listener.Addr().String()
				go func() {
					conn, e := listener.Accept()
					if e != nil {
						accepted <- nil
						return
					}
					if kind == "tls" {
						conn = tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
					}
					accepted <- conn
				}()
			}
			c, err := config.Load("../../../config/local.json")
			if err != nil {
				t.Fatal(err)
			}
			c.SourcePath = ""
			c.Journal.Path = filepath.Join(t.TempDir(), "events.jsonl")
			c.Admin.Listen = freeTransportAddress(t)
			c.SIP.Listen = signals[0].LocalAddr().String()
			c.SIP.Advertise = c.SIP.Listen
			c.SIP.Upstream = upstream
			c.SIP.SetupTimeoutMS = 5000
			c.SIP.AckTimeoutMS = 2000
			c.SIP.LocalExtensions = nil
			passwordFile := filepath.Join(t.TempDir(), "credential")
			if err = os.WriteFile(passwordFile, []byte("TEST_ONLY_PASSWORD"), 0600); err != nil {
				t.Fatal(err)
			}
			c.SIP.TrunkAuth = &config.SIPTrunkAuth{Username: "line", PasswordFile: passwordFile, Realm: "carrier"}
			c.SIP.Registration = &config.SIPRegistration{AOR: "sip:line@carrier", RegistrarURI: "sip:carrier", ExpiresSeconds: 300, TimeoutMS: 5000, RetrySeconds: 30}
			c.SIP.Stream = nil
			if kind != "udp" {
				c.SIP.Stream = &config.SIPStream{UpstreamTransport: kind, MaxConnections: 8, TLSCAFile: certFile, TLSServerName: "localhost"}
				if kind == "tls" {
					c.SIP.Stream.TLSListen = freeTransportAddress(t)
					c.SIP.Stream.TLSCertFile = certFile
					c.SIP.Stream.TLSKeyFile = keyFile
				} else {
					c.SIP.Stream.TCPListen = freeTransportAddress(t)
				}
			}
			c.Media.Binary = binaryPath
			c.Media.Workers = 1
			c.Media.PortReuseDelayMS = 0
			c.Media.AdaptiveAdmission = false
			c.Media.CPUCores = nil
			c.Media.PortStart = mediaPorts[0].LocalAddr().(*net.UDPAddr).Port
			c.Media.PortEnd = c.Media.PortStart + 3
			c.Limits.MaxCalls = 1
			c.Limits.CallsPerSecond = 1
			c.Limits.BurstCalls = 1
			c.Limits.MaxTransactions = 32
			for _, socket := range mediaPorts {
				_ = socket.Close()
			}
			_ = signals[0].Close()
			if err = c.Validate(); err != nil {
				t.Fatal(err)
			}
			// New 的 TLS 拨号需要对端同时握手；这个协程仅属于有外部期限的测试夹具。
			if kind == "tls" {
				go func() {
					conn := <-accepted
					if conn != nil {
						if secured, ok := conn.(*tls.Conn); ok {
							ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							_ = secured.HandshakeContext(ctx)
							cancel()
						}
					}
					accepted <- conn
				}()
			}
			ctx, cancel := context.WithCancel(context.Background())
			s, err := New(ctx, c)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			if kind != "udp" {
				conn := <-accepted
				if conn == nil {
					t.Fatal("中继未接受连接")
				}
				peer = &trunkWire{conn: conn, reader: bufio.NewReaderSize(conn, 4098)}
				defer conn.Close()
			}
			stop := make(chan struct{})
			done := make(chan error, 1)
			finished := false
			go func() { done <- s.Run(stop) }()
			defer func() {
				cancel()
				if !finished {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("控制器未退出")
					}
				}
				s.Close()
			}()
			first := peer.read(t)
			if first.Method != "REGISTER" {
				t.Fatal("启动未注册")
			}
			algorithm := "MD5"
			if kind == "tcp" {
				algorithm = "SHA-256"
			}
			if kind == "tls" {
				algorithm = "SHA-512-256"
			}
			challenge := func(request *sip.Message, status int, name, nonce string) {
				peer.write(t, sip.Response(request, status, "Challenge", "reg", "", c.SIP.Listen, nil, sip.Header{Name: name, Value: `Digest realm="carrier",nonce="` + nonce + `",algorithm=` + algorithm + `,qop="auth"`}))
			}
			challenge(first, 401, "WWW-Authenticate", "reg-uas")
			second := peer.read(t)
			checkTrunkDigest(t, second, "authorization", "reg-uas", algorithm)
			challenge(second, 407, "Proxy-Authenticate", "reg-proxy")
			third := peer.read(t)
			checkTrunkDigest(t, third, "authorization", "reg-uas", algorithm)
			checkTrunkDigest(t, third, "proxy-authorization", "reg-proxy", algorithm)
			contact, _ := sip.URI(third.Header("contact"))
			peer.write(t, sip.Response(third, 200, "OK", "reg", "", c.SIP.Listen, nil, sip.Header{Name: "Contact", Value: "<" + contact + ">;expires=2"}))
			waitTransportCondition(t, func() bool { return s.RegistrationStatus().State == "registered" })
			refresh := peer.read(t)
			if refresh.Method != "REGISTER" || refresh.CallID() != first.CallID() {
				t.Fatal("刷新改变了注册身份")
			}
			n, _, _ := refresh.CSeq()
			if n != 4 {
				t.Fatal("注册刷新CSeq未递增")
			}
			checkTrunkDigest(t, refresh, "authorization", "reg-uas", algorithm)
			peer.write(t, sip.Response(refresh, 200, "OK", "reg", "", c.SIP.Listen, nil, sip.Header{Name: "Contact", Value: "<" + contact + ">;expires=300"}))
			waitTransportCondition(t, func() bool { return s.pool.Workers[0].Healthy.Load() })
			a := &trunkWire{udp: signals[2], peer: netip.MustParseAddrPort(c.SIP.Listen)}
			from := "<sip:line@carrier>;tag=caller"
			a.write(t, sip.Request("INVITE", "sip:100@"+c.SIP.Listen, signals[2].LocalAddr().String(), "z9hG4bK-auth-call", from, "<sip:100@carrier>", "real-auth-"+kind, 1, transportSDP(endpoints[0], endpoints[1])))
			invite := peer.read(t)
			if invite.Method != "INVITE" {
				t.Fatal("上游未收到真实INVITE")
			}
			challenge(invite, 407, "Proxy-Authenticate", "invite-proxy")
			ack, retry := peer.read(t), peer.read(t)
			if ack.Method != "ACK" || ack.Branch() != invite.Branch() || retry.Method != "INVITE" || retry.Branch() == invite.Branch() || !bytes.Equal(invite.Body, retry.Body) {
				t.Fatal("INVITE挑战时序/SDP不正确")
			}
			checkTrunkDigest(t, retry, "proxy-authorization", "invite-proxy", algorithm)
			peer.write(t, sip.Response(retry, 200, "OK", "callee", "sip:callee@"+upstream, c.SIP.Listen, transportSDP(endpoints[2], endpoints[3])))
			ack = peer.read(t)
			if ack.Method != "ACK" || ack.Header("proxy-authorization") != retry.Header("proxy-authorization") {
				t.Fatal("成功ACK未携带相同凭据")
			}
			var answer *sip.Message
			for {
				answer = a.read(t)
				if answer.Status >= 200 {
					break
				}
			}
			if answer.Status != 200 {
				t.Fatal("认证呼叫未接通")
			}
			a.write(t, sip.Request("ACK", "sip:rustswitch@"+c.SIP.Listen, signals[2].LocalAddr().String(), "z9hG4bK-auth-ack", from, answer.Header("to"), "real-auth-"+kind, 1, nil))
			waitTransportCondition(t, func() bool { return s.Stats.Established.Load() == 1 })
			targets := [2]netip.AddrPort{transportMediaTarget(t, answer.Body), transportMediaTarget(t, retry.Body)}
			for sequence := uint16(1); sequence <= 32; sequence++ {
				for side := 0; side < 2; side++ {
					packet := make([]byte, 172)
					packet[0] = 0x80
					binary.BigEndian.PutUint16(packet[2:4], sequence)
					binary.BigEndian.PutUint32(packet[4:8], uint32(sequence)*160)
					binary.BigEndian.PutUint32(packet[8:12], uint32(side+1))
					for i := 12; i < len(packet); i++ {
						packet[i] = 0xff
					}
					if _, err = endpoints[side*2].WriteToUDPAddrPort(packet, targets[side]); err != nil {
						t.Fatal(err)
					}
					destination := endpoints[(1-side)*2]
					_ = destination.SetReadDeadline(time.Now().Add(2 * time.Second))
					buffer := make([]byte, 512)
					size, _, e := destination.ReadFromUDPAddrPort(buffer)
					if e != nil || !bytes.Equal(packet, buffer[:size]) {
						t.Fatal("认证呼叫真实RTP不一致")
					}
				}
			}
			a.write(t, sip.Request("BYE", "sip:rustswitch@"+c.SIP.Listen, signals[2].LocalAddr().String(), "z9hG4bK-auth-bye", from, answer.Header("to"), "real-auth-"+kind, 2, nil))
			if a.read(t).Status != 200 {
				t.Fatal("主叫BYE失败")
			}
			bye := peer.read(t)
			n, method, _ := bye.CSeq()
			if method != "BYE" || n != 3 {
				t.Fatal("认证后BYE序号错误")
			}
			checkTrunkDigest(t, bye, "proxy-authorization", "invite-proxy", algorithm)
			peer.write(t, sip.Response(bye, 200, "OK", "callee", "", c.SIP.Listen, nil))
			waitTransportCondition(t, func() bool { return s.Stats.Active.Load() == 0 })
			close(stop)
			unregister := peer.read(t)
			if unregister.Method != "REGISTER" || unregister.Header("expires") != "0" {
				t.Fatal("停机未注销")
			}
			checkTrunkDigest(t, unregister, "authorization", "reg-uas", algorithm)
			peer.write(t, sip.Response(unregister, 200, "OK", "reg", "", c.SIP.Listen, nil))
			select {
			case err := <-done:
				finished = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("注销完成后未结束排空")
			}
			if s.Stats.SendErrors.Load() != 0 || s.Stats.Malformed.Load() != 0 || s.RegistrationStatus().State != "unregistered" {
				t.Fatal("真实注册/呼叫/注销存在错误")
			}
			t.Log("真实注册401/407、刷新、INVITE Digest、64包双向Rust RTP、BYE与注销通过")
		})
	}
}
