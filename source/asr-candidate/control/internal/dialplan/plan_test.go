package dialplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const simple = `<context name="default"><extension name="first"><condition field="destination_number" expression="^71[0-9]+$"><action application="answer"/><action application="set" data="customer=中文客户"/><action application="park"/></condition></extension></context>`

// TestPlanWrappersMatchFirstAndKeepOrder 真实XML常用包装都编译同一有序匹配，未命中不会伪造路线。
func TestPlanWrappersMatchFirstAndKeepOrder(t *testing.T) {
	for _, input := range []string{simple, "<include>" + simple + "</include>", `<document type="freeswitch/xml"><section name="dialplan">` + simple + `</section></document>`} {
		p, err := Parse([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		match := p.Match("default", "7101")
		if match == nil || len(match.Actions) != 3 || match.Actions[1].Data != "customer=中文客户" || p.Match("default", "8101") != nil || p.Match("missing", "7101") != nil {
			t.Fatal("match or order incorrect")
		}
	}
	duplicate := strings.Replace(simple, "</context>", strings.Replace(strings.TrimSuffix(strings.TrimPrefix(simple, `<context name="default">`), "</context>"), `name="first"`, `name="second"`, 1)+"</context>", 1)
	p, err := Parse([]byte(duplicate))
	if err != nil || p.Match("default", "7101").Name != "first" {
		t.Fatal("first matching extension was not selected", err)
	}
}

// TestPlanRejectsUnsupportedBranches 不支持分支必须在整文件载入时失败，不能只忽略未命中的坏分支。
func TestPlanRejectsUnsupportedBranches(t *testing.T) {
	for name, input := range map[string]string{
		"directive":      `<!DOCTYPE a [<!ENTITY z "secret">]>` + simple,
		"processor":      `<?danger execute?>` + simple,
		"nested":         strings.Replace(simple, `<action application="park"/>`, `<condition field="destination_number" expression=".*"/>`, 1),
		"antiaction":     strings.Replace(simple, `<action application="park"/>`, `<anti-action application="hangup"/>`, 1),
		"unknownapp":     strings.Replace(simple, `application="park"`, `application="system"`, 1),
		"conditionfield": strings.Replace(simple, `field="destination_number"`, `field="caller_id_number"`, 1),
		"inline":         strings.Replace(simple, `application="answer"`, `application="answer" inline="true"`, 1),
		"continue":       strings.Replace(simple, `name="first"`, `name="first" continue="true"`, 1),
		"flags":          strings.Replace(simple, `application="answer"`, `application="answer" data="is_conference"`, 1),
		"capture":        strings.Replace(simple, `customer=中文客户`, `customer=$1`, 1),
		"expansion":      strings.Replace(simple, `customer=中文客户`, `customer=${password}`, 1),
		"pcre":           strings.Replace(simple, `^71[0-9]+$`, `^(?=7)71$`, 1),
		"duplicate":      strings.Replace(simple, `name="default"`, `name="default" name="other"`, 1),
		"unknownattr":    strings.Replace(simple, `expression="^71[0-9]+$"`, `expression=".*" break="never"`, 1),
		"namespace":      strings.Replace(simple, `<context `, `<context xmlns="urn:unknown" `, 1),
		"text":           strings.Replace(simple, `<action application="park"/>`, `text<action application="park"/>`, 1),
		"unanswered":     strings.Replace(simple, `<action application="answer"/>`, "", 1),
		"oversize":       strings.Repeat(" ", MaxFileBytes+1),
		"rootsecond":     simple + simple,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(input)); err == nil {
				t.Fatal("unsupported XML accepted")
			}
		})
	}
	actions := `<action application="answer"/>` + strings.Repeat(`<action application="set" data="x=y"/>`, 64)
	tooMany := strings.Replace(simple, `<action application="answer"/><action application="set" data="customer=中文客户"/><action application="park"/>`, actions, 1)
	if _, err := Parse([]byte(tooMany)); err == nil {
		t.Fatal("action budget bypassed")
	}
}

// TestPlanFileSnapshotRefusesSymlinkAndOversize 文件装载限定普通文件，错误不降级成空计划。
func TestPlanFileSnapshotRefusesSymlinkAndOversize(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "dialplan.xml")
	if err := os.WriteFile(file, []byte(simple), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.xml")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.WriteFile(file, []byte(strings.Repeat(" ", MaxFileBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("oversize accepted")
	}
}
