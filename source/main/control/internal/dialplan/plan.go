// Package dialplan 读取有限 FreeSWITCH XML 拨号计划；不运行预处理、脚本或未知节点。
package dialplan

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const MaxFileBytes = 256 * 1024

var captureExpansion = regexp.MustCompile(`\$[0-9]`)

// Action 是启动时验证的单个有序应用，数据不做变量或正则捕获展开。
type Action struct{ Application, Data string }

// Extension 的已编译 RE2 与动作切片只读共享，不为每通呼叫重新编译表达式。
type Extension struct {
	Name    string
	Actions []Action
	pattern *regexp.Regexp
}

// Plan 按 XML 声明顺序查找指定 context 的第一个命中 extension。
type Plan struct{ contexts map[string][]*Extension }

func (p *Plan) HasContext(name string) bool { _, ok := p.contexts[name]; return ok }
func (p *Plan) Match(context, number string) *Extension {
	if p == nil || len(number) > 64 {
		return nil
	}
	for _, e := range p.contexts[context] {
		if e.pattern.MatchString(number) {
			return e
		}
	}
	return nil
}
func (p *Plan) Visit(fn func(Action) error) error {
	for _, extensions := range p.contexts {
		for _, e := range extensions {
			for _, a := range e.Actions {
				if err := fn(a); err != nil {
					return fmt.Errorf("extension %s: %w", e.Name, err)
				}
			}
		}
	}
	return nil
}

// Load 只读取普通文件的一次有界快照；拒绝最终软链接，文件变更需显式重启重新载入。
func Load(path string) (*Plan, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("dialplan must be a regular file, not a symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, actual) || !actual.Mode().IsRegular() {
		return nil, errors.New("dialplan file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

type node struct {
	name     string
	attrs    map[string]string
	children []*node
}

// Parse 限制文件、节点、深度、属性及表达式；DTD、实体声明和指令不会进入运行时。
func Parse(data []byte) (*Plan, error) {
	if len(data) == 0 || len(data) > MaxFileBytes {
		return nil, errors.New("dialplan size must be 1..256 KiB")
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root *node
	var stack []*node
	nodes := 0
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			nodes++
			if nodes > 4096 || len(stack) >= 8 || len(t.Attr) > 8 || t.Name.Space != "" {
				return nil, errors.New("dialplan XML budget or namespace unsupported")
			}
			n := &node{name: t.Name.Local, attrs: make(map[string]string)}
			for _, a := range t.Attr {
				if a.Name.Space != "" {
					return nil, errors.New("namespaced attributes unsupported")
				}
				if _, ok := n.attrs[a.Name.Local]; ok {
					return nil, errors.New("duplicate dialplan attribute")
				}
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("dialplan requires one root")
				}
				root = n
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("dialplan text outside attributes unsupported")
			}
		case xml.Comment:
		case xml.ProcInst:
			if t.Target != "xml" || root != nil {
				return nil, errors.New("dialplan processing instructions unsupported")
			}
		default:
			return nil, errors.New("dialplan directives and external includes unsupported")
		}
	}
	if root == nil {
		return nil, errors.New("dialplan is empty")
	}
	contexts := root.children
	switch root.name {
	case "document":
		if err := attributes(root, map[string]bool{"type": true}); err != nil {
			return nil, err
		}
		if root.attrs["type"] != "freeswitch/xml" || len(root.children) != 1 {
			return nil, errors.New("document requires exactly one dialplan section")
		}
		section := root.children[0]
		if section.name != "section" || section.attrs["name"] != "dialplan" {
			return nil, errors.New("only dialplan section supported")
		}
		if err := attributes(section, map[string]bool{"name": true}); err != nil {
			return nil, err
		}
		contexts = section.children
	case "include":
		if err := attributes(root, nil); err != nil {
			return nil, err
		}
	case "context":
		contexts = []*node{root}
	default:
		return nil, errors.New("dialplan root must be document, include or context")
	}
	if len(contexts) == 0 || len(contexts) > 16 {
		return nil, errors.New("dialplan requires 1..16 contexts")
	}
	p := &Plan{contexts: make(map[string][]*Extension)}
	extensions, actions := 0, 0
	for _, c := range contexts {
		if c.name != "context" || !validName(c.attrs["name"]) {
			return nil, errors.New("invalid context")
		}
		if err := attributes(c, map[string]bool{"name": true}); err != nil {
			return nil, err
		}
		name := c.attrs["name"]
		if _, ok := p.contexts[name]; ok {
			return nil, errors.New("duplicate context")
		}
		p.contexts[name] = nil
		names := make(map[string]bool)
		for _, e := range c.children {
			extensions++
			if extensions > 256 || e.name != "extension" || !validName(e.attrs["name"]) || names[e.attrs["name"]] || len(e.children) != 1 {
				return nil, errors.New("requires unique extensions with one condition, at most 256")
			}
			names[e.attrs["name"]] = true
			if err := attributes(e, map[string]bool{"name": true}); err != nil {
				return nil, err
			}
			condition := e.children[0]
			if condition.name != "condition" || condition.attrs["field"] != "destination_number" {
				return nil, errors.New("only destination_number condition supported")
			}
			if err := attributes(condition, map[string]bool{"field": true, "expression": true}); err != nil {
				return nil, err
			}
			expression := condition.attrs["expression"]
			if len(expression) == 0 || len(expression) > 512 {
				return nil, errors.New("expression must be 1..512 bytes")
			}
			pattern, err := regexp.Compile(expression)
			if err != nil {
				return nil, fmt.Errorf("RE2 expression: %w", err)
			}
			if len(condition.children) == 0 || len(condition.children) > 64 {
				return nil, errors.New("extension requires 1..64 actions")
			}
			entry := &Extension{Name: e.attrs["name"], pattern: pattern}
			for _, a := range condition.children {
				actions++
				if actions > 1024 || a.name != "action" || len(a.children) > 0 {
					return nil, errors.New("only up to 1024 leaf actions supported")
				}
				if err := attributes(a, map[string]bool{"application": true, "data": true}); err != nil {
					return nil, err
				}
				app, arg := a.attrs["application"], a.attrs["data"]
				switch app {
				case "answer", "park", "hangup", "sleep", "set", "unset", "read", "playback":
				default:
					return nil, fmt.Errorf("unsupported dialplan application %q", app)
				}
				if len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") || strings.Contains(arg, "${") || strings.Contains(arg, "$${") || captureExpansion.MatchString(arg) {
					return nil, errors.New("action expansion or oversized data unsupported")
				}
				entry.Actions = append(entry.Actions, Action{app, arg})
			}
			if entry.Actions[0].Application != "answer" || entry.Actions[0].Data != "" {
				return nil, errors.New("local dialplan must begin with answer without flags")
			}
			p.contexts[name] = append(p.contexts[name], entry)
		}
	}
	return p, nil
}
func validName(v string) bool {
	return len(v) > 0 && len(v) <= 64 && !strings.ContainsAny(v, "\x00\r\n\t /\\")
}
func attributes(n *node, allowed map[string]bool) error {
	for name := range n.attrs {
		if !allowed[name] {
			return fmt.Errorf("unsupported %s attribute %s", n.name, name)
		}
	}
	return nil
}
