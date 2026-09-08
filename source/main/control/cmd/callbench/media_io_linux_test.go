//go:build linux && (amd64 || arm64)

// 本文件在 Linux 上验证真实 mmsg ABI、同 fd 多目的地和截止时间；不构成服务器容量证明。
package main

import (
	"bytes"
	"net"
	"testing"
	"time"
	"unsafe"
)

// TestLinuxBatchLayout 验证构建目标上的布局，避免把 amd64 系统调用结构误用于其他 ABI。
func TestLinuxBatchLayout(t *testing.T) {
	var message linuxMMsgHdr
	if unsafe.Sizeof(message) != 64 || unsafe.Offsetof(message.length) != 56 {
		t.Fatalf("Linux mmsghdr 布局不符: size=%d length=%d", unsafe.Sizeof(message), unsafe.Offsetof(message.length))
	}
}

// TestLinuxBatchRoundTripAndReadDeadline 验证真实批量发送保留每包目的地、内容和来源，且读超时仍可退出。
func TestLinuxBatchRoundTripAndReadDeadline(t *testing.T) {
	listen := func() *net.UDPConn {
		t.Helper()
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err = conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	sender := listen()
	receivers := []*net.UDPConn{listen(), listen()}
	writer, err := newPacketWriter(sender, false, 16)
	if err != nil {
		t.Fatal(err)
	}
	packets := make([]mediaDatagram, 10)
	for i := range packets {
		packets[i] = mediaDatagram{flow: &flow{index: i}, data: bytes.Repeat([]byte{byte(i + 1)}, 172), destination: receivers[i%2].LocalAddr().(*net.UDPAddr).AddrPort()}
	}
	transmitPackets(writer, packets)
	for _, packet := range packets {
		if packet.flow.sent[0] != 1 || packet.flow.writeErrors[0] != 0 {
			t.Fatal("真实 sendmmsg 未逐包完整提交")
		}
	}
	for side, conn := range receivers {
		reader, err := newPacketReader(conn, 16)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[byte]bool{}
		for len(seen) < 5 {
			received, err := reader.readBatch()
			if err != nil {
				t.Fatal(err)
			}
			for _, packet := range received {
				if packet.source != sender.LocalAddr().(*net.UDPAddr).AddrPort() || len(packet.data) != 172 {
					t.Fatal("recvmmsg 丢失来源或包长")
				}
				index := int(packet.data[0]) - 1
				if index < 0 || index >= 10 || index%2 != side || seen[packet.data[0]] || !bytes.Equal(packet.data, packets[index].data) {
					t.Fatal("同fd批次发生跨目的地、重复或内容损坏")
				}
				seen[packet.data[0]] = true
			}
		}
		if err = conn.SetReadDeadline(time.Now().Add(-time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		_, err = reader.readBatch()
		if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			t.Fatalf("RawConn 不遵守读取期限: %v", err)
		}
	}
}

// TestLinuxBatchTruncationNeverAcceptsPrefix 防止超长数据报的合法前缀被作为完整测试包接收。
func TestLinuxBatchTruncationNeverAcceptsPrefix(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.WriteToUDPAddrPort(make([]byte, 4096), conn.LocalAddr().(*net.UDPAddr).AddrPort()); err != nil {
		t.Fatal(err)
	}
	reader, err := newPacketReader(conn, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, err := reader.readBatch()
	if err != nil || len(packets) != 1 || len(packets[0].data) != 0 {
		t.Fatalf("截断包未转为明确校验失败: packets=%d err=%v", len(packets), err)
	}
}
