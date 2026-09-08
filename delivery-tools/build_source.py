#!/usr/bin/env python3
"""离线编译交付源码；输出到显式独立目录，不启动服务，不改变源码或发布证据。"""
from pathlib import Path
import argparse,subprocess,os,json,hashlib,platform,time

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--variant',choices=['main','asr-candidate'],default='main')
    parser.add_argument('--output',required=True,help='新的构建输出目录，必须在交付包之外')
    args=parser.parse_args();bundle=Path(__file__).resolve().parents[1]
    source=bundle/'source'/args.variant;output=Path(args.output).resolve()
    if output.is_relative_to(bundle):parser.error('输出目录必须在交付包之外，保留源码快照不变')
    output.mkdir(parents=True,exist_ok=False)
    env=dict(os.environ,GOTOOLCHAIN='local',GOPROXY='off',GOSUMDB='off')
    # Cargo使用本包固定依赖；绝对路径只写入此次构建输出，解压目录可任意移动。
    cargo_config=output/'cargo-config.toml'
    cargo_config.write_text('[source.crates-io]\nreplace-with = "vendored-sources"\n[source.vendored-sources]\ndirectory = '+json.dumps(str(bundle/'dependencies/rust-vendor'))+'\n')
    go=os.environ.get('GO','go');cargo=os.environ.get('CARGO','cargo');cc=os.environ.get('CC','cc')
    ext='dylib' if platform.system()=='Darwin' else 'so'
    cmds=[('go-version',[go,'version'],source),('rust-version',[cargo,'--version'],source),
      ('control',[go,'build','-mod=vendor','-trimpath','-o',str(output/'rustswitch'),'./cmd/rustswitch'],source/'control'),
      ('callbench',[go,'build','-mod=vendor','-trimpath','-o',str(output/'callbench'),'./cmd/callbench'],source/'control')]
    if args.variant=='asr-candidate':cmds.append(('asr-mock',[go,'build','-mod=vendor','-trimpath','-o',str(output/'asr-mock'),'./cmd/asr-mock'],source/'control'))
    cmds+=[('media',[cargo,'--config',str(cargo_config),'build','--locked','--offline','--release','--manifest-path',str(source/'media/Cargo.toml'),'--target-dir',str(output/'cargo-target')],source),
      ('codec-plugin',[cc,'-std=c11','-O2','-Wall','-Wextra','-Werror',*(['-dynamiclib'] if ext=='dylib' else ['-shared','-fPIC']),'-I',str(source/'native/include'),str(source/'native/examples/g711_plugin.c'),'-o',str(output/('librustswitch_g711.'+ext))],source)]
    receipt={'variant':args.variant,'scope':'离线编译源码与C ABI示例；不启动SIP/HTTP，不运行容量验收，不授予发布状态。','started_at':time.time(),'steps':[]}
    manifest=json.loads((bundle/'source'/f'{args.variant}-manifest.json').read_text())
    def verify():return all(hashlib.sha256((source/x['path']).read_bytes()).hexdigest()==x['sha256'] for x in manifest['files'])
    if not verify():raise ValueError('源码快照在构建前已变化')
    try:
        for name,cmd,cwd in cmds:
            with (output/(name+'.log')).open('w') as log:
                started=time.time();p=subprocess.run(cmd,cwd=cwd,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=900)
            receipt['steps'].append({'name':name,'argv':cmd,'exit_code':p.returncode,'seconds':round(time.time()-started,3)})
            print(name,p.returncode,flush=True)
            if p.returncode:break
    finally:
        receipt['source_unchanged']=verify();receipt['finished_at']=time.time()
        receipt['passed']=len(receipt['steps'])==len(cmds) and all(x['exit_code']==0 for x in receipt['steps']) and receipt['source_unchanged']
        receipt['logs']={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in output.glob('*.log')}
        (output/'receipt.json').write_text(json.dumps(receipt,ensure_ascii=False,indent=2)+'\n')
    raise SystemExit(0 if receipt['passed'] else 1)
if __name__=='__main__':main()
