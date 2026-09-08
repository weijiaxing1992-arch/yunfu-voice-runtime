package esl

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const executeTestUUID = "bc6877ec-cb82-4ebd-ad51-b297ea683fa6"

// TestSendmsgExecutesOnlyAuthenticatedValidatedRequests 验证真实 TCP 入站分派、UTF-8 正文与关联身份。
func TestSendmsgExecutesOnlyAuthenticatedValidatedRequests(t *testing.T) {
	requests := make(chan ExecuteRequest, 2)
	s := startTestServer(t, func(o *Options) {
		o.Execute = func(ctx context.Context, r ExecuteRequest) string { requests <- r; return "+OK" }
	})
	p := dialPeer(t, s, false)
	wire := "sendmsg " + executeTestUUID + "\ncall-command: execute\nexecute-app-name: set\nexecute-app-arg: customer=中文\nevent-uuid: " + executeTestUUID + "\nevent-lock: true\n\n"
	p.send(t, wire)
	if got := p.read(t).Get("Reply-Text"); got != "-ERR command not found" {
		t.Fatal(got)
	}
	if len(requests) != 0 {
		t.Fatal("unauthenticated application executed")
	}
	p.send(t, "auth test-only-secret\n\n")
	_ = p.read(t)
	body := "customer=正文中文"
	p.send(t, fmt.Sprintf("sendmsg\nsession-id: %s\ncall-command: execute\nexecute-app-name: set\nevent-uuid: %s\ncontent-type: text/plain\ncontent-length: %d\n\n%s", executeTestUUID, executeTestUUID, len(body), body))
	if got := p.read(t).Get("Reply-Text"); got != "+OK" {
		t.Fatal(got)
	}
	r := <-requests
	if r.UUID != executeTestUUID || r.EventUUID != executeTestUUID || r.Arguments != body || r.Application != "set" {
		t.Fatalf("bad actual dispatch: %#v", r)
	}
	p.send(t, wire)
	if p.read(t).Get("Reply-Text") != "+OK" {
		t.Fatal("header request rejected")
	}
	if !(<-requests).EventLock {
		t.Fatal("event-lock lost")
	}
}

// TestSendmsgUnsupportedControlsNeverReachBusiness 防止重复头、循环和未实现执行语义被静默忽略。
func TestSendmsgUnsupportedControlsNeverReachBusiness(t *testing.T) {
	var called atomic.Int32
	s := startTestServer(t, func(o *Options) {
		o.Execute = func(context.Context, ExecuteRequest) string { called.Add(1); return "+OK" }
	})
	p := dialPeer(t, s, true)
	base := "sendmsg " + executeTestUUID + "\ncall-command: execute\nexecute-app-name: set\nexecute-app-arg: x=y\n"
	for _, extra := range []string{"loops: -1\n", "hold-bleg: true\n", "execute-app-name: read\n", "session-id: different\n", "event-uuid: bad\n"} {
		p.send(t, base+extra+"\n")
		if !strings.HasPrefix(p.read(t).Get("Reply-Text"), "-ERR") {
			t.Fatal("unsupported request accepted", extra)
		}
	}
	if called.Load() != 0 {
		t.Fatal("invalid request produced business side effects")
	}
}

// TestSendmsgAdmissionTimeoutAndMissingHandlerAreFailures 受理超时不能制造成功，也不能阻塞整个连接永久。
func TestSendmsgAdmissionTimeoutAndMissingHandlerAreFailures(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprint(installed), func(t *testing.T) {
			s := startTestServer(t, func(o *Options) {
				o.CommandTimeout = 20 * time.Millisecond
				if installed {
					o.Execute = func(ctx context.Context, _ ExecuteRequest) string { <-ctx.Done(); return "-ERR operation cancelled" }
				}
			})
			p := dialPeer(t, s, true)
			p.send(t, "sendmsg "+executeTestUUID+"\ncall-command: execute\nexecute-app-name: read\nexecute-app-arg: 1 2 silence result\n\n")
			if !strings.HasPrefix(p.read(t).Get("Reply-Text"), "-ERR") {
				t.Fatal("missing or timed out executor accepted")
			}
			p.send(t, "api echo still-alive\n\n")
			if string(p.read(t).Body) != "still-alive" {
				t.Fatal("admission timeout stranded client")
			}
		})
	}
}
