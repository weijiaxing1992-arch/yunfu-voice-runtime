//go:build linux || darwin

// 本文件使用 Linux/macOS 的资源统计接口，报告压测生成器自身的资源消耗。
package main

import (
	"runtime"
	"syscall"
)

// processUsage 读取当前生成器进程的累计 CPU 时间、常驻内存峰值和当前 Go 堆快照。
// 这些值不是 RustSwitch 服务端用量；系统调用失败时省略相应字段，不能解释为零消耗。
func processUsage() map[string]any {
	var usage syscall.Rusage
	result := map[string]any{}
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) == nil {
		// RUSAGE_SELF 包含本进程线程累计的用户态/内核态 CPU，秒数不等同于墙钟测试时长。
		result["user_cpu_seconds"] = float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6
		result["system_cpu_seconds"] = float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6
		rss := usage.Maxrss
		if runtime.GOOS == "linux" {
			// Linux Maxrss 以 KiB 返回，macOS 已是字节，统一输出为字节。
			rss *= 1024
		}
		result["peak_resident_bytes"] = rss
	}
	var memory runtime.MemStats
	// 堆快照仅反映 Go 管理的当前内存；不能与进程历史常驻内存峰值混作同一指标。
	runtime.ReadMemStats(&memory)
	result["heap_allocated_bytes"] = memory.HeapAlloc
	result["heap_objects"] = memory.HeapObjects
	return result
}
