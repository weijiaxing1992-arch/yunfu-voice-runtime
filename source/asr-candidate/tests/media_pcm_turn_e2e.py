#!/usr/bin/env python3
"""有界PCM turn的独立Rust IPC＋真实UDP验收；不访问主9080或外部线路。

JSON只承载控制，PCM通过明确传递的UnixDatagram FD3发送。一个select循环同时
读取控制/二进制回应和RTP/RTCP，避免测试自身阻塞控制调用制造媒体接收积压。
本套件不拥有SIP ACK状态；Go业务入口的ACK门控需另外真实SIP验收。
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import select
import signal
import socket
import struct
import subprocess
import sys
import time
import unittest
import uuid
import wave

import media_interaction_e2e as endpoints
import media_processed_fixture as oracle
from dtmf_send_e2e import assert_events

MEDIA = None
ARTIFACTS = None
CONNECTED = False
STATES = ("idle", "buffering", "playing", "draining", "stopping", "completed", "stopped", "failed")
SOURCE_SSRC = 0x7052434D
# 固定标准码字向量单独覆盖符号、饱和和分段边界；A-law并非对任意PCM取最近可表示值。
FULL_SCALE_SAMPLES = (-32768, -32124, -16384, -1000, 0, 1000, 16384, 32767)
FULL_SCALE_CODES = {
    "PCMA": (0x2a, 0x2a, 0x3a, 0x7a, 0xd5, 0xfa, 0xa5, 0xaa),
    "PCMU": (0x00, 0x00, 0x0f, 0x4e, 0xff, 0xce, 0x8f, 0x80),
}
LAUNCHER = '''import os,sys
fd=int(sys.argv[1])
if fd!=3:
    os.dup2(fd,3,inheritable=True)
    os.close(fd)
else:
    os.set_inheritable(3,True)
os.execv(sys.argv[2],[sys.argv[2],"--worker-config",sys.argv[3]])
'''


def digest(path):
    """实际文件流式指纹，避免把二进制一次性读入内存。"""
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            value.update(block)
    return value.hexdigest()


def pcm_samples(frames, seed=1):
    """逐帧变幅双频输入；用于发现重复、跳帧、乱序与误用旧turn。"""
    return [round((4100 + (i // 160 % 7) * 131 + seed * 23)
                  * math.sin(2 * math.pi * (367 + seed * 31) * i / 8000)
                  + 800 * math.sin(2 * math.pi * (631 + seed * 17) * i / 8000))
            for i in range(frames * 160)]


def request_packet(request_id, session, turn_id, offset, samples):
    """独立按固定44字节RSP1头和i16LE样本构造，不调用生产序列化。"""
    return struct.pack("<4sIQQQQHH", b"RSP1", 0, request_id, session, turn_id, offset, len(samples), 0) + struct.pack("<" + "h" * len(samples), *samples)


def response_packet(raw):
    """固定96字节回应，所有保留位和关联字段必须原样核验。"""
    if len(raw) != 96 or raw[:4] != b"RSR1" or raw[6:8] != b"\0\0" or raw[81:] != bytes(15):
        raise AssertionError("二进制PCM回应长度/魔数/保留位错误")
    code = struct.unpack_from("<H", raw, 4)[0]
    values = struct.unpack_from("<9Q", raw, 8)
    if code > 9 or raw[80] >= len(STATES):
        raise AssertionError("二进制PCM回应枚举越界")
    result = dict(zip(("request_id", "session", "requested_turn", "current_turn", "accepted_samples",
                       "sent_samples", "queued_samples", "discarded_samples", "oldest_age_ms"), values))
    result.update(code=code, state=STATES[raw[80]])
    return result


class PcmTurnFixture(unittest.TestCase):
    """每例独占worker、FD通道和四端口块；只有原始字节可作为播放结果。"""
    def setUp(self):
        self.resources, self.wire, self.rtp, self.rtcp = [], [], [], []
        self.responses, self.data_responses, self.observations = {}, {}, []
        self.stdout_buffer = bytearray()
        self.sequence = self.data_sequence = self.wire_bytes = 0
        self.started = time.monotonic()
        self.process = self.log = None
        self.allocated = set()
        self.directory = ARTIFACTS / (self.id().rsplit(".", 1)[-1] + "-" + uuid.uuid4().hex[:8])
        self.directory.mkdir(parents=True, exist_ok=False)
        self.addCleanup(self.close)
        self.binary_before = digest(MEDIA)
        self.source_paths = [Path(__file__), Path(endpoints.__file__), Path(oracle.__file__), Path(__file__).with_name("dtmf_send_e2e.py")]
        self.sources_before = {p.name: digest(p) for p in self.source_paths}
        self.ar, self.ac, self.br, self.bc, self.stranger = [self.sock() for _ in range(5)]
        self.base = endpoints.free_block()
        self.data, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)
        self.resources += [self.data, child]
        self.data.setblocking(False)
        self.pcm_lane = "without_pcm_fd" not in self.id()
        self.prompt = pcm_samples(20, 7)
        with wave.open(str(self.directory / "prompt.wav"), "wb") as stream:
            stream.setnchannels(1)
            stream.setsampwidth(2)
            stream.setframerate(8000)
            stream.writeframes(struct.pack("<" + "h" * len(self.prompt), *self.prompt))
        config = dict(worker_id=0, bind_ip="127.0.0.1", port_start=self.base, port_end=self.base + 3,
                      max_calls=1, receive_buffer_bytes=262144, port_reuse_delay_ms=0,
                      max_packets_per_second_per_leg=1000, allowed_remote_networks=["127.0.0.0/8"],
                      cpu_core=None, connect_sockets=CONNECTED, playback_root=str(self.directory))
        (self.directory / "worker-config.json").write_text(json.dumps(config, indent=2) + "\n")
        (self.directory / "launcher.py").write_text(LAUNCHER)
        self.fixture_before = {p.name: digest(p) for p in self.directory.iterdir() if p.is_file()}
        self.log = (self.directory / "worker.log").open("wb")
        env = dict(os.environ)
        env.pop("RUSTSWITCH_PCM_FD", None)
        command = [str(MEDIA), "--worker-config", json.dumps(config)]
        options = {}
        if self.pcm_lane:
            env["RUSTSWITCH_PCM_FD"] = "3"
            command = [sys.executable, str(self.directory / "launcher.py"), str(child.fileno()), str(MEDIA), json.dumps(config)]
            options["pass_fds"] = (child.fileno(),)
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.log,
                                        env=env, bufsize=0, **options)
        child.close()
        os.set_blocking(self.process.stdout.fileno(), False)
        self.ready = None
        self.wait(lambda: self.ready is not None, 5)
        self.assertEqual(self.ready["type"], "ready")
        self.assertIn("processed_g711_local_v1", self.ready["capabilities"])
        self.assertEqual("pcm_turn_v1" in self.ready["capabilities"], self.pcm_lane)

    def sock(self):
        result = endpoints.endpoint()
        result.setblocking(False)
        self.resources.append(result)
        return result

    def record(self, kind, raw, **metadata):
        """固定预算保留全部实际帧，超限失败，不能截断后仍宣称完整。"""
        self.wire_bytes += len(raw)
        self.assertLess(len(self.wire), 4096)
        self.assertLessEqual(self.wire_bytes, 8 * 1024 * 1024)
        self.wire.append(dict(direction=kind, at_ms=(time.monotonic() - self.started) * 1000,
                              bytes=len(raw), hex=raw.hex(), **metadata))

    def poll(self, timeout):
        """所有实际输入源在一个有界select中排空，控制回应不会遮蔽同时到达的媒体。"""
        sources = {self.ar: "rtp", self.ac: "rtcp", self.br: "unexpected_b_rtp", self.bc: "unexpected_b_rtcp", self.stranger: "unexpected_source"}
        if self.pcm_lane:
            sources[self.data] = "pcm_reply"
        sources[self.process.stdout] = "json_reply"
        ready, _, _ = select.select(list(sources), [], [], max(0, min(timeout, .02)))
        for source in ready:
            kind = sources[source]
            if kind == "json_reply":
                raw = os.read(source.fileno(), 65536)
                self.assertTrue(raw, "worker控制输出提前关闭")
                self.stdout_buffer.extend(raw)
                self.assertLessEqual(len(self.stdout_buffer), 65536)
                while b"\n" in self.stdout_buffer:
                    line, _, rest = self.stdout_buffer.partition(b"\n")
                    self.stdout_buffer = bytearray(rest)
                    self.record("json_receive", bytes(line) + b"\n")
                    value = json.loads(line)
                    if value.get("type") == "ready":
                        self.assertIsNone(self.ready)
                        self.ready = value
                    else:
                        self.assertNotIn(value["id"], self.responses)
                        self.responses[value["id"]] = value
                continue
            for _ in range(64):
                try:
                    raw, peer = source.recvfrom(8192)
                except BlockingIOError:
                    break
                self.record(kind + "_receive", raw, peer=list(peer) if isinstance(peer, tuple) else str(peer))
                self.assertFalse(kind.startswith("unexpected"), "PCM本地流发送到未协商腿或来源")
                if kind == "pcm_reply":
                    value = response_packet(raw)
                    self.assertNotIn(value["request_id"], self.data_responses)
                    self.data_responses[value["request_id"]] = value
                elif kind == "rtp":
                    self.assertEqual(peer, ("127.0.0.1", self.base))
                    packet = oracle.unpack_rtp(raw)
                    packet.update(raw=raw, at=time.monotonic())
                    self.rtp.append(packet)
                elif kind == "rtcp":
                    self.assertEqual(peer, ("127.0.0.1", self.base + 1))
                    self.rtcp.append(dict(raw=raw, parts=oracle.unpack_rtcp(raw), at=time.monotonic()))

    def wait(self, predicate, timeout=2):
        end = time.monotonic() + timeout
        while not predicate():
            self.assertLess(time.monotonic(), end, "有界等待超时，不能跳过或延长到成功")
            self.poll(end - time.monotonic())

    def tick(self, duration):
        end = time.monotonic() + duration
        while time.monotonic() < end:
            self.poll(end - time.monotonic())

    def send_network(self, raw, rtcp=False, source=None):
        """只发送到本用例刚分配的端口，记录实际来源；生产PCM输入不是网络入站的回声。"""
        source = source or (self.ac if rtcp else self.ar)
        destination = ("127.0.0.1", self.base + int(rtcp))
        self.record("rtcp_send" if rtcp else "rtp_send", raw,
                    source=list(source.getsockname()), destination=list(destination))
        self.assertEqual(source.sendto(raw, destination), len(raw))

    def command(self, op, **fields):
        self.sequence += 1
        request = dict(id=self.sequence, op=op, **fields)
        raw = json.dumps(request).encode() + b"\n"
        self.record("json_send", raw)
        self.assertEqual(self.process.stdin.write(raw), len(raw))
        self.wait(lambda: self.sequence in self.responses)
        return self.responses.pop(self.sequence)

    def allocate(self, payload=0, session=1, topology="local"):
        self.payload, self.law = payload, "PCMA" if payload == 8 else "PCMU"
        self.session = session
        peer = dict(rtp=f"127.0.0.1:{self.ar.getsockname()[1]}", rtcp=f"127.0.0.1:{self.ac.getsockname()[1]}")
        fields = dict(session=session, a=peer, payload=payload, dtmf_payload=101,
                      codec=dict(name=self.law, sample_rate=8000, rtp_clock_rate=8000, channels=1, ptime_ms=20, fmtp=""),
                      dtmf_clock_rate=8000, dtmf_events="0-15", cn_payload=13, cn_clock_rate=8000)
        if topology is not None:
            fields["processing"] = dict(version=1, mode="g711", topology=topology, jitter_target_ms=40, max_delay_ms=120)
        reply = self.command("allocate", **fields)
        self.assertTrue(reply["ok"], reply)
        self.allocated.add(session)
        return reply

    def turn(self, op, turn_id=1, expected=True, **fields):
        reply = self.command("pcm_turn_" + op, session=self.session, turn_id=turn_id, **fields)
        self.assertIs(reply["ok"], expected, reply)
        if expected:
            self.assertEqual(reply["type"], "pcm_turn_state")
            self.check_counts(reply)
            self.observations.append(dict(observed_at=time.time(), op=op, turn_id=turn_id, reply=reply))
        return reply

    def begin(self, turn_id=1, buffer=200, prebuffer=100, expected=True):
        return self.turn("begin", turn_id, expected=expected, buffer_ms=buffer, prebuffer_ms=prebuffer)

    def check_counts(self, value):
        accepted, sent, queued, discarded = [value[name + "_samples"] for name in ("accepted", "sent", "queued", "discarded")]
        self.assertTrue(all(type(x) is int and x >= 0 and x % 160 == 0 for x in (accepted, sent, queued, discarded)))
        self.assertEqual(accepted, sent + queued + discarded)
        if "queued_bytes" in value:
            self.assertEqual(value["queued_bytes"], queued * 2)
            self.assertEqual(value["queued_ms"], queued // 8)
            self.assertLessEqual(queued, value["buffer_ms"] * 8)
        self.assertIn(value["state"], STATES)

    def push(self, samples, offset=0, turn_id=1, expected=0, session=None, mutate=None):
        self.data_sequence += 1
        request_id = self.data_sequence
        session = self.session if session is None else session
        raw = request_packet(request_id, session, turn_id, offset, samples)
        if mutate:
            raw = mutate(raw)
        self.record("pcm_send", raw)
        self.assertEqual(self.data.send(raw), len(raw))
        self.wait(lambda: request_id in self.data_responses)
        reply = self.data_responses.pop(request_id)
        self.assertEqual((reply["request_id"], reply["session"], reply["requested_turn"], reply["code"]),
                         (request_id, session, turn_id, expected), reply)
        self.check_counts(reply)
        return reply

    def feed(self, samples, turn_id=1, offset=0):
        self.assertEqual(len(samples) % 160, 0)
        for start in range(0, len(samples), 800):
            self.push(samples[start:start + 800], offset + start, turn_id)

    def final(self, state, turn_id=1, timeout=2.5):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            result = self.turn("status", turn_id)
            if result["state"] == state:
                return result
            self.tick(.003)
        self.fail("PCM终态未及时到达：" + str(result))

    def audio(self, start=0):
        return [p for p in self.rtp[start:] if p["payload"] == self.payload]

    def assert_pcm(self, packets, samples, full_scale=False):
        self.assertEqual(len(samples) % 160, 0)
        self.assertEqual(len(packets) * 160, len(samples))
        actual = []
        for p in packets:
            self.assertEqual((p["payload"], len(p["body"])), (self.payload, 160))
            actual.extend(oracle.decode(code, self.law) for code in p["body"])
        if full_scale:
            # 饱和端点按G711可表示码字核对；不能拿非饱和向量的512误差界限误判格式本身。
            self.assertEqual(tuple(samples), FULL_SCALE_SAMPLES * (len(samples) // len(FULL_SCALE_SAMPLES)))
            expected = [oracle.decode(code, self.law) for code in FULL_SCALE_CODES[self.law]] * (len(samples) // len(FULL_SCALE_SAMPLES))
            self.assertEqual(actual, expected)
        else:
            self.assertLessEqual(max(abs(a - b) for a, b in zip(actual, samples)), 512)
        for a, b in zip(packets, packets[1:]):
            self.assertEqual((b["timestamp"] - a["timestamp"]) & 0xffffffff, 160)
            self.assertGreaterEqual(b["at"] - a["at"], .005, "PCM发生追赶突发")

    def assert_identity(self, packets=None):
        packets = self.rtp if packets is None else packets
        self.assertTrue(packets)
        self.assertEqual(len({p["ssrc"] for p in packets}), 1)
        for a, b in zip(packets, packets[1:]):
            self.assertEqual((b["sequence"] - a["sequence"]) & 65535, 1)
        audio = [p for p in packets if p["payload"] in (0, 8)]
        for a, b in zip(audio, audio[1:]):
            self.assertTrue(0 < ((b["timestamp"] - a["timestamp"]) & 0xffffffff) < 0x80000000,
                            "跨播放源/turn的同一TX音频时钟不能重置或倒退")

    def release(self):
        self.assertTrue(self.command("release", session=self.session)["ok"])
        self.allocated.remove(self.session)
        state = self.command("stats")["stats"]
        self.assertEqual((state["active_calls"], state["processed_active_calls"], state["processed_local_active_calls"]), (0, 0, 0))
        sockets = []
        try:
            for port in range(self.base, self.base + 4):
                probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                sockets.append(probe)
                probe.bind(("127.0.0.1", port))
        finally:
            for probe in sockets:
                probe.close()
        self.observations.append(dict(op="released_ports_rebound", session=self.session, observed_at=time.time(), stats=state))
        return state

    def close(self):
        """任何失败都保留证据；强制退出或遗漏Release不能算通过。"""
        forced = False
        if self.process:
            if self.process.stdin:
                self.process.stdin.close()
            try:
                self.process.wait(3)
            except subprocess.TimeoutExpired:
                forced = True
                self.process.send_signal(signal.SIGCONT)
                self.process.terminate()
                try:
                    self.process.wait(3)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(2)
            self.process.stdout.close()
        for item in self.resources:
            item.close()
        if self.log:
            self.log.close()
        result = dict(test=self.id(), connected=CONNECTED, pcm_lane=getattr(self, "pcm_lane", None), wire=self.wire,
                      wire_bytes=self.wire_bytes, observations=self.observations, binary_before=getattr(self, "binary_before", None),
                      binary_after=digest(MEDIA), sources_before=getattr(self, "sources_before", {}),
                      sources_after={p.name: digest(p) for p in getattr(self, "source_paths", [])},
                      fixture_before=getattr(self, "fixture_before", {}),
                      fixture_after={name: digest(self.directory / name) for name in getattr(self, "fixture_before", {})},
                      forced_cleanup=forced, remaining_sessions=sorted(self.allocated),
                      process_returncode=self.process.returncode if self.process else None,
                      scope="直接Rust IPC+UnixDatagram+真实UDP；不等同SIP ACK、ASR/TTS、原版差分或容量认证")
        result["files"] = {p.name: {"sha256": digest(p), "bytes": p.stat().st_size} for p in self.directory.iterdir() if p.is_file()}
        (self.directory / "wire.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        self.assertFalse(forced)
        self.assertEqual(result["process_returncode"], 0)
        self.assertEqual(result["binary_before"], result["binary_after"])
        self.assertEqual(result["sources_before"], result["sources_after"])
        self.assertEqual(result["fixture_before"], result["fixture_after"])
        self.assertEqual(result["remaining_sessions"], [])


class PcmTurnIntegration(PcmTurnFixture):
    def test_complete_pcm_both_laws_and_last_frame_duration(self):
        """两个编码都逐样本核对；队列清空不等于最后20ms声音已经播放完。"""
        for payload in (0, 8):
            with self.subTest(payload=payload):
                self.rtp = []
                self.allocate(payload)
                self.begin()
                samples = pcm_samples(10, 2)
                feed_started = time.monotonic()
                self.feed(samples)
                self.turn("end", final_samples=len(samples))
                self.wait(lambda: len(self.audio()) == 10)
                self.assertEqual(self.final("completed")["sent_samples"], len(samples))
                # 收包可能晚于内核提交，不能把接收时刻当发送时刻；最早可能提交也晚于feed开始。
                self.assertGreaterEqual(time.monotonic() - feed_started, len(samples) / 8000)
                self.assert_pcm(self.audio(), samples)
                self.assert_identity()
                before = len(self.rtp)
                self.begin()
                self.tick(.06)
                self.assertEqual(len(self.rtp), before, "相同begin不能复活完成流")
                self.release()

    def test_full_scale_and_signed_little_endian_samples(self):
        """格式端点、符号和字节序使用独立码字结果，不放宽旧非饱和512门槛。"""
        samples = list(FULL_SCALE_SAMPLES) * 20
        for payload in (0, 8):
            with self.subTest(payload=payload):
                self.rtp = []
                self.allocate(payload)
                self.begin(buffer=20, prebuffer=20)
                self.push(samples)
                self.turn("end", final_samples=160)
                self.final("completed")
                self.assert_pcm(self.audio(), samples, full_scale=True)
                self.release()

    def test_prebuffer_queue_full_is_atomic_and_offset_never_replays(self):
        self.allocate()
        self.begin(buffer=100, prebuffer=100)
        samples = pcm_samples(5, 3)
        first = self.push(samples[:640])
        self.assertEqual((first["accepted_samples"], first["queued_samples"], first["state"]), (640, 640, "buffering"))
        full = self.push(pcm_samples(2, 8), 640, expected=8)
        self.assertEqual((full["accepted_samples"], full["queued_samples"]), (640, 640))
        for offset in (0, 480, 800):
            self.push(samples[:160], offset, expected=7)
        self.tick(.03)
        self.assertEqual(self.audio(), [])
        self.push(samples[640:], 640)
        self.turn("end", final_samples=800)
        self.final("completed")
        self.assert_pcm(self.audio(), samples)
        self.release()

    def test_binary_limits_reserved_fields_and_unknown_sessions(self):
        self.allocate()
        self.push(pcm_samples(1), expected=4)
        self.push(pcm_samples(1), session=99, expected=2)
        self.begin(buffer=100, prebuffer=100)
        for samples in ([], [1] * 159, [1] * 161, [1] * 960):
            self.push(samples, expected=1)
        for mutate in (lambda b: b[:4] + b"\x01" + b[5:], lambda b: b[:42] + b"\x01\x00" + b[44:],
                       lambda b: b[:-1], lambda b: b + b"\0", lambda b: b"BAD1" + b[4:]):
            self.push(pcm_samples(1), expected=1, mutate=mutate)
        self.assertEqual(self.turn("status")["accepted_samples"], 0)
        self.turn("end", final_samples=0)
        self.release()

    def test_truncated_binary_headers_echo_only_complete_identity_fields(self):
        """不完整头也必须有确定回应；不能越界读出旧请求的会话/轮次或伪造活动快照。"""
        self.allocate()
        self.begin(buffer=100, prebuffer=100)
        for length in (0, 4, 15, 16, 23, 24, 31, 32, 40, 43):
            self.data_sequence += 1
            raw = request_packet(self.data_sequence, self.session, 1, 0, pcm_samples(1))[:length]
            expected_request = self.data_sequence if length >= 16 else 0
            self.record("pcm_send", raw)
            self.assertEqual(self.data.send(raw), len(raw))
            self.wait(lambda: expected_request in self.data_responses)
            value = self.data_responses.pop(expected_request)
            self.assertEqual((value["request_id"], value["session"], value["requested_turn"], value["code"]),
                             (expected_request, self.session if length >= 24 else 0, 1 if length >= 32 else 0, 1))
            self.assertEqual(value["current_turn"], 0)
            self.assertEqual(value["state"], "idle")
            self.assertTrue(all(value[name + "_samples"] == 0 for name in ("accepted", "sent", "queued", "discarded")))
        self.assertEqual(self.turn("status")["accepted_samples"], 0)
        self.turn("end", final_samples=0)
        self.release()

    def test_maximum_buffer_is_fifty_frames_and_whole_batch_rejection(self):
        """1000ms的真实配置上界可用；第50帧到齐前不会播放或部分接受越容量的两帧。"""
        self.allocate()
        self.begin(buffer=1000, prebuffer=1000)
        samples = pcm_samples(50, 7)
        self.feed(samples[:49 * 160])
        before = self.turn("status")
        self.assertEqual((before["state"], before["queued_ms"], before["queued_bytes"]), ("buffering", 980, 15680))
        refused = self.push(samples[:320], 49 * 160, expected=8)
        self.assertEqual((refused["accepted_samples"], refused["queued_samples"]), (7840, 7840))
        self.assertEqual(self.audio(), [])
        self.push(samples[49 * 160:], 49 * 160)
        self.turn("end", final_samples=8000)
        self.final("completed", timeout=1.3)
        self.assert_pcm(self.audio(), samples)
        self.release()

    def test_new_turn_fences_late_append_end_interrupt_and_start(self):
        self.allocate()
        self.begin(10, buffer=200, prebuffer=200)
        old = pcm_samples(5, 1)
        self.feed(old, 10)
        self.begin(11, buffer=100, prebuffer=100)
        self.push(old[:160], 800, 10, expected=5)
        self.turn("end", 10, expected=False, final_samples=800)
        self.turn("interrupt", 10, expected=False, fade_ms=0)
        self.begin(10, expected=False)
        new = pcm_samples(5, 5)
        self.feed(new, 11)
        self.wait(lambda: len(self.audio()) >= 1)
        # 新轮次已经实际出声之后，再模拟旧生产者迟到，不能只验证尚无媒体时的表面拒绝。
        stale = self.push(old[:160], 800, 10, expected=5)
        self.assertEqual(stale["current_turn"], 11)
        self.turn("interrupt", 10, expected=False, fade_ms=40)
        self.turn("end", 10, expected=False, final_samples=800)
        self.turn("end", 11, final_samples=800)
        self.final("completed", 11)
        self.assert_pcm(self.audio(), new)
        self.release()

    def test_invalid_new_begin_and_end_do_not_cancel_valid_queue(self):
        self.allocate()
        self.begin(1, buffer=100, prebuffer=100)
        samples = pcm_samples(5, 4)
        self.push(samples[:640])
        for buffer, prebuffer in ((19, 20), (1001, 20), (100, 0), (100, 120)):
            self.begin(2, buffer=buffer, prebuffer=prebuffer, expected=False)
        self.begin(1, buffer=200, prebuffer=100, expected=False)
        self.turn("end", final_samples=800, expected=False)
        self.assertEqual(self.turn("status")["queued_samples"], 640)
        self.push(samples[640:], 640)
        self.turn("end", final_samples=800)
        self.final("completed")
        self.assert_pcm(self.audio(), samples)
        self.release()

    def test_interrupt_zero_has_real_send_barrier_and_shared_next_identity(self):
        self.allocate()
        self.begin(buffer=400, prebuffer=20)
        old = pcm_samples(20, 1)
        self.feed(old)
        self.wait(lambda: len(self.audio()) >= 2)
        stopped = self.turn("interrupt", fade_ms=0)
        self.assertEqual(stopped["state"], "stopped")
        self.push(old[:160], len(old), expected=6)
        self.tick(.04)
        self.assertEqual(len(self.audio()) * 160, stopped["sent_samples"], "stop屏障之后仍有旧turn被提交")
        self.assert_pcm(self.audio(), old[:stopped["sent_samples"]])
        cut = len(self.rtp)
        self.begin(2, buffer=100, prebuffer=100)
        new = pcm_samples(5, 6)
        self.feed(new, 2)
        self.turn("end", 2, final_samples=800)
        self.final("completed", 2)
        self.assert_pcm(self.audio(cut), new)
        self.assert_identity()
        self.release()

    def test_fade_uses_only_unsent_queue_and_exact_linear_samples(self):
        for fade, amplitude in ((20, 6000), (40, -6000)):
            with self.subTest(fade_ms=fade, amplitude=amplitude):
                self.rtp = []
                self.allocate(8)
                self.begin(buffer=200, prebuffer=200)
                samples = [amplitude] * 800
                self.feed(samples)
                reply = self.turn("interrupt", fade_ms=fade)
                self.assertEqual(reply["state"], "stopping")
                self.push(samples[:160], 800, expected=6)
                final = self.final("stopped")
                count = fade * 8
                self.assertEqual((final["sent_samples"], final["discarded_samples"]), (count, 800 - count))
                expected = [int(amplitude * (count - 1 - i) / (count - 1)) for i in range(count)]
                self.assert_pcm(self.audio(), expected)
                self.assertLessEqual(abs(oracle.decode(self.audio()[-1]["body"][-1], self.law)), 8)
                self.release()

    def test_stopping_can_escalate_to_immediate_interrupt(self):
        self.allocate()
        self.begin(buffer=200, prebuffer=200)
        self.feed(pcm_samples(5))
        self.turn("interrupt", fade_ms=40)
        final = self.turn("interrupt", fade_ms=0)
        self.assertEqual(final["state"], "stopped")
        self.tick(.05)
        self.assertEqual(len(self.audio()) * 160, final["sent_samples"])
        self.assertLessEqual(final["sent_samples"], 320)
        self.release()

    def test_eof_empty_and_below_prebuffer_drain_without_fabricated_padding(self):
        self.allocate()
        self.begin(buffer=200, prebuffer=200)
        self.assertEqual(self.turn("end", final_samples=0)["state"], "completed")
        self.tick(.04)
        self.assertEqual(self.audio(), [])
        self.begin(2, buffer=200, prebuffer=200)
        samples = pcm_samples(1, 9)
        self.push(samples, turn_id=2)
        self.turn("end", 2, final_samples=160)
        self.final("completed", 2)
        self.assert_pcm(self.audio(), samples)
        self.release()

    def test_first_frame_and_producer_starvation_timeout_without_silence(self):
        self.allocate()
        began = time.monotonic()
        self.begin(buffer=100, prebuffer=100)
        self.push(pcm_samples(1))
        failed = self.final("failed", timeout=2.5)
        self.assertGreaterEqual(time.monotonic() - began, 1.95)
        self.assertTrue(failed["error"])
        self.assertEqual((failed["sent_samples"], failed["discarded_samples"]), (0, 160))
        self.assertEqual(self.audio(), [])
        self.begin(2, buffer=100, prebuffer=20)
        self.push(pcm_samples(1, 2), turn_id=2)
        self.wait(lambda: len(self.audio()) == 1)
        failed = self.final("failed", 2, timeout=.3)
        self.assertTrue(failed["error"])
        self.assertEqual(failed["sent_samples"], 160)
        before = len(self.rtp)
        self.tick(.12)
        self.assertEqual(len(self.rtp), before, "生产方断流不能用无限静音/PLC伪装正常供音")
        self.release()

    def test_short_underflow_reanchors_to_wall_clock_without_catchup(self):
        self.allocate()
        self.begin(buffer=100, prebuffer=20)
        first = pcm_samples(1, 2)
        self.push(first)
        self.wait(lambda: len(self.audio()) == 1)
        self.tick(.05)
        second = pcm_samples(2, 4)
        self.push(second, 160)
        self.turn("end", final_samples=480)
        self.final("completed")
        self.assert_pcm(self.audio()[:1], first)
        self.assert_pcm(self.audio()[1:], second)
        a, b = self.audio()[:2]
        ticks = (b["timestamp"] - a["timestamp"]) & 0xffffffff
        self.assertGreater(ticks, 160)
        self.assertLessEqual(abs(ticks / 8000 - (b["at"] - a["at"])), .040)
        self.assert_identity()
        self.release()

    def test_tone_file_and_pcm_active_ownership_is_mutually_exclusive(self):
        self.allocate()
        tone = self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=500, duration_ms=400)
        self.assertTrue(tone["ok"])
        self.begin(expected=False)
        self.assertTrue(self.command("playback_stop", session=1, playback_id=1)["ok"])
        file = self.command("playback_file_start", session=1, playback_id=2, leg="a", path="prompt.wav")
        self.assertTrue(file["ok"])
        self.begin(expected=False)
        self.assertTrue(self.command("playback_stop", session=1, playback_id=2)["ok"])
        self.begin(buffer=200, prebuffer=200)
        self.assertFalse(self.command("playback_start", session=1, playback_id=3, leg="a", frequency_hz=500, duration_ms=400)["ok"])
        self.assertFalse(self.command("playback_file_start", session=1, playback_id=3, leg="a", path="prompt.wav")["ok"])
        self.tick(.04)
        cut = len(self.rtp)
        samples = pcm_samples(5, 9)
        self.feed(samples)
        self.turn("end", final_samples=800)
        self.final("completed")
        self.assert_pcm(self.audio(cut), samples)
        self.assert_identity()
        self.release()

    def test_active_dtmf_survives_pcm_interrupt_and_keeps_tx_clock(self):
        self.allocate(8)
        self.begin(buffer=400, prebuffer=20)
        self.feed(pcm_samples(20, 4))
        reply = self.command("dtmf_send", session=1, leg="a", digits="12#", duration_ms=55)
        self.assertTrue(reply["ok"])
        self.wait(lambda: len(self.audio()) >= 2)
        stopped = self.turn("interrupt", fade_ms=0)
        self.tick(.60)
        self.assertEqual(len(self.audio()) * 160, stopped["sent_samples"])
        count = assert_events(self, [p["raw"] for p in self.rtp], "12#", 55)
        status = self.command("dtmf_send_status", session=1)
        self.assertEqual((status["state"], status["completed_digits"], status["sent_packets"]), ("idle", 3, count))
        event = next(p for p in self.rtp if p["payload"] == 101)
        audio = self.audio()[0]
        # event每组timestamp保持恒定，但首包必须使用与PCM相同的Allocate媒体时钟。
        ticks = (event["timestamp"] - audio["timestamp"]) & 0xffffffff
        if ticks >= 0x80000000:
            ticks -= 0x100000000
        self.assertLessEqual(abs(ticks / 8000 - (event["at"] - audio["at"])), .040)
        self.assert_identity()
        self.release()

    def test_rtcp_counts_actual_pcm_and_dtmf_and_release_bye(self):
        self.allocate()
        self.begin(buffer=100, prebuffer=100)
        samples = pcm_samples(5, 5)
        self.feed(samples)
        self.turn("end", final_samples=800)
        self.assertTrue(self.command("dtmf_send", session=1, leg="a", digits="5", duration_ms=55)["ok"])
        self.final("completed")
        frames = oracle.audio_frames(self.law, 431, 6)
        input_started = time.monotonic()
        for index, body in enumerate(frames):
            planned = input_started + index * .020
            while time.monotonic() < planned:
                self.poll(planned - time.monotonic())
            actual = time.monotonic()
            lateness = actual - planned
            self.observations.append(dict(op="rtcp_fixture_rtp_schedule", sequence=31000 + index,
                                          planned_at_ms=(planned - self.started) * 1000,
                                          send_check_at_ms=(actual - self.started) * 1000,
                                          lateness_ms=lateness * 1000))
            # 固定源采样时钟使用绝对计划；发生器迟到一帧即失败，不靠追赶发包掩盖输入不足。
            self.assertLess(lateness, .020, "RTCP用例输入发生器迟到达到20ms")
            self.send_network(oracle.rtp(self.payload, 31000 + index, 176000 + index * 160, body, SOURCE_SSRC))
        while time.monotonic() < input_started + len(frames) * .020:
            self.poll(input_started + len(frames) * .020 - time.monotonic())
        report, lsr = oracle.rtcp_source_report(SOURCE_SSRC, "pcm-turn-fixture", 176960, 6, 960)
        self.send_network(report, rtcp=True)
        self.tick(7.6)
        self.assert_pcm(self.audio(), samples)
        event_packets = assert_events(self, [p["raw"] for p in self.rtp], "5", 55)
        self.assert_identity()
        reports = [p for r in self.rtcp for p in r["parts"] if p["kind"] == 200]
        self.assertTrue(reports)
        ssrc = self.rtp[0]["ssrc"]
        for report in reports:
            self.assertEqual((report["ssrc"], report["packets"], report["octets"]), (ssrc, 5 + event_packets, 800 + event_packets * 4))
        rx = [r for report in reports for r in report["reports"] if r["ssrc"] == SOURCE_SSRC and r["lsr"] == lsr]
        self.assertTrue(rx, "SR中的RR块必须关联实际入站SSRC及其已发送SR")
        self.assertTrue(all(r["highest_sequence"] == 31005 and r["cumulative_lost"] == 0 and r["dlsr"] > 0 for r in rx))
        self.assertEqual(self.command("stats")["stats"]["processed_decoded_frames"], 6)
        sd = [p for r in self.rtcp for p in r["parts"] if p["kind"] == 202]
        self.assertTrue(any(c["ssrc"] == ssrc and any(kind == 1 and value for kind, value in c["items"])
                            for p in sd for c in p["chunks"]))
        before_release = len(self.rtcp)
        self.release()
        self.tick(.04)
        bye = [p for r in self.rtcp for p in r["parts"] if p["kind"] == 203]
        self.assertEqual([p["sources"] for p in bye], [[ssrc]])
        final_reports = [p for r in self.rtcp[before_release:] for p in r["parts"] if p["kind"] == 200]
        self.assertEqual(len(final_reports), 1, "释放需要真实最终SR+SDES+BYE")
        self.assertEqual((final_reports[0]["packets"], final_reports[0]["octets"]), (5 + event_packets, 800 + event_packets * 4))
        for first, second in zip(reports + final_reports, (reports + final_reports)[1:]):
            a = (first["ntp_seconds"] << 32) | first["ntp_fraction"]
            b = (second["ntp_seconds"] << 32) | second["ntp_fraction"]
            self.assertGreater(b, a)
            ticks = (second["rtp_timestamp"] - first["rtp_timestamp"]) & 0xffffffff
            self.assertLessEqual(abs(ticks - (b - a) * 8000 / (1 << 32)), 2,
                                 "PCM结束/恒定DTMF结束包不能冻结SR媒体时钟")

    def test_inbound_voice_loss_cn_and_dtmf_do_not_replace_streamed_pcm(self):
        """入站真音频/丢包/CN独立消费；输出仍逐样本等于生产者PCM，没有自回声或假静音。"""
        self.allocate()
        self.begin(buffer=400, prebuffer=100)
        supplied = pcm_samples(20, 6)
        self.feed(supplied)
        self.turn("end", final_samples=len(supplied))
        frames = oracle.audio_frames(self.law, 541, 16)
        events = [(index * .020, self.payload, 176000 + index * 160, body, False)
                  for index, body in enumerate(frames) if index not in (6, 7, 8, 9)]
        events.append((.120, 13, 176960, bytes([40]), False))
        for at, duration, ended in ((.085, 160, False), (.105, 320, False), (.125, 440, True),
                                     (.145, 440, True), (.165, 440, True)):
            events.append((at, 101, 176640, struct.pack("!BBH", 5, 10 | (128 if ended else 0), duration), at == .085))
        started = time.monotonic()
        for index, (at, payload, timestamp, body, marker) in enumerate(sorted(events)):
            while time.monotonic() < started + at:
                self.poll(started + at - time.monotonic())
            self.assertLess(time.monotonic() - (started + at), .050, "测试发生器迟到，不能把输入错误当产品损失")
            if payload == self.payload and timestamp == 176480:
                # 真丢包保留源序号空洞；CN静默期间未产生包，不能伪造同样的网络丢失。
                continue
            self.send_network(oracle.rtp(payload, 31000 + index, timestamp, body, SOURCE_SSRC, marker))
        self.final("completed")
        self.tick(.20)
        self.assert_pcm(self.audio(), supplied)
        self.assertEqual(len(self.rtp), 20, "网络入站音频/按键不能自动反射到本地TX")
        received = self.command("dtmf_events", after_seq=0, limit=16)
        self.assertTrue(received["ok"])
        self.assertEqual(len(received["events"]), 1)
        self.assertEqual(received["events"][0]["digit"], "5")
        stats = self.command("stats")["stats"]
        self.assertEqual((stats["processed_decoded_frames"], stats["processed_encoded_frames"], stats["processed_cn_packets"]), (11, 20, 1))
        self.assertGreaterEqual(stats["processed_plc_frames"], 1)
        self.assertLessEqual(stats["processed_plc_frames"], 7)
        self.assertEqual(stats["processed_local_consumed_frames"], 11 + stats["processed_plc_frames"])
        self.assertEqual(stats["processed_local_consumed_samples"], stats["processed_local_consumed_frames"] * 160)
        self.assertGreater(stats["processed_local_energy_max"], 0)
        self.assert_identity()
        self.release()

    def test_release_reuse_rejects_old_session_and_discards_queued_pcm(self):
        self.allocate()
        self.begin(buffer=200, prebuffer=200)
        self.feed(pcm_samples(5, 1))
        self.release()
        self.allocate(session=2)
        self.push(pcm_samples(1), session=1, expected=2)
        self.begin()
        samples = pcm_samples(5, 8)
        self.feed(samples)
        self.turn("end", final_samples=800)
        self.final("completed")
        self.assert_pcm(self.audio(), samples)
        self.release()

    def test_relay_and_bridge_do_not_accept_pcm_turns(self):
        for topology in (None, "bridge"):
            with self.subTest(topology=topology):
                self.allocate(topology=topology)
                self.begin(expected=False)
                self.push(pcm_samples(1), expected=3)
                self.tick(.03)
                self.assertEqual(self.rtp, [])
                self.release()

    def test_without_pcm_fd_never_advertises_or_accepts_streams(self):
        self.allocate()
        self.begin(expected=False)
        self.tick(.05)
        self.assertEqual(self.rtp, [])
        self.release()


def main():
    global MEDIA, ARTIFACTS, CONNECTED
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--media", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--connected", action="store_true")
    args, remaining = parser.parse_known_args()
    MEDIA, ARTIFACTS, CONNECTED = args.media.resolve(), args.artifacts.resolve(), args.connected
    if not MEDIA.is_file():
        parser.error("实际候选媒体二进制缺失")
    ARTIFACTS.mkdir(parents=True, exist_ok=False)
    unittest.main(argv=[__file__, *remaining], verbosity=2)


if __name__ == "__main__":
    main()
