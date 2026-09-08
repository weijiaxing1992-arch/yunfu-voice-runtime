'use strict';

/* 测试模块只在操作者点击开始后创建任务；导航和轮询只读，不停止后台任务。 */
window.RustSwitchTests = (() => {
  const terminal = new Set(['completed', 'failed', 'stopped']);
  const statusNames = {preparing:'正在准备',running:'正在运行',stopping:'正在停止',completed:'已完成',failed:'失败',stopped:'已停止'};
  const phaseNames = {preflight:'资源预检',starting:'发生器启动',setup:'建立通话',dialing:'建立通话',media:'双向媒体',teardown:'拆线与清理',finished:'任务结束'};
  const storageKey = 'rustswitch-tests-pending-request-v1';
  /* 表单、任务详情与报告独立保存；状态采集不会重建输入框或改变本地参数。 */
  const view = {active:false,generation:0,request:null,timer:null,polling:false,snapshot:null,run:null,selectedId:'',explicitSelection:false,mode:'pressure',error:'',readError:'',detailError:'',selectionNotice:'',online:false,updatedAt:null,pending:false,stopping:false,uncertain:null,reportError:'',reports:new Map(),drafts:{pressure:{concurrency:500,cps:100,duration_seconds:10,payload:0,media_processing:"relay",custom:false},phone:{concurrency:1,cps:1,duration_seconds:30,payload:0,media_processing:"relay"}}};
  const html = value => String(value ?? '').replace(/[&<>"']/g, character => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[character]));
  const count = (value, digits = 0) => value == null || !Number.isFinite(Number(value)) ? '—' : Number(value).toLocaleString('zh-CN',{maximumFractionDigits:digits});
  const date = value => value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString('zh-CN',{hour12:false}) : '—';
  const ended = run => !!run && terminal.has(run.status);
  const activeRun = () => view.snapshot?.current && !ended(view.snapshot.current) ? view.snapshot.current : view.run && !ended(view.run) ? view.run : null;
  const caps = () => view.snapshot?.capabilities || {};
  const modeName = mode => mode === 'phone' ? '测试小电话' : '压力模拟';
  const reportURL = id => `/v1/tests/${encodeURIComponent(id)}/report`;
  const badge = (text, kind = 'neutral') => `<span class="badge ${kind}">${html(text)}</span>`;
  /* 预检未启动与实际媒体失败分开，不从中文错误内容猜测。 */
  const preflightOnly = run => run?.failure?.stage === 'preflight' && run.failure.processes_started === false;
  const codecProfiles = () => caps().codec_profiles || [{payload:0,name:'PCMU',sample_rate:8000,rtp_clock_rate:8000},{payload:8,name:'PCMA',sample_rate:8000,rtp_clock_rate:8000}];
  const codecName = payload => codecProfiles().find(item=>item.payload===Number(payload))?.name || `PT ${payload}`;
  // 每次测试冻结自己的处理模式；旧报告未记录模式时保留未知，不能默认为转码。
  const processingName = mode => mode==='g711'?'G.711 双腿音频处理':mode==='relay'?'同编码 RTP 透传':'未记录（历史任务）';
  const testCodecProfiles = () => codecProfiles().filter(item=>view.drafts[view.mode].media_processing!=='g711' || [0,8].includes(item.payload));
  const codecOptions = () => testCodecProfiles().map(item=>`<option value="${item.payload}" ${Number(view.drafts[view.mode].payload)===item.payload?'selected':''}>${html(item.name)} · 音频 ${count(item.sample_rate)} Hz / RTP ${count(item.rtp_clock_rate)} Hz</option>`).join('');
  // 能力刷新仅更新选择列表，不重建数字输入；后端不支持的草稿保持可见并由提交校验拒绝。
  function updateTestProfiles() {
    const draft=view.drafts[view.mode], mode=document.getElementById('test-media_processing'), payload=document.getElementById('test-payload');
    if(mode)mode.innerHTML=['relay','g711'].map(value=>`<option value="${value}" ${draft.media_processing===value?'selected':''} ${value==='g711'&&!caps().media_processing_modes?.includes('g711')?'disabled':''}>${processingName(value)}</option>`).join('');
    if(payload){payload.innerHTML=codecOptions();if(!testCodecProfiles().some(item=>item.payload===Number(draft.payload)))payload.innerHTML+=`<option value="${html(draft.payload)}" selected>当前模式不支持 PT ${html(draft.payload)}</option>`;}
  }
  const statusBadge = run => preflightOnly(run) ? badge('预检未启动','warning') : badge(statusNames[run?.status] || run?.status || '尚未开始',run?.status === 'failed' ? 'error' : run?.status === 'completed' ? 'good' : run && !ended(run) ? 'warning' : 'neutral');
  const summary = (name, value) => `<div class="tests-summary-line"><span>${html(name)}</span><strong>${html(value)}</strong></div>`;
  const metric = (name,value,note) => `<div class="tests-metric"><label>${html(name)}</label><strong>${html(value)}</strong><small>${html(note)}</small></div>`;
  // 零值只有在全部分片明确提供计数时才显示；旧报告缺支持字段保持未知，部分非零观测仍保留。
  function socketDropCount(sample) {
    const drops=sample?.socket_rx_drops, workers=sample?.worker_count, supported=sample?.socket_drop_counter_supported_workers;
    const valid=Number.isSafeInteger(drops)&&drops>=0;
    const complete=sample?.socket_drop_counter_supported===true&&Number.isInteger(workers)&&workers>0&&supported===workers;
    if(valid&&complete)return count(drops);
    return valid&&drops>0?`已记录 ${count(drops)}（完整计数未提供）`:'未提供';
  }
  // 补充统计不改变失败判定；超时或取消后保留的样本必须明确说明仍未证明退出后完整覆盖。
  function failureFinalSampling(report) {
    const value=report?.evidence?.final_status_sampling_outcome;
    if(value==='fresh'&&report.evidence.worker_stats_after_generator_exit===true)return '已取得退出后全部分片统计；原验收结论保留';
    if(value==='cancelled')return '补充统计已取消；保留最后样本，完整覆盖未确认';
    if(value==='deadline_exceeded')return '补充统计超时；保留最后样本，完整覆盖未确认';
    return '未提供退出后统计覆盖结论';
  }
  /* 建立错误按原因汇总，原始报告是原因到次数的对象，不是数组。 */
  const setupFailureCount = errors => errors && typeof errors === 'object' ? Object.values(errors).reduce((total,value)=>total+(Number.isFinite(value)?value:0),0) : null;

  /* 同一浏览器会话保留尚未确认的创建请求，网络超时后只能重试原 request_id。 */
  function restorePending() { try { const candidate = JSON.parse(sessionStorage.getItem(storageKey)); if (candidate && typeof candidate.request_id === 'string' && ['pressure','phone'].includes(candidate.mode) && Number.isInteger(candidate.concurrency)) view.uncertain = candidate; } catch { /* 浏览器禁用存储时仍保留本次页面内的幂等键。 */ } }
  function savePending(value) { view.uncertain = value; try { if (value) sessionStorage.setItem(storageKey,JSON.stringify(value)); else sessionStorage.removeItem(storageKey); } catch { /* 存储不可用不影响服务端幂等校验。 */ } }
  /* 生成不可预测的逻辑任务标识；失败时不降级为可碰撞的时间戳。 */
  function requestID() { if (window.crypto?.randomUUID) return window.crypto.randomUUID(); if (!window.crypto?.getRandomValues) throw new Error('浏览器不支持安全任务标识，请使用现代浏览器。'); const bytes = window.crypto.getRandomValues(new Uint8Array(16)); return Array.from(bytes,value=>value.toString(16).padStart(2,'0')).join(''); }
  /* 数字范围以服务端能力上限为准，页面规定的上限仍保留。 */
  function upper(key, fallback) { const value = Number(caps()[key]); return Number.isInteger(value) && value > 0 ? Math.min(value,fallback) : fallback; }
  function validateDraft(mode, draft) { const numeric = {concurrency:Number(draft.concurrency),cps:Number(draft.cps),duration_seconds:Number(draft.duration_seconds),payload:Number(draft.payload)}; const rules = [['concurrency','并发通话',upper('max_concurrency',10000)],['cps','每秒建立速率',upper('max_cps',1000)],['duration_seconds','媒体持续时间',upper('max_duration_seconds',300)]]; for (const [key,label,max] of rules) if (!Number.isInteger(numeric[key]) || numeric[key] < 1 || numeric[key] > max) throw new Error(`${label}需要填写 1 至 ${max} 之间的整数。`); if (!codecProfiles().some(item=>item.payload===numeric.payload) || (Array.isArray(caps().supported_payloads) && !caps().supported_payloads.includes(numeric.payload))) throw new Error('当前服务不支持所选编解码。'); if (Array.isArray(caps().modes) && !caps().modes.includes(mode)) throw new Error('当前服务不支持此测试模式。'); numeric.media_processing=draft.media_processing || 'relay'; if(!['relay','g711'].includes(numeric.media_processing) || (numeric.media_processing==='g711' && (!caps().media_processing_modes?.includes('g711') || ![0,8].includes(numeric.payload)))) throw new Error('G.711 音频处理需要当前服务支持，并选择 PCMU 或 PCMA。'); if (mode === 'phone') { numeric.concurrency = 1; numeric.cps = 1; } return numeric; }

  /* 整体框架仅在切换测试模式时重建；实时区域独立更新。 */
  function shell() {
    const root = document.getElementById('page-content');
    root.innerHTML = `<div id="tests-workspace"><div class="notice info"><div class="notice-content"><strong>真实本机 SIP 与双向媒体测试</strong><p>测试会启动独立控制实例、Rust 媒体进程和发生器，共用本机 CPU 与网卡。测试采用隔离容量模式，保留启动硬限制和媒体健康准入；同机结果不代表生产万路容量认证。</p></div></div><div class="tests-tabs" role="tablist" aria-label="测试类型"><button type="button" class="tests-tab ${view.mode==='pressure'?'active':''}" role="tab" aria-selected="${view.mode==='pressure'}" data-test-action="mode" data-mode="pressure">压力模拟</button><button type="button" class="tests-tab ${view.mode==='phone'?'active':''}" role="tab" aria-selected="${view.mode==='phone'}" data-test-action="mode" data-mode="phone">测试小电话</button></div><div id="tests-availability" aria-live="polite"></div><div class="tests-layout"><section class="panel tests-config"><div class="panel-header"><div><h2>${modeName(view.mode)}</h2><p>${view.mode==='phone'?'一条真实 SIP 会话，自动发送双向测试媒体':'按指定速率建立通话，保持媒体后自动拆线'}</p></div>${badge(view.mode==='phone'?'1 路通话':'本机发生器')}</div><div class="panel-body" id="tests-form-area"></div></section><div class="tests-results"><section class="panel"><div class="panel-body" id="tests-run"></div></section><section class="panel" id="tests-history"></section></div></div></div>`;
    root.querySelector('#tests-workspace').addEventListener('click',handleClick);
    root.querySelector('#tests-workspace').addEventListener('input',handleInput);
    root.querySelector('#tests-workspace').addEventListener('change',handleInput);
    renderForm(); renderAvailability(); renderRun(); renderHistory();
  }
  /* 所有表单字段均有中文单位；小电话不请求麦克风，不接受外部号码。 */
  function renderForm() {
    const target = document.getElementById('tests-form-area'); if (!target) return;
    const draft = view.drafts[view.mode], phone = view.mode === 'phone';
    const field = (key,label,max,help) => `<div class="form-field ${key==='duration_seconds' && phone?'tests-full':''}"><label class="field-label" for="test-${key}">${label}</label><input id="test-${key}" data-test-field="${key}" type="number" min="1" max="${max}" step="1" required value="${html(draft[key])}"><p class="tests-help">${help}</p></div>`;
    target.innerHTML = `<form id="tests-start-form"><div class="tests-form">${phone?'<div class="tests-full tests-phone-display"><div class="tests-phone-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M5 3h4l2 5-3 2a15 15 0 0 0 6 6l2-3 5 2v4a2 2 0 0 1-2 2A18 18 0 0 1 3 5a2 2 0 0 1 2-2Z"/></svg></div><strong>本机媒体回路</strong><p>SIP 建立 → 双向 RTP → 自动拆线<br>不使用麦克风，不拨打真实外部号码</p></div>':`<div class="form-field tests-full"><label class="field-label">并发规模 · 路</label><div class="tests-presets">${[500,1000,2000,3000,5000].map(value=>`<button type="button" class="tests-preset ${!draft.custom && Number(draft.concurrency)===value?'active':''}" data-test-action="preset" data-value="${value}" ${value>upper('max_concurrency',10000)?'disabled':''}>${count(value)}<small>路并发</small></button>`).join('')}<button type="button" class="tests-preset ${draft.custom?'active':''}" data-test-action="preset" data-value="custom">自定义<small>1—${count(upper('max_concurrency',10000))} 路</small></button></div><p class="tests-help">选择规模只修改参数。点击开始才会占用本机资源。</p></div>${field('concurrency','目标并发 · 路',upper('max_concurrency',10000),'每路包含双向媒体；实际建立结果以报告为准。')}${field('cps','建立速率 · 次/秒',upper('max_cps',1000),'发生器发起新呼叫的目标速率。')}`}${field('duration_seconds','媒体持续时间 · 秒',upper('max_duration_seconds',300),'全部呼叫建立后开始计时；总任务时长还包含建立和清理。')}<div class="form-field tests-full"><label class="field-label" for="test-media_processing">媒体处理模式</label><select id="test-media_processing" data-test-field="media_processing"></select><p class="tests-help">此选择独立于主服务配置。G.711 模式在两腿使用不同律编码，核对全部 160 个音频样本；当前仅支持 PCMA / PCMU、20 ms。</p></div><div class="form-field ${phone?'tests-full':''}"><label class="field-label" for="test-payload">音频编解码</label><select id="test-payload" data-test-field="payload">${codecProfiles().map(item=>`<option value="${item.payload}" ${Number(draft.payload)===item.payload?'selected':''}>${html(item.name)} · 音频 ${count(item.sample_rate)} Hz / RTP ${count(item.rtp_clock_rate)} Hz</option>`).join('')}</select><p class="tests-help">20 ms RTP 包间隔。透传模式核对原始字节；G.711 模式按固定量化误差核对全部音频样本，两种测试均不等于真人听感认证。</p></div></div><div class="tests-config-note">${phone?'拨号后自动发送测试媒体。可随时挂断；提前挂断记为已停止，不作为完整测试通过。':'该负载模型先建立全部通话，再同时发送媒体，最后统一拆线。它不覆盖持续呼叫建拆或独立发生器验收。'}</div><div id="tests-form-message" role="status"></div><div class="tests-form-actions"><button id="tests-start-button" class="button button-primary" type="submit">${phone?'拨号测试':'开始压力测试'}</button><button id="tests-phone-stop" class="button button-secondary" type="button" data-test-action="stop" ${phone?'':'hidden'}>挂断</button></div><p class="tests-help">同一时间仅运行一个测试任务。离开本页或刷新浏览器不会停止后台任务。</p></form>`;
    target.querySelector('form').addEventListener('submit',event=>{event.preventDefault();start();}); updateTestProfiles(); updateControls();
  }
  /* 状态更新仅调整按钮与错误区，不覆盖操作者正在填写的输入。 */
  function updateControls() {
    if (!view.active) return; const running = activeRun();
    const disabled = view.pending || !!running || !!view.uncertain || !view.snapshot?.enabled || !view.online;
    const button = document.getElementById('tests-start-button'); if (button) { button.disabled=disabled; button.textContent=view.pending?'正在提交…':running?'已有任务正在运行':view.mode==='phone'?'拨号测试':'开始压力测试'; }
    const stop = document.getElementById('tests-phone-stop'); if (stop) stop.disabled=!running || view.stopping || running.status==='stopping';
    const message = document.getElementById('tests-form-message'); if (message) message.innerHTML = view.error ? `<div class="tests-result-note error tests-error-message">${html(view.error)}</div>` : '';
    document.querySelectorAll('[data-test-action="retry-create"]').forEach(node=>node.disabled=view.pending || !view.online);
  }
  /* 可用性来自后端预检；缺二进制、端口或平台限制直接显示，不构造替代结果。 */
  function renderAvailability() {
    const node = document.getElementById('tests-availability'); if (!node) return;
    let message = !view.snapshot ? '<span class="tests-loading-text">正在读取测试能力与历史任务…</span>' : !view.snapshot.enabled ? `${badge('当前不可用','error')}<p class="tests-error-message">${html(view.snapshot.disabled_reason || '服务未启用测试运行能力。')}</p>` : `${badge('可创建独立测试','good')}<p>最多 ${count(upper('max_concurrency',10000))} 路 · ${count(upper('max_cps',1000))} 次/秒 · 媒体保持最多 ${count(upper('max_duration_seconds',300))} 秒</p>`;
    if (view.snapshot?.enabled && caps().port_capacity) { const capacity=caps().port_capacity; message += `<p>本机回退地址端口估计：${count(capacity.max_concurrency)} 路（按 ${count(capacity.cps)} 次/秒）；开始时按实际参数与端口占用重新预检。此数值只表示端口空间，不能作为性能结论。</p>`; }
    if (!view.online && view.readError) message = `${badge('读取失败','error')}<p class="tests-error-message">${html(view.readError)}<br>保留上次结果，数据尚未更新。</p>`;
    /* 单条历史过期或详情读取失败不代表整个服务离线，也不能锁死新任务入口。 */
    if (view.online && view.selectionNotice) message += `<p class="tests-help">${html(view.selectionNotice)}</p>`;
    if (view.online && view.detailError) message += `<p class="tests-error-message">任务详情暂未更新：${html(view.detailError)}；将自动重试。</p>`;
    node.innerHTML = `<div class="tests-control-line"><div class="tests-availability">${message}</div><button class="button button-secondary button-small" type="button" data-test-action="refresh">刷新状态</button></div>${view.uncertain?`<div class="tests-result-note warning"><strong>${view.pending?'正在提交测试请求':'创建结果尚未确认'}</strong>网络中断时，任务可能已在后台启动。重新读取状态或重试同一请求可避免重复创建。<p class="tests-local-request">请求标识：${html(view.uncertain.request_id)}</p><button class="button button-secondary button-small" type="button" data-test-action="retry-create" ${view.pending?'disabled':''}>重试同一请求</button></div>`:''}`;
    updateControls();
  }
  /* 仅由实际样本计算峰值；保留窗口与最终全程证据分开命名。 */
  function sampleMetrics(run, report) {
    const samples = Array.isArray(run.samples) ? run.samples : [], last = samples.at(-1);
    const peak = key => { const values=samples.map(sample=>sample[key]).filter(value=>typeof value==='number' && Number.isFinite(value)); return values.length?Math.max(...values):null; };
    /* 后端预置峰值为0；没有任何成功样本时，不能把初始化计数展示成已测得峰值。 */
    if (!samples.length) return {last,activePeak:null,establishedPeak:null,peakLabel:'未采集：尚无成功状态样本',journalPeak:report?.evidence?.journal_peak_established_calls};
    const fullActive=report?.evidence?.peak_sampled_active_calls, fullEstablished=report?.evidence?.peak_sampled_established_calls;
    const full=Number.isFinite(fullActive) && Number.isFinite(fullEstablished);
    return {last,activePeak:full?fullActive:peak('active_calls'),establishedPeak:full?fullEstablished:peak('established_calls'),peakLabel:full?'全程采样峰值':'当前采样窗口峰值',journalPeak:report?.evidence?.journal_peak_established_calls};
  }
  /* 阶段进度不估造总完成率；媒体保持时间独立于建立通话耗时。 */
  function phaseProgress(run) {
    const phase = run.phase || run.progress?.phase || 'preflight', progress=run.progress || {};
    if (['setup','dialing'].includes(phase)) { const requested=Number(progress.requested_calls || run.concurrency), attempted=Number(progress.attempted_calls || 0); return {ratio:requested>0?Math.min(1,attempted/requested):0,label:`已尝试建立 ${count(progress.attempted_calls)} / ${count(requested)} 路`,detail:`累计通过建立验证 ${count(progress.established_calls)} 路`}; }
    if (phase==='media') return {ratio:Math.min(1,Number(progress.media_elapsed_seconds || 0)/Math.max(1,Number(run.duration_seconds))),label:`媒体已运行 ${count(progress.media_elapsed_seconds,1)} / ${count(run.duration_seconds)} 秒`,detail:'当前为双向 RTP 持续阶段'};
    return {ratio:ended(run)?1:0,label:phaseNames[phase] || phase,detail:ended(run)?'最终结论结合发生器报告和资源清理结果':'等待此阶段的真实完成状态'};
  }
  /* 清理标识保留三态，不将缺失观测显示成已完成。 */
  function cleanupMarkup(cleanup) { const flags=[['controller_exited','控制实例退出'],['generator_exited','发生器退出'],['media_processes_exited','媒体进程退出'],['ports_released','端口释放']]; return `<div class="tests-cleanup">${flags.map(([key,label])=>badge(`${label}：${cleanup?.[key]===true?'已确认':cleanup?.[key]===false?'未完成':'待确认'}`,cleanup?.[key]===true?'good':'neutral')).join('')}</div>${cleanup?.detail?`<p class="tests-help tests-error-message">${html(cleanup.detail)}</p>`:''}`; }
  /* 结果区保留展开项与日志滚动位置；轮询不会把阅读中的详情折叠。 */
  function renderRun() {
    const node=document.getElementById('tests-run'); if (!node) return;
    const openDetails=new Set(Array.from(node.querySelectorAll('details[open]')).map(item=>item.dataset.testDetail));
    const logScroll=new Map(Array.from(node.querySelectorAll('[data-test-log]')).map(item=>[item.dataset.testLog,item.scrollTop]));
    const run=view.run, current=activeRun();
    if (!run) { node.innerHTML=`<div class="tests-live-empty"><h2>${view.snapshot?'等待开始测试':'正在载入测试任务'}</h2><p>${view.snapshot?'先选择通话规模或切换到测试小电话。<br>这里将显示真实进度、媒体统计和清理结果。':'读取后台当前任务与最近 20 条历史记录。'}</p></div>`; return; }
    const report=view.reports.get(run.id), measured=sampleMetrics(run,report), result=report?.result, phase=phaseProgress(run);
    const normalizedPhase=({starting:'preflight',dialing:'setup'})[run.phase] || run.phase || 'preflight';
    const phases=['preflight','setup','media','teardown','finished'], phaseIndex=phases.indexOf(normalizedPhase);
    /* 终态可能来自预检失败，不能因 finished 就暗示前面所有阶段均已执行。 */
    const priorPhaseLabel=preflightOnly(run)?'未进入呼叫与媒体阶段':ended(run)?'详见任务记录':'已进入后续阶段';
    const runningNotice=current && current.id!==run.id?`<div class="tests-result-note warning">另一个任务正在${html(phaseNames[current.phase] || '运行')}，当前查看的是历史记录。<button class="button button-secondary button-small" type="button" data-test-action="select-current">查看运行中任务</button></div>`:'';
    node.innerHTML = `${runningNotice}<div class="tests-run-heading"><div><h2>${modeName(run.mode)} · ${count(run.concurrency)} 路</h2><p>${html(run.id)}<br>创建于 ${html(date(run.created_at))}</p></div><div class="tests-status-actions">${statusBadge(run)}${!ended(run)?`<button class="button button-secondary button-small" type="button" data-test-action="stop" ${view.stopping || run.status==='stopping'?'disabled':''}>${run.mode==='phone'?'挂断':'停止测试'}</button>`:''}${run.report_available?`<a class="button button-secondary button-small" href="${reportURL(run.id)}" download="rustswitch-test-${html(run.id)}.json">下载 JSON</a>`:''}</div></div>${!view.online?'<p class="tests-loading-text tests-offline">连接中断，以下为上次成功读取的结果。</p>':''}<div class="tests-stages">${phases.map((key,index)=>`<div class="tests-stage ${index===phaseIndex?'active':index<phaseIndex && !ended(run)?'done':''}"><strong>${index+1}. ${phaseNames[key]}</strong>${index===phaseIndex?'当前阶段':index<phaseIndex?priorPhaseLabel:'尚未进入'}</div>`).join('')}</div><div class="tests-progress" role="progressbar" aria-label="当前阶段进度" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(phase.ratio*100)}"><span style="width:${Math.round(phase.ratio*100)}%"></span></div><div class="tests-progress-caption"><span>${html(phase.label)}</span><span>${html(phase.detail)}</span></div><div class="tests-metrics">${metric('实际活跃通话',count(measured.last?.active_calls),measured.last?`采样于 ${date(measured.last.at)}`:'尚未采集子实例状态')}${metric('实际已接通通话',count(measured.last?.established_calls),'实时值；拆线后会回落')}${metric('累计建立验证通过',count(run.progress?.established_calls ?? result?.established_calls),`建立失败 ${count(run.progress?.setup_failed ?? setupFailureCount(result?.setup_errors))} 路`)}${metric('活跃 / 接通峰值',`${count(measured.activePeak)} / ${count(measured.establishedPeak)}`,measured.peakLabel)}${metric('媒体接收 / 发送',`${count(measured.last?.rx_packets)} / ${count(measured.last?.tx_packets)}`,'子实例媒体分片累计包数')}${metric('端到端未接收包',count(result?.unreceived_packets),result?'发生器双向合计，以最终报告核对':'测试结束后由发生器核对')}</div>${summary('实际经过时间',`${count(run.elapsed_seconds ?? run.progress?.elapsed_seconds,1)} 秒`)}${summary('目标建立速率 / 媒体保持',`${count(run.cps)} 次/秒 · ${count(run.duration_seconds)} 秒`)}${summary('本次媒体处理模式',processingName(run.media_processing))}${summary('编解码 / RTP 包间隔',`${codecName(run.payload)} · 20 ms`)}${report?.evidence?.journal_peak_established_calls!=null?summary('持久化日志记录的接通峰值',`${count(measured.journalPeak)} 路`):''}${run.error?`<div class="tests-result-note error tests-error-message"><strong>${preflightOnly(run)?'资源预检未通过，任务未启动':'任务未完整完成'}</strong>${html(run.error)}</div>`:''}${reportMarkup(run,report)}<details class="tests-details" data-test-detail="media"><summary>媒体、资源与清理详情</summary>${summary('媒体活跃 / 健康分片',`${count(measured.last?.media_active_calls)} 路 · ${count(measured.last?.healthy_workers)} / ${count(measured.last?.worker_count)}`)}${summary('套接字丢弃计数 / 无效包',`${socketDropCount(measured.last)} / ${count(measured.last?.invalid_packets)}`)}${run.status==='failed'?summary('失败后补充统计',failureFinalSampling(report)):''}${summary('发送错误 / 队列丢弃 / 发送过期',`${count(measured.last?.send_errors)} / ${count(measured.last?.send_queue_drops)} / ${count(measured.last?.send_expired)}`)}${summary('媒体限速计数',count(measured.last?.rate_limited))}${summary('子进程用户态 / 内核态 CPU 时间',`${count(measured.last?.user_cpu_seconds,2)} / ${count(measured.last?.system_cpu_seconds,2)} 秒`)}${summary('采集到的峰值驻留内存',measured.last?.peak_resident_bytes!=null?`${count(measured.last.peak_resident_bytes/1048576,1)} MiB`:'未采集')}${processedSampleMarkup(run,measured.last)}${controllerDetails(measured.last?.controller)}${cleanupMarkup(run.cleanup)}<p class="tests-help">媒体分片接收与发送之差不等于端到端丢包。缺包只使用最终发生器的唯一包接收核对；尚无报告时不显示零丢包。套接字丢弃计数依赖平台支持，平台未提供该计数时零值不能证明无丢包。</p></details>`;
    node.querySelectorAll('details').forEach(item=>{if(openDetails.has(item.dataset.testDetail)) item.open=true;});
    node.querySelectorAll('[data-test-log]').forEach(item=>{item.scrollTop=logScroll.get(item.dataset.testLog) || 0;});
  }
  // 处理统计只显示真实完整样本，缺失字段显示占位，绝不由总 RTP 包数推算解码量。
  function processedSampleMarkup(run,sample) {
    if(run.media_processing!=='g711')return '';
    const p=sample?.processing;
    return `<h3>真实音频处理采样</h3>${summary('解码 / 编码帧',`${count(p?.processed_decoded_frames)} / ${count(p?.processed_encoded_frames)}`)}${summary('解码 / 编码样本',`${count(p?.processed_decoded_samples)} / ${count(p?.processed_encoded_samples)}`)}${summary('PLC / 播放过期 / 发送错过期限',`${count(p?.processed_plc_frames)} / ${count(p?.processed_playout_expired)} / ${count(p?.processed_send_deadline_misses)}`)}${summary('输出最大发送延迟',p?.processed_output_lateness_ns_max==null?'—':`${count(p.processed_output_lateness_ns_max/1e6,3)} ms`)}<p class="tests-help">丢失位置和 PLC 不能直接视为线路丢包；独立发生器的逐帧音频核对决定端到端结果。</p>`;
  }
  /* 双向统计来自发生器按 source_side 配对的实际汇总，缺失方向保留未提供。 */
  function directionMarkup(result) {
    if (!result) return '';
    const directions=Array.isArray(result.directions)?result.directions:[];
    return `<div class="tests-report-table"><table><thead><tr><th>媒体方向</th><th>发送 / 唯一接收</th><th>缺包 / 负载不足通话</th><th>单通话最少发送 / 接收包范围</th></tr></thead><tbody>${[0,1].map(side=>{const item=directions.find(value=>value.source_side===side);return `<tr><td>${side===0?'端点 A → B':'端点 B → A'}</td><td>${count(item?.sent)} / ${count(item?.received)}</td><td>${count(item?.flows_with_loss)} / ${count(item?.underloaded_flows)}</td><td>${count(item?.minimum_sent_per_flow)} / ${count(item?.minimum_received_per_flow)}—${count(item?.maximum_received_per_flow)}</td></tr>`;}).join('')}</tbody></table></div><p class="tests-help">两个方向来自发生器真实收发配对。缺失字段显示“—”；每路每方向也必须达到名义包量的 98%，不能用其他通话的包量抵消少发；历史报告没有新增字段时显示“—”。</p>`;
  }
  /* 最终报告分别呈现发生器判定和任务生命周期，提前停止永不冒充完整通过。 */
  /* CPU与协程只读独立测试控制进程快照，不混入Rust媒体的资源汇总。 */
  function controllerDetails(value) {
    if (!value) return '<p class="tests-help">尚未取得测试控制进程资源采样。</p>';
    return summary('测试控制进程协程 / 运行时线程',`${count(value.goroutines)} / ${count(value.runtime_threads)}`)
      + summary('测试控制 CPU 使用',value.cpu_cores_used == null ? '等待相邻采样' : `${count(value.cpu_cores_used,2)} 核`)
      + summary('测试控制 Go 运行时内存',`${count(value.go_memory_bytes/1048576,1)} MiB`)
      + summary('普通 / 优先信令 / 媒体结果队列',`${count(value.normal_queue_length)} / ${count(value.critical_queue_length)} / ${count(value.media_results_queue_length)}`);
  }

  // 后端验收标识与完整处理报告必须同时存在；不能把旧版本或残缺的 passed 布尔值画成转码通过。
  function reportChecksPassed(run,report) {
    const result=report?.result;
    if(run.status!=='completed'||result?.passed!==true||run.cleanup?.complete!==true)return false;
    if(run.media_processing && result.media_processing!==run.media_processing)return false;
    if(run.media_processing!=='g711')return true;
    const p=result.processed_media,e=report.evidence;
    if(!p||e?.validation_contract!=='testlab-media-v2'||e.processing_verified!==true||e.generator_contract_verified!==true||e.worker_stats_after_generator_exit!==true)return false;
    if(p.oracle_version!=='g711-pcm-v1'||p.samples_per_frame!==160||p.jitter_target_ms!==40||p.receive_late_limit_ms!==80||p.burst_gap_limit_ms!==5)return false;
    const a=Number(run.payload),b=a^8,names={0:'PCMU',8:'PCMA'};
    if(![0,8].includes(a)||p.payload_a!==a||p.payload_b!==b||p.codec_a!==names[a]||p.codec_b!==names[b])return false;
    const errors=['content_mismatch_packets','cross_flow_packets','codec_mismatch_packets','timestamp_errors','sequence_errors','identity_errors','late_packets','burst_packets','generator_late_packets'];
    if(errors.some(key=>p[key]!==0))return false;
    if(['observed_packets','content_verified_packets','tail_plc_packets'].some(key=>!Number.isSafeInteger(p[key])||p[key]<0))return false;
    return p.content_verified_packets>0&&p.content_verified_packets===result.received_unique_packets&&p.observed_packets===p.content_verified_packets+p.tail_plc_packets&&p.tail_plc_packets<=run.concurrency*12;
  }
  function reportMarkup(run, report) {
    if (!ended(run)) return '<p class="tests-help">双向音频发送与接收核对正在运行；完整缺包、重复包和拆线结果在结束后生成。</p>';
    if (!report) return `<div class="tests-result-note ${view.reportError?'error':'warning'}">${view.reportError?html(view.reportError):'正在读取最终报告；尚无结果可用于通过判定。'}<button class="button button-secondary button-small" type="button" data-test-action="retry-report">重新读取报告</button></div>`;
    if (preflightOnly(run)) { const capacity=run.failure.capacity; return `<div class="tests-result-note warning"><strong>未进行呼叫和媒体测试</strong>资源预检结束后未启动测试进程，因此没有发生器结果。${capacity?`<p>本次请求 ${count(capacity.requested_concurrency)} 路，建立速率 ${count(capacity.cps)} 次/秒；当前端口模型上限 ${count(capacity.max_concurrency)} 路（估计）。主服务媒体端口保持保留。</p>`:''}</div><details class="tests-details" data-test-detail="logs"><summary>查看资源预检依据</summary><pre class="tests-log">${html(JSON.stringify(report.evidence || {},null,2))}</pre></details>`; }
    const result=report.result, success=reportChecksPassed(run,report);
    const title=success?'本次同机测试检查通过':run.status==='stopped'?'测试已停止，未形成完整通过结论':result?.passed===true?'完整验收尚未确认，请核对报告证据':'本次测试未通过完整检查';
    const note=success?'通过结果仅适用于此任务的负载、持续时间和同机环境，不作为生产容量认证。':result?'请结合下面的包核对、任务错误和清理状态定位原因。':'发生器未生成完整结果；请查看任务错误、资源预检和进程日志。';
    const rows=result?[['报告媒体处理模式',processingName(result.media_processing)],['请求 / 建立成功通话',`${count(result.requested_calls)} / ${count(result.established_calls)}`],['双向实际发送 / 唯一接收',`${count(result.sent_packets)} / ${count(result.received_unique_packets)}`],['未接收 / 重复 / 无效包',`${count(result.unreceived_packets)} / ${count(result.duplicate_packets)} / ${count(result.invalid_packets)}`],['乱序 / 未知 RTP 包',`${count(result.reordered_packets)} / ${count(result.unknown_rtp_packets)}`],['发生器写入错误 / 延迟 tick',`${count(result.generator_write_errors)} / ${count(result.generator_late_ticks)}`],['接收提前退出 / 正常尾窗结束',`${count(result.generator_reader_errors)} / ${count(result.generator_reader_timeouts)}`],['媒体不足的通话方向 / 超额接收',`${count(result.underloaded_flow_directions)} / ${count(result.unexpected_received_packets)}`],['实际发送工作者 / 收发方式',`${count(result.generator_sender_workers)} / ${result.generator_io_mode==='linux_mmsg'?'Linux 批量收发':result.generator_io_mode==='portable_udp'?'标准 UDP 收发':'未提供'}`],['意外 BYE / 拆线失败',`${count(result.unexpected_byes)} / ${count(result.teardown_failures)}`],['实际提供负载比例',result.offered_load_ratio==null?'未提供':`${count(result.offered_load_ratio*100,2)}%`],['目标拨号速率 / 实际建立耗时',`${count(result.setup_cps,1)} 次/秒 · ${count(result.setup_seconds,2)} 秒`],['平均建立成功速率',result.setup_seconds>0?`${count(result.established_calls/result.setup_seconds,1)} 路/秒`:'未提供'],['媒体保持配置 / 已运行时间',`${count(result.media_seconds,2)} / ${count(run.progress?.media_elapsed_seconds,2)} 秒`],['发生器判定',result.passed===true?'通过':result.passed===false?'未通过':'未提供']]:[];
    if(result?.media_processing==='g711')rows.push(...processedReportRows(result));
    return `<div class="tests-result-note ${success?'':run.status==='stopped'?'warning':'error'}"><strong>${title}</strong>${note}</div>${rows.length?`<div class="tests-report-table"><table><thead><tr><th>双向核对项目</th><th>实际结果</th></tr></thead><tbody>${rows.map(([key,value])=>`<tr><td>${html(key)}</td><td>${html(value)}</td></tr>`).join('')}</tbody></table><p class="tests-help">包数是发生器两个方向的合计；下表分别核对两个方向的发送、接收与缺包通话数。双向负载为真实 RTP 发送与接收核对，不代表真人听感评分。</p>`:''}${directionMarkup(result)}${processedDiagnosticsMarkup(result)}<details class="tests-details" data-test-detail="logs"><summary>查看报告证据与进程日志</summary><h3>资源预检与测量边界</h3><pre class="tests-log" data-test-log="evidence">${html(JSON.stringify(report.evidence || {},null,2))}</pre><h3>控制实例日志尾部</h3><pre class="tests-log" data-test-log="controller">${html(report.logs?.controller_tail || '没有可用日志')}</pre><h3>发生器日志尾部</h3><pre class="tests-log" data-test-log="generator">${html(report.logs?.generator_tail || '没有可用日志')}</pre>${setupFailureCount(result?.setup_errors)?`<h3>建立错误</h3><pre class="tests-log" data-test-log="setup">${html(JSON.stringify(result.setup_errors,null,2))}</pre>`:''}</details>`;
  }
  // 独立发生器检验音频内容与输出时序；缺字段保持未知，不能靠 passed 布尔值补全证据。
  function processedReportRows(result) {
    const p=result.processed_media;
    return [['两腿实际格式',p&&['PCMU','PCMA'].includes(p.codec_a)&&['PCMU','PCMA'].includes(p.codec_b)&&[0,8].includes(p.payload_a)&&[0,8].includes(p.payload_b)?`${p.codec_a} (PT ${p.payload_a}) ↔ ${p.codec_b} (PT ${p.payload_b})`:'—'],['逐帧内容校验 / 内容错误',`${count(p?.content_verified_packets)} / ${count(p?.content_mismatch_packets)}`],['串流 / 编码不符',`${count(p?.cross_flow_packets)} / ${count(p?.codec_mismatch_packets)}`],['时间戳 / 序号 / 身份错误',`${count(p?.timestamp_errors)} / ${count(p?.sequence_errors)} / ${count(p?.identity_errors)}`],['迟到 / 集中补发 / 发生器迟到',`${count(p?.late_packets)} / ${count(p?.burst_packets)} / ${count(p?.generator_late_packets)}`],['尾部 PLC 包',count(p?.tail_plc_packets)],['真实接收工作者 / 共享端点组',`${count(result.generator_receiver_workers)} / ${count(result.socket_groups)}`],['迟到 / 集中补发阈值',`${count(p?.receive_late_limit_ms)} ms / ${count(p?.burst_gap_limit_ms)} ms`]];
  }
  // 有界首错只辅助定位，不增加成功接收数，也不改变完整报告的通过判断。
  function processedDiagnosticsMarkup(result) {
    if(result?.media_processing!=='g711')return '';
    const d=result.processed_diagnostics;
    if(d?.version!=='g711-observation-v1')return '<p class="tests-help">此报告未提供有界首错诊断；原有验收结果保留。</p>';
    const ms=value=>value==null?'—':`${count(value/1e6,3)} ms`;
    const samples={first_receive_errors:Array.isArray(d.first_receive_errors)?d.first_receive_errors.slice(0,32):[],first_late_writes:Array.isArray(d.first_late_writes)?d.first_late_writes.slice(0,32):[]};
    return `<details class="tests-details" data-test-detail="processed-diagnostics"><summary>查看音频时间轴与发生器首错</summary>${summary('完整 PCM 观察 / 其中时间戳错误',`${count(d.observed_content_valid_packets)} / ${count(d.timestamp_error_content_valid_packets)}`)}${summary('完整 PCM 观察中超过接收期限',count(d.observed_content_late_packets))}${summary('写入前 / 写入返回时超过源期限',`${count(d.write_attempt_late_packets)} / ${count(d.write_return_late_packets)}`)}${summary('最长批量写调用 / 最长写入返回延迟',`${ms(d.max_write_call_ns)} / ${ms(d.max_write_return_delay_ns)}`)}${summary('接收错误 / 发送迟到的方向数',`${count(d.receiver_error_flows)} / ${count(d.sender_late_flows)}`)}${summary('未展开的接收 / 发送方向首错',`${count(d.truncated_receiver_flows)} / ${count(d.truncated_sender_flows)}`)}<p class="tests-help">每种首错最多保留 32 个方向，接收原报文最多保留 172 字节。PCM 观察正确不等于时间轴或整轮通过；写调用前后时间仅界定批量发送适配器操作，不能视为每包精确内核提交时间。</p><pre class="tests-log">${html(JSON.stringify(samples,null,2))}</pre></details>`;
  }
  /* 历史摘要最多渲染二十条；点选后读取完整任务，摘要不伪装成采样结果。 */
  function renderHistory() { const node=document.getElementById('tests-history');if(!node)return;const history=(view.snapshot?.history || []).slice(0,20);node.innerHTML=`<div class="panel-header"><div><h2>最近测试</h2><p>保留后端最近 20 条记录，点击查看实际结果</p></div>${badge(`${history.length} 条`)}</div>${history.length?`<div class="tests-history-table"><table><thead><tr><th>创建时间 / 类型</th><th>并发</th><th>状态</th><th>操作</th></tr></thead><tbody>${history.map(run=>`<tr class="${run.id===view.selectedId?'selected':''}"><td>${html(date(run.created_at))}<br><span class="text-muted">${modeName(run.mode)}</span></td><td>${count(run.concurrency)} 路</td><td>${statusBadge(run)}</td><td><button class="button button-quiet button-small" type="button" data-test-action="select" data-id="${html(run.id)}">查看</button>${run.report_available?`<a class="button button-quiet button-small" href="${reportURL(run.id)}" download="rustswitch-test-${html(run.id)}.json">JSON</a>`:''}</td></tr>`).join('')}</tbody></table></div>`:'<div class="panel-body"><p class="tests-loading-text">尚无历史测试。页面不会自动发起压力任务。</p></div>'}`; }

  /* 单轮读取有代次保护，离开页面或选中其他任务后，旧响应不会覆盖新视图。 */
  async function refresh() {
    if (!view.active || view.polling) return; const generation=view.generation; view.polling=true;
    try {
      const snapshot=await view.request('/v1/tests'); if(!view.active || generation!==view.generation)return;
      const previousProfiles=JSON.stringify([caps().supported_payloads,caps().media_processing_modes,caps().codec_profiles]);
      view.snapshot=snapshot;
      if(previousProfiles!==JSON.stringify([caps().supported_payloads,caps().media_processing_modes,caps().codec_profiles]))updateTestProfiles();
      view.online=true;view.readError='';view.updatedAt=Date.now();
      /* 仅保留后端当前任务与最近历史的报告，长期开着页面也不会无限累积日志。 */
      const retained=new Set([snapshot.current?.id,...(snapshot.history || []).slice(0,20).map(run=>run.id)]);
      for(const id of view.reports.keys()) if(!retained.has(id))view.reports.delete(id);
      /* 历史只保留当前进程的最近20条。重启或淘汰后释放旧选择与旧运行锁，不能反复请求不存在的ID。 */
      if(view.selectedId && !retained.has(view.selectedId)) forgetSelection();
      if(view.uncertain){const resolved=[snapshot.current,...(snapshot.history || [])].find(run=>run?.request_id===view.uncertain.request_id);if(resolved){savePending(null);view.selectedId=resolved.id;view.explicitSelection=false;view.error='';}}
      if(!view.explicitSelection) view.selectedId=snapshot.current?.id || snapshot.history?.[0]?.id || '';
      if(view.selectedId){
        const selected=view.selectedId;
        try {
          const run=await view.request(`/v1/tests/${encodeURIComponent(selected)}`);
          if(!view.active || generation!==view.generation || selected!==view.selectedId)return;
          view.run=run;view.detailError='';
          if(ended(run)&&snapshot.current?.id===run.id)view.snapshot.current=null;
          await loadReport(run,generation);
        } catch(error) {
          if(!view.active || generation!==view.generation || selected!==view.selectedId)return;
          /* 列表读取后也可能恰逢重启或淘汰；404/410只使这份记录过期，下轮重新选择后台任务。 */
          if(error.status===404 || error.status===410){
            if(view.snapshot.current?.id===selected)view.snapshot.current=null;
            view.snapshot.history=(view.snapshot.history || []).filter(run=>run.id!==selected);
            forgetSelection();
          } else view.detailError=error.message;
        }
      } else {view.run=null;view.detailError='';view.reportError='';}
      renderAvailability();renderRun();renderHistory();updateControls();
    } catch(error){if(view.active && generation===view.generation){view.online=false;view.readError=error.message;renderAvailability();renderRun();updateControls();}}
    finally {if(generation===view.generation){view.polling=false;schedule();}}
  }
  /* 只清除已过期任务的本地结果；表单草稿和结果未知的创建幂等键必须保留。 */
  function forgetSelection(){view.reports.delete(view.selectedId);view.selectedId='';view.explicitSelection=false;view.run=null;view.detailError='';view.reportError='';view.selectionNotice='之前选择的测试记录已过期或服务已重启，已重新读取当前任务；历史报告不会跨服务重启保存。';}
  /* 终态报告按任务缓存，日志和证据不随每秒轮询重复下载。 */
  async function loadReport(run, generation) { if(!ended(run) || !run.report_available || view.reports.has(run.id))return;try{const report=await view.request(reportURL(run.id));if(view.active && generation===view.generation && run.id===view.selectedId){view.reports.set(run.id,report);view.reportError='';}}catch(error){if(view.active && generation===view.generation && run.id===view.selectedId)view.reportError=error.message;} }
  /* 后台标签页降低读取频率；离开本页只取消定时器，不发送停止请求。 */
  function schedule(){clearTimeout(view.timer);if(view.active)view.timer=setTimeout(refresh,document.hidden?5000:activeRun()?1000:4000);}
  function leave(){view.active=false;view.generation++;clearTimeout(view.timer);view.timer=null;view.polling=false;}
  /* 重新进入始终读取后台事实；正在运行任务不依赖浏览器保活。 */
  function mount(adapter){view.request=adapter.request;if(view.active && document.getElementById('tests-workspace'))return;view.active=true;view.generation++;view.polling=false;restorePending();shell();refresh();}

  /* 新任务只从提交事件创建；不确定结果的再次提交复用完整原始参数和幂等键。 */
  async function start(retry=false){if(view.pending)return;if(!view.snapshot?.enabled || !view.online){view.error='测试能力尚不可用，请先刷新状态。';updateControls();return;}if(activeRun()){view.error='已有测试正在运行，请等待结束或先停止当前任务。';updateControls();return;}let body;try{if(retry){if(!view.uncertain)return;body=view.uncertain;}else{if(view.uncertain)throw new Error('上一个创建结果尚未确认，请先重试同一请求或刷新状态。');body={request_id:requestID(),mode:view.mode,...validateDraft(view.mode,view.drafts[view.mode])};}}catch(error){view.error=error.message;updateControls();return;}
    view.generation++;view.polling=false;clearTimeout(view.timer);const generation=view.generation;view.pending=true;view.error='';savePending(body);renderAvailability();updateControls();
    try{const run=await view.request('/v1/tests',{method:'POST',body});savePending(null);if(view.active && generation===view.generation){view.run=run;view.selectedId=run.id;view.explicitSelection=false;if(view.snapshot)view.snapshot.current=ended(run)?null:run;renderRun();renderHistory();}}
    catch(error){view.error=error.message;if(error.status>=400 && error.status<500)savePending(null);}
    finally{view.pending=false;if(view.active){renderAvailability();updateControls();refresh();}}
  }
  /* 停止是显式操作；服务端仍负责拆线和子进程清理，完成前继续锁定新任务。 */
  async function stop(){const run=activeRun();if(!run || view.stopping || run.status==='stopping')return;view.generation++;view.polling=false;clearTimeout(view.timer);view.stopping=true;view.error='';updateControls();renderRun();try{const updated=await view.request(`/v1/tests/${encodeURIComponent(run.id)}/stop`,{method:'POST',body:{}});if(view.snapshot)view.snapshot.current=ended(updated)?null:updated;if(view.selectedId===updated.id)view.run=updated;}catch(error){view.error=error.message;}finally{view.stopping=false;if(view.active){renderRun();updateControls();refresh();}}}
  /* 历史切换采用独立代次，刚刚返回的旧任务详情不能覆盖新的选择。 */
  function select(id){view.selectedId=id;view.explicitSelection=true;view.reportError='';view.detailError='';view.selectionNotice='';view.run=null;view.generation++;view.polling=false;clearTimeout(view.timer);renderRun();renderHistory();refresh();}
  /* 事件委托仅接受固定动作；服务端文本和日志不进入动作解析。 */
  function handleClick(event){const button=event.target.closest('[data-test-action]');if(!button || button.disabled)return;const action=button.dataset.testAction;if(action==='mode'){if(view.mode===button.dataset.mode)return;view.mode=button.dataset.mode==='phone'?'phone':'pressure';shell();}else if(action==='preset'){const draft=view.drafts.pressure;draft.custom=button.dataset.value==='custom';if(!draft.custom)draft.concurrency=Number(button.dataset.value);renderForm();if(draft.custom)document.getElementById('test-concurrency')?.focus();}else if(action==='refresh'){view.error='';refresh();}else if(action==='retry-create')start(true);else if(action==='stop')stop();else if(action==='select')select(button.dataset.id);else if(action==='select-current' && activeRun())select(activeRun().id);else if(action==='retry-report'){view.reports.delete(view.selectedId);view.reportError='';refresh();}}
  /* 输入草稿保持原字符串，提交时集中验证；预设高亮只反映实际填写值。 */
  function handleInput(event){const input=event.target.closest('[data-test-field]');if(!input)return;const key=input.dataset.testField;if(!['concurrency','cps','duration_seconds','payload','media_processing'].includes(key))return;view.drafts[view.mode][key]=input.value;if(key==='media_processing'){if(input.value==='g711'&&![0,8].includes(Number(view.drafts[view.mode].payload)))view.drafts[view.mode].payload=0;updateTestProfiles();}if(key==='concurrency'){view.drafts.pressure.custom=true;document.querySelectorAll('.tests-preset').forEach(button=>button.classList.toggle('active',button.dataset.value==='custom'));}}
  return {mount,leave};
})();
