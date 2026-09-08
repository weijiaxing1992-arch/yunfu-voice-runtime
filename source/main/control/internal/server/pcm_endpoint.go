package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"rustswitch/control/internal/config"
)

// pcmEntry只在注册表短锁内修改；传出的是不可变PCMHandle指针，网络I/O从不持此锁。
type pcmEntry struct {
	uuid          string
	slot          int
	token         [16]byte
	handle        *PCMHandle
	previous      *PCMHandle // 未知替换最多保留一个旧所有者，明确拒绝新候选后可恢复，仍只占一个槽。
	previousToken [16]byte
	busy          bool // 同UUID只允许一个建立/遗忘操作，Status/Interrupt仍能经旧token对账。
}

// pcmEndpoint是同UID可信进程入口，不是租户认证或HTTP CSRF入口。
// 固定连接配额、每连接固定缓冲；一个连接可承载多通话，不为通话/帧增加协程。
type pcmEndpoint struct {
	server               *Server
	options              config.PCMStream
	ctx                  context.Context
	cancel               context.CancelFunc
	listener             *net.UnixListener
	identity             os.FileInfo
	slots, control, data chan struct{}
	wg                   sync.WaitGroup
	once                 sync.Once
	mu                   sync.RWMutex
	connections          map[*net.UnixConn]struct{}
	byUUID               map[string]*pcmEntry
	byToken              map[[16]byte]*pcmEntry
	entries              []*pcmEntry
	free                 []int
	cursor               int
}

func (s *Server) startPCMTransport() error {
	if s.Config.PCMStream == nil {
		return nil
	}
	o := s.Config.PCMStreamOptions()
	e, err := newPCMEndpoint(s, o)
	if err != nil {
		return err
	}
	s.pcmEndpoint = e
	e.wg.Add(2)
	go e.accept()
	go e.collect()
	return nil
}

func (s *Server) closePCMTransport() {
	if s.pcmEndpoint != nil {
		s.pcmEndpoint.close()
	}
}

// newPCMEndpoint拒绝非私有父目录和已存在路径，不删除别的实例、普通文件或符号链接。
func newPCMEndpoint(s *Server, o config.PCMStream) (*pcmEndpoint, error) {
	if s == nil || s.ctx == nil || o.MaxStreams < 1 || o.MaxConnections < 2 || o.MaxConnections > 64 || o.MaxConnections%2 != 0 {
		return nil, errors.New("invalid PCM endpoint resource budget")
	}
	parent, err := os.Lstat(filepath.Dir(o.SocketPath))
	if err != nil {
		return nil, fmt.Errorf("PCM socket parent: %w", err)
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !parent.IsDir() || parent.Mode().Perm()&0077 != 0 || !ok || owner.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("PCM socket requires an existing private 0700 parent owned by this UID")
	}
	if _, err = os.Lstat(o.SocketPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("PCM socket path already exists or cannot be checked")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: o.SocketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// Go默认关闭时按路径删除；关闭它以便下方只删除本实例创建且inode仍匹配的socket。
	listener.SetUnlinkOnClose(false)
	identity, err := os.Lstat(o.SocketPath)
	if err != nil {
		listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	e := &pcmEndpoint{server: s, options: o, ctx: ctx, cancel: cancel, listener: listener, identity: identity,
		slots: make(chan struct{}, o.MaxConnections), control: make(chan struct{}, o.MaxConnections/2), data: make(chan struct{}, o.MaxConnections/2),
		connections: make(map[*net.UnixConn]struct{}), byUUID: make(map[string]*pcmEntry), byToken: make(map[[16]byte]*pcmEntry),
		entries: make([]*pcmEntry, o.MaxStreams), free: make([]int, o.MaxStreams)}
	for i := range e.free {
		e.free[i] = o.MaxStreams - 1 - i
	}
	if identity.Mode()&os.ModeSocket == 0 {
		cancel()
		listener.Close()
		return nil, errors.New("PCM listener path changed during creation")
	}
	if err = os.Chmod(o.SocketPath, 0600); err != nil {
		cancel()
		listener.Close()
		e.unlinkOwned()
		return nil, err
	}
	return e, nil
}

func (e *pcmEndpoint) unlinkOwned() {
	current, err := os.Lstat(e.options.SocketPath)
	if err == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(e.identity, current) {
		_ = os.Remove(e.options.SocketPath)
	}
}

func (e *pcmEndpoint) close() {
	e.once.Do(func() {
		e.cancel()
		_ = e.listener.Close()
		e.mu.Lock()
		for c := range e.connections {
			_ = c.Close()
		}
		e.mu.Unlock()
		e.wg.Wait()
		e.unlinkOwned()
	})
}

func (e *pcmEndpoint) accept() {
	defer e.wg.Done()
	for {
		conn, err := e.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case e.slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		e.mu.Lock()
		if e.ctx.Err() != nil {
			e.mu.Unlock()
			<-e.slots
			conn.Close()
			return
		}
		e.connections[conn] = struct{}{}
		e.wg.Add(1)
		e.mu.Unlock()
		go e.serve(conn)
	}
}

// collect每250ms最多检查256个槽，回收已结束/换代通话，不遍历全局SIP映射或给每通话设轮询。
func (e *pcmEndpoint) collect() {
	defer e.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.mu.Lock()
			e.collectLocked(256)
			e.mu.Unlock()
		}
	}
}

func (e *pcmEndpoint) collectLocked(budget int) {
	if budget > len(e.entries) {
		budget = len(e.entries)
	}
	for i := 0; i < budget; i++ {
		entry := e.entries[e.cursor]
		e.cursor = (e.cursor + 1) % len(e.entries)
		if entry != nil && !entry.busy {
			e.reconcileLocked(entry)
		}
		if entry != nil && !entry.busy && entry.handle != nil && entry.handle.Retired() {
			e.removeLocked(entry)
		}
	}
}

// reconcileLocked只看原子退休状态，不发RPC：新轮确认使旧句柄退休；新候选拒绝则还原旧令牌。
func (e *pcmEndpoint) reconcileLocked(entry *pcmEntry) {
	if entry.previous == nil {
		return
	}
	if entry.previous.Retired() {
		delete(e.byToken, entry.previousToken)
		entry.previous, entry.previousToken = nil, [16]byte{}
	} else if entry.handle.Retired() {
		delete(e.byToken, entry.token)
		entry.handle, entry.token = entry.previous, entry.previousToken
		entry.previous, entry.previousToken = nil, [16]byte{}
	}
}

func (e *pcmEndpoint) removeLocked(entry *pcmEntry) {
	if e.byUUID[entry.uuid] != entry {
		return
	}
	delete(e.byUUID, entry.uuid)
	delete(e.byToken, entry.token)
	delete(e.byToken, entry.previousToken)
	e.entries[entry.slot] = nil
	e.free = append(e.free, entry.slot) // 容量在启动时固定，最多恢复到MaxStreams，不扩容。
}

func (e *pcmEndpoint) begin(ctx context.Context, r pcmWireRequest, body []byte) pcmWireResult {
	if !utf8.Valid(body) {
		return pcmWireResult{code: 3, message: "UUID must be UTF-8"}
	}
	uuid := string(body)
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil || token == [16]byte{} {
		return pcmWireResult{code: 10, message: "cannot allocate stream identity"}
	}
	e.mu.Lock()
	if _, exists := e.byToken[token]; exists {
		e.mu.Unlock()
		return pcmWireResult{code: 10, message: "stream identity collision"}
	}
	entry := e.byUUID[uuid]
	if entry != nil && !entry.busy {
		e.reconcileLocked(entry)
	}
	if entry != nil && entry.busy {
		e.mu.Unlock()
		return pcmWireResult{code: 5, message: "stream operation busy"}
	}
	if entry != nil && entry.previous != nil {
		e.mu.Unlock()
		return pcmWireResult{code: 5, message: "stream replacement pending"}
	}
	if entry == nil {
		// 满载准入直接拒绝；退休回收交给固定预算定时器，避免每次BEGIN在写锁内扫描万路。
		if len(e.free) == 0 {
			e.mu.Unlock()
			return pcmWireResult{code: 7, message: "stream limit reached"}
		}
		slot := e.free[len(e.free)-1]
		e.free = e.free[:len(e.free)-1]
		entry = &pcmEntry{uuid: uuid, slot: slot}
		e.entries[slot] = entry
		e.byUUID[uuid] = entry
	}
	entry.busy = true
	e.mu.Unlock()
	h, reply, err := e.server.BeginPCM(ctx, uuid, r.turn, r.arg1, r.arg2)
	e.mu.Lock()
	defer e.mu.Unlock()
	entry.busy = false
	if h == nil {
		if entry.handle == nil {
			e.removeLocked(entry)
		}
		if err != nil {
			return pcmWireError(err)
		}
		return pcmWireResult{code: 3, message: reply.Message}
	}
	if h != entry.handle {
		if entry.handle != nil && !entry.handle.Retired() {
			entry.previous, entry.previousToken = entry.handle, entry.token
		} else {
			delete(e.byToken, entry.token)
		}
		entry.handle = h
		entry.token = token
		e.byToken[token] = entry
	}
	// 已提交但回执未知也占用流预算；返还稳定令牌供其他控制连接查询/停止。
	// SDK在提交前发布句柄，稍后的明确拒绝或通话结束会将其退休并由有界GC回收。
	if err != nil {
		failure := pcmWireError(err)
		failure.token, failure.turn = entry.token, h.TurnID()
		return failure
	}
	if !reply.OK {
		return pcmWireResult{code: 3, token: entry.token, turn: h.TurnID(), message: reply.Message}
	}
	return pcmResult(reply, entry.token)
}

func (e *pcmEndpoint) execute(ctx context.Context, r pcmWireRequest, body []byte, samples *[800]int16) pcmWireResult {
	if r.op == pcmBegin {
		return e.begin(ctx, r, body)
	}
	if r.op == pcmPing {
		return pcmWireResult{}
	}
	e.mu.RLock()
	entry := e.byToken[r.token]
	if entry == nil {
		e.mu.RUnlock()
		return pcmWireResult{code: 4, message: "unknown stream"}
	}
	h := entry.handle
	if r.token == entry.previousToken {
		h = entry.previous
	}
	if h.Retired() {
		e.mu.RUnlock()
		return pcmWireResult{code: 8, message: "stream retired"}
	}
	if r.turn != h.TurnID() {
		e.mu.RUnlock()
		return pcmWireResult{code: 3, message: "stream turn mismatch"}
	}
	e.mu.RUnlock()
	if r.op == pcmForget {
		e.mu.Lock()
		if e.byToken[r.token] != entry {
			e.mu.Unlock()
			return pcmWireResult{code: 4, message: "unknown stream"}
		}
		if entry.busy {
			e.mu.Unlock()
			return pcmWireResult{code: 5, message: "stream operation busy"}
		}
		if h != entry.handle || entry.previous != nil && !entry.previous.Retired() {
			e.mu.Unlock()
			return pcmWireResult{code: 5, message: "stream replacement pending; interrupt the selected turn"}
		}
		entry.busy = true
		e.mu.Unlock()
	}
	if r.op == pcmPush {
		for i := 0; i < len(body)/2; i++ {
			samples[i] = int16(binary.LittleEndian.Uint16(body[2*i : 2*i+2]))
		}
		reply, err := h.Push(ctx, r.offset, samples[:len(body)/2])
		result := pcmWireResult{token: r.token, turn: reply.CurrentTurnID, state: reply.State, accepted: reply.AcceptedSamples, sent: reply.SentSamples, queued: reply.QueuedSamples, discarded: reply.DiscardedSamples, age: reply.OldestAgeMS, buffer: h.bufferMS, prebuffer: h.prebufferMS}
		if err != nil {
			failure := pcmWireError(err)
			result.code = failure.code
			result.message = failure.message
		} else if reply.Code != 0 {
			result.code = 3
			result.message = reply.Error
		}
		return result
	}
	var replyResult pcmWireResult
	switch r.op {
	case pcmEnd:
		reply, err := h.End(ctx, r.offset)
		replyResult = pcmResult(reply, r.token)
		if err != nil {
			replyResult = pcmWireError(err)
		} else if !reply.OK {
			replyResult.code = 3
			replyResult.message = reply.Message
		}
	case pcmInterrupt, pcmForget:
		fade := r.arg1
		if r.op == pcmForget {
			fade = 0
		}
		reply, err := h.Interrupt(ctx, fade)
		replyResult = pcmResult(reply, r.token)
		if err != nil {
			replyResult = pcmWireError(err)
		} else if !reply.OK {
			replyResult.code = 3
			replyResult.message = reply.Message
		}
		if r.op == pcmForget {
			e.mu.Lock()
			entry.busy = false
			if replyResult.code == 0 && (reply.State == "completed" || reply.State == "stopped" || reply.State == "failed") {
				e.removeLocked(entry)
			}
			e.mu.Unlock()
		}
	case pcmStatus:
		reply, err := h.Status(ctx)
		replyResult = pcmResult(reply, r.token)
		if err != nil {
			replyResult = pcmWireError(err)
		} else if !reply.OK {
			replyResult.code = 3
			replyResult.message = reply.Message
		}
	}
	return replyResult
}

func (e *pcmEndpoint) serve(conn *net.UnixConn) {
	defer func() {
		conn.Close()
		e.mu.Lock()
		delete(e.connections, conn)
		e.mu.Unlock()
		<-e.slots
		e.wg.Done()
	}()
	var header [pcmWireHeader]byte
	var body [pcmWireBody]byte
	var output [pcmWireReply + pcmWireMessage]byte
	var samples [800]int16
	var last uint64
	mode := uint16(0)
	for {
		r, err := readPCMRequest(conn, &header, &body, mode == 0)
		if err != nil {
			if err != io.EOF && r.id != 0 {
				_ = writePCMResult(conn, r, pcmWireResult{code: 1, message: "invalid or incomplete request"}, &output)
			}
			return
		}
		if r.id <= last {
			_ = writePCMResult(conn, r, pcmWireResult{code: 1, message: "request ID must increase"}, &output)
			return
		}
		last = r.id
		if mode == 0 {
			if r.op != pcmHello {
				_ = writePCMResult(conn, r, pcmWireResult{code: 1, message: "HELLO required"}, &output)
				return
			}
			quota := e.control
			if r.arg1 == pcmDataMode {
				quota = e.data
			}
			select {
			case quota <- struct{}{}:
			default:
				_ = writePCMResult(conn, r, pcmWireResult{code: 7, message: "connection class limit"}, &output)
				return
			}
			defer func() { <-quota }()
			mode = r.arg1
			if writePCMResult(conn, r, pcmWireResult{}, &output) != nil {
				return
			}
			continue
		}
		if r.op == pcmHello {
			_ = writePCMResult(conn, r, pcmWireResult{code: 1, message: "HELLO cannot repeat"}, &output)
			return
		}
		if r.op != pcmPing && ((mode == pcmDataMode && r.op != pcmPush) || (mode == pcmControlMode && r.op == pcmPush)) {
			if writePCMResult(conn, r, pcmWireResult{code: 9, message: "use the matching control or data connection"}, &output) != nil {
				return
			}
			continue
		}
		ctx, cancel := context.WithTimeout(e.ctx, time.Second)
		result := e.execute(ctx, r, body[:r.length], &samples)
		cancel()
		if writePCMResult(conn, r, result, &output) != nil {
			return
		}
	}
}
