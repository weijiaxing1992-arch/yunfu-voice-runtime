#!/usr/bin/env python3
"""真实历史证据的离线反例；修改内容后重算外层SHA，验证语义门禁而不只测哈希。

夹具保留实际Go/Rust收据、SIP/RTP/PCM/OS队首与过程日志，未包含可执行二进制。
测试始终关闭可执行文件读取；发布目录导入仍默认逐字节核对四个实际二进制。
"""
import base64
import copy
from datetime import datetime
import json
from pathlib import Path
import struct
import tempfile
import unittest
from unittest.mock import patch
import zlib

import rx_verification as rx

FIXTURE = json.loads(zlib.decompress(Path(__file__).with_name('rx_verification_fixture.json.zlib').read_bytes()))
CASE = 'payload_0_connected_false'


class RXVerification(unittest.TestCase):
    def setUp(self):
        self.fixture = copy.deepcopy(FIXTURE)
        self.receipt = self.fixture['receipt']
        self.embedded = self.fixture['embedded']

    def evaluate(self, identifier='key-ipc-rx-stream', **kwargs):
        return rx.evaluate_rx(self.receipt, self.embedded, self.fixture['current'],
                              self.fixture['as_of'], identifier, verify_binaries=False, **kwargs)

    def raw(self, key):
        return rx.read_artifact(self.embedded, self.receipt['artifacts'][key])

    def replace(self, key, raw):
        """修复归档外层SHA，避免反例在重放前就只因封装损坏失败。"""
        ref = self.receipt['artifacts'][key]
        ref['sha256'] = rx.sha(raw)
        self.embedded[ref['path']] = {'encoding': 'zlib+base64', 'bytes': len(raw), 'sha256': rx.sha(raw),
                                      'data': base64.b64encode(zlib.compress(raw)).decode()}

    def change(self, role, name, raw):
        """同时修复原收据中的文件摘要；发布器仍必须拒绝语义不成立的轨迹。"""
        key = role+'/'+name
        self.replace(key, raw)
        receipt_key = role+'/$receipt'
        original = json.loads(self.raw(receipt_key))
        digest = rx.sha(raw)
        if role == 'pool':
            original['log_sha256'] = digest
        elif role == 'pause':
            original['artifacts'][name] = digest
            for step in original['steps']:
                if name == step['name']+'.log':
                    step['log_sha256'] = digest
        elif role == 'server':
            original['files'][name]['sha256'] = digest
            original['files'][name]['bytes'] = len(raw)
            for step in original['steps']:
                if name == step['name']+'.log':
                    step['log_sha256'] = digest
        self.replace(receipt_key, rx.wire(original))

    def server(self):
        return rx.rows(self.raw('server/wire/'+CASE+'/wire.jsonl'))

    def save_server(self, rows):
        self.change('server', 'wire/'+CASE+'/wire.jsonl',
                    b''.join((json.dumps(row, separators=(',', ':'))+'\n').encode() for row in rows))

    def pause(self, name, mutate):
        path = 'cases/'+CASE+'/'+name+'.json'
        data = json.loads(self.raw('pause/'+path))
        mutate(data)
        self.change('pause', path, rx.wire(data))

    def rejected(self, message=None, stale=False):
        proof = self.evaluate()
        self.assertFalse(proof['green_eligible'], proof)
        self.assertEqual(proof['status'], 'stale' if stale else 'failed', proof)
        if message:
            self.assertIn(message, proof['reason'])

    def test_actual_twelve_cases_all_five_scopes(self):
        for identifier in rx.RX_CLAIMS:
            with self.subTest(identifier=identifier):
                proof = self.evaluate(identifier)
                self.assertEqual(proof['status'], 'passed', proof)
                self.assertTrue(proof['green_eligible'])
                self.assertEqual(proof['evidence_cases'], 12)
                self.assertEqual(proof['decoded_samples'], 8960)
                self.assertIn('仅内部SDK', proof['scope'])

    def test_builder_aware_datetime_shape(self):
        expected = self.evaluate()
        self.fixture['as_of'] = rx.utc(self.fixture['as_of'])
        self.assertEqual(self.evaluate(), expected)
        self.fixture['as_of'] = datetime(2026, 9, 7, 4)
        self.rejected('没有采集时区')

    def test_failure_keeps_execution_time_for_ordering(self):
        self.receipt['binaries']['media']['sha256'] = '0'*64
        proof = self.evaluate()
        self.assertEqual(proof['finished_at'], self.receipt['finished_at'])
        self.assertEqual(proof['started_at'], self.receipt['started_at'])
        self.assertFalse(proof['green_eligible'])

    def test_original_failure_cannot_be_overridden(self):
        key = 'pool/$receipt'
        original = json.loads(self.raw(key))
        original['passed'] = False
        self.replace(key, rx.wire(original))
        self.rejected('明确失败')

    def test_directory_and_embedded_replay_identical(self):
        with tempfile.TemporaryDirectory() as directory:
            for ref in self.receipt['artifacts'].values():
                target = Path(directory)/ref['path']
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(rx.read_artifact(self.embedded, ref))
            directory_proof = rx.evaluate_rx(self.receipt, directory, self.fixture['current'], self.fixture['as_of'],
                                             'key-ipc-rx-stream', verify_binaries=False)
            self.assertEqual(directory_proof, self.evaluate())
            packed = rx.pack_rx(self.receipt, directory)
            self.assertEqual(rx.evaluate_rx(self.receipt, packed, self.fixture['current'], self.fixture['as_of'],
                                            'key-ipc-rx-stream', verify_binaries=False), self.evaluate())

    def test_no_execution_of_evidence(self):
        # 收据含原执行器文本，重放只解析它的字节摘要，绝不会调用命令或eval。
        with patch('subprocess.run', side_effect=AssertionError('must not execute')), \
                patch('subprocess.Popen', side_effect=AssertionError('must not execute')):
            self.assertTrue(self.evaluate()['green_eligible'])

    def test_single_pcm_sample_corrupted(self):
        data = self.server()
        decoded = next(r['value']['Frame'] for r in data if r['kind'] == 'rx_sdk_delivered' and r['value']['Frame']['Kind'] == 1)
        decoded['PCM'][79] += 1
        self.save_server(data)
        self.rejected('原样本')

    def test_full_rtp_epoch_cannot_jump_then_rollback(self):
        data = self.server()
        decoded = [r['value']['Frame'] for r in data if r['kind'] == 'rx_sdk_delivered' and r['value']['Frame']['Kind'] == 1]
        decoded[2]['RTPSequence'] += 1 << 16
        decoded[2]['RTPTimestamp'] += 1 << 32
        self.save_server(data)
        self.rejected('完整RTP')

    def test_missing_valid_flags_cannot_keep_rtp_positions(self):
        data = self.server()
        for row in data:
            if row['kind'] == 'rx_sdk_delivered':
                row['value']['Frame']['Flags'] = 0
        self.save_server(data)
        self.rejected('有效位')

    def test_invite_200_tag_must_match_following_dialog(self):
        data = self.server()
        response = next(r['value'] for r in data if r['kind'] == 'sip_receive'
                        and rx.sip(r['value'])[0] == 'SIP/2.0 200 OK'
                        and rx.sip(r['value'])[1]['cseq'] == '1 INVITE')
        headers = rx.sip(response)[1]
        response['hex'] = bytes.fromhex(response['hex']).replace(headers['to'].encode(),
                          (headers['to']+'-wrong-dialog').encode()).hex()
        self.save_server(data)
        self.rejected('对话tag')

    def test_wrong_ack_must_reach_actual_server(self):
        data = self.server()
        next(r['value'] for r in data if r['kind'] == 'sip_wrong_source_ack')['target'] = '127.0.0.1:9999'
        self.save_server(data)
        self.rejected('同一隔离Server')

    def test_fake_silent_pcm_cannot_replace_audio(self):
        data = self.server()
        for r in data:
            if r['kind'] == 'rx_sdk_delivered' and r['value']['Frame']['Kind'] == 1:
                r['value']['Frame']['PCM'] = [0]*160
        self.save_server(data)
        self.rejected('原样本')

    def test_actual_delivery_at_expiry_rejected(self):
        data = self.server()
        delivery = next(r['value'] for r in data if r['kind'] == 'rx_sdk_delivered')
        delivery['ClockNS'] = delivery['Frame']['ExpiresAtNS']
        self.save_server(data)
        self.rejected('交付时样本已过期')

    def test_unknown_clock_domain(self):
        data = self.server()
        next(r['value'] for r in data if r['kind'] == 'rx_sdk_delivered')['Frame']['ClockDomain'] = 99
        self.save_server(data)
        self.rejected('绝对期限')

    def test_renewed_deadline_is_not_valid(self):
        data = self.server()
        next(r['value'] for r in data if r['kind'] == 'rx_sdk_delivered')['Frame']['ExpiresAtNS'] += 1
        self.save_server(data)
        self.rejected('绝对期限')

    def test_missing_law_udp_combination(self):
        rows = rx.rows(self.raw('pool/tests.jsonl'))
        rows = [r for r in rows if not r.get('Test', '').endswith('/payload_8_connected_true')]
        self.change('pool', 'tests.jsonl', b''.join((json.dumps(r)+'\n').encode() for r in rows))
        self.rejected('四组合')

    def test_passed_boolean_does_not_hide_actual_failure(self):
        rows = rx.rows(self.raw('pool/tests.jsonl'))
        next(r for r in rows if r['Action'] == 'pass')['Action'] = 'fail'
        self.change('pool', 'tests.jsonl', b''.join((json.dumps(r)+'\n').encode() for r in rows))
        self.rejected('失败或跳过')

    def test_pause_does_not_stop_rust(self):
        self.pause('parent-observation', lambda d: d.update(worker_during_stop=d['worker_before'][:-1]+'T'))
        self.rejected('SIGSTOP')

    def test_pause_pid_not_owned(self):
        self.pause('helper-ready', lambda d: d.update(helper_pid=d['helper_pid']+1))
        self.rejected('SIGSTOP')

    def test_pause_shortened_window(self):
        self.pause('parent-observation', lambda d: d.update(paused_ns=99_000_000))
        self.rejected('250ms')

    def test_pause_old_pcm_must_not_deliver(self):
        self.pause('helper-result', lambda d: d['sdk'].update(DeliveredSamples=160))
        self.rejected('旧样本被交付')

    def test_pause_counters_cannot_invent_silence(self):
        self.pause('helper-result', lambda d: d['sdk'].update(DroppedSamples=d['sdk']['DroppedSamples']+160))
        self.rejected('计数不守恒')

    def test_pause_rust_backpressure_not_freshness(self):
        self.pause('helper-result', lambda d: d['remote'].update(queued_events=1))
        self.rejected('队列未清空')

    def test_os_head_is_actual_rxs2(self):
        path = 'cases/'+CASE+'/os-head.rxs2'
        raw = bytearray(self.raw('pause/'+path))
        raw[:4] = b'RXS1'
        self.change('pause', path, bytes(raw))
        self.rejected('RXS2')

    def test_os_head_transcription_cannot_renew(self):
        path = 'cases/'+CASE+'/os-head.rxs2'
        raw = bytearray(self.raw('pause/'+path))
        struct.pack_into('<Q', raw, 160, struct.unpack_from('<Q', raw, 160)[0]+rx.PERIOD)
        self.change('pause', path, bytes(raw))
        self.rejected('期限转录')

    def test_wrong_ack_has_no_handle(self):
        data = self.server()
        next(r['value'] for r in data if r['kind'] == 'wrong_ack_subscribe')['has_handle'] = True
        self.save_server(data)
        self.rejected('错误ACK')

    def test_missing_bye_revocation(self):
        data = [r for r in self.server() if r['kind'] != 'rx_bye_revocation']
        self.save_server(data)
        self.rejected('rx_bye_revocation')

    def test_media_ports_must_rebind_all_four(self):
        data = self.server()
        next(r['value'] for r in data if r['kind'] == 'media_ports_rebound').pop()
        self.save_server(data)
        self.rejected('四个实际媒体端口')

    def test_cleanup_cannot_keep_call(self):
        data = self.server()
        next(r['value'] for r in data if r['kind'] == 'fresh_control_zero')['active_calls'] = 1
        self.save_server(data)
        self.rejected('控制会话')

    def test_history_overlap_cannot_bind_old_pool_to_new_server(self):
        for role in ('pool', 'pool_build'):
            key = role+'/$receipt'
            data = json.loads(self.raw(key))
            for phase in ('source_before', 'source_after'):
                data[phase]['control/internal/media/rx_stream.go'] = '0'*64
            self.replace(key, rx.wire(data))
        self.rejected('历史运行间重叠源码')

    def test_history_missing_core_cannot_be_filled_from_server(self):
        for role in ('pool', 'pool_build'):
            key = role+'/$receipt'
            data = json.loads(self.raw(key))
            for phase in ('source_before', 'source_after'):
                del data[phase]['control/internal/media/rx_stream.go']
            self.replace(key, rx.wire(data))
        self.rejected('历史编译闭包')

    def test_current_changed_source(self):
        self.fixture['current']['control/internal/media/rx_stream.go'] = '0'*64
        self.rejected('源码与真实', stale=True)

    def test_new_production_file_invalidates_exact_set(self):
        self.fixture['current']['control/internal/media/new_handler.go'] = '0'*64
        self.rejected('新增或删除源码', stale=True)

    def test_new_native_go_build_inputs_invalidate_exact_set(self):
        for suffix in rx.GO_INPUT_SUFFIXES:
            with self.subTest(suffix=suffix):
                name = 'control/internal/media/new_rx'+suffix
                self.fixture['current'][name] = '0'*64
                self.rejected('新增或删除源码', stale=True)
                del self.fixture['current'][name]

    def test_vendor_assembly_is_bound(self):
        name = next(k for k in self.fixture['current'] if k.startswith('control/vendor/') and k.endswith('.s'))
        self.fixture['current'][name] = '0'*64
        self.rejected('源码与真实', stale=True)

    def test_uncollected_docs_python_do_not_change_tested_scope(self):
        self.fixture['current']['tests/unrelated.py'] = '0'*64
        self.fixture['current']['control/internal/server/doc_assets/new.md'] = '0'*64
        self.assertTrue(self.evaluate()['green_eligible'])

    def test_binary_manifest_tampering(self):
        self.receipt['binaries']['pool_test']['sha256'] = '0'*64
        self.rejected('二进制SHA')

    def test_build_step_missing_even_passed(self):
        key = 'server/$receipt'
        data = json.loads(self.raw(key))
        data['steps'] = [s for s in data['steps'] if s['name'] != 'compile']
        self.replace(key, rx.wire(data))
        self.rejected('构建步骤')

    def test_evidence_age_and_future(self):
        self.fixture['as_of'] = '2026-11-01T00:00:00Z'
        self.rejected('超过30天', stale=True)
        self.fixture['as_of'] = '2026-09-06T00:00:00Z'
        self.rejected('未来')

    def test_other_comparison_cannot_borrow_rx_scope(self):
        self.assertFalse(self.evaluate('key-api-originate')['green_eligible'])

    def test_artifact_escape_and_corrupt_compression(self):
        ref = self.receipt['artifacts']['pool/tests.jsonl']
        ref['path'] = '../outside'
        self.rejected('越界')
        self.setUp()
        ref = self.receipt['artifacts']['pool/tests.jsonl']
        self.embedded[ref['path']]['data'] = base64.b64encode(b'bad compressed stream').decode()
        self.rejected()


if __name__ == '__main__':
    unittest.main()
