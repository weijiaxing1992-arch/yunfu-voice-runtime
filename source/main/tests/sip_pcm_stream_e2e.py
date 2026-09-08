#!/usr/bin/env python3
"""真实SIP授权→私有RVA1流→Go SDK→Rust→UDP验收；仅使用本例隔离进程。

不访问主9080、外部电话或VM。私有流/ESL/RTP由同一个有界select持续读取，
避免等待控制回执时积累音频并把测试接收积压误判成媒体发送突发。
"""
import argparse
import json
import os
from pathlib import Path
import select
import shutil
import signal
import socket
import stat
import struct
import subprocess
import tempfile
import time
import unittest
import uuid
import wave

import e2e
import esl_e2e
import media_processed_local_e2e as local
import media_processed_fixture as oracle
import media_pcm_turn_e2e as pcm_oracle
from dtmf_send_e2e import assert_events

CONTROL = MEDIA = ARTIFACTS = None
CONNECTED = False
ZERO_TOKEN = bytes(16)
STATES = pcm_oracle.STATES


def request_frame(op, request_id, token=ZERO_TOKEN, turn_id=0, offset=0, body=b"", arg1=0, arg2=0):
    """按公开冻结偏移独立构造64字节头，不依赖生产序列化或JSON音频。"""
    if len(token) != 16:
        raise ValueError("测试令牌必须正好16字节")
    return struct.pack("<4sBBHQ16sQQIHH8s", b"RVA1", op, 0, 0, request_id, token,
                       turn_id, offset, len(body), arg1, arg2, bytes(8)) + body


def response_frame(raw):
    """完整96字节头与实际UTF8消息逐字段校验，未声明的保留位必须为零。"""
    if len(raw) < 96 or raw[:4] != b"RVR1" or raw[94:96] != bytes(2):
        raise AssertionError("RVR1回应头损坏")
    length = struct.unpack_from("<H", raw, 92)[0]
    if length > 512 or len(raw) != 96 + length or raw[5] >= len(STATES):
        raise AssertionError("RVR1长度/状态非法")
    code = struct.unpack_from("<H", raw, 6)[0]
    if code > 10:
        raise AssertionError("RVR1错误码越界")
    turn, accepted, sent, queued, discarded, age = struct.unpack_from("<6Q", raw, 32)
    qbytes, qms, buffer, prebuffer = struct.unpack_from("<IIHH", raw, 80)
    if accepted != sent + queued + discarded or any(x % 160 for x in (accepted, sent, queued, discarded)):
        raise AssertionError("RVR1样本计数不守恒")
    if qbytes != queued * 2 or qms != queued // 8:
        raise AssertionError("RVR1队列样本/字节/毫秒不一致")
    return dict(op=raw[4], state=STATES[raw[5]], code=code, request_id=struct.unpack_from("<Q", raw, 8)[0],
                token=raw[16:32].hex(), turn_id=turn, accepted_samples=accepted, sent_samples=sent,
                queued_samples=queued, discarded_samples=discarded, oldest_age_ms=age,
                queued_bytes=qbytes, queued_ms=qms, buffer_ms=buffer, prebuffer_ms=prebuffer,
                message=raw[96:].decode("utf-8"))


class StreamPeer:
    """每连接递增请求ID；令牌按真实回应取得，跨连接使用不绑定猜测的内部session。"""
    def __init__(self, owner, role):
        self.owner, self.role, self.sequence = owner, role, 0
        self.closed, self.expect_close = False, False
        self.buffer, self.replies = bytearray(), {}
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(2)
        self.sock.connect(str(owner.socket_path))
        self.sock.setblocking(False)
        owner.streams.append(self)
        self.request(0, arg1=role)

    def request(self, op, token=ZERO_TOKEN, turn_id=0, offset=0, body=b"", arg1=0, arg2=0, expected=0):
        self.sequence += 1
        identifier = self.sequence
        raw = request_frame(op, identifier, token, turn_id, offset, body, arg1, arg2)
        self.expect_close = expected == 1
        self.owner.record("stream_send", raw, (str(self.owner.socket_path), self.role))
        self.owner.write_stream(self.sock, raw)
        self.owner.wait(lambda: identifier in self.replies, 1.5)
        value = self.replies.pop(identifier)
        self.owner.assertEqual((value["op"], value["request_id"]), (op, identifier), value)
        self.owner.assertIn(value["code"], expected if isinstance(expected, tuple) else (expected,), value)
        if expected == 0 and op not in (0, 1, 7):
            self.owner.assertEqual(value["token"], token.hex())
            self.owner.assertEqual(value["turn_id"], turn_id)
        if expected != 0:
            self.owner.assertTrue(value["message"], "拒绝必须说明实际原因")
        self.owner.observations.append(dict(label="stream_reply", observed_at=time.time(), role=self.role, response=value))
        if self.expect_close:
            self.owner.wait(lambda: self.closed, 1)
        return value

    def ingest(self, raw):
        self.buffer.extend(raw)
        self.owner.assertLessEqual(len(self.buffer), 65536)
        while len(self.buffer) >= 96:
            size = struct.unpack_from("<H", self.buffer, 92)[0]
            self.owner.assertLessEqual(size, 512)
            if len(self.buffer) < 96 + size:
                return
            frame = bytes(self.buffer[:96 + size])
            del self.buffer[:96 + size]
            value = response_frame(frame)
            self.owner.assertNotIn(value["request_id"], self.replies)
            self.replies[value["request_id"]] = value

    def close(self):
        self.closed = True
        self.sock.close()
        if self in self.owner.streams:
            self.owner.streams.remove(self)


class PollingESL(esl_e2e.ESLPeer):
    """保留ESL原语义，仅替换阻塞read为统一媒体poll，不丢弃中间事件。"""
    def __init__(self, owner, password):
        self.owner, self.events, self.frames = owner, [], []
        self.buffer = bytearray()
        self.sock = socket.create_connection(("127.0.0.1", owner.esl_port), 2)
        self.sock.setblocking(False)
        owner.esl = self
        owner.assertEqual(self.frame()[0]["content-type"], "auth/request")
        # 凭据只在隔离端点内发送，不归档可恢复明文。
        owner.write_stream(self.sock, ("auth " + password + "\n\n").encode())
        owner.assertEqual(self.frame()[0]["reply-text"], "+OK accepted")

    def ingest(self, raw):
        self.buffer.extend(raw)
        self.owner.assertLessEqual(len(self.buffer), 262144 + 65536)
        while True:
            boundary, delimiter = self.buffer.find(b"\n\n"), 2
            alternate = self.buffer.find(b"\r\n\r\n")
            if alternate >= 0 and (boundary < 0 or alternate < boundary):
                boundary, delimiter = alternate, 4
            if boundary < 0:
                self.owner.assertLessEqual(len(self.buffer), 65536)
                return
            headers = {}
            for line in self.buffer[:boundary].decode().splitlines():
                key, value = line.split(":", 1)
                headers[key.lower()] = value.strip()
            size = int(headers.get("content-length", "0"))
            self.owner.assertTrue(0 <= size <= 262144)
            total = boundary + delimiter + size
            if len(self.buffer) < total:
                return
            frame = bytes(self.buffer[:total])
            del self.buffer[:total]
            self.owner.record("esl_receive", frame, self.sock.getpeername())
            self.frames.append((headers, frame[boundary + delimiter:]))

    def frame(self):
        self.owner.wait(lambda: bool(self.frames), 4)
        return self.frames.pop(0)

    def command(self, command):
        raw = (command + "\n\n").encode()
        self.owner.record("esl_send", raw, self.sock.getpeername())
        self.owner.write_stream(self.sock, raw)
        while True:
            head, body = self.frame()
            if head["content-type"] == "text/event-json":
                self.events.append(json.loads(body))
            else:
                return head, body

    def close(self):
        self.sock.close()


class SIPPCMFixture(local.LocalFixture):
    """只在新夹具初始化私有监听；旧测试模块和主服务的配置均不修改。"""
    def setUp(self):
        self.resources, self.wire, self.observations, self.output, self.reports, self.streams = [], [], [], [], [], []
        self.process = self.esl = self.log = None
        self.started, self.wire_bytes = time.monotonic(), 0
        self.expected_generation, self.expected_restarts = 1, 0
        self.directory = ARTIFACTS / (self.id().rsplit(".", 1)[-1] + "-" + uuid.uuid4().hex[:8])
        self.directory.mkdir(parents=True, exist_ok=False)
        self.private_dir = Path(tempfile.mkdtemp(prefix="rvapcm-", dir="/private/tmp"))
        self.private_dir.chmod(0o700)
        self.socket_path = self.private_dir / "agent.sock"
        self.addCleanup(self.close)
        self.binary_before = {"control": local.digest(CONTROL), "media": local.digest(MEDIA)}
        self.test_paths = [Path(__file__), Path(local.__file__), Path(oracle.__file__), Path(pcm_oracle.__file__),
                           Path(e2e.__file__), Path(esl_e2e.__file__), Path(local.applications.__file__),
                           Path(__file__).with_name("dtmf_send_e2e.py")]
        self.test_sources_before = {p.name: local.digest(p) for p in self.test_paths}
        self.upstream, self.a, self.ar, self.ac, self.stranger = [self.sock() for _ in range(5)]
        probe = self.sock()
        self.address = probe.getsockname()
        probe.close()
        self.port, self.esl_port, self.base = self.tcp_port(), self.tcp_port(), self.free_media_block()
        self.source_sequence, self.source_timestamp = 31000, 176000
        self.prompt = pcm_oracle.pcm_samples(10, 7)
        for name, samples in (("prompt.wav", self.prompt), ("long-prompt.wav", self.prompt * 10)):
            with wave.open(str(self.directory / name), "wb") as stream:
                stream.setnchannels(1)
                stream.setsampwidth(2)
                stream.setframerate(8000)
                stream.writeframes(struct.pack("<" + "h" * len(samples), *samples))
        config = json.loads((e2e.ROOT / "config/local.json").read_text())
        config["sip"].update(listen=f"127.0.0.1:{self.address[1]}", advertise=f"127.0.0.1:{self.address[1]}",
                             upstream=f"127.0.0.1:{self.upstream.getsockname()[1]}", local_extensions=["1000"], ack_timeout_ms=1000)
        config["media"].update(binary=str(MEDIA), workers=1, connect_sockets=CONNECTED, processing="g711",
                               port_start=self.base, port_end=self.base + 3, port_reuse_delay_ms=0,
                               adaptive_admission=False, playback_root=str(self.directory), max_packets_per_second_per_leg=500)
        config["limits"].update(max_calls=1, calls_per_second=10, burst_calls=10)
        config["admin"]["listen"] = f"127.0.0.1:{self.port}"
        config["journal"]["path"] = str(self.directory / "events.jsonl")
        config["pcm_stream"] = dict(socket_path=str(self.socket_path), max_connections=8, max_streams=1)
        self.config = config
        (self.directory / "config.json").write_text(json.dumps(config, ensure_ascii=False, indent=2) + "\n")
        self.fixture_before = {p.name: local.digest(p) for p in self.directory.iterdir() if p.is_file()}
        password = uuid.uuid4().hex
        secret = self.directory / "esl.secret"
        secret.write_text(password + "\n")
        secret.chmod(0o600)
        self.log = (self.directory / "server.log").open("w+")
        self.process = subprocess.Popen([str(CONTROL), "-config", str(self.directory / "config.json"),
                                         "-esl-listen", f"127.0.0.1:{self.esl_port}", "-esl-password-file", str(secret)],
                                        cwd=e2e.ROOT, stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                self.fail("SIP/PCM候选启动失败：" + self.log_tail())
            try:
                if self.get("/readyz")["ready"] and self.socket_path.exists():
                    break
            except (OSError, ValueError):
                pass
            time.sleep(.03)
        else:
            self.fail("SIP/PCM候选未在期限内就绪")
        self.assertTrue(stat.S_ISSOCK(self.socket_path.stat().st_mode))
        self.assertEqual(stat.S_IMODE(self.socket_path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.private_dir.stat().st_mode), 0o700)
        self.assertEqual(self.private_dir.stat().st_uid, os.getuid())
        self.assertLessEqual(len(os.fsencode(self.socket_path)), 100)
        self.esl = PollingESL(self, password)
        head, _ = self.esl.command("event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_HANGUP CHANNEL_EXECUTE "
                                  "CHANNEL_EXECUTE_COMPLETE DTMF PLAYBACK_START PLAYBACK_STOP CHANNEL_PARK CHANNEL_UNPARK")
        self.assertEqual(head["reply-text"], "+OK event listener enabled json")
        self.control, self.data = StreamPeer(self, 1), StreamPeer(self, 2)

    def write_stream(self, sock, raw):
        """完整请求在一秒内写入；短写继续原帧，不重发请求或丢掉同时到达的媒体。"""
        end, cursor = time.monotonic() + 1, 0
        while cursor < len(raw):
            self.assertLess(time.monotonic(), end, "测试流写入超时")
            try:
                sent = sock.send(raw[cursor:])
            except BlockingIOError:
                self.poll(.001)
                continue
            self.assertGreater(sent, 0)
            cursor += sent

    def wait(self, predicate, timeout=2):
        end = time.monotonic() + timeout
        while not predicate():
            self.assertLess(time.monotonic(), end, "SIP/PCM有界等待超时")
            self.poll(min(.020, end - time.monotonic()))

    def poll(self, timeout):
        sockets = {self.ar: "rtp_receive", self.ac: "rtcp_receive", self.stranger: "stranger_receive"}
        peers = {p.sock: p for p in self.streams}
        sockets.update({p.sock: "stream_receive" for p in self.streams})
        if self.esl is not None:
            sockets[self.esl.sock] = "esl"
        for sock in sockets:
            sock.setblocking(False)
        ready, _, _ = select.select(list(sockets), [], [], max(0, min(.020, timeout)))
        for sock in ready:
            for _ in range(64):
                try:
                    raw, peer = sock.recvfrom(65536)
                except BlockingIOError:
                    break
                kind = sockets[sock]
                if not raw and kind == "stream_receive" and peers[sock].expect_close:
                    peers[sock].close()
                    break
                self.assertTrue(raw, "私有流或ESL提前关闭")
                if kind == "esl":
                    self.esl.ingest(raw)
                    continue
                if kind == "stream_receive":
                    self.record(kind, raw, (str(self.socket_path), peers[sock].role))
                    peers[sock].ingest(raw)
                    continue
                self.record(kind, raw, peer)
                self.assertNotEqual(kind, "stranger_receive", "未协商来源收到媒体")
                expected = self.base + int(kind == "rtcp_receive")
                self.assertEqual(peer, ("127.0.0.1", expected))
                if kind == "rtcp_receive":
                    self.reports.append(dict(parts=oracle.unpack_rtcp(raw), at=time.monotonic(), raw=raw))
                else:
                    packet = oracle.unpack_rtp(raw)
                    packet.update(raw=raw, peer=peer, at=time.monotonic())
                    self.output.append(packet)
                    self.assertLessEqual(len(self.output), 2048)

    def pump(self, duration, events=(), actions=()):
        """固定绝对计划且20ms迟到即失败；不能积累相对等待或赶发过期音频。"""
        self.assertTrue(0 <= duration <= 9)
        events, actions = sorted(events, key=lambda v: v[0]), sorted(actions, key=lambda v: v[0])
        began, cursor, action_cursor = time.monotonic(), 0, 0
        first, report_first = len(self.output), len(self.reports)
        while cursor < len(events) or action_cursor < len(actions) or time.monotonic() < began + duration:
            now = time.monotonic()
            if cursor < len(events) and now >= began + events[cursor][0]:
                at, source, port, raw = events[cursor]
                self.assertLess(now - began - at, .020, "真实来源发生器迟到达到20ms")
                self.record("rtcp_send" if source is self.ac else "rtp_send", raw, ("127.0.0.1", port))
                self.assertEqual(source.sendto(raw, ("127.0.0.1", port)), len(raw))
                cursor += 1
                continue
            if action_cursor < len(actions) and now >= began + actions[action_cursor][0]:
                at, action = actions[action_cursor]
                self.assertLess(now - began - at, .020)
                action()
                action_cursor += 1
                continue
            next_at = began + duration
            if cursor < len(events):
                next_at = min(next_at, began + events[cursor][0])
            if action_cursor < len(actions):
                next_at = min(next_at, began + actions[action_cursor][0])
            self.poll(next_at - time.monotonic())
        return self.output[first:], self.reports[report_first:]

    def begin(self, turn=1, buffer=200, prebuffer=100, expected=0, channel=None):
        result = self.control.request(1, turn_id=turn, body=(channel or self.channel).encode(), arg1=buffer, arg2=prebuffer, expected=expected)
        if expected == 0:
            self.assertNotEqual(result["token"], ZERO_TOKEN.hex())
            self.assertEqual(result["turn_id"], turn)
            self.assertEqual((result["buffer_ms"], result["prebuffer_ms"]), (buffer, prebuffer))
            return (bytes.fromhex(result["token"]), turn), result
        return None, result

    def push(self, handle, samples, offset=0, expected=0, peer=None):
        body = struct.pack("<" + "h" * len(samples), *samples)
        return (peer or self.data).request(2, token=handle[0], turn_id=handle[1], offset=offset, body=body, expected=expected)

    def feed(self, handle, samples, offset=0):
        for index in range(0, len(samples), 800):
            self.push(handle, samples[index:index + 800], offset + index)

    def control_op(self, op, handle, expected=0, offset=0, fade=0, peer=None):
        return (peer or self.control).request(op, token=handle[0], turn_id=handle[1], offset=offset, arg1=fade, expected=expected)

    def final(self, handle, state="completed", timeout=2):
        end = time.monotonic() + timeout
        result = None
        while time.monotonic() < end:
            result = self.control_op(5, handle)
            if result["state"] == state:
                return result
            self.poll(.003)
        self.fail("流未到真实终态：" + str(result))

    def audio(self, start=0):
        return [p for p in self.output[start:] if p["payload"] == self.payload]

    def zero_status(self, previous):
        # 故障例必须匹配预期新代；常规例仍严格要求初代，绝不把任何健康worker当旧句柄所属者。
        state = self.wait_status(lambda s: s["active_calls"] == s["established_calls"] == 0
                                and len(s["workers"]) == 1 and s["workers"][0]["generation"] == self.expected_generation
                                and s["workers"][0]["restarts"] == self.expected_restarts
                                and s["workers"][0]["healthy"] and all(s["workers"][0]["stats"].get(k) == 0 for k in
                                    ("active_calls", "processed_active_calls", "processed_local_active_calls"))
                                and s["workers"][0].get("admission", {}).get("sampled_at") not in (None, previous)
                                and s["controller"]["media_results_queue_length"] == 0, require_stats=True)
        return state

    def close(self):
        for peer in list(getattr(self, "streams", [])):
            peer.close()
        try:
            super().close()
        finally:
            # 主进程关闭后才能移除本例私有目录，不能删除其他监听器或绕过正常退出检查。
            if hasattr(self, "private_dir"):
                removed = not self.socket_path.exists()
                evidence = self.directory / "wire.json"
                if evidence.exists():
                    result = json.loads(evidence.read_text())
                    result.update(scope="真实SIP ACK授权+私有RVA1流+Go SDK+FD3+Rust本地G711 RTP；不是ASR/TTS/原版等价/容量认证",
                                  private_socket_removed_by_server=removed)
                    evidence.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
                shutil.rmtree(self.private_dir)
                self.assertTrue(removed, "服务未正常移除自己创建的私有socket")


class SIPPCMIntegration(SIPPCMFixture):
    def test_ack_before_wrong_and_correct_gate_real_pcm(self):
        self.local_call(ack=False)
        self.begin(expected=3)
        header = e2e.parse(self.accepted)[1]
        for source, tag, cseq in ((self.stranger, header["to"], 1), (self.a, "<sip:1000@local>;tag=wrong", 1),
                                  (self.a, header["to"], 2)):
            raw = e2e.message("ACK", "sip:1000@local", source.getsockname()[1], header["call-id"],
                              "z9hG4bK" + uuid.uuid4().hex, to=tag, cseq=cseq)
            self.send_sip(source, raw, self.address)
            self.pump(.025)
            self.assertEqual(self.get("/v1/status")["established_calls"], 0)
            self.begin(expected=3)
        self.assertEqual(self.output, [])
        self.ack()
        self.ack()
        self.wait_status(lambda s: s["established_calls"] == 1)
        handle, _ = self.begin()
        samples = pcm_oracle.pcm_samples(5, 1)
        self.feed(handle, samples)
        self.control_op(3, handle, offset=800)
        self.final(handle)
        self.assert_pcm(self.audio(), samples)
        self.finish()

    def test_no_ack_timeout_reclaims_and_old_uuid_cannot_begin(self):
        self.local_call(ack=False)
        self.begin(expected=3)
        bye, peer = self.receive_sip(self.a, lambda m: m[0].startswith("BYE "), timeout=2)
        self.send_sip(self.a, e2e.response(bye, port=self.a.getsockname()[1]), peer)
        self.final_status = self.observe("fresh_final_zero", self.zero_status(self.admission_before_call))
        self.assert_media_ports_free()
        self.begin(expected=3)
        self.assertEqual(self.esl.api("uuid_exists " + self.channel), "false")
        self.assertEqual(self.pump(.10)[0], [])
        self.no_upstream()

    def test_both_laws_persistent_stream_complete_and_same_begin_token(self):
        for law, payload in (("PCMU", 0), ("PCMA", 8)):
            with self.subTest(law=law):
                self.local_call(law, payload)
                handle, _ = self.begin()
                again, _ = self.begin()
                self.assertEqual(again, handle)
                samples = pcm_oracle.pcm_samples(10, 2)
                self.feed(handle, samples)
                self.control_op(3, handle, offset=1600)
                final = self.final(handle)
                self.assertEqual((final["accepted_samples"], final["sent_samples"]), (1600, 1600))
                self.assert_pcm(self.audio(), samples)
                self.assert_timeline()
                again, state = self.begin()
                self.assertEqual(again, handle)
                self.assertEqual(state["state"], "completed")
                self.assertEqual(self.pump(.08)[0], [])
                self.control_op(6, handle)
                self.control_op(5, handle, expected=4)
                self.finish()

    def test_new_turn_old_token_and_stop_barriers(self):
        self.local_call()
        old, _ = self.begin(10, buffer=200, prebuffer=200)
        self.feed(old, pcm_oracle.pcm_samples(5, 1))
        new, _ = self.begin(11, buffer=400, prebuffer=20)
        self.assertNotEqual(old[0], new[0])
        self.push(old, pcm_oracle.pcm_samples(1), 800, expected=4)
        samples = pcm_oracle.pcm_samples(20, 4)
        self.feed(new, samples)
        self.wait(lambda: len(self.audio()) >= 2)
        self.control_op(3, old, offset=800, expected=4)
        self.begin(10, expected=3)
        stopped = self.control_op(4, new)
        self.assertEqual(stopped["state"], "stopped")
        self.push(new, samples[:160], len(samples), expected=3)
        self.pump(.04)
        self.assertEqual(len(self.audio()) * 160, stopped["sent_samples"])
        self.assert_pcm(self.audio(), samples[:stopped["sent_samples"]])
        cut = len(self.output)
        fade, _ = self.begin(12, buffer=200, prebuffer=200)
        self.feed(fade, [-6000] * 800)
        self.assertEqual(self.control_op(4, fade, fade=40)["state"], "stopping")
        final = self.final(fade, "stopped")
        self.assertEqual((final["sent_samples"], final["discarded_samples"]), (320, 480))
        self.assert_pcm(self.audio(cut), [int(-6000 * (319 - i) / 319) for i in range(320)])
        self.assert_timeline()
        self.finish()

    def test_park_and_pcm_lifecycles_are_independent(self):
        self.local_call()
        park = self.execute(self.channel, "park", "")
        self.application_event(park, False)
        self.assertEqual(len(self.esl.wait_events("CHANNEL_PARK", 1)), 1)
        handle, _ = self.begin()
        samples = pcm_oracle.pcm_samples(5, 5)
        self.feed(handle, samples)
        self.control_op(3, handle, offset=800)
        self.final(handle)
        self.assert_pcm(self.audio(), samples)
        self.esl.api("echo pcm-park-barrier")
        self.assertFalse(any(e["Event-Name"] == "CHANNEL_UNPARK" or
                             (e["Event-Name"] == "CHANNEL_EXECUTE_COMPLETE" and e.get("Application-UUID") == park)
                             for e in self.esl.events))
        self.finish(server=True)
        self.assertEqual(self.application_event(park)["Application-Response"], "_none_")

    def test_silent_read_digits_and_timeout_with_pcm(self):
        self.local_call("PCMA", 8)
        read = self.execute(self.channel, "read", "1 4 silence choice 1500 #")
        self.application_event(read, False)
        handle, _ = self.begin(buffer=1000, prebuffer=100)
        samples = pcm_oracle.pcm_samples(40, 3)
        self.feed(handle, samples)
        self.control_op(3, handle, offset=len(samples))
        events = []
        for index, digit in enumerate("12#"):
            events.extend(self.digit_events(digit, index * .20))
        self.pump(.75, events)
        end = self.application_event(read)
        self.assertEqual((end["variable_choice"], end["variable_read_result"], end["variable_read_terminator_used"]), ("12", "success", "#"))
        self.final(handle)
        self.assert_pcm(self.audio(), samples)
        self.esl.api("echo pcm-read-barrier")
        self.assertEqual([e["DTMF-Digit"] for e in self.esl.events if e["Event-Name"] == "DTMF"], list("12#"))
        cut = len(self.output)
        read = self.execute(self.channel, "read", "1 1 silence next_choice 350 #")
        self.application_event(read, False)
        handle, _ = self.begin(2, buffer=1000, prebuffer=100)
        samples = pcm_oracle.pcm_samples(25, 6)
        self.feed(handle, samples)
        self.control_op(3, handle, offset=len(samples))
        end = self.application_event(read)
        self.assertEqual((end["Application-Response"], end["variable_read_result"]), ("_none_", "failure"))
        self.final(handle)
        self.assert_pcm(self.audio(cut), samples)
        self.assert_timeline()
        self.finish()

    def test_wav_and_prompt_read_exclude_pcm_without_cancelling_owner(self):
        self.local_call()
        for application, arguments in (("playback", "prompt.wav"), ("read", "1 1 prompt.wav no_input 300 #")):
            with self.subTest(application=application):
                cut = len(self.output)
                token = self.execute(self.channel, application, arguments)
                self.application_event(token, False)
                self.begin(expected=5)
                self.application_event(token)
                self.assert_pcm(self.audio(cut), self.prompt)
        cut = len(self.output)
        handle, _ = self.begin(buffer=200, prebuffer=200)
        self.execute(self.channel, "playback", "prompt.wav", expected="-ERR")
        self.execute(self.channel, "read", "1 1 prompt.wav blocked 300 #", expected="-ERR")
        samples = pcm_oracle.pcm_samples(5, 8)
        self.feed(handle, samples)
        self.control_op(3, handle, offset=800)
        self.final(handle)
        self.assert_pcm(self.audio(cut), samples)
        self.assert_timeline()
        self.finish()

    def test_hangup_revokes_tokens_and_reused_ports_have_only_new_call_pcm(self):
        for server in (False, True):
            with self.subTest(server_hangup=server):
                self.local_call()
                old_channel = self.channel
                handle, _ = self.begin(buffer=400, prebuffer=20)
                self.feed(handle, pcm_oracle.pcm_samples(20, 1))
                self.wait(lambda: len(self.audio()) >= 2)
                self.finish(server=server)
                self.pump(.05)
                # 回收器每250ms运行；退休尚未回收和已回收两种确定拒绝均不能调用媒体。
                self.control_op(5, handle, expected=(4, 8))
                self.push(handle, pcm_oracle.pcm_samples(1), 3200, expected=(4, 8))
                self.begin(channel=old_channel, expected=3)
                self.assertEqual(self.pump(.10)[0], [])
                self.local_call()
                new, _ = self.begin()
                samples = pcm_oracle.pcm_samples(5, 9)
                self.feed(new, samples)
                self.control_op(3, new, offset=800)
                self.final(new)
                self.assert_pcm(self.audio(), samples)
                self.finish()

    def test_worker_restart_retires_old_generation_and_new_call_stream_works(self):
        self.local_call()
        old_channel = self.channel
        handle, _ = self.begin(buffer=400, prebuffer=20)
        self.feed(handle, pcm_oracle.pcm_samples(20, 1))
        self.wait(lambda: len(self.audio()) >= 2)
        state = self.get("/v1/status")
        worker = state["workers"][0]
        pid = worker["pid"]
        identity = subprocess.check_output(["ps", "-p", str(pid), "-o", "ppid=", "-o", "command="], text=True).strip()
        parent, command = identity.split(None, 1)
        self.assertEqual(int(parent), self.process.pid)
        self.assertIn(str(MEDIA), command)
        self.assertIn("--worker-config", command)
        self.observations.append(dict(label="owned_worker_fault_injection", pid=pid, parent_pid=int(parent),
                                      generation=worker["generation"], signal="SIGTERM", observed_at=time.time()))
        os.kill(pid, signal.SIGTERM)
        self.expected_generation, self.expected_restarts = 2, 1
        bye, peer = self.receive_sip(self.a, lambda m: m[0].startswith("BYE "))
        self.send_sip(self.a, e2e.response(bye, port=self.a.getsockname()[1]), peer)
        self.final_status = self.observe("fresh_final_zero_after_worker_fault", self.zero_status(worker.get("admission", {}).get("sampled_at")))
        self.assertNotEqual(self.final_status["workers"][0]["pid"], pid)
        self.assert_media_ports_free()
        self.control_op(5, handle, expected=(4, 8))
        self.push(handle, pcm_oracle.pcm_samples(1), 3200, expected=(4, 8))
        self.begin(channel=old_channel, expected=3)
        self.pump(.05)
        self.local_call()
        new, _ = self.begin()
        samples = pcm_oracle.pcm_samples(5, 7)
        self.feed(new, samples)
        self.control_op(3, new, offset=800)
        self.final(new)
        self.assert_pcm(self.audio(), samples)
        self.finish()

    def test_deterministic_offset_queue_rejections_preserve_input_and_cross_connection_token(self):
        self.local_call()
        handle, _ = self.begin(buffer=100, prebuffer=100)
        # 结构错误关闭该数据连接；完整媒体句柄仍未收到任何样本，新数据连接可继续使用。
        self.push(handle, [1] * 159, expected=1)
        self.data = StreamPeer(self, 2)
        self.assertEqual(self.control_op(5, handle)["accepted_samples"], 0)
        samples = pcm_oracle.pcm_samples(5, 5)
        self.push(handle, samples[:640])
        full = self.push(handle, samples[:320], 640, expected=5)
        self.assertEqual((full["accepted_samples"], full["queued_samples"]), (640, 640))
        offset = self.push(handle, samples[:160], 0, expected=3)
        self.assertEqual(offset["accepted_samples"], 640)
        self.control_op(3, handle, offset=800, expected=3)
        status = self.control_op(5, handle)
        self.assertEqual((status["accepted_samples"], status["queued_samples"]), (640, 640))
        # 数据连接关闭不隐式撤销，另一同类连接用同一令牌续写，偏移必须依原真实计数。
        self.data.close()
        self.data = StreamPeer(self, 2)
        second_control = StreamPeer(self, 1)
        self.control_op(5, handle, peer=second_control)
        self.push(handle, samples[640:], 640)
        self.control_op(3, handle, offset=800, peer=second_control)
        self.final(handle)
        self.assert_pcm(self.audio(), samples)
        second_control.close()
        self.finish()

    def test_active_dtmf_and_pcm_interrupt_keep_shared_tx(self):
        self.local_call("PCMA", 8)
        handle, _ = self.begin(buffer=400, prebuffer=20)
        samples = pcm_oracle.pcm_samples(20, 4)
        self.feed(handle, samples)
        self.assertEqual(self.esl.api("uuid_send_dtmf " + self.channel + " 12#@55"),
                         "+OK " + self.channel + " sent DTMF 12#@55.\n")
        self.wait(lambda: len(self.audio()) >= 2)
        stop = self.control_op(4, handle)
        self.assertEqual(stop["state"], "stopped")
        self.pump(.60)
        self.assertEqual(len(self.audio()) * 160, stop["sent_samples"])
        self.assert_pcm(self.audio(), samples[:stop["sent_samples"]])
        count = assert_events(self, [p["raw"] for p in self.output], "12#", 55)
        status = json.loads(self.esl.api("uuid_send_dtmf_status " + self.channel))
        self.assertEqual((status["state"], status["completed_digits"], status["sent_packets"]), ("idle", 3, count))
        self.assert_timeline()
        self.finish()


def configure(control, media, artifacts, connected=False):
    """给CLI和独立收据执行器同一处设置实际路径，避免继承帮助方法误读旧默认二进制。"""
    global CONTROL, MEDIA, ARTIFACTS, CONNECTED
    CONTROL, MEDIA, ARTIFACTS, CONNECTED = Path(control).resolve(), Path(media).resolve(), Path(artifacts).resolve(), connected
    local.CONTROL, local.MEDIA, local.ARTIFACTS, local.CONNECTED = CONTROL, MEDIA, ARTIFACTS, CONNECTED


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--control", type=Path, required=True)
    parser.add_argument("--media", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--connected", action="store_true")
    args, remaining = parser.parse_known_args()
    configure(args.control, args.media, args.artifacts, args.connected)
    if not CONTROL.is_file() or not MEDIA.is_file():
        parser.error("实际候选二进制缺失")
    ARTIFACTS.mkdir(parents=True, exist_ok=False)
    unittest.main(argv=[__file__, *remaining], verbosity=2)


if __name__ == "__main__":
    main()
