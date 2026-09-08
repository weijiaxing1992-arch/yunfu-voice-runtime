// 本文件区分可立即重试的系统调用中断和需要等待就绪的 EAGAIN，不把信号中断误当作背压。
package main

import "syscall"

// maxInterruptedAttempts 限制一次 fd 回调被持续信号占用的时间；超限返回真实 EINTR 并令本次验收失败。
const maxInterruptedAttempts = 8

// retryInterrupted 在同一就绪回调内重试 EINTR；EAGAIN 与其他结果立即交给平台调用者处理。
// call 不会被存储或启动到其他协程中，生产闭包与内核缓冲的生命周期不会越过调用返回。
func retryInterrupted(call func() (uintptr, syscall.Errno)) (uintptr, syscall.Errno, uint64) {
	for attempt := uint64(1); attempt <= maxInterruptedAttempts; attempt++ {
		n, errno := call()
		if errno != syscall.EINTR {
			return n, errno, attempt
		}
	}
	return 0, syscall.EINTR, maxInterruptedAttempts
}
