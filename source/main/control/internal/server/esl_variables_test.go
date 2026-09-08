package server

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestChannelIdentityWithoutESL XML应用的内部身份不依赖外部事件订阅，挂机仍需彻底回收。
func TestChannelIdentityWithoutESL(t *testing.T) {
	for _, local := range []bool{false, true} {
		s := &Server{}
		c := &Call{Local: local}
		s.compatibilityEvent(c, "call_admitted", "")
		want := 2
		if local {
			want = 1
		}
		if len(s.compatChannels) != want || len(c.compatUUIDs[0]) != 36 {
			t.Fatal("missing internal identity")
		}
		a := c.compatUUIDs[0]
		s.compatibilityEvent(c, "call_answered", "")
		if c.compatUUIDs[0] != a {
			t.Fatal("identity changed")
		}
		c.Ended = true
		s.compatibilityEvent(c, "call_ended", "normal_hangup")
		if len(s.compatChannels) != 0 {
			t.Fatal("channel index leaked without ESL")
		}
	}
}

// TestVariableListSyntax 覆盖真实业务值中的分号、引号、空格和Unicode，防止分隔损坏。
func TestVariableListSyntax(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{"a=中文;b=x=y;c=", []string{"a=中文", "b=x=y", "c="}},
		{`^^|a=1|b='two|three'|c=a\|b`, []string{"a=1", "b=two|three", "c=a|b"}},
		{`a=' x;y ';b=x\;y;c=\'z\';d=one\s`, []string{"a= x;y ", "b=x;y", "c='z'", "d=one "}},
		{"  a=x  ; b='  y  '  ;", []string{"a=x", "b=  y  "}},
		{`a=\q;b=unpaired';;c=end`, []string{`a=\q`, "b=unpaired'", "", "c=end"}},
	} {
		got, ok := splitVariableAssignments(test.input)
		if !ok || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%q: %#v, want %#v", test.input, got, test.want)
		}
	}
}

// TestSetVariablesNativeSemantics 逐项读回验证实际修改及A/B腿隔离，不能只检查返回+OK。
func TestSetVariablesNativeSemantics(t *testing.T) {
	s := &Server{}
	c := &Call{}
	s.compatibilityEvent(c, "call_admitted", "")
	a, b := c.compatUUIDs[0], c.compatUUIDs[1]
	for _, step := range []struct {
		body, reply string
		values      map[string]string
	}{
		{"customer=中文;token=x=y;empty=;repeat=first;repeat=last", "+OK\n", map[string]string{"customer": "中文", "token": "x=y", "empty": "_undef_", "repeat": "last"}},
		{"customer=;one=same;legacy;first=;deleted", "+OK\n", map[string]string{"customer": "_undef_", "one": "same", "legacy": "_undef_", "deleted": "_undef_"}},
		{`quoted=' x;y ';escaped=x\;y`, "+OK\n", map[string]string{"quoted": " x;y ", "escaped": "x;y"}},
		{"=bad;after=yes", "-ERR No variable specified\n+OK\n", map[string]string{"after": "yes"}},
		{"^^|custom=one|second=two", "+OK\n", map[string]string{"custom": "one", "second": "two"}},
	} {
		if got := s.compatibilityChannelCommand("uuid_setvar_multi", a+" "+step.body); got != step.reply {
			t.Fatalf("%s: %q", step.body, got)
		}
		for key, want := range step.values {
			if got := s.compatibilityChannelCommand("uuid_getvar", a+" "+key); got != want {
				t.Fatalf("%s: %q != %q", key, got, want)
			}
			if got := s.compatibilityChannelCommand("uuid_getvar", b+" "+key); got != "_undef_" {
				t.Fatalf("other leg changed: %s", key)
			}
		}
	}
	for _, step := range [][2]string{{"", setVariablesUsage}, {a, ""}, {"missing a=b", "-ERR No such channel!\n" + setVariablesUsage}} {
		if got := s.compatibilitySetVariables(step[0]); got != step[1] {
			t.Fatalf("response: %q want %q", got, step[1])
		}
	}
}

// TestSetVariablesBoundsAndCancellation 超限列表在任何修改前拒绝；单项错误保留明确部分失败。
func TestSetVariablesBoundsAndCancellation(t *testing.T) {
	s := &Server{}
	c := &Call{}
	s.compatibilityEvent(c, "call_admitted", "")
	a := c.compatUUIDs[0]
	for _, body := range []string{strings.Repeat("a=b;", 65), "sentinel=" + strings.Repeat("x", 16384), "^^ sentinel=x b=y", "sentinel=x\x00"} {
		if got := s.compatibilitySetVariables(a + " " + body); !strings.HasPrefix(got, "-ERR") {
			t.Fatal("over-budget list accepted")
		}
		if len(c.compatVariables[0]) != 0 {
			t.Fatal("rejected list mutated variables")
		}
	}
	if got := s.compatibilitySetVariables(a + " uuid=forged"); strings.Contains(got, "+OK") {
		t.Fatal("protected write claimed success")
	}
	if got := s.compatibilitySetVariables(a + " unsupported=${missing}"); strings.Contains(got, "+OK") {
		t.Fatal("unsupported expansion claimed success")
	}
	for i := 0; i < 128; i++ {
		s.compatibilityChannelCommand("uuid_setvar", fmt.Sprintf("%s k%d value", a, i))
	}
	if got := s.compatibilitySetVariables(a + " k0=updated;overflow=bad"); got != "-ERR variable budget exceeded\n+OK\n" {
		t.Fatalf("partial failure lost: %q", got)
	}
	if s.compatibilityChannelCommand("uuid_getvar", a+" k0") != "updated" || s.compatibilityChannelCommand("uuid_getvar", a+" overflow") != "_undef_" {
		t.Fatal("budget results incorrect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.handleCompatibilityRequest(compatibilityRequest{ctx: ctx, command: "uuid_setvar_multi", arguments: a + " k0=cancelled", reply: make(chan string, 1)})
	if s.compatibilityChannelCommand("uuid_getvar", a+" k0") != "updated" {
		t.Fatal("cancelled request executed")
	}
}
