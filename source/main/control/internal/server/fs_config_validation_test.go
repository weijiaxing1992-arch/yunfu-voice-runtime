package server

import (
	"strings"
	"testing"
)

// TestFSRejectsDuplicateAttributes 验证重复属性不能从高级 XML 编辑器进入草稿。
// 标准库接受某些重复属性，但参数表的单值读取与按位置替换必须指向唯一属性。
func TestFSRejectsDuplicateAttributes(t *testing.T) {
	for _, raw := range []string{
		`<include><param name="rate" value="first" value="last"/></include>`,
		`<include><param name="first" name="last" value="value"/></include>`,
		`<include><param xmlns:x="urn:meta" xmlns:y="urn:meta" name="rate" x:value="first" y:value="last"/></include>`,
		`<include><param xmlns:x="urn:first" xmlns:x="urn:last" name="rate" value="value"/></include>`,
	} {
		if _, err := parseFSFile("review.xml", raw); err == nil || !strings.Contains(err.Error(), "重复属性") {
			t.Fatalf("重复属性未被明确拒绝：%s；错误：%v", raw, err)
		}
		if _, err := patchFSParameters(map[string]string{"review.xml": raw}, map[string]string{"review.xml#0": "changed"}); err == nil {
			t.Fatal("重复属性仍可通过参数更新路径保存")
		}
	}
}

// TestFSParameterPatchPreservesNamespacedMetadata 验证同名命名空间元数据不会覆盖真实字段或被错改。
// 两种属性排列顺序都应读取、替换唯一的无命名空间 value，而不是依赖 map 的最后赋值。
func TestFSParameterPatchPreservesNamespacedMetadata(t *testing.T) {
	for _, raw := range []string{
		`<include><param xmlns:meta="urn:meta" name="rate" value="old" meta:value="metadata" meta:name="other"/></include>`,
		`<include><param xmlns:meta="urn:meta" meta:value="metadata" meta:name="other" name="rate" value="old"/></include>`,
	} {
		fields, err := parseFSFile("review.xml", raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(fields) != 1 || fields[0].Name != "rate" || fields[0].Value != "old" {
			t.Fatalf("命名空间元数据污染了参数目录：%+v", fields)
		}
		updated, err := patchFSParameters(map[string]string{"review.xml": raw}, map[string]string{"review.xml#0": "new-value"})
		if err != nil {
			t.Fatal(err)
		}
		// 完整文本比较同时确认其他属性、前缀和排列顺序未变。
		want := strings.Replace(raw, ` value="old"`, ` value="new-value"`, 1)
		if updated["review.xml"] != want {
			t.Fatalf("修改了目标 value 之外的 XML：%s", updated["review.xml"])
		}
		after, err := parseFSFile("review.xml", updated["review.xml"])
		if err != nil || len(after) != 1 || after[0].Value != "new-value" {
			t.Fatalf("重新读取未得到已保存的新值：%+v；错误：%v", after, err)
		}
	}
}

// TestFSNamespacedOnlyFieldsStayInRawXML 验证第三方名称空间不会被当成可视化 FreeSWITCH 字段。
// 配置原文仍可保存/导出，只是不为这些具有不同语义的名称生成误导性表单项。
func TestFSNamespacedOnlyFieldsStayInRawXML(t *testing.T) {
	raw := `<include xmlns:meta="urn:meta"><param name="rate" meta:value="metadata"/><param meta:name="rate" value="metadata"/><meta:param name="rate" value="metadata"/></include>`
	fields, err := parseFSFile("review.xml", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 0 {
		t.Fatalf("命名空间字段被误当成 FreeSWITCH 属性：%+v", fields)
	}
}

// TestFSParameterPatchDoesNotMatchUnicodeNameSuffix 验证合法 Unicode 属性名的 ASCII 尾部不能成为替换目标。
func TestFSParameterPatchDoesNotMatchUnicodeNameSuffix(t *testing.T) {
	for _, raw := range []string{
		`<include><param name="rate" 原value="metadata" value="old"/></include>`,
		`<include><param name="rate" 原value="metadata value='bait'" value="old"/></include>`,
	} {
		updated, err := patchFSParameters(map[string]string{"review.xml": raw}, map[string]string{"review.xml#0": "new-value"})
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(raw, ` value="old"`, ` value="new-value"`, 1)
		if updated["review.xml"] != want {
			t.Fatalf("将 Unicode 属性名尾部或值内文本误认成 value：%s", updated["review.xml"])
		}
	}
}
