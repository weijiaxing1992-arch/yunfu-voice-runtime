#!/usr/bin/env python3
"""逐ID比较已发布基线与当前证据，生成首屏进展和全量推进清单。

实现状态、限定运行结果、完整原版契约是不同维度；本工具不授予新的通过结论。
每一条原目录记录都必须进入工作清单，已本地通过仍保留完整契约待对照。
"""
import argparse
from collections import Counter
import csv
import hashlib
import io
import json
from pathlib import Path
import build_runtime_verification

ROOT = Path(__file__).resolve().parents[1]
VERSION = '1.13.0'


def require(condition, message):
    """证据门禁在 Python 优化模式下也必须执行。"""
    if not condition:
        raise ValueError(message)


def wire(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + '\n').encode()


def sha(value):
    return hashlib.sha256(value).hexdigest()


def index(rows, key):
    """拒绝重复编号，避免后一个记录静默覆盖先前的失败记录。"""
    result = {row[key]: row for row in rows}
    require(len(result) == len(rows), '推进清单含重复ID')
    return result


def snapshot(version, rows):
    return {'version': version, 'comparison_entries': len(rows),
            'implementation_counts': dict(sorted(Counter(x['status'] for x in rows).items())),
            'local_counts': dict(sorted(Counter(x['local_status'] for x in rows).items())),
            'green_entries': sum(x['green_eligible'] for x in rows)}


def delivery_scope(row):
    """产品范围来自首期Voice Agent需求；延后只影响排序，不改变兼容/运行结论。"""
    category, name, identifier = row['category'], row['name'].lower(), row['id']
    module = row.get('module', '').lower()
    deferred = ('conference', 'fax', 'spandsp', 'video', 'h323', 'skinny', 'verto', 'lua', 'python', 'perl', 'javascript', 'v8', 'java', 'erlang')
    if identifier == 'key-media-conference' or category in {'fs_conference_subcommand', 'fs_chat_application'} or any(word in module or word in name for word in deferred):
        return 'deferred', '首期延后：通用PBX、其他端点或脚本能力'
    if category in {'fs_native_function', 'fs_module'}:
        return 'deferred', '首期延后：完整原生符号与模块宿主'
    if identifier in {'key-media-capacity', 'key-api-pcm-stream', 'key-api-rx-sdk'} or identifier.startswith(('key-media-', 'key-codec-', 'key-ipc-')):
        return 'core', '媒体闭环与稳定性'
    if category == 'esl' or identifier.startswith(('key-esl-', 'key-sip-')):
        return 'compatibility_adapter', '电话线路与ESL适配'
    if category == 'fs_event' and any(word in name for word in ('channel', 'playback', 'dtmf', 'record')):
        return 'compatibility_adapter', '电话业务事件'
    if category in {'fs_api', 'fs_application'} and any(word in name for word in ('originate', 'uuid_', 'bridge', 'transfer', 'read', 'digit', 'playback', 'record', 'speak', 'ivr', 'answer', 'hangup', 'park', 'sleep', 'socket')):
        return 'compatibility_adapter', 'AI电话应用与API'
    return 'on_demand', '按目标线路与现网用法再纳入'


def priority(row):
    """先推进核心和常用适配；完整原目录保留，不靠改研发范围减少未实现数量。"""
    scope, group = delivery_scope(row)
    return ('P0' if scope == 'core' else 'P1' if scope == 'compatibility_adapter' else 'P2'), group


def build(comparison_raw, runtime_raw, baseline_raw):
    """只从已校验的运行记录派生增量；不存在的旧ID必须公开为删除，不能藏分母。"""
    comparison, runtime, baseline = map(json.loads, (comparison_raw, runtime_raw, baseline_raw))
    entries = index(comparison['entries'], 'id')
    proofs = index(runtime['entries'], 'comparison_id')
    old = index(baseline['entries'], 'id')
    require(set(entries) == set(proofs), '当前对照与运行证据ID不一致')
    require(runtime['comparison_sha256'] == sha(comparison_raw), '运行证据不属于当前对照')
    current = []
    for identifier, row in entries.items():
        proof = proofs[identifier]
        require(proof['implementation_status'] == row['status'], '运行证据实现状态不一致')
        require(type(proof['green_eligible']) is bool, '绿色字段不是布尔值')
        require(not proof['green_eligible'] or proof['local_status'] == 'passed', '非通过记录被标绿')
        current.append({'id': identifier, 'status': row['status'], 'local_status': proof['local_status'], 'green_eligible': proof['green_eligible']})
    summary = snapshot(VERSION, current)
    require(summary['local_counts'] == runtime['summary']['by_local_status'], '当前本地统计失真')
    require(summary['green_entries'] == runtime['summary']['green_entries'], '当前绿色统计失真')
    for row in old.values():
        require(type(row['green_eligible']) is bool and (not row['green_eligible'] or row['local_status'] == 'passed'), '基线绿色字段无效')
    old_green = {key for key, row in old.items() if row['green_eligible']}
    new_green = {row['id'] for row in current if row['green_eligible']}
    changes = {
        'new_green': [{'id': key, 'name': entries[key]['name'], 'scope': proofs[key]['scope']} for key in sorted(new_green - old_green)],
        'lost_green': [{'id': key, 'name': entries.get(key, {}).get('name', key), 'reason': proofs.get(key, {}).get('reason', '当前目录已删除此ID')} for key in sorted(old_green - new_green)],
        'implementation_changed': [{'id': key, 'name': entries[key]['name'], 'from': old[key]['status'], 'to': entries[key]['status']} for key in sorted(entries.keys() & old.keys()) if entries[key]['status'] != old[key]['status']],
        'added_ids': sorted(entries.keys() - old.keys()), 'removed_ids': sorted(old.keys() - entries.keys()),
    }
    require(len(new_green) - len(old_green) == len(changes['new_green']) - len(changes['lost_green']), '净变化不等于新增减撤回')
    progress = {'schema_version': '1.0.0', 'comparison_sha256': sha(comparison_raw), 'runtime_sha256': sha(runtime_raw), 'baseline_sha256': sha(baseline_raw),
                'source_sha256': sha(wire(runtime['source_snapshot'])), 'baseline': snapshot(baseline['version'], list(old.values())),
                'current': summary, 'changes': changes,
                'scope': '通过数只代表当前源码下的限定本地断言。目录条目并非独立功能数量；增量不是FreeSWITCH完整兼容率。'}
    worklist = []
    for identifier, row in entries.items():
        proof = proofs[identifier]
        rank, group = priority(row)
        if row['status'] == 'not_implemented':
            action = '实现原版调用入口及业务副作用，再执行正向、错误、事件与回收对照'
        elif row['status'] == 'export_only':
            action = '实现配置在运行时的真实消费与重载，再对照配置生效和错误行为'
        elif row['status'] == 'not_verified':
            action = '将已有验收定义实例化为双端可运行测试；未知结果保留待验收'
        elif not proof['green_eligible']:
            action = '补齐当前源码绑定的真实运行测试；失败先修复，过期证据重新执行'
        else:
            action = '限定本地断言已通过；继续补全所列差异并进行完整原版契约对照'
        worklist.append({'id': identifier, 'name': row['name'], 'category': row['category'], 'module': row['module'],
                         'priority': rank, 'group': group, 'delivery_scope': delivery_scope(row)[0], 'implementation_status': row['status'], 'local_status': proof['local_status'],
                         'green_eligible': proof['green_eligible'], 'complete_contract_passed': False,
                         'scope': proof['scope'], 'remaining_differences': row['differences'], 'next_action': action,
                         'document_path': row['document_path'], 'source_url': row['source_url']})
    worklist.sort(key=lambda x: (x['priority'], x['group'], x['id']))
    return progress, {'schema_version': '1.0.0', 'version': VERSION, 'comparison_sha256': sha(comparison_raw), 'runtime_sha256': sha(runtime_raw),
                      'summary': {'total': len(worklist), 'by_priority': dict(sorted(Counter(x['priority'] for x in worklist).items())),
                                  'by_category': dict(sorted(Counter(x['category'] for x in worklist).items())), 'by_delivery_scope': dict(sorted(Counter(x['delivery_scope'] for x in worklist).items())), 'complete_contract_passed': 0},
                      'scope': '原始目录全量逐ID保留，按Voice Agent核心、兼容适配、按需和延后排序；延后不是已实现或兼容通过。', 'entries': worklist}


def outputs(root=ROOT):
    """先复核原始执行证据，再生成公开视图；单改统计JSON不能授予绿色。"""
    build_runtime_verification.validate_runtime(root)
    folder = root / 'docs/api'
    progress, worklist = build(*[(folder / name).read_bytes() for name in ('comparison.json', 'runtime-verification.json', 'compatibility-baseline-v1.12.json')])
    lines = ['# 兼容缺口逐项推进清单', '', progress['scope'], '',
             f"基线 {progress['baseline']['version']} → 当前 {VERSION}：限定本地通过 {progress['baseline']['green_entries']} → {progress['current']['green_entries']}，新增 {len(progress['changes']['new_green'])}、撤回 {len(progress['changes']['lost_green'])}。", '',
             '“完整契约待验收”是177份验收定义和容量目标的实现分类；“本地未执行”是尚无绑定当前源码运行证据的条目。两者不可相加或替代。', '',
             '## 当前计数', '', '| 维度 | 状态 | 条数 |', '|---|---|---|']
    for label, counts in [('实现', progress['current']['implementation_counts']), ('本地执行', progress['current']['local_counts'])]:
        lines += [f'| {label} | `{key}` | {value} |' for key, value in counts.items()]
    lines += ['', '## 按层剩余条目', '', '下表按目录记录统计，原生声明、重复配置和验收定义不是独立业务能力。各列是不同维度，不能横向相加。', '',
              '| 层／类别 | 全部 | 未实现入口 | 本地未执行 | 本地限定通过 |', '|---|---:|---:|---:|---:|']
    for category in sorted({row['category'] for row in worklist['entries']}):
        rows = [row for row in worklist['entries'] if row['category'] == category]
        lines.append(f"| `{category}` | {len(rows)} | {sum(row['implementation_status']=='not_implemented' for row in rows)} | {sum(row['local_status']=='not_run' for row in rows)} | {sum(row['green_eligible'] for row in rows)} |")
    lines += ['', '## 本轮新增限定通过', '', '| 原始ID | 名称 | 已测范围 |', '|---|---|---|']
    def cell(value):
        return str(value).replace('|', '\\|').replace('\n', '<br>')
    lines += [f"| `{row['id']}` | {cell(row['name'])} | {cell(row['scope'])} |" for row in progress['changes']['new_green']]
    if not progress['changes']['new_green']:
        lines.append('| — | 暂无新增 | 不以修改目录状态替代执行测试 |')
    lines += ['', '## 全量任务及推进顺序', '', f"按云蝠Voice Runtime首期范围排序：P0核心媒体与稳定性；P1电话业务兼容适配；P2按现网需求纳入或首期延后。完整{len(worklist['entries'])}个原始ID及状态保留；延后不标绿、不改变分母。产品阶段里程碑和性能目标见[研发方向](voice-runtime-direction.md)及[研发清单](voice-runtime-roadmap.json)。", '',
              '[下载全部逐项任务 JSON](compatibility-worklist.json) · [下载全部逐项任务 CSV](compatibility-worklist.csv) · [查看计数变化及原始ID](compatibility-progress.json)', '',
              '后续每轮必须先冻结上版证据，再实现、执行、记录失败或通过并重新发布；不能缩减分母、把订阅成功当事件已实现、把API目录存在当业务成功，或给未执行记录标绿。', '']
    stream = io.StringIO(newline='')
    writer = csv.writer(stream)
    writer.writerow(['优先级', '首期范围', '原始ID', '名称', '分类', '模块', '实现状态', '本地验证状态', '完整契约通过', '下一步动作', '剩余差异', '已测范围', '接口文档'])
    for row in worklist['entries']:
        values = [row[key] for key in ('priority', 'delivery_scope', 'id', 'name', 'category', 'module', 'implementation_status', 'local_status', 'complete_contract_passed', 'next_action')] + ['；'.join(row['remaining_differences']), row['scope'], row['document_path']]
        # 名称来自原版目录；避免用户用表格工具打开时将外部文本解释为公式。
        writer.writerow(["'" + value if isinstance(value, str) and value[:1] in ('=', '+', '-', '@', '\t', '\r') else value for value in values])
    return {'compatibility-progress.json': wire(progress), 'compatibility-worklist.json': wire(worklist),
            'compatibility-worklist.csv': stream.getvalue().encode('utf-8-sig'), 'compatibility-worklist.md': '\n'.join(lines).encode()}


def validate(root=ROOT):
    """后台文档发布门禁：输入变化后必须重新生成计数和逐ID任务。"""
    for name, raw in outputs(root).items():
        path = root / 'docs/api' / name
        require(path.is_file() and path.read_bytes() == raw, '兼容进展已过期：' + name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    result = outputs()
    for name, raw in result.items():
        path = ROOT / 'docs/api' / name
        if args.check:
            require(path.is_file() and path.read_bytes() == raw, '兼容进展已过期：' + name)
        else:
            path.write_bytes(raw)
    progress = json.loads(result['compatibility-progress.json'])
    print(json.dumps({'passed': True, 'current': progress['current'], 'new_green': len(progress['changes']['new_green']), 'lost_green': len(progress['changes']['lost_green'])}, ensure_ascii=False))


if __name__ == '__main__':
    main()
