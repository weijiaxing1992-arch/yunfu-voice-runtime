//go:build linux

package media

import "golang.org/x/sys/unix"

// Linux父子进程继承同一time namespace；此域与Rust RX使用的CLOCK_MONOTONIC相同。
const rxClockDomain uint16 = 1
const rxClockID = unix.CLOCK_MONOTONIC
