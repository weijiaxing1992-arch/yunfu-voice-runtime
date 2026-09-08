#!/usr/bin/env python3
"""真实媒体停止/播放终止键/缺文件回归；控制成功与实际停音、读号完成分别验证。"""
import argparse
from pathlib import Path
import socket
import time
import unittest
import uuid

import e2e
import esl_applications_e2e as applications
import dialplan_e2e as dialplan


class PlaybackControls(applications.ApplicationFixture):
    local = True
    call = dialplan.DialplanFixture.call
    server_hangup = dialplan.DialplanFixture.server_hangup

    def configure(self, config):
        config['media']['playback_root'] = self.tmp.name

    def channel(self):
        accepted, _ = self.call('1000')
        channel = self.esl.wait_events('CHANNEL_CREATE', 1)[0]['Unique-ID']
        deadline = time.monotonic() + 2
        while self.get('/v1/status')['established_calls'] != 1:
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.01)
        return channel, e2e.media_ports(e2e.parse(accepted)[2])[0]

    def first_audio(self):
        self.ar.settimeout(2)
        data = self.ar.recvfrom(2048)[0]
        self.assertEqual(len(data), 172)
        self.assertEqual(data[1] & 127, 0)

    def quiet(self):
        # 清空在完成确认之前已发送并抵达socket的有限尾包，再确认不会恢复发送。
        end = time.monotonic() + 0.15
        while time.monotonic() < end:
            self.ar.settimeout(0.02)
            try:
                self.ar.recvfrom(2048)
            except socket.timeout:
                break
        self.ar.settimeout(0.08)
        with self.assertRaises(socket.timeout):
            self.ar.recvfrom(2048)

    def finish_call(self, channel):
        self.assertEqual(self.esl.api('uuid_kill ' + channel), '+OK\n')
        self.server_hangup()

    def test_default_and_custom_terminators_record_real_key(self):
        channel, port = self.channel()
        for mode, key in [(None, '*'), ('#', '#')]:
            with self.subTest(mode=mode):
                if mode:
                    token = self.execute(channel, 'set', 'playback_terminators=' + mode)
                    self.application_event(token)
                play = self.execute(channel, 'playback', 'tone_stream://%(2000,0,440)')
                self.first_audio()
                self.send_digit(port, key)
                result = self.application_event(play)
                self.assertEqual(result['Application-Response'], 'FILE PLAYED')
                self.assertEqual(result['variable_playback_terminator_used'], key)
                self.quiet()
        self.finish_call(channel)

    def test_none_ignores_digit_then_uuid_break_confirms_silence(self):
        channel, port = self.channel()
        self.application_event(self.execute(channel, 'set', 'playback_terminators=NoNe'))
        play = self.execute(channel, 'playback', 'tone_stream://%(2000,0,440)')
        self.first_audio()
        self.send_digit(port, '*')
        time.sleep(0.1)
        self.esl.api('echo event-barrier')
        self.assertFalse(any(e.get('Application-UUID') == play and e['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE' for e in self.esl.events))
        self.assertEqual(self.esl.api('uuid_break ' + channel), '+OK\n')
        result = self.application_event(play)
        self.assertEqual(result['Application-Response'], 'FILE PLAYED')
        self.assertNotIn('variable_playback_terminator_used', result)
        self.quiet()
        self.assertEqual(self.get('/v1/status')['established_calls'], 1)
        self.assertTrue(self.esl.api('uuid_break ' + channel + ' all').startswith('-ERR'))
        self.finish_call(channel)

    def test_read_break_stops_prompt_but_waits_for_failure_timeout(self):
        channel, _ = self.channel()
        read = self.execute(channel, 'read', '1 3 tone_stream://%(2000,0,440) digits 1000 #')
        self.first_audio()
        began = time.monotonic()
        self.assertEqual(self.esl.api('uuid_break ' + channel), '+OK\n')
        self.quiet()
        result = self.application_event(read)
        self.assertGreater(time.monotonic() - began, 0.9)
        self.assertEqual(result['Application-Response'], '_none_')
        self.assertEqual(result['variable_read_result'], 'failure')
        self.assertNotIn('variable_digits', result)
        self.finish_call(channel)

    def test_missing_file_clears_old_terminator_and_reports_not_found(self):
        channel, _ = self.channel()
        self.application_event(self.execute(channel, 'set', 'playback_terminator_used=old'))
        play = self.execute(channel, 'playback', 'missing.wav')
        result = self.application_event(play)
        self.assertEqual(result['Application-Response'], 'FILE NOT FOUND')
        self.assertNotIn('variable_playback_terminator_used', result)
        self.quiet()
        self.finish_call(channel)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--control', type=Path, default=e2e.CONTROL)
    parser.add_argument('--media', type=Path, default=e2e.MEDIA)
    parser.add_argument('--connected', action='store_true')
    args, remaining = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    unittest.main(argv=[__file__] + remaining, verbosity=2)
