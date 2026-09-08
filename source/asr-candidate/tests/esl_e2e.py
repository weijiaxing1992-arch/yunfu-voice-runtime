#!/usr/bin/env python3
"""真实 SIP 双腿 + Rust RTP + ESL 通话控制回归。每例独立进程，不计原版差分认证。"""
import argparse
import json
import os
from pathlib import Path
import signal
import socket
import struct
import subprocess
import tempfile
import time
import unittest
import urllib.parse

import e2e


class ESLPeer:
    """单连接保留接收缓冲，按字节长度解析回复与嵌套事件正文。"""
    def __init__(self, port, password):
        self.sock = socket.create_connection(("127.0.0.1", port), 2)
        self.file = self.sock.makefile("rb")
        self.events = []
        assert self.frame()[0]["content-type"] == "auth/request"
        self.sock.sendall(("auth " + password + "\n\n").encode())
        assert self.frame()[0]["reply-text"] == "+OK accepted"

    def frame(self):
        """逐行读取有界头，正文允许中文、空行和任意二进制数据。"""
        headers, count = {}, 0
        while True:
            line = self.file.readline(65537)
            if not line:
                raise AssertionError("ESL connection ended unexpectedly")
            count += len(line)
            assert count <= 65536, "oversized ESL header"
            if line in (b"\n", b"\r\n"):
                break
            key, value = line.decode().rstrip("\r\n").split(":", 1)
            headers[key.lower()] = value.strip()
        length = int(headers.get("content-length", "0"))
        assert 0 <= length <= 16 * 1024 * 1024
        body = self.file.read(length)
        assert len(body) == length
        return headers, body

    def command(self, command):
        """事件可以穿插命令回复；保留它们供生命周期断言，不通过丢弃事件隐藏时序问题。"""
        self.sock.sendall((command + "\n\n").encode())
        while True:
            head, body = self.frame()
            if head["content-type"] == "text/event-json":
                self.events.append(json.loads(body))
            else:
                return head, body

    def api(self, command):
        head, body = self.command("api " + command)
        assert head["content-type"] == "api/response"
        return body.decode()

    def wait_events(self, event_name, count=2):
        """等待明确数量的真实事件，连接超时使漏报变成失败。"""
        while len([e for e in self.events if e["Event-Name"] == event_name]) < count:
            head, body = self.frame()
            assert head["content-type"] == "text/event-json"
            self.events.append(json.loads(body))
        return [e for e in self.events if e["Event-Name"] == event_name]

    def close(self):
        self.file.close()
        self.sock.close()


class ESLIntegration(unittest.TestCase):
    """借用已有 SIP 断言，另建 ESL 隔离启动夹具，避免重复执行旧的全部用例。"""
    sock = e2e.Integration.sock
    free_tcp_port = e2e.Integration.free_tcp_port
    get = e2e.Integration.get
    invite = e2e.Integration.invite
    establish = e2e.Integration.establish
    hangup = e2e.Integration.hangup
    wait_active = e2e.Integration.wait_active

    def setUp(self):
        self.resources = []
        self.tmp = tempfile.TemporaryDirectory(prefix="rustswitch-esl-e2e-")
        self.addCleanup(self.tmp.cleanup)
        self.upstream, self.a, self.ar, self.ac, self.br, self.bc = [self.sock() for _ in range(6)]
        for resource in self.resources:
            self.addCleanup(resource.close)
        self.port, self.esl_port = self.free_tcp_port(), self.free_tcp_port()
        sip_probe = self.sock()
        self.address = sip_probe.getsockname()
        sip_probe.close()
        cfg = json.loads((e2e.ROOT / "config/local.json").read_text())
        cfg["sip"].update(listen=f"127.0.0.1:{self.address[1]}", advertise=f"127.0.0.1:{self.address[1]}", upstream=f"127.0.0.1:{self.upstream.getsockname()[1]}", ack_timeout_ms=1000)
        cfg["media"].update(connect_sockets=e2e.CONNECTED, binary=str(e2e.MEDIA), port_start=6000, port_end=6099)
        cfg["limits"].update(max_calls=2, calls_per_second=10, burst_calls=10)
        cfg["admin"]["listen"] = f"127.0.0.1:{self.port}"
        cfg["journal"]["path"] = str(Path(self.tmp.name) / "events.jsonl")
        conf = Path(self.tmp.name) / "config.json"
        conf.write_text(json.dumps(cfg))
        secret = Path(self.tmp.name) / "esl.secret"
        secret.write_text("isolated-test-password\n")
        secret.chmod(0o600)
        self.log = open(Path(self.tmp.name) / "server.log", "w+")
        self.addCleanup(self.log.close)
        self.process = subprocess.Popen([str(e2e.CONTROL), "-config", str(conf), "-esl-listen", f"127.0.0.1:{self.esl_port}", "-esl-password-file", str(secret)], cwd=e2e.ROOT, stdout=self.log, stderr=self.log)
        self.addCleanup(self.stop_process)
        end = time.monotonic() + 8
        while time.monotonic() < end:
            try:
                if self.get("/readyz")["ready"]:
                    self.esl = ESLPeer(self.esl_port, "isolated-test-password")
                    self.addCleanup(self.esl.close)
                    head, _ = self.esl.command("event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_HANGUP")
                    self.assertEqual(head["reply-text"], "+OK event listener enabled json")
                    return
            except (OSError, ValueError):
                pass
            if self.process.poll() is not None:
                self.log.seek(0)
                self.fail(self.log.read())
            time.sleep(0.03)
        self.fail("isolated ESL service did not become ready")

    def stop_process(self):
        """仅清理本例拥有的子进程；第一阶段排空，第二阶段取消。"""
        if self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(1)
            except subprocess.TimeoutExpired:
                self.process.send_signal(signal.SIGTERM)
                self.process.wait(4)

    def check_media(self, outgoing, accepted):
        """实际双向发送 PCMU RTP；仅有 SIP 200 或事件不能证明通话可用。"""
        a_port = e2e.media_ports(e2e.parse(accepted)[2])[0]
        b_port = e2e.media_ports(e2e.parse(outgoing)[2])[0]
        for sequence in range(3):
            packet = struct.pack("!BBHII", 0x80, 0, sequence, sequence * 160, 123) + bytes(160)
            for sender, receiver, port in [(self.ar, self.br, a_port), (self.br, self.ar, b_port)]:
                sender.sendto(packet, ("127.0.0.1", port))
                self.assertEqual(receiver.recvfrom(2048)[0], packet)

    def test_esl_variables_do_not_interrupt_audio(self):
        original, outgoing, accepted = self.establish()
        events = self.esl.wait_events("CHANNEL_CREATE")
        self.esl.wait_events("CHANNEL_ANSWER")
        a = next(e for e in events if e["Call-Direction"] == "inbound")
        b = next(e for e in events if e["Call-Direction"] == "outbound")
        self.assertNotEqual(a["Unique-ID"], b["Unique-ID"])
        self.assertEqual(a["Other-Leg-Unique-ID"], b["Unique-ID"])
        self.assertEqual(self.esl.api("uuid_getvar " + a["Unique-ID"] + " sip_call_id"), e2e.parse(original)[1]["call-id"])
        self.assertEqual(self.esl.api("uuid_setvar " + a["Unique-ID"] + " customer 中文客户"), "+OK\n")
        self.assertEqual(self.esl.api("uuid_getvar " + a["Unique-ID"] + " customer"), "中文客户")
        self.assertEqual(self.esl.api("uuid_getvar " + b["Unique-ID"] + " customer"), "_undef_")
        self.check_media(outgoing, accepted)
        self.hangup(accepted)
        self.esl.wait_events("CHANNEL_HANGUP")
        self.assertEqual(self.esl.api("uuid_exists " + a["Unique-ID"]), "false")

    def test_uuid_kill_hangs_up_both_legs_and_reclaims_media(self):
        _, outgoing, accepted = self.establish()
        events = self.esl.wait_events("CHANNEL_CREATE")
        self.check_media(outgoing, accepted)
        channel = events[0]["Unique-ID"]
        self.assertEqual(self.esl.api("uuid_kill " + channel + " NORMAL_CLEARING"), "+OK\n")
        for endpoint in (self.a, self.upstream):
            bye, source = e2e.receive(endpoint, lambda m: m[0].startswith("BYE "))
            endpoint.sendto(e2e.response(bye), source)
        ended = self.esl.wait_events("CHANNEL_HANGUP")
        self.assertTrue(all(e["Hangup-Cause"] == "NORMAL_CLEARING" for e in ended))
        self.wait_active(0)
        self.assertEqual(self.esl.api("uuid_exists " + channel), "false")
        # 第二次真实呼叫验证媒体预留可再用，不只核对计数器归零。
        _, outgoing2, accepted2 = self.establish()
        self.check_media(outgoing2, accepted2)
        self.hangup(accepted2)

    def test_esl_disconnect_preserves_established_call(self):
        _, outgoing, accepted = self.establish()
        self.esl.wait_events("CHANNEL_ANSWER")
        self.esl.close()
        self.check_media(outgoing, accepted)
        self.hangup(accepted)

    def test_channel_create_answer_hangup_are_unique_and_correlated(self):
        """逐腿核对身份、来源和真实生命周期顺序，不把订阅成功当作事件已发出。"""
        original, outgoing, accepted = self.establish()
        created = self.esl.wait_events("CHANNEL_CREATE")
        answered = self.esl.wait_events("CHANNEL_ANSWER")
        self.assertEqual(len(created), 2)
        self.assertEqual(len(answered), 2)
        ids = {event["Call-Direction"]: event["Unique-ID"] for event in created}
        self.assertEqual(set(ids), {"inbound", "outbound"})
        self.assertNotEqual(ids["inbound"], ids["outbound"])
        call_ids = {"inbound": e2e.parse(original)[1]["call-id"], "outbound": e2e.parse(outgoing)[1]["call-id"]}
        for event in created + answered:
            direction = event["Call-Direction"]
            self.assertEqual(event["Unique-ID"], ids[direction])
            self.assertEqual(event["variable_uuid"], ids[direction])
            self.assertEqual(event["variable_sip_call_id"], call_ids[direction])
            self.assertEqual(event["Other-Leg-Unique-ID"], ids["outbound" if direction == "inbound" else "inbound"])
            self.assertEqual(self.esl.api("uuid_exists " + ids[direction]), "true")
        self.check_media(outgoing, accepted)
        self.hangup(accepted)
        ended = self.esl.wait_events("CHANNEL_HANGUP")
        self.assertEqual(len(ended), 2)
        for uuid in ids.values():
            events = [event for event in self.esl.events if event.get("Unique-ID") == uuid]
            self.assertEqual([event["Event-Name"] for event in events], ["CHANNEL_CREATE", "CHANNEL_ANSWER", "CHANNEL_HANGUP"])
            stamps = [int(event["Event-Date-Timestamp"]) for event in events]
            self.assertEqual(stamps, sorted(stamps))
            self.assertEqual(events[-1]["Hangup-Cause"], "NORMAL_CLEARING")
            self.assertEqual(self.esl.api("uuid_exists " + uuid), "false")
        self.wait_active(0)

    def test_basic_uuid_api_errors_mutations_and_deleted_channels(self):
        """真实活动通道上逐个核对变量读写/删除、双腿隔离和错误不伤媒体。"""
        _, outgoing, accepted = self.establish()
        channels = self.esl.wait_events("CHANNEL_CREATE")
        a, b = [next(event["Unique-ID"] for event in channels if event["Call-Direction"] == direction) for direction in ("inbound", "outbound")]
        self.assertEqual(self.esl.api("uuid_getvar"), "-USAGE: <uuid> <var>\n")
        self.assertEqual(self.esl.api("uuid_setvar"), "-USAGE: <uuid> <var> [value]\n")
        self.assertEqual(self.esl.api("uuid_getvar " + a + " absent"), "_undef_")
        self.assertEqual(self.esl.api("uuid_setvar " + a + " customer 中文 客户"), "+OK\n")
        self.assertEqual(self.esl.api("uuid_getvar " + a + " customer"), "中文 客户")
        self.assertEqual(self.esl.api("uuid_getvar " + b + " customer"), "_undef_")
        self.assertTrue(self.esl.api("uuid_setvar " + a + " uuid overwrite").startswith("-ERR"))
        self.assertEqual(self.esl.api("uuid_getvar " + a + " uuid"), a)
        self.assertEqual(self.esl.api("uuid_setvar " + a + " customer"), "+OK\n")
        self.assertEqual(self.esl.api("uuid_getvar " + a + " customer"), "_undef_")
        self.assertTrue(self.esl.api("uuid_kill " + a + " unsupported-cause").startswith("-ERR"))
        self.assertEqual(self.esl.api("uuid_exists " + a), "true")
        self.check_media(outgoing, accepted)
        self.hangup(accepted)
        self.esl.wait_events("CHANNEL_HANGUP")
        for uuid in (a, b):
            self.assertEqual(self.esl.api("uuid_exists " + uuid), "false")
            self.assertEqual(self.esl.api("uuid_getvar " + uuid + " customer"), "-ERR No such channel!\n")
            self.assertEqual(self.esl.api("uuid_setvar " + uuid + " customer 迟到写入"), "-ERR No such channel!\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--control", type=Path, default=e2e.CONTROL)
    parser.add_argument("--media", type=Path, default=e2e.MEDIA)
    parser.add_argument("--connected", action="store_true")
    args, remaining = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    unittest.main(argv=[__file__] + remaining, verbosity=2)
