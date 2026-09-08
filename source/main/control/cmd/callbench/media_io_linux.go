//go:build linux && (amd64 || arm64)

// 本文件复用 Go 网络轮询器执行 Linux 有界批量收发，连接仍由 net.UDPConn 持有。
package main

import (
	"encoding/binary"
	"net"
	"net/netip"
	"runtime"
	"syscall"
	"unsafe"
)

const mediaIOMode = "linux_mmsg"

// linuxMMsgHdr 对应 Linux amd64/arm64 的 mmsghdr：56 字节 msghdr、4 字节长度、4 字节对齐。
// 仅这两个已经限定为小端的 64 位目标使用此布局；其他目标保留标准库实现。
type linuxMMsgHdr struct {
	header syscall.Msghdr
	length uint32
	pad    uint32
}

// linuxPacketWriter 的数组与回调只分配一次；每个对象对应单 fd、单发送工作者。
type linuxPacketWriter struct {
	raw       syscall.RawConn
	connected bool
	headers   []linuxMMsgHdr
	iovecs    []syscall.Iovec
	addresses [][16]byte
	lengths   []int
	active    []mediaDatagram
	ready     func(uintptr) bool
	completed int
	err       error
	count     uint64
}

// newPacketWriter 不复制 fd；RawConn 负责在回调执行时保持原连接存活并遵守写截止时间。
func newPacketWriter(conn *net.UDPConn, connected bool, capacity int) (packetWriter, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	w := &linuxPacketWriter{raw: raw, connected: connected,
		headers: make([]linuxMMsgHdr, capacity), iovecs: make([]syscall.Iovec, capacity),
		addresses: make([][16]byte, capacity), lengths: make([]int, capacity)}
	w.ready = w.writeReady
	return w, nil
}

// writeBatch 每次至多提交 32 包。部分成功立即释放 RawConn 写锁，后缀由公共逻辑继续提交。
func (w *linuxPacketWriter) writeBatch(packets []mediaDatagram) ([]int, error) {
	w.active, w.completed, w.err = packets, 0, nil
	for i := range packets {
		p := &packets[i]
		w.iovecs[i] = syscall.Iovec{Base: &p.data[0]}
		w.iovecs[i].SetLen(len(p.data))
		w.headers[i] = linuxMMsgHdr{header: syscall.Msghdr{Iov: &w.iovecs[i], Iovlen: 1}}
		if !w.connected {
			address := &w.addresses[i]
			*address = [16]byte{syscall.AF_INET, 0}
			binary.BigEndian.PutUint16(address[2:4], p.destination.Port())
			ip := p.destination.Addr().As4()
			copy(address[4:8], ip[:])
			w.headers[i].header.Name, w.headers[i].header.Namelen = &address[0], 16
		}
	}
	err := w.raw.Write(w.ready)
	if err == nil {
		err = w.err
	}
	for i := 0; i < w.completed; i++ {
		w.lengths[i] = int(w.headers[i].length)
	}
	// 系统调用不保留用户内存；维持包体与描述符到调用返回的存活后即可释放活动切片引用。
	runtime.KeepAlive(packets)
	w.active = nil
	return w.lengths[:w.completed], err
}

// writeReady 一次成功调用即归还写锁；仅 EAGAIN 交轮询器，EINTR 在回调内有界重试。
func (w *linuxPacketWriter) writeReady(fd uintptr) bool {
	// 安全依据：headers 为固定布局的连续数组，iov/name 均指向本对象或活动包体；
	// 长度不超过 32，回调期间对象被 Go 引用，内核只在本次同步系统调用内访问这些地址。
	n, errno, attempts := retryInterrupted(func() (uintptr, syscall.Errno) {
		n, _, errno := syscall.Syscall6(linuxSendMMsg, fd, uintptr(unsafe.Pointer(&w.headers[0])), uintptr(len(w.active)), syscall.MSG_DONTWAIT, 0, 0)
		return n, errno
	})
	w.count += attempts
	if errno == syscall.EAGAIN {
		return false
	}
	if errno != 0 {
		w.err = errno
		return true
	}
	w.completed = int(n)
	return true
}

// calls 返回实际 sendmmsg 尝试数，包含因暂不可写而交还轮询器的调用。
func (w *linuxPacketWriter) calls() uint64 { return w.count }

// linuxPacketReader 独占一个 fd 的读取；单次就绪最多取 32 包，再让 Go 调度其他读者。
type linuxPacketReader struct {
	raw       syscall.RawConn
	headers   []linuxMMsgHdr
	iovecs    []syscall.Iovec
	addresses [][16]byte
	buffers   [][2048]byte
	packets   []receivedDatagram
	ready     func(uintptr) bool
	completed int
	err       error
	count     uint64
}

// newPacketReader 在计时前准备批量缓冲；容量按读取 socket 而非按每通共享呼叫分配。
func newPacketReader(conn *net.UDPConn, capacity int) (packetReader, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	r := &linuxPacketReader{raw: raw,
		headers: make([]linuxMMsgHdr, capacity), iovecs: make([]syscall.Iovec, capacity),
		addresses: make([][16]byte, capacity), buffers: make([][2048]byte, capacity),
		packets: make([]receivedDatagram, capacity)}
	r.ready = r.readReady
	for i := range r.headers {
		r.iovecs[i] = syscall.Iovec{Base: &r.buffers[i][0]}
		r.iovecs[i].SetLen(len(r.buffers[i]))
		r.headers[i].header = syscall.Msghdr{Iov: &r.iovecs[i], Iovlen: 1, Name: &r.addresses[i][0], Namelen: 16}
	}
	return r, nil
}

// readBatch 不传超时结构，不等凑包；现有连接读截止时间仍由 Go 轮询器统一处理。
func (r *linuxPacketReader) readBatch() ([]receivedDatagram, error) {
	r.completed, r.err = 0, nil
	for i := range r.headers {
		r.headers[i].header.Namelen, r.headers[i].header.Flags, r.headers[i].length = 16, 0, 0
	}
	err := r.raw.Read(r.ready)
	if err == nil {
		err = r.err
	}
	for i := 0; i < r.completed; i++ {
		header, address := &r.headers[i], &r.addresses[i]
		source := netip.AddrPort{}
		if header.header.Namelen == 16 && address[0] == syscall.AF_INET && address[1] == 0 {
			source = netip.AddrPortFrom(netip.AddrFrom4([4]byte{address[4], address[5], address[6], address[7]}), binary.BigEndian.Uint16(address[2:4]))
		}
		n := min(int(header.length), len(r.buffers[i]))
		if header.header.Flags&syscall.MSG_TRUNC != 0 {
			// 截断包不能因前缀恰好匹配而通过；空数据交给公共校验明确计为未知报文。
			n = 0
		}
		r.packets[i] = receivedDatagram{data: r.buffers[i][:n], source: source}
	}
	return r.packets[:r.completed], err
}

// readReady 不使用 MSG_WAITFORONE 等阻塞选项，保证回调与 fd 持有时间受批次上限约束。
func (r *linuxPacketReader) readReady(fd uintptr) bool {
	// 安全依据：所有描述符、源地址和包缓冲均由 r 持有，数组布局对应目标 Linux ABI；
	// 内核最多写入各 iovec 声明的 2048 字节，同步返回后才由 Go 读取长度和源地址。
	n, errno, attempts := retryInterrupted(func() (uintptr, syscall.Errno) {
		n, _, errno := syscall.Syscall6(syscall.SYS_RECVMMSG, fd, uintptr(unsafe.Pointer(&r.headers[0])), uintptr(len(r.headers)), syscall.MSG_DONTWAIT, 0, 0)
		return n, errno
	})
	r.count += attempts
	if errno == syscall.EAGAIN {
		return false
	}
	if errno != 0 {
		r.err = errno
		return true
	}
	r.completed = int(n)
	return true
}

// calls 返回实际 recvmmsg 尝试数，包含一次可读事件后已被排空而返回 EAGAIN 的尝试。
func (r *linuxPacketReader) calls() uint64 { return r.count }
