package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/sip"
	"strconv"
	"strings"
	"testing"
	"time"
)

// transportCertificate 创建临时测试 CA 及监听证书，客户端仍校验名称和信任链。
func transportCertificate(t *testing.T) (tls.Certificate, *x509.CertPool, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "RustSwitch test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	rawKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: rawKey})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return pair, roots, certPath, keyPath
}

// isolatedTransportUDP 仅预留主服务媒体范围以外的端口，避免占用 10000—64999。
func isolatedTransportUDP(t *testing.T) [4]*net.UDPConn {
	t.Helper()
	for base := 65000; base <= 65528; base += 4 {
		var sockets [4]*net.UDPConn
		ok := true
		for i := range sockets {
			conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(base+i))))
			if err != nil {
				ok = false
				break
			}
			sockets[i] = conn
		}
		if ok {
			t.Cleanup(func() {
				for _, c := range sockets {
					_ = c.Close()
				}
			})
			return sockets
		}
		for _, c := range sockets {
			if c != nil {
				_ = c.Close()
			}
		}
	}
	t.Fatal("隔离 UDP 端口不足，测试未执行")
	return [4]*net.UDPConn{}
}

// freeTransportAddress 获取临时 TCP 地址；与 UDP 媒体端口不共用资源。
func freeTransportAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

// readTransportMessage 用生产分帧器读取对端真实消息，失败有明确期限。
func readTransportMessage(t *testing.T, conn net.Conn, reader *bufio.Reader) *sip.Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	wire, err := sip.ReadStreamFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	message, err := sip.Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

// waitTransportCondition 等待原子快照，避免跨线程读取主循环私有映射。
func waitTransportCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("真实传输状态未按期收敛")
}

// transportSDP 为两端真实 RTP/RTCP socket 生成 PCMU 描述。
func transportSDP(rtp, rtcp *net.UDPConn) []byte {
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=transport-test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=rtcp:%d\r\na=ptime:20\r\na=sendrecv\r\n", rtp.LocalAddr().(*net.UDPAddr).Port, rtcp.LocalAddr().(*net.UDPAddr).Port))
}

// transportMediaTarget 读取实际协商 SDP，禁止测试伪造分配结果。
func transportMediaTarget(t *testing.T, body []byte) netip.AddrPort {
	t.Helper()
	for _, line := range strings.Split(string(body), "\r\n") {
		if strings.HasPrefix(line, "m=audio ") {
			n, err := strconv.Atoi(strings.Fields(line)[1])
			if err != nil {
				t.Fatal(err)
			}
			return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(n))
		}
	}
	t.Fatal("缺少真实媒体目标")
	return netip.AddrPort{}
}

// TestSIPReliableTransportRealRustMedia 逐一验证 TCP/TLS 双腿真实 INVITE/ACK/BYE、双向 Rust RTP 和资源回收。
// 缺少 Rust 媒体程序的源码环境明确跳过，不得用替身或 OPTIONS 成功冒充媒体兼容。
func TestSIPReliableTransportRealRustMedia(t *testing.T) {
	mediaBinary := os.Getenv("RUSTSWITCH_REAL_MEDIA")
	if mediaBinary == "" {
		mediaBinary = "../../../bin/rustswitch-media"
	}
	mediaBinary, err := filepath.Abs(mediaBinary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(mediaBinary); err != nil {
		t.Skip("真实 Rust 媒体程序未构建，可靠传输媒体用例未验证")
	}
	for _, scenario := range []struct{ kind, end string }{{"tcp", "bye"}, {"tls", "bye"}, {"tcp", "caller_disconnect"}, {"tls", "upstream_disconnect"}} {
		kind := scenario.kind
		t.Run(kind+"_"+scenario.end, func(t *testing.T) {
			pair, roots, certPath, keyPath := transportCertificate(t)
			upstream, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer upstream.Close()
			type accepted struct {
				conn net.Conn
				err  error
			}
			acceptedPeer := make(chan accepted, 1)
			go func() {
				conn, e := upstream.Accept()
				if e == nil && kind == "tls" {
					secured := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					e = secured.HandshakeContext(ctx)
					cancel()
					if e != nil {
						_ = conn.Close()
					} else {
						conn = secured
					}
				}
				acceptedPeer <- accepted{conn, e}
			}()
			mediaPorts, endpoints, signalPorts := isolatedTransportUDP(t), isolatedTransportUDP(t), isolatedTransportUDP(t)
			c, err := config.Load("../../../config/local.json")
			if err != nil {
				t.Fatal(err)
			}
			c.SourcePath = ""
			c.Journal.Path = filepath.Join(t.TempDir(), "events.jsonl")
			c.SIP.Listen = signalPorts[0].LocalAddr().String()
			c.SIP.Advertise = c.SIP.Listen
			c.SIP.Upstream = upstream.Addr().String()
			c.SIP.SetupTimeoutMS = 5000
			c.SIP.AckTimeoutMS = 3000
			c.Admin.Listen = freeTransportAddress(t)
			c.Media.Binary = mediaBinary
			c.Media.Workers = 1
			c.Media.PortReuseDelayMS = 0
			c.Media.AdaptiveAdmission = false
			c.Media.CPUCores = nil
			c.Media.PortStart = mediaPorts[0].LocalAddr().(*net.UDPAddr).Port
			c.Media.PortEnd = c.Media.PortStart + 3
			c.Limits.MaxCalls = 1
			c.Limits.CallsPerSecond = 1
			c.Limits.BurstCalls = 1
			c.Limits.MaxTransactions = 16
			address := freeTransportAddress(t)
			c.SIP.Stream = &config.SIPStream{UpstreamTransport: kind, MaxConnections: 8, TLSCAFile: certPath, TLSServerName: "localhost"}
			if kind == "tls" {
				c.SIP.Stream.TLSListen = address
				c.SIP.Stream.TLSCertFile = certPath
				c.SIP.Stream.TLSKeyFile = keyPath
			} else {
				c.SIP.Stream.TCPListen = address
			}
			for _, socket := range mediaPorts {
				_ = socket.Close()
			}
			_ = signalPorts[0].Close()
			if err = c.Validate(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			s, err := New(ctx, c)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			runDone := make(chan error, 1)
			go func() { runDone <- s.Run(nil) }()
			defer func() { cancel(); <-runDone; s.Close() }()
			peer := <-acceptedPeer
			if peer.err != nil {
				t.Fatal(peer.err)
			}
			defer peer.conn.Close()
			waitTransportCondition(t, func() bool { return s.pool.Workers[0].Healthy.Load() })
			var a net.Conn
			if kind == "tls" {
				a, err = tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp4", address, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost"})
			} else {
				a, err = net.DialTimeout("tcp4", address, 5*time.Second)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			ar, br := bufio.NewReaderSize(a, 4098), bufio.NewReaderSize(peer.conn, 4098)
			from, to := "<sip:alice@127.0.0.1>;tag=caller", "<sip:100@127.0.0.1>"
			invite := sip.RequestTransport(kind, "INVITE", "sip:100@"+address, a.LocalAddr().String(), "z9hG4bK-transport", from, to, "transport-"+kind, 1, transportSDP(endpoints[0], endpoints[1]))
			// 故意按不对齐的小片段写入，接入必须等待完整请求后才分配媒体。
			for offset := 0; offset < len(invite); offset += 17 {
				if _, err = a.Write(invite[offset:min(offset+17, len(invite))]); err != nil {
					t.Fatal(err)
				}
			}
			bInvite := readTransportMessage(t, peer.conn, br)
			if bInvite.Method != "INVITE" || bInvite.Transport() != kind {
				t.Fatalf("上游未收到真实 %s INVITE：%s", kind, bInvite.Method)
			}
			if _, err = peer.conn.Write(sip.Response(bInvite, 200, "OK", "callee", "sip:callee@"+upstream.Addr().String()+";transport="+kind, peer.conn.RemoteAddr().String(), transportSDP(endpoints[2], endpoints[3]))); err != nil {
				t.Fatal(err)
			}
			bAck := readTransportMessage(t, peer.conn, br)
			if bAck.Method != "ACK" {
				t.Fatal("上游未收到 ACK")
			}
			var aAnswer *sip.Message
			for {
				aAnswer = readTransportMessage(t, a, ar)
				if aAnswer.Status >= 200 {
					break
				}
			}
			if aAnswer.Status != 200 {
				t.Fatalf("呼叫未接通：%d %s", aAnswer.Status, aAnswer.Reason)
			}
			ack := sip.RequestTransport(kind, "ACK", "sip:rustswitch@"+address, a.LocalAddr().String(), "z9hG4bK-ack", from, aAnswer.Header("to"), "transport-"+kind, 1, nil)
			if _, err = a.Write(ack); err != nil {
				t.Fatal(err)
			}
			waitTransportCondition(t, func() bool { return s.Stats.Established.Load() == 1 })
			aTarget, bTarget := transportMediaTarget(t, aAnswer.Body), transportMediaTarget(t, bInvite.Body)
			for sequence := uint16(1); sequence <= 32; sequence++ {
				for direction := 0; direction < 2; direction++ {
					source, destination, target := endpoints[0], endpoints[2], aTarget
					if direction == 1 {
						source, destination, target = endpoints[2], endpoints[0], bTarget
					}
					packet := make([]byte, 172)
					packet[0] = 0x80
					binary.BigEndian.PutUint16(packet[2:4], sequence)
					binary.BigEndian.PutUint32(packet[4:8], uint32(sequence)*160)
					binary.BigEndian.PutUint32(packet[8:12], uint32(direction+1))
					for i := 12; i < len(packet); i++ {
						packet[i] = 0xff
					}
					if _, err = source.WriteToUDPAddrPort(packet, target); err != nil {
						t.Fatal(err)
					}
					_ = destination.SetReadDeadline(time.Now().Add(2 * time.Second))
					buffer := make([]byte, 512)
					n, _, err := destination.ReadFromUDPAddrPort(buffer)
					if err != nil || !bytes.Equal(packet, buffer[:n]) {
						t.Fatalf("方向 %d 第 %d 个真实 Rust RTP 包不一致：%v", direction, sequence, err)
					}
				}
			}
			if scenario.end != "bye" {
				survivor, reader := peer.conn, br
				if scenario.end == "caller_disconnect" {
					_ = a.Close()
				} else {
					_ = peer.conn.Close()
					survivor, reader = a, ar
				}
				ended := readTransportMessage(t, survivor, reader)
				if ended.Method != "BYE" {
					t.Fatal("连接断开后另一条腿未被拆线")
				}
				if _, err = survivor.Write(sip.Response(ended, 200, "OK", "", "", survivor.RemoteAddr().String(), nil)); err != nil {
					t.Fatal(err)
				}
				waitTransportCondition(t, func() bool { return s.Stats.Active.Load() == 0 && s.Stats.Established.Load() == 0 })
				if s.Stats.Abnormal.Load() != 1 {
					t.Fatal("断线未记录异常结束")
				}
				t.Logf("%s：64 个双向 Rust RTP 包后真实断线、对腿 BYE 和资源回收通过", scenario.end)
				return
			}
			bye := sip.RequestTransport(kind, "BYE", "sip:rustswitch@"+address, a.LocalAddr().String(), "z9hG4bK-bye", from, aAnswer.Header("to"), "transport-"+kind, 2, nil)
			if _, err = a.Write(bye); err != nil {
				t.Fatal(err)
			}
			aBye := readTransportMessage(t, a, ar)
			if aBye.Status != 200 {
				t.Fatal("A 腿 BYE 未获得成功响应")
			}
			bBye := readTransportMessage(t, peer.conn, br)
			if bBye.Method != "BYE" || bBye.Transport() != kind {
				t.Fatal("B 腿未经原连接拆线")
			}
			if _, err = peer.conn.Write(sip.Response(bBye, 200, "OK", "callee", "", peer.conn.RemoteAddr().String(), nil)); err != nil {
				t.Fatal(err)
			}
			waitTransportCondition(t, func() bool { return s.Stats.Active.Load() == 0 && s.Stats.Established.Load() == 0 })
			if s.Stats.SendErrors.Load() != 0 || s.Stats.Malformed.Load() != 0 {
				t.Fatalf("信令发送或解析错误：%d/%d", s.Stats.SendErrors.Load(), s.Stats.Malformed.Load())
			}
			t.Logf("%s 双腿 INVITE/ACK/BYE、64 个真实双向 Rust RTP 包及资源回收通过", kind)
		})
	}
}
