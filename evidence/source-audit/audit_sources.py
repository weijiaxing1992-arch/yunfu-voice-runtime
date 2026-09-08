"""只读核对交付源、私有补丁和历史证据；仅向本审计目录写机器清单。"""
from pathlib import Path
from datetime import datetime, timezone
from collections import Counter
import hashlib
import json
import re

ROOT = Path(__file__).resolve().parents[2]
OUT = Path(__file__).resolve().parent
ASR = ROOT / 'work/voice-runtime-m2-asr'
MAIN = ROOT / 'outputs/rustswitch'
CANDIDATE = ASR / 'project'
BUNDLE = ROOT / 'outputs/云蝠VoiceRuntime-项目资料-2026-09-08'
SOURCE_ROOTS = {'control', 'media', 'native', 'config', 'deploy', 'tests', 'tools', 'docs'}
ROOT_FILES = {'README.md', 'Makefile', '.gitignore'}
EXCLUDED_COMPONENTS = {'__pycache__', '.git', '.DS_Store', 'target', 'node_modules'}


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None


def read(path):
    return json.loads(path.read_text())


def save(name, value):
    (OUT / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def inventory(base):
    included, excluded = {}, []
    for p in sorted(base.rglob('*')):
        if p.is_symlink():
            raise ValueError('不自动跟随源码符号链接: ' + str(p))
        if not p.is_file():
            continue
        rel = p.relative_to(base)
        if (rel.parts[0] not in SOURCE_ROOTS and str(rel) not in ROOT_FILES) or any(x in EXCLUDED_COMPONENTS for x in rel.parts) or p.suffix in {'.pyc', '.pyo'}:
            excluded.append(str(rel))
            continue
        included[str(rel)] = {'sha256': sha(p), 'bytes': p.stat().st_size}
    canonical = json.dumps({p: x['sha256'] for p, x in included.items()}, sort_keys=True, separators=(',', ':')).encode()
    return {'base': str(base.relative_to(ROOT)), 'count': len(included), 'bytes': sum(x['bytes'] for x in included.values()),
            'path_sha256_map_sha256': hashlib.sha256(canonical).hexdigest(), 'files': included, 'excluded': excluded}


def bind(base, mapping):
    rows = []
    for p, value in mapping.items():
        expected = value.get('sha256') if isinstance(value, dict) else value
        actual = sha(base / p)
        rows.append({'path': p, 'expected': expected, 'actual': actual, 'matches': actual == expected})
    return {'total': len(rows), 'matched': sum(x['matches'] for x in rows), 'mismatches': [x for x in rows if not x['matches']], 'files': rows}


at = datetime.now(timezone.utc).isoformat()
main = inventory(MAIN)
candidate = inventory(CANDIDATE)
delta = []
for p in sorted(main['files'].keys() | candidate['files'].keys()):
    a, b = main['files'].get(p), candidate['files'].get(p)
    if a != b:
        delta.append({'path': p, 'change': 'added' if a is None else 'removed' if b is None else 'modified', 'main': a, 'candidate': b})
source_summary = {'at': at, 'read_only': True, 'main': main, 'candidate': candidate, 'delta_count': len(delta), 'delta_counts': dict(Counter(x['change'] for x in delta)), 'delta': delta}
save('source-inventory.json', source_summary)

# 收据中的测试计数从原始Go事件再算；不把passed字段直接当作证明。
full_path = ASR / 'full-review-01/receipt.json'
full = read(full_path)
full_binding = bind(CANDIDATE, full['source_after'])
events = [json.loads(x) for x in (full_path.parent / 'go.jsonl').read_text().splitlines() if x.strip()]
events_by_action = dict(Counter(x['Action'] for x in events if x.get('Test') and x['Action'] in {'pass', 'fail', 'skip'}))
top_by_action = dict(Counter(x['Action'] for x in events if x.get('Test') and '/' not in x['Test'] and x['Action'] in {'pass', 'fail', 'skip'}))
package_by_action = dict(Counter(x['Action'] for x in events if not x.get('Test') and x['Action'] in {'pass', 'fail', 'skip'}))
log_binding = [{'name': x['name'], 'exit_code': x['returncode'], 'expected': x['sha256'], 'actual': sha(full_path.parent / x['log'])} for x in full['steps']]
binaries = [{'path': p, 'expected': s, 'actual': sha(Path(p))} for p, s in full['binaries'].items()]
binaries += [{'path': str(full_path.parent / p), 'expected': s, 'actual': sha(full_path.parent / p)} for p, s in full['built'].items()]
doc = read(ASR / 'source-doc-update-01/receipt.json')
doc_binding = bind(CANDIDATE, doc['after'])
evidence = {'at': at, 'full_review_receipt_sha256': sha(full_path), 'full_review_source_before_equals_after': full['source_before'] == full['source_after'], 'full_review_binding': full_binding,
            'test_events_recomputed': events_by_action, 'top_level_events_recomputed': top_by_action, 'package_events_recomputed': package_by_action,
            'logs': log_binding, 'binaries': binaries, 'source_documentation_binding': doc_binding,
            'documentation_receipt_sha256': sha(ASR / 'source-doc-update-01/receipt.json'),
            'full_review_scope': full['scope'], 'source_docs_published': doc['published']}
save('evidence-binding.json', evidence)

# 文件级核对已测补丁和后续工作稿，区分原补丁与未复验的两个新文件版本。
bench = ASR / 'benchmark-fix-01'
patch_manifest = read(bench / 'files.json')
bench_test = read(bench / 'test-02/receipt.json')
patch_rows = []
for f in patch_manifest:
    p = f['path']
    restored = OUT / 'restore-tested-benchmark' / p
    test_hash = bench_test['source_after'][p]['sha256']
    patch_rows.append({**f, 'current_baseline_sha256': sha(CANDIDATE / p), 'baseline_matches': sha(CANDIDATE / p) == f['baseline_sha256'],
                       'test02_sha256': test_hash, 'tested_manifest_matches': test_hash == f['candidate_sha256'],
                       'restored_sha256': sha(restored), 'restored_matches_tested': sha(restored) == test_hash,
                       'working_sha256': sha(bench / p), 'working_matches_tested': sha(bench / p) == test_hash})
staging = read(ROOT / 'work/voice-runtime-m2-asr-staging/test-02/receipt.json')
staging_bind = bind(CANDIDATE / 'control', {p: staging['candidate_after'][p] for p in staging['changed_files']})
prefix = read(ASR / 'initial-prefix-review/final-receipt.json')
prefix_bind = bind(ROOT, prefix['files'])
patches = {'at': at, 'benchmark_test02_receipt_sha256': sha(bench / 'test-02/receipt.json'), 'benchmark_original_patch_sha256': sha(bench / 'unified.patch'),
           'benchmark_test03_exists': (bench / 'test-03/receipt.json').exists(), 'benchmark_files': patch_rows,
           'benchmark_working_vs_test02': bind(bench, bench_test['source_after']),
           'atomic_stats_private_directory_exists': (ASR / 'worker-observation-fix-01').exists(),
           'staging_boundary_patch_binding': staging_bind, 'initial_prefix_final_binding': prefix_bind}
save('patch-state.json', patches)

# 对根代理完成的源包逐字节核对；不把交付文档或新构建产物混入原源码身份。
bundle_sources = {}
for name, original in [('main', main), ('asr-candidate', candidate)]:
    packed = inventory(BUNDLE / 'source' / name)
    bundle_sources[name] = {'count': packed['count'], 'matches_original_source_map': packed['files'] == original['files'],
                            'source_manifest_binding': bind(BUNDLE / 'source' / name, {x['path']: x['sha256'] for x in read(BUNDLE / 'source' / (name + '-manifest.json'))['files']}),
                            'path_sha256_map_sha256': packed['path_sha256_map_sha256']}
vendor = BUNDLE / 'dependencies/rust-vendor'
crates = []
for d in sorted(x for x in vendor.iterdir() if x.is_dir()):
    check = read(d / '.cargo-checksum.json')
    files = bind(d, check['files'])
    all_files = {str(p.relative_to(d)) for p in d.rglob('*') if p.is_file()}
    crates.append({'directory': d.name, 'package_checksum': check['package'], 'file_count': files['total'], 'mismatches': files['mismatches'],
                   'unexpected_files': sorted(all_files - set(check['files']) - {'.cargo-checksum.json'}), 'checksum_file_sha256': sha(d / '.cargo-checksum.json')})
lock = (CANDIDATE / 'media/Cargo.lock').read_text()
lock_crates = []
for block in lock.split('[[package]]')[1:]:
    fields = dict(re.findall(r'^(name|version|source|checksum) = "([^"]+)"$', block, re.M))
    if 'checksum' in fields:
        lock_crates.append(fields)
vendor_checks = {x['package_checksum'] for x in crates}
builds = []
for name in ['main', 'asr-candidate']:
    bp = OUT / ('build-' + name + '-01')
    receipt = read(bp / 'receipt.json')
    builds.append({'variant': name, 'receipt_sha256': sha(bp / 'receipt.json'), 'steps': receipt['steps'],
                   'source_unchanged_recorded': receipt['source_unchanged'], 'logs': bind(bp, receipt['logs']),
                   'current_outputs': {x.name: sha(x) for x in bp.iterdir() if x.is_file() and x.name in {'rustswitch', 'callbench', 'asr-mock', 'librustswitch_g711.dylib'}},
                   'media_output_current_sha256': sha(bp / 'cargo-target/release/rustswitch-media'),
                   'scope': receipt['scope']})
bundle_audit = {'at': at, 'sources': bundle_sources, 'rust_vendor_crates': crates, 'cargo_lock_registry_crates': len(lock_crates),
                'rust_vendor_crate_count': len(crates), 'lock_crates_missing_vendor': [x for x in lock_crates if x['checksum'] not in vendor_checks],
                'opus_archive_sha256': sha(BUNDLE / 'dependencies/opus-1.5.2.tar.gz'), 'builds': builds,
                'delivery_doc_gate_receipt': read(BUNDLE / 'evidence/delivery-doc-gate/receipt.json'),
                'delivery_doc_gate_receipt_sha256': sha(BUNDLE / 'evidence/delivery-doc-gate/receipt.json')}
save('delivery-source-binding.json', bundle_audit)

# 完成时重新读取两棵源树；证明本审计期间所报告的源身份没有漂移。
assert inventory(MAIN)['files'] == main['files']
assert inventory(CANDIDATE)['files'] == candidate['files']
print(json.dumps({'main_files': main['count'], 'candidate_files': candidate['count'], 'delta': source_summary['delta_counts'],
                  'full_review_match': full_binding['matched'], 'full_review_total': full_binding['total'], 'doc_match': doc_binding['matched'],
                  'benchmark_working_changed': [x['path'] for x in patch_rows if not x['working_matches_tested']],
                  'source_maps_match_delivery': {k: v['matches_original_source_map'] for k, v in bundle_sources.items()},
                  'rust_vendor_crates': len(crates), 'source_stable_during_audit': True}, ensure_ascii=False))
