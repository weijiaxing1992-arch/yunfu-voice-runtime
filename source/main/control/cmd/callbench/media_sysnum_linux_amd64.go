//go:build linux && amd64

package main

// linuxSendMMsg 来自 Linux x86-64 系统调用表；Go syscall 冻结表未收录该后增调用。
const linuxSendMMsg = 307
