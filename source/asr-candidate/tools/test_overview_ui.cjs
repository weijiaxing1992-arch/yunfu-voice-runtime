// 总览压测采样状态机与呈现负例；只模拟HTTP读取，不替代真实SIP、媒体或浏览器验收。
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),assert=require('node:assert/strict');
const app=fs.readFileSync(path.join(__dirname,'../control/internal/server/web/app.js'),'utf8');
const section=app.slice(app.indexOf('/* 压测总览独立读'),app.indexOf('/* 主服务总览保留'));
const helpers=['escapeHTML','number','badge','panel'].map(name=>app.split('\n').find(line=>line.startsWith('function '+name+'('))).join('\n');
function setup(){
 const state={page:'overview',online:true,status:{active_calls:0},history:[{active:0}],configDraft:{keep:'draft'}};
 const sandbox={state,Date,Number,encodeURIComponent,renderOverview(){},request:async()=>{throw Error('unexpected request');}};
 vm.createContext(sandbox);vm.runInContext(helpers+'\n'+section+'\nthis.view=overviewTests;',sandbox);
 return sandbox;
}
const sample=(active=500,age=0)=>({at:new Date(Date.now()-age).toISOString(),active_calls:active,established_calls:active-1,worker_count:2,healthy_workers:2,rx_packets:12345,tx_packets:12340});
const run=(id='one',status='running',samples=[sample()])=>({id,status,phase:status==='running'?'media':'finished',mode:'pressure',concurrency:500,samples,finished_at:status==='running'?'':new Date().toISOString(),progress:{established_calls:500}});
const snapshot=(current=null,history=[])=>({enabled:true,current,history});
const cases=[],test=(name,fn)=>cases.push([name,fn]);

test('运行中的隔离并发来自current样本，主服务0和编辑草稿保持独立',async()=>{
 const s=setup(),task=run();s.request=async url=>{assert.equal(url,'/v1/tests');return snapshot(task);};
 await s.pollOverviewTests();const html=s.renderOverviewTests();
 assert.match(html,/实际活跃通话<\/span><strong>500/);assert.match(html,/来源：\/v1\/tests.current/);
 assert.equal(s.state.status.active_calls,0);assert.deepEqual(s.state.history,[{active:0}]);assert.deepEqual(s.state.configDraft,{keep:'draft'});
 assert.match(html,/媒体接收 \/ 发送包/);assert.match(html,/健康媒体工作进程/);
});
test('预检尚无样本不使用目标或累计建立数伪造实际并发0正常',async()=>{
 const s=setup(),task={...run('preflight','starting',[]),phase:'preflight'};s.request=async()=>snapshot(task);
 await s.pollOverviewTests();const html=s.renderOverviewTests();
 assert.match(html,/实际活跃通话<\/span><strong>—/);assert.match(html,/尚无子实例采样/);assert.doesNotMatch(html,/实际活跃通话<\/span><strong>500/);
});
test('样本超过10秒或未来时钟异常均提示，终态明确为历史值',()=>{
 const s=setup();s.view.loaded=true;s.view.run=run('old','running',[sample(500,11000)]);
 assert.match(s.renderOverviewTests(),/采样超过 10 秒未更新/);
 s.view.run=run('future','running',[sample(500,-6000)]);assert.match(s.renderOverviewTests(),/采样时刻异常/);
 s.view.run=run('ended','completed',[sample(0,60000)]);assert.match(s.renderOverviewTests(),/历史值，不是实时负载/);assert.doesNotMatch(s.renderOverviewTests(),/采样超过 10 秒未更新/);
});
test('新任务无样本清空旧任务峰值和趋势，不混接两个任务历史',async()=>{
 const s=setup();s.view.run=run('old','completed',[sample(5000)]);s.request=async()=>snapshot(run('new','starting',[]));
 await s.pollOverviewTests();assert.equal(s.view.run.id,'new');assert.equal(s.overviewTestMeasurement(s.view.run).activePeak,null);
 assert.doesNotMatch(s.renderOverviewTests(),/5,000/);assert.match(s.renderOverviewTests(),/等待至少两个/);
});
test('列表读取失败保留并标注旧样本，但主服务仍保持在线；恢复自动清除错误',async()=>{
 const s=setup(),old=run();s.view.run=old;s.request=async()=>{throw Error('network lost');};await s.pollOverviewTests();
 assert.equal(s.view.run,old);assert.equal(s.state.online,true);assert.match(s.renderOverviewTests(),/不能视作实时状态/);
 s.request=async()=>snapshot(run('new'));await s.pollOverviewTests();assert.equal(s.view.readError,'');assert.equal(s.view.run.id,'new');
});
test('服务重启或历史淘汰后清除旧任务而不是持续显示旧并发',async()=>{
 const s=setup();s.view.run=run();s.request=async()=>snapshot();await s.pollOverviewTests();
 assert.equal(s.view.run,null);assert.match(s.renderOverviewTests(),/已过期或服务已重启/);
});
test('最近终态详情只成功读取一次，同时任务列表仍持续检查',async()=>{
 const s=setup(),summary=run('ended','completed',[]),detail={...summary,samples:[sample(0)]},urls=[];
 s.request=async url=>{urls.push(url);return url==='/v1/tests'?snapshot(null,[summary]):detail;};
 await s.pollOverviewTests();await s.pollOverviewTests();assert.deepEqual(urls,['/v1/tests','/v1/tests/ended','/v1/tests']);
 assert.equal(s.view.run.samples.length,1);assert.match(s.renderOverviewTests(),/最近测试/);
});
for(const status of [404,410])test(`详情${status}不恢复过期任务或影响主服务在线`,async()=>{
 const s=setup();s.request=async url=>{if(url==='/v1/tests')return snapshot(null,[run('ended','completed',[])]);throw Object.assign(Error('gone'),{status});};
 await s.pollOverviewTests();assert.equal(s.view.run,null);assert.equal(s.state.online,true);assert.match(s.renderOverviewTests(),/详情已过期/);
});
test('详情临时失败仅保留同一任务采样，下次重新读取恢复',async()=>{
 const s=setup(),old=run('ended','completed');s.view.run=old;
 s.request=async url=>{if(url==='/v1/tests')return snapshot(null,[{...old,samples:[]}]);throw Object.assign(Error('busy'),{status:503});};
 await s.pollOverviewTests();assert.equal(s.view.run,old);assert.match(s.renderOverviewTests(),/任务详情暂未更新/);
 s.request=async url=>url==='/v1/tests'?snapshot(null,[old]):old;await s.pollOverviewTests();assert.equal(s.view.detailError,'');
});
test('离开再返回使迟到旧响应失效，未完成的详情不能被错误缓存',async()=>{
 const s=setup(),old=run('old','completed',[]);let resolve,reached;const issued=new Promise(r=>reached=r);
 s.request=async url=>url==='/v1/tests'?snapshot(null,[old]):new Promise(r=>{resolve=r;reached();});
 const pending=s.pollOverviewTests();await issued;s.state.page='tests';s.view.epoch++;resolve({...old,samples:[sample(5000)]});await pending;
 assert.equal(s.view.detailID,'');s.state.page='overview';s.view.epoch++;
 const newer=run('new','running',[sample(1000)]);s.request=async()=>snapshot(newer);await s.pollOverviewTests();assert.equal(s.view.run.id,'new');
 assert.doesNotMatch(s.renderOverviewTests(),/5,000/);
});
test('一轮未完成不重入或堆积轮询，离开总览不发额外读取',async()=>{
 const s=setup();let resolve,calls=0;s.request=async()=>{calls++;return new Promise(r=>resolve=r);};
 const first=s.pollOverviewTests();await s.pollOverviewTests();assert.equal(calls,1);resolve(snapshot());await first;
 s.state.page='tests';await s.pollOverviewTests();assert.equal(calls,1);
});
test('当前任务和最近终态摘要同时显示，服务端字符串不能注入HTML',async()=>{
 const s=setup();s.request=async()=>snapshot({...run('<img src=x onerror=bad>'),error:'<script>bad</script>'},[run('last','failed')]);
 await s.pollOverviewTests();const html=s.renderOverviewTests();assert.match(html,/最近结束的测试/);assert.match(html,/last/);assert.match(html,/&lt;script&gt;/);assert.doesNotMatch(html,/<script>/);assert.doesNotMatch(html,/<img /);
});
test('保留样本峰值不是全程峰值，缺失CPU和控制快照仍显示未知',()=>{
 const s=setup();s.view.run=run();const html=s.renderOverviewTests();assert.match(html,/最多 300/);assert.match(html,/测试控制 CPU 使用<\/dt><dd>—/);assert.match(html,/零值不证明无丢包/);assert.match(html,/任务完成不自动等于测试通过/);
});

(async()=>{for(const [name,fn]of cases){await fn();console.log('PASS '+name);}console.log(JSON.stringify({tests:cases.length,failed:0,scope:'总览独立测试采样与错误恢复，不等于电话容量验收'}));})().catch(error=>{console.error(error);process.exitCode=1;});
