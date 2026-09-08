package server

import "strings"

const setVariablesUsage = "-USAGE: <uuid> <var>=<value>;<var>=<value>...\n"

// splitVariableAssignments 解析分号、成对单引号及反斜线，保持值内分号与空格。
// 预算限制在分配或写变量前执行；不沿用原版超过64项后把尾部混入最后值的行为。
func splitVariableAssignments(value string) ([]string, bool) {
	if len(value) > 16384 || strings.ContainsRune(value, 0) {
		return nil, false
	}
	delimiter := byte(';')
	if strings.HasPrefix(value, "^^") && len(value) > 3 {
		delimiter = value[2]
		// 空格走原版另一分词器，暂不接受；控制字节及多字节分隔符也显式拒绝。
		if delimiter <= ' ' || delimiter >= 127 || delimiter == '\'' || delimiter == '\\' {
			return nil, false
		}
		value = value[3:]
	}
	var result []string
	for value != "" {
		quoted, end := false, len(value)
		for i := 0; i < len(value); i++ {
			switch value[i] {
			case '\\':
				i++
			case '\'':
				if quoted || strings.ContainsRune(value[i+1:], '\'') {
					quoted = !quoted
				}
			case delimiter:
				if !quoted {
					end = i
					i = len(value)
				}
			}
		}
		result = append(result, cleanVariableAssignment(value[:end], delimiter))
		if len(result) > 64 {
			return nil, false
		}
		if end == len(value) {
			break
		}
		value = value[end+1:]
	}
	return result, true
}

// cleanVariableAssignment 对齐所选FreeSWITCH分隔器的引号、常见转义和外侧ASCII空格处理。
// 未知转义保留反斜线；只有未被引号保护的尾部空格被删除。
func cleanVariableAssignment(value string, delimiter byte) string {
	value = strings.TrimLeft(value, " ")
	out := make([]byte, 0, len(value))
	quoted, meaningful := false, 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\\' && i+1 < len(value) {
			n := value[i+1]
			escaped := true
			switch n {
			case '\'', '"', '\\', delimiter:
			case 'n':
				n = '\n'
			case 'r':
				n = '\r'
			case 't':
				n = '\t'
			case 's':
				n = ' '
			default:
				escaped = false
			}
			if escaped {
				out = append(out, n)
				meaningful = len(out)
				i++
				continue
			}
		}
		if c == '\'' && (quoted || strings.ContainsRune(value[i+1:], '\'')) {
			quoted = !quoted
			if quoted {
				meaningful = len(out)
			}
			continue
		}
		out = append(out, c)
		if c != ' ' || quoted {
			meaningful = len(out)
		}
	}
	return string(out[:meaningful])
}

// compatibilitySetVariables 在通话主循环顺序写入；逐项结果保留原版部分成功语义。
// 缺等号项与空值均删除变量；这一路径已用原版实际读回核对。
func (s *Server) compatibilitySetVariables(arguments string) string {
	if arguments == "" {
		return setVariablesUsage
	}
	uuid, body, found := strings.Cut(arguments, " ")
	if !found {
		return "" // 固定原版函数此路径直接结束，不虚构写入或成功。
	}
	entry := s.compatChannels[uuid]
	if entry.call == nil || entry.call.Ended {
		return "-ERR No such channel!\n" + setVariablesUsage
	}
	items, ok := splitVariableAssignments(body)
	if !ok {
		return "-ERR variable list exceeds limits or uses unsupported delimiter\n"
	}
	var output strings.Builder
	applied := 0
	for _, item := range items {
		name, value, _ := strings.Cut(item, "=")
		if name == "" {
			output.WriteString("-ERR No variable specified\n")
			continue
		}
		if !validApplicationVariable(name) {
			output.WriteString("-ERR protected or unsupported variable\n")
			continue
		}
		// 不支持变量展开时明确报告，不能把表达式误写成实际已展开结果。
		if strings.Contains(value, "${") {
			output.WriteString("-ERR variable expansion is unsupported\n")
			continue
		}
		argument := uuid + " " + name
		if value != "" {
			argument += " " + value
		}
		response := s.compatibilityChannelCommand("uuid_setvar", argument)
		if response == "+OK\n" {
			applied++
		} else {
			output.WriteString(response)
		}
	}
	if applied > 0 {
		output.WriteString("+OK\n")
	} else {
		output.WriteString(setVariablesUsage)
	}
	return output.String()
}
