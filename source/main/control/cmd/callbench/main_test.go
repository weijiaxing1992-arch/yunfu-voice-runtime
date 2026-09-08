// 本文件验证生成器自身的 RTP 归属与校验规则，不启动服务，也不构成吞吐或网络无损验收。
package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
)

// TestSharedEndpointValidatesFlowAndTimestamp 确认共享端点仍按 SSRC 分流，并拒绝重复、坏时间戳和错来源。
func TestSharedEndpointValidatesFlowAndTimestamp(t *testing.T) {
	// 使用仅作身份比较的连接对象，不绑定 UDP 端口；两个呼叫共享接收套接字。
	conn := &net.UDPConn{}
	source := netip.MustParseAddrPort("127.0.0.1:12345")
	b := &bench{}
	for i := 0; i < 2; i++ {
		f := &flow{index: i}
		f.accepted.Store(true)
		f.sockets[0] = conn
		f.destinations[0] = source
		f.mediaDestinations[0] = source
		b.flows = append(b.flows, f)
	}
	payload := bytes.Repeat([]byte{0xff}, 160)
	packet := make([]byte, 172)
	packet[0] = 0x80
	copy(packet[12:], payload)
	for i := 0; i < 2; i++ {
		binary.BigEndian.PutUint16(packet[2:4], 1)
		binary.BigEndian.PutUint32(packet[4:8], 160)
		binary.BigEndian.PutUint32(packet[8:12], uint32(i*2+2))
		b.recordRTP(conn, 0, source, packet, 200, payload)
	}
	// 两个不同 SSRC 的合法首包必须分别进入各自呼叫，不能因共享端口合并计数。
	if b.flows[0].received[0] != 1 || b.flows[1].received[0] != 1 {
		t.Fatal("shared endpoint mixed up SSRCs")
	}
	b.recordRTP(conn, 0, source, packet, 200, payload)
	// 同一时间戳的重传只增加重复数，不再次增加唯一接收数。
	if b.flows[1].duplicate[0] != 1 {
		t.Fatal("duplicate not identified")
	}
	binary.BigEndian.PutUint16(packet[2:4], 2)
	// 时间戳必须是 160 的整数倍，并与序列号匹配。
	binary.BigEndian.PutUint32(packet[4:8], 321)
	b.recordRTP(conn, 0, source, packet, 200, payload)
	if b.flows[1].invalid[0] != 1 {
		t.Fatal("timestamp corruption accepted")
	}
	binary.BigEndian.PutUint32(packet[4:8], 320)
	// 内容恢复合法后仍必须验证远端地址，防止串流包进入正常接收统计。
	b.recordRTP(conn, 0, netip.MustParseAddrPort("127.0.0.1:12346"), packet, 200, payload)
	if b.flows[1].invalid[0] != 2 {
		t.Fatal("wrong source accepted")
	}
	b.recordRTP(&net.UDPConn{}, 0, source, packet, 200, payload)
	// 即使 SSRC 和源地址正确，来自另一套接字组也必须计为无法归属的 RTP。
	if b.unknownRTP.Load() != 1 {
		t.Fatal("SSRC delivered to wrong socket group accepted")
	}
}
