// 真实页面函数的离线呈现测试；不代替浏览器或实际识别服务验收。
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync(path.join(__dirname,'../control/internal/server/web/app.js'),'utf8');
const body=source.slice(source.indexOf('function asrPanel('),source.indexOf('function controllerPanel('));
const helpers=['escapeHTML','number','badge','panel','healthRow'].map(name=>source.split('\n').find(line=>line.startsWith('function '+name+'('))).join('\n');
const context={icon:()=>'',Number};vm.createContext(context);vm.runInContext(helpers+'\n'+body,context);
assert.match(context.asrPanel(undefined),/当前服务版本未提供/);
assert.doesNotMatch(context.asrPanel(undefined),/占用 \/ 上限/);
assert.match(context.asrPanel({enabled:false}),/本次部署未启用/);
const html=context.asrPanel({enabled:true,stopping:false,max_streams:64,slots:8,starting:2,active:3,cleanup_pending:3,rejected_capacity:7,rejected_uuid:1});
assert.match(html,/8 \/ 64/);assert.match(html,/2 \/ 3/);assert.match(html,/等待资源清理/);assert.match(html,/7 \/ 1/);assert.match(html,/不代表识别成功数/);
assert.match(context.asrPanel({enabled:true,stopping:true}),/正在停止/);
assert.match(source,/\$\{asrPanel\(data\.asr_stream\)\}/);
console.log(JSON.stringify({checks:10,failed:0,scope:'真实页面函数离线呈现，未知/禁用/有效/停止及实际总览调用接线'}));
