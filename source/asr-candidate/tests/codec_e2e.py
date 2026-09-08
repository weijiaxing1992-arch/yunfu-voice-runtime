#!/usr/bin/env python3
"""在自己启动的隔离实例中验证音频 SDP 协商、RTP 字节透传与拒绝路径。

复用 e2e.py 的进程/端点夹具及 SIP 报文助手，不访问已有管理服务，不继承其整套测试。
这些用例验证信令与转发契约，不验证解码音质、实际跨编码转码、原版 FreeSWITCH 互通或容量。
固定媒体端口沿用既有夹具，因此本脚本、e2e.py 及两种 UDP 模式必须顺序运行。
"""

import argparse
from dataclasses import dataclass
from pathlib import Path
import socket
import struct
import sys
import time
import unittest
import uuid

import e2e


@dataclass(frozen=True)
class CodecCase:
    """冻结一个线路格式；采样率、RTP 时钟及 SDP 声道声明相互独立。"""

    key: str  # 稳定的测试名称，不参与线路协商。
    name: str  # 输入 SDP 的编码名称；允许 G729A 这类受支持的别名。
    payload: int  # RTP 主音频载荷编号。
    sample_rate: int  # 名义 PCM 采样率，用于构造定长测试负载。
    clock_rate: int  # RTP 时间戳每秒刻度，G722 为 8000。
    channels: int = 1  # SDP 声道声明；Opus 固定为 2，并不保证负载含双声道音频。
    fmtp: str = ""  # 传给协商器的格式参数。

    @property
    def canonical_name(self):
        """G729A 与 G729 共用线路格式，其他编码和打包变体保持独立名称。"""
        return "G729" if self.name == "G729A" else self.name.upper()


# 每个格式都建立一通真实 SIP 呼叫；压缩码流只作为转发向量，不作为听感或解码器正确性证据。
CASES = (
    CodecCase("pcmu", "PCMU", 0, 8000, 8000),
    CodecCase("pcma", "PCMA", 8, 8000, 8000),
    CodecCase("pcmu_dynamic", "PCMU", 96, 8000, 8000),
    CodecCase("pcma_dynamic", "PCMA", 97, 8000, 8000),
    CodecCase("g722", "G722", 9, 16000, 8000),
    CodecCase("g722_dynamic", "G722", 98, 16000, 8000),
    CodecCase("opus", "OPUS", 111, 48000, 48000, 2, "minptime=10;useinbandfec=1;stereo=0"),
    CodecCase("g729", "G729", 18, 8000, 8000, 1, "annexb=no"),
    CodecCase("g729a", "G729A", 99, 8000, 8000),
    *(CodecCase(f"g726_{rate}", f"G726-{rate}", 100, 8000, 8000) for rate in (16, 24, 32, 40)),
    *(CodecCase(f"aal2_g726_{rate}", f"AAL2-G726-{rate}", 100, 8000, 8000) for rate in (16, 24, 32, 40)),
    CodecCase("l16_16000_mono", "L16", 110, 16000, 16000),
    CodecCase("l16_44100_mono_static", "L16", 11, 44100, 44100),
    CodecCase("l16_44100_stereo_static", "L16", 10, 44100, 44100, 2),
    CodecCase("l16_48000_stereo", "L16", 110, 48000, 48000, 2),
)


def audio_payload(case):
    """生成确定性的 20 ms 转发向量；L16 显式写网络大端，其余定长格式保留各自包长。

    Opus 使用 20 ms 单帧静音包。其他压缩格式的位串仅用于原样透传检验，不能据此宣称音频质量。
    """
    if case.name == "OPUS":
        return bytes.fromhex("f8fffe")
    if case.name == "L16":
        count = case.sample_rate * case.channels // 50
        return b"".join(struct.pack("!h", (index * 257) % 65536 - 32768) for index in range(count))
    if "G726-" in case.name:
        length = int(case.name.rsplit("-", 1)[1]) * 1000 // 8 // 50
    elif case.name in ("G729", "G729A"):
        length = 20  # 两个 10 ms G.729 语音帧，每帧 10 字节。
    else:
        length = 160  # G.711/G.722 的 20 ms 线路包均为 160 字节。
    return bytes((index * 37 + 11) % 256 for index in range(length))


def session_description(case, rtp, rtcp, extra_payloads=(), extra_attributes=""):
    """用原始 SDP 文本构造 offer/answer，独立于服务的渲染器，避免同一实现相互掩盖错误。"""
    payloads = " ".join(str(value) for value in (case.payload, *extra_payloads))
    mapping = f"{case.name}/{case.clock_rate}"
    if case.channels != 1:
        mapping += f"/{case.channels}"
    description = (
        "v=0\r\no=codec-test 1 1 IN IP4 127.0.0.1\r\ns=codec-test\r\n"
        "c=IN IP4 127.0.0.1\r\nt=0 0\r\n"
        f"m=audio {rtp} RTP/AVP {payloads}\r\na=rtcp:{rtcp} IN IP4 127.0.0.1\r\n"
        f"a=rtpmap:{case.payload} {mapping}\r\na=ptime:20\r\n"
    )
    if case.fmtp:
        description += f"a=fmtp:{case.payload} {case.fmtp}\r\n"
    return (description + extra_attributes).encode()


class CodecIntegration(unittest.TestCase):
    """以组合方式复用既有夹具；每项测试拥有自己的控制/媒体进程，不重复加载原有测试。"""

    def setUp(self):
        """提前登记清理，再启动真实本机子实例；缺二进制作为明确失败，不能跳过后宣称通过。"""
        self.h = e2e.Integration()
        self.addCleanup(self.h.tearDown)
        self.h.setUp()

    def tearDown(self):
        """失败时在夹具删除临时目录前保存有界服务日志到测试输出，便于区分协商拒绝与媒体进程故障。"""
        outcome = getattr(self, "_outcome", None)
        result = getattr(outcome, "result", None)
        failed = result is not None and any(test is self or getattr(test, "test_case", None) is self
                                             for test, _ in result.failures + result.errors)
        if failed and hasattr(self.h, "log"):
            self.h.log.flush()
            self.h.log.seek(0, 2)
            self.h.log.seek(max(0, self.h.log.tell() - 16384))
            print("\n失败用例的隔离服务日志（最多 16 KiB）：\n" + self.h.log.read(), file=sys.stderr)

    def assert_clean(self):
        """等待控制面、媒体分配和 IPC 结果队列清空，区别于只收到 BYE 的应答。"""
        deadline = time.monotonic() + 4
        state = None
        while time.monotonic() < deadline:
            state = self.h.get("/v1/status")
            if (state["active_calls"] == 0 and state["established_calls"] == 0
                    and all(worker["stats"].get("active_calls") == 0 for worker in state["workers"])
                    and state["controller"]["media_results_queue_length"] == 0):
                return
            time.sleep(0.05)
        self.fail(f"拆呼资源未回收：{state}")

    def offer(self, body):
        """提交一条自定义编码的初始 INVITE，返回请求以便匹配最终应答及事务 ACK。"""
        wire = e2e.message(
            "INVITE", "sip:100@local", self.h.a.getsockname()[1], uuid.uuid4().hex,
            "z9hG4bK" + uuid.uuid4().hex, body,
        )
        self.h.a.sendto(wire, self.h.address)
        return wire

    def assert_mapping(self, body, case):
        """断言服务输出的主 PT、编码名、RTP 时钟与声道声明，没有把 PCM 采样率写进 G722 rtpmap。"""
        lines = body.decode().splitlines()
        media_line = next(line for line in lines if line.startswith("m=audio "))
        self.assertEqual(int(media_line.split()[3]), case.payload)
        mapping = f"a=rtpmap:{case.payload} {case.canonical_name}/{case.clock_rate}"
        if case.channels != 1:
            mapping += f"/{case.channels}"
        self.assertIn(mapping.lower(), [line.lower() for line in lines])
        self.assertIn("a=ptime:20", lines)
        if case.name == "G722":
            self.assertNotIn(f"a=rtpmap:{case.payload} G722/16000".lower(), [line.lower() for line in lines])

    def establish(self, case, extra_payloads=(), extra_attributes="", answer_case=None, answer_attributes=None):
        """完成真实双腿 SIP 建立，同时检查发往上游和返回主叫的 SDP。"""
        h = self.h
        self.offer(session_description(case, h.ar.getsockname()[1], h.ac.getsockname()[1], extra_payloads, extra_attributes))
        outgoing, peer = e2e.receive(h.upstream, lambda item: item[0].startswith("INVITE "))
        self.assert_mapping(e2e.parse(outgoing)[2], case)
        selected = answer_case or case
        attributes = extra_attributes if answer_attributes is None else answer_attributes
        answer = e2e.response(
            outgoing, body=session_description(selected, h.br.getsockname()[1], h.bc.getsockname()[1], extra_payloads, attributes),
            port=h.upstream.getsockname()[1],
        )
        h.upstream.sendto(answer, peer)
        e2e.receive(h.upstream, lambda item: item[0].startswith("ACK "))
        accepted, _ = e2e.receive(h.a, lambda item: item[0].startswith("SIP/2.0 200") and item[1]["cseq"].endswith(" INVITE"))
        self.assert_mapping(e2e.parse(accepted)[2], selected)
        headers = e2e.parse(accepted)[1]
        h.a.sendto(e2e.message("ACK", "sip:rustswitch@local", h.a.getsockname()[1], headers["call-id"],
                            "z9hG4bK" + uuid.uuid4().hex, to=headers["to"]), h.address)
        h.wait_active(1)
        return outgoing, accepted

    def transfer(self, sender, receiver, target, source, packet):
        """核对完整 RTP 字节、服务发送源端口及时间戳，包含扩展前的主头而不只比较音频负载。"""
        sender.sendto(packet, ("127.0.0.1", target))
        receiver.settimeout(2)
        received, peer = receiver.recvfrom(65535)
        self.assertEqual(received, packet)
        self.assertEqual(peer, ("127.0.0.1", source))
        self.assertEqual(struct.unpack("!I", received[4:8])[0], struct.unpack("!I", packet[4:8])[0])

    def relay_codec(self, case):
        """每方向发送四个真实 UDP/RTP 包；20 ms 的时间戳步长以 RTP clock 计算，不以样本总数计算。"""
        outgoing, accepted = self.establish(case)
        a_ports = e2e.media_ports(e2e.parse(accepted)[2])
        b_ports = e2e.media_ports(e2e.parse(outgoing)[2])
        payload = audio_payload(case)
        for sender, receiver, target, source, ssrc in (
            (self.h.ar, self.h.br, a_ports[0], b_ports[0], 0x12345678),
            (self.h.br, self.h.ar, b_ports[0], a_ports[0], 0x87654321),
        ):
            for sequence in range(1, 5):
                timestamp = 10000 + sequence * case.clock_rate // 50
                packet = struct.pack("!BBHII", 0x80, case.payload, sequence, timestamp, ssrc) + payload
                self.transfer(sender, receiver, target, source, packet)
        if case.name == "L16":
            # 检查输入向量确实采用大端；转发验证不能替代两端 PCM 解码正确性测试。
            self.assertEqual(payload[:4], bytes.fromhex("80008101"))
        self.h.hangup(accepted)
        self.assert_clean()

    def test_dtmf_and_cn_preserve_negotiated_clocks(self):
        """用 48 kHz Opus 搭配 8 kHz 电话事件及 48 kHz CN，验证辅助包保持各自时钟与负载编号。"""
        case = next(value for value in CASES if value.key == "opus")
        attributes = "a=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-9\r\na=rtpmap:103 CN/48000\r\n"
        outgoing, accepted = self.establish(case, (101, 103), attributes)
        for body in (e2e.parse(outgoing)[2], e2e.parse(accepted)[2]):
            self.assertIn(b"a=rtpmap:101 telephone-event/8000\r\n", body)
            self.assertIn(b"a=fmtp:101 0-9\r\n", body)
            self.assertIn(b"a=rtpmap:103 CN/48000\r\n", body)
        a_ports = e2e.media_ports(e2e.parse(accepted)[2])
        b_ports = e2e.media_ports(e2e.parse(outgoing)[2])
        for sender, receiver, target, source, ssrc in (
            (self.h.ar, self.h.br, a_ports[0], b_ports[0], 100),
            (self.h.br, self.h.ar, b_ports[0], a_ports[0], 200),
        ):
            packets = (
                struct.pack("!BBHII", 0x80, 111, 1, 48000, ssrc) + audio_payload(case),
                struct.pack("!BBHII", 0x80, 101, 2, 8000, ssrc) + struct.pack("!BBH", 5, 0x8a, 160),
                struct.pack("!BBHII", 0x80, 103, 3, 48960, ssrc) + bytes([42]),
            )
            for packet in packets:
                self.transfer(sender, receiver, target, source, packet)
        # 终端只协商 0-9；事件 16 即使结构完整也不能穿透媒体过滤。
        denied = struct.pack("!BBHII", 0x80, 101, 4, 8160, 100) + struct.pack("!BBH", 16, 0x8a, 160)
        self.h.ar.sendto(denied, ("127.0.0.1", a_ports[0]))
        self.h.br.settimeout(0.25)
        with self.assertRaises(socket.timeout):
            self.h.br.recvfrom(65535)
        self.h.hangup(accepted)
        self.assert_clean()

    def test_unnegotiated_payload_is_rejected(self):
        """未协商的 CN 和另一主编码不能作为普通 RTP 穿透；随后正常音频仍必须可用。"""
        case = CASES[0]
        outgoing, accepted = self.establish(case)
        a_ports = e2e.media_ports(e2e.parse(accepted)[2])
        b_ports = e2e.media_ports(e2e.parse(outgoing)[2])
        for pt in (8, 13, 101):
            packet = struct.pack("!BBHII", 0x80, pt, pt + 1, 160, 123) + bytes(160)
            self.h.ar.sendto(packet, ("127.0.0.1", a_ports[0]))
            self.h.br.settimeout(0.2)
            with self.assertRaises(socket.timeout):
                self.h.br.recvfrom(65535)
        packet = struct.pack("!BBHII", 0x80, 0, 110, 320, 123) + audio_payload(case)
        self.transfer(self.h.ar, self.h.br, a_ports[0], b_ports[0], packet)
        self.h.hangup(accepted)
        self.assert_clean()

    def test_static_cn_and_opus_directional_preferences(self):
        """先验证 G711 静态 CN，再用独立通话确认 Opus 接收偏好允许不同并正确返回主叫。"""
        pcmu = CASES[0]
        outgoing, accepted = self.establish(pcmu, (13,), "a=rtpmap:13 CN/8000\r\n")
        a_ports = e2e.media_ports(e2e.parse(accepted)[2])
        b_ports = e2e.media_ports(e2e.parse(outgoing)[2])
        self.assertIn(b"a=rtpmap:13 CN/8000\r\n", e2e.parse(accepted)[2])
        packet = struct.pack("!BBHII", 0x80, 13, 1, 160, 17) + bytes([42])
        self.transfer(self.h.ar, self.h.br, a_ports[0], b_ports[0], packet)
        self.h.hangup(accepted)
        self.assert_clean()

        opus = next(value for value in CASES if value.key == "opus")
        answer = CodecCase("opus_answer", "OPUS", 111, 48000, 48000, 2, "stereo=1;useinbandfec=0")
        outgoing, accepted = self.establish(opus, answer_case=answer)
        self.assertIn(b"stereo=1", e2e.parse(accepted)[2])
        self.assertIn(b"useinbandfec=0", e2e.parse(accepted)[2])
        a_ports = e2e.media_ports(e2e.parse(accepted)[2])
        b_ports = e2e.media_ports(e2e.parse(outgoing)[2])
        packet = struct.pack("!BBHII", 0x80, 111, 1, 960, 18) + audio_payload(opus)
        self.transfer(self.h.ar, self.h.br, a_ports[0], b_ports[0], packet)
        self.h.hangup(accepted)
        self.assert_clean()

    def reject_offer(self, body):
        """初始错误 SDP 必须返回 488，不启动上游呼叫，也不留下媒体分配。"""
        original = self.offer(body)
        call_id = e2e.parse(original)[1]["call-id"]
        rejected, _ = e2e.receive(self.h.a, lambda item: item[0].startswith("SIP/2.0 488") and item[1]["call-id"] == call_id)
        self.assertEqual(e2e.parse(rejected)[1]["cseq"], "1 INVITE")
        self.h.upstream.settimeout(0.15)
        with self.assertRaises(socket.timeout):
            self.h.upstream.recvfrom(65535)
        self.assert_clean()

    def test_invalid_and_unsupported_offers_return_488(self):
        """覆盖错误 G722/Opus 时钟、重复 PT/fmtp、内部小端别名及暂未实现的 AMR/EVS。"""
        rtp, rtcp = self.h.ar.getsockname()[1], self.h.ac.getsockname()[1]
        pcmu = session_description(CASES[0], rtp, rtcp)
        opus = session_description(next(value for value in CASES if value.key == "opus"), rtp, rtcp)
        cases = {
            "G722 clock": session_description(CodecCase("bad", "G722", 9, 16000, 16000), rtp, rtcp),
            "Opus channels": session_description(CodecCase("bad", "OPUS", 111, 48000, 48000, 1), rtp, rtcp),
            "duplicate PT": pcmu.replace(b"RTP/AVP 0", b"RTP/AVP 0 0"),
            "conflicting static PT": pcmu.replace(b"PCMU/8000", b"OPUS/48000/2"),
            "duplicate fmtp": opus + b"a=fmtp:111 stereo=1\r\n",
            "unknown fmtp": opus.replace(b"minptime=10", b"unknown-format-mode=1"),
            "little endian alias": session_description(CodecCase("bad", "S16LE", 110, 16000, 16000), rtp, rtcp),
            "AMR unavailable": session_description(CodecCase("bad", "AMR", 96, 8000, 8000), rtp, rtcp),
            "EVS unavailable": session_description(CodecCase("bad", "EVS", 96, 16000, 16000), rtp, rtcp),
        }
        for name, body in cases.items():
            with self.subTest(name=name):
                self.reject_offer(body)

    def reject_answer(self, offer_case, answer_case):
        """上游已回答 200 后出现不兼容格式，主叫必须收到 488，已建立的上游腿必须收到清理 BYE。"""
        h = self.h
        original = self.offer(session_description(offer_case, h.ar.getsockname()[1], h.ac.getsockname()[1]))
        outgoing, peer = e2e.receive(h.upstream, lambda item: item[0].startswith("INVITE "))
        h.upstream.sendto(e2e.response(outgoing, body=session_description(answer_case, h.br.getsockname()[1], h.bc.getsockname()[1]),
                                        port=h.upstream.getsockname()[1]), peer)
        e2e.receive(h.upstream, lambda item: item[0].startswith("ACK "))
        rejected, _ = e2e.receive(h.a, lambda item: item[0].startswith("SIP/2.0 488"))
        headers = e2e.parse(rejected)[1]
        original_headers = e2e.parse(original)[1]
        branch = original_headers["via"].split("branch=", 1)[1].split(";", 1)[0]
        h.a.sendto(e2e.message("ACK", "sip:100@local", h.a.getsockname()[1], headers["call-id"], branch, to=headers["to"]), h.address)
        bye, peer = e2e.receive(h.upstream, lambda item: item[0].startswith("BYE "))
        h.upstream.sendto(e2e.response(bye, port=h.upstream.getsockname()[1]), peer)
        self.assert_clean()

    def test_answer_cannot_switch_g726_packing(self):
        """上游把 G726 改成 AAL2-G726 时必须拒绝，不能把不同打包方式当作同一种透传。"""
        self.reject_answer(next(value for value in CASES if value.key == "g726_32"),
                           next(value for value in CASES if value.key == "aal2_g726_32"))

    def test_answer_cannot_request_implicit_transcoding(self):
        """PCMU offer 收到 PCMA answer 必须拒绝，不能把线路转发成功当作实际转码已实现。"""
        self.reject_answer(CASES[0], CASES[1])

    def test_answer_cannot_remap_dynamic_payload(self):
        """同一 Opus 编码换成另一个动态 PT 时明确拒绝，验证尚未实现的双腿编号重写边界。"""
        opus = next(value for value in CASES if value.key == "opus")
        self.reject_answer(opus, CodecCase("remapped", "OPUS", 112, 48000, 48000, 2, opus.fmtp))


def install_codec_case(case):
    """为每个编解码组合生成独立测试项；闭包固定本次向量，避免循环变量被后续覆盖。"""
    def test(self):
        """建立一通当前线路格式的呼叫，完成双向转发和清理验证。"""
        self.relay_codec(case)
    test.__name__ = "test_relay_" + case.key
    setattr(CodecIntegration, test.__name__, test)


for _case in CASES:
    install_codec_case(_case)


def main():
    """命令行只选择固定夹具的二进制和 UDP 模式；--list 可在不启动任何监听时检查测试清单。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--control", type=Path, default=e2e.CONTROL)
    parser.add_argument("--media", type=Path, default=e2e.MEDIA)
    parser.add_argument("--connected", action="store_true")
    parser.add_argument("--list", action="store_true", help="列出测试，不启动控制或媒体进程")
    args, remaining = parser.parse_known_args()
    if args.list:
        for name in unittest.TestLoader().getTestCaseNames(CodecIntegration):
            print("CodecIntegration." + name)
        return
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    for path in (e2e.CONTROL, e2e.MEDIA):
        if not path.is_file():
            parser.error(f"待测二进制不存在：{path}")
    unittest.main(argv=[__file__, *remaining], verbosity=2)


if __name__ == "__main__":
    main()
