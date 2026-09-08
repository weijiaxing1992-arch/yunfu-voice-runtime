//go:build linux && arm64

package main

import "syscall"

// linuxSendMMsg 使用 Go 随附的 Linux arm64 系统调用号，不共用 x86-64 编号。
const linuxSendMMsg = syscall.SYS_SENDMMSG
