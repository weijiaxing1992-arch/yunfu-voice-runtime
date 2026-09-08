#!/usr/bin/env python3
"""离线复核真实SIP授权PCM证据；不导入执行器、不执行来源脚本、不访问网络。

六项入口共享完整11方法×两种UDP模式门禁；证据只覆盖本地A腿G.711和私有RVA1，
不能授予FreeSWITCH成对兼容、ASR/TTS供应商或容量通过。原文件逐字节压缩归档。
"""
import base64
from collections import Counter
from datetime import datetime, timedelta, timezone
import hashlib
import json
import math
from pathlib import Path
import re
import struct
import uuid
import zlib

ROOT = Path(__file__).resolve().parents[1]
PCM_KIND = 'python_unittest_sip_pcm_stream_e2e'
PCM_METHODS = tuple('test_' + name for name in (
    'ack_before_wrong_and_correct_gate_real_pcm', 'active_dtmf_and_pcm_interrupt_keep_shared_tx',
    'both_laws_persistent_stream_complete_and_same_begin_token',
    'deterministic_offset_queue_rejections_preserve_input_and_cross_connection_token',
    'hangup_revokes_tokens_and_reused_ports_have_only_new_call_pcm', 'new_turn_old_token_and_stop_barriers',
    'no_ack_timeout_reclaims_and_old_uuid_cannot_begin', 'park_and_pcm_lifecycles_are_independent',
    'silent_read_digits_and_timeout_with_pcm', 'wav_and_prompt_read_exclude_pcm_without_cancelling_owner',
    'worker_restart_retires_old_generation_and_new_call_stream_works'))
PREFIX = 'sip_pcm_stream_e2e.SIPPCMIntegration.'
SCOPE_TAIL = ('两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；'
              '共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。')
PCM_CLAIMS = {
    'key-ipc-pcm-turn-begin': '真实ACK授权、同轮令牌幂等、较新轮次替换及播放互斥；' + SCOPE_TAIL,
    'key-ipc-pcm-turn-end': '实际PCM入队后End、样本守恒与排空终态，错误最终偏移可修正；' + SCOPE_TAIL,
    'key-ipc-pcm-turn-interrupt': '真实立即停止和40ms淡出、旧轮拒绝、DTMF共用RTP身份继续；' + SCOPE_TAIL,
    'key-ipc-pcm-turn-status': '原始回应身份/计数/终态及挂断、worker代次失效后的明确拒绝；' + SCOPE_TAIL,
    'key-ipc-pcm-push': '原始S16LE批次到全部实际G.711样本、偏移/容量整批拒绝后继续；' + SCOPE_TAIL,
    'key-api-pcm-stream': '本地私有Unix控制/数据连接跨连接令牌、ACK/应用互斥/挂断和代次撤销；' + SCOPE_TAIL,
}
STATES = ('idle', 'buffering', 'playing', 'draining', 'stopping', 'completed', 'stopped', 'failed')
CASE_FILES = {'wire.json', 'config.json', 'prompt.wav', 'long-prompt.wav', 'events.jsonl', 'server.log'}
TOP_FILES = {'build.json','runtime-build.json','run-before.json','progress.json','recorder.py','runner.py',
             'udp.result.json','udp.stdout.log','udp.stderr.log','connected.result.json','connected.stdout.log','connected.stderr.log'}
TEST_DEPENDENCIES = {'sip_pcm_stream_e2e.py','media_processed_local_e2e.py','media_processed_fixture.py',
                     'media_pcm_turn_e2e.py','e2e.py','esl_e2e.py','esl_applications_e2e.py','dtmf_send_e2e.py'}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def wire(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2)+'\n').encode()


def utc(value):
    require(isinstance(value, str), '时刻不是带时区字符串')
    parsed = datetime.fromisoformat(value.replace('Z','+00:00'))
    require(parsed.tzinfo is not None, '没有采集时区')
    return parsed.astimezone(timezone.utc)


def safe_path(name):
    require(isinstance(name, str) and name and not Path(name).is_absolute()
            and all(p not in ('..','.') for p in name.split('/')) and '\\' not in name,
            '证据路径越界')
    return Path(name)


def read_artifact(base, reference):
    """同时限制解压量和路径；拒绝父目录符号链接及损坏SHA，不补造缺失文件。"""
    require(isinstance(reference, dict), '缺少原文件引用')
    name, expected = reference.get('path'), reference.get('sha256')
    relative = safe_path(name)
    require(isinstance(expected, str) and re.fullmatch('[a-f0-9]{64}', expected), 'SHA格式不合法')
    if isinstance(base, dict):
        item = base.get(name, {})
        require(item.get('encoding') == 'zlib+base64' and item.get('sha256') == expected, '内嵌引用不一致')
        require(isinstance(item.get('data'), str) and len(item['data']) <= 90 << 20, '压缩输入超过预算')
        decoder = zlib.decompressobj()
        raw = decoder.decompress(base64.b64decode(item['data'], validate=True), (64 << 20)+1)
        require(len(raw) <= 64 << 20 and decoder.eof and not decoder.unconsumed_tail and not decoder.unused_data, '压缩流截断或超过边界')
        require(item.get('bytes') == len(raw), '内嵌字节数不一致')
    else:
        base = Path(base)
        path = base / relative
        require(all(not (base/Path(*relative.parts[:i])).is_symlink() for i in range(1,len(relative.parts)+1)), '证据含符号链接')
        require(path.is_file() and path.resolve().is_relative_to(base.resolve()) and path.stat().st_size <= 64 << 20, '原文件缺失或超出预算')
        raw = path.read_bytes()
    require(sha(raw) == expected, '证据SHA不匹配')
    if 'bytes' in reference:
        require(type(reference['bytes']) is int and reference['bytes'] == len(raw), '原文件字节数不匹配')
    return raw


def pcm_refs(raw):
    require(isinstance(raw.get('artifacts'), list) and len(raw['artifacts']) == 144, '完整运行必须归档144个原文件')
    refs = {}
    for item in raw['artifacts']:
        safe_path(item.get('path'))
        require(item['path'] not in refs and Path(item['path']).name != 'esl.secret', '文件重复或混入凭据')
        require(type(item.get('bytes')) is int and 0<=item['bytes']<=64<<20, '原文件大小不是有界整数')
        refs[item['path']] = item
    require(sum(x['bytes'] for x in refs.values())<=64<<20, '整套原证据超过64MiB预算')
    return refs


def import_pcm(receipt_path, build_path, control, media, evidence_dir, root=ROOT):
    """导入真实已有文件，返回execution id；不会在导入时创造源码before或执行时刻。"""
    receipt_path, build_path, evidence_dir = Path(receipt_path), Path(build_path), Path(evidence_dir)
    require(receipt_path.is_file() and not receipt_path.is_symlink() and receipt_path.stat().st_size <= 8 << 20, '原收据缺失或超预算')
    original_bytes = receipt_path.read_bytes()
    original = json.loads(original_bytes)
    require(original.get('kind') == PCM_KIND and original.get('schema_version') == '1.0.0', '不是SIP PCM原始运行')
    refs = pcm_refs(original)
    identifier = 'pcm-' + uuid.uuid4().hex
    destination = evidence_dir / identifier
    destination.mkdir(parents=True, exist_ok=False)
    artifacts = {}
    def save(key, relative, data):
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        artifacts[key] = {'path': target.relative_to(evidence_dir).as_posix(), 'sha256': sha(data)}
    save('$receipt', 'original.receipt.json', original_bytes)
    save('$build', 'official-runtime-build.json', build_path.read_bytes())
    for key, reference in refs.items():
        save(key, 'raw/'+key, read_artifact(receipt_path.parent, reference))
    binaries = {}
    for role, path in [('control',Path(control)), ('media',Path(media))]:
        require(path.is_file() and not path.is_symlink() and 0 < path.stat().st_size <= 512 << 20, '实际二进制缺失或超预算')
        data = path.read_bytes()
        require(sha(data) == original['binaries_before'][role], '导入二进制不是实际运行文件')
        target = destination / ('binary-'+role)
        target.write_bytes(data)
        binaries[role] = {'path': target.relative_to(evidence_dir).as_posix(), 'sha256': sha(data)}
    envelope = {'schema_version':'1.0.0', 'kind':PCM_KIND, 'id':identifier,
                'started_at':original['started_at'], 'finished_at':original['finished_at'],
                'artifacts':artifacts, 'binaries':binaries}
    (evidence_dir/(identifier+'.receipt.json')).write_bytes(wire(envelope))
    return identifier


def pack_pcm(receipt, base):
    """与现运行证据zlib+base64形状一致；不向网页压入可执行二进制。"""
    result = {}
    for ref in receipt['artifacts'].values():
        data = read_artifact(base, ref)
        result[ref['path']] = {'encoding':'zlib+base64', 'bytes':len(data), 'sha256':sha(data),
                               'data':base64.b64encode(zlib.compress(data,9)).decode()}
    return result


def response_frame(raw):
    require(len(raw) >= 96 and raw[:4] == b'RVR1' and raw[94:96] == bytes(2), 'PCM回应头或保留位错误')
    length = struct.unpack_from('<H',raw,92)[0]
    require(length <= 512 and len(raw) == 96+length and raw[5] < len(STATES), 'PCM回应长度/状态错误')
    code = struct.unpack_from('<H',raw,6)[0]
    require(code <= 10, 'PCM错误码越界')
    turn,a,s,q,d,age = struct.unpack_from('<6Q',raw,32)
    qb,qm,b,p = struct.unpack_from('<IIHH',raw,80)
    require(a == s+q+d and all(v%160 == 0 for v in (a,s,q,d)) and qb==q*2 and qm==q//8, 'PCM实际计数不守恒')
    state = STATES[raw[5]]
    require((q or age==0) and (state not in ('completed','stopped','failed') or q==0), '空队列年龄或终态错误')
    if state != 'idle':
        require(turn>0 and 20<=p<=b<=1000 and p%20==b%20==0 and q<=b*8, 'PCM实际冻结容量错误')
    return dict(op=raw[4],state=state,code=code,request_id=struct.unpack_from('<Q',raw,8)[0],token=raw[16:32].hex(),
                turn_id=turn,accepted_samples=a,sent_samples=s,queued_samples=q,discarded_samples=d,
                oldest_age_ms=age,queued_bytes=qb,queued_ms=qm,buffer_ms=b,prebuffer_ms=p,message=raw[96:].decode('utf8'))


def replay_stream(rows, observations):
    """此固定夹具全局至多一个未决请求；精确重组分片而不依赖摘要中completed字段。"""
    pending, buffered, exchanges = None, bytearray(), []
    for row in rows:
        kind = row['direction']
        if kind not in ('stream_send','stream_receive'):
            continue
        data = bytes.fromhex(row['hex'])
        if kind == 'stream_send':
            require(pending is None and not buffered and len(data)>=64 and data[:4]==b'RVA1', '请求覆盖未决回应或头截断')
            require(data[5:8]==bytes(3) and data[56:64]==bytes(8), '请求保留位错误')
            length = struct.unpack_from('<I',data,48)[0]
            require(len(data)==64+length and length<=1600 and data[4]<=7, '请求正文缺失/越界')
            pending = dict(op=data[4],request_id=struct.unpack_from('<Q',data,8)[0],token=data[16:32].hex(),
                           turn_id=struct.unpack_from('<Q',data,32)[0],offset=struct.unpack_from('<Q',data,40)[0],
                           arg1=struct.unpack_from('<H',data,52)[0],arg2=struct.unpack_from('<H',data,54)[0],
                           body=data[64:],at_ms=row['at_ms'],peer=row['peer'])
        else:
            require(pending is not None and pending['peer']==row['peer'], '未关联私有连接回应')
            buffered.extend(data)
            require(len(buffered)<=608, '回应积压或多余数据')
            if len(buffered)<96:
                continue
            length = struct.unpack_from('<H',buffered,92)[0]
            require(length<=512, '错误正文超预算')
            if len(buffered)<96+length:
                continue
            value=response_frame(bytes(buffered))
            require((value['op'],value['request_id'])==(pending['op'],pending['request_id']), 'PCM实际请求ID/op不匹配')
            if value['code']==0 and pending['op'] not in (0,1,7):
                require((value['token'],value['turn_id'])==(pending['token'],pending['turn_id']), 'PCM实际轮次/令牌不匹配')
            if value['code']!=0:
                require(bool(value['message']), '明确拒绝缺实际原因')
            exchanges.append({'request':pending,'response':value,'at_ms':row['at_ms']})
            pending,buffered=None,bytearray()
    require(pending is None and not buffered and exchanges, 'PCM请求/回应不完整')
    observed=[o for o in observations if o['label']=='stream_reply']
    require(len(observed)==len(exchanges), '实际收包与记录回应数量不同')
    for event,item in zip(exchanges,observed):
        require(event['response']==item['response'] and event['request']['peer'][1]==item['role'], '解析值与实际收到的PCM帧不对应')
    return exchanges


def sip_frame(data):
    head, sep, body=data.partition(b'\r\n\r\n')
    require(sep, 'SIP头截断')
    lines=head.decode('utf8').split('\r\n')
    headers={}
    for line in lines[1:]:
        key,value=line.split(':',1)
        require(key.lower() not in headers, 'SIP重复单值头')
        headers[key.lower()]=value.strip()
    require(len(body)==int(headers['content-length']), 'SIP正文截断')
    return lines[0],headers,body


def replay_sip(rows):
    calls={}
    for row in rows:
        if row['direction'] not in ('sip_send','sip_receive'):
            continue
        first,h,body=sip_frame(bytes.fromhex(row['hex']))
        c=calls.setdefault(h['call-id'],dict(invite=0,accepted=0,ack=0,valid_ack=0,bye=0,bye_ok=0,acks=[]))
        number,method=h['cseq'].split()
        if first.startswith('INVITE '):
            require(row['direction']=='sip_send' and c['invite']==0, 'INVITE归属/数量错误')
            c.update(invite=1,from_header=h['from'],via=h['via'].split(';',1)[0],invite_cseq=number,start=row['at_ms'])
            match=re.search(rb'^m=audio (\d+) RTP/AVP (0|8) ',body,re.M)
            require(match and b'a=ptime:20\r\n' in body, '本地G711 20ms协商缺失')
            c['payload']=int(match[2]);c['remote_port']=int(match[1])
        elif first.startswith('ACK '):
            c['ack']+=1
            if (number==c.get('invite_cseq') and h['from']==c.get('from_header') and h['to']==c.get('accepted_to') and h['via'].split(';',1)[0]==c.get('via')):
                c['valid_ack']+=1;c['acks'].append(row['at_ms'])
        elif first.startswith('BYE '):
            c['bye']+=1;c['stop']=row['at_ms'];c['bye_cseq']=number;c['bye_direction']=row['direction']
        elif first.startswith('SIP/2.0 200'):
            if method=='INVITE':
                c['accepted']+=1;c['accepted_to']=h['to']
                match=re.search(rb'^m=audio (\d+) RTP/AVP (0|8) ',body,re.M)
                require(match and int(match[2])==c['payload'] and b'a=ptime:20\r\n' in body, '响应音频格式错误')
                c['media_port']=int(match[1])
            elif method=='BYE':
                require(number==c.get('bye_cseq') and row['direction']!=c.get('bye_direction'), 'BYE确认事务不匹配')
                c['bye_ok']+=1;c['closed']=row['at_ms']
    return calls


def esl_events(rows):
    result=[]
    for row in rows:
        if row['direction']!='esl_receive':continue
        data=bytes.fromhex(row['hex']);head,sep,body=data.partition(b'\n\n')
        require(sep, 'ESL帧截断')
        headers={k.lower():v.strip() for k,v in (line.decode().split(':',1) for line in head.splitlines())}
        require(int(headers.get('content-length','0'))==len(body), 'ESL正文长度错误')
        if headers['content-type']=='text/event-json':
            event=json.loads(body);event['_at_ms']=row['at_ms'];result.append(event)
    return result


def g711(samples, payload):
    """独立按G.711分段量化生成完整160字节，不调用产品或测试夹具的编解码实现。"""
    out=bytearray()
    for value in samples:
        if payload==0:
            mask=0x7f if value<0 else 0xff
            magnitude=min(abs(value),32635)+132
            exponent=max(0,magnitude.bit_length()-8)
            code=(exponent<<4)|((magnitude>>(exponent+3))&15)
        else:
            mask=0x55 if value<0 else 0xd5
            magnitude=-value-1 if value<0 else value
            exponent=max(0,magnitude.bit_length()-8)
            code=(exponent<<4)|((magnitude>>(4 if exponent==0 else exponent+3))&15)
        out.append(code^mask)
    return bytes(out)


def verify_zero(observation,generation,started,finished):
    state=observation['state'];at=observation['observed_at']
    require(type(at) in (float,int) and math.isfinite(at) and started<=at<=finished, '回收采样超出用例执行窗口')
    require(all(type(state[k]) is int and state[k]==0 for k in ('active_calls','established_calls')) and len(state['workers'])==1, '通话未真实回收')
    worker=state['workers'][0]
    require(worker['healthy'] is True and type(worker['generation']) is int and worker['generation']==generation and type(worker['restarts']) is int and worker['restarts']==generation-1 and type(worker['pid']) is int and worker['pid']>1, '回收worker代次/健康错误')
    require(all(type(worker['stats'].get(k)) is int and worker['stats'][k]==0 for k in ('active_calls','processed_active_calls','processed_local_active_calls')) and type(state['controller']['media_results_queue_length']) is int and state['controller']['media_results_queue_length']==0, '媒体/控制队列未归零')
    require(0<=at-utc(worker['admission']['sampled_at']).timestamp()<=4, '回收快照不是新鲜采样')


def replay_media(rows,exchanges,calls,events,method,prompt):
    """以实际BEGIN的UUID关联SIP周期；拒绝批次绝不进入内容oracle，逐字节比全部RTP音频。"""
    uuids={e['Unique-ID']:e['variable_sip_call_id'] for e in events if e['Event-Name']=='CHANNEL_CREATE'}
    require(set(uuids.values())==set(calls), 'ESL实际UUID与SIP周期未关联')
    sources={};begin_tokens={};rejection=Counter()
    for x in exchanges:
        q,r=x['request'],x['response'];op=q['op']
        if r['code']:
            rejection[(op,r['code'])]+=1;continue
        if op==1:
            channel=q['body'].decode();token=r['token']
            require(channel in uuids and token!='00'*16 and r['turn_id']==q['turn_id'] and (r['buffer_ms'],r['prebuffer_ms'])==(q['arg1'],q['arg2']), 'begin实际授权身份错误')
            call=calls[uuids[channel]]
            require(call['acks'] and q['at_ms']>=call['acks'][0], '成功BEGIN早于真实有效ACK')
            key=(channel,q['turn_id'])
            require(begin_tokens.setdefault(key,token)==token, '同轮begin令牌不幂等')
            sources.setdefault(token,dict(call=uuids[channel],start=q['at_ms'],samples=[],last=r,interrupt=None,rows=[]))
        elif op in (2,3,4,5):
            require(q['token'] in sources, '成功PCM操作未绑定实际BEGIN')
            source=sources[q['token']]
            require(r['sent_samples']>=source['last']['sent_samples'], '已发送计数回退')
            if op==2:
                require(len(q['body']) in (320,640,960,1280,1600) and q['offset']==len(source['samples']), '受理PCM不是有序完整帧')
                source['samples'].extend(struct.unpack('<'+'h'*(len(q['body'])//2),q['body']))
                require(r['accepted_samples']==len(source['samples']), '受理计数与真实PCM正文不一致')
            elif op==3:
                require(q['offset']==len(source['samples'])==r['accepted_samples'], 'End超越真实完整PCM')
            elif op==4:
                require(q['arg1'] in (0,20,40), '非法淡出却成功')
                source['interrupt']=(q['arg1'],r['sent_samples'],q['at_ms'])
            source['last']=r
    # 只有对应真实ESL EXECUTE才允许WAV进入oracle；不能让任意错误音频碰巧匹配提示文件。
    legacy=[]
    for e in events:
        if e['Event-Name']=='CHANNEL_EXECUTE' and e.get('Application') in ('playback','read') and 'prompt.wav' in e.get('Application-Data',''):
            legacy.append(dict(call=uuids[e['Unique-ID']],start=e['_at_ms'],samples=prompt,last=None,interrupt=None,rows=[]))
    all_sources=list(sources.values())+legacy
    count=0
    for row in rows:
        if row['direction']=='rtp_receive':
            require(sum(c['start']<=row['at_ms']<=c['closed'] for c in calls.values())==1, '挂机后或未建呼仍收到旧媒体')
    for call_id,c in calls.items():
        stream=[s for s in all_sources if s['call']==call_id]
        stream.sort(key=lambda s:s['start'])
        previous=None;call_audio=0;previous_audio=None
        for row in rows:
            if row['direction']!='rtp_receive' or not c['start']<=row['at_ms']<=c['closed']:continue
            data=bytes.fromhex(row['hex'])
            require(len(data)>=12 and data[0]==0x80 and row['peer']==['127.0.0.1',c['media_port']], '实际RTP来源/标准头不正确')
            pt=data[1]&127;seq,ts,ssrc=struct.unpack_from('!HII',data,2)
            if previous:
                require(ssrc==previous[2] and seq==(previous[0]+1)%65536, '同呼叫RTP身份/序号不连续')
            previous=(seq,ts,ssrc)
            if pt!=c['payload']:
                require(pt==101 and len(data)==16, '未协商辅助流或音频载荷错误');continue
            require(len(data)==172 and c['acks'] and row['at_ms']>=c['acks'][0], '实际音频不是ACK后20ms完整G711')
            if previous_audio:
                ticks=(ts-previous_audio[0])%2**32
                require(0<ticks<80000 and abs(ticks/8-(row['at_ms']-previous_audio[1]))<=40, '跨轮次RTP时钟回退或不符合真实时间')
            previous_audio=(ts,row['at_ms'])
            eligible=[s for s in stream if s['start']<=row['at_ms']]
            require(eligible, '实际音频没有PCM/WAV来源')
            source=eligible[-1]
            index=len(source['rows'])*160
            samples=source['samples'][index:index+160]
            if source['interrupt'] and source['interrupt'][0]>0:
                _,sent_before,_=source['interrupt']
                kept=min(source['interrupt'][0]*8,len(source['samples'])-sent_before)
                if index>=sent_before:
                    samples=[int(v*(kept-1-(index-sent_before+i))/(kept-1)) for i,v in enumerate(samples)]
            require(len(samples)==160 and data[12:]==g711(samples,pt), '实际RTP完整样本与受理PCM/淡出不一致')
            if source['rows']:
                p=source['rows'][-1]
                require((ts-p['timestamp'])%2**32==160 and row['at_ms']-p['at_ms']>=5, '连续PCM时钟错误或实际接收集中突发')
            source['rows'].append({'timestamp':ts,'at_ms':row['at_ms']})
            count+=1;call_audio+=1
        require(call_audio==0 if method==6 else call_audio>=2, '实际呼叫没有完整可验证媒体')
    for source in all_sources:
        actual=len(source['rows'])*160;r=source['last']
        if r is None:
            require(actual==len(prompt), '实际WAV提示不完整')
        elif r['state'] in ('completed','stopped'):
            require(actual==r['sent_samples'], '缺RTP帧或实际发送终态计数不对应')
            if r['state']=='completed':require(actual==len(source['samples']), '自然完成丢失样本')
        else:
            require(actual<=len(source['samples']), '中断/挂机后多发旧PCM')
    expected_fixed={0:5,2:20,3:5,6:0,7:5,8:65,9:25}
    if method in expected_fixed:require(count==expected_fixed[method], '固定场景实际完整音频数量错误')
    if method in (1,4,5,10):require(count>0, '故障/停止场景无真实PCM')
    return {'audio_packets':count,'content_samples':count*160,'sources':sources,'rejections':rejection}


def check_case(document,case,read,binaries,source,mode,method,timing,media_path):
    name=PCM_METHODS[method];start,end=timing['started_at'],timing['finished_at']
    require(document['test']==case['test']==PREFIX+name and document['connected'] is (mode=='connected') and document['topology']=='local', '用例身份/拓扑错误')
    require(document['binaries_before']==document['binaries_after']==binaries and document['binaries_unchanged'] is True, '用例实际二进制改变')
    require(document['test_sources_before']==document['test_sources_after'] and set(document['test_sources_before'])==TEST_DEPENDENCIES, '用例源码依赖缺失/改变')
    require(all(source['tests/'+key]==value for key,value in document['test_sources_before'].items()), '用例源码不属于正式运行清单')
    require(document['fixture_before']==document['fixture_after'] and set(document['fixture_before'])=={'config.json','prompt.wav','long-prompt.wav'}, '实际配置/WAV夹具未绑定')
    directory=Path(case['wire']['path']).parent.as_posix()
    require(set(document['files'])==CASE_FILES-{'wire.json'}, '原用例文件清单不完整')
    for name_,ref in document['files'].items():
        data=read(directory+'/'+name_)
        require(sha(data)==ref['sha256'] and len(data)==ref['bytes'], '原用例文件SHA或字节数不符')
        if name_ in document['fixture_before']:
            require(sha(data)==document['fixture_before'][name_], '夹具内容不是执行时文件')
    config=json.loads(read(directory+'/config.json'))
    require(config['media']['processing']=='g711' and config['media']['workers']==1 and config['media'].get('connect_sockets',False) is (mode=='connected') and config['sip']['local_extensions']==['1000'], '实际配置不是声明的本地处理模式')
    require(config['media']['binary']==media_path and config['media']['bind_ip']=='127.0.0.1' and config['media']['port_end']==config['media']['port_start']+3, '实际worker路径/隔离四端口配置错误')
    require(config['pcm_stream']['max_connections']==8 and config['pcm_stream']['max_streams']==1, '资源预算夹具被替换')
    require(document['forced_cleanup'] is False and type(document['process_returncode']) is int and document['process_returncode']==0 and document.get('private_socket_removed_by_server') is True, '进程/socket没有正常清理')
    rows=document['wire'];require(0<len(rows)<=4096 and document['wire_bytes']<=8<<20, '原报文数量或字节超预算')
    require(len(rows)==case['wire_records'] and sum(len(bytes.fromhex(x['hex'])) for x in rows)==document['wire_bytes']==case['wire_bytes'], '原报文数量/字节不完整')
    allowed={'sip_send','sip_receive','esl_send','esl_receive','rtp_send','rtp_receive','rtcp_send','rtcp_receive','stream_send','stream_receive'}
    times=[]
    for row in rows:
        require(row['direction'] in allowed and type(row['at_ms']) in (float,int) and math.isfinite(row['at_ms']) and 0<=row['at_ms']<=(end-start)*1000+100, '原帧来源/采集时钟错误')
        times.append(row['at_ms'])
    require(times==sorted(times), '原帧时钟回退')
    exchanges=replay_stream(rows,document['observations']);calls=replay_sip(rows);events=esl_events(rows)
    expected_calls={2:2,4:4,10:2}.get(method,1)
    require(len(calls)==expected_calls, '真实SIP周期缺失')
    for c in calls.values():
        require(c['invite']==1 and c['accepted']>=1 and c['bye']==c['bye_ok']==1, '真实INVITE/200/BYE/200不完整')
        require(c['ack']==0 if method==6 else c['valid_ack']==(2 if method==0 else 1), '有效ACK门禁不足')
    if method==0:require(all(c['ack']==5 for c in calls.values()), '三个错误ACK及重复正确ACK分支缺失')
    # 原收据摘要必须由真实报文重算得到，不能作为独立事实来源。
    derived={'completed_responses':len(exchanges),'response_codes':dict(Counter(str(x['response']['code']) for x in exchanges)),
             'request_model':'one_outstanding_request_in_this_fixture'}
    require(case['stream']==derived, '流摘要与实际报文不符')
    raw_sip={key:{k:v for k,v in value.items() if k in ('invite','accepted','ack','valid_ack','bye','bye_ok','from_header','via','invite_cseq','accepted_to')} for key,value in calls.items()}
    require(case['sip_transactions']==raw_sip, 'SIP摘要不是实际事务')
    stream_obs=[o for o in document['observations'] if o['label']=='stream_reply']
    bases=[o['observed_at']-x['at_ms']/1000 for o,x in zip(stream_obs,exchanges)]
    require(bases and max(bases)-min(bases)<.05 and start<=min(bases)<=end, '原帧单调时钟与真实采集时刻无法对应')
    zero=[o for o in document['observations'] if o['label'] in ('fresh_final_zero','fresh_final_zero_after_worker_fault')]
    require(len(zero)==expected_calls==case['fresh_zero_observations'] and document['final_status']==zero[-1]['state'], '最后真实零值采样缺失')
    for observation,c in zip(zero,calls.values()):
        verify_zero(observation,2 if method==10 else 1,start,end)
        require(observation['observed_at']>=min(bases)+c['closed']/1000, '回收状态早于真实BYE确认')
        require(utc(observation['state']['workers'][0]['admission']['sampled_at']).timestamp()>=min(bases)+c['closed']/1000, '复用了呼叫释放前的旧零值采样')
    journal=[json.loads(x) for x in read(directory+'/events.jsonl').decode().splitlines() if x.strip()]
    require(journal and journal[0]['kind']=='controller_started' and len({x['run_id'] for x in journal})==1, '实际运行日志缺失或混用')
    for call_id in calls:
        lifecycle=[x['kind'] for x in journal if x.get('call_id')==call_id]
        require(lifecycle==(['call_admitted','call_answered','call_ended'] if method==6 else ['call_admitted','call_answered','call_established','call_ended']), 'SIP实际准入/接通/回收日志不完整')
    require(all(start<=utc(j['time']).timestamp()<=end for j in journal), '通话日志超出运行窗口')
    prompt_wire=read(directory+'/prompt.wav')
    require(prompt_wire[:4]==b'RIFF' and prompt_wire[8:12]==b'WAVE' and prompt_wire[36:40]==b'data' and len(prompt_wire)==3244, '固定WAV原文件不完整')
    prompt=struct.unpack('<1600h',prompt_wire[44:])
    result=replay_media(rows,exchanges,calls,events,method,prompt)
    rejects=result['rejections']
    if method==0:require(rejects[(1,3)]==4, 'ACK前/错误ACK实际拒绝不足')
    if method==3:
        require(rejects[(2,1)]==rejects[(2,5)]==rejects[(2,3)]==rejects[(3,3)]==1, '坏帧/背压/偏移/End拒绝路径缺失')
    if method==6:require(rejects[(1,3)]==2 and not result['sources'], '无ACK调用错误受理或拒绝缺失')
    if method==5:
        require(rejects[(2,4)]==rejects[(3,4)]==rejects[(1,3)]==rejects[(2,3)]==1, '旧令牌/旧轮/停止屏障缺失')
        fade=[s for s in result['sources'].values() if s['interrupt'] and s['interrupt'][0]==40]
        require(len(fade)==1 and fade[0]['last']['sent_samples']==320 and fade[0]['last']['discarded_samples']==480, '真实40ms淡出计数缺失')
    if method in (4,10):
        cycles=2 if method==4 else 1
        require(sum(rejects[(5,k)] for k in (4,8))==cycles and sum(rejects[(2,k)] for k in (4,8))==cycles and rejects[(1,3)]==cycles, '挂断/失效令牌仍能使用或旧UUID拒绝缺失')
    if method==10:
        fault=[o for o in document['observations'] if o['label']=='owned_worker_fault_injection']
        require(len(fault)==1 and fault[0]['signal']=='SIGTERM' and fault[0]['generation']==1 and fault[0]['pid']>1 and fault[0]['parent_pid']>1, '缺少明确自有worker故障注入记录')
        require(all(o['state']['workers'][0]['pid']!=fault[0]['pid'] for o in zero), '故障后不是实际新worker')
    if method==7:
        kinds=[e['Event-Name'] for e in events]
        require(kinds.count('CHANNEL_PARK')==kinds.count('CHANNEL_UNPARK')==1 and kinds.index('CHANNEL_PARK')<kinds.index('CHANNEL_UNPARK'), 'park进入/离开事件缺失')
        terminal=max(x['at_ms'] for x in exchanges if x['response']['state']=='completed')
        unpark=next(e for e in events if e['Event-Name']=='CHANNEL_UNPARK')
        require(unpark['_at_ms']>terminal, 'PCM完成错误结束park')
    if method==8:
        require([e['DTMF-Digit'] for e in events if e['Event-Name']=='DTMF']==list('12#'), '真实收号不是12#且仅一次')
        completed=[e for e in events if e['Event-Name']=='CHANNEL_EXECUTE_COMPLETE' and e.get('Application')=='read']
        require(len(completed)==2 and completed[0].get('variable_choice')=='12' and completed[0].get('variable_read_result')=='success' and completed[0].get('variable_read_terminator_used')=='#' and completed[1].get('variable_read_result')=='failure', 'silent read成功/超时实际结果缺失')
    if method==9:
        require(rejects[(1,5)]==2, '真实WAV/read互斥拒绝缺失')
        refusals=[bytes.fromhex(x['hex']) for x in rows if x['direction']=='esl_receive' and b'-ERR PCM' in bytes.fromhex(x['hex'])]
        require(len(refusals)==2, 'PCM所有权未阻止两种提示应用')
    if method==1:
        dtmf=[bytes.fromhex(x['hex']) for x in rows if x['direction']=='rtp_receive' and bytes.fromhex(x['hex'])[1]&127==101]
        for event in (1,2,11):
            packets=[p for p in dtmf if p[12]==event]
            require(len(packets)==5 and len({p[4:8] for p in packets})==1 and [bool(p[13]&128) for p in packets]==[False,False,True,True,True] and [int.from_bytes(p[14:16],'big') for p in packets]==[160,320,440,440,440], '实际55ms DTMF事件/三end不完整')
        require(len(dtmf)==15, '停止PCM后DTMF缺失/重复')
    return {'call_cycles':len(calls),'audio_packets':result['audio_packets'],'content_samples':result['content_samples'],
            'stream_responses':len(exchanges),'raw_packets':len(rows)}


def evaluate_pcm(receipt,base,current,as_of,identifier,verify_binaries=True):
    """目录/内嵌包采用同一验证逻辑；真实过期标stale，任何漏项/损坏不能授予绿色。"""
    proof={'id':str(receipt.get('id','invalid'))+':'+identifier,'comparison_id':identifier,'kind':PCM_KIND,
           'scope':PCM_CLAIMS.get(identifier,''),'execution_id':receipt.get('id'),
           'test_names':[mode+':'+PREFIX+name for mode in ('udp','connected') for name in PCM_METHODS],
           'status':'missing_evidence','reason':'缺少完整SIP PCM原始证据','executed':False,'green_eligible':False,
           'started_at':receipt.get('started_at'),'finished_at':receipt.get('finished_at')}
    try:
        require(identifier in PCM_CLAIMS and receipt.get('kind')==PCM_KIND and receipt.get('schema_version')=='1.0.0', '错误的证据kind/入口')
        artifacts=receipt['artifacts'];cache={}
        def read(key):
            if key not in cache:cache[key]=read_artifact(base,artifacts[key])
            return cache[key]
        raw=json.loads(read('$receipt'));build=json.loads(read('$build'))
        require(raw['kind']==PCM_KIND and raw['schema_version']=='1.0.0' and raw['binding_method']=='source_before_and_after_actual_execution' and raw['mode']=='full', '不是当时完整运行绑定')
        require(build['schema_version']=='1.0.0' and build['kind']=='local_runtime_build' and type(build['returncode']) is int and build['returncode']==0, '官方构建未成功')
        require(raw['started_at']==receipt['started_at'] and raw['finished_at']==receipt['finished_at'], '导入器改写执行时刻')
        started,finished=utc(raw['started_at']),utc(raw['finished_at'])
        require(utc(build['started_at'])<=utc(build['finished_at'])<=started<=finished<=as_of+timedelta(minutes=5) and (finished-started).total_seconds()<=600, '构建/执行时间不一致或超期')
        source=raw['source_before']
        require(source and source==raw['source_after']==build['source_before']==build['source_after'], '没有相等的真实构建/运行前后源码')
        require(all(safe_path(k) and isinstance(v,str) and re.fullmatch('[a-f0-9]{64}',v) for k,v in source.items()), '源码清单字段错误')
        require(all('tests/'+name in source for name in TEST_DEPENDENCIES) and all(k in source for k in ('control/internal/server/pcm_stream.go','control/internal/server/pcm_endpoint.go','control/internal/media/pcm_stream.go','media/src/media/worker_pcm.rs','media/src/media/pcm_transport.rs')), '新链路源码未绑定，不能借旧内层证据')
        binaries=raw['binaries_before']
        require(binaries==raw['binaries_after'] and set(binaries)=={'control','media','generator'}, '实际运行二进制改变/漏项')
        require(all(build['binaries'][k]['sha256']==v for k,v in binaries.items()), '运行SHA不匹配官方构建')
        require(set(receipt['binaries'])=={'control','media'}, '归档二进制角色错误')
        for role in ('control','media'):
            ref=receipt['binaries'][role]
            require(ref['sha256']==binaries[role], '归档binary不是本次实际执行文件')
            if verify_binaries:
                relative=safe_path(ref['path']);path=Path(base)/relative
                require(path.is_file() and not path.is_symlink() and path.resolve().is_relative_to(Path(base).resolve()) and 0<path.stat().st_size<=512<<20, '归档二进制缺失或路径越界')
                require(all(not (Path(base)/Path(*relative.parts[:i])).is_symlink() for i in range(1,len(relative.parts)+1)), '归档二进制父目录含符号链接')
                digest=hashlib.sha256()
                with path.open('rb') as stream:
                    for block in iter(lambda:stream.read(1<<20),b''):digest.update(block)
                require(digest.hexdigest()==ref['sha256'], '实际归档binary SHA错误')
        refs=pcm_refs(raw)
        require(set(artifacts)=={'$receipt','$build',*refs}, '完整原文件引用被删改或混入')
        for key,ref in refs.items():require(sha(read(key))==ref['sha256'] and len(read(key))==ref['bytes'], '原始144文件SHA/字节不一致')
        require(read(raw['build_receipt']['path'])==read('$build') and raw['build_receipt']==refs['runtime-build.json'], '官方build不是录制时保存原件')
        before=json.loads(read('run-before.json'))
        require(before['started_at']==raw['started_at'] and before['source_before']==source and before['binaries_before']==binaries and before['stages']==[] and before['passed'] is False and 'source_after' not in before, '缺少真实执行前快照或补造before')
        metadata=json.loads(read('build.json'))
        require(metadata['ok'] is True and metadata['source_before']==metadata['source_after'], '构建详细原始记录错误')
        require(all(metadata['binaries'][name]==binaries[role] for role,name in [('control','rustswitch'),('media','rustswitch-media'),('generator','callbench')]), '详细构建记录binary不一致')
        require(raw['passed'] is True and raw['errors']==[] and raw['methods']==list(PCM_METHODS), '固定方法未完整通过')
        require([s['mode'] for s in raw['stages']]==['udp','connected'], '缺少两个UDP模式')
        total=Counter();expected_paths=set(TOP_FILES);previous=started
        for stage in raw['stages']:
            require(stage['passed'] is True and type(stage['exit_code']) is int and stage['exit_code']==0 and stage['timed_out'] is stage['forced_cleanup'] is False and stage['validation_errors']==[], '阶段失败/跳过/超时')
            st,en=utc(stage['started_at']),utc(stage['finished_at'])
            require(previous<=st<en<=finished and 0<stage['wall_seconds']<=180, '阶段没有顺序实际运行')
            previous=en;mode=stage['mode']
            command=stage['command']
            require(isinstance(command,list) and len(command)==9 and Path(command[1]).name=='runner.py' and Path(command[2]).name=='tests'
                    and Path(command[3]).name=='rustswitch' and Path(command[4]).name=='rustswitch-media' and Path(command[5]).name==mode
                    and command[6]==mode and Path(command[7]).name==mode+'.result.json' and json.loads(command[8])==list(PCM_METHODS), '实际执行参数未绑定固定方法/候选')
            require(stage['result']==refs[mode+'.result.json'] and stage['stdout']==refs[mode+'.stdout.log'] and stage['stderr']==refs[mode+'.stderr.log'], '阶段日志引用被替换')
            observed=json.loads(read(mode+'.result.json'))
            require(observed==stage['observed'] and observed['discovered']==list(PCM_METHODS) and observed['expected']==observed['started']==observed['passed']==[PREFIX+n for n in PCM_METHODS] and observed['tests_run']==11 and observed['successful'] is True, '原unittest回调未逐项通过')
            require(all(observed[k]==[] for k in ('failures','errors','skipped','expected_failures','unexpected_successes')), '子执行器含失败或跳过')
            expected_sub=[(PCM_METHODS[2],"law='PCMU'"),(PCM_METHODS[2],"law='PCMA'"),(PCM_METHODS[4],'server_hangup=False'),(PCM_METHODS[4],'server_hangup=True'),(PCM_METHODS[9],"application='playback'"),(PCM_METHODS[9],"application='read'")]
            require(observed['subtests']==[{'parent':PREFIX+p,'id':PREFIX+p+' ('+arg+')','successful':True,'traceback':None} for p,arg in expected_sub], '12个真实子分支不完整/失败')
            log=read(mode+'.stderr.log').decode()
            require(not re.search(r'^FAILED|^(FAIL|ERROR):|\.\.\. (FAIL|ERROR|skipped)',log,re.M), '原unittest日志包含失败或跳过')
            require(read(mode+'.stdout.log')==b'' and re.findall(r'^(test_\w+) \('+re.escape(PREFIX)+r'test_\w+\) \.\.\. ok$',log,re.M)==list(PCM_METHODS) and re.search(r'\nRan 11 tests in [0-9.]+s\n\nOK\n?\Z',log), '原unittest日志缺项/截断/失败')
            require(len(observed['timings'])==len(stage['cases'])==11 and [c['test'] for c in stage['cases']]==[PREFIX+n for n in PCM_METHODS], '原帧用例缺失/重复')
            last=st.timestamp();stagepaths=set()
            for index,(case,timing) in enumerate(zip(stage['cases'],observed['timings'])):
                require(timing['id']==PREFIX+PCM_METHODS[index] and last<=timing['started_at']<timing['finished_at']<=en.timestamp(), '方法时间交叠或越界')
                last=timing['finished_at'];directory=Path(case['wire']['path']).parent.as_posix()
                require(directory.startswith(mode+'/'+PCM_METHODS[index]+'-'), '方法原文件路径混用')
                names={directory+'/'+name for name in CASE_FILES}
                require(not expected_paths.intersection(names), '方法重复使用他例证据')
                expected_paths.update(names);stagepaths.update(names)
                require(case['wire']==refs[case['wire']['path']] and case['normal_exit'] is True, '原帧索引不一致')
                document=json.loads(read(case['wire']['path']))
                total.update(check_case(document,case,read,{k:binaries[k] for k in ('control','media')},source,mode,index,timing,command[4]))
            require(stage['raw_artifacts']==[refs[k] for k in sorted(stagepaths)], '阶段原文件列表被删改')
        require(set(refs)==expected_paths and total['call_cycles']==32, '144文件/32实际SIP周期不完整')
        proof.update(executed=True,binding_contract='official_full_build_and_run_snapshots',source_snapshot_sha256=sha(wire(source)),
                     verified_binary_sha256={k:binaries[k] for k in ('control','media')},raw_artifact_count=144,
                     method_count=22,subtest_count=12,**dict(total),log=artifacts['udp.stderr.log'],
                     recorder_sha256=refs['recorder.py']['sha256'],runner_sha256=refs['runner.py']['sha256'])
        if source!=current or as_of>=finished+timedelta(days=30):
            proof.update(status='stale',reason='当前功能/测试源码改变或证据超过30天，必须完整重跑')
        else:
            proof.update(status='passed',green_eligible=True,expires_at=(finished+timedelta(days=30)).isoformat(),reason='完整22方法/12子项、实际构建运行绑定、全部PCM/RTP样本及32通话回收通过限定门禁')
    except (OSError,ValueError,TypeError,KeyError,AttributeError,OverflowError,IndexError,struct.error,zlib.error) as error:
        proof.update(status='failed',green_eligible=False,reason='SIP PCM原始证据门禁未通过：'+str(error))
    return proof
