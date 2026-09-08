#!/usr/bin/env python3
"""验证原版双端原始报告并发布轻量后台摘要；源码变化或缺少真实报文禁止生成绿色。"""
import argparse
import base64
from collections import Counter
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import re
import time
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
TARGET=ROOT/'docs/api'
module_spec=importlib.util.spec_from_file_location('conformance_publish_suite',ROOT/'tests/conformance/suite.py')
suite=importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(suite)


def require(condition,message):
    if not condition:
        raise ValueError(message)


def encoded(value):
    return (json.dumps(value,ensure_ascii=False,indent=2)+'\n').encode()


def validate_wire(observation):
    """校验全部观察结果的原始字节，包括身份、目录和失败差异，不能只保护绿色行。"""
    records=observation.get('wire',[])
    require(isinstance(records,list),'报文记录不是列表')
    decoded=[]
    total=0
    for item in records:
        require(item.get('direction') in ('send','receive'),'报文方向非法')
        require(type(item.get('length')) is int and 0<item['length']<=16*1024*1024,'报文字节长度非法')
        require(isinstance(item.get('sha256'),str) and re.fullmatch('[0-9a-f]{64}',item['sha256']) is not None,'报文SHA格式非法')
        total+=item['length']
        if 'base64' in item:
            data=base64.b64decode(item['base64'],validate=True)
            require(len(data)==item['length'] and hashlib.sha256(data).hexdigest()==item['sha256'],'原始报文SHA或字节长度不一致')
        else:
            require(item['direction']=='send' and item.get('redacted')=='ESL 认证秘密；不保留可恢复密码','只有固定认证秘密允许脱敏；收到的报文必须完整保留')
            data=None
        decoded.append((item,data))
    if observation.get('state')=='observed':
        require(total==observation.get('wire_bytes') and total>0,'完整观察的原始报文字节总数不一致')
    return decoded


class ReplaySocket:
    """只从已有证据读取，绝不打开网络；任何缺失请求/响应都会中止回放。"""
    def __init__(self, transcript):
        self.transcript=transcript

    def settimeout(self, _seconds):
        pass

    def recv(self, _maximum):
        if not self.transcript:
            return b''
        item,data=self.transcript[0]
        require(item['direction']=='receive' and data is not None,'回放需要接收时缺少原始响应')
        self.transcript.pop(0)
        return data


def replay_esl(probe,observation,decoded):
    """用冻结执行器在内存收发适配器上重跑相同断言；这是证据复核，不是新增产品运行。"""
    transcript=list(decoded)
    placeholder='__publisher_redacted_password__'

    class ReplayESL:
        def __init__(self,_target,limits,wire,deadline):
            self.reader=suite.FrameReader(ReplaySocket(transcript),deadline,limits,wire)

        def __enter__(self):
            greeting=self.reader.frame()
            require(greeting['headers'].get('content-type')=='auth/request' and not greeting['body'],'回放缺少真实ESL握手')
            self.greeting=suite.comparable_frame(greeting)
            return self

        def __exit__(self,*_):
            require(not self.reader.buffer,'连接结束时仍有未核对的接收帧')

        def send(self,data,fragment=False,secret=False):
            require(bool(transcript),'回放缺少原始请求')
            item,actual=transcript.pop(0)
            require(item['direction']=='send','请求记录缺失或顺序不一致')
            if secret:
                require(actual is None and bool(item.get('redacted')),'认证请求必须保留原脱敏证据')
                # 合法密码在归档中不可恢复；固定错误密码可按原请求摘要再次校验。
                if placeholder.encode() not in data:
                    require(item['length']==len(data) and item['sha256']==hashlib.sha256(data).hexdigest(),'固定错误认证请求与报文摘要不一致')
            else:
                require(actual==data,'原始请求与冻结探针实际需要发送的内容不同')

        def auth(self,password,separator=b'\n\n',fragment=False):
            self.send(b'auth '+password.encode()+separator,fragment=fragment,secret=True)
            frame=self.reader.frame()
            require(frame['headers'].get('content-type')=='command/reply' and frame['headers'].get('reply-text')=='+OK accepted','原始认证未成功')
            return suite.comparable_frame(frame)

        def request(self,data):
            self.send(data)
            return self.reader.frame()

    # 临时替换的只是传输和秘密获取接口；所有命令、期望值、格式与分支仍来自冻结 suite。
    original_esl,original_password=suite.ESL,suite.endpoint_password
    limits={'connect_seconds':2,'probe_seconds':60,'max_header_bytes':65536,'max_body_bytes':16*1024*1024,'max_evidence_bytes':16*1024*1024}
    try:
        suite.ESL=ReplayESL
        suite.endpoint_password=lambda _target:placeholder
        value=suite.run_esl(probe,{},limits,suite.Wire(limits['max_evidence_bytes']))
    except (OSError,EOFError,suite.ProbeError) as error:
        raise ValueError('原始ESL报文未能重现通过断言：'+str(error)) from error
    finally:
        suite.ESL=original_esl
        suite.endpoint_password=original_password
    require(not transcript,'回放结束后仍有未核对的额外请求或响应')
    require(value==observation.get('value'),'声明的解析结果与真实收发回放结果不同')


def validate_sip_observation(observation,decoded):
    """OPTIONS 差异行也从实际请求/响应重新提取能力头，不信任可编辑的解析字段。"""
    sent=b''.join(data for item,data in decoded if item['direction']=='send' and data is not None)
    received=b''.join(data for item,data in decoded if item['direction']=='receive' and data is not None)
    require(sent.startswith(b'OPTIONS ') and b'\r\n\r\n' in sent,'缺少真实OPTIONS请求')
    _,header_data=sent.split(b'\r\n',1)
    request_headers=suite.parse_headers(header_data.split(b'\r\n\r\n',1)[0])
    limits={'max_header_bytes':65536,'max_body_bytes':16*1024*1024}
    reader=suite.FrameReader(suite.SingleDatagram(b''),time.monotonic()+60,limits,suite.Wire(16*1024*1024))
    reader.buffer=bytearray(received)
    final=None
    while reader.buffer:
        require(b'\r\n' in reader.buffer,'SIP响应状态行不完整')
        line,rest=bytes(reader.buffer).split(b'\r\n',1)
        reader.buffer=bytearray(rest)
        match=re.fullmatch(rb'SIP/2.0 ([1-6][0-9][0-9]) (.*)',line)
        require(match is not None,'SIP响应状态行非法')
        frame=reader.frame()
        headers=frame['headers']
        require(headers.get('call-id')==request_headers.get('call-id') and headers.get('cseq')==request_headers.get('cseq'),'SIP响应与实际请求事务不同')
        code=int(match.group(1))
        if code>=200:
            require(final is None,'OPTIONS含多条最终响应，超出冻结探针观察')
            final={'status_code':code,'reason_base64':base64.b64encode(match.group(2)).decode(),
                'capabilities':{name:sorted(part.strip().lower() for part in headers[name].split(',') if part.strip()) if name in headers else None for name in ('allow','supported','accept','allow-events')},
                'body_base64':base64.b64encode(frame['body']).decode()}
    require(final is not None,'缺少完整OPTIONS最终响应')
    require(all(observation.get('value',{}).get(key)==value for key,value in final.items()),'OPTIONS解析值与真实接收能力头不同')


def summarize(raw):
    """仅复制每个探针的有限声明，不以接口发现、身份或完整清单产生认证通过。"""
    report=json.loads(raw)
    require(report.get('source_stable') is True,'成对执行期间源码变化')
    current=suite.source_fingerprint(ROOT)
    require(report.get('source_before')==current==report.get('source_after'),'成对报告不属于当前产品和执行器源码')
    require(report.get('reference',{}).get('version')=='1.11.3' and report['reference'].get('commit')==suite.REFERENCE_COMMIT,'原版基线不匹配')
    require(report.get('reference_runtime_executed') is True,'原版运行未取得证据')
    require(not report.get('full_compatibility_passed') and not report.get('all_protocols_passed') and not report.get('full_superiority_proven'),'本执行器没有完整认证分支，不允许宣称全面通过')
    comparison=json.loads((ROOT/'docs/api/comparison.json').read_bytes())
    expected_count=len(comparison['entries'])
    require(expected_count>0 and len(report.get('entries',[]))==expected_count and len({x['id'] for x in report['entries']})==expected_count,'原始条目缺失或重复')
    require({x['id'] for x in report['entries']}=={x['id'] for x in comparison['entries']},'原始ID集合与权威对照不同')
    require(all(x.get('complete_contract_passed') is False for x in report['entries']),'原始条目被误标完整通过')
    require({p['id'] for p in report.get('probes',[])}=={p['id'] for p in suite.PROBES} and len(report['probes'])==len(suite.PROBES),'探针集合缺失或重复')
    # 用真实构建收据及原版安装树绑定两端文件；它仍是隔离本机证据，不冒充远程进程证明。
    build_raw=(TARGET/'conformance-build.json').read_bytes();build=json.loads(build_raw)
    reference_raw=(TARGET/'conformance-reference.json').read_bytes();reference=json.loads(reference_raw)
    require(build.get('source_before')==current==build.get('source_after') and build.get('returncode')==0,'候选构建源码与报告不一致')
    for side,manifest_raw,binary_sha in [('candidate',build_raw,build['binaries']['rustswitch']),('reference',reference_raw,reference['binary']['sha256'])]:
        artifacts=report['endpoint_artifacts'][side]
        require(not artifacts.get('missing'),'成对端点构建/配置清单缺失')
        require(artifacts['files']['binary_path']['sha256']==binary_sha,'成对二进制与构建/安装产物不一致')
        require(artifacts['files']['source_manifest_path']['sha256']==hashlib.sha256(manifest_raw).hexdigest(),'成对构建/安装清单SHA不同')
    require(report['endpoint_artifacts']['reference']['files']['config_path']['sha256']==reference['active_config']['sha256'],'原版运行配置与安装树不同')
    require(report['endpoint_artifacts']['candidate']['files']['config_path']['sha256']==build['runtime_config']['sha256'],'候选运行配置没有绑定收据')
    ended=datetime.fromisoformat(report['finished_at'].replace('Z','+00:00'))
    require(ended.tzinfo is not None,'完成时刻缺时区')
    require(ended<=datetime.now(timezone.utc)+timedelta(minutes=5),'完成时刻来自未来')
    require(datetime.now(timezone.utc)<ended+timedelta(days=30),'成对报告已经超过30天复核期限')
    definitions={p['id']:p for p in suite.PROBES}
    for probe in report['probes']:
        definition=definitions[probe['id']]
        require(probe.get('scope')==definition['scope'] and probe.get('identity_only',False)==definition.get('identity_only',False),'探针范围或身份属性被改变')
        for observation in probe.get('results',{}).values():
            decoded=validate_wire(observation)
            if observation.get('state')=='observed':
                if definition['adapter']=='esl':
                    replay_esl(definition,observation,decoded)
                else:
                    validate_sip_observation(observation,decoded)
    # 目录只以重新核对的实际注册观察推导，防止复制可篡改的行数、状态或删减后的分母。
    plan=suite.build_plan(ROOT)
    inventory_record=next(p for p in report['probes'] if p['id']=='identity.api_inventory')
    expected_inventory=suite.compare_api_inventory(plan,inventory_record)
    require(report.get('api_inventory')==expected_inventory,'API目录与实际两端注册/全部原始ID重算结果不同')
    identity=next(p for p in report['probes'] if p['id']=='identity.esl')
    require(identity.get('state')=='identity_observed','原版身份探针未通过观察')
    for side in ('reference','candidate'):
        observation=identity.get('results',{}).get(side,{})
        require(observation.get('state')=='observed' and observation.get('wire_bytes',0)>0 and observation.get('wire'),'缺少双端身份原始证据')
        received=b''.join(base64.b64decode(x['base64'],validate=True) for x in observation['wire'] if x['direction']=='receive' and 'base64' in x)
        require(observation.get('value',{}).get('version','').encode() in received and bool(received),'身份版本正文与原始接收不同')
    require(suite.reference_version_matches(identity['results']['reference']['value']['version']),'原版身份版本错误')
    definitions={p['id']:p for p in suite.PROBES}
    rows=[]
    for probe in report['probes']:
        state=probe['state']
        require(probe.get('scope')==definitions[probe['id']]['scope'] and probe.get('identity_only',False)==definitions[probe['id']].get('identity_only',False),'探针范围或身份属性被改变')
        for observed in probe.get('results',{}).values():
            for item in observed.get('wire',[]):
                if item.get('direction')=='receive':require('base64' in item,'收到的原版/候选报文不能整体脱敏而失去依据')
        if state=='passed_scoped':
            require(not probe.get('identity_only') and not probe['id'].startswith('identity.'),'身份/目录发现不能计为兼容通过')
            require(probe.get('complete_contract_passed') is False,'探针越界声称完整契约')
            results=probe.get('results',{})
            require(set(results)=={'reference','candidate'},'成对端点证据缺失')
            require(all(v.get('state')=='observed' and v.get('wire_bytes',0)>0 and v.get('wire') for v in results.values()),'通过探针没有真实双端报文')
            require(results['reference']['value']==results['candidate']['value'],'不相等探针被标为通过')
        rows.append({k:probe[k] for k in ('id','state','scope') if k in probe})
    return {'schema_version':'1.0.0','reference':report['reference'],'finished_at':report['finished_at'],'expires_at':(ended+timedelta(days=30)).isoformat(),
            'source_snapshot_sha256':current['sha256'],'report_sha256':hashlib.sha256(raw).hexdigest(),'reference_runtime_executed':True,
            'full_compatibility_passed':False,'all_protocols_passed':False,'full_superiority_proven':False,'counts':dict(Counter(x['state'] for x in rows)),
            'original_entries':expected_count,'probes':rows,'api_inventory':expected_inventory,
            'scope':'每项绿色仅表示显示的双端子场景通过；API入口存在、原版已加载、目录完整均不等于语义全兼容。'}


def validate(root=ROOT):
    """文档发布时离线重算，不依赖生产服务或原版仍然运行。"""
    raw=(root/'docs/api/conformance-report.json').read_bytes()
    expected=encoded(summarize(raw))
    require((root/'docs/api/conformance-summary.json').read_bytes()==expected,'后台成对摘要不匹配原始报告')
    return json.loads(expected)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--report',type=Path)
    parser.add_argument('--check',action='store_true')
    args=parser.parse_args()
    require(bool(args.report)!=args.check,'选择--report或--check之一')
    if args.check:
        value=validate()
    else:
        raw=args.report.read_bytes();value=summarize(raw)
        (TARGET/'conformance-report.json').write_bytes(raw)
        (TARGET/'conformance-summary.json').write_bytes(encoded(value))
        (TARGET/'conformance-report.md').write_text(suite.render_report(json.loads(raw)))
    print(json.dumps({'passed':True,'scoped_states':value['counts'],'full_compatibility_passed':False},ensure_ascii=False))


if __name__=='__main__':
    main()
