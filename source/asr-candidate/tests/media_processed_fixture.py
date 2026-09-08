#!/usr/bin/env python3
"""M1 真实双腿媒体夹具：只启动自己的 Go/Rust 进程，不调用主服务。

G.711 参考输入从独立码字展开表选择最近量化值；不调用待测 Rust 编码器。
固定低端口范围与其他本机媒体回归顺序运行，绑定/启动失败必须失败而非跳过。
"""
import bisect
import hashlib
import json
import math
from pathlib import Path
import select
import signal
import socket
import struct
import subprocess
import tempfile
import time
import unittest
import urllib.request
import uuid

import e2e
import esl_e2e

ROOT = e2e.ROOT
CONTROL = ROOT.parents[1] / "work/voice-runtime-m1/bin/rustswitch"
MEDIA = ROOT.parents[1] / "work/voice-runtime-m1/bin/rustswitch-media"
CONNECTED = False
ARTIFACTS = None
STEP = 0.020
SAMPLES = 160
SOURCE_SSRC = {"a": 0xA1357135, "b": 0xB2468246}
SOURCE_SEQ = {"a": 31000, "b": 49000}
SOURCE_TS = {"a": 176000, "b": 912000}


def decode(value, law):
    """按 G.711 码字的符号、段号和量化区间独立展开，返回 8 kHz PCM 样本。"""
    if law == "PCMA":
        value ^= 0x55
        segment = (value >> 4) & 7
        magnitude = ((value & 15) << 4) + (8 if segment == 0 else 264)
        if segment > 1:
            magnitude <<= segment - 1
        return magnitude if value & 128 else -magnitude
    if law != "PCMU":
        raise ValueError("夹具只构造独立 G.711 输入")
    value = (~value) & 255
    magnitude = (((value & 15) << 3) + 132) * (1 << ((value >> 4) & 7)) - 132
    return -magnitude if value & 128 else magnitude


LEVELS = {law: sorted((decode(code, law), code) for code in range(256))
          for law in ("PCMU", "PCMA")}


def encode(sample, law):
    """用独立展开表的最近邻选择输入码字，不镜像生产编码器的分段/位操作实现。"""
    values = LEVELS[law]
    index = bisect.bisect_left(values, (sample, -1))
    candidates = values[max(0, index - 1):min(len(values), index + 1)]
    return min(candidates, key=lambda value: (abs(value[0] - sample), value[1]))[1]


def audio_frames(law, frequency, count=32):
    """每帧幅度/第二频率不同，避免固定静音、复制一帧或交换双腿仍蒙混通过。"""
    frames = []
    for frame in range(count):
        samples = []
        for offset in range(SAMPLES):
            at = frame * SAMPLES + offset
            sample = ((5000 + (frame % 11) * 173) * math.sin(2 * math.pi * frequency * at / 8000)
                      + 1100 * math.sin(2 * math.pi * (frequency + 137) * at / 8000))
            samples.append(encode(round(sample), law))
        frames.append(bytes(samples))
    return frames


def rtp(payload, sequence, timestamp, body, ssrc, marker=False):
    """只构造无扩展的测试 RTP；序号与时间戳按网络宽度显式回绕。"""
    return struct.pack("!BBHII", 0x80, payload | (0x80 if marker else 0), sequence & 0xFFFF,
                       timestamp & 0xFFFFFFFF, ssrc) + body


def unpack_rtp(raw):
    """独立解析输出包，扩展/CSRC/padding 若出现则明确检查其长度，不能错切 PCM。"""
    if len(raw) < 12 or raw[0] >> 6 != 2:
        raise AssertionError("非法 RTP 头")
    offset = 12 + 4 * (raw[0] & 15)
    if offset > len(raw):
        raise AssertionError("RTP CSRC 超过报文长度")
    if raw[0] & 0x10:
        if offset + 4 > len(raw):
            raise AssertionError("RTP 扩展头截断")
        offset += 4 + struct.unpack("!H", raw[offset + 2:offset + 4])[0] * 4
    end = len(raw)
    if raw[0] & 0x20:
        padding = raw[-1]
        if padding == 0 or padding > end - offset:
            raise AssertionError("RTP padding 非法")
        end -= padding
    if offset > end:
        raise AssertionError("RTP 载荷越界")
    seq, timestamp, ssrc = struct.unpack("!HII", raw[2:12])
    return dict(payload=raw[1] & 127, marker=bool(raw[1] & 128), sequence=seq,
                timestamp=timestamp, ssrc=ssrc, body=raw[offset:end])


def rtcp_source_report(ssrc, cname, timestamp, packets, octets):
    """构造端点自己的SR+SDES；保存LSR供验证候选实际接收报告，不复用候选报文生成器。"""
    now = time.time() + 2208988800
    seconds = int(now) & 0xFFFFFFFF
    fraction = int((now - int(now)) * (1 << 32)) & 0xFFFFFFFF
    sr = struct.pack("!BBHIIIIII", 0x80, 200, 6, ssrc, seconds, fraction, timestamp, packets, octets)
    name = cname.encode("ascii")
    chunk = struct.pack("!I", ssrc) + bytes([1, len(name)]) + name + b"\x00"
    chunk += bytes((-len(chunk)) % 4)
    sdes = struct.pack("!BBH", 0x81, 202, len(chunk) // 4) + chunk
    return sr + sdes, ((seconds & 0xFFFF) << 16) | (fraction >> 16)


def unpack_rtcp(raw):
    """解析本范围的复合SR/RR/SDES/BYE，长度和接收块全部独立验证。"""
    offset, parts = 0, []
    while offset < len(raw):
        if offset + 4 > len(raw):
            raise AssertionError("RTCP头截断")
        first, kind, words = struct.unpack("!BBH", raw[offset:offset + 4])
        length = (words + 1) * 4
        if first >> 6 != 2 or offset + length > len(raw):
            raise AssertionError("RTCP版本/长度非法")
        body = raw[offset + 4:offset + length]
        if first & 32:
            if offset + length != len(raw) or not body or body[-1] == 0 or body[-1] > len(body):
                raise AssertionError("RTCP padding非法")
            body = body[:-body[-1]]
        count = first & 31
        part = dict(kind=kind, count=count)
        if kind in (200, 201):
            minimum = 24 if kind == 200 else 4
            if len(body) < minimum + count * 24:
                raise AssertionError("RTCP SR/RR 接收块截断")
            part["ssrc"] = struct.unpack("!I", body[:4])[0]
            if kind == 200:
                part.update(zip(("ntp_seconds", "ntp_fraction", "rtp_timestamp", "packets", "octets"),
                                struct.unpack("!IIIII", body[4:24])))
            part["reports"] = []
            for index in range(count):
                block = body[minimum + index * 24:minimum + (index + 1) * 24]
                source, loss, highest, jitter, lsr, dlsr = struct.unpack("!IIIIII", block)
                cumulative = loss & 0xFFFFFF
                if cumulative & 0x800000:
                    cumulative -= 1 << 24
                part["reports"].append(dict(ssrc=source, fraction_lost=loss >> 24,
                                            cumulative_lost=cumulative, highest_sequence=highest,
                                            jitter=jitter, lsr=lsr, dlsr=dlsr))
        elif kind == 202:
            position = 0
            chunks = []
            for _ in range(count):
                if position + 4 > len(body):
                    raise AssertionError("SDES SSRC截断")
                source = struct.unpack("!I", body[position:position + 4])[0]
                position += 4
                items = []
                while True:
                    if position >= len(body):
                        raise AssertionError("SDES缺少END")
                    item = body[position]
                    position += 1
                    if item == 0:
                        position = (position + 3) // 4 * 4
                        break
                    if position >= len(body):
                        raise AssertionError("SDES项目长度截断")
                    size = body[position]
                    position += 1
                    if position + size > len(body):
                        raise AssertionError("SDES项目正文截断")
                    items.append((item, body[position:position + size]))
                    position += size
                chunks.append(dict(ssrc=source, items=items))
            part["chunks"] = chunks
        elif kind == 203:
            if count == 0 or len(body) < count * 4:
                raise AssertionError("BYE源列表非法")
            part["sources"] = list(struct.unpack("!" + "I" * count, body[:count * 4]))
        else:
            raise AssertionError(f"本场景输出未知RTCP类型{kind}")
        parts.append(part)
        offset += length
    if not parts or parts[0]["kind"] not in (200, 201):
        raise AssertionError("缺少复合RTCP首SR/RR")
    return parts


def description(law, payload, rtp_port, rtcp_port, *, auxiliary=True, ptime=20):
    """A/B 分别生成 SDP，不调用服务端协商器；可用于不支持输入的负例。"""
    extra = " 101 13" if auxiliary else ""
    clock = 16000 if law == "G722-bad-clock" else 8000
    name = "G722" if law == "G722-bad-clock" else law
    body = ("v=0\r\no=processed-fixture 1 1 IN IP4 127.0.0.1\r\ns=M1-independent\r\n"
            "c=IN IP4 127.0.0.1\r\nt=0 0\r\n"
            f"m=audio {rtp_port} RTP/AVP {payload}{extra}\r\n"
            f"a=rtcp:{rtcp_port} IN IP4 127.0.0.1\r\n"
            f"a=rtpmap:{payload} {name}/{clock}\r\na=ptime:{ptime}\r\n")
    if auxiliary:
        body += "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-15\r\na=rtpmap:13 CN/8000\r\n"
    return body.encode()


class ProcessedFixture(unittest.TestCase):
    """测试基类只提供夹具，不注册产品测试；所有等待、收包和原帧留存都有上限。"""
    processing = "g711"

    def setUp(self):
        self.resources = []
        self.process = None
        self.wire = []
        self.started = time.monotonic()
        self.tmp = tempfile.TemporaryDirectory(prefix="rustswitch-processed-e2e-")
        self.directory = Path(self.tmp.name)
        if ARTIFACTS is not None:
            self.directory = ARTIFACTS / (self.id().rsplit(".", 1)[-1] + "-" + uuid.uuid4().hex[:8])
            self.directory.mkdir(parents=True, exist_ok=False)
        self.addCleanup(self.close)
        self.assertTrue(CONTROL.is_file(), f"控制二进制不存在：{CONTROL}")
        self.assertTrue(MEDIA.is_file(), f"媒体二进制不存在：{MEDIA}")
        self.binary_hashes = {"control": hashlib.sha256(CONTROL.read_bytes()).hexdigest(),
                              "media": hashlib.sha256(MEDIA.read_bytes()).hexdigest()}
        self.upstream, self.a, self.ar, self.ac, self.br, self.bc = [self.sock() for _ in range(6)]
        probe = self.sock()
        self.address = probe.getsockname()
        probe.close()
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            self.port = probe.getsockname()[1]
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            self.esl_port = probe.getsockname()[1]
        self.base = self.free_media_block()
        config = json.loads((ROOT / "config/local.json").read_text())
        config["sip"].update(listen=f"127.0.0.1:{self.address[1]}", advertise=f"127.0.0.1:{self.address[1]}",
                             upstream=f"127.0.0.1:{self.upstream.getsockname()[1]}", ack_timeout_ms=1500)
        config["media"].update(binary=str(MEDIA), workers=1, connect_sockets=CONNECTED,
                               port_start=self.base, port_end=self.base + 3, processing=self.processing,
                               port_reuse_delay_ms=0, adaptive_admission=False,
                               max_packets_per_second_per_leg=500)
        config["limits"].update(max_calls=1, calls_per_second=10, burst_calls=10)
        config["admin"]["listen"] = f"127.0.0.1:{self.port}"
        config["journal"]["path"] = str(self.directory / "events.jsonl")
        self.config = config
        conf = self.directory / "config.json"
        conf.write_text(json.dumps(config, ensure_ascii=False, indent=2) + "\n")
        secret = self.directory / "esl.secret"
        password = uuid.uuid4().hex
        secret.write_text(password + "\n")
        secret.chmod(0o600)
        self.log = open(self.directory / "server.log", "w+")
        self.process = subprocess.Popen([str(CONTROL), "-config", str(conf), "-esl-listen",
                                         f"127.0.0.1:{self.esl_port}", "-esl-password-file", str(secret)], cwd=ROOT,
                                        stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                self.fail("控制/媒体启动失败：" + self.log_tail())
            try:
                if self.get("/readyz")["ready"]:
                    self.esl = esl_e2e.ESLPeer(self.esl_port, password)
                    head, _ = self.esl.command("event json DTMF")
                    self.assertEqual(head["reply-text"], "+OK event listener enabled json")
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.03)
        self.fail("真实服务未就绪；缺能力不能跳过：" + self.log_tail())

    def sock(self):
        """从明确测试范围寻找 UDP 端点，不使用主服务媒体区或临时 UDP 高端口。"""
        for port in range(6800, 7000):
            sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            try:
                sock.bind(("127.0.0.1", port))
            except OSError as error:
                sock.close()
                if error.errno not in (48, 98):
                    raise
                continue
            sock.settimeout(2)
            self.resources.append(sock)
            return sock
        raise RuntimeError("隔离测试 UDP 端口不足")

    def free_media_block(self):
        """按四端口预检独立媒体范围；仅一个槽位确保回收负例真实复用。"""
        for base in range(7200, 7997, 4):
            probes = []
            try:
                for port in range(base, base + 4):
                    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                    probes.append(sock)
                    sock.bind(("127.0.0.1", port))
                return base
            except OSError as error:
                if error.errno not in (48, 98):
                    raise
            finally:
                for sock in probes:
                    sock.close()
        raise RuntimeError("没有独立测试媒体块")

    def log_tail(self):
        if not hasattr(self, "log"):
            return ""
        self.log.flush()
        self.log.seek(0, 2)
        self.log.seek(max(0, self.log.tell() - 16384))
        return self.log.read()

    def close(self):
        """只关闭本例持有的进程/socket；失败仍落原帧，不查杀同名或主服务进程。"""
        if hasattr(self, "esl"):
            self.esl.close()
        if self.process is not None and self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(1)
            except subprocess.TimeoutExpired:
                self.process.send_signal(signal.SIGTERM)
                try:
                    self.process.wait(3)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(2)
        for sock in self.resources:
            sock.close()
        if hasattr(self, "log"):
            self.log.close()
        if ARTIFACTS is not None:
            after = {"control": hashlib.sha256(CONTROL.read_bytes()).hexdigest() if CONTROL.is_file() else None,
                     "media": hashlib.sha256(MEDIA.read_bytes()).hexdigest() if MEDIA.is_file() else None}
            manifest = dict(test=self.id(), connected=CONNECTED, processing=self.processing, wire=self.wire,
                            binaries_before=getattr(self, "binary_hashes", None), binaries_after=after,
                            binaries_unchanged=getattr(self, "binary_hashes", None) == after,
                            final_status=getattr(self, "final_status", None),
                            rejection_statuses=getattr(self, "rejection_statuses", None),
                            scope="真实本地SIP与G711处理正确性；不是原版差分或容量认证")
            (self.directory / "wire.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
            (self.directory / "esl.secret").unlink(missing_ok=True)
        self.tmp.cleanup()

    def record(self, direction, data, peer):
        self.assertLess(len(self.wire), 4096, "原帧记录超过本用例预算")
        self.wire.append(dict(at_ms=round((time.monotonic() - self.started) * 1000, 3),
                              direction=direction, peer=list(peer), hex=data.hex()))

    def get(self, path):
        with urllib.request.urlopen(f"http://127.0.0.1:{self.port}{path}", timeout=1) as reply:
            return json.load(reply)

    def send_sip(self, sock, raw, target):
        self.record("sip_send", raw, target)
        self.assertEqual(sock.sendto(raw, target), len(raw))

    def receive_sip(self, sock, predicate, timeout=3):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            sock.settimeout(max(0.001, deadline - time.monotonic()))
            raw, peer = sock.recvfrom(65536)
            self.record("sip_receive", raw, peer)
            if predicate(e2e.parse(raw)):
                return raw, peer
        self.fail("预期 SIP 报文未到达")

    def offer(self, law="PCMU", payload=0, *, ptime=20, auxiliary=True):
        body = description(law, payload, self.ar.getsockname()[1], self.ac.getsockname()[1],
                           ptime=ptime, auxiliary=auxiliary)
        wire = e2e.message("INVITE", "sip:100@local", self.a.getsockname()[1], uuid.uuid4().hex,
                           "z9hG4bK" + uuid.uuid4().hex, body)
        self.send_sip(self.a, wire, self.address)
        return wire

    def establish(self, a_law="PCMU", a_payload=0, b_law="PCMA", b_payload=8, auxiliary=True):
        """B 独立选择与 A 不同的编码；旧透明转发会拒绝或在后续内容断言失败。"""
        self.offer(a_law, a_payload, auxiliary=auxiliary)
        outgoing, peer = self.receive_sip(self.upstream, lambda item: item[0].startswith("INVITE "))
        outgoing_sdp = e2e.parse(outgoing)[2]
        line = next(value for value in outgoing_sdp.decode().splitlines() if value.startswith("m=audio "))
        self.assertTrue({0, 8}.issubset(set(map(int, line.split()[3:]))), outgoing_sdp)
        self.assertIn(b"a=rtpmap:0 PCMU/8000", outgoing_sdp)
        self.assertIn(b"a=rtpmap:8 PCMA/8000", outgoing_sdp)
        answer = e2e.response(outgoing, body=description(b_law, b_payload, self.br.getsockname()[1],
                                                        self.bc.getsockname()[1], auxiliary=auxiliary),
                              port=self.upstream.getsockname()[1])
        self.send_sip(self.upstream, answer, peer)
        self.receive_sip(self.upstream, lambda item: item[0].startswith("ACK "))
        accepted, _ = self.receive_sip(self.a, lambda item: item[0].startswith("SIP/2.0 200")
                                      and item[1]["cseq"].endswith("INVITE"))
        body = e2e.parse(accepted)[2]
        self.assertIn(f"a=rtpmap:{a_payload} {a_law}/8000".encode(), body)
        self.assertEqual(next(value for value in body.decode().splitlines() if value.startswith("m=audio ")).split()[3], str(a_payload))
        header = e2e.parse(accepted)[1]
        self.send_sip(self.a, e2e.message("ACK", "sip:rustswitch@local", self.a.getsockname()[1],
                                        header["call-id"], "z9hG4bK" + uuid.uuid4().hex, to=header["to"]), self.address)
        self.a_ports = e2e.media_ports(body)
        self.b_ports = e2e.media_ports(outgoing_sdp)
        self.accepted = accepted
        self.codecs = {"a": (a_law, a_payload), "b": (b_law, b_payload)}
        self.wait_status(lambda state: state["established_calls"] == 1)
        return outgoing, accepted

    def wait_status(self, predicate, timeout=4, require_stats=False):
        deadline = time.monotonic() + timeout
        state = None
        while time.monotonic() < deadline:
            state = self.get("/v1/status")
            # worker统计有独立采样周期；首次空快照表示未知，等待实际字段，绝不填零。
            if require_stats and not all("active_calls" in worker["stats"]
                                         and "processed_active_calls" in worker["stats"] for worker in state["workers"]):
                time.sleep(0.03)
                continue
            if predicate(state):
                return state
            time.sleep(0.03)
        self.fail(f"真实状态没有收敛：{state}")

    def stats(self):
        """每例只有一worker，字段不存在必须失败，不把未实现指标填0。"""
        workers = self.get("/v1/status")["workers"]
        self.assertEqual(len(workers), 1)
        return workers[0]["stats"]

    def hangup(self):
        before = {worker["id"]: worker.get("admission", {}).get("sampled_at")
                  for worker in self.get("/v1/status")["workers"]}
        header = e2e.parse(self.accepted)[1]
        self.send_sip(self.a, e2e.message("BYE", "sip:rustswitch@local", self.a.getsockname()[1],
                                        header["call-id"], "z9hG4bK" + uuid.uuid4().hex,
                                        to=header["to"], cseq=2), self.address)
        self.receive_sip(self.a, lambda item: item[0].startswith("SIP/2.0 200") and item[1]["cseq"].endswith("BYE"))
        bye, peer = self.receive_sip(self.upstream, lambda item: item[0].startswith("BYE "))
        self.send_sip(self.upstream, e2e.response(bye, port=self.upstream.getsockname()[1]), peer)
        state = self.wait_status(lambda item: item["active_calls"] == 0 and item["established_calls"] == 0
                                 and all(worker["stats"]["active_calls"] == 0
                                         and worker["stats"]["processed_active_calls"] == 0
                                         and worker.get("admission", {}).get("sampled_at") not in (None, before[worker["id"]])
                                         for worker in item["workers"])
                                 and item["controller"]["media_results_queue_length"] == 0, require_stats=True)
        self.final_status = state
        # 真实重新绑定四个媒体端口，防止只有计数归零但worker仍持有UDP描述符。
        probes = []
        try:
            for port in (*self.a_ports, *self.b_ports):
                probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                probes.append(probe)
                probe.bind(("127.0.0.1", port))
        finally:
            for probe in probes:
                probe.close()
        return state["workers"][0]["stats"]

    def audio_events(self, side, frames, *, omitted=(), offsets=None, sequence_offset=0, frame_offset=0):
        """给定明确发送时刻和帧索引，故障注入不靠随机丢包，所有原包可重建。"""
        payload = self.codecs[side][1]
        events = []
        for index, body in enumerate(frames):
            if index in omitted:
                continue
            absolute = index + frame_offset
            raw = rtp(payload, SOURCE_SEQ[side] + absolute + sequence_offset,
                      SOURCE_TS[side] + absolute * SAMPLES, body, SOURCE_SSRC[side], absolute == 0)
            events.append((offsets.get(index, index * STEP) if offsets else index * STEP, side, raw))
        return events

    def exchange(self, events, *, tail=0.16, capture_rtcp=False, start_at=None, actions=()):
        """一个有界select循环同时发两方向并持续收包，不另起每方向线程或漏收尾部。"""
        events = sorted(events, key=lambda event: event[0])
        actions = sorted(actions, key=lambda action: action[0])
        self.assertTrue(events)
        self.assertLessEqual(len(actions), 8)
        self.assertTrue(all(0 <= at <= events[-1][0] for at, _ in actions), "进程故障动作必须位于有界发包窗口内")
        self.assertLessEqual(len(events), 1000)
        self.assertLessEqual(events[-1][0] + tail, 9)
        began = time.monotonic() + 0.025 if start_at is None else start_at
        self.last_exchange_origin = began
        deadline = began + events[-1][0] + tail
        received = {"a": [], "b": []}
        receivers = {self.ar: "a", self.br: "b"}
        senders = {"a": (self.ar, self.a_ports[0]), "b": (self.br, self.b_ports[0])}
        if capture_rtcp:
            receivers.update({self.ac: "a_rtcp", self.bc: "b_rtcp"})
            senders.update({"a_rtcp": (self.ac, self.a_ports[1]), "b_rtcp": (self.bc, self.b_ports[1])})
            received.update({"a_rtcp": [], "b_rtcp": []})
        for sock in receivers:
            sock.setblocking(False)
        cursor, action_cursor = 0, 0
        # 短尾窗可能在 select 返回后已经结束，但最后一组已到期输入仍须按原50ms迟到上限检查。
        # 不延长接收期限、不补造发送成功；超过原有调度上限仍立即失败。
        while cursor < len(events) or action_cursor < len(actions) or time.monotonic() < deadline:
            now = time.monotonic()
            while action_cursor < len(actions) and began + actions[action_cursor][0] <= now:
                planned, action = actions[action_cursor]
                self.assertLess(now - began - planned, 0.050, "测试故障注入调度迟到")
                action()
                action_cursor += 1
                now = time.monotonic()
            while cursor < len(events) and began + events[cursor][0] <= now:
                planned, side, raw = events[cursor]
                self.assertLess(now - began - planned, 0.050, "测试发生器调度迟到，不能伪称稳定输入")
                sock, port = senders[side]
                target = ("127.0.0.1", port)
                self.assertEqual(sock.sendto(raw, target), len(raw))
                self.record("rtp_send_" + side, raw, target)
                cursor += 1
            next_at = began + events[cursor][0] if cursor < len(events) else deadline
            if action_cursor < len(actions):
                next_at = min(next_at, began + actions[action_cursor][0])
            ready, _, _ = select.select(list(receivers), [], [], max(0, min(0.020, next_at - time.monotonic(), deadline - time.monotonic())))
            for sock in ready:
                # 每轮收包预算固定，不让一端异常输出饿死发送时钟。
                for _ in range(64):
                    try:
                        raw, peer = sock.recvfrom(4096)
                    except BlockingIOError:
                        break
                    side = receivers[sock]
                    self.record("rtp_receive_" + side, raw, peer)
                    packet = dict(parts=unpack_rtcp(raw)) if side.endswith("_rtcp") else unpack_rtp(raw)
                    packet.update(raw=raw, peer=peer, at=time.monotonic() - began)
                    received[side].append(packet)
                    self.assertLessEqual(len(received[side]), 512, "媒体输出超过有限测试预算")
        self.assertEqual(cursor, len(events), "发生器未发送完整输入")
        self.assertEqual(action_cursor, len(actions), "故障动作未完整执行")
        for sock in receivers:
            sock.settimeout(2)
        return received

    def assert_timeline(self, packets, side):
        """混合音频/DTMF/CN的发包也必须共用连续TX序号和新SSRC，不能退回透明转发。"""
        self.assertTrue(packets, f"{side}没有实际收到媒体")
        source = "b" if side == "a" else "a"
        expected_port = self.a_ports[0] if side == "a" else self.b_ports[0]
        self.assertTrue(all(packet["peer"] == ("127.0.0.1", expected_port) for packet in packets))
        self.assertEqual(len({packet["ssrc"] for packet in packets}), 1)
        self.assertNotIn(packets[0]["ssrc"], set(SOURCE_SSRC.values()), "媒体仍使用输入SSRC，未终结处理")
        # 随机的16位首序号可能恰好等于源序号，不能以1/65536概率误报。
        # 新SSRC、连续TX序号及逐样本异律内容共同验证RTP终结，不能只看首序号是否不同。
        for first, second in zip(packets, packets[1:]):
            self.assertEqual((second["sequence"] - first["sequence"]) & 0xFFFF, 1,
                             "输出丢包、重复或未统一辅助媒体序号")

    def assert_audio(self, packets, side, inputs, *, indexes=None):
        """独立解码接收端格式并逐帧对照源PCM，PT改写但载荷透传必须失败。"""
        target_law, target_pt = self.codecs[side]
        source_law = self.codecs["b" if side == "a" else "a"][0]
        audio = [packet for packet in packets if packet["payload"] == target_pt]
        indexes = list(range(len(inputs))) if indexes is None else list(indexes)
        self.assertGreaterEqual(len(audio), len(indexes), "真实PCM输出不足，未处理全部已发音频")
        self.assertLessEqual(len(audio), len(indexes) + 8, "停止输入后输出未按预算结束")
        expected = [inputs[index] for index in indexes]
        for ordinal, (packet, original) in enumerate(zip(audio, expected)):
            self.assertEqual(len(packet["body"]), SAMPLES)
            decoded = [decode(value, target_law) for value in packet["body"]]
            source = [decode(value, source_law) for value in original]
            energy = sum(value * value for value in source)
            error = sum((first - second) ** 2 for first, second in zip(decoded, source))
            self.assertGreater(energy, 1_000_000, "夹具输入意外成为静音")
            self.assertLess(error / energy, 0.003, f"第{ordinal}帧不是源音频的目标G711量化结果")
        for ordinal, (first, second) in enumerate(zip(audio, audio[1:])):
            if ordinal + 1 < len(indexes):
                step = (indexes[ordinal + 1] - indexes[ordinal]) * SAMPLES
                self.assertEqual((second["timestamp"] - first["timestamp"]) & 0xFFFFFFFF, step)
        return audio
