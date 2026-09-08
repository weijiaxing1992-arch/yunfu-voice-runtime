package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// readProgress 读取完整快照；解码失败意味着读取者观察到了不完整文件，测试必须直接失败。
func readProgress(t *testing.T, path string) progressSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot progressSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestProgressPhasesAndFrozenMedia 验证立即发布阶段、累计呼叫口径和冻结媒体计时。
func TestProgressPhasesAndFrozenMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	p, err := newProgressWriter(path, "", 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer p.close(false)
	if snapshot := readProgress(t, path); snapshot.Phase != "starting" || snapshot.RequestedCalls != 2 || snapshot.AttemptedCalls != 0 || snapshot.MediaElapsedSeconds != 0 {
		t.Fatalf("unexpected starting snapshot: %+v", snapshot)
	}
	b := &bench{flows: []*flow{{}, {}}}
	if err := p.beginDialing(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	if snapshot := readProgress(t, path); snapshot.Phase != "dialing" {
		t.Fatalf("unexpected dialing snapshot: %+v", snapshot)
	}
	b.attemptedCalls.Add(2)
	b.flows[0].accepted.Store(true)
	b.setupFailed.Add(1)
	mediaStart := time.Now()
	if err := p.beginMedia(mediaStart); err != nil {
		t.Fatal(err)
	}
	if snapshot := readProgress(t, path); snapshot.Phase != "media" || snapshot.AttemptedCalls != 2 || snapshot.EstablishedCalls != 1 || snapshot.SetupFailed != 1 {
		t.Fatalf("unexpected media snapshot: %+v", snapshot)
	}
	if err := p.endMedia(mediaStart.Add(1250 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := p.beginTeardown(mediaStart.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if snapshot := readProgress(t, path); snapshot.Phase != "teardown" || snapshot.MediaElapsedSeconds != 1.25 || snapshot.EstablishedCalls != 1 {
		t.Fatalf("teardown changed media duration or cumulative calls: %+v", snapshot)
	}
	// 此例含建立失败，停止发布时保留 teardown；不会用 finished 暗示通过。
	if err := p.close(false); err != nil {
		t.Fatal(err)
	}
	if snapshot := readProgress(t, path); snapshot.Phase != "teardown" {
		t.Fatalf("failed run was marked finished: %+v", snapshot)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected snapshot permissions: %v, %v", info, err)
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files were left behind: %v, %v", files, err)
	}
}

// TestProgressFinishedAfterExplicitSuccess 确认只有显式成功结束才发布 finished，重复清理不会覆盖它。
func TestProgressFinishedAfterExplicitSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	p, err := newProgressWriter(path, "", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := &bench{flows: []*flow{{}}}
	b.attemptedCalls.Add(1)
	b.flows[0].accepted.Store(true)
	for _, transition := range []func() error{
		func() error { return p.beginDialing(b, time.Now()) },
		func() error { return p.beginMedia(time.Now()) },
		func() error { return p.endMedia(time.Now()) },
		func() error { return p.beginTeardown(time.Now()) },
		func() error { return p.close(true) },
		func() error { return p.close(false) },
	} {
		if err := transition(); err != nil {
			t.Fatal(err)
		}
	}
	if snapshot := readProgress(t, path); snapshot.Phase != "finished" || snapshot.EstablishedCalls != 1 {
		t.Fatalf("unexpected final snapshot: %+v", snapshot)
	}
}

// TestProgressConcurrentReaders 验证原子替换与并发拨号观测，不启动 SIP 服务或发送媒体包。
// 普通媒体数组在另一个协程持续写入，用 race 检查保证进度发布不偷偷读取这些数组。
func TestProgressConcurrentReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	b := &bench{flows: make([]*flow, 128)}
	for i := range b.flows {
		b.flows[i] = &flow{}
	}
	p, err := newProgressWriter(path, "", len(b.flows), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer p.close(false)
	if err := p.beginDialing(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	errors := make(chan error, 8)
	var workers sync.WaitGroup
	var stopOnce sync.Once
	stopWorkers := func() {
		stopOnce.Do(func() { close(stop) })
		workers.Wait()
	}
	defer stopWorkers()
	for reader := 0; reader < 4; reader++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(path)
				if err != nil {
					errors <- err
					return
				}
				var snapshot progressSnapshot
				if err := json.Unmarshal(data, &snapshot); err != nil {
					errors <- err
					return
				}
				if snapshot.AttemptedCalls > 128 || snapshot.EstablishedCalls+snapshot.SetupFailed > snapshot.AttemptedCalls {
					errors <- fmt.Errorf("inconsistent call counts: %+v", snapshot)
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i, f := range b.flows {
			b.attemptedCalls.Add(1)
			if i%2 == 0 {
				f.accepted.Store(true)
			} else {
				b.setupFailed.Add(1)
			}
			// 这些普通数组不属于进度契约，不应出现在任何进度采样路径中。
			f.sent[0]++
			f.received[1]++
		}
		for {
			select {
			case <-stop:
				return
			default:
				b.flows[0].sent[0]++
				b.flows[0].received[1]++
			}
		}
	}()
	// 主动重复采样，让多个读取者覆盖足够多次 Rename；另外等待一次真实 500 ms 发布。
	for i := 0; i < 64; i++ {
		p.mu.Lock()
		err := p.publishLocked(time.Now())
		p.mu.Unlock()
		if err != nil {
			errors <- err
			break
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for snapshot := readProgress(t, path); snapshot.ElapsedSeconds < 0.5; snapshot = readProgress(t, path) {
		if time.Now().After(deadline) {
			errors <- fmt.Errorf("periodic progress was not published")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopWorkers()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

// TestProgressWriteFailurePreservesLastPhase 模拟目标目录突然不可用，确认旧 JSON 保持完整且不能假报 finished。
func TestProgressWriteFailurePreservesLastPhase(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "live")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "progress.json")
	p, err := newProgressWriter(path, "", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer p.close(false)
	b := &bench{flows: []*flow{{}}}
	if err := p.beginDialing(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	moved := directory + "-moved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := p.beginMedia(time.Now()); err == nil {
		t.Fatal("missing output directory did not fail")
	}
	if err := p.close(true); err == nil {
		t.Fatal("failed progress writer reported successful close")
	}
	if snapshot := readProgress(t, filepath.Join(moved, "progress.json")); snapshot.Phase != "dialing" {
		t.Fatalf("write failure changed the last complete phase: %+v", snapshot)
	}
}

// TestProgressDisabledAndInvalidPaths 验证空选项不创建资源，显式路径不得覆盖报告或自动创建目录。
func TestProgressDisabledAndInvalidPaths(t *testing.T) {
	p, err := newProgressWriter("", "", 1, time.Now())
	if p != nil || err != nil {
		t.Fatalf("disabled progress created state: %v, %v", p, err)
	}
	if err := p.beginDialing(nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.beginMedia(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.endMedia(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.beginTeardown(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.close(true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if _, err := newProgressWriter(path, path, 1, time.Now()); err == nil {
		t.Fatal("progress was allowed to overwrite the report")
	}
	if _, err := newProgressWriter(filepath.Join(t.TempDir(), "missing", "progress.json"), "", 1, time.Now()); err == nil {
		t.Fatal("missing parent directory was silently created")
	}
}
