package esl

import (
	"bytes"
	"encoding/xml"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// encodeEventValue 对齐固定原版 switch_url_encode：保留安全标点以及现有大写 %HH，
// 空格编码为 %20。它与 application/x-www-form-urlencoded 的加号规则不同。
func encodeEventValue(value string) string {
	const unsafe = "\r\n #%&+:;<=>?@[\\]^`{|}\""
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		alreadyEncoded := b == '%' && i+2 < len(value) && strings.ContainsRune(hex, rune(value[i+1])) && strings.ContainsRune(hex, rune(value[i+2]))
		if !alreadyEncoded && (b < ' ' || b > '~' || strings.ContainsRune(unsafe, rune(b))) {
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&15])
		} else {
			out.WriteByte(b)
		}
	}
	return out.String()
}

// serializeXMLEvent 对齐原版 event/headers 和根级 Content-Length/body 结构；
// 头值百分号编码，正文仅作 XML 转义，长度仍表示转义前 UTF-8 字节数。
// 当前业务事件头只支持单值，原事件数组、重复 XML 标签等语义另列未兼容。
func serializeXMLEvent(e event) ([]byte, error) {
	var body bytes.Buffer
	encoder := xml.NewEncoder(&body)
	encoder.Indent("", "  ")
	element := func(name, value string) error {
		// Go 的 XML Encoder 不负责验证调用方提供的动态标签，必须先限制合法名称。
		if !validXMLHeaderName(name) {
			return errors.New("invalid XML event header")
		}
		return encoder.EncodeElement(value, xml.StartElement{Name: xml.Name{Local: name}})
	}
	root := xml.StartElement{Name: xml.Name{Local: "event"}}
	headers := xml.StartElement{Name: xml.Name{Local: "headers"}}
	if err := encoder.EncodeToken(root); err != nil {
		return nil, err
	}
	if err := encoder.EncodeToken(headers); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(e.headers))
	for key := range e.headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := element(key, encodeEventValue(e.headers[key])); err != nil {
			return nil, err
		}
	}
	if err := encoder.EncodeToken(headers.End()); err != nil {
		return nil, err
	}
	if len(e.body) > 0 {
		if err := element("Content-Length", strconv.Itoa(len(e.body))); err != nil {
			return nil, err
		}
		if err := element("body", string(e.body)); err != nil {
			return nil, err
		}
	}
	if err := encoder.EncodeToken(root.End()); err != nil {
		return nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	body.WriteByte('\n')
	return body.Bytes(), nil
}

// validXMLHeaderName 按 XML 1.0 第五版 NameStartChar/NameChar 接受合法 Unicode 名称。
// 来源：https://www.w3.org/TR/xml/#NT-NameStartChar；本事件结构不声明命名空间，仍拒绝冒号。
// 先验证 UTF-8，避免非法字节被范围循环替换成合法的 U+FFFD 后误接受。
func validXMLHeaderName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for offset, character := range name {
		if xmlHeaderNameStart(character) {
			continue
		}
		if offset == 0 || !(character == '-' || character == '.' || character >= '0' && character <= '9' || character == 0xB7 || character >= 0x0300 && character <= 0x036F || character >= 0x203F && character <= 0x2040) {
			return false
		}
	}
	return true
}

// xmlHeaderNameStart 使用规范定义的固定区间，不依赖随 Go 版本变化的 Unicode 字母分类。
func xmlHeaderNameStart(character rune) bool {
	return character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
		character >= 0xC0 && character <= 0xD6 || character >= 0xD8 && character <= 0xF6 ||
		character >= 0xF8 && character <= 0x2FF || character >= 0x370 && character <= 0x37D ||
		character >= 0x37F && character <= 0x1FFF || character >= 0x200C && character <= 0x200D ||
		character >= 0x2070 && character <= 0x218F || character >= 0x2C00 && character <= 0x2FEF ||
		character >= 0x3001 && character <= 0xD7FF || character >= 0xF900 && character <= 0xFDCF ||
		character >= 0xFDF0 && character <= 0xFFFD || character >= 0x10000 && character <= 0xEFFFF
}
