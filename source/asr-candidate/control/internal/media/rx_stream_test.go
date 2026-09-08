package media

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"rustswitch/control/internal/config"
)

// rxWire是独立协议夹具，只验证SDK边界；不代替真实Rust/RTP解码证据。
func rxWire(kind RXKind, session, sub, seq uint64) []byte {
	body := 0
	if kind == RXDecoded || kind == RXHistoryPLC {
		body = 320
	}
	if kind == RXComfortNoise {
		body = 3
	}
	b := make([]byte, rxHeaderSize+body)
	copy(b, "RXS2")
	b[4] = byte(kind)
	binary.LittleEndian.PutUint64(b[8:], session)
	binary.LittleEndian.PutUint64(b[16:], sub)
	binary.LittleEndian.PutUint64(b[24:], seq)
	binary.LittleEndian.PutUint32(b[76:], 8000)
	binary.LittleEndian.PutUint32(b[80:], 8000)
	binary.LittleEndian.PutUint16(b[84:], uint16(body))
	if body == 320 {
		binary.LittleEndian.PutUint16(b[86:], 160)
		for i := 0; i < 160; i++ {
			binary.LittleEndian.PutUint16(b[rxHeaderSize+i*2:], uint16(int16(i*313-25000)))
		}
	}
	if kind == RXExportFailed {
		binary.LittleEndian.PutUint16(b[124:], 1)
	}
	if kind == RXComfortNoise {
		copy(b[rxHeaderSize:], []byte{7, 19, 91})
	}
	binary.LittleEndian.PutUint16(b[150:], rxClockDomain)
	if kind != RXExportGap && kind != RXEnd && kind != RXExportFailed {
		now, err := rxClockNow()
		if err != nil {
			panic(err)
		}
		binary.LittleEndian.PutUint64(b[152:], now)
		binary.LittleEndian.PutUint64(b[160:], now+rxObservationBudgetNS)
	}
	return b
}
func rxState(session, sub uint64) Reply {
	return Reply{OK: true, Type: "rx_state", Session: session, SubscriptionID: sub, State: "active"}
}
func rxFixture(t *testing.T) (*Pool, *rxLane) {
	t.Helper()
	l := &rxLane{subscriptions: make(map[uint64]*RXSubscription), maxSubscriptions: 2, generation: 1}
	w := &Worker{rx: l, Config: WorkerConfig{MaxCalls: 2}, jobs: make(chan job, 2), urgent: make(chan job, 2), capabilities: []string{"processed_g711_v1", "processed_g711_local_v1", rxCapability}}
	w.Healthy.Store(true)
	w.Generation.Store(1)
	w.PID.Store(123)
	p := &Pool{Workers: []*Worker{w}}
	return p, l
}
func rxRegister(p *Pool, l *rxLane, session, sub uint64) *RXSubscription {
	h := &RXSubscription{pool: p, lane: l, worker: 0, generation: 1, session: session, subscriptionID: sub, notify: make(chan struct{}, 1), readDone: make(chan struct{}), remote: rxState(session, sub)}
	l.subscriptions[session] = h
	return h
}
func rxFinish(j job, r Reply, err error) {
	if !j.callState.CompareAndSwap(0, 1) {
		j.finish(Reply{}, j.ctx.Err())
		return
	}
	j.finish(r, err)
}

func TestRXWirePreservesAudioSIDAndExtendedPosition(t *testing.T) {
	b := rxWire(RXDecoded, 4, 9, 1)
	binary.LittleEndian.PutUint16(b[6:], 0x3df)
	b[148] = 5
	binary.LittleEndian.PutUint64(b[32:], 4)
	binary.LittleEndian.PutUint64(b[40:], 8)
	binary.LittleEndian.PutUint64(b[48:], math.MaxUint32+160)
	binary.LittleEndian.PutUint64(b[56:], 65537)
	binary.LittleEndian.PutUint64(b[64:], 20000000)
	binary.LittleEndian.PutUint32(b[72:], 7788)
	binary.LittleEndian.PutUint32(b[136:], 25)
	binary.LittleEndian.PutUint32(b[140:], 65)
	binary.LittleEndian.PutUint32(b[144:], 3)
	f, err := decodeRXFrame(b)
	if err != nil || f.PCM[0] != -25000 || f.PCM[159] != int16(159*313-25000) || f.RTPSequence != 65537 || f.RTPTimestamp != math.MaxUint32+160 || f.ObservationAgeMS != 25 || f.ArrivalAgeMS != 65 || f.DeadlineLatenessMS != 3 {
		t.Fatalf("lost metadata/audio: %+v %v", f, err)
	}
	b[rxHeaderSize] = 0
	if f.PCM[0] != -25000 {
		t.Fatal("frame aliases reusable wire buffer")
	}
	f, err = decodeRXFrame(rxWire(RXComfortNoise, 4, 9, 2))
	if err != nil || f.SampleCount != 0 || f.SID[2] != 91 {
		t.Fatalf("CN became PCM: %+v %v", f, err)
	}
	for _, kind := range []RXKind{RXHistoryPLC, RXMissing, RXLocalExpired, RXAuxiliaryExpired, RXSourceBoundary, RXInactiveSuspended, RXObservationFailed, RXAuxiliary, RXEnd, RXExportFailed} {
		if _, err := decodeRXFrame(rxWire(kind, 4, 9, 3)); err != nil {
			t.Fatalf("kind %d: %v", kind, err)
		}
	}
	if allocations := testing.AllocsPerRun(100, func() { _, _ = decodeRXFrame(b) }); allocations != 0 {
		t.Fatalf("per-frame allocations: %g", allocations)
	}
}
func TestRXWireRejectsMalformedOrInventedMetadata(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"short": func(b []byte) []byte { return b[:159] }, "old128header": func(b []byte) []byte { return b[:128] }, "trailing": func(b []byte) []byte { return append(b, 1) },
		"magic": func(b []byte) []byte { b[0] = 'X'; return b }, "kind": func(b []byte) []byte { b[4] = 14; return b }, "reserved5": func(b []byte) []byte { b[5] = 1; return b }, "reserved126": func(b []byte) []byte { b[127] = 1; return b }, "unknown_domain": func(b []byte) []byte { binary.LittleEndian.PutUint16(b[150:], 99); return b },
		"flags": func(b []byte) []byte { binary.LittleEndian.PutUint16(b[6:], 1024); return b }, "zero_session": func(b []byte) []byte { binary.LittleEndian.PutUint64(b[8:], 0); return b }, "zero_sub": func(b []byte) []byte { binary.LittleEndian.PutUint64(b[16:], 0); return b }, "zero_seq": func(b []byte) []byte { binary.LittleEndian.PutUint64(b[24:], 0); return b },
		"wrong_rate": func(b []byte) []byte { binary.LittleEndian.PutUint32(b[76:], 16000); return b }, "wrong_clock": func(b []byte) []byte { binary.LittleEndian.PutUint32(b[80:], 48000); return b }, "partial_pcm": func(b []byte) []byte { binary.LittleEndian.PutUint16(b[86:], 159); return b }, "body_len": func(b []byte) []byte { binary.LittleEndian.PutUint16(b[84:], 319); return b },
		"fake_timestamp": func(b []byte) []byte { b[48] = 1; return b }, "fake_seq": func(b []byte) []byte { b[56] = 1; return b }, "fake_ssrc": func(b []byte) []byte { b[72] = 1; return b }, "fake_media": func(b []byte) []byte { b[64] = 1; return b }, "fake_arrival": func(b []byte) []byte { b[140] = 1; return b }, "fake_deadline": func(b []byte) []byte { b[144] = 1; return b }, "invalid_reason": func(b []byte) []byte { b[124] = 12; return b }, "fake_gap": func(b []byte) []byte { b[88] = 1; return b }, "fake_boundary": func(b []byte) []byte { b[148] = 1; return b }, "invalid_cn_flag": func(b []byte) []byte { b[149] = 2; return b }, "fake_cn": func(b []byte) []byte { b[149] = 1; return b }, "fake_discard": func(b []byte) []byte { b[128] = 1; return b },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRXFrame(mutate(rxWire(RXDecoded, 1, 2, 1))); err == nil {
				t.Fatal("malformed wire accepted")
			}
		})
	}
}
func TestRXGapMustExactlyAccountForLostObservations(t *testing.T) {
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 2)
	gap := rxWire(RXExportGap, 1, 2, 3)
	binary.LittleEndian.PutUint64(gap[88:], 1)
	binary.LittleEndian.PutUint64(gap[96:], 3)
	binary.LittleEndian.PutUint32(gap[104:], 2)
	binary.LittleEndian.PutUint32(gap[108:], 1)
	binary.LittleEndian.PutUint16(gap[124:], 3)
	f, err := decodeRXFrame(gap)
	if err != nil {
		t.Fatal(err)
	}
	l.dispatch(f)
	voice, _ := decodeRXFrame(rxWire(RXDecoded, 1, 2, 4))
	l.dispatch(voice)
	end, _ := decodeRXFrame(rxWire(RXEnd, 1, 2, 4))
	l.dispatch(end)
	for _, kind := range []RXKind{RXExportGap, RXDecoded, RXEnd} {
		r, e := h.Read(context.Background())
		if e != nil || r.Kind != kind {
			t.Fatalf("bad ordering: %v %v", r.Kind, e)
		}
	}
	if _, err = h.Read(context.Background()); err != io.EOF {
		t.Fatalf("no terminal EOF: %v", err)
	}
	if h.Snapshot().ExportGapEvents != 3 {
		t.Fatal("gap count lost")
	}
	for name, mutate := range map[string]func([]byte){"zero_first": func(b []byte) { binary.LittleEndian.PutUint64(b[88:], 0) }, "wrong_end": func(b []byte) { binary.LittleEndian.PutUint64(b[24:], 4) }, "too_many_pcm": func(b []byte) { binary.LittleEndian.PutUint32(b[104:], 3) }, "position": func(b []byte) { binary.LittleEndian.PutUint16(b[6:], 1) }, "unknown_mask": func(b []byte) { binary.LittleEndian.PutUint16(b[124:], 8) }} {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), gap...)
			mutate(b)
			if _, e := decodeRXFrame(b); e == nil {
				t.Fatal("bad gap accepted")
			}
		})
	}
	for _, kind := range []RXKind{RXDecoded, RXEnd, RXExportGap} {
		t.Run("unaccounted_"+string(rune('A'+kind)), func(t *testing.T) {
			p, l := rxFixture(t)
			h := rxRegister(p, l, 1, 2)
			f := voice
			f.Kind = kind
			f.GapFirst = 2
			f.GapLast = 4
			l.dispatch(f)
			if _, e := h.Read(context.Background()); e == nil {
				t.Fatal("sequence gap silently ignored")
			}
		})
	}
}
func TestRXSlowConsumerDoesNotBlockOtherSubscriptions(t *testing.T) {
	p, l := rxFixture(t)
	slow := rxRegister(p, l, 1, 1)
	fast := rxRegister(p, l, 2, 1)
	for i := uint64(1); i <= 12; i++ {
		f, _ := decodeRXFrame(rxWire(RXDecoded, 1, 1, i))
		l.dispatch(f)
		f.Session = 2
		l.dispatch(f)
		if r, err := fast.Read(context.Background()); err != nil || r.Sequence != i {
			t.Fatalf("fast blocked: %+v %v", r, err)
		}
	}
	if _, err := slow.Read(context.Background()); !errors.Is(err, ErrRXSlowConsumer) {
		t.Fatalf("slow consumer not explicit: %v", err)
	}
	s := slow.Snapshot()
	if s.ReceivedRecords != 12 || s.DroppedRecords != 12 || s.QueuedRecords != 0 || s.Retired {
		t.Fatalf("bad local accounting: %+v", s)
	}
	if fast.Snapshot().DeliveredRecords != 12 {
		t.Fatal("fast flow lost records")
	}
}
func TestRXOldIdentityAndGenerationNeverReachNewSubscription(t *testing.T) {
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 20)
	for _, identity := range [][2]uint64{{9, 20}, {1, 19}} {
		f, _ := decodeRXFrame(rxWire(RXDecoded, identity[0], identity[1], 1))
		l.dispatch(f)
	}
	l.generation = 2
	f, _ := decodeRXFrame(rxWire(RXDecoded, 1, 20, 1))
	l.dispatch(f)
	if h.Snapshot().ReceivedRecords != 0 {
		t.Fatal("old generation delivered")
	}
	l.generation = 1
	l.release(1)
	if !h.Retired() || len(l.subscriptions) != 0 {
		t.Fatal("release leaks handle")
	}
	if _, err := h.Status(context.Background()); !errors.Is(err, ErrRXRetired) {
		t.Fatal("retired handle queued command")
	}
}
func TestRXControlCountersAndMissingFields(t *testing.T) {
	request := Request{Op: "rx_status", Generation: 1, Session: 1, SubscriptionID: 2}
	valid := rxState(1, 2)
	valid.ProducedEvents = 5
	valid.SubmittedEvents = 2
	valid.DroppedEvents = 1
	valid.QueuedEvents = 2
	valid.ProducedSamples = 640
	valid.SubmittedSamples = 320
	valid.DroppedSamples = 160
	if err := validateRXControlReply(request, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Reply){"session": func(r *Reply) { r.Session++ }, "sub": func(r *Reply) { r.SubscriptionID++ }, "unknown_state": func(r *Reply) { r.State = "idle" }, "missing_error": func(r *Reply) { r.State = "failed" }, "unknown_error": func(r *Reply) { r.State = "failed"; r.Error = "anything" }, "event_sum": func(r *Reply) { r.ProducedEvents++ }, "overflow": func(r *Reply) { r.SubmittedEvents = math.MaxUint64 }, "partial_samples": func(r *Reply) { r.ProducedSamples++ }, "invented_pcm": func(r *Reply) { r.ProducedSamples = 16000 }, "stopped_queued": func(r *Reply) { r.State = "stopped" }, "queue_bound": func(r *Reply) { r.QueuedEvents = 9; r.ProducedEvents = 12 }} {
		t.Run(name, func(t *testing.T) {
			r := valid
			mutate(&r)
			if err := validateRXControlReply(request, r); err == nil {
				t.Fatal("bad counters accepted")
			}
		})
	}
	wire, _ := json.Marshal(valid)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(wire, &fields)
	for _, name := range []string{"id", "ok", "type", "session", "subscription_id", "state", "produced_events", "submitted_events", "dropped_events", "queued_events", "produced_samples", "submitted_samples", "dropped_samples", "oldest_age_ms", "error"} {
		t.Run("presence_"+name, func(t *testing.T) {
			value := fields[name]
			defer func() { fields[name] = value }()
			for _, mode := range []string{"absent", "null"} {
				if mode == "absent" {
					delete(fields, name)
				} else {
					fields[name] = json.RawMessage("null")
				}
				b, _ := json.Marshal(fields)
				if validateRXRaw(b) == nil {
					t.Fatal("absent/null became zero")
				}
			}
		})
	}
	empty := rxState(1, 2)
	empty.State = "stopped"
	if err := validateRXControlReply(request, empty); err != nil {
		t.Fatal(err)
	}
	empty.OldestAgeMS = 1
	if validateRXControlReply(request, empty) == nil {
		t.Fatal("empty queue invented age")
	}
}
func TestRXSubscribeRegistersBeforeReplyAndSameIDDoesNotResurrect(t *testing.T) {
	p, l := rxFixture(t)
	complete := make(chan *RXSubscription, 1)
	go func() {
		h, _, err := p.SubscribeRX(context.Background(), 0, 1, 1, 2)
		if err != nil {
			t.Error(err)
		}
		complete <- h
	}()
	j := <-p.Workers[0].urgent
	f, _ := decodeRXFrame(rxWire(RXDecoded, 1, 2, 1))
	l.dispatch(f)
	rxFinish(j, rxState(1, 2), nil)
	h := <-complete
	if r, e := h.Read(context.Background()); e != nil || r.Sequence != 1 {
		t.Fatal("first frame before JSON lost")
	}
	go func() {
		r, e := h.Unsubscribe(context.Background())
		if e != nil || r.State != "stopped" {
			t.Errorf("unsubscribe %v %v", r, e)
		}
		complete <- h
	}()
	j = <-p.Workers[0].urgent
	r := rxState(1, 2)
	r.State = "stopped"
	rxFinish(j, r, nil)
	<-complete
	go func() {
		next, _, e := p.SubscribeRX(context.Background(), 0, 1, 1, 2)
		if e != nil {
			t.Error(e)
		}
		complete <- next
	}()
	j = <-p.Workers[0].urgent
	rxFinish(j, r, nil)
	if next := <-complete; next != h {
		t.Fatal("same ID created new queue")
	}
}
func TestRXSubscribeCancellationAndUnknownRetainCleanupOwnership(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued_cancel", true: "inflight_cancel"}[started], func(t *testing.T) {
			p, l := rxFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			out := make(chan *RXSubscription, 1)
			errs := make(chan error, 1)
			go func() { h, _, err := p.SubscribeRX(ctx, 0, 1, 1, 2); out <- h; errs <- err }()
			j := <-p.Workers[0].urgent
			if started {
				j.callState.Store(1)
			}
			cancel()
			h := <-out
			err := <-errs
			if !started {
				if h != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("unsubmitted leaked %v %v", h, err)
				}
				j.finish(Reply{}, ctx.Err())
				if len(l.subscriptions) != 0 {
					t.Fatal("cancelled candidate leaked")
				}
				return
			}
			if h == nil || !errors.Is(err, ErrRXOutcomeUnknown) || h.Retired() {
				t.Fatalf("unknown lost owner: %v %v", h, err)
			}
			if _, err := h.Status(context.Background()); !errors.Is(err, ErrRXPending) {
				t.Fatalf("pending handle not guarded: %v", err)
			}
			j.finish(rxState(1, 2), nil)
			if h.Snapshot().Pending {
				t.Fatal("callback did not settle")
			}
			done := make(chan error, 1)
			go func() { _, err := h.Unsubscribe(context.Background()); done <- err }()
			j = <-p.Workers[0].urgent
			r := rxState(1, 2)
			r.State = "stopped"
			rxFinish(j, r, nil)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRXUnknownResultAndKnownRejectionReplacement(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "negative"}[known], func(t *testing.T) {
			p, l := rxFixture(t)
			old := rxRegister(p, l, 1, 1)
			old.remote.State = "stopped"
			out := make(chan *RXSubscription, 1)
			errs := make(chan error, 1)
			go func() { h, _, err := p.SubscribeRX(context.Background(), 0, 1, 1, 2); out <- h; errs <- err }()
			j := <-p.Workers[0].urgent
			r := Reply{}
			if known {
				r = Reply{Type: "error", Message: "unsupported"}
			}
			rxFinish(j, r, errors.New("test failure"))
			h := <-out
			err := <-errs
			if known {
				if h != nil || old.Retired() || l.subscriptions[1] != old {
					t.Fatal("known rejection destroyed old owner")
				}
			} else {
				if h == nil || !errors.Is(err, ErrRXOutcomeUnknown) || !old.Retired() || l.subscriptions[1] != h {
					t.Fatal("unknown replacement ownership lost")
				}
			}
		})
	}
}
func TestRXCapabilityGenerationAndBoundedAdmission(t *testing.T) {
	p, l := rxFixture(t)
	w := p.Workers[0]
	for name, mutate := range map[string]func(){"missing_cap": func() { w.capabilities = w.capabilities[:2] }, "missing_local": func() { w.capabilities = []string{rxCapability, "processed_g711_v1"} }, "wrong_generation": func() { w.Generation.Store(2) }, "unhealthy": func() { w.Healthy.Store(false) }, "missing_pid": func() { w.PID.Store(0) }, "failed_lane": func() { l.failed = true }} {
		t.Run(name, func(t *testing.T) {
			p, l = rxFixture(t)
			w = p.Workers[0]
			mutate()
			if h, _, e := p.SubscribeRX(context.Background(), 0, 1, 1, 1); e == nil || h != nil {
				t.Fatal("invalid worker admitted")
			}
		})
	}
	p, l = rxFixture(t)
	w = p.Workers[0]
	rxRegister(p, l, 1, 1)
	rxRegister(p, l, 2, 1)
	if h, _, e := p.SubscribeRX(context.Background(), 0, 1, 3, 1); !errors.Is(e, ErrQueueFull) || h != nil {
		t.Fatal("subscription table unbounded")
	}
	for i := 0; i < cap(w.urgent); i++ {
		w.urgent <- job{}
	}
	if _, _, e := p.SubscribeRX(context.Background(), 0, 1, 1, 1); !errors.Is(e, ErrQueueFull) {
		t.Fatal("control queue not bounded")
	}
	if l.subscriptions[1].Snapshot().Pending {
		t.Fatal("queuefull left pending")
	}
	if err := p.Submit(context.Background(), 0, Request{Op: "rx_status", Session: 1, SubscriptionID: 1}, func(Reply, error) {}); err == nil {
		t.Fatal("implicit generation admitted")
	}
}
func TestRXConcurrentReadFailureAndRetirement(t *testing.T) {
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if _, err := h.Read(ctx); err != nil {
					return
				}
			}
		}()
	}
	for i := uint64(1); i <= 30; i++ {
		f, _ := decodeRXFrame(rxWire(RXDecoded, 1, 1, i))
		l.dispatch(f)
	}
	l.isolate(ErrRXRetired, true)
	wg.Wait()
	if !h.Retired() {
		t.Fatal("generation shutdown did not retire")
	}
}

// 独立子进程验证FD3未改变、FD4能在JSON回执前交付观察；不声称此夹具编码了真实RTP。
func init() {
	if os.Getenv("RUSTSWITCH_RX_MEDIA_HELPER") == "" || len(os.Args) < 3 || os.Args[1] != "--worker-config" {
		return
	}
	if os.Getenv("RUSTSWITCH_PCM_FD") != "3" || os.Getenv("RUSTSWITCH_RX_FD") != "4" {
		os.Exit(21)
	}
	var cfg WorkerConfig
	if json.Unmarshal([]byte(os.Args[2]), &cfg) != nil {
		os.Exit(22)
	}
	file := os.NewFile(4, "rx-child")
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		os.Exit(23)
	}
	defer conn.Close()
	encoder, decoder := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	caps := []string{"processed_g711_v1", "processed_g711_local_v1", rxCapability}
	if encoder.Encode(Reply{OK: true, Type: "ready", ProtocolVersion: 1, WorkerID: cfg.WorkerID, PID: os.Getpid(), Capabilities: caps}) != nil {
		os.Exit(24)
	}
	for {
		var r Request
		if decoder.Decode(&r) != nil {
			os.Exit(0)
		}
		reply := Reply{OK: true, ID: r.ID, Type: "ack"}
		switch r.Op {
		case "rx_subscribe", "rx_status", "rx_unsubscribe":
			if r.Op == "rx_status" && os.Getenv("RUSTSWITCH_RX_MEDIA_HELPER") == "malformed" {
				_, _ = conn.Write([]byte("bad-rxs1"))
			}
			reply = rxState(r.Session, r.SubscriptionID)
			reply.ID = r.ID
			if r.Op == "rx_subscribe" {
				_, _ = conn.Write(rxWire(RXDecoded, r.Session, r.SubscriptionID, 1))
			}
			reply.ProducedEvents = 1
			reply.SubmittedEvents = 1
			reply.ProducedSamples = 160
			reply.SubmittedSamples = 160
			if r.Op == "rx_unsubscribe" {
				reply.State = "stopped"
				_, _ = conn.Write(rxWire(RXEnd, r.Session, r.SubscriptionID, 1))
			}
		case "stats":
			reply.Type = "stats"
			reply.Stats = map[string]any{"dtmf_send_packets": 0, "dtmf_send_errors": 0, "dtmf_send_cancelled_digits": 0, "dtmf_send_active": 0}
		}
		if encoder.Encode(reply) != nil {
			os.Exit(25)
		}
	}
}
func TestRXSubprocessFD4AndControlRemainIndependent(t *testing.T) {
	t.Setenv("RUSTSWITCH_RX_MEDIA_HELPER", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{}
	c.Media.Binary = binary
	c.Media.Workers = 1
	c.Limits.MaxCalls = 2
	c.Media.PortStart = 32000
	c.Media.PortEnd = 32015
	c.Media.Processing = ""
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := Start(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	h, r, err := p.SubscribeRX(ctx, 0, 1, 1, 2)
	if err != nil || r.State != "active" {
		t.Fatalf("subscribe: %v %v", r, err)
	}
	f, err := h.Read(ctx)
	if err != nil || f.PCM[0] != -25000 {
		t.Fatalf("real FD4 receive: %+v %v", f, err)
	}
	if r, err = h.Unsubscribe(ctx); err != nil || r.State != "stopped" {
		t.Fatalf("unsubscribe: %v %v", r, err)
	}
	if f, err = h.Read(ctx); err != nil || f.Kind != RXEnd {
		t.Fatalf("terminal wire: %v %v", f.Kind, err)
	}
	generation := p.Workers[0].Generation.Load()
	p.Workers[0].rx.isolate(errors.New("injected receive fault"), false)
	if r, err = h.Status(ctx); err != nil || r.SubscriptionID != 2 {
		t.Fatalf("RX failure killed JSON: %v %v", r, err)
	}
	if !p.Workers[0].Healthy.Load() || p.Workers[0].Generation.Load() != generation {
		t.Fatal("RX fault restarted worker")
	}
	p.Close()
	if !h.Retired() {
		t.Fatal("Close leaked RX subscription")
	}
}

func TestRXMalformedDatagramIsolatesOnlyRX(t *testing.T) {
	t.Setenv("RUSTSWITCH_RX_MEDIA_HELPER", "malformed")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Media: config.Media{Binary: executable, Workers: 1, PortStart: 32000, PortEnd: 32003}, Limits: config.Limits{MaxCalls: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	h, _, err := p.SubscribeRX(ctx, 0, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Read(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("malformed wire not isolated: %v", err)
	}
	if h.Retired() {
		t.Fatal("RX fault falsely ended media generation")
	}
	if _, err = h.Unsubscribe(ctx); err != nil {
		t.Fatalf("JSON unavailable after bad RX: %v", err)
	}
	if !p.Workers[0].Healthy.Load() || p.Workers[0].Generation.Load() != 1 {
		t.Fatal("bad RX restarted media")
	}
}

// 少于8条也必须过期；短暂停读不会被误认为仅仅没有触发队满。
func TestRXQueueAgeExpiresBelowCapacityAndRemainsCleanable(t *testing.T) {
	p, l := rxFixture(t)
	slow := rxRegister(p, l, 1, 1)
	fast := rxRegister(p, l, 2, 1)
	f, _ := decodeRXFrame(rxWire(RXDecoded, 1, 1, 1))
	l.dispatch(f)
	time.Sleep(110 * time.Millisecond)
	if _, err := slow.Read(context.Background()); !errors.Is(err, ErrRXExpired) {
		t.Fatalf("stale audio replayed: %v", err)
	}
	snapshot := slow.Snapshot()
	if snapshot.DroppedRecords != 1 || snapshot.QueuedRecords != 0 || snapshot.Retired {
		t.Fatalf("expired counter: %+v", snapshot)
	}
	f, _ = decodeRXFrame(rxWire(RXDecoded, 2, 1, 1))
	l.dispatch(f)
	fresh, err := fast.Read(context.Background())
	if err != nil || fresh.ReceivedAt.IsZero() || fresh.GoQueueAgeMS >= 100 {
		t.Fatalf("other stream affected: %+v %v", fresh, err)
	}
	for _, op := range []string{"rx_status", "rx_unsubscribe"} {
		done := make(chan error, 1)
		go func() { _, err := slow.control(context.Background(), op); done <- err }()
		j := <-p.Workers[0].urgent
		r := rxState(1, 1)
		if op == "rx_unsubscribe" {
			r.State = "stopped"
		}
		rxFinish(j, r, nil)
		if err := <-done; err != nil {
			t.Fatalf("expired handle cannot cleanup: %v", err)
		}
	}
}
func TestRXEnqueueExpiresOldPrefixWithoutConsumerPoll(t *testing.T) {
	p, l := rxFixture(t)
	h := rxRegister(p, l, 1, 1)
	f, _ := decodeRXFrame(rxWire(RXDecoded, 1, 1, 1))
	l.dispatch(f)
	h.mu.Lock()
	h.queue[h.head].ReceivedAt = time.Now().Add(-101 * time.Millisecond)
	h.mu.Unlock()
	f.Sequence = 2
	l.dispatch(f)
	if _, err := h.Read(context.Background()); !errors.Is(err, ErrRXExpired) {
		t.Fatal("stale prefix not invalidated on enqueue", err)
	}
	if snapshot := h.Snapshot(); snapshot.DroppedRecords != 2 || snapshot.ReceivedRecords != 2 {
		t.Fatal("expired prefix lost accounting", snapshot)
	}
}
