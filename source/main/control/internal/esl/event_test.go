package esl

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestXMLEventUnicodeHeaderNames 从实际序列化正文读回中文及组合字符头名，防止合法变量使整份快照失败。
func TestXMLEventUnicodeHeaderNames(t *testing.T) {
	headers := map[string]string{"variable_客户": "张三<&>", "客户": "中文", "variable_é": "预组合", "variable_e\u0301": "组合"}
	frame, err := serializeEvent(event{headers: headers}, "xml")
	if err != nil {
		t.Fatal(err)
	}
	// 使用任意标签匹配保持原始名称，不能将 Unicode 名字归一化或替换为 ASCII。
	var decoded struct {
		Headers struct {
			Entries []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"headers"`
	}
	if err = xml.Unmarshal(frame.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Headers.Entries) != len(headers) {
		t.Fatalf("header count changed: %s", frame.Body)
	}
	for _, entry := range decoded.Headers.Entries {
		value, found := headers[entry.XMLName.Local]
		if !found || entry.XMLName.Space != "" || entry.Value != encodeEventValue(value) {
			t.Fatalf("header name or value changed: %#v", entry)
		}
	}
	if frame.Get("Content-Length") != strconv.Itoa(len(frame.Body)) {
		t.Fatal("UTF-8 body length changed")
	}
}

// TestXMLEventRejectsIllegalHeaderNames 保持空名、注入字符、命名空间前缀和非法 UTF-8 明确失败。
func TestXMLEventRejectsIllegalHeaderNames(t *testing.T) {
	for _, name := range []string{"", "1bad", "-bad", ".bad", "\u0301bad", "bad:name", "xmlns:x", "bad name", "bad\tname", "bad\nname", "bad\rname", "bad\x00name", "bad<name", "bad>name", "bad/name", "bad&name", "bad\"name", "bad'name", "bad\xffname", "bad\xed\xa0\x80"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			if _, err := serializeEvent(event{headers: map[string]string{name: "value"}}, "xml"); err == nil {
				t.Fatal("illegal XML header accepted")
			}
		})
	}
}

// TestXMLHeaderNameBoundaries 固定核对规范区间边界；第五版新名称不依赖 Go 解码器的旧版字符表。
func TestXMLHeaderNameBoundaries(t *testing.T) {
	for _, character := range []rune{'_', 'A', 'Z', 'a', 'z', 0xC0, 0xD6, 0xD8, 0xF6, 0xF8, 0x2FF, 0x370, 0x37D, 0x37F, 0x1FFF, 0x200C, 0x200D, 0x2070, 0x218F, 0x2C00, 0x2FEF, 0x3001, 0xD7FF, 0xF900, 0xFDCF, 0xFDF0, 0xFFFD, 0x10000, 0xEFFFF} {
		if !validXMLHeaderName(string(character) + "名-1.") {
			t.Errorf("valid name start U+%04X rejected", character)
		}
	}
	for _, character := range []rune{':', 0xB7, 0xD7, 0xF7, 0x300, 0x36F, 0x37E, 0x200B, 0x200E, 0x206F, 0x2190, 0x2BFF, 0x2FF0, 0x3000, 0xF8FF, 0xFDD0, 0xFDEF, 0xFFFE, 0xFFFF, 0xF0000, 0x10FFFF} {
		if validXMLHeaderName(string(character) + "name") {
			t.Errorf("invalid name start U+%04X accepted", character)
		}
	}
	for _, character := range []rune{'-', '.', '0', '9', 0xB7, 0x300, 0x36F, 0x203F, 0x2040} {
		if !validXMLHeaderName("客户"+string(character)) || validXMLHeaderName(string(character)+"客户") {
			t.Errorf("name continuation U+%04X has wrong position rules", character)
		}
	}
}

// TestEventEncodingUsesFreeSwitchEscaping 保留原安全标点及已有大写转义，避免中文/URI 被二次编码。
func TestEventEncodingUsesFreeSwitchEscaping(t *testing.T) {
	for input, want := range map[string]string{
		"中文 /a,b!()'*-_.~": "%E4%B8%AD%E6%96%87%20/a,b!()'*-_.~",
		"a+b&c=d":          "a%2Bb%26c%3Dd", "%20 %2f %GG": "%20%20%252f%20%25GG",
	} {
		if got := encodeEventValue(input); got != want {
			t.Fatalf("%q => %q want %q", input, got, want)
		}
	}
}

// TestXMLEventBodyAndHeaderBytes 核对与原版相同 XML 层级和两种转义，防止把头的编码方式用于正文。
func TestXMLEventBodyAndHeaderBytes(t *testing.T) {
	e := event{name: "BACKGROUND_JOB", headers: map[string]string{"Event-Name": "BACKGROUND_JOB", "Job-Command-Arg": "a+b 中文"}, body: []byte("中文<&>\n")}
	f, err := serializeEvent(e, "xml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Get("Content-Type") != "text/event-xml" || f.Get("Content-Length") != strconv.Itoa(len(f.Body)) {
		t.Fatal("outer byte count wrong")
	}
	var value struct {
		Headers struct {
			Name string `xml:"Event-Name"`
			Args string `xml:"Job-Command-Arg"`
		} `xml:"headers"`
		Length string `xml:"Content-Length"`
		Body   string `xml:"body"`
	}
	if err := xml.Unmarshal(f.Body, &value); err != nil {
		t.Fatal(err)
	}
	args, _ := url.QueryUnescape(value.Headers.Args)
	if value.Headers.Name != "BACKGROUND_JOB" || args != "a+b 中文" || value.Body != string(e.body) || value.Length != strconv.Itoa(len(e.body)) {
		t.Fatalf("incorrect XML event: %s", f.Body)
	}
	if _, err := serializeEvent(event{headers: map[string]string{"bad:name": "x"}}, "xml"); err == nil {
		t.Fatal("unsafe XML header allowed")
	}
}

// TestJSONEventIncludesOriginalBodyLength JSON 字段同样携带原始正文长度，空正文与没有正文不能混淆。
func TestJSONEventIncludesOriginalBodyLength(t *testing.T) {
	for _, body := range [][]byte{nil, {}, []byte("中文")} {
		f, err := serializeEvent(event{headers: map[string]string{"Event-Name": "BACKGROUND_JOB"}, body: body}, "json")
		if err != nil {
			t.Fatal(err)
		}
		var h map[string]string
		if err := json.Unmarshal(f.Body, &h); err != nil {
			t.Fatal(err)
		}
		_, present := h["_body"]
		if present != (body != nil) || (present && h["Content-Length"] != strconv.Itoa(len(body))) {
			t.Fatalf("body presence/length wrong: %s", f.Body)
		}
	}
}

// TestActualBackgroundXMLSubscription XML 订阅需要真实作业完成事件，不能只回复订阅成功。
func TestActualBackgroundXMLSubscription(t *testing.T) {
	s := startTestServer(t, nil)
	p := dialPeer(t, s, true)
	p.send(t, "event XML BACKGROUND_JOB\n\n")
	if p.read(t).Get("Reply-Text") != "+OK event listener enabled xml" {
		t.Fatal("XML subscription denied")
	}
	p.send(t, "bgapi echo 中文<&>\nJob-UUID: fixture-xml\n\n")
	if p.read(t).Get("Job-UUID") != "fixture-xml" {
		t.Fatal("job ack lost")
	}
	f := p.read(t)
	if f.Get("Content-Type") != "text/event-xml" || !strings.Contains(string(f.Body), "<Job-UUID>fixture-xml</Job-UUID>") {
		t.Fatalf("missing actual job: %s", f.Body)
	}
	if strings.Contains(string(f.Body), "Event-UUID") {
		t.Fatal("ordinary background event gained non-original UUID field")
	}
}

// TestEmptyBackgroundBodyDifferentFromSyncReply 原版同步无输出回退错误，但后台完成事件保留实际空正文。
func TestEmptyBackgroundBodyDifferentFromSyncReply(t *testing.T) {
	s := startTestServer(t, nil)
	p := dialPeer(t, s, true)
	p.send(t, "api echo \n\nevent json BACKGROUND_JOB\n\n")
	if string(p.read(t).Body) != "-ERR no reply\n" {
		t.Fatal("sync empty reply changed")
	}
	p.read(t)
	p.send(t, "bgapi echo \nJob-UUID: fixture-empty\n\n")
	if p.read(t).Get("Job-UUID") != "fixture-empty" {
		t.Fatal("missing background ack")
	}
	f := p.read(t)
	var value map[string]string
	if err := json.Unmarshal(f.Body, &value); err != nil {
		t.Fatal(err)
	}
	if body, present := value["_body"]; !present || body != "" || value["Content-Length"] != "0" {
		t.Fatalf("empty body fabricated: %s", f.Body)
	}
}

// TestBackgroundKeepsOriginalArguments 原版真实边界显示 Job-Command-Arg 保留空白，而 echo 结果经过裁剪。
func TestBackgroundKeepsOriginalArguments(t *testing.T) {
	s := startTestServer(t, nil)
	p := dialPeer(t, s, true)
	p.send(t, "event json BACKGROUND_JOB\n\n")
	p.read(t)
	p.send(t, "bgapi echo   a  \nJob-UUID: original-arguments\n\n")
	p.read(t)
	var value map[string]string
	if err := json.Unmarshal(p.read(t).Body, &value); err != nil {
		t.Fatal(err)
	}
	if value["Job-Command-Arg"] != "  a  " || value["_body"] != "a" || value["Content-Length"] != "1" {
		t.Fatalf("original arguments lost: %#v", value)
	}
}
