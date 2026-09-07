// 佛跳墙 托盘客户端页面:纯 JS,后端方法在 window.go.main.App,状态由服务端每 1.5 秒推一次 "state" 事件。
const App = () => window.go.main.App;
const $ = s => document.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
let state = null, page = 'home', pageTimer = null;

// ---- 格式化 ----
function fmtBytes(n, d = 1) {
  n = Number(n) || 0;
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(i === 0 ? 0 : d) + ' ' + u[i];
}
const fmtSpeed = n => fmtBytes(n) + '/s';
function fmtDuration(sec) {
  sec = Math.max(0, Math.floor(sec));
  const h = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
  return h ? `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}` : `${m}:${String(s).padStart(2, '0')}`;
}
const fmtDay = ts => ts ? new Date(ts * 1000).toLocaleDateString() : '';
const fmtTime = ts => ts ? new Date(ts * 1000).toLocaleString() : '';
function errText(e) {
  const m = String((e && e.message) || e || '');
  if (m === 'SERVICE_DOWN') return t('svc.down');
  const code = m.match(/^(E_[A-Z_]+):\s*(.*)$/);
  if (code) return t('code.' + code[1]) + (code[2] ? ' · ' + code[2] : '');
  return m;
}
let toastTimer = null;
function toast(msg, kind = '') {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast ' + kind;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 2600);
}

// ---- 导航 ----
const ICONS = {
  home: '<path d="M3 11 12 3l9 8"/><path d="M5 10v10h14V10"/>',
  nodes: '<circle cx="12" cy="5" r="2"/><circle cx="5" cy="19" r="2"/><circle cx="19" cy="19" r="2"/><path d="M12 7v5l-7 5M12 12l7 5"/>',
  sub: '<path d="M4 4h16v16H4z"/><path d="M8 9h8M8 13h8M8 17h5"/>',
  conns: '<path d="M4 6h16M4 12h16M4 18h10"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  logs: '<path d="M6 3h9l5 5v13H6z"/><path d="M14 3v6h6M9 13h6M9 17h6"/>',
  about: '<circle cx="12" cy="12" r="9"/><path d="M12 8h.01M11 12h1v4h1"/>',
};
const NAV = ['home', 'nodes', 'sub', 'conns', 'settings', 'logs', 'about'];
const icon = k => `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${ICONS[k]}</svg>`;
function renderNav() {
  $('#nav').innerHTML = NAV.map(n => `<a data-page="${n}" class="${n === page ? 'active' : ''}">${icon(n)}${t('nav.' + n)}</a>`).join('');
  $('#nav').querySelectorAll('a').forEach(a => a.addEventListener('click', () => route(a.dataset.page)));
}
function route(name) {
  page = name;
  clearInterval(pageTimer); pageTimer = null;
  renderNav();
  const el = $('#page');
  el.innerHTML = '';
  ({ home: renderHome, nodes: renderNodes, sub: renderSub, conns: renderConns, settings: renderSettings, logs: renderLogs, about: renderAbout })[name](el);
}

// ---- 侧栏与横幅 ----
function updateSide() {
  const dot = $('#svc-dot'), txt = $('#svc-text');
  if (!state.service) { dot.className = 'dot err'; txt.textContent = t(state.svcState === 'not-installed' ? 'svc.notInstalled' : 'svc.down'); }
  else { dot.className = 'dot on'; txt.textContent = t('svc.running'); }
  $('#ver').textContent = 'v' + state.version;
  const b = $('#banner');
  if (!state.service) {
    b.hidden = false;
    b.innerHTML = `<span class="grow">${esc(t('banner.svcDown'))}</span><button class="btn sm" id="b-repair">${t('banner.repair')}</button>`;
    $('#b-repair').addEventListener('click', repairService);
  } else if (!state.view.profile) {
    b.hidden = false;
    b.innerHTML = `<span class="grow">${esc(t('banner.noProfile'))}</span><button class="btn sm" id="b-sub">${t('banner.goSub')}</button>`;
    $('#b-sub').addEventListener('click', () => route('sub'));
  } else b.hidden = true;
}
async function repairService() {
  try { await App().RepairService(); toast(t('about.repairDone'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
}

// ---- 首页 ----
function statusText(v) {
  const st = v.state.status;
  return t('st.' + st) || st;
}
function renderHome(el) {
  el.innerHTML = `
    <h1>${t('home.title')}</h1><p class="sub">&nbsp;</p>
    <div class="card"><div class="big">
      <div id="power" class="power"></div>
      <div class="grow">
        <div id="status" class="status-line"></div>
        <div id="status-err" class="status-err"></div>
        <div class="speed">
          <div><b id="sp-down">0 B/s</b><span>↓ ${t('home.down')}</span></div>
          <div><b id="sp-up">0 B/s</b><span>↑ ${t('home.up')}</span></div>
          <div><b id="uptime">–</b><span>${t('home.uptime')}</span></div>
        </div>
      </div>
    </div></div>
    <div class="grid2">
      <div class="card"><h2>${t('home.mode')}</h2>
        <div class="seg" id="mode">${['rule', 'global', 'direct'].map(m => `<button data-mode="${m}">${t('mode.' + m)}</button>`).join('')}</div>
        <div style="margin-top:14px" class="row"><span class="muted">${t('home.node')}</span><b id="node" class="sel"></b><span class="grow"></span><button class="btn sm" id="test">${t('home.test')}</button><span id="test-r" class="muted small"></span></div>
      </div>
      <div class="card"><h2>${t('home.sub')}</h2><div id="sub-info"></div>
        <div class="row" style="margin-top:12px"><button class="btn sm" id="refresh">${t('home.refresh')}</button></div></div>
    </div>`;
  $('#power').addEventListener('click', togglePower);
  $('#mode').querySelectorAll('button').forEach(b => b.addEventListener('click', async () => {
    try { await App().SetMode(b.dataset.mode); } catch (e) { toast(errText(e), 'err'); }
  }));
  $('#test').addEventListener('click', async () => {
    $('#test-r').textContent = '…';
    try { const ms = await App().TestLatency('proxy'); $('#test-r').textContent = ms + ' ' + t('common.ms'); }
    catch (e) { $('#test-r').textContent = t('common.failed'); }
  });
  $('#refresh').addEventListener('click', async () => {
    try { await App().RefreshProfile(); toast(t('sub.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
  });
  updateHome();
}
async function togglePower() {
  if (!state || !state.service) return;
  try {
    if (state.view.state.wanted) await App().Disconnect(); else await App().Connect();
  } catch (e) { toast(errText(e), 'err'); }
}
function updateHome() {
  if (page !== 'home' || !state) return;
  const v = state.view, st = v.state.status, wanted = v.state.wanted;
  const p = $('#power');
  p.textContent = wanted ? t('home.disconnect') : t('home.connect');
  p.className = 'power ' + (st === 'connected' ? 'on' : (st === 'error' || st === 'degraded' || !state.service) ? 'err' : wanted ? 'busy' : '');
  $('#status').textContent = state.service ? statusText(v) : t('svc.down');
  $('#status-err').textContent = v.state.error ? (t('code.' + v.state.code) !== 'code.' + v.state.code ? t('code.' + v.state.code) + ' · ' : '') + v.state.error : '';
  $('#sp-down').textContent = fmtSpeed(state.down);
  $('#sp-up').textContent = fmtSpeed(state.up);
  $('#uptime').textContent = v.uptime ? fmtDuration(v.uptime) : '–';
  $('#mode').querySelectorAll('button').forEach(b => b.classList.toggle('active', b.dataset.mode === v.mode));
  $('#node').textContent = v.node || 'auto';
  const pr = v.profile;
  $('#sub-info').innerHTML = pr ? subInfoHTML(pr) : `<span class="muted">${t('sub.none')}</span>`;
}
function subInfoHTML(pr) {
  const u = pr.usage || {}, used = (u.upload || 0) + (u.download || 0);
  const pct = u.total ? Math.min(100, used / u.total * 100) : 0;
  return `<dl class="kv">
    <dt>${t('sub.name')}</dt><dd>${esc(pr.title || '—')}</dd>
    <dt>${t('sub.nodes')}</dt><dd>${pr.nodeCount}</dd>
    <dt>${t('sub.usage')}</dt><dd>${fmtBytes(used)} / ${u.total ? fmtBytes(u.total) : t('sub.unlimited')}${u.total ? `<div class="progress"><i class="${pct > 90 ? 'warn' : ''}" style="width:${pct}%"></i></div>` : ''}</dd>
    <dt>${t('sub.expire')}</dt><dd>${u.expire ? fmtDay(u.expire) : t('sub.unlimited')}</dd>
    <dt>${t('sub.updated')}</dt><dd>${fmtTime(pr.fetchedAt)}</dd>
  </dl>`;
}

// ---- 节点 ----
function delayClass(d) { return !d ? '' : d < 150 ? 'good' : d < 400 ? 'mid' : 'bad'; }
async function renderNodes(el) {
  el.innerHTML = `<h1>${t('nodes.title')}</h1><p class="sub">&nbsp;</p>
    <div class="card"><div class="row" style="margin-bottom:12px"><span class="grow"></span><button class="btn sm" id="testall">${t('nodes.testAll')}</button></div><div id="nodes" class="list"></div></div>`;
  $('#testall').addEventListener('click', async () => {
    const b = $('#testall'); b.disabled = true; b.textContent = t('nodes.testing');
    try { await App().TestAll(); } catch (e) { toast(errText(e), 'err'); }
    b.disabled = false; b.textContent = t('nodes.testAll');
    loadNodes();
  });
  loadNodes();
}
async function loadNodes() {
  const box = $('#nodes');
  if (!box) return;
  let nodes = [];
  try { nodes = await App().GetNodes() || []; } catch (e) { box.innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
  if (!nodes.length) { box.innerHTML = `<div class="empty">${t('nodes.empty')}</div>`; return; }
  box.innerHTML = nodes.map(n => `<div class="item ${n.current ? 'current' : ''}" data-name="${esc(n.name)}">
    <span class="radio"></span><span class="grow">${esc(n.name === 'auto' ? t('nodes.auto') : n.name)}</span>
    ${n.type ? `<span class="tag">${esc(n.type)}</span>` : ''}<span class="delay ${delayClass(n.delay)}">${n.delay ? n.delay + ' ms' : '–'}</span></div>`).join('');
  box.querySelectorAll('.item').forEach(it => it.addEventListener('click', async () => {
    try { await App().SelectNode(it.dataset.name); toast(t('nodes.selected', { n: it.dataset.name }), 'ok'); loadNodes(); } catch (e) { toast(errText(e), 'err'); }
  }));
}

// ---- 订阅 ----
function renderSub(el) {
  const pr = state && state.view.profile;
  const url = (state && state.view.settings && state.view.settings.profileUrl) || '';
  el.innerHTML = `<h1>${t('sub.title')}</h1><p class="sub">&nbsp;</p>
    <div class="card"><div class="field"><label>${t('sub.url')}</label><input type="url" id="sub-url" value="${esc(url)}" placeholder="https://…/sub/…"><span class="help">${t('sub.urlHelp')}</span></div>
      <div class="row" style="margin-top:12px"><button class="btn primary" id="sub-save">${t('sub.save')}</button><button class="btn" id="sub-refresh" ${pr ? '' : 'disabled'}>${t('sub.refresh')}</button></div></div>
    <div class="card" id="sub-card">${pr ? subInfoHTML(pr) : `<span class="muted">${t('sub.none')}</span>`}</div>`;
  $('#sub-save').addEventListener('click', async () => {
    const b = $('#sub-save'); b.disabled = true;
    try { const p = await App().SetProfileURL($('#sub-url').value); $('#sub-card').innerHTML = subInfoHTML(p); toast(t('sub.saved'), 'ok'); }
    catch (e) { toast(errText(e), 'err'); }
    b.disabled = false;
  });
  $('#sub-refresh').addEventListener('click', async () => {
    try { const p = await App().RefreshProfile(); $('#sub-card').innerHTML = subInfoHTML(p); toast(t('sub.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
  });
}

// ---- 连接 ----
function renderConns(el) {
  el.innerHTML = `<h1>${t('conns.title')}</h1><p class="sub">&nbsp;</p><div class="card"><div id="conns"></div></div>`;
  const load = async () => {
    const box = $('#conns');
    if (!box) return;
    let list;
    try { list = await App().GetConnections() || []; } catch (e) { box.innerHTML = `<div class="empty">${esc(state && state.view.state.status === 'connected' ? errText(e) : t('conns.needCore'))}</div>`; return; }
    if (!list.length) { box.innerHTML = `<div class="empty">${t('conns.empty')}</div>`; return; }
    box.innerHTML = `<table><thead><tr><th>${t('conns.host')}</th><th></th><th>${t('conns.chain')}</th><th>${t('conns.rule')}</th><th class="num">${t('conns.up')}</th><th class="num">${t('conns.down')}</th><th></th></tr></thead><tbody>
      ${list.map(c => `<tr><td class="sel" title="${esc(c.host)}">${esc(c.host)}</td><td><span class="tag">${esc(c.net)}</span></td><td title="${esc(c.chain)}">${esc(c.chain)}</td><td class="muted small">${esc(c.rule)}</td><td class="num">${fmtBytes(c.up)}</td><td class="num">${fmtBytes(c.down)}</td><td><button class="btn sm" data-id="${esc(c.id)}">${t('conns.close')}</button></td></tr>`).join('')}</tbody></table>`;
    box.querySelectorAll('button[data-id]').forEach(b => b.addEventListener('click', async () => { try { await App().CloseConnection(b.dataset.id); load(); } catch (e) { toast(errText(e), 'err'); } }));
  };
  load();
  pageTimer = setInterval(load, 2000);
}

// ---- 设置 ----
async function renderSettings(el) {
  let s;
  try { s = await App().GetSettings(); } catch (e) { el.innerHTML = `<h1>${t('set.title')}</h1><div class="card"><div class="empty">${esc(errText(e))}</div></div>`; return; }
  const auto = await App().GetAutostart();
  const chk = (id, label, on, help) => `<div class="field"><label class="switch"><input type="checkbox" id="${id}" ${on ? 'checked' : ''}> ${label}</label>${help ? `<span class="help">${help}</span>` : ''}</div>`;
  const inp = (id, label, val, type = 'text', help = '') => `<div class="field"><label>${label}</label><input type="${type}" id="${id}" value="${esc(val)}">${help ? `<span class="help">${help}</span>` : ''}</div>`;
  const sel = (id, label, val, opts, help = '') => `<div class="field"><label>${label}</label><select id="${id}">${opts.map(o => `<option value="${o[0]}" ${o[0] === val ? 'selected' : ''}>${o[1]}</option>`).join('')}</select>${help ? `<span class="help">${help}</span>` : ''}</div>`;
  el.innerHTML = `<h1>${t('set.title')}</h1><p class="sub">${t('set.restartNote')}</p>
    <div class="card"><div class="form">
      ${chk('f-tun', t('set.tun'), s.tun, t('set.tunHelp'))}
      ${sel('f-stack', t('set.tunStack'), s.tunStack, [['mixed', 'mixed'], ['system', 'system'], ['gvisor', 'gvisor']])}
      ${chk('f-strict', t('set.strict'), s.strictRoute, t('set.strictHelp'))}
      ${chk('f-lan', t('set.lan'), s.lanBypass, t('set.lanHelp'))}
      ${inp('f-mixed', t('set.mixed'), s.mixedPort, 'number', t('set.mixedHelp'))}
      ${inp('f-update', t('set.update'), s.updateHours, 'number')}
      ${inp('f-rdns', t('set.remoteDns'), s.remoteDns)}
      ${inp('f-ldns', t('set.localDns'), s.localDns)}
      ${chk('f-fakeip', t('set.fakeip'), s.fakeIp)}
      ${chk('f-ipv6', t('set.ipv6'), s.ipv6, t('set.ipv6Help'))}
      ${chk('f-ad', t('set.adblock'), s.adBlock)}
      ${sel('f-log', t('set.logLevel'), s.logLevel, [['debug', 'debug'], ['info', 'info'], ['warn', 'warn'], ['error', 'error']])}
    </div><div class="row" style="margin-top:16px"><button class="btn primary" id="save">${t('set.save')}</button></div></div>
    <div class="card"><div class="form">
      ${chk('f-autostart', t('set.autostart'), auto, t('set.autostartHelp'))}
      ${sel('f-lang', t('set.lang'), LANG, [['zh', '中文'], ['en', 'English']])}
    </div></div>`;
  $('#save').addEventListener('click', async () => {
    const n = { ...s, tun: $('#f-tun').checked, tunStack: $('#f-stack').value, strictRoute: $('#f-strict').checked, lanBypass: $('#f-lan').checked,
      mixedPort: Number($('#f-mixed').value), updateHours: Number($('#f-update').value), remoteDns: $('#f-rdns').value.trim(), localDns: $('#f-ldns').value.trim(),
      fakeIp: $('#f-fakeip').checked, ipv6: $('#f-ipv6').checked, adBlock: $('#f-ad').checked, logLevel: $('#f-log').value };
    try { s = await App().SaveSettings(n); toast(t('set.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
  });
  $('#f-autostart').addEventListener('change', async e => { try { await App().SetAutostart(e.target.checked); toast(t('set.saved'), 'ok'); } catch (err) { toast(errText(err), 'err'); e.target.checked = !e.target.checked; } });
  $('#f-lang').addEventListener('change', async e => { LANG = e.target.value; await App().SetLang(LANG); renderNav(); route('settings'); });
}

// ---- 日志 ----
function renderLogs(el) {
  el.innerHTML = `<h1>${t('logs.title')}</h1><p class="sub">&nbsp;</p>
    <div class="card"><div class="row wrap" style="margin-bottom:10px">
      <div class="seg" id="logsrc"><button data-core="0" class="active">${t('logs.service')}</button><button data-core="1">${t('logs.core')}</button></div>
      <span class="muted small">${t('logs.lines')}</span><select id="lines" style="width:90px"><option>100</option><option>300</option><option>1000</option></select>
      <label class="switch"><input type="checkbox" id="auto" checked> ${t('logs.auto')}</label>
      <span class="grow"></span>
      <button class="btn sm" id="reload">${t('logs.refresh')}</button><button class="btn sm" id="open">${t('logs.open')}</button><button class="btn sm" id="diag">${t('logs.diag')}</button>
    </div><pre class="log sel" id="log"></pre></div>`;
  let core = false;
  const load = async () => {
    const pre = $('#log'); if (!pre) return;
    try { const lines = await App().GetLogs(Number($('#lines').value), core) || []; const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 8; pre.textContent = lines.join('\n'); if (atBottom) pre.scrollTop = pre.scrollHeight; }
    catch (e) { pre.textContent = errText(e); }
  };
  $('#logsrc').querySelectorAll('button').forEach(b => b.addEventListener('click', () => { core = b.dataset.core === '1'; $('#logsrc').querySelectorAll('button').forEach(x => x.classList.toggle('active', x === b)); load(); }));
  $('#reload').addEventListener('click', load);
  $('#lines').addEventListener('change', load);
  $('#open').addEventListener('click', () => App().OpenLogs().catch(e => toast(errText(e), 'err')));
  $('#diag').addEventListener('click', async () => {
    try { const txt = await App().Diagnose(); await copyText(txt); toast(t('logs.copied'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
  });
  load();
  pageTimer = setInterval(() => { if ($('#auto') && $('#auto').checked) load(); }, 3000);
}
async function copyText(txt) {
  try { await navigator.clipboard.writeText(txt); return; } catch (e) { /* 回退 */ }
  const ta = document.createElement('textarea'); ta.value = txt; document.body.appendChild(ta); ta.select(); document.execCommand('copy'); ta.remove();
}

// ---- 关于 ----
function renderAbout(el) {
  const v = state ? state.view : {};
  el.innerHTML = `<h1>${t('about.title')}</h1><p class="sub">&nbsp;</p>
    <div class="card"><dl class="kv">
      <dt>${t('about.ui')}</dt><dd>${esc(state ? state.version : '')}</dd>
      <dt>${t('about.svc')}</dt><dd>${esc(v.version || '—')}</dd>
      <dt>${t('about.svcState')}</dt><dd id="about-svc">${esc(state ? state.svcState : '')}</dd>
      <dt>${t('about.core')}</dt><dd>sing-box</dd>
      <dt></dt><dd class="muted small">${t('about.license')} · <a id="repo">github.com/Maoyangui/godusevpn</a></dd>
    </dl>
    <div class="row wrap" style="margin-top:14px"><button class="btn" id="repair">${t('about.repair')}</button><span class="muted small">${t('about.repairHelp')}</span></div>
    <div class="row wrap" style="margin-top:10px"><button class="btn danger" id="quit">${t('about.quit')}</button><span class="muted small">${t('about.quitHelp')}</span></div></div>`;
  $('#repair').addEventListener('click', repairService);
  $('#quit').addEventListener('click', () => App().QuitApp());
  $('#repo').addEventListener('click', () => App().OpenURL('https://github.com/Maoyangui/godusevpn'));
}

// ---- 启动 ----
async function init() {
  state = await App().GetState();
  LANG = state.lang || 'zh';
  document.documentElement.lang = LANG === 'en' ? 'en' : 'zh';
  updateSide();
  route('home');
  window.runtime.EventsOn('state', st => {
    const langChanged = st.lang !== LANG;
    state = st; LANG = st.lang || 'zh';
    updateSide();
    if (langChanged) { renderNav(); route(page); return; }
    updateHome();
    if (page === 'about' && $('#about-svc')) $('#about-svc').textContent = st.svcState;
  });
  window.runtime.EventsOn('traffic', tr => {
    if (!state) return;
    state.up = tr.up; state.down = tr.down;
    if (page === 'home' && $('#sp-down')) { $('#sp-down').textContent = fmtSpeed(tr.down); $('#sp-up').textContent = fmtSpeed(tr.up); }
  });
}
document.addEventListener('DOMContentLoaded', init);
