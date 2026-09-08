#!/usr/bin/env python3
"""本地单腿 G.711 的真实 SIP/ESL/RTP/RTCP 验收；不访问主服务或外部线路。

仅复用既有测试的独立 G.711 公式、协议解析和端口助手，不调用生产 LUT。
所有原帧、实际配置、提示文件和回收状态均留在本轮独立目录；失败不跳过。
本套件独立于双腿 M1 的固定 22 项，不能把本地通过扩成原版或容量认证。
"""
import argparse
import hashlib
import json
import math
from pathlib import Path
import select
import signal
import socket
import struct
import subprocess
import time
import unittest
import uuid
import wave

import e2e
import esl_applications_e2e as applications
import esl_e2e
import media_processed_fixture as oracle
from dtmf_send_e2e import assert_events

CONTROL = None
MEDIA = None
ARTIFACTS = None
CONNECTED = False
STEP = 0.020
SSRC = 0xA1357135


def digest(path):
    """流式计算实际文件指纹，避免二进制整体读入影响独立测试资源。"""
    result = hashlib.sha256()
    with path.open("rb") as stream:
        for data in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(data)
    return result.hexdigest()


class EvidencePeer(esl_e2e.ESLPeer):
    """保存真实 ESL 帧；认证发送仅由父类完成，不把测试口令写入证据。"""
    def __init__(self, port, password, owner):
        self.owner = owner
        super().__init__(port, password)

    def frame(self):
        headers, raw = {}, bytearray()
        while True:
            line = self.file.readline(65537)
            if not line:
                raise AssertionError("ESL 在完整帧之前关闭")
            raw.extend(line)
            if len(raw) > 65536:
                raise AssertionError("ESL 头超过有界证据预算")
            if line in (b"\n", b"\r\n"):
                break
            key, value = line.decode().rstrip("\r\n").split(":", 1)
            headers[key.lower()] = value.strip()
        size = int(headers.get("content-length", "0"))
        if not 0 <= size <= 262144:
            raise AssertionError("ESL 正文超过本例预算")
        body = self.file.read(size)
        if len(body) != size:
            raise AssertionError("ESL 正文截断")
        self.owner.record("esl_receive", bytes(raw) + body, self.sock.getpeername())
        return headers, body

    def command(self, command):
        self.owner.record("esl_send", (command + "\n\n").encode(), self.sock.getpeername())
        return super().command(command)


class LocalFixture(unittest.TestCase):
    """每例独占一块四端口和一组控制/媒体进程；不修改旧双腿夹具。"""
    sock = oracle.ProcessedFixture.sock
    free_media_block = oracle.ProcessedFixture.free_media_block
    log_tail = oracle.ProcessedFixture.log_tail
    send_sip = oracle.ProcessedFixture.send_sip
    receive_sip = oracle.ProcessedFixture.receive_sip
    wait_status = oracle.ProcessedFixture.wait_status
    execute = applications.ApplicationFixture.execute
    application_event = applications.ApplicationFixture.application_event

    def setUp(self):
        self.resources, self.wire, self.observations, self.output = [], [], [], []
        self.process, self.esl, self.log = None, None, None
        self.started, self.wire_bytes = time.monotonic(), 0
        self.directory = ARTIFACTS / (self.id().rsplit(".", 1)[-1] + "-" + uuid.uuid4().hex[:8])
        self.directory.mkdir(parents=True, exist_ok=False)
        self.addCleanup(self.close)
        self.binary_before = {"control": digest(CONTROL), "media": digest(MEDIA)}
        self.test_paths = [Path(__file__), Path(e2e.__file__), Path(esl_e2e.__file__),
                           Path(applications.__file__), Path(oracle.__file__),
                           Path(__file__).with_name("dtmf_send_e2e.py")]
        self.test_sources_before = {path.name: digest(path) for path in self.test_paths}
        self.upstream, self.a, self.ar, self.ac, self.stranger = [self.sock() for _ in range(5)]
        probe = self.sock()
        self.address = probe.getsockname()
        probe.close()
        self.port, self.esl_port = self.tcp_port(), self.tcp_port()
        self.base = self.free_media_block()
        self.source_sequence, self.source_timestamp = 31000, 176000
        self.prompt = [round((4200 + (i // 160) * 97) * math.sin(2 * math.pi * 467 * i / 8000)
                             + 900 * math.sin(2 * math.pi * 733 * i / 8000)) for i in range(801)]
        with wave.open(str(self.directory / "prompt.wav"), "wb") as stream:
            stream.setnchannels(1)
            stream.setsampwidth(2)
            stream.setframerate(8000)
            stream.writeframes(b"".join(struct.pack("<h", value) for value in self.prompt))
        with wave.open(str(self.directory / "long-prompt.wav"), "wb") as stream:
            stream.setnchannels(1)
            stream.setsampwidth(2)
            stream.setframerate(8000)
            stream.writeframes(b"".join(struct.pack("<h", self.prompt[index % len(self.prompt)])
                                        for index in range(16000)))
        config = json.loads((e2e.ROOT / "config/local.json").read_text())
        config["sip"].update(listen=f"127.0.0.1:{self.address[1]}", advertise=f"127.0.0.1:{self.address[1]}",
                             upstream=f"127.0.0.1:{self.upstream.getsockname()[1]}",
                             local_extensions=["1000"], ack_timeout_ms=1000)
        config["media"].update(binary=str(MEDIA), workers=1, connect_sockets=CONNECTED,
                               port_start=self.base, port_end=self.base + 3, processing="g711",
                               port_reuse_delay_ms=0, adaptive_admission=False,
                               playback_root=str(self.directory), max_packets_per_second_per_leg=500)
        config["limits"].update(max_calls=1, calls_per_second=10, burst_calls=10)
        config["admin"]["listen"] = f"127.0.0.1:{self.port}"
        config["journal"]["path"] = str(self.directory / "events.jsonl")
        if "xml" in self.id():
            path = self.directory / "dialplan.xml"
            path.write_text('<include><context name="default"><extension name="local-test">'
                            '<condition field="destination_number" expression="^7101$">'
                            '<action application="answer"/><action application="set" data="local_test=中文"/>'
                            '<action application="playback" data="prompt.wav"/>'
                            '<action application="park"/></condition></extension></context></include>')
            config["sip"]["dialplan"] = {"file": str(path), "context": "default"}
        self.config = config
        (self.directory / "config.json").write_text(json.dumps(config, ensure_ascii=False, indent=2) + "\n")
        self.fixture_before = {p.name: digest(p) for p in self.directory.iterdir() if p.is_file()}
        secret = self.directory / "esl.secret"
        password = uuid.uuid4().hex
        secret.write_text(password + "\n")
        secret.chmod(0o600)
        self.log = (self.directory / "server.log").open("w+")
        self.process = subprocess.Popen([str(CONTROL), "-config", str(self.directory / "config.json"),
                                         "-esl-listen", f"127.0.0.1:{self.esl_port}",
                                         "-esl-password-file", str(secret)], cwd=e2e.ROOT,
                                        stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                self.fail("真实本地处理候选启动失败：" + self.log_tail())
            try:
                if self.get("/readyz")["ready"]:
                    self.esl = EvidencePeer(self.esl_port, password, self)
                    head, _ = self.esl.command("event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_HANGUP "
                                              "CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE DTMF "
                                              "PLAYBACK_START PLAYBACK_STOP CHANNEL_PARK CHANNEL_UNPARK")
                    self.assertEqual(head["reply-text"], "+OK event listener enabled json")
                    return
            except (OSError, ValueError):
                pass
            time.sleep(.03)
        self.fail("未取得本地处理能力就绪，不能跳过")

    @staticmethod
    def tcp_port():
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            return probe.getsockname()[1]

    def record(self, direction, data, peer):
        """固定条数和字节上限；证据超限明确失败，不截断成表面成功。"""
        self.wire_bytes += len(data)
        self.assertLess(len(self.wire), 4096)
        self.assertLessEqual(self.wire_bytes, 8 * 1024 * 1024)
        self.wire.append({"at_ms": round((time.monotonic() - self.started) * 1000, 3),
                          "direction": direction, "peer": list(peer), "hex": data.hex()})

    def get(self, path):
        return oracle.ProcessedFixture.get(self, path)

    def observe(self, label, state):
        self.observations.append({"label": label, "observed_at": time.time(), "state": state})
        return state

    def stats(self, predicate=lambda value: True):
        """等待本代实际字段；本地消费计数不能从既有桥接统计推算或补零。"""
        required = ("processed_local_active_calls", "processed_local_consumed_frames",
                    "processed_local_consumed_samples", "processed_local_nonzero_frames",
                    "processed_local_energy_max", "processed_decoded_frames", "processed_encoded_frames")
        state = self.wait_status(lambda item: len(item["workers"]) == 1
                                 and all(name in item["workers"][0]["stats"] for name in required)
                                 and predicate(item["workers"][0]["stats"]), require_stats=True)
        worker = state["workers"][0]
        self.assertTrue(worker["healthy"])
        self.assertEqual((worker["generation"], worker["restarts"]), (1, 0))
        stats = worker["stats"]
        self.assertEqual(stats["processed_local_consumed_samples"], stats["processed_local_consumed_frames"] * 160)
        self.assertEqual(stats["processed_decoded_samples"], stats["processed_decoded_frames"] * 160)
        self.assertEqual(stats["processed_encoded_samples"], stats["processed_encoded_frames"] * 160)
        self.assertLessEqual(stats["processed_local_nonzero_frames"], stats["processed_local_consumed_frames"])
        self.assertLessEqual(stats["processed_local_energy_max"], 171798691840)
        self.observe("stats", state)
        return stats

    def local_call(self, law="PCMU", payload=0, *, ack=True, number="1000", ptime=20, expected=200):
        """只有真实本地200/ACK构成接通；上游套接字一直保持沉默并留存负例。"""
        self.law, self.payload = law, payload
        self.esl.events.clear()
        self.output = []
        self.source_sequence, self.source_timestamp = 31000, 176000
        self.call_id = uuid.uuid4().hex
        self.admission_before_call = self.get("/v1/status")["workers"][0].get("admission", {}).get("sampled_at")
        body = oracle.description(law, payload, self.ar.getsockname()[1], self.ac.getsockname()[1], ptime=ptime)
        invite = e2e.message("INVITE", f"sip:{number}@local", self.a.getsockname()[1], self.call_id,
                              "z9hG4bK" + uuid.uuid4().hex, body)
        self.send_sip(self.a, invite, self.address)
        response, _ = self.receive_sip(self.a, lambda m: m[0].startswith(f"SIP/2.0 {expected}")
                                        and m[1]["cseq"].endswith("INVITE"))
        self.accepted = response
        if expected != 200:
            self.no_upstream()
            return response
        body = e2e.parse(response)[2]
        self.assertIn(f"a=rtpmap:{payload} {law}/8000".encode(), body)
        self.a_ports = e2e.media_ports(body)
        self.assertEqual(self.a_ports, (self.base, self.base + 1))
        if ack:
            self.ack(number)
        creates = self.esl.wait_events("CHANNEL_CREATE", 1)
        self.assertEqual(len(creates), 1)
        self.channel = creates[0]["Unique-ID"]
        self.assertEqual(creates[0]["Call-Direction"], "inbound")
        self.assertNotIn("Other-Leg-Unique-ID", creates[0])
        if ack:
            self.wait_status(lambda state: state["active_calls"] == state["established_calls"] == 1)
        self.no_upstream()
        return response

    def ack(self, number="1000"):
        header = e2e.parse(self.accepted)[1]
        self.send_sip(self.a, e2e.message("ACK", f"sip:{number}@local", self.a.getsockname()[1],
                                        header["call-id"], "z9hG4bK" + uuid.uuid4().hex,
                                        to=header["to"]), self.address)

    def no_upstream(self):
        """本地媒体不应为模拟B腿发送任何SIP；收到消息即保留并失败。"""
        self.upstream.settimeout(.025)
        try:
            raw, peer = self.upstream.recvfrom(65536)
        except socket.timeout:
            return
        self.record("unexpected_upstream", raw, peer)
        self.fail("本地单腿意外向上游发送SIP")

    def audio_events(self, frames, *, omitted=(), cn_at=None):
        events = []
        for index, body in enumerate(frames):
            if index not in omitted:
                payload, content = (13, bytes([30])) if cn_at == index else (self.payload, body)
                events.append((index * STEP, self.ar, self.a_ports[0],
                               oracle.rtp(payload, self.source_sequence, self.source_timestamp, content, SSRC,
                                          index == 0)))
                self.source_sequence = (self.source_sequence + 1) & 65535
            self.source_timestamp += 160
        return events

    def digit_events(self, digit, start=0, duration=100):
        """RTP事件发送节奏、起点和三个结束包均独立构造；与来源音频共用序号。"""
        event = "0123456789*#ABCD".index(digit)
        stamp = self.source_timestamp
        self.source_timestamp += duration * 8 + 400
        # duration表示事件起点后已覆盖的采样时间；不能把100ms结束包提前到80ms发送。
        steps = [(elapsed / 1000, False, elapsed) for elapsed in range(20, duration, 20)]
        steps += [(duration / 1000 + repeat * STEP, True, duration) for repeat in range(3)]
        result = []
        for index, (at, ended, elapsed) in enumerate(steps):
            data = bytes([event, (128 if ended else 0) | 10]) + struct.pack("!H", elapsed * 8)
            result.append((start + at, self.ar, self.a_ports[0],
                           oracle.rtp(101, self.source_sequence, stamp, data, SSRC, index == 0)))
            self.source_sequence = (self.source_sequence + 1) & 65535
        return result

    def pump(self, duration, events=(), actions=()):
        """一个有界select同时发送来源与读取本地TX；不引入逐会话后台发生线程。"""
        self.assertLessEqual(duration, 9)
        events, actions = sorted(events, key=lambda e: e[0]), sorted(actions, key=lambda e: e[0])
        self.assertTrue(all(0 <= e[0] < duration for e in events))
        self.assertTrue(all(0 <= e[0] < duration for e in actions))
        began, cursor, action_cursor = time.monotonic() + .005, 0, 0
        deadline = began + duration
        outputs, reports = [], []
        sockets = {self.ar: "rtp_receive", self.ac: "rtcp_receive", self.stranger: "stranger_receive"}
        for sock in sockets:
            sock.setblocking(False)
        while cursor < len(events) or action_cursor < len(actions) or time.monotonic() < deadline:
            now = time.monotonic()
            while cursor < len(events) and began + events[cursor][0] <= now:
                at, sock, port, data = events[cursor]
                self.assertLess(now - began - at, .050, "夹具发送计划迟到超过原50ms上限")
                target = ("127.0.0.1", port)
                self.assertEqual(sock.sendto(data, target), len(data))
                self.record("rtcp_send" if sock is self.ac else "rtp_send", data, target)
                self.wire[-1]["local"] = list(sock.getsockname())
                cursor += 1
            while action_cursor < len(actions) and began + actions[action_cursor][0] <= now:
                at, action = actions[action_cursor]
                self.assertLess(now - began - at, .050, "夹具控制动作迟到")
                action()
                action_cursor += 1
                now = time.monotonic()
            next_at = began + events[cursor][0] if cursor < len(events) else deadline
            if action_cursor < len(actions):
                next_at = min(next_at, began + actions[action_cursor][0])
            ready, _, _ = select.select(list(sockets), [], [], max(0, min(.020, next_at - time.monotonic(), deadline - time.monotonic())))
            for sock in ready:
                for _ in range(64):
                    try:
                        raw, peer = sock.recvfrom(4096)
                    except BlockingIOError:
                        break
                    kind = sockets[sock]
                    self.record(kind, raw, peer)
                    self.wire[-1]["local"] = list(sock.getsockname())
                    self.assertNotEqual(kind, "stranger_receive", "来源之外端点收到本地媒体")
                    if sock is self.ac:
                        self.assertEqual(peer, ("127.0.0.1", self.a_ports[1]))
                        reports.append({"parts": oracle.unpack_rtcp(raw), "raw": raw, "at": time.monotonic()})
                        continue
                    packet = oracle.unpack_rtp(raw)
                    packet.update(raw=raw, peer=peer, at=time.monotonic())
                    self.assertEqual(peer, ("127.0.0.1", self.a_ports[0]))
                    self.output.append(packet)
                    outputs.append(packet)
                    self.assertLessEqual(len(self.output), 1024)
        self.assertEqual(cursor, len(events))
        for sock in sockets:
            sock.settimeout(2)
        return outputs, reports

    def assert_timeline(self):
        self.assertTrue(self.output, "没有实际收到本地生成媒体")
        self.assertEqual(len({p["ssrc"] for p in self.output}), 1)
        self.assertNotEqual(self.output[0]["ssrc"], SSRC)
        for first, second in zip(self.output, self.output[1:]):
            self.assertEqual((second["sequence"] - first["sequence"]) & 65535, 1)
        audio = [p for p in self.output if p["payload"] == self.payload]
        for first, second in zip(audio, audio[1:]):
            self.assertGreater((second["timestamp"] - first["timestamp"]) & 0xffffffff, 0)
            self.assertLess((second["timestamp"] - first["timestamp"]) & 0xffffffff, 80000)
            # 两端各有一个20ms发送/观测区间；这里比较墙钟映射，不放宽逐帧发送期限。
            ticks = (second["timestamp"] - first["timestamp"]) & 0xffffffff
            self.assertLessEqual(abs(ticks / 8000 - (second["at"] - first["at"])), .040,
                                 "跨任务RTP时钟没有跟随真实播放间隔")

    def assert_pcm(self, packets, expected):
        """全部样本与独立已知PCM对照；能量和逐样本界限均要求，不以PT或计数代替。"""
        self.assertEqual(len(packets), (len(expected) + 159) // 160)
        values = []
        for packet in packets:
            self.assertEqual((packet["payload"], len(packet["body"])), (self.payload, 160))
            values += [oracle.decode(code, self.law) for code in packet["body"]]
        self.assertGreater(sum(value * value for value in expected), 1_000_000)
        self.assertLessEqual(max(abs(a - b) for a, b in zip(values, expected)), 512)
        self.assertTrue(all(abs(value) <= 8 for value in values[len(expected):]))
        for a, b in zip(packets, packets[1:]):
            self.assertEqual((b["timestamp"] - a["timestamp"]) & 0xffffffff, 160)
            self.assertGreaterEqual(b["at"] - a["at"], .005, "连续源音频出现追赶突发")

    def finish(self, server=False):
        """本地单腿只结束A；必须等实际worker新采样归零并重新绑定整个媒体块。"""
        before = self.get("/v1/status")
        previous = before["workers"][0].get("admission", {}).get("sampled_at")
        if server:
            self.assertEqual(self.esl.api("uuid_kill " + self.channel), "+OK\n")
            bye, peer = self.receive_sip(self.a, lambda m: m[0].startswith("BYE "))
            self.send_sip(self.a, e2e.response(bye, port=self.a.getsockname()[1]), peer)
        else:
            header = e2e.parse(self.accepted)[1]
            self.send_sip(self.a, e2e.message("BYE", "sip:1000@local", self.a.getsockname()[1],
                                            header["call-id"], "z9hG4bK" + uuid.uuid4().hex,
                                            to=header["to"], cseq=2), self.address)
            self.receive_sip(self.a, lambda m: m[0].startswith("SIP/2.0 200") and m[1]["cseq"].endswith("BYE"))
        state = self.zero_status(previous)
        self.final_status = self.observe("fresh_final_zero", state)
        self.assert_media_ports_free()
        self.no_upstream()
        self.assertEqual(self.esl.api("uuid_exists " + self.channel), "false")
        return state["workers"][0]["stats"]

    def zero_status(self, previous):
        """拒绝、ACK超时与正常挂机都读取新的实际worker快照，不用旧零值替代回收。"""
        state = self.wait_status(lambda s: s["active_calls"] == s["established_calls"] == 0
                                 and len(s["workers"]) == 1
                                 and s["workers"][0]["stats"].get("processed_local_active_calls") == 0
                                 and s["workers"][0]["stats"]["active_calls"] == s["workers"][0]["stats"]["processed_active_calls"] == 0
                                 and s["workers"][0].get("admission", {}).get("sampled_at") not in (None, previous)
                                 and s["controller"]["media_results_queue_length"] == 0, require_stats=True)
        worker = state["workers"][0]
        self.assertTrue(worker["healthy"])
        self.assertEqual((worker["generation"], worker["restarts"]), (1, 0))
        return state

    def assert_media_ports_free(self):
        probes = []
        try:
            for port in range(self.base, self.base + 4):
                probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                probes.append(probe)
                probe.bind(("127.0.0.1", port))
        finally:
            for probe in probes:
                probe.close()

    def close(self):
        """失效场景也保留原帧；仅操作持有的PID，强制回收永远计为测试失败。"""
        if self.esl is not None:
            self.esl.close()
        forced = False
        if self.process is not None and self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(2)
            except subprocess.TimeoutExpired:
                self.process.send_signal(signal.SIGTERM)
                try:
                    self.process.wait(3)
                except subprocess.TimeoutExpired:
                    forced = True
                    self.process.kill()
                    self.process.wait(2)
        for sock in self.resources:
            sock.close()
        if self.log is not None:
            self.log.close()
        after = {"control": digest(CONTROL), "media": digest(MEDIA)}
        secret = self.directory / "esl.secret"
        secret.unlink(missing_ok=True)
        files = {p.name: {"bytes": p.stat().st_size, "sha256": digest(p)} for p in self.directory.iterdir()
                 if p.is_file() and p.name != "wire.json"}
        result = {"test": self.id(), "connected": CONNECTED, "topology": "local", "wire": self.wire,
                  "wire_bytes": self.wire_bytes, "observations": self.observations,
                  "binaries_before": getattr(self, "binary_before", None), "binaries_after": after,
                  "binaries_unchanged": getattr(self, "binary_before", None) == after,
                  "forced_cleanup": forced, "process_returncode": self.process.returncode if self.process else None,
                  "final_status": getattr(self, "final_status", None), "files": files,
                  "scope": "真实本地单腿G7118k20ms；独立于原双腿22项，不是ASR/TTS/原版差分/并发容量认证"}
        result["test_sources_before"] = getattr(self, "test_sources_before", None)
        result["test_sources_after"] = {p.name: digest(p) for p in getattr(self, "test_paths", [])}
        result["fixture_before"] = getattr(self, "fixture_before", None)
        result["fixture_after"] = {name: digest(self.directory / name)
                                   for name in getattr(self, "fixture_before", {})}
        (self.directory / "wire.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        self.assertFalse(forced, "本例不得依靠强制结束进程通过")
        self.assertEqual(result["process_returncode"], 0, "隔离候选必须正常退出")
        self.assertEqual(result["binaries_before"], after, "执行期间实际二进制发生变化")
        self.assertEqual(result["test_sources_before"], result["test_sources_after"], "执行期间测试源码变化")
        self.assertEqual(result["fixture_before"], result["fixture_after"], "执行期间配置或WAV夹具变化")


class LocalProcessedIntegration(LocalFixture):
    """方法数量表示独立场景，subTest两种律编码不会重复授予兼容接口数量。"""
    def test_rx_both_laws_decodes_without_echo_or_upstream(self):
        for law, payload in [("PCMU", 0), ("PCMA", 8)]:
            with self.subTest(law=law):
                before = self.stats()
                self.local_call(law, payload)
                frames = oracle.audio_frames(law, 467, 24)
                received, _ = self.pump(.70, self.audio_events(frames))
                self.assertEqual(received, [], "本地接收PCM不应自回声或发PLC静音")
                stats = self.stats(lambda s: s["processed_decoded_frames"] >= before["processed_decoded_frames"] + 24)
                self.assertEqual(stats["processed_decoded_frames"] - before["processed_decoded_frames"], 24)
                self.assertEqual(stats["processed_encoded_frames"], before["processed_encoded_frames"])
                consumed = stats["processed_local_consumed_frames"] - before["processed_local_consumed_frames"]
                self.assertGreaterEqual(consumed, 24)
                self.assertLessEqual(consumed, 30)
                expected_energy = max(sum(oracle.decode(code, law) ** 2 for code in frame) for frame in frames)
                self.assertEqual(stats["processed_local_energy_max"],
                                 max(before["processed_local_energy_max"], expected_energy),
                                 "必须观察到真实解码PCM节点能量，不以包计数冒充解码")
                self.assertEqual(stats["processed_local_nonzero_frames"] - before["processed_local_nonzero_frames"], consumed)
                self.finish()

    def test_tone_then_file_all_pcm_and_shared_tx_identity_both_laws(self):
        for law, payload in [("PCMU", 0), ("PCMA", 8)]:
            with self.subTest(law=law):
                self.local_call(law, payload)
                token = self.execute(self.channel, "playback", "tone_stream://%(200,0,500)")
                tone, _ = self.pump(.35)
                expected = [round(8192 * math.sin(2 * math.pi * 500 * i / 8000)) for i in range(1600)]
                self.assert_pcm(tone, expected)
                self.assertEqual(self.application_event(token)["Application-Response"], "FILE PLAYED")
                token = self.execute(self.channel, "playback", "prompt.wav")
                file, _ = self.pump(.30)
                self.assert_pcm(file, self.prompt)
                self.assertEqual(self.application_event(token)["Application-Response"], "FILE PLAYED")
                self.assert_timeline()
                self.assertEqual(self.pump(.10)[0], [])
                self.finish()

    def test_read_real_digits_end_repeats_and_no_rtp_reflection(self):
        self.local_call("PCMA", 8)
        token = self.execute(self.channel, "read", "1 4 silence choice 2000 #")
        self.application_event(token, False)
        events = []
        for index, digit in enumerate("12#"):
            events += self.digit_events(digit, index * .20)
        self.assertEqual(self.pump(.75, events)[0], [])
        end = self.application_event(token)
        self.assertEqual((end["Application-Response"], end["variable_choice"], end["variable_read_result"],
                          end["variable_read_terminator_used"]), ("_none_", "12", "success", "#"))
        self.esl.api("echo local-event-barrier")
        events = [e for e in self.esl.events if e["Event-Name"] == "DTMF"]
        self.assertEqual([e["DTMF-Digit"] for e in events], list("12#"))
        self.assertTrue(all(e["Unique-ID"] == self.channel for e in events))
        self.finish()

    def test_read_wav_barge_in_and_break_waits_for_real_timeout(self):
        self.local_call()
        token = self.execute(self.channel, "read", "1 1 long-prompt.wav choice 1000 #")
        packets, _ = self.pump(.24, self.digit_events("5", .04))
        self.assertTrue(packets)
        self.assertLess(len(packets), 20)
        self.assert_pcm(packets, (self.prompt * 4)[:len(packets) * 160])
        end = self.application_event(token)
        self.assertEqual((end["variable_choice"], end["variable_read_result"]), ("5", "success"))
        self.assertEqual(self.pump(.10)[0], [])
        token = self.execute(self.channel, "read", "1 3 tone_stream://%(2000,0,500) next_choice 1000 #")
        self.assertTrue(self.pump(.08)[0])
        began = time.monotonic()
        self.assertEqual(self.esl.api("uuid_break " + self.channel), "+OK\n")
        self.pump(.04)  # 仅排空已提交内核的停止前报文，随后单独检查静音。
        self.assertEqual(self.pump(.20)[0], [])
        end = self.application_event(token)
        self.assertGreaterEqual(time.monotonic() - began, .90)
        self.assertEqual((end["Application-Response"], end["variable_read_result"]), ("_none_", "failure"))
        self.assertNotIn("variable_next_choice", end)
        self.finish()

    def test_default_dtmf_terminator_and_missing_file_do_not_resume_audio(self):
        self.local_call()
        token = self.execute(self.channel, "playback", "tone_stream://%(2000,0,500)")
        packets, _ = self.pump(.32, self.digit_events("*", .08))
        self.assertGreater(len(packets), 1)
        self.assertLess(len(packets), 18)
        end = self.application_event(token)
        self.assertEqual((end["Application-Response"], end["variable_playback_terminator_used"]), ("FILE PLAYED", "*"))
        self.assertEqual(self.pump(.12)[0], [])
        missing = self.execute(self.channel, "playback", "missing.wav")
        self.assertEqual(self.application_event(missing)["Application-Response"], "FILE NOT FOUND")
        self.assertEqual(self.pump(.12)[0], [])
        self.finish()

    def test_active_dtmf_shared_audio_clock_all_digits_and_bounded_queue(self):
        self.local_call("PCMA", 8)
        play = self.execute(self.channel, "playback", "tone_stream://%(600,0,500)")
        first, _ = self.pump(.08)
        self.assertTrue(first)
        digits = "0123456789*#ABCD"
        self.assertEqual(self.esl.api(f"uuid_send_dtmf {self.channel} {digits}@55"),
                         f"+OK {self.channel} sent DTMF {digits}@55.\n")
        self.pump(2.9)
        self.assertEqual(self.application_event(play)["Application-Response"], "FILE PLAYED")
        audio = [p for p in self.output if p["payload"] == 8]
        self.assert_pcm(audio, [round(8192 * math.sin(2 * math.pi * 500 * i / 8000)) for i in range(4800)])
        count = assert_events(self, [p["raw"] for p in self.output], digits, 55)
        self.assert_timeline()
        starts = [p for p in self.output if p["payload"] == 101 and p["raw"][1] & 128]
        self.assertEqual(len(starts), len(digits))
        for previous, current in zip(starts, starts[1:]):
            self.assertEqual((current["timestamp"] - previous["timestamp"]) & 0xffffffff, (55 + 100) * 8,
                             "相邻事件必须保持55ms数字加100ms间隔的共同8k时间线")
        elapsed = (starts[0]["timestamp"] - audio[0]["timestamp"]) & 0xffffffff
        self.assertLessEqual(abs(elapsed / 8000 + .020 - (starts[0]["at"] - audio[0]["at"])), .040,
                             "主动按键起点没有使用音频的同一本地时钟")
        status = json.loads(self.esl.api("uuid_send_dtmf_status " + self.channel))
        self.assertEqual((status["state"], status["completed_digits"], status["sent_packets"]), ("idle", 16, count))
        self.assertTrue(self.esl.api("uuid_send_dtmf " + self.channel + " " + "1" * 32 + "@1000").startswith("+OK"))
        self.assertIn("queue full", self.esl.api("uuid_send_dtmf " + self.channel + " 2"))
        self.pump(.04)
        stats = self.finish(server=True)
        self.assertEqual(stats["dtmf_send_active"], 0)
        self.assertGreaterEqual(stats["dtmf_send_cancelled_digits"], 32)
        self.pump(.03)
        self.assertEqual(self.pump(.15)[0], [])

    def test_cn_gap_and_loss_consume_pcm_without_transmitting_silence(self):
        self.local_call()
        frames = oracle.audio_frames("PCMU", 733, 24)
        received, _ = self.pump(.75, self.audio_events(frames, omitted=(5, 11, 12, 13, 14), cn_at=10))
        self.assertEqual(received, [])
        stats = self.stats(lambda s: s["processed_decoded_frames"] == 18 and s["processed_cn_packets"] == 1)
        self.assertGreater(stats["processed_plc_frames"], 0)
        self.assertLessEqual(stats["processed_plc_frames"], 7)
        self.assertEqual(stats["processed_local_consumed_frames"], stats["processed_decoded_frames"] + stats["processed_plc_frames"])
        self.assertEqual(stats["processed_encoded_frames"], 0)
        self.finish()

    def test_unnegotiated_source_cannot_feed_pcm_or_receive_audio(self):
        self.local_call()
        frames = oracle.audio_frames("PCMU", 467, 8)
        forged = [(at, self.stranger, port, raw) for at, _, port, raw in self.audio_events(frames)]
        self.assertEqual(self.pump(.30, forged)[0], [])
        self.assertEqual(self.stats()["processed_decoded_frames"], 0)
        self.source_sequence, self.source_timestamp = 31000, 176000
        self.assertEqual(self.pump(.34, self.audio_events(frames))[0], [])
        self.assertEqual(self.stats(lambda s: s["processed_decoded_frames"] == 8)["processed_decoded_frames"], 8)
        self.finish()

    def test_rtcp_matches_real_local_tx_and_rx_then_bye(self):
        self.local_call()
        frames = oracle.audio_frames("PCMU", 467, 12)
        events = self.audio_events(frames)
        report, lsr = oracle.rtcp_source_report(SSRC, "local-fixture", self.source_timestamp, 12, 1920)
        events.append((.28, self.ac, self.a_ports[1], report))
        token = self.execute(self.channel, "playback", "tone_stream://%(200,0,500)")
        audio, reports = self.pump(8.1, events)
        self.assert_pcm(audio, [round(8192 * math.sin(2 * math.pi * 500 * i / 8000)) for i in range(1600)])
        self.assertEqual(self.application_event(token)["Application-Response"], "FILE PLAYED")
        self.assertTrue(reports, "未收到真实周期RTCP")
        ssrc = audio[0]["ssrc"]
        valid, sender_reports = [], []
        for item in reports:
            for part in item["parts"]:
                if part["kind"] == 200:
                    self.assertEqual((part["ssrc"], part["packets"], part["octets"]), (ssrc, 10, 1600))
                    sender_reports.append(part)
                    valid += [r for r in part["reports"] if r["ssrc"] == SSRC and r["lsr"] == lsr]
                if part["kind"] == 202:
                    self.assertTrue(any(c["ssrc"] == ssrc and any(k == 1 and value for k, value in c["items"])
                                        for c in part["chunks"]))
        self.assertTrue(valid, "本地RTCP没有关联真实A来源的SR")
        self.assertTrue(all(r["highest_sequence"] == 31011 and r["cumulative_lost"] == 0 and r["dlsr"] > 0 for r in valid))
        self.finish()
        _, ended = self.pump(.06)
        bye = [p for r in ended for p in r["parts"] if p["kind"] == 203]
        self.assertEqual(len(bye), 1)
        self.assertEqual(bye[0]["sources"], [ssrc])
        released = [p for r in ended for p in r["parts"] if p["kind"] == 200]
        self.assertEqual(len(released), 1, "释放需真实最终SR加SDES/BYE复合包")
        self.assertEqual((released[0]["ssrc"], released[0]["packets"], released[0]["octets"]), (ssrc, 10, 1600))
        sender_reports += released
        self.assertGreaterEqual(len(sender_reports), 2)
        for first, second in zip(sender_reports, sender_reports[1:]):
            ntp_first = (first["ntp_seconds"] << 32) | first["ntp_fraction"]
            ntp_second = (second["ntp_seconds"] << 32) | second["ntp_fraction"]
            self.assertGreater(ntp_second, ntp_first)
            ticks = (second["rtp_timestamp"] - first["rtp_timestamp"]) & 0xffffffff
            expected = (ntp_second - ntp_first) * 8000 / (1 << 32)
            self.assertGreater(ticks, 0, "播放停止后SR媒体时钟不能冻结")
            self.assertLessEqual(abs(ticks - expected), 2,
                                 "RTCP NTP与本地RTP时钟需保持8k映射；两tick仅覆盖整数取整")

    def test_port_reuse_rejects_old_endpoint_and_restarts_local_identity(self):
        self.local_call()
        old_sender = self.ar
        token = self.execute(self.channel, "playback", "prompt.wav")
        first, _ = self.pump(.25)
        self.assert_pcm(first, self.prompt)
        self.application_event(token)
        self.finish()
        self.ar, self.ac = self.sock(), self.sock()
        # 新通话保持同一合法PT/codec，只改变已协商端点；不能靠编码不匹配掩盖旧来源被接纳。
        self.local_call()
        before = self.stats()["processed_decoded_frames"]
        stale = oracle.rtp(0, 31001, 176160, oracle.audio_frames("PCMU", 467, 1)[0], SSRC)
        self.assertEqual(self.pump(.25, [(0, old_sender, self.a_ports[0], stale)])[0], [])
        self.assertEqual(self.stats()["processed_decoded_frames"], before)
        frames = oracle.audio_frames("PCMU", 733, 8)
        self.assertEqual(self.pump(.34, self.audio_events(frames))[0], [])
        self.assertEqual(self.stats(lambda s: s["processed_decoded_frames"] == before + 8)
                         ["processed_decoded_frames"], before + 8)
        token = self.execute(self.channel, "playback", "prompt.wav")
        second, _ = self.pump(.25)
        self.assert_pcm(second, self.prompt)
        self.assertNotEqual(second[0]["ssrc"], first[0]["ssrc"])
        self.application_event(token)
        self.finish()

    def test_xml_waits_for_ack_before_playing_and_never_connects_b(self):
        self.local_call(ack=False, number="7101")
        self.assertEqual(self.get("/v1/status")["established_calls"], 0)
        self.assertEqual(self.pump(.10)[0], [])
        self.execute(self.channel, "playback", "prompt.wav", expected="-ERR")
        self.ack("7101")
        audio, _ = self.pump(.30)
        self.assert_pcm(audio, self.prompt)
        self.assertEqual(self.esl.api("uuid_getvar " + self.channel + " local_test"), "中文")
        self.esl.api("echo xml-order-barrier")
        done = [e["Application"] for e in self.esl.events if e["Event-Name"] == "CHANNEL_EXECUTE_COMPLETE"]
        self.assertEqual(done, ["answer", "set", "playback"])
        self.no_upstream()
        self.finish(server=True)

    def test_xml_missing_ack_reclaims_without_playback(self):
        self.local_call(ack=False, number="7101")
        self.assertEqual(self.pump(1.3)[0], [])
        state = self.zero_status(self.admission_before_call)
        self.final_status = self.observe("missing_ack_zero", state)
        self.assert_media_ports_free()
        self.no_upstream()
        self.esl.api("echo missing-ack-barrier")
        self.assertNotIn("playback", [e.get("Application") for e in self.esl.events])
        self.assertEqual(self.esl.api("uuid_exists " + self.channel), "false")

    def test_unsupported_codec_and_ptime_reject_before_allocation(self):
        self.rejection_statuses = []
        for law, payload, ptime in [("G722", 9, 20), ("PCMU", 0, 10), ("PCMA", 8, 30)]:
            with self.subTest(law=law, ptime=ptime):
                self.local_call(law, payload, ptime=ptime, expected=488)
                state = self.zero_status(self.admission_before_call)
                self.rejection_statuses.append(state)
                self.observe("rejected_before_allocate", state)
                self.assert_media_ports_free()


def main():
    """每轮显式输入候选并新建证据目录，禁止覆盖历史原帧或混用已部署二进制。"""
    global CONTROL, MEDIA, ARTIFACTS, CONNECTED
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--control", type=Path, required=True)
    parser.add_argument("--media", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--connected", action="store_true")
    args, remaining = parser.parse_known_args()
    CONTROL, MEDIA, ARTIFACTS, CONNECTED = args.control.resolve(), args.media.resolve(), args.artifacts.resolve(), args.connected
    for path in (CONTROL, MEDIA):
        if not path.is_file():
            parser.error("实际候选二进制缺失：" + str(path))
    ARTIFACTS.mkdir(parents=True, exist_ok=False)
    unittest.main(argv=[__file__, *remaining], verbosity=2)


if __name__ == "__main__":
    main()
