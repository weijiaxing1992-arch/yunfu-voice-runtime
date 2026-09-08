package server

import (
	"fmt"
	"io"
	"runtime/metrics"
	"sync"
	"syscall"
	"time"
)

// controllerRuntime 是当前 Go 控制进程的采样，不包含 Rust 媒体及压测发生器。
// 空值表示尚无第二次采样或当前 Go 版本不提供该项；不能把未知线程/CPU 当成零。
type controllerRuntime struct {
	SampledAt             string   `json:"sampled_at"`              // 资源采样时刻，最多每秒更新一次。
	Goroutines            uint64   `json:"goroutines"`              // 当前 Go 协程数，不等同于操作系统线程。
	PeakSampledGoroutines uint64   `json:"peak_sampled_goroutines"` // 本进程管理观测采样到的最高值。
	RuntimeThreads        *uint64  `json:"runtime_threads"`         // Go运行时拥有的存活线程；旧版本不提供时为空。
	GOMAXPROCS            uint64   `json:"gomaxprocs"`              // 可同时执行 Go 代码的线程额度，并非总线程数。
	HeapAllocatedBytes    uint64   `json:"heap_allocated_bytes"`    // Go堆对象实际占用字节。
	GoMemoryBytes         uint64   `json:"go_memory_bytes"`         // Go运行时映射字节扣除已归还堆页，不等同于RSS。
	UserCPUSeconds        *float64 `json:"user_cpu_seconds"`        // 当前控制进程累计用户态CPU秒。
	SystemCPUSeconds      *float64 `json:"system_cpu_seconds"`      // 当前控制进程累计内核态CPU秒。
	CPUCoresUsed          *float64 `json:"cpu_cores_used"`          // 相邻成功采样间使用的CPU核数，允许超过1。
}

// controllerState 加入无锁可读的有界队列和管理预算，不读取呼叫主循环私有映射。
type controllerState struct {
	controllerRuntime
	NormalQueue          int    `json:"normal_queue_length"`
	NormalCapacity       int    `json:"normal_queue_capacity"`
	CriticalQueue        int    `json:"critical_queue_length"`
	CriticalCapacity     int    `json:"critical_queue_capacity"`
	ResultsQueue         int    `json:"media_results_queue_length"`
	ResultsCapacity      int    `json:"media_results_queue_capacity"`
	AdminConnections     int64  `json:"admin_connections"`
	AdminConnectionLimit int    `json:"admin_connection_limit"`
	HeavyRequests        int64  `json:"admin_heavy_requests"`
	HeavyRequestLimit    int    `json:"admin_heavy_request_limit"`
	HeavyRejected        uint64 `json:"admin_heavy_rejected_total"`
}

// controllerObserver 缓存运行时采样，频繁刷新管理页不会触发同样频繁的运行时扫描。
type controllerObserver struct {
	mu      sync.Mutex
	at      time.Time
	lastCPU float64
	hadCPU  bool
	value   controllerRuntime
}

// snapshot 使用 runtime/metrics 的可选指标；不通过创建协程、线程或子进程来观测负载。
func (o *controllerObserver) snapshot(now time.Time) controllerRuntime {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.at.IsZero() && now.Sub(o.at) < time.Second {
		return o.value
	}
	names := []string{"/sched/goroutines:goroutines", "/sched/gomaxprocs:threads", "/sched/threads/total:threads", "/memory/classes/heap/objects:bytes", "/memory/classes/total:bytes", "/memory/classes/heap/released:bytes"}
	samples := make([]metrics.Sample, len(names))
	for i, name := range names {
		samples[i].Name = name
	}
	metrics.Read(samples)
	number := func(i int) uint64 {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			return samples[i].Value.Uint64()
		}
		return 0
	}
	v := controllerRuntime{SampledAt: now.UTC().Format(time.RFC3339Nano), Goroutines: number(0), GOMAXPROCS: number(1), HeapAllocatedBytes: number(3)}
	v.PeakSampledGoroutines = max(o.value.PeakSampledGoroutines, v.Goroutines)
	if samples[2].Value.Kind() == metrics.KindUint64 {
		n := number(2)
		v.RuntimeThreads = &n
	}
	if total, released := number(4), number(5); total >= released {
		v.GoMemoryBytes = total - released
	}
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) == nil {
		user := float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6
		system := float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6
		v.UserCPUSeconds, v.SystemCPUSeconds = &user, &system
		if o.hadCPU && now.After(o.at) && user+system >= o.lastCPU {
			cores := (user + system - o.lastCPU) / now.Sub(o.at).Seconds()
			v.CPUCoresUsed = &cores
		}
		o.lastCPU, o.hadCPU = user+system, true
	} else {
		o.hadCPU = false
	}
	o.at, o.value = now, v
	return v
}

// controllerSnapshot 把有界的运行时采样和当前队列计数合并；各项不是同一事务快照。
func (s *Server) controllerSnapshot() controllerState {
	load := s.adminLoad
	if load == nil {
		load = newAdminLoad()
	} // 未启动HTTP的隔离单元测试保持可读取。
	return controllerState{controllerRuntime: load.observer.snapshot(time.Now()),
		NormalQueue: len(s.normal), NormalCapacity: cap(s.normal), CriticalQueue: len(s.critical), CriticalCapacity: cap(s.critical),
		ResultsQueue: len(s.results), ResultsCapacity: cap(s.results), AdminConnections: load.connections.Load(),
		AdminConnectionLimit: adminConnectionLimit, HeavyRequests: load.active.Load(), HeavyRequestLimit: adminHeavyRequestLimit, HeavyRejected: load.rejected.Load()}
}

// controllerMetrics 只输出实际已获得的可选值；缺少线程/CPU数据时不伪造零值曲线。
func (s *Server) controllerMetrics(w io.Writer) {
	v := s.controllerSnapshot()
	fmt.Fprintf(w, "rustswitch_controller_goroutines %d\nrustswitch_controller_peak_sampled_goroutines %d\nrustswitch_controller_gomaxprocs %d\nrustswitch_controller_heap_allocated_bytes %d\nrustswitch_controller_go_memory_bytes %d\n", v.Goroutines, v.PeakSampledGoroutines, v.GOMAXPROCS, v.HeapAllocatedBytes, v.GoMemoryBytes)
	fmt.Fprintf(w, "rustswitch_controller_normal_queue_length %d\nrustswitch_controller_critical_queue_length %d\nrustswitch_controller_media_results_queue_length %d\nrustswitch_admin_connections %d\nrustswitch_admin_connection_limit %d\nrustswitch_admin_heavy_requests %d\nrustswitch_admin_heavy_request_limit %d\nrustswitch_admin_heavy_rejected_total %d\n", v.NormalQueue, v.CriticalQueue, v.ResultsQueue, v.AdminConnections, v.AdminConnectionLimit, v.HeavyRequests, v.HeavyRequestLimit, v.HeavyRejected)
	if v.RuntimeThreads != nil {
		fmt.Fprintf(w, "rustswitch_controller_runtime_threads %d\n", *v.RuntimeThreads)
	}
	if v.UserCPUSeconds != nil {
		fmt.Fprintf(w, "rustswitch_controller_user_cpu_seconds %g\n", *v.UserCPUSeconds)
	}
	if v.SystemCPUSeconds != nil {
		fmt.Fprintf(w, "rustswitch_controller_system_cpu_seconds %g\n", *v.SystemCPUSeconds)
	}
	if v.CPUCoresUsed != nil {
		fmt.Fprintf(w, "rustswitch_controller_cpu_cores_used %g\n", *v.CPUCoresUsed)
	}
}
