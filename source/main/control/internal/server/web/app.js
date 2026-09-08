'use strict';

/* 控制台只使用同源接口。运行数据、配置草稿和本地输入分开存放，轮询不覆盖编辑。 */
const state = {
  codecs: null, codecsLoading: false, codecsError: '',
  page: 'overview', statusReadAt: 0, status: null, metrics: {}, online: false, polling: false, loading: true,
  config: null, configDraft: null, configBase: null, configRevision: null, csrf: '',
  guardDraft: null, guardBase: null, guardRevision: null, history: [], observations: [],
  fs: null, fsDraft: {}, fsBase: {}, fsRevision: null, fsLoading: false,
  fsSearch: '', fsCategory: 'all', fsFile: 'all', fsPage: 1, fsPageSize: 16,
  fsMode: 'parameters', xmlPath: '', xmlDrafts: {}, xmlBases: {}, xmlBusy: false,
  addParameter: false, parents: [], busy: false, conflict: false, lastError: ''
};
const pages = {
  overview: ['Voice Runtime', '运行总览', '分别观察主服务与隔离测试实例的通话、媒体和资源状态。'],
  protection: ['容量与准入', '峰值保护', '用动态准入保护已有通话，为突发呼叫保留处理余量。'],
  media: ['信令与音频', 'SIP 与媒体', '设置对外地址、媒体资源与通话协议参数。'],
  codecs: ['音频能力', '音频编解码', '查看协商格式、RTP 时钟和当前原生音频后端。'],
  operations: ['服务管理', '运维配置', '管理控制接口、日志持久化与服务排空。'],
  freeswitch: ['兼容工作区', 'FreeSWITCH 配置', '浏览官方配置目录，编辑业务参数并导出标准 XML 文件树。'],
  tests: ['验证工作区', '压力与电话测试', '在独立实例中运行真实 SIP 与双向媒体测试，观察进度和完整结果。'],
  'api-docs': ['开发者工作区', '接口文档', '阅读 HTTP、SIP、媒体 IPC、原生 ABI 和 CLI 的完整接口契约。'],
  'fs-comparison': ['迁移依据', 'FreeSWITCH 对照', '逐项比较接口、配置与运行语义，查看真实差异及验证状态。'],
  compatibility: ['产品能力', '兼容能力', '了解当前运行能力、配置覆盖范围和仍待实现的兼容接口。']
};
const icons = {
  overview: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  shield: '<path d="m12 3 8 3v6c0 5-8 9-8 9S4 17 4 12V6l8-3Z"/><path d="m8.5 12 2.5 2.5 4.5-5"/>',
  network: '<rect x="8" y="3" width="8" height="5" rx="1"/><rect x="2" y="16" width="7" height="5" rx="1"/><rect x="15" y="16" width="7" height="5" rx="1"/><path d="M12 8v4H5.5v4M12 12h6.5v4"/>',
  settings: '<path d="M4 7h16M4 17h16"/><circle cx="9" cy="7" r="3" fill="currentColor" stroke="none"/><circle cx="16" cy="17" r="3" fill="currentColor" stroke="none"/>',
  file: '<path d="M14 3H5v18h14V8l-5-5Z"/><path d="M14 3v5h5M8 12h8M8 16h6"/>',
  layers: '<path d="m12 3 10 5-10 5L2 8l10-5ZM2 12l10 5 10-5M2 16l10 5 10-5"/>',
  refresh: '<path d="M20 8a8 8 0 0 0-14-3L3 8m0-5v5h5M4 16a8 8 0 0 0 14 3l3-3m0 5v-5h-5"/>',
  menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  calls: '<path d="M5 3h4l2 5-3 2a15 15 0 0 0 6 6l2-3 5 2v4a2 2 0 0 1-2 2A18 18 0 0 1 3 5a2 2 0 0 1 2-2Z"/>',
  activity: '<path d="M2 12h5l3-8 4 16 3-8h5"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v4m6-4v4M9 18v4m6-4v4M2 9h4m-4 6h4m12-6h4m-4 6h4"/><rect x="9" y="9" width="6" height="6" rx="1"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  alert: '<path d="m12 3 10 18H2L12 3Z"/><path d="M12 9v5m0 3v.1"/>',
  check: '<path d="m5 12 4 4L19 6"/>',
  search: '<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>',
  download: '<path d="M12 3v12m-4-4 4 4 4-4M4 16v5h16v-5"/>',
  plus: '<path d="M12 4v16M4 12h16"/>',
  server: '<rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.1M7 17.5h.1M15 6.5h3M15 17.5h3"/>',
  lock: '<rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3"/>'
};

/* 所有动态字符串先转义，服务端字段不能作为 HTML 或脚本执行。 */
function escapeHTML(value) { return String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char])); }
/* 创建本地 SVG 图标，路径仅来自上方固定图标表。 */
function icon(name) { return `<svg viewBox="0 0 24 24" aria-hidden="true">${icons[name] || icons.file}</svg>`; }
/* 为静态 HTML 中的图标占位元素添加固定 SVG。 */
function fillIcons() { document.querySelectorAll('[data-icon]').forEach(node => { node.innerHTML = icon(node.dataset.icon); }); }
/* 数据副本用于隔离有效配置、已保存草稿与本地修改。 */
function clone(value) { return JSON.parse(JSON.stringify(value)); }
/* 缺失数据使用占位符，避免将未采集指标显示为零。 */
function number(value, digits = 0) { return value == null || !Number.isFinite(Number(value)) ? '—' : Number(value).toLocaleString('zh-CN', {maximumFractionDigits: digits}); }
/* 显示持续时间，不把服务运行时长当作压测通过证据。 */
function duration(seconds) { if (seconds == null) return '—'; const minutes = Math.floor(seconds / 60); return minutes < 60 ? `${minutes} 分钟` : `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分钟`; }
/* 读写嵌套配置键，保持提交时原有配置完整结构。 */
function getPath(object, path) { return path.split('.').reduce((value, key) => value?.[key], object); }
function setPath(object, path, value) { const parts = path.split('.'); const last = parts.pop(); parts.reduce((target, key) => target[key], object)[last] = value; }
/* 统一格式化数组输入，网络列表和 CPU 列表使用相同分隔规则。 */
function inputValue(value) { return Array.isArray(value) ? value.join('\n') : (value ?? ''); }
/* 用排序后的对象进行比较，避免属性顺序造成伪变更。 */
function canonical(value) { if (Array.isArray(value)) return JSON.stringify(value); if (value && typeof value === 'object') return JSON.stringify(Object.keys(value).sort().map(key => [key, canonical(value[key])])); return JSON.stringify(value); }
function changed(a, b) { return canonical(a) !== canonical(b); }
/* 计算各类草稿修改状态，不因状态轮询而清除用户编辑。 */
function configDirty() { return state.configDraft && changed(state.configDraft, state.configBase); }
function guardDirty() { return state.guardDraft && changed(state.guardDraft, state.guardBase); }
function fsDirty() { return changed(state.fsDraft, state.fsBase); }
function xmlDirty(path = state.xmlPath) { return path && state.xmlDrafts[path] != null && state.xmlDrafts[path] !== state.xmlBases[path]; }
function anyDirty() { return configDirty() || guardDirty() || fsDirty() || Object.keys(state.xmlDrafts).some(path => xmlDirty(path)); }
/* 提交成功后只推进同一旧版本的本地草稿，已有过期草稿仍保留冲突检查。 */
function advanceRevision(previous, revision) { ['configRevision','guardRevision','fsRevision'].forEach(key => { if (state[key] === previous) state[key] = revision; }); }

/* 请求设置超时、同源凭据与 CSRF；服务端错误和 409 冲突保留原始含义。 */
async function request(path, options = {}, csrfRetried = false) {
  const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), 10000);
  const headers = {'Accept':'application/json', ...(options.headers || {})};
  if (options.body !== undefined) { headers['Content-Type'] = 'application/json'; headers['X-RustSwitch-CSRF'] = state.csrf; }
  try {
    const response = await fetch(path, {...options, headers, credentials:'same-origin', cache:'no-store', signal:controller.signal, body:options.body === undefined ? undefined : JSON.stringify(options.body)});
    const text = await response.text(); let data;
    try { data = text ? JSON.parse(text) : {}; } catch { throw new Error('服务端返回了无法识别的响应，请检查服务版本。'); }
    if (!response.ok) {
      /* 服务重启会更换令牌。只有服务端明确确认写入尚未执行，才使用同一请求重试一次。
         只更新令牌，不能用这次读取覆盖未保存配置、原revision或测试request_id。网络超时不走此分支。 */
      if (response.status===403 && response.headers.get('X-RustSwitch-CSRF-Refresh')==='required' && options.body!==undefined && !csrfRetried) {
        const fresh=await request('/v1/config');
        if (typeof fresh.csrf_token==='string' && fresh.csrf_token.length>0) {
          state.csrf=fresh.csrf_token;
          return await request(path,options,true);
        }
      }
      const error = new Error(data.error || `请求失败（HTTP ${response.status}）`); error.status = response.status; error.revision = data.revision; throw error;
    }
    return data;
  } catch (error) { if (error.name === 'AbortError') throw new Error('请求超时，请确认服务仍在运行。'); throw error; }
  finally { clearTimeout(timeout); }
}
/* 非阻塞中文反馈；错误消息不写入页面历史日志来冒充服务事件。 */
function toast(message, type = 'good') { const node = document.createElement('div'); node.className = `toast ${type}`; node.textContent = message; document.getElementById('toast-container').append(node); setTimeout(() => node.remove(), type === 'error' ? 9000 : 5500); }
/* 全局错误区提供显式冲突恢复入口，绝不自动覆盖本地草稿。 */
function showError(error) { const notice = document.getElementById('global-notice'); state.conflict = error.status === 409; notice.hidden = false; notice.innerHTML = `<div class="notice-content"><strong>${state.conflict ? '配置版本发生变化，本地修改已保留。' : '操作未完成'}</strong><br>${escapeHTML(state.conflict ? '请先导出本地草稿，再重新载入服务端最新配置并重新合并修改。' : error.message)}</div>${state.conflict ? '<div class="dialog-actions"><button class="button button-secondary button-small" data-action="download-drafts">导出本地草稿</button><button class="button button-secondary button-small" data-action="reload-config">重新载入最新配置</button></div>' : ''}`; toast(state.conflict ? '发现版本冲突，没有覆盖服务端或本地配置。' : error.message, 'error'); }
/* 原生对话框用于可能丢弃草稿或改变服务准入的操作。 */
async function confirmAction(title, description, label = '确认') { const dialog = document.getElementById('confirm-dialog'); document.getElementById('confirm-title').textContent = title; document.getElementById('confirm-description').textContent = description; document.getElementById('confirm-action').textContent = label; dialog.returnValue = ''; dialog.showModal(); return new Promise(resolve => { dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm'), {once:true}); }); }
/* 统一展示离线状态；已有数据保留，并标记为上一次采集结果。 */
function connection(online) { state.online = online; const node = document.getElementById('connection-state'); node.innerHTML = `<i class="status-dot ${online ? '' : 'offline'}"></i>${online ? '服务已连接' : '服务离线 / 数据未更新'}`; }
/* 只记录浏览器实际观察到的状态变化，不宣称它是持久化服务器日志。 */
function observe(previous, current) { const events = []; if (!previous) events.push('已连接服务，开始采集运行状态'); if (previous && previous.draining !== current.draining) events.push(current.draining ? '服务进入排空状态' : '服务恢复新呼叫准入'); if (previous && current.journal_lost_events > previous.journal_lost_events) events.push(`持久化日志累计丢失增加 ${current.journal_lost_events - previous.journal_lost_events} 条`); (current.workers || []).forEach(worker => { const before = previous?.workers?.find(item => item.id === worker.id); if (before && worker.restarts > before.restarts) events.push(`媒体分片 ${worker.id} 的重启次数增加`); }); events.forEach(message => state.observations.unshift({at:Date.now(), message})); state.observations = state.observations.slice(0, 30); }
/* 轮询仅刷新运行数据，总览每次更新，配置页面不重绘输入框。 */
async function pollStatus() { if (state.polling) return; state.polling = true; try { const data = await request('/v1/status'); observe(state.status, data); state.status = data; state.statusReadAt = Date.now(); connection(true); state.history.push({at:Date.now(), active:data.active_calls, established:data.established_calls}); state.history = state.history.slice(-60); document.getElementById('last-updated').textContent = `更新于 ${new Date().toLocaleTimeString('zh-CN', {hour12:false})}`; document.getElementById('sidebar-version').textContent = `v${data.version || '—'} · 开发原型`; if (state.page === 'overview') renderOverview(); else if (state.page === 'protection') updateGuardSummary(); else if (state.page === 'media') updateRegistrationStatus(); } catch (error) { connection(false); state.lastError = error.message; if (state.page === 'overview') renderOverview(); else if (state.page === 'protection') updateGuardSummary(); else if (state.page === 'media') updateRegistrationStatus(); } finally { state.polling = false; } }
/* 配置的初次加载同时取得 CSRF token；后续读取不会隐式丢弃本地编辑。 */
async function loadConfig() { const data = await request('/v1/config'); if (!data.active || !data.desired) throw new Error('配置接口缺少有效配置或待重启配置。'); state.config = data; state.csrf = data.csrf_token || ''; state.configDraft = clone(data.desired); state.configBase = clone(data.desired); state.configRevision = data.revision; const guard = data.guard?.policy ? data.guard : (await request('/v1/guard')).guard; state.guardDraft = clone(guard?.policy || {}); state.guardBase = clone(state.guardDraft); state.guardRevision = data.revision; state.config.guard = guard; }
/* 读取 FreeSWITCH 目录和草稿，只在明确加载时替换本地参数值。 */
async function loadFS() { state.fsLoading = true; try { const data = await request('/v1/fs-config'); state.fs = data; state.fsBase = clone(data.overrides || {}); state.fsDraft = clone(data.overrides || {}); state.fsRevision = data.revision; } finally { state.fsLoading = false; } }
/* 用户主动放弃本地修改后，重新获取共享版本和各类草稿。 */
async function reloadConfig() { if (anyDirty() && !(await confirmAction('重新载入服务端配置？', '此操作将放弃当前浏览器中未保存的参数和 XML 修改。已保存的服务端配置不会被删除。', '放弃修改并载入'))) return; try { await loadConfig(); if (state.fs) await loadFS(); state.xmlDrafts = {}; state.xmlBases = {}; state.conflict = false; document.getElementById('global-notice').hidden = true; renderPage(); toast('已载入服务端最新版本。'); } catch (error) { showError(error); } }
/* 页面切换只切换视图，全部本地草稿留在状态对象中。 */
async function navigate() { overviewTests.epoch++; const target = location.hash.slice(1).split('?')[0]; state.page = pages[target] ? target : 'overview'; document.querySelectorAll('[data-page]').forEach(node => { const active = node.dataset.page === state.page; node.classList.toggle('active', active); if (active) node.setAttribute('aria-current','page'); else node.removeAttribute('aria-current'); }); const [eyebrow,title,description] = pages[state.page]; document.title = `${title} · 云蝠 Voice Runtime`; document.getElementById('page-eyebrow').textContent = eyebrow; document.getElementById('page-title').textContent = title; document.getElementById('page-description').textContent = description; document.getElementById('breadcrumb-current').textContent = title; setMenu(false); renderPage(); if(state.page==='overview')void pollOverviewTests();if(state.page==='codecs')void pollCodecs(); if ((state.page === 'freeswitch' || state.page === 'compatibility') && !state.fs && !state.fsLoading) { try { state.fsLoading = true; renderPage(); await loadFS(); renderPage(); } catch (error) { showError(error); renderPage(); } } }
/* 页面分派与保存工具栏互相独立，表单状态不会被总览轮询重置。 */
function renderPage() { const docsPage=['api-docs','fs-comparison'].includes(state.page);document.getElementById('heading-meta').hidden=docsPage||state.page==='tests'||state.page==='codecs';if(docsPage){window.RustSwitchTests?.leave();window.RustSwitchDocs?.mount(state.page);updateSavebar();return;}window.RustSwitchDocs?.leave();if(state.page==='tests'){window.RustSwitchTests?.mount({request});updateSavebar();return;}window.RustSwitchTests?.leave();if (state.page === 'overview') renderOverview(); else if (state.page === 'codecs') renderCodecs(); else if (state.page === 'freeswitch') renderFS(); else if (state.page === 'compatibility') renderCompatibility(); else if (!state.config) renderUnavailable(); else if (state.page === 'protection') renderProtection(); else if (state.page === 'media') renderMedia(); else renderOperations(); updateSavebar(); }
/* 没有可编辑配置时明确显示加载或连接错误，避免提交空配置。 */
function renderUnavailable() { document.getElementById('page-content').innerHTML = `<div class="empty-state"><h2>${state.loading ? '正在读取配置' : '暂时无法读取服务配置'}</h2><p>请确认服务已启动，并通过本机管理地址打开控制台。</p><button class="button button-secondary" style="margin-top:18px" data-action="reload-config">重新载入</button></div>`; }
/* 小型公用卡片和状态标签保持所有页面视觉一致。 */
function badge(text, type = 'neutral') { return `<span class="badge ${type}">${escapeHTML(text)}</span>`; }
function panel(title, description, body, actions = '') { return `<section class="panel"><div class="panel-header"><div><h2>${escapeHTML(title)}</h2>${description ? `<p>${escapeHTML(description)}</p>` : ''}</div>${actions ? `<div class="panel-header-actions">${actions}</div>` : ''}</div><div class="panel-body">${body}</div></section>`; }
function healthRow(label, value, iconName = 'check') { return `<div class="health-row"><span class="health-row-label">${icon(iconName)}${escapeHTML(label)}</span><strong>${value}</strong></div>`; }
/* 用真实最近采样绘制 SVG 趋势，样本不足时明确显示等待。 */
function chart() { const history = state.history; if (history.length < 2) return '<div class="chart-empty">等待更多运行样本后显示趋势</div>'; const max = Math.max(1,...history.map(item => Number(item.active) || 0)); const pathFor = key => history.map((item,index) => `${index ? 'L' : 'M'}${(index / (history.length - 1) * 600).toFixed(1)},${(155 - ((item[key] || 0) / max) * 136).toFixed(1)}`).join(' '); const main = pathFor('active'); return `<div class="chart"><svg viewBox="0 0 600 176" preserveAspectRatio="none" role="img" aria-label="最近采样的活跃通话与已接通通话变化"><defs><linearGradient id="chart-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#48b89d" stop-opacity=".16"/><stop offset="100%" stop-color="#48b89d" stop-opacity="0"/></linearGradient></defs><path d="M0 19H600M0 64H600M0 110H600M0 155H600" stroke="#edf2f1" stroke-dasharray="3 4" stroke-width="1"/><path d="${main} L600 155L0 155Z" fill="url(#chart-fill)" stroke="none"/><path d="${pathFor('established')}" stroke="#a0b9c3" stroke-width="1.6"/><path d="${main}" stroke="#209882" stroke-width="2"/></svg><div class="chart-labels"><span>${new Date(history[0].at).toLocaleTimeString('zh-CN',{hour12:false})}</span><span>最近 ${number((history.at(-1).at-history[0].at)/1000)} 秒</span><span>现在</span></div></div>`; }
/* 将工作进程准入原因转换为业务可读中文，未知原因保留原文可排查。 */
function reasonLabel(value) { return ({media_drops:'检测到媒体丢包',media_cpu:'媒体进程 CPU 压力高',control_backlog:'媒体控制队列积压',worker_unavailable:'分片不可用',stale_media_stats:'分片统计已过期',max_active_calls:'达到并发保护上限',max_establishing_calls:'达到建立中上限',rate_limited:'当前新呼叫令牌不足',draining:'正在排空',disabled:'保护未启用'})[value] || value || '正常准入'; }
/* 当前准入状态只依据即时指标，历史拒绝原因不会被当作持续故障。 */
function admissionState(data = state.status) { if (!state.online) return ['数据未更新','warning']; if (data?.draining) return ['正在排空','warning']; if (data?.journal_healthy === false) return ['日志异常 / 准入受限','error']; if (data?.guard?.limit_reason) return [reasonLabel(data.guard.limit_reason),'warning']; if (data?.workers?.length && data.workers.every(worker => !worker.healthy || worker.admission?.reason)) return ['媒体暂不可准入','warning']; if (data?.guard?.throttled) return ['柔性降速中','warning']; return ['正常准入','good']; }
/* 独立更新保护页的实时摘要，不重绘正在编辑的策略字段。 */
function updateGuardSummary() { const node = document.getElementById('guard-live-summary'); if (!node) return; const guard = state.status?.guard || state.config?.guard || {}, [label,type] = admissionState(); node.innerHTML = `${badge(label,type)} 当前有效准入速率：${number(guard.effective_cps,1)} 次 / 秒；建立中：${number(guard.establishing_calls)} 路；累计拒绝：${number(guard.rejected)} 次。${guard.last_reject_reason ? `<br>最近一次拒绝原因（历史记录）：${escapeHTML(reasonLabel(guard.last_reject_reason))}` : ''}`; }
/* 告警全部来自当前真实状态，累计异常计数明确标为累计。 */
function alerts() { const data = state.status; const list = []; if (!state.online) list.push(['error','服务连接中断','当前显示的是上一次成功采集的数据。']); if (data?.draining) list.push(['warning','服务正在排空','已有通话继续服务，暂停接纳新呼叫。']); if (data && !data.journal_healthy) list.push(['error','事件日志不可用','检查持久化目录权限、磁盘空间与服务日志。']); if (data?.journal_lost_events > 0) list.push(['error','事件日志存在累计丢失',`当前进程累计 ${number(data.journal_lost_events)} 条，不代表本次采样新增。`]); (data?.workers || []).forEach(worker => { if (!worker.healthy) list.push(['error',`媒体分片 ${worker.id} 不可用`,'请查看服务进程与媒体程序运行状态。']); else if (worker.admission?.reason) list.push(['warning',`媒体分片 ${worker.id} 暂停准入`,reasonLabel(worker.admission.reason)]); }); if (data?.guard?.limit_reason) list.push(['warning','峰值保护正在限制新呼叫',reasonLabel(data.guard.limit_reason)]); else if (data?.guard?.throttled) list.push(['warning','峰值保护正在柔性降速',`当前有效速率 ${number(data.guard.effective_cps,1)} 次 / 秒。`]); if (data?.restart_required || state.config?.restart_required) list.push(['warning','有待重启配置','已保存的启动配置将在重启后生效，当前通话继续使用有效配置。']); return list; }
/* 总览同时展示瞬时状态、分片计数和真实浏览器观察记录。 */
/* 控制进程资源与Rust分片分开显示，避免把协程误当线程或把核数误当百分比。 */
function controllerPanel(data) {
  if (!data) return '';
  return panel('控制进程与排队','Go 控制进程采样；资源约每秒更新，队列为当前观测',
    `<div class="dashboard-grid"><div>${healthRow('Go 协程 / 运行时存活线程',`${number(data.goroutines)} / ${number(data.runtime_threads)}`,'cpu')}${healthRow('控制 CPU 使用',data.cpu_cores_used == null ? '等待采样' : `${number(data.cpu_cores_used,2)} 核`,'cpu')}${healthRow('Go 运行时内存',`${number(data.go_memory_bytes/1048576,1)} MiB`,'layers')}</div><div>${healthRow('普通 / 优先信令队列',`${number(data.normal_queue_length)} / ${number(data.critical_queue_length)}`,'layers')}${healthRow('管理连接 / 上限',`${number(data.admin_connections)} / ${number(data.admin_connection_limit)}`,'layers')}${healthRow('管理重请求 / 上限',`${number(data.admin_heavy_requests)} / ${number(data.admin_heavy_request_limit)}`,'layers')}${healthRow('管理重请求累计退避',number(data.admin_heavy_rejected_total),'shield')}</div></div><p class="field-help">未提供的线程或 CPU 数据显示“—”或等待采样。Go 内存不是进程 RSS，CPU 核数允许超过 1；这些观测不能单独证明容量或无泄漏。</p>`);
}

/* 压测总览独立读现有测试接口，主服务与子实例不共享健康状态或时间序列。
   页面代次阻止离开/返回后旧回复覆盖新选择；同一时间最多一轮读取，不主动创建任务。 */
const overviewTests = {epoch:0, polling:false, loaded:false, snapshot:null, run:null, detailID:'', readAt:0, readError:'', detailError:'', notice:''};
function overviewEnded(run) { return ['completed','failed','stopped'].includes(run?.status); }
function overviewTime(value) { const timestamp=typeof value==='number'?value:Date.parse(value);return Number.isFinite(timestamp)&&timestamp>0?new Date(timestamp).toLocaleString('zh-CN',{hour12:false}):'未提供'; }
async function pollOverviewTests() {
  if(state.page!=='overview'||overviewTests.polling)return;
  const epoch=overviewTests.epoch, current=()=>state.page==='overview'&&overviewTests.epoch===epoch;
  overviewTests.polling=true;
  try {
    const snapshot=await request('/v1/tests');
    if(!current())return;
    if(!snapshot||!Array.isArray(snapshot.history)||typeof snapshot.enabled!=='boolean')throw new Error('测试接口缺少有效任务列表');
    const selected=snapshot.current || snapshot.history[0] || null;
    const previous=overviewTests.run;
    overviewTests.snapshot=snapshot;overviewTests.loaded=true;overviewTests.readAt=Date.now();overviewTests.readError='';overviewTests.notice='';
    if(!selected){overviewTests.run=null;overviewTests.detailID='';overviewTests.detailError='';if(previous)overviewTests.notice='测试记录已过期或服务已重启；旧任务采样已清除。';}
    else if(snapshot.current){overviewTests.run=snapshot.current;overviewTests.detailID='';overviewTests.detailError='';}
    else if(overviewTests.detailID===selected.id&&previous?.id===selected.id&&overviewEnded(previous)&&previous.finished_at===selected.finished_at&&!overviewTests.detailError){
      // 已结束的同一任务无需每三秒重复下载300个样本；服务重启/历史淘汰仍以上方列表为准。
    } else {
      // 新任务的详情没到达时使用它自己的摘要，绝不把前一次并发/曲线搬到新任务。
      overviewTests.run=previous?.id===selected.id?previous:selected;
      try {
        const detail=await request('/v1/tests/'+encodeURIComponent(selected.id));
        if(!current())return;
        if(detail.id!==selected.id||!Array.isArray(detail.samples))throw new Error('测试详情身份或样本格式不一致');
        overviewTests.run=detail;overviewTests.detailID=detail.id;overviewTests.detailError='';
      }catch(error){
        if(!current())return;
        if(error.status===404||error.status===410){overviewTests.run=null;overviewTests.detailID='';overviewTests.detailError='';overviewTests.notice='任务详情已过期；下一轮重新读取任务列表。';}
        else overviewTests.detailError='任务详情暂未更新：'+error.message;
      }
    }
  }catch(error){if(current()){overviewTests.loaded=true;overviewTests.readError=error.message;}}
  finally{overviewTests.polling=false;if(current())renderOverview();}
}
/* 缺少采样是未知，绝不能用目标并发或累计建立数补作实际并发；最多渲染最近300个后端样本。 */
function overviewTestMeasurement(run,now=Date.now()) {
  const samples=(Array.isArray(run?.samples)?run.samples:[]).slice(-300).filter(item=>item&&typeof item==='object');
  const last=samples.at(-1) || null, stamp=Date.parse(last?.at);
  const invalidTime=last&&!Number.isFinite(stamp), future=Number.isFinite(stamp)&&stamp>now+5000;
  const stale=!overviewEnded(run)&&last&&(invalidTime||future||now-stamp>10000);
  const finite=value=>typeof value==='number'&&Number.isFinite(value)&&value>=0;
  const peak=key=>{const values=samples.map(item=>item[key]).filter(finite);return values.length?Math.max(...values):null;};
  return {samples,last,stale,invalidTime,future,activePeak:peak('active_calls'),establishedPeak:peak('established_calls')};
}
function overviewTestChart(samples) {
  const points=samples.slice(-60).filter(item=>Number.isFinite(Date.parse(item.at))&&typeof item.active_calls==='number'&&typeof item.established_calls==='number'&&Number.isFinite(item.active_calls)&&Number.isFinite(item.established_calls));
  if(points.length<2)return '<p class="overview-test-empty">等待至少两个子实例实测样本后显示趋势。</p>';
  const ceiling=Math.max(1,...points.flatMap(item=>[item.active_calls,item.established_calls]));
  const line=key=>points.map((item,index)=>`${index?'L':'M'}${(index/(points.length-1)*600).toFixed(1)},${(125-item[key]/ceiling*110).toFixed(1)}`).join(' ');
  return `<div class="overview-test-chart"><svg viewBox="0 0 600 145" role="img" aria-label="当前任务隔离实例的实际活跃与接通通话趋势"><path d="M0 15H600M0 70H600M0 125H600" stroke="#e7e8f3" stroke-dasharray="3 4"/><path d="${line('active_calls')}" fill="none" stroke="#7866c1" stroke-width="2.5"/><path d="${line('established_calls')}" fill="none" stroke="#408c9b" stroke-width="2"/></svg><div class="chart-labels"><span>${escapeHTML(overviewTime(points[0].at))}</span><span>${escapeHTML(overviewTime(points.at(-1).at))}</span></div></div>`;
}
function renderOverviewTests(detailsOpen=false) {
  const view=overviewTests,run=view.run,measured=overviewTestMeasurement(run),sample=measured.last;
  const link='<a id="overview-tests-link" class="button button-secondary button-small" href="#tests">压力与电话测试</a>';
  const error=view.readError?`<div class="notice warning">测试列表读取失败：${escapeHTML(view.readError)}。${run?'以下保留上一次读取结果，不能视作实时状态。':'尚无可展示的测试记录。'}</div>`:'';
  if(!run)return `<section class="overview-test-section" data-overview-source="isolated-test">${panel('隔离测试实例','来源：/v1/tests；与当前主服务独立',`${error}<p class="overview-test-empty">${escapeHTML(view.notice || (!view.loaded?'正在读取正在运行与最近测试…':view.snapshot?.enabled===false?(view.snapshot.disabled_reason||'此实例未启用压测工作区'):'暂无正在运行或保留中的测试。发起测试后，这里自动显示真实通话和媒体采样。'))}</p>`,link)}</section>`;
  const statuses={queued:'等待启动',starting:'正在准备',running:'正在运行',stopping:'正在停止',completed:'已完成',failed:'任务失败',stopped:'已停止'};
  const phases={preflight:'资源预检',setup:'建立呼叫',media:'双向媒体',teardown:'拆线清理',finished:'已结束'};
  const ended=overviewEnded(run),readingStale=!!view.readError||!!view.detailError;
  const stateLabel=statuses[run.status]||run.status||'状态未知';
  const sampleNote=sample?`${ended?'结束前最后采样':'子实例采样'}：${overviewTime(sample.at)}`:'尚无子实例采样；预检或准备阶段不会产生实际通话指标。';
  const metric=(label,value,note)=>`<div class="overview-test-metric"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong><small>${escapeHTML(note)}</small></div>`;
  let metrics=metric('实际活跃通话',number(sample?.active_calls),`目标并发 ${number(run.concurrency)} 路，不代表已达到`)
    +metric('实际已接通通话',number(sample?.established_calls),'本次采样时刻值；拆线后回落')
    +metric('媒体接收 / 发送包',`${number(sample?.rx_packets)} / ${number(sample?.tx_packets)}`,'隔离媒体累计计数，不推算端到端丢包')
    +metric('健康媒体工作进程',`${number(sample?.healthy_workers)} / ${number(sample?.worker_count)}`,'仅隔离测试实例的媒体进程');
  if(run.media_processing==='g711')metrics+=metric('实际解码 / 编码帧',`${number(sample?.processing?.processed_decoded_frames)} / ${number(sample?.processing?.processed_encoded_frames)}`,'真实图累计值，未采集保持未知');
  /* 首屏保留实测峰值；测试拆线后的当前零值不能让用户误读为没有形成负载。 */
  const peakMarkup=`<p class="overview-test-sampling">保留样本内实测峰值：<strong>活跃 ${number(measured.activePeak)} / 接通 ${number(measured.establishedPeak)} 路</strong><span>来自 ${number(measured.samples.length)} 个实际样本，不使用目标并发补值。</span></p>`;
  const controller=sample?.controller;
  const rows=[['测试媒体处理模式',run.media_processing==='g711'?'G.711 双腿音频处理':run.media_processing==='relay'?'同编码 RTP 透传':'历史任务未记录'],['媒体活跃通话',number(sample?.media_active_calls)],['保留样本内活跃 / 接通峰值',`${number(measured.activePeak)} / ${number(measured.establishedPeak)}`],['发生器累计建立成功',number(run.progress?.established_calls)],['发生器累计建立失败',number(run.progress?.setup_failed)],['媒体用户态 / 内核态累计 CPU 秒',`${number(sample?.user_cpu_seconds,2)} / ${number(sample?.system_cpu_seconds,2)}`],['各媒体进程自身峰值 RSS 之和',sample?.peak_resident_bytes==null?'—':`${number(sample.peak_resident_bytes/1048576,1)} MiB`],['测试控制 CPU 使用',controller?.cpu_cores_used==null?'—':`${number(controller.cpu_cores_used,2)} 核`],['测试控制协程 / 运行时线程',`${number(controller?.goroutines)} / ${number(controller?.runtime_threads)}`],['套接字丢弃 / 无效包',`${number(sample?.socket_rx_drops)} / ${number(sample?.invalid_packets)}`],['发送错误 / 排队丢弃 / 过期',`${number(sample?.send_errors)} / ${number(sample?.send_queue_drops)} / ${number(sample?.send_expired)}`]];
  const recent=view.snapshot?.current&&view.snapshot?.history?.[0];
  const recentMarkup=recent?`<div class="overview-recent-test"><strong>最近结束的测试</strong><span>${escapeHTML(recent.id)} · ${number(recent.concurrency)} 路 · ${escapeHTML(statuses[recent.status]||recent.status)} · ${escapeHTML(overviewTime(recent.finished_at))}</span><a id="overview-history-link" href="#tests">查看历史记录</a></div>`:'';
  const taskError=run.error?`<div class="notice error">${run.failure?.stage==='preflight'?'资源预检未通过：':'任务错误：'}${escapeHTML(run.error)}</div>`:'';
  return `<section class="overview-test-section" data-overview-source="isolated-test"><div class="overview-source-heading"><div><span class="eyebrow">隔离测试实例</span><h2>${ended?'最近测试':'正在测试'} · ${run.mode==='phone'?'单路电话':'并发压力'} · ${number(run.concurrency)} 路</h2><p>来源：/v1/tests${view.snapshot?.current?.id===run.id?'.current':'/'+escapeHTML(run.id)} · 任务 <span class="mono">${escapeHTML(run.id)}</span></p></div><div class="overview-source-actions">${badge(stateLabel,readingStale?'warning':run.status==='failed'?'error':ended?'neutral':'info')}${link}</div></div>${error}${view.detailError?`<div class="notice warning">${escapeHTML(view.detailError)}</div>`:''}${taskError}<div class="overview-test-sampling ${measured.stale||readingStale?'stale':''}">${escapeHTML(sampleNote)}${measured.stale?`<strong>${measured.invalidTime||measured.future?'采样时刻异常，请检查服务时钟':'采样超过 10 秒未更新，数值已过期'}</strong>`:''}<span>列表读取于 ${escapeHTML(overviewTime(view.readAt))} · ${escapeHTML(phases[run.phase]||run.phase||'等待阶段信息')}${ended?' · 历史值，不是实时负载':''}</span></div><div class="overview-test-metrics">${metrics}</div>${peakMarkup}<div class="overview-test-trend"><div class="chart-legend"><span><i class="legend-dot test-active"></i>隔离实例活跃</span><span><i class="legend-dot secondary"></i>隔离实例已接通</span></div>${overviewTestChart(measured.samples)}</div><details id="overview-test-details" class="overview-test-details" ${detailsOpen?'open':''}><summary id="overview-test-details-summary">查看测试资源与采样边界</summary><div class="definition-list">${rows.map(([label,value])=>`<div><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value)}</dd></div>`).join('')}</div><p>峰值来自后端保留的最近 ${number(measured.samples.length)} 个样本（最多 300），可能错过采样间的瞬时峰值。CPU 与内存为隔离媒体进程汇总，未提供逐 worker 明细；RSS 并非同一时刻总峰值。平台可能不提供套接字丢弃计数，零值不证明无丢包。完整双向丢包和验收结果请查看任务报告；任务完成不自动等于测试通过。</p></details>${recentMarkup}</section>`;
}

/* 主服务总览保留自己的数据源；刷新隔离测试不会修改state.status/history或编辑草稿。 */
function renderOverview() {
  const root = document.getElementById('page-content'), data = state.status;
  const focusId=document.activeElement?.id, detailsOpen=document.getElementById('overview-test-details')?.open===true;
  const testsPanel=renderOverviewTests(detailsOpen);
  const restoreFocus=()=>{if(focusId)document.getElementById(focusId)?.focus({preventScroll:true});};
  if (!data) { root.innerHTML = `${testsPanel}<div class="${state.loading ? 'loading-state' : 'empty-state'}">${state.loading ? '<span class="spinner"></span>' : ''}<h2>${state.loading ? '正在读取运行状态' : '暂时无法连接服务'}</h2><p>${escapeHTML(state.lastError || '等待服务返回第一个有效样本。')}</p><button class="button button-secondary" data-action="refresh" style="margin-top:18px">重新连接</button></div>`; restoreFocus();return; }
  const guard = data.guard || state.config?.guard || {}, policy = guard.policy || {}, workers = data.workers || [];
  const hard = state.config?.active?.limits?.max_calls, cap = policy.enabled ? policy.max_active_calls : hard;
  const healthy = workers.filter(item => item.healthy).length, rx = workers.some(item => item.stats?.rx_packets != null) ? workers.reduce((sum,item) => sum + (Number(item.stats?.rx_packets) || 0),0) : null, tx = workers.some(item => item.stats?.tx_packets != null) ? workers.reduce((sum,item) => sum + (Number(item.stats?.tx_packets) || 0),0) : null;
  const activeRatio = cap > 0 ? Math.min(100,100 * data.active_calls / cap) : 0;
  const metrics = [
    ['活跃通话', number(data.active_calls), `保护上限 ${number(cap)} 路`, 'calls', `<div class="metric-progress"><span style="width:${activeRatio}%"></span></div>`],
    ['已接通通话', number(data.established_calls), `建立中 ${number(guard.establishing_calls ?? Math.max(0,data.active_calls-data.established_calls))} 路`, 'activity',''],
    ['媒体分片', `${healthy}<small>/ ${workers.length}</small>`, `${workers.filter(item => item.admission?.reason).length} 个分片暂缓新准入`, 'cpu',''],
    ['准入速率', number(guard.effective_cps,1), `当前允许的新呼叫 / 秒`, 'shield','']
  ];
  const alertItems = alerts();
  const workersBody = workers.length ? `<div class="table-scroll"><table><thead><tr><th>媒体分片</th><th>状态</th><th>通话</th><th>CPU / 核</th><th>接收 / 发送包</th><th>重启次数</th><th>准入状态</th></tr></thead><tbody>${workers.map(worker => `<tr><td><span class="worker-name"><span class="worker-symbol">${escapeHTML(worker.id)}</span>媒体分片 ${escapeHTML(worker.id)}</span></td><td>${badge(worker.healthy ? '运行中' : '不可用',worker.healthy ? 'good':'error')}</td><td>${number(worker.stats?.active_calls)}</td><td><div class="inline-progress"><span class="bar"><i style="width:${Math.min(100,(worker.admission?.cpu_cores_used || 0)*100)}%"></i></span>${number(worker.admission?.cpu_cores_used,2)}</div></td><td class="mono">${number(worker.stats?.rx_packets)} / ${number(worker.stats?.tx_packets)}</td><td>${number(worker.restarts)}</td><td>${badge(reasonLabel(worker.admission?.reason),worker.admission?.reason ? 'warning':'neutral')}</td></tr>`).join('')}</tbody></table></div>` : '<div class="empty-state"><p>服务尚未返回媒体分片。</p></div>';
  root.innerHTML = `${testsPanel}<section class="overview-main-section" data-overview-source="main-service"><div class="overview-source-heading"><div><span class="eyebrow">主服务实例</span><h2>业务服务运行状态</h2><p>来源：/v1/status · 读取于 ${escapeHTML(overviewTime(state.statusReadAt))} · 此处不包含隔离压测负载</p></div><a id="overview-direction-link" class="button button-quiet button-small" href="#api-docs?document=api/voice-runtime-direction.md">云蝠 Voice Runtime 方向</a></div>${!state.online ? '<div class="notice warning">主服务连接已中断，以下为上一次成功采集的结果。</div>' : ''}<div class="metrics-grid">${metrics.map(([label,value,note,name,progress]) => `<div class="metric-card"><div class="metric-top"><span>${label}</span><span class="metric-icon">${icon(name)}</span></div><div class="metric-value">${value}</div>${progress}<div class="metric-bottom">${escapeHTML(note)}</div></div>`).join('')}</div><div class="dashboard-grid">${panel('主服务通话趋势','仅显示本次打开控制台后的主服务采样',`${chart()}<div class="chart-summary"><span>观察峰值<strong>${number(Math.max(...state.history.map(item=>item.active || 0)))}</strong></span><span>运行时长<strong>${duration(data.uptime_seconds)}</strong></span></div>`,'<div class="chart-legend"><span><i class="legend-dot"></i>活跃</span><span><i class="legend-dot secondary"></i>已接通</span></div>')}${panel('服务健康','运行状态与持久化概况',`${healthRow('新呼叫准入',badge(...admissionState(data)),'shield')}${healthRow('事件日志',badge(data.journal_healthy ? '正常' : '异常',data.journal_healthy ? 'good':'error'),'file')}${healthRow('已同步事件',number(data.journal_synced_events),'layers')}${healthRow('累计丢失事件',number(data.journal_lost_events),'alert')}${healthRow('活跃定时任务',number(data.active_timers),'clock')}`)}</div><section class="panel" style="margin-bottom:22px"><div class="panel-header"><div><h2>媒体分片</h2><p>每个分片独立处理媒体与准入压力</p></div>${badge(`${workers.length} 个工作进程`)}</div>${workersBody}</section>${controllerPanel(data.controller)}${localConsumptionPanel(data,state.config?.active?.media,state.online)}<div class="dashboard-grid">${panel('状态提醒',`${alertItems.length} 条需要关注的当前状态`,alertItems.length ? alertItems.map(([type,title,description])=>`<div class="alert-item"><span class="alert-marker ${type}">${icon('alert')}</span><div><h3>${escapeHTML(title)}</h3><p>${escapeHTML(description)}</p></div></div>`).join('') : '<div class="alert-item"><span class="alert-marker good">'+icon('check')+'</span><div><h3>当前未发现状态告警</h3><p>此结果依据可用指标，不代表完整性能或兼容验收通过。</p></div></div>')}${panel('日志与观察','事件日志内容未通过管理接口提供',`<div class="definition-list" style="margin-bottom:16px"><div><dt>累计媒体接收</dt><dd>${number(rx)} 包</dd></div><div><dt>累计媒体发送</dt><dd>${number(tx)} 包</dd></div></div><div class="log-list">${state.observations.map(item=>`<div class="log-item"><span class="log-time">${new Date(item.at).toLocaleTimeString('zh-CN',{hour12:false})}</span><span class="log-message">${escapeHTML(item.message)}</span></div>`).join('')}</div><p class="field-help">以上为本次浏览器实际观察的状态变化；刷新页面后清空。</p>`)}</div></section>`;
  restoreFocus();
}

/* 字段元数据覆盖当前原型全部配置，类型、范围和单位与服务端配置契约一致。 */
const fields = {
  'sip.dialplan.file':['XML 拨号计划文件','部署只读','readonly',null,null,'可选的启动级XML文件；启用后按所选context匹配号码，未匹配返回404。后台FreeSWITCH配置编辑仍为导出草稿。'],
  'sip.dialplan.context':['拨号计划 context','部署只读','readonly',null,null,'未配置时不加载XML；已配置且context留空时使用default。'],
  'media.playback_root':['WAV 放音根目录','部署只读','readonly',null,null,'空值关闭文件放音。启用后仅播放该根目录内受限WAV；最长30秒，8kHz单声道。'],
  'sip.local_extensions':['本地接听号码','每行一个','extensions',null,null,'这些号码由本机接听并可执行提示音与收号；最多256个精确号码，仅数字和加号。留空继续转发固定上游，保存后需重启。'],
  'limits.max_calls':['并发通话硬上限','路','number',1,16128,'最多同时预留的通话数量，每路占用四个媒体端口。'],
  'limits.calls_per_second':['每秒新呼叫硬上限','次 / 秒','number',1,100000,'启动级准入上限，峰值保护不能超过此值。'],
  'limits.burst_calls':['突发呼叫容量','次','number',1,1000000,'允许短时间内突发到达的呼叫令牌容量。'],
  'limits.max_transactions':['SIP 事务表容量','项','number',2,10000000,'不得低于并发通话硬上限的两倍。'],
  'sip.listen':['SIP 监听地址','IPv4:端口','text',null,null,'本机用于接收 SIP/UDP 的地址和端口。'],
  'sip.advertise':['SIP 对外地址','IPv4:端口','text',null,null,'终端与上游可访问的信令地址。'],
  'sip.upstream':['上游信令地址','IPv4:端口','text',null,null,'不能指向当前服务的监听地址或对外地址。'],
  'sip.trusted_networks':['信令来源白名单','IPv4 CIDR','list',null,null,'每行一个网段，例如 192.0.2.0/24，至少保留一个。'],
  'sip.setup_timeout_ms':['呼叫建立超时','毫秒','number',1000,180000,'超过建立期限后终止本次呼叫建立。'],
  'sip.ack_timeout_ms':['接通确认超时','毫秒','number',1000,64000,'等待最终应答对应 ACK 的期限。'],
  'sip.max_call_seconds':['最长通话时长','秒','number',1,86400,'从接通确认开始计时；长时测试需要相应增大。'],
  'media.binary':['媒体程序路径','只读','readonly',null,null,'敏感执行路径保持当前值，请由受控部署流程修改。'],
  'media.bind_ip':['媒体监听地址','IPv4','text',null,null,'本机 RTP/RTCP socket 绑定的地址。'],
  'media.advertise_ip':['媒体对外地址','IPv4','text',null,null,'写入 SDP 的媒体地址。'],
  'media.port_start':['媒体起始端口','偶数端口','number',1024,65534,'范围必须从非特权偶数端口开始。'],
  'media.port_end':['媒体结束端口','端口','number',1025,65535,'包含该端口；同时预留端口隔离期的余量。'],
  'media.processing':['媒体处理模式','','processing',null,null,'relay 保留同编码透传；g711 启用 8 kHz / 20 ms PCM 处理，双腿桥接与本地 IVR 分别检查实际能力。G.722/Opus 实时转码尚未接入。保存后需受控重启生效。'],
  'media.workers':['媒体工作进程','个','number',1,64,'分片独立处理媒体；数量不得高于并发通话上限。'],
  'media.receive_buffer_bytes':['每 socket 接收缓冲','字节','number',16384,1048576,'实际缓冲容量同时受操作系统限制。'],
  'media.port_reuse_delay_ms':['端口复用隔离期','毫秒','number',0,60000,'通话结束后保留端口，降低迟到报文误入新通话的风险。'],
  'media.max_packets_per_second_per_leg':['每条呼叫腿包率上限','包 / 秒','number',100,10000,'超过预算的数据包进入限流统计。'],
  'media.allowed_remote_networks':['媒体来源白名单','IPv4 CIDR','list',null,null,'每行一个允许的媒体来源网段。'],
  'media.cpu_cores':['CPU 核心绑定','核心编号','integers',null,null,'仅 Linux 支持；留空自动调度，填写时每个工作进程对应一个核心。'],
  'media.connect_sockets':['使用连接式 UDP','','boolean',null,null,'协商后绑定固定媒体对端，使用连接式发送路径。'],
  'media.adaptive_admission':['按媒体压力动态准入','','boolean',null,null,'根据工作进程 CPU、已观测丢包和控制积压暂缓新呼叫。'],
  'media.admission_cpu_high':['CPU 暂停准入门限','占用核心数','decimal',0.5,1,'达到高门限后暂缓本分片新呼叫。'],
  'media.admission_cpu_low':['CPU 恢复准入门限','占用核心数','decimal',0.01,0.99,'必须低于高门限，连续健康样本后恢复。'],
  'admin.listen':['管理接口监听地址','本机 IPv4:端口','text',null,null,'当前版本只允许回环地址；更换端口后需用新地址访问。'],
  'journal.path':['事件日志文件路径','只读','readonly',null,null,'持久化数据路径保持当前值，请由受控部署流程修改。'],
  'journal.queue_capacity':['日志队列容量','条','number',64,65536,'有界事件队列，需结合磁盘写入能力选择容量。']
};
const guardFields = {
  enabled:['开启峰值保护','','boolean',null,null,'即时控制新呼叫准入，不影响已有通话的正常释放。'],
  max_active_calls:['并发通话保护上限','路','number',1,16128,'不能超过当前启动配置的并发硬上限。'],
  max_establishing_calls:['同时建立中的呼叫上限','路','number',1,16128,'活跃资源数减去已接通数，也包括等待释放的资源。'],
  calls_per_second:['新呼叫速率上限','次 / 秒','number',1,100000,'不能超过当前启动配置的每秒呼叫硬上限。'],
  burst_calls:['保护层突发容量','次','number',1,1000000,'控制保护层短时突发令牌数量。'],
  soft_limit_ratio:['开始柔性降速的位置','容量比例','decimal',0.01,0.99,'例如 0.80 表示达到保护并发上限的 80% 后开始降速。'],
  minimum_rate_ratio:['最低准入速率比例','速率比例','decimal',0.01,1,'高负载降速期间保留的速率比例。'],
  retry_after_seconds:['拒接后的建议重试时间','秒','number',1,3600,'通过 SIP Retry-After 告知上游建议等待时间。']
};
/* 根据字段元数据生成中文输入控件，并显示有效值与草稿的差别。 */
function field(path, guard = false) { const spec = (guard ? guardFields : fields)[path]; if (!spec) return ''; const [label,unit,type,min,max,help] = spec; const value = guard ? state.guardDraft[path] : getPath(state.configDraft,path), active = guard ? state.guardBase[path] : getPath(state.config.active,path); const id = `field-${guard ? 'guard-' : ''}${path.replaceAll('.','-')}`; const attributes = `${guard ? 'data-guard' : 'data-config'}="${escapeHTML(path)}" id="${id}"`; const current = changed(value,active) ? `<div class="field-current">当前${guard ? '生效':'有效'}：${escapeHTML(type === 'boolean' ? active ? '开启':'关闭' : inputValue(active) || '空')} ${escapeHTML(unit)}</div>` : ''; if (type === 'boolean') return `<div class="form-field full-width"><div class="toggle-field"><div><label class="field-label" for="${id}">${label}</label><p class="field-help">${help}</p>${current}</div><label class="switch"><input type="checkbox" ${attributes} ${value ? 'checked':''}><span class="slider"></span></label></div></div>`; const control = type === 'processing' ? `<select ${attributes} data-type="processing">${[['','默认透传（兼容旧配置）'],['relay','同编码 RTP 透传'],['g711','G.711 音频处理']].map(([key,label])=>`<option value="${key}" ${(value??'')===key?'selected':''}>${label}</option>`).join('')}</select>` : ['list','integers','extensions'].includes(type) ? `<textarea ${attributes} data-type="${type}" rows="3" spellcheck="false">${escapeHTML(inputValue(value))}</textarea>` : `<input ${attributes} data-type="${type}" type="${type === 'number' || type === 'decimal' ? 'number':'text'}" ${min == null ? '' : `min="${min}" max="${max}" step="${type === 'decimal' ? '.01':'1'}"`} ${type === 'readonly' ? 'readonly' : 'required'} value="${escapeHTML(inputValue(value))}" spellcheck="false">`; return `<div class="form-field ${['list','integers','extensions'].includes(type) || type === 'readonly' ? 'full-width':''}" data-field="${escapeHTML(path)}"><label class="field-label" for="${id}"><span>${label}${unit ? ` <span class="text-muted">· ${unit}</span>`:''}</span><code>${escapeHTML(path.split('.').at(-1))}</code></label>${control}<p class="field-help">${help}</p>${current}<p class="field-error" data-error="${escapeHTML(path)}" hidden></p></div>`; }
/* 展示保存语义和配置版本，不把待重启草稿误标为已生效。 */
function configBanner() { return `<div class="notice info">${icon('file')}<div class="notice-content"><strong>${state.config.restart_required ? '已有配置等待重启生效。' : '以下为启动配置草稿。'}</strong> 表单展示待重启配置；保存不会自动重启服务。当前版本 ${number(state.configRevision)}。</div>${badge(state.config.restart_required ? '待重启':'与当前一致',state.config.restart_required ? 'warning':'good')}</div>`; }
/* 展示端口预算及变更清单，给容量选择提供可核对的具体数字。 */
function configAside() { const config = state.configDraft, blocks = Math.floor((Number(config.media.port_end)-Number(config.media.port_start)+1)/4), required = Number(config.limits.max_calls)+Math.ceil(Number(config.limits.calls_per_second)*Number(config.media.port_reuse_delay_ms)/1000); const differences = Object.keys(fields).filter(path=>changed(getPath(config,path),getPath(state.configBase,path))); return `<aside class="config-aside"><div class="panel"><div class="aside-section"><h3>媒体端口预算</h3><dl class="definition-list"><div><dt>可分配通话槽位</dt><dd>${number(blocks)}</dd></div><div><dt>并发与隔离期需求</dt><dd>${number(required)}</dd></div><div><dt>剩余槽位</dt><dd class="${blocks<required ? 'text-danger':'text-good'}">${number(blocks-required)}</dd></div></dl><p class="field-help" style="margin-top:14px">每路四个端口。预算只反映资源容量，不代表已通过性能验收。</p></div><div class="aside-section"><h3>本地修改 · ${differences.length} 项</h3>${differences.length ? `<ul class="changes-list">${differences.map(path=>`<li>${escapeHTML(fields[path][0])}</li>`).join('')}</ul>` : '<p>当前没有未保存的启动配置修改。</p>'}</div></div></aside>`; }
/* 峰值保护即时生效；启动硬上限在独立分组中保存待重启。 */
function renderProtection() { document.getElementById('page-content').innerHTML = `<div class="config-layout"><div class="section-stack">${panel('动态峰值保护','本组保存后即时生效，并持久化到管理配置',`<div class="preset-card"><div><div class="preset-values"><strong>8,000</strong><span>路并发</span><strong>1,000</strong><span>次 / 秒</span></div><p>应用建议值时会受当前启动硬上限约束；并发和速率是不同单位。</p></div><button class="button button-secondary button-small" data-action="guard-preset">使用建议值</button></div><form id="guard-form"><div class="form-grid">${Object.keys(guardFields).map(path=>field(path,true)).join('')}</div></form><div class="capacity-note">当前启动硬上限：${number(state.config.active.limits.max_calls)} 路并发，${number(state.config.active.limits.calls_per_second)} 次 / 秒。</div><div id="guard-live-summary" class="capacity-note" aria-live="polite"></div><div class="dialog-actions"><button class="button button-secondary" data-action="guard-reset">撤销本地修改</button><button class="button button-primary" data-action="guard-save" ${state.busy ? 'disabled':''}>立即应用峰值保护</button></div>`,badge('即时生效','good'))}<div>${configBanner()}${panel('启动级容量硬上限','本组属于启动配置，修改后需重启',`<form class="config-form"><div class="form-grid">${['limits.max_calls','limits.calls_per_second','limits.burst_calls','limits.max_transactions'].map(path=>field(path)).join('')}</div></form><div class="dialog-actions"><button class="button button-secondary button-small" data-action="capacity-preset">填写 8,000 路容量草稿</button></div>`,badge('保存后待重启','warning'))}</div></div>${configAside()}</div>`; updateGuardSummary(); updateSavebar(); }
/* 注册状态独立刷新，避免覆盖用户尚未保存的配置。 */
function updateRegistrationStatus(){const target=document.getElementById('registration-status');if(!target)return;const value=state.status?.sip_registration;if(!value){target.textContent='尚未取得注册状态';return;}const fresh=value.state==='registered'&&Number.isFinite(Date.parse(value.expires_at))&&Date.parse(value.expires_at)>Date.now();const labels={disabled:'未启用',registering:'正在注册',registered:'已注册',retrying:'等待重试',unregistered:'已注销',unregister_failed:'注销未确认'};target.innerHTML=`${badge(value.state==='registered'&&!fresh?'注册有效期已过，等待刷新':labels[value.state]||value.state,fresh&&state.online?'good':value.enabled?'warning':'neutral')} ${value.last_status?`最近响应 ${number(value.last_status)}`:''}${value.expires_at?` · 有效至 ${escapeHTML(new Date(value.expires_at).toLocaleString('zh-CN'))}`:''}${!state.online?' · 服务离线，以上为上次状态':''}`;}
/* 信令与媒体页覆盖全部运行地址、白名单、定时器、缓冲及工作进程配置。 */
function renderMedia() { const sections = [ ['SIP 连接','可信线路支持 UDP；TCP/TLS、注册与认证按启动配置启用',['sip.listen','sip.advertise','sip.upstream','sip.trusted_networks']], ['本地 IVR 接听','精确号码在媒体准备成功后自动应答，由控制接口驱动提示音和收号',['sip.local_extensions','sip.dialplan.file','sip.dialplan.context','media.playback_root']], ['SIP 定时器','按呼叫阶段管理建立、确认与最长时长',['sip.setup_timeout_ms','sip.ack_timeout_ms','sip.max_call_seconds']], ['媒体网络与端口','RTP 与 RTCP 使用独立端口',['media.bind_ip','media.advertise_ip','media.port_start','media.port_end','media.allowed_remote_networks']], ['媒体资源与调度','CPU 绑定仅在 Linux 上启用',['media.processing','media.workers','media.receive_buffer_bytes','media.port_reuse_delay_ms','media.max_packets_per_second_per_leg','media.cpu_cores','media.connect_sockets']], ['媒体压力保护','此设置属于启动配置，和即时峰值策略分别管理',['media.adaptive_admission','media.admission_cpu_high','media.admission_cpu_low']], ['媒体程序','敏感执行路径通过受控部署管理',['media.binary']] ]; document.getElementById('page-content').innerHTML = `${configBanner()}${panel('线路注册状态','状态来自真实注册事务；注册成功不等于线路呼叫与媒体质量已验收','<div id="registration-status"></div><p><a href="#api-docs?document=api/ai-calling-reference.md">线路注册、本地接听与收号接入文档</a></p>')}<div class="config-layout"><div class="section-stack">${sections.map(([title,description,list])=>panel(title,description,`<form class="config-form"><div class="form-grid">${list.map(path=>field(path)).join('')}</div></form>`)).join('')}</div>${configAside()}</div>`; updateRegistrationStatus(); }
/* 运维页提供真实排空/恢复入口；不提供未经服务端支持的自动重启按钮。 */
function renderOperations() { const data = state.status || {}; document.getElementById('page-content').innerHTML = `${configBanner()}<div class="config-layout"><div class="section-stack">${panel('管理接口','管理页面与 API 当前仅在本机回环地址提供',`<form class="config-form"><div class="form-grid">${field('admin.listen')}</div></form>`)}${panel('事件日志','查看持久化状态并设置有界日志队列',`<form class="config-form"><div class="form-grid">${field('journal.path')}${field('journal.queue_capacity')}</div></form><div class="form-divider"></div>${healthRow('日志写入健康',badge(data.journal_healthy == null ? '未采集':data.journal_healthy ? '正常':'异常',data.journal_healthy ? 'good':'neutral'),'file')}${healthRow('已同步事件',number(data.journal_synced_events),'check')}${healthRow('累计丢失事件',number(data.journal_lost_events),'alert')}`)}${panel('通话排空','排空期间保留现有通话，停止接纳新呼叫',`<p class="text-muted" style="font-size:12px;line-height:1.9">当前${data.draining ? '正在排空' : '允许接纳新呼叫'}，活跃通话 ${number(data.active_calls)} 路。由停止信号触发的退出流程不能通过恢复准入取消。</p><div class="dialog-actions"><button class="button ${data.draining ? 'button-primary':'button-danger'}" data-action="${data.draining ? 'resume':'drain'}">${data.draining ? '恢复新呼叫准入':'开始排空'}</button></div>`)}${panel('指标与配置','保留原有指标接口和完整配置查看方式',`<div class="health-row"><span>Prometheus 指标</span><a href="/metrics" target="_blank" rel="noopener" class="button button-secondary button-small">查看指标</a></div><div class="health-row"><span>有效与待重启配置</span><button class="button button-secondary button-small" data-action="download-config">下载当前配置快照</button></div>`)}</div>${configAside()}</div>`; }
/* 输入发生变化时只刷新旁侧预算和有效值说明，保留输入焦点与光标。 */
function updateConfigFeedback(input) { const aside=document.querySelector('.config-aside');if(aside)aside.outerHTML=configAside();const path=input.dataset.config,wrapper=input.closest('.form-field'),active=getPath(state.config.active,path);if(!wrapper)return;wrapper.querySelector('.field-current')?.remove();if(changed(getPath(state.configDraft,path),active)){const note=document.createElement('div');note.className='field-current';note.textContent=`当前有效：${fields[path][2]==='boolean'?(active?'开启':'关闭'):inputValue(active)} ${fields[path][1]}`;wrapper.append(note);}wrapper.classList.remove('invalid');const error=wrapper.querySelector('[data-error]');if(error)error.hidden=true; }
/* 从输入框类型读取值，服务端仍会做完整配置及安全规则校验。 */
function readInput(input) { if (input.type === 'checkbox') return input.checked; if (input.dataset.type === 'number' || input.dataset.type === 'decimal') return input.value === '' ? null : Number(input.value); if (['list','extensions'].includes(input.dataset.type)) return input.value.split(/[\n,]+/).map(value=>value.trim()).filter(Boolean); if (input.dataset.type === 'integers') return input.value.split(/[\s,]+/).filter(Boolean).map(value=>Number(value)); return input.value; }
/* 客户端校验帮助快速定位错误，但不替代服务端的 IP、安全路径和平台验证。 */
function validateConfiguration() { const config = state.configDraft, errors = []; for (const [path,spec] of Object.entries(fields)) { const value = getPath(config,path), type = spec[2]; if ((type === 'number' || type === 'decimal') && (!Number.isFinite(value) || value < spec[3] || value > spec[4] || (type === 'number' && !Number.isInteger(value)))) errors.push([path,`${spec[0]}必须为 ${spec[3]} 至 ${spec[4]} ${spec[1]}${type === 'number' ? '的整数':''}。`]); if ((type === 'text' || type === 'readonly') && !String(value || '').trim()) errors.push([path,`${spec[0]}不能为空。`]); if (type === 'list' && (!Array.isArray(value) || !value.length || value.some(item=>!validCIDR(item)))) errors.push([path,'请填写有效的 IPv4 CIDR，每行一个，例如 192.0.2.0/24。']); }
  if(!['','relay','g711'].includes(config.media.processing??''))errors.push(['media.processing','请选择支持的媒体处理模式。']);
  const extensions=config.sip.local_extensions; if(extensions!=null && (!Array.isArray(extensions) || extensions.length>256 || new Set(extensions).size!==extensions.length || extensions.some(value=>typeof value!=='string'||!/^[+0-9]{1,32}$/.test(value)))) errors.push(['sip.local_extensions','请填写最多256个不重复号码，每个1至32个字符，仅数字和加号。']);
  if (config.media.port_start % 2) errors.push(['media.port_start','媒体起始端口必须为偶数。']);
  const blocks = Math.floor((config.media.port_end-config.media.port_start+1)/4), need = config.limits.max_calls+Math.ceil(config.limits.calls_per_second*config.media.port_reuse_delay_ms/1000);
  if (blocks < need) errors.push(['media.port_end',`媒体端口容量不足：当前 ${blocks} 组，至少需要 ${need} 组（含隔离期）。`]);
  if (config.media.adaptive_admission && config.media.admission_cpu_low >= config.media.admission_cpu_high) errors.push(['media.admission_cpu_low','恢复门限必须低于暂停门限。']);
  if (config.limits.max_transactions < config.limits.max_calls*2) errors.push(['limits.max_transactions','事务表容量至少为并发硬上限的两倍。']);
  if (config.media.workers > config.limits.max_calls) errors.push(['media.workers','工作进程数不能高于并发硬上限。']);
  if (config.media.cpu_cores?.some(value=>!Number.isInteger(value)||value<0||value>1023) || (config.media.cpu_cores?.length && config.media.cpu_cores.length !== config.media.workers)) errors.push(['media.cpu_cores','核心编号应为 0 至 1023 的整数，填写数量须等于工作进程数。']);
  if (config.sip.upstream === config.sip.listen || config.sip.upstream === config.sip.advertise) errors.push(['sip.upstream','上游不能指向本服务的监听或对外地址。']);
  document.querySelectorAll('[data-error]').forEach(node=>{node.hidden=true;node.closest('.form-field')?.classList.remove('invalid');});
  errors.forEach(([path,message])=>{const node=document.querySelector(`[data-error="${path}"]`);if(node){node.textContent=message;node.hidden=false;node.closest('.form-field')?.classList.add('invalid');}});
  if (errors.length) throw new Error(errors[0][1]); return config;
}
/* 检查 IPv4 CIDR 的数值边界，避免只按字符串格式误判。 */
function validCIDR(value) { const parts=String(value).split('/');return parts.length===2 && validIPv4(parts[0]) && /^\d+$/.test(parts[1]) && Number(parts[1])>=0 && Number(parts[1])<=32; }
function validIPv4(value) { const parts=String(value).split('.');return parts.length===4 && parts.every(part=>/^\d{1,3}$/.test(part)&&Number(part)>=0&&Number(part)<=255); }
/* 启动配置提交使用乐观版本锁，成功后仍明确显示待重启。 */
async function saveConfig(validateOnly = false) { if (state.busy || !state.config) return; try { const config=validateConfiguration(); if (validateOnly) { toast('本地字段校验通过；保存时服务端还会校验地址、平台与安全规则。'); return; } state.busy=true;updateSavebar();const previous=state.configRevision;const result=await request('/v1/config',{method:'PUT',body:{revision:previous,config}});advanceRevision(previous,result.revision);state.config=result;state.csrf=result.csrf_token||state.csrf;state.configDraft=clone(result.desired);state.configBase=clone(result.desired);state.configRevision=result.revision;document.getElementById('global-notice').hidden=true;toast(result.restart_required ? '配置已保存，重启服务后生效。':'配置已保存，与当前有效配置一致。');renderPage(); } catch(error){showError(error);}finally{state.busy=false;updateSavebar();} }
/* 即时保护策略单独保存，不会同时写入启动配置草稿。 */
async function saveGuard() { if(state.busy)return;try{const policy=state.guardDraft;for(const [key,spec]of Object.entries(guardFields)){if(spec[2]==='boolean')continue;const value=policy[key];if(!Number.isFinite(value)||value<spec[3]||value>spec[4]||(spec[2]==='number'&&!Number.isInteger(value)))throw new Error(`${spec[0]}超出允许范围。`);}if(policy.max_active_calls>state.config.active.limits.max_calls)throw new Error('并发保护上限超过当前启动硬上限，请先修改硬容量并重启。');if(policy.max_establishing_calls>state.config.active.limits.max_calls)throw new Error('建立中保护上限超过当前启动并发硬上限。');if(policy.burst_calls>state.config.active.limits.burst_calls)throw new Error('突发保护容量超过当前启动突发硬上限。');if(policy.calls_per_second>state.config.active.limits.calls_per_second)throw new Error('速率保护上限超过当前启动硬上限，请先修改硬容量并重启。');state.busy=true;updateSavebar();const previous=state.guardRevision;const result=await request('/v1/guard',{method:'PUT',body:{revision:previous,policy}});advanceRevision(previous,result.revision);state.guardRevision=result.revision;state.guardBase=clone(result.guard.policy);state.guardDraft=clone(result.guard.policy);state.config.guard=result.guard;if(state.status)state.status.guard=result.guard;toast('峰值保护已立即生效，并完成保存。');renderPage();}catch(error){showError(error);}finally{state.busy=false;updateSavebar();} }
/* 保存栏根据当前页面显示不同保存动作；不把 FS 草稿称为运行配置。 */
function updateSavebar() {
  /* 保存期间锁定所有编辑入口，防止响应覆盖请求发出后的新输入。 */
  document.querySelectorAll('[data-config],[data-guard],[data-fs],#xml-path,#xml-file-select,#xml-editor,#entity-form input,#entity-form select,#add-parameter-form input,#add-parameter-form select').forEach(node => { node.disabled = state.busy || (['xml-editor','xml-path','xml-file-select'].includes(node.id) && state.xmlBusy); });
  document.querySelectorAll('[data-action="guard-save"],[data-action="guard-reset"],[data-action="guard-preset"],[data-action="capacity-preset"],[data-action="reload-config"],[data-action="reload-fs"],[data-action="fs-mode"],[data-action="new-entity"],[data-action="add-parameter"],#entity-form button,#add-parameter-form button').forEach(node => { node.disabled = state.busy || (state.xmlBusy && ['fs-mode','new-entity','add-parameter'].includes(node.dataset?.action)); });
  const guardSave = document.querySelector('[data-action="guard-save"]'); if (guardSave) { guardSave.disabled = state.busy || !guardDirty(); guardSave.textContent = state.busy ? '正在保存…' : '立即应用峰值保护'; }
  const bar=document.getElementById('savebar'), fs=state.page==='freeswitch';bar.hidden=!['protection','media','operations','freeswitch'].includes(state.page)||(!fs&&!state.config)||(fs&&!state.fs);if(bar.hidden)return;
  const dirty=fs ? state.fsMode==='xml'?xmlDirty():fsDirty() : configDirty();document.getElementById('savebar-title').innerHTML=`${dirty?'<i class="edit-dot"></i>':''}${dirty?'有未保存的本地修改':fs?'FreeSWITCH 配置草稿':'启动配置草稿已同步'}`;document.getElementById('savebar-description').textContent=fs?'用于导出标准配置，当前 RustSwitch 引擎不会执行。':'保存不会重启服务；当前有效配置保持不变。';document.getElementById('save-button').textContent=state.busy?'正在保存…':fs?state.fsMode==='xml'?'保存 XML 草稿':'保存参数草稿':'保存待重启配置';document.getElementById('save-button').disabled=state.busy||!dirty||(fs&&state.xmlBusy);document.getElementById('validate-button').hidden=fs;document.getElementById('validate-button').disabled=state.busy;document.getElementById('discard-button').disabled=state.busy||!dirty;
}

/* 将官方参数展平供分类和搜索使用，保留稳定 ID、来源文件及 XML 作用域。 */
function fsParameters() { return (state.fs?.files || []).flatMap(file => (file.parameters || []).map(parameter => ({...parameter,file:file.path,category:file.category || '其他模块'}))); }
/* FreeSWITCH 配置总览只标识编辑/导出覆盖，运行支持始终单独说明。 */
function renderFS() {
  const root=document.getElementById('page-content');
  if(!state.fs){root.innerHTML=`<div class="${state.fsLoading?'loading-state':'empty-state'}">${state.fsLoading?'<span class="spinner"></span>':''}<h2>${state.fsLoading?'正在加载官方配置目录':'FreeSWITCH 配置目录暂不可用'}</h2><p>目录由本地服务提供，无需连接外部网站。</p><button class="button button-secondary" data-action="reload-fs" style="margin-top:18px">重新载入目录</button></div>`;return;}
  const all=fsParameters(),categories=[...new Set(state.fs.files.map(file=>file.category||'其他模块'))];
  const filtered=all.filter(parameter=>(state.fsCategory==='all'||parameter.category===state.fsCategory)&&(state.fsFile==='all'||parameter.file===state.fsFile)&&`${parameter.name} ${parameter.value} ${parameter.scope} ${parameter.file}`.toLowerCase().includes(state.fsSearch.toLowerCase()));
  const pagesCount=Math.max(1,Math.ceil(filtered.length/state.fsPageSize));state.fsPage=Math.min(state.fsPage,pagesCount);const shown=filtered.slice((state.fsPage-1)*state.fsPageSize,state.fsPage*state.fsPageSize);
  const options=state.fs.files.map(file=>`<option value="${escapeHTML(file.path)}" ${state.fsFile===file.path?'selected':''}>${escapeHTML(file.path)}</option>`).join('');
  const advancedOptions=state.fs.files.map(file=>`<option value="${escapeHTML(file.path)}" ${state.xmlPath===file.path?'selected':''}>${escapeHTML(file.path)}</option>`).join('');
  const parameterCards=shown.map((parameter,index)=>{const edited=Object.hasOwn(state.fsDraft,parameter.id),value=edited?state.fsDraft[parameter.id]:parameter.value,sensitive=/password|passwd|secret|credential/i.test(parameter.name),id=`fs-parameter-${index}`;return `<article class="parameter-card ${edited?'edited':''}"><div class="parameter-top"><div><label class="parameter-name" for="${id}">${escapeHTML(parameter.name)}</label><p class="parameter-path">${escapeHTML(parameter.file)}<br>${escapeHTML(parameter.scope)}</p></div>${badge(edited?'未保存':'仅配置导出',edited?'warning':'neutral')}</div><div class="parameter-edit"><div><input id="${id}" type="${sensitive?'password':'text'}" data-fs="${escapeHTML(parameter.id)}" value="${escapeHTML(value)}" spellcheck="false" autocomplete="off"><p class="field-help">${escapeHTML(parameter.description || '按原模块语义填写；当前引擎不执行。')}</p></div><div class="parameter-meta"><span>所属分类<br><code>${escapeHTML(parameter.category)}</code></span><span>当前引擎<br>未执行此 XML</span></div></div></article>`;}).join('');
  root.innerHTML=`<div class="notice warning">${icon('file')}<div class="notice-content"><strong>此处编辑的XML仅保存为导出草稿。</strong><br>运行内核可通过部署文件加载受限拨号计划；此处保存不会自动加载该计划或 Sofia、目录、会议、脚本模块。</div>${badge(`参考 ${state.fs.reference_version||'1.11.3'}`,'warning')}</div><div class="fs-toolbar"><div class="search-wrap">${icon('search')}<input id="fs-search" class="search-input" placeholder="搜索参数名称、取值或文件路径…" aria-label="搜索 FreeSWITCH 参数" value="${escapeHTML(state.fsSearch)}"></div><select class="select-input file-selector" id="fs-file-filter" aria-label="按配置文件筛选"><option value="all">全部配置文件</option>${options}</select><div class="fs-actions"><button class="button button-secondary" data-action="fs-mode">${state.fsMode==='xml'?'参数表单':'XML 编辑器'}</button><button class="button button-secondary" data-action="new-entity">${icon('plus')}新建业务配置</button><button class="button button-primary" data-action="export-fs">${icon('download')}导出 ZIP</button></div></div><div class="fs-summary"><span>${number(state.fs.files.length)} 份文件 · ${number(all.length)} 个可编辑条目 · ${number(state.fs.modified_files)} 份已修改文件</span><span>管理版本 ${number(state.fsRevision)}</span></div>${state.entityType?entityForm():''}${state.fsMode==='xml'?`<section class="panel"><div class="panel-header"><div><h2>XML 高级编辑</h2><p>支持目录、拨号计划、网关和新文件；只校验保存，不执行预处理指令。</p></div>${badge('导出草稿','warning')}</div><div class="panel-body"><div class="form-grid"><div class="form-field full-width"><label class="field-label" for="xml-file-select">已有配置文件</label><select id="xml-file-select"><option value="">选择文件以读取 XML</option>${advancedOptions}</select></div><div class="form-field full-width"><label class="field-label" for="xml-path">配置相对路径</label><input id="xml-path" value="${escapeHTML(state.xmlPath)}" placeholder="例如 dialplan/default/90_custom.xml" spellcheck="false"><p class="field-help">仅允许配置树内的相对 .xml 路径，不写入任意主机文件。</p></div><div class="form-field full-width"><label class="field-label" for="xml-editor">XML 内容 ${state.xmlBusy?'<span>正在读取…</span>':''}</label><textarea id="xml-editor" rows="22" style="font-family:monospace;font-size:12px;line-height:1.7;min-height:430px" spellcheck="false" ${state.xmlBusy?'disabled':''}>${escapeHTML(state.xmlDrafts[state.xmlPath] || '')}</textarea><p class="field-help">修改结构后，参数列表将重新生成。此编辑器不验证 FreeSWITCH 模块业务语义。</p></div></div><div class="dialog-actions"><button class="button button-secondary" data-action="add-parameter">向此 XML 新增参数</button></div>${state.addParameter?addParameterForm():''}</div></section>`:`<div class="fs-layout"><aside class="panel category-panel" aria-label="参数分类"><button class="category-button ${state.fsCategory==='all'?'active':''}" data-category="all">全部分类<span>${number(all.length)}</span></button>${categories.map(category=>`<button class="category-button ${state.fsCategory===category?'active':''}" data-category="${escapeHTML(category)}">${escapeHTML(category==='SIP'?'Sofia / SIP':category)}<span>${number(all.filter(parameter=>parameter.category===category).length)}</span></button>`).join('')}</aside><section class="panel"><div class="panel-header"><div><h2>${state.fsCategory==='all'?'官方配置参数':escapeHTML(state.fsCategory)}</h2><p>可同时修改多个参数后统一保存</p></div>${badge(`${number(filtered.length)} 项`)}</div>${parameterCards||'<div class="empty-state"><h2>没有匹配的参数</h2><p>尝试其他关键词或清除分类、文件筛选。没有参数的文件可通过 XML 编辑器修改。</p></div>'}<div class="pagination"><span>第 ${state.fsPage} / ${pagesCount} 页</span><div class="pagination-controls"><button class="button button-secondary button-small" data-action="fs-prev" ${state.fsPage<=1?'disabled':''}>上一页</button><button class="button button-secondary button-small" data-action="fs-next" ${state.fsPage>=pagesCount?'disabled':''}>下一页</button></div></div></section></div>`}<p class="reference-note">参数目录来自固定 FreeSWITCH 版本。参数数量表示编辑覆盖范围，不作为功能兼容率或通过率。</p>`;
  updateSavebar();
}
/* 保存本次参数修改，服务端保留未修改属性和原 XML 格式。 */
async function saveFS() { if(state.busy||!state.fs)return;try{if(Object.keys(state.xmlDrafts).some(path=>xmlDirty(path)))throw new Error('请先保存或撤销 XML 草稿，再保存参数表单。');state.busy=true;updateSavebar();const previous=state.fsRevision,result=await request('/v1/fs-config',{method:'PUT',body:{revision:previous,overrides:state.fsDraft}});advanceRevision(previous,result.revision);state.fs=result;state.fsRevision=result.revision;state.fsBase=clone(result.overrides||{});state.fsDraft=clone(result.overrides||{});state.xmlDrafts={};state.xmlBases={};toast('FreeSWITCH 参数草稿已保存，仅用于导出，未应用到当前引擎。');renderPage();}catch(error){showError(error);}finally{state.busy=false;updateSavebar();} }
/* 保持每个 XML 文件自己的编辑缓存；读取失败不清空其他文件的草稿。 */
async function openXML(path) { if(!path||state.busy||state.xmlBusy)return;state.xmlPath=path;state.fsMode='xml';state.addParameter=false;if(state.xmlDrafts[path]!=null){renderFS();return;}state.xmlBusy=true;renderFS();try{const result=await request(`/v1/fs-config/file?path=${encodeURIComponent(path)}`);state.xmlDrafts[path]=result.xml;state.xmlBases[path]=result.xml;if(state.fsRevision!==result.revision){const error=new Error('读取到较新的配置版本，请重新载入后再保存已有参数草稿。');error.status=409;showError(error);}}catch(error){showError(error);}finally{state.xmlBusy=false;renderPage();} }
/* 单个 XML 文件保存后重新取目录，保留其他尚未保存的 XML 本地内容。 */
async function saveXML() { if(state.busy||state.xmlBusy||!state.xmlPath)return;try{if(fsDirty())throw new Error('仍有未保存的参数表单修改，请先保存或撤销参数草稿，再保存 XML 结构。');validateXMLPath(state.xmlPath);const xml=state.xmlDrafts[state.xmlPath]||'';if(!xml.trim())throw new Error('XML 内容不能为空。');if(/<!DOCTYPE|<!ENTITY/i.test(xml))throw new Error('不接受 DTD 或实体声明。');state.busy=true;updateSavebar();const previous=state.fsRevision,result=await request('/v1/fs-config/file',{method:'PUT',body:{revision:previous,path:state.xmlPath,xml}});advanceRevision(previous,result.revision);state.fsRevision=result.revision;state.xmlDrafts[result.path]=result.xml;state.xmlBases[result.path]=result.xml;const catalog=await request('/v1/fs-config');state.fs=catalog;state.fsBase=clone(catalog.overrides||{});state.fsDraft=clone(catalog.overrides||{});if(catalog.revision!==result.revision){const conflict=new Error('XML 已保存，但随后有其他修改。请重新载入最新配置再继续编辑。');conflict.status=409;showError(conflict);}toast('XML 草稿已保存，可导出使用；当前引擎不会执行。');renderPage();}catch(error){showError(error);}finally{state.busy=false;updateSavebar();} }
/* 客户端拒绝明显的配置目录穿越，最终以服务端路径策略为准。 */
function validateXMLPath(path) { if(!/^[A-Za-z0-9_./-]+\.xml$/.test(path)||path.startsWith('/')||path.split('/').some(part=>part==='..'||part==='.'||part===''))throw new Error('请使用配置树内的相对 XML 路径，不包含 ..、空格或绝对路径。'); }
/* 浏览器 DOM 只用于本地生成和编辑文本；多根官方片段通过临时容器解析。 */
function parseXMLFragment(raw) { if(/<!DOCTYPE|<!ENTITY/i.test(raw))throw new Error('XML 不接受 DTD 或实体声明。');const cleaned=raw.replace(/<\?xml\s[^?]*\?>/g,'');const doc=new DOMParser().parseFromString(`<rustswitch-editor-root>${cleaned}</rustswitch-editor-root>`,'application/xml');if(doc.querySelector('parsererror'))throw new Error('XML 语法无效，请先修正现有内容。');return doc; }
/* 按现有 XML 容器生成可读父节点列表，新增参数无需手写选择器。 */
function prepareParents() { const doc=parseXMLFragment(state.xmlDrafts[state.xmlPath]||'');state.parents=[...doc.querySelectorAll('settings,params,variables,configuration,profile')].map((node,index)=>{let current=node,path=[];while(current&&current.nodeName!=='rustswitch-editor-root'){path.unshift(current.nodeName+(current.getAttribute('name')?`[${current.getAttribute('name')}]`:''));current=current.parentElement;}return {index,label:path.join(' / ')};});if(!state.parents.length)throw new Error('当前文件没有 settings、params 或 variables 容器，请使用业务模板或直接编辑 XML。'); }
/* 新增配置项表单只提供保存文本所需字段，不冒充已实现业务功能。 */
function addParameterForm() { return `<div class="custom-form" style="margin-top:18px;border:1px solid var(--line);border-radius:8px"><h3 style="font-size:12px;margin-bottom:17px">新增 XML 参数</h3><form id="add-parameter-form"><div class="form-grid"><div class="form-field full-width"><label class="field-label" for="parameter-parent">放入容器</label><select id="parameter-parent" name="parent">${state.parents.map(parent=>`<option value="${parent.index}">${escapeHTML(parent.label)}</option>`).join('')}</select></div><div class="form-field"><label class="field-label" for="parameter-name">参数名称</label><input id="parameter-name" name="name" required placeholder="例如 user_context"></div><div class="form-field"><label class="field-label" for="parameter-value">参数取值</label><input id="parameter-value" name="value" placeholder="例如 default"></div></div><div class="dialog-actions"><button type="button" class="button button-secondary" data-action="cancel-parameter">取消</button><button class="button button-primary" type="submit">加入 XML 草稿</button></div></form></div>`; }
/* 使用 DOM setAttribute 自动转义新增项，输出仍保留在本地待保存草稿。 */
function addParameter(form) { try{const values=new FormData(form),doc=parseXMLFragment(state.xmlDrafts[state.xmlPath]||''),parents=[...doc.querySelectorAll('settings,params,variables,configuration,profile')],parent=parents[Number(values.get('parent'))];if(!parent)throw new Error('XML 容器已改变，请重新选择。');const name=String(values.get('name')||'').trim();if(!name)throw new Error('参数名称不能为空。');const node=doc.createElement(parent.nodeName==='variables'?'variable':'param');node.setAttribute('name',name);node.setAttribute('value',String(values.get('value')||''));parent.append(doc.createTextNode('\n    '),node,doc.createTextNode('\n'));const serializer=new XMLSerializer();state.xmlDrafts[state.xmlPath]=[...doc.documentElement.childNodes].map(child=>serializer.serializeToString(child)).join('');state.addParameter=false;renderFS();toast('新参数已加入本地 XML 草稿，请保存后导出。');}catch(error){showError(error);} }
/* 三种常用业务模板明确生成目标文件，所有表单值通过 XML 属性转义。 */
function entityForm() { const type=state.entityType||'user';const shared=`<div class="form-field full-width"><label class="field-label" for="entity-kind">配置类型</label><select id="entity-kind" name="kind"><option value="user" ${type==='user'?'selected':''}>目录用户 / 分机</option><option value="gateway" ${type==='gateway'?'selected':''}>Sofia 上游网关</option><option value="dialplan" ${type==='dialplan'?'selected':''}>拨号计划 / 路由</option></select></div>`;const input=(name,label,placeholder='',kind='text')=>`<div class="form-field"><label class="field-label" for="entity-${name}">${label}</label><input id="entity-${name}" name="${name}" type="${kind}" required placeholder="${placeholder}" autocomplete="off"></div>`;const specific=type==='user'?input('name','分机号','1001')+input('password','认证密码','','password')+input('context','用户路由 context','default')+input('caller_name','主叫显示名称','分机 1001'):type==='gateway'?input('name','网关名称','carrier_primary')+input('proxy','代理地址','sip.example.com')+input('username','认证用户名','')+input('password','认证密码','','password'):input('name','路由名称','custom_route')+input('number','匹配被叫号码','1001')+input('destination','桥接目标','user/1001');return `<section class="panel" style="margin-bottom:20px"><div class="panel-header"><div><h2>新建业务配置</h2><p>生成标准 XML 草稿，需对应 FreeSWITCH 模块和正确的 include 路径。</p></div>${badge('仅生成配置','warning')}</div><div class="panel-body"><form id="entity-form"><div class="form-grid">${shared}${specific}</div><div class="dialog-actions"><button class="button button-secondary" type="button" data-action="cancel-entity">取消</button><button class="button button-primary" type="submit">生成并检查 XML</button></div></form></div></section>`; }
/* XML 属性转义独立于 HTML，确保密码、号码和网关参数不会注入额外节点。 */
function escapeXML(value) { return String(value).replace(/[&<>"']/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;'}[char])); }
/* 从业务表单生成可复核的 XML，而不是立即部署或加载模块。 */
async function createEntity(form) { try{const values=new FormData(form),kind=String(values.get('kind')),name=String(values.get('name')||'').trim();if(!/^[A-Za-z0-9_-]{1,64}$/.test(name))throw new Error('名称或分机号请使用 1 至 64 位字母、数字、下划线或横线。');let path,xml;const val=key=>escapeXML(String(values.get(key)||''));if(kind==='user'){path=`directory/default/${name}.xml`;xml=`<include>\n  <user id="${val('name')}">\n    <params>\n      <param name="password" value="${val('password')}"/>\n    </params>\n    <variables>\n      <variable name="user_context" value="${val('context')}"/>\n      <variable name="effective_caller_id_name" value="${val('caller_name')}"/>\n      <variable name="effective_caller_id_number" value="${val('name')}"/>\n    </variables>\n  </user>\n</include>\n`;}else if(kind==='gateway'){path=`sip_profiles/external/${name}.xml`;xml=`<include>\n  <gateway name="${val('name')}">\n    <param name="proxy" value="${val('proxy')}"/>\n    <param name="username" value="${val('username')}"/>\n    <param name="password" value="${val('password')}"/>\n    <param name="register" value="true"/>\n  </gateway>\n</include>\n`;}else{path=`dialplan/default/90_${name}.xml`;const expression=String(values.get('number')||'').replace(/[.*+?^${}()|[\]\\]/g,'\\$&');xml=`<include>\n  <extension name="${val('name')}">\n    <condition field="destination_number" expression="^${escapeXML(expression)}$">\n      <action application="bridge" data="${val('destination')}"/>\n    </condition>\n  </extension>\n</include>\n`;}
if(state.fs.files.some(file=>file.path===path)||state.xmlDrafts[path]!=null){if(!(await confirmAction('替换已有文件草稿？',`目标路径 ${path} 已存在。生成后会替换该文件的本地 XML 草稿，保存前可检查全部内容。`,'生成替换草稿')))return;if(!Object.hasOwn(state.xmlBases,path)&&state.fs.files.some(file=>file.path===path)){const original=await request(`/v1/fs-config/file?path=${encodeURIComponent(path)}`);if(original.revision!==state.fsRevision){const conflict=new Error('文件版本已改变，请重新载入后生成替换草稿。');conflict.status=409;throw conflict;}state.xmlBases[path]=original.xml;}}
state.xmlPath=path;state.xmlDrafts[path]=xml;if(!Object.hasOwn(state.xmlBases,path))state.xmlBases[path]='';state.fsMode='xml';state.entityType=null;state.addParameter=false;renderFS();toast('业务配置已生成，请检查路径与 XML 后保存。');}catch(error){showError(error);} }
/* 兼容页以服务端能力清单为准，展示配置可编辑与引擎实现的不同状态。 */
/* 完整模块审计与本轮稳定性说明从受控文档中心打开，结论不以静态清单冒充认证。 */
function featureAuditPanel() {
  return panel('完整模块检查与稳定性说明','FreeSWITCH 1.11.3 固定源码树 · 145 条目录记录 · 35 个业务域',
    '<p class="text-muted" style="line-height:1.9">逐项查看模块、现有实现、遗漏能力与待执行测试。当前尚未完成原版运行时差分和 Linux 万路实机认证。</p><div class="dialog-actions"><a class="button button-primary" href="#api-docs?document=api%2Ffeature-audit.md">查看完整功能审计</a><a class="button button-secondary" href="#api-docs?document=api%2Fstability-reference.md">查看稳定性与资源保护</a></div>');
}
function renderCompatibility() { const capabilities=state.fs?.capabilities||[];document.getElementById('page-content').innerHTML=`<div class="notice info">${icon('layers')}<div class="notice-content"><strong>当前为开发原型，尚未通过完整 FreeSWITCH 无感替换认证。</strong><br>配置目录可编辑和导出，不代表该模块已在 RustSwitch 中运行。容量与故障接管也需要独立验收。</div></div>${featureAuditPanel()}${capabilities.length?`<div class="support-grid">${capabilities.map((item,index)=>`<section class="panel support-card"><div class="support-card-icon">${icon(['shield','network','file','layers','calls','activity','cpu','server'][index%8])}${badge(item.status,item.status==='已实现'?'good':item.status==='导出可用'?'neutral':'warning')}</div><h2>${escapeHTML(item.name)}</h2><p>${escapeHTML(item.description)}</p></section>`).join('')}</div>`:`<div class="${state.fsLoading?'loading-state':'empty-state'}"><h2>${state.fsLoading?'正在读取能力清单':'能力清单尚未加载'}</h2><p>请重新连接服务以获得真实支持状态。</p><button class="button button-secondary" data-action="reload-fs" style="margin-top:18px">重新载入</button></div>`}<p class="reference-note">最终认证必须绑定具体构建、配置、模块与测试证据。当前页面不展示未经验证的兼容完成百分比。</p>`; }
/* 本地下载用于保留配置快照，生成对象 URL 后及时释放。 */
function downloadJSON(value,filename) { const blob=new Blob([JSON.stringify(value,null,2)+'\n'],{type:'application/json'}),url=URL.createObjectURL(blob),anchor=document.createElement('a');anchor.href=url;anchor.download=filename;anchor.click();setTimeout(()=>URL.revokeObjectURL(url),1000); }
/* 窄屏导航展开状态同时更新可访问性属性。 */
function setMenu(open) { document.getElementById('sidebar').classList.toggle('open',open);document.getElementById('nav-scrim').hidden=!open;document.getElementById('menu-toggle').setAttribute('aria-expanded',String(open)); }
/* 对各页面按钮集中分派，接口操作统一捕获错误并保留本地草稿。 */
async function action(name) {
  if(state.busy && name!=='refresh')return;
  if(name==='refresh'){await Promise.allSettled([pollStatus(),pollOverviewTests()]);if(!state.config)await reloadConfig();return;}
  if(name==='reload-config'){await reloadConfig();return;}
  if(name==='guard-save'){await saveGuard();return;}
  if(name==='guard-reset'){state.guardDraft=clone(state.guardBase);renderProtection();return;}
  if(name==='guard-preset'){const hard=state.config.active.limits;Object.assign(state.guardDraft,{enabled:true,max_active_calls:Math.min(8000,hard.max_calls),max_establishing_calls:Math.min(1000,hard.max_calls),calls_per_second:Math.min(1000,hard.calls_per_second),burst_calls:Math.min(1000,hard.burst_calls)});renderProtection();toast('建议策略已填入本地表单，并按当前启动硬上限约束。点击立即应用后生效。');return;}
  if(name==='capacity-preset'){Object.assign(state.configDraft.limits,{max_calls:8000,calls_per_second:1000,burst_calls:1000,max_transactions:Math.max(32000,state.configDraft.limits.max_transactions)});Object.assign(state.configDraft.media,{port_start:10000,port_end:64999});renderProtection();updateSavebar();toast('已填写 8,000 路 / 1,000 次每秒及端口范围草稿，请核对后保存并重启。');return;}
  if(name==='drain'||name==='resume'){if(!(await confirmAction(name==='drain'?'开始排空服务？':'恢复新呼叫准入？',name==='drain'?'暂停接纳新呼叫，已有通话继续服务。此操作不会直接停止程序。':'重新接纳新呼叫；如果服务已收到停止信号，服务端将拒绝恢复。',name==='drain'?'开始排空':'恢复准入')))return;try{await request(`/v1/${name}`,{method:'POST',body:{}});await pollStatus();renderPage();toast(name==='drain'?'服务已进入排空状态。':'服务已恢复新呼叫准入。');}catch(error){showError(error);}return;}
  if(name==='download-drafts'){downloadJSON({config_revision:state.configRevision,config:state.configDraft,guard_revision:state.guardRevision,guard:state.guardDraft,fs_revision:state.fsRevision,fs_parameters:state.fsDraft,xml_files:state.xmlDrafts},'rustswitch-local-drafts.json');return;}
  if(name==='download-config'){downloadJSON({active:state.config.active,desired:state.config.desired,revision:state.configRevision},'rustswitch-config-snapshot.json');return;}
  if(name==='reload-codecs'){state.codecs=null;state.codecsError='';renderCodecs();return;}
  if(name==='reload-fs'){if((fsDirty()||Object.keys(state.xmlDrafts).some(path=>xmlDirty(path)))&&!(await confirmAction('重新载入 FreeSWITCH 草稿？','将放弃当前未保存的参数与 XML 修改。','重新载入')))return;try{await loadFS();state.xmlDrafts={};state.xmlBases={};renderPage();toast('已载入最新 FreeSWITCH 配置草稿。');}catch(error){showError(error);}return;}
  if(name==='fs-mode'){if(state.fsMode==='parameters'){if(fsDirty()){toast('请先保存或撤销参数草稿，再打开 XML 编辑器。','warning');return;}state.fsMode='xml';if(!state.xmlPath){await openXML(state.fsFile!=='all'?state.fsFile:state.fs.files[0]?.path);return;}}else{if(Object.keys(state.xmlDrafts).some(path=>xmlDirty(path))){toast('请先保存或撤销 XML 草稿，再切换参数表单。','warning');return;}state.fsMode='parameters';}renderFS();return;}
  if(name==='fs-prev'||name==='fs-next'){state.fsPage+=name==='fs-prev'?-1:1;renderFS();return;}
  if(name==='export-fs'){if(fsDirty()||Object.keys(state.xmlDrafts).some(path=>xmlDirty(path))){toast('请先保存或撤销本地修改，再导出已保存的配置。','warning');return;}const anchor=document.createElement('a');anchor.href='/v1/fs-config/export';anchor.download='freeswitch-1.11.3-config.zip';anchor.click();toast('正在下载已保存的配置树；导出不会部署或执行配置。');return;}
  if(name==='new-entity'){if(fsDirty()){toast('请先保存或撤销参数草稿，再新建业务配置。','warning');return;}state.entityType='user';renderFS();return;}
  if(name==='cancel-entity'){state.entityType=null;renderFS();return;}
  if(name==='add-parameter'){try{if(!state.xmlPath)throw new Error('请先选择或创建一个 XML 文件。');prepareParents();state.addParameter=true;renderFS();}catch(error){showError(error);}return;}
  if(name==='cancel-parameter'){state.addParameter=false;renderFS();}
}
/* 草稿输入只更新对应键，避免每个按键重建整个表单而丢失焦点。 */
document.addEventListener('input',event=>{const input=event.target;if(input.dataset.config){setPath(state.configDraft,input.dataset.config,readInput(input));updateConfigFeedback(input);updateSavebar();}else if(input.dataset.guard){state.guardDraft[input.dataset.guard]=readInput(input);updateSavebar();}else if(input.dataset.fs){const parameter=fsParameters().find(item=>item.id===input.dataset.fs);if(parameter&&input.value===parameter.value)delete state.fsDraft[input.dataset.fs];else state.fsDraft[input.dataset.fs]=input.value;input.closest('.parameter-card')?.classList.toggle('edited',Object.hasOwn(state.fsDraft,input.dataset.fs));updateSavebar();}else if(input.id==='fs-search'){state.fsSearch=input.value;state.fsPage=1;const caret=input.selectionStart;renderFS();const search=document.getElementById('fs-search');search.focus();search.setSelectionRange(caret,caret);}else if(input.id==='xml-editor'){if(!state.xmlPath){toast('请先填写配置相对路径。','warning');return;}state.xmlDrafts[state.xmlPath]=input.value;updateSavebar();}});
/* 选择器改变只影响筛选或目标文件，XML 内容保存前始终可检查。 */
document.addEventListener('change',event=>{const input=event.target;if(input.id==='fs-file-filter'){state.fsFile=input.value;state.fsPage=1;renderFS();}else if(input.id==='xml-file-select'){openXML(input.value);}else if(input.id==='xml-path'){const previous=state.xmlPath;state.xmlPath=input.value.trim();if(!Object.hasOwn(state.xmlDrafts,state.xmlPath)){state.xmlDrafts[state.xmlPath]=state.xmlDrafts[previous]||'';state.xmlBases[state.xmlPath]='';}renderFS();}else if(input.id==='entity-kind'){state.entityType=input.value;renderFS();}});
/* 点击代理仅识别固定动作名称或目录分类，不执行服务端传入的代码。 */
document.addEventListener('click',event=>{const button=event.target.closest('[data-action]');if(button){event.preventDefault();if(!button.disabled)action(button.dataset.action);return;}const category=event.target.closest('[data-category]');if(category){state.fsCategory=category.dataset.category;state.fsPage=1;renderFS();}});
/* 业务表单阻止跳转；保留原生确认对话框的 method=dialog 提交行为。 */
document.addEventListener('submit',event=>{if(event.target.closest('#confirm-dialog'))return;event.preventDefault();if(state.busy)return;if(event.target.id==='entity-form')createEntity(event.target);else if(event.target.id==='add-parameter-form')addParameter(event.target);});
/* 主要保存动作遵循当前页面语义，避免把 XML 保存误路由到启动配置。 */
document.getElementById('save-button').addEventListener('click',()=>{if(state.page==='freeswitch'){if(state.fsMode==='xml')saveXML();else saveFS();}else saveConfig();});
document.getElementById('validate-button').addEventListener('click',()=>saveConfig(true));
/* 撤销只重置当前页面对应的本地草稿，不删除服务端已保存配置。 */
document.getElementById('discard-button').addEventListener('click',()=>{if(state.page==='freeswitch'){if(state.fsMode==='xml')state.xmlDrafts[state.xmlPath]=state.xmlBases[state.xmlPath]||'';else state.fsDraft=clone(state.fsBase);}else state.configDraft=clone(state.configBase);renderPage();toast('已撤销当前页面对应的本地修改。');});
document.getElementById('refresh-button').addEventListener('click',()=>action('refresh'));
document.getElementById('menu-toggle').addEventListener('click',()=>setMenu(!document.getElementById('sidebar').classList.contains('open')));
document.getElementById('nav-scrim').addEventListener('click',()=>setMenu(false));
window.addEventListener('hashchange',navigate);
/* 离开页面前由浏览器提醒未保存内容；不阻止没有修改的正常导航。 */
window.addEventListener('beforeunload',event=>{if(anyDirty()){event.preventDefault();event.returnValue='';}});
/* 初次加载并行读取状态与配置；各失败独立呈现，不用模拟数据填补。 */
async function initialize() { fillIcons();const results=await Promise.allSettled([loadConfig(),pollStatus()]);state.loading=false;results.forEach(result=>{if(result.status==='rejected')showError(result.reason);});await navigate();setInterval(()=>{if(!document.hidden){void pollStatus();if(state.page==='overview')void pollOverviewTests();if(state.page==='codecs')void pollCodecs();}},3000); }
initialize();

/* 能力探测由后端缓存；进入页面只读取结果，不向通话热路径加载原生库。 */
function renderCodecs() {
  const root=document.getElementById('page-content'), catalog=state.codecs;
  if(!catalog){root.innerHTML=`<div class="empty-state"><h2>${state.codecsError?'音频能力暂不可读取':'正在读取音频能力'}</h2><p>${escapeHTML(state.codecsError || '核对媒体程序与本机原生后端。')}</p>${state.codecsError?'<button class="button button-secondary" data-action="reload-codecs">重试读取</button>':''}</div>`;
    if(!state.codecsLoading&&!state.codecsError){state.codecsLoading=true;request('/v1/codecs').then(value=>{state.codecs=value;}).catch(error=>{state.codecsError=error.message;}).finally(()=>{state.codecsLoading=false;if(state.page==='codecs')renderCodecs();});}return;}
  const native=catalog.native_audio;
  const runtime=catalog.runtime_media, age=Date.now()-Date.parse(runtime?.observed_at), fresh=!state.codecsError&&Number.isFinite(age)&&age>=-5000&&age<=10000;
  const profiles=catalog.codec_profiles || [], live=new Set((catalog.live_codec_profiles || []).map(item=>item.payload));
  const rows=profiles.map(item=>`<tr><td>${escapeHTML(item.name)}</td><td>${item.payload}</td><td>${number(item.sample_rate)} Hz</td><td>${number(item.rtp_clock_rate)} Hz</td><td>${item.channels}</td><td>${item.ptime_ms} ms</td><td>${catalog.runtime_media ? live.has(item.payload)?runtime.processing==='g711'?'双腿 PCM 处理':'同格式双向透传':'当前模式不支持' : '旧版本未提供当前模式'}</td></tr>`).join('');
  const backends=(native?.codecs || []).map(item=>`<tr><td>${escapeHTML(item.name)}</td><td>${badge(item.available?'可用':['PCMU','PCMA','G722','OPUS'].includes(item.name)?'后端未就绪':'尚未接入',item.available?'good':'warning')}</td><td>${item.encode?'支持':'—'}</td><td>${item.decode?'支持':'—'}</td><td>${item.plc_mode==='codec_native'?'原生 PLC':item.plc_mode==='bounded_history_concealment'?'≤120 ms 历史衰减近似':item.plc?'见后端说明':'—'}</td><td>${escapeHTML(!['PCMU','PCMA','G722','OPUS'].includes(item.name)?'仅支持 RTP 透传，未接入 PCM 编解码':item.reason || '')}</td></tr>`).join('');
  root.innerHTML=`<div class="notice info"><div class="notice-content"><strong>有效媒体模式：${runtime?.processing==='g711'?'G.711 音频处理':runtime?.processing==='relay'?'同编码 RTP 透传':'未提供'}</strong><p>${escapeHTML(catalog.scope)}</p></div></div>${panel('实时处理图状态','每次刷新核对当前运行进程；待重启配置不会改变此处',`<div>${badge(!fresh?'状态未更新':runtime?.ready?'处理图已就绪':runtime?.enabled?'处理图未就绪':'处理图未启用',fresh&&runtime?.ready?'good':'warning')}</div><p>健康 / 声明处理能力 / 预期分片：${number(runtime?.healthy_workers)} / ${number(runtime?.capable_workers)} / ${number(runtime?.expected_workers)}</p><p>${escapeHTML(runtime?.scope || '当前版本未提供握手证据')}</p>${state.codecsError?`<p class="text-muted">读取失败：${escapeHTML(state.codecsError)}，以上保留历史数据。</p>`:''}`)}${localProcessingMarkup(runtime,fresh)}${panel('通话与测试支持的格式','PT 为发生器预设；真实 SIP 动态 PT 以 SDP 协商为准',`<div class="table-scroll"><table><thead><tr><th>格式</th><th>测试 PT</th><th>音频采样率</th><th>RTP 时钟</th><th>声道</th><th>测试包时长</th><th>实时能力</th></tr></thead><tbody>${rows}</tbody></table></div><p class="field-help">G.722 的音频采样率与 RTP 时钟不同。Opus SDP 为 48 kHz / 2 声道，允许单声道包；G.726 与 AAL2 的位打包方式独立协商。</p>`,`<a class="button button-secondary button-small" href="#tests">进入电话测试</a>`)}${panel('原生 PCM 音频 SDK','当前构建实际探测结果；库路径变更后需重启控制服务',catalog.native_probe_error?`<div class="notice warning">${escapeHTML(catalog.native_probe_error)}</div>`:`<div class="table-scroll"><table><thead><tr><th>格式</th><th>后端</th><th>编码</th><th>解码</th><th>丢包补偿</th><th>说明</th></tr></thead><tbody>${backends}</tbody></table></div><p class="field-help">兼容入口仍输出 16 kHz PCM；新增按需采样率 AudioFrame SDK 见接口文档。实时 G.711 图采用独立的有界编解码路径；此处 SDK 自检不证明实时图就绪。ASR、TTS 与完整语音智能体链路尚待接入。</p>`)}${panel('后续兼容范围','保持明确的实现状态',`<div class="table-scroll"><table><thead><tr><th>格式</th><th>优先级</th><th>状态</th></tr></thead><tbody>${(catalog.roadmap || []).map(item=>`<tr><td>${escapeHTML(item.name)}</td><td>${escapeHTML(item.priority)}</td><td>${escapeHTML(item.status)}</td></tr>`).join('')}</tbody></table></div><p><a href="#api-docs?document=api/audio-reference.md">阅读音频接口与验证边界</a></p>`)} `;
}

/* 本地接听独立检查能力、启用与新鲜度；双腿桥就绪不能自动证明本地 IVR 可用。 */
function localProcessingMarkup(runtime,fresh) {
  const known=runtime?.local_capability==='processed_g711_local_v1'&&typeof runtime.local_ready==='boolean'&&typeof runtime.local_available==='boolean';
  const complete=known&&Number.isSafeInteger(runtime.expected_workers)&&runtime.expected_workers>0&&runtime.local_capable_workers===runtime.expected_workers;
  const ready=fresh&&complete&&runtime.processing==='g711'&&runtime.enabled===true&&runtime.available===true&&runtime.ready===true&&runtime.local_available===true&&runtime.local_ready===true;
  const label=!fresh?'本地状态未更新':!known?'当前版本未提供本地能力':ready?'本地接听处理已就绪':runtime?.enabled?'本地接听处理未就绪':'本地接听处理未启用';
  return panel('本地接听与 IVR','与双腿桥接分别核对；只支持 G.711、8 kHz 单声道、20 ms',`<div>${badge(label,ready?'good':'warning')}</div><p>具备本地处理能力 / 预期分片：${number(runtime?.local_capable_workers)} / ${number(runtime?.expected_workers)}</p><p>本地入站音频经过缓冲和 PCM 消费，不回送为回声；提示音、WAV 与主动按键共用发送身份。非零 PCM 和能量统计不表示语音识别或 VAD，流式 ASR/TTS 仍需接入。</p><p><a href="#api-docs?document=api/processed-local-reference.md">本地音频接口与验证范围</a></p>`);
}

/* 聚合只接受全部预期分片的新鲜完整样本；帧最大能量取最大值，不错误相加。 */
function localConsumptionPanel(data,media,online,now=Date.now()) {
  if(media?.processing!=='g711')return '';
  const workers=data?.workers, expected=media.workers, fields=['processed_local_active_calls','processed_local_consumed_frames','processed_local_consumed_samples','processed_local_nonzero_frames','processed_local_energy_max'];
  let complete=online&&Number.isSafeInteger(expected)&&expected>0&&Array.isArray(workers)&&workers.length===expected;
  const totals=Object.fromEntries(fields.map(name=>[name,0])), identities=new Set();
  for(const worker of Array.isArray(workers)?workers:[]) {
    if(!worker||typeof worker!=='object'){complete=false;continue;}
    const stats=worker.stats,age=now-Date.parse(worker.admission?.sampled_at);
    if(!Number.isSafeInteger(worker.id)||worker.id<0||worker.id>=expected||identities.has(worker.id)||worker.healthy!==true||!Number.isSafeInteger(worker.generation)||!(worker.generation>0)||!Number.isSafeInteger(worker.pid)||!(worker.pid>0)||!Number.isFinite(age)||age< -1000||age>3000||fields.some(name=>!Number.isSafeInteger(stats?.[name])||stats[name]<0)){complete=false;continue;}
    identities.add(worker.id);
    if(['processed_active_calls','processed_decoded_frames','processed_plc_frames'].some(name=>!Number.isSafeInteger(stats[name])||stats[name]<0)||stats.processed_local_active_calls>stats.processed_active_calls||stats.processed_local_consumed_frames>stats.processed_decoded_frames+stats.processed_plc_frames||stats.processed_local_consumed_samples!==stats.processed_local_consumed_frames*160||stats.processed_local_nonzero_frames>stats.processed_local_consumed_frames||stats.processed_local_energy_max>171798691840||(stats.processed_local_nonzero_frames===0)!==(stats.processed_local_energy_max===0)){complete=false;continue;}
    for(const name of fields)totals[name]=name==='processed_local_energy_max'?Math.max(totals[name],stats[name]):totals[name]+stats[name];
  }
  complete=complete&&fields.every(name=>Number.isSafeInteger(totals[name]));
  const value=name=>number(complete?totals[name]:undefined);
  return panel('本地 PCM 消费（主服务）','仅主服务本地单腿；不包含上方的隔离压测实例',`<div class="definition-list"><div><dt>本地活跃通话</dt><dd>${value('processed_local_active_calls')}</dd></div><div><dt>实际消费帧 / 样本</dt><dd>${value('processed_local_consumed_frames')} / ${value('processed_local_consumed_samples')}</dd></div><div><dt>包含非零 PCM 的帧</dt><dd>${value('processed_local_nonzero_frames')}</dd></div><div><dt>单帧平方和最大值</dt><dd>${value('processed_local_energy_max')}</dd></div></div><p class="field-help">${complete?'累计值来自当前运行进程，重启后重新计数。':'分片统计缺失、过期或不完整，保持未知。'}非零与能量不是语音识别或 VAD；历史补偿不得当成用户真实讲话，ASR/TTS 尚未接入。</p>`);
}

/* 原生 SDK 自检由后端缓存；当前 worker 就绪状态每三秒重新读取，失败时撤销页面绿色结论。 */
async function pollCodecs() {
  if(state.codecsLoading)return;
  state.codecsLoading=true;
  try { state.codecs=await request('/v1/codecs');state.codecsError=''; }
  catch(error) { state.codecsError=error.message; }
  finally { state.codecsLoading=false;if(state.page==='codecs')renderCodecs(); }
}
