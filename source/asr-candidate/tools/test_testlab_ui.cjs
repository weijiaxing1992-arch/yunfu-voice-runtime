// 测试真实页面的轮询状态机；模拟网络顺序，不替代 SIP/媒体或浏览器验收。
// 运行：node tools/test_testlab_ui.cjs。仅在沙箱中暴露内部状态，交付页面不增加调试入口。
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync(path.join(__dirname, '../control/internal/server/web/tests.js'), 'utf8')
  .replace('return {mount,leave};', 'return {mount,leave,refresh,view,activeRun};');
const task = (id, status = 'completed') => ({id, status, report_available:false, mode:'phone', concurrency:1});
const snapshot = (current = null, history = []) => ({enabled:true, capabilities:{}, current, history});
const failure = (status, message) => Object.assign(new Error(message), {status});

function setup() {
  const notices = {innerHTML:''};
  const sandbox = {window:{}, document:{hidden:false, getElementById:id=>id==='tests-availability'?notices:null,
    querySelectorAll:()=>[]}, setTimeout:()=>1, clearTimeout:()=>{}, sessionStorage:{removeItem(){},setItem(){}}};
  vm.createContext(sandbox); vm.runInContext(source, sandbox);
  const api = sandbox.window.RustSwitchTests, requests = [];
  api.view.active = true;
  api.view.request = async (url, options) => {requests.push([url,options]); throw new Error('未配置的请求');};
  return {api, requests, notices};
}

const cases = [];
const test = (name, fn) => cases.push([name, fn]);

for (const explicit of [false,true]) test(`服务重启后清除旧${explicit?'手动':'自动'}选择及运行锁`, async () => {
  const {api,notices} = setup(), urls = [];
  Object.assign(api.view, {selectedId:'old', explicitSelection:explicit, run:task('old','running')});
  api.view.reports.set('old', {result:{passed:true}});
  api.view.request = async url => {urls.push(url); return snapshot();};
  await api.refresh(); await api.refresh();
  assert.deepEqual(urls, ['/v1/tests','/v1/tests']);
  assert.equal(api.view.online,true); assert.equal(api.view.selectedId,'');
  assert.equal(api.activeRun(),null); assert.equal(api.view.reports.size,0);
  assert.match(notices.innerHTML,/记录已过期或服务已重启/);
  assert.doesNotMatch(notices.innerHTML,/读取失败/);
});

test('历史淘汰后转向后台正在运行的任务', async () => {
  const {api} = setup(), live = task('live','running'), urls = [];
  Object.assign(api.view,{selectedId:'expired',explicitSelection:true});
  api.view.request = async url => {urls.push(url); return url==='/v1/tests'?snapshot(live):live;};
  await api.refresh();
  assert.deepEqual(urls,['/v1/tests','/v1/tests/live']);
  assert.equal(api.view.run.id,'live'); assert.equal(api.view.explicitSelection,false);
});

test('已保留的手动历史选择不会被正在运行的任务抢走', async () => {
  const {api} = setup(), selected=task('old'), live=task('live','running');
  Object.assign(api.view,{selectedId:'old',explicitSelection:true});
  api.view.request=async url=>url==='/v1/tests'?snapshot(live,[selected]):selected;
  await api.refresh();
  assert.equal(api.view.run.id,'old'); assert.equal(api.activeRun().id,'live');
});

for (const status of [404,410]) test(`列表与详情之间记录过期 ${status} 不误报离线`, async () => {
  const {api,notices}=setup(), live=task('race','running');
  api.view.request=async url=>{if(url==='/v1/tests')return snapshot(live);throw failure(status,'记录已过期');};
  await api.refresh();
  assert.equal(api.view.online,true); assert.equal(api.activeRun(),null);
  assert.equal(api.view.selectedId,''); assert.doesNotMatch(notices.innerHTML,/读取失败/);
});

test('详情503保留旧结果且下轮恢复，不把整个服务标记为离线', async () => {
  const {api,notices}=setup(), old=task('one'), updated={...old,elapsed_seconds:8};
  Object.assign(api.view,{selectedId:'one',run:old});
  api.view.request=async url=>{if(url==='/v1/tests')return snapshot(null,[old]);throw failure(503,'繁忙');};
  await api.refresh();
  assert.equal(api.view.online,true); assert.equal(api.view.run,old);
  assert.match(notices.innerHTML,/任务详情暂未更新/);
  api.view.request=async url=>url==='/v1/tests'?snapshot(null,[updated]):updated;
  await api.refresh(); assert.equal(api.view.run.elapsed_seconds,8); assert.equal(api.view.detailError,'');
});

test('列表网络失败保留任务和报告，恢复后自动解除离线', async () => {
  const {api,notices}=setup(), old=task('one');
  Object.assign(api.view,{selectedId:'one',run:old,snapshot:snapshot(null,[old])});
  api.view.request=async ()=>{throw new Error('连接中断');};
  await api.refresh(); assert.equal(api.view.online,false); assert.equal(api.view.run,old);
  assert.match(notices.innerHTML,/读取失败/);
  api.view.request=async url=>url==='/v1/tests'?snapshot(null,[old]):old;
  await api.refresh(); assert.equal(api.view.online,true); assert.equal(api.view.readError,'');
});

test('过期恢复不会清除表单或结果未知的创建幂等键', async () => {
  const {api}=setup(), pending={request_id:'original',mode:'phone',concurrency:1};
  Object.assign(api.view,{selectedId:'expired',uncertain:pending});
  api.view.drafts.pressure.concurrency='1234';
  api.view.request=async ()=>snapshot();
  await api.refresh(); assert.equal(api.view.uncertain,pending);
  assert.equal(api.view.drafts.pressure.concurrency,'1234');
});

test('离开页面后迟到的404不会覆盖其他页面状态', async () => {
  const {api}=setup(), live=task('one'); let reject, reached;
  const requested=new Promise(resolve=>{reached=resolve;});
  api.view.request=async url=>url==='/v1/tests'?snapshot(null,[live]):new Promise((_,r)=>{reject=r;reached();});
  const pending=api.refresh(); await requested;
  api.leave(); reject(failure(404,'已过期')); await pending;
  assert.equal(api.view.selectionNotice,''); assert.equal(api.view.readError,'');
});

test('报告暂时失败只标记报告，重试成功后清除错误', async () => {
  const {api}=setup(), run={...task('one'),report_available:true}; let fail=true;
  api.view.request=async url=>{
    if(url==='/v1/tests')return snapshot(null,[run]);
    if(url.endsWith('/report')){if(fail)throw failure(503,'报告繁忙');return {run,result:{passed:false}};}
    return run;
  };
  await api.refresh(); assert.equal(api.view.online,true); assert.equal(api.view.reportError,'报告繁忙');
  fail=false; await api.refresh(); assert.equal(api.view.reportError,'');
  assert.equal(api.view.reports.get('one').result.passed,false);
});

(async()=>{
  for(const [name,fn] of cases){await fn(); process.stdout.write(`PASS ${name}\n`);}
  console.log(JSON.stringify({tests:cases.length,failed:0,scope:'测试页面轮询恢复，独立于电话兼容和容量验收'}));
})().catch(error=>{console.error(error);process.exitCode=1;});
