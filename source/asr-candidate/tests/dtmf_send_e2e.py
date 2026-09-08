#!/usr/bin/env python3
"""真实UDP和SIP/ESL发送按键回归；只启动本夹具进程，不访问外部线路或共享后台。"""
import argparse
import json
import os
from pathlib import Path
import socket
import signal
import struct
import time
import unittest

import e2e
import media_interaction_e2e as media_fixture
import esl_applications_e2e as applications
import playback_controls_e2e as controls


def packets(sock, duration):
    """按总期限取实际报文，超时结束收集而不增加假包。"""
    result, deadline = [], time.monotonic() + duration
    while time.monotonic() < deadline:
        sock.settimeout(max(.001, deadline - time.monotonic()))
        try:
            result.append(sock.recvfrom(4096)[0])
        except socket.timeout:
            break
    return result


def assert_events(case, wire, digits, duration, clock=8000, payload=101):
    """从线上字节独立分组，不引用生产解析器；每数字起点固定且必须有三个相同结束包。"""
    events = [p for p in wire if p[1] & 127 == payload]
    groups = []
    for packet in events:
        case.assertEqual(len(packet), 16)
        key = (packet[12], packet[4:8])
        if not groups or groups[-1][0] != key:
            groups.append((key, []))
        groups[-1][1].append(packet)
    case.assertEqual(["0123456789*#ABCD"[key[0]] for key, _ in groups], list(digits))
    for _, group in groups:
        case.assertTrue(group[0][1] & 128)
        case.assertTrue(all(not (p[1] & 128) for p in group[1:]))
        durations = [struct.unpack('!H', p[14:16])[0] for p in group]
        case.assertEqual(durations, sorted(durations))
        endings = [p for p in group if p[13] & 128]
        case.assertEqual(len(endings), 3)
        case.assertTrue(all(p[12:] == endings[0][12:] for p in endings))
        case.assertEqual(durations[-1], duration * clock // 1000)
        case.assertTrue(all(p[13] & 64 == 0 for p in group))
    return len(events)


def assert_sequence(case, wire):
    """音频与按键共同检查一个SSRC和连续包序，按键timestamp不要求跟音频逐包增长。"""
    case.assertGreater(len(wire), 2)
    case.assertEqual(len({p[8:12] for p in wire}), 1)
    sequences = [struct.unpack('!H', p[2:4])[0] for p in wire]
    case.assertTrue(all(b == (a + 1) % 65536 for a, b in zip(sequences, sequences[1:])), sequences)


class DTMFWorker(unittest.TestCase):
    """复用有所有权的worker夹具，所有断言直接读取真实UDP。"""
    setUp = media_fixture.MediaInteraction.setUp
    close = media_fixture.MediaInteraction.close
    sock = media_fixture.MediaInteraction.sock
    reply = media_fixture.MediaInteraction.reply
    command = media_fixture.MediaInteraction.command
    allocate = media_fixture.MediaInteraction.allocate

    def send(self, digits, duration=55, **extra):
        return self.command('dtmf_send', session=1, leg='a', digits=digits, duration_ms=duration, **extra)

    def test_all_sixteen_digits_exact_duration_and_state(self):
        self.allocate(connect=False)
        self.assertTrue(self.send('0123456789*#ABCD')['ok'])
        wire = packets(self.ar, 2.7)
        count = assert_events(self, wire, '0123456789*#ABCD', 55)
        assert_sequence(self, wire)
        state = self.command('dtmf_send_status', session=1)
        self.assertEqual((state['state'], state['completed_digits'], state['queued_digits'], state['failed_digits'], state['sent_packets']), ('idle', 16, 0, 0, count))

    def test_playback_keeps_sequence_audio_clock_and_ssrc_across_dtmf_and_next_play(self):
        self.allocate(connect=False)
        self.assertTrue(self.command('playback_start', session=1, playback_id=1, leg='a', frequency_hz=440, duration_ms=500)['ok'])
        wire = packets(self.ar, .09)
        self.assertTrue(self.send('5', 100)['ok'])
        wire += packets(self.ar, .5)
        self.assertTrue(self.command('playback_start', session=1, playback_id=2, leg='a', frequency_hz=440, duration_ms=100)['ok'])
        wire += packets(self.ar, .2)
        assert_sequence(self, wire)
        assert_events(self, wire, '5', 100)
        audio = [p for p in wire if p[1] & 127 == 0]
        self.assertEqual(len(audio), 30)
        stamps = [struct.unpack('!I', p[4:8])[0] for p in audio]
        self.assertTrue(all(b - a == 160 for a, b in zip(stamps[:25], stamps[1:25])))
        self.assertGreater(stamps[25], stamps[24])
        self.assertTrue(all(b - a == 160 for a, b in zip(stamps[25:], stamps[26:])))

    def test_queue_limit_is_atomic_and_release_cancels(self):
        self.allocate(connect=False)
        self.assertTrue(self.send('1' * 32, 1000)['ok'])
        self.assertFalse(self.send('2')['ok'])
        self.assertEqual(self.command('dtmf_send_status', session=1)['accepted_digits'], 32)
        packets(self.ar, .04)
        self.assertTrue(self.command('release', session=1)['ok'])
        self.assertLessEqual(len(packets(self.ar, .16)), 1)
        self.assertFalse(self.command('dtmf_send_status', session=1)['ok'])
        stats = self.command('stats')['stats']
        self.assertEqual((stats['dtmf_send_active'], stats['dtmf_send_cancelled_digits']), (0, 32))

    def test_invalid_batch_negotiation_and_leg_never_partially_send(self):
        self.allocate(connect=False, events='0-9')
        for digits, duration in [('1#', 55), ('1w', 55), ('1a', 55), ('', 55), ('1', 49), ('1', 1001)]:
            self.assertFalse(self.send(digits, duration)['ok'])
        self.assertFalse(self.command('dtmf_send', session=1, leg='b', digits='1', duration_ms=55)['ok'])
        self.assertEqual(packets(self.ar, .12), [])
        self.assertEqual(self.command('dtmf_send_status', session=1)['accepted_digits'], 0)

    def test_bridge_rejection_preserves_both_rtp_and_rtcp(self):
        self.allocate()
        self.assertFalse(self.send('1')['ok'])
        for source, target, port in [(self.ar, self.br, self.base), (self.br, self.ar, self.base + 2)]:
            packet = media_fixture.rtp(0, 123, 560, b'\xff' * 160)
            source.sendto(packet, ('127.0.0.1', port))
            self.assertEqual(target.recv(4096), packet)
        rtcp = struct.pack('!BBHI', 0x80, 201, 1, 0x12345678)
        self.ac.sendto(rtcp, ('127.0.0.1', self.base + 1))
        self.assertEqual(self.bc.recv(4096), rtcp)

    def test_opus_48k_and_g722_8k_timestamp_clock(self):
        for session, payload, name, sample, clock, channels in [(1, 111, 'OPUS', 48000, 48000, 2), (2, 9, 'G722', 16000, 8000, 1)]:
            fields = dict(session=session, payload=payload, dtmf_payload=110, dtmf_clock_rate=clock, dtmf_events='0-15', codec=dict(name=name, sample_rate=sample, rtp_clock_rate=clock, channels=channels, ptime_ms=20, fmtp=''))
            peer = dict(rtp=f'127.0.0.1:{self.ar.getsockname()[1]}', rtcp=f'127.0.0.1:{self.ac.getsockname()[1]}')
            self.assertTrue(self.command('allocate', a=peer, **fields)['ok'])
            self.assertTrue(self.command('dtmf_send', session=session, leg='a', digits='A', duration_ms=55)['ok'])
            assert_events(self, packets(self.ar, .2), 'A', 55, clock, 110)
            self.assertTrue(self.command('release', session=session)['ok'])

    def test_unnegotiated_or_mismatched_clock_fails(self):
        self.allocate(connect=False, clock=48000)
        self.assertFalse(self.send('1')['ok'])
        self.assertEqual(packets(self.ar, .1), [])

    def test_scheduler_failure_is_visible_and_does_not_stall_other_media(self):
        self.allocate(connect=False)
        self.assertTrue(self.send('12', 1000)['ok'])
        self.ar.recv(2048)
        # 故障注入只暂停此测试直接创建的worker；finally必恢复，绝不搜索或操作共享进程。
        os.kill(self.process.pid, signal.SIGSTOP)
        try:
            time.sleep(.14)
        finally:
            os.kill(self.process.pid, signal.SIGCONT)
        time.sleep(.02)
        state = self.command('dtmf_send_status', session=1)
        self.assertEqual((state['state'], state['failed_digits'], state['queued_digits']), ('failed', 2, 0))
        self.assertEqual(state['last_error'], 'dtmf_send_scheduler_late')
        self.assertEqual(self.command('stats')['stats']['dtmf_send_errors'], 1)
        packets(self.ar, .03)
        self.assertTrue(self.command('playback_start', session=1, playback_id=1, leg='a', frequency_hz=440, duration_ms=100)['ok'])
        self.assertEqual(len(packets(self.ar, .18)), 5)

    def test_activated_local_stream_rejects_connect_repeat_without_mutation(self):
        self.allocate(connect=False)
        self.assertTrue(self.send('5')['ok'])
        assert_events(self, packets(self.ar, .2), '5', 55)
        peer = dict(rtp=f'127.0.0.1:{self.br.getsockname()[1]}', rtcp=f'127.0.0.1:{self.bc.getsockname()[1]}')
        for _ in range(2):
            reply = self.command('connect', session=1, b=peer, payload=0, dtmf_payload=101)
            self.assertFalse(reply['ok'])
            self.assertIn('transparent bridge', reply['message'])
        state = self.command('dtmf_send_status', session=1)
        self.assertEqual((state['accepted_digits'], state['completed_digits']), (1, 1))
        self.assertTrue(self.send('6')['ok'])
        assert_events(self, packets(self.ar, .2), '6', 55)
        self.assertEqual(packets(self.br, .05), [])


class DTMFAPI(applications.ApplicationFixture):
    local = True
    call = controls.PlaybackControls.call
    channel = controls.PlaybackControls.channel
    server_hangup = controls.PlaybackControls.server_hangup
    finish_call = controls.PlaybackControls.finish_call

    def test_api_sends_actual_digits_and_reports_complete(self):
        channel, _ = self.channel()
        self.assertEqual(self.esl.api('uuid_send_dtmf ' + channel + ' 12#@55'), f'+OK {channel} sent DTMF 12#@55.\n')
        wire = packets(self.ar, .7)
        count = assert_events(self, wire, '12#', 55)
        state = json.loads(self.esl.api('uuid_send_dtmf_status ' + channel))
        self.assertEqual((state['state'], state['completed_digits'], state['sent_packets']), ('idle', 3, count))
        self.assertEqual(state['uuid'], channel)
        self.finish_call(channel)
        self.assertEqual(self.esl.api('uuid_send_dtmf ' + channel + ' 1'), '-ERR Cannot locate session!\n')

    def test_api_rejects_bad_parameters_and_info_mode_without_packets(self):
        channel, _ = self.channel()
        for value in ['12w', 'a', '1+2', '~1', '1@49', '1@1001', '1@55x', '1 2', '1' * 33]:
            self.assertTrue(self.esl.api('uuid_send_dtmf ' + channel + ' ' + value).startswith('-ERR'))
        self.assertTrue(self.esl.api('uuid_send_dtmf').startswith('-USAGE'))
        self.assertEqual(self.esl.api('uuid_setvar ' + channel + ' dtmf_type INFO'), '+OK\n')
        self.assertIn('rfc2833', self.esl.api('uuid_send_dtmf ' + channel + ' 1'))
        self.assertEqual(packets(self.ar, .12), [])
        self.assertEqual(json.loads(self.esl.api('uuid_send_dtmf_status ' + channel))['accepted_digits'], 0)
        self.finish_call(channel)

    def test_api_queue_and_hangup_reclaim_without_replaying_old_digits(self):
        channel, _ = self.channel()
        self.assertTrue(self.esl.api('uuid_send_dtmf ' + channel + ' ' + '1' * 32 + '@1000').startswith('+OK'))
        self.assertIn('queue full', self.esl.api('uuid_send_dtmf ' + channel + ' 2'))
        packets(self.ar, .05)
        self.finish_call(channel)
        packets(self.ar, .05)
        self.assertEqual(packets(self.ar, .15), [])


class DTMFBridgeAPI(applications.ApplicationFixture):
    def test_bridged_a_and_b_legs_reject_without_disrupting_real_media(self):
        a, b, outgoing, accepted = self.established_channel()
        for channel in [a, b]:
            self.assertIn('local A leg only', self.esl.api('uuid_send_dtmf ' + channel + ' 1'))
        self.check_media(outgoing, accepted)
        self.hangup(accepted)
        self.wait_active(0)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--control', type=Path, default=e2e.CONTROL)
    parser.add_argument('--media', type=Path, default=e2e.MEDIA)
    parser.add_argument('--connected', action='store_true')
    args, rest = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    media_fixture.MEDIA, media_fixture.CONNECTED = e2e.MEDIA, args.connected
    unittest.main(argv=[__file__] + rest, verbosity=2)
