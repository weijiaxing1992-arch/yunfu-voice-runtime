package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// progressSnapshot 是页面读取的轻量观测；完成与否仍须结合最终报告和进程退出码判断。
// established_calls 是累计通过建立校验的数量，拆线不会把它减为零。
type progressSnapshot struct {
	Phase               string  `json:"phase"`                 // starting、dialing、media、teardown 或 finished。
	RequestedCalls      int     `json:"requested_calls"`       // 操作者请求生成的呼叫总数。
	AttemptedCalls      uint64  `json:"attempted_calls"`       // 已启动的拨号任务数，不重复统计 SIP 重传。
	EstablishedCalls    uint64  `json:"established_calls"`     // SDP、Contact 和建立流程通过的累计呼叫数。
	SetupFailed         uint64  `json:"setup_failed"`          // 拨号任务确认失败的累计数量。
	ElapsedSeconds      float64 `json:"elapsed_seconds"`       // 从进度发布开始到本次采样的墙钟秒数。
	MediaElapsedSeconds float64 `json:"media_elapsed_seconds"` // 实际发送窗口秒数，结束发送后冻结。
}

// progressWriter 只读已冻结的呼叫集合和原子状态，以互斥锁串行化阶段切换及文件替换。
// 它不读取 sent/received 等普通数组，因此不会给媒体热路径增加锁或引入数据竞争。
type progressWriter struct {
	mu           sync.Mutex    // 保护以下观测状态，并阻止旧阶段快照覆盖新阶段。
	path         string        // 操作者指定的最终路径；不自动创建父目录。
	requested    int           // 本次计划的总呼叫数，构造后不再改变。
	started      time.Time     // 进度生命周期起点，不修改已有报告的 setup_seconds 口径。
	phase        string        // 当前发布阶段；只有报告完成且通过才进入 finished。
	bench        *bench        // 仅在 flows 构建完毕后发布，之后不替换呼叫集合。
	mediaStarted time.Time     // 媒体发送窗口起点，尚未开始时为零值。
	mediaElapsed time.Duration // 发送结束时冻结的时长。
	mediaEnded   bool          // 区分尚在发送和已经冻结，避免零时长被误判为运行中。
	err          error         // 首个发布错误；停止继续发布，交由主流程明确失败。
	stop         chan struct{} // 通知周期发布协程退出。
	done         chan struct{} // 周期发布协程完全退出后关闭。
	closeOnce    sync.Once     // 显式结束和 defer 清理只能执行一次。
}

// newProgressWriter 先原子写入 starting，再启动 500 ms 发布；空路径不创建文件或协程。
// 进度不能与报告使用同一路径，以免阶段快照覆盖最终验收证据。
func newProgressWriter(path, reportPath string, requested int, now time.Time) (*progressWriter, error) {
	if path == "" {
		return nil, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("progress path: %w", err)
	}
	if reportPath != "" {
		reportAbsolute, err := filepath.Abs(reportPath)
		if err != nil {
			return nil, fmt.Errorf("report path: %w", err)
		}
		if absolute == reportAbsolute {
			return nil, fmt.Errorf("progress and report paths must differ")
		}
	}
	p := &progressWriter{path: absolute, requested: requested, started: now, phase: "starting", stop: make(chan struct{}), done: make(chan struct{})}
	if err := p.publishLocked(now); err != nil {
		return nil, err
	}
	go p.run()
	return p, nil
}

// run 定期发布完整快照；发生写盘错误即停止，保留最近一次完整文件而不是制造成功状态。
func (p *progressWriter) run() {
	defer close(p.done)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.mu.Lock()
			// 等待锁期间可能已经切换阶段，使用真正采样时刻，避免旧 tick 让时间倒退。
			err := p.publishLocked(time.Now())
			p.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// beginDialing 发布已构建的只读 flows 集合，再立即写入拨号阶段，单路测试也能看到阶段变化。
func (p *progressWriter) beginDialing(b *bench, now time.Time) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bench, p.phase = b, "dialing"
	return p.publishLocked(now)
}

// beginMedia 从媒体发送起点计时；不读取发送协程的计数或改变原来的通过条件。
func (p *progressWriter) beginMedia(now time.Time) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase, p.mediaStarted = "media", now
	return p.publishLocked(now)
}

// endMedia 冻结发送时长；接收尾包窗口仍属于 media 阶段，但不继续累计发送秒数。
func (p *progressWriter) endMedia(now time.Time) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mediaElapsed, p.mediaEnded = now.Sub(p.mediaStarted), true
	return p.publishLocked(now)
}

// beginTeardown 在开始挂断前立即发布拆线阶段；此时媒体普通计数的汇总仍由主流程负责。
func (p *progressWriter) beginTeardown(now time.Time) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = "teardown"
	return p.publishLocked(now)
}

// snapshotLocked 先读已确认结果，再读启动计数，避免并发拨号时结果数大于已尝试数。
// 调用方必须持有 mu，或处于尚未启动发布协程的构造阶段。
func (p *progressWriter) snapshotLocked(now time.Time) progressSnapshot {
	snapshot := progressSnapshot{Phase: p.phase, RequestedCalls: p.requested, ElapsedSeconds: max(0, now.Sub(p.started).Seconds())}
	if p.bench != nil {
		snapshot.SetupFailed = p.bench.setupFailed.Load()
		for _, f := range p.bench.flows {
			if f.accepted.Load() {
				snapshot.EstablishedCalls++
			}
		}
		snapshot.AttemptedCalls = p.bench.attemptedCalls.Load()
	}
	if !p.mediaStarted.IsZero() {
		elapsed := p.mediaElapsed
		if !p.mediaEnded {
			elapsed = now.Sub(p.mediaStarted)
		}
		snapshot.MediaElapsedSeconds = max(0, elapsed.Seconds())
	}
	return snapshot
}

// publishLocked 保存首个错误，使后续阶段转换不能在失败后再次发布 finished。
func (p *progressWriter) publishLocked(now time.Time) error {
	if p.err == nil {
		p.err = writeProgressAtomically(p.path, p.snapshotLocked(now))
	}
	return p.err
}

// close 等待周期发布结束后提交最终观测；false 保留当前阶段，不能把验收失败标为完成。
// 该操作可重复调用，且空接收者用于保持未指定 --progress 时的原有行为。
func (p *progressWriter) close(finished bool) error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		close(p.stop)
		<-p.done
		p.mu.Lock()
		defer p.mu.Unlock()
		if finished && p.err == nil {
			p.phase = "finished"
		}
		_ = p.publishLocked(time.Now())
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// writeProgressAtomically 在同一目录写入 0600 临时文件并关闭，然后 Rename 替换完整快照。
// 读取者只能看到旧版或新版 JSON；这是可见性保证，不承诺断电持久化，也不创建操作者未指定的目录。
func writeProgressAtomically(path string, snapshot progressSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode progress: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create progress: %w", err)
	}
	defer os.Remove(temporary.Name())
	data = append(data, '\n')
	n, writeErr := temporary.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	closeErr := temporary.Close()
	if writeErr != nil {
		return fmt.Errorf("write progress: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close progress: %w", closeErr)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace progress: %w", err)
	}
	return nil
}
