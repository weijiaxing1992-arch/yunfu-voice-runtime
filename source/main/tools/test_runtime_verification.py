#!/usr/bin/env python3
"""运行证据门禁的纯内存/临时文件回归；这里的合成报告仅用于负例，不进入交付证据。"""
import copy
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import build_runtime_verification as audit

NOW = datetime(2026, 9, 6, 10, tzinfo=timezone.utc)
KEY = 'key-http-authentication'
PACKAGE, TESTS, _ = audit.CLAIMS[KEY]


class RuntimeVerificationTest(unittest.TestCase):
    """用小型合成对照与日志测门禁，不启动服务或伪造真实测试执行。"""
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root, self.evidence = self.base / 'project', self.base / 'evidence'
        (self.root / 'control').mkdir(parents=True)
        (self.root / 'control/main.go').write_text('package main\n')
        (self.root / 'docs/api').mkdir(parents=True)
        self.evidence.mkdir()
        self.comparison = {'entries': [{'id': KEY, 'status': 'implemented'},
                                       {'id': 'key-media-capacity', 'status': 'not_verified'},
                                       {'id': 'fs-api-unimplemented', 'status': 'not_implemented'}]}
        (self.root / 'docs/api/comparison.json').write_bytes(audit.wire(self.comparison))
        self.source = audit.source_snapshot(self.root)

    def artifact(self, path, value):
        """建立带正确指纹的测试文件，修改内容须显式重建引用。"""
        raw = value if isinstance(value, bytes) else audit.wire(value)
        (self.evidence / path).write_bytes(raw)
        return {'path': path, 'sha256': audit.sha(raw)}

    def receipt(self, identifier='go-fixture', age=0, key=KEY):
        time = NOW - timedelta(days=age)
        package, tests, _ = audit.CLAIMS[key]
        rows = [{'Time': time.isoformat(), 'Action': 'start', 'Package': package}]
        for name in tests:
            rows += [{'Time': time.isoformat(), 'Action': action, 'Package': package, 'Test': name}
                     for action in ['run', 'pass']]
        rows += [{'Time': time.isoformat(), 'Action': 'pass', 'Package': package}]
        record = {'schema_version': '1.0.0', 'id': identifier, 'kind': 'go_test_json',
                  'started_at': (time - timedelta(seconds=1)).isoformat(),
                  'finished_at': (time + timedelta(seconds=1)).isoformat(),
                  'binding_method': 'source_before_and_after_actual_execution',
                  'source_before': self.source, 'source_after': self.source,
                  'command': audit.GO_COMMAND.copy(), 'returncode': 0, 'timed_out': False,
                  'tool_sha256': '0' * 64,
                  'log': self.artifact(identifier + '.jsonl', b''.join(audit.wire(r).replace(b'\n', b'') + b'\n' for r in rows)),
                  'stderr': self.artifact(identifier + '.stderr', b'')}
        return record

    def save(self, receipt, name=None):
        (self.evidence / ((name or receipt['id']) + '.receipt.json')).write_bytes(audit.wire(receipt))

    def build(self):
        return audit.build(self.evidence, self.root, self.base / 'no-history', NOW)

    def row(self, data, key=KEY):
        return next(r for r in data['entries'] if r['comparison_id'] == key)

    def evaluate(self, receipt):
        return audit.evaluate_go(receipt, self.evidence, audit.source_snapshot(self.root), NOW, KEY, audit.CLAIMS[KEY])

    def change_trace(self, receipt, transform):
        rows = [json.loads(line) for line in (self.evidence / receipt['log']['path']).read_text().splitlines()]
        rows = transform(rows)
        receipt['log'] = self.artifact(receipt['log']['path'], b'\n'.join(json.dumps(r).encode() for r in rows))

    def test_complete_bound_subset_is_green_without_changing_implementation(self):
        self.save(self.receipt())
        data = self.build()
        self.assertEqual(data['summary']['green_entries'], 1)
        self.assertTrue(self.row(data)['green_eligible'])
        self.assertEqual(self.row(data)['implementation_status'], 'implemented')
        self.assertEqual(self.row(data, 'fs-api-unimplemented')['local_status'], 'not_run')
        self.assertFalse(data['upstream_runtime_executed'])
        self.assertEqual(audit.validate_runtime(self.root, data, NOW)['green_entries'], 1)
        # 删除外部证据后，离线发布检查仍能重算嵌入日志SHA及完整执行事件。
        for path in self.evidence.iterdir(): path.unlink()
        self.assertEqual(audit.validate_runtime(self.root, data, NOW)['green_entries'], 1)

    def test_missing_or_corrupt_log_never_green(self):
        for mode in ['missing', 'hash', 'truncated', 'bad-command', 'boolean-code']:
            with self.subTest(mode=mode):
                record = self.receipt()
                if mode == 'missing': (self.evidence / record['log']['path']).unlink()
                elif mode == 'hash': record['log']['sha256'] = 'f' * 64
                elif mode == 'truncated': record['log'] = self.artifact(record['log']['path'], b'{')
                elif mode == 'bad-command': record['command'].remove('-race')
                else: record['returncode'] = False
                self.assertFalse(self.evaluate(record)['green_eligible'])

    def test_vendor_assembly_and_module_selection_invalidate_old_proof(self):
        """第三方Go依赖的汇编和模块清单变更必须使旧通过失效，不能只核对.go和go.sum。"""
        vendor = self.root / 'control/vendor'
        source = vendor / 'example.org/clock/read_clock.s'
        source.parent.mkdir(parents=True)
        source.write_text('// synthetic assembly fixture\n')
        modules = vendor / 'modules.txt'
        modules.write_text('# example.org/clock v1.0.0\n')
        self.source = audit.source_snapshot(self.root)
        record = self.receipt()
        self.assertTrue(self.evaluate(record)['green_eligible'])
        self.assertIn('control/vendor/example.org/clock/read_clock.s', self.source)
        for path in (source, modules):
            original = path.read_bytes()
            path.write_bytes(original + b'// modified\n')
            self.assertFalse(self.evaluate(record)['green_eligible'])
            path.write_bytes(original)
        modules.unlink()
        self.assertFalse(self.evaluate(record)['green_eligible'])

    def test_sources_added_changed_deleted_and_changed_during_run_are_stale(self):
        for mode in ['added', 'changed', 'deleted', 'during']:
            with self.subTest(mode=mode):
                path = self.root / 'control/main.go'
                path.write_text('package main\n')
                (self.root / 'control/new.go').unlink(missing_ok=True)
                record = self.receipt()
                if mode == 'added': (self.root / 'control/new.go').write_text('package main\n')
                elif mode == 'changed': path.write_text('package different\n')
                elif mode == 'deleted': path.unlink()
                else: record['source_before'] = {'old.go': 'f' * 64}
                self.assertEqual(self.evaluate(record)['status'], 'stale')

    def test_local_go_native_inputs_are_bound_even_outside_vendor(self):
        """本包新增汇编/CGO文件不能绕开已有Go源码验证。"""
        record = self.receipt()
        folder = self.root / 'control/internal/clock'
        folder.mkdir(parents=True)
        for suffix in audit.GO_NATIVE_SUFFIXES:
            with self.subTest(suffix=suffix):
                path = folder / ('local_clock.' + suffix)
                path.write_bytes(b'local compilation input\n')
                self.assertEqual(self.evaluate(record)['status'], 'stale')
                path.unlink()
                self.assertTrue(self.evaluate(record)['green_eligible'])

    def test_embedded_document_headers_are_not_compilation_inputs(self):
        """原生接口的可下载副本是文档资产，更新它不构造功能源码循环。"""
        before = audit.source_snapshot(self.root)
        header = self.root / 'control/internal/server/doc_assets/native/include/example.h'
        header.parent.mkdir(parents=True)
        header.write_text('documented declaration\n')
        self.assertEqual(audit.source_snapshot(self.root), before)

    def test_old_or_unbound_evidence_is_not_green(self):
        self.assertEqual(self.evaluate(self.receipt(age=31))['status'], 'stale')
        record = self.receipt()
        record.pop('binding_method')
        self.assertEqual(self.evaluate(record)['status'], 'unbound')
        record = self.receipt()
        record['finished_at'] = None
        self.assertFalse(self.evaluate(record)['green_eligible'])

    def test_fail_skip_absent_run_and_incomplete_package_do_not_pass(self):
        transforms = [lambda rows: rows[:-1],
                      lambda rows: rows + [{'Time': NOW.isoformat(), 'Action': 'start', 'Package': 'unfinished'}],
                      lambda rows: [r for r in rows if r['Action'] != 'run'],
                      lambda rows: [{**r, 'Action': 'skip'} if r.get('Test') and r['Action'] == 'pass' else r for r in rows],
                      lambda rows: rows[:-1] + [{'Time': NOW.isoformat(), 'Action': 'fail', 'Package': PACKAGE}]]
        for transform in transforms:
            record = self.receipt()
            self.change_trace(record, transform)
            self.assertFalse(self.evaluate(record)['green_eligible'])

    def test_latest_failure_masks_older_pass(self):
        self.save(self.receipt('older', age=1))
        record = self.receipt('newer')
        record['returncode'] = 1
        self.save(record)
        self.assertEqual(self.row(self.build())['local_status'], 'failed')

    def test_malformed_or_duplicate_receipt_blocks_older_green(self):
        self.save(self.receipt())
        (self.evidence / 'newest.receipt.json').write_text('{')
        self.assertEqual(self.build()['summary']['green_entries'], 0)
        (self.evidence / 'newest.receipt.json').unlink()
        self.save(self.receipt(), 'duplicate')
        self.assertEqual(self.build()['summary']['green_entries'], 0)

    def test_offline_gate_rejects_stale_and_tampered_green(self):
        self.save(self.receipt())
        data = self.build()
        for alteration in ['missing-id', 'upgrade', 'fake-scope', 'log', 'source', 'age', 'missing-expiry', 'extended-expiry']:
            with self.subTest(alteration=alteration):
                candidate = copy.deepcopy(data)
                when = NOW
                if alteration == 'missing-id': candidate['entries'].pop()
                elif alteration == 'upgrade': candidate['upstream_runtime_executed'] = True
                elif alteration == 'fake-scope': self.row(candidate)['scope'] = 'FreeSWITCH全部兼容'
                elif alteration == 'log': next(iter(candidate['receipts'][0]['embedded_artifacts'].values()))['data'] = 'broken'
                elif alteration == 'source': (self.root / 'control/main.go').write_text('package changed\n')
                elif alteration == 'age': when += timedelta(days=32)
                elif alteration == 'missing-expiry': self.row(candidate).pop('effective_expires_at')
                elif alteration == 'extended-expiry': self.row(candidate)['effective_expires_at'] = (NOW + timedelta(days=31)).isoformat()
                with self.assertRaises(ValueError): audit.validate_runtime(self.root, candidate, when)
                (self.root / 'control/main.go').write_text('package main\n')

    def staggered_scopes(self):
        """同一SDP条目具备两个真实类型的合成证据，Go比E2E早一天到期。"""
        key = 'key-sip-sdp'
        self.comparison['entries'].append({'id': key, 'status': 'partial'})
        (self.root / 'docs/api/comparison.json').write_bytes(audit.wire(self.comparison))
        self.save(self.receipt('older-go', age=1, key=key))
        self.save(self.e2e_receipt())
        return self.build(), key

    def test_all_required_scopes_expire_at_earliest_proof_not_selected_proof(self):
        """最新E2E尚未到期也不能遮盖先到期的Go；同日重新生成必须立即撤绿。"""
        data, key = self.staggered_scopes()
        entry = self.row(data, key)
        selected = next(o for o in data['observations'] if o['id'] == entry['verification_id'])
        deadline = audit.utc(entry['effective_expires_at'])
        self.assertEqual(selected['kind'], 'python_unittest_e2e')
        self.assertLess(deadline, audit.utc(selected['expires_at']))
        self.assertEqual(audit.validate_runtime(self.root, data, deadline - timedelta(microseconds=1))['green_entries'], 1)
        with self.assertRaisesRegex(ValueError, '其他当前本地范围失败或失效'):
            audit.validate_runtime(self.root, data, deadline)
        rebuilt = audit.build(self.evidence, self.root, self.base / 'no-history', deadline)
        self.assertEqual(self.row(rebuilt, key)['local_status'], 'stale')
        self.assertIsNone(self.row(rebuilt, key)['effective_expires_at'])
        self.assertEqual(audit.validate_runtime(self.root, rebuilt, deadline)['green_entries'], 0)

    def test_browser_green_gate_hash_expiry_and_implementation_are_independent(self):
        """只运行沙箱中的JS纯函数与只读fetch替身，不操作浏览器或真实管理服务。"""
        node = shutil.which('node') or str(Path.home() / '.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node')
        if not Path(node).is_file(): self.skipTest('本机没有Node，浏览器门禁需在前端验收环境复核')
        data, key = self.staggered_scopes()
        raw = audit.wire(data).decode()
        payload = {'data': data, 'raw': raw, 'comparison': self.comparison,
                   'manifest': {'documents': [{'path': 'api/comparison.json', 'sha256': data['comparison_sha256']},
                                              {'path': 'api/runtime-verification.json', 'sha256': audit.sha(raw.encode())}]},
                   'now': NOW.isoformat(), 'entry_id': key}
        script = r'''
const vm=require('node:vm'),fs=require('node:fs'),assert=require('node:assert/strict');
const fixture=JSON.parse(fs.readFileSync(0,'utf8')),source=fs.readFileSync(process.argv[1],'utf8');
let currentTime=Date.parse(fixture.now),timer,delay;
class Clock extends Date {static now(){return currentTime;}}
let raw=fixture.raw;
const badgeNode={dataset:{runtimeEntry:fixture.entry_id},outerHTML:''},summaryNode={innerHTML:''};
const context={window:{},document:{addEventListener(){},querySelectorAll(){return [badgeNode];},getElementById(){return summaryNode;}},location:{hash:''},history:{replaceState(){}},
 URLSearchParams,URL,Date:Clock,Map,Set,TextEncoder,AbortController,Uint8Array,crypto:require('node:crypto').webcrypto,
 setTimeout(callback,milliseconds){timer=callback;delay=milliseconds;return 1;},clearTimeout(){},fetch:async(path,options)=>{assert.equal(options.method,'GET');return {ok:true,text:async()=>raw};}};
vm.runInNewContext(source.replace('return {mount,leave};','return {mount,leave,view,validateRuntimeView,loadRuntime,runtimeState,runtimeBadge,statusBadge,scheduleRuntimeExpiry,runtimeMarkup};'),context);
const api=context.window.RustSwitchDocs;
(async()=>{
 const checked=await api.loadRuntime(fixture.manifest,fixture.comparison);
 Object.assign(api.view,{runtimeIndex:checked.rows,runtimeProofs:checked.proofs,runtimeError:'',comparison:fixture.comparison,page:'fs-comparison'});
 const entry=fixture.comparison.entries.find(row=>row.id===fixture.entry_id),row=checked.rows.get(entry.id),proof=checked.proofs.get(row.verification_id);
 assert.match(api.runtimeBadge(entry),/badge good/);assert.match(api.statusBadge('implemented'),/badge neutral/);
 const selectedExpiry=proof.expires_at;
 assert.ok(Date.parse(row.effective_expires_at)<Date.parse(selectedExpiry));
 currentTime=Date.parse(row.effective_expires_at)-1000;
 api.scheduleRuntimeExpiry();assert.equal(delay,1001);assert.match(api.runtimeMarkup(entry),new RegExp(row.effective_expires_at.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')));
 currentTime=Date.parse(row.effective_expires_at);timer();
 assert.equal(api.runtimeState(entry).status,'stale');assert.doesNotMatch(badgeNode.outerHTML,/badge good/);
 assert.ok(currentTime<Date.parse(selectedExpiry));
 currentTime=Date.parse(fixture.now);proof.expires_at='2000-01-01T00:00:00Z';
 assert.equal(api.runtimeState(entry).status,'stale');assert.doesNotMatch(api.runtimeBadge(entry),/badge good/);
 proof.expires_at=selectedExpiry;
 for(const change of ['scope','false-green','missing-proof','upgrade','denominator','missing-expiry','extended-expiry','other-kind-failed','other-kind-time-invalid']){
  const data=JSON.parse(fixture.raw),target=data.entries.find(row=>row.comparison_id===fixture.entry_id);
  if(change==='scope')target.scope='全部兼容';
  else if(change==='false-green')target.green_eligible=false;
  else if(change==='missing-proof')data.observations=[];
  else if(change==='upgrade')data.upstream_runtime_executed=true;
  else if(change==='missing-expiry')delete target.effective_expires_at;
  else if(change==='extended-expiry')target.effective_expires_at=selectedExpiry;
  else if(change==='other-kind-failed'){const other=data.observations.find(item=>item.comparison_id===fixture.entry_id&&item.kind==='go_race_subset');other.status='failed';other.green_eligible=false;}
  else if(change==='other-kind-time-invalid')data.observations.find(item=>item.comparison_id===fixture.entry_id&&item.kind==='go_race_subset').finished_at=null;
  else data.entries.pop();
  assert.throws(()=>api.validateRuntimeView(data,fixture.comparison,fixture.manifest));
 }
 raw+=' ';await assert.rejects(api.loadRuntime(fixture.manifest,fixture.comparison),/SHA/);
 await assert.rejects(api.loadRuntime({documents:[]},fixture.comparison),/尚未收录/);
})().catch(error=>{process.stderr.write(String(error));process.exitCode=1;});
'''
        completed = subprocess.run([node, '-e', script, str(audit.ROOT / 'control/internal/server/web/docs.js')],
                                   input=json.dumps(payload).encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
        self.assertEqual(completed.returncode, 0, completed.stderr.decode())

    def capacity(self):
        """构造完整5000路门禁夹具；不写入交付目录且不冒充真实容量执行。"""
        record = self.receipt('capacity-fixture')
        record.update(kind='capacity_5000', binding_method='source_before_build_and_after_run',
                      source_before=audit.source_snapshot(self.root, True), source_after=audit.source_snapshot(self.root, True),
                      build_returncode=0, controller_run_id='fixture', runtime_os='linux', environment={'test_fixture': True})
        amount = 5000000
        report = {'passed': True, 'media_server_bypassed': False, 'requested_calls': 5000, 'established_calls': 5000,
                  'media_seconds': 10, 'sent_packets': amount, 'received_unique_packets': amount,
                  'nominal_packets': amount, 'offered_load_ratio': 1,
                  **dict.fromkeys(['unreceived_packets', 'duplicate_packets', 'invalid_packets', 'generator_write_errors',
                                  'unknown_rtp_packets', 'unexpected_byes', 'teardown_failures', 'generator_reader_errors', 'underloaded_flow_directions', 'unexpected_received_packets'], 0),
                  'directions': [{'source_side': side, 'sent': amount // 2, 'received': amount // 2,
                                  'flows_with_loss': 0, 'minimum_received_per_flow': 500} for side in [0, 1]]}
        stats = {'active_calls': 0, 'socket_drop_counter_supported': True,
                 **dict.fromkeys(['socket_rx_drops', 'send_errors', 'send_expired', 'send_queue_drops',
                                  'invalid_packets', 'rate_limited', 'receive_errors'], 0)}
        samples = [{'at': NOW.isoformat(), 'run_id': 'fixture', 'journal_healthy': True, 'journal_lost_events': 0, 'active_calls': 5000, 'established_calls': 5000, 'workers': []},
                   {'at': NOW.isoformat(), 'run_id': 'fixture', 'journal_healthy': True, 'journal_lost_events': 0, 'active_calls': 0, 'established_calls': 0,
                    'workers': [{'healthy': True, 'restarts': 0, 'stats': stats}]}]
        cleanup = dict.fromkeys(['complete', 'controller_exited', 'generator_exited', 'media_processes_exited',
                                'ports_released', 'work_dir_removed'], True)
        events = [{'run_id': 'fixture', 'kind': kind, 'call_id': str(i)}
                  for kind in ['call_established', 'call_ended'] for i in range(5000)]
        record['artifacts'] = {'report': self.artifact('report.json', report), 'status_samples': self.artifact('samples.json', samples),
                               'cleanup': self.artifact('cleanup.json', cleanup),
                               'journal': self.artifact('journal.jsonl', b'\n'.join(json.dumps(r).encode() for r in events))}
        record['binaries'] = {role: self.artifact(role + '.bin', b'synthetic fixture only') for role in ['control', 'media', 'generator']}
        # 合成构建文件只用于门禁负例，不进入真实收据目录。
        build = {'schema_version': '1.0.0', 'kind': 'local_runtime_build', 'returncode': 0,
                 'started_at': record['started_at'], 'finished_at': record['started_at'],
                 'source_before': record['source_before'], 'source_after': record['source_after'], 'binaries': record['binaries']}
        record['artifacts']['build'] = self.artifact('build.json', build)
        return record, report, samples, cleanup

    def e2e_receipt(self):
        """完整十组的临时门禁夹具；不以这些合成用例作为产品执行证据。"""
        record = self.receipt('e2e-fixture')
        record.update(kind='python_unittest_e2e', source_before=audit.source_snapshot(self.root, True),
                      source_after=audit.source_snapshot(self.root, True), runner_source_sha256=audit.sha(audit.E2E_RUNNER.encode()))
        binaries = {role: self.artifact(role + '.bin', b'synthetic binary fixture') for role in ['control', 'media']}
        record['binaries'], record['binaries_after'] = binaries, {role: item['sha256'] for role, item in binaries.items()}
        build = {'schema_version': '1.0.0', 'kind': 'local_runtime_build', 'returncode': 0,
                 'started_at': (NOW - timedelta(seconds=5)).isoformat(), 'finished_at': (NOW - timedelta(seconds=3)).isoformat(),
                 'source_before': record['source_before'], 'source_after': record['source_after'], 'binaries': binaries}
        record['artifacts'] = {'build': self.artifact('build.json', build)}
        all_names = set(name for names, _ in audit.E2E_CLAIMS.values() for name in names)
        stages = []
        for name, suite, connected in audit.E2E_STAGES:
            names = sorted(n for n in all_names if n.startswith(suite + '.'))
            result = {'expected': names, 'started': names, 'passed': names, 'tests_run': len(names),
                      'successful': True, 'failures': 0, 'errors': 0, 'skipped': 0}
            stderr = '\n'.join(f'{n.rsplit(".", 1)[-1]} ({n}) ... ok' for n in names) + f'\n\nRan {len(names)} tests in 0.1s\n\nOK\n'
            stage = {'name': name, 'suite': suite, 'connected': connected, 'started_at': NOW.isoformat(),
                     'finished_at': NOW.isoformat(), 'returncode': 0, 'timed_out': False, 'forced_cleanup': False,
                     'stdout': self.artifact(name + '.stdout', result), 'stderr': self.artifact(name + '.stderr', stderr.encode())}
            record['artifacts'].update({name + '-' + kind: stage[kind] for kind in ['stdout', 'stderr']})
            stages.append(stage)
        record['stages'] = stages
        return record

    def test_e2e_exact_scopes_require_ten_actual_suites_and_build_binding(self):
        record = self.e2e_receipt()
        key = 'key-media-rtcp'
        self.comparison['entries'].append({'id': key, 'status': 'partial'})
        (self.root / 'docs/api/comparison.json').write_bytes(audit.wire(self.comparison))
        self.save(record)
        data = self.build()
        self.assertTrue(self.row(data, key)['green_eligible'])
        self.assertEqual(self.row(data, key)['implementation_status'], 'partial')
        self.assertEqual(audit.validate_runtime(self.root, data, NOW)['green_entries'], 1)
        for mode in ['no-build', 'binary', 'missing-stage', 'listed-only', 'skipped', 'forced-cleanup']:
            with self.subTest(mode=mode):
                candidate = copy.deepcopy(record)
                if mode == 'no-build': candidate['artifacts'].pop('build')
                elif mode == 'binary': candidate['binaries_after']['media'] = 'f' * 64
                elif mode == 'missing-stage': candidate['stages'].pop()
                elif mode == 'listed-only': candidate['stages'][0]['stderr'] = self.artifact('list.stderr', b'e2e.Integration.test_bidirectional_rtp_rtcp_dtmf_and_cleanup\n')
                elif mode == 'skipped':
                    result = json.loads((self.evidence / candidate['stages'][0]['stdout']['path']).read_bytes())
                    result['skipped'] = 1
                    candidate['stages'][0]['stdout'] = self.artifact('skip.stdout', result)
                else: candidate['stages'][0]['forced_cleanup'] = True
                proof = audit.evaluate_e2e(candidate, self.evidence, audit.source_snapshot(self.root, True), NOW, key, audit.E2E_CLAIMS[key])
                self.assertFalse(proof['green_eligible'])
                if mode == 'no-build': self.assertEqual(proof['status'], 'unbound')

    def test_e2e_recorder_really_executes_fixed_runner_in_isolated_temporary_fixture(self):
        # 这里只测试采集器本身：临时测试模块不含网络或媒体，名称不匹配产品声明，因此不能产品标绿。
        (self.root / 'tests').mkdir()
        for module in dict.fromkeys(stage[1] for stage in audit.E2E_STAGES):
            source = 'import unittest\nclass Fixture(unittest.TestCase):\n def test_fixture(self): self.assertEqual(1, 1)\n'
            if module == 'dtmf_send_e2e':
                # 新发送阶段由入口设置嵌套媒体夹具；只引用同一临时目录的空测试模块，不启动产品进程。
                source = 'import media_interaction_e2e as media_fixture\n' + source
            (self.root / 'tests' / (module + '.py')).write_text(source)
        binary = self.base / 'fixture.bin'
        binary.write_bytes(b'not executed; recorder fixture')
        identifier, code = audit.capture_e2e(self.evidence, Path(sys.executable), binary, binary, root=self.root)
        self.assertEqual(code, 0)
        receipt = json.loads((self.evidence / (identifier + '.receipt.json')).read_bytes())
        self.assertEqual(len(receipt['stages']), len(audit.E2E_STAGES))
        for stage in receipt['stages']:
            passed = audit.parse_unittest(audit.read_artifact(self.evidence, stage['stdout']), audit.read_artifact(self.evidence, stage['stderr']))
            self.assertEqual(len(passed), 1)
        proof = audit.evaluate_e2e(receipt, self.evidence, audit.source_snapshot(self.root, True), datetime.now(timezone.utc),
                                   'key-media-rtcp', audit.E2E_CLAIMS['key-media-rtcp'])
        self.assertFalse(proof['green_eligible'])

    def test_capacity_green_is_only_five_thousand_extra_scope(self):
        record, _, _, _ = self.capacity()
        self.save(record)
        data = self.build()
        entry = self.row(data, 'key-media-capacity')
        self.assertTrue(entry['green_eligible'])
        self.assertEqual(entry['implementation_status'], 'not_verified')
        self.assertIn('不是万路', entry['scope'])
        self.assertEqual(audit.validate_runtime(self.root, data, NOW)['green_entries'], 1)

    def test_capacity_direct_loss_underload_cleanup_and_unknown_kernel_counters_never_green(self):
        for mode in ['direct', 'loss', 'underload', 'too-few', 'cleanup', 'kernel', 'no-live-sample', 'journal', 'silent-flow', 'reader-error', 'mixed-instance']:
            with self.subTest(mode=mode):
                record, report, samples, cleanup = self.capacity()
                if mode == 'direct': report['media_server_bypassed'] = True
                elif mode == 'loss': report['received_unique_packets'] -= 1
                elif mode == 'underload': report['sent_packets'] = report['received_unique_packets'] = 1
                elif mode == 'too-few': report['requested_calls'] = report['established_calls'] = 1000
                elif mode == 'cleanup': cleanup['complete'] = False
                elif mode == 'kernel': samples[-1]['workers'][0]['stats']['socket_drop_counter_supported'] = False
                elif mode == 'no-live-sample': samples.pop(0)
                elif mode == 'silent-flow': report['directions'][0]['minimum_received_per_flow'] = 0
                elif mode == 'reader-error': report['generator_reader_errors'] = 1
                elif mode == 'mixed-instance': samples[0]['run_id'] = 'another-process'
                else: record['artifacts']['journal'] = self.artifact('journal.jsonl', b'{}')
                record['artifacts'].update(report=self.artifact('report.json', report),
                                           status_samples=self.artifact('samples.json', samples), cleanup=self.artifact('cleanup.json', cleanup))
                result = audit.evaluate_capacity(record, self.evidence, audit.source_snapshot(self.root, True), NOW)
                self.assertFalse(result['green_eligible'])
                self.assertNotIn('场景通过', result['scope'])

    def test_capacity_requires_actual_matching_build_before_samples(self):
        """容量成功报告不能替代真实构建来源，缺失、错源、错产物和时序倒置都拒绝绿色。"""
        for mode in ['missing', 'source', 'binary', 'late-build']:
            with self.subTest(mode=mode):
                record, _, _, _ = self.capacity()
                build = json.loads(audit.read_artifact(self.evidence, record['artifacts']['build']))
                if mode == 'missing':
                    record['artifacts'].pop('build')
                else:
                    if mode == 'source': build['source_before'] = {'unrelated.go': '0' * 64}
                    elif mode == 'binary': build['binaries']['media']['sha256'] = 'f' * 64
                    else: build['finished_at'] = record['finished_at']
                    record['artifacts']['build'] = self.artifact('build.json', build)
                self.assertFalse(audit.evaluate_capacity(record, self.evidence, audit.source_snapshot(self.root, True), NOW)['green_eligible'])


if __name__ == '__main__':
    unittest.main()
