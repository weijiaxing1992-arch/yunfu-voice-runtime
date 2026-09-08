// asr-mock 是独立、本机、有界的ASR1供应商模拟进程；输出确定性PCM摘要而非识别文字。
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"rustswitch/control/internal/asr/wire"
)

type options struct {
	listen, artifacts, fault             string
	maxConnections, maxTotal, faultAfter int
	maxBytes, perConnectionBytes         int64
	delay                                time.Duration
	finalGate                            bool
}

var faultNames = map[string]bool{"none": true, "ready-delay": true, "read-pause": true, "consume-pause": true, "late-final": true, "close-before-done": true, "wrong-token": true, "duplicate-final": true, "wrong-done": true, "oversize": true, "half-eof": true}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("ASR1 socket父目录必须为同UID且无组/其他用户权限的真实目录")
	}
	return nil
}
func (o options) validate() error {
	if !filepath.IsAbs(o.listen) || !filepath.IsAbs(o.artifacts) || filepath.Clean(o.listen) != o.listen || filepath.Clean(o.artifacts) != o.artifacts {
		return errors.New("必须提供规范绝对 --listen / --artifacts 路径")
	}
	if len(o.listen) > 100 {
		return errors.New("Unix socket路径过长")
	}
	if err := privateDirectory(filepath.Dir(o.listen)); err != nil {
		return err
	}
	if _, err := os.Lstat(o.listen); !errors.Is(err, os.ErrNotExist) {
		return errors.New("socket路径已存在或不可检查，不会删除其他对象")
	}
	if _, err := os.Lstat(o.artifacts); !errors.Is(err, os.ErrNotExist) {
		return errors.New("证据目录必须不存在，不覆盖旧证据")
	}
	if o.maxConnections < 1 || o.maxConnections > 64 || o.maxTotal < o.maxConnections || o.maxTotal > 4096 || o.maxBytes < 65536 || o.maxBytes > 1<<30 || o.perConnectionBytes < 16384 || o.perConnectionBytes > o.maxBytes || o.faultAfter < 1 || o.faultAfter > 100000 || o.delay < 0 || o.delay > 5*time.Second || !faultNames[o.fault] {
		return errors.New("ASR1 模拟参数超出有界范围")
	}
	return nil
}

func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if n < 0 || n > len(b) {
			return errors.New("非法Write计数")
		}
		b = b[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func waitFinalGate(ctx context.Context, e *evidence, o options) error {
	if err := e.event("waiting-final", nil); err != nil {
		return err
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		path := filepath.Join(o.artifacts, "final.release")
		info, err := os.Lstat(path)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != uint32(os.Geteuid()) || info.Size() > 32 {
				return errors.New("final.release不是同UID的有界正规控制文件")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("final gate总2秒期限耗尽")
		case <-tick.C:
		}
	}
}

func mutateJSON(m wire.Message, key string, value any) (wire.Message, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(m.Body, &object); err != nil {
		return wire.Message{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return wire.Message{}, err
	}
	object[key] = raw
	body, err := json.Marshal(object)
	return wire.Message{Kind: m.Kind, Body: body}, err
}

func serve(ctx context.Context, e *evidence, p *provider, o options) error {
	var input, output [wire.MaxWireSize]byte
	audioSeen := 0
	readFault, consumeFault, resultFault := false, false, false
	for {
		if o.fault == "read-pause" && !readFault && audioSeen >= o.faultAfter {
			readFault = true
			if err := e.event("fault-read-pause", map[string]any{"delay_ms": o.delay.Milliseconds()}); err != nil {
				return err
			}
			if err := pause(ctx, o.delay); err != nil {
				return err
			}
		}
		idle := 12 * time.Second
		if p.state == "starting" {
			idle = 2 * time.Second
		}
		if err := e.beginRead(idle); err != nil {
			return err
		}
		start := e.inBytes
		m, err := wire.Read(e, &input)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return p.eof()
			}
			return err
		}
		if err := e.event("read-complete", map[string]any{"kind": m.Kind, "offset": start, "bytes": e.inBytes - start}); err != nil {
			return err
		}
		if m.Kind == wire.KindAudio {
			audioSeen++
			if o.fault == "consume-pause" && !consumeFault && audioSeen == o.faultAfter {
				consumeFault = true
				if err := e.event("fault-consume-pause", map[string]any{"delay_ms": o.delay.Milliseconds()}); err != nil {
					return err
				}
				if err := pause(ctx, o.delay); err != nil {
					return err
				}
			}
		}
		now, domain, err := wire.ClockNow()
		if err != nil {
			return err
		}
		if domain != p.domain {
			return errors.New("模拟进程时钟域发生变化")
		}
		replies, err := p.accept(m, now)
		if err != nil {
			return errors.Join(err, e.rejected(m, now, err))
		}
		if m.Kind == wire.KindAudio {
			a, err := wire.ParseAudio(m.Body)
			if err != nil {
				return err
			}
			if err = e.savePCM(a.PCM); err != nil {
				return err
			}
			if err = e.event("audio-consumed", map[string]any{"event_seq": fmt.Sprint(a.EventSeq), "source_generation": fmt.Sprint(a.SourceGeneration), "source_segment": fmt.Sprint(a.SourceSegment), "lower_ns": fmt.Sprint(a.LowerNS), "expires_ns": fmt.Sprint(a.ExpiresNS), "checked_clock_ns": fmt.Sprint(now), "samples": a.SampleCount, "pcm_offset": e.pcmBytes - uint64(len(a.PCM)), "pcm_bytes": len(a.PCM)}); err != nil {
				return err
			}
		}
		if m.Kind == wire.KindMarker {
			if err := e.event("marker-consumed", map[string]any{"prefix": p.counters()}); err != nil {
				return err
			}
		}
		for _, reply := range replies {
			isFinal := false
			if reply.Kind == wire.KindResult {
				var r wire.Result
				if err := wire.ParseJSON(reply.Body, &r); err != nil {
					return err
				}
				isFinal = r.Type == "final"
			}
			if reply.Kind == wire.KindReady && o.fault == "ready-delay" {
				if err := e.event("fault-ready-delay", map[string]any{"delay_ms": o.delay.Milliseconds()}); err != nil {
					return err
				}
				if err := pause(ctx, o.delay); err != nil {
					return err
				}
			}
			if isFinal {
				if o.finalGate {
					if err := waitFinalGate(ctx, e, o); err != nil {
						return err
					}
				}
				if o.fault == "late-final" {
					if err := e.event("fault-late-final", map[string]any{"delay_ms": o.delay.Milliseconds()}); err != nil {
						return err
					}
					if err := pause(ctx, o.delay); err != nil {
						return err
					}
				}
			}
			if reply.Kind == wire.KindDone && o.fault == "close-before-done" {
				if err := e.event("fault-close-before-done", nil); err != nil {
					return err
				}
				return errors.New("injected close_before_done")
			}
			if reply.Kind == wire.KindDone && o.fault == "wrong-done" {
				var f wire.Finish
				if err := wire.ParseJSON(reply.Body, &f); err != nil {
					return err
				}
				reply, err = mutateJSON(reply, "samples", wire.U64(uint64(f.Samples)+1))
				if err != nil {
					return err
				}
			}
			if reply.Kind == wire.KindResult && !resultFault && o.fault == "wrong-token" {
				resultFault = true
				bad := strings.Repeat("f", 32)
				if bad == p.hello.StreamToken {
					bad = strings.Repeat("1", 32)
				}
				reply, err = mutateJSON(reply, "stream_token", bad)
				if err != nil {
					return err
				}
			}
			if err := e.conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
				return err
			}
			if reply.Kind == wire.KindResult && !resultFault && o.fault == "oversize" {
				resultFault = true
				var bad [8]byte
				binary.LittleEndian.PutUint32(bad[:], 8193)
				bad[4] = byte(wire.KindResult)
				bad[5] = 1
				if err := writeAll(e, bad[:]); err != nil {
					return err
				}
				if err := e.event("fault-oversize", nil); err != nil {
					return err
				}
				return errors.New("injected oversize")
			}
			raw, err := wire.Encode(output[:], reply)
			if err != nil {
				return err
			}
			if reply.Kind == wire.KindResult && !resultFault && o.fault == "half-eof" {
				resultFault = true
				if err := writeAll(e, raw[:17]); err != nil {
					return err
				}
				if err := e.event("fault-half-eof", nil); err != nil {
					return err
				}
				return errors.New("injected half_frame_eof")
			}
			before := e.outBytes
			if err := e.writeResponse(raw, isFinal, func(b []byte) error { return writeAll(e, b) }); err != nil {
				return err
			}
			if err := e.event("write-complete", map[string]any{"kind": reply.Kind, "offset": before, "bytes": e.outBytes - before}); err != nil {
				return err
			}
			if isFinal && o.fault == "duplicate-final" {
				var r wire.Result
				if err := wire.ParseJSON(reply.Body, &r); err != nil {
					return err
				}
				duplicate, err := mutateJSON(reply, "result_seq", wire.U64(uint64(r.ResultSeq)+1))
				if err != nil {
					return err
				}
				raw, err := wire.Encode(output[:], duplicate)
				if err != nil {
					return err
				}
				if err := e.writeResponse(raw, true, func(b []byte) error { return writeAll(e, b) }); err != nil {
					return err
				}
				if err := e.event("fault-duplicate-final", nil); err != nil {
					return err
				}
			}
		}
		if p.state == "completed" || p.state == "cancelled" {
			return nil
		}
	}
}

func run(ctx context.Context, o options) error {
	if err := o.validate(); err != nil {
		return err
	}
	if err := os.Mkdir(o.artifacts, 0700); err != nil {
		return err
	}
	now, domain, err := wire.ClockNow()
	if err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: o.listen, Net: "unix"})
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(false)
	identity, err := os.Lstat(o.listen)
	if err != nil {
		listener.Close()
		return err
	}
	defer func() {
		current, e := os.Lstat(o.listen)
		if e == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(identity, current) {
			_ = os.Remove(o.listen)
		}
	}()
	if err := os.Chmod(o.listen, 0600); err != nil {
		listener.Close()
		return err
	}
	if err := writeNewJSON(filepath.Join(o.artifacts, "ready.json"), map[string]any{"schema": "asr1-mock-ready-v1", "pid": os.Getpid(), "socket": o.listen, "clock_domain": domain, "clock_ns": fmt.Sprint(now), "max_connections": o.maxConnections, "max_total_connections": o.maxTotal, "recognition_model": false}); err != nil {
		listener.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := &evidenceBudget{maximum: o.maxBytes}
	var mu sync.Mutex
	connections := map[*net.UnixConn]bool{}
	var wg sync.WaitGroup
	accepted, rejected, completed, failed, peak := 0, 0, 0, 0, 0
	var fatal error
	shutdown := make(chan struct{})
	go func() {
		defer close(shutdown)
		<-ctx.Done()
		_ = listener.Close()
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
	}()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			mu.Lock()
			if fatal == nil {
				fatal = err
			}
			mu.Unlock()
			cancel()
			break
		}
		uid, err := wire.PeerUID(conn)
		if err != nil || uid != uint32(os.Geteuid()) {
			_ = conn.Close()
			mu.Lock()
			rejected++
			mu.Unlock()
			continue
		}
		mu.Lock()
		if len(connections) >= o.maxConnections || accepted >= o.maxTotal {
			rejected++
			mu.Unlock()
			_ = conn.Close()
			continue
		}
		accepted++
		serial := accepted
		connections[conn] = true
		if len(connections) > peak {
			peak = len(connections)
		}
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := newProvider(domain)
			e, err := newEvidence(filepath.Join(o.artifacts, fmt.Sprintf("connection-%04d", serial)), conn, budget, o.perConnectionBytes)
			if err == nil {
				err = e.event("connection-open", map[string]any{"peer_uid": uid, "serial": serial})
				if err == nil {
					err = serve(ctx, e, p, o)
				}
			}
			_ = conn.Close()
			if err != nil {
				p.state = "failed"
				if ctx.Err() != nil {
					p.state = "cancelled"
				}
			}
			if e != nil {
				err = errors.Join(err, e.finish(p, err))
			}
			mu.Lock()
			delete(connections, conn)
			if err != nil {
				failed++
			} else {
				completed++
			}
			if errors.Is(err, errEvidence) && fatal == nil {
				fatal = err
				cancel()
			}
			mu.Unlock()
		}()
	}
	cancel()
	<-shutdown
	wg.Wait()
	mu.Lock()
	summary := map[string]any{"schema": "asr1-mock-run-v1", "pid": os.Getpid(), "accepted": accepted, "rejected": rejected, "completed_connections": completed, "failed_connections": failed, "active": len(connections), "peak_connections": peak, "all_connection_goroutines_joined": true, "listener_closed": true, "output_budget_reserved": budget.used, "output_budget_limit": budget.maximum, "recognition_model": false, "fault": o.fault}
	if fatal != nil {
		summary["error"] = fatal.Error()
	}
	mu.Unlock()
	writeErr := writeNewJSON(filepath.Join(o.artifacts, "run-receipt.json"), summary)
	return errors.Join(fatal, writeErr)
}

func main() {
	var o options
	var delayMS int
	flag.StringVar(&o.listen, "listen", "", "同UID私有目录中的绝对Unix socket路径")
	flag.StringVar(&o.artifacts, "artifacts", "", "不存在的新证据目录绝对路径")
	flag.IntVar(&o.maxConnections, "max-connections", 64, "同时连接上限(1..64)")
	flag.IntVar(&o.maxTotal, "max-total-connections", 256, "本次总接入上限(至多4096)")
	flag.Int64Var(&o.maxBytes, "max-bytes", 64<<20, "全进程原帧/PCM/事件预留预算")
	flag.Int64Var(&o.perConnectionBytes, "per-connection-bytes", 8<<20, "单连接证据预留预算")
	flag.StringVar(&o.fault, "fault", "none", "显式故障: none/ready-delay/read-pause/consume-pause/late-final/close-before-done/wrong-token/duplicate-final/wrong-done/oversize/half-eof")
	flag.IntVar(&delayMS, "delay-ms", 150, "显式故障暂停毫秒(0..5000)")
	flag.IntVar(&o.faultAfter, "fault-after", 1, "读暂停在第N个AUDIO后；消费暂停在第N个AUDIO前")
	flag.BoolVar(&o.finalGate, "final-gate", false, "final前等待证据目录final.release正规文件，至多2秒")
	flag.Parse()
	o.delay = time.Duration(delayMS) * time.Millisecond
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
