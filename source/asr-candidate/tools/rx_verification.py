#!/usr/bin/env python3
"""离线重放本地 A 腿 RXS2 收音证据；只读原文件，不运行收据中的程序。

三个真实运行各含 PCMU/PCMA × 普通/connected UDP 四例。Go 测试二进制不是
部署中的控制进程；本门禁不证明 FreeSWITCH 成对、供应商接入或并发容量。
"""
import base64
from collections import Counter
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import re
import struct
import uuid
import zlib

from pcm_verification import read_artifact, require, safe_path, sha, utc as pcm_utc, wire

ROOT = Path(__file__).resolve().parents[1]
RX_KIND = 'go_rxs2_local_g711_composite_e2e'
ROLES = ('pool', 'pool_build', 'pause', 'server', 'media_build')
CASES = tuple(f'payload_{law}_connected_{mode}' for mode in ('false', 'true') for law in (0, 8))
TESTS = dict(pool='TestRealRXPoolG711SamplesAndLifecycle',
             pause='TestRealRXReceiverPauseRejectsOSResidentPCM',
             server='TestRealRXServerSIPAuthorizationSamplesAndCleanup')
TAIL = ('PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；'
        '正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、'
        'ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。')
RX_CLAIMS = {
    'key-ipc-rx-subscribe': '真实订阅及ACK前/错误来源ACK拒绝；' + TAIL,
    'key-ipc-rx-status': '实际身份、事件/样本计数和失败后仍可查询；' + TAIL,
    'key-ipc-rx-unsubscribe': '明确退订、挂断撤销、资源归零及端口重新绑定；' + TAIL,
    'key-ipc-rx-stream': '真实G.711输入逐样本核对、原始期限和过期拒绝；' + TAIL,
    'key-api-rx-sdk': '内部Go收音句柄、SIP授权、交付期限及挂断清理；' + TAIL,
}
EXPIRED = 'RX observation delivery deadline reached; subscription failed locally'
DENIED = 'RX requires an ACKed local processed G711 A leg'
REVOKED = 'RX read authorization revoked; query or unsubscribe for cleanup'
PERIOD = 100_000_000
GO_INPUT_SUFFIXES = ('.go', '.s', '.S', '.c', '.h', '.cc', '.cpp', '.cxx', '.m', '.mm',
                     '.f', '.F', '.f90', '.for', '.syso', '.swig', '.swigcxx')


def utc(value):
    """兼容Python3.9解析Go RFC3339Nano；期限比较始终使用原整数纳秒，不借UTC判断。"""
    if isinstance(value, datetime):
        require(value.tzinfo is not None and value.utcoffset() is not None, '没有采集时区')
        return value.astimezone(timezone.utc)
    require(isinstance(value, str), '时刻不是带时区字符串')
    match = re.fullmatch(r'(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)', value)
    require(match is not None, '时刻不是RFC3339')
    return pcm_utc(match[1]+('.'+(match[2]+'000000')[:6] if match[2] else '')+match[3])


def bound_path(name):
    """确定的路径规则同时检查新增文件；不把旧运行未采集的Python/native/网页算入绑定。"""
    if name.startswith(('control/internal/server/doc_assets/', 'control/internal/server/web/')):
        return False
    return ((name.startswith('control/') and name.endswith(GO_INPUT_SUFFIXES))
            or name in ('control/go.mod', 'control/go.sum', 'config/local.json',
                        'media/Cargo.toml', 'media/Cargo.lock', 'media/build.rs')
            or name.startswith(('control/vendor/', 'control/internal/server/fs_templates/'))
            or (name.startswith(('media/src/', 'media/.cargo/')) and name.endswith(('.rs', '.toml'))))


def source_map(value):
    require(isinstance(value, dict) and value, '缺少实际源码快照')
    for name, digest in value.items():
        safe_path(name)
        require(isinstance(digest, str) and re.fullmatch('[a-f0-9]{64}', digest), '源码SHA格式错误')
    return value


def rows(raw):
    require(len(raw) <= 8 << 20, '轨迹超过重放预算')
    result = [json.loads(line) for line in raw.decode().splitlines() if line.strip()]
    require(result and len(result) <= 10000 and all(isinstance(x, dict) for x in result), '轨迹为空或过长')
    return result


def required_files(role, receipt):
    """按固定适配器取文件，绝不执行或跟随命令参数中的路径。"""
    if role == 'pool':
        return {'tests.jsonl': receipt['log_sha256']}
    if role in ('pool_build', 'media_build'):
        steps = receipt['commands' if role == 'pool_build' else 'steps']
        return {s['name']+'.log': s['log_sha256'] for s in steps}
    if role == 'pause':
        expected = {'build.log', 'tests.log'} | {
            f'wire/{case}/{name}' for case in CASES for name in (
                'helper-ready.json', 'helper-result.json', 'helper.log',
                'os-head.rxs2', 'parent-observation.json', 'input-0.rtp', 'input-1.rtp')}
        # 早期原运行的四例直接位于artifacts/下；前缀是固定采集约定的一部分。
        actual = receipt['artifacts']
        if not any(n.startswith('wire/') for n in actual):
            expected = {n.replace('wire/', 'cases/', 1) for n in expected}
        require(expected <= actual.keys(), '暂停运行缺少四组合原始文件')
        return {n: actual[n] for n in sorted(expected)}
    require(role == 'server', '未知RX证据角色')
    names = {'go-version.log', 'dependencies.log', 'compile.log', 'control-buildinfo.log',
             'test.log', 'recorder.py', 'rust-build-receipt.json'} | {
        f'wire/{case}/{name}' for case in CASES for name in ('wire.jsonl', 'events.jsonl')}
    return {name: receipt['files'][name]['sha256'] for name in sorted(names)}


def binary_contract(receipts):
    return {'pool_test': ('pool_build', 'media-race.test', receipts['pool']['test_before']),
            'pause_test': ('pause', 'media-pause.test', receipts['pause']['test_before']),
            'server_test': ('server', 'server-rx.test', receipts['server']['control_binary_before']),
            'media': ('media_build', 'rustswitch-media', receipts['media_build']['binary']['sha256'])}


def build_steps(original, raw):
    """编译命令只作为不可执行的采集数据核对；不能用任意成功日志替代真实构建。"""
    expected = {'pool_build': {'build-race', 'sdk-race', 'shared-clock-bench', 'build-cgo0-darwin-arm64',
                              'build-cgo0-linux-arm64', 'build-cgo0-linux-amd64', 'sdk-cgo0-darwin'},
                'pause': {'build', 'tests'}, 'server': {'go-version', 'dependencies', 'compile', 'control-buildinfo', 'test'},
                'media_build': {'tests', 'clippy', 'release'}}
    grouped = {}
    for role, names in expected.items():
        steps = original[role]['commands' if role == 'pool_build' else 'steps']
        require(Counter(s['name'] for s in steps) == Counter(names), role+'构建步骤缺失/重复')
        require(all(s.get('exit_code', s.get('returncode')) == 0 for s in steps), role+'实际构建/测试失败')
        grouped[role] = {s['name']: s.get('args', s.get('command')) for s in steps}
    for role, name, target, binary in [('pool_build', 'build-race', './internal/media', 'media-race.test'),
                                      ('pause', 'build', './internal/media', 'media-pause.test'),
                                      ('server', 'compile', './internal/server', 'server-rx.test')]:
        args = grouped[role][name]
        require(all(x in args for x in ('test', '-c', '-race', '-o', target))
                and Path(args[args.index('-o')+1]).name == binary, 'Go实际编译目标不符')
    for role, name, binary in [('pool', None, 'media-race.test'), ('pause', 'tests', 'media-pause.test'),
                               ('server', 'test', 'server-rx.test')]:
        args = original['pool']['command'] if role == 'pool' else grouped[role][name]
        require('test2json' in args and binary in [Path(x).name for x in args]
                and ('^'+TESTS[role]+'$' in args or '-test.run=^'+TESTS[role]+'$' in args), 'Go实际执行命令不符')
    for name, verb in [('tests', 'test'), ('clippy', 'clippy'), ('release', 'build')]:
        args = grouped['media_build'][name]
        require(args[:2] == ['cargo', verb] and 'media/Cargo.toml' in args
                and (name != 'release' or '--release' in args), 'Rust实际构建步骤不符')
    for step in original['server']['steps']:
        require(utc(original['server']['started_at']) <= utc(step['started_at']) <= utc(step['finished_at'])
                <= utc(original['server']['finished_at']), 'Server构建步骤时刻越界')
    require(sha(raw['server/recorder.py']) == original['server']['recorder_sha256'], 'Server采集器原字节不符')


def import_rx(receipts, evidence_dir):
    """仅归档调用者给出的已有原收据和固定相邻文件；失败原证据也不会被改成通过。"""
    require(set(receipts) == set(ROLES), 'RX导入需要五种原收据')
    paths = {role: Path(path) for role, path in receipts.items()}
    originals = {}
    for role, path in paths.items():
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 8 << 20, '原始RX收据缺失或超预算')
        originals[role] = path.read_bytes()
    decoded = {role: json.loads(raw) for role, raw in originals.items()}
    identifier, base = 'rx-'+uuid.uuid4().hex, Path(evidence_dir)
    destination = base/identifier
    destination.mkdir(parents=True, exist_ok=False)
    artifacts, binaries = {}, {}

    def save(key, name, data, mapping):
        target = destination/safe_path(name)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        mapping[key] = {'path': target.relative_to(base).as_posix(), 'sha256': sha(data)}

    for role in ROLES:
        save(role+'/$receipt', role+'/receipt.json', originals[role], artifacts)
        for name, digest in required_files(role, decoded[role]).items():
            save(role+'/'+name, role+'/'+name,
                 read_artifact(paths[role].parent, {'path': name, 'sha256': digest}), artifacts)
    for role, (origin, name, digest) in binary_contract(decoded).items():
        path = paths[origin].parent/name
        require(path.is_file() and not path.is_symlink() and 0 < path.stat().st_size <= 512 << 20,
                '实际运行二进制缺失或超过预算')
        raw = path.read_bytes()
        require(sha(raw) == digest, '二进制不是收据绑定的实际运行文件')
        save(role, 'binary-'+role, raw, binaries)
    envelope = {'schema_version': '1.0.0', 'kind': RX_KIND, 'id': identifier,
                'started_at': min((r['started_at'] for r in decoded.values()), key=utc),
                'finished_at': max((r['finished_at'] for r in decoded.values()), key=utc),
                'artifacts': artifacts, 'binaries': binaries}
    (base/(identifier+'.receipt.json')).write_bytes(wire(envelope))
    return identifier


def pack_rx(receipt, base):
    """网页只内嵌原始日志/轨迹/收据，不内嵌可执行文件。"""
    packed = {}
    for ref in receipt['artifacts'].values():
        raw = read_artifact(base, ref)
        packed[ref['path']] = {'encoding': 'zlib+base64', 'bytes': len(raw), 'sha256': sha(raw),
                               'data': base64.b64encode(zlib.compress(raw, 9)).decode()}
    return packed


def go_log(raw, test, start, finish):
    """test2json可能拆分长PCM输出；先按Test重组，再解析完整原始行。"""
    data = rows(raw)
    require(all(start <= utc(r['Time']) <= finish for r in data), 'Go原始测试日志不在收据运行时段')
    require(not any(r.get('Action') in ('fail', 'skip') for r in data), '真实测试含失败或跳过')
    expected = {test} | {test+'/'+c for c in CASES}
    passed = Counter(r.get('Test') for r in data if r.get('Action') == 'pass' and r.get('Test'))
    require(set(passed) == expected and all(v == 1 for v in passed.values()), '四组合及父测试未完整通过')
    require(sum(r.get('Action') == 'pass' and not r.get('Test') for r in data) == 1, '缺少包最终通过')
    return {case: ''.join(r.get('Output', '') for r in data if r.get('Test') == test+'/'+case)
            for case in CASES}


def tagged(text, tag):
    return [json.loads(line.split(tag, 1)[1]) for line in text.splitlines() if tag in line]


def one(values, label):
    require(len(values) == 1, label+'必须恰好一次')
    return values[0]


def worker(value):
    require(value['healthy'] is True and value['pid'] > 0 and value['generation'] > 0
            and value['worker_id'] == 0 and 'rx_g711_local_v2' in value['capabilities'], 'worker代次/能力无效')


def reply(value, state):
    require(value['ok'] is True and value['type'] == 'rx_state' and value['state'] == state
            and value['session'] > 0 and value['subscription_id'] > 0 and not value['error'], '实际RX回应失败')
    for suffix in ('events', 'samples'):
        counts = [value[p+'_'+suffix] for p in ('produced', 'submitted', 'dropped')]
        require(all(type(x) is int and x >= 0 for x in counts), '远端计数不是非负整数')
        require(counts[0] == counts[1]+counts[2] and value['queued_events'] == 0, '远端计数不守恒或队列未清空')


def zero_media(stats, decoded):
    require(all(stats[k] == 0 for k in ('active_calls', 'processed_active_calls', 'processed_local_active_calls',
                'connected_sockets', 'rx_active_subscriptions', 'rx_queued_events', 'rx_observation_storage_bytes',
                'rx_observation_failed', 'receive_errors', 'send_errors', 'send_queue_drops')), '媒体资源/错误没有归零')
    require(stats['processed_decoded_frames'] == decoded and stats['processed_decoded_samples'] == decoded*160,
            '实际解码计数不等于原始输入')


def rtp(raw, law):
    require(len(raw) == 172 and raw[0] == 0x80 and raw[1] & 127 == law, '输入RTP不是单声道20ms G.711')
    seq, timestamp, ssrc = struct.unpack_from('>HII', raw, 2)
    return dict(seq=seq, timestamp=timestamp, ssrc=ssrc, body=raw[12:])


def g711(body, law):
    """独立按两种压扩律算出实际S16样本，不调用Rust/Go实现或收据中的oracle。"""
    samples = []
    for encoded in body:
        if law == 0:
            u = (~encoded) & 255
            value = (((u & 15) << 3)+132) << ((u >> 4) & 7)
            samples.append(132-value if u & 128 else value-132)
        else:
            a = encoded ^ 85
            segment = (a >> 4) & 7
            value = (a & 15) << 4
            value += 8 if segment == 0 else 264
            if segment > 1:
                value <<= segment-1
            samples.append(value if a & 128 else -value)
    return samples


def frame(value, clock=None):
    require(value['ClockDomain'] in (1, 2) and type(value['ObservationLowerBoundNS']) is int
            and 0 < value['ObservationLowerBoundNS'] < (1 << 64)-PERIOD
            and value['ExpiresAtNS'] == value['ObservationLowerBoundNS']+PERIOD, 'RXS2原始绝对期限无效')
    require(value['Kind'] in (1, 7) and value['SampleRate'] == value['RTPClockRate'] == 8000,
            '正常链路混入补偿/静默或采样时钟不符')
    require(value['Session'] > 0 and value['SubscriptionID'] > 0 and value['SourceGeneration'] > 0
            and value['SourceSegment'] > 0 and value['go_queue_age_ms'] < 100, '帧身份或本地期限无效')
    flags = value['Flags']
    require(type(flags) is int and flags & ~0x3ff == 0 and flags & 7 == 7
            and (value['Boundary'] != 0) == bool(flags & 16)
            and (flags & 8 != 0 or value['MediaTimeNS'] == 0)
            and (flags & 256 != 0 or value['ArrivalAgeMS'] == 0)
            and (flags & 512 != 0 or value['DeadlineLatenessMS'] == 0)
            and value['CNApplied'] is False, 'RXS2有效位/保留位与来源字段矛盾')
    require(all(type(value[k]) is int and 0 <= value[k] < 1 << 64 for k in
                ('Session', 'SubscriptionID', 'Sequence', 'SourceGeneration', 'SourceSegment', 'RTPTimestamp', 'RTPSequence', 'MediaTimeNS')),
            'RXS2完整位置不是u64')
    if clock is not None:
        require(clock['Domain'] == value['ClockDomain'] and
                value['ObservationLowerBoundNS'] <= clock['ClockNS'] < value['ExpiresAtNS'], '实际交付时样本已过期或来自未来')
    if value['Kind'] == 7:
        require(value['BodyBytes'] == value['SampleCount'] == 0 and value['Boundary'] == 1
                and not any(value['PCM']), '来源边界携带伪PCM')
    else:
        require(value['BodyBytes'] == 320 and value['SampleCount'] == 160
                and flags & 8 != 0 and value['Boundary'] in (0, 5) and value['DiscardedPackets'] == 0
                and len(value['PCM']) == 160 and all(type(x) is int and -32768 <= x <= 32767 for x in value['PCM']),
                '真实PCM长度/取值无效')


def samples(inputs, frames, law, count):
    require(len(inputs) == count and len(frames) == count+1 and frames[0]['Kind'] == 7,
            '原始输入或实际交付数量缺失')
    decoded = [f for f in frames if f['Kind'] == 1]
    require(len(decoded) == count, '不能用PLC/CN代替真实解码通过')
    initial = frames[0]
    for i, value in enumerate(frames):
        frame(value)
        require(value['Sequence'] == i+1 and all(value[k] == initial[k] for k in
                ('Session', 'SubscriptionID', 'SourceGeneration', 'SourceSegment', 'SSRC', 'ClockDomain')), '交付顺序/来源身份改变')
    for i, (packet, value) in enumerate(zip(inputs, decoded)):
        require(packet['ssrc'] == value['SSRC'] and packet['seq'] == value['RTPSequence'] & 65535
                and packet['timestamp'] == value['RTPTimestamp'] & 0xffffffff, 'RTP来源位置不能对应PCM')
        require(packet['seq'] == (inputs[0]['seq']+i) & 65535
                and packet['timestamp'] == (inputs[0]['timestamp']+i*160) & 0xffffffff
                and value['MediaTimeNS'] == i*20_000_000
                and value['RTPSequence'] == decoded[0]['RTPSequence']+i
                and value['RTPTimestamp'] == decoded[0]['RTPTimestamp']+i*160, '完整RTP/媒体时间不连续')
        if i == 0:
            require(value['RTPSequence'] == initial['RTPSequence'] and value['RTPTimestamp'] == initial['RTPTimestamp'],
                    '第一解码帧与来源边界完整位置不一致')
        require(value['PCM'] == g711(packet['body'], law), '实际G.711原样本解码不一致')


def pool_case(text, case, media):
    law = int(case.split('_')[1])
    matches = re.findall(r'actual_rx_worker=(\{[^\n]+\}) binary_sha256=([a-f0-9]{64})', text)
    identity, digest = one(matches, '实际worker身份')
    worker(json.loads(identity))
    require(digest == media, 'Pool运行了其他媒体二进制')
    inputs = [rtp(bytes.fromhex(x), law) for x in re.findall(r'actual_input_rtp destination=127\.0\.0\.1:\d+ hex=([a-f0-9]+)', text)]
    delivered = tagged(text, 'decoded_actual_rxs2=')
    samples(inputs, delivered, law, 6)
    stopped = one(tagged(text, 'actual_rx_stopped='), '退订回应')
    reply(stopped, 'stopped')
    require(stopped['session'] == delivered[0]['Session'] and stopped['subscription_id'] == delivered[0]['SubscriptionID']
            and stopped['submitted_samples'] == 960 and stopped['submitted_events'] == 7
            and stopped['dropped_events'] == stopped['dropped_samples'] == 0, '退订身份或实际样本计数错误')


def pause_case(get, prefix, case):
    """校验真实OS队首和自有进程身份；队首为边界时不把它描述为PCM抓包。"""
    load = lambda name: json.loads(get(prefix+case+'/'+name))
    ready, done, parent = load('helper-ready.json'), load('helper-result.json'), load('parent-observation.json')
    worker(ready['worker'])
    require(done['worker_after'] == ready['worker'], '暂停时Rust进程被替换或停止')
    pid = ready['helper_pid']
    def ps(key):
        parts = parent[key].split()
        require(len(parts) == 4, '缺少自有进程身份原记录')
        return tuple(map(int, parts[:3])), parts[3]
    hb, hs, wb, ws = (ps(x) for x in ('helper_before', 'helper_stopped', 'worker_before', 'worker_during_stop'))
    require(hb[0] == hs[0] and hb[0][0] == hb[0][2] == pid and hb[0][1] > 0
            and 'T' not in hb[1] and 'T' in hs[1] and wb == ws
            and wb[0] == (ready['worker']['pid'], pid, pid) and 'T' not in wb[1], 'SIGSTOP目标或父子身份不符')
    require(parent['paused_ns'] == parent['resume_ns']-parent['stop_ns'] and
            250_000_000 <= parent['paused_ns'] < 1_000_000_000 and
            parent['stop_ns'] < parent['peek_ns'] < parent['resume_ns'] <= done['read_clock_ns'], '实际暂停不在250ms至1s范围')
    raw = get(prefix+case+'/os-head.rxs2')
    require(len(raw) == 168 and raw[:4] == b'RXS2' and raw[4] == 7, '实际OS队首不是本次来源边界RXS2')
    peek = parent['peek_frame']
    frame(peek)
    nonblock = 4 if peek['ClockDomain'] == 2 else 2048
    require(parent['fd_flags_before'] == parent['fd_flags_after'] and parent['fd_flags_before'] & nonblock,
            'FD复制改变了O_NONBLOCK')
    require(struct.unpack_from('<HQQ', raw, 150) == (peek['ClockDomain'], peek['ObservationLowerBoundNS'], peek['ExpiresAtNS'])
            and peek['ExpiresAtNS'] <= parent['peek_ns'] and parent['peek_kind'] == peek['Kind'] == 7,
            'OS原始队首未过期或期限转录错误')
    require(parent['stop_ns'] <= peek['ObservationLowerBoundNS'] < parent['resume_ns'], '队首不是本次暂停窗口产生')
    require(struct.unpack_from('<QQQ', raw, 8) == (peek['Session'], peek['SubscriptionID'], peek['Sequence']), 'OS原始帧身份转录不符')
    require(raw[5] == 0 and struct.unpack_from('<H', raw, 6)[0] == peek['Flags']
            and struct.unpack_from('<QQQQQ', raw, 32) == tuple(peek[k] for k in
                ('SourceGeneration', 'SourceSegment', 'RTPTimestamp', 'RTPSequence', 'MediaTimeNS'))
            and struct.unpack_from('<IIIHH', raw, 72) == tuple(peek[k] for k in
                ('SSRC', 'SampleRate', 'RTPClockRate', 'BodyBytes', 'SampleCount')),
            'OS原始帧来源/格式转录不符')
    require(parent['input_packets'] == 2, '暂停输入不是两包')
    law = int(case.split('_')[1])
    packets = [rtp(get(prefix+case+f'/input-{i}.rtp'), law) for i in range(2)]
    require(packets[0]['body'] != packets[1]['body'] and any(g711(packets[0]['body'], law)), '输入不是独立有声样本')
    require(all(p['ssrc'] == peek['SSRC'] for p in packets) and packets[0]['seq'] == peek['RTPSequence'] & 65535
            and packets[0]['timestamp'] == peek['RTPTimestamp'] & 0xffffffff
            and packets[1]['seq'] == (packets[0]['seq']+1) & 65535
            and packets[1]['timestamp'] == (packets[0]['timestamp']+160) & 0xffffffff, '暂停RTP不属于OS队首来源')
    require(done['read_error'] == EXPIRED and done['cleanup_error'] == '', '过期读取没有明确失败或清理失败')
    require(done['passed'] is True, '暂停helper实际断言未通过')
    reply(ready['subscribed'], 'active')
    reply(done['remote'], 'active')
    reply(done['stopped'], 'stopped')
    require(ready['subscribed']['submitted_samples'] == ready['sdk']['DeliveredSamples'] == 0,
            '暂停前已经存在音频交付')
    remote, sdk = done['remote'], done['sdk']
    require(all(r['session'] == peek['Session'] and r['subscription_id'] == peek['SubscriptionID']
                for r in (ready['subscribed'], remote, done['stopped'])), '暂停前后远端身份不符')
    require(sdk['Session'] == peek['Session'] and sdk['SubscriptionID'] == peek['SubscriptionID']
            and sdk['Generation'] == ready['worker']['generation'] and sdk['Worker'] == ready['worker']['worker_id'], '暂停SDK身份不符')
    require(all(remote[k] == done['stopped'][k] for k in ('session', 'subscription_id', 'produced_samples', 'submitted_samples')),
            '退订改换身份或样本')
    require(remote['submitted_samples'] >= 320 and remote['submitted_events'] > 0
            and remote['dropped_samples'] == remote['dropped_events'] == 0, 'Rust没有实际提交或已背压丢弃')
    require(sdk['ReceivedSamples'] == sdk['DroppedSamples'] == remote['submitted_samples'] and
            sdk['ReceivedRecords'] == sdk['DroppedRecords'] == remote['submitted_events'] and
            sdk['DeliveredSamples'] == sdk['DeliveredRecords'] == sdk['QueuedRecords'] == sdk['ClockFailures'] == 0
            and sdk['FreshnessFailures'] == 1 and sdk['Error'] == EXPIRED, '暂停后旧样本被交付或实际计数不守恒')
    zero_media(done['after_release'], 2)


def sip(value):
    raw = bytes.fromhex(value['hex'])
    header, body = raw.split(b'\r\n\r\n', 1)
    lines = header.decode().split('\r\n')
    headers = {}
    for line in lines[1:]:
        key, val = line.split(':', 1)
        key = key.lower()
        require(key not in headers, '本次SIP固定夹具出现重复头')
        headers[key] = val.strip()
    require(int(headers['content-length']) == len(body), 'SIP实际正文截断')
    return lines[0], headers, body


def server_case(raw, case, binaries, start, end):
    data = rows(raw)
    require(all(start <= utc(r['utc']) <= end for r in data), 'SIP轨迹不在实际运行时段')
    require(all(a['elapsed_ns'] <= b['elapsed_ns'] for a, b in zip(data, data[1:])), 'SIP轨迹时间倒退')
    def item(kind):
        return one([r['value'] for r in data if r['kind'] == kind], kind)
    def position(kind):
        return one([i for i, r in enumerate(data) if r['kind'] == kind], kind)
    identity = item('identity')
    require(identity['media_sha256'] == binaries['media'] and identity['control_sha256'] == binaries['server_test']
            and identity['test'] == TESTS['server']+'/'+case, 'Server实际运行身份不符')
    config = item('actual_config')
    require(config['media']['connect_sockets'] is (case.endswith('true')) and config['media']['processing'] == 'g711'
            and config['media']['workers'] == 1 and config['media']['bind_ip'] == '127.0.0.1', '四组合配置未实际执行')
    worker(item('worker_ready'))
    for kind in ('before_ack_subscribe', 'wrong_ack_subscribe'):
        denied = item(kind)
        require(denied['error'] == DENIED and denied['has_handle'] is False and denied['reply']['ok'] is False,
                '未ACK/错误ACK意外获得收音权限')
    for kind in ('authorized_subscribe', 'rx_active_status'):
        value = item(kind)
        require(value['error'] == '<nil>', '合法授权或状态查询失败')
        reply(value['reply'], 'active')
    # 报文和地址共同验证：错误ACK来自另一端口，并有实际OPTIONS往返作为处理屏障。
    messages = [(i, r['kind'], r['value'], sip(r['value'])) for i, r in enumerate(data)
                if r['kind'] in ('sip_send', 'sip_receive', 'sip_wrong_source_ack')]
    def request(method, kind='sip_send'):
        return one([m for m in messages if m[1] == kind and m[3][0].startswith(method+' ')], method)
    invite, wrong, barrier, ack, bye = (request('INVITE'), request('ACK', 'sip_wrong_source_ack'),
                                      request('OPTIONS'), request('ACK'), request('BYE'))
    call_id = invite[3][1]['call-id']
    require(invite[0] < wrong[0] < barrier[0] < ack[0] < bye[0]
            and wrong[2]['source'] == barrier[2]['source'] != invite[2]['source']
            and ack[2]['source'] == bye[2]['source'] == invite[2]['source'], 'SIP授权来源/时序错误')
    require(all(m[2]['target'] == config['sip']['listen'] for m in (invite, wrong, barrier, ack, bye))
            and config['sip']['listen'].startswith('127.0.0.1:'), 'SIP实际请求没有指向同一隔离Server')
    require(invite[0] < position('before_ack_subscribe') < wrong[0] < barrier[0] < position('wrong_ack_subscribe')
            < ack[0] < position('authorized_subscribe') < position('rx_active_status') < bye[0]
            < position('rx_bye_revocation') < position('fresh_media_stats') < position('media_ports_rebound')
            < position('fresh_control_zero') < position('cleanup'), '授权/清理观察缺失或顺序错误')
    require(all(m[3][1]['call-id'] == call_id for m in (wrong, ack, bye)) and
            all(m[3][1]['from'] == invite[3][1]['from'] for m in (wrong, ack, bye)) and
            wrong[3][1]['to'] == ack[3][1]['to'] == bye[3][1]['to'] and
            wrong[3][1]['cseq'] == ack[3][1]['cseq'] == '1 ACK' and bye[3][1]['cseq'] == '2 BYE', 'SIP对话身份不符')
    for request_row, cseq in ((invite, '1 INVITE'), (barrier, '1 OPTIONS'), (bye, '2 BYE')):
        response = one([m for m in messages if m[1] == 'sip_receive' and m[3][0] == 'SIP/2.0 200 OK'
                        and m[3][1]['call-id'] == request_row[3][1]['call-id'] and m[3][1]['cseq'] == cseq], '实际200回应')
        require(response[0] > request_row[0] and response[2]['source'] == request_row[2]['target']
                and response[2]['target'] == request_row[2]['source'], 'SIP回应地址/顺序错误')
        req_headers, rep_headers = request_row[3][1], response[3][1]
        require(rep_headers['from'] == req_headers['from'] == invite[3][1]['from']
                and rep_headers['via'].split(';')[0] == req_headers['via'].split(';')[0]
                and one(re.findall(r'(?:^|;)branch=([^;]+)', rep_headers['via']), '回应Via分支')
                    == one(re.findall(r'(?:^|;)branch=([^;]+)', req_headers['via']), '请求Via分支'), 'SIP回应From/Via事务不符')
        if cseq == '1 INVITE':
            require(rep_headers['to'] == ack[3][1]['to'] == wrong[3][1]['to'] == bye[3][1]['to']
                    and rep_headers['to'].split(';tag=')[0] == invite[3][1]['to']
                    and len(re.findall(r';tag=[^;]+', rep_headers['to'])) == 1
                    and response[0] < position('before_ack_subscribe') < wrong[0], 'INVITE 200与实际ACK/BYE对话tag不符')
            answer_audio = one(re.findall(rb'^m=audio (\d+) RTP/AVP ([^\r]+)', response[3][2], re.M), '实际200 SDP音频')
            require(int(answer_audio[0]) == config['media']['port_start']
                    and case.split('_')[1].encode() in answer_audio[1].split()
                    and b'c=IN IP4 127.0.0.1\r\n' in response[3][2], 'INVITE 200 SDP与实际RTP链路不符')
        elif cseq == '2 BYE':
            require(rep_headers['to'] == req_headers['to'], 'BYE回应对话tag不符')
        if cseq == '1 OPTIONS':
            require(response[0] < position('wrong_ack_subscribe'), '错误ACK缺少实际处理屏障')
    law = int(case.split('_')[1])
    inputs = [rtp(bytes.fromhex(r['value']['hex']), law) for r in data if r['kind'] == 'rtp_send']
    invite_audio = one(re.findall(rb'^m=audio (\d+) RTP/AVP ([^\r]+)', invite[3][2], re.M), '实际SDP音频')
    require(str(law).encode() in invite_audio[1].split(), 'SIP SDP未提供当前压扩律')
    require(all(r['value']['target'] == '127.0.0.1:'+str(config['media']['port_start'])
                and r['value']['source'] == '127.0.0.1:'+invite_audio[0].decode()
                for r in data if r['kind'] == 'rtp_send'), '实际RTP地址不属于本次SIP媒体会话')
    deliveries = [r['value'] for r in data if r['kind'] == 'rx_sdk_delivered']
    for delivery in deliveries:
        frame(delivery['Frame'], delivery)
    samples(inputs, [v['Frame'] for v in deliveries], law, 8)
    require(all(i < bye[0] for i, r in enumerate(data) if r['kind'] == 'rx_sdk_delivered'), '挂断后仍交付原音频')
    status = item('rx_active_status')
    require(all(v['Frame']['Session'] == status['reply']['session']
                and v['Frame']['SubscriptionID'] == status['reply']['subscription_id'] for v in deliveries)
            and item('authorized_subscribe')['reply']['subscription_id'] == status['reply']['subscription_id'], 'SIP授权/状态与交付身份不符')
    require(status['reply']['submitted_samples'] == 1280 and status['reply']['submitted_events'] == 9
            and status['snapshot']['DeliveredSamples'] == status['snapshot']['ReceivedSamples'] == 1280
            and status['snapshot']['FreshnessFailures'] == status['snapshot']['ClockFailures'] == status['snapshot']['DroppedSamples'] == 0,
            'Server新鲜交付计数不一致')
    require(item('rx_bye_revocation')['error'] == REVOKED, '实际BYE没有撤销读授权')
    stats = item('fresh_media_stats')
    require(stats['error'] == '<nil>' and stats['reply']['ok'] is True, '清理后没有实际媒体查询')
    zero_media(stats['reply']['stats'], 8)
    require(item('media_ports_rebound') == list(range(config['media']['port_start'], config['media']['port_end']+1))
            and config['media']['port_end'] == config['media']['port_start']+3, '四个实际媒体端口未全部重新绑定')
    for kind in ('fresh_control_zero', 'cleanup'):
        require(all(item(kind)[k] == 0 for k in ('active_calls', 'established_calls', 'active_timers')), '控制会话/事务未清空')
    require(item('fresh_control_zero')['worker'] == item('worker_ready'), '清理时更换了实际worker')
    cleanup = item('cleanup')
    require(cleanup['normal_server_drain'] is True and cleanup['test_failed'] is False
            and cleanup['control_sha256_after'] == binaries['server_test']
            and cleanup['media_sha256_after'] == binaries['media'], 'Server清理路径或结束二进制不符')


def evaluate_rx(receipt, base, current, as_of, identifier, verify_binaries=True):
    """与PCM发布器相同的证明形状；任何缺件/版本变化均不能保留绿色。"""
    proof = {'id': str(receipt.get('id', 'missing'))+':'+identifier, 'comparison_id': identifier,
             'kind': RX_KIND, 'scope': RX_CLAIMS.get(identifier, ''), 'execution_id': receipt.get('id'),
             'test_names': [TESTS[k]+'/'+c for k in TESTS for c in CASES], 'status': 'missing_evidence',
             'reason': '缺少完整RX原始证据', 'executed': False, 'green_eligible': False,
             'started_at': receipt.get('started_at'), 'finished_at': receipt.get('finished_at')}
    try:
        require(identifier in RX_CLAIMS and receipt['kind'] == RX_KIND and receipt['schema_version'] == '1.0.0', 'RX范围/版本不匹配')
        require(re.fullmatch('rx-[a-f0-9]{32}', receipt['id']), 'RX执行编号无效')
        artifacts = receipt['artifacts']
        require(isinstance(artifacts, dict) and len(artifacts) <= 128, 'RX归档数量超过预算')
        require(len({ref['path'] for ref in artifacts.values()}) == len(artifacts), 'RX原文件路径重复')
        if isinstance(base, dict):
            require(sum(len(base.get(ref['path'], {}).get('data', '')) for ref in artifacts.values()) <= 48 << 20,
                    'RX整体压缩输入超过预算')
        raw, total = {}, 0
        for name, ref in artifacts.items():
            raw[name] = read_artifact(base, ref)
            total += len(raw[name])
            require(total <= 32 << 20, 'RX整体原证据超过预算')
        original = {role: json.loads(raw[role+'/$receipt']) for role in ROLES}
        require(all(r['passed'] is True for r in original.values()), '原始RX运行或构建明确失败')
        expected = {role+'/$receipt' for role in ROLES}
        for role, record in original.items():
            for name, digest in required_files(role, record).items():
                expected.add(role+'/'+name)
                require(sha(raw[role+'/'+name]) == digest, role+'原始文件与实际收据SHA不符')
        require(set(raw) == expected, 'RX归档含未知/缺失原文件')
        pool, build, pause, server, rust = (original[k] for k in ROLES)
        stamps = [(utc(r['started_at']), utc(r['finished_at'])) for r in original.values()]
        require(all(a <= b for a, b in stamps), '原始构建/运行时段倒置')
        start, finish, now = min(a for a, _ in stamps), max(b for _, b in stamps), utc(as_of)
        require(utc(receipt['started_at']) == start and utc(receipt['finished_at']) == finish, '发布信封改写实际时段')
        require(finish <= now+timedelta(minutes=5), 'RX运行证据来自未来')
        require(utc(build['finished_at']) <= utc(pool['started_at']) and all(utc(rust['finished_at']) <= utc(original[k]['started_at'])
                for k in ('pool', 'pause', 'server')), '测试早于所声称的实际二进制构建')
        binaries = {role: spec[2] for role, spec in binary_contract(original).items()}
        require(set(receipt['binaries']) == set(binaries), '二进制角色集合不完整')
        for role, digest in binaries.items():
            ref = receipt['binaries'][role]
            require(ref['sha256'] == digest, '发布二进制SHA不等于实际运行')
            safe_path(ref['path'])
            if verify_binaries:
                require(not isinstance(base, dict), '内嵌证据不含可执行二进制')
                path = Path(base)/ref['path']
                require(path.is_file() and path.resolve().is_relative_to(Path(base).resolve()) and
                        all(not (Path(base)/Path(*Path(ref['path']).parts[:i])).is_symlink()
                            for i in range(1, len(Path(ref['path']).parts)+1)) and
                        0 < path.stat().st_size <= 512 << 20 and sha(path.read_bytes()) == digest, '实际运行二进制缺失或改变')
        require(pool['test_after'] == build['race_binary_sha256'] == binaries['pool_test']
                and pause['test_after'] == binaries['pause_test'] and server['control_binary_after'] == binaries['server_test'], 'Go构建/运行二进制不一致')
        require(all(original[k][field] == binaries['media'] for k, fields in
                    [('pool', ('media_before', 'media_after')), ('pause', ('media_before', 'media_after')),
                     ('server', ('media_binary_before', 'media_binary_after'))] for field in fields), '媒体运行前后SHA不一致')
        require(server['rust_build_receipt_sha256'] == sha(raw['media_build/$receipt'])
                and raw['server/rust-build-receipt.json'] == raw['media_build/$receipt'], 'Server未绑定实际Rust构建收据')
        snapshots = []
        for record in original.values():
            before = source_map(record['source_before'])
            require(before == record['source_after'], '采集过程中源码改变')
            if 'source_after_compile' in record:
                require(before == record['source_after_compile'], 'Go编译时源码改变')
            snapshots.append(before)
        require(pool['source_before'] == build['source_before'], '旧Pool测试源码不同于其实际构建')
        require(server['media_source_before'] == server['media_source_after'] == rust['source_before'], 'Server所用Rust源码不一致')
        # 历史构建闭包逐个校验，禁止从Server并集补回被删掉的旧Pool实现/依赖。
        # Pool旧构建早于pause测试新增，只允许这一已知测试文件时间差；生产文件不例外。
        sdk_paths = {k for k in server['source_before'] if bound_path(k) and
                     (k.startswith(('control/internal/media/', 'control/internal/config/', 'control/vendor/'))
                      or k in ('control/go.mod', 'control/go.sum'))}
        for role in ('pool', 'pool_build', 'pause'):
            expected_paths = sdk_paths - ({'control/internal/media/processing_rx_pause_real_test.go'}
                                         if role != 'pause' else set())
            require(set(original[role]['source_before']) == expected_paths, role+'历史编译闭包缺少或多出源/依赖')
        historical = {}
        for snapshot in snapshots:
            for name, digest in snapshot.items():
                require(name not in historical or historical[name] == digest, '历史运行间重叠源码不一致：'+name)
                historical[name] = digest
        bound = {k: v for k, v in historical.items() if bound_path(k)}
        current_bound = {k: v for k, v in source_map(current).items() if bound_path(k)}
        require('control/vendor/modules.txt' in bound and any(n.endswith('.s') for n in bound)
                and {'control/internal/media/rx_stream.go', 'control/internal/server/rx_stream.go', 'media/src/media/rx_export.rs'} <= bound.keys(),
                '缺少RX实现或汇编/vendor依赖绑定')
        if set(bound) != set(current_bound):
            proof.update(status='stale', reason='当前绑定范围出现新增或删除源码')
            return proof
        if bound != current_bound:
            proof.update(status='stale', reason='当前Go/vendor/本地配置或Rust源码与真实RX运行不同')
            return proof
        # 读取成功、历史来源绑定和二进制核实后，仍须逐条重放实际协议和样本。
        build_steps(original, raw)
        require(pool['exit_code'] == 0 and server['timed_out'] is False, '真实运行失败或超时')
        pool_texts = go_log(raw['pool/tests.jsonl'], TESTS['pool'], utc(pool['started_at']), utc(pool['finished_at']))
        for role in ('pause', 'server'):
            go_log(raw[role+('/tests.log' if role == 'pause' else '/test.log')], TESTS[role],
                   utc(original[role]['started_at']), utc(original[role]['finished_at']))
        rust_count = re.search(rb'test result: ok\. (\d+) passed; 0 failed;', raw['media_build/tests.log'])
        require(rust_count and int(rust_count[1]) >= 170, 'Rust完整单测未通过')
        for case in CASES:
            pool_case(pool_texts[case], case, binaries['media'])
            pause_prefix = 'wire/' if 'pause/wire/'+case+'/helper-ready.json' in raw else 'cases/'
            pause_case(lambda name: raw['pause/'+name], pause_prefix, case)
            server_case(raw['server/wire/'+case+'/wire.jsonl'], case, binaries,
                        utc(server['started_at']), utc(server['finished_at']))
        expires = finish+timedelta(days=30)
        proof.update(status='passed' if now <= expires else 'stale',
                     reason='12个真实子例的样本、原始期限、授权与清理逐项重放通过' if now <= expires else 'RX真实运行证据超过30天',
                     executed=True, green_eligible=now <= expires, started_at=receipt['started_at'], finished_at=receipt['finished_at'],
                     expires_at=expires.isoformat().replace('+00:00', 'Z'), source_snapshot_sha256=sha(wire(bound)),
                     source_files=len(bound), verified_binary_sha256=binaries,
                     binding_contract='历史各运行源码重叠逐件一致；当前完整Go及vendor(含.s/modules.txt)、fs_templates、config/local.json、Rust src/Cargo绑定；未绑定Python/native/网页文档及其他配置',
                     log=receipt['artifacts']['server/test.log'], evidence_cases=12, decoded_samples=8960)
    except (KeyError, ValueError, TypeError, IndexError, OSError, OverflowError, UnicodeError, struct.error, zlib.error) as exc:
        proof.update(status='failed', reason=str(exc) or type(exc).__name__)
    return proof
