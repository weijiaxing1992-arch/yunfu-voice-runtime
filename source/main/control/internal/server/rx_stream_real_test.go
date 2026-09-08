package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
)

// rxSIPTrace只记录测试拥有的真实线缆和SDK输出；有界落盘失败不能悄悄截断后报通过。
type rxSIPTrace struct {
	mu             sync.Mutex
	file           *os.File
	started        time.Time
	entries, bytes int
	err            error
}

func (r *rxSIPTrace) record(kind string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	line, err := json.Marshal(map[string]any{"kind": kind, "elapsed_ns": time.Since(r.started).Nanoseconds(), "utc": time.Now().UTC().Format(time.RFC3339Nano), "value": value})
	if err != nil {
		r.err = err
		return
	}
	r.entries++
	r.bytes += len(line) + 1
	if r.entries > 4096 || r.bytes > 8<<20 {
		r.err = errors.New("RX SIP原证据超过固定预算")
		return
	}
	_, r.err = r.file.Write(append(line, '\n'))
}

func (r *rxSIPTrace) finish(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.file.Sync(); err != nil {
		t.Error(err)
	}
	if err := r.file.Close(); err != nil {
		t.Error(err)
	}
	if r.err != nil {
		t.Error(r.err)
	}
}

// rxSIPHash保留实际Go测试可执行文件和Rust候选的身份，不把它们说成主9080实例。
func rxSIPHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// rxSIPClock使用RXS2约定的公开同机时钟；Unix常量值在两个明确平台内固定。
func rxSIPClock() (uint16, uint64, error) {
	var domain uint16
	var clock int32
	switch runtime.GOOS {
	case "linux":
		domain, clock = 1, 1 // CLOCK_MONOTONIC。
	case "darwin":
		domain, clock = 2, 8 // CLOCK_UPTIME_RAW，不是Darwin CLOCK_MONOTONIC。
	default:
		return 0, 0, errors.New("未定义RXS2共享时钟的平台")
	}
	var ts unix.Timespec
	if err := unix.ClockGettime(clock, &ts); err != nil {
		return 0, 0, err
	}
	if ts.Sec < 0 || ts.Nsec < 0 || ts.Nsec >= 1_000_000_000 || uint64(ts.Sec) > (math.MaxUint64-uint64(ts.Nsec))/1_000_000_000 {
		return 0, 0, errors.New("共享时钟数值非法")
	}
	ns := uint64(ts.Sec)*1_000_000_000 + uint64(ts.Nsec)
	if ns == 0 {
		return 0, 0, errors.New("共享时钟为零")
	}
	return domain, ns, nil
}

// rxSIPReserve仅在4000..4799中同时预留自有端口，禁止随机端口落入主服务媒体范围。
func rxSIPReserve(t *testing.T) (int, []*net.UDPConn, net.Listener) {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		base := 4000 + ((os.Getpid()+attempt)%50)*16
		var sockets []*net.UDPConn
		for offset := 0; offset < 10; offset++ {
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: base + offset})
			if err != nil {
				break
			}
			sockets = append(sockets, c)
		}
		if len(sockets) == 10 {
			listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", base+10))
			if err == nil {
				return base, sockets, listener
			}
		}
		for _, c := range sockets {
			_ = c.Close()
		}
	}
	t.Fatal("4000..4799中没有完整隔离SIP/媒体/管理端口块")
	return 0, nil, nil
}

func rxSIPWait(t *testing.T, duration time.Duration, condition func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for !condition() {
		if !time.Now().Before(deadline) {
			t.Fatal("有界等待失败：", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func rxSIPSend(t *testing.T, trace *rxSIPTrace, source *net.UDPConn, target *net.UDPAddr, raw []byte, kind string) {
	t.Helper()
	trace.record(kind, map[string]any{"source": source.LocalAddr().String(), "target": target.String(), "hex": hex.EncodeToString(raw)})
	n, err := source.WriteToUDP(raw, target)
	if err != nil || n != len(raw) {
		t.Fatalf("真实%s发送失败 n=%d: %v", kind, n, err)
	}
}

// rxSIPResponse保留每一条实际响应，再检查指定事务；不能丢弃非预期错误帧冒充成功。
func rxSIPResponse(t *testing.T, trace *rxSIPTrace, socket *net.UDPConn, target *net.UDPAddr, callID, method string) *sip.Message {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for count := 0; count < 16; count++ {
		_ = socket.SetReadDeadline(deadline)
		buffer := make([]byte, sip.MaxMessageSize+1)
		n, peer, err := socket.ReadFromUDP(buffer)
		if err != nil {
			t.Fatal("真实SIP响应缺失：", err)
		}
		trace.record("sip_receive", map[string]any{"source": peer.String(), "target": socket.LocalAddr().String(), "hex": hex.EncodeToString(buffer[:n])})
		if peer.String() != target.String() {
			t.Fatal("SIP回应来自非测试服务器")
		}
		m, err := sip.Parse(buffer[:n])
		if err != nil || m.CallID() != callID {
			t.Fatalf("响应身份/格式错误：%v", err)
		}
		_, gotMethod, err := m.CSeq()
		if err != nil || gotMethod != method || m.Status == 0 {
			t.Fatal("响应事务方法不匹配")
		}
		if m.Status >= 200 {
			return m
		}
	}
	t.Fatal("SIP临时响应超过固定预算")
	return nil
}

func rxSIPSDP(payload uint8, rtp, rtcp int) []byte {
	law := "PCMU"
	if payload == 8 {
		law = "PCMA"
	}
	return []byte(fmt.Sprintf("v=0\r\no=- 7 1 IN IP4 127.0.0.1\r\ns=rx-sip-real\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP %d 101 13\r\na=rtpmap:%d %s/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-15\r\na=rtpmap:13 CN/8000\r\na=rtcp:%d\r\na=ptime:20\r\na=sendrecv\r\n", rtp, payload, payload, law, rtcp))
}

// rxSIPG711从输入码字按标准量化律独立展开；不调用生产Codec或待测输出生成期望。
func rxSIPG711(payload uint8, value byte) int16 {
	if payload == 0 {
		code := ^value
		magnitude := (((int(code&15) << 3) + 132) << ((code >> 4) & 7)) - 132
		if code&128 != 0 {
			return int16(-magnitude)
		}
		return int16(magnitude)
	}
	code := value ^ 0x55
	magnitude := (int(code&15) << 4) + 8
	exponent := (code >> 4) & 7
	if exponent != 0 {
		magnitude = (magnitude + 256) << (exponent - 1)
	}
	if code&128 == 0 {
		return int16(-magnitude)
	}
	return int16(magnitude)
}

func rxSIPStat(t *testing.T, values map[string]any, key string) uint64 {
	t.Helper()
	v, ok := values[key].(float64)
	if !ok || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v >= float64(math.MaxUint64) {
		t.Fatalf("真实stats缺少非负整数 %s=%v", key, values[key])
	}
	return uint64(v)
}

type rxSIPDelivered struct {
	Frame   media.RXFrame
	Domain  uint16
	ClockNS uint64
}
type rxSIPReadResult struct {
	Frames []rxSIPDelivered
	Err    error
}

// TestRealRXServerSIPAuthorizationSamplesAndCleanup穿过真正Server.Run和SIP UDP授权，不修改Call字段。
// 两个显式环境变量都缺失时说明真实验收未执行；只设置一个、候选缺失、能力不符均失败。
func TestRealRXServerSIPAuthorizationSamplesAndCleanup(t *testing.T) {
	mediaPath, artifacts := os.Getenv("RUSTSWITCH_REAL_MEDIA"), os.Getenv("RUSTSWITCH_RX_SIP_ARTIFACTS")
	if mediaPath == "" && artifacts == "" {
		t.Skip("未显式设置Rust候选和RX SIP原证据目录；真实验收未执行")
	}
	if mediaPath == "" || artifacts == "" {
		t.Fatal("真实RX SIP验收必须同时设置RUSTSWITCH_REAL_MEDIA和RUSTSWITCH_RX_SIP_ARTIFACTS")
	}
	mediaPath, err := filepath.Abs(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	controlPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mediaBefore, err := rxSIPHash(mediaPath)
	if err != nil {
		t.Fatal("真实Rust候选不可读：", err)
	}
	controlBefore, err := rxSIPHash(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, connected := range []bool{false, true} {
		for _, payload := range []uint8{0, 8} {
			t.Run(fmt.Sprintf("payload_%d_connected_%t", payload, connected), func(t *testing.T) {
				directory := filepath.Join(artifacts, fmt.Sprintf("payload_%d_connected_%t", payload, connected))
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal("必须使用全新原证据子目录：", err)
				}
				file, err := os.OpenFile(filepath.Join(directory, "wire.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				trace := &rxSIPTrace{file: file, started: time.Now()}
				defer trace.finish(t)
				trace.record("identity", map[string]any{"test": t.Name(), "control": controlPath, "control_sha256": controlBefore, "media": mediaPath, "media_sha256": mediaBefore, "scope": "自有隔离Server SIP授权+真实Rust RX，不是主9080、原版FS差分、供应商或容量验收"})
				base, sockets, adminReservation := rxSIPReserve(t)
				defer func() {
					for _, c := range sockets {
						_ = c.Close()
					}
					_ = adminReservation.Close()
				}()
				cfg, err := config.Load("../../../config/local.json")
				if err != nil {
					t.Fatal(err)
				}
				cfg.SourcePath = ""
				cfg.Journal.Path = filepath.Join(directory, "events.jsonl")
				cfg.SIP.Listen = fmt.Sprintf("127.0.0.1:%d", base+4)
				cfg.SIP.Advertise = cfg.SIP.Listen
				cfg.SIP.Upstream = fmt.Sprintf("127.0.0.1:%d", base+9)
				cfg.SIP.Stream, cfg.SIP.Registration, cfg.SIP.TrunkAuth, cfg.SIP.Dialplan = nil, nil, nil, nil
				cfg.SIP.LocalExtensions = []string{"1000"}
				cfg.SIP.SetupTimeoutMS, cfg.SIP.AckTimeoutMS = 3000, 3000
				cfg.SIP.MaxCallSeconds = 60
				cfg.Admin.Listen = fmt.Sprintf("127.0.0.1:%d", base+10)
				cfg.PCMStream = nil
				cfg.Media.Binary, cfg.Media.Processing, cfg.Media.PlaybackRoot = mediaPath, "g711", ""
				cfg.Media.Workers, cfg.Media.PortStart, cfg.Media.PortEnd = 1, base, base+3
				cfg.Media.CPUCores, cfg.Media.ExcludedPortBlocks = nil, nil
				cfg.Media.PortReuseDelayMS, cfg.Media.AdaptiveAdmission, cfg.Media.ConnectSockets = 0, false, connected
				cfg.Media.MaxPacketsPerSecondPerLeg = 1000
				cfg.Limits.MaxCalls, cfg.Limits.CallsPerSecond, cfg.Limits.BurstCalls, cfg.Limits.MaxTransactions = 1, 2, 2, 32
				if err := cfg.Validate(); err != nil {
					t.Fatal(err)
				}
				trace.record("actual_config", cfg)
				for _, c := range sockets[:5] {
					_ = c.Close()
				}
				_ = adminReservation.Close()
				ctx, cancel := context.WithCancel(context.Background())
				s, err := New(ctx, cfg)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				created := make(chan string, 4)
				s.SetCompatibilityEvents(func(name string, headers map[string]string, body []byte) {
					trace.record("compatibility_event", map[string]any{"name": name, "headers": headers, "body_hex": hex.EncodeToString(body)})
					if name == "CHANNEL_CREATE" {
						select {
						case created <- headers["Unique-ID"]:
						default:
							trace.record("event_error", "CHANNEL_CREATE超出单腿预算")
						}
					}
				})
				stop, done := make(chan struct{}), make(chan error, 1)
				runFinished, normalShutdown := false, false
				go func() { done <- s.Run(stop) }()
				var readerCancel context.CancelFunc
				var readerDone chan rxSIPReadResult
				readerFinished := false
				defer func() {
					if readerCancel != nil {
						readerCancel()
					}
					if readerDone != nil && !readerFinished {
						select {
						case result := <-readerDone:
							trace.record("cleanup_reader", map[string]any{"error": fmt.Sprint(result.Err), "frames": len(result.Frames)})
						case <-time.After(2 * time.Second):
							t.Error("测试读取协程未结束")
						}
					}
					cancel()
					if !runFinished {
						select {
						case runErr := <-done:
							trace.record("cleanup_run", fmt.Sprint(runErr))
						case <-time.After(5 * time.Second):
							t.Error("真实Server.Run未退出")
						}
					}
					s.Close()
					mediaAfter, e1 := rxSIPHash(mediaPath)
					controlAfter, e2 := rxSIPHash(controlPath)
					trace.record("cleanup", map[string]any{"normal_server_drain": normalShutdown, "pool_close_policy": "existing supervisor closes pipes and invokes Kill+Wait after call resources are released; not proof of Rust exit code zero", "control_sha256_after": controlAfter, "media_sha256_after": mediaAfter, "active_calls": s.Stats.Active.Load(), "established_calls": s.Stats.Established.Load(), "active_timers": s.Stats.Timers.Load(), "test_failed": t.Failed()})
					if e1 != nil || e2 != nil || mediaAfter != mediaBefore || controlAfter != controlBefore {
						t.Error("运行期间二进制身份变化", e1, e2)
					}
					if !normalShutdown {
						t.Error("未完成正常SIP/Server资源清理")
					}
				}()
				capability := s.pool.Workers[0].CapabilitySnapshot()
				trace.record("worker_ready", capability)
				if !capability.Healthy || capability.Generation == 0 || !s.pool.Workers[0].SupportsRX() {
					t.Fatal("真实候选没有就绪的RXS2能力")
				}
				target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: base + 4}
				caller, stranger, rtp := sockets[5], sockets[6], sockets[7]
				callID := fmt.Sprintf("rx-real-%d-%d-%t", os.Getpid(), payload, connected)
				from, to := "<sip:caller@local>;tag=rx-caller", "<sip:1000@local>"
				uri := "sip:1000@" + cfg.SIP.Listen
				rxSIPSend(t, trace, caller, target, sip.Request("INVITE", uri, caller.LocalAddr().String(), "z9hG4bK-rx-invite", from, to, callID, 1, rxSIPSDP(payload, base+7, base+8)), "sip_send")
				answer := rxSIPResponse(t, trace, caller, target, callID, "INVITE")
				if answer.Status != 200 {
					t.Fatalf("真实本地INVITE失败 status=%d", answer.Status)
				}
				mediaPort := 0
				for _, line := range strings.Split(string(answer.Body), "\r\n") {
					if strings.HasPrefix(line, "m=audio ") {
						fields := strings.Fields(line)
						if len(fields) < 4 {
							t.Fatal("SDP媒体行截断")
						}
						mediaPort, err = strconv.Atoi(fields[1])
						if err != nil || fields[2] != "RTP/AVP" || fields[3] != strconv.Itoa(int(payload)) {
							t.Fatal("SDP媒体合同不符")
						}
					}
				}
				if mediaPort != base || !strings.Contains(string(answer.Body), "c=IN IP4 127.0.0.1") {
					t.Fatal("实际SDP不指向测试预留媒体块")
				}
				var uuid string
				select {
				case uuid = <-created:
				case <-time.After(time.Second):
					t.Fatal("实际CHANNEL_CREATE没有UUID")
				}
				if len(uuid) != 36 {
					t.Fatal("通道UUID格式错误")
				}
				callCtx, callCancel := context.WithTimeout(ctx, 4*time.Second)
				defer callCancel()
				if value := s.CompatibilityAPI(callCtx, "uuid_exists", uuid); strings.TrimSpace(value) != "true" {
					t.Fatal("真实UUID不存在：", value)
				}
				beforeACK, reply, subscribeErr := s.SubscribeRX(callCtx, uuid, 1)
				trace.record("before_ack_subscribe", map[string]any{"reply": reply, "error": fmt.Sprint(subscribeErr), "has_handle": beforeACK != nil})
				if beforeACK != nil || !errors.Is(subscribeErr, ErrRXNotEligible) || s.Stats.Established.Load() != 0 {
					t.Fatal("ACK前错误开放RX")
				}
				ack := sip.Request("ACK", uri, stranger.LocalAddr().String(), "z9hG4bK-rx-wrong-ack", from, answer.Header("to"), callID, 1, nil)
				rxSIPSend(t, trace, stranger, target, ack, "sip_wrong_source_ack")
				// 同一UDP接收循环后的OPTIONS回应构成处理屏障；不用睡眠猜测ACK是否已被消费。
				barrierID := callID + "-barrier"
				rxSIPSend(t, trace, stranger, target, sip.Request("OPTIONS", uri, stranger.LocalAddr().String(), "z9hG4bK-rx-barrier", from, to, barrierID, 1, nil), "sip_send")
				if rxSIPResponse(t, trace, stranger, target, barrierID, "OPTIONS").Status != 200 {
					t.Fatal("错误ACK后的真实信令屏障失败")
				}
				wrongACK, reply, subscribeErr := s.SubscribeRX(callCtx, uuid, 1)
				trace.record("wrong_ack_subscribe", map[string]any{"reply": reply, "error": fmt.Sprint(subscribeErr), "has_handle": wrongACK != nil})
				if wrongACK != nil || !errors.Is(subscribeErr, ErrRXNotEligible) || s.Stats.Established.Load() != 0 {
					t.Fatal("错误来源ACK开放RX")
				}
				rxSIPSend(t, trace, caller, target, sip.Request("ACK", uri, caller.LocalAddr().String(), "z9hG4bK-rx-good-ack", from, answer.Header("to"), callID, 1, nil), "sip_send")
				rxSIPWait(t, time.Second, func() bool { return s.Stats.Established.Load() == 1 }, "真实ACK未建立本地通话")
				h, reply, err := s.SubscribeRX(callCtx, uuid, 1)
				trace.record("authorized_subscribe", map[string]any{"reply": reply, "error": fmt.Sprint(err)})
				if err != nil || h == nil || !reply.OK || reply.State != "active" || h.UUID() != uuid || h.SubscriptionID() != 1 {
					t.Fatal("已ACK的真实媒体订阅失败：", err, reply)
				}
				progress := make(chan rxSIPDelivered, 64)
				readerDone = make(chan rxSIPReadResult, 1)
				readerCtx, rc := context.WithCancel(ctx)
				readerCancel = rc
				go func() {
					var records []rxSIPDelivered
					for len(records) < 256 {
						frame, readErr := h.Read(readerCtx)
						if readErr != nil {
							readerDone <- rxSIPReadResult{records, readErr}
							return
						}
						domain, now, clockErr := rxSIPClock()
						if clockErr != nil {
							readerDone <- rxSIPReadResult{records, clockErr}
							return
						}
						got := rxSIPDelivered{frame, domain, now}
						records = append(records, got)
						trace.record("rx_sdk_delivered", got)
						select {
						case progress <- got:
						case <-readerCtx.Done():
							readerDone <- rxSIPReadResult{records, readerCtx.Err()}
							return
						}
					}
					readerDone <- rxSIPReadResult{records, errors.New("SDK记录超出单路固定预算")}
				}()
				const frames = 8
				var input [frames][172]byte
				start := time.Now()
				for index := range input {
					deadline := start.Add(time.Duration(index) * 20 * time.Millisecond)
					if delay := time.Until(deadline); delay > 0 {
						time.Sleep(delay)
					}
					late := time.Since(deadline)
					trace.record("generator_deadline", map[string]any{"frame": index, "late_ns": late.Nanoseconds()})
					if late >= 20*time.Millisecond {
						t.Fatal("实际输入发生器晚到一帧，不能追赶伪造通过")
					}
					packet := input[index][:]
					packet[0], packet[1] = 0x80, payload
					if index == 0 {
						packet[1] |= 0x80
					}
					binary.BigEndian.PutUint16(packet[2:], uint16(65532+index))
					binary.BigEndian.PutUint32(packet[4:], uint32(uint64(0xfffffc00)+uint64(index)*160))
					binary.BigEndian.PutUint32(packet[8:], 0x7a123456)
					for sample := 0; sample < 160; sample++ {
						packet[12+sample] = byte(index*47 + sample*13 + 31)
					}
					rxSIPSend(t, trace, rtp, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: mediaPort}, packet, "rtp_send")
				}
				decoded := 0
				deadline := time.NewTimer(2 * time.Second)
				defer deadline.Stop()
				for decoded < frames {
					select {
					case got := <-progress:
						if got.Frame.Kind == media.RXDecoded {
							decoded++
						}
					case result := <-readerDone:
						readerFinished = true
						t.Fatal("SIP仍活动时RX读取提前终止：", result.Err)
					case <-deadline.C:
						t.Fatal("真实RX没有完整八帧")
					}
				}
				status, statusErr := h.Status(callCtx)
				trace.record("rx_active_status", map[string]any{"reply": status, "error": fmt.Sprint(statusErr), "snapshot": h.Snapshot()})
				if statusErr != nil || !status.OK || status.State != "active" {
					t.Fatal("实际RX活动状态不可读", statusErr)
				}
				rxSIPSend(t, trace, caller, target, sip.Request("BYE", uri, caller.LocalAddr().String(), "z9hG4bK-rx-bye", from, answer.Header("to"), callID, 2, nil), "sip_send")
				if rxSIPResponse(t, trace, caller, target, callID, "BYE").Status != 200 {
					t.Fatal("实际BYE失败")
				}
				var result rxSIPReadResult
				select {
				case result = <-readerDone:
					readerFinished = true
				case <-time.After(2 * time.Second):
					t.Fatal("BYE未唤醒RX读者")
				}
				trace.record("rx_bye_revocation", map[string]any{"error": fmt.Sprint(result.Err), "snapshot": h.Snapshot()})
				if !errors.Is(result.Err, ErrRXHandleClosed) && !errors.Is(result.Err, ErrRXHandleRetired) {
					t.Fatal("BYE读取结果不是明确撤销：", result.Err)
				}
				count := 0
				var sequenceBase, timestampBase, sourceGeneration, sourceSegment uint64
				for _, got := range result.Frames {
					f := got.Frame
					if f.Kind == media.RXExportGap || f.Kind == media.RXObservationFailed || f.Kind == media.RXExportFailed {
						t.Fatal("正常单路存在丢失/观察故障")
					}
					if f.Kind != media.RXDecoded {
						continue
					}
					if count >= frames {
						t.Fatal("SDK重复输出真实输入帧")
					}
					if count == 0 {
						sequenceBase, timestampBase, sourceGeneration, sourceSegment = f.RTPSequence, f.RTPTimestamp, f.SourceGeneration, f.SourceSegment
					}
					if f.SSRC != 0x7a123456 || uint16(f.RTPSequence) != uint16(65532+count) || uint32(f.RTPTimestamp) != uint32(uint64(0xfffffc00)+uint64(count)*160) || f.RTPSequence != sequenceBase+uint64(count) || f.RTPTimestamp != timestampBase+uint64(count)*160 {
						t.Fatal("实际RX来源/完整展开时间线不匹配")
					}
					if sourceGeneration == 0 || sourceSegment == 0 || f.SourceGeneration != sourceGeneration || f.SourceSegment != sourceSegment || f.SampleCount != 160 || f.SampleRate != 8000 || f.RTPClockRate != 8000 || f.Flags&15 != 15 {
						t.Fatal("RX格式/来源代次/位置无效")
					}
					if f.ClockDomain != got.Domain || f.ObservationLowerBoundNS == 0 || f.ExpiresAtNS != f.ObservationLowerBoundNS+100_000_000 || got.ClockNS >= f.ExpiresAtNS {
						t.Fatal("SDK交付原帧已经过期或跨时钟域")
					}
					for sample, wantCode := range input[count][12:] {
						if f.PCM[sample] != rxSIPG711(payload, wantCode) {
							t.Fatalf("真实PCM不符 frame=%d sample=%d got=%d", count, sample, f.PCM[sample])
						}
					}
					count++
				}
				if count != frames {
					t.Fatalf("实际解码帧=%d want=%d", count, frames)
				}
				rxSIPWait(t, 2*time.Second, func() bool { return s.Stats.Active.Load() == 0 && s.Stats.Established.Load() == 0 && h.Retired() }, "BYE没有完成真实媒体资源释放")
				postCtx, postCancel := context.WithTimeout(ctx, 2*time.Second)
				defer postCancel()
				stats, err := s.pool.Call(postCtx, 0, media.Request{Op: "stats", Generation: capability.Generation})
				trace.record("fresh_media_stats", map[string]any{"reply": stats, "error": fmt.Sprint(err)})
				if err != nil || !stats.OK || stats.Type != "stats" {
					t.Fatal("无法取得Release后的新鲜媒体stats", err)
				}
				for _, key := range []string{"active_calls", "processed_active_calls", "processed_local_active_calls", "rx_active_subscriptions", "rx_queued_events", "rx_observation_storage_bytes", "rx_observation_failed"} {
					if rxSIPStat(t, stats.Stats, key) != 0 {
						t.Fatal("Release残留：", key)
					}
				}
				if rxSIPStat(t, stats.Stats, "processed_decoded_frames") != frames {
					t.Fatal("媒体实际解码帧计数不等于输入")
				}
				if value := s.CompatibilityAPI(postCtx, "uuid_exists", uuid); strings.TrimSpace(value) != "false" {
					t.Fatal("BYE后旧UUID仍可使用", value)
				}
				if h2, _, err := s.SubscribeRX(postCtx, uuid, 2); h2 != nil || !errors.Is(err, ErrRXNotEligible) {
					t.Fatal("已结束UUID重新开放RX：", err)
				}
				var rebound []*net.UDPConn
				for port := base; port <= base+3; port++ {
					c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
					if err != nil {
						for _, p := range rebound {
							_ = p.Close()
						}
						t.Fatal("释放后媒体端口无法真实复绑：", err)
					}
					rebound = append(rebound, c)
				}
				for _, c := range rebound {
					_ = c.Close()
				}
				trace.record("media_ports_rebound", []int{base, base + 1, base + 2, base + 3})
				// SIP响应缓存与call_gc按现有协议保留32秒；等待真实GC，不手工清空timer伪造零值。
				trace.record("await_real_sip_gc", map[string]any{"active_timers": s.Stats.Timers.Load(), "limit_seconds": 35})
				rxSIPWait(t, 35*time.Second, func() bool { return s.Stats.Timers.Load() == 0 }, "实际SIP缓存/会话定时器没有归零")
				trace.record("fresh_control_zero", map[string]any{"active_calls": s.Stats.Active.Load(), "established_calls": s.Stats.Established.Load(), "active_timers": s.Stats.Timers.Load(), "worker": s.pool.Workers[0].CapabilitySnapshot()})
				close(stop)
				select {
				case runErr := <-done:
					runFinished = true
					if runErr != nil {
						t.Fatal("正常排空失败：", runErr)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("清理后Server未正常排空")
				}
				normalShutdown = true
				t.Logf("真实SIP错误ACK拒绝/正确ACK授权，payload=%d connected=%t 8帧1280独立样本，BYE撤销、freshzero、四端口复绑及真实32秒GC通过", payload, connected)
			})
		}
	}
}
