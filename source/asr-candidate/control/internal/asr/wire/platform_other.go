//go:build !linux && !darwin

package wire

import (
	"errors"
	"net"
)

func ClockNow() (uint64, uint16, error) {
	return 0, 0, errors.New("ASR1 不支持本平台共享时钟")
}
func PeerUID(*net.UnixConn) (uint32, error) {
	return 0, errors.New("ASR1 不支持本平台peer凭据")
}
