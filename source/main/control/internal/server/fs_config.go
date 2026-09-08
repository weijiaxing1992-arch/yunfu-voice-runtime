package server

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// fsTemplates 保留固定 FreeSWITCH 版本的原始 XML、许可和来源校验值。
// 这些模板用于配置编辑/导出，不会交给尚未实现 XML 宿主的 RustSwitch 执行。
//
//go:embed fs_templates
var fsTemplates embed.FS

// fsParameter 标识一个可视化属性；位置只供服务器原样替换，避免重排 XML 注释和预处理指令。
type fsParameter struct {
	// ID 由相对路径与字段次序组成，必须与配置版本一起使用，不能跨版本猜测位置。
	ID string `json:"id"`
	// Name、Value 和 Scope 提供业务名称、解码后的值和所在节点，帮助区分同名参数。
	Name        string `json:"name"`
	Value       string `json:"value"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	// start/end 是原始 UTF-8 字节区间；prefix 保存预处理变量的 key= 部分。
	start, end int
	prefix     string
}

// fsConfigFile 是一个配置文件及其可编辑参数列表。没有参数的文件仍可使用 XML 编辑器。
type fsConfigFile struct {
	// Path 限制在导出配置树内，Category 只影响页面分类，不决定运行模块。
	Path     string `json:"path"`
	Category string `json:"category"`
	// Parameters 只包含当前可映射为表单的属性；其余结构在 XML 编辑器中保留。
	Parameters []fsParameter `json:"parameters"`
}

var (
	fsOnce      sync.Once
	fsBase      map[string]string
	fsBaseError error
	// 官方模板只解析一次；字段切片保留为私有只读值，对外响应始终复制。
	fsParsedOnce  sync.Once
	fsParsedBase  map[string]fsParsedFile
	fsParsedError error
	// 仅识别已经过 XML 解码器验证的起始标签属性，不用正则承担 XML 语法校验。
	// 属性必须从 XML 空白后开始，并完整消费 Unicode 名称及其引号值，避免把名称尾部或值内文本当成新属性。
	attributePattern   = regexp.MustCompile(`[ \t\r\n]+([^ \t\r\n=<>/"']+)\s*=\s*("[^"]*"|'[^']*')`)
	fsPathPattern      = regexp.MustCompile(`^[A-Za-z0-9_./-]+\.xml$`)
	xmlEncodingPattern = regexp.MustCompile(`(?i)(<\?xml[^>]*\bencoding\s*=\s*["'])([^"']+)(["'])`)
)

// baseFSFiles 懒加载只读参考模板；以后每次请求只合并已保存的少量文件修改。
func baseFSFiles() (map[string]string, error) {
	fsOnce.Do(func() {
		fsBase = map[string]string{}
		fsBaseError = fs.WalkDir(fsTemplates, "fs_templates", func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(name, ".xml") {
				return nil
			}
			data, e := fsTemplates.ReadFile(name)
			if e != nil {
				return e
			}
			raw := string(data)
			// 固定基线中五个 Windows-1252 声明文件实际上只包含 ASCII，统一声明不会改变字符。
			// 不把这一规则套用到真实旧编码的非 ASCII 数据，否则会造成静默乱码。
			if match := xmlEncodingPattern.FindStringSubmatch(raw); len(match) > 0 && !strings.EqualFold(match[2], "utf-8") {
				if !strings.EqualFold(match[2], "windows-1252") {
					return fmt.Errorf("模板编码不受支持：%s", match[2])
				}
				for _, b := range data {
					if b >= 128 {
						return fmt.Errorf("旧编码模板含非 ASCII 字符，须先转换 UTF-8：%s", name)
					}
				}
				raw = xmlEncodingPattern.ReplaceAllString(raw, "${1}UTF-8${3}")
			}
			fsBase[strings.TrimPrefix(name, "fs_templates/")] = raw
			return nil
		})
	})
	return cloneFSFiles(fsBase), fsBaseError
}

// fsParsedFile 绑定精确的原始内容与属性字节位置；内容不同绝不沿用旧字段偏移或校验结果。
type fsParsedFile struct {
	raw        string
	parameters []fsParameter
}

// baseFSParsed 在启动时准备固定目录，后续请求不重复解码官方XML；初始化错误仍明确返回。
func baseFSParsed() (map[string]fsParsedFile, error) {
	fsParsedOnce.Do(func() {
		files, err := baseFSFiles()
		if err != nil {
			fsParsedError = err
			return
		}
		fsParsedBase = make(map[string]fsParsedFile, len(files))
		for name, raw := range files {
			fields, err := parseFSFile(name, raw)
			if err != nil {
				fsParsedError = err
				return
			}
			fsParsedBase[name] = fsParsedFile{raw, fields}
		}
	})
	return fsParsedBase, fsParsedError
}

// fsCatalogCache 只保存最近一个完整目录的内容快照，避免反复改名/编辑导致缓存无限增长。
// entries 发布后不再修改；短锁只交换映射引用，XML计算与对外复制都在锁外执行。
type fsCatalogCache struct {
	mu      sync.Mutex
	entries map[string]fsParsedFile
}

// catalog 优先复用精确相同的官方或草稿内容；返回的每个参数切片都由本次调用独占。
func (cache *fsCatalogCache) catalog(files map[string]string) ([]fsConfigFile, error) {
	base, err := baseFSParsed()
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	previous := cache.entries
	cache.mu.Unlock()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	next := make(map[string]fsParsedFile, len(files))
	result := make([]fsConfigFile, 0, len(files))
	for _, name := range names {
		raw := files[name]
		parsed, ok := base[name]
		if !ok || parsed.raw != raw {
			parsed, ok = previous[name]
		}
		if !ok || parsed.raw != raw {
			fields, err := parseFSFile(name, raw)
			if err != nil {
				return nil, err
			}
			parsed = fsParsedFile{raw, fields}
		}
		next[name] = parsed
		fields := make([]fsParameter, len(parsed.parameters))
		copy(fields, parsed.parameters)
		result = append(result, fsConfigFile{name, fsCategory(name), fields})
	}
	cache.mu.Lock()
	cache.entries = next
	cache.mu.Unlock()
	return result, nil
}

// validFSPath 只允许配置树内的相对 XML 路径，保证导出的 ZIP 没有目录穿越条目。
func validFSPath(name string) bool {
	return len(name) > 0 && len(name) <= 200 && fsPathPattern.MatchString(name) && !strings.HasPrefix(name, "/") && path.Clean(name) == name && !strings.HasPrefix(name, "../") && !strings.Contains(name, "/../")
}

// parseFSFile 允许官方配置中的多根 XML 片段，保留变量表达式、注释和 X-PRE-PROCESS。
// 禁止 DTD/实体声明，不读取外部文件、不请求网络、不执行任何配置指令。
func parseFSFile(name, raw string) ([]fsParameter, error) {
	if !validFSPath(name) || len(raw) > 256<<10 {
		return nil, errors.New("XML 文件路径无效或超过 256 KiB")
	}
	if match := xmlEncodingPattern.FindStringSubmatch(raw); len(match) > 0 && !strings.EqualFold(match[2], "utf-8") {
		return nil, errors.New("网页 XML 草稿须使用 UTF-8 编码声明")
	}
	decoder := xml.NewDecoder(strings.NewReader(raw))
	parameters := []fsParameter{}
	stack := []string{}
	rootCount, tokens := 0, 0
	for {
		before := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s：%w", name, err)
		}
		tokens++
		if tokens > 100000 {
			return nil, errors.New("XML 内容过于复杂")
		}
		switch current := token.(type) {
		case xml.Directive:
			return nil, errors.New("配置编辑器不接受 DTD 或实体声明")
		case xml.CharData:
			if len(stack) == 0 && strings.TrimSpace(string(current)) != "" {
				return nil, errors.New("XML 根元素之外存在文本")
			}
		case xml.StartElement:
			if len(stack) == 0 {
				rootCount++
			}
			if len(stack) >= 128 {
				return nil, errors.New("XML 嵌套过深")
			}
			attrs := map[string]string{}
			// encoding/xml 不保证拒绝重复属性；必须按展开后的名称主动检查。
			// 同一命名空间使用不同前缀但局部名称相同，也属于重复属性，不能交给后值覆盖前值。
			seenAttributes := make(map[xml.Name]bool, len(current.Attr))
			for _, a := range current.Attr {
				if seenAttributes[a.Name] {
					return nil, fmt.Errorf("%s：XML 元素 %s 存在重复属性 %s", name, current.Name.Local, a.Name.Local)
				}
				seenAttributes[a.Name] = true
				// FreeSWITCH 参数表只解释无命名空间的属性；第三方元数据如 meta:value 原样保留。
				// 否则目录可能读取 meta:value，却把更新写到无前缀 value，造成显示与修改目标不一致。
				if a.Name.Space == "" {
					attrs[a.Name.Local] = a.Value
				}
			}
			label := current.Name.Local
			if value := attrs["name"]; value != "" {
				label += "[" + value + "]"
			} else if value = attrs["id"]; value != "" {
				label += "[" + value + "]"
			}
			stack = append(stack, label)
			// 带命名空间的第三方元素只能通过原始 XML 编辑，不冒充同名 FreeSWITCH 配置元素。
			if current.Name.Space != "" {
				continue
			}
			attribute, display, prefix, description := "", "", "", ""
			switch current.Name.Local {
			case "param", "variable", "option":
				attribute, display = "value", attrs["name"]
				description = "保留原模块参数名称与取值；当前仅编辑和导出。"
			case "action", "anti-action":
				attribute, display = "data", "执行 "+attrs["application"]
				description = "拨号计划应用参数；执行顺序和条件通过 XML 结构保持。"
			case "condition":
				attribute, display = "expression", "匹配 "+attrs["field"]
				description = "条件表达式；嵌套、继续规则及反向动作请在 XML 中调整。"
			case "load":
				attribute, display = "module", "加载模块"
				description = "FreeSWITCH 模块名；启用此配置不代表 RustSwitch 已实现对应模块。"
			case "X-PRE-PROCESS":
				if attrs["cmd"] == "set" {
					if key, _, ok := strings.Cut(attrs["data"], "="); ok {
						attribute, display, prefix = "data", key, key+"="
						description = "FreeSWITCH 预处理变量，保留 $${...} 表达式。"
					}
				}
			case "node":
				if _, ok := attrs["cidr"]; ok {
					attribute, display = "cidr", "访问控制网段"
					description = "ACL 网段；允许或拒绝属性仍保留在原 XML 节点中。"
				}
			}
			value, present := attrs[attribute]
			if attribute == "" || display == "" || !present {
				continue
			}
			segment := raw[before:int(decoder.InputOffset())]
			for _, match := range attributePattern.FindAllStringSubmatchIndex(segment, -1) {
				if segment[match[2]:match[3]] != attribute {
					continue
				}
				parameters = append(parameters, fsParameter{ID: name + "#" + strconv.Itoa(len(parameters)), Name: display, Value: strings.TrimPrefix(value, prefix), Scope: strings.Join(stack, " / "), Description: description, start: before + match[4] + 1, end: before + match[5] - 1, prefix: prefix})
				break
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if rootCount == 0 {
		return nil, errors.New("XML 至少需要一个根元素")
	}
	return parameters, nil
}

// validateFSFiles 校验草稿大小与结构，既用于 HTTP 写入也用于服务重启恢复。
func validateFSFiles(files map[string]string) error {
	if err := validateFSFileLimits(files); err != nil {
		return err
	}
	for name, raw := range files {
		if _, err := parseFSFile(name, raw); err != nil {
			return err
		}
	}
	return nil
}

// validateFSFileLimits 始终核对完整草稿的数量与大小；结构校验仍由解析器或同内容缓存完成。
func validateFSFileLimits(files map[string]string) error {
	if len(files) > 512 {
		return errors.New("XML 草稿最多保存 512 个文件")
	}
	total := 0
	for name, raw := range files {
		total += len(raw)
		if !validFSPath(name) || len(raw) > 256<<10 {
			return errors.New("XML 文件路径无效或超过 256 KiB")
		}
	}
	if total > 5<<20 {
		return errors.New("XML 草稿总量超过 5 MiB")
	}
	return nil
}

// effectiveFSFiles 合并官方基线与用户草稿，不将数据写到任意文件系统路径。
func effectiveFSFiles(overrides map[string]string) (map[string]string, error) {
	base, err := baseFSFiles()
	if err != nil {
		return nil, err
	}
	result := base
	for name, raw := range overrides {
		result[name] = raw
	}
	return result, nil
}

// fsCategory 将配置按业务领域归类；未知或第三方模块仍能在其他模块中编辑。
func fsCategory(name string) string {
	switch {
	case strings.HasPrefix(name, "sip_profiles/") || strings.Contains(name, "sofia"):
		return "SIP"
	case strings.HasPrefix(name, "directory/"):
		return "用户目录"
	case strings.HasPrefix(name, "dialplan/") || strings.HasPrefix(name, "chatplan/"):
		return "拨号计划"
	case strings.HasPrefix(name, "lang/"):
		return "语言与提示音"
	case strings.Contains(name, "conference"):
		return "会议"
	case strings.Contains(name, "cdr") || strings.Contains(name, "record"):
		return "录音与话单"
	case strings.Contains(name, "lua") || strings.Contains(name, "v8") || strings.Contains(name, "python"):
		return "脚本"
	case strings.Contains(name, "opus") || strings.Contains(name, "av.conf") || strings.Contains(name, "codec"):
		return "编解码"
	case strings.Contains(name, "event_socket") || strings.Contains(name, "xml_curl"):
		return "控制与外部接口"
	case strings.Contains(name, "acl"):
		return "访问控制"
	case name == "vars.xml" || name == "freeswitch.xml" || strings.Contains(name, "switch.conf") || strings.Contains(name, "modules.conf"):
		return "全局与模块"
	default:
		return "其他模块"
	}
}

// fsCatalog 生成当前有效草稿的完整字段目录，而非把参考目录计作运行能力。
func fsCatalog(files map[string]string) ([]fsConfigFile, error) {
	return (&fsCatalogCache{}).catalog(files)
}

// patchFSParameters 从后往前替换属性值，避免前一次替换改变后续字节位置。
// XML 特殊字符统一转义，输入不能通过关闭引号注入新的标签或属性。
func patchFSParameters(files map[string]string, changes map[string]string) (map[string]string, error) {
	result := cloneFSFiles(files)
	// 空修改沿用已验证快照；调用者仍须核对完整草稿的结构/大小后才允许提交。
	if len(changes) == 0 {
		return result, nil
	}
	changedFiles := map[string]bool{}
	for id := range changes {
		name, _, ok := strings.Cut(id, "#")
		if _, exists := files[name]; !ok || !exists {
			return nil, fmt.Errorf("参数不存在或 XML 结构已变化：%s", id)
		}
		changedFiles[name] = true
	}
	known := map[string]bool{}
	for name := range changedFiles {
		raw := files[name]
		fields, err := parseFSFile(name, raw)
		if err != nil {
			return nil, err
		}
		for i := len(fields) - 1; i >= 0; i-- {
			field := fields[i]
			value, ok := changes[field.ID]
			if !ok {
				continue
			}
			known[field.ID] = true
			if len(value) > 16384 {
				return nil, errors.New("单个 XML 参数超过 16 KiB")
			}
			var escaped bytes.Buffer
			if err = xml.EscapeText(&escaped, []byte(field.prefix+value)); err != nil {
				return nil, err
			}
			raw = raw[:field.start] + escaped.String() + raw[field.end:]
		}
		result[name] = raw
	}
	for id := range changes {
		if !known[id] {
			return nil, fmt.Errorf("参数不存在或 XML 结构已变化：%s", id)
		}
	}
	return result, nil
}

// fsCapabilities 描述真实运行边界；页面可导出完整配置不等于引擎实现了该功能。
func fsCapabilities() []map[string]string {
	return []map[string]string{
		{"name": "峰值保护与运行监控", "status": "已实现", "description": "动态新呼叫准入、媒体分片压力保护、状态与指标。"},
		{"name": "可信中继 SIP / 主流音频透传", "status": "部分实现", "description": "受限 SIP/UDP 双呼叫腿，支持 G.711、G.722、Opus、G.729、G.726/AAL2、L16 同格式 RTP/RTCP 与 DTMF/CN；完整 Sofia 与实时转码待实现。"},
		{"name": "FreeSWITCH XML 配置", "status": "导出可用", "description": "固定 1.11.3 参考配置可视化编辑及 XML/ZIP 导出，当前 RustSwitch 不执行。"},
		{"name": "ESL / fs_cli / 核心 API", "status": "待实现", "description": "已有兼容标准，尚无原版客户端完整差分验收。"},
		{"name": "用户目录 / XML 拨号计划 / 网关", "status": "待实现", "description": "配置可编辑导出；RustSwitch 仍使用固定上游，不执行 XML 业务。"},
		{"name": "录音 / 会议 / IVR / 脚本 / 话单", "status": "待实现", "description": "配置编辑覆盖相关模块；业务执行与生命周期兼容尚未完成。"},
		{"name": "C/C++ 原生生态", "status": "部分实现", "description": "自定义 C 编解码 ABI 可用；旧 FreeSWITCH 模块二进制宿主仍待实现。"},
		{"name": "一万路与故障无损接管", "status": "待验收", "description": "Linux 服务器尚待提供；当前控制进程退出会结束媒体子进程。"},
	}
}

// exportFSConfig 输出完整参考配置树及修改记录；下载不会重载或部署任何服务器。
func exportFSConfig(w http.ResponseWriter, files map[string]string, revision uint64) error {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	add := func(name string, data []byte) error {
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}
	for _, name := range names {
		if err := add("conf/"+name, []byte(files[name])); err != nil {
			return err
		}
	}
	for _, name := range []string{"LICENSE.freeswitch", "SOURCE.json"} {
		data, err := fsTemplates.ReadFile("fs_templates/" + name)
		if err != nil {
			return err
		}
		if err = add(name, data); err != nil {
			return err
		}
	}
	note := fmt.Sprintf("FreeSWITCH 1.11.3 配置导出，管理版本 %d。\n来源：SignalWire FreeSWITCH / Anthony Minessale II，commit ef32e205295e29f034f1453ad245ba5efb07b94a。\n本包基于原版 vanilla 配置，按网页保存的草稿修改；修改版本及日期：%d / 2026-09-05。\n包含原版示例账号和模块参数，部署时按实际环境核对；导出不代表已在任何服务器生效。\nRustSwitch 当前不执行这些 FreeSWITCH XML 配置，配置的业务语义需由对应版本 FreeSWITCH 验证。\n五份仅含 ASCII 的旧编码模板已统一 UTF-8 声明。原始文件哈希见 SOURCE.json，可用于比较修改。\n", revision, revision)
	if err := add("README-中文.txt", []byte(note)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="freeswitch-1.11.3-config.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(buffer.Len()))
	_, err := w.Write(buffer.Bytes())
	return err
}
