#!/usr/bin/env python3
"""真实Rust A腿RX→FD4上行验收；只启动隔离Worker，不访问主9080。

独立解析RXS2与G.711参考码字；控制状态、上行原帧及真实RTP/RTCP同时留证。
本文件不提供SIP ACK鉴权、Go ASR消费者、供应商或并发容量通过结论。
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

import media_interaction_e2e as endpoints
import media_pcm_turn_e2e as pcm_fixture
import media_processed_fixture as oracle
from dtmf_send_e2e import assert_events

MEDIA = None
ARTIFACTS = None
CONNECTED = False
HEADER = 168
MAX_BODY = 480
OBSERVATION_BUDGET_NS = 100_000_000
STEP = .020
SOURCE = 0x52585031
KINDS = {1: "decoded", 2: "history_plc", 3: "missing", 4: "comfort_noise",
         5: "local_expired", 6: "auxiliary_expired", 7: "source_boundary",
         8: "inactive_suspended", 9: "observation_failed", 10: "export_gap",
         11: "end", 12: "export_failed", 13: "auxiliary"}
LAUNCHER = '''import os,sys
fd=int(sys.argv[1])
if fd!=4:
    os.dup2(fd,4,inheritable=True)
    os.close(fd)
else:
    os.set_inheritable(4,True)
os.execv(sys.argv[2],[sys.argv[2],"--worker-config",sys.argv[3]])
'''


def digest(path):
    """文件流式指纹，不一次读入媒体二进制。"""
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            value.update(block)
    return value.hexdigest()


def shared_clock():
    """读取协议规定的同机时钟，不能用墙钟或不同暂停语义的时钟替代。"""
    if sys.platform == "darwin":
        domain, clock = 2, time.CLOCK_UPTIME_RAW
    elif sys.platform.startswith("linux"):
        domain, clock = 1, time.CLOCK_MONOTONIC
    else:
        raise RuntimeError("RXS2验收只支持明确共享时钟语义的Linux/macOS")
    return domain, time.clock_gettime_ns(clock)


def parse_record(raw, received_clock=None):
    """按168字节头独立展开；旧协议、无效期限和时钟域不能降级通过。"""
    if len(raw) < HEADER or raw[:4] != b"RXS2" or raw[5] or any(raw[126:128]):
        raise AssertionError("RXS2头长度、魔数或保留位错误")
    kind, flags = raw[4], struct.unpack_from("<H", raw, 6)[0]
    if kind not in KINDS or flags & ~0x3ff:
        raise AssertionError("RXS2类型或标志越界")
    names = ("session", "subscription_id", "event_seq", "source_generation", "source_segment",
             "rtp_timestamp", "rtp_sequence", "media_time_ns")
    value = dict(zip(names, struct.unpack_from("<8Q", raw, 8)))
    ssrc, sample_rate, clock = struct.unpack_from("<III", raw, 72)
    body_bytes, sample_count = struct.unpack_from("<HH", raw, 84)
    first, last = struct.unpack_from("<QQ", raw, 88)
    gap_decoded, gap_plc = struct.unpack_from("<II", raw, 104)
    duration = struct.unpack_from("<Q", raw, 112)[0]
    age, reason = struct.unpack_from("<IH", raw, 120)
    discarded = struct.unpack_from("<Q", raw, 128)[0]
    observed_age, arrival_age, deadline_late = struct.unpack_from("<III", raw, 136)
    boundary, cn_applied = raw[148], raw[149]
    clock_domain, lower_ns, expires_ns = struct.unpack_from("<HQQ", raw, 150)
    local_domain, received_ns = shared_clock() if received_clock is None else received_clock
    if clock_domain != local_domain:
        raise AssertionError("RXS2共享单调时钟域不匹配")
    if kind in (10, 11, 12):
        if lower_ns or expires_ns:
            raise AssertionError("Gap/End/ExportFailed不能伪造可恢复音频的期限")
        expired = None
    else:
        if not 0 < lower_ns <= (1 << 64) - 1 - OBSERVATION_BUDGET_NS:
            raise AssertionError("观察时钟下界为零或期限加法溢出")
        if expires_ns != lower_ns + OBSERVATION_BUDGET_NS or lower_ns > received_ns:
            raise AssertionError("观察期限不等于原下界加100ms，或来源时刻在未来")
        if received_ns - lower_ns < observed_age * 1_000_000:
            raise AssertionError("绝对观察下界比发送方保留的实际观察年龄更新，疑似重试续期")
        # 本进程是保留原帧的故障注入记录器，不是Go SDK消费者。
        # 故意慢读的OS旧帧仍留证并明确标过期，不能据此宣称SDK已交付新鲜音频。
        expired = received_ns >= expires_ns
    if sample_rate != 8000 or clock != 8000 or body_bytes > MAX_BODY or len(raw) != HEADER + body_bytes:
        raise AssertionError("RXS2格式、正文上限或数据报长度错误")
    if not value["session"] or not value["subscription_id"] or boundary > 6 or cn_applied > 1:
        raise AssertionError("RXS2身份或来源边界非法")
    body = raw[HEADER:]
    if kind in (1, 2):
        if sample_count != 160 or body_bytes != 320 or duration != 160:
            raise AssertionError("真实PCM/PLC必须完整160样本和160时钟刻度")
    elif kind == 4:
        if sample_count or not 1 <= body_bytes <= MAX_BODY or duration:
            raise AssertionError("CN必须保留SID且不能伪造PCM或20ms音频区间")
    elif sample_count or body_bytes:
        raise AssertionError("非音频记录不能携带PCM")
    for bit, number in ((0, ssrc), (1, value["rtp_timestamp"]), (2, value["rtp_sequence"]),
                        (3, value["media_time_ns"]), (8, arrival_age), (9, deadline_late)):
        if not flags & (1 << bit) and number:
            raise AssertionError("未声明有效的RXS2位置/年龄必须为零")
    if bool(flags & 16) != bool(boundary):
        raise AssertionError("来源边界标志与扩展枚举不一致")
    if kind == 2 and flags & 4:
        raise AssertionError("PLC没有真实对应RTP包序，不能伪造有效位")
    if kind == 10:
        if not 0 < first <= last == value["event_seq"] or gap_decoded + gap_plc > last - first + 1:
            raise AssertionError("ExportGap范围或音频条数错误")
        if not reason or reason & ~7 or flags & 15 or duration:
            raise AssertionError("ExportGap原因或来源位置错误")
    elif first or last or gap_decoded or gap_plc:
        raise AssertionError("普通观察不能带ExportGap字段")
    if kind == 11 and reason != 0 or kind == 12 and reason != 1:
        raise AssertionError("终止摘要原因编号错误")
    if kind not in (10, 11, 12) and (not value["event_seq"] or reason > 11):
        raise AssertionError("生产观察序号或原因枚举错误")
    if kind in (6, 7, 8, 13) and duration:
        raise AssertionError("辅助或边界不能猜测音频缺口时长")
    value.update(kind=kind, kind_name=KINDS[kind], flags=flags, ssrc=ssrc,
                 sample_rate=sample_rate, rtp_clock_rate=clock, sample_count=sample_count,
                 body=body, samples=struct.unpack("<160h", body) if kind in (1, 2) else (),
                 gap_first_seq=first, gap_last_seq=last, gap_decoded_events=gap_decoded,
                 gap_plc_events=gap_plc, known_duration_ticks=duration, queue_age_ms=age,
                 reason=reason, discarded_packets=discarded, observation_age_ms=observed_age,
                 arrival_age_ms=arrival_age, deadline_lateness_ms=deadline_late,
                 boundary=boundary, cn_applied=bool(cn_applied), suspended_after=bool(flags & 64),
                 clock_domain=clock_domain, observation_lower_bound_ns=lower_ns,
                 expires_at_ns=expires_ns, received_clock_ns=received_ns, expired_at_recording=expired)
    return value


def code_frames(count, seed=1):
    """确定原码字覆盖符号/分段/全幅端点，每帧内容不同，不借用生产编码器。"""
    return [bytes((sample * 29 + frame * 17 + seed * 11) & 255 for sample in range(160))
            for frame in range(count)]


def decay(samples):
    """历史PLC按有符号乘3/4逐帧向零截断，和G.711量化容差分开。"""
    return tuple((abs(sample) * 3 // 4) * (-1 if sample < 0 else 1) for sample in samples)


class RxStreamFixture(unittest.TestCase):
    """复用已有隔离端口/JSON/RTP助手；只增加FD4采集，旧夹具保持原样。"""
    sock = pcm_fixture.PcmTurnFixture.sock
    record = pcm_fixture.PcmTurnFixture.record
    wait = pcm_fixture.PcmTurnFixture.wait
    tick = pcm_fixture.PcmTurnFixture.tick
    send_network = pcm_fixture.PcmTurnFixture.send_network
    command = pcm_fixture.PcmTurnFixture.command
    allocate = pcm_fixture.PcmTurnFixture.allocate
    assert_identity = pcm_fixture.PcmTurnFixture.assert_identity
    assert_pcm = pcm_fixture.PcmTurnFixture.assert_pcm

    def setUp(self):
        self.resources, self.wire, self.rtp, self.rtcp, self.rx_records = [], [], [], [], []
        self.responses, self.observations = {}, []
        self.stdout_buffer = bytearray()
        self.sequence = self.wire_bytes = 0
        self.started = time.monotonic()
        self.process = self.log = None
        self.ready = None
        self.allocated = set()
        self.read_rx = True
        self.directory = ARTIFACTS / (self.id().rsplit(".", 1)[-1] + "-" + uuid.uuid4().hex[:8])
        self.directory.mkdir(parents=True, exist_ok=False)
        self.addCleanup(self.close)
        self.binary_before = digest(MEDIA)
        self.source_paths = [Path(__file__), Path(pcm_fixture.__file__), Path(endpoints.__file__),
                             Path(oracle.__file__), Path(__file__).with_name("dtmf_send_e2e.py")]
        self.sources_before = {p.name: digest(p) for p in self.source_paths}
        self.ar, self.ac, self.br, self.bc, self.stranger = [self.sock() for _ in range(5)]
        self.base = endpoints.free_block()
        self.rx, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_DGRAM)
        self.resources += [self.rx, child]
        self.rx.setblocking(False)
        self.rx_lane = "without_rx_fd" not in self.id()
        small = "slow_reader" in self.id() or "blackhole" in self.id()
        if small:
            self.rx.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 1024)
            child.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 1024)
        self.socket_budget = {"small_buffer_requested": small,
                              "reader_receive_bytes": self.rx.getsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF),
                              "writer_send_bytes": child.getsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF)}
        config = dict(worker_id=0, bind_ip="127.0.0.1", port_start=self.base, port_end=self.base + 3,
                      max_calls=1, receive_buffer_bytes=262144, port_reuse_delay_ms=0,
                      max_packets_per_second_per_leg=1000, allowed_remote_networks=["127.0.0.0/8"],
                      cpu_core=None, connect_sockets=CONNECTED)
        (self.directory / "worker-config.json").write_text(json.dumps(config, indent=2) + "\n")
        (self.directory / "launcher.py").write_text(LAUNCHER)
        self.fixture_before = {p.name: digest(p) for p in self.directory.iterdir() if p.is_file()}
        self.log = (self.directory / "worker.log").open("wb")
        env = dict(os.environ)
        env.pop("RUSTSWITCH_PCM_FD", None)
        env.pop("RUSTSWITCH_RX_FD", None)
        command = [str(MEDIA), "--worker-config", json.dumps(config)]
        options = {}
        if self.rx_lane:
            env["RUSTSWITCH_RX_FD"] = "4"
            command = [sys.executable, str(self.directory / "launcher.py"), str(child.fileno()), str(MEDIA), json.dumps(config)]
            options["pass_fds"] = (child.fileno(),)
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.log,
                                        env=env, bufsize=0, **options)
        child.close()
        os.set_blocking(self.process.stdout.fileno(), False)
        self.wait(lambda: self.ready is not None, 5)
        self.assertEqual((self.ready["id"], self.ready["type"], self.ready["protocol_version"], self.ready["worker_id"]),
                         (0, "ready", 1, 0))
        self.assertEqual(self.ready["pid"], self.process.pid)
        self.assertIn("processed_g711_local_v1", self.ready["capabilities"])
        self.assertEqual("rx_g711_local_v2" in self.ready["capabilities"], self.rx_lane)
        self.assertNotIn("rx_g711_local_v1", self.ready["capabilities"], "RXS2不能宣告旧wire能力")
        self.assertNotIn("pcm_turn_v1", self.ready["capabilities"], "未继承FD3不能冒充下行数据就绪")

    def poll(self, timeout):
        """同时处理真实JSON/上行/音频，故障例只暂停FD4，不能饿死媒体接收夹具。"""
        sources = {self.ar: "rtp", self.ac: "rtcp", self.br: "unexpected_b_rtp",
                   self.bc: "unexpected_b_rtcp", self.stranger: "unexpected_source",
                   self.process.stdout: "json_reply"}
        if self.rx_lane and self.read_rx:
            sources[self.rx] = "rx"
        readable, _, _ = select.select(list(sources), [], [], max(0, min(timeout, .020)))
        for source in readable:
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
                    raw, peer = source.recvfrom(HEADER + MAX_BODY + 1 if kind == "rx" else 8192)
                except BlockingIOError:
                    break
                received_clock = shared_clock() if kind == "rx" else None
                metadata = {} if received_clock is None else dict(
                    received_clock_domain=received_clock[0], received_clock_ns=received_clock[1])
                self.record(kind + "_receive", raw, peer=list(peer) if isinstance(peer, tuple) else str(peer), **metadata)
                self.assertFalse(kind.startswith("unexpected"), "单腿媒体发往未协商对端")
                if kind == "rx":
                    value = parse_record(raw, received_clock)
                    value["at"] = time.monotonic()
                    self.rx_records.append(value)
                    self.observations.append(dict(op="rx_absolute_deadline_observed",
                        session=value["session"], subscription_id=value["subscription_id"],
                        event_seq=value["event_seq"], kind=value["kind"], clock_domain=value["clock_domain"],
                        observation_lower_bound_ns=value["observation_lower_bound_ns"],
                        expires_at_ns=value["expires_at_ns"], received_clock_ns=value["received_clock_ns"],
                        expired_at_recording=value["expired_at_recording"]))
                elif kind == "rtp":
                    self.assertEqual(peer, ("127.0.0.1", self.base))
                    value = oracle.unpack_rtp(raw)
                    value.update(raw=raw, at=time.monotonic())
                    self.rtp.append(value)
                else:
                    self.assertEqual(peer, ("127.0.0.1", self.base + 1))
                    self.rtcp.append(dict(raw=raw, parts=oracle.unpack_rtcp(raw), at=time.monotonic()))

    def rx_command(self, op, subscription=1, expected=True, session=None):
        """JSON字段逐项必需，不能把缺计数或null默认为空流。"""
        session = self.session if session is None else session
        result = self.command("rx_" + op, session=session, subscription_id=subscription)
        self.assertIs(result["ok"], expected, result)
        if not expected:
            self.assertEqual(result["type"], "error")
            return result
        self.assertEqual((result["type"], result["session"], result["subscription_id"]),
                         ("rx_state", session, subscription))
        self.check_status(result)
        self.observations.append(dict(op="rx_" + op, at=time.time(), result=result))
        return result

    def release(self):
        """沿用真实四端口复绑，再核对本批新增订阅/队列/观察存储已归零。"""
        stats = pcm_fixture.PcmTurnFixture.release(self)
        for key in ("rx_active_subscriptions", "rx_queued_events", "rx_observation_storage_bytes", "rx_observation_failed"):
            self.assertIn(key, stats)
            self.assertIs(type(stats[key]), int)
            self.assertEqual(stats[key], 0)
        return stats

    def check_status(self, result):
        """生产观察守恒；没有queued_samples字段时只计算可证明的剩余范围。"""
        keys = ("produced_events", "submitted_events", "dropped_events", "queued_events",
                "produced_samples", "submitted_samples", "dropped_samples", "oldest_age_ms")
        for key in keys:
            self.assertIn(key, result)
            self.assertIs(type(result[key]), int)
            self.assertGreaterEqual(result[key], 0)
            self.assertLessEqual(result[key], (1 << 64) - 1)
        self.assertIn(result["state"], ("active", "stopped", "failed"))
        self.assertIs(type(result["error"]), str)
        self.assertEqual(bool(result["error"]), result["state"] == "failed")
        self.assertEqual(result["produced_events"], result["submitted_events"] + result["dropped_events"] + result["queued_events"])
        self.assertLessEqual(result["queued_events"], 8)
        self.assertLessEqual(result["produced_events"], 4096, "超过本用例固定观察预算，不能展开无界缺口")
        remainder = result["produced_samples"] - result["submitted_samples"] - result["dropped_samples"]
        self.assertGreaterEqual(remainder, 0)
        self.assertLessEqual(remainder, result["queued_events"] * 160)
        self.assertEqual(remainder % 160, 0)
        for key in ("produced_samples", "submitted_samples", "dropped_samples"):
            self.assertEqual(result[key] % 160, 0)
        if result["state"] != "active":
            self.assertEqual(result["queued_events"], 0)
        if not result["queued_events"]:
            self.assertEqual(result["oldest_age_ms"], 0)

    def records(self, subscription=1, kind=None, session=None):
        session = self.session if session is None else session
        return [r for r in self.rx_records if r["session"] == session and r["subscription_id"] == subscription
                and (kind is None or r["kind"] == kind)]

    def feed(self, frames, sequence=31000, timestamp=176000, ssrc=SOURCE, marker=True, plan=None):
        """绝对20ms发送；晚到达到一帧时保留诊断并失败，不追赶伪造完整负载。"""
        events = [(i * STEP, i) for i in range(len(frames))] if plan is None else sorted(plan)
        start, lateness = time.monotonic(), []
        for offset, index in events:
            deadline = start + offset
            while time.monotonic() < deadline:
                self.poll(deadline - time.monotonic())
            late = time.monotonic() - deadline
            lateness.append(late * 1000)
            self.observations.append(dict(op="generator_send_check", index=index, late_ms=late * 1000))
            self.assertLess(late, STEP, "RX输入发生器迟到至少20ms，不能调宽或追赶")
            raw = oracle.rtp(self.payload, sequence + index, timestamp + index * 160, frames[index], ssrc,
                             marker=marker and index == 0)
            self.send_network(raw)
            self.poll(0)
        self.observations.append(dict(op="generator_finished", frames=len(events), late_ms_max=max(lateness, default=0)))

    def assert_decoded(self, frames, sequence=31000, timestamp=176000, subscription=1, ssrc=SOURCE):
        """独立压扩公式逐样本精确核验，来源包序/时间不能用导出事件号冒充。"""
        data = self.records(subscription, 1)
        self.assertEqual(len(data), len(frames))
        # 展开时间线的起始周数由接收器选择，不能把原始u16/u32误当u64绝对值。
        # 独立核对首包低位后，整段仍必须按真实输入完整递增，防止仅掩码漏检倒退。
        sequence_base, timestamp_base = data[0]["rtp_sequence"], data[0]["rtp_timestamp"]
        self.assertEqual(sequence_base & 0xffff, sequence & 0xffff)
        self.assertEqual(timestamp_base & 0xffffffff, timestamp & 0xffffffff)
        for index, (record, body) in enumerate(zip(data, frames)):
            self.assertEqual(record["samples"], tuple(oracle.decode(code, self.law) for code in body))
            self.assertEqual((record["ssrc"], record["rtp_sequence"], record["rtp_timestamp"]),
                             (ssrc, sequence_base + index, timestamp_base + index * 160))
            self.assertEqual(record["flags"] & 15, 15)
            self.assertTrue(record["flags"] & 256, "真实解码应保留输入到达位置")
            self.assertTrue(record["flags"] & 512, "真实播放应保留计划期限位置")
            self.assertEqual(record["sample_count"], 160)
            self.assertLess(record["queue_age_ms"], 100, "过期导出记录没有转为显式缺口")
        self.assertEqual(len({r["source_generation"] for r in data}), 1)
        self.assertEqual(len({r["source_segment"] for r in data}), 1)
        for previous, current in zip(data, data[1:]):
            self.assertEqual(current["media_time_ns"] - previous["media_time_ns"], 20_000_000)
        return data

    def assert_accounting(self, status, subscription=1):
        """终态且通道已排空后，用全部原帧对账，不将ExportGap当成功送音。"""
        rows = self.records(subscription)
        events = [r for r in rows if r["kind"] not in (10, 11, 12)]
        gaps = [r for r in rows if r["kind"] == 10]
        self.assertEqual(len(events), status["submitted_events"])
        self.assertEqual(sum(r["sample_count"] for r in events), status["submitted_samples"])
        self.assertEqual(sum(r["gap_last_seq"] - r["gap_first_seq"] + 1 for r in gaps), status["dropped_events"])
        self.assertEqual(sum((r["gap_decoded_events"] + r["gap_plc_events"]) * 160 for r in gaps), status["dropped_samples"])
        covered = []
        for row in rows:
            if row["kind"] == 10:
                self.assertLessEqual(row["gap_last_seq"], status["produced_events"])
                covered.extend(range(row["gap_first_seq"], row["gap_last_seq"] + 1))
            elif row["kind"] not in (11, 12):
                covered.append(row["event_seq"])
        self.assertEqual(covered, list(range(1, status["produced_events"] + 1)), "观察缺口、重复或乱序没有如实保留")
        self.assertEqual(status["queued_events"], 0)

    def stop_subscription(self, subscription=1):
        result = self.rx_command("unsubscribe", subscription)
        self.assertEqual(result["state"], "stopped")
        self.wait(lambda: bool(self.records(subscription, 11)))
        self.tick(.015)
        self.assertEqual(len(self.records(subscription, 11)), 1)
        self.assertEqual(self.records(subscription, 11)[0]["event_seq"], result["produced_events"])
        self.assert_accounting(result, subscription)
        return result

    def close(self):
        """所有退出路径落原证据；强制清理或漏Release保持失败，不补造归零。"""
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
        result = dict(test=self.id(), connected=CONNECTED, rx_lane=getattr(self, "rx_lane", None),
                      socket_budget=getattr(self, "socket_budget", {}), wire=self.wire, wire_bytes=self.wire_bytes,
                      observations=self.observations, binary_before=getattr(self, "binary_before", None),
                      binary_after=digest(MEDIA), sources_before=getattr(self, "sources_before", {}),
                      sources_after={p.name: digest(p) for p in getattr(self, "source_paths", [])},
                      fixture_before=getattr(self, "fixture_before", {}),
                      fixture_after={name: digest(self.directory / name) for name in getattr(self, "fixture_before", {})},
                      forced_cleanup=forced, remaining_sessions=sorted(self.allocated),
                      process_returncode=self.process.returncode if self.process else None,
                      scope="Rust JSON+FD4真实UDP单腿RX；不覆盖SIP ACK授权、Go ASR消费者、供应商、原版差分或容量")
        result["files"] = {p.name: {"sha256": digest(p), "bytes": p.stat().st_size}
                           for p in self.directory.iterdir() if p.is_file()}
        (self.directory / "wire.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        self.assertFalse(forced)
        self.assertEqual(result["process_returncode"], 0)
        self.assertEqual(result["binary_before"], result["binary_after"])
        self.assertEqual(result["sources_before"], result["sources_after"])
        self.assertEqual(result["fixture_before"], result["fixture_after"])
        self.assertEqual(result["remaining_sessions"], [])


class RxStreamIntegration(RxStreamFixture):
    def test_exact_both_laws_wrap_metadata_and_status_never_replays(self):
        """所有样本、两种整数回绕与一次性观察；状态读取不能重新生产音频。"""
        for payload in (0, 8):
            with self.subTest(payload=payload):
                self.allocate(payload, session=payload + 1)
                empty = self.command("stats")["stats"]
                self.assertEqual(empty["rx_active_subscriptions"], 0)
                self.assertEqual(empty["rx_observation_storage_bytes"], 0, "未订阅不应预分配观察队列")
                self.rx_command("subscribe")
                self.assertEqual(self.rx_command("subscribe")["produced_events"], 0)
                active = self.command("stats")["stats"]
                self.assertEqual(active["rx_active_subscriptions"], 1)
                self.assertEqual(active["rx_queued_events"], 0)
                self.assertGreater(active["rx_observation_storage_bytes"], 0)
                frames = code_frames(12, payload + 1)
                self.feed(frames, sequence=65530, timestamp=0xfffffc00)
                self.tick(.23)
                decoded = self.assert_decoded(frames, sequence=65530, timestamp=0xfffffc00)
                boundaries = self.records(kind=7)
                self.assertTrue(boundaries)
                self.assertEqual(boundaries[0]["boundary"], 1)
                self.assertEqual(boundaries[0]["source_generation"], decoded[0]["source_generation"])
                self.assertTrue(decoded[0]["flags"] & 16)
                before = len(self.rx_records)
                first = self.rx_command("status")
                for _ in range(3):
                    again = self.rx_command("status")
                    self.assertEqual(again["produced_events"], first["produced_events"])
                    self.tick(.010)
                self.assertEqual(len(self.rx_records), before)
                self.assertEqual(self.rtp, [], "本地RX不能被当作TX回声")
                self.stop_subscription()
                self.release()

    def test_rx_does_not_include_tone_or_active_dtmf_tx(self):
        """独立RX仍逐样本精确，真实tone/按键共享TX但不污染上行。"""
        self.allocate(8)
        self.rx_command("subscribe")
        self.assertTrue(self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=500, duration_ms=200)["ok"])
        self.assertTrue(self.command("dtmf_send", session=1, leg="a", digits="5", duration_ms=55)["ok"])
        frames = code_frames(6, 9)
        self.feed(frames)
        self.tick(.30)
        self.assert_decoded(frames)
        audio = [p for p in self.rtp if p["payload"] == 8]
        self.assert_pcm(audio, [round(8192 * math.sin(2 * math.pi * 500 * i / 8000)) for i in range(1600)])
        assert_events(self, [p["raw"] for p in self.rtp], "5", 55)
        self.assert_identity()
        self.assertEqual(self.records(kind=13), [], "TX按键不能伪造RX辅助观察")
        self.stop_subscription()
        self.release()

    def test_cn_original_sid_and_six_plc_tail_suspend_without_silence(self):
        """SID不伪造成PCM；恢复的新段和六帧逐次衰减都来自实际消费。"""
        self.allocate()
        self.rx_command("subscribe")
        first = code_frames(3, 2)
        self.feed(first)
        self.tick(.020)
        sid = b"\x1e\x07\x11"
        self.send_network(oracle.rtp(13, 31003, 176480, sid, SOURCE))
        self.tick(.28)
        self.assert_decoded(first)
        cn = self.records(kind=4)
        self.assertEqual(len(cn), 1)
        self.assertEqual(cn[0]["body"], sid)
        self.assertTrue(cn[0]["cn_applied"])
        self.assertEqual(self.records(kind=2), [])
        self.assertEqual(self.records(kind=3), [])
        old_segment = self.records(kind=1)[-1]["source_segment"]
        resume = code_frames(5, 7)
        self.feed(resume, sequence=31004, timestamp=179200)
        self.tick(.24)
        decoded = self.records(kind=1)
        self.assertEqual(len(decoded), 8)
        self.assertGreater(decoded[3]["source_segment"], old_segment)
        self.assertEqual(decoded[3]["source_generation"], decoded[0]["source_generation"])
        timestamp_epoch = decoded[0]["rtp_timestamp"] - 176000
        for i, row in enumerate(decoded[3:]):
            self.assertEqual(row["samples"], tuple(oracle.decode(code, self.law) for code in resume[i]))
            self.assertEqual(row["rtp_timestamp"], timestamp_epoch + 179200 + i * 160)
        tail = self.records(kind=2)
        self.assertEqual(len(tail), 6)
        expected = decoded[-1]["samples"]
        for i, row in enumerate(tail):
            expected = decay(expected)
            self.assertEqual(row["samples"], expected)
            self.assertEqual(row["rtp_timestamp"], timestamp_epoch + 179200 + (5 + i) * 160)
            self.assertEqual(row["known_duration_ticks"], 160)
            self.assertEqual(row["flags"] & 4, 0)
        self.assertTrue(tail[-1]["suspended_after"])
        before = len(self.rx_records)
        self.tick(.12)
        self.assertEqual(len(self.rx_records), before, "尾部暂停不能每20ms继续导出空白音频")
        self.assertEqual(self.rtp, [])
        self.stop_subscription()
        self.release()

    def test_reorder_and_duplicates_have_exactly_one_consumed_frame(self):
        """在40ms缓冲内交换输入次序，重复包只增加真实拒绝计数。"""
        self.allocate()
        self.rx_command("subscribe")
        frames = code_frames(12, 4)
        plan = [(i * STEP, i) for i in range(len(frames)) if i not in (4, 5)]
        plan += [(4 * STEP, 5), (4 * STEP + .010, 4), (8 * STEP + .004, 8)]
        self.feed(frames, plan=plan)
        self.tick(.23)
        self.assert_decoded(frames)
        stats = self.command("stats")["stats"]
        self.assertGreaterEqual(stats["processed_jitter_reordered"], 1)
        self.assertGreaterEqual(stats["processed_jitter_duplicates"], 1)
        self.assertEqual(stats["processed_jitter_late"], 0)
        self.stop_subscription()
        self.release()

    def test_inbound_telephone_event_is_auxiliary_and_never_pcm(self):
        """仅有入站按键时记录辅助状态，不合成静音/PLC，也不反射按键。"""
        self.allocate()
        self.rx_command("subscribe")
        for index, ended in enumerate((False, True, True, True)):
            self.send_network(oracle.rtp(101, 31000 + index, 176000,
                                        bytes((5, 10 | (128 if ended else 0))) + struct.pack("!H", 440 if ended else 160),
                                        SOURCE, marker=index == 0))
            self.tick(.020)
        self.tick(.24)
        self.assertTrue(self.records(kind=13))
        self.assertFalse(any(row["sample_count"] for row in self.records()))
        self.assertFalse(any(row["kind"] in (1, 2, 3) for row in self.records()))
        self.assertEqual(self.rtp, [])
        status = self.stop_subscription()
        self.assertEqual(status["produced_samples"], 0)
        self.release()

    def test_known_source_rtcp_bye_emits_boundary_without_fake_packet_sequence(self):
        """真实已知源RTCP BYE是来源边界；不是新音频、虚构包序或订阅完成。"""
        self.allocate()
        self.rx_command("subscribe")
        frames = code_frames(3, 5)
        self.feed(frames)
        self.wait(lambda: len(self.records(kind=1)) == 3)
        decoded = self.assert_decoded(frames)
        report, _ = oracle.rtcp_source_report(SOURCE, "rx-source", 176480, 3, 480)
        bye = struct.pack("!BBHI", 0x81, 203, 1, SOURCE)
        self.send_network(report + bye, rtcp=True)
        self.wait(lambda: any(row["boundary"] == 6 for row in self.records(kind=7)))
        end = next(row for row in self.records(kind=7) if row["boundary"] == 6)
        self.assertEqual(end["ssrc"], SOURCE)
        self.assertEqual(end["source_generation"], decoded[-1]["source_generation"])
        self.assertEqual(end["flags"] & 4, 0)
        self.assertTrue(end["suspended_after"])
        self.tick(.20)
        self.assertFalse(any(row["event_seq"] > end["event_seq"] and row["sample_count"] for row in self.records()))
        self.assertEqual(self.rx_command("status")["state"], "active")
        self.stop_subscription()
        self.release()

    def test_unsubscribe_idempotence_and_new_subscription_fence(self):
        """旧ID的同配置重试不复活；active订阅不能被新ID隐式抢占。"""
        self.allocate()
        self.rx_command("subscribe")
        self.rx_command("subscribe", subscription=2, expected=False)
        frames = code_frames(4, 1)
        self.feed(frames)
        self.tick(.24)
        self.assert_decoded(frames)
        stopped = self.stop_subscription()
        self.assertEqual(self.rx_command("subscribe")["state"], "stopped")
        before = len(self.rx_records)
        self.feed(code_frames(3, 8), sequence=31004, timestamp=179200)
        self.tick(.23)
        self.assertEqual(len(self.rx_records), before)
        self.assertEqual(self.rx_command("status")["produced_events"], stopped["produced_events"])
        self.rx_command("subscribe", subscription=2)
        self.rx_command("unsubscribe", expected=False)
        new_frames = code_frames(4, 6)
        self.feed(new_frames, sequence=31007, timestamp=182400)
        self.tick(.24)
        self.assert_decoded(new_frames, sequence=31007, timestamp=182400, subscription=2)
        self.assertEqual(len(self.records(subscription=1)), before)
        self.stop_subscription(2)
        self.release()

    def test_release_reuse_rejects_old_session_and_preserves_new_identity(self):
        """真实端口复绑与freshzero；旧内部session不能重新订阅或污染新会话。"""
        self.allocate(session=1)
        self.rx_command("subscribe")
        self.feed(code_frames(3))
        self.tick(.22)
        self.stop_subscription()
        self.release()
        self.rx_command("status", session=1, expected=False)
        self.allocate(payload=8, session=2)
        self.rx_command("subscribe", session=1, expected=False)
        self.rx_command("subscribe", subscription=9)
        frames = code_frames(4, 12)
        self.feed(frames, sequence=9000, timestamp=256000, ssrc=SOURCE + 1)
        self.tick(.24)
        self.assert_decoded(frames, sequence=9000, timestamp=256000, subscription=9, ssrc=SOURCE + 1)
        self.stop_subscription(9)
        self.release()

    def test_relay_bridge_and_unknown_session_do_not_accept_rx(self):
        """纯透传/双腿桥和未知会话不是本地A腿上行入口，不降级伪成功。"""
        self.rx_command("subscribe", session=999, expected=False)
        for session, topology in ((1, None), (2, "bridge")):
            with self.subTest(topology=topology):
                self.allocate(session=session, topology=topology)
                self.rx_command("subscribe", expected=False)
                self.tick(.05)
                self.assertEqual(self.rx_records, [])
                self.release()

    def test_without_rx_fd_never_advertises_or_accepts_subscription(self):
        """没有继承专用FD4时保持旧媒体能力，不发布可用的RX订阅。"""
        self.allocate()
        self.rx_command("subscribe", expected=False)
        self.tick(.05)
        self.assertEqual(self.rx_records, [])
        self.release()

    def test_slow_reader_exports_gap_and_preserves_exact_accounting(self):
        """主动缩小Unix缓冲，短暂停读必须明确丢弃而不能阻塞RTP。"""
        self.allocate()
        self.rx_command("subscribe")
        self.read_rx = False
        frames = code_frames(25, 3)
        self.feed(frames)
        state = self.rx_command("status")
        self.assertEqual(state["state"], "active")
        self.assertGreater(state["dropped_events"], 0, "未实际迫使背压；保留socket预算诊断，不能skip")
        self.assertLessEqual(state["queued_events"], 8)
        self.read_rx = True
        self.tick(.24)
        gaps = self.records(kind=10)
        self.assertTrue(gaps)
        self.assertTrue(any(row["reason"] & 3 for row in gaps))
        # 首批已交给内核的少量帧在暂停读取约0.5秒后必然超过原观察期限。
        # 这些原帧的绝对期限不能因恢复读取变成新鲜；这不是Go SDK拒绝测试。
        expired = [row for row in self.records(kind=1) if row["expired_at_recording"]]
        self.assertTrue(expired, "未留下真实OS驻留过期帧，不能声称覆盖恢复读取时效")
        self.assertTrue(any(row["received_clock_ns"] - row["observation_lower_bound_ns"] >= 200_000_000
                            for row in expired))
        self.assertTrue(any(row["observation_age_ms"] >= 20 for row in self.records(kind=1)),
                        "未记录排队后仍保留原观察年龄的音频，不能声称覆盖重试期限")
        previous = None
        for row in self.records(kind=1):
            ticks = (row["rtp_timestamp"] & 0xffffffff) - 176000
            self.assertEqual(ticks % 160, 0)
            index = ticks // 160
            self.assertTrue(0 <= index < len(frames))
            self.assertEqual(row["rtp_sequence"] & 0xffff, 31000 + index)
            if previous is not None:
                earlier, earlier_index = previous
                self.assertGreater(index, earlier_index)
                self.assertEqual(row["rtp_sequence"] - earlier["rtp_sequence"], index - earlier_index)
                self.assertEqual(row["rtp_timestamp"] - earlier["rtp_timestamp"], (index - earlier_index) * 160)
            previous = row, index
            self.assertEqual(row["samples"], tuple(oracle.decode(code, self.law) for code in frames[index]))
        self.assertFalse(self.records(kind=9))
        stopped = self.stop_subscription()
        self.assertGreater(stopped["dropped_samples"], 0)
        self.assertLess(stopped["submitted_samples"], stopped["produced_samples"])
        self.release()

    def test_blackhole_isolates_rx_after_deadline_without_stopping_tx(self):
        """超过一秒无法前进后只撤销上行，JSON和实际tone发送仍可用。"""
        self.allocate()
        self.rx_command("subscribe")
        self.read_rx = False
        self.feed(code_frames(65, 11))
        self.tick(.20)
        failed = self.rx_command("status")
        self.assertEqual(failed["state"], "failed")
        self.assertEqual(failed["error"], "rx_lane_unavailable")
        self.assertGreater(failed["dropped_events"], 0)
        self.assertEqual(failed["queued_events"], 0)
        stats = self.command("stats")["stats"]
        self.assertEqual(stats["processed_decoded_frames"], 65, "上行故障不能停止A腿解码")
        self.assertEqual(stats["rx_active_subscriptions"], 0)
        self.assertEqual(stats["rx_queued_events"], 0)
        started = time.monotonic()
        self.assertTrue(self.command("playback_start", session=1, playback_id=1, leg="a", frequency_hz=500, duration_ms=200)["ok"])
        self.assertLess(time.monotonic() - started, .2, "上行黑洞阻塞了有界JSON控制")
        self.tick(.26)
        audio = [p for p in self.rtp if p["payload"] == 0]
        self.assert_pcm(audio, [round(8192 * math.sin(2 * math.pi * 500 * i / 8000)) for i in range(1600)])
        self.assert_identity()
        self.assertEqual(self.rx_command("unsubscribe")["state"], "failed", "普通退订不能清除真实失败")
        self.rx_command("subscribe", subscription=2, expected=False)
        self.release()


def main():
    global MEDIA, ARTIFACTS, CONNECTED
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--media", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--connected", action="store_true")
    parser.add_argument("--port-start", type=int, default=8600)
    parser.add_argument("--port-end", type=int, default=8799)
    args, remaining = parser.parse_known_args()
    MEDIA, ARTIFACTS, CONNECTED = args.media.resolve(), args.artifacts.resolve(), args.connected
    if not MEDIA.is_file():
        parser.error("实际候选媒体二进制缺失")
    if not 1024 <= args.port_start < args.port_end <= 8999 or args.port_end - args.port_start < 31:
        parser.error("必须提供1024..8999中的至少32个隔离端口，不接触主9080或主媒体范围")
    if any(args.port_start <= port <= args.port_end for port in (5060, 5080, 8021)):
        parser.error("测试范围不能包含主SIP/ESL监听端口5060、5080、8021")
    endpoints.PORT_START, endpoints.PORT_END = args.port_start, args.port_end
    ARTIFACTS.mkdir(parents=True, exist_ok=False)
    unittest.main(argv=[__file__, *remaining], verbosity=2)


if __name__ == "__main__":
    main()
