package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
	"unicode/utf8"

	"rustswitch/control/internal/media"
)

const (
	pcmWireHeader  = 64
	pcmWireReply   = 96
	pcmWireBody    = 1600 // 最多100ms原生8k单声道S16LE；长度先校验再读取。
	pcmWireMessage = 512
	pcmHello       = 0
	pcmBegin       = 1
	pcmPush        = 2
	pcmEnd         = 3
	pcmInterrupt   = 4
	pcmStatus      = 5
	pcmForget      = 6
	pcmPing        = 7
	pcmControlMode = 1
	pcmDataMode    = 2
)

// pcmWireRequest使用固定头与调用者提供的固定body缓冲，不把音频转为JSON或创建逐样本对象。
type pcmWireRequest struct {
	op           byte
	id           uint64
	token        [16]byte
	turn, offset uint64
	length       uint32
	arg1, arg2   uint16
}

func parsePCMHeader(b *[pcmWireHeader]byte) (pcmWireRequest, error) {
	r := pcmWireRequest{op: b[4], id: binary.LittleEndian.Uint64(b[8:16]), turn: binary.LittleEndian.Uint64(b[32:40]), offset: binary.LittleEndian.Uint64(b[40:48]), length: binary.LittleEndian.Uint32(b[48:52]), arg1: binary.LittleEndian.Uint16(b[52:54]), arg2: binary.LittleEndian.Uint16(b[54:56])}
	copy(r.token[:], b[16:32])
	if string(b[:4]) != "RVA1" || b[5] != 0 || b[6] != 0 || b[7] != 0 || binary.LittleEndian.Uint64(b[56:64]) != 0 || r.id == 0 || r.op > pcmPing || r.length > pcmWireBody {
		return r, errors.New("invalid RVA1 header")
	}
	zero := r.token == [16]byte{}
	valid := false
	switch r.op {
	case pcmHello:
		valid = zero && r.turn == 0 && r.offset == 0 && r.length == 0 && r.arg2 == 0 && (r.arg1 == pcmControlMode || r.arg1 == pcmDataMode)
	case pcmBegin:
		valid = zero && r.turn != 0 && r.offset == 0 && r.length > 0 && r.length <= 128
	case pcmPush:
		valid = !zero && r.turn != 0 && r.length >= 320 && r.length%320 == 0 && r.arg1 == 0 && r.arg2 == 0
	case pcmEnd:
		valid = !zero && r.turn != 0 && r.length == 0 && r.arg1 == 0 && r.arg2 == 0
	case pcmInterrupt:
		valid = !zero && r.turn != 0 && r.length == 0 && r.offset == 0 && r.arg2 == 0
	case pcmStatus, pcmForget:
		valid = !zero && r.turn != 0 && r.length == 0 && r.offset == 0 && r.arg1 == 0 && r.arg2 == 0
	case pcmPing:
		valid = zero && r.turn == 0 && r.offset == 0 && r.length == 0 && r.arg1 == 0 && r.arg2 == 0
	}
	if !valid {
		return r, errors.New("invalid RVA1 operation fields")
	}
	return r, nil
}

// readPCMRequest先允许空闲等首字节，再使用不可续期的一秒完整帧期限，慢字节无法无限占连接。
func readPCMRequest(c net.Conn, header *[pcmWireHeader]byte, body *[pcmWireBody]byte, first bool) (pcmWireRequest, error) {
	idle := 30 * time.Second
	frameDeadline := time.Now().Add(time.Second)
	if first {
		idle = time.Second
	}
	if err := c.SetReadDeadline(time.Now().Add(idle)); err != nil {
		return pcmWireRequest{}, err
	}
	if _, err := io.ReadFull(c, header[:1]); err != nil {
		return pcmWireRequest{}, err
	}
	if !first {
		frameDeadline = time.Now().Add(time.Second)
	}
	// HELLO从连接处理开始共一秒，不能通过在最后一刻发送首字节再续一秒。
	if err := c.SetReadDeadline(frameDeadline); err != nil {
		return pcmWireRequest{}, err
	}
	if _, err := io.ReadFull(c, header[1:]); err != nil {
		return pcmWireRequest{}, err
	}
	r, err := parsePCMHeader(header)
	if err != nil {
		return r, err
	}
	if _, err = io.ReadFull(c, body[:r.length]); err != nil {
		return r, err
	}
	return r, nil
}

// pcmWireResult只导出轮次与计数；不泄露内部worker/session/代次或进程路径。
type pcmWireResult struct {
	code                                   uint16
	token                                  [16]byte
	turn                                   uint64
	state                                  string
	accepted, sent, queued, discarded, age uint64
	buffer, prebuffer                      uint16
	message                                string
}

func pcmResult(r media.Reply, token [16]byte) pcmWireResult {
	return pcmWireResult{token: token, turn: r.TurnID, state: r.State, accepted: r.AcceptedSamples, sent: r.SentSamples, queued: r.QueuedSamples, discarded: r.DiscardedSamples, age: r.OldestAgeMS, buffer: r.BufferMS, prebuffer: r.PrebufferMS, message: r.Error}
}

func pcmWireError(err error) pcmWireResult {
	r := pcmWireResult{code: 3}
	if err == nil {
		return r
	}
	r.message = err.Error()
	var rejection *media.PCMRejection
	switch {
	case errors.Is(err, ErrPCMHandleBusy), errors.Is(err, media.ErrQueueFull), errors.As(err, &rejection) && rejection.Code == 8:
		r.code = 5
	case errors.Is(err, ErrPCMHandleRetired):
		r.code = 8
	case errors.Is(err, media.ErrPCMUnavailable):
		r.code = 2
	case errors.Is(err, ErrPCMBeginUnknown), errors.Is(err, media.ErrPCMOutcomeUnknown), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		r.code = 6
	}
	return r
}

func pcmWireState(state string) byte {
	switch state {
	case "buffering":
		return 1
	case "playing":
		return 2
	case "draining":
		return 3
	case "stopping":
		return 4
	case "completed":
		return 5
	case "stopped":
		return 6
	case "failed":
		return 7
	default:
		return 0
	}
}

// writePCMResult使用固定96字节头，计数保持u64精度。错误消息截至UTF8边界，不截断音频。
func writePCMResult(c net.Conn, r pcmWireRequest, result pcmWireResult, buffer *[pcmWireReply + pcmWireMessage]byte) error {
	clear(buffer[:])
	copy(buffer[:4], "RVR1")
	buffer[4], buffer[5] = r.op, pcmWireState(result.state)
	binary.LittleEndian.PutUint16(buffer[6:8], result.code)
	binary.LittleEndian.PutUint64(buffer[8:16], r.id)
	copy(buffer[16:32], result.token[:])
	for i, value := range [...]uint64{result.turn, result.accepted, result.sent, result.queued, result.discarded, result.age} {
		binary.LittleEndian.PutUint64(buffer[32+i*8:40+i*8], value)
	}
	binary.LittleEndian.PutUint32(buffer[80:84], uint32(result.queued*2))
	binary.LittleEndian.PutUint32(buffer[84:88], uint32(result.queued/8))
	binary.LittleEndian.PutUint16(buffer[88:90], result.buffer)
	binary.LittleEndian.PutUint16(buffer[90:92], result.prebuffer)
	message := result.message
	if len(message) > pcmWireMessage {
		message = message[:pcmWireMessage]
		for len(message) > 0 && !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	binary.LittleEndian.PutUint16(buffer[92:94], uint16(len(message)))
	copy(buffer[pcmWireReply:], message)
	if err := c.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	wire := buffer[:pcmWireReply+len(message)]
	for len(wire) > 0 {
		n, err := c.Write(wire)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
		wire = wire[n:]
	}
	return nil
}
