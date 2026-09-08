//go:build linux

package wire

import (
	"errors"
	"golang.org/x/sys/unix"
	"math"
	"net"
)

// ClockNow 与同机RXS2使用相同MONOTONIC域；不支持跨time namespace的代理。
func ClockNow() (uint64, uint16, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, 1, err
	}
	if ts.Sec < 0 || ts.Nsec < 0 || ts.Nsec >= 1_000_000_000 || uint64(ts.Sec) > (math.MaxUint64-uint64(ts.Nsec))/1_000_000_000 {
		return 0, 1, errors.New("ASR1 共享时钟溢出")
	}
	return uint64(ts.Sec)*1_000_000_000 + uint64(ts.Nsec), 1, nil
}

func PeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		inner = e
		if e == nil {
			uid = cred.Uid
		}
	})
	if err != nil {
		return 0, err
	}
	return uid, inner
}
