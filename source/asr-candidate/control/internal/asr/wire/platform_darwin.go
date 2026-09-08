//go:build darwin

package wire

import (
	"errors"
	"golang.org/x/sys/unix"
	"math"
	"net"
)

// ClockNow 必须使用UPTIME_RAW，不能用Darwin CLOCK_MONOTONIC冒充Rust Instant域。
func ClockNow() (uint64, uint16, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_UPTIME_RAW, &ts); err != nil {
		return 0, 2, err
	}
	if ts.Sec < 0 || ts.Nsec < 0 || ts.Nsec >= 1_000_000_000 || uint64(ts.Sec) > (math.MaxUint64-uint64(ts.Nsec))/1_000_000_000 {
		return 0, 2, errors.New("ASR1 共享时钟溢出")
	}
	return uint64(ts.Sec)*1_000_000_000 + uint64(ts.Nsec), 2, nil
}

func PeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		inner = e
		if e == nil {
			uid = cred.Uid
			if cred.Version != 0 {
				inner = errors.New("ASR1 未知peer凭据版本")
			}
		}
	})
	if err != nil {
		return 0, err
	}
	return uid, inner
}
