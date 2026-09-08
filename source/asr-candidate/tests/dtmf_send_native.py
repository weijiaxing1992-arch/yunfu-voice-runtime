#!/usr/bin/env python3
"""原版与候选共用的有限uuid_send_dtmf探针：实际SIP、按键RTP和完整非认证原字节。"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import struct
import time
import unittest

from native_call_fixture import Call
from dtmf_send_e2e import assert_events, assert_sequence


def run(args):
    result = {'schema_version': '1.0.0', 'scope': '本地已应答A腿PCMU/telephone-event 101/8000；API正文与真实RTP。不是全部uuid_send_dtmf参数或远端语音质量认证。',
              'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), 'passed': False, 'observations': {}}
    call = None
    try:
        password = os.environ[args.password_env]
        call = Call(args.sip_port, args.esl_port, password, args.extension)
        result['observations']['invite'] = call.invite()
        assert result['observations']['invite']['status'] == 200
        # 相同参数跨实现观察，先用20ms整数倍避免把原版时长量化误说成逐毫秒完全等价。
        token = call.execute('playback', 'tone_stream://%(1600,0,440)')
        deadline = time.monotonic() + 2
        while len(call.received) < 3:
            assert time.monotonic() < deadline
            time.sleep(.01)
        reply = call.peer.api('uuid_send_dtmf ' + call.channel + ' 12#@100')
        result['observations']['api_response'] = reply
        assert reply == f'+OK {call.channel} sent DTMF 12#@100.\n'
        completed = call.completed(token, timeout=4)
        assert completed['Application-Response'] == 'FILE PLAYED'
        time.sleep(.1)
        with call.media_lock:
            wire = [row['packet'] for row in call.received]
        case = unittest.TestCase()
        count = assert_events(case, wire, '12#', 100)
        assert_sequence(case, wire)
        audio = [p for p in wire if p[1] & 127 == 0]
        events = [p for p in wire if p[1] & 127 == 101]
        # 原版可能在按键期间替代音频；如实保留包数与间隔，不要求两端音频包数相等。
        assert len(audio) > 5
        result['observations'].update(digits='12#', duration_ticks=800, event_packets=count,
            audio_packets=len(audio), ssrc_count=len({p[8:12] for p in wire}),
            first_audio_timestamp=struct.unpack('!I', audio[0][4:8])[0],
            audio_timestamp_steps=[(struct.unpack('!I', b[4:8])[0] - struct.unpack('!I', a[4:8])[0]) % 2**32 for a,b in zip(audio,audio[1:])],
            ending_repetitions=[sum(1 for p in events if p[12]==digit and p[13]&128) for digit in [1,2,11]])
        assert call.peer.api('uuid_send_dtmf') == '-USAGE: <uuid> <dtmf_data>\n'
        assert call.peer.api('uuid_send_dtmf 00000000-0000-4000-8000-000000000000 1') == '-ERR Cannot locate session!\n'
        call.caller_hangup()
        assert not call.media_errors, call.media_errors
        result['passed'] = True
    except Exception as error:
        result['error'] = type(error).__name__ + ': ' + str(error)
    finally:
        if call:
            call.close()
            result['actual'] = call.report()
        result['finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2))
    print(json.dumps({'passed': result['passed'], 'output': str(args.output), 'error': result.get('error')}, ensure_ascii=False))
    return 0 if result['passed'] else 2


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--sip-port', type=int, required=True)
    parser.add_argument('--esl-port', type=int, required=True)
    parser.add_argument('--extension', default='9197')
    parser.add_argument('--password-env', default='NATIVE_ESL_PASSWORD')
    parser.add_argument('--output', type=Path, required=True)
    raise SystemExit(run(parser.parse_args()))
