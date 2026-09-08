package esl

import (
	"errors"
	"strconv"
	"strings"
)

// consoleExecuteFlag 对齐原版常用 switch_true 标记；非数字未知词为 false。
// 数字先按原版 atoi 规则取小数点前整数，显式拒绝超出 C int 范围的未定义转换。
func consoleExecuteFlag(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "yes", "on", "true", "t", "enabled", "active", "allow":
		return true, nil
	}
	numeric := value
	if strings.HasPrefix(numeric, "+") || strings.HasPrefix(numeric, "-") {
		numeric = numeric[1:]
	}
	for _, char := range numeric {
		if char != '.' && (char < '0' || char > '9') {
			return false, nil
		}
	}
	integer, _, _ := strings.Cut(value, ".")
	if integer == "" || integer == "+" || integer == "-" {
		return false, nil
	}
	parsed, err := strconv.ParseInt(integer, 10, 32)
	if err != nil {
		return false, errors.New("console_execute numeric flag outside supported range")
	}
	return parsed != 0, nil
}

// singleConsoleCommand 实现 switch_console_execute 的单命令路径：先跳过首空格，
// 再按第一个空格划分 API 名称和参数，由调用处沿用原 API 的 ASCII 空白裁剪。
// 原版还支持 ;; 批处理和别名数据库；这些尚未实现，必须在执行任何 API 前明确拒绝。
func singleConsoleCommand(line string) (string, string, error) {
	if strings.Contains(line, ";;") || strings.ContainsAny(line, "\r\n\x00") {
		return "", "", errors.New("console_execute supports one API command; batches and aliases are not supported")
	}
	line = strings.TrimLeft(line, " ")
	name, arguments, _ := strings.Cut(line, " ")
	name = trimAPIWhitespace(name)
	switch name {
	case "echo", "create_uuid", "show", "status", "version", "uuid_exists", "uuid_getvar", "uuid_setvar", "uuid_kill":
		return name, arguments, nil
	default:
		return "", "", errors.New("console_execute API or alias is not supported: " + name)
	}
}
