#!/usr/bin/env python3
"""双方同断言的真实 WAV 播放探针：波形、自然结束、主动停止、按键打断和缺失文件。

原版可用 --application-prefix 指向同一文件根，候选使用受限相对路径；报告显式保留路径差异。
此脚本不启动服务器、不接触外部通话，不以 API 受理或退出码代替真实媒体和完成事件。
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import struct
import time
import wave

from native_call_fixture import Call


def decode_mulaw(value):
    """标准 μ-law 独立解码，用于检查实际收到的声音，而不复用被测媒体实现。"""
    value = (~value) & 255
    sample = (((value & 15) << 3) + 132) << ((value >> 4) & 7)
    return 132 - sample if value & 128 else sample - 132


def sound_packets(call):
    """只选择已完整收到的 PCMU 音频包，并剔除静音；保留真实包用于后续计数和波形核验。"""
    with call.media_lock:
        packets = list(call.received)
    selected = []
    for row in packets:
        packet = row['packet']
        if len(packet) < 12 or packet[1] & 127 != 0:
            continue
        assert packet[0] == 0x80 and len(packet) == 172, '限定夹具预期无扩展的20ms PCMU RTP'
        samples = [decode_mulaw(value) for value in packet[12:]]
        if max(abs(value) for value in samples) > 1000:
            selected.append((row, samples))
    return selected


def waveform(call):
    packets = sound_packets(call)
    samples = [sample for _, values in packets for sample in values]
    assert len(packets) >= 8, ('实际有声音RTP包不足', len(packets))
    peak = max(abs(value) for value in samples)
    assert 5000 <= peak <= 6500, ('声音峰值不符合固定WAV', peak)
    crossings = sum(left < 0 <= right for left, right in zip(samples, samples[1:]))
    frequency = crossings * 8000 / len(samples)
    assert 420 <= frequency <= 460, ('声音频率不符合440Hz样本', frequency)
    return {'non_silent_pcmu_packets': len(packets), 'decoded_samples': len(samples), 'peak_pcm': peak,
            'estimated_frequency_hz': round(frequency, 3), 'source_frequency_hz': 440,
            'waveform_scope': '真实PCMU包解码后的峰值与零交叉频率；非逐字节相同保证'}


def scenario(call, args):
    assert call.invite()['status'] == 200
    name = 'native-1s.wav' if args.scenario == 'eos' else 'native-4s.wav'
    if args.scenario == 'missing':
        name = 'missing-native.wav'
        assert not (args.audio_root / name).exists()
    else:
        source = args.audio_root / name
        with wave.open(str(source), 'rb') as reader:
            assert (reader.getnchannels(), reader.getsampwidth(), reader.getframerate()) == (1, 2, 8000)
    # 显式设置按键合同，避免继承其他拨号计划变量影响测试。
    configured = call.execute('set', 'playback_terminators=' + ('#' if args.scenario == 'dtmf' else 'none'))
    assert call.completed(configured)['Application-Response'] == '_none_'
    argument = args.application_prefix + name
    token = call.execute('playback', argument)
    started = call.peer.event(lambda event: event.get('Application-UUID') == token and event.get('Event-Name') == 'CHANNEL_EXECUTE')
    observed = {'application_argument': argument, 'expected_application_response': 'FILE NOT FOUND' if args.scenario == 'missing' else 'FILE PLAYED'}
    if args.scenario in ('stop', 'dtmf'):
        deadline = time.monotonic() + 2
        while len(sound_packets(call)) < 10 and time.monotonic() < deadline:
            time.sleep(.01)
        assert len(sound_packets(call)) >= 10, '停止前未真正播放出声音'
        if args.scenario == 'stop':
            reply = call.peer.api('uuid_break ' + call.channel)
            observed['uuid_break_response'] = reply
            assert reply.strip() == '+OK', reply
        else:
            call.digit('#')
            dtmf = call.peer.event(lambda event: event.get('Unique-ID') == call.channel and event.get('Event-Name') == 'DTMF' and event.get('DTMF-Digit') == '#')
            observed['dtmf'] = {key: dtmf.get(key) for key in ['DTMF-Digit', 'DTMF-Duration', 'DTMF-Source']}
    completed = call.completed(token, timeout=6)
    observed['application_response'] = completed['Application-Response']
    # 先保存客观观察，再由调用方写出失败，便于定位差异而非只有笼统的assert错误。
    observed['elapsed_event_us'] = int(completed['Event-Date-Timestamp']) - int(started['Event-Date-Timestamp'])
    call.peer.collect(.2)
    if args.scenario == 'missing':
        observed['non_silent_pcmu_packets'] = len(sound_packets(call))
        assert observed['non_silent_pcmu_packets'] == 0, observed
    else:
        observed['waveform'] = waveform(call)
        count = observed['waveform']['non_silent_pcmu_packets']
        if args.scenario == 'eos':
            assert 45 <= count <= 52, ('1秒样本应基本完整播放', observed)
        else:
            assert count < 100 and observed['elapsed_event_us'] < 2000000, ('4秒样本未及时停止', observed)
        previous_count = count
        call.peer.collect(.2)
        assert len(sound_packets(call)) == previous_count, '完成事件之后仍在继续播放文件'
    if args.scenario == 'dtmf':
        observed['playback_terminator_used'] = call.peer.api('uuid_getvar ' + call.channel + ' playback_terminator_used')
        assert observed['playback_terminator_used'] == '#', observed
    # 对原版事件额外完整观察，缺失事件不冒充已兼容；应用/媒体子集与事件子集明确区分。
    events = [event for event in call.peer.events if event.get('Unique-ID') == call.channel and event.get('Event-Name') in ['PLAYBACK_START', 'PLAYBACK_STOP']]
    observed['playback_events'] = [{key: event.get(key) for key in ['Event-Name', 'Playback-File-Path', 'Playback-Status']} for event in events]
    assert observed['application_response'] == observed['expected_application_response'], observed
    return observed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--isolated-fixture', required=True, action='store_true')
    parser.add_argument('--sip-port', required=True, type=int)
    parser.add_argument('--esl-port', required=True, type=int)
    parser.add_argument('--password-env', required=True)
    parser.add_argument('--extension', required=True)
    parser.add_argument('--audio-root', required=True, type=Path)
    parser.add_argument('--application-prefix', default='')
    parser.add_argument('--scenario', required=True, choices=['eos', 'stop', 'dtmf', 'missing'])
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    assert not args.output.exists(), '不能覆盖历史原始结果'
    report = {'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'scenario': args.scenario,
              'scope': '限定8kHz单声道PCM16 WAV经PCMU输出及指定完成行为；PLAYBACK事件仅观察，非全格式/全事件兼容',
              'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              'fixture_sha256': hashlib.sha256(Path(__file__).with_name('native_call_fixture.py').read_bytes()).hexdigest(),
              'source_files': [{'path': name, 'sha256': hashlib.sha256((args.audio_root / name).read_bytes()).hexdigest()}
                               for name in ['native-1s.wav', 'native-4s.wav']], 'passed_scoped': False}
    call = None
    try:
        call = Call(args.sip_port, args.esl_port, os.environ[args.password_env], args.extension)
        report['observed'] = scenario(call, args)
        report['passed_scoped'] = True
    except Exception as error:
        report['error'] = type(error).__name__ + ': ' + str(error)
    finally:
        if call:
            report['cleanup'] = call.close()
            report.update(call.report())
            if report['cleanup'].get('error') or report['cleanup']['media_errors'] or not report['cleanup']['media_thread_exited']:
                report['passed_scoped'] = False
    report['finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({key: report.get(key) for key in ['scenario', 'passed_scoped', 'observed', 'error', 'cleanup']}, ensure_ascii=False))
    return 0 if report['passed_scoped'] else 2


if __name__ == '__main__':
    raise SystemExit(main())
