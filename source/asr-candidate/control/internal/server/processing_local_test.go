package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"rustswitch/control/internal/config"
	"rustswitch/control/internal/dialplan"
	"rustswitch/control/internal/media"
	"rustswitch/control/internal/sip"
)

// init为本文件提供有界的实际JSON管道子进程，只记录控制请求、不打开媒体端口。
// SIP响应和ACK状态在父进程真实执行；音频正确性由独立Rust媒体E2E检验。
func init() {
	mode := os.Getenv("RUSTSWITCH_LOCAL_CALL_HELPER")
	if mode == "" || len(os.Args) < 3 || os.Args[1] != "--worker-config" {
		return
	}
	var cfg media.WorkerConfig
	if json.Unmarshal([]byte(os.Args[2]), &cfg) != nil {
		os.Exit(2)
	}
	caps := []string{"processed_g711_v1"}
	if mode == "local" {
		caps = append(caps, "processed_g711_local_v1")
	}
	out, in := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	if out.Encode(media.Reply{OK: true, Type: "ready", WorkerID: cfg.WorkerID, ProtocolVersion: 1, PID: os.Getpid(), Capabilities: caps}) != nil {
		os.Exit(2)
	}
	trace, err := os.OpenFile(os.Getenv("RUSTSWITCH_LOCAL_CALL_TRACE"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer trace.Close()
	for count := 0; count < 64; count++ {
		var req media.Request
		if in.Decode(&req) != nil {
			os.Exit(0)
		}
		if json.NewEncoder(trace).Encode(req) != nil {
			os.Exit(2)
		}
		r := media.Reply{ID: req.ID, OK: true, Type: "ack"}
		switch req.Op {
		case "allocate":
			r.Type, r.Session = "allocated", req.Session
			r.ARTP, r.ARTCP, r.BRTP, r.BRTCP = cfg.PortStart, cfg.PortStart+1, cfg.PortStart+2, cfg.PortStart+3
			if req.Processing != nil {
				version := uint32(1)
				r.ProcessingVersion = &version
				if req.Processing.Topology == "local" {
					r.ProcessingTopology = "local"
				}
			}
		case "stats":
			r.Type, r.Stats = "stats", map[string]any{}
			for _, key := range strings.Fields("active_calls dtmf_send_packets dtmf_send_errors dtmf_send_cancelled_digits dtmf_send_active processed_active_calls processed_decoded_frames processed_decoded_samples processed_encoded_frames processed_encoded_samples processed_plc_frames processed_missing_frames processed_cn_packets processed_send_errors processed_jitter_lost processed_jitter_late processed_jitter_reordered processed_jitter_duplicates processed_jitter_overflow processed_playout_expired processed_send_deadline_misses processed_output_lateness_ns_max processed_aux_expired processed_aux_timeouts processed_queue_reset_drops processed_source_resets processed_local_active_calls processed_local_consumed_frames processed_local_consumed_samples processed_local_nonzero_frames processed_local_energy_max") {
				r.Stats[key] = 0
			}
			r.Stats["processed_output_lateness_buckets"] = [8]uint64{}
		}
		if out.Encode(r) != nil || req.Op == "shutdown" {
			os.Exit(0)
		}
	}
	os.Exit(2)
}

// localCallPool不绑定现有监听，trace保存worker实际收到的请求供路由断言。
func localCallPool(t *testing.T, mode string) (*media.Pool, config.Media, string) {
	t.Helper()
	t.Setenv("RUSTSWITCH_LOCAL_CALL_HELPER", mode)
	trace := t.TempDir() + "/media-requests.jsonl"
	t.Setenv("RUSTSWITCH_LOCAL_CALL_TRACE", trace)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Media{Binary: binary, Processing: "g711", Workers: 1, PortStart: 20000, PortEnd: 20039, BindIP: "127.0.0.1", AdvertiseIP: "127.0.0.1", AllowedRemoteNetworks: []string{"127.0.0.0/8"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	p, err := media.Start(ctx, config.Config{Media: cfg, Limits: config.Limits{MaxCalls: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, cfg, trace
}

func localCallSDP(payload, ptime, dtmfClock int) []byte {
	codec := map[int]string{0: "PCMU", 8: "PCMA", 9: "G722"}[payload]
	return []byte(fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=local-contract\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP %d 101 13\r\na=rtpmap:%d %s/8000\r\na=rtpmap:101 telephone-event/%d\r\na=fmtp:101 0-15\r\na=rtpmap:13 CN/8000\r\na=rtcp:30001\r\na=ptime:%d\r\na=sendrecv\r\n", payload, payload, codec, dtmfClock, ptime))
}

// TestLocalProcessedCallUsesOnlyAAndWaitsForActualACK穿过INVITE解析、媒体RPC、200、ACK、read与BYE。
// XML与显式本地号码均使用同一个真实A腿身份；这里不把管道替身当成RTP处理证据。
func TestLocalProcessedCallUsesOnlyAAndWaitsForActualACK(t *testing.T) {
	for _, kind := range []string{"local_pcmu", "xml_pcma"} {
		t.Run(kind, func(t *testing.T) {
			s, peer, source := guardUDPServer(t)
			pool, cfg, trace := localCallPool(t, "local")
			s.pool, s.Config.Media, s.Config.SIP.LocalExtensions = pool, cfg, []string{"1000"}
			s.Config.SIP.SetupTimeoutMS = 1000
			s.ctx, s.cancel = context.WithCancel(context.Background())
			t.Cleanup(s.cancel)
			s.results = make(chan mediaResult, 8)
			s.initApplications()
			payload := 0
			if kind == "xml_pcma" {
				payload = 8
				var err error
				s.dialplan, err = dialplan.Parse([]byte(`<include><context name="default"><extension name="local"><condition field="destination_number" expression="^1000$"><action application="answer"/><action application="set" data="menu=中文"/><action application="park"/></condition></extension></context></include>`))
				if err != nil {
					t.Fatal(err)
				}
			}
			invite, err := sip.Parse(sip.Request("INVITE", "sip:1000@127.0.0.1", source.String(), "z9hG4bK-local", "<sip:alice@local>;tag=caller", "<sip:1000@local>", kind, 1, localCallSDP(payload, 20, 8000)))
			if err != nil {
				t.Fatal(err)
			}
			s.handle(datagram{invite, source})
			if response := readGuardPacket(t, peer); response.Status != 100 {
				t.Fatal(response)
			}
			select {
			case result := <-s.results:
				s.onMedia(result)
			case <-time.After(2 * time.Second):
				t.Fatal("分配无回复")
			}
			answer := readGuardPacket(t, peer)
			c := s.calls[1]
			if answer.Status != 200 || c == nil || !c.Local || !c.Allocated || c.Established || c.AAck || c.ProcessedOffer != nil || c.BInviteSent || c.compatUUIDs[0] == "" || c.compatUUIDs[1] != "" || len(s.byDialog) != 1 || s.Stats.Active.Load() != 1 {
				t.Fatalf("本地身份/分配/ACK边界错误 status=%d call=%+v", answer.Status, c)
			}
			if !strings.Contains(string(answer.Body), fmt.Sprintf("RTP/AVP %d 101 13", payload)) {
				t.Fatal("本地A腿被改成桥接报价", string(answer.Body))
			}
			for _, app := range [][2]string{{"read", "1 3 silence digits 1000 #"}, {"playback", "tone_stream://%(100,0,440)"}} {
				if got := applicationAdmit(s, c.compatUUIDs[0], app[0], app[1], false); !strings.HasPrefix(got, "-ERR") {
					t.Fatal("ACK前执行媒体应用", got)
				}
			}
			ack, err := sip.Parse(sip.Request("ACK", "sip:1000@127.0.0.1", source.String(), "z9hG4bK-ack", invite.Header("from"), answer.Header("to"), kind, 1, nil))
			if err != nil {
				t.Fatal(err)
			}
			wrong := netip.AddrPortFrom(source.Addr(), source.Port()^1)
			s.handle(datagram{ack, wrong})
			if c.Established {
				t.Fatal("错误来源ACK推进本地媒体")
			}
			s.handle(datagram{ack, source})
			s.handle(datagram{ack, source})
			if !c.Established || s.Stats.Established.Load() != 1 {
				t.Fatal("真实ACK没有唯一建立通道")
			}
			if kind == "xml_pcma" {
				if c.compatVariables[0]["menu"] != "中文" || s.applications.lanes[c.compatUUIDs[0]].active.request.Application != "park" {
					t.Fatal("XML没有在ACK后推进有序动作")
				}
			} else {
				if got := applicationAdmit(s, c.compatUUIDs[0], "read", "1 3 silence digits 1000 #", false); got != "+OK" {
					t.Fatal(got)
				}
				s.applicationDTMF(c, 0, "2", 800, 8000, "RTP")
				s.applicationDTMF(c, 0, "#", 800, 8000, "RTP")
				if c.compatVariables[0]["digits"] != "2" || c.compatVariables[0]["read_result"] != "success" {
					t.Fatal("ACK后的实际应用状态未推进")
				}
			}
			bye, err := sip.Parse(sip.Request("BYE", "sip:1000@127.0.0.1", source.String(), "z9hG4bK-bye", invite.Header("from"), answer.Header("to"), kind, 2, nil))
			if err != nil {
				t.Fatal(err)
			}
			s.handle(datagram{bye, source})
			if r := readGuardPacket(t, peer); r.Status != 200 {
				t.Fatal(r)
			}
			select {
			case result := <-s.results:
				s.onMedia(result)
			case <-time.After(2 * time.Second):
				t.Fatal("释放无回复")
			}
			if s.Stats.Active.Load() != 0 || s.Stats.Established.Load() != 0 || len(s.compatChannels) != 0 || s.applications.jobs != 0 {
				t.Fatal("本地BYE没有完整回收")
			}
			wire, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(wire)), "\n") {
				var req media.Request
				if json.Unmarshal([]byte(line), &req) != nil {
					t.Fatal("trace不是完整请求")
				}
				if req.Op == "connect" || req.B != nil {
					t.Fatal("本地调用产生了虚构B腿", req)
				}
				if req.Op == "allocate" && (req.Processing == nil || req.Processing.Topology != "local" || req.Codec == nil || req.Codec.RTPClockRate != 8000 || req.Payload != uint8(payload)) {
					t.Fatal("实际分配没有使用本地A腿合同", req)
				}
			}
		})
	}
}

// TestLocalProcessedInviteRejectsUnavailableAndUnsupportedPlans验证失败响应及零资源副作用，
// 旧worker没有local能力时不能仅因已支持双腿而接受；本地号码也不能绕过SDP边界。
func TestLocalProcessedInviteRejectsUnavailableAndUnsupportedPlans(t *testing.T) {
	for _, tt := range []struct {
		name                          string
		payload, ptime, clock, status int
	}{{"legacy_worker", 0, 20, 8000, 503}, {"g722", 9, 20, 8000, 488}, {"ptime40", 0, 40, 8000, 488}, {"dtmf16k", 8, 20, 16000, 488}} {
		t.Run(tt.name, func(t *testing.T) {
			s, peer, source := guardUDPServer(t)
			p, cfg, trace := localCallPool(t, "bridge")
			s.pool, s.Config.Media, s.Config.SIP.LocalExtensions = p, cfg, []string{"1000"}
			m, err := sip.Parse(sip.Request("INVITE", "sip:1000@127.0.0.1", source.String(), "z9hG4bK-reject", "<sip:a@local>;tag=a", "<sip:1000@local>", tt.name, 1, localCallSDP(tt.payload, tt.ptime, tt.clock)))
			if err != nil {
				t.Fatal(err)
			}
			s.handle(datagram{m, source})
			if r := readGuardPacket(t, peer); r.Status != tt.status {
				t.Fatalf("status=%d want%d", r.Status, tt.status)
			}
			if len(s.calls) != 0 || s.Stats.Active.Load() != 0 || s.workerLoads[0] != 0 {
				t.Fatal("拒绝呼叫消耗了资源")
			}
			wire, _ := os.ReadFile(trace)
			if strings.Contains(string(wire), `"allocate"`) {
				t.Fatal("拒绝之后仍调用媒体分配")
			}
		})
	}
}
