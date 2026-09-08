#!/usr/bin/env python3
"""真实 Rust worker 的 telephone-event 与 G.711 提示音回归。

只启动本测试子进程，所有 UDP 地址固定本机，默认仅在 2000–4999 中找空闲端口。
不访问 9080，不请求已有控制服务，不以原版 FreeSWITCH 互通或听感认证名义报告结果。
与其他本机媒体测试顺序运行；不能因绑定权限不足而跳过后宣称测试通过。
"""
import argparse
import json
import math
from pathlib import Path
import select
import socket
import struct
import subprocess
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]
MEDIA = ROOT / "bin/rustswitch-media"
CONNECTED = False
PORT_START, PORT_END = 2000, 4999


def endpoint():
    """仅在显式隔离低端口范围找空闲 UDP 端点，不借助可能落入主媒体范围的临时端口。"""
    for port in range(PORT_START, PORT_END + 1):
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            sock.bind(("127.0.0.1", port))
        except OSError as error:
            sock.close()
            if error.errno in (48, 98):
                continue
            raise
        sock.settimeout(1)
        return sock
    raise RuntimeError("隔离端口范围已耗尽")


def free_block():
    """探测完整四端口块，短暂预留只发生在声明的测试范围；冲突直接换下一完整块。"""
    for base in range((PORT_START + 1) // 2 * 2, PORT_END - 2, 4):
        sockets = []
        try:
            for port in range(base, base + 4):
                sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                sockets.append(sock)
                sock.bind(("127.0.0.1", port))
            return base
        except OSError as error:
            if error.errno not in (48, 98):
                raise
        finally:
            for sock in sockets:
                sock.close()
    raise RuntimeError("没有完整空闲测试媒体块")


def rtp(payload, sequence, timestamp, body, ssrc=1234):
    """独立按网络字节序构造 RTP，测试不调用服务端编码或解析代码。"""
    return struct.pack("!BBHII", 0x80, payload, sequence, timestamp, ssrc) + body


def decode_g711(value, alaw):
    """独立展开标准压扩码字，用于检查真实接收波形幅度和频率，不涉及音频设备。"""
    if alaw:
        value ^= 0x55
        segment = (value >> 4) & 7
        magnitude = ((value & 15) << 4) + (8 if segment == 0 else 264)
        if segment > 1:
            magnitude <<= segment - 1
        return magnitude if value & 128 else -magnitude
    value = (~value) & 255
    magnitude = (((value & 15) << 3) + 132) * (1 << ((value >> 4) & 7)) - 132
    return -magnitude if value & 128 else magnitude


class MediaInteraction(unittest.TestCase):
    """每项用例独占一个 worker 和四个终端端口，不依赖前项缓存或已建立通话。"""

    def setUp(self):
        """登记清理后才分配资源，失败也只终止本夹具拥有的进程。"""
        self.resources = []
        self.process = None
        self.log = tempfile.TemporaryFile()
        self.addCleanup(self.close)
        self.ar, self.ac, self.br, self.bc = [self.sock() for _ in range(4)]
        self.base = free_block()
        config = dict(worker_id=0, bind_ip="127.0.0.1", port_start=self.base,
                      port_end=self.base + 3, max_calls=1, receive_buffer_bytes=262144,
                      port_reuse_delay_ms=0, max_packets_per_second_per_leg=10000,
                      allowed_remote_networks=["127.0.0.0/8"], cpu_core=None,
                      connect_sockets=CONNECTED)
        self.process = subprocess.Popen([str(MEDIA), "--worker-config", json.dumps(config)],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.log, bufsize=0)
        self.sequence = 0
        self.assertEqual(self.reply()["type"], "ready")

    def close(self):
        """最多等待两秒后终止自己启动的 worker；不按名称查杀或接触其他进程。"""
        if self.process:
            if self.process.stdin:
                self.process.stdin.close()
            try:
                self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=2)
            if self.process.stdout:
                self.process.stdout.close()
        for sock in self.resources:
            sock.close()
        self.log.close()

    def sock(self):
        """将端点所有权登记到夹具，便于任何断言失败后完整释放。"""
        sock = endpoint()
        self.resources.append(sock)
        return sock

    def reply(self):
        """控制输出只接受有限等待的一行 JSON；卡死或异常退出作为真实失败。"""
        ready, _, _ = select.select([self.process.stdout], [], [], 3)
        self.assertTrue(ready, "媒体控制响应超时")
        line = self.process.stdout.readline(16384)
        self.assertTrue(line.endswith(b"\n"), "媒体控制行缺失或超长")
        return json.loads(line)

    def command(self, op, **fields):
        """发送内部命令并核对关联编号，不把业务错误当作成功应答。"""
        self.sequence += 1
        self.process.stdin.write(json.dumps(dict(id=self.sequence, op=op, **fields)).encode() + b"\n")
        result = self.reply()
        self.assertEqual(result["id"], self.sequence)
        return result

    def allocate(self, payload=0, connect=True, clock=8000, events="0-15", comfort_noise=False):
        """构造真实 offer 元数据；可只分配 A 腿验证无上游的本地 IVR。"""
        peer = lambda a, b: dict(rtp=f"127.0.0.1:{a.getsockname()[1]}", rtcp=f"127.0.0.1:{b.getsockname()[1]}")
        fields = dict(session=1, payload=payload, dtmf_payload=101,
                      dtmf_clock_rate=clock, dtmf_events=events)
        if comfort_noise:
            fields.update(cn_payload=13, cn_clock_rate=8000)
        self.assertTrue(self.command("allocate", a=peer(self.ar, self.ac), **fields)["ok"])
        if connect:
            self.assertTrue(self.command("connect", b=peer(self.br, self.bc), **fields)["ok"])

    def events(self, after=0):
        """取得真实全局日志，不根据发送脚本推断终端按键已被采集。"""
        response = self.command("dtmf_events", after_seq=after, limit=64)
        self.assertTrue(response["ok"])
        return response

    def wait_events(self, count, after=0, timeout=3):
        """用固定总期限轮询真实事件数量，未到齐不会无限等待。"""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            response = self.events(after)
            if len(response["events"]) >= count:
                return response
            time.sleep(0.01)
        self.fail(f"只收到 {len(response['events'])}/{count} 个媒体事件")

    def test_completed_dtmf_deduplicates_and_bridge_preserves_packets(self):
        """同事件完成重传全部透传，但仅采集一次；A/B 方向与 48k 时钟分别保留。"""
        self.allocate(clock=48000)
        packets = []
        for sequence, duration, end in [(1, 2400, False), (2, 4800, True), (3, 4800, True), (4, 4800, True)]:
            packet = rtp(101, sequence, 10000, bytes([5, 0x8a if end else 10]) + struct.pack("!H", duration))
            self.ar.sendto(packet, ("127.0.0.1", self.base))
            self.assertEqual(self.br.recv(8192), packet)
            packets.append(packet)
        got = self.wait_events(1)
        self.assertEqual(len(got["events"]), 1)
        event = got["events"][0]
        self.assertEqual((event["digit"], event["leg"], event["duration_ticks"], event["clock_rate"]), ("5", "a", 4800, 48000))
        packet = rtp(101, 1, 10000, bytes([11, 0x8a, 0x12, 0xc0]))
        self.br.sendto(packet, ("127.0.0.1", self.base + 2))
        self.assertEqual(self.ar.recv(8192), packet)
        event = self.wait_events(1, got["next_seq"])["events"][0]
        self.assertEqual((event["digit"], event["leg"]), ("#", "b"))

    def test_wrong_source_payload_and_event_never_collect(self):
        """地址、PT、事件集合及 R 位均参与验证，拒绝报文不能驱动业务收号。"""
        self.allocate(events="0-9")
        rogue = self.sock()
        packet = rtp(101, 1, 100, bytes([5, 0x80, 0x03, 0x20]))
        rogue.sendto(packet, ("127.0.0.1", self.base))
        for payload, event, flags in [(102, 5, 0x80), (101, 11, 0x80), (101, 5, 0xc0)]:
            self.ar.sendto(rtp(payload, 1, 100, bytes([event, flags, 0x03, 0x20])), ("127.0.0.1", self.base))
        time.sleep(0.08)
        self.assertEqual(self.events()["events"], [])
        stats = self.command("stats")["stats"]
        self.assertGreaterEqual(stats["invalid_packets"], 3)
        if not CONNECTED:
            self.assertEqual(stats["source_rejected"], 1)

    def test_missing_end_is_reported_and_release_cancels_pending(self):
        """未收到结束包只能产生不完整证据；释放后没有迟到定时器继续产生旧会话按键。"""
        self.allocate()
        self.ar.sendto(rtp(101, 1, 100, bytes([5, 10, 0x03, 0x20])), ("127.0.0.1", self.base))
        self.br.recv(8192)
        event = self.wait_events(1, timeout=3)["events"][0]
        self.assertEqual((event["kind"], event["digit"], event["reason"]), ("incomplete", "", "end_packet_not_received"))
        self.ar.sendto(rtp(101, 2, 1000, bytes([6, 10, 0x03, 0x20])), ("127.0.0.1", self.base))
        self.br.recv(8192)
        self.assertTrue(self.command("release", session=1)["ok"])
        time.sleep(2.05)
        self.assertEqual(len(self.events()["events"]), 1)

    def check_tone(self, payload, local):
        """验证真实 G.711 报文、RTP 序号/时间戳和正弦相关度，完成状态不能替代收包断言。"""
        self.allocate(payload=payload, connect=not local)
        start = self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=1000, duration_ms=200)
        self.assertTrue(start["ok"], start)
        packets = [self.ar.recv(8192) for _ in range(10)]
        for index, packet in enumerate(packets):
            self.assertEqual((len(packet), packet[1] & 127), (172, payload))
            self.assertEqual(struct.unpack("!HI", packet[2:8]), (index, index * 160))
        samples = [decode_g711(value, payload == 8) for packet in packets for value in packet[12:]]
        expected = [math.sin(2 * math.pi * 1000 * index / 8000) for index in range(len(samples))]
        correlation = sum(a * b for a, b in zip(samples, expected)) / math.sqrt(sum(a * a for a in samples) * sum(b * b for b in expected))
        self.assertGreater(correlation, 0.995)
        self.assertGreater(max(samples), 7000)
        deadline = time.monotonic() + 1
        while time.monotonic() < deadline:
            state = self.command("playback_status", session=1, playback_id=1)
            if state["state"] != "running":
                break
            time.sleep(0.01)
        self.assertEqual((state["state"], state["sent_packets"], state["total_packets"]), ("completed", 10, 10))
        self.assertTrue(self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=1000, duration_ms=200)["ok"])
        self.ar.settimeout(0.05)
        with self.assertRaises(socket.timeout):
            self.ar.recv(8192)
        self.ar.settimeout(1)
        if local:
            self.assertFalse(self.command("playback_start", session=1, playback_id=2, leg="b", frequency_hz=1000, duration_ms=200)["ok"])
            self.ar.sendto(rtp(101, 1, 100, bytes([3, 0x80, 0x03, 0x20])), ("127.0.0.1", self.base))
            self.assertEqual(self.wait_events(1)["events"][0]["digit"], "3")
        else:
            packet = rtp(payload, 55, 999, bytes([0x55]) * 160)
            self.br.sendto(packet, ("127.0.0.1", self.base + 2))
            self.assertEqual(self.ar.recv(8192), packet)

    def test_pcmu_tone_and_bridge_restoration(self):
        """PCMU 提示音完成后恢复原样桥转发。"""
        self.check_tone(0, False)

    def test_pcma_tone_and_bridge_restoration(self):
        """PCMA 使用 A-law 真正编码，不能用 μ-law 改 PT 冒充。"""
        self.check_tone(8, False)

    def test_local_a_leg_ivr_without_any_b_peer(self):
        """只 Allocate A 腿便可播放和收号，不创建假 B 地址或请求上游。"""
        self.check_tone(0, True)

    def test_playback_stop_is_bounded_and_ids_are_owned(self):
        """停止幂等、编号所有权和参数拒绝均验证，主动停止不返回 completed。"""
        self.allocate()
        self.assertFalse(self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=1000, duration_ms=21)["ok"])
        self.assertTrue(self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=1000, duration_ms=1000)["ok"])
        self.ar.recv(8192)
        self.assertFalse(self.command("playback_stop", session=1, playback_id=2)["ok"])
        self.assertFalse(self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=1200, duration_ms=1000)["ok"])
        stopped = self.command("playback_stop", session=1, playback_id=1)
        self.assertEqual(stopped["state"], "stopped")
        self.assertLess(stopped["sent_packets"], stopped["total_packets"])
        self.assertEqual(self.command("playback_stop", session=1, playback_id=1)["state"], "stopped")
        self.assertTrue(self.command("release", session=1)["ok"])
        self.assertFalse(self.command("playback_status", session=1, playback_id=1)["ok"])

    def test_playback_preserves_reverse_audio_and_both_dtmf_legs(self):
        """提示音只替代目的腿主音频，反方向主音频、双腿按键仍真实透传并采集。"""
        self.allocate(comfort_noise=True)
        self.assertTrue(self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=1000, duration_ms=1000)["ok"])
        self.ar.recv(8192)
        suppressed = rtp(0, 50, 5555, bytes([0x33]) * 160, ssrc=444)
        self.br.sendto(suppressed, ("127.0.0.1", self.base + 2))
        noise = rtp(13, 51, 5555, bytes([40]), ssrc=444)
        self.br.sendto(noise, ("127.0.0.1", self.base + 2))
        reverse = rtp(0, 51, 6666, bytes([0x55]) * 160, ssrc=445)
        self.ar.sendto(reverse, ("127.0.0.1", self.base))
        self.assertEqual(self.br.recv(8192), reverse)
        digit_a = rtp(101, 52, 10000, bytes([2, 0x80, 0x03, 0x20]))
        digit_b = rtp(101, 53, 20000, bytes([4, 0x80, 0x03, 0x20]))
        self.ar.sendto(digit_a, ("127.0.0.1", self.base))
        self.assertEqual(self.br.recv(8192), digit_a)
        self.br.sendto(digit_b, ("127.0.0.1", self.base + 2))
        deadline = time.monotonic() + 0.5
        found = False
        while time.monotonic() < deadline:
            received = self.ar.recv(8192)
            self.assertNotEqual(received, suppressed)
            self.assertNotEqual(received, noise)
            if received == digit_b:
                found = True
                break
        self.assertTrue(found, "播放期间 B 腿按键没有到达 A")
        events = self.wait_events(2)["events"]
        self.assertEqual([(event["leg"], event["digit"]) for event in events], [("a", "2"), ("b", "4")])
        self.assertEqual(self.command("stats")["stats"]["playback_replaced_packets"], 2)
        self.assertEqual(self.command("playback_stop", session=1, playback_id=1)["state"], "stopped")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--media", type=Path, default=MEDIA)
    parser.add_argument("--connected-media", action="store_true")
    args, remaining = parser.parse_known_args()
    MEDIA = args.media.resolve()
    CONNECTED = args.connected_media
    unittest.main(argv=[__file__, *remaining])
