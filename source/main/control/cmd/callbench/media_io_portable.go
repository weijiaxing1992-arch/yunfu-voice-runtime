//go:build !linux || (!amd64 && !arm64)

// 本文件让不使用已核对 Linux mmsghdr ABI 的平台继续采用标准库 UDP。
package main

import "net"

const mediaIOMode = "portable_udp"

// newPacketWriter 不取得文件描述符所有权；连接仍由主流程统一关闭。
func newPacketWriter(conn *net.UDPConn, connected bool, _ int) (packetWriter, error) {
	return &portablePacketWriter{conn: conn, connected: connected}, nil
}

// newPacketReader 在媒体计时前创建复用缓冲；不改变既有连接的端口或过滤策略。
func newPacketReader(conn *net.UDPConn, _ int) (packetReader, error) {
	return &portablePacketReader{conn: conn}, nil
}
