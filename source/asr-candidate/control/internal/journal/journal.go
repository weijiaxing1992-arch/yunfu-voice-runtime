// Package journal 将控制面事件写入追加式 JSON 行日志，隔离信令主循环与磁盘写入。
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// Event 是一条控制面生命周期记录；它不包含恢复 SIP 对话所需的完整状态。
type Event struct {
	Time   time.Time `json:"time"`              // 事件产生时刻。
	RunID  string    `json:"run_id"`            // 区分不同控制进程启动周期。
	CallID string    `json:"call_id,omitempty"` // 可选的 A 腿 Call-ID。
	Kind   string    `json:"kind"`              // 事件类别。
	Reason string    `json:"reason,omitempty"`  // 可选的结束或异常原因。
}

// Journal 由多个调用者非阻塞提交、单个后台协程写盘，原子计数区分入缓冲和已同步的记录。
type Journal struct {
	queue   chan Event    // 有界待写队列，满时立即拒绝。
	done    chan struct{} // 后台写入协程退出通知。
	failed  atomic.Bool   // 溢出或存储故障后保持失败，当前实现不会自行恢复。
	Written atomic.Uint64 // 本进程已编码到写缓冲的记录数，尚不等于持久化成功。
	Synced  atomic.Uint64 // 最近一次 Flush 和 Sync 均成功后的记录数。
	Lost    atomic.Uint64 // 被拒绝或未能写入的记录计数。
	file    *os.File      // 被本进程独占写锁保护的日志文件。
}

// Open 取得独占写锁并从头检查历史日志；只截掉末尾未完成的一行，完整坏行必须报错。
// 该恢复检查扫描全部历史内容，日志大小会影响启动时间；它并不恢复正在通话的会话。
func Open(path string, capacity int) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	// 失败统一关闭描述符，关闭时系统也会释放已经取得的文件锁。
	fail := func(e error) (*Journal, error) { _ = f.Close(); return nil, e }
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(fmt.Errorf("journal is already owned: %w", err))
	}
	reader := bufio.NewReaderSize(f, 65536)
	var offset int64
	for {
		line, e := reader.ReadSlice('\n')
		if len(line) > 65536 {
			return fail(errors.New("journal record too large"))
		}
		if e == io.EOF {
			if len(line) > 0 {
				if err = f.Truncate(offset); err != nil {
					return fail(err)
				}
			}
			break
		}
		if e != nil {
			return fail(e)
		}
		if !json.Valid(line) {
			return fail(fmt.Errorf("invalid complete journal record at byte %d", offset))
		}
		offset += int64(len(line))
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		return fail(err)
	}
	j := &Journal{queue: make(chan Event, capacity), done: make(chan struct{}), file: f}
	go j.run()
	return j, nil
}

// Healthy 在队列达到八成前提供准入预警；它是瞬时观测，不保证下一次提交一定成功。
func (j *Journal) Healthy() bool { return !j.failed.Load() && len(j.queue) < cap(j.queue)*8/10 }

// Record 仅确认事件进入内存队列，不承诺已落盘；当前策略在一次队列溢出后保持失败状态。
// Close 开始后调用者必须停止提交，避免向已关闭通道发送。
func (j *Journal) Record(e Event) bool {
	if j.failed.Load() {
		j.Lost.Add(1)
		return false
	}
	select {
	case j.queue <- e:
		return true
	default:
		j.failed.Store(true)
		j.Lost.Add(1)
		return false
	}
}

// Close 在生产者已停止后调用一次，排空队列并等待最终刷盘尝试结束；此方法不是幂等操作。
func (j *Journal) Close() { close(j.queue); <-j.done }

// run 独占日志编码器，每二十毫秒或关闭时执行刷盘；存储故障后继续接收并计数丢弃事件。
func (j *Journal) run() {
	defer close(j.done)
	defer j.file.Close()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	writer := bufio.NewWriterSize(j.file, 65536)
	encoder := json.NewEncoder(writer)
	dirty := false
	storageFailed := false
	// 只有写缓冲和文件同步都成功才推进 Synced；失败时保留与 Written 的差异供诊断。
	flush := func() {
		if !dirty || storageFailed {
			return
		}
		if err := writer.Flush(); err != nil {
			storageFailed = true
			j.failed.Store(true)
			return
		}
		if err := j.file.Sync(); err != nil {
			storageFailed = true
			j.failed.Store(true)
			return
		}
		j.Synced.Store(j.Written.Load())
		dirty = false
	}
	for {
		select {
		case event, ok := <-j.queue:
			if !ok {
				flush()
				return
			}
			if storageFailed {
				j.Lost.Add(1)
				continue
			}
			if err := encoder.Encode(event); err != nil {
				storageFailed = true
				j.failed.Store(true)
				j.Lost.Add(1)
				continue
			}
			j.Written.Add(1)
			dirty = true
		case <-ticker.C:
			flush()
		}
	}
}
