#!/usr/bin/env python3
"""受限XML路由的真实SIP/Rust媒体回归；独立端口/进程，不重启主服务或伪造上游。"""
import argparse
import json
import math
from pathlib import Path
import socket
import struct
import time
import unittest
import uuid
import wave

import e2e
import esl_applications_e2e as applications


class DialplanFixture(applications.ApplicationFixture):
    """按每个用例写独立冻结XML；动作不通过HTTP草稿假定已加载。"""
    needs_file = False
    actions = [('answer', ''), ('set', 'customer=中文客户'), ('sleep', '200'), ('set', 'after=done'), ('park', '')]

    def configure(self, config):
        import xml.etree.ElementTree as ET
        root = ET.Element('include')
        context = ET.SubElement(root, 'context', name='default')
        extension = ET.SubElement(context, 'extension', name='owned-ivr')
        condition = ET.SubElement(extension, 'condition', field='destination_number', expression='^7101$')
        for application, data in self.actions:
            ET.SubElement(condition, 'action', application=application, data=data)
        file = Path(self.tmp.name) / 'dialplan.xml'
        file.write_bytes(ET.tostring(root, encoding='utf-8'))
        config['sip']['dialplan'] = {'file': str(file), 'context': 'default'}
        config['sip']['local_extensions'] = ['9999']
        if self.needs_file:
            config['media']['playback_root'] = self.tmp.name
        # 1kHz真实PCM16波形；文件不是固定无声或随机字节，通过远端RTP校验播放。
        with wave.open(str(Path(self.tmp.name) / 'prompt.wav'), 'wb') as wav:
            wav.setnchannels(1)
            wav.setsampwidth(2)
            wav.setframerate(8000)
            wav.writeframes(b''.join(struct.pack('<h', int(10000 * math.sin(2 * math.pi * 1000 * i / 8000))) for i in range(1600)))
        (Path(self.tmp.name) / 'invalid.wav').write_bytes(b'invalid file')

    def call(self, number='7101', ack=True, expected=200):
        call_id, branch = uuid.uuid4().hex, 'z9hG4bK' + uuid.uuid4().hex
        invite = e2e.message('INVITE', f'sip:{number}@local', self.a.getsockname()[1], call_id, branch, e2e.sdp(self.ar.getsockname()[1], self.ac.getsockname()[1]))
        self.a.sendto(invite, self.address)
        accepted, _ = e2e.receive(self.a, lambda v: v[0].startswith(f'SIP/2.0 {expected}'))
        if ack:
            headers = e2e.parse(accepted)[1]
            self.a.sendto(e2e.message('ACK', f'sip:{number}@local', self.a.getsockname()[1], call_id, 'z9hG4bK' + uuid.uuid4().hex, to=headers['to']), self.address)
        return accepted, invite

    def server_hangup(self, timeout=4):
        bye, peer = e2e.receive(self.a, lambda v: v[0].startswith('BYE '), timeout)
        self.a.sendto(e2e.response(bye), peer)
        self.wait_active(0)
        return bye

    def no_upstream(self):
        self.upstream.settimeout(0.05)
        with self.assertRaises(socket.timeout):
            self.upstream.recvfrom(2048)


class XMLIntegration(DialplanFixture):
    def test_xml_answer_sleep_order_and_park(self):
        began = time.monotonic()
        self.call()
        create = self.esl.wait_events('CHANNEL_CREATE', 1)[0]
        self.assertNotIn('Other-Leg-Unique-ID', create)
        channel = create['Unique-ID']
        deadline = time.monotonic() + 3
        while True:
            self.esl.api('echo synchronize-events')
            parked = [e for e in self.esl.events if e['Event-Name'] == 'CHANNEL_EXECUTE' and e.get('Application') == 'park']
            if parked:
                break
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.02)
        self.assertGreaterEqual(time.monotonic() - began, 0.19)
        completed = [e for e in self.esl.events if e['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE']
        self.assertEqual([e['Application'] for e in completed], ['answer', 'set', 'sleep', 'set'])
        self.assertEqual(completed[-1]['variable_customer'], '中文客户')
        self.assertEqual(completed[-1]['variable_after'], 'done')
        self.assertTrue(all(e['Application-Response'] == '_none_' for e in completed))
        self.no_upstream()
        self.assertEqual(self.esl.api(f'uuid_kill {channel}'), '+OK\n')
        self.server_hangup()

    def test_xml_unmatched_404_retransmission_ignores_legacy_local(self):
        response, invite = self.call(number='9999', ack=False, expected=404)
        self.a.sendto(invite, self.address)
        repeated, _ = e2e.receive(self.a, lambda v: v[0].startswith('SIP/2.0 404'))
        self.assertEqual(response, repeated)
        self.assertEqual(self.get('/v1/status')['active_calls'], 0)
        self.no_upstream()

    def test_xml_late_cancel_wrong_ack_and_duplicate_ack_do_not_repeat_plan(self):
        accepted, invite = self.call(ack=False)
        headers, initial = e2e.parse(accepted)[1], e2e.parse(invite)[1]
        call_id = headers['call-id']
        branch = initial['via'].split('branch=', 1)[1].split(';', 1)[0]
        cancel = e2e.message('CANCEL', 'sip:7101@local', self.a.getsockname()[1], call_id, branch, to=initial['to'])
        self.a.sendto(cancel, self.address)
        e2e.receive(self.a, lambda v: v[0].startswith('SIP/2.0 200') and v[1]['cseq'].endswith('CANCEL'))
        wrong = e2e.message('ACK', 'sip:7101@local', self.a.getsockname()[1], call_id, 'z9hG4bK' + uuid.uuid4().hex, to='<sip:7101@local>;tag=wrong')
        self.a.sendto(wrong, self.address)
        time.sleep(0.03)
        self.assertEqual(self.get('/v1/status')['established_calls'], 0)
        right = e2e.message('ACK', 'sip:7101@local', self.a.getsockname()[1], call_id, 'z9hG4bK' + uuid.uuid4().hex, to=headers['to'])
        self.a.sendto(right, self.address)
        self.a.sendto(right, self.address)
        time.sleep(0.25)
        self.esl.api('echo after-duplicate-ack')
        starts = [e.get('Application') for e in self.esl.events if e['Event-Name'] == 'CHANNEL_EXECUTE']
        self.assertEqual(starts, ['answer', 'set', 'sleep', 'set', 'park'])
        channel = next(e['Unique-ID'] for e in self.esl.events if e['Event-Name'] == 'CHANNEL_CREATE')
        self.esl.api(f'uuid_kill {channel}')
        self.server_hangup()
        self.no_upstream()

    def test_xml_answer_waits_for_ack_and_reclaims_on_missing_ack(self):
        self.call(ack=False)
        channel = self.esl.wait_events('CHANNEL_CREATE', 1)[0]['Unique-ID']
        self.wait_active(0)
        self.esl.wait_events('CHANNEL_HANGUP', 1)
        started = [e.get('Application') for e in self.esl.events if e['Event-Name'] == 'CHANNEL_EXECUTE']
        self.assertEqual(started, ['answer'])
        self.assertEqual(self.esl.api(f'uuid_exists {channel}'), 'false')
        self.no_upstream()


class XMLWithoutESL(DialplanFixture):
    esl_enabled = False
    actions = [('answer', ''), ('read', '2 4 silence digits 3000 #'), ('sleep', '100'), ('hangup', '')]

    def test_xml_dtmf_and_hangup_execute_without_esl(self):
        accepted, _ = self.call()
        deadline = time.monotonic() + 2
        while self.get('/v1/status')['established_calls'] != 1:
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.01)
        port = e2e.media_ports(e2e.parse(accepted)[2])[0]
        self.send_digit(port, '1')
        self.a.settimeout(0.15)
        with self.assertRaises(socket.timeout):
            self.a.recvfrom(2048)
        self.send_digit(port, '2')
        self.send_digit(port, '#')
        self.server_hangup()
        self.no_upstream()
        self.assertEqual(self.get('/v1/status')['established_calls'], 0)


class XMLFileIntegration(DialplanFixture):
    needs_file = True
    actions = [('answer', ''), ('playback', 'prompt.wav'), ('set', 'played=yes'), ('hangup', '')]

    def test_xml_wav_real_packets_then_hangup(self):
        self.call()
        self.ar.settimeout(3)
        arrived = []
        packets = []
        for _ in range(10):
            packets.append(self.ar.recvfrom(2048)[0])
            arrived.append(time.monotonic())
        self.assertGreater(arrived[-1] - arrived[0], 0.12, "WAV frames were burst instead of paced")
        def decode_mulaw(value):
            # 独立展开标准μ-law并对比原PCM波形；1kHz/8kHz仅数个量化值，不能误用值种类数判断音频。
            value = (~value) & 255
            magnitude = (((value & 15) << 3) + 132) << ((value >> 4) & 7)
            return 132 - magnitude if value & 128 else magnitude - 132
        for index, packet in enumerate(packets):
            self.assertEqual(len(packet), 172)
            self.assertEqual(packet[1] & 127, 0)
            for sample_index, value in enumerate(packet[12:]):
                expected = int(10000 * math.sin(2 * math.pi * 1000 * (index * 160 + sample_index) / 8000))
                self.assertLessEqual(abs(decode_mulaw(value) - expected), 256)
            if index:
                self.assertEqual((struct.unpack('!H', packet[2:4])[0] - struct.unpack('!H', packets[index-1][2:4])[0]) & 65535, 1)
                self.assertEqual((struct.unpack('!I', packet[4:8])[0] - struct.unpack('!I', packets[index-1][4:8])[0]) & 0xFFFFFFFF, 160)
        self.server_hangup()
        self.esl.wait_events('CHANNEL_HANGUP', 1)
        completed = [e for e in self.esl.events if e['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE']
        playback = next(e for e in completed if e.get('Application') == 'playback')
        self.assertEqual(playback['Application-Response'], 'FILE PLAYED')
        self.assertEqual(next(e for e in completed if e.get('Application') == 'set')['variable_played'], 'yes')
        self.no_upstream()


class XMLFileFailure(DialplanFixture):
    needs_file = True
    actions = [('answer', ''), ('playback', 'invalid.wav'), ('set', 'after=must-not-run'), ('hangup', '')]

    def test_xml_invalid_wav_fails_and_never_runs_later_actions(self):
        self.call()
        self.server_hangup()
        self.esl.wait_events('CHANNEL_HANGUP', 1)
        complete = [e for e in self.esl.events if e['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE']
        playback = next(e for e in complete if e.get('Application') == 'playback')
        self.assertTrue(playback['Application-Response'].startswith('-ERR'), playback)
        self.assertFalse(any(e.get('Application') == 'set' for e in complete))
        self.no_upstream()


class XMLFilePrompt(DialplanFixture):
    needs_file = True
    actions = [('answer', ''), ('read', '1 1 prompt.wav choice 2000 #'), ('hangup', '')]

    def test_xml_read_wav_prompt_actual_dtmf_barge_in(self):
        accepted, _ = self.call()
        self.ar.settimeout(3)
        first = self.ar.recvfrom(2048)[0]
        self.assertEqual(len(first), 172)
        self.send_digit(e2e.media_ports(e2e.parse(accepted)[2])[0], '7')
        self.server_hangup()
        self.esl.wait_events('CHANNEL_HANGUP', 1)
        read = next(e for e in self.esl.events if e['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE' and e.get('Application') == 'read')
        self.assertEqual(read['variable_choice'], '7')
        self.assertEqual(read['variable_read_result'], 'success')
        self.assertEqual(read['Application-Response'], '_none_')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--control', type=Path, default=e2e.CONTROL)
    parser.add_argument('--media', type=Path, default=e2e.MEDIA)
    parser.add_argument('--connected', action='store_true')
    args, remaining = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    unittest.main(argv=[__file__] + remaining, verbosity=2)
