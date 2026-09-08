// Package config 定义控制进程启动配置及硬资源边界，在线峰值策略只能在这些边界内收紧。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SIP 定义信令监听、固定上游、来源信任和会话超时；省略 Stream 保持原 UDP 配置。
// SIPStream 定义可选 TCP/TLS 的硬连接预算及绝对期限；零值使用保守默认值。
// TLS 仅保护信令，不能据此声明 SRTP、WebRTC、SIPS 路由或任意 SIP 扩展已经实现。
type SIPStream struct {
	TCPListen         string `json:"tcp_listen,omitempty"`         // 可选 TCP 监听，空值关闭。
	TLSListen         string `json:"tls_listen,omitempty"`         // 可选 TLS 监听，空值关闭。
	TLSCertFile       string `json:"tls_cert_file,omitempty"`      // TLS 监听证书 PEM 文件。
	TLSKeyFile        string `json:"tls_key_file,omitempty"`       // TLS 监听私钥 PEM 文件。
	TLSCAFile         string `json:"tls_ca_file,omitempty"`        // 固定 TLS 上游信任的额外 CA；空值使用系统根。
	TLSServerName     string `json:"tls_server_name,omitempty"`    // 校验上游证书的名称；空值校验上游 IP SAN。
	UpstreamTransport string `json:"upstream_transport,omitempty"` // udp、tcp 或 tls；空值默认为 udp。
	MaxConnections    int    `json:"max_connections,omitempty"`    // 包含上游和握手中的硬连接上限，默认 256。
	FrameTimeoutMS    int    `json:"frame_timeout_ms,omitempty"`   // 从首字节开始接收完整消息的绝对期限，默认 5000 毫秒。
	IdleTimeoutMS     int    `json:"idle_timeout_ms,omitempty"`    // 连接等待下一条消息的最长空闲时间，默认 300000 毫秒。
	WriteTimeoutMS    int    `json:"write_timeout_ms,omitempty"`   // 单条完整消息发送期限，默认 1000 毫秒。
}

// StreamOptions 返回独立的有效配置，避免运行中修改持久化草稿或调用者共享对象。
func (s SIP) StreamOptions() SIPStream {
	var o SIPStream
	if s.Stream != nil {
		o = *s.Stream
	}
	if o.UpstreamTransport == "" {
		o.UpstreamTransport = "udp"
	}
	if o.MaxConnections == 0 {
		o.MaxConnections = 256
	}
	if o.FrameTimeoutMS == 0 {
		o.FrameTimeoutMS = 5000
	}
	if o.IdleTimeoutMS == 0 {
		o.IdleTimeoutMS = 300000
	}
	if o.WriteTimeoutMS == 0 {
		o.WriteTimeoutMS = 1000
	}
	return o
}

type SIP struct {
	Dialplan        *SIPDialplan     `json:"dialplan,omitempty"`         // 可选冻结XML本地路由，缺省保留既有行为。
	LocalExtensions []string         `json:"local_extensions,omitempty"` // 精确匹配的本地自动应答号码；空值保留固定上游桥接。
	TrunkAuth       *SIPTrunkAuth    `json:"trunk_auth,omitempty"`       // 固定上游认证；配置只存密码文件路径，不存密码。
	Registration    *SIPRegistration `json:"registration,omitempty"`     // 固定上游注册客户端；空值不注册。
	Stream          *SIPStream       `json:"stream,omitempty"`           // 可选可靠信令传输；与媒体加密能力分开配置。
	Listen          string           `json:"listen"`                     // 本地 UDP 监听地址与端口。
	Advertise       string           `json:"advertise"`                  // 向对端宣告的 SIP 地址与端口。
	Upstream        string           `json:"upstream"`                   // 固定 B 腿上游地址与端口。
	TrustedNetworks []string         `json:"trusted_networks"`           // 允许初始信令来源的 IPv4 CIDR 列表。
	SetupTimeoutMS  int              `json:"setup_timeout_ms"`           // 呼叫建立阶段超时，毫秒。
	AckTimeoutMS    int              `json:"ack_timeout_ms"`             // 最终响应等待 ACK 的最长时间，毫秒。
	MaxCallSeconds  int              `json:"max_call_seconds"`           // 成功 ACK 后允许的最大通话时长，秒。
}

// Media 定义 Rust 分片启动参数、端口隔离与单进程压力保护。
type Media struct {
	Processing                string   `json:"processing,omitempty"`           // 空/relay保留透传；g711显式启用20ms双腿PCM处理，当前不用于本地IVR。
	PlaybackRoot              string   `json:"playback_root,omitempty"`        // 可选本地 WAV 根目录；空值关闭文件播放，运行中不热切换。
	AdaptiveAdmission         bool     `json:"adaptive_admission"`             // 是否按分片 CPU、丢包和队列压力限制新准入。
	AdmissionCPUHigh          float64  `json:"admission_cpu_high"`             // 单分片拒绝阈值，以占用 CPU 核心数计。
	AdmissionCPULow           float64  `json:"admission_cpu_low"`              // 单分片恢复阈值，须低于拒绝阈值。
	ConnectSockets            bool     `json:"connect_sockets"`                // 是否把媒体 UDP socket 连接到固定远端。
	Binary                    string   `json:"binary"`                         // Rust 媒体可执行文件路径。
	BindIP                    string   `json:"bind_ip"`                        // 媒体 socket 本地绑定地址。
	AdvertiseIP               string   `json:"advertise_ip"`                   // SDP 中宣告的可达媒体地址。
	PortStart                 int      `json:"port_start"`                     // 整体媒体端口范围起点，偶数且包含边界。
	PortEnd                   int      `json:"port_end"`                       // 整体媒体端口范围终点，包含边界。
	ExcludedPortBlocks        []int    `json:"excluded_port_blocks,omitempty"` // 禁止分配的四端口块起点；按 PortStart 对齐，空值保持连续范围模型。
	Workers                   int      `json:"workers"`                        // Rust 媒体进程数，各占独立端口分区。
	ReceiveBufferBytes        int      `json:"receive_buffer_bytes"`           // 每个媒体 socket 请求的内核接收缓冲字节数。
	PortReuseDelayMS          int      `json:"port_reuse_delay_ms"`            // 媒体端口回收后的隔离时间，毫秒。
	MaxPacketsPerSecondPerLeg int      `json:"max_packets_per_second_per_leg"` // 每条媒体腿的包处理速率预算。
	AllowedRemoteNetworks     []string `json:"allowed_remote_networks"`        // 允许媒体目的地址的 IPv4 CIDR 列表。
	CPUCores                  []int    `json:"cpu_cores"`                      // Linux 可选绑核列表，必须与进程数量一致。
}

// Limits 是本次启动不可在线抬高的资源上限，改变它需要重启应用待启动配置。
type Limits struct {
	MaxCalls        int `json:"max_calls"`        // 整体媒体资源预留上限。
	CallsPerSecond  int `json:"calls_per_second"` // 每秒新呼叫令牌补充速率硬上限。
	BurstCalls      int `json:"burst_calls"`      // 瞬时新呼叫令牌容量硬上限。
	MaxTransactions int `json:"max_transactions"` // 控制面会话/缓存容量和出站事务容量的检查边界。
}

// Admin 定义本地可视化管理和观测入口；当前版本只允许回环监听。
type Admin struct {
	Listen string `json:"listen"` // 管理 HTTP 地址与端口。
}

// Journal 定义追加日志存储位置及非阻塞提交队列。
type Journal struct {
	Path          string `json:"path"`           // 事件日志路径，也用于关联管理状态文件。
	QueueCapacity int    `json:"queue_capacity"` // 待写事件数量上限。
}

// Config 是完整启动参数；运行中由 Server 冻结，管理草稿另存并在下次启动应用。
type Config struct {
	// SourcePath 记录启动配置位置，仅用于管理状态绑定，不向 JSON 暴露本机路径。
	SourcePath string     `json:"-"`
	SIP        SIP        `json:"sip"`                  // 信令地址、信任与定时器。
	Media      Media      `json:"media"`                // 媒体分片与 socket 配置。
	Limits     Limits     `json:"limits"`               // 本次启动硬容量边界。
	Admin      Admin      `json:"admin"`                // 本地管理入口。
	Journal    Journal    `json:"journal"`              // 业务日志。
	PCMStream  *PCMStream `json:"pcm_stream,omitempty"` // 可选私有流式音频入口及固定连接/句柄预算，省略关闭。
}

// Load 读取限定大小的单个 JSON 对象，拒绝未知字段并保留来源路径供管理状态绑定。
func Load(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return c, errors.New("configuration must contain exactly one JSON object")
	}
	c.SourcePath = path
	return c, c.Validate()
}

// Validate 检查地址、白名单、端口容量与超时等静态约束，不绑定 socket 或启动进程。
func (c Config) Validate() error {
	if err := c.validatePCMStream(); err != nil {
		return err
	}
	if c.Media.Processing != "" && c.Media.Processing != "relay" && c.Media.Processing != "g711" {
		return errors.New("media.processing must be relay or g711")
	}
	if err := c.SIP.validateDialplan(); err != nil {
		return err
	}
	if err := c.SIP.validateTrunk(); err != nil {
		return err
	}
	o := c.SIP.StreamOptions()
	if o.UpstreamTransport != "udp" && o.UpstreamTransport != "tcp" && o.UpstreamTransport != "tls" {
		return errors.New("SIP upstream_transport must be udp, tcp or tls")
	}
	if o.MaxConnections < 1 || o.MaxConnections > 4096 || o.FrameTimeoutMS < 100 || o.FrameTimeoutMS > 30000 || o.IdleTimeoutMS < 1000 || o.IdleTimeoutMS > 86400000 || o.WriteTimeoutMS < 20 || o.WriteTimeoutMS > 5000 {
		return errors.New("invalid SIP stream resource limits")
	}
	if o.TLSListen != "" && (o.TLSCertFile == "" || o.TLSKeyFile == "") {
		return errors.New("TLS SIP listener requires certificate and private key")
	}
	for name, value := range map[string]string{"sip.stream.tcp_listen": o.TCPListen, "sip.stream.tls_listen": o.TLSListen} {
		if value == "" {
			continue
		}
		a, e := netip.ParseAddrPort(value)
		if e != nil || !a.Addr().Is4() || a.Port() == 0 {
			return fmt.Errorf("%s must be literal IPv4 with nonzero port", name)
		}
		if value == c.Admin.Listen || value == c.SIP.Upstream {
			return errors.New("SIP stream listener overlaps admin or upstream")
		}
	}
	if o.TCPListen != "" && o.TCPListen == o.TLSListen {
		return errors.New("TCP and TLS SIP listeners must use distinct addresses")
	}
	addresses := make(map[string]netip.AddrPort)
	for name, value := range map[string]string{"sip.listen": c.SIP.Listen, "sip.advertise": c.SIP.Advertise, "sip.upstream": c.SIP.Upstream, "admin.listen": c.Admin.Listen} {
		a, e := netip.ParseAddrPort(value)
		if e != nil || !a.Addr().Is4() || a.Port() == 0 {
			return fmt.Errorf("%s must be a literal IPv4 address and nonzero port", name)
		}
		addresses[name] = a
	}
	if !addresses["admin.listen"].Addr().IsLoopback() {
		return errors.New("admin API must listen on loopback in this release")
	}
	if c.SIP.Upstream == c.SIP.Listen || c.SIP.Upstream == c.SIP.Advertise {
		return errors.New("upstream must not point to this server")
	}
	if err := validateLocalExtensions(c.SIP.LocalExtensions); err != nil {
		return err
	}
	for _, a := range []netip.Addr{addresses["sip.advertise"].Addr(), addresses["sip.upstream"].Addr()} {
		if !UsableIP(a) {
			return errors.New("invalid SIP address")
		}
	}
	for _, text := range []string{c.Media.BindIP, c.Media.AdvertiseIP} {
		a, e := netip.ParseAddr(text)
		if e != nil || !a.Is4() {
			return errors.New("media addresses must be literal IPv4")
		}
	}
	if !UsableIP(netip.MustParseAddr(c.Media.AdvertiseIP)) {
		return errors.New("invalid advertised media IP")
	}
	for _, list := range [][]string{c.SIP.TrustedNetworks, c.Media.AllowedRemoteNetworks} {
		if len(list) == 0 {
			return errors.New("explicit signaling and media allowlists are required")
		}
		for _, text := range list {
			p, e := netip.ParsePrefix(text)
			if e != nil || !p.Addr().Is4() {
				return fmt.Errorf("invalid IPv4 allowlist prefix %q", text)
			}
		}
	}
	m, l := c.Media, c.Limits
	// 这里只检查静态路径形状；媒体进程启动时打开目录并逐级拒绝符号链接，配置校验不做阻塞文件读取。
	if m.PlaybackRoot != "" && (!filepath.IsAbs(m.PlaybackRoot) || filepath.Clean(m.PlaybackRoot) != m.PlaybackRoot || m.PlaybackRoot == string(filepath.Separator) || len(m.PlaybackRoot) > 4096 || strings.ContainsAny(m.PlaybackRoot, "\x00\r\n")) {
		return errors.New("media playback_root must be a clean absolute directory path other than root")
	}
	if m.AdaptiveAdmission && (m.AdmissionCPULow <= 0 || m.AdmissionCPUHigh > 1 || m.AdmissionCPUHigh < .5 || m.AdmissionCPULow >= m.AdmissionCPUHigh) {
		return errors.New("invalid adaptive admission CPU thresholds")
	}
	if m.Workers < 1 || m.Workers > 64 || l.MaxCalls < m.Workers {
		return errors.New("invalid worker count or call limit")
	}
	if m.PortStart < 1024 || m.PortStart%2 != 0 || m.PortEnd > 65535 || m.PortEnd <= m.PortStart {
		return errors.New("media range must begin at an even unprivileged port")
	}
	// 每通电话使用 A/B 两腿各自的 RTP/RTCP，共四个端口，尾部不足一个块的端口不可用。
	blocks := (m.PortEnd - m.PortStart + 1) / 4
	excluded := make(map[int]bool, len(m.ExcludedPortBlocks))
	for _, base := range m.ExcludedPortBlocks {
		if base < m.PortStart || base+3 > m.PortEnd || (base-m.PortStart)%4 != 0 || excluded[base] {
			return errors.New("excluded media blocks must be unique complete blocks aligned to port_start")
		}
		excluded[base] = true
	}
	blocks -= len(excluded)
	if l.MaxCalls > blocks {
		return errors.New("media port range cannot accommodate four ports per call")
	}
	if l.CallsPerSecond < 1 || l.CallsPerSecond > 100000 || l.BurstCalls < 1 || l.MaxTransactions < l.MaxCalls*2 {
		return errors.New("invalid admission or transaction limits")
	}
	if m.PortReuseDelayMS < 0 || m.PortReuseDelayMS > 60000 {
		return errors.New("invalid port quarantine")
	}
	// 全局额外预留 CPS 乘隔离期的端口块；这不等同于证明每个分片在任意周转分布下都足够。
	if l.MaxCalls+(l.CallsPerSecond*m.PortReuseDelayMS+999)/1000 > blocks {
		return errors.New("media ports lack headroom for configured CPS and port quarantine")
	}
	if m.ReceiveBufferBytes < 16384 || m.ReceiveBufferBytes > 1048576 || m.MaxPacketsPerSecondPerLeg < 100 || m.MaxPacketsPerSecondPerLeg > 10000 {
		return errors.New("invalid media buffer or packet budget")
	}
	// 绑核是 Linux 专属启动能力，不能把开发机上接受的配置误当作已设置成功。
	if len(m.CPUCores) > 0 {
		if runtime.GOOS != "linux" || len(m.CPUCores) != m.Workers {
			return errors.New("CPU affinity requires Linux and one core per worker")
		}
		for _, core := range m.CPUCores {
			if core < 0 || core > 1023 {
				return errors.New("invalid CPU core")
			}
		}
	}
	if c.SIP.SetupTimeoutMS < 1000 || c.SIP.SetupTimeoutMS > 180000 || c.SIP.AckTimeoutMS < 1000 || c.SIP.AckTimeoutMS > 64000 || c.SIP.MaxCallSeconds < 1 || c.SIP.MaxCallSeconds > 86400 {
		return errors.New("invalid SIP timer configuration")
	}
	if c.Journal.Path == "" || c.Journal.QueueCapacity < 64 || c.Journal.QueueCapacity > 65536 {
		return errors.New("invalid journal configuration")
	}
	if m.Binary == "" {
		return errors.New("media binary path is required")
	}
	p := int(addresses["sip.listen"].Port())
	if p >= m.PortStart && p <= m.PortEnd {
		return errors.New("SIP port overlaps media range")
	}
	return nil
}

// UsableIP 筛掉本原型不能作为远端使用的 IPv4 地址；不替代用途相关白名单检查。
func UsableIP(ip netip.Addr) bool {
	return ip.Is4() && !ip.IsUnspecified() && !ip.IsMulticast() && ip.String() != "255.255.255.255"
}

// Allowed 检查地址是否属于任一有效前缀；无匹配时默认拒绝。
func Allowed(ip netip.Addr, networks []string) bool {
	for _, text := range networks {
		prefix, e := netip.ParsePrefix(text)
		if e == nil && prefix.Contains(ip) {
			return true
		}
	}
	return false
}
