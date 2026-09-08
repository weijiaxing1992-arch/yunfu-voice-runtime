package main

import (
	"encoding/binary"
	"net"
	"net/netip"
	"rustswitch/control/internal/codecprofile"
	"testing"
)

// TestCodecRTPClockAndLength 防止新编码继续使用G711包长或把G722采样率误用成RTP时钟。
func TestCodecRTPClockAndLength(t *testing.T) {
	for _, profile := range codecprofile.List() {
		t.Run(profile.Name, func(t *testing.T) {
			conn := &net.UDPConn{}
			source := netip.MustParseAddrPort("127.0.0.1:12345")
			f := &flow{}
			f.accepted.Store(true)
			f.sockets[0] = conn
			f.mediaDestinations[0] = source
			b := &bench{packetType: profile.Payload, codecProfile: profile, flows: []*flow{f}}
			frame := profile.Frame()
			packet := make([]byte, 12+len(frame))
			packet[0] = 0x80
			packet[1] = profile.Payload
			copy(packet[12:], frame)
			binary.BigEndian.PutUint16(packet[2:4], 1)
			binary.BigEndian.PutUint32(packet[4:8], profile.TimestampStep())
			binary.BigEndian.PutUint32(packet[8:12], 2)
			b.recordRTP(conn, 0, source, packet, 200, frame)
			if f.received[0] != 1 || f.invalid[0] != 0 {
				t.Fatal("正确时钟/长度的包被拒绝")
			}
			binary.BigEndian.PutUint16(packet[2:4], 2)
			binary.BigEndian.PutUint32(packet[4:8], profile.TimestampStep()*2+1)
			b.recordRTP(conn, 0, source, packet, 200, frame)
			if f.invalid[0] != 1 {
				t.Fatal("错时间戳未被拒绝")
			}
		})
	}
	g722, _ := codecprofile.Lookup(9)
	opus, _ := codecprofile.Lookup(111)
	if g722.SampleRate != 16000 || g722.TimestampStep() != 160 || opus.TimestampStep() != 960 {
		t.Fatal("关键编码的音频采样率与RTP时钟被混淆")
	}
}
