// 文档进展界面的纯 Node 回归：内存负例与只读真实发布包回放；不改发布数据或运行媒体。
'use strict';
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),assert=require('node:assert/strict'),crypto=require('node:crypto');
const source=fs.readFileSync(path.join(__dirname,'../control/internal/server/web/docs.js'),'utf8');
const clone=value=>JSON.parse(JSON.stringify(value)),hash=value=>crypto.createHash('sha256').update(value).digest('hex');
const wire=value=>JSON.stringify(value,null,2)+'\n',now=Date.parse('2026-09-07T10:00:00Z');
const cases=[];const test=(name,fn)=>cases.push([name,fn]);

// 小分母覆盖全部六种本地状态，保留一条完整契约待验收且本地已通过的条目。
function fixture(){
  const ids=['new-pass','old-pass','lost-pass','not-run','stale','missing','unbound'];
  const states=['passed','passed','failed','not_run','stale','missing_evidence','unbound'];
  const comparison={reference_version:'1.11.3',reference_commit:'a'.repeat(40),scope:'合成界面夹具',entries:ids.map((id,index)=>({id,name:'夹具 '+id,status:index===0?'not_verified':'partial',category:'key_media',differences:[],searchText:id})),summary:{by_status:{not_verified:1,partial:6}}};
  const observations=ids.slice(0,2).map(id=>({id:'proof-'+id,comparison_id:id,kind:'go_race_subset',status:'passed',green_eligible:true,executed:true,scope:'明确限定 '+id,test_names:['TestFixture'],source_snapshot_sha256:'b'.repeat(64),log:{sha256:'c'.repeat(64)},finished_at:'2026-09-07T09:00:00Z',expires_at:'2026-10-07T09:00:00Z'}));
  const entries=ids.map((id,index)=>({comparison_id:id,implementation_status:comparison.entries[index].status,local_status:states[index],green_eligible:index<2,verification_id:index<2?'proof-'+id:null,effective_expires_at:null,scope:'明确限定 '+id,reason:'夹具状态'}));
  for(const row of entries)row.effective_expires_at=row.green_eligible?observations[0].expires_at:null;
  const local_counts={passed:2,failed:1,not_run:1,stale:1,missing_evidence:1,unbound:1};
  const runtime={version:'1.0.0',comparison_sha256:hash(wire(comparison)),upstream_runtime_executed:false,paired_status:'not_run',production_capacity_certified:false,evaluated_on:'2026-09-07',source_snapshot:{'control/main.go':'d'.repeat(64)},entries,observations,integrity_errors:[],summary:{green_entries:2,comparison_entries:7,by_local_status:local_counts}};
  const progress={schema_version:'1.0.0',comparison_sha256:runtime.comparison_sha256,runtime_sha256:hash(wire(runtime)),baseline_sha256:'e'.repeat(64),source_sha256:hash(wire(runtime.source_snapshot)),baseline:{version:'1.7.0',comparison_entries:7,green_entries:2,implementation_counts:{not_verified:1,partial:6},local_counts:{passed:2,not_run:5}},current:{version:'1.8.0',comparison_entries:7,green_entries:2,implementation_counts:{not_verified:1,partial:6},local_counts},changes:{new_green:[{id:'new-pass',name:'夹具 new-pass',scope:'明确限定 new-pass'}],lost_green:[{id:'lost-pass',name:'夹具 lost-pass',reason:'本次运行失败'}],implementation_changed:[],added_ids:[],removed_ids:[]}};
  const conformance={expires_at:'2026-10-07T09:00:00Z',finished_at:'2026-09-07T09:00:00Z',reference:{version:'1.11.3'},original_entries:7,api_inventory:{},probes:[{id:'sip-flow',state:'passed_scoped',scope:'单路子场景'},{id:'api-inventory',state:'inventory_observed',scope:'目录发现'},{id:'control-error',state:'failed_difference',scope:'错误行为差异'}]};
  const manifest={version:'1.8.0',documents:[{path:'api/comparison.json',sha256:runtime.comparison_sha256},{path:'api/runtime-verification.json',sha256:progress.runtime_sha256},{path:'api/compatibility-progress.json',sha256:hash(wire(progress))},{path:'api/compatibility-baseline-v1.7.json',sha256:progress.baseline_sha256},{path:'api/compatibility-worklist.md',sha256:'f'.repeat(64)}]};
  return {comparison,runtime,progress,manifest,conformance};
}

// DOM 与网络替身只接受只读请求；按需导出闭包函数用于检查，产品导出面不变。
function setup(){
  const data=fixture(),nodes=new Map(),listeners={},timers=[];let time=now,raw=wire(data.progress);
  const node=id=>{if(!nodes.has(id))nodes.set(id,{id,innerHTML:'',dataset:{},querySelectorAll(){return [];},scrollIntoView(){}});return nodes.get(id);};
  class Clock extends Date{static now(){return time;}}
  const context={window:{},document:{addEventListener(name,fn){listeners[name]=fn;},getElementById:node,querySelectorAll(){return [];}},location:{hash:'#fs-comparison'},history:{replaceState(){}},URL,URLSearchParams,Map,Set,TextEncoder,Uint8Array,AbortController,Date:Clock,crypto:crypto.webcrypto,requestAnimationFrame(){},setTimeout(fn,delay){timers.push({fn,delay});return timers.length;},clearTimeout(){},fetch:async(_url,options)=>{assert.equal(options.method,'GET');return {ok:true,text:async()=>raw};}};
  const exports='view,completePCMProof,completeRXProof,rxBoundPath,rxTimestampNS,validateRuntimeView,validateProgressView,loadProgress,loadRuntime,runtimeTotals,runtimeSummary,progressSummary,renderComparison,filteredComparison,conformanceCounts,scheduleRuntimeExpiry';
  vm.runInNewContext(source.replace('return {mount,leave};',`return {mount,leave,${exports}};`),context);
  const api=context.window.RustSwitchDocs,checked=api.validateRuntimeView(data.runtime,data.comparison,data.manifest);
  Object.assign(api.view,{comparison:data.comparison,runtime:data.runtime,runtimeIndex:checked.rows,runtimeProofs:checked.proofs,manifest:data.manifest,conformance:data.conformance,progress:data.progress,page:'fs-comparison'});
  return {api,data,nodes,node,listeners,timers,setTime(value){time=value;},setRaw(value){raw=value;}};
}

test('首屏四卡按本地证据计数，完整契约状态独立且折叠',()=>{
  const s=setup();s.api.renderComparison();const html=s.node('docs-root').innerHTML;
  assert.ok(html.indexOf('id="docs-runtime-summary"')<html.indexOf('id="docs-conformance-summary"'));
  assert.ok(html.indexOf('id="docs-conformance-summary"')<html.indexOf('实现与完整契约目录'));
  assert.match(html,/实现与完整契约目录：7 条 · 完整契约待验收 1 条/);
  for(const [label,total]of [['本地验证通过',2],['本地尚未执行',1],['本地验证失败',1],['证据失效或不可用',3]])assert.ok(html.includes(`aria-label="${label} ${total} 条"`));
  assert.equal(s.data.comparison.entries[0].status,'not_verified');
  assert.ok(html.includes('全部缺口推进清单'));assert.ok(html.includes('刷新验证记录'));
  assert.match(s.api.conformanceCounts(),/<strong>1<\/strong>有效的成对子场景通过/);
  assert.equal(s.api.runtimeTotals().passed,2);
});
test('失效合并筛选仍覆盖过期、缺失和未绑定三种实际记录',()=>{
  const s=setup();s.api.view.runtimeStatus='invalid';
  assert.deepEqual(Array.from(s.api.filteredComparison(),row=>row.id),['stale','missing','unbound']);
  s.api.view.runtimeStatus='not_run';assert.deepEqual(Array.from(s.api.filteredComparison(),row=>row.id),['not-run']);
});
test('可靠发布增量通过指纹、当前计数及逐ID检查',async()=>{
  const s=setup(),result=await s.api.loadProgress(s.data.manifest);
  await s.api.validateProgressView(result,s.data.comparison,s.data.runtime,s.data.manifest);
  const html=s.api.progressSummary(s.api.runtimeTotals());
  assert.match(html,/新增限定通过 <strong>1<\/strong>，撤回 <strong>1<\/strong>/);
  assert.ok(html.includes('entry=new-pass'));assert.ok(html.includes('entry=lost-pass'));
});
test('证据到期从四卡撤绿，同时隐藏此前有效的增量',()=>{
  const s=setup();s.setTime(Date.parse('2026-10-07T09:00:00Z'));const totals=s.api.runtimeTotals();
  assert.equal(totals.passed,0);assert.equal(totals.stale,3);
  const html=s.api.runtimeSummary();assert.ok(html.includes('aria-label="本地验证通过 0 条"'));assert.ok(html.includes('当前证据状态已变化'));
  assert.doesNotMatch(html,/新增限定通过 <strong>/);assert.doesNotMatch(s.api.conformanceCounts(),/<strong>1<\/strong>有效的成对子场景通过/);
});
test('运行文件缺失显示未知，不能把未读到的结果写成零通过',()=>{
  const s=setup();s.api.view.runtime=null;s.api.view.runtimeError='证据尚未取得';s.api.view.progress=null;
  const html=s.api.runtimeSummary();assert.ok(html.includes('aria-label="本地验证通过 尚未取得 条"'));assert.ok(html.includes('本轮变化：暂不提供数量'));assert.doesNotMatch(html,/新增限定通过 <strong>0/);
});
for(const mode of ['file-hash','missing-file','source-hash','runtime-hash','baseline-hash','version','counts','boolean-count','unknown-state','duplicate-new','unknown-new','lost-still-green','scope','denominator','net-change'])test(`进展损坏或错配必须拒绝：${mode}`,async()=>{
  const s=setup(),p=clone(s.data.progress),m=clone(s.data.manifest);
  if(mode==='file-hash'){s.setRaw(wire(p)+' ');await assert.rejects(()=>s.api.loadProgress(m),/SHA/);return;}
  if(mode==='missing-file'){m.documents=m.documents.filter(row=>row.path!=='api/compatibility-progress.json');await assert.rejects(()=>s.api.loadProgress(m),/尚未提供/);return;}
  if(mode==='source-hash')p.source_sha256='0'.repeat(64);
  else if(mode==='runtime-hash')p.runtime_sha256='0'.repeat(64);
  else if(mode==='baseline-hash')delete p.baseline_sha256;
  else if(mode==='version')p.current.version='9.0.0';
  else if(mode==='counts')p.current.local_counts.not_run++;
  else if(mode==='boolean-count')p.current.local_counts.stale=true;
  else if(mode==='unknown-state')p.current.local_counts.imagined=1;
  else if(mode==='duplicate-new')p.changes.new_green.push(clone(p.changes.new_green[0]));
  else if(mode==='unknown-new')p.changes.new_green[0].id='not-in-catalog';
  else if(mode==='lost-still-green')p.changes.lost_green[0]={id:'old-pass',name:'夹具 old-pass',reason:'错误撤回'};
  else if(mode==='scope')p.changes.new_green[0].scope='所有FreeSWITCH功能兼容';
  else if(mode==='denominator')p.changes.removed_ids.push('removed-but-uncounted');
  else if(mode==='net-change')p.changes.lost_green=[];
  await assert.rejects(()=>s.api.validateProgressView(p,s.data.comparison,s.data.runtime,m));
});
test('增量条目名称与范围始终转义，不执行原始HTML',()=>{
  const s=setup();s.api.view.progress.changes.new_green[0].name='<img src=x onerror=alert(1)>';
  s.api.view.progress.changes.new_green[0].scope='<script>bad()</script>';
  const html=s.api.progressSummary(s.api.runtimeTotals());assert.ok(html.includes('&lt;img'));assert.ok(html.includes('&lt;script&gt;'));assert.doesNotMatch(html,/<img|<script>/);
});
test('未提供或拒绝的增量不影响本地和原版探针的独立结果',()=>{
  const s=setup();s.api.view.progress=null;s.api.view.progressError='进展与当前发布证据指纹不一致';
  const html=s.api.runtimeSummary();assert.ok(html.includes('aria-label="本地验证通过 2 条"'));assert.ok(html.includes('本轮变化：暂不提供数量'));assert.equal(s.api.runtimeTotals().failed,1);
  assert.match(s.api.conformanceCounts(),/<strong>1<\/strong>有效的成对子场景通过/);
});

// 真实发布包必须走同一个浏览器读取/SHA/严格kind门禁，避免小夹具漏掉新增正式证据类型。
const publishedRoot=process.env.RUSTSWITCH_DOCS_UI_ROOT||path.join(__dirname,'../docs/api');
function publishedFixture(){
  const raw=fs.readFileSync(path.join(publishedRoot,'runtime-verification.json'),'utf8');
  return {raw,runtime:JSON.parse(raw),comparison:JSON.parse(fs.readFileSync(path.join(publishedRoot,'comparison.json'),'utf8')),manifest:JSON.parse(fs.readFileSync(path.join(__dirname,'../control/internal/server/doc_assets/manifest.json'),'utf8'))};
}
function publishedRXCount(p){const rows=p.runtime.observations.filter(item=>item.kind==='go_rxs2_local_g711_composite_e2e'&&item.green_eligible);assert.ok(rows.length===0||rows.length===5);return rows.length;}
function updatePublishedHash(p){p.raw=wire(p.runtime);p.manifest.documents.find(item=>item.path==='api/runtime-verification.json').sha256=hash(p.raw);}
function selectProcessed(p){const proof=p.runtime.observations.find(item=>item.kind==='python_unittest_processed_media_e2e'&&item.green_eligible);assert.ok(proof,'实际发布必须有新processed证据');return proof;}
function publishIntoView(s,p,checked){Object.assign(s.api.view,{comparison:p.comparison,runtime:p.runtime,runtimeIndex:checked.rows,runtimeProofs:checked.proofs,manifest:p.manifest,progress:null,conformance:null,runtimeError:''});}
test('当前嵌入runtime/comparison/manifest经SHA门禁后保留基线及实际RX增量',async()=>{
  const s=setup(),p=publishedFixture();
  assert.equal(hash(fs.readFileSync(path.join(publishedRoot,'comparison.json'),'utf8')),p.manifest.documents.find(item=>item.path==='api/comparison.json').sha256);
  s.setRaw(p.raw);const checked=await s.api.loadRuntime(p.manifest,p.comparison);publishIntoView(s,p,checked);
  const html=s.api.runtimeSummary();
  for(const[label,total]of [['本地验证通过',77+publishedRXCount(p)],['本地尚未执行',3916],['本地验证失败',1],['证据失效或不可用',0]])assert.ok(html.includes(`aria-label="${label} ${total.toLocaleString('en-US')} 条"`)||html.includes(`aria-label="${label} ${total} 条"`),label);
  assert.equal(s.api.runtimeTotals().passed,77+publishedRXCount(p));assert.equal(checked.rows.get('key-media-transcoding').green_eligible,true);
  assert.equal(p.comparison.entries.find(item=>item.id==='key-media-transcoding').status,'partial');
});
for(const mode of ['runtime-file-sha','unknown-kind','wrong-key','missing-mode','duplicate-test','substituted-test','binding','source-sha','source-snapshot','binary-sha','binary-missing','rejection-count','rejection-evidence','catalog-count','connected-count','call-cycles','scope','future-start','expired-contract','missing-receipt','receipt-binary','missing-raw','raw-sha','log-sha','failed-latest'])test(`实际processed损坏证据不能授绿：${mode}`,async()=>{
  const s=setup(),p=publishedFixture(),proof=selectProcessed(p);
  const receipt=p.runtime.receipts.find(item=>item.id===proof.execution_id);
  if(mode==='runtime-file-sha'){s.setRaw(p.raw+' ');await assert.rejects(()=>s.api.loadRuntime(p.manifest,p.comparison),/SHA/);return;}
  if(mode==='unknown-kind')proof.kind='python_unittest_anything';
  else if(mode==='wrong-key')proof.comparison_id='key-media-capacity';
  else if(mode==='missing-mode')proof.test_names=proof.test_names.slice(0,11);
  else if(mode==='duplicate-test')proof.test_names[21]=proof.test_names[20];
  else if(mode==='substituted-test')proof.test_names[21]='connected:ProcessedMediaIntegration.test_empty_fake';
  else if(mode==='binding')proof.binding_contract='legacy_actual_go_rust_tests_plus_official_build_native_config';
  else if(mode==='source-sha')proof.source_snapshot_sha256='0'.repeat(64);
  else if(mode==='source-snapshot')p.runtime.runtime_source_snapshot['media/src/media/processed.rs']='0'.repeat(64);
  else if(mode==='binary-sha')proof.verified_binary_sha256.media='0'.repeat(64);
  else if(mode==='binary-missing')delete proof.verified_binary_sha256.control;
  else if(mode==='rejection-count')proof.rejection_cases=1;
  else if(mode==='rejection-evidence')proof.rejection_evidence='raw_488_no_admission_and_bound_test_status_assertions';
  else if(mode==='catalog-count')proof.runtime_catalog_observations=0;
  else if(mode==='connected-count')proof.connected_cases=19;
  else if(mode==='call-cycles')proof.call_cycles=21;
  else if(mode==='scope')proof.scope='全部FreeSWITCH和10000路已经通过';
  else if(mode==='future-start')proof.started_at='2030-01-01T00:00:00Z';
  else if(mode==='expired-contract')proof.expires_at='2030-01-01T00:00:00Z';
  else if(mode==='missing-receipt')p.runtime.receipts=p.runtime.receipts.filter(item=>item.id!==proof.execution_id);
  else if(mode==='receipt-binary')receipt.record.binaries.media.sha256='0'.repeat(64);
  else if(mode==='missing-raw')delete receipt.record.artifacts[Object.keys(receipt.record.artifacts).find(key=>key.endsWith('/wire.json'))];
  else if(mode==='raw-sha')receipt.embedded_artifacts[receipt.record.artifacts['connected.log'].path].sha256='0'.repeat(64);
  else if(mode==='log-sha')proof.log.sha256='0'.repeat(64);
  else if(mode==='failed-latest'){const bad=clone(proof);bad.id+='-failed';bad.status='failed';bad.green_eligible=false;bad.finished_at='2026-09-07T09:00:00Z';p.runtime.observations.push(bad);}
  updatePublishedHash(p);s.setRaw(p.raw);await assert.rejects(()=>s.api.loadRuntime(p.manifest,p.comparison));
});
test('真实processed到期后撤回绿色但保留其余通过和失败记录',async()=>{
  const s=setup(),p=publishedFixture(),proof=selectProcessed(p);s.setRaw(p.raw);
  const checked=await s.api.loadRuntime(p.manifest,p.comparison);publishIntoView(s,p,checked);s.setTime(Date.parse(proof.expires_at));
  const entry=p.comparison.entries.find(item=>item.id==='key-media-transcoding');s.api.view.runtimeStatus='stale';
  assert.ok(Array.from(s.api.filteredComparison(),row=>row.id).includes(entry.id));assert.ok(s.api.runtimeTotals().passed<77+publishedRXCount(p));
});

const pcmKind='python_unittest_sip_pcm_stream_e2e';
const pcmKeys=['key-ipc-pcm-turn-begin','key-ipc-pcm-turn-end','key-ipc-pcm-turn-interrupt','key-ipc-pcm-turn-status','key-ipc-pcm-push','key-api-pcm-stream'];
const sorted=value=>Array.isArray(value)?value.map(sorted):value&&typeof value==='object'?Object.fromEntries(Object.keys(value).sort().map(key=>[key,sorted(value[key])])):value;
function selectPCM(p){const proofs=p.runtime.observations.filter(item=>item.kind===pcmKind&&item.green_eligible);assert.equal(proofs.length,6,'实际发布必须保留六个PCM限定入口');return proofs;}
function pcmReceipt(p,proof){return p.runtime.receipts.find(item=>item.id===proof.execution_id);}
function rehashPCMReceipt(receipt){receipt.sha256=hash(wire(sorted(receipt.record)));}
test('新PCM真实发布仅六个私有ID且共同完整证据不会授予其他FS入口',async()=>{
  const s=setup(),p=publishedFixture(),proofs=selectPCM(p),sourceHash=hash(wire(sorted(p.runtime.runtime_source_snapshot)));
  assert.deepEqual(proofs.map(x=>x.comparison_id).sort(),pcmKeys.slice().sort());
  for(const proof of proofs){
    assert.equal(s.api.completePCMProof(proof,p.runtime,sourceHash,new Set([proof.execution_id])),true);
    for(const id of ['key-media-transcoding','key-media-capacity','key-esl-api','registration:api:mod_commands:version:1']){
      const fake=clone(proof);fake.comparison_id=id;fake.id=`${fake.execution_id}:${id}`;
      assert.equal(s.api.completePCMProof(fake,p.runtime,sourceHash,new Set([fake.execution_id])),false,id);
    }
  }
  s.setRaw(p.raw);const checked=await s.api.loadRuntime(p.manifest,p.comparison);publishIntoView(s,p,checked);
  for(const key of pcmKeys)assert.equal(checked.rows.get(key).green_eligible,true);
  assert.equal(s.api.runtimeTotals().passed,77+publishedRXCount(p));
});

for(const mode of ['kind','key','scope','missing-mode','duplicate-test','wrong-test','method-count','subtest-count','call-cycles','raw-count','boolean-audio','negative-responses','fraction-packets','sample-relation','zero-audio','source-hash','source-snapshot','missing-source-file','binding','future-finish','expiry-extension','execution-id','missing-receipt','receipt-sha','binary-sha','binary-role','binary-path','recorder-sha','runner-sha','log-sha','missing-raw','wrong-file-group','raw-sha','wrong-encoding','embedded-missing','duplicate-path','path-escape','invalid-base64','extra-embedded','old-build-ref','failed-latest'])test(`实际PCM损坏证据不能授绿：${mode}`,async()=>{
  const s=setup(),p=publishedFixture(),proof=selectPCM(p)[0],receipt=pcmReceipt(p,proof),record=receipt.record;
  if(mode==='kind')proof.kind='python_unittest_pcm_fake';
  else if(mode==='key')proof.comparison_id='key-esl-api';
  else if(mode==='scope')proof.scope='全部ASR/TTS/FreeSWITCH以及万路通过';
  else if(mode==='missing-mode')proof.test_names=proof.test_names.slice(0,11);
  else if(mode==='duplicate-test')proof.test_names[21]=proof.test_names[20];
  else if(mode==='wrong-test')proof.test_names[21]='connected:MediaPCMTurn.test_old_inner_suite';
  else if(mode==='method-count')proof.method_count=21;
  else if(mode==='subtest-count')proof.subtest_count=10;
  else if(mode==='call-cycles')proof.call_cycles=31;
  else if(mode==='raw-count')proof.raw_artifact_count=143;
  else if(mode==='boolean-audio')proof.audio_packets=true;
  else if(mode==='negative-responses')proof.stream_responses=-1;
  else if(mode==='fraction-packets')proof.raw_packets=2732.5;
  else if(mode==='sample-relation')proof.content_samples--;
  else if(mode==='zero-audio'){proof.audio_packets=0;proof.content_samples=0;}
  else if(mode==='source-hash')proof.source_snapshot_sha256='0'.repeat(64);
  else if(mode==='source-snapshot')p.runtime.runtime_source_snapshot['media/src/media/worker_pcm.rs']='0'.repeat(64);
  else if(mode==='missing-source-file')delete p.runtime.runtime_source_snapshot['tests/sip_pcm_stream_e2e.py'];
  else if(mode==='binding')proof.binding_contract='old_inner_42_fake_binding';
  else if(mode==='future-finish')proof.finished_at='2030-01-01T00:00:00Z';
  else if(mode==='expiry-extension')proof.expires_at='2030-01-01T00:00:00Z';
  else if(mode==='execution-id')proof.execution_id='pcm-unrecognized';
  else if(mode==='missing-receipt')p.runtime.receipts=p.runtime.receipts.filter(item=>item.id!==proof.execution_id);
  else if(mode==='receipt-sha')receipt.sha256='0'.repeat(64);
  else if(mode==='binary-sha'){record.binaries.media.sha256='0'.repeat(64);rehashPCMReceipt(receipt);}
  else if(mode==='binary-role'){delete record.binaries.control;rehashPCMReceipt(receipt);}
  else if(mode==='binary-path'){record.binaries.media.path='../old-media';rehashPCMReceipt(receipt);}
  else if(mode==='recorder-sha')proof.recorder_sha256='0'.repeat(64);
  else if(mode==='runner-sha')proof.runner_sha256='0'.repeat(64);
  else if(mode==='log-sha')proof.log.sha256='0'.repeat(64);
  else if(mode==='missing-raw'){delete record.artifacts[Object.keys(record.artifacts).find(key=>key.endsWith('/wire.json'))];rehashPCMReceipt(receipt);}
  else if(mode==='wrong-file-group'){const key=Object.keys(record.artifacts).find(key=>key.endsWith('/prompt.wav'));record.artifacts[key.replace('/prompt.wav','/unknown.wav')]=record.artifacts[key];delete record.artifacts[key];rehashPCMReceipt(receipt);}
  else if(mode==='raw-sha')receipt.embedded_artifacts[record.artifacts['connected.stderr.log'].path].sha256='0'.repeat(64);
  else if(mode==='wrong-encoding')receipt.embedded_artifacts[record.artifacts['runner.py'].path].encoding='raw';
  else if(mode==='embedded-missing')delete receipt.embedded_artifacts[record.artifacts['runner.py'].path];
  else if(mode==='duplicate-path'){record.artifacts['runner.py'].path=record.artifacts['recorder.py'].path;rehashPCMReceipt(receipt);}
  else if(mode==='path-escape'){record.artifacts['runner.py'].path='../runner.py';rehashPCMReceipt(receipt);}
  else if(mode==='invalid-base64')receipt.embedded_artifacts[record.artifacts['runner.py'].path].data='!invalid';
  else if(mode==='extra-embedded')receipt.embedded_artifacts['unreferenced']=clone(receipt.embedded_artifacts[record.artifacts['runner.py'].path]);
  else if(mode==='old-build-ref'){record.artifacts['runtime-build.json'].sha256='0'.repeat(64);receipt.embedded_artifacts[record.artifacts['runtime-build.json'].path].sha256='0'.repeat(64);rehashPCMReceipt(receipt);}
  else if(mode==='failed-latest'){const bad=clone(proof);bad.id+='-failed';bad.status='failed';bad.green_eligible=false;bad.finished_at='2026-09-07T09:00:00Z';p.runtime.observations.push(bad);}
  updatePublishedHash(p);s.setRaw(p.raw);await assert.rejects(()=>s.api.loadRuntime(p.manifest,p.comparison));
});
test('PCM轮询次数随运行变化，页面只验证有界计数关系而不冒充原始解压回放',async()=>{
  const s=setup(),p=publishedFixture();
  for(const proof of selectPCM(p)){proof.stream_responses+=2;proof.raw_packets+=4;}
  // 这仅测试界面元数据的取值范围；实际发布器会独立核对这些数字与压缩原帧，不能发布本变体。
  updatePublishedHash(p);s.setRaw(p.raw);const checked=await s.api.loadRuntime(p.manifest,p.comparison);
  for(const key of pcmKeys)assert.equal(checked.rows.get(key).green_eligible,true);
});
test('PCM期限到达撤回六入口且仍能显示证据过期',async()=>{
  const s=setup(),p=publishedFixture(),proof=selectPCM(p)[0];s.setRaw(p.raw);
  const checked=await s.api.loadRuntime(p.manifest,p.comparison);publishIntoView(s,p,checked);s.setTime(Date.parse(proof.expires_at));
  s.api.view.runtimeStatus='stale';const ids=new Set(Array.from(s.api.filteredComparison(),row=>row.id));
  for(const key of pcmKeys)assert.ok(ids.has(key),key);
  assert.ok(s.api.runtimeTotals().passed<=71+publishedRXCount(p));
});


const rxKind='go_rxs2_local_g711_composite_e2e';
const rxKeys=['key-ipc-rx-subscribe','key-ipc-rx-status','key-ipc-rx-unsubscribe','key-ipc-rx-stream','key-api-rx-sdk'];
let rxFixtureCache;
function rxFixture(){
  if(rxFixtureCache)return clone(rxFixtureCache);
  const published=publishedFixture();
  if(published.runtime.observations.some(item=>item.kind===rxKind&&item.green_eligible))return clone(rxFixtureCache=published);
  // 发布生成之前只读已经真实执行的归档/独立重放proof；不编造通过、不运行收据命令。
  const work=path.resolve(__dirname,'../../../work/voice-runtime-m2-rx');
  const proofPath=process.env.RUSTSWITCH_RX_UI_PROOF||path.join(work,'rx-verifier-02/receipt.json');
  const report=JSON.parse(fs.readFileSync(proofPath,'utf8'));
  assert.equal(report.passed,true,'RX UI夹具必须来自实际离线重放通过收据');
  const proofs=Object.values(report.proofs),execution=proofs[0].execution_id;
  assert.equal(proofs.length,5);assert.deepEqual(proofs.map(x=>x.comparison_id).sort(),rxKeys.slice().sort());
  const archive=process.env.RUSTSWITCH_RX_UI_ARCHIVE||path.join(work,'rx-gate-01');
  const record=JSON.parse(fs.readFileSync(path.join(archive,execution+'.receipt.json'),'utf8'));
  const embedded={},snapshot={};
  for(const[key,ref]of Object.entries(record.artifacts)){
    const raw=fs.readFileSync(path.join(archive,ref.path));assert.equal(hash(raw),ref.sha256,ref.path);
    embedded[ref.path]={encoding:'zlib+base64',bytes:raw.length,sha256:ref.sha256,data:require('node:zlib').deflateSync(raw,{level:9}).toString('base64')};
    if(key.endsWith('/$receipt'))for(const[name,digest]of Object.entries(JSON.parse(raw).source_before)){
      if(Object.hasOwn(snapshot,name))assert.equal(snapshot[name],digest,'历史源重叠必须一致');snapshot[name]=digest;
    }
  }
  const comparison={reference_version:'1.11.3',reference_commit:'a'.repeat(40),scope:'真实RX证据的独立页面夹具，未发布',entries:rxKeys.map(id=>({id,name:id,status:'internal_only',category:'key_media',differences:[],searchText:id})),summary:{by_status:{internal_only:5}}};
  const receipt={id:execution,kind:rxKind,path:execution+'.receipt.json',sha256:hash(wire(sorted(record))),record,embedded_artifacts:embedded};
  const runtime={version:'1.0.0',comparison_sha256:hash(wire(comparison)),upstream_runtime_executed:false,paired_status:'not_run',production_capacity_certified:false,evaluated_on:'2026-09-07',runtime_source_snapshot:snapshot,source_snapshot:snapshot,receipts:[receipt],observations:proofs,integrity_errors:[],entries:proofs.map(proof=>({comparison_id:proof.comparison_id,implementation_status:'internal_only',local_status:'passed',green_eligible:true,verification_id:proof.id,effective_expires_at:proof.expires_at,scope:proof.scope,reason:proof.reason})),summary:{green_entries:5,comparison_entries:5,by_local_status:{passed:5}}};
  const manifest={version:'1.13.0',documents:[{path:'api/comparison.json',sha256:runtime.comparison_sha256},{path:'api/runtime-verification.json',sha256:hash(wire(runtime))}]};
  return clone(rxFixtureCache={raw:wire(runtime),runtime,comparison,manifest});
}
function selectRX(p){const proofs=p.runtime.observations.filter(item=>item.kind===rxKind&&item.green_eligible);assert.equal(proofs.length,5);return proofs;}
function rxSourceBinding(s,p){const snapshot=p.runtime.runtime_source_snapshot,keys=Object.keys(snapshot).filter(s.api.rxBoundPath).sort();return {files:keys.length,sha256:hash(wire(Object.fromEntries(keys.map(key=>[key,snapshot[key]]))))};}
function rxReceipt(p,proof){return p.runtime.receipts.find(item=>item.id===proof.execution_id);}
function rehashRXReceipt(receipt){receipt.sha256=hash(wire(sorted(receipt.record)));}

test('真实RX五入口完整12组合和8960样本经发布SHA及独立绑定门禁',async()=>{
  const s=setup(),p=rxFixture(),proofs=selectRX(p),bound=rxSourceBinding(s,p);
  assert.deepEqual(proofs.map(x=>x.comparison_id).sort(),rxKeys.slice().sort());
  for(const proof of proofs){
    assert.equal(proof.source_files,bound.files);assert.equal(proof.source_snapshot_sha256,bound.sha256);
    assert.equal(s.api.completeRXProof(proof,p.runtime,bound,new Set([proof.execution_id])),true);
    assert.equal(s.api.completeRXProof(proof,p.runtime,bound,new Set()),false,'未经过canonical收据SHA不得通过');
    for(const key of ['key-esl-api','key-media-capacity','key-media-transcoding','key-api-pcm-stream']){
      const fake=clone(proof);fake.comparison_id=key;fake.id=fake.execution_id+':'+key;
      assert.equal(s.api.completeRXProof(fake,p.runtime,bound,new Set([fake.execution_id])),false,key);
    }
  }
  s.setRaw(p.raw);const checked=await s.api.loadRuntime(p.manifest,p.comparison);publishIntoView(s,p,checked);
  for(const key of rxKeys)assert.equal(checked.rows.get(key).green_eligible,true);
});

const rxMutations=['kind','key','scope','missing-case','duplicate-case','wrong-case','case-count','samples','samples-bool','source-hash','source-count','source-count-bool','binding','source-snapshot','missing-source','missing-vendor-asm','source-path','source-value','source-new-go','source-new-native','source-new-vendor','source-new-template','source-new-rust','source-new-cargo','future-finish','invalid-calendar','missing-timezone','negative-duration','expiry-extension','expiry-nanosecond','execution-id','missing-receipt','duplicate-receipt','receipt-sha','receipt-path','receipt-kind','receipt-id','receipt-version','receipt-time','binary-sha','binary-proof-sha','binary-role','extra-binary','binary-path','missing-artifact','wrong-file-group','mixed-pause-prefix','missing-original-receipt','original-receipt-zero','original-sha','wrong-encoding','missing-embedded','extra-embedded','duplicate-path','path-escape','path-backslash','invalid-base64','artifact-bytes','artifact-budget','archive-budget','compressed-budget','rust-build-receipt','log-sha','log-path','missing-peer-id','duplicate-peer-id','peer-scope','failed-latest'];
for(const mode of rxMutations)test(`RX真实归档损坏或错配不能授绿：${mode}`,async()=>{
  const s=setup(),p=rxFixture(),proof=selectRX(p)[0],receipt=rxReceipt(p,proof),record=receipt.record;
  const original=record.artifacts['pool/$receipt'];
  if(mode==='kind')proof.kind='go_rx_fake';
  else if(mode==='key')proof.comparison_id='key-media-capacity';
  else if(mode==='scope')proof.scope='ASR供应商、全部FreeSWITCH和万路通过';
  else if(mode==='missing-case')proof.test_names.pop();
  else if(mode==='duplicate-case')proof.test_names[11]=proof.test_names[10];
  else if(mode==='wrong-case')proof.test_names[11]='TestFake/payload_8_connected_true';
  else if(mode==='case-count')proof.evidence_cases=11;
  else if(mode==='samples')proof.decoded_samples=8959;
  else if(mode==='samples-bool')proof.decoded_samples=true;
  else if(mode==='source-hash')proof.source_snapshot_sha256='0'.repeat(64);
  else if(mode==='source-count')proof.source_files--;
  else if(mode==='source-count-bool')proof.source_files=true;
  else if(mode==='binding')proof.binding_contract='全部源码已验证';
  else if(mode==='source-snapshot')p.runtime.runtime_source_snapshot['control/internal/server/rx_stream.go']='0'.repeat(64);
  else if(mode==='missing-source')delete p.runtime.runtime_source_snapshot['control/internal/media/rx_stream.go'];
  else if(mode==='missing-vendor-asm'){for(const key of Object.keys(p.runtime.runtime_source_snapshot))if(key.startsWith('control/vendor/')&&key.endsWith('.s'))delete p.runtime.runtime_source_snapshot[key];}
  else if(mode==='source-path')p.runtime.runtime_source_snapshot['control/../escape.go']='0'.repeat(64);
  else if(mode==='source-value')p.runtime.runtime_source_snapshot['control/go.mod']=false;
  else if(mode.startsWith('source-new-'))p.runtime.runtime_source_snapshot[{'source-new-go':'control/internal/media/unrecorded.go','source-new-native':'control/internal/media/unrecorded.c','source-new-vendor':'control/vendor/new/modules.txt','source-new-template':'control/internal/server/fs_templates/new.xml','source-new-rust':'media/src/new.rs','source-new-cargo':'media/.cargo/new.toml'}[mode]]='0'.repeat(64);
  else if(mode==='future-finish')proof.finished_at='2030-01-01T00:00:00Z';
  else if(mode==='invalid-calendar')proof.started_at='2026-02-30T00:00:00Z';
  else if(mode==='missing-timezone')proof.started_at='2026-09-07T02:00:00';
  else if(mode==='negative-duration')proof.started_at=proof.expires_at;
  else if(mode==='expiry-extension')proof.expires_at='2030-01-01T00:00:00Z';
  else if(mode==='expiry-nanosecond')proof.expires_at=proof.expires_at.replace(/(\.\d{6})Z$/,'$1001Z');
  else if(mode==='execution-id')proof.execution_id='rx-unknown';
  else if(mode==='missing-receipt')p.runtime.receipts=p.runtime.receipts.filter(item=>item.id!==proof.execution_id);
  else if(mode==='duplicate-receipt')p.runtime.receipts.push(clone(receipt));
  else if(mode==='receipt-sha')receipt.sha256='0'.repeat(64);
  else if(mode==='receipt-path')receipt.path='../other.receipt.json';
  else if(mode==='receipt-kind')receipt.kind='go_rx_fake';
  else if(mode==='receipt-id')record.id='rx-'+ '0'.repeat(32);
  else if(mode==='receipt-version')record.schema_version='9.0.0';
  else if(mode==='receipt-time')record.finished_at=proof.started_at;
  else if(mode==='binary-sha')record.binaries.media.sha256='0'.repeat(64);
  else if(mode==='binary-proof-sha')proof.verified_binary_sha256.media='0'.repeat(64);
  else if(mode==='binary-role')delete record.binaries.pool_test;
  else if(mode==='extra-binary')record.binaries.control=clone(record.binaries.server_test);
  else if(mode==='binary-path')record.binaries.media.path='../media';
  else if(mode==='missing-artifact')delete record.artifacts['server/wire/payload_8_connected_true/wire.jsonl'];
  else if(mode==='wrong-file-group'){record.artifacts['server/wire/payload_8_connected_true/unknown.jsonl']=record.artifacts['server/wire/payload_8_connected_true/wire.jsonl'];delete record.artifacts['server/wire/payload_8_connected_true/wire.jsonl'];}
  else if(mode==='mixed-pause-prefix'){const key=Object.keys(record.artifacts).find(key=>key.startsWith('pause/cases/'));record.artifacts[key.replace('pause/cases/','pause/wire/')]=record.artifacts[key];delete record.artifacts[key];}
  else if(mode==='missing-original-receipt')delete record.artifacts['pause/$receipt'];
  else if(mode==='original-receipt-zero')receipt.embedded_artifacts[original.path].bytes=0;
  else if(mode==='original-sha')receipt.embedded_artifacts[original.path].sha256='0'.repeat(64);
  else if(mode==='wrong-encoding')receipt.embedded_artifacts[original.path].encoding='raw';
  else if(mode==='missing-embedded')delete receipt.embedded_artifacts[original.path];
  else if(mode==='extra-embedded')receipt.embedded_artifacts.unreferenced=clone(receipt.embedded_artifacts[original.path]);
  else if(mode==='duplicate-path')record.artifacts['pause/$receipt'].path=original.path;
  else if(mode==='path-escape')original.path='../pool/receipt.json';
  else if(mode==='path-backslash')original.path='pool\\receipt.json';
  else if(mode==='invalid-base64')receipt.embedded_artifacts[original.path].data='!bad!';
  else if(mode==='artifact-bytes')receipt.embedded_artifacts[original.path].bytes=true;
  else if(mode==='artifact-budget')receipt.embedded_artifacts[original.path].bytes=33*1024*1024;
  else if(mode==='archive-budget'){for(const item of Object.values(receipt.embedded_artifacts))item.bytes=1024*1024;}
  else if(mode==='compressed-budget'){const shared='A'.repeat(1024*1024);for(const item of Object.values(receipt.embedded_artifacts))item.data=shared;}
  else if(mode==='rust-build-receipt'){record.artifacts['server/rust-build-receipt.json'].sha256='0'.repeat(64);receipt.embedded_artifacts[record.artifacts['server/rust-build-receipt.json'].path].sha256='0'.repeat(64);}
  else if(mode==='log-sha')proof.log.sha256='0'.repeat(64);
  else if(mode==='log-path')proof.log.path=record.artifacts['pause/tests.log'].path;
  else if(mode==='missing-peer-id')p.runtime.observations=p.runtime.observations.filter(item=>item.id!==selectRX(p)[1].id);
  else if(mode==='duplicate-peer-id'){const sibling=selectRX(p)[1];sibling.comparison_id=proof.comparison_id;sibling.id+='-duplicate';}
  else if(mode==='peer-scope')selectRX(p)[1].scope='另一范围';
  else if(mode==='failed-latest'){const bad=clone(proof);bad.id+='-failed';bad.execution_id='rx-'+ '0'.repeat(32);bad.status='failed';bad.green_eligible=false;bad.finished_at='2026-09-07T09:00:00Z';p.runtime.observations.push(bad);}
  if(mode!=='receipt-sha')rehashRXReceipt(receipt);
  updatePublishedHash(p);s.setRaw(p.raw);await assert.rejects(()=>s.api.loadRuntime(p.manifest,p.comparison));
});

test('RX绑定规则覆盖所有Go本地编译文件和vendor，同时不冒充Python/native/网页已绑定',()=>{
  const s=setup();
  for(const suffix of ['go','s','S','c','h','cc','cpp','cxx','m','mm','f','F','f90','for','syso','swig','swigcxx'])assert.equal(s.api.rxBoundPath('control/internal/media/new.'+suffix),true,suffix);
  for(const file of ['control/go.mod','control/go.sum','config/local.json','control/vendor/modules.txt','control/internal/server/fs_templates/new.xml','media/build.rs','media/Cargo.lock','media/Cargo.toml','media/src/new.rs','media/.cargo/config.toml'])assert.equal(s.api.rxBoundPath(file),true,file);
  for(const file of ['tests/new.py','native/new.c','control/internal/server/web/docs.js','control/internal/server/web/example.go','control/internal/server/doc_assets/native/include/a.h','docs/api/new.md','config/production.json','tools/rx_verification.py'])assert.equal(s.api.rxBoundPath(file),false,file);
});
test('RX范围外文档改变不会伪称旧源码失配，新增绑定编译源不能被忽略',async()=>{
  const s=setup(),p=rxFixture();
  // 仅检验RX门禁，所以剥离其他依赖完整源码快照的PCM/processed证据，保留RX五条及其真实收据。
  const ids=new Set(rxKeys);p.comparison.entries=p.comparison.entries.filter(item=>ids.has(item.id));p.runtime.entries=p.runtime.entries.filter(item=>ids.has(item.comparison_id));p.runtime.observations=selectRX(p);p.runtime.receipts=p.runtime.receipts.filter(item=>item.kind===rxKind);p.runtime.summary.green_entries=5;
  p.runtime.runtime_source_snapshot['control/internal/server/web/docs.js']='0'.repeat(64);p.runtime.runtime_source_snapshot['tests/excluded.py']='0'.repeat(64);
  p.runtime.comparison_sha256=hash(wire(p.comparison));p.manifest.documents.find(item=>item.path==='api/comparison.json').sha256=p.runtime.comparison_sha256;
  updatePublishedHash(p);s.setRaw(p.raw);await s.api.loadRuntime(p.manifest,p.comparison);
});
test('RX证据复核期限到达同时撤回五入口，保留原条目与过期原因',async()=>{
  const s=setup(),p=rxFixture(),proof=selectRX(p)[0];s.setRaw(p.raw);
  const checked=await s.api.loadRuntime(p.manifest,p.comparison);publishIntoView(s,p,checked);s.setTime(Date.parse(proof.expires_at));s.api.view.runtimeStatus='stale';
  const ids=new Set(Array.from(s.api.filteredComparison(),row=>row.id));for(const key of rxKeys)assert.ok(ids.has(key),key);
});

(async()=>{for(const[name,fn]of cases){await fn();console.log('PASS '+name);}console.log(JSON.stringify({tests:cases.length,failed:0}));})().catch(error=>{console.error(error);process.exitCode=1;});
