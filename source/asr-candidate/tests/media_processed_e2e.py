#!/usr/bin/env python3
"""M1 真实 Go/Rust 双腿 G.711 处理回归；不是 FreeSWITCH 成对或容量验收。

单个用例拥有独立控制、媒体和UDP端点。必须使用新processing实现，旧relay、缺二进制、
不支持能力或未接线均失败，不skip。两种UDP模式以及其他本机媒体测试应顺序运行。
"""
import argparse
import math
import os
from pathlib import Path
import select
import signal
import socket
import struct
import subprocess
import time
import unittest
import uuid

import e2e
import media_processed_fixture as fixture
from media_processed_fixture import ProcessedFixture, SOURCE_SEQ, SOURCE_SSRC, SOURCE_TS, STEP, SAMPLES


class ProcessedMediaIntegration(ProcessedFixture):
    def test_cn_then_marker_with_new_timestamp_phase_resumes_real_audio(self):
        """CN后新talkspurt偏离旧160ticks格点；不能因此让已接通通道永久失声。"""
        self.establish()
        old_a = fixture.audio_frames("PCMU", 467, 2)
        new_a = fixture.audio_frames("PCMU", 677, 20)
        b_frames = fixture.audio_frames("PCMA", 883, 28)
        events = self.audio_events("a", old_a) + self.audio_events("b", b_frames)
        events.append((0.040, "a", fixture.rtp(13, SOURCE_SEQ["a"] + 2, SOURCE_TS["a"] + 320,
                                               bytes([42]), SOURCE_SSRC["a"])))
        for index, body in enumerate(new_a):
            events.append((0.125 + index * STEP, "a",
                           fixture.rtp(0, SOURCE_SEQ["a"] + 3 + index, SOURCE_TS["a"] + 1000 + index * SAMPLES,
                                       body, SOURCE_SSRC["a"], index == 0)))
        got = self.exchange(events)
        self.assert_timeline(got["a"], "a")
        self.assert_timeline(got["b"], "b")
        self.assert_audio(got["a"], "a", b_frames)
        audio = [packet for packet in got["b"] if packet["payload"] == 8]
        self.assertGreaterEqual(len(audio), len(old_a) + len(new_a), "新相位语音被拒绝后通道失声")
        self.assert_audio(audio[:2], "b", old_a)
        self.assert_audio(audio[2:22], "b", new_a)
        gap = (audio[2]["timestamp"] - audio[1]["timestamp"]) & 0xFFFFFFFF
        self.assertGreater(gap, SAMPLES, "新talkspurt没有保留真实静默时间")
        self.assertLess(gap, 1600, "新段时间回拨或异常跳跃")
        self.assertEqual(len([packet for packet in got["b"] if packet["payload"] == 13]), 1)
        stats = self.hangup()
        self.assertEqual(stats["processed_decoded_frames"], 50)
        self.assertEqual(stats["processed_cn_packets"], 1)
        self.assertEqual(stats["processed_send_errors"], 0)

    def test_worker_pause_expires_old_audio_without_catchup_burst(self):
        """只暂停本例worker140ms，输入仍按20ms发送；恢复丢弃过期音频并继续真实语音。"""
        self.establish()
        worker = self.get("/v1/status")["workers"][0]
        pid = worker["pid"]
        self.assertIsInstance(pid, int)
        self.assertNotIn(pid, (0, 1, os.getpid(), self.process.pid))
        parent = subprocess.run(["/bin/ps", "-p", str(pid), "-o", "ppid="],
                                capture_output=True, text=True, check=True, timeout=2)
        self.assertEqual(int(parent.stdout.strip()), self.process.pid,
                         "拒绝向不属于本测试Go控制进程的PID发送信号")
        signals = {}
        stopped = False

        def pause():
            nonlocal stopped
            os.kill(pid, signal.SIGSTOP)
            stopped = True
            signals["stopped"] = time.monotonic()
            self.record("owned_worker_SIGSTOP", str(pid).encode(), ("pid", pid))

        def resume():
            nonlocal stopped
            os.kill(pid, signal.SIGCONT)
            stopped = False
            signals["resumed"] = time.monotonic()
            self.record("owned_worker_SIGCONT", str(pid).encode(), ("pid", pid))

        a_frames = fixture.audio_frames("PCMU", 463, 48)
        b_frames = fixture.audio_frames("PCMA", 941, 48)
        try:
            got = self.exchange(self.audio_events("a", a_frames) + self.audio_events("b", b_frames),
                                actions=((0.250, pause), (0.390, resume)))
        finally:
            # 任何发送、收包或断言失败也必须恢复自己的worker，避免SIGSTOP阻挡进程清理。
            if stopped:
                try:
                    os.kill(pid, signal.SIGCONT)
                except ProcessLookupError:
                    pass
        self.assertEqual(set(signals), {"stopped", "resumed"}, "未真正执行暂停/恢复")
        self.assertGreaterEqual(signals["resumed"] - signals["stopped"], 0.130)
        resumed_at = signals["resumed"] - self.last_exchange_origin
        for side, inputs in (("a", b_frames), ("b", a_frames)):
            packets = got[side]
            self.assert_timeline(packets, side)
            audio = [packet for packet in packets if packet["payload"] == self.codecs[side][1]]
            self.assertTrue(any(packet["at"] < 0.250 for packet in audio), "暂停前未产生真实音频")
            positions = []
            for packet in audio:
                offset = (packet["timestamp"] - audio[0]["timestamp"]) & 0xFFFFFFFF
                self.assertEqual(offset % SAMPLES, 0)
                position = offset // SAMPLES
                positions.append(position)
                # 恢复瞬间若某个源包已过期，可输出一帧有标记的PLC；其后新鲜源帧必须精确对照。
                due = position * STEP + 0.040
                if position < len(inputs) and (packet["at"] < 0.250 or due >= resumed_at):
                    self.assert_audio([packet], side, [inputs[position]])
            self.assertEqual(positions, sorted(set(positions)), "恢复后重放了旧时间位置")
            self.assertTrue(any(second - first > 1 for first, second in zip(positions, positions[1:])),
                            "暂停期间的过期音频没有被淘汰")
            after = [(packet, position) for packet, position in zip(audio, positions) if packet["at"] >= resumed_at]
            self.assertTrue(after, "worker恢复后没有继续输出")
            # 40ms缓冲+20ms过期界限，恢复时已超期的位置不能再进入UDP。
            for packet, position in after:
                self.assertGreaterEqual(packet["at"] - (position * STEP + 0.040), -0.010,
                                        "恢复后提前发送了尚未到播放时刻的未来音频")
                self.assertLess(packet["at"] - (position * STEP + 0.040), 0.030,
                                "恢复后仍发送超过20ms期限的旧音频（含10ms观察余量）")
            self.assertLessEqual(sum(resumed_at <= packet["at"] < resumed_at + 0.015 for packet in audio), 2,
                                 "worker恢复后集中补发一串过期音频")
            self.assertIn(47, positions, "恢复后的最后真实语音未到达")
            self.assertTrue(set(range(math.ceil(resumed_at / STEP), 48)).issubset(positions),
                            "恢复后按时发送的新音频仍被丢弃")
        status = self.get("/v1/status")
        self.assertEqual(status["established_calls"], 1)
        self.assertEqual(status["workers"][0]["restarts"], 0, "140ms暂停不应超过一秒监督期限而重启worker")
        stats = self.hangup()
        self.assertGreater(stats["processed_playout_expired"], 0)
        self.assertGreater(stats["processed_jitter_late"], 0)

    def test_183_without_aux_then_200_restores_dtmf_without_restarting_media(self):
        """183移除辅助格式，最终200恢复原offer允许的辅助格式；已播放的处理图不可重建。"""
        self.offer()
        outgoing, peer = self.receive_sip(self.upstream, lambda item: item[0].startswith("INVITE "))
        proposed = e2e.parse(outgoing)[2]
        self.assertIn(b"a=rtpmap:8 PCMA/8000", proposed)
        self.assertIn(b"a=rtpmap:101 telephone-event/8000", proposed)
        self.send_sip(self.upstream, e2e.response(outgoing, status=183, reason="Session Progress",
                                                body=fixture.description("PCMA", 8, self.br.getsockname()[1],
                                                                         self.bc.getsockname()[1], auxiliary=False),
                                                port=self.upstream.getsockname()[1]), peer)
        provisional, _ = self.receive_sip(self.a, lambda item: item[0].startswith("SIP/2.0 183"))
        early_sdp = e2e.parse(provisional)[2]
        self.assertIn(b"a=rtpmap:0 PCMU/8000", early_sdp)
        self.assertNotIn(b"telephone-event", early_sdp)
        self.assertNotIn(b"CN/", early_sdp)
        self.a_ports, self.b_ports = e2e.media_ports(early_sdp), e2e.media_ports(proposed)
        self.codecs = {"a": ("PCMU", 0), "b": ("PCMA", 8)}
        a_frames = fixture.audio_frames("PCMU", 461, 24)
        b_frames = fixture.audio_frames("PCMA", 919, 24)
        early = self.exchange(self.audio_events("a", a_frames[:8]) + self.audio_events("b", b_frames[:8]), tail=0.005)
        original_clock = self.last_exchange_origin
        self.assertTrue(early["a"] and early["b"], "183阶段没有真实双向媒体，不能验证图保持")
        self.send_sip(self.upstream, e2e.response(outgoing, body=fixture.description("PCMA", 8,
                                                self.br.getsockname()[1], self.bc.getsockname()[1]),
                                                port=self.upstream.getsockname()[1]), peer)
        self.receive_sip(self.upstream, lambda item: item[0].startswith("ACK "))
        accepted, _ = self.receive_sip(self.a, lambda item: item[0].startswith("SIP/2.0 200")
                                      and item[1]["cseq"].endswith("INVITE"))
        final_sdp = e2e.parse(accepted)[2]
        self.assertIn(b"a=rtpmap:0 PCMU/8000", final_sdp)
        self.assertIn(b"a=rtpmap:101 telephone-event/8000", final_sdp)
        self.assertIn(b"a=rtpmap:13 CN/8000", final_sdp)
        self.assertEqual(e2e.media_ports(final_sdp), self.a_ports)
        header = e2e.parse(accepted)[1]
        self.send_sip(self.a, e2e.message("ACK", "sip:rustswitch@local", self.a.getsockname()[1],
                                        header["call-id"], "z9hG4bK" + uuid.uuid4().hex, to=header["to"]), self.address)
        self.accepted = accepted
        events = self.audio_events("a", a_frames[8:], frame_offset=8) + self.audio_events("b", b_frames[8:], frame_offset=8)
        for index, at in enumerate((0.065, 0.085, 0.105)):
            events.append((at, "a", fixture.rtp(101, 0, SOURCE_TS["a"] + 8 * SAMPLES,
                                               bytes([7, 0x8A, 0x01, 0xE0]), SOURCE_SSRC["a"], index == 0)))
        # 保持183之前的RTP序号连续，辅助包只影响序号，不制造缺失音频时间位置。
        counters = {"a": SOURCE_SEQ["a"] + 8, "b": SOURCE_SEQ["b"] + 8}
        ordered = []
        for at, side, raw in sorted(events, key=lambda item: item[0]):
            value = bytearray(raw)
            value[2:4] = struct.pack("!H", counters[side] & 0xFFFF)
            counters[side] += 1
            ordered.append((at, side, bytes(value)))
        final = self.exchange(ordered, start_at=original_clock + 8 * STEP)
        for side, inputs in (("a", b_frames), ("b", a_frames)):
            all_packets = early[side] + final[side]
            self.assert_timeline(all_packets, side)
            self.assert_audio(all_packets, side, inputs)
        received_dtmf = [packet for packet in final["b"] if packet["payload"] == 101]
        self.assertEqual(len(received_dtmf), 3, "200恢复能力后没有真实发送DTMF")
        self.assertTrue(all(packet["body"] == bytes([7, 0x8A, 0x01, 0xE0]) for packet in received_dtmf))
        observed = self.esl.wait_events("DTMF", count=1)
        self.assertEqual([(item["DTMF-Digit"], item["DTMF-Duration"], item["DTMF-Source"]) for item in observed],
                         [("7", "480", "RTP")], "结束重传必须只产生一个真实业务按键")
        self.assertEqual(self.esl.api("uuid_getvar " + observed[0]["Unique-ID"] + " sip_call_id"), header["call-id"])
        stats = self.hangup()
        self.assertEqual(stats["processed_decoded_frames"], 48)

    def test_rtcp_reports_are_terminated_count_media_and_finish_with_bye(self):
        """持续真实语音覆盖随机首周期，SR/RR/SDES必须属于新会话，释放发送复合BYE。"""
        self.establish()
        a_frames = fixture.audio_frames("PCMU", 449, 400)
        b_frames = fixture.audio_frames("PCMA", 863, 400)
        events = self.audio_events("a", a_frames) + self.audio_events("b", b_frames)
        source_reports, expected_lsr = {}, {}
        for side in ("a", "b"):
            raw, lsr = fixture.rtcp_source_report(SOURCE_SSRC[side], "independent-" + side,
                                                 SOURCE_TS[side] + 50 * SAMPLES, 51, 51 * SAMPLES)
            source_reports[side], expected_lsr[side] = raw, lsr
            events.append((1.0, side + "_rtcp", raw))
        got = self.exchange(events, capture_rtcp=True)
        self.assert_audio(got["a"], "a", b_frames)
        self.assert_audio(got["b"], "b", a_frames)
        for side in ("a", "b"):
            self.assert_timeline(got[side], side)
            reports = got[side + "_rtcp"]
            self.assertTrue(reports, f"{side}在最长首周期内没有实际RTCP报告")
            sent_counts = []
            for packet in reports:
                self.assertNotIn(packet["raw"], source_reports.values(), "RTCP仍原样转发另一腿报告")
                self.assertEqual(packet["peer"], ("127.0.0.1", self.a_ports[1] if side == "a" else self.b_ports[1]))
                parts = packet["parts"]
                self.assertEqual(parts[0]["kind"], 200, "有实际TX音频时应生成SR")
                sr = parts[0]
                self.assertEqual(sr["ssrc"], got[side][0]["ssrc"])
                self.assertGreater(sr["packets"], 0)
                self.assertLessEqual(sr["packets"], len(got[side]), "SR宣称发送了端点未收到的RTP")
                self.assertEqual(sr["octets"], sum(len(item["body"]) for item in got[side][:sr["packets"]]))
                sent_counts.append(sr["packets"])
                sdes = next((item for item in parts if item["kind"] == 202), None)
                self.assertIsNotNone(sdes, "复合报告没有SDES")
                matching = [chunk for chunk in sdes["chunks"] if chunk["ssrc"] == sr["ssrc"]]
                self.assertEqual(len(matching), 1)
                self.assertTrue(any(kind == 1 and value for kind, value in matching[0]["items"]), "SDES没有有效CNAME")
                received_blocks = [block for item in parts if item["kind"] in (200, 201)
                                   for block in item["reports"] if block["ssrc"] == SOURCE_SSRC[side]]
                self.assertEqual(len(received_blocks), 1, "RR必须报告本腿RX，不能引用另一腿")
                rr = received_blocks[0]
                self.assertEqual(rr["lsr"], expected_lsr[side])
                self.assertGreater(rr["dlsr"], 0)
                self.assertLess(abs(rr["dlsr"] / 65536 - (packet["at"] - 1.0)), 0.2,
                                "DLSR不是从真实收到端点SR的时刻计算")
                self.assertGreaterEqual(rr["highest_sequence"], SOURCE_SEQ[side])
                self.assertEqual(rr["cumulative_lost"], 0)
            self.assertEqual(sent_counts, sorted(sent_counts))
        self.hangup()
        ended = {"a": [], "b": []}
        endpoints = {self.ac: "a", self.bc: "b"}
        deadline = time.monotonic() + 1
        while time.monotonic() < deadline and not all(ended.values()):
            ready, _, _ = select.select(list(endpoints), [], [], min(0.05, max(0, deadline - time.monotonic())))
            for sock in ready:
                raw, peer = sock.recvfrom(4096)
                self.record("rtcp_after_hangup_" + endpoints[sock], raw, peer)
                parts = fixture.unpack_rtcp(raw)
                byes = [part for part in parts if part["kind"] == 203]
                if byes:
                    self.assertTrue(any(part["kind"] == 202 for part in parts), "终止BYE不是复合报告")
                    self.assertEqual(len(byes), 1)
                    self.assertEqual(byes[0]["sources"], [got[endpoints[sock]][0]["ssrc"]])
                    ended[endpoints[sock]].append(raw)
        self.assertTrue(all(ended.values()), "没有收到双方真实RTCP BYE")
        # 已进入内核的有限尾部先排空；媒体释放后的新周期/声音都不应再产生。
        sockets = [self.ar, self.br, self.ac, self.bc]
        for sock in sockets:
            sock.setblocking(False)
            for _ in range(16):
                try:
                    sock.recvfrom(4096)
                except BlockingIOError:
                    break
            else:
                self.fail("挂机后媒体尾包超过有限预算")
        self.assertEqual(select.select(sockets, [], [], 0.20)[0], [], "已释放处理图仍产生RTP/RTCP")

    def test_bidirectional_pcmu_pcma_decodes_changes_and_reencodes(self):
        """不同编码与不同波形双向实际通信；原样转发或只改PT不能通过PCM内容校验。"""
        catalog = self.get("/v1/codecs")
        self.assertEqual(catalog["media_mode"], "g711_audio_graph")
        self.assertTrue(catalog["live_transcoding"])
        runtime = catalog["runtime_media"]
        self.assertEqual(runtime["processing"], "g711")
        self.assertTrue(runtime["ready"])
        self.assertEqual(runtime["healthy_workers"], runtime["expected_workers"])
        self.assertEqual({item["payload"] for item in catalog["live_codec_profiles"]}, {0, 8})
        # 保存实际目录响应；离线 SDK 是否探测成功与实时 G711 的握手能力各自独立。
        self.record("runtime_codec_catalog", fixture.json.dumps(catalog).encode(), ("http", 0))
        self.establish()
        a_frames = fixture.audio_frames("PCMU", 437, 32)
        b_frames = fixture.audio_frames("PCMA", 823, 32)
        got = self.exchange(self.audio_events("a", a_frames) + self.audio_events("b", b_frames))
        for side, inputs in (("a", b_frames), ("b", a_frames)):
            self.assert_timeline(got[side], side)
            self.assert_audio(got[side], side, inputs)
            self.assertGreaterEqual(got[side][0]["at"], 0.035, "未经过固定40ms播放缓冲")
            self.assertLess(got[side][0]["at"], 0.120, "首包超过固定图的有界播放窗口")
        stats = self.hangup()
        self.assertEqual(stats["processed_decoded_frames"], 64)
        self.assertEqual(stats["processed_decoded_samples"], 64 * SAMPLES)
        self.assertGreaterEqual(stats["processed_encoded_frames"], 64)
        self.assertEqual(stats["processed_encoded_samples"], stats["processed_encoded_frames"] * SAMPLES)
        self.assertEqual(stats["processed_send_errors"], 0)
        self.assertEqual(stats["processed_jitter_duplicates"], 0)
        self.assertEqual(stats["processed_jitter_reordered"], 0)

    def test_dynamic_a_pcma_and_static_b_pcmu_keep_independent_sdp(self):
        """A的动态PCMA编号与B的静态PCMU独立，不能把B选择错误回写给A。"""
        self.establish(a_law="PCMA", a_payload=96, b_law="PCMU", b_payload=0)
        a_frames = fixture.audio_frames("PCMA", 517, 24)
        b_frames = fixture.audio_frames("PCMU", 967, 24)
        got = self.exchange(self.audio_events("a", a_frames) + self.audio_events("b", b_frames))
        self.assert_timeline(got["a"], "a")
        self.assert_timeline(got["b"], "b")
        self.assert_audio(got["a"], "a", b_frames)
        self.assert_audio(got["b"], "b", a_frames)
        stats = self.hangup()
        self.assertEqual(stats["processed_decoded_frames"], 48)
        self.assertEqual(stats["processed_decoded_samples"], 48 * SAMPLES)

    def test_bounded_reorder_and_duplicate_do_not_duplicate_audio(self):
        """第4帧在第5帧之后但40ms期限内到达，第8帧重发；PCM仍严格原顺序且只出现一次。"""
        self.establish()
        a_frames = fixture.audio_frames("PCMU", 439, 28)
        b_frames = fixture.audio_frames("PCMA", 887, 28)
        events = self.audio_events("a", a_frames, offsets={4: 0.105})
        duplicate = next(raw for at, side, raw in events if at == 8 * STEP)
        events.append((8 * STEP + 0.004, "a", duplicate))
        got = self.exchange(events + self.audio_events("b", b_frames))
        self.assert_timeline(got["b"], "b")
        self.assert_audio(got["b"], "b", a_frames)
        self.assert_audio(got["a"], "a", b_frames)
        stats = self.hangup()
        self.assertGreaterEqual(stats["processed_jitter_reordered"], 1)
        self.assertGreaterEqual(stats["processed_jitter_duplicates"], 1)
        self.assertEqual(stats["processed_decoded_frames"], 56)

    def test_missing_frame_then_late_arrival_is_not_replayed(self):
        """第6帧过播放期限才送到，必须报告丢失/迟到，不能把旧语音插回后续时间线。"""
        self.establish()
        a_frames = fixture.audio_frames("PCMU", 431, 30)
        b_frames = fixture.audio_frames("PCMA", 829, 30)
        got = self.exchange(self.audio_events("a", a_frames, offsets={6: 0.310})
                            + self.audio_events("b", b_frames))
        self.assert_timeline(got["b"], "b")
        audio = [packet for packet in got["b"] if packet["payload"] == 8]
        self.assertGreaterEqual(len(audio), len(a_frames), "有历史的单帧缺失应得到有标记的PLC播放帧")
        # 缺失位置不拿发送脚本当成功证据；只对实际收到的其余帧逐一校验内容与位置。
        self.assert_audio(audio[:6], "b", a_frames[:6])
        suffix = audio[7:30]
        self.assert_audio(suffix, "b", a_frames[7:30])
        for first, second in zip(audio[:30], audio[1:30]):
            self.assertEqual((second["timestamp"] - first["timestamp"]) & 0xFFFFFFFF, SAMPLES)
        self.assert_audio(got["a"], "a", b_frames)
        stats = self.hangup()
        self.assertGreaterEqual(stats["processed_jitter_lost"], 1)
        self.assertGreaterEqual(stats["processed_jitter_late"], 1)
        self.assertGreaterEqual(stats["processed_plc_frames"], 1)
        self.assertEqual(stats["processed_decoded_frames"], 59, "迟到帧不能重新解码或挤掉后续有效帧")

    def test_interleaved_dtmf_and_cn_use_new_timeline_without_false_audio_loss(self):
        """辅助PT重封装而非音频解码；CN覆盖明确静默区，恢复后的真实音频仍正确。"""
        self.establish()
        a_frames = fixture.audio_frames("PCMU", 443, 32)
        b_frames = fixture.audio_frames("PCMA", 881, 32)
        events = self.audio_events("a", a_frames, omitted=range(14, 18)) + self.audio_events("b", b_frames)
        # 辅助包占源RTP序号却不等于丢了音频帧；严格单调重编号全部A腿输入。
        for index, (at, duration, end) in enumerate(((0.065, 160, False), (0.085, 320, False),
                                                   (0.105, 480, True), (0.125, 480, True), (0.145, 480, True))):
            body = bytes([5, 10 | (0x80 if end else 0)]) + struct.pack("!H", duration)
            events.append((at, "a", fixture.rtp(101, 0, SOURCE_TS["a"] + 3 * SAMPLES,
                                               body, SOURCE_SSRC["a"], index == 0)))
        events.append((14 * STEP, "a", fixture.rtp(13, 0, SOURCE_TS["a"] + 14 * SAMPLES,
                                                  bytes([42]), SOURCE_SSRC["a"])))
        counters = {"a": SOURCE_SEQ["a"], "b": SOURCE_SEQ["b"]}
        ordered = []
        for at, side, raw in sorted(events, key=lambda item: item[0]):
            value = bytearray(raw)
            value[2:4] = struct.pack("!H", counters[side] & 0xFFFF)
            counters[side] += 1
            ordered.append((at, side, bytes(value)))
        got = self.exchange(ordered)
        self.assert_timeline(got["b"], "b")
        self.assert_timeline(got["a"], "a")
        dtmf = [packet for packet in got["b"] if packet["payload"] == 101]
        cn = [packet for packet in got["b"] if packet["payload"] == 13]
        self.assertEqual(len(dtmf), 5)
        self.assertEqual([packet["body"] for packet in dtmf], [raw[12:] for _, side, raw in ordered
                                                               if side == "a" and raw[1] & 127 == 101])
        self.assertEqual(len({packet["timestamp"] for packet in dtmf}), 1)
        self.assertEqual([packet["body"] for packet in cn], [bytes([42])])
        self.assert_audio(got["b"], "b", a_frames, indexes=[index for index in range(32) if index not in range(14, 18)])
        self.assert_audio(got["a"], "a", b_frames)
        audio = [packet for packet in got["b"] if packet["payload"] == 8]
        self.assertEqual((dtmf[0]["timestamp"] - audio[0]["timestamp"]) & 0xFFFFFFFF, 3 * SAMPLES)
        self.assertEqual((cn[0]["timestamp"] - audio[0]["timestamp"]) & 0xFFFFFFFF, 14 * SAMPLES)
        stats = self.hangup()
        self.assertEqual(stats["processed_cn_packets"], 1)
        self.assertEqual(stats["processed_decoded_frames"], 60)
        self.assertEqual(stats["processed_jitter_duplicates"], 0, "辅助PT不能冒充重复音频")

    def test_bye_reclaims_graph_and_stale_source_cannot_feed_reused_ports(self):
        """处理图释放后旧端点不能污染新通话；新输入须重新生成正确内容与TX身份。"""
        self.establish()
        a_frames = fixture.audio_frames("PCMU", 457, 12)
        b_frames = fixture.audio_frames("PCMA", 853, 12)
        got = self.exchange(self.audio_events("a", a_frames) + self.audio_events("b", b_frames))
        self.assert_audio(got["b"], "b", a_frames)
        old_sender, old_target = self.ar, self.a_ports[0]
        old_ssrc = got["b"][0]["ssrc"]
        first_stats = self.hangup()
        self.assertEqual(first_stats["processed_decoded_frames"], 24)
        self.ar, self.ac, self.br, self.bc = [self.sock() for _ in range(4)]
        self.establish()
        self.assertEqual(self.a_ports[0], old_target, "端口复用负例没有实际复用")
        stale = fixture.rtp(0, 400, 50000, a_frames[0], SOURCE_SSRC["a"])
        self.record("rtp_stale_source", stale, ("127.0.0.1", old_target))
        old_sender.sendto(stale, ("127.0.0.1", old_target))
        self.br.settimeout(0.10)
        with self.assertRaises(socket.timeout):
            self.br.recvfrom(4096)
        fresh_a = fixture.audio_frames("PCMU", 619, 12)
        fresh_b = fixture.audio_frames("PCMA", 1091, 12)
        received = self.exchange(self.audio_events("a", fresh_a) + self.audio_events("b", fresh_b))
        self.assert_timeline(received["b"], "b")
        self.assert_audio(received["b"], "b", fresh_a)
        self.assert_audio(received["a"], "a", fresh_b)
        self.assertNotEqual(received["b"][0]["ssrc"], old_ssrc, "复用会话不能继承旧发送身份")
        final_stats = self.hangup()
        self.assertEqual(final_stats["processed_decoded_frames"], 48, "第二次清理不能沿用第一通的旧统计")

    def test_non_g711_and_non_20ms_offers_are_rejected_before_upstream(self):
        """首个图的范围必须诚实：G722和10/30ms不能无声回落relay再返回接通。"""
        self.rejection_statuses = []
        for law, payload, ptime in (("G722", 9, 20), ("PCMU", 0, 10), ("PCMA", 8, 30)):
            with self.subTest(law=law, ptime=ptime):
                request = self.offer(law, payload, ptime=ptime)
                call_id = e2e.parse(request)[1]["call-id"]
                rejected, _ = self.receive_sip(self.a, lambda item: item[0].startswith("SIP/2.0 488")
                                                and item[1]["call-id"] == call_id)
                headers = e2e.parse(rejected)[1]
                branch = e2e.parse(request)[1]["via"].split("branch=", 1)[1].split(";", 1)[0]
                self.send_sip(self.a, e2e.message("ACK", "sip:100@local", self.a.getsockname()[1],
                                                call_id, branch, to=headers["to"]), self.address)
                self.upstream.settimeout(0.10)
                with self.assertRaises(socket.timeout):
                    self.upstream.recvfrom(65536)
                status = self.wait_status(lambda state: state["active_calls"] == 0 and state["established_calls"] == 0
                                 and all(worker["stats"]["processed_active_calls"] == 0 for worker in state["workers"]),
                                 require_stats=True)
                # 保存刚刚实际读取的拒绝状态；拒绝场景不存在 BYE，不能补写接通场景的 final_status。
                status["observed_at"] = time.time()
                self.rejection_statuses.append(status)


def main():
    """明确选择M1二进制；--list不绑定任何端口，其结果不算产品执行证据。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--control", type=Path, default=fixture.CONTROL)
    parser.add_argument("--media", type=Path, default=fixture.MEDIA)
    parser.add_argument("--connected", action="store_true")
    parser.add_argument("--artifacts", type=Path)
    parser.add_argument("--list", action="store_true")
    args, remaining = parser.parse_known_args()
    if args.list:
        for name in unittest.TestLoader().getTestCaseNames(ProcessedMediaIntegration):
            print("ProcessedMediaIntegration." + name)
        return
    fixture.CONTROL, fixture.MEDIA = args.control.resolve(), args.media.resolve()
    fixture.CONNECTED = args.connected
    fixture.ARTIFACTS = args.artifacts.resolve() if args.artifacts else None
    if fixture.ARTIFACTS:
        fixture.ARTIFACTS.mkdir(parents=True, exist_ok=True)
    for path in (fixture.CONTROL, fixture.MEDIA):
        if not path.is_file():
            parser.error(f"待测二进制不存在：{path}")
    unittest.main(argv=[__file__, *remaining], verbosity=2)


if __name__ == "__main__":
    main()
