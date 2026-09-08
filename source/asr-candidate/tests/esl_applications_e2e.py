#!/usr/bin/env python3
"""真实 SIP + Rust 媒体 + ESL 应用回归；本地通过不等于原版完整差分认证。"""
import argparse
import json
from pathlib import Path
import signal
import socket
import struct
import subprocess
import tempfile
import time
import unittest
import uuid

import e2e
import esl_e2e


class ApplicationFixture(unittest.TestCase):
    """每例拥有独立进程、口令和 socket；本地路由不使用假上游应答。"""
    local = False
    esl_enabled = True
    sock = e2e.Integration.sock
    free_tcp_port = e2e.Integration.free_tcp_port
    get = e2e.Integration.get
    invite = e2e.Integration.invite
    establish = e2e.Integration.establish
    hangup = e2e.Integration.hangup
    wait_active = e2e.Integration.wait_active
    stop_process = esl_e2e.ESLIntegration.stop_process
    check_media = esl_e2e.ESLIntegration.check_media

    def setUp(self):
        self.resources = []
        self.tmp = tempfile.TemporaryDirectory(prefix="rustswitch-application-e2e-")
        self.addCleanup(self.tmp.cleanup)
        self.upstream, self.a, self.ar, self.ac, self.br, self.bc = [self.sock() for _ in range(6)]
        for resource in self.resources:
            self.addCleanup(resource.close)
        self.port, self.esl_port = self.free_tcp_port(), self.free_tcp_port()
        probe = self.sock()
        self.address = probe.getsockname()
        probe.close()
        config = json.loads((e2e.ROOT / "config/local.json").read_text())
        config["sip"].update(listen=f"127.0.0.1:{self.address[1]}", advertise=f"127.0.0.1:{self.address[1]}", upstream=f"127.0.0.1:{self.upstream.getsockname()[1]}", ack_timeout_ms=1000)
        if self.local:
            config["sip"]["local_extensions"] = ["1000"]
        config["media"].update(connect_sockets=e2e.CONNECTED, binary=str(e2e.MEDIA), port_start=6100, port_end=6199)
        config["limits"].update(max_calls=2, calls_per_second=10, burst_calls=10)
        config["admin"]["listen"] = f"127.0.0.1:{self.port}"
        config["journal"]["path"] = str(Path(self.tmp.name) / "events.jsonl")
        if hasattr(self, "configure"):
            self.configure(config)
        conf = Path(self.tmp.name) / "config.json"
        conf.write_text(json.dumps(config))
        secret = Path(self.tmp.name) / "esl.secret"
        secret.write_text("isolated-test-password\n")
        secret.chmod(0o600)
        self.log = open(Path(self.tmp.name) / "server.log", "w+")
        self.addCleanup(self.log.close)
        command = [str(e2e.CONTROL), "-config", str(conf)]
        if self.esl_enabled:
            command += ["-esl-listen", f"127.0.0.1:{self.esl_port}", "-esl-password-file", str(secret)]
        self.process = subprocess.Popen(command, cwd=e2e.ROOT, stdout=self.log, stderr=self.log)
        self.addCleanup(self.stop_process)
        end = time.monotonic() + 8
        while time.monotonic() < end:
            try:
                if self.get("/readyz")["ready"]:
                    if self.esl_enabled:
                        self.esl = esl_e2e.ESLPeer(self.esl_port, "isolated-test-password")
                        self.addCleanup(self.esl.close)
                        head, _ = self.esl.command("event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_HANGUP CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE DTMF")
                        self.assertTrue(head["reply-text"].startswith("+OK"))
                    self.digit_sequence, self.digit_timestamp = 100, 4000
                    return
            except (OSError, ValueError):
                pass
            if self.process.poll() is not None:
                self.log.seek(0)
                self.fail(self.log.read())
            time.sleep(0.03)
        self.fail("application fixture did not become ready")

    def established_channel(self):
        _, outgoing, accepted = self.establish()
        events = self.esl.wait_events("CHANNEL_CREATE")
        self.esl.wait_events("CHANNEL_ANSWER")
        end = time.monotonic() + 2
        while self.get("/v1/status")["established_calls"] != 1:
            self.assertLess(time.monotonic(), end)
            time.sleep(0.01)
        a = next(item["Unique-ID"] for item in events if item["Call-Direction"] == "inbound")
        b = next(item["Unique-ID"] for item in events if item["Call-Direction"] == "outbound")
        return a, b, outgoing, accepted

    def execute(self, channel, application, arguments, expected="+OK"):
        """受理与完成分开验证；固定关联UUID防止把另一任务事件误认成本任务。"""
        token = str(uuid.uuid4())
        head, _ = self.esl.command(f"sendmsg {channel}\ncall-command: execute\nexecute-app-name: {application}\nexecute-app-arg: {arguments}\nevent-lock: true\nevent-uuid: {token}")
        if expected == "+OK":
            self.assertEqual(head["reply-text"], "+OK")
        else:
            self.assertTrue(head["reply-text"].startswith(expected), head)
        return token

    def application_event(self, token, complete=True, timeout=4):
        wanted = "CHANNEL_EXECUTE_COMPLETE" if complete else "CHANNEL_EXECUTE"
        end = time.monotonic() + timeout
        while True:
            found = [item for item in self.esl.events if item.get("Application-UUID") == token and item["Event-Name"] == wanted]
            if found:
                self.assertEqual(len(found), 1, "application outcome was duplicated")
                return found[0]
            remaining = end - time.monotonic()
            self.assertGreater(remaining, 0, "application outcome missing")
            self.esl.sock.settimeout(remaining)
            head, body = self.esl.frame()
            self.assertEqual(head["content-type"], "text/event-json")
            self.esl.events.append(json.loads(body))

    def send_digit(self, port, digit, sender=None, complete=True):
        """真实 RFC4733 起始/进度/结束包；结束重复两次，业务层必须只得到一个按键。"""
        sender = sender or self.ar
        event = "0123456789*#ABCD".index(digit)
        self.digit_timestamp += 1600
        packets = []
        for index, (ended, duration) in enumerate([(False, 160), (False, 800)] + ([(True, 800)] * 3 if complete else [])):
            self.digit_sequence += 1
            packet = struct.pack("!BBHII", 0x80, 101 | (0x80 if index == 0 else 0), self.digit_sequence, self.digit_timestamp, 0xD7AF) + bytes([event, (0x80 if ended else 0) | 10]) + struct.pack("!H", duration)
            sender.sendto(packet, ("127.0.0.1", port))
            packets.append(packet)
        return packets

    def receive_tone(self, duration_ms):
        """核对远端真实收到有节奏的PCMU提示音，不把媒体RPC的OK当成发音成功。"""
        self.ar.settimeout(3)
        packets = [self.ar.recvfrom(2048)[0] for _ in range((duration_ms + 19) // 20)]
        self.assertTrue(all(len(packet) == 172 and packet[1] & 0x7F == 0 for packet in packets))
        self.assertTrue(any(len(set(packet[12:])) > 8 for packet in packets), "tone payload contained no waveform")
        for first, second in zip(packets, packets[1:]):
            self.assertEqual((struct.unpack("!H", second[2:4])[0] - struct.unpack("!H", first[2:4])[0]) & 0xFFFF, 1)
            self.assertEqual((struct.unpack("!I", second[4:8])[0] - struct.unpack("!I", first[4:8])[0]) & 0xFFFFFFFF, 160)
        return packets


class ApplicationIntegration(ApplicationFixture):
    def test_info_mode_collects_once_and_ignores_parallel_rtp(self):
        a, _, _, accepted = self.established_channel()
        enabled = self.execute(a, "set", "dtmf_type=info")
        self.application_event(enabled)
        read = self.execute(a, "read", "1 1 silence info_digit 3000 #")
        self.application_event(read, False)
        self.send_digit(e2e.media_ports(e2e.parse(accepted)[2])[0], "8")
        headers = e2e.parse(accepted)[1]
        info = e2e.message("INFO", "sip:rustswitch@local", self.a.getsockname()[1], headers["call-id"], "z9hG4bK" + uuid.uuid4().hex, b"Signal=5\r\nDuration=100\r\n", to=headers["to"], cseq=2)
        info = info.replace(b"Content-Type: application/sdp", b"Content-Type: application/dtmf-relay")
        for _ in range(2):
            self.a.sendto(info, self.address)
            e2e.receive(self.a, lambda value: value[0].startswith("SIP/2.0 200") and value[1]["cseq"].endswith("INFO"))
        result = self.application_event(read)
        self.assertEqual(result["variable_info_digit"], "5")
        self.esl.api("echo event-barrier")
        digits = [item for item in self.esl.events if item["Event-Name"] == "DTMF"]
        self.assertEqual([(item["DTMF-Digit"], item["DTMF-Source"]) for item in digits], [("5", "INFO")])
        bye = e2e.message("BYE", "sip:rustswitch@local", self.a.getsockname()[1], headers["call-id"], "z9hG4bK" + uuid.uuid4().hex, to=headers["to"], cseq=3)
        self.a.sendto(bye, self.address)
        e2e.receive(self.a, lambda value: value[0].startswith("SIP/2.0 200") and value[1]["cseq"].endswith("BYE"))
        outgoing, source = e2e.receive(self.upstream, lambda value: value[0].startswith("BYE "))
        self.upstream.sendto(e2e.response(outgoing), source)
        self.wait_active(0)

    def test_set_unset_and_rtp_read_menu_have_real_effects(self):
        a, b, outgoing, accepted = self.established_channel()
        set_job = self.execute(a, "set", "customer=中文客户")
        self.assertEqual(self.application_event(set_job)["variable_customer"], "中文客户")
        self.assertEqual(self.esl.api(f"uuid_getvar {b} customer"), "_undef_")
        read_job = self.execute(a, "read", "1 4 silence digits 2000 #")
        self.application_event(read_job, False)
        queued = self.execute(a, "set", "menu=finished")
        port = e2e.media_ports(e2e.parse(accepted)[2])[0]
        for digit in "12#":
            self.send_digit(port, digit)
        result = self.application_event(read_job)
        self.assertEqual(result["variable_digits"], "12")
        self.assertEqual(result["variable_read_result"], "success")
        self.assertEqual(result["variable_read_terminator_used"], "#")
        self.application_event(queued)
        self.assertEqual(self.esl.api(f"uuid_getvar {a} menu"), "finished")
        self.assertEqual([item["DTMF-Digit"] for item in self.esl.events if item["Event-Name"] == "DTMF"], list("12#"))
        unset = self.execute(a, "unset", "customer")
        self.application_event(unset)
        self.assertEqual(self.esl.api(f"uuid_getvar {a} customer"), "_undef_")
        self.hangup(accepted)

    def test_tone_playback_sends_actual_audio_then_restores_bridge(self):
        a, _, outgoing, accepted = self.established_channel()
        job = self.execute(a, "playback", "tone_stream://%(200,0,440)")
        self.receive_tone(200)
        self.assertEqual(self.application_event(job)["Application-Response"], "FILE PLAYED")
        self.check_media(outgoing, accepted)
        self.hangup(accepted)

    def test_read_prompt_barge_in_and_missing_end_fail_honestly(self):
        a, _, _, accepted = self.established_channel()
        port = e2e.media_ports(e2e.parse(accepted)[2])[0]
        job = self.execute(a, "read", "1 1 tone_stream://%(2000,0,440) choice 3000 #")
        self.ar.settimeout(3)
        first = self.ar.recvfrom(2048)[0]
        self.assertEqual(len(first), 172)
        self.send_digit(port, "5")
        result = self.application_event(job)
        self.assertEqual(result["variable_choice"], "5")
        self.assertEqual(result["variable_read_result"], "success")
        # 收到完成事件后只排空已进入内核的有限尾包；不能还在持续播放整段提示音。
        self.ar.settimeout(0.15)
        remaining = 0
        while True:
            try:
                self.ar.recvfrom(2048)
                remaining += 1
            except socket.timeout:
                break
        self.assertLess(remaining, 50)
        missing = self.execute(a, "read", "1 2 silence incomplete 5000 #")
        self.application_event(missing, False)
        self.send_digit(port, "8", complete=False)
        failed = self.application_event(missing, timeout=5)
        self.assertEqual(failed["variable_read_result"], "failure")
        self.assertTrue(failed["Application-Response"].startswith("-ERR incomplete DTMF"))
        self.execute(a, "read", "1 1 silence again", expected="-ERR DTMF collection disabled")
        self.hangup(accepted)

    def test_hangup_cancels_active_read_and_queued_mutation(self):
        a, _, _, accepted = self.established_channel()
        job = self.execute(a, "read", "1 4 silence digits 60000 #")
        self.application_event(job, False)
        queued = self.execute(a, "set", "must_not_execute=yes")
        self.hangup(accepted)
        self.assertTrue(self.application_event(job)["Application-Response"].startswith("-ERR channel ended"))
        self.assertEqual(self.application_event(queued)["variable_rustswitch_application_state"], "cancelled_before_execution")
        self.assertEqual(self.esl.api(f"uuid_exists {a}"), "false")

    def test_read_timeout_is_complete_and_unknown_application_is_rejected(self):
        a, _, outgoing, accepted = self.established_channel()
        job = self.execute(a, "read", "1 2 silence digits 1000 #")
        result = self.application_event(job)
        self.assertEqual(result["variable_read_result"], "failure")
        self.execute(a, "playback", "/etc/passwd", expected="-ERR")
        self.execute(a, "conference", "invented-room", expected="-ERR")
        self.check_media(outgoing, accepted)
        self.hangup(accepted)


class LocalApplicationIntegration(ApplicationFixture):
    local = True

    def test_local_missing_ack_reclaims_channel_and_media(self):
        """本地应答也必须受 ACK 截止时间保护，防止单腿通道永远占用媒体。"""
        call_id = uuid.uuid4().hex
        self.a.sendto(e2e.message("INVITE", "sip:1000@local", self.a.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, e2e.sdp(self.ar.getsockname()[1], self.ac.getsockname()[1])), self.address)
        e2e.receive(self.a, lambda value: value[0].startswith("SIP/2.0 200"))
        created = self.esl.wait_events("CHANNEL_CREATE", 1)
        self.assertEqual(len(created), 1)
        self.assertNotIn("Other-Leg-Unique-ID", created[0])
        self.wait_active(0)
        ended = self.esl.wait_events("CHANNEL_HANGUP", 1)
        self.assertEqual(ended[0]["Unique-ID"], created[0]["Unique-ID"])
        self.assertEqual(self.esl.command("api uuid_exists " + created[0]["Unique-ID"])[1], b"false")
        self.upstream.settimeout(0.1)
        with self.assertRaises(socket.timeout):
            self.upstream.recvfrom(2048)

    def test_unmatched_extension_still_routes_to_upstream(self):
        """号码白名单不能吞掉普通外呼；上游拒接时仍释放两腿资源。"""
        call_id = uuid.uuid4().hex
        self.a.sendto(e2e.message("INVITE", "sip:1002@local", self.a.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, e2e.sdp(self.ar.getsockname()[1], self.ac.getsockname()[1])), self.address)
        outgoing, source = e2e.receive(self.upstream, lambda value: value[0].startswith("INVITE "))
        self.assertIn("sip:1002@", e2e.parse(outgoing)[0])
        self.upstream.sendto(e2e.response(outgoing, 486, "Busy Here", port=self.upstream.getsockname()[1]), source)
        e2e.receive(self.a, lambda value: value[0].startswith("SIP/2.0 486"))
        e2e.receive(self.upstream, lambda value: value[0].startswith("ACK "))
        self.wait_active(0)

    def test_local_inbound_tone_and_digits_without_upstream(self):
        call_id = uuid.uuid4().hex
        wire = e2e.message("INVITE", "sip:1000@local", self.a.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, e2e.sdp(self.ar.getsockname()[1], self.ac.getsockname()[1]))
        self.a.sendto(wire, self.address)
        accepted, _ = e2e.receive(self.a, lambda value: value[0].startswith("SIP/2.0 200"))
        headers = e2e.parse(accepted)[1]
        self.a.sendto(e2e.message("ACK", "sip:1000@local", self.a.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, to=headers["to"]), self.address)
        events = self.esl.wait_events("CHANNEL_CREATE", 1)
        self.esl.wait_events("CHANNEL_ANSWER", 1)
        channel = events[0]["Unique-ID"]
        end = time.monotonic() + 2
        while self.get("/v1/status")["established_calls"] != 1:
            self.assertLess(time.monotonic(), end)
            time.sleep(0.01)
        self.upstream.settimeout(0.1)
        with self.assertRaises(socket.timeout):
            self.upstream.recvfrom(2048)
        play = self.execute(channel, "playback", "tone_stream://%(200,0,440)")
        self.receive_tone(200)
        self.assertEqual(self.application_event(play)["Application-Response"], "FILE PLAYED")
        read = self.execute(channel, "read", "1 1 silence choice 2000 #")
        self.application_event(read, False)
        self.send_digit(e2e.media_ports(e2e.parse(accepted)[2])[0], "7")
        self.assertEqual(self.application_event(read)["variable_choice"], "7")
        self.a.sendto(e2e.message("BYE", "sip:1000@local", self.a.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, to=headers["to"], cseq=2), self.address)
        e2e.receive(self.a, lambda value: value[0].startswith("SIP/2.0 200") and value[1]["cseq"].endswith("BYE"))
        self.wait_active(0)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--control", type=Path, default=e2e.CONTROL)
    parser.add_argument("--media", type=Path, default=e2e.MEDIA)
    parser.add_argument("--connected", action="store_true")
    args, remaining = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    unittest.main(argv=[__file__] + remaining, verbosity=2)
