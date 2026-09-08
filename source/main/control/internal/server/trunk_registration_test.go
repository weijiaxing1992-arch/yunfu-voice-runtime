package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/sip"
	"strings"
	"testing"
	"time"
)

// trunkUnitServer 只创建真实隔离 UDP 信令，不启动媒体；测试按唯一线程推进注册与计时器。
func trunkUnitServer(t *testing.T) (*Server, *net.UDPConn) {
	t.Helper()
	sockets := isolatedTransportUDP(t)
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("TEST_ONLY_PASSWORD\n"), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := journal.Open(filepath.Join(t.TempDir(), "events"), 128)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	s := &Server{journal: log, conn: sockets[0], calls: make(map[uint64]*Call), byDialog: make(map[string]uint64), transactions: make(map[string]*transaction), cache: make(map[string]cacheEntry), Config: config.Config{SIP: config.SIP{Listen: sockets[0].LocalAddr().String(), Advertise: sockets[0].LocalAddr().String(), Upstream: sockets[1].LocalAddr().String(), TrunkAuth: &config.SIPTrunkAuth{Username: "line", PasswordFile: path, Realm: "carrier"}, Registration: &config.SIPRegistration{AOR: "sip:line@carrier", RegistrarURI: "sip:carrier", TimeoutMS: 500, RetrySeconds: 1}}, Limits: config.Limits{MaxTransactions: 32}}}
	if err = s.initTrunk(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.clearTrunkCredentials)
	return s, sockets[1]
}

// readTrunkUDP 读取一条真实数据报；单个失败最多等待两秒。
func readTrunkUDP(t *testing.T, conn *net.UDPConn) *sip.Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, sip.MaxMessageSize)
	n, _, err := conn.ReadFromUDPAddrPort(buffer)
	if err != nil {
		t.Fatal(err)
	}
	m, err := sip.Parse(buffer[:n])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// trunkResponse 将测试响应送回状态机，默认使用真实固定上游身份。
func trunkResponse(t *testing.T, s *Server, request *sip.Message, status int, extra ...sip.Header) *sip.Message {
	t.Helper()
	m, err := sip.Parse(sip.Response(request, status, "Fixture", "registrar", "", s.Config.SIP.Listen, nil, extra...))
	if err != nil {
		t.Fatal(err)
	}
	s.handle(datagram{m, s.upstream()})
	return m
}

// TestRegistrationRejectsForeignResponsesAndLoops 伪造事务/来源不能推进注册，错误或循环挑战不会无限发包。
func TestRegistrationRejectsForeignResponsesAndLoops(t *testing.T) {
	s, peer := trunkUnitServer(t)
	s.startRegistration()
	first := readTrunkUDP(t, peer)
	challenge := sip.Header{Name: "WWW-Authenticate", Value: `Digest realm="carrier",nonce="nonce-1",qop="auth",algorithm=SHA-256`}
	m, err := sip.Parse(sip.Response(first, 401, "Fixture", "registrar", "", s.Config.SIP.Listen, nil, challenge))
	if err != nil {
		t.Fatal(err)
	}
	wrong := *m
	wrong.TransportID = 999
	s.handle(datagram{&wrong, s.upstream()})
	if s.registration().cseq != 1 {
		t.Fatal("不同连接挑战推进了注册")
	}
	s.handle(datagram{m, s.conn.LocalAddr().(*net.UDPAddr).AddrPort()})
	if s.registration().cseq != 1 {
		t.Fatal("不同来源挑战推进了注册")
	}
	for name, value := range map[string]string{"call-id": "foreign", "cseq": "999 REGISTER", "from": "<sip:line@carrier>;tag=foreign", "to": "<sip:other@carrier>", "via": "SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bKforeign"} {
		wrong := *m
		wrong.Headers = append([]sip.Header(nil), m.Headers...)
		for i := range wrong.Headers {
			if wrong.Headers[i].Name == name {
				wrong.Headers[i].Value = value
			}
		}
		s.handle(datagram{&wrong, s.upstream()})
		if s.registration().cseq != 1 {
			t.Fatal("错误事务/地址标识推进注册")
		}
	}
	s.handle(datagram{m, s.upstream()})
	second := readTrunkUDP(t, peer)
	if second.CallID() != first.CallID() || second.Branch() == first.Branch() || second.Header("authorization") == "" {
		t.Fatal("注册认证重试身份不正确")
	}
	trunkResponse(t, s, second, 401, challenge)
	if s.registration().pending || s.RegistrationStatus().Error != "authentication_failed" || len(s.timers) != 1 {
		t.Fatal("循环挑战没有退避或留下多余定时器")
	}
	encoded, err := json.Marshal(s.RegistrationStatus())
	if err != nil || strings.Contains(string(encoded), "PASSWORD") || strings.Contains(string(encoded), "nonce") {
		t.Fatal("状态泄漏认证信息")
	}
}

// TestRegistration423ExpiryTimeoutAndShutdown 验证过短租期、异常200、计时取消及无通话停机等待。
func TestRegistration423ExpiryTimeoutAndShutdown(t *testing.T) {
	s, peer := trunkUnitServer(t)
	s.startRegistration()
	first := readTrunkUDP(t, peer)
	trunkResponse(t, s, first, 423, sip.Header{Name: "Min-Expires", Value: "600"})
	second := readTrunkUDP(t, peer)
	if second.Header("expires") != "600" || s.registration().requested != 600 {
		t.Fatal("未按有效Min-Expires调整")
	}
	trunkResponse(t, s, second, 200, sip.Header{Name: "Contact", Value: "<sip:someone@127.0.0.1>;expires=600"})
	if s.RegistrationStatus().Error != "invalid_binding_response" {
		t.Fatal("其他绑定被当作注册成功")
	}
	s.beginRegistration(false)
	_ = readTrunkUDP(t, peer)
	r := s.registration()
	s.onTimer(timerItem{kind: "register_expire", version: r.version})
	if s.registrationPending() || s.RegistrationStatus().Error != "timeout" {
		t.Fatal("注册超时未收敛")
	}
	s.stopRegistration()
	unregister := readTrunkUDP(t, peer)
	if unregister.Header("expires") != "0" || !s.registrationPending() {
		t.Fatal("无通话时没有等待注销")
	}
	trunkResponse(t, s, unregister, 200)
	if s.registrationPending() || s.RegistrationStatus().State != "unregistered" || len(s.timers) != 0 {
		t.Fatal("注销后仍有循环任务")
	}
}

// TestRegistrationStaleChallengeBound 强制改变nonce也不能越过每轮四次挑战上限。
func TestRegistrationStaleChallengeBound(t *testing.T) {
	s, peer := trunkUnitServer(t)
	s.startRegistration()
	for i := 0; i < 5; i++ {
		request := readTrunkUDP(t, peer)
		trunkResponse(t, s, request, 401, sip.Header{Name: "WWW-Authenticate", Value: `Digest realm="carrier",nonce="nonce-` + string(rune('a'+i)) + `",stale=true,qop="auth"`})
	}
	if s.registration().pending || s.registration().cseq != 5 || s.RegistrationStatus().Error != "authentication_failed" {
		t.Fatal("恶意stale挑战突破次数上限")
	}
}

// TestTrunkSecretFilesAndConfigCopies 私密文件不接受链接/公共权限，管理副本不共享配置指针。
func TestTrunkSecretFilesAndConfigCopies(t *testing.T) {
	s, _ := trunkUnitServer(t)
	original := s.Config
	copy := cloneAdminConfig(original)
	copy.SIP.TrunkAuth.Username = "changed"
	copy.SIP.Registration.AOR = "sip:other@carrier"
	if original.SIP.TrunkAuth.Username != "line" || original.SIP.Registration.AOR != "sip:line@carrier" {
		t.Fatal("管理深拷贝共享注册配置")
	}
	if err := os.Chmod(original.SIP.TrunkAuth.PasswordFile, 0644); err != nil {
		t.Fatal(err)
	}
	if (&Server{Config: original}).initTrunk() == nil {
		t.Fatal("接受共享权限密码文件")
	}
	if err := os.Chmod(original.SIP.TrunkAuth.PasswordFile, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(original.SIP.TrunkAuth.PasswordFile, link); err != nil {
		t.Fatal(err)
	}
	copy = cloneAdminConfig(original)
	copy.SIP.TrunkAuth.PasswordFile = link
	if (&Server{Config: copy}).initTrunk() == nil {
		t.Fatal("接受密码软链接")
	}
}

// TestRegistrationShutdownTimeoutAndOldTimers 注销失败不能标为成功；旧版本定时器不能干扰新请求。
func TestRegistrationShutdownTimeoutAndOldTimers(t *testing.T) {
	s, peer := trunkUnitServer(t)
	s.startRegistration()
	_ = readTrunkUDP(t, peer)
	oldVersion := s.registration().version
	s.stopRegistration()
	_ = readTrunkUDP(t, peer)
	s.onTimer(timerItem{kind: "register_expire", version: oldVersion})
	if !s.registrationPending() {
		t.Fatal("旧注册超时结束了新注销")
	}
	s.onTimer(timerItem{kind: "register_expire", version: s.registration().version})
	if s.registrationPending() || s.RegistrationStatus().State != "unregister_failed" || s.RegistrationStatus().Error != "timeout" {
		t.Fatal("注销超时被错误标记为成功")
	}
}

// TestRegistrationContactExpiryValidation 覆盖多个绑定、重复expires和安全注销的精确边界。
func TestRegistrationContactExpiryValidation(t *testing.T) {
	for _, test := range []struct {
		value          string
		closing, valid bool
		expires        int
	}{
		{"<sip:line@carrier>;expires=60", false, true, 60},
		{"<sip:other@carrier>;expires=90,<sip:line@carrier>;expires=2", false, true, 2},
		{"<sip:line@carrier>;expires=60;expires=600", false, false, 0},
		{"<sip:line@carrier>;expires=-1", false, false, 0},
		{"<sip:line@carrier>;expires=86401", false, false, 0},
		{"<sip:line@carrier>;expires=0", true, true, 0},
		{"<sip:other@carrier>;expires=90", true, true, 0},
	} {
		m := &sip.Message{Headers: []sip.Header{{Name: "contact", Value: test.value}}}
		n, ok := registrationExpires(m, "sip:line@carrier", test.closing)
		if ok != test.valid || ok && n != test.expires {
			t.Fatal("Contact有效期边界判定不符")
		}
	}
}
