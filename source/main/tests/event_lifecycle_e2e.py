#!/usr/bin/env python3
"""四个生命周期事件的真实SIP/Rust媒体回归：检查包、停止、先后次序与单腿身份。"""
import argparse
import math
import os
from pathlib import Path
import struct
import signal
import time
import unittest
import wave

import e2e
import esl_applications_e2e as applications
import playback_controls_e2e as controls


class LifecycleFixture(applications.ApplicationFixture):
    local = True
    call = controls.PlaybackControls.call
    channel = controls.PlaybackControls.channel
    server_hangup = controls.PlaybackControls.server_hangup
    first_audio = controls.PlaybackControls.first_audio
    quiet = controls.PlaybackControls.quiet
    finish_call = controls.PlaybackControls.finish_call

    def configure(self, config):
        config['media']['playback_root'] = self.tmp.name
        # 独立产生确定的非静音波形，远端必须真实收到有序PCMU RTP。
        with wave.open(str(Path(self.tmp.name) / 'prompt.wav'), 'wb') as output:
            output.setnchannels(1)
            output.setsampwidth(2)
            output.setframerate(8000)
            output.writeframes(b''.join(struct.pack('<h', int(10000 * math.sin(2 * math.pi * 440 * i / 8000))) for i in range(1600)))

    def setUp(self):
        super().setUp()
        head, _ = self.esl.command('event json CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_HANGUP CHANNEL_EXECUTE CHANNEL_EXECUTE_COMPLETE DTMF PLAYBACK_START PLAYBACK_STOP CHANNEL_PARK CHANNEL_UNPARK')
        self.assertTrue(head['reply-text'].startswith('+OK'))

    def lifecycle(self, token, names, channel, path=None, status=None, hangup=False):
        complete = self.application_event(token)
        self.esl.api('echo lifecycle-barrier')
        events = self.esl.events
        begin = next(i for i, event in enumerate(events) if event.get('Application-UUID') == token and event['Event-Name'] == 'CHANNEL_EXECUTE')
        end = next(i for i, event in enumerate(events) if event.get('Application-UUID') == token and event['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE')
        # 四事件无顶层Application-UUID：按同一腿的实际执行区间关联，不能拿别的通话补数。
        found = [event for event in events[begin + 1:end] if event.get('Unique-ID') == channel and event['Event-Name'] in {'PLAYBACK_START', 'PLAYBACK_STOP', 'CHANNEL_PARK', 'CHANNEL_UNPARK'}]
        self.assertEqual([event['Event-Name'] for event in found], names)
        for event in found:
            self.assertNotIn('Application-UUID', event)
            self.assertNotIn('Application', event)
            self.assertEqual(event['Channel-State'], 'CS_EXECUTE')
            self.assertEqual(event['variable_current_application'], complete['Application'])
            self.assertEqual(event['variable_current_application_data'], complete['Application-Data'])
            if self.local:
                self.assertNotIn('Other-Leg-Unique-ID', event)
            if path:
                self.assertEqual(event['Playback-File-Path'], path)
        if status:
            self.assertEqual(found[-1]['Playback-Status'], status)
        if hangup:
            self.assertEqual(found[-1]['Answer-State'], 'hangup')
        return found, complete


class PlaybackLifecycle(LifecycleFixture):
    def test_wav_natural_end_has_one_start_done_stop_and_real_audio(self):
        channel, _ = self.channel()
        token = self.execute(channel, 'playback', 'prompt.wav')
        self.receive_tone(200)
        self.lifecycle(token, ['PLAYBACK_START', 'PLAYBACK_STOP'], channel, 'prompt.wav', 'done')
        self.quiet()
        self.finish_call(channel)

    def test_digit_stop_is_break_with_used_digit(self):
        channel, port = self.channel()
        path = 'tone_stream://%(2000,0,440)'
        token = self.execute(channel, 'playback', path)
        self.first_audio()
        self.send_digit(port, '*')
        _, complete = self.lifecycle(token, ['PLAYBACK_START', 'PLAYBACK_STOP'], channel, path, 'break')
        self.assertEqual(complete['variable_playback_terminator_used'], '*')
        self.quiet()
        self.finish_call(channel)

    def test_api_break_stops_media_before_completion(self):
        channel, _ = self.channel()
        path = 'tone_stream://%(2000,0,440)'
        token = self.execute(channel, 'playback', path)
        self.first_audio()
        self.assertEqual(self.esl.api('uuid_break ' + channel), '+OK\n')
        self.lifecycle(token, ['PLAYBACK_START', 'PLAYBACK_STOP'], channel, path, 'break')
        self.quiet()
        self.finish_call(channel)

    def test_hangup_waits_for_media_release_stop_then_complete(self):
        channel, _ = self.channel()
        path = 'tone_stream://%(2000,0,440)'
        token = self.execute(channel, 'playback', path)
        self.first_audio()
        self.finish_call(channel)
        _, complete = self.lifecycle(token, ['PLAYBACK_START', 'PLAYBACK_STOP'], channel, path, 'done', hangup=True)
        self.assertTrue(complete['Application-Response'].startswith('-ERR channel ended'))
        self.quiet()

    def test_missing_wav_has_no_fake_start_or_stop(self):
        channel, _ = self.channel()
        token = self.execute(channel, 'playback', 'missing.wav')
        _, complete = self.lifecycle(token, [], channel)
        self.assertEqual(complete['Application-Response'], 'FILE NOT FOUND')
        self.quiet()
        self.finish_call(channel)

    def test_worker_termination_confirms_stop_and_reclaims_call(self):
        channel, _ = self.channel()
        path = 'tone_stream://%(2000,0,440)'
        token = self.execute(channel, 'playback', path)
        self.first_audio()
        self.esl.wait_events('PLAYBACK_START', 1)
        deadline = time.monotonic() + 3
        while True:
            worker = next((item for item in self.get('/v1/status')['workers'] if item['stats'].get('active_calls') == 1), None)
            if worker:
                break
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.02)
        # 只终止本测试独立服务状态中持有该呼叫的子进程，绝不查找/终止共享主服务。
        os.kill(worker['pid'], signal.SIGKILL)
        self.server_hangup()
        self.lifecycle(token, ['PLAYBACK_START', 'PLAYBACK_STOP'], channel, path, 'done', hangup=True)
        self.quiet()

    def test_read_prompt_lifecycle_does_not_complete_collection_early(self):
        channel, port = self.channel()
        token = self.execute(channel, 'read', '1 2 prompt.wav digits 2000 #')
        self.receive_tone(200)
        self.esl.wait_events('PLAYBACK_STOP', 1)
        self.assertFalse(any(event.get('Application-UUID') == token and event['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE' for event in self.esl.events))
        self.send_digit(port, '1')
        self.send_digit(port, '#')
        _, complete = self.lifecycle(token, ['PLAYBACK_START', 'PLAYBACK_STOP'], channel, 'prompt.wav', 'done')
        self.assertEqual(complete['variable_digits'], '1')
        self.finish_call(channel)


class ParkLifecycle(LifecycleFixture):
    def test_park_enter_and_hangup_leave_before_completion(self):
        channel, _ = self.channel()
        token = self.execute(channel, 'park', '')
        self.esl.wait_events('CHANNEL_PARK', 1)
        self.assertFalse(any(event.get('Application-UUID') == token and event['Event-Name'] == 'CHANNEL_EXECUTE_COMPLETE' for event in self.esl.events))
        self.finish_call(channel)
        found, complete = self.lifecycle(token, ['CHANNEL_PARK', 'CHANNEL_UNPARK'], channel, hangup=True)
        self.assertEqual(found[0]['Answer-State'], 'answered')
        self.assertEqual(complete['Application-Response'], '_none_')


class BridgeLifecycle(LifecycleFixture):
    local = False

    def test_b_leg_events_and_audio_do_not_use_a_leg_identity(self):
        a, b, _, accepted = self.established_channel()
        path = 'tone_stream://%(200,0,440)'
        token = self.execute(b, 'playback', path)
        self.br.settimeout(3)
        packets = [self.br.recvfrom(2048)[0] for _ in range(10)]
        self.assertTrue(all(len(packet) == 172 and packet[1] & 127 == 0 for packet in packets))
        self.assertTrue(any(len(set(packet[12:])) > 8 for packet in packets))
        found, _ = self.lifecycle(token, ['PLAYBACK_START', 'PLAYBACK_STOP'], b, path, 'done')
        for event in found:
            self.assertEqual(event['Other-Leg-Unique-ID'], a)
            self.assertEqual(event['Call-Direction'], 'outbound')
            self.assertEqual(event['variable_uuid'], b)
        self.hangup(accepted)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--control', type=Path, default=e2e.CONTROL)
    parser.add_argument('--media', type=Path, default=e2e.MEDIA)
    parser.add_argument('--connected', action='store_true')
    args, remaining = parser.parse_known_args()
    e2e.CONTROL, e2e.MEDIA, e2e.CONNECTED = args.control.resolve(), args.media.resolve(), args.connected
    unittest.main(argv=[__file__] + remaining, verbosity=2)
