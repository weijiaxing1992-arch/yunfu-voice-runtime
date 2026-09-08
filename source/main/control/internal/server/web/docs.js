'use strict';

/* 文档阅读器独立管理状态，不调用管理写接口，也不参与每三秒运行轮询。 */
window.RustSwitchDocs = (() => {
  /* cache 保存只读响应；generation/readGeneration 阻止旧异步请求覆盖新页面。 */
  const view = {page:'', routeKey:'', generation:0, readGeneration:0, manifest:null, api:null, comparison:null, ledger:null, ledgerIndex:new Map(), ledgerError:'', runtime:null, runtimeIndex:new Map(), runtimeProofs:new Map(), runtimeError:'', progress:null, progressError:'', conformance:null, conformanceError:'', runtimeStatus:'all', expiryTimer:0, cache:new Map(), files:new Map(), operations:[], search:'', category:'all', status:'all', pageNumber:1, pageSize:12, tab:'operations', operation:'', document:'', schema:'', anchor:'', entry:'', codes:new Map(), schemas:new Map(), persistentCodes:new Set(), serial:0, debounce:0};
  /* 服务端枚举映射为中文；未知状态保留原值，不能被猜测为已实现。 */
  const statuses = {implemented:['自有接口已实现','neutral'],partial:['部分实现','warning'],export_only:['仅配置导出','neutral'],not_implemented:['未实现','warning'],internal_only:['内部接口','neutral'],not_verified:['完整契约待验收','warning']};
  /* 运行验证颜色独立于实现状态；只有绑定当前发布源码的真实限定范围通过才使用绿色。 */
  const runtimeStatuses = {passed:['本地验证通过（限定范围）','good'],failed:['本地验证失败','warning'],not_run:['本地验证未执行','neutral'],stale:['证据已过期或源码已变化','warning'],missing_evidence:['证据缺失或校验失败','warning'],unbound:['历史运行未绑定当前源码','neutral']};
  /* 对应关系说明接口用途与报文兼容边界，未知枚举或自由中文原样显示。 */
  const relationships = {
    direct_comparison:'直接对照',
    analogous_not_wire_compatible:'用途相近但报文不兼容',
    export_artifact_only:'仅配置产物',
    no_compatible_entrypoint:'无兼容入口',
    internal_contract_only:'仅内部契约',
    acceptance_requirement_only:'仅验收定义'
  };
  /* 自有接口实现状态与 FreeSWITCH 报文兼容性分别展示，不自动互相推导。 */
  function relationshipLabel(value) { return relationships[value] || value || '未提供'; }
  /* 对照分类只翻译展示标签；查询值与原始下载仍使用服务端机器名。 */
  const categories = {
    acceptance_definition:'验收用例定义', fs_api:'FreeSWITCH 命令 API',
    fs_application:'拨号计划应用', fs_channel_variable:'通道变量',
    fs_chat_application:'聊天应用', fs_conference_subcommand:'会议子命令',
    fs_event:'事件类型', fs_json_api:'JSON API', fs_module:'模块目录',
    fs_native_function:'原生函数目录', fs_sofia_subcommand:'Sofia 子命令',
    fs_vanilla_parameter:'官方配置参数', key_c_abi:'C/C++ 适配 ABI',
    key_cli:'命令行接口', key_config:'配置与保存', key_esl:'ESL 与事件套接字',
    key_guard:'峰值保护', key_http:'HTTP 管理接口', key_ipc:'媒体 IPC 协议',
    key_media:'媒体与编解码', key_sip:'SIP 信令'
  };
  /* 未知分类仍显示原值，避免把新目录归到错误的业务领域。 */
  function categoryLabel(value) { return categories[value] || value || '未分类'; }
  /* 仅接受整行严格空锚点；其他 HTML、额外属性及不合规 ID 均继续转义。 */
  const emptyAnchorPattern = /^<a id="([A-Za-z0-9_-]{1,160})"><\/a>$/;
  const methods = ['get','put','post','delete','patch','head','options','trace'];
  /* 所有插入 HTML 的接口、配置、Markdown 字符串统一转义。 */
  function escape(value) { return String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
  /* 任意结构以可读 JSON 展示；不把未采集值写成成功或零。 */
  function text(value) { return value == null ? '未提供' : typeof value === 'string' ? value : JSON.stringify(value,null,2); }
  /* 数字仅用于目录实际计数，未知计数保留占位。 */
  function count(value) { return Number.isFinite(Number(value)) && value != null ? Number(value).toLocaleString('zh-CN') : '—'; }
  /* 状态颜色与文字同时表达结果，数据不能注入 class。 */
  function statusBadge(status) { const [label,color]=statuses[status]||[status||'未提供','neutral'];return `<span class="badge ${color}">${escape(label)}</span>`; }
  /* hash 只允许两个固定文档路由，参数交给 URLSearchParams 编码。 */
  function href(page, parameters={}) { const query=new URLSearchParams();Object.entries(parameters).forEach(([key,value])=>{if(value!=null&&value!==''&&value!=='all')query.set(key,String(value));});return `#${page}${query.size?'?'+query:''}`; }
  /* 读取 URL 中的页面内状态，以便刷新或复制链接后回到同一内容。 */
  function readRoute(page) { const query=new URLSearchParams(location.hash.split('?').slice(1).join('?'));view.page=page;view.search=query.get('search')||'';view.category=query.get('category')||'all';view.status=query.get('status')||'all';view.runtimeStatus=query.get('runtime')||'all';view.pageNumber=Math.max(1,Number.parseInt(query.get('page')||'1',10)||1);view.operation=query.get('operation')||'';view.document=query.get('document')||'';view.schema=query.get('schema')||'';view.anchor=query.get('anchor')||'';view.entry=query.get('entry')||'';view.tab=view.document?'documents':view.schema?'schemas':query.get('view')==='documents'?'documents':query.get('view')==='schemas'?'schemas':'operations'; }
  /* 搜索和分页更新地址而不触发全页面重载，保留输入焦点及滚动位置。 */
  function remember() { const parameters={search:view.search};if(view.page==='fs-comparison')Object.assign(parameters,{category:view.category,status:view.status,runtime:view.runtimeStatus,page:view.pageNumber===1?'':view.pageNumber,entry:view.entry});else Object.assign(parameters,{view:view.tab==='operations'?'':view.tab,operation:view.tab==='operations'?view.operation:'',document:view.tab==='documents'?view.document:'',schema:view.tab==='schemas'?view.schema:'',anchor:view.anchor});history.replaceState(null,'',href(view.page,parameters));view.routeKey=location.hash; }
  /* 只读请求带超时；失败会清理 promise 缓存，让重试真正发起新请求。 */
  async function fetchRead(path, asText=false) { const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),15000);try{const response=await fetch(path,{method:'GET',credentials:'same-origin',cache:'no-store',headers:{Accept:asText?'text/plain':'application/json'},signal:controller.signal});if(!response.ok)throw new Error(`读取失败（HTTP ${response.status}）。请检查服务后重试。`);return asText?await response.text():await response.json();}catch(error){if(error.name==='AbortError')throw new Error('文档读取超时，请重试。');throw error;}finally{clearTimeout(timer);} }
  /* 同一个目录并发打开时共享读取，成功数据只在本次浏览器会话缓存。 */
  function load(key,path) { if(!view.cache.has(key)){const promise=fetchRead(path).catch(error=>{view.cache.delete(key);throw error;});view.cache.set(key,promise);}return view.cache.get(key); }
  /* 文档路径必须来自 manifest；未知路径不能拿来读取其他服务端文件。 */
  function documentEntry(path) { return view.manifest?.documents?.find(item=>item.path===path); }
  /* 当前报告从已发布版本及白名单选择，避免新版本页面仍跳到旧轮次。 */
  function versionedReport(stem,fallback) { const version=/^(\d+)\.(\d+)(?:\.\d+)?$/.exec(view.manifest?.version||''); const path=version?`${stem}-v${version[1]}.${version[2]}.md`:''; return documentEntry(path)?path:fallback; }
  /* 来源链接仅允许明确列出的官方只读域和固定 FreeSWITCH 仓库。 */
  function officialURL(raw) { try{const url=new URL(raw);if(!['http:','https:'].includes(url.protocol)||url.username||url.password)return '';const host=url.hostname.toLowerCase();const allowed=['developer.signalwire.com','signalwire.com','www.signalwire.com','freeswitch.org','www.freeswitch.org','rfc-editor.org','www.rfc-editor.org','datatracker.ietf.org','docs.kernel.org','spec.openapis.org','opus-codec.org','downloads.xiph.org'];if(allowed.includes(host)||(host==='github.com'&&/^\/(?:signalwire\/freeswitch|freeswitch\/spandsp)(?:\/|$)/.test(url.pathname)))return url.href;}catch{}return ''; }
  /* 相对 Markdown 链接先按当前文档路径解析，再与本地索引白名单核对。 */
  function documentURL(raw,current=view.document) { if(/^[a-z][a-z\d+.-]*:/i.test(raw)||raw.startsWith('//'))return officialURL(raw);try{const url=new URL(raw,'https://rustswitch-doc.local/'+current);if(url.origin!=='https://rustswitch-doc.local')return '';const path=decodeURIComponent(url.pathname.slice(1));if(documentEntry(path))return href('api-docs',{document:path,anchor:decodeURIComponent(url.hash.slice(1))});}catch{}return ''; }
  /* 下载入口只引用白名单中的服务端文档路径。 */
  function fileURL(path) { return documentEntry(path)?'/v1/docs/file?path='+encodeURIComponent(path):''; }
  /* 复制反馈写入纯文本提示，不把失败包装成成功。 */
  function feedback(message,error=false) { const node=document.createElement('div');node.className='toast'+(error?' error':'');node.textContent=message;document.getElementById('toast-container').append(node);setTimeout(()=>node.remove(),error?8000:4000); }
  /* 代码按钮绑定本地字符串，不在事件处理器里插入代码或自动执行示例。 */
  function code(value,label='JSON',wrap=false) { const id='code-'+(++view.serial),raw=value===null?'null':text(value);view.codes.set(id,raw);return `<div class="docs-code"><div class="docs-code-header"><span>${escape(label)}</span><button class="button button-secondary" data-doc-action="copy" data-doc-code="${id}" type="button">复制</button></div><pre${wrap?' class="wrap"':''}><code>${escape(raw)}</code></pre></div>`; }
  /* 加载与错误只渲染在当前文档区域，旧页面响应不能替换其他管理页面。 */
  function loading(label='正在读取文档') { return `<div class="loading-state"><span class="spinner"></span><h2>${escape(label)}</h2><p>从当前 RustSwitch 服务读取内嵌文档。</p></div>`; }
  function failure(error,action='retry') { return `<div class="docs-load-error" role="alert"><strong>文档暂时无法读取</strong><p>${escape(error.message)}</p><button class="button button-secondary" data-doc-action="${action}">重新读取</button></div>`; }
  /* 本地 JSON Pointer 解引用保留循环标识；禁止联网获取外部 $ref。 */
  function resolve(ref) { if(typeof ref!=='string'||!ref.startsWith('#/'))return null;try{return ref.slice(2).split('/').map(part=>decodeURIComponent(part).replace(/~1/g,'/').replace(/~0/g,'~')).reduce((item,key)=>Object.hasOwn(item||{},key)?item[key]:undefined,view.api)??null;}catch{return null;} }
  /* 非 Schema 对象可沿引用读取，遇到循环或未知引用时仍显示原文。 */
  function dereference(object,seen=new Set()) { if(!object?.$ref||seen.has(object.$ref))return object;const target=resolve(object.$ref);if(!target)return object;const next=new Set(seen);next.add(object.$ref);return {...dereference(target,next),...Object.fromEntries(Object.entries(object).filter(([key])=>key!=='$ref'))}; }
  /* schema 显示每层名称、类型、必填、约束、引用及组合分支；循环使用显式入口。 */
  function schemaView(schema,label='结构',trail=new Set(),depth=0,required=false) {
    if(typeof schema==='boolean')return `<div class="docs-schema"><code>${escape(label)}</code>：${schema?'允许任意值':'不接受任何值'}</div>`;
    if(!schema||typeof schema!=='object')return `<p>${escape(label)}：未提供 Schema</p>`;
    const type=Array.isArray(schema.type)?schema.type.join(' | '):schema.type||(schema.properties?'object':schema.items?'array':schema.$ref?'引用':'未指定类型');
    const labelHTML=`<code>${escape(label)}</code><span class="docs-schema-tags"><span>${escape(type)}</span>${required?'<span>必填</span>':''}${schema.nullable?'<span>可为 null</span>':''}${schema.readOnly?'<span>只读</span>':''}${schema.writeOnly?'<span>只写</span>':''}${schema.deprecated?'<span>已弃用</span>':''}</span>`;
    if(depth>12){const key='schema-'+(++view.serial);view.schemas.set(key,schema);return `<div class="docs-schema">${labelHTML}<p>深层结构按需展开；完整原始 Schema 可在下方查看。</p><button class="button button-secondary button-small" data-doc-action="schema-expand" data-doc-schema="${key}">展开此层</button>${code(schema,'完整深层 Schema')}</div>`;}
    let body=schema.description?`<p>${escape(schema.description)}</p>`:'';
    if(schema.$ref){const target=resolve(schema.$ref);body+=`<p>引用：<code>${escape(schema.$ref)}</code></p>`;if(trail.has(schema.$ref)){const name=schema.$ref.startsWith('#/components/schemas/')?schema.$ref.slice(21).replace(/~1/g,'/').replace(/~0/g,'~'):'';body+=`<p>递归引用：再次出现的同一结构在此结束展开。${name?`<a href="${escape(href('api-docs',{schema:name}))}">查看该 Schema 定义</a>`:''}</p>`;}else if(target!==null){const next=new Set(trail);next.add(schema.$ref);body+=schemaView(target,'引用目标',next,depth+1);}else body+='<p>该引用不在当前 OpenAPI 内，保留引用原文，不加载外部资源。</p>';}
    const childKeys=['properties','patternProperties','$defs','definitions','dependentSchemas'];
    childKeys.forEach(key=>{if(schema[key]&&typeof schema[key]==='object'){body+=`<p><strong>${escape(key)}</strong></p>`;Object.entries(schema[key]).forEach(([name,item])=>{body+=schemaView(item,name,new Set(trail),depth+1,key==='properties'&&schema.required?.includes(name));});}});
    ['allOf','anyOf','oneOf','prefixItems'].forEach(key=>{if(Array.isArray(schema[key])){body+=`<p><strong>${escape(key)}</strong> · ${schema[key].length} 个分支</p>`;schema[key].forEach((item,index)=>body+=schemaView(item,`${key} [${index+1}]`,new Set(trail),depth+1));}});
    ['items','additionalProperties','unevaluatedProperties','unevaluatedItems','contains','not','if','then','else','propertyNames','contentSchema'].forEach(key=>{if(Object.hasOwn(schema,key))body+=Array.isArray(schema[key])?schema[key].map((item,index)=>schemaView(item,`${key} [${index}]`,new Set(trail),depth+1)).join(''):schemaView(schema[key],key,new Set(trail),depth+1);});
    const structural=new Set([...childKeys,'allOf','anyOf','oneOf','prefixItems','items','additionalProperties','unevaluatedProperties','unevaluatedItems','contains','not','if','then','else','propertyNames','contentSchema','$ref','description']);
    const constraints=Object.fromEntries(Object.entries(schema).filter(([key])=>!structural.has(key)));
    if(Object.keys(constraints).length)body+=`<pre class="docs-constraints">${escape(JSON.stringify(constraints,null,2))}</pre>`;
    return `<details class="docs-schema" ${depth<2?'open':''}><summary>${labelHTML}</summary>${body||'<p>没有额外约束。</p>'}</details>`;
  }
  /* 数值示例同时考虑开闭区间、整数及倍数约束，不把严格大于零的比例生成为零。 */
  function numericExample(schema,type) {
    const lowerExclusive=typeof schema.exclusiveMinimum==='number'?schema.exclusiveMinimum:schema.exclusiveMinimum===true?schema.minimum:undefined;
    const upperExclusive=typeof schema.exclusiveMaximum==='number'?schema.exclusiveMaximum:schema.exclusiveMaximum===true?schema.maximum:undefined;
    const lower=Math.max(schema.minimum??-Infinity,lowerExclusive??-Infinity),upper=Math.min(schema.maximum??Infinity,upperExclusive??Infinity);
    const openLower=lowerExclusive!=null&&lowerExclusive===lower,openUpper=upperExclusive!=null&&upperExclusive===upper;
    const valid=value=>Number.isFinite(value)&&(value>lower||(!openLower&&value===lower))&&(value<upper||(!openUpper&&value===upper))&&(type!=='integer'||Number.isInteger(value));
    let value=0;
    if(type==='integer'){const min=Number.isFinite(lower)?Math.ceil(lower)+(openLower&&Number.isInteger(lower)?1:0):-Infinity,max=Number.isFinite(upper)?Math.floor(upper)-(openUpper&&Number.isInteger(upper)?1:0):Infinity;value=Math.min(max,Math.max(min,0));}
    else if(!valid(value)){if(Number.isFinite(lower)&&Number.isFinite(upper))value=lower/2+upper/2;else if(Number.isFinite(lower))value=openLower?lower+Math.max(1,Math.abs(lower)*0.1):lower;else if(Number.isFinite(upper))value=openUpper?upper-Math.max(1,Math.abs(upper)*0.1):upper;}
    if(schema.multipleOf>0){const multiple=schema.multipleOf;const rounded=Number((Math.ceil(value/multiple)*multiple).toPrecision(15));const candidates=[rounded,Number((rounded+multiple).toPrecision(15)),Number((rounded-multiple).toPrecision(15))];value=candidates.find(valid)??NaN;}
    return valid(value)?value:null;
  }
  /* 显式 example/examples、const/default/enum 优先；24 层上限与引用路径共同限制递归。 */
  function exampleFor(schema,trail=new Set(),depth=0) {
    if(schema==null||schema===true)return {};if(schema===false)return null;
    if(Object.hasOwn(schema,'example'))return schema.example;
    if(Array.isArray(schema.examples)&&schema.examples.length)return schema.examples[0];
    if(Object.hasOwn(schema,'const'))return schema.const;
    if(Object.hasOwn(schema,'default'))return schema.default;
    if(schema.enum?.length)return schema.enum[0];
    if(depth>24)return null;
    if(schema.$ref){if(trail.has(schema.$ref))return null;const next=new Set(trail);next.add(schema.$ref);const target=resolve(schema.$ref);if(!target||typeof target!=='object')return exampleFor(target,next,depth+1);return exampleFor({...target,...Object.fromEntries(Object.entries(schema).filter(([key])=>key!=='$ref'))},next,depth+1);}
    if(schema.allOf){return schema.allOf.reduce((result,item)=>{const sample=exampleFor(item,trail,depth+1);return sample&&typeof sample==='object'&&!Array.isArray(sample)?{...result,...sample}:result;},{});}
    if(schema.oneOf||schema.anyOf)return exampleFor((schema.oneOf||schema.anyOf)[0],trail,depth+1);
    const type=Array.isArray(schema.type)?schema.type.find(item=>item!=='null'):schema.type;
    if(type==='object'||schema.properties)return Object.fromEntries(Object.entries(schema.properties||{}).filter(([,item])=>!item.readOnly).map(([name,item])=>[name,exampleFor(item,trail,depth+1)]));
    if(type==='array'||schema.items)return [exampleFor(schema.items,trail,depth+1)];
    if(type==='integer'||type==='number')return numericExample(schema,type);
    if(type==='boolean')return false;
    if(type==='null')return null;
    return '填写实际值';
  }
  /* 媒体类型可同时列出多个命名示例；externalValue 仅显示允许的来源链接。 */
  function examples(media,mediaType='application/json') { const items=[];if(Object.hasOwn(media||{},'example'))items.push(['文档示例',media.example]);Object.entries(media?.examples||{}).forEach(([name,raw])=>{const item=dereference(raw);if(Object.hasOwn(item||{},'value'))items.push([item.summary||name,item.value]);else if(item?.externalValue)items.push([name,`外部示例地址：${item.externalValue}`]);});return items.length?items.map(([label,value])=>code(mediaType.includes('json')&&typeof value==='string'?JSON.stringify(value):value,label)).join(''):media?.schema?code(exampleFor(media.schema),'根据 Schema 生成的示例模板；需填写实际值'):''; }
  /* 请求体、响应体的每一种 Content-Type 都完整展示 Schema、约束和示例。 */
  function contentView(content) { return Object.entries(content||{}).map(([type,media])=>`<section><h4>${escape(type)}</h4>${media.schema?schemaView(media.schema,'body'):''}${media.encoding?code(media.encoding,'字段编码规则'):''}${examples(media,type)}</section>`).join('')||'<p>未定义响应/请求正文。</p>'; }
  /* 参数同时保留位置、必填、序列化方式与全部原始字段，避免遗漏约束。 */
  function parameterView(raw) { const p=dereference(raw);return `<section class="docs-response"><h4><code>${escape(p.name||p.$ref||'参数')}</code> ${p.required?'<span class="badge warning">必填</span>':''}</h4><p>${escape(p.in||'header')} · ${escape(p.description||'')}</p>${p.schema?schemaView(p.schema,p.name||'参数值',new Set(),0,p.required):''}${p.content?contentView(p.content):''}${Object.hasOwn(p,'example')?code(p.example,'参数示例'):''}${p.examples?code(p.examples,'参数命名示例'):''}<details class="docs-detail"><summary>全部参数字段与序列化规则</summary>${code(p,'Parameter Object')}</details></section>`; }
  /* 将同名同位置的 operation 参数覆盖 path 参数，遵循 OpenAPI 参数合并方式。 */
  function parametersFor(operation) { const map=new Map();[...(operation.pathItem.parameters||[]),...(operation.value.parameters||[])].forEach(raw=>{const item=dereference(raw);map.set(`${item.in}:${item.name}`,raw);});return [...map.values()]; }
  /* shell 单引号转义只用于展示可复制 cURL 文本，不会触发命令执行。 */
  function shellQuote(value) { return "'"+String(value).replace(/'/g,"'\"'\"'")+"'"; }
  /* cURL 示例固定本机地址；Host 由客户端按 URL 生成，写接口保留 token/版本占位。 */
  function curlExample(operation) { let path=operation.path;const query=new URLSearchParams(),headers=[];parametersFor(operation).forEach(raw=>{const p=dereference(raw),value=p.example??exampleFor(p.schema);if(p.in==='path')path=path.replace('{'+p.name+'}',encodeURIComponent(typeof value==='object'?'实际值':value));else if(p.in==='query'&&p.required)query.set(p.name,typeof value==='object'?'实际值':String(value));else if(p.in==='header'&&p.required&&p.name.toLowerCase()!=='host')headers.push(`${p.name}: ${p.name.toLowerCase()==='x-rustswitch-csrf'?'TOKEN_FROM_GET_CONFIG':typeof value==='object'?'实际值':value}`);});const body=dereference(operation.value.requestBody),type=Object.keys(body?.content||{})[0],media=body?.content?.[type];const parts=[`curl -X ${operation.method.toUpperCase()} ${shellQuote('http://127.0.0.1:9080'+path+(query.size?'?'+query:''))}`];if(type)headers.push('Content-Type: '+type);if(!['get','head','options'].includes(operation.method)&&!headers.some(item=>item.toLowerCase().startsWith('x-rustswitch-csrf:')))headers.push('X-RustSwitch-CSRF: TOKEN_FROM_GET_CONFIG');headers.forEach(header=>parts.push('  -H '+shellQuote(header)));if(media){const first=Object.values(media.examples||{})[0];const sample=media.example??dereference(first)?.value??exampleFor(media.schema);parts.push('  --data-raw '+shellQuote(type?.includes('json')?JSON.stringify(sample,null,2):typeof sample==='string'?sample:JSON.stringify(sample,null,2)));}return parts.join(' \\\n'); }
  /* 按已发布 operationId 绑定语义条目；自有测试接口定位测试边界，不假造原版映射。 */
  const operationComparisonTargets = {
    getHealth:{entry:'key-http-health'},
    getReadiness:{entry:'key-http-readiness'},
    getStatus:{entry:'key-http-status'},
    getMetrics:{entry:'key-http-metrics'},
    getConfig:{entry:'key-http-config-read'},
    putConfig:{entry:'key-http-config-write'},
    getGuard:{entry:'key-guard-capacity'},
    putGuard:{entry:'key-guard-capacity'},
    drain:{entry:'key-guard-drain-resume'},
    resume:{entry:'key-guard-drain-resume'},
    getFSConfig:{entry:'key-config-runtime-xml'},
    putFSConfig:{entry:'key-config-parameter-edit'},
    getFSFile:{entry:'key-config-raw-xml'},
    putFSFile:{entry:'key-config-raw-xml'},
    getFSExport:{entry:'key-config-export'},
    /* 五个文档发布接口无 FreeSWITCH 同名入口，直接定位其自有契约，不伪造对照。 */
    getDocs:{document:'api/http-reference.md',anchor:'616-get-v1docs-读取文档清单'},
    getOpenAPI:{document:'api/http-reference.md',anchor:'617-get-v1docsopenapijson-下载openapi-31描述'},
    getComparison:{document:'api/http-reference.md',anchor:'618-get-v1docscomparisonjson-下载完整接口对照'},
    getDocument:{document:'api/http-reference.md',anchor:'619-get-v1docsfile-读取允许清单内原始文档'},
    getDocsExport:{document:'api/http-reference.md',anchor:'620-get-v1docsexportzip-下载完整文档zip'},
    getTests:{document:'api/testing-reference.md',anchor:'api-与调用顺序'},
    startTest:{document:'api/testing-reference.md',anchor:'api-与调用顺序'},
    getTest:{document:'api/testing-reference.md',anchor:'结果判读'},
    stopTest:{document:'api/testing-reference.md',anchor:'api-与调用顺序'},
    getTestReport:{document:'api/testing-reference.md',anchor:'freeswitch-对应说明'}
  };
  /* 精确 entry 由对照页计算页码并展开；文档目标仍须经过 manifest 白名单。 */
  function operationComparisonLink(operationId) { const target=operationComparisonTargets[operationId];if(!target)return '';if(target.entry)return `<a href="${escape(href('fs-comparison',{entry:target.entry}))}">查看对应语义对照</a>`;if(documentEntry(target.document))return `<a href="${escape(href('api-docs',{document:target.document,anchor:target.anchor}))}">查看自有文档接口说明</a>`;return '<span class="docs-source-muted">对应说明未列入当前文档清单。</span>'; }
  /* 扩展字段显示原 FreeSWITCH 接口与差异，不把相似管理动作写成可替换接口。 */
  function fsRelationship(value,operationId) { if(!value)return '';const source=officialURL(value.reference_url);return `<div class="docs-note"><strong>FreeSWITCH 对应关系</strong><p>${escape(value.interface||'未提供直接映射')}</p><p>关系：${escape(relationshipLabel(value.relationship))} · 状态：${statusBadge(value.status)}</p><p>${escape(value.difference||'未提供差异说明')}</p>${source?`<a href="${escape(source)}" target="_blank" rel="noopener noreferrer">查看官方来源</a>`:''} ${operationComparisonLink(operationId)}</div>`; }
  /* 选中一个 HTTP 操作才渲染完整契约，包含所有响应状态和引用后的字段。 */
  function operationView(operation) {
    if(!operation)return '<div class="empty-state"><h2>没有匹配的 HTTP 接口</h2><p>调整搜索关键词，或打开章节文档。</p></div>';
    const item=operation.value,body=dereference(item.requestBody),security=item.security??view.api.security??[];
    return `<div class="docs-operation-title"><span class="docs-method ${operation.method}">${operation.method.toUpperCase()}</span><code>${escape(operation.path)}</code></div><h2 style="margin-top:16px">${escape(item.summary||item.operationId||'接口详情')}</h2><p>${escape(item.description||'')}</p><div class="docs-meta"><span>operationId：${escape(item.operationId||'未提供')}</span>${item.deprecated?'<span class="badge warning">已弃用</span>':''}<button class="button button-secondary button-small" data-doc-action="copy-link">复制定位链接</button></div>${fsRelationship(item['x-freeswitch'],item.operationId)}<h3>请求参数</h3>${parametersFor(operation).map(parameterView).join('')||'<p>没有声明请求参数。</p>'}<h3>请求正文</h3>${body?`<p>${body.required?'正文必填。':'正文可选。'} ${escape(body.description||'')}</p>${contentView(body.content)}<details class="docs-detail"><summary>完整 RequestBody Object</summary>${code(item.requestBody,'原始请求体定义')}</details>`:'<p>没有请求正文。</p>'}<h3>请求示例</h3><p>以下示例仅供复制，不会自动请求服务。填写真实参数，写操作须先读取 token 和最新版本。</p>${code(curlExample(operation),'cURL 示例模板')}${Array.isArray(item['x-codeSamples'])?item['x-codeSamples'].map(sample=>code(sample.source,sample.lang||'文档代码示例')).join(''):''}<h3>所有响应状态</h3>${Object.entries(item.responses||{}).map(([status,raw])=>{const response=dereference(raw);return `<details class="docs-response" open><summary><code>${escape(status)}</code><span>${escape(response.description||'未提供响应说明')}</span></summary>${response.headers?`<h4>响应头</h4>${Object.entries(response.headers).map(([name,header])=>parameterView({...dereference(header),name,in:'header'})).join('')}`:''}${contentView(response.content)}${response.links?code(response.links,'响应 Links'):''}<details class="docs-detail"><summary>完整响应定义</summary>${code(raw,'Response Object')}</details></details>`;}).join('')||'<p>没有声明响应。</p>'}<h3>认证与其他约束</h3>${code({security,securitySchemes:view.api.components?.securitySchemes||{},servers:item.servers??operation.pathItem.servers??view.api.servers??[]},'Security / Servers')}${item.callbacks?code(item.callbacks,'Callbacks 定义'):''}<details class="docs-detail"><summary>完整原始 Operation Object</summary>${code(item,'Operation Object：包含全部扩展与约束')}</details>`;
  }
  /* 搜索递归收集被引用结构的字段名称和语义；同一引用只展开一次，循环有界。 */
  function referenceText(value,visited=new Set()) { if(!value||typeof value!=='object')return '';let result='';for(const [key,item]of Object.entries(value)){if(key==='$ref'&&typeof item==='string'&&!visited.has(item)){visited.add(item);const target=resolve(item);if(target)result+=' '+JSON.stringify(target)+referenceText(target,visited);}else if(item&&typeof item==='object')result+=referenceText(item,visited);}return result; }
  /* 路径条目引用和公共参数保留在操作索引中，搜索覆盖操作及递归参数字段。 */
  function buildOperations() { const list=[];Object.entries(view.api.paths||{}).forEach(([path,raw])=>{const pathItem=dereference(raw);methods.forEach(method=>{if(pathItem[method]){const value=pathItem[method];list.push({id:value.operationId||method.toUpperCase()+' '+path,path,method,value,pathItem,search:(JSON.stringify({path,method,...value,...{'path-parameters':pathItem.parameters}})+referenceText(value)+referenceText(pathItem.parameters)).toLowerCase()});}});});view.operations=list; }
  /* 顶部固定只读操作不会输出可执行脚本，下载保持同源。 */
  function downloads() { return '<a class="button button-secondary" href="/v1/docs/openapi.json" download="rustswitch-openapi.json">下载 OpenAPI</a><a class="button button-primary" href="/v1/docs/export.zip" download="rustswitch-documents.zip">下载完整文档包</a>'; }
  /* 接口目录、章节目录和数据结构入口共用搜索，当前选择仍由 URL 定位。 */
  function apiDirectory() { const query=view.search.toLowerCase();let content='';if(view.tab==='operations'){const list=view.operations.filter(item=>item.search.includes(query));if(!list.some(item=>item.id===view.operation))view.operation=list[0]?.id||'';content=`<h3>HTTP 操作 · ${list.length}</h3>`+list.map(item=>`<a href="${escape(href('api-docs',{operation:item.id,search:view.search}))}" class="${item.id===view.operation?'active':''}"><span class="docs-method ${item.method}">${item.method.toUpperCase()}</span><span>${escape(item.path)}</span><small>${escape(item.value.summary||item.id)}</small></a>`).join('');}else if(view.tab==='schemas'){const list=Object.entries(view.api.components?.schemas||{}).filter(([name,schema])=>(name+' '+JSON.stringify(schema)).toLowerCase().includes(query));if(!list.some(([name])=>name===view.schema))view.schema=list[0]?.[0]||'';content=`<h3>数据结构 · ${list.length}</h3>`+list.map(([name])=>`<a href="${escape(href('api-docs',{schema:name,search:view.search}))}" class="${name===view.schema?'active':''}">${escape(name)}</a>`).join('');}else{const list=(view.manifest.documents||[]).filter(item=>(item.path+' '+item.title+' '+item.category).toLowerCase().includes(query));if(!list.some(item=>item.path===view.document))view.document=list.find(item=>item.format==='md'||item.path.endsWith('.md'))?.path||list[0]?.path||'';let category='';content=list.map(item=>{let prefix='';if(item.category!==category){category=item.category;prefix=`<h3>${escape(category||'参考文档')}</h3>`;}return prefix+`<a href="${escape(href('api-docs',{document:item.path,search:view.search}))}" class="${item.path===view.document?'active':''}">${escape(item.title||item.path)}<small>${escape(item.format||item.path.split('.').at(-1))} · ${count(item.bytes)} 字节</small></a>`;}).join('')||'<p class="docs-summary">没有匹配的章节。</p>';}return content; }
  /* 首期定位只改变研发顺序；文档不存在时不显示悬空入口，也不改变兼容证据。 */
  function productDirection() {
    if(!documentEntry('api/voice-runtime-direction.md'))return '';
    return `<section class="notice info" aria-label="Voice Runtime 首期研发方向"><div class="notice-content"><strong>云蝠 Voice Runtime · 为电话语音智能体服务</strong><p>优先推进实时音频、全双工打断、ASR/TTS、录音与常用FS接口。完整目录保留，首期延后项仍如实显示差异。</p><a href="#api-docs?document=api%2Fvoice-runtime-direction.md">研发方向与验收目标</a> · <a href="#api-docs?document=api%2Fvoice-runtime-roadmap.json">首期里程碑与缺口</a></div></section>`;
  }
  /* API 壳只在进入页面或主动切换时重绘；运行状态轮询没有此函数入口。 */
  function renderAPI() { view.codes.clear();view.schemas.clear();const directory=apiDirectory();document.getElementById('docs-root').innerHTML=`${productDirection()}<div class="notice info"><div class="notice-content"><strong>只读接口与迁移文档</strong><br>${escape(text(view.manifest.scope||'覆盖当前自研接口及 FreeSWITCH 替换目标。'))}${view.manifest.notice?'<br>'+escape(view.manifest.notice):''}</div><span class="badge neutral">文档 ${escape(view.manifest.version)}</span></div><div class="docs-toolbar"><input class="search-input" id="docs-api-search" value="${escape(view.search)}" placeholder="搜索接口路径、名称、章节或结构…" aria-label="搜索接口文档">${downloads()}</div><div class="docs-tabs" role="tablist" aria-label="接口文档类型">${[['operations','HTTP 接口'],['documents','章节文档'],['schemas','数据结构']].map(([key,label])=>`<button role="tab" aria-selected="${view.tab===key}" class="docs-tab ${view.tab===key?'active':''}" data-doc-action="api-tab" data-doc-tab="${key}">${label}</button>`).join('')}</div><div class="docs-counts"><span><strong>${count(view.operations.length)}</strong>HTTP 操作</span><span><strong>${count(view.manifest.documents?.length)}</strong>份文档</span><span>参考 FreeSWITCH ${escape(view.manifest.reference_version)}</span></div><div class="docs-layout"><aside class="panel docs-directory" id="docs-directory" aria-label="接口或章节目录">${directory}</aside><article class="panel docs-content" id="docs-main" aria-label="文档正文"></article></div>`;renderAPISelection(); }
  /* 搜索只更新目录与对应正文，搜索框本身保留，避免重建导致光标跳动。 */
  function filterAPI() { const directory=document.getElementById('docs-directory');if(!directory)return;directory.innerHTML=apiDirectory();remember();renderAPISelection(); }
  /* 正文选择读取的代次独立于页面导航，快速切换文档时不呈现迟到结果。 */
  function renderAPISelection() { const main=document.getElementById('docs-main');if(!main)return;view.readGeneration++;view.codes.clear();view.schemas.clear();if(view.tab==='operations')main.innerHTML=operationView(view.operations.find(item=>item.id===view.operation));else if(view.tab==='schemas'){const schema=view.api.components?.schemas?.[view.schema];main.innerHTML=schema?`<h2>${escape(view.schema)}</h2><p>展开层级可查看全部属性、组合条件、枚举与引用。原始 JSON 保留所有扩展字段。</p>${schemaView(schema,view.schema)}<details class="docs-detail"><summary>完整原始 Schema</summary>${code(schema,'Schema Object')}</details>`:'<div class="empty-state">没有匹配的数据结构。</div>';}else openDocument(view.document);remember(); }
  /* 简单行内 Markdown 只生成白名单标签；原始 HTML 全部当作文字转义。 */
  function inline(raw,path,depth=0) { if(depth>4)return escape(raw);const pattern=/(`[^`\n]+`)|(!?\[([^\]\n]+)\]\(([^)\n]+)\))|(\*\*([^*\n]+)\*\*)|(\*([^*\n]+)\*)/g;let result='',start=0;for(const match of String(raw).matchAll(pattern)){result+=escape(String(raw).slice(start,match.index));if(match[1])result+='<code>'+escape(match[1].slice(1,-1))+'</code>';else if(match[2]){const label=match[3],target=documentURL(match[4].trim(),path);result+=target?`<a href="${escape(target)}" ${target.startsWith('http')?'target="_blank" rel="noopener noreferrer"':''}>${escape(label)}</a>`:`<span class="docs-source-muted" title="${escape(match[4])}">${escape(label)}（引用：${escape(match[4])}）</span>`;}else if(match[5])result+='<strong>'+inline(match[6],path,depth+1)+'</strong>';else result+='<em>'+inline(match[8],path,depth+1)+'</em>';start=match.index+match[0].length;}return result+escape(String(raw).slice(start)); }
  /* 标题 ID 使用确定性 slug，并对同名标题加序号，保证章节跳转稳定。 */
  function slug(raw) { return String(raw).replace(/[`*_]/g,'').toLowerCase().trim().replace(/[^\p{L}\p{N}\s_-]/gu,'').replace(/\s+/g,'-')||'section'; }
  /* Markdown 表格分割保留转义竖线，单元格内容继续走安全行内解析。 */
  function tableCells(line) { return line.trim().replace(/^\|/,'').replace(/\|$/,'').split(/(?<!\\)\|/).map(cell=>cell.trim().replace(/\\\|/g,'|')); }
  /* 安全块级渲染支持标题、代码、列表、引用和表格；不会执行 HTML 或嵌入图像。 */
  function markdown(raw,path) { const lines=raw.replace(/\r\n/g,'\n').split('\n'),headings=[],used=new Map();let output='',index=0;while(index<lines.length){const line=lines[index];if(!line.trim()){index++;continue;}const explicitAnchor=line.match(emptyAnchorPattern);if(explicitAnchor){output+=`<span id="doc-heading-${explicitAnchor[1]}" class="docs-anchor" aria-hidden="true"></span>`;index++;continue;}const fence=line.match(/^\s*(`{3,}|~{3,})(.*)$/);if(fence){const block=[];index++;while(index<lines.length&&!lines[index].trim().startsWith(fence[1]))block.push(lines[index++]);if(index<lines.length)index++;output+=code(block.join('\n'),fence[2].trim()||'代码');continue;}const heading=line.match(/^(#{1,6})\s+(.+?)\s*#*$/);if(heading){const base=slug(heading[2]),occurrence=used.get(base)||0;used.set(base,occurrence+1);const anchor=base+(occurrence?'-'+occurrence:'');headings.push({level:heading[1].length,label:heading[2],anchor});output+=`<h${heading[1].length} id="doc-heading-${escape(anchor)}">${inline(heading[2],path)}</h${heading[1].length}>`;index++;continue;}if(index+1<lines.length&&line.includes('|')&&/^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)+\|?\s*$/.test(lines[index+1])){const headers=tableCells(line),rows=[];index+=2;while(index<lines.length&&lines[index].includes('|')&&lines[index].trim())rows.push(tableCells(lines[index++]));output+=`<div class="docs-table"><table><thead><tr>${headers.map(cell=>'<th>'+inline(cell,path)+'</th>').join('')}</tr></thead><tbody>${rows.map(row=>'<tr>'+headers.map((_,column)=>'<td>'+inline(row[column]||'',path)+'</td>').join('')+'</tr>').join('')}</tbody></table></div>`;continue;}if(/^\s*([-*_])(?:\s*\1){2,}\s*$/.test(line)){output+='<hr>';index++;continue;}if(/^\s*>/.test(line)){const block=[];while(index<lines.length&&/^\s*>/.test(lines[index]))block.push(lines[index++].replace(/^\s*>\s?/,''));output+='<blockquote><p>'+inline(block.join('\n'),path)+'</p></blockquote>';continue;}const list=line.match(/^\s*(?:([-+*])|(\d+)[.)])\s+(.+)/);if(list){const ordered=!!list[2],items=[];while(index<lines.length){const item=lines[index].match(/^\s*(?:([-+*])|(\d+)[.)])\s+(.+)/);if(!item||!!item[2]!==ordered)break;items.push('<li>'+inline(item[3],path)+'</li>');index++;}output+=`<${ordered?'ol':'ul'}>${items.join('')}</${ordered?'ol':'ul'}>`;continue;}const paragraph=[line];index++;while(index<lines.length&&lines[index].trim()&&!emptyAnchorPattern.test(lines[index])&&!/^\s*(?:#{1,6}\s|```|~~~|>|[-+*]\s|\d+[.)]\s)/.test(lines[index])&&!(index+1<lines.length&&lines[index].includes('|')&&/^\s*\|?\s*:?-+/.test(lines[index+1])))paragraph.push(lines[index++]);output+='<p>'+inline(paragraph.join('\n'),path)+'</p>';}
    const toc=headings.length>2?`<nav class="docs-toc" aria-label="本文目录">${headings.map(item=>`<a href="${escape(href('api-docs',{document:path,anchor:item.anchor}))}" style="padding-left:${Math.min(3,item.level-1)*10}px">${escape(item.label.replace(/[`*_]/g,''))}</a>`).join('')}</nav>`:'';return toc+'<div class="docs-reader">'+output+'</div>'; }
  /* CSV 预览限制行数，全部原文仍可展开或下载，避免大量表格节点占用页面。 */
  function csvPreview(raw) { const rows=[];let row=[],field='',quoted=false;for(let i=0;i<raw.length&&rows.length<101;i++){const char=raw[i];if(char==='"'){if(quoted&&raw[i+1]==='"'){field+='"';i++;}else quoted=!quoted;}else if(char===','&&!quoted){row.push(field);field='';}else if(char==='\n'&&!quoted){row.push(field.replace(/\r$/,''));rows.push(row);row=[];field='';}else field+=char;}if(rows.length<101&&(field||row.length)){row.push(field);rows.push(row);}const headers=rows.shift()||[];return `<p>表格预览最多 100 行；完整内容在下方原文与下载文件中。</p><div class="docs-table"><table><thead><tr>${headers.map(value=>'<th>'+escape(value)+'</th>').join('')}</tr></thead><tbody>${rows.slice(0,100).map(row=>'<tr>'+headers.map((_,index)=>'<td>'+escape(row[index]||'')+'</td>').join('')+'</tr>').join('')}</tbody></table></div><details class="docs-detail"><summary>查看完整 CSV 原文</summary>${code(raw,'完整 CSV')}</details>`; }
  /* 章节正文读取严格限于 manifest 路径，文件结果迟到时只缓存，不改当前页面。 */
  async function openDocument(path) { const entry=documentEntry(path),main=document.getElementById('docs-main'),pageGeneration=view.generation,readGeneration=++view.readGeneration;if(!main)return;if(!entry){main.innerHTML='<div class="empty-state">该文档不在当前发布索引中，请从目录选择。</div>';return;}main.innerHTML=loading('正在读取 '+(entry.title||entry.path));try{if(!view.files.has(path))view.files.set(path,fetchRead(fileURL(path),true).catch(error=>{view.files.delete(path);throw error;}));const raw=await view.files.get(path);if(view.page!=='api-docs'||view.generation!==pageGeneration||view.readGeneration!==readGeneration||view.document!==path)return;view.codes.clear();const format=(entry.format||path.split('.').at(-1)).toLowerCase();let body;if(format==='md'||format==='markdown')body=markdown(raw,path);else if(format==='csv')body=csvPreview(raw);else if(format==='json'){let parsed;try{parsed=JSON.parse(raw);}catch{}body=code(parsed??raw,'完整 JSON');}else body=code(raw,'完整 '+format.toUpperCase(),true);main.innerHTML=`<div class="docs-meta"><span>${escape(entry.title||path)}</span><a href="${escape(fileURL(path))}" download="${escape(path.split('/').at(-1))}" class="button button-secondary button-small">下载原文</a><button class="button button-secondary button-small" data-doc-action="copy-link">复制定位链接</button></div><p class="docs-raw-meta">${escape(path)} · ${count(entry.bytes)} 字节<br>SHA-256：${escape(entry.sha256||'未提供')}</p>${body}`;if(view.anchor)requestAnimationFrame(()=>{if(view.page==='api-docs'&&view.readGeneration===readGeneration)document.getElementById('doc-heading-'+view.anchor)?.scrollIntoView({block:'start'});});}catch(error){if(view.generation===pageGeneration&&view.readGeneration===readGeneration&&view.page==='api-docs')main.innerHTML=failure(error,'retry-document');} }
  /* 对照来源中的语义文档仅能跳转到已发布文档，源代码路径只显示为引用文字。 */
  function semanticLink(path) { if(!path)return '';const split=String(path).split('#'),candidates=[split[0],split[0].replace(/^docs\//,'')],known=candidates.find(documentEntry);return known?href('api-docs',{document:known,anchor:split.slice(1).join('#')}):documentURL(String(path),''); }
  /* 处理媒体证据只属于这一条目录和固定两模式22项，不能把任意新kind加入通用白名单。 */
  function completeProcessedProof(proof,data,sourceHash) {
    const fingerprint=value=>typeof value==='string'&&/^[0-9a-f]{64}$/.test(value);
    const scope='两种 UDP socket 模式各完整 11 项真实双腿 G.711 PCMU/PCMA、8kHz、20ms 转码；40ms 缓冲/120ms 上限、重排/迟到/CN/DTMF、基本 RTCP 和回收。仅本地限定功能，不包含 G.722/Opus、其他帧长、本地 IVR、ASR/TTS、FreeSWITCH 原版 API 或并发容量认证。';
    const names=['183_without_aux_then_200_restores_dtmf_without_restarting_media','bidirectional_pcmu_pcma_decodes_changes_and_reencodes','bounded_reorder_and_duplicate_do_not_duplicate_audio','bye_reclaims_graph_and_stale_source_cannot_feed_reused_ports','cn_then_marker_with_new_timestamp_phase_resumes_real_audio','dynamic_a_pcma_and_static_b_pcmu_keep_independent_sdp','interleaved_dtmf_and_cn_use_new_timeline_without_false_audio_loss','missing_frame_then_late_arrival_is_not_replayed','non_g711_and_non_20ms_offers_are_rejected_before_upstream','rtcp_reports_are_terminated_count_media_and_finish_with_bye','worker_pause_expires_old_audio_without_catchup_burst'];
    const expected=['udp','connected'].flatMap(mode=>names.map(name=>`${mode}:ProcessedMediaIntegration.test_${name}`));
    if(proof.comparison_id!=='key-media-transcoding'||proof.scope!==scope||proof.binding_contract!=='official_full_build_and_run_snapshots'||!fingerprint(sourceHash)||proof.source_snapshot_sha256!==sourceHash||proof.connected_cases!==20||proof.call_cycles!==22||proof.rejection_cases!==2||proof.rejection_evidence!=='raw_488_and_recorded_zero_stats'||proof.runtime_catalog_observations!==2)return false;
    if(proof.test_names.length!==expected.length||!expected.every((name,index)=>proof.test_names[index]===name))return false;
    const started=Date.parse(proof.started_at),finished=Date.parse(proof.finished_at),expires=Date.parse(proof.expires_at);
    if(!Number.isFinite(started)||started>finished||finished-started>600000||expires-finished!==30*86400000)return false;
    if(typeof proof.execution_id!=='string'||proof.id!==`${proof.execution_id}:key-media-transcoding`||!Array.isArray(data.receipts))return false;
    const receipts=data.receipts.filter(item=>item.id===proof.execution_id),receipt=receipts[0],record=receipt?.record;
    if(receipts.length!==1||receipt.kind!==proof.kind||!fingerprint(receipt.sha256)||record?.schema_version!=='1.0.0'||record.id!==proof.execution_id||record.kind!==proof.kind||record.started_at!==proof.started_at||record.finished_at!==proof.finished_at)return false;
    const binaries=proof.verified_binary_sha256;
    if(!binaries||Object.keys(binaries).sort().join(',')!=='control,media'||!['control','media'].every(role=>fingerprint(binaries[role])&&record.binaries?.[role]?.sha256===binaries[role]))return false;
    const artifacts=record.artifacts,embedded=receipt.embedded_artifacts;
    // 发布器重验全部原报文后才出具此声明；这里核对归档引用与内嵌清单，不假称浏览器重跑媒体。
    if(!artifacts||!embedded||Object.keys(artifacts).length!==93||Object.keys(embedded).length!==93)return false;
    const safePath=value=>typeof value==='string'&&value.length>0&&!value.startsWith('/')&&!value.split('/').includes('..');
    if(!Object.values(artifacts).every(item=>safePath(item?.path)&&fingerprint(item.sha256)&&embedded[item.path]?.sha256===item.sha256&&embedded[item.path]?.encoding==='zlib+base64'&&Number.isSafeInteger(embedded[item.path]?.bytes)&&embedded[item.path].bytes>=0&&typeof embedded[item.path]?.data==='string'&&embedded[item.path].data.length>0))return false;
    if(new Set(Object.values(artifacts).map(item=>item.path)).size!==93)return false;
    if(!['$receipt','$build','udp.log','connected.log'].every(key=>artifacts[key]))return false;
    return proof.log.path===artifacts['udp.log'].path&&proof.log.sha256===artifacts['udp.log'].sha256;
  }
  /* 私有PCM只接受六个精确入口；完整原报文解压/回放由发布器负责，页面仅核验绑定清单。 */
  const pcmProofScopes = {"key-ipc-pcm-turn-begin":"真实ACK授权、同轮令牌幂等、较新轮次替换及播放互斥；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。","key-ipc-pcm-turn-end":"实际PCM入队后End、样本守恒与排空终态，错误最终偏移可修正；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。","key-ipc-pcm-turn-interrupt":"真实立即停止和40ms淡出、旧轮拒绝、DTMF共用RTP身份继续；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。","key-ipc-pcm-turn-status":"原始回应身份/计数/终态及挂断、worker代次失效后的明确拒绝；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。","key-ipc-pcm-push":"原始S16LE批次到全部实际G.711样本、偏移/容量整批拒绝后继续；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。","key-api-pcm-stream":"本地私有Unix控制/数据连接跨连接令牌、ACK/应用互斥/挂断和代次撤销；两种UDP模式的真实SIP/私有RVA1/Go句柄/继承FD/Rust本地A腿G.711、8kHz、20ms限定链路；共22顶层及12子项。不是FreeSWITCH成对、通用二进制畸形输入全集、ASR/TTS供应商或并发容量认证。"};
  const pcmProofMethods = ["test_ack_before_wrong_and_correct_gate_real_pcm","test_active_dtmf_and_pcm_interrupt_keep_shared_tx","test_both_laws_persistent_stream_complete_and_same_begin_token","test_deterministic_offset_queue_rejections_preserve_input_and_cross_connection_token","test_hangup_revokes_tokens_and_reused_ports_have_only_new_call_pcm","test_new_turn_old_token_and_stop_barriers","test_no_ack_timeout_reclaims_and_old_uuid_cannot_begin","test_park_and_pcm_lifecycles_are_independent","test_silent_read_digits_and_timeout_with_pcm","test_wav_and_prompt_read_exclude_pcm_without_cancelling_owner","test_worker_restart_retires_old_generation_and_new_call_stream_works"];
  function completePCMProof(proof,data,sourceHash,verifiedReceipts) {
    const fingerprint=value=>typeof value==='string'&&/^[0-9a-f]{64}$/.test(value);
    const integer=value=>Number.isSafeInteger(value)&&value>=0;
    const safePath=value=>typeof value==='string'&&value.length>0&&!value.startsWith('/')&&!value.includes('\\')&&value.split('/').every(part=>part&&part!=='.'&&part!=='..');
    if(proof.kind!=='python_unittest_sip_pcm_stream_e2e'||!Array.isArray(proof.test_names)||!Object.hasOwn(pcmProofScopes,proof.comparison_id)||proof.scope!==pcmProofScopes[proof.comparison_id]||proof.binding_contract!=='official_full_build_and_run_snapshots'||!fingerprint(sourceHash)||proof.source_snapshot_sha256!==sourceHash)return false;
    const sources=['control/internal/server/pcm_stream.go','control/internal/server/pcm_endpoint.go','control/internal/media/pcm_stream.go','media/src/media/worker_pcm.rs','media/src/media/pcm_transport.rs','tests/sip_pcm_stream_e2e.py','tests/media_processed_local_e2e.py','tests/media_processed_fixture.py','tests/media_pcm_turn_e2e.py','tests/e2e.py','tests/esl_e2e.py','tests/esl_applications_e2e.py','tests/dtmf_send_e2e.py'];
    if(!sources.every(path=>fingerprint(data.runtime_source_snapshot?.[path])))return false;
    if(proof.method_count!==22||proof.subtest_count!==12||proof.call_cycles!==32||proof.raw_artifact_count!==144)return false;
    // 状态轮询与分片回应数量会随调度变化，不能锁死一次运行的896；保留真实整数和样本关系。
    if(!['audio_packets','content_samples','stream_responses','raw_packets'].every(key=>integer(proof[key]))||proof.audio_packets===0||proof.stream_responses===0||proof.content_samples!==proof.audio_packets*160||proof.raw_packets<proof.audio_packets+proof.stream_responses*2)return false;
    const expected=['udp','connected'].flatMap(mode=>pcmProofMethods.map(name=>`${mode}:sip_pcm_stream_e2e.SIPPCMIntegration.${name}`));
    if(proof.test_names.length!==22||!expected.every((name,index)=>proof.test_names[index]===name))return false;
    const started=Date.parse(proof.started_at),finished=Date.parse(proof.finished_at),expires=Date.parse(proof.expires_at);
    if(!Number.isFinite(started)||!Number.isFinite(finished)||started>finished||finished-started>600000||finished>Date.now()+300000||expires-finished!==30*86400000)return false;
    if(typeof proof.execution_id!=='string'||!/^pcm-[0-9a-f]{32}$/.test(proof.execution_id)||proof.id!==`${proof.execution_id}:${proof.comparison_id}`||!verifiedReceipts?.has(proof.execution_id)||!Array.isArray(data.receipts))return false;
    const receipts=data.receipts.filter(item=>item.id===proof.execution_id),receipt=receipts[0],record=receipt?.record;
    if(receipts.length!==1||receipt.kind!==proof.kind||!fingerprint(receipt.sha256)||receipt.path!==`${proof.execution_id}.receipt.json`||record?.schema_version!=='1.0.0'||record.id!==proof.execution_id||record.kind!==proof.kind||record.started_at!==proof.started_at||record.finished_at!==proof.finished_at)return false;
    const binaries=proof.verified_binary_sha256;
    if(!binaries||!record.binaries||Object.keys(binaries).sort().join(',')!=='control,media'||Object.keys(record.binaries).sort().join(',')!=='control,media'||!['control','media'].every(role=>fingerprint(binaries[role])&&record.binaries[role]?.sha256===binaries[role]&&record.binaries[role]?.path===`${proof.execution_id}/binary-${role}`))return false;
    const artifacts=record.artifacts,embedded=receipt.embedded_artifacts;
    if(!artifacts||Array.isArray(artifacts)||!embedded||Array.isArray(embedded)||Object.keys(artifacts).length!==146||Object.keys(embedded).length!==146)return false;
    const required=['$receipt','$build','build.json','runtime-build.json','run-before.json','progress.json','recorder.py','runner.py','udp.result.json','udp.stdout.log','udp.stderr.log','connected.result.json','connected.stdout.log','connected.stderr.log'];
    if(!required.every(key=>Object.hasOwn(artifacts,key)))return false;
    // 每方法独占六份原文件，不能借另一用例或把同文件重复登记凑144原引用。
    const seen=new Set(required),files=['config.json','events.jsonl','long-prompt.wav','prompt.wav','server.log','wire.json'];
    for(const mode of ['udp','connected'])for(const method of pcmProofMethods){
      const matches=Object.keys(artifacts).filter(key=>key.startsWith(`${mode}/${method}-`)&&key.endsWith('/wire.json'));
      if(matches.length!==1)return false;
      const directory=matches[0].slice(0,-'/wire.json'.length),suffix=directory.slice(`${mode}/${method}-`.length);
      if(!/^[0-9a-f]{8}$/.test(suffix))return false;
      for(const file of files){const key=`${directory}/${file}`;if(seen.has(key)||!Object.hasOwn(artifacts,key))return false;seen.add(key);}
    }
    if(seen.size!==146)return false;
    let bytes=0;const paths=new Set();
    for(const[key,item]of Object.entries(artifacts)){
      const expectedPath=`${proof.execution_id}/`+(key==='$receipt'?'original.receipt.json':key==='$build'?'official-runtime-build.json':`raw/${key}`),packed=embedded[item?.path];
      if(!safePath(item?.path)||item.path!==expectedPath||paths.has(item.path)||!fingerprint(item.sha256)||packed?.sha256!==item.sha256||packed.encoding!=='zlib+base64'||!integer(packed.bytes)||packed.bytes>64*1024*1024||typeof packed.data!=='string'||!packed.data.length||packed.data.length>90*1024*1024||packed.data.length%4!==0||!/^[A-Za-z0-9+/]*={0,2}$/.test(packed.data))return false;
      paths.add(item.path);if(!key.startsWith('$'))bytes+=packed.bytes;
    }
    if(bytes>64*1024*1024||!fingerprint(proof.recorder_sha256)||!fingerprint(proof.runner_sha256)||proof.recorder_sha256!==artifacts['recorder.py'].sha256||proof.runner_sha256!==artifacts['runner.py'].sha256)return false;
    return artifacts['$build'].sha256===artifacts['runtime-build.json'].sha256&&proof.log.path===artifacts['udp.stderr.log'].path&&proof.log.sha256===artifacts['udp.stderr.log'].sha256;
  }
  /* RXS2只属于五个内部入口；固定原文件合同由发布器重放，页面不解压音频或执行收据。 */
  const rxProofKind='go_rxs2_local_g711_composite_e2e';
  const rxProofScopes={"key-ipc-rx-subscribe":"真实订阅及ACK前/错误来源ACK拒绝；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。","key-ipc-rx-status":"实际身份、事件/样本计数和失败后仍可查询；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。","key-ipc-rx-unsubscribe":"明确退订、挂断撤销、资源归零及端口重新绑定；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。","key-ipc-rx-stream":"真实G.711输入逐样本核对、原始期限和过期拒绝；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。","key-api-rx-sdk":"内部Go收音句柄、SIP授权、交付期限及挂断清理；PCMU/PCMA×普通/connected UDP 的真实 Rust→RXS2→Go SDK、本地 A 腿、8kHz/20ms；正常样本/250ms接收暂停/实际SIP授权清理共12子例。仅内部SDK，非HTTP、原版FS成对、ASR供应商、Linux运行或容量认证。MSG_PEEK仅核实OS队首，不代表整个队列抓包。"};
  const rxProofBinding='历史各运行源码重叠逐件一致；当前完整Go及vendor(含.s/modules.txt)、fs_templates、config/local.json、Rust src/Cargo绑定；未绑定Python/native/网页文档及其他配置';
  const rxProofCases=['false','true'].flatMap(mode=>[0,8].map(law=>`payload_${law}_connected_${mode}`));
  const rxProofMethods=['TestRealRXPoolG711SamplesAndLifecycle','TestRealRXReceiverPauseRejectsOSResidentPCM','TestRealRXServerSIPAuthorizationSamplesAndCleanup'];
  // 与 tools/rx_verification.py 的 bound_path 相同；新增/删除绑定文件也必须改变摘要，不能只核对旧白名单。
  function rxBoundPath(name) {
    if(name.startsWith('control/internal/server/doc_assets/')||name.startsWith('control/internal/server/web/'))return false;
    return (name.startsWith('control/')&&/\.(?:go|s|S|c|h|cc|cpp|cxx|m|mm|f|F|f90|for|syso|swig|swigcxx)$/.test(name))||['control/go.mod','control/go.sum','config/local.json','media/Cargo.toml','media/Cargo.lock','media/build.rs'].includes(name)||name.startsWith('control/vendor/')||name.startsWith('control/internal/server/fs_templates/')||((name.startsWith('media/src/')||name.startsWith('media/.cargo/'))&&(name.endsWith('.rs')||name.endsWith('.toml')));
  }
  // RFC3339完整日期和纳秒比较，避免Date.parse舍入掩盖亚毫秒延长或自动修正非法日期。
  function rxTimestampNS(value) {
    if(typeof value!=='string')return null;
    const m=value.match(/^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/);
    if(!m)return null;
    const[y,month,day,hour,minute,second]=m.slice(1,7).map(Number);
    if(y<1000||month<1||month>12||day<1||hour>23||minute>59||second>59)return null;
    const utc=Date.UTC(y,month-1,day,hour,minute,second),calendar=new Date(utc);
    if(!Number.isFinite(utc)||calendar.getUTCFullYear()!==y||calendar.getUTCMonth()!==month-1||calendar.getUTCDate()!==day)return null;
    let offset=0;
    if(m[8]!=='Z'){
      const hours=Number(m[8].slice(1,3)),minutes=Number(m[8].slice(4,6));
      if(hours>23||minutes>59)return null;
      offset=(hours*60+minutes)*60000*(m[8][0]==='+'?1:-1);
    }
    return BigInt(utc-offset)*1000000n+BigInt((m[7]||'').padEnd(9,'0'));
  }
  function completeRXProof(proof,data,sourceBinding,verifiedReceipts) {
    const fingerprint=value=>typeof value==='string'&&/^[0-9a-f]{64}$/.test(value);
    const object=value=>value&&typeof value==='object'&&!Array.isArray(value);
    const integer=value=>Number.isSafeInteger(value)&&value>=0;
    const safePath=value=>typeof value==='string'&&value.length>0&&!value.startsWith('/')&&!value.includes('\\')&&value.split('/').every(part=>part&&part!=='.'&&part!=='..');
    if(proof?.kind!==rxProofKind||!Object.hasOwn(rxProofScopes,proof.comparison_id)||proof.scope!==rxProofScopes[proof.comparison_id]||proof.binding_contract!==rxProofBinding||!fingerprint(sourceBinding?.sha256)||proof.source_snapshot_sha256!==sourceBinding.sha256||!integer(proof.source_files)||proof.source_files===0||proof.source_files!==sourceBinding.files)return false;
    const snapshot=data.runtime_source_snapshot,requiredSources=['control/go.mod','control/go.sum','config/local.json','control/internal/media/rx_stream.go','control/internal/server/rx_stream.go','control/vendor/modules.txt','media/Cargo.toml','media/Cargo.lock','media/src/media/rx_export.rs'];
    if(!object(snapshot)||!requiredSources.every(key=>fingerprint(snapshot[key]))||!Object.keys(snapshot).some(key=>key.startsWith('control/vendor/')&&key.endsWith('.s')))return false;
    const expected=rxProofMethods.flatMap(method=>rxProofCases.map(name=>`${method}/${name}`));
    if(proof.evidence_cases!==12||proof.decoded_samples!==8960||!Array.isArray(proof.test_names)||proof.test_names.length!==expected.length||!expected.every((name,index)=>proof.test_names[index]===name))return false;
    const start=rxTimestampNS(proof.started_at),finish=rxTimestampNS(proof.finished_at),expires=rxTimestampNS(proof.expires_at);
    if(start===null||finish===null||expires===null||start>finish||finish>BigInt(Date.now()+300000)*1000000n||expires-finish!==2592000000000000n)return false;
    if(typeof proof.execution_id!=='string'||!/^rx-[0-9a-f]{32}$/.test(proof.execution_id)||proof.id!==`${proof.execution_id}:${proof.comparison_id}`||!verifiedReceipts?.has(proof.execution_id)||!Array.isArray(data.receipts)||!Array.isArray(data.observations))return false;
    const group=data.observations.filter(item=>item.kind===rxProofKind&&item.execution_id===proof.execution_id);
    if(group.length!==5||new Set(group.map(item=>item.comparison_id)).size!==5||!group.every(item=>Object.hasOwn(rxProofScopes,item.comparison_id)&&item.scope===rxProofScopes[item.comparison_id]&&item.id===`${proof.execution_id}:${item.comparison_id}`&&item.green_eligible===true&&item.executed===true&&item.status==='passed'))return false;
    const receipts=data.receipts.filter(item=>item.id===proof.execution_id),receipt=receipts[0],record=receipt?.record;
    if(receipts.length!==1||receipt.kind!==rxProofKind||receipt.path!==`${proof.execution_id}.receipt.json`||!fingerprint(receipt.sha256)||!object(record)||record.schema_version!=='1.0.0'||record.kind!==rxProofKind||record.id!==proof.execution_id||record.started_at!==proof.started_at||record.finished_at!==proof.finished_at)return false;
    const roles=['media','pause_test','pool_test','server_test'],binaries=proof.verified_binary_sha256;
    if(!object(binaries)||!object(record.binaries)||Object.keys(binaries).sort().join(',')!==roles.join(',')||Object.keys(record.binaries).sort().join(',')!==roles.join(',')||!roles.every(role=>fingerprint(binaries[role])&&record.binaries[role]?.sha256===binaries[role]&&record.binaries[role]?.path===`${proof.execution_id}/binary-${role}`))return false;
    const artifacts=record.artifacts,embedded=receipt.embedded_artifacts;
    if(!object(artifacts)||!object(embedded)||Object.keys(artifacts).length>128)return false;
    // 文件名属于采集器固定合同；只限制文件组，不硬编码任意一次状态轮询或日志行数。
    const required=new Set(['pool/$receipt','pool/tests.jsonl','pool_build/$receipt','pause/$receipt','pause/build.log','pause/tests.log','server/$receipt','media_build/$receipt']);
    for(const name of ['build-race','sdk-race','shared-clock-bench','build-cgo0-darwin-arm64','build-cgo0-linux-arm64','build-cgo0-linux-amd64','sdk-cgo0-darwin'])required.add(`pool_build/${name}.log`);
    for(const name of ['tests','clippy','release'])required.add(`media_build/${name}.log`);
    for(const name of ['go-version.log','dependencies.log','compile.log','control-buildinfo.log','test.log','recorder.py','rust-build-receipt.json'])required.add(`server/${name}`);
    const pausePrefix=Object.keys(artifacts).some(key=>key.startsWith('pause/wire/'))?'wire':'cases';
    for(const name of rxProofCases){
      for(const file of ['helper-ready.json','helper-result.json','helper.log','os-head.rxs2','parent-observation.json','input-0.rtp','input-1.rtp'])required.add(`pause/${pausePrefix}/${name}/${file}`);
      for(const file of ['wire.jsonl','events.jsonl'])required.add(`server/wire/${name}/${file}`);
    }
    if(Object.keys(artifacts).length!==required.size||Object.keys(embedded).length!==required.size||![...required].every(key=>Object.hasOwn(artifacts,key)))return false;
    let bytes=0,compressed=0;const paths=new Set();
    for(const[key,item]of Object.entries(artifacts)){
      const expectedPath=`${proof.execution_id}/${key.replace(/\/\$receipt$/,'/receipt.json')}`,packed=embedded[item?.path];
      if(!safePath(item?.path)||item.path!==expectedPath||paths.has(item.path)||!fingerprint(item.sha256)||!object(packed)||packed.sha256!==item.sha256||packed.encoding!=='zlib+base64'||!integer(packed.bytes)||packed.bytes>32*1024*1024||typeof packed.data!=='string'||!packed.data.length||packed.data.length>48*1024*1024||packed.data.length%4!==0||!/^[A-Za-z0-9+/]*={0,2}$/.test(packed.data)||(key.endsWith('/$receipt')&&packed.bytes===0))return false;
      paths.add(item.path);bytes+=packed.bytes;compressed+=packed.data.length;
    }
    if(bytes>32*1024*1024||compressed>48*1024*1024)return false;
    return artifacts['media_build/$receipt'].sha256===artifacts['server/rust-build-receipt.json'].sha256&&proof.log?.path===artifacts['server/test.log'].path&&proof.log?.sha256===artifacts['server/test.log'].sha256;
  }
  /* 发布器已离线重验源码与原始日志；浏览器再核对发布SHA、原ID分母和绿色所需字段。 */
  function validateRuntimeView(data,comparison,manifest,processedSourceHash=null,pcmReceiptsVerified=null,rxSourceBinding=null,rxReceiptsVerified=null) {
    const fail=message=>{throw new Error(message);},fingerprint=value=>typeof value==='string'&&/^[0-9a-f]{64}$/.test(value);
    const original=new Map(comparison.entries.map(row=>[row.id,row.status])),rows=new Map(),proofs=new Map(),latestByEntry=new Map();
    if(data?.version!=='1.0.0'||data.upstream_runtime_executed!==false||data.paired_status!=='not_run'||data.production_capacity_certified!==false)fail('运行证据边界无效，已禁用绿色。');
    if(data.comparison_sha256!==manifest.documents.find(item=>item.path==='api/comparison.json')?.sha256)fail('运行证据与当前对照指纹不一致。');
    if(!Array.isArray(data.entries)||data.entries.length!==original.size||!Array.isArray(data.observations))fail('运行证据缺少完整原始条目。');
    /* 每种本地范围分别采用最近证据；无效时刻不能被忽略后退回旧成功。 */
    for(const proof of data.observations){
      if(!proof?.id||proofs.has(proof.id))fail('运行证据身份重复。');proofs.set(proof.id,proof);
      if(!latestByEntry.has(proof.comparison_id))latestByEntry.set(proof.comparison_id,new Map());
      const kinds=latestByEntry.get(proof.comparison_id),previous=kinds.get(proof.kind),order=item=>Number.isFinite(Date.parse(item.finished_at))?Date.parse(item.finished_at):Infinity;
      if(!previous||order(proof)>order(previous))kinds.set(proof.kind,proof);
    }
    /* 选中的展示证据及其他最新必需范围都须完整，通过期限取所有范围的最小值。 */
    const completeProof=proof=>proof&&proof.green_eligible===true&&proof.status==='passed'&&proof.executed===true&&Array.isArray(proof.test_names)&&proof.test_names.length>0&&fingerprint(proof.source_snapshot_sha256)&&fingerprint(proof.log?.sha256)&&Number.isFinite(Date.parse(proof.finished_at))&&Number.isFinite(Date.parse(proof.expires_at))&&(['go_race_subset','python_unittest_e2e','capacity_5000'].includes(proof.kind)||(proof.kind==='python_unittest_processed_media_e2e'&&completeProcessedProof(proof,data,processedSourceHash))||(proof.kind==='python_unittest_sip_pcm_stream_e2e'&&completePCMProof(proof,data,processedSourceHash,pcmReceiptsVerified))||(proof.kind===rxProofKind&&completeRXProof(proof,data,rxSourceBinding,rxReceiptsVerified)));
    for(const row of data.entries){
      if(!original.has(row.comparison_id)||rows.has(row.comparison_id)||row.implementation_status!==original.get(row.comparison_id)||!Object.hasOwn(runtimeStatuses,row.local_status)||typeof row.green_eligible!=='boolean')fail('运行证据条目缺失、重复或修改了实现状态。');
      if((row.local_status==='passed')!==row.green_eligible)fail('通过状态缺少绿色门禁。');
      if(row.green_eligible){
        const proof=proofs.get(row.verification_id),latest=[...(latestByEntry.get(row.comparison_id)?.values()||[])];
        if(row.local_status!=='passed'||!completeProof(proof)||proof.comparison_id!==row.comparison_id||row.scope!==proof.scope||(data.integrity_errors||[]).length)fail('绿色条目缺少完整通过证据。');
        if(!latest.length||!latest.every(completeProof)||Date.parse(row.effective_expires_at)!==Math.min(...latest.map(item=>Date.parse(item.expires_at))))fail('绿色条目的其他必需范围失效或最早复核期限不一致。');
        for(const item of latest)if(item.kind==='capacity_5000'&&(row.comparison_id!=='key-media-capacity'||item.measurement?.requested_calls!==5000))fail('容量证据超出5000路限定范围。');
      }
      rows.set(row.comparison_id,row);
    }
    if(data.summary?.green_entries!==data.entries.filter(row=>row.green_eligible).length)fail('运行证据绿色计数不一致。');
    return {data,rows,proofs};
  }
  /* 只读取已发布的运行证据，SHA错误/功能缺失时保留可见错误，不自动推断通过。 */
  async function loadRuntime(manifest,comparison) {
    const item=manifest.documents.find(row=>row.path==='api/runtime-verification.json');
    if(!item)throw new Error('当前发布包尚未收录运行证据，不能判断本地通过。');
    if(!globalThis.crypto?.subtle)throw new Error('当前浏览器不能核对SHA，请通过本机或HTTPS访问后重新读取。');
    const raw=await fetchRead('/v1/docs/file?path=api%2Fruntime-verification.json',true);
    const digest=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(raw)),actual=[...new Uint8Array(digest)].map(value=>value.toString(16).padStart(2,'0')).join('');
    if(actual!==item.sha256)throw new Error('运行证据文件SHA校验失败，已禁用绿色。');
    const data=JSON.parse(raw);let processedSourceHash=null,rxSourceBinding=null;
    if(data.observations?.some(proof=>['python_unittest_processed_media_e2e','python_unittest_sip_pcm_stream_e2e'].includes(proof.kind)&&proof.green_eligible)){
      const snapshot=data.runtime_source_snapshot;
      if(!snapshot||typeof snapshot!=='object'||Array.isArray(snapshot)||!Object.keys(snapshot).length||Object.values(snapshot).some(value=>typeof value!=='string'||!/^[0-9a-f]{64}$/.test(value)))throw new Error('处理媒体证据缺少完整功能源码指纹。');
      processedSourceHash=await shaText(JSON.stringify(Object.fromEntries(Object.keys(snapshot).sort().map(key=>[key,snapshot[key]])),null,2)+'\n');
    }
    if(data.observations?.some(proof=>proof.kind===rxProofKind&&proof.green_eligible)){
      const snapshot=data.runtime_source_snapshot;
      if(!snapshot||typeof snapshot!=='object'||Array.isArray(snapshot)||!Object.keys(snapshot).length||Object.entries(snapshot).some(([key,value])=>!key||key.startsWith('/')||key.includes('\\')||key.split('/').some(part=>!part||part==='.'||part==='..')||typeof value!=='string'||!/^[0-9a-f]{64}$/.test(value)))throw new Error('RX证据缺少可核对的完整功能源码指纹。');
      const keys=Object.keys(snapshot).filter(rxBoundPath).sort(),bound=Object.fromEntries(keys.map(key=>[key,snapshot[key]]));
      rxSourceBinding={sha256:await shaText(JSON.stringify(bound,null,2)+'\n'),files:keys.length};
    }
    const pcmReceiptsVerified=new Set(),rxReceiptsVerified=new Set();
    if(data.observations?.some(proof=>['python_unittest_sip_pcm_stream_e2e',rxProofKind].includes(proof.kind)&&proof.green_eligible)){
      if(!Array.isArray(data.receipts))throw new Error('PCM/RX证据缺少归档收据。');
      // 与Python原收据的递归排序JSON一致；只哈希小型索引，浏览器不解压或执行原始证据。
      const canonical=(value,depth=0)=>{
        if(depth>16)throw new Error('PCM/RX归档索引层级越界。');
        if(Array.isArray(value))return value.map(item=>canonical(item,depth+1));
        if(value&&typeof value==='object')return Object.fromEntries(Object.keys(value).sort().map(key=>[key,canonical(value[key],depth+1)]));
        return value;
      };
      for(const receipt of data.receipts.filter(item=>['python_unittest_sip_pcm_stream_e2e',rxProofKind].includes(item.kind))){
        if(!receipt.record||await shaText(JSON.stringify(canonical(receipt.record),null,2)+'\n')!==receipt.sha256)throw new Error('PCM/RX归档收据SHA不一致。');
        (receipt.kind===rxProofKind?rxReceiptsVerified:pcmReceiptsVerified).add(receipt.id);
      }
    }
    return validateRuntimeView(data,comparison,manifest,processedSourceHash,pcmReceiptsVerified,rxSourceBinding,rxReceiptsVerified);
  }
  /* 进展文件是可选发布产物；缺失与读取失败保留原状，不伪造本轮零增量。 */
  async function loadProgress(manifest) {
    const item=manifest.documents.find(row=>row.path==='api/compatibility-progress.json');
    if(!item)throw new Error('当前发布包尚未提供可核对的本轮增量。');
    if(!globalThis.crypto?.subtle)throw new Error('当前浏览器不能核对进展文件 SHA。');
    const raw=await fetchRead('/v1/docs/file?path=api%2Fcompatibility-progress.json',true);
    if(await shaText(raw)!==item.sha256)throw new Error('进展文件 SHA 校验失败，未展示增量。');
    return JSON.parse(raw);
  }
  /* SHA 只核对已读取的文本；不上传源码或运行任何接口示例。 */
  async function shaText(raw) {
    const digest=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(raw));
    return [...new Uint8Array(digest)].map(value=>value.toString(16).padStart(2,'0')).join('');
  }
  /* 状态计数允许省略零值，显式负值、小数、布尔值及未知非零状态均不能通过。 */
  function countsEqual(actual,expected) {
    if(!actual||typeof actual!=='object'||Array.isArray(actual))return false;
    return Object.keys({...actual,...expected}).every(key=>(!Object.hasOwn(actual,key)||Number.isSafeInteger(actual[key])&&actual[key]>=0)&&(actual[key]??0)===(expected[key]??0));
  }
  /* 当前增量须绑定已验证运行文件、目录和基线；它只描述变化，不授予新的绿色。 */
  async function validateProgressView(data,comparison,runtime,manifest) {
    const fail=message=>{throw new Error(message);},fingerprint=value=>typeof value==='string'&&/^[0-9a-f]{64}$/.test(value),documentHash=path=>manifest.documents.find(item=>item.path===path)?.sha256;
    if(data?.schema_version!=='1.0.0'||!runtime||!data.baseline||!data.current||!data.changes)fail('进展缺少当前运行证据或版本基线。');
    const baselineVersion=/^(\d+)\.(\d+)\.\d+$/.exec(data.baseline.version||''),baselinePath=baselineVersion?`api/compatibility-baseline-v${baselineVersion[1]}.${baselineVersion[2]}.json`:'';
    if(![data.source_sha256,data.comparison_sha256,data.runtime_sha256,data.baseline_sha256].every(fingerprint)||data.comparison_sha256!==documentHash('api/comparison.json')||data.runtime_sha256!==documentHash('api/runtime-verification.json')||data.baseline_sha256!==documentHash(baselinePath))fail('进展与当前发布证据指纹不一致。');
    /* 源快照是路径到SHA的平面对象；排序与发布器的规范化 JSON 保持一致。 */
    const snapshot=runtime.source_snapshot;
    if(!snapshot||typeof snapshot!=='object'||Array.isArray(snapshot)||Object.values(snapshot).some(value=>!fingerprint(value)))fail('运行证据缺少可核对的源快照。');
    const canonical=JSON.stringify(Object.fromEntries(Object.keys(snapshot).sort().map(key=>[key,snapshot[key]])),null,2)+'\n';
    if(await shaText(canonical)!==data.source_sha256)fail('进展源码指纹不属于当前运行证据。');
    const entries=new Map(comparison.entries.map(row=>[row.id,row])),proofs=new Map(runtime.entries.map(row=>[row.comparison_id,row])),implementations={},locals={};
    for(const row of comparison.entries)implementations[row.status]=(implementations[row.status]||0)+1;
    for(const row of runtime.entries)locals[row.local_status]=(locals[row.local_status]||0)+1;
    if(data.current.version!==manifest.version||typeof data.baseline.version!=='string'||!data.baseline.version.trim()||data.baseline.version.length>64||data.current.comparison_entries!==entries.size||data.current.green_entries!==runtime.summary.green_entries||!countsEqual(data.current.implementation_counts,implementations)||!countsEqual(data.current.local_counts,locals))fail('进展的当前版本、分母或状态计数不一致。');
    const baseline=data.baseline,validCounts=value=>value&&typeof value==='object'&&!Array.isArray(value)&&Object.values(value).every(total=>Number.isSafeInteger(total)&&total>=0)&&Object.values(value).reduce((sum,total)=>sum+total,0)===baseline.comparison_entries;
    if(!Number.isSafeInteger(baseline.comparison_entries)||baseline.comparison_entries<0||!Number.isSafeInteger(baseline.green_entries)||baseline.green_entries<0||baseline.green_entries!==(baseline.local_counts?.passed||0)||!validCounts(baseline.local_counts)||!validCounts(baseline.implementation_counts))fail('基线计数不完整，未展示增量。');
    const changes=data.changes,ids=name=>{
      const list=changes[name];if(!Array.isArray(list)||list.length>baseline.comparison_entries+entries.size)fail('进展缺少有界的逐项变化列表。');
      const values=list.map(row=>typeof row==='string'?row:row?.id);
      if(values.some(id=>typeof id!=='string'||!id||id.length>1024)||new Set(values).size!==values.length)fail('进展变化ID缺失或重复。');return new Set(values);
    };
    const added=ids('added_ids'),removed=ids('removed_ids'),newGreen=ids('new_green'),lostGreen=ids('lost_green');ids('implementation_changed');
    if([...added].some(id=>!entries.has(id)||removed.has(id))||[...removed].some(id=>entries.has(id))||entries.size-baseline.comparison_entries!==added.size-removed.size)fail('进展新增或删除的目录分母不一致。');
    for(const row of changes.new_green)if(!entries.has(row.id)||newGreen.has(row.id)&&lostGreen.has(row.id)||proofs.get(row.id)?.green_eligible!==true||proofs.get(row.id)?.local_status!=='passed'||row.scope!==proofs.get(row.id)?.scope||row.name!==entries.get(row.id)?.name)fail('新增通过未对应当前真实绿色条目。');
    for(const row of changes.lost_green)if((!entries.has(row.id)&&!removed.has(row.id))||proofs.get(row.id)?.green_eligible===true||typeof row.reason!=='string'||!row.reason.trim()||(entries.has(row.id)&&row.name!==entries.get(row.id).name))fail('撤回记录与当前条目状态不一致。');
    for(const row of changes.implementation_changed)if(!entries.has(row.id)||row.name!==entries.get(row.id).name||row.to!==entries.get(row.id).status||row.from===row.to||!Object.hasOwn(statuses,row.from))fail('实现状态变化不匹配当前条目。');
    if(data.current.green_entries-baseline.green_entries!==newGreen.size-lostGreen.size)fail('增量与前后绿色数量不一致。');
    return data;
  }
  /* 成对摘要独立于本地运行证据；两个源码指纹覆盖的范围不同，不相互替代或强行比较。 */
  function validateConformanceView(data,comparison,manifest) {
    const fail=message=>{throw new Error(message);},fingerprint=value=>typeof value==='string'&&/^[0-9a-f]{64}$/.test(value);
    const report=manifest.documents.find(item=>item.path==='api/conformance-report.json');
    if(data?.schema_version!=='1.0.0'||data.reference_runtime_executed!==true||data.full_compatibility_passed!==false)fail('成对报告缺少真实原版运行边界，已禁用绿色。');
    if(['all_protocols_passed','full_superiority_proven'].some(key=>Object.hasOwn(data,key)&&data[key]!==false))fail('成对摘要越界声明全部协议或能力通过。');
    if(data.reference?.version!==comparison.reference_version||data.reference?.commit!==comparison.reference_commit||data.reference.version!==manifest.reference_version||data.reference.commit!==manifest.reference_commit)fail('成对报告的 FreeSWITCH 版本或提交不匹配当前对照。');
    if(!fingerprint(data.source_snapshot_sha256)||!fingerprint(data.report_sha256)||data.report_sha256!==report?.sha256)fail('成对摘要缺少可核对源码或原始报告指纹。');
    const finished=Date.parse(data.finished_at),expires=Date.parse(data.expires_at);
    if(!Number.isFinite(finished)||!Number.isFinite(expires)||finished>Date.now()+300000||expires<=finished||expires>finished+30*86400000)fail('成对报告完成时刻或复核期限无效。');
    if(!Number.isSafeInteger(data.original_entries)||data.original_entries<1||comparison.entries.length!==data.original_entries||!Array.isArray(data.probes)||data.probes.length<1||data.probes.length>512)fail('成对报告缺少完整对照分母或有界探针列表。');
    const ids=new Set(),totals=Object.create(null);
    for(const probe of data.probes){
      if(typeof probe?.id!=='string'||!probe.id||probe.id.length>128||ids.has(probe.id)||typeof probe.state!=='string'||!probe.state||typeof probe.scope!=='string'||!probe.scope.trim()||probe.scope.length>4096)fail('成对探针身份、状态或限定范围缺失。');
      if(probe.state==='passed_scoped'&&conformanceDiscovery(probe))fail('身份或接口目录发现被错误标为语义通过，已禁用绿色。');
      ids.add(probe.id);totals[probe.state]=(totals[probe.state]||0)+1;
    }
    if(!data.counts||typeof data.counts!=='object'||Array.isArray(data.counts)||Object.keys({...totals,...data.counts}).some(key=>!Number.isSafeInteger(data.counts[key])||data.counts[key]<0||(totals[key]||0)!==data.counts[key]))fail('成对探针状态计数与实际列表不一致。');
    if(!data.api_inventory||typeof data.api_inventory!=='object'||Array.isArray(data.api_inventory))fail('API 入口发现摘要格式无效。');
    return data;
  }
  /* 身份核实与入口发现始终保持中性，不能凭 passed 字符串得到绿色。 */
  function conformanceDiscovery(probe) {return String(probe.id).split(/[._:/-]/).some(part=>['identity','inventory'].includes(part.toLowerCase()));}
  /* 文件内容先与发布清单 SHA 比较；缺失、损坏及不可信时间均不会影响原本地证据。 */
  async function loadConformance(manifest,comparison) {
    const item=manifest.documents.find(row=>row.path==='api/conformance-summary.json');
    if(!item)throw new Error('尚未取得可核对的成对报告；请等待发布本轮原始证据及摘要。');
    if(!globalThis.crypto?.subtle)throw new Error('当前浏览器不能核对成对报告 SHA，请通过本机或 HTTPS 访问。');
    const raw=await fetchRead('/v1/docs/file?path=api%2Fconformance-summary.json',true);
    const digest=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(raw)),actual=[...new Uint8Array(digest)].map(value=>value.toString(16).padStart(2,'0')).join('');
    if(actual!==item.sha256)throw new Error('成对摘要文件 SHA 校验失败，已禁用本面板绿色。');
    return validateConformanceView(JSON.parse(raw),comparison,manifest);
  }
  /* 只对有证据且未到期的精确子场景标绿，未知状态保持中性并显示原值。 */
  function conformanceProbeState(probe) {
    if(!view.conformance||view.conformanceError)return ['缺少可核对证据','warning'];
    if(probe.state==='passed_scoped'&&!conformanceDiscovery(probe))return Date.now()<Date.parse(view.conformance.expires_at)?['成对子场景通过','good']:['报告已过期，待重测','warning'];
    const labels={identity_observed:'原版身份已核实',inventory_observed:'入口目录已发现',failed:'执行失败',failed_difference:'双方行为有差异',failed_missing_traffic:'真实报文不足',failed_baseline:'原版基线不匹配',blocked_baseline:'原版基线不可用',blocked_environment:'缺少执行条件',not_selected:'本轮未执行',not_run:'尚未执行',stale_source:'执行期间源码变化'};
    return [labels[probe.state]||probe.state,/^(failed|blocked|stale)/.test(probe.state)?'warning':'neutral'];
  }
  /* 成对徽标仅绑定探针 ID，不写入 全部原条目的实现状态或本地验证索引。 */
  function conformanceProbeBadge(probe) {const [label,color]=conformanceProbeState(probe);return `<span class="badge ${color}" data-conformance-probe="${escape(probe.id)}">${escape(label)}</span>`;}
  /* 面板统计明确分开对子场景通过、身份/发现和其他状态，不计算总体兼容率。 */
  function conformanceCounts() {
    const data=view.conformance;if(!data||view.conformanceError)return '';
    const green=data.probes.filter(probe=>conformanceProbeState(probe)[1]==='good').length;
    const discovery=data.probes.filter(probe=>['identity_observed','inventory_observed'].includes(probe.state)).length;
    const failed=data.probes.filter(probe=>probe.state.startsWith('failed')).length;
    const pending=data.probes.length-green-discovery-failed;
    return `<span><strong>${count(data.probes.length)}</strong>实际记录的探针</span><span><strong>${count(green)}</strong>有效的成对子场景通过</span><span><strong>${count(failed)}</strong>行为差异或执行失败</span><span><strong>${count(discovery)}</strong>身份或入口发现</span><span><strong>${count(pending)}</strong>未执行或待复核</span><span class="badge warning">完整兼容尚未通过</span>${Date.now()>=Date.parse(data.expires_at)?'<span class="badge warning">报告已过复核期限</span>':''}`;
  }
  /* 明确显示全部原始 API 注册和缺口数量；入口发现不授予命令语义通过。 */
  function conformanceInventory() {
    const inventory=view.conformance?.api_inventory||{},states=inventory.counts_by_state||{};
    const rows=[['原始 API 注册记录',inventory.source_registration_entries],['原版本次运行目录',inventory.reference_runtime_rows],['RustSwitch 本次运行目录',inventory.candidate_runtime_rows],['原版模块未加载，待补充基线',states.blocked_reference_module],['RustSwitch 尚缺入口',states.missing_candidate_entrypoint],['入口存在，完整语义待验证',states.requires_semantic_verification]];
    const number=value=>Number.isSafeInteger(value)&&value>=0?count(value):'尚未采集';
    return `<h3>API 入口发现</h3><p>入口存在或模块已加载只证明本次能够发现，不能作为命令语义、通信协议或完整功能通过的证据。</p><div class="docs-table"><table><tbody>${rows.map(([label,value])=>`<tr><th>${escape(label)}</th><td>${number(value)}</td></tr>`).join('')}</tbody></table></div>`;
  }
  /* 独立报告保留全部探针及范围；缺失状态仍提供固定文档入口供后续复核。 */
  function conformanceSummary() {
    const links=`<div class="docs-toolbar"><a href="#api-docs?document=api%2Fconformance-report.md" class="button button-secondary">查看完整成对报告</a><a href="#api-docs?document=api%2Fconformance-testing.md" class="button button-secondary">测试用例与判定标准</a><a href="#api-docs?document=api%2Fesl-reference.md" class="button button-secondary">ESL 接口说明</a><a href="${escape(href('api-docs',{document:versionedReport('api/ai-calling-verification','api/ai-calling-verification.md')}))}" class="button button-secondary">AI 呼叫成对实测</a></div>`;
    if(!view.conformance||view.conformanceError)return `<div class="panel docs-content docs-conformance"><h2>与 FreeSWITCH 原版成对验证</h2><div class="notice warning"><div class="notice-content">${escape(view.conformanceError||'尚未取得可核对的成对报告。')}</div><button class="button button-secondary button-small" data-doc-action="retry">重新读取报告</button></div>${links}</div>`;
    const data=view.conformance;
    return `<div class="panel docs-content docs-conformance"><h2>与 FreeSWITCH 原版成对验证</h2><p>原版已真实运行。下面每个绿色只覆盖该探针列出的双方对照场景；身份核实、入口发现和原始 ${count(data.original_entries)} 条目录不会因此整体标绿。</p><div class="docs-counts" id="docs-conformance-counts" aria-label="成对探针统计">${conformanceCounts()}</div><p>参考 FreeSWITCH ${escape(data.reference.version)} · 完成：${escape(data.finished_at)}<br>复核期限：${escape(data.expires_at)}</p>${links}<details class="docs-detail"><summary>逐个查看 ${count(data.probes.length)} 个探针及验证范围</summary><div class="docs-table"><table><thead><tr><th>探针</th><th>实际状态</th><th>限定验证范围</th></tr></thead><tbody>${data.probes.map(probe=>`<tr><td><code>${escape(probe.id)}</code></td><td>${conformanceProbeBadge(probe)}</td><td>${escape(probe.scope)}</td></tr>`).join('')}</tbody></table></div></details><details class="docs-detail"><summary>API 入口发现及报告指纹</summary>${conformanceInventory()}<p class="docs-raw-meta">参考提交：${escape(data.reference.commit)}<br>源码快照 SHA-256：${escape(data.source_snapshot_sha256)}<br>原始报告 SHA-256：${escape(data.report_sha256)}</p><a href="#api-docs?document=api%2Fconformance-report.json">查看完整采集报告</a></details></div>`;
  }
  /* 每次展示/筛选都重新检查有效期；浏览器中的历史缓存不能继续沿用到期绿色。 */
  function runtimeState(entry) {
    const row=view.runtimeIndex.get(entry.id),proof=row&&view.runtimeProofs.get(row.verification_id);
    if(view.runtimeError||!row)return {status:'missing_evidence',scope:'尚未取得可核对的运行证据',reason:view.runtimeError||'该条目没有运行证据记录',proof:null};
    const expiresAt=row.effective_expires_at||proof?.expires_at;
    if(row.green_eligible&&(!proof||!Number.isFinite(Date.parse(row.effective_expires_at))||Date.now()>=Date.parse(row.effective_expires_at)||!Number.isFinite(Date.parse(proof.expires_at))||Date.now()>=Date.parse(proof.expires_at)))return {status:'stale',scope:row.scope,reason:'至少一项必需证据超过复核期限，必须重新运行后更新发布包',proof,expiresAt};
    return {status:row.local_status,scope:row.scope,reason:row.reason,proof,expiresAt};
  }
  /* 5000路仅显示附加场景标签，万路目标仍沿用原实现状态，不互相替代。 */
  function runtimeBadge(entry) { const result=runtimeState(entry),[label,color]=runtimeStatuses[result.status]||runtimeStatuses.missing_evidence;return `<span class="badge ${color}" data-runtime-entry="${escape(entry.id)}">${escape(result.status==='passed'&&result.proof?.kind==='capacity_5000'?'5000路本地场景通过（万路待验证）':label)}</span>`; }
  /* 明确展示真实测试名、源码/日志指纹与期限；静态定义数量不计为执行结果。 */
  function runtimeMarkup(entry) {
    const result=runtimeState(entry),proof=result.proof;
    return `<h3>独立本地运行验证</h3>${runtimeBadge(entry)}<p><strong>限定范围：</strong>${escape(result.scope)}</p><p>${escape(result.reason||'')}</p><p>本地证据不承担成对认证；另见本页成对报告。生产容量仍需独立验收。</p>${proof?`<p>执行完成：${escape(proof.finished_at||'未知')}<br>复核期限（所有必需范围取最早）：${escape(result.expiresAt||'当前证据不满足通过门禁')}</p><p>实际要求测试：${proof.test_names.map(name=>`<code>${escape(name)}</code>`).join('、')}</p><p class="docs-raw-meta">源码快照SHA-256：${escape(proof.source_snapshot_sha256||'缺失')}<br>日志SHA-256：${escape(proof.log?.sha256||'缺失')}</p><details class="docs-detail"><summary>完整本地运行声明</summary>${code(proof,'Runtime Verification')}</details>`:''}`;
  }
  /* 计数逐ID重新走运行门禁，已到期的绿色不能继续留在首屏统计中。 */
  function runtimeTotals() {
    const totals=Object.fromEntries(Object.keys(runtimeStatuses).map(status=>[status,0]));
    for(const entry of view.comparison.entries){const status=runtimeState(entry).status;totals[status]=(totals[status]||0)+1;}
    return totals;
  }
  /* 增量属于已发布版本；打开页面后证据到期时隐藏旧增量，防止它继续暗示当前通过。 */
  function progressSummary(totals) {
    const data=view.progress,links=`<a href="${escape(href('api-docs',{document:'api/compatibility-worklist.md'}))}" class="button button-secondary button-small">全部缺口推进清单</a>`;
    const unavailable=view.progressError||(!data?'当前发布包尚未提供可核对的本轮增量。':!countsEqual(data.current.local_counts,totals)?'当前证据状态已变化，本轮增量须重新核对后发布。':'');
    if(unavailable)return `<div class="notice"><div class="notice-content"><strong>本轮变化：暂不提供数量</strong><br>${escape(unavailable)}</div>${links}</div>`;
    const changes=data.changes;
    const list=(rows,label,field)=>`<h3>${label}（${count(rows.length)}）</h3>${rows.length?`<ul>${rows.slice(0,100).map(row=>`<li>${view.comparison.entries.some(entry=>entry.id===row.id)?`<a href="${escape(href('fs-comparison',{entry:row.id}))}">${escape(row.id)} · ${escape(row.name)}</a>`:`<span>${escape(row.id)} · ${escape(row.name)}</span>`}<br>${escape(field(row))}</li>`).join('')}</ul>${rows.length>100?'<p>此处预览前 100 条，全部记录见完整进展文件。</p>':''}`:'<p>本轮此类变化为 0 条。</p>'}`;
    return `<details id="docs-progress-details" class="panel docs-content" style="margin-bottom:18px;padding:16px 20px"><summary style="cursor:pointer;color:var(--teal)">相对 ${escape(data.baseline.version)}：新增限定通过 <strong>${count(changes.new_green.length)}</strong>，撤回 <strong>${count(changes.lost_green.length)}</strong> · 展开逐项变化</summary><p>已发布 ${escape(data.baseline.version)} → ${escape(data.current.version)}：限定本地通过 ${count(data.baseline.green_entries)} → ${count(data.current.green_entries)}。这些变化不表示完整原版契约通过。</p>${list(changes.new_green,'新增限定通过',row=>row.scope)}${list(changes.lost_green,'撤回本地通过',row=>row.reason)}${list(changes.implementation_changed,'实现状态变化',row=>`${statuses[row.from]?.[0]||row.from} → ${statuses[row.to]?.[0]||row.to}`)}<p>目录新增 ${count(changes.added_ids.length)} 条，删除 ${count(changes.removed_ids.length)} 条；分母变化与验证变化分别记录。</p><div class="docs-toolbar">${links}<a href="${escape(href('api-docs',{document:'api/compatibility-progress.json'}))}" class="button button-secondary button-small">完整进展与来源指纹</a></div></details>`;
  }
  /* 四张首屏卡片只计当前本地证据；完整契约与原版探针使用另外两组统计。 */
  function runtimeSummary() {
    const totals=runtimeTotals(),available=!!view.runtime&&!view.runtimeError,invalid=totals.stale+totals.missing_evidence+totals.unbound;
    const cards=[['passed','本地验证通过',totals.passed,'仅当前证据列出的限定范围','good'],['not_run','本地尚未执行',totals.not_run,'尚无绑定当前源码的运行结果','neutral'],['failed','本地验证失败',totals.failed,'当前所需测试未通过','warning'],['invalid','证据失效或不可用',invalid,'过期、源码变化、缺失或未绑定','warning']];
    return `<div class="docs-toolbar"><h2 style="font-size:18px;margin-right:auto">当前本地验证进展</h2><span class="badge neutral">发布 ${escape(view.manifest?.version||'未知')}</span><button class="button button-secondary button-small" data-doc-action="retry">刷新验证记录</button></div><div class="metrics-grid" aria-label="当前本地验证四项统计">${cards.map(([status,label,total,note,color])=>`<a class="metric-card" href="${escape(href('fs-comparison',{runtime:status}))}" data-runtime-count="${status}" aria-label="${escape(label)} ${available?count(total):'尚未取得'} 条"><div class="metric-top">${label}</div><div class="metric-value" style="color:${status==='passed'&&available&&total>0?'var(--teal)':'inherit'}">${available?count(total):'—'}<small>条</small></div><div class="metric-bottom">${note}</div></a>`).join('')}</div><div class="notice ${view.runtimeError?'warning':''}"><div class="notice-content"><strong>以上是本地运行结果；完整契约待验收、原版成对探针分别统计。</strong><br>${view.runtimeError?escape(view.runtimeError):`全量 ${count(view.comparison.entries.length)} 条目录按原ID计数，重复配置不等于独立功能。本地子集通过不会自动减少“完整契约待验收”数量。`}</div></div>${progressSummary(totals)}<details id="docs-runtime-detail" class="docs-detail" style="margin-bottom:18px"><summary>本地证据细分与更新日期</summary><div class="docs-counts" aria-label="本地运行验证细分统计">${Object.entries(runtimeStatuses).map(([status,[label]])=>`<span>${escape(label)} <strong>${available?count(totals[status]):'—'}</strong></span>`).join('')}</div><p class="reference-note">运行证据复核日期：${escape(view.runtime?.evaluated_on||'尚未取得')}。过期证据会自动撤绿；“刷新验证记录”只读取当前已发布结果，不执行测试。</p></details>`;
  }
  /* 期限到达时只替换可见徽标和统计，不重绘卡片、输入、折叠状态或滚动位置。 */
  function scheduleRuntimeExpiry() {
    clearTimeout(view.expiryTimer);if(view.page!=='fs-comparison')return;
    const deadlines=[...view.runtimeIndex.values()].filter(row=>row.green_eligible).map(row=>Date.parse(row.effective_expires_at)).filter(time=>Number.isFinite(time)&&time>Date.now());
    if(view.conformance&&!view.conformanceError&&Date.parse(view.conformance.expires_at)>Date.now())deadlines.push(Date.parse(view.conformance.expires_at));
    if(!deadlines.length)return;
    view.expiryTimer=setTimeout(()=>{
      if(view.page!=='fs-comparison')return;
      for(const node of document.querySelectorAll('[data-runtime-entry]')){const entry=view.comparison.entries.find(row=>row.id===node.dataset.runtimeEntry);if(entry)node.outerHTML=runtimeBadge(entry);}
      const summary=document.getElementById('docs-runtime-summary');if(summary){const expanded=[...(summary.querySelectorAll?.('details[open]')||[])].map(node=>node.id);summary.innerHTML=runtimeSummary();for(const id of expanded){const detail=document.getElementById(id);if(detail)detail.open=true;}}
      for(const node of document.querySelectorAll('[data-conformance-probe]')){const probe=view.conformance?.probes.find(row=>row.id===node.dataset.conformanceProbe);if(probe)node.outerHTML=conformanceProbeBadge(probe);}
      const paired=document.getElementById('docs-conformance-counts');if(paired)paired.innerHTML=conformanceCounts();scheduleRuntimeExpiry();
    },Math.min(86400000,Math.max(1,Math.min(...deadlines)-Date.now()+1)));
  }
  /* 单条对照采用同一组标题展示两侧语义，详细差异完整保留而不省略内容。 */
  /* 按原ID取证，源码检查、定义存在与原版运行结果分别显示。 */
  function verificationMarkup(entry) {
    const row=view.ledgerIndex.get(entry.id);
    if(!row)return runtimeMarkup(entry)+`<h3>逐项验证账本</h3><p>${escape(view.ledgerError || '未取得此项账本，不能据此推断已通过。')}</p>`;
    const tests=row.local_test_coverage || {},direct=tests.direct_definition_ids || [];
    return runtimeMarkup(entry)+`<h3>逐项验证账本</h3><div class="docs-summary"><p>本地实现：${escape(row.implementation_label)}；源码审查：${row.local_evidence_ids?.length?'有对应证据':'仅边界审查'}。</p><p>直接本地用例定义：${count(direct.length)}；本条账本原版证据：${row.upstream_runtime_verification?.executed?'见原始证据':'未关联'}；完整条目契约：${row.paired_verification?.status==='not_run'?'尚未验收':escape(row.paired_verification?.status)}。新成对子场景见本页独立报告。</p><p>用例定义存在不代表本轮执行，也不证明原接口全部断言通过。${escape(row.current_review_note || '')}</p></div><p><strong>当前差距：</strong>${escape(row.gap_reason)}</p><p><strong>后续验证：</strong>${escape(row.required_next_step)}</p>${direct.length?`<p>直接用例：${direct.map(item=>`<code>${escape(item)}</code>`).join('、')}</p>`:''}<details class="docs-detail"><summary>此项完整验证记录</summary>${code(row,'Verification Ledger Entry')}</details>`;
  }

  function comparisonCard(entry) { const source=officialURL(entry.source_url),document=semanticLink(entry.document_path),selected=view.entry===entry.id;return `<article class="panel docs-compare-card ${selected?'docs-focus':''}" id="comparison-${escape(entry.id)}"><div class="docs-compare-head"><div><h2>${escape(entry.name||entry.id)}</h2><div class="docs-compare-id">${escape(entry.id)} · ${escape(categoryLabel(entry.category))}${entry.module?' · '+escape(entry.module):''}</div></div>${statusBadge(entry.status)}</div><p>${runtimeBadge(entry)}</p><div class="docs-compare-columns"><div class="docs-compare-side"><h3>FREESWITCH ${escape(view.comparison.reference_version||'')}</h3><code>${escape(text(entry.fs_interface))}</code>${entry.fs_syntax?`<p>调用或配置形式</p><code>${escape(text(entry.fs_syntax))}</code>`:''}<p>${escape(text(entry.fs_semantics))}</p></div><div class="docs-compare-side rustswitch"><h3>RUSTSWITCH</h3><code>${escape(text(entry.rustswitch_interface))}</code><p>${escape(text(entry.rustswitch_semantics))}</p></div></div><details class="docs-compare-details" ${selected?'open':''}><summary>完整差异、验证状态与来源</summary><h3>对应关系</h3><p>${escape(relationshipLabel(entry.relationship))}</p><h3>全部差异</h3>${Array.isArray(entry.differences)?`<ul>${entry.differences.map(item=>'<li>'+escape(text(item))+'</li>').join('')}</ul>`:`<p>${escape(text(entry.differences))}</p>`}<h3>验证依据与限制</h3>${typeof entry.verification==='object'?code(entry.verification,'完整 verification'): `<p>${escape(text(entry.verification))}</p>`}${verificationMarkup(entry)}<h3>来源位置</h3><p><code>${escape(entry.source_path||'未提供路径')}${entry.source_line?':'+escape(entry.source_line):''}</code></p><div class="docs-compare-footer">${source?`<a class="button button-secondary button-small" href="${escape(source)}" target="_blank" rel="noopener noreferrer">官方源文件</a>`:entry.source_url?`<span class="docs-source-muted">来源引用：${escape(entry.source_url)}</span>`:''}${document?`<a class="button button-secondary button-small" href="${escape(document)}">阅读语义文档</a>`:entry.document_path?`<span class="docs-source-muted">文档引用：${escape(entry.document_path)}</span>`:''}<button class="button button-secondary button-small" data-doc-action="entry-link" data-doc-entry="${escape(entry.id)}">复制此项链接</button></div><details class="docs-detail"><summary>完整原始对照条目</summary>${code(entry,'Comparison Entry')}</details></details></article>`; }
  /* 搜索预先构建字符串索引，筛选覆盖全量条目，DOM 始终只生成一页。 */
  function filteredComparison() { const query=view.search.trim().toLowerCase();return view.comparison.entries.filter(entry=>(view.category==='all'||entry.category===view.category)&&(view.status==='all'||entry.status===view.status)&&(view.runtimeStatus==='all'||(view.runtimeStatus==='invalid'?['stale','missing_evidence','unbound'].includes(runtimeState(entry).status):runtimeState(entry).status===view.runtimeStatus))&&(!query||entry.searchText.includes(query))); }
  /* 分页保留 URL；直接链接某项时先定位其所在页，再展开该项。 */
  function comparisonResults(locate=false) { const root=document.getElementById('docs-comparison-results');if(!root)return;const entries=filteredComparison();if(locate&&view.entry){const index=entries.findIndex(entry=>entry.id===view.entry);if(index>=0)view.pageNumber=Math.floor(index/view.pageSize)+1;}const totalPages=Math.max(1,Math.ceil(entries.length/view.pageSize));view.pageNumber=Math.min(Math.max(1,view.pageNumber),totalPages);const visible=entries.slice((view.pageNumber-1)*view.pageSize,view.pageNumber*view.pageSize);for(const key of view.codes.keys()){if(!view.persistentCodes.has(key))view.codes.delete(key);}const pagebar=`<div class="docs-pagination"><span>${count(entries.length)} 条匹配 · 第 ${view.pageNumber} / ${totalPages} 页 · 每页 ${view.pageSize} 条</span><div class="pagination-controls"><button class="button button-secondary button-small" data-doc-action="comparison-prev" ${view.pageNumber<=1?'disabled':''}>上一页</button><label>跳至 <input class="docs-page-input" id="docs-page-number" type="number" min="1" max="${totalPages}" value="${view.pageNumber}" aria-label="跳转页码"></label><button class="button button-secondary button-small" data-doc-action="comparison-go">跳转</button><button class="button button-secondary button-small" data-doc-action="comparison-next" ${view.pageNumber>=totalPages?'disabled':''}>下一页</button></div></div>`;root.innerHTML=pagebar+visible.map(comparisonCard).join('')+(visible.length?'':'<div class="empty-state"><h2>没有符合条件的对照</h2><p>尝试调整名称、模块、分类或状态。</p><button class="button button-secondary" data-doc-action="comparison-clear" style="margin-top:16px">清除筛选</button></div>')+`<p class="reference-note">已在全量 ${count(view.comparison.entries.length)} 条记录中筛选，仅渲染当前 ${visible.length} 条。字段与语言重复条目不代表独立功能或认证通过数量。</p>`;remember();if(locate&&view.entry)requestAnimationFrame(()=>{if(view.page==='fs-comparison')document.getElementById('comparison-'+view.entry)?.scrollIntoView({block:'start'});}); }
  /* 对照总览展示实际分母和分状态数量，不用目录规模推导兼容百分比。 */
  function renderComparison() {
    view.codes.clear();const data=view.comparison,categories=[...new Set(data.entries.map(entry=>entry.category||'未分类'))].sort(),implementationCounts={};
    for(const entry of data.entries)implementationCounts[entry.status]=(implementationCounts[entry.status]||0)+1;
    document.getElementById('docs-root').innerHTML=`${productDirection()}<section id="docs-runtime-summary" aria-label="本地运行验证统计与边界">${runtimeSummary()}</section><section id="docs-conformance-summary" aria-label="与 FreeSWITCH 原版的成对验证报告">${conformanceSummary()}</section><details class="panel docs-content" style="padding:15px 20px;margin-bottom:18px"><summary style="cursor:pointer;font-size:12px;color:var(--teal)">实现与完整契约目录：${count(data.entries.length)} 条 · 完整契约待验收 ${count(implementationCounts.not_verified||0)} 条</summary><p>此处记录实现覆盖与完整契约状态，不是本轮测试结果。“完整契约待验收”包含已有用例定义及尚待认证的容量目标，不能解释为尚无验收定义，也不能与本地未执行数相加。</p><div class="docs-counts" aria-label="静态实现状态统计">${Object.entries(implementationCounts).map(([status,total])=>`<span>${escape((statuses[status]||[status])[0])} <strong>${count(total)}</strong></span>`).join('')}</div><p>${escape(text(data.scope||view.manifest.scope))}</p><p class="docs-raw-meta">FreeSWITCH ${escape(data.reference_version)} · ${escape(data.reference_commit||'未提供 commit')}</p>${code(data.summary||{},'完整目录统计口径')}</details><div class="docs-toolbar"><input class="search-input" id="docs-comparison-search" value="${escape(view.search)}" placeholder="全量搜索名称、模块、接口、语义或差异…" aria-label="搜索全部 FreeSWITCH 对照"><select class="select-input" id="docs-comparison-category" aria-label="对照分类"><option value="all">全部分类</option>${categories.map(category=>`<option value="${escape(category)}" ${view.category===category?'selected':''}>${escape(categoryLabel(category))}</option>`).join('')}</select><select class="select-input" id="docs-comparison-status" aria-label="实现状态"><option value="all">全部状态</option>${Object.entries(statuses).map(([value,[label]])=>`<option value="${value}" ${view.status===value?'selected':''}>${label}</option>`).join('')}</select><select class="select-input" id="docs-comparison-runtime" aria-label="本地验证状态"><option value="all">全部本地验证状态</option><option value="invalid" ${view.runtimeStatus==='invalid'?'selected':''}>证据失效或不可用</option>${Object.entries(runtimeStatuses).map(([value,[label]])=>`<option value="${value}" ${view.runtimeStatus===value?'selected':''}>${label}</option>`).join('')}</select><a href="#api-docs?document=api%2Fruntime-verification.md" class="button button-secondary">绿色证据与范围</a><a href="${escape(href('api-docs',{document:versionedReport('api/release-validation','api/runtime-verification.md')}))}" class="button button-secondary">本轮实测记录</a><a href="#api-docs?document=api%2Fverification-ledger.md" class="button button-secondary">逐项验证账本</a><a href="/v1/docs/comparison.json" download="freeswitch-comparison.json" class="button button-secondary">下载完整对照</a><a href="/v1/docs/export.zip" download="rustswitch-documents.zip" class="button button-primary">下载文档包</a></div><section id="docs-comparison-results" aria-label="对照检索结果"></section>`;view.persistentCodes=new Set(view.codes.keys());comparisonResults(true);scheduleRuntimeExpiry(); }
  /* mount 由主页面调用；所有数据加载完成后再次检查代次，避免导航竞争。 */
  async function mount(page,force=false) {
    if(!force&&view.page===page&&view.routeKey===location.hash&&document.getElementById('docs-root'))return;
    view.routeKey=location.hash;const generation=++view.generation;view.readGeneration++;clearTimeout(view.debounce);readRoute(page);
    document.getElementById('page-content').innerHTML='<div class="docs-root" id="docs-root">'+loading()+'</div>';
    try{
      const [manifest,data,ledgerResult]=await Promise.all([load('manifest','/v1/docs'),load(page,page==='api-docs'?'/v1/docs/openapi.json':'/v1/docs/comparison.json'),page==='fs-comparison'?load('verification-ledger','/v1/docs/file?path=api%2Fverification-ledger.json').then(value=>({value})).catch(error=>({error:error.message})):Promise.resolve(null)]);
      if(view.generation!==generation||view.page!==page)return;
      if(!Array.isArray(manifest.documents))throw new Error('文档目录响应缺少 documents 列表。');view.manifest=manifest;
      if(page==='api-docs'){
        if(!data.paths)throw new Error('OpenAPI 响应缺少 paths。');view.api=data;buildOperations();renderAPI();
      }else{
        if(!Array.isArray(data.entries))throw new Error('对照响应缺少 entries 列表。');
        /* 运行证据依赖索引SHA，读取失败只禁用绿色；异步旧响应不能覆盖用户的新页面。 */
        const [runtime,conformance,progress]=await Promise.all([loadRuntime(manifest,data).then(value=>({value})).catch(error=>({error:error.message})),loadConformance(manifest,data).then(value=>({value})).catch(error=>({error:error.message})),loadProgress(manifest).then(value=>({value})).catch(error=>({error:error.message}))]);
        /* 进展验证依赖已经通过门禁的运行文件；失败只隐藏增量，不影响独立的运行或原版报告。 */
        let checkedProgress=null,progressError=progress.error||'';
        if(progress.value)try{checkedProgress=await validateProgressView(progress.value,data,runtime.value?.data,manifest);}catch(error){progressError=error.message;}
        if(view.generation!==generation||view.page!==page)return;
        view.progress=checkedProgress;view.progressError=progressError;
        view.conformance=conformance.value||null;view.conformanceError=conformance.error||'';
        view.runtime=runtime.value?.data||null;view.runtimeIndex=runtime.value?.rows||new Map();view.runtimeProofs=runtime.value?.proofs||new Map();view.runtimeError=runtime.error||'';
        view.ledger=ledgerResult?.value;view.ledgerError=ledgerResult?.error||'';view.ledgerIndex=new Map((view.ledger?.entries || []).map(row=>[row.id,row]));
        view.comparison={...data,entries:data.entries.map(entry=>{const record={...entry},proof=runtimeState(entry);Object.defineProperty(record,'searchText',{value:(JSON.stringify(entry)+' '+(statuses[entry.status]?.[0]||'')+' '+categoryLabel(entry.category)+' '+relationshipLabel(entry.relationship)+' '+proof.scope+' '+(runtimeStatuses[proof.status]?.[0]||'')+' '+(proof.proof?.test_names||[]).join(' ')).toLowerCase(),enumerable:false});return record;})};renderComparison();
      }
    }catch(error){if(view.generation===generation&&view.page===page)document.getElementById('docs-root').innerHTML=failure(error);}
  }
  /* 离开页面使所有读取失效；缓存可复用，但迟到响应不能重绘管理表单。 */
  function leave() { if(view.page){view.page='';view.generation++;view.readGeneration++;clearTimeout(view.debounce);clearTimeout(view.expiryTimer);view.codes.clear();view.schemas.clear();} }
  /* 剪贴板降级仍要求显式点击；浏览器拒绝时显示错误，不声称复制成功。 */
  async function copy(value) { try{if(navigator.clipboard?.writeText)await navigator.clipboard.writeText(value);else{const textarea=document.createElement('textarea');textarea.value=value;textarea.style.position='fixed';textarea.style.opacity='0';document.body.append(textarea);textarea.select();const copied=document.execCommand('copy');textarea.remove();if(!copied)throw new Error('浏览器拒绝复制。');}feedback('已复制，可粘贴查看；没有执行示例。');}catch{feedback('复制失败，请在正文中选择内容后手动复制。',true);} }
  /* 文档按钮只分派本地渲染、复制和只读重试，不识别任何服务端脚本指令。 */
  document.addEventListener('click',event=>{const button=event.target.closest('[data-doc-action]');if(!button||button.disabled||!view.page)return;event.preventDefault();const action=button.dataset.docAction;if(action==='copy'){const value=view.codes.get(button.dataset.docCode);if(value!==undefined)copy(value);}else if(action==='copy-link')copy(location.href);else if(action==='retry'){view.cache.delete(view.page);view.cache.delete('manifest');view.cache.delete('verification-ledger');mount(view.page,true);}else if(action==='retry-document'){view.files.delete(view.document);openDocument(view.document);}else if(action==='api-tab'){view.tab=button.dataset.docTab;view.search='';view.anchor='';view.document='';view.schema='';remember();renderAPI();}else if(action==='schema-expand'){const schema=view.schemas.get(button.dataset.docSchema);if(schema){const wrapper=button.closest('.docs-schema');wrapper.innerHTML=schemaView(schema,'深层结构');}}else if(action==='entry-link'){const parameters={search:view.search,category:view.category,status:view.status,runtime:view.runtimeStatus,page:view.pageNumber,entry:button.dataset.docEntry};copy(location.href.split('#')[0]+href('fs-comparison',parameters));}else if(action==='comparison-clear'){view.search='';view.category='all';view.status='all';view.runtimeStatus='all';view.entry='';view.pageNumber=1;renderComparison();}else if(action.startsWith('comparison-')){if(action==='comparison-prev')view.pageNumber--;else if(action==='comparison-next')view.pageNumber++;else if(action==='comparison-go')view.pageNumber=Number.parseInt(document.getElementById('docs-page-number').value,10)||1;else return;view.entry='';comparisonResults();document.getElementById('docs-comparison-results').scrollIntoView({block:'start'});}});
  /* 输入采用短防抖；只更新结果区域，浏览器 IME 组合输入完成后才触发搜索。 */
  function searchInput(event) { const input=event.target;if(event.isComposing||!['docs-api-search','docs-comparison-search'].includes(input.id))return;const page=view.page,generation=view.generation;clearTimeout(view.debounce);view.debounce=setTimeout(()=>{if(view.page!==page||view.generation!==generation)return;view.search=input.value;view.anchor='';view.entry='';view.pageNumber=1;if(page==='api-docs')filterAPI();else comparisonResults();},160); }
  document.addEventListener('input',searchInput);
  document.addEventListener('compositionend',searchInput);
  /* 分类和状态筛选与分页独立，不把数千条记录全部展开到 DOM。 */
  document.addEventListener('change',event=>{const input=event.target;if(view.page!=='fs-comparison')return;if(input.id==='docs-comparison-category')view.category=input.value;else if(input.id==='docs-comparison-status')view.status=input.value;else if(input.id==='docs-comparison-runtime')view.runtimeStatus=input.value;else return;view.pageNumber=1;view.entry='';comparisonResults();});
  return {mount,leave};
})();
