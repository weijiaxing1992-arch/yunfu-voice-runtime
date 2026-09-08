package server

import (
	"fmt"
	"net"
	"net/netip"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/journal"
	"rustswitch/control/internal/sip"
	"testing"
)

// forkTestServer 不绑定任何端口，只检查分叉重传对状态机的影响；空 UDPConn 让发送尝试立即返回错误。
// SendErrors 在这些测试中只用于计数发送尝试，不作为真实线路已传输的证据。
func forkTestServer(t *testing.T, limit int) (*Server, *Call) {
	t.Helper()
	log, err := journal.Open(t.TempDir()+"/events", 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	c := &Call{ID: 1, AID: "caller", BID: "outgoing", BTag: "local-tag", BRemoteTag: "selected-tag", BAnswered: true, AStatus: 200, Established: true,
		BURI: "sip:callee@127.0.0.1:5070", BFrom: "<sip:caller@127.0.0.1:5068>;tag=local-tag", BTo: "<sip:callee@127.0.0.1:5070>;tag=selected-tag", InviteBranch: "z9hG4bK-original"}
	s := &Server{conn: &net.UDPConn{}, journal: log, calls: map[uint64]*Call{1: c}, byDialog: map[string]uint64{c.AID: 1, c.BID: 1}, transactions: make(map[string]*transaction),
		Config: config.Config{SIP: config.SIP{Upstream: "127.0.0.1:5070", Advertise: "127.0.0.1:5068"}, Limits: config.Limits{MaxTransactions: limit}}}
	s.Stats.Established.Store(1)
	return s, c
}

// forkFinal 构造同一出站 INVITE 下不同远端标签的 200；报文经过真实 SIP 解析器再进入控制状态机。
func forkFinal(t *testing.T, s *Server, c *Call, tag string) *sip.Message {
	t.Helper()
	invite, err := sip.Parse(sip.Request("INVITE", c.BURI, s.Config.SIP.Advertise, c.InviteBranch, c.BFrom, "<"+c.BURI+">", c.BID, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	response, err := sip.Parse(sip.Response(invite, 200, "OK", tag, "sip:callee@127.0.0.1:5070", s.Config.SIP.Upstream, nil))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// forkByeResponse 从实际已登记的清理请求生成最终响应，用于验证来源和事务关联检查。
func forkByeResponse(t *testing.T, s *Server, c *Call, tag string) *sip.Message {
	t.Helper()
	request, err := sip.Parse(c.ForkCleanups[tag].wire)
	if err != nil {
		t.Fatal(err)
	}
	response, err := sip.Parse(sip.Response(request, 200, "OK", tag, "", s.Config.SIP.Upstream, nil))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// TestForkFinalRetransmissionKeepsOneCleanupTransaction 复现原先 32 份同一分叉 200 产生 32 个 BYE/64 个重传定时器的问题。
func TestForkFinalRetransmissionKeepsOneCleanupTransaction(t *testing.T) {
	s, c := forkTestServer(t, 1024)
	response := forkFinal(t, s, c, "other-fork")
	source := netip.MustParseAddrPort(s.Config.SIP.Upstream)
	s.responseB(response, source)
	expires := s.timerIndex[timerKey{"fork_gc", c.ID, "other-fork"}].when
	key := c.ForkCleanups["other-fork"].key
	bye, err := sip.Parse(c.ForkCleanups["other-fork"].wire)
	if err != nil || bye.Header("from") != c.BFrom || sip.Tag(bye.Header("to")) != "other-fork" || sip.Tag(c.BTo) != "selected-tag" {
		t.Fatal("分支清理使用或修改了错误的From/To对话身份", err)
	}
	for range 31 {
		s.responseB(response, source)
	}
	if len(s.transactions) != 1 || len(c.ForkCleanups) != 1 || len(s.timers) != 3 || s.Stats.Timers.Load() != 3 {
		t.Fatalf("重传扩大资源：transactions=%d forks=%d timers=%d", len(s.transactions), len(c.ForkCleanups), len(s.timers))
	}
	if c.ForkCleanups["other-fork"].key != key || !s.timerIndex[timerKey{"fork_gc", c.ID, "other-fork"}].when.Equal(expires) {
		t.Fatal("重复200改变事务身份或延长去重期限")
	}
	if s.Stats.SendErrors.Load() != 33 {
		t.Fatalf("应尝试32份ACK及首份BYE，实际发送尝试=%d", s.Stats.SendErrors.Load())
	}
	if c.Ended || !c.Established || c.BRemoteTag != "selected-tag" || c.BByeSent || s.Stats.Established.Load() != 1 {
		t.Fatal("清理未选中分支损坏了主对话")
	}
}

// TestForkCleanupFinalResponseRequiresExactTransaction 检查伪来源/错误序号不能完成清理，正确响应后重传200只重发ACK。
func TestForkCleanupFinalResponseRequiresExactTransaction(t *testing.T) {
	s, c := forkTestServer(t, 1024)
	source := netip.MustParseAddrPort(s.Config.SIP.Upstream)
	final := forkFinal(t, s, c, "other-fork")
	s.responseB(final, source)
	response := forkByeResponse(t, s, c, "other-fork")
	s.responseB(response, netip.MustParseAddrPort("127.0.0.1:5071"))
	for _, change := range []sip.Header{{Name: "cseq", Value: "3 BYE"}, {Name: "call-id", Value: "unrelated-call"}, {Name: "from", Value: "<sip:caller@127.0.0.1:5068>;tag=unrelated-tag"}} {
		wrong := *response
		wrong.Headers = append([]sip.Header(nil), response.Headers...)
		for index := range wrong.Headers {
			if wrong.Headers[index].Name == change.Name {
				wrong.Headers[index].Value = change.Value
			}
		}
		s.responseB(&wrong, source)
	}
	if len(s.transactions) != 1 || c.ForkCleanups["other-fork"].completed {
		t.Fatal("错误来源或序号完成了分支清理")
	}
	s.responseB(response, source)
	if len(s.transactions) != 0 || len(s.timers) != 1 || s.Stats.Timers.Load() != 1 || !c.ForkCleanups["other-fork"].completed {
		t.Fatal("有效最终响应未撤销事务重传并保留短期去重标记")
	}
	before := s.Stats.SendErrors.Load()
	s.responseB(final, source)
	if len(s.transactions) != 0 || s.Stats.SendErrors.Load() != before+1 {
		t.Fatal("清理完成后的重复200又新建或发送BYE")
	}
}

// TestForkCleanupStateBoundAndStatelessOverflow 验证超过八个远端标签时明确降级，既不无限登记事务，也不挂断主对话。
func TestForkCleanupStateBoundAndStatelessOverflow(t *testing.T) {
	s, c := forkTestServer(t, 1024)
	source := netip.MustParseAddrPort(s.Config.SIP.Upstream)
	for index := 0; index < maxForkCleanups+40; index++ {
		s.responseB(forkFinal(t, s, c, fmt.Sprintf("fork-%d", index)), source)
	}
	if len(c.ForkCleanups) != maxForkCleanups || len(s.transactions) != maxForkCleanups || len(s.timers) != maxForkCleanups*3 || !c.ForkCleanupLimited {
		t.Fatalf("清理资源未限制：forks=%d tx=%d timers=%d limited=%v", len(c.ForkCleanups), len(s.transactions), len(s.timers), c.ForkCleanupLimited)
	}
	if c.Ended || !c.Established || c.BRemoteTag != "selected-tag" {
		t.Fatal("分支数量超限损坏了主对话")
	}
}

// TestForkCleanupGlobalTransactionPressureDoesNotAddRetryContext 全局事务额度不足时，重复200仅触发固定BYE无状态重发。
func TestForkCleanupGlobalTransactionPressureDoesNotAddRetryContext(t *testing.T) {
	s, c := forkTestServer(t, 1)
	source := netip.MustParseAddrPort(s.Config.SIP.Upstream)
	s.responseB(forkFinal(t, s, c, "first"), source)
	second := forkFinal(t, s, c, "second")
	s.responseB(second, source)
	key := c.ForkCleanups["second"].key
	version := s.nextVersion
	before := s.Stats.SendErrors.Load()
	for range 20 {
		s.responseB(second, source)
	}
	if len(s.transactions) != 1 || len(c.ForkCleanups) != 2 || len(s.timers) != 4 || c.ForkCleanups["second"].key != key || s.nextVersion != version {
		t.Fatal("全局额度不足后仍累积新重传或去重任务")
	}
	if s.Stats.SendErrors.Load() != before+40 {
		t.Fatal("无状态分支应随每份200重发一次ACK和同一BYE")
	}
}

// TestForkCleanupExpiresAndCallGCRemovesItsTimers 保证去重状态有固定生命期，通话对象回收时不留下无主的分支定时器。
func TestForkCleanupExpiresAndCallGCRemovesItsTimers(t *testing.T) {
	s, c := forkTestServer(t, 1024)
	source := netip.MustParseAddrPort(s.Config.SIP.Upstream)
	s.responseB(forkFinal(t, s, c, "expiring"), source)
	key := c.ForkCleanups["expiring"].key
	version := s.transactions[key].version
	// 模拟调度器先移除到期堆项，再执行对应动作，不等待真实三十二秒。
	s.cancelTimer("out_expire", c.ID, key)
	s.onTimer(timerItem{kind: "out_expire", id: c.ID, key: key, version: version})
	s.cancelTimer("fork_gc", c.ID, "expiring")
	s.onTimer(timerItem{kind: "fork_gc", id: c.ID, key: "expiring"})
	if len(c.ForkCleanups) != 0 || len(s.transactions) != 0 || len(s.timers) != 0 || s.Stats.Timers.Load() != 0 {
		t.Fatal("分支去重或事务到期后仍残留资源")
	}
	s.responseB(forkFinal(t, s, c, "late"), source)
	response := forkByeResponse(t, s, c, "late")
	c.Ended = true
	c.Established = false
	s.onTimer(timerItem{kind: "call_gc", id: c.ID})
	if len(s.calls) != 0 || len(s.byDialog) != 0 || len(c.ForkCleanups) != 0 || len(s.timers) != 2 || s.Stats.Timers.Load() != 2 {
		t.Fatal("通话回收未撤销其分支去重任务，或提前删除了在途BYE")
	}
	s.responseB(response, source)
	if len(s.transactions) != 0 || len(s.timers) != 0 || s.Stats.Timers.Load() != 0 {
		t.Fatal("通话回收之后匹配的BYE最终响应无法清理事务")
	}
}
