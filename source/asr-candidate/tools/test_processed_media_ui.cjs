// 验证管理页面的真实模式、缺失数据与故障降级；VM 只模拟 HTTP，不冒充真实媒体或浏览器验收。
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),assert=require('node:assert/strict');
const base=path.join(__dirname,'../control/internal/server/web');
const app=fs.readFileSync(path.join(base,'app.js'),'utf8');
const source=fs.readFileSync(path.join(base,'tests.js'),'utf8').replace('return {mount,leave};','return {mount,leave,view,validateDraft,processedSampleMarkup,processedReportRows,reportChecksPassed,processedDiagnosticsMarkup};');
const sandbox={window:{},document:{},setTimeout,clearTimeout};vm.createContext(sandbox);vm.runInContext(source,sandbox);
const tests=sandbox.window.RustSwitchTests;
tests.view.snapshot={capabilities:{media_processing_modes:['relay','g711']}};
const draft={concurrency:500,cps:100,duration_seconds:10,payload:0,media_processing:'g711'};
assert.equal(tests.validateDraft('pressure',draft).media_processing,'g711');
assert.equal(tests.validateDraft('pressure',{...draft,media_processing:undefined}).media_processing,'relay');
assert.throws(()=>tests.validateDraft('pressure',{...draft,payload:9}));
tests.view.snapshot={capabilities:{}};assert.throws(()=>tests.validateDraft('phone',draft));
// 即使报告声称passed，没有完整处理证据也不能显示转码通过。
const completed={status:'completed',cleanup:{complete:true},media_processing:'g711',payload:0,concurrency:1};
assert.equal(tests.reportChecksPassed(completed,{result:{passed:true,media_processing:'g711',processed_media:{}}}),false);
assert.equal(tests.reportChecksPassed({...completed,media_processing:'relay'},{result:{passed:true,media_processing:'g711'}}),false);
assert.equal(tests.reportChecksPassed({status:'completed',cleanup:{complete:true}},{result:{passed:true}}),true);
const empty=tests.processedSampleMarkup({media_processing:'g711'},null);
assert.match(empty,/解码 \/ 编码帧<\/span><strong>— \/ —/);assert.doesNotMatch(empty,/>0 \/ 0</);
const actual=tests.processedSampleMarkup({media_processing:'g711'},{processing:{processed_decoded_frames:321,processed_encoded_frames:333,processed_playout_expired:2,processed_send_deadline_misses:1}});
assert.match(actual,/>321 \/ 333</);assert.match(actual,/>— \/ 2 \/ 1</);
assert.equal(tests.processedSampleMarkup({media_processing:'relay'},{}),'');
assert.ok(tests.processedReportRows({processed_media:{}}).every(row=>!String(row[1]).includes('undefined')));
assert.ok(tests.processedReportRows({}).every(row=>!String(row[1]).includes('undefined')));

assert.equal(tests.processedDiagnosticsMarkup({media_processing:'relay'}),'');
assert.match(tests.processedDiagnosticsMarkup({media_processing:'g711'}),/未提供有界首错诊断/);
const diagnosticMarkup=tests.processedDiagnosticsMarkup({media_processing:'g711',processed_diagnostics:{version:'g711-observation-v1',observed_content_valid_packets:100,timestamp_error_content_valid_packets:5,first_receive_errors:[{reason:'<script>bad</script>'}]}});
assert.match(diagnosticMarkup,/100 \/ 5/);assert.doesNotMatch(diagnosticMarkup,/<script>/);assert.match(diagnosticMarkup,/&lt;script&gt;/);assert.doesNotMatch(diagnosticMarkup,/undefined/);


const helpers=['escapeHTML','number','badge','panel'].map(name=>app.split('\n').find(line=>line.startsWith('function '+name+'('))).join('\n');
const root={innerHTML:''},state={page:'codecs',codecsLoading:false,codecsError:''};
const page={state,Date,document:{getElementById:()=>root},request:async()=>{throw Error('network lost');}};
vm.createContext(page);vm.runInContext(helpers+'\n'+app.slice(app.indexOf('function renderCodecs()')),page);
const value=()=>({runtime_media:{processing:'g711',enabled:true,available:true,ready:true,observed_at:new Date().toISOString(),healthy_workers:2,capable_workers:2,expected_workers:2,scope:'G711 runtime'},live_codec_profiles:[{payload:0},{payload:8}],codec_profiles:[{payload:0,name:'PCMU',sample_rate:8000,rtp_clock_rate:8000},{payload:9,name:'G722',sample_rate:16000,rtp_clock_rate:8000}],native_audio:{codecs:[]}});
state.codecs=value();page.renderCodecs();assert.match(root.innerHTML,/badge good">处理图已就绪/);assert.match(root.innerHTML,/当前模式不支持/);assert.match(root.innerHTML,/2 \/ 2 \/ 2/);
// 单腿能力必须有独立且一致的当前证据，不能由桥就绪推断，更不能把未知补成零。
assert.match(root.innerHTML,/当前版本未提供本地能力/);
state.codecs.runtime_media={...state.codecs.runtime_media,local_capability:'processed_g711_local_v1',local_available:true,local_ready:true,local_capable_workers:2};
page.renderCodecs();assert.match(root.innerHTML,/badge good">本地接听处理已就绪/);
for(const patch of [{local_capable_workers:1},{local_available:false},{enabled:false},{local_capability:'processed_g711_v1'},{local_ready:undefined},{processing:'relay'},{ready:false},{available:false}]) {
 state.codecs.runtime_media={...value().runtime_media,local_capability:'processed_g711_local_v1',local_available:true,local_ready:true,local_capable_workers:2,...patch};
 page.renderCodecs();assert.doesNotMatch(root.innerHTML,/badge good">本地接听处理已就绪/);
}
state.codecs.runtime_media={...value().runtime_media,local_capability:'processed_g711_local_v1',local_available:true,local_ready:true,local_capable_workers:2};
state.codecs.runtime_media.observed_at=new Date(Date.now()-11000).toISOString();page.renderCodecs();assert.match(root.innerHTML,/状态未更新/);assert.doesNotMatch(root.innerHTML,/badge good">处理图已就绪/);assert.doesNotMatch(root.innerHTML,/badge good">本地接听处理已就绪/);
// 主服务本地统计不能借用测试任务样本，也不能只汇总正常分片而隐去失联分片。
const localStats={processed_active_calls:1,processed_decoded_frames:3,processed_plc_frames:0,processed_local_active_calls:1,processed_local_consumed_frames:3,processed_local_consumed_samples:480,processed_local_nonzero_frames:2,processed_local_energy_max:100};
const localWorker=id=>({id,pid:100+id,generation:1,healthy:true,admission:{sampled_at:new Date().toISOString()},stats:{...localStats,processed_local_energy_max:100+id}});
const localStatus={workers:[localWorker(0),localWorker(1)]},localConfig={processing:'g711',workers:2};
assert.match(page.localConsumptionPanel(localStatus,localConfig,true),/实际消费帧 \/ 样本<\/dt><dd>6 \/ 960/);
assert.match(page.localConsumptionPanel(localStatus,localConfig,true),/平方和最大值<\/dt><dd>101/);
for(const change of [x=>x.workers.pop(),x=>x.workers[1].healthy=false,x=>x.workers[1].id=0,x=>delete x.workers[1].stats.processed_local_nonzero_frames,x=>x.workers[1].admission.sampled_at=new Date(Date.now()-4000).toISOString(),x=>x.workers[1].stats.processed_local_energy_max=171798691841,x=>x.workers={},x=>x.workers[1]=null,x=>x.workers[1].stats.processed_active_calls=0,x=>x.workers[1].stats.processed_decoded_frames=0,x=>x.workers[1].stats.processed_local_nonzero_frames=0,x=>x.workers[1].generation=1.5]) {
 const copy=JSON.parse(JSON.stringify(localStatus));change(copy);assert.match(page.localConsumptionPanel(copy,localConfig,true),/实际消费帧 \/ 样本<\/dt><dd>— \/ —/);
}
assert.match(page.localConsumptionPanel(localStatus,localConfig,false),/保持未知/);
assert.equal(page.localConsumptionPanel(localStatus,{processing:'relay',workers:2},true),'');
(async()=>{
 state.codecs=value();await page.pollCodecs();assert.match(root.innerHTML,/读取失败：network lost/);assert.doesNotMatch(root.innerHTML,/badge good">处理图已就绪/);
 page.request=async()=>({...value(),runtime_media:{...value().runtime_media,processing:'relay',enabled:false,ready:false}});await page.pollCodecs();assert.equal(state.codecsError,'');assert.match(root.innerHTML,/处理图未启用/);assert.doesNotMatch(root.innerHTML,/badge good">处理图已就绪/);
 state.codecs={codec_profiles:[]};page.renderCodecs();assert.match(root.innerHTML,/有效媒体模式：未提供/);assert.match(root.innerHTML,/— \/ — \/ —/);
 console.log(JSON.stringify({passed:true,scope:'UI模式绑定、内容观测缺失、进程故障和过期状态不显示假绿色'}));
})().catch(error=>{console.error(error);process.exitCode=1;});
