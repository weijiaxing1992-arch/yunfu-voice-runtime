package sip

import (
	"errors"
	"fmt"
	"net/netip"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/media"
	"sort"
	"strconv"
	"strings"
)

// SDP 保存单音频流及选择后的主编码和辅助载荷；当前媒体路径仅保证同编码原样透传。
type SDP struct {
	Peer          media.Peer      // 经地址白名单及反射检查后的远端 RTP/RTCP 目标。
	Payload       uint8           // 主音频载荷编号，静态编号或经过 rtpmap 验证的动态编号。
	DTMF          *uint8          // 可选 telephone-event 动态载荷编号。
	PTime         int             // 音频分包间隔，单位为毫秒。
	Codec         media.CodecSpec // 采样率、RTP 时钟、声道与格式参数分别保存，避免从 PT 猜编码。
	DTMFClockRate uint32          // 电话事件的时间戳时钟；没有 DTMF 时为零。
	DTMFEvents    string          // 支持的事件集合；未声明 fmtp 时按 RFC 4733 默认 0-15。
	CN            *uint8          // 可选 RFC 3389 舒适噪声载荷，不与音频或 DTMF 共用编号。
	CNClockRate   uint32          // CN 时钟；当前只选择与主音频 RTP 时钟相同的候选。
	MaxPTime      int             // 对端允许接收的最大分包间隔；零表示没有显式声明。
}

// ParseSDP 在有限长度内解析一个 IPv4 RTP/AVP 音频流，筛选编解码并验证可用媒体目的地址。
// ICE、SRTP、RTCP 复用、方向变化和多流等能力尚未实现，出现时明确拒绝。
func ParseSDP(body []byte, c config.Media) (SDP, error) {
	var result SDP
	if len(body) == 0 || len(body) > 8192 {
		return result, errors.New("invalid SDP length")
	}
	var sessionIP, mediaIP string
	mediaSeen := false
	mediaCount := 0
	port := 0
	rtcpPort := 0
	rtcpIP := ""
	payloads := []int{}
	rtpmap := map[int]string{}
	fmtp := map[int]string{}
	listed := map[int]bool{}
	ptime := 20
	maxptime := 0
	ptimeSeen := false
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if len(lines) > 128 {
		return result, errors.New("too many SDP lines")
	}
	for _, line := range lines {
		if len(line) > 1024 || strings.ContainsRune(line, '\r') {
			return result, errors.New("invalid SDP line")
		}
		// 媒体级连接地址优先于会话级地址；最终仍要求整个描述恰好只有一个音频流。
		if strings.HasPrefix(line, "c=") {
			p := strings.Fields(line[2:])
			if len(p) != 3 || p[0] != "IN" || p[1] != "IP4" {
				return result, errors.New("IPv4 SDP required")
			}
			if mediaSeen {
				mediaIP = p[2]
			} else {
				sessionIP = p[2]
			}
		}
		if strings.HasPrefix(line, "m=") {
			mediaCount++
			mediaSeen = true
			p := strings.Fields(line[2:])
			if len(p) < 4 || p[0] != "audio" || p[2] != "RTP/AVP" || len(p) > 36 {
				return result, errors.New("one RTP/AVP audio stream required")
			}
			n, e := strconv.Atoi(p[1])
			if e != nil || n < 1 || n > 65535 {
				return result, errors.New("invalid RTP port")
			}
			port = n
			for _, v := range p[3:] {
				n, e := strconv.Atoi(v)
				if e != nil || n < 0 || n > 127 {
					return result, errors.New("invalid payload type")
				}
				if listed[n] {
					return result, errors.New("duplicate payload in media description")
				}
				listed[n] = true
				payloads = append(payloads, n)
			}
		}
		if strings.HasPrefix(line, "a=rtpmap:") {
			p := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(p) != 2 {
				return result, errors.New("invalid rtpmap")
			}
			n, e := strconv.Atoi(p[0])
			if e != nil || n < 0 || n > 127 {
				return result, errors.New("invalid rtpmap payload")
			}
			if _, exists := rtpmap[n]; exists {
				return result, errors.New("duplicate rtpmap")
			}
			rtpmap[n] = strings.ToLower(p[1])
		}
		if strings.HasPrefix(line, "a=fmtp:") {
			p := strings.SplitN(strings.TrimPrefix(line, "a=fmtp:"), " ", 2)
			if len(p) != 2 || strings.TrimSpace(p[1]) == "" {
				return result, errors.New("invalid fmtp")
			}
			n, e := strconv.Atoi(p[0])
			if e != nil || n < 0 || n > 127 {
				return result, errors.New("invalid fmtp payload")
			}
			if _, exists := fmtp[n]; exists {
				return result, errors.New("duplicate fmtp")
			}
			fmtp[n] = strings.TrimSpace(p[1])
		}
		if strings.HasPrefix(line, "a=rtcp:") {
			p := strings.Fields(strings.TrimPrefix(line, "a=rtcp:"))
			if len(p) != 1 && len(p) != 4 {
				return result, errors.New("invalid RTCP attribute")
			}
			n, e := strconv.Atoi(p[0])
			if e != nil || n < 1 || n > 65535 {
				return result, errors.New("invalid RTCP port")
			}
			rtcpPort = n
			if len(p) == 4 {
				if p[1] != "IN" || p[2] != "IP4" {
					return result, errors.New("IPv4 RTCP required")
				}
				rtcpIP = p[3]
			}
		}
		if strings.HasPrefix(line, "a=ptime:") {
			n, e := strconv.Atoi(strings.TrimPrefix(line, "a=ptime:"))
			if e != nil || n < 3 || n > 120 || ptimeSeen {
				return result, errors.New("unsupported ptime")
			}
			ptime = n
			ptimeSeen = true
		}
		if strings.HasPrefix(line, "a=maxptime:") {
			n, e := strconv.Atoi(strings.TrimPrefix(line, "a=maxptime:"))
			if e != nil || n < 3 || n > 120 || maxptime != 0 {
				return result, errors.New("unsupported maxptime")
			}
			maxptime = n
		}
		if line == "a=sendonly" || line == "a=recvonly" || line == "a=inactive" || strings.HasPrefix(line, "a=rtcp-mux") || strings.HasPrefix(line, "a=crypto:") || strings.HasPrefix(line, "a=ice-") {
			return result, errors.New("SDP feature not supported by UDP prototype")
		}
	}
	if mediaCount != 1 {
		return result, errors.New("exactly one audio stream required")
	}
	for pt := range rtpmap {
		if !listed[pt] {
			return result, errors.New("rtpmap payload absent from media description")
		}
	}
	for pt := range fmtp {
		if !listed[pt] {
			return result, errors.New("fmtp payload absent from media description")
		}
	}
	if maxptime != 0 && ptime > maxptime {
		return result, errors.New("ptime exceeds maxptime")
	}
	ip := mediaIP
	if ip == "" {
		ip = sessionIP
	}
	if rtcpIP == "" {
		rtcpIP = ip
	}
	// 未显式声明 RTCP 时采用 RTP 加一；最高端口必须显式声明，防止溢出到零。
	if rtcpPort == 0 {
		if port == 65535 {
			return result, errors.New("explicit RTCP port required with RTP port 65535")
		}
		rtcpPort = port + 1
	}
	if ip == rtcpIP && port == rtcpPort {
		return result, errors.New("RTCP mux unsupported")
	}
	// 分别验证 RTP 和 RTCP，防止把服务器变成任意 UDP 反射器或形成自身媒体环路。
	for _, pair := range []struct {
		ip   string // 当前检查的远端地址文本。
		port int    // 当前检查的远端端口。
	}{{ip, port}, {rtcpIP, rtcpPort}} {
		addr, e := netip.ParseAddr(pair.ip)
		if e != nil || !config.UsableIP(addr) || !config.Allowed(addr, c.AllowedRemoteNetworks) {
			return result, errors.New("SDP peer outside media allowlist")
		}
		if (pair.ip == c.BindIP || pair.ip == c.AdvertiseIP) && pair.port >= c.PortStart && pair.port <= c.PortEnd {
			return result, errors.New("SDP peer points into server media range")
		}
	}
	var err error
	result, err = selectFormatsForMode(payloads, rtpmap, fmtp, ptime, c.Processing)
	if err != nil {
		return result, err
	}
	result.MaxPTime = maxptime
	result.Peer = media.Peer{RTP: fmt.Sprintf("%s:%d", ip, port), RTCP: fmt.Sprintf("%s:%d", rtcpIP, rtcpPort)}
	return result, nil
}

// RenderSDP 按已选择的音频和电话事件参数生成本机媒体描述，显式声明独立 RTCP 地址。
// 这里只宣告透传所需参数，不进行编解码转换，也不扩展原始协商能力。
func RenderSDP(id uint64, ip string, rtp, rtcp int, offer SDP) []byte {
	codec := offer.CodecMetadata()
	payloads := strconv.Itoa(int(offer.Payload))
	if offer.DTMF != nil {
		payloads += " " + strconv.Itoa(int(*offer.DTMF))
	}
	if offer.CN != nil {
		payloads += " " + strconv.Itoa(int(*offer.CN))
	}
	encoding := fmt.Sprintf("%s/%d", codec.Name, codec.RTPClockRate)
	if codec.Channels != 1 {
		encoding += fmt.Sprintf("/%d", codec.Channels)
	}
	s := fmt.Sprintf("v=0\r\no=rustswitch %d 1 IN IP4 %s\r\ns=RustSwitch\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP %s\r\na=rtcp:%d IN IP4 %s\r\na=rtpmap:%d %s\r\na=ptime:%d\r\na=sendrecv\r\n", id, ip, ip, rtp, payloads, rtcp, ip, offer.Payload, encoding, offer.PTime)
	if codec.FMTP != "" {
		s += fmt.Sprintf("a=fmtp:%d %s\r\n", offer.Payload, codec.FMTP)
	}
	if offer.MaxPTime != 0 {
		s += fmt.Sprintf("a=maxptime:%d\r\n", offer.MaxPTime)
	}
	if offer.DTMF != nil {
		clock, events := offer.DTMFClockRate, offer.DTMFEvents
		if clock == 0 {
			clock = 8000
		}
		if events == "" {
			events = "0-15"
		}
		s += fmt.Sprintf("a=rtpmap:%d telephone-event/%d\r\na=fmtp:%d %s\r\n", *offer.DTMF, clock, *offer.DTMF, events)
	}
	if offer.CN != nil {
		s += fmt.Sprintf("a=rtpmap:%d CN/%d\r\n", *offer.CN, offer.CNClockRate)
	}
	return []byte(s)
}

// CodecMetadata 仅为旧的内部 PT 0/8 构造器补默认值；动态编号绝不反推编码名称。
func (s SDP) CodecMetadata() media.CodecSpec {
	if s.Codec.Name != "" {
		return s.Codec
	}
	name := ""
	if s.Payload == 0 {
		name = "PCMU"
	}
	if s.Payload == 8 {
		name = "PCMA"
	}
	return media.CodecSpec{Name: name, SampleRate: 8000, RTPClockRate: 8000, Channels: 1, PTimeMS: uint16(s.PTime)}
}

// staticMapping 只列本实现能解释的 RTP/AVP 静态音频编号；G.722 的历史时钟必须为 8000。
func staticMapping(pt int) string {
	switch pt {
	case 0:
		return "PCMU/8000"
	case 8:
		return "PCMA/8000"
	case 9:
		return "G722/8000"
	case 10:
		return "L16/44100/2"
	case 11:
		return "L16/44100/1"
	case 13:
		return "CN/8000"
	case 18:
		return "G729/8000"
	default:
		return ""
	}
}

// parseMapping 校验线路格式，并把 G729A 别名规范化为同一种 RTP 格式。
// G726 与 AAL2-G726 保留不同名称，L16 只表示网络大端 PCM，不能接受内部 S16LE 别名。
func parseMapping(value string) (media.CodecSpec, bool, error) {
	var spec media.CodecSpec
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" {
		return spec, false, errors.New("invalid RTP encoding mapping")
	}
	rate, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || rate == 0 {
		return spec, false, errors.New("invalid RTP clock rate")
	}
	channels := uint64(1)
	if len(parts) == 3 {
		channels, err = strconv.ParseUint(parts[2], 10, 8)
		if err != nil || channels == 0 {
			return spec, false, errors.New("invalid RTP channels")
		}
	}
	name := strings.ToUpper(parts[0])
	if name == "G729A" {
		name = "G729"
	}
	spec = media.CodecSpec{Name: name, SampleRate: uint32(rate), RTPClockRate: uint32(rate), Channels: uint8(channels)}
	valid := false
	switch name {
	case "PCMU", "PCMA", "G729", "G726-16", "G726-24", "G726-32", "G726-40", "AAL2-G726-16", "AAL2-G726-24", "AAL2-G726-32", "AAL2-G726-40":
		valid = rate == 8000 && channels == 1
	case "G722":
		spec.SampleRate = 16000
		valid = rate == 8000 && channels == 1
	case "OPUS":
		valid = rate == 48000 && channels == 2
	case "L16":
		valid = audioClock(uint32(rate)) && channels <= 2
	case "TELEPHONE-EVENT", "CN":
		valid = audioClock(uint32(rate)) && channels == 1
	default:
		return spec, false, nil
	}
	if !valid {
		return spec, true, errors.New("unsupported codec clock rate or channels")
	}
	return spec, true, nil
}

// audioClock 明确当前媒体计时可处理的时钟集合，不把所有动态音频当作 8 kHz。
func audioClock(rate uint32) bool {
	return rate == 8000 || rate == 16000 || rate == 32000 || rate == 44100 || rate == 48000
}

// sameEncoding 比较线路格式身份；接收偏好的 fmtp 与 ptime 另行处理。
func sameEncoding(a, b media.CodecSpec) bool {
	return a.Name == b.Name && a.SampleRate == b.SampleRate && a.RTPClockRate == b.RTPClockRate && a.Channels == b.Channels
}

// selectFormats 按 offer 顺序选择一个主编码，验证全部已知映射，之后选择不冲突的辅助载荷。
// 未实现的 AMR、AMR-WB、EVS 等候选可被跳过；仅含这些格式时返回明确的不支持错误。
func selectFormats(payloads []int, mappings, parameters map[int]string, ptime int) (SDP, error) {
	return selectFormatsForMode(payloads, mappings, parameters, ptime, "")
}

// selectFormatsForMode 先验证每个编码的合法映射，再按实际处理能力选候选，不改变原透传选择。
func selectFormatsForMode(payloads []int, mappings, parameters map[int]string, ptime int, processing string) (SDP, error) {
	var result SDP
	formats := make(map[int]media.CodecSpec)
	selected := false
	for _, pt := range payloads {
		mapping := mappings[pt]
		assigned := staticMapping(pt)
		if mapping == "" {
			mapping = assigned
		}
		if mapping == "" {
			if pt >= 96 {
				return result, errors.New("dynamic payload requires rtpmap")
			}
			continue
		}
		spec, known, err := parseMapping(mapping)
		if err != nil {
			return result, err
		}
		if assigned != "" {
			expected, _, _ := parseMapping(assigned)
			if !known || !sameEncoding(spec, expected) {
				return result, errors.New("static payload mapping conflicts with codec")
			}
		} else if known && pt < 96 {
			return result, errors.New("codec requires a dynamic payload in 96..127")
		}
		if !known {
			continue
		}
		if spec.Name == "TELEPHONE-EVENT" {
			events, err := parseEvents(parameters[pt])
			if err != nil {
				return result, err
			}
			// 本版原生事件解析只处理 DTMF 0..15 及事件 16；未实现的命名事件不宣告。
			for event := 17; event < len(events); event++ {
				events[event] = false
			}
			spec.FMTP = formatEvents(events)
		} else {
			spec.FMTP, err = normalizeFMTP(spec.Name, parameters[pt], ptime)
			if err != nil {
				return result, err
			}
		}
		spec.PTimeMS = uint16(ptime)
		formats[pt] = spec
		if spec.Name == "TELEPHONE-EVENT" || spec.Name == "CN" {
			continue
		}
		if !validCodecPTime(spec.Name, ptime) {
			// 映射和 fmtp 已经严格校验；本实现不支持该候选的分包间隔，只排除该候选。
			// 例如 PCMU/40ms 可用时，另一 L16/40ms 候选不能导致整个 offer 被拒绝。
			continue
		}
		if !selected {
			if processing == "g711" && (ptime != 20 || (spec.Name != "PCMU" && spec.Name != "PCMA")) {
				continue
			}
			result.Payload = uint8(pt)
			result.Codec = spec
			result.PTime = ptime
			selected = true
		}
	}
	if !selected {
		return result, errors.New("no supported audio codec (AMR/AMR-WB/EVS and unspecified encodings unavailable)")
	}
	for _, pt := range payloads {
		spec, ok := formats[pt]
		if !ok {
			continue
		}
		if spec.Name == "TELEPHONE-EVENT" && spec.FMTP != "" && (result.DTMF == nil || result.DTMFClockRate != result.Codec.RTPClockRate && spec.RTPClockRate == result.Codec.RTPClockRate) {
			value := uint8(pt)
			result.DTMF = &value
			result.DTMFClockRate = spec.RTPClockRate
			result.DTMFEvents = spec.FMTP
		}
		if spec.Name == "CN" && result.CN == nil && spec.RTPClockRate == result.Codec.RTPClockRate {
			value := uint8(pt)
			result.CN = &value
			result.CNClockRate = spec.RTPClockRate
		}
	}
	return result, nil
}

// validCodecPTime 给透传分包设置明确边界；L16 限为 10/20 ms，保证 48 kHz 双声道也能进入媒体接收缓冲。
// Opus 的 3 ms 声明对应 RFC 7587 对 2.5 ms 的向上取整，内部 PCM 分帧不改变 RTP 声明。
func validCodecPTime(name string, ptime int) bool {
	if name == "L16" {
		return ptime == 10 || ptime == 20
	}
	if name == "OPUS" {
		return ptime == 3 || ptime == 5 || ptime >= 10 && ptime <= 120 && ptime%10 == 0
	}
	return ptime >= 10 && ptime <= 60 && ptime%10 == 0
}

// normalizeFMTP 对已支持格式采用明确参数白名单，拒绝重复、空值和未知扩展，避免丢弃格式约束后伪接受。
// Opus minptime 是常见终端扩展；其余 Opus 参数来自 RFC 7587，均原样保留语义并排序输出。
func normalizeFMTP(name, text string, ptime int) (string, error) {
	if name != "OPUS" && name != "G729" {
		if text != "" {
			return "", errors.New("fmtp unsupported for selected codec")
		}
		return "", nil
	}
	values := map[string]string{}
	if name == "G729" {
		values["annexb"] = "yes"
	}
	seen := map[string]bool{}
	if text != "" {
		for _, item := range strings.Split(text, ";") {
			pair := strings.SplitN(strings.TrimSpace(item), "=", 2)
			if len(pair) != 2 {
				return "", errors.New("invalid codec fmtp parameter")
			}
			key, value := strings.ToLower(strings.TrimSpace(pair[0])), strings.ToLower(strings.TrimSpace(pair[1]))
			if key == "" || value == "" || seen[key] {
				return "", errors.New("duplicate or empty codec fmtp parameter")
			}
			seen[key] = true
			if name == "G729" {
				if key != "annexb" || value != "yes" && value != "no" {
					return "", errors.New("unsupported G729 fmtp")
				}
			} else {
				n, err := strconv.Atoi(value)
				if err != nil {
					return "", errors.New("Opus fmtp requires integer values")
				}
				valid := false
				switch key {
				case "stereo", "sprop-stereo", "cbr", "useinbandfec", "usedtx":
					valid = n == 0 || n == 1
				case "maxplaybackrate", "sprop-maxcapturerate":
					valid = n >= 8000 && n <= 48000
				case "maxaveragebitrate":
					valid = n >= 6000 && n <= 510000
				case "minptime":
					valid = n >= 3 && n <= 120 && n <= ptime
				}
				if !valid {
					return "", errors.New("unsupported Opus fmtp parameter or value")
				}
				value = strconv.Itoa(n)
			}
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, ";"), nil
}

// parseEvents 将 RFC 4733 的逗号/范围表达式解析为有限集合；省略参数时只默认支持 0-15。
func parseEvents(text string) ([256]bool, error) {
	var events [256]bool
	if text == "" {
		text = "0-15"
	}
	for _, part := range strings.Split(text, ",") {
		bounds := strings.Split(strings.TrimSpace(part), "-")
		if len(bounds) < 1 || len(bounds) > 2 {
			return events, errors.New("invalid telephone-event range")
		}
		first, err := strconv.Atoi(bounds[0])
		if err != nil || first < 0 || first > 255 {
			return events, errors.New("invalid telephone-event number")
		}
		last := first
		if len(bounds) == 2 {
			last, err = strconv.Atoi(bounds[1])
			if err != nil || last < first || last > 255 {
				return events, errors.New("invalid telephone-event range")
			}
		}
		for event := first; event <= last; event++ {
			events[event] = true
		}
	}
	return events, nil
}

// formatEvents 按递增顺序压缩集合，输出确定性的线路参数；空集合返回空串而非默认扩大支持。
func formatEvents(events [256]bool) string {
	var parts []string
	for first := 0; first < len(events); first++ {
		if !events[first] {
			continue
		}
		last := first
		for last+1 < len(events) && events[last+1] {
			last++
		}
		value := strconv.Itoa(first)
		if last != first {
			value += "-" + strconv.Itoa(last)
		}
		parts = append(parts, value)
		first = last
	}
	return strings.Join(parts, ",")
}

// NegotiateAnswer 验证同格式透传能兑现的应答；没有 PT 重写、重分包或转码时不能接受格式身份变化。
// Opus 参数是每个方向的接收偏好，不能要求双方 fmtp 文本相等；应答的约束继续转发给另一端。
func NegotiateAnswer(offer, answer SDP) (SDP, error) {
	a, b := offer.CodecMetadata(), answer.CodecMetadata()
	if offer.Payload != answer.Payload || !sameEncoding(a, b) || offer.PTime != answer.PTime {
		return SDP{}, errors.New("answer requires unsupported payload remapping, repacketization or transcoding")
	}
	if a.Name != "OPUS" && a.FMTP != b.FMTP {
		// G.729 Annex B 允许应答收紧为 no，不能把 offer 已禁用的 Annex B 再打开。
		if a.Name != "G729" || a.FMTP != "annexb=yes" || b.FMTP != "annexb=no" {
			return SDP{}, errors.New("incompatible codec fmtp in answer")
		}
	}
	if answer.DTMF != nil {
		clock := offer.DTMFClockRate
		if clock == 0 {
			clock = 8000
		}
		if offer.DTMF == nil || *offer.DTMF != *answer.DTMF || clock != answer.DTMFClockRate {
			return SDP{}, errors.New("answer changed telephone-event payload or clock")
		}
		left, _ := parseEvents(offer.DTMFEvents)
		right, _ := parseEvents(answer.DTMFEvents)
		for event := range right {
			right[event] = right[event] && left[event]
		}
		answer.DTMFEvents = formatEvents(right)
		if answer.DTMFEvents == "" {
			answer.DTMF = nil
			answer.DTMFClockRate = 0
		}
	}
	if answer.CN != nil && (offer.CN == nil || *answer.CN != *offer.CN || answer.CNClockRate != offer.CNClockRate) {
		return SDP{}, errors.New("answer changed comfort-noise payload or clock")
	}
	return answer, nil
}
