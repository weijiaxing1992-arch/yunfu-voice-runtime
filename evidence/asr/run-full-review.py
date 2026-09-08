"""审查后完整Go回归：串行包执行、显式真实媒体和代理、独立证据目录，不使用主服务。"""
from pathlib import Path
from collections import Counter
from datetime import datetime, timezone
import hashlib, json, os, subprocess, sys, time

base = Path(__file__).resolve().parent
project = base / 'project'
out = base / sys.argv[1]
out.mkdir(mode=0o700)
media = base.parent / 'voice-runtime-m2-rx/admin-build-01/rustswitch-media'
mock = base / 'mock-build-04/asr-mock'
node = Path('/home/developer/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node')

def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()

def sources():
    return {str(p.relative_to(project)): sha(p) for folder in ('control', 'config', 'media/src')
            for p in sorted((project / folder).rglob('*')) if p.is_file()}

env = os.environ.copy()
env.update(RUSTSWITCH_REAL_MEDIA=str(media), RUSTSWITCH_ASR_MOCK_BIN=str(mock),
           RUSTSWITCH_ASR_REAL_OUTPUT=str(out/'asr-client'), RUSTSWITCH_ASR_SIP_OUTPUT=str(out/'asr-sip'),
           RUSTSWITCH_RX_SIP_ARTIFACTS=str(out/'rx-sip'), RUSTSWITCH_RX_PAUSE_ARTIFACTS=str(out/'rx-pause'))
(out/'rx-sip').mkdir(mode=0o700)
receipt = {'schema': 'voice-runtime-full-code-review-v1', 'started_at': datetime.now(timezone.utc).isoformat(),
           'source_before': sources(), 'binaries': {str(p):sha(p) for p in (media,mock)}, 'steps': [],
           'scope': 'Go全包竞态/真实本机SIP与媒体回归、静态检查、页面函数、双平台编译；不是Linux容量或真实识别模型认证'}
steps = [('go', ['go','test','-race','-count=1','-p=1','-json','-timeout=10m','./...'], project/'control', {}),
         ('vet',['go','vet','./...'], project/'control', {}),
         ('ui-asr',[str(node),'tools/test_asr_overview_ui.cjs'], project, {}),
         ('ui-overview',[str(node),'tools/test_overview_ui.cjs'], project, {}),
         ('build-darwin',['go','build','-trimpath','-o',str(out/'rustswitch-darwin'),'./cmd/rustswitch'], project/'control', {}),
         ('build-linux',['go','build','-trimpath','-o',str(out/'rustswitch-linux-arm64'),'./cmd/rustswitch'], project/'control', {'GOOS':'linux','GOARCH':'arm64','CGO_ENABLED':'0'})]
try:
    for name, args, cwd, additions in steps:
        started=time.monotonic()
        logpath=out/(name+'.jsonl' if name=='go' else name+'.log')
        with logpath.open('xb') as log:
            result=subprocess.run(args,cwd=cwd,env={**env,**additions},stdout=log,stderr=subprocess.STDOUT)
        receipt['steps'].append({'name':name,'args':args,'returncode':result.returncode,'seconds':time.monotonic()-started,'log':logpath.name,'sha256':sha(logpath)})
        print(json.dumps(receipt['steps'][-1]),flush=True)
        if result.returncode: break
finally:
    counts=Counter();top=Counter();packages=Counter()
    if (out/'go.jsonl').exists():
        for line in (out/'go.jsonl').read_text().splitlines():
            try: item=json.loads(line)
            except ValueError: continue
            action=item.get('Action')
            if action not in ('pass','fail','skip'): continue
            if 'Test' in item:
                counts[action]+=1
                if '/' not in item['Test']: top[action]+=1
            else: packages[action]+=1
    receipt.update(test_events=dict(counts),top_level_tests=dict(top),package_events=dict(packages),source_after=sources(),
                   finished_at=datetime.now(timezone.utc).isoformat())
    receipt['source_stable']=receipt['source_before']==receipt['source_after']
    receipt['binaries_stable']=all(sha(Path(p))==h for p,h in receipt['binaries'].items())
    receipt['passed']=len(receipt['steps'])==len(steps) and all(s['returncode']==0 for s in receipt['steps']) and counts['fail']==0 and counts['skip']==0 and receipt['source_stable'] and receipt['binaries_stable']
    for name in ('rustswitch-darwin','rustswitch-linux-arm64'):
        if (out/name).is_file(): receipt.setdefault('built',{})[name]=sha(out/name)
    (out/'receipt.json').write_text(json.dumps(receipt,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({k:receipt[k] for k in ('test_events','top_level_tests','package_events','source_stable','binaries_stable','passed')}),flush=True)
sys.exit(0 if receipt['passed'] else 1)
