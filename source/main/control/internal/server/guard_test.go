package server

import (
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestGuard 创建独立保护器并返回确定的初始时刻，测试不依赖睡眠或调度速度。
func newTestGuard(t *testing.T) (*AdmissionGuard, time.Time) {
	t.Helper()
	hard := config.Limits{MaxCalls: 10000, CallsPerSecond: 1000, BurstCalls: 2000, MaxTransactions: 40000}
	g, err := NewAdmissionGuard(hard, DefaultGuardPolicy(hard))
	if err != nil {
		t.Fatal(err)
	}
	return g, g.lastRefill
}

// TestGuardDefaultAndValidation 确认默认值适配小规格服务器，非法更新保持当前策略和余额不变。
func TestGuardDefaultAndValidation(t *testing.T) {
	hard := config.Limits{MaxCalls: 16, CallsPerSecond: 8, BurstCalls: 12}
	p := DefaultGuardPolicy(hard)
	if p.MaxActiveCalls != 16 || p.MaxEstablishingCalls != 16 || p.CallsPerSecond != 8 || p.BurstCalls != 8 {
		t.Fatalf("默认策略没有收敛到启动限制：%+v", p)
	}
	g, now := newTestGuard(t)
	before := g.Snapshot(0, 0, now)
	cases := map[string]func(*GuardPolicy){
		"总并发越界":  func(p *GuardPolicy) { p.MaxActiveCalls = 10001 },
		"建立中为零":  func(p *GuardPolicy) { p.MaxEstablishingCalls = 0 },
		"接入速率越界": func(p *GuardPolicy) { p.CallsPerSecond = 1001 },
		"突发额度越界": func(p *GuardPolicy) { p.BurstCalls = 2001 },
		"软阈值非数":  func(p *GuardPolicy) { p.SoftLimitRatio = math.NaN() },
		"最低速率无穷": func(p *GuardPolicy) { p.MinimumRateRatio = math.Inf(1) },
		"软阈值等于一": func(p *GuardPolicy) { p.SoftLimitRatio = 1 },
		"最低速率为零": func(p *GuardPolicy) { p.MinimumRateRatio = 0 },
		"重试间隔越界": func(p *GuardPolicy) { p.RetryAfterSeconds = 3601 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			invalid := before.Policy
			change(&invalid)
			if g.Validate(invalid) == nil || g.Update(invalid) == nil {
				t.Fatal("非法策略被接受")
			}
			if after := g.Snapshot(0, 0, now); after != before {
				t.Fatalf("拒绝的更新修改了运行状态：%+v", after)
			}
		})
	}
	if _, err := NewAdmissionGuard(config.Limits{}, p); err == nil {
		t.Fatal("无效硬上限被接受")
	}
}

// TestGuardPeakBoundaries 验证总并发与建立中分别拒绝到达上限的下一通，且拒绝不扣令牌。
func TestGuardPeakBoundaries(t *testing.T) {
	cases := []struct {
		name                string // 场景名称。
		active, established uint64 // 准入前已经预留和已建立的数量。
		reason              string // 预期拒绝原因，空字符串表示允许。
	}{
		{"总并发最后一席", 7999, 7999, ""},
		{"总并发达到上限", 8000, 8000, "max_active_calls"},
		{"建立中最后一席", 7999, 7000, ""},
		{"建立中达到上限", 7000, 6000, "max_establishing_calls"},
		{"采样先后不下溢", 1, 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, now := newTestGuard(t)
			decision := g.TryAdmit(tc.active, tc.established, now)
			if decision.Reason != tc.reason || decision.Allowed != (tc.reason == "") {
				t.Fatalf("准入结果错误：%+v", decision)
			}
			snapshot := g.Snapshot(tc.active, tc.established, now)
			wantTokens := float64(snapshot.Policy.BurstCalls)
			if decision.Allowed {
				wantTokens--
			} else if decision.RetryAfterSeconds != 1 {
				t.Fatalf("拒绝未返回重试间隔：%+v", decision)
			}
			if snapshot.Tokens != wantTokens {
				t.Fatalf("令牌变化错误：%v，希望 %v", snapshot.Tokens, wantTokens)
			}
		})
	}
}

// TestGuardSmoothRefill 使用明确时间跨度验证软阈值、线性缩速、令牌耗尽和恢复边界。
func TestGuardSmoothRefill(t *testing.T) {
	g, now := newTestGuard(t)
	p := g.policy
	p.CallsPerSecond = 100
	if err := g.update(p, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < p.BurstCalls; i++ {
		if !g.TryAdmit(0, 0, now).Allowed {
			t.Fatal("初始突发额度提前耗尽")
		}
	}
	if d := g.TryAdmit(0, 0, now); d.Reason != "rate_limited" {
		t.Fatalf("余额耗尽仍允许接入：%+v", d)
	}
	if snapshot := g.Snapshot(6400, 6400, now); snapshot.EffectiveCPS != 100 || snapshot.Throttled {
		t.Fatalf("软阈值处不应提前缩速：%+v", snapshot)
	}
	for _, counts := range [][2]uint64{{7200, 7200}, {900, 0}} {
		snapshot := g.Snapshot(counts[0], counts[1], now)
		if math.Abs(snapshot.EffectiveCPS-55) > 1e-9 || !snapshot.Throttled {
			t.Fatalf("九成占用未按线性曲线缩为每秒 55 通：%+v", snapshot)
		}
	}
	filled := g.Snapshot(7200, 7200, now.Add(time.Second))
	if math.Abs(filled.Tokens-55) > 1e-9 {
		t.Fatalf("按缩速后的速率补充错误：%v", filled.Tokens)
	}
	if snapshot := g.Snapshot(8000, 8000, now.Add(time.Second)); math.Abs(snapshot.EffectiveCPS-10) > 1e-9 || snapshot.LimitReason != "max_active_calls" {
		t.Fatalf("硬阈值必须拒绝，即使最小补充速率非零：%+v", snapshot)
	}
	if back := g.Snapshot(8000, 8000, now); back.Tokens != filled.Tokens {
		t.Fatal("回退时间改变了余额")
	}
}

// TestGuardUpdatesNeverRefillBurst 验证在线保存、提高突发和反复启停均不会凭空增加余额。
func TestGuardUpdatesNeverRefillBurst(t *testing.T) {
	g, now := newTestGuard(t)
	for i := 0; i < 987; i++ {
		g.TryAdmit(0, 0, now)
	}
	p := g.policy
	p.BurstCalls = 2000
	for i := 0; i < 100; i++ {
		p.Enabled = i%2 == 0
		if err := g.update(p, now); err != nil {
			t.Fatal(err)
		}
		if snapshot := g.Snapshot(0, 0, now); snapshot.Tokens != 13 {
			t.Fatalf("策略保存绕过令牌限制：%+v", snapshot)
		}
	}
	p.Enabled = true
	p.BurstCalls = 3
	if err := g.update(p, now); err != nil {
		t.Fatal(err)
	}
	if snapshot := g.Snapshot(0, 0, now); snapshot.Tokens != 3 {
		t.Fatalf("降低突发没有收紧余额：%v", snapshot.Tokens)
	}
}

// TestGuardDisabledRetainsHardCaps 确认关闭附加保护时仍执行启动总并发、接入速率及瞬时上限。
func TestGuardDisabledRetainsHardCaps(t *testing.T) {
	hard := config.Limits{MaxCalls: 10, CallsPerSecond: 2, BurstCalls: 3}
	p := DefaultGuardPolicy(hard)
	p.Enabled, p.MaxActiveCalls, p.MaxEstablishingCalls, p.CallsPerSecond, p.BurstCalls = false, 1, 1, 1, 1
	g, err := NewAdmissionGuard(hard, p)
	if err != nil {
		t.Fatal(err)
	}
	now := g.lastRefill
	for i := 0; i < hard.BurstCalls; i++ {
		if !g.TryAdmit(5, 0, now).Allowed {
			t.Fatal("关闭保护后仍被附加阈值阻挡")
		}
	}
	if d := g.TryAdmit(5, 0, now); d.Reason != "rate_limited" {
		t.Fatal("关闭保护后绕过了硬突发限制")
	}
	if d := g.TryAdmit(10, 10, now.Add(time.Second)); d.Reason != "max_active_calls" {
		t.Fatal("关闭保护后绕过了硬并发限制")
	}
	if snapshot := g.Snapshot(0, 0, now.Add(time.Second)); snapshot.Tokens != 2 || snapshot.EffectiveCPS != 2 {
		t.Fatalf("关闭保护后硬 CPS 不正确：%+v", snapshot)
	}
}

// TestGuardConcurrentUpdatesAndSnapshots 在竞争检测器下检查更新原子性、计数完整性和余额边界。
func TestGuardConcurrentUpdatesAndSnapshots(t *testing.T) {
	g, _ := newTestGuard(t)
	first := g.policy
	second := first
	second.MaxActiveCalls, second.MaxEstablishingCalls, second.CallsPerSecond, second.BurstCalls = 7000, 500, 300, 500
	var workers sync.WaitGroup
	const iterations = 400
	for role := 0; role < 6; role++ {
		workers.Add(1)
		go func(role int) {
			defer workers.Done()
			for i := 0; i < iterations; i++ {
				switch role % 3 {
				case 0:
					candidate := first
					if i%2 == 0 {
						candidate = second
					}
					if err := g.Update(candidate); err != nil {
						t.Error(err)
					}
				case 1:
					g.TryAdmit(0, 0, time.Now())
				case 2:
					snapshot := g.Snapshot(0, 0, time.Now())
					if snapshot.Policy != first && snapshot.Policy != second {
						t.Errorf("观察到混合策略：%+v", snapshot.Policy)
					}
					if snapshot.Tokens < 0 || snapshot.Tokens > float64(snapshot.Policy.BurstCalls) {
						t.Errorf("令牌余额越界：%+v", snapshot)
					}
				}
			}
		}(role)
	}
	workers.Wait()
	if snapshot := g.Snapshot(0, 0, time.Now()); snapshot.Accepted+snapshot.Rejected != 2*iterations {
		t.Fatalf("并发检查统计丢失：%+v", snapshot)
	}
}

// guardRequest 生成语法完整的本地 SIP 请求，便于直接调用会话处理路径。
func guardRequest(t *testing.T, method, to string, cseq uint32) *sip.Message {
	t.Helper()
	m, err := sip.Parse(sip.Request(method, "sip:1000@127.0.0.1", "127.0.0.1:5062", "z9hG4bK-guard", "<sip:alice@127.0.0.1>;tag=alice", to, "guard-test", cseq, nil))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestGuardRetransmissionsDoNotCharge 验证进行中 INVITE 重传及最终响应缓存均在保护器之前返回。
func TestGuardRetransmissionsDoNotCharge(t *testing.T) {
	g, now := newTestGuard(t)
	g.TryAdmit(0, 0, now)
	m := guardRequest(t, "INVITE", "<sip:1000@127.0.0.1>", 1)
	source := netip.MustParseAddrPort("127.0.0.1:5062")
	c := &Call{ID: 1, AID: m.CallID(), ARequest: m, ASource: source}
	s := &Server{guard: g, calls: map[uint64]*Call{1: c}, byDialog: map[string]uint64{m.CallID(): 1}, cache: make(map[string]cacheEntry)}
	before := g.Snapshot(1, 0, now)
	for i := 0; i < 100; i++ {
		s.handle(datagram{m, source})
	}
	// 空响应只用于避免引入传输依赖；若错误进入新呼叫路径，未装配的媒体组件会使测试失败。
	s.cache[m.Key()+"|"+source.String()] = cacheEntry{source: source}
	delete(s.byDialog, m.CallID())
	for i := 0; i < 100; i++ {
		s.handle(datagram{m, source})
	}
	if after := g.Snapshot(1, 0, now); after != before {
		t.Fatalf("重传重复检查或消耗了令牌：%+v", after)
	}
}

// guardUDPServer 创建本机 UDP 收发端与临时日志，覆盖真正响应报文及已有呼叫的清理动作。
func guardUDPServer(t *testing.T) (*Server, *net.UDPConn, netip.AddrPort) {
	t.Helper()
	listen := func() *net.UDPConn {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	sender, receiver := listen(), listen()
	log, err := journal.Open(t.TempDir()+"/events", 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	g, _ := newTestGuard(t)
	source := receiver.LocalAddr().(*net.UDPAddr).AddrPort()
	s := &Server{
		guard: g, conn: sender, journal: log,
		Config: config.Config{Limits: g.hard, SIP: config.SIP{Advertise: sender.LocalAddr().String(), Upstream: source.String(), AckTimeoutMS: 1000, MaxCallSeconds: 60}},
		calls:  make(map[uint64]*Call), byDialog: make(map[string]uint64), transactions: make(map[string]*transaction), cache: make(map[string]cacheEntry), workerLoads: []int{0},
	}
	return s, receiver, source
}

// readGuardPacket 以有限等待读取一条 SIP 报文，失败时明确报告传输错误。
func readGuardPacket(t *testing.T, receiver *net.UDPConn) *sip.Message {
	t.Helper()
	if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, sip.MaxMessageSize)
	n, _, err := receiver.ReadFromUDPAddrPort(packet)
	if err != nil {
		t.Fatal(err)
	}
	m, err := sip.Parse(packet[:n])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestGuardRetryAfterAndRejectedRetransmission 确认过载响应携带重试间隔，并完整重放同一拒绝事务。
func TestGuardRetryAfterAndRejectedRetransmission(t *testing.T) {
	s, receiver, source := guardUDPServer(t)
	m := guardRequest(t, "INVITE", "<sip:1000@127.0.0.1>", 1)
	s.rejectAdmission(datagram{m, source}, "Peak Protection", 7)
	first := readGuardPacket(t, receiver)
	s.handle(datagram{m, source})
	second := readGuardPacket(t, receiver)
	if first.Status != 503 || first.Header("retry-after") != "7" || second.Header("retry-after") != "7" || second.Header("to") != first.Header("to") || s.Stats.Rejected.Load() != 1 {
		t.Fatalf("过载响应或拒绝事务重放错误：first=%+v second=%+v", first, second)
	}
}

// TestGuardRetryAfterBeforeTokenCheck 覆盖硬容量和无媒体分片分支，保证提前拒绝仍携带当前重试建议。
func TestGuardRetryAfterBeforeTokenCheck(t *testing.T) {
	for _, hardFull := range []bool{false, true} {
		t.Run(map[bool]string{false: "没有可接入媒体", true: "达到启动硬容量"}[hardFull], func(t *testing.T) {
			s, receiver, source := guardUDPServer(t)
			p := s.guard.policy
			p.RetryAfterSeconds = 9
			if err := s.guard.Update(p); err != nil {
				t.Fatal(err)
			}
			s.Config.Media = config.Media{BindIP: "127.0.0.1", AdvertiseIP: "127.0.0.1", PortStart: 20000, PortEnd: 21999, AllowedRemoteNetworks: []string{"127.0.0.0/8"}}
			s.pool = &media.Pool{}
			if hardFull {
				s.Stats.Active.Store(uint64(s.Config.Limits.MaxCalls))
			}
			body := sip.RenderSDP(1, "127.0.0.1", 30000, 30001, sip.SDP{Payload: 0, PTime: 20})
			m, err := sip.Parse(sip.Request("INVITE", "sip:1000@127.0.0.1", source.String(), "z9hG4bK-guard", "<sip:alice@127.0.0.1>;tag=alice", "<sip:1000@127.0.0.1>", "guard-hard-limit", 1, body))
			if err != nil {
				t.Fatal(err)
			}
			s.handle(datagram{m, source})
			if response := readGuardPacket(t, receiver); response.Status != 503 || response.Header("retry-after") != "9" {
				t.Fatalf("提前过载拒绝缺少当前重试策略：%+v", response)
			}
			if snapshot := s.guard.Snapshot(s.Stats.Active.Load(), 0, time.Now()); snapshot.Accepted != 0 || snapshot.Rejected != 0 {
				t.Fatal("前置拒绝还进入了令牌检查")
			}
		})
	}
}

// TestGuardReadinessTracksAdmission 验证健康检查保持存活含义，就绪检查则反映当前新准入上限。
func TestGuardReadinessTracksAdmission(t *testing.T) {
	g, _ := newTestGuard(t)
	log, err := journal.Open(t.TempDir()+"/events", 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	worker := &media.Worker{}
	worker.Healthy.Store(true)
	s := &Server{guard: g, journal: log, pool: &media.Pool{Workers: []*media.Worker{worker}}}
	for _, active := range []uint64{7999, 8000} {
		s.Stats.Active.Store(active)
		s.Stats.Established.Store(active)
		response := httptest.NewRecorder()
		s.ready(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		want := http.StatusOK
		if active == 8000 {
			want = http.StatusServiceUnavailable
		}
		if response.Code != want {
			t.Fatalf("活跃 %d 的就绪状态为 %d，希望 %d", active, response.Code, want)
		}
	}
	response := httptest.NewRecorder()
	s.health(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"0.3.0"`) {
		t.Fatalf("达到准入阈值错误改变存活检查或版本：%s", response.Body.String())
	}
}

// TestGuardLoweringLimitsPreservesAnswerACKAndBYE 确认降限只拒绝新呼叫，存量 200、ACK 和 BYE 继续处理。
func TestGuardLoweringLimitsPreservesAnswerACKAndBYE(t *testing.T) {
	s, receiver, source := guardUDPServer(t)
	m := guardRequest(t, "INVITE", "<sip:1000@127.0.0.1>", 1)
	c := &Call{ID: 1, AID: m.CallID(), ARequest: m, ASource: source, ATag: "server", AInviteCSeq: 1, ARemoteCSeq: 1, Reserved: true}
	s.calls[1], s.byDialog[c.AID], s.workerLoads[0] = c, 1, 2
	s.Stats.Active.Store(2)
	p := s.guard.policy
	p.MaxActiveCalls, p.MaxEstablishingCalls = 1, 1
	if err := s.guard.Update(p); err != nil {
		t.Fatal(err)
	}
	if s.guard.TryAdmit(2, 0, time.Now()).Allowed || c.Ended || s.Stats.Active.Load() != 2 {
		t.Fatal("降低限制终止了现有呼叫或仍放行新呼叫")
	}
	s.answerA(c, 200, "OK", nil)
	if response := readGuardPacket(t, receiver); response.Status != 200 {
		t.Fatal("降限阻止了已接纳呼叫的成功应答")
	}
	ack := guardRequest(t, "ACK", "<sip:1000@127.0.0.1>;tag=server", 1)
	s.handle(datagram{ack, source})
	if !c.Established || c.Ended || s.Stats.Established.Load() != 1 {
		t.Fatal("降限阻止了 ACK 确认")
	}
	bye := guardRequest(t, "BYE", "<sip:1000@127.0.0.1>;tag=server", 2)
	s.handle(datagram{bye, source})
	if response := readGuardPacket(t, receiver); response.Status != 200 || !c.Ended || s.Stats.Active.Load() != 1 || s.Stats.Established.Load() != 0 {
		t.Fatal("降限阻止了 BYE 或资源释放")
	}
	if snapshot := s.guard.Snapshot(1, 0, time.Now()); snapshot.Accepted != 0 || snapshot.Rejected != 1 {
		t.Fatalf("已有会话消息进入了新呼叫保护器：%+v", snapshot)
	}
}

// TestWorkerFailureCancelsEarlyLeg 覆盖已有临时响应和等待临时响应两种故障时刻，确保 B 腿取消。
func TestWorkerFailureCancelsEarlyLeg(t *testing.T) {
	for _, provisional := range []bool{false, true} {
		t.Run(map[bool]string{false: "等待临时响应", true: "已经振铃"}[provisional], func(t *testing.T) {
			s, receiver, source := guardUDPServer(t)
			m := guardRequest(t, "INVITE", "<sip:1000@127.0.0.1>", 1)
			c := &Call{ID: 1, AID: m.CallID(), BID: "upstream-test", ARequest: m, ASource: source, ATag: "server", Worker: 0, Generation: 9, Reserved: true, BInviteSent: true, BProvisional: provisional, BURI: "sip:1000@127.0.0.1", BFrom: "<sip:rustswitch@127.0.0.1>;tag=server", InviteBranch: "z9hG4bK-upstream"}
			s.calls[1], s.workerLoads[0] = c, 1
			s.Stats.Active.Store(1)
			s.workerFailed(media.Failure{Worker: 0, Generation: 9})
			if response := readGuardPacket(t, receiver); response.Status != 503 {
				t.Fatal("媒体失败未通知 A 腿")
			}
			if !c.CancelWanted || !c.Ended || c.BByeSent || s.Stats.Active.Load() != 0 {
				t.Fatal("媒体失败未记录早期腿取消或未释放预留")
			}
			if !provisional {
				if c.BCancelSent {
					t.Fatal("尚无临时响应时提前发送了 CANCEL")
				}
				c.BProvisional = true
				s.sendCancelB(c)
			}
			cancel := readGuardPacket(t, receiver)
			if cancel.Method != "CANCEL" || cancel.CallID() != c.BID || cancel.Branch() != c.InviteBranch || !c.BCancelSent {
				t.Fatalf("早期 B 腿未收到匹配事务的 CANCEL：%+v", cancel)
			}
		})
	}
}
