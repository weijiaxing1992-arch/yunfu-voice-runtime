#!/usr/bin/env python3
"""在显式隔离端点执行真实基础应用/XML拨号计划场景；双方必须使用相同断言。"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path

from native_call_fixture import Call


def application_event(call, application, kind, data=None):
    return call.peer.event(lambda event: event.get('Unique-ID') == call.channel and event.get('Application') == application
                           and event.get('Event-Name') == kind and (data is None or event.get('Application-Data') == data))


def verify_sleep_order(call, sleep_start, sleep_complete, after_start):
    """计时允许10ms采集边界误差，同时必须保证下一动作在sleep完成后执行。"""
    elapsed = int(sleep_complete['Event-Date-Timestamp']) - int(sleep_start['Event-Date-Timestamp'])
    assert 190000 <= elapsed <= 2000000, ('sleep200 timing', elapsed)
    assert call.peer.events.index(after_start) > call.peer.events.index(sleep_complete)
    return {'requested_ms': 200, 'actual_event_interval_us': elapsed, 'minimum_verified_ms': 190,
            'next_application_started_after_completion': True}


def killed_park(call):
    started = application_event(call, 'park', 'CHANNEL_EXECUTE')
    call.peer.collect(.2)
    assert not any(event.get('Application-UUID') == started['Application-UUID'] and event.get('Event-Name') == 'CHANNEL_EXECUTE_COMPLETE' for event in call.peer.events)
    reply = call.peer.api('uuid_kill ' + call.channel + ' NORMAL_CLEARING')
    assert reply.strip() == '+OK', reply
    hangup = call.server_hangup()
    completed = call.peer.completed(started['Application-UUID'])
    assert completed['Application-Response'] == '_none_', completed
    return {'still_parked_after_ms': 200, 'uuid_kill_response': reply,
            'hangup_cause': hangup.get('Hangup-Cause'), 'park_completion_response': completed['Application-Response'],
            'actual_server_bye_answered': True}


def scenario(call, name):
    observed = call.invite()
    if name == 'xml-miss':
        call.peer.collect(.1)
        assert observed['status'] == 404, observed
        return {'sip_status': 404, 'answered': False, 'scope': '已配置权威XML时，没有匹配destination返回404'}
    assert observed['status'] == 200, observed
    if name == 'foundation':
        token = call.execute('answer')
        repeated = call.completed(token)
        assert repeated['Application-Response'] == '_none_'
        assert sum(event.get('Unique-ID') == call.channel and event.get('Event-Name') == 'CHANNEL_ANSWER' for event in call.peer.events) == 1
        before = call.execute('set', 'rs_native_stage=before')
        assert call.completed(before)['Application-Response'] == '_none_'
        sleep = call.execute('sleep', '200')
        after = call.execute('set', 'rs_native_stage=after')
        sleep_done, after_done = call.completed(sleep), call.completed(after)
        assert sleep_done['Application-Response'] == after_done['Application-Response'] == '_none_'
        started = call.peer.event(lambda event: event.get('Application-UUID') == sleep and event.get('Event-Name') == 'CHANNEL_EXECUTE')
        following = call.peer.event(lambda event: event.get('Application-UUID') == after and event.get('Event-Name') == 'CHANNEL_EXECUTE')
        order = verify_sleep_order(call, started, sleep_done, following)
        value = call.peer.api('uuid_getvar ' + call.channel + ' rs_native_stage')
        assert value == 'after'
        zero = call.execute('sleep', '0')
        assert call.completed(zero)['Application-Response'] == '_none_'
        end = call.execute('hangup', 'NORMAL_CLEARING')
        hangup = call.server_hangup()
        completed = call.completed(end)
        assert completed['Application-Response'] == '_none_'
        return {'answer_idempotent': True, 'sleep_order': order, 'final_variable': value,
                'sleep_zero_completed': True, 'hangup_cause': hangup.get('Hangup-Cause'),
                'hangup_completion_response': completed['Application-Response'], 'actual_server_bye_answered': True}
    if name == 'xml-sequence':
        after = application_event(call, 'set', 'CHANNEL_EXECUTE_COMPLETE', 'rs_native_stage=after')
        assert after['Application-Response'] == '_none_'
        value = call.peer.api('uuid_getvar ' + call.channel + ' rs_native_stage')
        assert value == 'after'
        start = application_event(call, 'sleep', 'CHANNEL_EXECUTE', '200')
        complete = application_event(call, 'sleep', 'CHANNEL_EXECUTE_COMPLETE', '200')
        following = application_event(call, 'set', 'CHANNEL_EXECUTE', 'rs_native_stage=after')
        order = verify_sleep_order(call, start, complete, following)
        return {'ordered_xml_actions': ['answer', 'set', 'sleep', 'set', 'park'], 'final_variable': value,
                'sleep_order': order, 'park_cleanup': killed_park(call)}
    if name == 'xml-park':
        return killed_park(call)
    if name == 'xml-hangup':
        started = application_event(call, 'hangup', 'CHANNEL_EXECUTE', 'NORMAL_CLEARING')
        hangup = call.server_hangup()
        completed = call.peer.completed(started['Application-UUID'])
        assert completed['Application-Response'] == '_none_'
        return {'hangup_cause': hangup.get('Hangup-Cause'), 'hangup_completion_response': completed['Application-Response'],
                'actual_server_bye_answered': True, 'xml_actions': ['answer', 'sleep', 'hangup']}
    raise ValueError('未知场景')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--isolated-fixture', required=True, action='store_true')
    parser.add_argument('--sip-port', required=True, type=int)
    parser.add_argument('--esl-port', required=True, type=int)
    parser.add_argument('--password-env', required=True)
    parser.add_argument('--extension', required=True)
    parser.add_argument('--scenario', choices=['foundation', 'xml-sequence', 'xml-hangup', 'xml-park', 'xml-miss'], required=True)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    assert not args.output.exists(), '历史运行证据不覆盖'
    assert args.extension.isdigit() and len(args.extension) <= 32
    report = {'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'scenario': args.scenario,
              'scope': '单路基础应用或XML指定动作正常路径；不代表完整dialplan语法、并发或全部模块兼容',
              'passed_scoped': False, 'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              'fixture_sha256': hashlib.sha256(Path(__file__).with_name('native_call_fixture.py').read_bytes()).hexdigest()}
    call = None
    try:
        call = Call(args.sip_port, args.esl_port, os.environ[args.password_env], args.extension)
        report['observed'] = scenario(call, args.scenario)
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
