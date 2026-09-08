// 本文件通过确定性注入验证 EINTR，不依赖真实信号或网络时序。
package main

import (
	"syscall"
	"testing"
)

// TestInterruptedBatchRetriesWithoutWaitingForNewReadiness 防止持续可写 fd 被错误交给边缘触发轮询等待。
func TestInterruptedBatchRetriesWithoutWaitingForNewReadiness(t *testing.T) {
	calls := 0
	n, errno, attempts := retryInterrupted(func() (uintptr, syscall.Errno) {
		calls++
		if calls < 3 {
			return 0, syscall.EINTR
		}
		return 7, 0
	})
	if n != 7 || errno != 0 || attempts != 3 || calls != 3 {
		t.Fatal("EINTR 后未在同一次就绪处理内保留完整结果")
	}
	_, errno, attempts = retryInterrupted(func() (uintptr, syscall.Errno) { return 0, syscall.EAGAIN })
	if errno != syscall.EAGAIN || attempts != 1 {
		t.Fatal("EAGAIN 被忙循环重试")
	}
}

// TestInterruptedBatchSignalStormIsBounded 验证连续中断不会无限持有 fd，也不会伪装为成功或可轮询背压。
func TestInterruptedBatchSignalStormIsBounded(t *testing.T) {
	n, errno, attempts := retryInterrupted(func() (uintptr, syscall.Errno) { return 0, syscall.EINTR })
	if n != 0 || errno != syscall.EINTR || attempts != maxInterruptedAttempts {
		t.Fatal("连续信号中断未按上限返回真实错误")
	}
	if allocations := testing.AllocsPerRun(100, func() {
		retryInterrupted(func() (uintptr, syscall.Errno) { return 1, 0 })
	}); allocations != 0 {
		t.Fatalf("重试辅助引入逐调用分配: %f", allocations)
	}
}
