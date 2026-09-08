package media

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"rustswitch/control/internal/config"
)

// rxIndependentG711不调用生产解码器；按两种标准量化律独立检查全部160个样本。
func rxIndependentG711(payload uint8, value byte) int16 {
	if payload == 0 {
		code := ^value
		magnitude := ((int(code&15) << 3) + 132) << ((code >> 4) & 7)
		magnitude -= 132
		if code&128 != 0 {
			return int16(-magnitude)
		}
		return int16(magnitude)
	}
	code := value ^ 0x55
	magnitude := (int(code&15) << 4) + 8
	exponent := (code >> 4) & 7
	if exponent != 0 {
		magnitude += 256
		magnitude <<= exponent - 1
	}
	if code&128 == 0 {
		return int16(-magnitude)
	}
	return int16(magnitude)
}

// rxRealIsolatedPorts只在2000..3599内占用六个自有端口；避开主媒体10000..64999及管理/SIP监听。
// 前四个暂留给worker，后两个始终由测试持有为真实远端；不使用可能落入主范围的ephemeral端口。
func rxRealIsolatedPorts(t *testing.T) (int, func(), *net.UDPConn, *net.UDPConn) {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		base := 2000 + ((os.Getpid()+attempt)%200)*8
		sockets := make([]*net.UDPConn, 0, 6)
		for offset := 0; offset < 6; offset++ {
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: base + offset})
			if err != nil {
				break
			}
			sockets = append(sockets, c)
		}
		if len(sockets) == 6 {
			return base, func() {
				for _, c := range sockets[:4] {
					_ = c.Close()
				}
			}, sockets[4], sockets[5]
		}
		for _, c := range sockets {
			_ = c.Close()
		}
	}
	t.Fatal("无可用低于10000的隔离端口块")
	return 0, func() {}, nil, nil
}

// TestRealRXPoolG711SamplesAndLifecycle是内部Pool→Rust→FD4的真实媒体验证，不经过SIP授权。
// 实際二进制缺能力、输入未接入、只有零PCM或旧代次不清理均失败，不skip或用协议夹具替代。
func TestRealRXPoolG711SamplesAndLifecycle(t *testing.T) {
	if os.Getenv("RUSTSWITCH_REAL_MEDIA") == "" {
		t.Fatal("RX真实测试必须显式设置RUSTSWITCH_REAL_MEDIA，不能回退到已部署旧媒体")
	}
	mediaBinary := realLocalMediaBinary(t)
	before := realLocalBinaryHash(t, mediaBinary)
	for _, connected := range []bool{false, true} {
		for _, payload := range []uint8{0, 8} {
			t.Run(fmt.Sprintf("payload_%d_connected_%t", payload, connected), func(t *testing.T) {

				base, releasePorts, rtp, rtcp := rxRealIsolatedPorts(t)
				defer releasePorts()
				defer rtp.Close()
				defer rtcp.Close()
				cfg, err := config.Load("../../../config/local.json")
				if err != nil {
					t.Fatal(err)
				}
				cfg.Media.Binary, cfg.Media.Processing, cfg.Media.PlaybackRoot = mediaBinary, "g711", ""
				cfg.Media.Workers, cfg.Media.BindIP, cfg.Media.PortStart, cfg.Media.PortEnd = 1, "127.0.0.1", base, base+3
				cfg.Media.CPUCores, cfg.Media.ExcludedPortBlocks, cfg.Media.AllowedRemoteNetworks = nil, nil, []string{"127.0.0.0/8"}
				cfg.Limits.MaxCalls = 1
				cfg.Media.ConnectSockets = connected
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				p, err := Start(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				if !p.Workers[0].SupportsRX() {
					t.Fatalf("actual Rust missing RX capability: %+v", p.Workers[0].CapabilitySnapshot())
				}
				identity, _ := json.Marshal(p.Workers[0].CapabilitySnapshot())
				t.Logf("actual_rx_worker=%s binary_sha256=%s", identity, before)
				codec := "PCMU"
				if payload == 8 {
					codec = "PCMA"
				}
				releasePorts()
				allocated, err := p.Call(ctx, 0, Request{Op: "allocate", Generation: 1, Session: 73, Processing: G711LocalProcessingPlan(), A: &Peer{RTP: rtp.LocalAddr().String(), RTCP: rtcp.LocalAddr().String()}, Payload: payload, Codec: &CodecSpec{Name: codec, SampleRate: 8000, RTPClockRate: 8000, Channels: 1, PTimeMS: 20}})
				if err != nil {
					t.Fatal(err)
				}
				h, reply, err := p.SubscribeRX(ctx, 0, 1, 73, 1)
				if err != nil || h == nil {
					t.Fatalf("subscribe: %+v %v", reply, err)
				}
				target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: allocated.ARTP}
				const frames = 6
				var input [frames][172]byte
				for index := range input {
					b := input[index][:]
					b[0] = 0x80
					b[1] = payload
					if index == 0 {
						b[1] |= 0x80
					}
					binary.BigEndian.PutUint16(b[2:], uint16(400+index))
					binary.BigEndian.PutUint32(b[4:], uint32(16000+index*160))
					binary.BigEndian.PutUint32(b[8:], 0x71230456)
					for i := 0; i < 160; i++ {
						b[12+i] = byte(index*47 + i*13 + 31)
					}
				}
				sent := make(chan error, 1)
				go func() {
					start := time.Now()
					for index := range input {
						delay := time.Until(start.Add(time.Duration(index) * 20 * time.Millisecond))
						if delay > 0 {
							timer := time.NewTimer(delay)
							select {
							case <-timer.C:
							case <-ctx.Done():
								timer.Stop()
								sent <- ctx.Err()
								return
							}
						}
						if _, err := rtp.WriteToUDP(input[index][:], target); err != nil {
							sent <- err
							return
						}
					}
					sent <- nil
				}()
				seen := map[uint64]bool{}
				var firstSequence, firstTimestamp uint64
				for len(seen) < frames {
					f, err := h.Read(ctx)
					if err != nil {
						t.Fatalf("actual RX read failed: %v snapshot=%+v", err, h.Snapshot())
					}
					if err := f.CheckFresh(); err != nil {
						t.Fatalf("actual RXS2 deadline: %v", err)
					}
					encoded, _ := json.Marshal(f)
					t.Logf("decoded_actual_rxs2=%s", encoded)
					if f.Kind != RXDecoded {
						if f.Kind == RXExportGap || f.Kind == RXExportFailed || f.Kind == RXObservationFailed {
							t.Fatalf("unexpected actual RX loss: %+v", f)
						}
						continue
					}
					if f.Flags&15 != 15 || f.SSRC != 0x71230456 || uint16(f.RTPSequence) < 400 || uint16(f.RTPSequence) >= 400+frames || seen[f.RTPSequence] {
						t.Fatalf("actual RX source mismatch: %+v", f)
					}
					index := int(uint16(f.RTPSequence) - 400)
					// 展开序号的初始cycle不固定；低位必须逐字对应RTP，完整u64仍须连续，不能容忍任意重锚。
					if len(seen) == 0 {
						firstSequence = f.RTPSequence
						firstTimestamp = f.RTPTimestamp
					}
					if index != len(seen) || f.RTPSequence != firstSequence+uint64(index) || f.RTPTimestamp != firstTimestamp+uint64(index*160) || uint32(f.RTPTimestamp) != uint32(16000+index*160) || f.SampleCount != 160 || f.SampleRate != 8000 {
						t.Fatalf("actual RX position/format: %+v", f)
					}
					for i, sample := range f.PCM {
						if want := rxIndependentG711(payload, input[index][12+i]); sample != want {
							t.Fatalf("actual %s frame%d sample%d=%d want%d", codec, index, i, sample, want)
						}
					}
					seen[f.RTPSequence] = true
				}
				if err := <-sent; err != nil {
					t.Fatal(err)
				}
				for i := range input {
					t.Logf("actual_input_rtp destination=%s hex=%x", target, input[i])
				}
				stopped, err := h.Unsubscribe(ctx)
				if err != nil || stopped.State != "stopped" || stopped.SubmittedSamples < frames*160 {
					t.Fatalf("actual RX stop accounting: %+v %v", stopped, err)
				}
				wire, _ := json.Marshal(stopped)
				t.Logf("actual_rx_stopped=%s", wire)
				if _, err = p.Call(ctx, 0, Request{Op: "release", Generation: 1, Session: 73}); err != nil {
					t.Fatal(err)
				}
				if !h.Retired() {
					t.Fatal("real release did not revoke SDK handle")
				}
				if _, err = h.Status(ctx); err != ErrRXRetired {
					t.Fatal("retired handle sent status", err)
				}
				stats, err := p.Call(ctx, 0, Request{Op: "stats", Generation: 1})
				if err != nil {
					t.Fatal(err)
				}
				if stats.Stats["active_calls"] != float64(0) || stats.Stats["processed_local_active_calls"] != float64(0) {
					t.Fatalf("actual RX call leaked: %v", stats.Stats)
				}
			})
		}
	}
	if after := realLocalBinaryHash(t, mediaBinary); before != after {
		t.Fatal("media binary changed during RX test")
	}
}
