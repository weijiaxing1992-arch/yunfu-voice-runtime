#!/usr/bin/env python3
"""仅验证私有压测诊断候选；内存HTTP/VM夹具不构成真实媒体或容量验收。"""
from pathlib import Path
from datetime import datetime,timezone
import hashlib,json,os,subprocess,sys
BASE=Path(__file__).resolve().parent;ROOT=BASE.parents[2];CONTROL=BASE/'control'
GO=ROOT/'work/toolchain/go/bin/go'
NODE=Path('/home/developer/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node')
def stamp():return datetime.now(timezone.utc).isoformat()
def digest(p):return {'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'size_bytes':p.stat().st_size}
def snapshot():
 paths=[p for prefix in [CONTROL,BASE/'tools'] for p in prefix.rglob('*') if p.is_file()]
 paths.append(Path(__file__))
 return {str(p.relative_to(BASE)):digest(p) for p in sorted(paths)}
def main():
 out=BASE/sys.argv[1];out.mkdir(exist_ok=False)
 common=BASE.parent/'resample-evidence'
 env=dict(os.environ,DEVELOPER_DIR='/Library/Developer/CommandLineTools',PATH=f'{GO.parent}:/Library/Developer/CommandLineTools/usr/bin:/usr/bin:/bin:/usr/sbin:/sbin',GOENV='off',GOTOOLCHAIN='local',GOPROXY='off',GOCACHE=str(common/'.gocache'),GOPATH=str(common/'.gopath'),TMPDIR=str(common/'tmp'))
 for key in list(env):
  if key.startswith('RUSTSWITCH_'):env.pop(key)
 pattern='^TestBenchmark(FailureFinal|FinalObservation|SocketDropCoverage|Processing(MissingStatsAreUnknown|Aggregation|FinalFreshness|ReportContract|SharedReceiverBudget)|StatusRequiredFields)'
 base=[str(GO),'test','-mod=vendor','-json','-count=1','-timeout=90s','-run='+pattern]
 steps=[('go-version',[str(GO),'version']),('unit',base+['./internal/server']),('race',base+['-race','./internal/server'])]
 steps.extend((Path(name).stem,[str(NODE),str(BASE/'tools'/name)]) for name in ['test_testlab_ui.cjs','test_processed_media_ui.cjs','test_overview_ui.cjs'])
 record={'started_at':stamp(),'scope':'private Go benchmark diagnostics pure targeted unit/race and existing JS VM; no real HTTP listening, pressure traffic, service changes, or deployment','source_before':snapshot(),'checks':[]}
 try:
  for name,argv in steps:
   started=stamp()
   with (out/(name+'.stdout.log')).open('w') as stdout,(out/(name+'.stderr.log')).open('w') as stderr:
    p=subprocess.run(argv,cwd=CONTROL,env=env,stdout=stdout,stderr=stderr,timeout=180)
   c={'name':name,'argv':argv,'started_at':started,'finished_at':stamp(),'exit_code':p.returncode}
   if name in ['unit','race']:
    events=[json.loads(s) for s in (out/(name+'.stdout.log')).read_text().splitlines() if s.startswith('{')]
    c.update(top_level_pass=sum(e.get('Action')=='pass' and bool(e.get('Test')) and '/' not in e['Test'] for e in events),subtest_pass=sum(e.get('Action')=='pass' and '/' in e.get('Test','') for e in events),fail_events=sum(e.get('Action')=='fail' for e in events),skip_events=sum(e.get('Action')=='skip' for e in events))
   record['checks'].append(c);print(name,p.returncode,flush=True)
 finally:
  record['source_after']=snapshot();record['source_stable']=record['source_before']==record['source_after'];record['finished_at']=stamp();record['passed']=len(record['checks'])==len(steps) and all(c['exit_code']==0 and c.get('fail_events',0)==0 and c.get('skip_events',0)==0 for c in record['checks']) and record['source_stable'];record['logs']={p.name:digest(p) for p in sorted(out.iterdir()) if p.is_file()};(out/'receipt.json').write_text(json.dumps(record,ensure_ascii=False,indent=2)+'\n')
 raise SystemExit(0 if record['passed'] else 1)
if __name__=='__main__':main()
