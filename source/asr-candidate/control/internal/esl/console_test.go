package esl

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// TestFSCLIConsoleSingleCommand 原版 fs_cli -x 的原样头必须到达实际 API，而不是只返回协议层成功。
func TestFSCLIConsoleSingleCommand(t *testing.T) {
	var calls atomic.Int64
	s := startTestServer(t, func(o *Options) {
		o.API = func(_ context.Context, name, arguments string) string {
			calls.Add(1)
			switch name {
			case "echo":
				return arguments
			case "version":
				return "RustSwitch fixture version\n"
			case "show":
				if arguments != "api as json" {
					t.Errorf("show arguments lost: %q", arguments)
				}
				return `{"row_count":1,"rows":[{"name":"echo"}]}`
			}
			return "-ERR unexpected API\n"
		}
	})
	p := dialPeer(t, s, true)
	cases := []struct{ command, want string }{
		{"echo 中文☃", "中文☃"},
		{"version", "RustSwitch fixture version\n"},
		{"show api as json", `{"row_count":1,"rows":[{"name":"echo"}]}`},
		{"   echo   a;b  ", "a;b"},
	}
	for _, item := range cases {
		p.send(t, "api "+item.command+"\nconsole_execute: true\n\n")
		frame := p.read(t)
		if frame.Get("Content-Type") != "api/response" || string(frame.Body) != item.want {
			t.Fatalf("console API failed: %#v", frame)
		}
	}
	if calls.Load() != int64(len(cases)) {
		t.Fatal("console wrapper did not execute exactly once")
	}
}

// TestConsoleRejectsUnimplementedBatchBeforeSideEffects 未实现的批处理/别名不执行任何前置命令，且仍返回 api/response。
func TestConsoleRejectsUnimplementedBatchBeforeSideEffects(t *testing.T) {
	var calls atomic.Int64
	s := startTestServer(t, func(o *Options) {
		o.API = func(context.Context, string, string) string { calls.Add(1); return "MUTATED" }
	})
	p := dialPeer(t, s, true)
	for _, command := range []string{"echo a;;echo b", "alias add shortcut echo unsafe", "shortcut", "uuid_kill any;;echo after"} {
		p.send(t, "api "+command+"\nconsole_execute: true\n\n")
		frame := p.read(t)
		if frame.Get("Content-Type") != "api/response" || !strings.HasPrefix(string(frame.Body), "-ERR console_execute") {
			t.Fatalf("unsupported console was not explicit: %#v", frame)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported console sequence executed an API")
	}
}

// TestConsoleFalsePreservesLiteralAPIArguments false 必须走普通 API；不能因为头存在就误启用控制台拆分。
func TestConsoleFalsePreservesLiteralAPIArguments(t *testing.T) {
	s := startTestServer(t, nil)
	p := dialPeer(t, s, true)
	for _, flag := range []string{"false", "0", "no", "unknown", "0.5"} {
		p.send(t, "api echo a;;echo b\nconsole_execute: "+flag+"\n\n")
		if got := string(p.read(t).Body); got != "a;;echo b" {
			t.Fatalf("false flag %q changed literal body %q", flag, got)
		}
	}
	for _, flag := range []string{"Yes", "ON", "true", "T", "enabled", "active", "allow", "1", "-1", "2.5"} {
		p.send(t, "api    echo flag-ok\nconsole_execute: "+flag+"\n\n")
		if got := string(p.read(t).Body); got != "flag-ok" {
			t.Fatalf("true flag %q rejected: %q", flag, got)
		}
	}
}

// TestConsoleWrapperKeepsExistingAdmissionAndAuthentication 控制台包装不绕过认证，也不为未支持语法创建作业。
func TestConsoleWrapperKeepsExistingAdmissionAndAuthentication(t *testing.T) {
	var calls atomic.Int64
	s := startTestServer(t, func(o *Options) {
		o.API = func(context.Context, string, string) string { calls.Add(1); return "MUTATED" }
	})
	p := dialPeer(t, s, false)
	p.send(t, "api echo no\nconsole_execute: true\n\n")
	if p.read(t).Get("Reply-Text") != "-ERR command not found" {
		t.Fatal("console bypassed auth")
	}
	if calls.Load() != 0 {
		t.Fatal("unauthenticated wrapper executed")
	}
}
