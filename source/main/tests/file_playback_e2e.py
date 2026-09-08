#!/usr/bin/env python3
"""真实 Rust worker 的受限 WAV 文件播放回归；不访问已有 9080 或任何外部服务。

测试使用标准库构造固定 WAV，再独立解码收到的 G.711 RTP；文件被打开或应用返回 OK 均不能替代音频断言。
默认只使用 2000–4999 内空闲 UDP 端口，与其他本机测试顺序运行；绑定被禁止时必须报告失败。
"""
import argparse
import json
import math
import os
from pathlib import Path
import select
import socket
import struct
import subprocess
import tempfile
import time
import unittest

import media_interaction_e2e as media

MEDIA = media.MEDIA
CONNECTED = False


def wave_bytes(samples, format_tag=1):
    """独立生成三种允许的 RIFF/WAVE；G.711 额外声明 cbSize=0 和准确 fact 样本数。"""
    if format_tag == 1:
        data = b"".join(struct.pack("<h", value) for value in samples)
        fmt = struct.pack("<HHIIHH", 1, 1, 8000, 16000, 2, 16)
        fact = b""
    else:
        data = bytes(samples)
        fmt = struct.pack("<HHIIHHH", format_tag, 1, 8000, 8000, 1, 8, 0)
        fact = b"fact" + struct.pack("<II", 4, len(samples))
    body = b"WAVEfmt " + struct.pack("<I", len(fmt)) + fmt + fact
    body += b"data" + struct.pack("<I", len(data)) + data + (b"\0" if len(data) & 1 else b"")
    return b"RIFF" + struct.pack("<I", len(body)) + body


class FilePlayback(unittest.TestCase):
    """只复用现有夹具的有界 IPC 与端点助手，不重复导入其全部测试。"""
    sock = media.MediaInteraction.sock
    reply = media.MediaInteraction.reply
    command = media.MediaInteraction.command
    allocate = media.MediaInteraction.allocate
    events = media.MediaInteraction.events
    wait_events = media.MediaInteraction.wait_events
    close = media.MediaInteraction.close

    def setUp(self):
        """每项拥有专属提示根、进程和 UDP 端点；任何错误后均先停止自己的进程再删除文件。"""
        self.directory = tempfile.TemporaryDirectory(prefix="rustswitch-file-playback-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name) / "prompts"
        self.root.mkdir()
        self.resources = []
        self.process = None
        self.log = tempfile.TemporaryFile()
        self.addCleanup(self.close)
        self.ar, self.ac, self.br, self.bc = [self.sock() for _ in range(4)]
        self.base = media.free_block()
        self.sequence = 0
        self.start_worker(self.root)

    def start_worker(self, root):
        """只启动本夹具子进程，配置固定回环及可选提示根。"""
        config = dict(worker_id=0, bind_ip="127.0.0.1", port_start=self.base,
                      port_end=self.base + 3, max_calls=1, receive_buffer_bytes=262144,
                      port_reuse_delay_ms=0, max_packets_per_second_per_leg=10000,
                      allowed_remote_networks=["127.0.0.0/8"], cpu_core=None,
                      connect_sockets=CONNECTED, playback_root=str(root) if root else "")
        self.process = subprocess.Popen([str(MEDIA), "--worker-config", json.dumps(config)],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.log, bufsize=0)
        self.assertEqual(self.reply()["type"], "ready")

    def start_file(self, name="voice.wav", playback_id=1, leg="a"):
        """受理回复必须仍是 loading 且零分母，不允许把读取文件当作发送音频完成。"""
        reply = self.command("playback_file_start", session=1, playback_id=playback_id, leg=leg, path=name)
        self.assertTrue(reply["ok"], reply)
        self.assertEqual((reply["state"], reply["sent_packets"], reply["total_packets"]), ("loading", 0, 0))
        return reply

    def final_state(self, playback_id=1, timeout=3):
        """使用固定总期限等待文件任务终态，超时或失败不能由测试自动延长成成功。"""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            state = self.command("playback_status", session=1, playback_id=playback_id)
            self.assertTrue(state["ok"], state)
            if state["state"] not in ("loading", "running"):
                return state
            time.sleep(0.005)
        self.fail(f"文件播放未在总期限内结束：{state}")

    def assert_no_later_audio(self, duration=0.12):
        """先清理停止确认之前已到达的报文，再验证之后没有迟到文件继续发音。"""
        self.ar.setblocking(False)
        try:
            while True:
                self.ar.recv(8192)
        except BlockingIOError:
            pass
        self.ar.settimeout(duration)
        with self.assertRaises(socket.timeout):
            self.ar.recv(8192)
        self.ar.settimeout(1)

    def check_waveform(self, output_payload, source_format=1):
        """逐样本对比真实编码波形及尾包静音，RTP 分母、序号和时间戳均独立计算。"""
        pcm = [round(math.sin(2 * math.pi * 500 * index / 8000) * 7000) for index in range(401)]
        if source_format != 1:
            encoded = ([0xd5, 0xfa, 0xea, 0x55, 0x7a, 0x6a] if source_format == 6 else [0xff, 0xc0, 0xb0, 0x7f, 0x40, 0x30])
            source = [encoded[index % len(encoded)] for index in range(401)]
            pcm = [media.decode_g711(value, source_format == 6) for value in source]
        else:
            source = pcm
        (self.root / "voice.wav").write_bytes(wave_bytes(source, source_format))
        self.allocate(payload=output_payload, connect=False)
        self.start_file()
        packets = [self.ar.recv(8192) for _ in range(3)]
        for index, packet in enumerate(packets):
            self.assertEqual((len(packet), packet[1] & 127), (172, output_payload))
            self.assertEqual(struct.unpack("!HI", packet[2:8]), (index, index * 160))
        received = [media.decode_g711(value, output_payload == 8) for packet in packets for value in packet[12:]]
        self.assertGreater(max(abs(value) for value in received), 1000)
        self.assertLessEqual(max(abs(a - b) for a, b in zip(received, pcm)), 1024)
        self.assertTrue(all(abs(value) <= 8 for value in received[len(pcm):]))
        state = self.final_state()
        self.assertEqual((state["state"], state["sent_packets"], state["total_packets"]), ("completed", 3, 3))
        # 同编号重试只返回已完成状态，不再次读取或播放文件。
        retry = self.command("playback_file_start", session=1, playback_id=1, leg="a", path="voice.wav")
        self.assertEqual(retry["state"], "completed")
        self.assert_no_later_audio()

    def test_pcm16_to_pcmu_actual_samples_and_tail(self):
        """PCM16 WAV 实际转成 μ-law，尾部不足一帧可见静音补齐。"""
        self.check_waveform(0)

    def test_pcm16_to_pcma_actual_samples_and_tail(self):
        """PCM16 WAV 实际转成 A-law，不能只改 RTP PT。"""
        self.check_waveform(8)

    def test_alaw_wave_to_pcma(self):
        """验证 WAVE format 6 容器及 A-law 实际声音样本。"""
        self.check_waveform(8, 6)

    def test_mulaw_wave_to_pcmu(self):
        """验证 WAVE format 7 容器及 μ-law 实际声音样本。"""
        self.check_waveform(0, 7)

    def test_invalid_and_missing_files_never_send_audio(self):
        """容器、格式、长度、符号链接和不存在文件都产生失败且零已发包。"""
        good = wave_bytes([1000] * 160)
        (self.root / "invalid.wav").write_bytes(b"not a wave container")
        stereo = bytearray(good)
        stereo[22:24] = struct.pack("<H", 2)
        (self.root / "stereo.wav").write_bytes(stereo)
        (self.root / "truncated.wav").write_bytes(good[:-1])
        (self.root / "huge.wav").write_bytes(b"\0" * (1048576 + 1))
        outside = Path(self.directory.name) / "outside.wav"
        outside.write_bytes(good)
        (self.root / "link.wav").symlink_to(outside)
        os.link(outside, self.root / "hard.wav")
        os.mkfifo(self.root / "pipe.wav")
        self.allocate(connect=False)
        for identifier, name in enumerate(["missing.wav", "invalid.wav", "stereo.wav", "truncated.wav", "huge.wav", "link.wav", "hard.wav", "pipe.wav"], 1):
            self.start_file(name, identifier)
            state = self.final_state(identifier)
            self.assertEqual((state["state"], state["sent_packets"], state["total_packets"]), ("failed", 0, 0), state)
            if name == "missing.wav":
                self.assertIn("file_not_found", state["message"])
        self.assert_no_later_audio()

    def test_invalid_paths_and_disabled_root_are_rejected(self):
        """地址和路径越界在受理前拒绝，空提示根保留明确关闭语义。"""
        self.allocate(connect=False)
        for name in ["../outside.wav", "/tmp/a.wav", "https://host/a.wav", "a\\b.wav", "a//b.wav", "a/./b.wav", "a.mp3"]:
            reply = self.command("playback_file_start", session=1, playback_id=1, leg="a", path=name)
            self.assertFalse(reply["ok"], name)
        self.process.stdin.close()
        self.process.wait(timeout=2)
        self.process.stdout.close()
        self.start_worker(None)
        self.allocate(connect=False)
        reply = self.command("playback_file_start", session=1, playback_id=1, leg="a", path="voice.wav")
        self.assertFalse(reply["ok"])
        self.assertIn("disabled", reply["message"])

    def test_loading_stop_and_new_tone_reject_late_file(self):
        """加载受理后立即停止，再启动新编号单音，旧加载不能重新占用输出。"""
        (self.root / "voice.wav").write_bytes(wave_bytes([7000] * 240000))
        self.allocate(connect=False)
        self.start_file()
        stopped = self.command("playback_stop", session=1, playback_id=1)
        self.assertEqual(stopped["state"], "stopped")
        self.assert_no_later_audio()
        self.assertTrue(self.command("playback_start", session=1, playback_id=2, leg="a", frequency_hz=1000, duration_ms=100)["ok"])
        packets = [self.ar.recv(8192) for _ in range(5)]
        values = [media.decode_g711(value, False) for value in packets[0][12:]]
        self.assertGreater(max(values), 7000)
        self.assertLess(min(values), -7000)
        self.assertEqual(self.final_state(2)["state"], "completed")
        self.assert_no_later_audio()

    def test_release_during_loading_has_no_late_audio_or_resources(self):
        """加载受理后挂断释放；超过加载期限仍不得复活旧任务或继续占用媒体会话。"""
        (self.root / "voice.wav").write_bytes(wave_bytes([7000] * 240000))
        self.allocate(connect=False)
        self.start_file()
        self.assertTrue(self.command("release", session=1)["ok"])
        self.assert_no_later_audio(2.1)
        self.assertEqual(self.command("stats")["stats"]["active_calls"], 0)
        self.assertFalse(self.command("playback_status", session=1, playback_id=1)["ok"])

    def test_file_dtmf_interrupt_and_bridge_restoration(self):
        """真实按键在播放期间仍透传采集；业务 stop 后文件结束，原桥字节继续完整转发。"""
        (self.root / "voice.wav").write_bytes(wave_bytes([7000] * 32000))
        self.allocate()
        self.start_file()
        self.ar.recv(8192)
        packet = media.rtp(101, 10, 8000, bytes([11, 0x80, 3, 32]))
        self.ar.sendto(packet, ("127.0.0.1", self.base))
        self.assertEqual(self.br.recv(8192), packet)
        self.assertEqual(self.wait_events(1)["events"][0]["digit"], "#")
        stopped = self.command("playback_stop", session=1, playback_id=1)
        self.assertEqual(stopped["state"], "stopped")
        self.assertLess(stopped["sent_packets"], stopped["total_packets"])
        self.assert_no_later_audio()
        bridge = media.rtp(0, 11, 10000, bytes([0x55]) * 160)
        self.br.sendto(bridge, ("127.0.0.1", self.base + 2))
        self.assertEqual(self.ar.recv(8192), bridge)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--media", type=Path, default=MEDIA)
    parser.add_argument("--connected-media", action="store_true")
    args, remaining = parser.parse_known_args()
    MEDIA, CONNECTED = args.media.resolve(), args.connected_media
    unittest.main(argv=[__file__, *remaining])
