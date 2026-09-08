#!/usr/bin/env python3
"""用已归档原帧和主动损坏验证离线门禁；不执行服务、不将测试自身算产品通过。"""
import base64
import copy
from datetime import timedelta
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zlib

import pcm_verification as audit


class PCMVerificationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # 历史原件固定保留，测试使用当时source/as_of，仅证明解析器接受合法证据。
        # 正式发布调用方仍必须传真实当前源码清单和当前时刻，不得使用此fixture代替。
        path=Path(__file__).with_name('pcm_verification_fixture.json.zlib')
        cls.original=json.loads(zlib.decompress(path.read_bytes()))

    def setUp(self):
        self.envelope=copy.deepcopy(self.original['envelope'])
        self.embedded=copy.deepcopy(self.original['embedded'])
        self.raw=self.read('$receipt')
        self.current=copy.deepcopy(self.raw['source_after'])
        self.as_of=audit.utc(self.raw['finished_at'])+timedelta(seconds=1)

    def read_bytes(self,key):
        return audit.read_artifact(self.embedded,self.envelope['artifacts'][key])

    def read(self,key):
        return json.loads(self.read_bytes(key))

    def write(self,key,value):
        """同时改正确外层SHA，保证负例必须走语义核验，而非只被外层哈希拦截。"""
        data=value if isinstance(value,bytes) else audit.wire(value)
        ref=self.envelope['artifacts'][key]
        ref['sha256']=audit.sha(data)
        self.embedded[ref['path']]={'encoding':'zlib+base64','bytes':len(data),'sha256':audit.sha(data),
                                    'data':base64.b64encode(zlib.compress(data)).decode()}
        return {'path':key,'bytes':len(data),'sha256':audit.sha(data)}

    def rebind(self,key,value):
        reference=self.write(key,value)
        for ref in self.raw['artifacts']:
            if ref['path']==key:ref.update(reference)
        for stage in self.raw['stages']:
            for ref in stage['raw_artifacts']:
                if ref['path']==key:ref.update(reference)
            for attr in ('result','stdout','stderr'):
                if stage[attr]['path']==key:stage[attr].update(reference)
            for case in stage['cases']:
                if case['wire']['path']==key:case['wire'].update(reference)
        self.write('$receipt',self.raw)

    def mutate_case(self,transform,method=0):
        case=self.raw['stages'][0]['cases'][method]
        key=case['wire']['path'];doc=self.read(key)
        transform(doc)
        # 同步非语义数量元数据，让缺音频/错误音频负例进入真实逐包复核。
        doc['wire_bytes']=sum(len(bytes.fromhex(x['hex'])) for x in doc['wire'])
        case['wire_bytes']=doc['wire_bytes'];case['wire_records']=len(doc['wire'])
        self.rebind(key,doc)

    def evaluate(self,key='key-api-pcm-stream'):
        return audit.evaluate_pcm(self.envelope,self.embedded,self.current,self.as_of,key,verify_binaries=False)

    def assert_rejected(self,reason=None):
        result=self.evaluate()
        self.assertFalse(result['green_eligible'],result)
        self.assertEqual(result['status'],'failed',result)
        if reason:self.assertIn(reason,result['reason'])

    def test_actual_complete_historical_capture_replays_for_six_scopes(self):
        for key in audit.PCM_CLAIMS:
            with self.subTest(key=key):
                result=self.evaluate(key)
                self.assertEqual(result['status'],'passed',result)
                self.assertEqual((result['method_count'],result['subtest_count'],result['raw_artifact_count'],result['call_cycles']),(22,12,144,32))
                self.assertEqual((result['audio_packets'],result['content_samples'],result['stream_responses']),(304,48640,896))
                self.assertNotIn('upstream_runtime_executed',result)

    def test_corrupt_raw_sha_and_missing_artifact_never_pass(self):
        ref=self.envelope['artifacts'][self.raw['stages'][0]['cases'][0]['wire']['path']]
        self.embedded[ref['path']]['sha256']='f'*64
        self.assert_rejected('内嵌引用')
        self.setUp()
        del self.envelope['artifacts'][self.raw['stages'][0]['cases'][0]['wire']['path']]
        self.assert_rejected('完整原文件引用')

    def test_remove_bye_with_recomputed_container_hash_still_fails(self):
        self.mutate_case(lambda d:d['wire'].__setitem__(slice(None),[r for r in d['wire'] if not (r['direction']=='sip_send' and bytes.fromhex(r['hex']).startswith(b'BYE '))]))
        self.assert_rejected('BYE')

    def test_remove_last_rtp_frame_with_recomputed_counts_still_fails(self):
        def remove(d):
            at=max(i for i,r in enumerate(d['wire']) if r['direction']=='rtp_receive')
            del d['wire'][at]
        self.mutate_case(remove)
        self.assert_rejected('RTP帧')

    def test_changed_full_pcm_payload_cannot_be_hidden_by_passed(self):
        def change(d):
            row=next(r for r in d['wire'] if r['direction']=='rtp_receive')
            raw=bytearray.fromhex(row['hex']);raw[-20]^=127;row['hex']=raw.hex()
        self.mutate_case(change)
        self.assert_rejected('完整样本')

    def test_remove_pcm_request_preserves_no_false_audio_source(self):
        def remove(d):
            at=next(i for i,r in enumerate(d['wire']) if r['direction']=='stream_send' and bytes.fromhex(r['hex'])[4]==2)
            del d['wire'][at]
        self.mutate_case(remove)
        self.assert_rejected('未关联')

    def test_response_observation_cannot_substitute_for_actual_bytes(self):
        def change(d):
            next(o for o in d['observations'] if o['label']=='stream_reply')['response']['request_id']+=1
        self.mutate_case(change)
        self.assert_rejected('实际收到')

    def test_lost_ack_or_bad_timestamp_cannot_be_counted_as_media_success(self):
        for fault in ('ack','timestamp'):
            with self.subTest(fault=fault):
                self.setUp()
                def change(d):
                    if fault=='ack':
                        d['wire']=[r for r in d['wire'] if not(r['direction']=='sip_send' and bytes.fromhex(r['hex']).startswith(b'ACK '))]
                    else:
                        row=[r for r in d['wire'] if r['direction']=='rtp_receive'][1]
                        raw=bytearray.fromhex(row['hex']);raw[7]^=1;row['hex']=raw.hex()
                self.mutate_case(change)
                self.assert_rejected()

    def test_subtest_failure_and_missing_method_cannot_hide_in_success_summary(self):
        for failure in ('subtest','method'):
            with self.subTest(failure=failure):
                self.setUp()
                stage=self.raw['stages'][0];result=copy.deepcopy(stage['observed'])
                if failure=='subtest':result['subtests'][0]['successful']=False
                else:result['passed'].pop()
                stage['observed']=result
                self.rebind(stage['result']['path'],result)
                self.assert_rejected()

    def test_failure_log_cannot_be_hidden_by_trailing_ok(self):
        key='udp.stderr.log'
        self.rebind(key,b'FAIL: hidden-subcase\n'+self.read_bytes(key))
        self.assert_rejected('日志包含失败')

    def test_current_source_or_expiry_is_stale_not_green(self):
        self.current['control/internal/server/pcm_stream.go']='f'*64
        result=self.evaluate();self.assertEqual(result['status'],'stale');self.assertFalse(result['green_eligible'])
        self.current=self.raw['source_after'];self.as_of+=timedelta(days=31)
        result=self.evaluate();self.assertEqual(result['status'],'stale');self.assertFalse(result['green_eligible'])

    def test_wrong_binary_and_fabricated_before_fail(self):
        self.envelope['binaries']['media']['sha256']='e'*64
        self.assert_rejected('binary')
        self.setUp()
        before=self.read('run-before.json');before['source_before']['media/src/media/worker.rs']='d'*64
        self.rebind('run-before.json',before)
        self.assert_rejected('before')

    def test_path_escape_and_appended_compressed_bytes_are_rejected(self):
        self.envelope['artifacts']['$receipt']['path']='../escape.json'
        self.assert_rejected('路径越界')
        self.setUp()
        ref=self.envelope['artifacts']['$receipt'];item=self.embedded[ref['path']]
        item['data']=base64.b64encode(base64.b64decode(item['data'])+b'trailing').decode()
        self.assert_rejected('压缩流')

    def test_missing_zero_or_boolean_zero_is_not_resource_cleanup(self):
        for change in ('missing','boolean'):
            with self.subTest(change=change):
                self.setUp()
                def mutate(d):
                    if change=='missing':d['observations']=[o for o in d['observations'] if o['label']!='fresh_final_zero']
                    else:
                        zero=next(o for o in d['observations'] if o['label']=='fresh_final_zero')
                        zero['state']['active_calls']=False;d['final_status']=zero['state']
                self.mutate_case(mutate)
                self.assert_rejected()

    def test_directory_read_refuses_parent_symlink(self):
        with tempfile.TemporaryDirectory() as folder:
            base=Path(folder);(base/'real').mkdir();(base/'real/data').write_bytes(b'data')
            (base/'alias').symlink_to(base/'real',target_is_directory=True)
            with self.assertRaisesRegex(ValueError,'符号链接'):
                audit.read_artifact(base,{'path':'alias/data','sha256':audit.sha(b'data')})

    def test_runtime_build_and_offline_publish_route_only_six_private_ids(self):
        """只测发布路由：历史源码用注入值，二进制单独由导入/实际回放核验；不执行历史来源。"""
        import build_runtime_verification as runtime
        with tempfile.TemporaryDirectory() as directory:
            base=Path(directory);root=base/'root';evidence=base/'evidence'
            (root/'docs/api').mkdir(parents=True);evidence.mkdir()
            comparison={'entries':[{'id':key,'status':'partial'} for key in audit.PCM_CLAIMS]+
                         [{'id':'fs-unverified-api','status':'not_implemented'}]}
            (root/'docs/api/comparison.json').write_bytes(audit.wire(comparison))
            for key,ref in self.envelope['artifacts'].items():
                path=evidence/ref['path'];path.parent.mkdir(parents=True,exist_ok=True)
                path.write_bytes(self.read_bytes(key))
            receipt=evidence/(self.envelope['id']+'.receipt.json');receipt.write_bytes(audit.wire(self.envelope))
            original_evaluate=audit.evaluate_pcm
            # 夹具特意不携带二进制；仅此路由测试禁用物理binary读取，其余完整原帧门禁保持原实现。
            def route(record,base,current,as_of,key,verify_binaries=True):
                return original_evaluate(record,base,current,as_of,key,verify_binaries=False)
            with patch.object(runtime,'source_snapshot',return_value=self.current), patch.object(audit,'evaluate_pcm',side_effect=route):
                result=runtime.build(evidence,root,base/'no-history',self.as_of)
            self.assertEqual(result['summary']['green_entries'],6,result['observations'])
            self.assertEqual({x['comparison_id'] for x in result['entries'] if x['green_eligible']},set(audit.PCM_CLAIMS))
            self.assertFalse(result['upstream_runtime_executed']);self.assertFalse(result['production_capacity_certified'])
            self.assertFalse(result['entries'][-1]['green_eligible'])
            # 删除全部原目录后，由正式validate_runtime独立重放压缩证据，不mock评价器。
            import shutil
            shutil.rmtree(evidence)
            with patch.object(runtime,'source_snapshot',return_value=self.current):
                self.assertEqual(runtime.validate_runtime(root,result,self.as_of)['green_entries'],6)


if __name__=='__main__':
    unittest.main()
