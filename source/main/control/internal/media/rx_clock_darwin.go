//go:build darwin

package media

import "golang.org/x/sys/unix"

// Darwin必须使用Mach uptime域；CLOCK_MONOTONIC在此平台不是Rust Instant的时钟源。
const rxClockDomain uint16 = 2
const rxClockID = unix.CLOCK_UPTIME_RAW
