package server

import (
	"bytes"
	"net"
	"rustswitch/control/internal/sip"
	"testing"
)

// trunkInviteFixture 建立无媒体资源的在途INVITE，用真实UDP记录每条新请求及ACK。
func trunkInviteFixture(t *testing.T, s *Server) *Call {
	t.Helper()
	a, err := sip.Parse(sip.Request("INVITE", "sip:100@carrier", s.Config.SIP.Listen, "z9hG4bK-caller", "<sip:caller@carrier>;tag=a", "<sip:100@carrier>", "caller-auth-test", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	c := &Call{ID: 1, AID: a.CallID(), BID: "outbound-auth-test", ARequest: a, ASource: s.conn.LocalAddr().(*net.UDPAddr).AddrPort(), ATag: "local-a", BTag: "local-b", BURI: "sip:100@carrier", BFrom: "<sip:caller@carrier>;tag=local-b", BTo: "<sip:100@carrier>", InviteBranch: "z9hG4bK-first", BInviteCSeq: 1, BLocalCSeq: 1, BInviteSent: true}
	s.calls[c.ID] = c
	s.byDialog[c.BID] = c.ID
	wire := s.requestFlow(0, "INVITE", c.BURI, c.InviteBranch, c.BFrom, c.BTo, c.BID, 1, []byte("immutable-sdp"))
	s.startTransaction(c, "INVITE", c.InviteBranch, wire, s.upstream())
	return c
}

// TestInviteDigestRetriesPreserveACKAndCancellation 检查401→407重试、旧挑战去重、ACK凭据和取消序号。
func TestInviteDigestRetriesPreserveACKAndCancellation(t *testing.T) {
	s, peer := trunkUnitServer(t)
	c := trunkInviteFixture(t, s)
	first := readTrunkUDP(t, peer)
	challenge := trunkResponse(t, s, first, 401, sip.Header{Name: "WWW-Authenticate", Value: `Digest realm="carrier",nonce="uas-nonce",qop="auth",algorithm=SHA-256`})
	ack, second := readTrunkUDP(t, peer), readTrunkUDP(t, peer)
	an, _, _ := ack.CSeq()
	sn, _, _ := second.CSeq()
	if ack.Method != "ACK" || ack.Branch() != first.Branch() || an != 1 || sn != 2 || second.Branch() == first.Branch() || second.CallID() != first.CallID() || !bytes.Equal(first.Body, second.Body) || second.Header("authorization") == "" {
		t.Fatal("认证重试破坏事务/正文身份")
	}
	s.handle(datagram{challenge, s.upstream()})
	replayed := readTrunkUDP(t, peer)
	if replayed.Method != "ACK" || c.BInviteCSeq != 2 || len(s.transactions) != 1 {
		t.Fatal("重复挑战重新创建呼叫")
	}
	trunkResponse(t, s, second, 407, sip.Header{Name: "Proxy-Authenticate", Value: `Digest realm="carrier",nonce="proxy-nonce",qop="auth",algorithm=MD5`})
	ack, third := readTrunkUDP(t, peer), readTrunkUDP(t, peer)
	if ack.Header("authorization") != second.Header("authorization") || third.Header("authorization") == "" || third.Header("proxy-authorization") == "" || c.BInviteCSeq != 3 {
		t.Fatal("双重挑战/ACK凭据不正确")
	}
	c.BProvisional = true
	s.sendCancelB(c)
	cancel := readTrunkUDP(t, peer)
	n, method, _ := cancel.CSeq()
	if method != "CANCEL" || n != 3 || cancel.Branch() != third.Branch() {
		t.Fatal("认证后CANCEL使用了旧序号或分支")
	}
	c.CancelWanted = true
	trunkResponse(t, s, third, 401, sip.Header{Name: "WWW-Authenticate", Value: `Digest realm="carrier",nonce="new-nonce",stale=true,qop="auth"`})
	ack = readTrunkUDP(t, peer)
	if ack.Method != "ACK" || c.BInviteCSeq != 3 {
		t.Fatal("已取消通话因认证重新拨号")
	}
}

// TestInviteAuthenticationInvalidChallengesFailClosed 不匹配域、重复参数及不支持的qop不能得到凭据。
func TestInviteAuthenticationInvalidChallengesFailClosed(t *testing.T) {
	for _, value := range []string{`Digest realm="other",nonce="n"`, `Digest realm="carrier",nonce="n",nonce="x"`, `Digest realm="carrier",nonce="n",qop="auth-int"`, `Basic abc`} {
		t.Run(value[:5], func(t *testing.T) {
			s, peer := trunkUnitServer(t)
			c := trunkInviteFixture(t, s)
			first := readTrunkUDP(t, peer)
			trunkResponse(t, s, first, 401, sip.Header{Name: "WWW-Authenticate", Value: value})
			ack := readTrunkUDP(t, peer)
			if ack.Method != "ACK" || !c.Ended || c.AStatus != 502 || c.BInviteCSeq != 1 {
				t.Fatal("无效挑战未有界失败")
			}
		})
	}
}
