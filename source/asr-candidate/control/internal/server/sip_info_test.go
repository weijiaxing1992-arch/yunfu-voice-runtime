package server

import (
	"fmt"
	"net"
	"net/netip"
	"rustswitch/control/internal/sip"
	"strings"
	"testing"
	"time"
)

// infoFixture 使用真实回环响应socket与现有双腿模型；没有模拟媒体发送成功。
func infoFixture(t *testing.T) (*Server, *Call, *net.UDPConn, *[]map[string]string) {
	t.Helper()
	s, c := forkTestServer(t, 1024)
	listen, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		listen.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { listen.Close(); peer.Close() })
	s.conn = listen
	s.cache = make(map[string]cacheEntry)
	c.ASource = peer.LocalAddr().(*net.UDPAddr).AddrPort()
	c.ATag = "local-a"
	c.ARemoteCSeq = 1
	c.ARequest, err = sip.Parse(sip.Request("INVITE", "sip:callee@localhost", c.ASource.String(), "z9hG4bK-a", "<sip:a@localhost>;tag=remote-a", "<sip:b@localhost>", c.AID, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	c.compatUUIDs = [2]string{compatibilityUUID(), compatibilityUUID()}
	c.compatVariables[0] = map[string]string{"dtmf_type": "info"}
	c.compatVariables[1] = map[string]string{"dtmf_type": "info"}
	events := []map[string]string{}
	s.compatEvents = func(name string, headers map[string]string, _ []byte) {
		if name == "DTMF" {
			events = append(events, headers)
		}
	}
	return s, c, peer, &events
}

func infoRequest(t *testing.T, c *Call, n uint32, body string) *sip.Message {
	t.Helper()
	wire := sip.Request("INFO", "sip:b@localhost", c.ASource.String(), fmt.Sprintf("z9hG4bK-info-%d", n), c.ARequest.Header("from"), "<sip:b@localhost>;tag="+c.ATag, c.AID, n, []byte(body))
	wire = []byte(strings.Replace(string(wire), "application/sdp", "application/dtmf-relay", 1))
	m, err := sip.Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func infoReply(t *testing.T, peer *net.UDPConn) *sip.Message {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 16384)
	n, _, err := peer.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	m, err := sip.Parse(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSIPInfoDTMFDeduplicatesAndPreservesCall 验证真实200响应、事务重放只产生一次业务DTMF，以及逐腿事件身份。
func TestSIPInfoDTMFDeduplicatesAndPreservesCall(t *testing.T) {
	s, c, peer, events := infoFixture(t)
	m := infoRequest(t, c, 2, "Signal=5\r\nDuration=100\r\n")
	for range 3 {
		s.handle(datagram{m, c.ASource})
		if got := infoReply(t, peer); got.Status != 200 {
			t.Fatal(got.Status)
		}
	}
	if len(*events) != 1 || (*events)[0]["DTMF-Digit"] != "5" || (*events)[0]["DTMF-Duration"] != "800" || (*events)[0]["Unique-ID"] != c.compatUUIDs[0] || (*events)[0]["DTMF-Source"] != "INFO" {
		t.Fatal(*events)
	}
	if c.Ended || !c.Established || c.ARemoteCSeq != 2 {
		t.Fatal("INFO影响通话状态")
	}
	// B腿在自己的源和标签上产生B腿事件，不能借用A腿UUID。
	b := infoRequest(t, c, 3, "Signal=#\r\nDuration=200\r\n")
	for i, h := range b.Headers {
		switch h.Name {
		case "call-id":
			b.Headers[i].Value = c.BID
		case "from":
			b.Headers[i].Value = "<sip:b@localhost>;tag=" + c.BRemoteTag
		case "to":
			b.Headers[i].Value = "<sip:a@localhost>;tag=" + c.BTag
		}
	}
	s.Config.SIP.Upstream = c.ASource.String()
	s.handle(datagram{b, c.ASource})
	if got := infoReply(t, peer); got.Status != 200 {
		t.Fatal(got.Status)
	}
	if len(*events) != 2 || (*events)[1]["Unique-ID"] != c.compatUUIDs[1] {
		t.Fatal(*events)
	}
}

// TestSIPInfoRejectsInvalidDialogAndPayload 伪造来源/连接/标签/序号、未启用模式和无效正文都不能采集按键。
func TestSIPInfoRejectsInvalidDialogAndPayload(t *testing.T) {
	for _, kind := range []string{"source", "flow", "tag", "cseq", "ended", "disabled", "body", "content-type", "route"} {
		t.Run(kind, func(t *testing.T) {
			s, c, peer, events := infoFixture(t)
			m := infoRequest(t, c, 2, "Signal=5\r\nDuration=100\r\n")
			source := c.ASource
			want := 481
			switch kind {
			case "source":
				c.ASource = netip.MustParseAddrPort("127.0.0.1:1")
			case "flow":
				c.ARequest.TransportID = 99
			case "tag":
				c.ATag = "different"
			case "cseq":
				c.ARemoteCSeq = 2
			case "ended":
				c.Ended = true
			case "disabled":
				delete(c.compatVariables[0], "dtmf_type")
				want = 488
			case "body":
				m.Body = []byte("Signal=not-a-digit\nDuration=100")
				want = 400
			case "content-type":
				for i, h := range m.Headers {
					if h.Name == "content-type" {
						m.Headers[i].Value = "application/dtmf"
					}
				}
				want = 415
			case "route":
				m.Headers = append(m.Headers, sip.Header{Name: "route", Value: "<sip:elsewhere>"})
				want = 501
			}
			s.handle(datagram{m, source})
			if got := infoReply(t, peer); got.Status != want {
				t.Fatalf("得到%d，期望%d", got.Status, want)
			}
			if len(*events) != 0 {
				t.Fatal("错误请求产生按键事件", *events)
			}
		})
	}
}
