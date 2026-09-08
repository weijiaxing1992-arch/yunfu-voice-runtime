package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rustswitch/control/internal/asr/wire"
)

var errEvidence = errors.New("ASR1 原始证据保存失败")

// evidenceBudget 同时限制所有连接输出；写尝试预留全部尺寸，部分写不返还，预算保守有界。
type evidenceBudget struct {
	mu            sync.Mutex
	used, maximum int64
}

func (b *evidenceBudget) reserve(n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < 0 || n > b.maximum-b.used {
		return fmt.Errorf("%w: 总输出预算耗尽", errEvidence)
	}
	b.used += n
	return nil
}

type evidence struct {
	dir                             string
	in, out, pcm, events, attempted *os.File
	budget                          *evidenceBudget
	used, maximum                   int64
	inBytes, outBytes, pcmBytes     uint64
	attemptedBytes                  uint64
	deadline                        time.Time
	firstRead                       bool
	conn                            *net.UnixConn
}

func newEvidence(dir string, conn *net.UnixConn, budget *evidenceBudget, maximum int64) (*evidence, error) {
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("%w: %v", errEvidence, err)
	}
	e := &evidence{dir: dir, conn: conn, budget: budget, maximum: maximum}
	files := []struct {
		name   string
		target **os.File
	}{{"inbound.bin", &e.in}, {"outbound.bin", &e.out}, {"consumed.pcm", &e.pcm}, {"events.jsonl", &e.events}, {"final-attempted.bin", &e.attempted}}
	for _, item := range files {
		f, err := os.OpenFile(filepath.Join(dir, item.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			e.closeFiles()
			return nil, fmt.Errorf("%w: %v", errEvidence, err)
		}
		*item.target = f
	}
	return e, nil
}
func (e *evidence) reserve(n int) error {
	if int64(n) > e.maximum-e.used {
		return fmt.Errorf("%w: 单连接输出预算耗尽", errEvidence)
	}
	if err := e.budget.reserve(int64(n)); err != nil {
		return err
	}
	e.used += int64(n)
	return nil
}
func (e *evidence) writeFile(f *os.File, b []byte) error {
	if err := e.reserve(len(b)); err != nil {
		return err
	}
	n, err := f.Write(b)
	if err != nil || n != len(b) {
		return fmt.Errorf("%w: 文件短写 %d/%d %v", errEvidence, n, len(b), err)
	}
	return nil
}
func (e *evidence) event(stage string, details map[string]any) error {
	now, domain, err := wire.ClockNow()
	if err != nil {
		return err
	}
	if details == nil {
		details = map[string]any{}
	}
	details["stage"] = stage
	details["clock_ns"] = fmt.Sprint(now)
	details["clock_domain"] = domain
	details["wall_time"] = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return e.writeFile(e.events, append(raw, '\n'))
}

// beginRead 限定空闲等待；首字节后的完整帧另有1秒总期限，不随后续字节续期。
func (e *evidence) beginRead(idle time.Duration) error {
	e.deadline = time.Now().Add(idle)
	e.firstRead = true
	return e.conn.SetReadDeadline(e.deadline)
}
func (e *evidence) Read(p []byte) (int, error) {
	if err := e.reserve(len(p)); err != nil {
		return 0, err
	}
	n, err := e.conn.Read(p)
	if n > 0 {
		e.inBytes += uint64(n)
		written, save := e.in.Write(p[:n])
		if save != nil || written != n {
			return 0, fmt.Errorf("%w: 保存入站原帧 %v", errEvidence, save)
		}
		if e.firstRead {
			e.firstRead = false
			end := time.Now().Add(time.Second)
			if e.deadline.Before(end) {
				end = e.deadline
			}
			if deadlineErr := e.conn.SetReadDeadline(end); deadlineErr != nil {
				return 0, deadlineErr
			}
		}
	}
	return n, err
}
func (e *evidence) Write(p []byte) (int, error) {
	if err := e.reserve(len(p)); err != nil {
		return 0, err
	}
	n, err := e.conn.Write(p)
	if n > 0 {
		e.outBytes += uint64(n)
		written, save := e.out.Write(p[:n])
		if save != nil || written != n {
			return n, fmt.Errorf("%w: 保存出站原帧 %v", errEvidence, save)
		}
	}
	return n, err
}
func (e *evidence) savePCM(pcm []byte) error {
	if err := e.writeFile(e.pcm, pcm); err != nil {
		return err
	}
	e.pcmBytes += uint64(len(pcm))
	return nil
}

// 记录完整消息的实际拒绝检查点。检查时钟在accept前采集，不能用稍后的日志时间替代。
func (e *evidence) rejected(m wire.Message, checked uint64, failure error) error {
	details := map[string]any{"kind": m.Kind, "checked_clock_ns": fmt.Sprint(checked), "error": failure.Error()}
	if m.Kind == wire.KindAudio {
		if a, err := wire.ParseAudio(m.Body); err == nil {
			details["event_seq"], details["lower_ns"], details["expires_ns"] = fmt.Sprint(a.EventSeq), fmt.Sprint(a.LowerNS), fmt.Sprint(a.ExpiresNS)
			details["source_generation"], details["source_segment"] = fmt.Sprint(a.SourceGeneration), fmt.Sprint(a.SourceSegment)
			details["input_clock_domain"] = a.ClockDomain
		}
	} else if m.Kind == wire.KindMarker {
		var mark wire.Marker
		if wire.ParseJSON(m.Body, &mark) == nil {
			details["event_seq"], details["lower_ns"], details["expires_ns"] = fmt.Sprint(uint64(mark.EventSeq)), fmt.Sprint(uint64(mark.LowerNS)), fmt.Sprint(uint64(mark.ExpiresNS))
			details["input_clock_domain"] = mark.ClockDomain
		}
	}
	return e.event("consume-rejected", details)
}

// 尝试原文单独保存；outbound.bin只保存系统Write实际接受的前缀，失败不冒充交付。
// 注入write函数仅供无网络的短写/错误单元测试；真实serve始终传入writeAll(e,raw)。
func (e *evidence) writeResponse(raw []byte, final bool, write func([]byte) error) error {
	if !final {
		return write(raw)
	}
	start, attemptStart := e.outBytes, e.attemptedBytes
	if err := e.writeFile(e.attempted, raw); err != nil {
		return err
	}
	e.attemptedBytes += uint64(len(raw))
	sum := sha256.Sum256(raw)
	if err := e.event("final-write-attempt", map[string]any{"attempt_offset": attemptStart, "bytes": len(raw), "sha256": hex.EncodeToString(sum[:]), "outbound_offset": start}); err != nil {
		return err
	}
	writeErr := write(raw)
	errorText := ""
	if writeErr != nil {
		errorText = writeErr.Error()
	}
	logErr := e.event("final-write-result", map[string]any{"attempt_offset": attemptStart, "attempt_bytes": len(raw), "outbound_offset": start, "accepted_bytes": e.outBytes - start, "error": errorText})
	return errors.Join(writeErr, logErr)
}

func (e *evidence) closeFiles() error {
	var errs []error
	for _, f := range []*os.File{e.in, e.out, e.pcm, e.events, e.attempted} {
		if f != nil {
			if err := f.Sync(); err != nil {
				errs = append(errs, err)
			}
			if err := f.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func writeNewJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > 8192 {
		return errors.New("ASR1 收据超过固定8192字节")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(raw, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	return errors.Join(werr, serr, cerr)
}

func (e *evidence) finish(p *provider, serveErr error) error {
	closeErr := e.closeFiles()
	files := map[string]string{}
	var errs []error
	for _, name := range []string{"inbound.bin", "outbound.bin", "consumed.pcm", "events.jsonl", "final-attempted.bin"} {
		sum, err := hashFile(filepath.Join(e.dir, name))
		if err != nil {
			errs = append(errs, err)
		} else {
			files[name] = sum
		}
	}
	errorText := ""
	if serveErr != nil {
		errorText = serveErr.Error()
	}
	if closeErr != nil {
		errs = append(errs, closeErr)
	}
	r := map[string]any{"schema": "asr1-mock-connection-v1", "state": p.state, "error": errorText, "counters": p.counters(), "inbound_bytes": e.inBytes, "outbound_bytes": e.outBytes, "consumed_pcm_bytes": e.pcmBytes, "output_budget_reserved": e.used, "files": files, "pid": os.Getpid(), "clock_domain": p.domain, "recognition_model": false, "connection_closed": true}
	r["final_attempted_bytes"] = e.attemptedBytes
	if err := writeNewJSON(filepath.Join(e.dir, "receipt.json"), r); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %v", errEvidence, errors.Join(errs...))
	}
	return nil
}
