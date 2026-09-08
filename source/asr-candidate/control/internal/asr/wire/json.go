package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"unicode/utf8"
)

type validator interface{ Validate() error }

func jsonType(kind Kind, v any) bool {
	switch kind {
	case KindHello, KindReady:
		switch v.(type) {
		case Hello, *Hello:
			return true
		}
	case KindMarker:
		switch v.(type) {
		case Marker, *Marker:
			return true
		}
	case KindFinish, KindDone:
		switch v.(type) {
		case Finish, *Finish:
			return true
		}
	case KindResult:
		switch v.(type) {
		case Result, *Result:
			return true
		}
	case KindError:
		switch v.(type) {
		case Error, *Error:
			return true
		}
	case KindCancel:
		switch v.(type) {
		case Cancel, *Cancel:
			return true
		}
	case KindPing, KindPong:
		switch v.(type) {
		case Ping, *Ping:
			return true
		}
	}
	return false
}

func JSONMessage(kind Kind, value any) (Message, error) {
	if !jsonType(kind, value) {
		return Message{}, errors.New("ASR1 JSON 类型与kind不一致")
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return Message{}, errors.New("ASR1 JSON 指针为空")
	}
	if err := value.(validator).Validate(); err != nil {
		return Message{}, err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return Message{}, err
	}
	if len(body) > MaxLength-4 {
		return Message{}, errors.New("ASR1 JSON 消息过大")
	}
	return Message{Kind: kind, Body: body}, nil
}

// ParseJSON 保证精确字段、必填字段和null语义，避免encoding/json默认的宽松转换。
func ParseJSON(body []byte, dst any) error {
	if len(body) == 0 || len(body) > MaxLength-4 || !utf8.Valid(body) {
		return errors.New("ASR1 JSON 长度或UTF-8错误")
	}
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("ASR1 JSON 目标必须为非空指针")
	}
	if _, ok := dst.(validator); !ok {
		return errors.New("ASR1 JSON 目标类型不受支持")
	}
	if err := unicodeEscapes(body); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := uniqueValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("ASR1 JSON 尾随内容")
	}
	if err := exactShape(body, rv.Type().Elem(), 0); err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("ASR1 JSON: %w", err)
	}
	return dst.(validator).Validate()
}

func uniqueValue(dec *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("ASR1 JSON 嵌套过深")
	}
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("ASR1 JSON 重复或非法字段")
			}
			seen[s] = true
			if err := uniqueValue(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("ASR1 JSON 对象未完整结束")
		}
	case '[':
		for dec.More() {
			if err := uniqueValue(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("ASR1 JSON 数组未完整结束")
		}
	default:
		return errors.New("ASR1 JSON 非法括号")
	}
	return nil
}

func exactShape(raw []byte, t reflect.Type, depth int) error {
	if depth > 8 {
		return errors.New("ASR1 JSON 类型嵌套过深")
	}
	nullable := t.Kind() == reflect.Pointer
	if nullable {
		t = t.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if nullable {
			return nil
		}
		return errors.New("ASR1 必填字段不能为null")
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields) != t.NumField() {
		return errors.New("ASR1 JSON 未知或缺少字段")
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := f.Tag.Get("json")
		value, ok := fields[name]
		if !ok {
			return fmt.Errorf("ASR1 JSON 缺少字段 %s", name)
		}
		if err := exactShape(value, f.Type, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// 拒绝孤立UTF-16代理项；Go默认替换字符会让原始结果内容悄悄改变。
func unicodeEscapes(raw []byte) error {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return errors.New("ASR1 JSON 末尾转义")
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return errors.New("ASR1 JSON unicode转义截断")
		}
		v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return err
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return errors.New("ASR1 JSON 孤立低代理项")
		}
		if v < 0xd800 || v > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return errors.New("ASR1 JSON 孤立高代理项")
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return errors.New("ASR1 JSON 代理项配对错误")
		}
		i += 6
	}
	return nil
}
