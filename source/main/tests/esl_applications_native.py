#!/usr/bin/env python3
"""对显式隔离的已启动服务验证一通自有呼叫的 set/unset/read，保留原始收发，不比较整产品。"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import threading
import time
import uuid

import e2e


class Evidence:
    """总报文预算固定4MiB；认证请求仅保留不可恢复哈希。"""
    def __init__(self):
        self.rows, self.size = [], 0
        self.lock = threading.Lock()

    def add(self, transport, direction, packet, secret=False):
        with self.lock:
            self.size += len(packet)
            if self.size > 4 * 1024 * 1024:
                raise ValueError("evidence limit exceeded")
            row = {"transport": transport, "direction": direction, "bytes": len(packet), "sha256": hashlib.sha256(packet).hexdigest()}
            if secret:
                row["redacted"] = "专用认证秘密，不保留可恢复密码"
            else:
                row["base64"] = base64.b64encode(packet).decode()
            self.rows.append(row)


class Peer:
    """固定超时、长度和事件数量；必须读取真实执行完成事件，不能只看 +OK。"""
    def __init__(self, port, password, evidence):
        self.evidence, self.buffer, self.events = evidence, b"", []
        self.socket = socket.create_connection(("127.0.0.1", port), 3)
        assert self.frame()[0]["content-type"] == "auth/request"
        self.send(("auth " + password + "\n\n").encode(), True)
        assert self.frame()[0]["reply-text"] == "+OK accepted"

    def send(self, packet, secret=False):
        self.evidence.add("ESL", "send", packet, secret)
        self.socket.settimeout(3)
        self.socket.sendall(packet)

    def frame(self):
        deadline = time.monotonic() + 5
        while b"\n\n" not in self.buffer:
            self.receive(deadline)
            assert len(self.buffer) <= 1024 * 1024
        raw, self.buffer = self.buffer.split(b"\n\n", 1)
        assert len(raw) <= 65536
        headers = {}
        for line in raw.decode().splitlines():
            key, value = line.split(":", 1)
            assert key.lower() not in headers
            headers[key.lower()] = value.strip()
        length = int(headers.get("content-length", "0"))
        assert 0 <= length <= 1024 * 1024
        while len(self.buffer) < length:
            self.receive(deadline)
        body, self.buffer = self.buffer[:length], self.buffer[length:]
        return headers, body

    def receive(self, deadline):
        assert time.monotonic() < deadline, "ESL response timeout"
        self.socket.settimeout(max(.01, deadline - time.monotonic()))
        packet = self.socket.recv(65536)
        assert packet, "ESL peer closed"
        self.evidence.add("ESL", "receive", packet)
        self.buffer += packet

    def save_event(self, head, body):
        assert head["content-type"] == "text/event-json"
        assert len(self.events) < 128
        self.events.append(json.loads(body))

    def command(self, command):
        self.send((command + "\n\n").encode())
        for _ in range(128):
            head, body = self.frame()
            if head["content-type"] == "text/event-json":
                self.save_event(head, body)
            else:
                return head, body
        raise AssertionError("command reply was hidden by excessive events")

    def api(self, command):
        head, body = self.command("api " + command)
        assert head["content-type"] == "api/response"
        return body.decode()

    def event(self, predicate):
        for _ in range(128):
            found = next((event for event in self.events if predicate(event)), None)
            if found is not None:
                return found
            head, body = self.frame()
            self.save_event(head, body)
        raise AssertionError("required event not observed")

    def execute(self, channel, application, arguments):
        job = str(uuid.uuid4())
        head, _ = self.command(f"sendmsg {channel}\ncall-command: execute\nexecute-app-name: {application}\nexecute-app-arg: {arguments}\nevent-lock: true\nevent-uuid: {job}")
        assert head["reply-text"] == "+OK", head
        return job

    def completed(self, job):
        return self.event(lambda event: event.get("Application-UUID") == job and event["Event-Name"] == "CHANNEL_EXECUTE_COMPLETE")


def run(args):
    password = os.environ[args.password_env]
    assert password and not any(char in password for char in "\r\n\x00")
    evidence = Evidence()
    report = {"scope": "隔离服务上一通本脚本拥有的SIP呼叫；set/unset和read silence正常收号。不是全API/全事件/全dialplan兼容认证。", "identity_only": {}, "observations": {}, "passed_scoped": False, "wire": evidence.rows, "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
    signaling, rtp, rtcp = e2e.udp(), e2e.udp(), e2e.udp()
    peer, accepted = None, None
    pump, pump_errors, stopped = None, [], threading.Event()
    stream_lock = threading.Lock()
    stream = {"sequence": 100, "timestamp": 4000}
    target = ("127.0.0.1", args.sip_port)
    def send(packet):
        evidence.add("SIP", "send", packet)
        signaling.sendto(packet, target)
    def response(predicate):
        for _ in range(32):
            packet, remote = signaling.recvfrom(65536)
            assert remote == target
            evidence.add("SIP", "receive", packet)
            if predicate(e2e.parse(packet)):
                return packet
        raise AssertionError("required SIP response absent")
    try:
        peer = Peer(args.esl_port, password, evidence)
        report["identity_only"]["version"] = peer.api("version")
        head, _ = peer.command("event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE DTMF CHANNEL_HANGUP")
        assert head["reply-text"].startswith("+OK")
        call_id = uuid.uuid4().hex
        uri = f"sip:{args.extension}@127.0.0.1:{args.sip_port}"
        send(e2e.message("INVITE", uri, signaling.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, e2e.sdp(rtp.getsockname()[1], rtcp.getsockname()[1])))
        accepted = response(lambda value: value[0].startswith("SIP/2.0 200"))
        headers = e2e.parse(accepted)[1]
        send(e2e.message("ACK", uri, signaling.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, to=headers["to"]))
        body = e2e.parse(accepted)[2]
        media_port = e2e.media_ports(body)[0]
        media_host = next(line.split()[-1] for line in body.decode().splitlines() if line.startswith("c=IN IP4 "))
        assert media_host == "127.0.0.1", "fixture must use isolated loopback RTP"
        # 原版park/read通过实际音频帧推进私有事件。从ACK起连续发送有界20ms PCMU帧，
        # 与DTMF共用SSRC和序号锁，避免无媒体时把原版的正常等待误判成应用不可用。
        def pump_audio():
            try:
                for _ in range(1500):
                    if stopped.is_set():
                        return
                    with stream_lock:
                        stream["sequence"] += 1
                        stream["timestamp"] += 160
                        packet = struct.pack("!BBHII", 0x80, 0, stream["sequence"], stream["timestamp"], 0xD7AF) + bytes([0xFF]) * 160
                        evidence.add("RTP", "send", packet)
                        rtp.sendto(packet, (media_host, media_port))
                    if stopped.wait(.02):
                        return
                pump_errors.append("media pump exceeded 30 second fixture budget")
            except Exception as error:
                pump_errors.append(type(error).__name__ + ": " + str(error))
        pump = threading.Thread(target=pump_audio, name="owned-call-pcmu-fixture")
        pump.start()
        created = peer.event(lambda event: event["Event-Name"] == "CHANNEL_CREATE" and event.get("variable_sip_call_id") == call_id)
        channel = created["Unique-ID"]
        peer.event(lambda event: event["Event-Name"] == "CHANNEL_ANSWER" and event.get("Unique-ID") == channel)
        # 给候选控制循环处理已发出的ACK；后续read仍会实际验证媒体就绪。
        time.sleep(.05)
        for application, arguments in [("set", "customer=中文客户"), ("unset", "customer")]:
            job = peer.execute(channel, application, arguments)
            completed = peer.completed(job)
            assert completed.get("Application-Response") == "_none_", completed.get("Application-Response")
            value = peer.api("uuid_getvar " + channel + " customer")
            assert value == ("中文客户" if application == "set" else "_undef_")
            report["observations"][application] = {"value": value, "application_response": completed.get("Application-Response")}
        job = peer.execute(channel, "read", "1 4 silence digits 3000 #")
        peer.event(lambda event: event.get("Application-UUID") == job and event["Event-Name"] == "CHANNEL_EXECUTE")
        for digit in "12#":
            with stream_lock:
                timestamp = stream["timestamp"]
            for part, (end, duration) in enumerate([(False, 160), (False, 320), (True, 480), (True, 480), (True, 480)]):
                with stream_lock:
                    stream["sequence"] += 1
                    packet = struct.pack("!BBHII", 0x80, 101 | (0x80 if part == 0 else 0), stream["sequence"], timestamp, 0xD7AF) + bytes(["0123456789*#ABCD".index(digit), 10 | (0x80 if end else 0)]) + struct.pack("!H", duration)
                    evidence.add("RTP", "send", packet)
                    rtp.sendto(packet, (media_host, media_port))
                time.sleep(.02)
        completed = peer.completed(job)
        assert completed.get("Application-Response") == "_none_", completed.get("Application-Response")
        values = {name: peer.api("uuid_getvar " + channel + " " + name) for name in ["digits", "read_result", "read_terminator_used"]}
        assert values == {"digits": "12", "read_result": "success", "read_terminator_used": "#"}, values
        report["observations"]["read"] = {"variables": values, "application_response": completed.get("Application-Response")}
        send(e2e.message("BYE", uri, signaling.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, to=headers["to"], cseq=2))
        response(lambda value: value[0].startswith("SIP/2.0 200") and value[1]["cseq"].endswith("BYE"))
        accepted = None
        peer.event(lambda event: event["Event-Name"] == "CHANNEL_HANGUP" and event.get("Unique-ID") == channel)
        assert not pump_errors, pump_errors
        report["passed_scoped"] = True
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        stopped.set()
        if pump is not None:
            pump.join(3)
            if pump.is_alive():
                report["passed_scoped"] = False
                report["error"] = "owned media pump did not terminate"
        if accepted is not None:
            try:
                headers = e2e.parse(accepted)[1]
                send(e2e.message("BYE", uri, signaling.getsockname()[1], call_id, "z9hG4bK" + uuid.uuid4().hex, to=headers["to"], cseq=3))
            except OSError:
                pass
        if peer:
            peer.socket.close()
        for endpoint in (signaling, rtp, rtcp):
            endpoint.close()
    report["wire_bytes"] = evidence.size
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--isolated-fixture", action="store_true", required=True)
    parser.add_argument("--sip-port", type=int, required=True)
    parser.add_argument("--esl-port", type=int, required=True)
    parser.add_argument("--extension", required=True)
    parser.add_argument("--password-env", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    assert 1 <= args.sip_port <= 65535 and 1 <= args.esl_port <= 65535
    assert args.extension.isdigit() and len(args.extension) <= 32
    assert not args.output.exists(), "historical evidence must not be overwritten"
    result = run(args)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"passed_scoped": result["passed_scoped"], "observations": result["observations"], "error": result.get("error")}, ensure_ascii=False))
    raise SystemExit(0 if result["passed_scoped"] else 2)
