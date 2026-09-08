#!/usr/bin/env python3
"""M1 门禁的隔离合成夹具，仅测试验证器；这些文件绝不作为产品运行证据发布。"""
import copy
from datetime import datetime, timedelta, timezone
import json
import hashlib
from pathlib import Path
import shutil
import struct
import tempfile
import unittest

import build_runtime_verification as audit


class ProcessedRuntimeVerificationTest(unittest.TestCase):
    """独立造最小有效证据，再逐项篡改，验证绿色不能由状态文字或部分结果获得。"""
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root, self.evidence = self.base / 'root', self.base / 'evidence'
        self.root.mkdir(); self.evidence.mkdir()
        for name in ('control/main.go', 'media/src/lib.rs', 'config/local.json', 'native/support.c',
                     'tests/media_processed_e2e.py', 'tests/media_processed_fixture.py', 'tests/e2e.py', 'tests/esl_e2e.py'):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('# isolated validator fixture\n')
        self.current = audit.source_snapshot(self.root, True)
        self.start = datetime(2026, 9, 7, 1, tzinfo=timezone.utc)
        self.finish = self.start + timedelta(seconds=60)
        self.binaries = {'control': self.put('control.bin', b'control'), 'media': self.put('media.bin', b'media')}
        self.hashes = {role: ref['sha256'] for role, ref in self.binaries.items()}
        build = {'schema_version': '1.0.0', 'kind': 'local_runtime_build', 'returncode': 0,
                 'source_before': self.current, 'source_after': self.current,
                 'started_at': (self.start-timedelta(seconds=2)).isoformat(),
                 'finished_at': (self.start-timedelta(seconds=1)).isoformat(),
                 'binaries': self.binaries}
        self.buildref = self.put('runtime-build.json', build)
        custom = {'ok': True, 'source_before': {'fixture': True}, 'source_after': {'fixture': True},
                  'binaries': {'rustswitch': self.binaries['control'], 'rustswitch-media': self.binaries['media']}}
        customref = self.put('build.json', custom)
        captured = {p: value for p, value in self.current.items() if not p.startswith(('native/', 'config/'))}
        tests = {p: value for p, value in captured.items() if p.startswith('tests/')}
        self.raw = {'schema_version': 1, 'kind': audit.PROCESSED_KIND,
                    'binding_method': 'source_before_and_after_actual_execution',
                    'started_at': self.start.isoformat(), 'finished_at': self.finish.isoformat(),
                    'source_before': captured.copy(), 'source_after': captured.copy(),
                    'test_sources_before': tests, 'test_sources_after': tests,
                    'binaries_before': self.hashes, 'binaries_after': self.hashes,
                    'build_path': customref['path'], 'build_sha256': customref['sha256'],
                    'passed': True, 'outcome': 'passed_scoped', 'stages': []}
        for mode in ('udp', 'connected'):
            stage = {'mode': mode, 'exit_code': 0, 'elapsed_seconds': 20.0, 'passed': 11, 'failed': 0, 'skipped': 0,
                     'tests': [{'id': name, 'status': 'ok'} for name in audit.PROCESSED_TESTS], 'artifacts': []}
            text = ''.join(name.split('.')[-1] + ' (__main__.' + name + ')\nfixture assertion ... ok\n' for name in audit.PROCESSED_TESTS)
            stage['log'] = self.put(mode + '.log', (text+'\nRan 11 tests in 20.000s\n\nOK\n').encode())
            for index, name in enumerate(audit.PROCESSED_TESTS):
                doc, events = self.document(name, mode == 'connected', index)
                folder = mode + '/' + str(index)
                items = {folder+'/wire.json': doc,
                         folder+'/config.json': {'media': {'processing': 'g711', 'connect_sockets': mode == 'connected', 'workers': 1}, 'limits': {'max_calls': 1}},
                         folder+'/events.jsonl': b''.join(json.dumps(e).encode()+b'\n' for e in events),
                         folder+'/server.log': b'RustSwitch ready\ndraining existing calls\n'}
                refs = {path: self.put(path, data) for path, data in items.items()}
                case = dict(refs[folder+'/wire.json'], test='__main__.'+name, wire_records=len(doc['wire']),
                            cleanup_confirmed_by_fresh_stats=name != audit.PROCESSED_REJECT,
                            artifacts={path: {'sha256': ref['sha256'], 'bytes': (self.evidence/path).stat().st_size}
                                       for path, ref in refs.items()})
                stage['artifacts'].append(case)
            self.raw['stages'].append(stage)

    def put(self, name, value):
        raw = value if isinstance(value, bytes) else audit.wire(value)
        path = self.evidence / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
        return {'path': name, 'sha256': audit.sha(raw)}

    def zero(self, frames=64):
        return {'active_calls': 0, 'established_calls': 0, 'controller': {'media_results_queue_length': 0},
                'workers': [{'healthy': True, 'restarts': 0, 'generation': 1,
                             'admission': {'sampled_at': (self.start+timedelta(seconds=30)).isoformat()},
                             'stats': {'active_calls': 0, 'processed_active_calls': 0, 'available_blocks': 1,
                                       'processed_decoded_frames': frames, 'processed_decoded_samples': frames*160,
                                       'processed_encoded_frames': frames, 'processed_encoded_samples': frames*160,
                                       'processed_output_lateness_ns_max': 1000, 'processed_output_lateness_buckets': [frames,0,0,0,0,0,0,0],
                                       'processed_send_errors': 0, 'processed_send_deadline_misses': 0,
                                       'processed_jitter_reordered': 1, 'processed_jitter_duplicates': 1,
                                       'processed_jitter_lost': 1, 'processed_jitter_late': 1, 'processed_plc_frames': 1,
                                       'processed_cn_packets': 1}}]}

    def document(self, name, connected, index):
        rows = []
        def record(at, direction, raw, peer=None):
            rows.append({'at_ms': at, 'direction': direction, 'peer': peer or ['127.0.0.1', 7200], 'hex': raw.hex()})
        def sip(at, direction, first, call, method, body=b''):
            data = (first+'\r\nCall-ID: '+call+'\r\nCSeq: 1 '+method+'\r\nContent-Length: '+str(len(body))+'\r\n\r\n').encode()+body
            record(at, direction, data)
        events = [{'time': self.start.isoformat(), 'kind': 'controller_started', 'run_id': str(index)}]
        reject = name == audit.PROCESSED_REJECT
        if reject:
            for i, (law, ptime) in enumerate([('G722',20),('PCMU',10),('PCMA',30)]):
                sip(i*100, 'sip_send', 'INVITE sip:fixture SIP/2.0', str(i), 'INVITE', (law+'/8000\r\na=ptime:'+str(ptime)).encode())
                sip(i*100+1, 'sip_receive', 'SIP/2.0 488 Not Acceptable', str(i), 'INVITE')
                sip(i*100+2, 'sip_send', 'ACK sip:fixture SIP/2.0', str(i), 'ACK')
        else:
            for cycle in range(2 if index == 3 else 1):
                call = 'call-'+str(cycle)
                for j, kind in enumerate(('call_admitted','call_answered','call_established','call_ended')):
                    event = {'time': (self.start+timedelta(seconds=cycle*5+j+1)).isoformat(), 'kind':kind, 'run_id':str(index), 'call_id':call}
                    if kind == 'call_ended': event['reason']='normal_hangup'
                    events.append(event)
                sip(1+cycle*10, 'sip_send','INVITE sip:fixture SIP/2.0',call,'INVITE')
                sip(2+cycle*10, 'sip_receive','SIP/2.0 200 OK',call,'INVITE')
                sip(3+cycle*10, 'sip_send','ACK sip:fixture SIP/2.0',call,'ACK')
                sip(2000+cycle*10, 'sip_send','BYE sip:fixture SIP/2.0',call,'BYE')
                sip(2001+cycle*10, 'sip_receive','SIP/2.0 200 OK',call,'BYE')
                sip(2002+cycle*10, 'sip_receive','BYE sip:fixture SIP/2.0','b-'+call,'BYE')
            dynamic, paused = index == 5, index == 10
            for side in ('a','b'):
                law = ('PCMA' if side == 'a' else 'PCMU') if dynamic else ('PCMU' if side == 'a' else 'PCMA')
                other = 'b' if side == 'a' else 'a'
                target_law = 'PCMU' if law == 'PCMA' else 'PCMA'
                pt = 96 if dynamic and side == 'a' else 0 if law == 'PCMU' else 8
                target_pt = 96 if dynamic and other == 'a' else 0 if target_law == 'PCMU' else 8
                levels = [(audit.processed_pcm(bytes([c]),law)[0],c) for c in range(256)]
                targets = [(audit.processed_pcm(bytes([c]),target_law)[0],c) for c in range(256)]
                sequence = 500
                for frame in range(48 if paused else 24 if dynamic else 32):
                    code = min(levels, key=lambda p:abs(p[0]-(5000+(frame%5)*100)))[1]
                    sample = audit.processed_pcm(bytes([code]),law)[0]
                    output = min(targets,key=lambda p:abs(p[0]-sample))[1]
                    record(100+frame*20,'rtp_send_'+side, struct.pack('!BBHII',128,pt,frame,frame*160,1 if side=='a' else 2)+bytes([code])*160)
                    if paused and 11 <= frame <= 16: continue
                    record(140+frame*20,'rtp_receive_'+other,struct.pack('!BBHII',128,target_pt,sequence,4000+frame*160,3 if other=='a' else 4)+bytes([output])*160)
                    sequence += 1
            if index in (0,6):
                for digit in range(3 if index==0 else 5):
                    for direction in ('rtp_send_a','rtp_receive_b'):
                        record(170+digit*20,direction,struct.pack('!BBHII',128,101,800+digit,4160,5)+bytes([7,128,1,224]))
            if index in (4,6):
                record(410,'rtp_receive_b',struct.pack('!BBHII',128,13,850,6400,5)+bytes([42]))
            if index == 9:
                for side in ('a','b'):
                    record(500,'rtp_receive_'+side+'_rtcp',bytes([128,200,0,1,0,0,0,1,128,202,0,1,0,0,0,1]))
                    record(2010,'rtcp_after_hangup_'+side,bytes([128,202,0,1,0,0,0,1,129,203,0,1,0,0,0,1]))
            if paused:
                record(350,'owned_worker_SIGSTOP',b'123',['pid',123])
                record(490,'owned_worker_SIGCONT',b'123',['pid',123])
        rows.sort(key=lambda x:x['at_ms'])
        count = [48,64,56,48,50,48,60,59,0,800,82][index]
        return {'test':'__main__.'+name,'connected':connected,'processing':'g711','wire':rows,
                'binaries_before':self.hashes,'binaries_after':self.hashes,'binaries_unchanged':True,
                'final_status':None if reject else self.zero(count)}, events

    def envelope(self):
        rawref = self.put('original.json', self.raw)
        refs = audit.processed_refs(self.raw)
        return {'schema_version':'1.0.0','id':'processed-fixture','kind':audit.PROCESSED_KIND,
                'started_at':self.raw['started_at'],'finished_at':self.raw['finished_at'],
                'binaries':self.binaries,'artifacts':{'$receipt':rawref,'$build':self.buildref,**refs}}

    def evaluate(self, envelope=None, current=None, as_of=None):
        return audit.evaluate_processed(envelope or self.envelope(),self.evidence,
                                        current if current is not None else audit.source_snapshot(self.root,True),as_of or self.finish)

    def mutate_case(self, index, change, mode=0):
        case = self.raw['stages'][mode]['artifacts'][index]
        path = case['path']; doc=json.loads((self.evidence/path).read_text()); change(doc)
        ref=self.put(path,doc);case['sha256']=ref['sha256'];case['wire_records']=len(doc['wire'])
        case['artifacts'][path]={'sha256':ref['sha256'],'bytes':(self.evidence/path).stat().st_size}

    def test_complete_contract_import_and_offline_replay_only_green_transcoding(self):
        proof=self.evaluate();self.assertTrue(proof['green_eligible'],proof['reason'])
        self.assertEqual((proof['connected_cases'],proof['call_cycles'],proof['rejection_cases']),(20,22,2))
        comparison={'entries':[{'id':audit.PROCESSED_KEY,'status':'partial'},{'id':'registration:api:mod_commands:uuid_jitterbuffer:7781','status':'not_implemented'}]}
        path=self.root/'docs/api/comparison.json';path.parent.mkdir(parents=True);path.write_bytes(audit.wire(comparison))
        raw_path=self.evidence/'raw.receipt.input.json';raw_path.write_bytes(audit.wire(self.raw))
        imported=self.base/'imported';imported.mkdir()
        identity=audit.import_processed(raw_path,self.evidence/self.buildref['path'],self.evidence/'control.bin',self.evidence/'media.bin',imported,self.root)
        data=audit.build(imported,self.root,self.base/'none',self.finish)
        self.assertEqual(data['summary']['green_entries'],1)
        self.assertFalse(data['entries'][1]['green_eligible'])
        self.assertEqual((imported/identity/'original.receipt.json').read_bytes(),raw_path.read_bytes())
        shutil.rmtree(imported)
        self.assertEqual(audit.validate_runtime(self.root,data,self.finish)['green_entries'],1)
        data['observations'][0]['scope']='all FreeSWITCH codecs'
        with self.assertRaises(ValueError):audit.validate_runtime(self.root,data,self.finish)

    def test_missing_failed_skipped_duplicate_and_truncated_test_sets_never_green(self):
        original=copy.deepcopy(self.raw)
        for mode in ('missing','failed','skipped','duplicate','log','mode','exit_bool','elapsed'):
            with self.subTest(mode=mode):
                self.raw=copy.deepcopy(original);stage=self.raw['stages'][0]
                if mode=='missing':stage['tests'].pop()
                elif mode=='failed':stage['failed']=1
                elif mode=='skipped':stage['skipped']=1
                elif mode=='duplicate':stage['tests'][1]=stage['tests'][0]
                elif mode=='log':stage['log']=self.put('bad.log',b'Ran 11 tests\nOK\n')
                elif mode=='mode':self.raw['stages'][1]['mode']='udp'
                elif mode=='elapsed':stage['elapsed_seconds']=121
                else:stage['exit_code']=False
                self.assertFalse(self.evaluate()['green_eligible'])

    def test_rehashed_failure_text_and_expired_build_cannot_be_masked_by_success_summary(self):
        stage=self.raw['stages'][0]
        contents=(self.evidence/stage['log']['path']).read_bytes()
        stage['log']=self.put('failure-extra.log',b'ERROR: real_failure\n'+contents)
        self.assertFalse(self.evaluate()['green_eligible'])
        stage['log']=self.put('udp.log',contents)
        build=json.loads((self.evidence/'runtime-build.json').read_text())
        build['finished_at']=(self.finish+timedelta(seconds=1)).isoformat()
        self.buildref=self.put('runtime-build.json',build)
        self.assertFalse(self.evaluate()['green_eligible'])

    def test_latest_failed_independent_receipt_prevents_falling_back_to_old_green(self):
        comparison={'entries':[{'id':audit.PROCESSED_KEY,'status':'partial'}]}
        path=self.root/'docs/api/comparison.json';path.parent.mkdir(parents=True);path.write_bytes(audit.wire(comparison))
        good=self.envelope()
        self.put('good.receipt.json',good)
        # 故意失败的另一次收据保留完整原日志，不允许以早一次通过遮盖最新失败。
        self.raw['stages'][1]['skipped']=1
        self.raw['finished_at']=(self.finish+timedelta(seconds=1)).isoformat()
        bad=self.envelope();bad['id']='processed-later-failure'
        # 两份原始收据必须各自归档，不能让后写原文覆盖前者。
        bad['artifacts']['$receipt']=self.put('bad-original.json',self.raw)
        good_raw=copy.deepcopy(self.raw);good_raw['stages'][1]['skipped']=0
        good_raw['finished_at']=self.finish.isoformat()
        good['artifacts']['$receipt']=self.put('good-original.json',good_raw)
        self.put('good.receipt.json',good);self.put('bad.receipt.json',bad)
        result=audit.build(self.evidence,self.root,self.base/'none',self.finish)
        self.assertEqual(result['entries'][0]['local_status'],'failed')
        self.assertFalse(result['entries'][0]['green_eligible'])

    def test_raw_hash_binary_and_original_receipt_tampering_never_green(self):
        envelope=self.envelope()
        target=self.evidence/'udp/1/wire.json';target.write_bytes(target.read_bytes()+b' ')
        self.assertFalse(self.evaluate(envelope)['green_eligible'])
        target.write_bytes(target.read_bytes()[:-1])
        (self.evidence/'media.bin').write_bytes(b'changed')
        self.assertFalse(self.evaluate(envelope)['green_eligible'])
        (self.evidence/'media.bin').write_bytes(b'media')
        envelope['finished_at']=self.start.isoformat()
        self.assertFalse(self.evaluate(envelope)['green_eligible'])

    def test_fake_reclamation_old_generation_and_missing_bye_never_green_even_rehashed(self):
        case=copy.deepcopy(json.loads((self.evidence/'udp/1/wire.json').read_text()))
        for mode in ('active','old_sample','restart','bye','zero_samples'):
            with self.subTest(mode=mode):
                self.mutate_case(1,lambda d:d.update(copy.deepcopy(case)))
                def change(d):
                    if mode=='active':d['final_status']['workers'][0]['stats']['processed_active_calls']=1
                    elif mode=='old_sample':d['final_status']['workers'][0]['admission']['sampled_at']=self.start.isoformat()
                    elif mode=='restart':d['final_status']['workers'][0]['restarts']=1
                    elif mode=='zero_samples':d['final_status']['workers'][0]['stats']['processed_decoded_frames']=0
                    else:d['wire']=[r for r in d['wire'] if not bytes.fromhex(r['hex']).startswith(b'BYE ')]
                self.mutate_case(1,change)
                self.assertFalse(self.evaluate()['green_eligible'])

    def test_pcm_payload_replacement_and_late_reanchoring_never_green_even_rehashed(self):
        self.mutate_case(1,lambda d:next(r for r in d['wire'] if r['direction']=='rtp_receive_b').update(hex='800800010000000100000003'+'ff'*160))
        proof=self.evaluate();self.assertFalse(proof['green_eligible'])
        # 另一个完全正常夹具只推迟暂停恢复后的输出，捕捉旧版本的真实改期错误。
        self.setUp()
        def delay(d):
            for r in d['wire']:
                if r['direction'].startswith('rtp_receive_') and r['at_ms']>=490:r['at_ms']+=50
            d['wire'].sort(key=lambda r:r['at_ms'])
        self.mutate_case(10,delay)
        proof=self.evaluate();self.assertFalse(proof['green_eligible']);self.assertIn('期限',proof['reason'])

    def test_source_add_delete_change_and_expiry_are_stale_docs_do_not_pollute(self):
        for mode in ('add','delete','change'):
            current=self.current.copy()
            if mode=='add':current['control/new.go']='f'*64
            elif mode=='delete':del current['control/main.go']
            else:current['media/src/lib.rs']='f'*64
            self.assertEqual(self.evaluate(current=current)['status'],'stale')
        self.assertEqual(self.evaluate(as_of=self.finish+timedelta(days=31))['status'],'stale')
        docs=self.root/'control/internal/server/doc_assets/api/runtime-verification.json';docs.parent.mkdir(parents=True)
        docs.write_text('{"generated":true}')
        self.assertEqual(audit.source_snapshot(self.root,True),self.current)
        self.assertTrue(self.evaluate()['green_eligible'])
        del self.raw['source_before']['control/main.go']
        self.assertFalse(self.evaluate()['green_eligible'])

    def test_full_snapshot_contract_cannot_downgrade_or_invent_rejection_zero(self):
        self.raw['runtime_source_before']=self.current
        self.assertFalse(self.evaluate()['green_eligible'])
        self.raw['runtime_source_after']=self.current
        self.assertFalse(self.evaluate()['green_eligible'])
        for mode in (0,1):self.mutate_case(8,lambda d:d.update(rejection_statuses=[dict(self.zero(0),observed_at=self.finish.timestamp()) for _ in range(3)]),mode)
        self.assertFalse(self.evaluate()['green_eligible'])
        for mode in (0,1):self.mutate_case(1,self.add_catalog,mode)
        self.assertTrue(self.evaluate()['green_eligible'])
        self.mutate_case(8,lambda d:d['rejection_statuses'].pop())
        self.assertFalse(self.evaluate()['green_eligible'])

    def add_catalog(self, document):
        catalog={'media_mode':'g711_audio_graph','live_transcoding':True,
                 'runtime_media':{'processing':'g711','enabled':True,'available':True,'ready':True,'state':'ready',
                                  'capability':'processed_g711_v1','observed_at':self.start.isoformat(),
                                  'expected_workers':1,'healthy_workers':1,'capable_workers':1,
                                  'workers':[{'healthy':True,'generation':1,'pid':123,'capabilities':['processed_g711_v1']}],
                                  'sample_rate':8000,'frame_ms':20,'jitter_target_ms':40,'max_delay_ms':120},
                 'live_codec_profiles':[{'payload':pt,'name':name,'sample_rate':8000,'rtp_clock_rate':8000,'channels':1,'ptime_ms':20}
                                        for pt,name in [(0,'PCMU'),(8,'PCMA')]]}
        document['wire'].insert(0,{'at_ms':0,'direction':'runtime_codec_catalog','peer':['http',0],'hex':json.dumps(catalog).encode().hex()})

    def test_static_catalog_false_ready_unknown_codec_and_unknown_direction_are_rejected(self):
        original=json.loads((self.evidence/'udp/1/wire.json').read_text())
        for case in ('ready','workers','profile','direction'):
            with self.subTest(case=case):
                self.mutate_case(1,lambda d:d.update(copy.deepcopy(original)))
                self.mutate_case(1,self.add_catalog)
                def change(d):
                    row=d['wire'][0];catalog=json.loads(bytes.fromhex(row['hex']))
                    if case=='ready':catalog['runtime_media']['ready']=False
                    elif case=='workers':catalog['runtime_media']['workers'][0]['capabilities']=[]
                    elif case=='profile':catalog['live_codec_profiles'][0]['name']='Opus'
                    else:row['direction']='pretend_success'
                    row['hex']=json.dumps(catalog).encode().hex()
                self.mutate_case(1,change)
                self.assertFalse(self.evaluate()['green_eligible'])

    def test_large_binary_uses_separate_bounded_stream_without_relaxing_log_limit(self):
        path=self.evidence/'large.bin'
        with path.open('wb') as stream:stream.truncate((64<<20)+1)
        digest=hashlib.sha256()
        with path.open('rb') as stream:
            for block in iter(lambda:stream.read(1<<20),b''):digest.update(block)
        reference={'path':path.name,'sha256':digest.hexdigest()}
        audit.check_runtime_binary(self.evidence,reference)
        with self.assertRaisesRegex(ValueError,'64MiB'):audit.read_artifact(self.evidence,reference)
        with path.open('wb') as stream:stream.truncate((512<<20)+1)
        with self.assertRaises(ValueError):audit.check_runtime_binary(self.evidence,reference)

    def test_duplicate_artifacts_escape_and_modified_scope_cannot_grant_other_ids(self):
        envelope=self.envelope();envelope['artifacts']['$build']['path']='../outside'
        self.assertFalse(self.evaluate(envelope)['green_eligible'])
        self.raw['stages'][0]['artifacts'][1]=copy.deepcopy(self.raw['stages'][0]['artifacts'][0])
        self.assertFalse(self.evaluate()['green_eligible'])


if __name__=='__main__':unittest.main()
