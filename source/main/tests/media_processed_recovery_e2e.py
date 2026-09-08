#!/usr/bin/env python3
"""验证发送方停顿后的真实 G.711 恢复；独立于固定 M1 22 项验收和容量结论。

保持同一 SSRC、序号及 RTP 时间戳，故意延迟一方向的输入再恢复正常发送。
暂停期间允许明确的过期与 PLC，恢复后的新音频必须保持原时间映射和完整内容。
"""
import argparse
from pathlib import Path
import unittest

import media_processed_fixture as fixture
from media_processed_fixture import ProcessedFixture, SAMPLES, STEP


class ProcessedSourceRecovery(ProcessedFixture):
    def check_source_pause(self, paused):
        """只改变夹具的发送计划，不暂停服务器或忽略任何恢复后的内容错误。"""
        self.establish()
        inputs = {"a": fixture.audio_frames("PCMU", 467, 200),
                  "b": fixture.audio_frames("PCMA", 911, 200)}
        events = []
        for side in ("a", "b"):
            # 1.2秒至1.383秒模拟发送方调度停顿，保留这段真实输入后一次到达。
            # 后续仍使用原20ms时钟；不能把晚到包重新标为新帧来掩盖时间轴漂移。
            offsets = {index: 1.383 for index in range(60, 70)} if side == paused else None
            events.extend(self.audio_events(side, inputs[side], offsets=offsets))
        received = self.exchange(events)
        for side, source in (("a", "b"), ("b", "a")):
            self.assert_timeline(received[side], side)
            audio = [packet for packet in received[side] if packet["payload"] == self.codecs[side][1]]
            self.assertTrue(audio, "没有收到真实音频")
            positions = []
            for packet in audio:
                offset = (packet["timestamp"] - audio[0]["timestamp"]) & 0xFFFFFFFF
                self.assertEqual(offset % SAMPLES, 0,
                                 "同源连续时间戳恢复后被墙钟重锚，离开原160ticks格点")
                position = offset // SAMPLES
                positions.append(position)
                if position >= len(inputs[source]):
                    continue  # 有限停止输入尾部PLC不当作真实源音频验证。
                if source != paused or position < 60 or position >= 72:
                    self.assert_audio([packet], side, [inputs[source][position]])
            self.assertEqual(positions, sorted(set(positions)), "恢复后旧时间位置被重放")
            self.assertTrue(set(range(72, 200)).issubset(positions),
                            "恢复后的新语音仍缺失或被错误时间轴错配")
            if source != paused:
                self.assertTrue(set(range(200)).issubset(positions), "另一方向受到源暂停影响")
        stats = self.hangup()
        self.assertGreater(stats["processed_jitter_late"], 0, "夹具没有触发真实晚到输入")
        self.assertEqual(stats["processed_source_resets"], 0, "暂停不应重建RTP来源")
        self.assertEqual(stats["processed_send_errors"], 0)

    def test_paused_pcmu_source_recovers_without_shifting_other_leg(self):
        """PCMU发送方停顿，PCMA接收轨恢复后保持原来的音频内容和时间位置。"""
        self.check_source_pause("a")

    def test_paused_pcma_source_recovers_without_shifting_other_leg(self):
        """反向PCMA发送方停顿，同样不能影响对向实时收音或持续改变RTP时钟。"""
        self.check_source_pause("b")


def main():
    """每轮显式指定实际候选二进制；缺失、绑定失败或不支持处理模式均真实失败。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--control", type=Path, required=True)
    parser.add_argument("--media", type=Path, required=True)
    parser.add_argument("--connected", action="store_true")
    parser.add_argument("--artifacts", type=Path, required=True)
    args, remaining = parser.parse_known_args()
    fixture.CONTROL, fixture.MEDIA = args.control.resolve(), args.media.resolve()
    fixture.CONNECTED, fixture.ARTIFACTS = args.connected, args.artifacts.resolve()
    fixture.ARTIFACTS.mkdir(parents=True, exist_ok=False)
    for path in (fixture.CONTROL, fixture.MEDIA):
        if not path.is_file():
            parser.error("待测二进制不存在：" + str(path))
    unittest.main(argv=[__file__, *remaining], verbosity=2)


if __name__ == "__main__":
    main()
