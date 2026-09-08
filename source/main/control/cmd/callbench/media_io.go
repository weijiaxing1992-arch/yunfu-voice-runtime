// 本文件定义生成器的有界报文批次；每批只能属于同一个实际 UDP 套接字。
package main

import (
	"io"
	"net"
	"net/netip"
	"time"
)

// mediaBatchCapacity 限制一次批量调用占用的报文数，不等待凑满批次。
const mediaBatchCapacity = 32

// mediaDatagram 的包体在准备阶段分配，发送阶段仅由所属工作者更新 RTP 头。
type mediaDatagram struct {
	flow        *flow
	side        int
	data        []byte
	destination netip.AddrPort
}

// receivedDatagram 引用读者自己的复用缓冲，仅在下一次 readBatch 前有效。
type receivedDatagram struct {
	data   []byte
	source netip.AddrPort
}

// packetWriter 报告已提交的连续前缀的实际包长；发生错误后的后缀不能假装已发送。
// 每个对象只由一个工作者调用；calls 在工作者结束后汇总。
type packetWriter interface {
	writeBatch([]mediaDatagram) ([]int, error)
	calls() uint64
}

// packetReader 每次返回已就绪的一批包，不等待完整批次；对象和缓冲由单个读取协程持有。
type packetReader interface {
	readBatch() ([]receivedDatagram, error)
	calls() uint64
}

// portablePacketWriter 保留普通 UDP 路径，供 macOS 及未适配批量 ABI 的平台使用。
type portablePacketWriter struct {
	conn      *net.UDPConn
	connected bool
	lengths   [mediaBatchCapacity]int
	count     uint64
}

// writeBatch 对每个包保留真实 Write 结果；某次出错立即把未提交后缀交给公共统计逻辑。
func (w *portablePacketWriter) writeBatch(packets []mediaDatagram) ([]int, error) {
	for i := range packets {
		p := &packets[i]
		var n int
		var err error
		w.count++
		if w.connected {
			n, err = w.conn.Write(p.data)
		} else {
			n, err = w.conn.WriteToUDPAddrPort(p.data, p.destination)
		}
		if err != nil {
			return w.lengths[:i], err
		}
		w.lengths[i] = n
	}
	return w.lengths[:len(packets)], nil
}

// calls 统计普通 UDP API 调用次数；API 内部的内核重试不在此平台的计数边界内。
func (w *portablePacketWriter) calls() uint64 { return w.count }

// portablePacketReader 使用复用缓冲，不为每包创建新的 UDPAddr。
type portablePacketReader struct {
	conn    *net.UDPConn
	buffer  [2048]byte
	packets [1]receivedDatagram
	count   uint64
}

// readBatch 在普通平台返回单包，错误仍由调用者区分尾窗超时与提前退出。
func (r *portablePacketReader) readBatch() ([]receivedDatagram, error) {
	r.count++
	n, source, err := r.conn.ReadFromUDPAddrPort(r.buffer[:])
	if err != nil {
		return nil, err
	}
	r.packets[0] = receivedDatagram{data: r.buffer[:n], source: source}
	return r.packets[:], nil
}

// calls 与普通写路径一样统计 API 调用，而不是不可观察的内核重试。
func (r *portablePacketReader) calls() uint64 { return r.count }

// transmitPackets 对部分批量成功只重试尚未提交的后缀，绝不重复发送成功前缀。
// 短写和失败分别计入对应方向；本地失败不进入 sent，但仍令最终验收失败。
func transmitPackets(writer packetWriter, packets []mediaDatagram) {
	transmitPacketsObserved(writer, packets, time.Time{}, time.Time{})
}

// 非零planned仅用于g711诊断：调用前后给出本地写入时间区间，绝不把返回时刻当内核精确发出时刻。
// relay保持原有路径；短写/失败仍失败，成功前缀既不重试也不重复计数。
func transmitPacketsObserved(writer packetWriter, packets []mediaDatagram, planned, started time.Time) {
	for len(packets) > 0 {
		var before, after time.Time
		if !planned.IsZero() {
			before = time.Now()
		}
		lengths, err := writer.writeBatch(packets)
		if !planned.IsZero() {
			after = time.Now()
		}
		if len(lengths) > len(packets) {
			// 平台适配违约不能把越界结果用于计数；把剩余报文全部保留为写入失败。
			lengths, err = nil, io.ErrShortWrite
		}
		for i, n := range lengths {
			p := &packets[i]
			if n != len(p.data) {
				p.flow.writeErrors[p.side]++
			} else {
				p.flow.sent[p.side]++
				if !planned.IsZero() {
					recordProcessedWrite(p, planned, started, before, after)
				}
			}
		}
		packets = packets[len(lengths):]
		if err != nil || len(lengths) == 0 {
			for i := range packets {
				p := &packets[i]
				p.flow.writeErrors[p.side]++
			}
			return
		}
	}
}
