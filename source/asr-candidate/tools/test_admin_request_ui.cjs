// 验证管理请求在明确未执行的令牌拒绝后恢复；不在不确定网络结果下重复写入。
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),assert=require('node:assert/strict');
const app=fs.readFileSync(path.join(__dirname,'../control/internal/server/web/app.js'),'utf8');
const source=app.slice(app.indexOf('async function request('),app.indexOf('/* 非阻塞中文反馈'));
const response=(status,body,refresh='')=>({ok:status>=200&&status<300,status,text:async()=>JSON.stringify(body),headers:{get:()=>refresh}});
function setup(responses){
  const requests=[],state={csrf:'old',configDraft:{unsaved:true},configRevision:7};
  const sandbox={state,AbortController,setTimeout,clearTimeout,fetch:async(url,options)=>{
    requests.push({url,options});const value=responses.shift();if(value instanceof Error)throw value;
    if(!value)throw new Error('意外的额外网络请求');return value;
  }};
  vm.createContext(sandbox);vm.runInContext(source,sandbox);
  return {...sandbox,requests};
}
const cases=[];const test=(name,fn)=>cases.push([name,fn]);
test('服务重启后保留原幂等键及草稿，只刷新令牌并重试一次',async()=>{
  const s=setup([response(403,{error:'token'},'required'),response(200,{csrf_token:'new',revision:99}),response(202,{id:'same'})]);
  const body={request_id:'fixed',mode:'phone',concurrency:1};
  const result=await s.request('/v1/tests',{method:'POST',body});
  assert.equal(result.id,'same');assert.deepEqual(s.requests.map(r=>r.url),['/v1/tests','/v1/config','/v1/tests']);
  assert.equal(s.requests[0].options.body,s.requests[2].options.body);
  assert.equal(s.requests[0].options.headers['X-RustSwitch-CSRF'],'old');
  assert.equal(s.requests[2].options.headers['X-RustSwitch-CSRF'],'new');
  assert.equal(s.state.configDraft.unsaved,true);assert.equal(s.state.configRevision,7);
});
for(const status of [403,409,500,503])test(`普通${status}不重复写入`,async()=>{
  const s=setup([response(status,{error:'denied'})]);
  await assert.rejects(()=>s.request('/v1/tests',{method:'POST',body:{}}),e=>e.status===status);
  assert.equal(s.requests.length,1);
});
test('未知网络失败不刷新令牌或重放请求',async()=>{
  const s=setup([new Error('connection lost')]);
  await assert.rejects(()=>s.request('/v1/tests',{method:'POST',body:{}}),/connection lost/);
  assert.equal(s.requests.length,1);
});
test('令牌再次失效立即返回403，禁止无界重试',async()=>{
  const s=setup([response(403,{error:'token'},'required'),response(200,{csrf_token:'new'}),response(403,{error:'again'},'required')]);
  await assert.rejects(()=>s.request('/v1/tests',{method:'POST',body:{}}),e=>e.status===403);
  assert.equal(s.requests.length,3);
});
test('新配置缺少令牌时保留原始403',async()=>{
  const s=setup([response(403,{error:'token'},'required'),response(200,{})]);
  await assert.rejects(()=>s.request('/v1/tests',{method:'POST',body:{}}),e=>e.status===403);
  assert.equal(s.requests.length,2);assert.equal(s.state.csrf,'old');
});
(async()=>{for(const [name,fn]of cases){await fn();console.log('PASS '+name);}console.log(JSON.stringify({tests:cases.length,failed:0}));})().catch(e=>{console.error(e);process.exitCode=1;});
