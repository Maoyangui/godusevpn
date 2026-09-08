// 佛跳墙 竖向客户端页面。后端方法在 window.go.main.App;服务端每 1.5 秒推 "state",内核每秒推 "traffic"。
const App = () => window.go.main.App;
const $ = s => document.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
let state = null;          // 最新 UIState
let view = null;           // 当前页面名:onboard / home / settings / profiles / conns / logs / about
let pageTimer = null;      // 页面自己的定时刷新
let lastPing = 0;          // 最近一次测得的延迟
let tweens = {};           // 数字过渡

// ---- 工具 ----
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
  return (h ? h + ':' : '') + String(m).padStart(2, '0') + ':' + String(s).padStart(2, '0');
}
const fmtDay = ts => ts ? new Date(ts * 1000).toLocaleDateString() : '';
const fmtTime = ts => ts ? new Date(ts * 1000).toLocaleString([], { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '';
function errText(e) {
  const m = String((e && e.message) || e || '');
  if (m === 'SERVICE_DOWN') return t('svc.down');
  const code = m.match(/^(E_[A-Z_]+):\s*(.*)$/);
  if (code) return t('code.' + code[1]) + (code[2] && code[2] !== t('code.' + code[1]) ? ' · ' + code[2] : '');
  return m;
}
let toastTimer = null;
function toast(msg, kind = '') {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast ' + kind + ' show';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.classList.remove('show'); }, 2400);
}
function tween(el, from, to, fmt, ms = 500) {
  const key = el.id || el;
  if (tweens[key]) cancelAnimationFrame(tweens[key]);
  const start = performance.now();
  const step = now => {
    const k = Math.min(1, (now - start) / ms), e = 1 - Math.pow(1 - k, 3);
    el.textContent = fmt(from + (to - from) * e);
    if (k < 1) tweens[key] = requestAnimationFrame(step);
  };
  tweens[key] = requestAnimationFrame(step);
}
function applyTheme(theme) {
  const html = document.documentElement;
  if (theme === 'light' || theme === 'dark') html.setAttribute('data-theme', theme); else html.removeAttribute('data-theme');
}
function svcText(st) {
  if (st.service) return t('svc.running');
  if (st.svcState === 'not-installed') return t('svc.notInstalled');
  if (st.svcState === 'starting') return t('svc.starting');
  return t('svc.down');
}

// ---- 顶栏、抽屉、面板 ----
const ICON = {
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  profiles: '<path d="M4 5h16v14H4z"/><path d="M8 10h8M8 14h5"/>',
  conns: '<path d="M4 7h16M4 12h16M4 17h10"/>',
  logs: '<path d="M6 3h9l5 5v13H6z"/><path d="M14 3v6h6M9 13h6M9 17h6"/>',
  about: '<circle cx="12" cy="12" r="9"/><path d="M12 8h.01M11 12h1v4h1"/>',
  rules: '<path d="M4 6h3l8 12h5M4 18h3l2.5-3.7M13.5 9.7L15 7.5h5"/><path d="M17 4l3 3-3 3M17 14l3 4-3 3"/>',
  devices: '<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>',
};
const MENU = ['settings', 'profiles', 'rules', 'devices', 'conns', 'logs', 'about'];
const menuItems = () => MENU.filter(m => m !== 'devices' || (state && state.platform === 'linux')); // 设备页只有 Linux 软路由有
const PAGES = { onboard: renderOnboard, home: renderHome, settings: renderSettings, profiles: renderProfiles, rules: renderRules, ruleEdit: renderRuleEdit, devices: renderDevices, conns: renderConns, logs: renderLogs, about: renderAbout };
const TITLES = { settings: 'set.title', profiles: 'prof.title', rules: 'rules.title', ruleEdit: 'rules.edit', devices: 'dev.title', conns: 'conns.title', logs: 'logs.title', about: 'about.title' };
const BACK = { ruleEdit: 'rules' }; // 返回键去哪(默认回首页)
function renderDrawer() {
  $('#drawer-items').innerHTML = menuItems().map(m => `<a data-page="${m}"><svg viewBox="0 0 24 24">${ICON[m]}</svg><span>${t('menu.' + m)}</span>${m === 'about' && state && state.update ? '<span class="grow"></span><span class="pill-badge">NEW</span>' : ''}</a>`).join('');
  $('#drawer-items').querySelectorAll('a').forEach(a => a.addEventListener('click', () => { closeDrawer(); nav(a.dataset.page); }));
  $('#drawer-ver').textContent = 'v' + (state ? state.version : '');
  const u = $('#drawer-upd');
  u.hidden = !(state && state.update);
  if (state && state.update) u.textContent = t('drawer.update', { v: state.update.version });
}
function openDrawer() {
  renderDrawer(); $('#drawer').classList.add('show'); $('#drawer-backdrop').classList.add('show');
  try { App().PokeUpdate(); } catch (e) { /* 旧服务没有这个方法 */ }
}
function closeDrawer() { $('#drawer').classList.remove('show'); $('#drawer-backdrop').classList.remove('show'); }
let sheetOnClose = null;
function openSheet(title, html, opts = {}) {
  $('#sheet-head').innerHTML = `<span class="grow">${esc(title)}</span>${opts.action ? `<button class="btn sm ghost" id="sheet-action">${esc(opts.action)}</button>` : ''}`;
  $('#sheet-body').innerHTML = html;
  $('#sheet').classList.add('show'); $('#sheet-backdrop').classList.add('show');
  sheetOnClose = opts.onClose || null;
  if (opts.action && opts.onAction) $('#sheet-action').addEventListener('click', opts.onAction);
}
function closeSheet() {
  $('#sheet').classList.remove('show'); $('#sheet-backdrop').classList.remove('show');
  if (sheetOnClose) { const f = sheetOnClose; sheetOnClose = null; f(); }
}
function setTop(title, back) {
  $('#topbar-title').textContent = title;
  $('#back-btn').hidden = !back;
  $('#menu-btn').hidden = !!back || view === 'onboard';
}

// ---- 页面切换 ----
function nav(name, arg) {
  const stage = $('#stage');
  const old = stage.querySelector('.view');
  if (old && name === 'home' && view !== 'home') { old.classList.add('pop'); setTimeout(() => old.remove(), 240); } else if (old) old.remove();
  view = name;
  clearInterval(pageTimer); pageTimer = null;
  const el = document.createElement('div');
  el.className = 'view ' + (name === 'home' ? 'home' : '') + (name === 'settings' || name === 'ruleEdit' ? ' has-bar' : ''); // 带保存栏的页:内容不够高时保存栏也贴底
  stage.appendChild(el);
  PAGES[name](el, arg);
  setTop(name === 'home' || name === 'onboard' ? t('app.name') : t(TITLES[name]), name !== 'home' && name !== 'onboard');
}
function navHome() { nav(state && state.view.profiles && state.view.profiles.length ? 'home' : 'onboard'); }

// ---- 引导 ----
function renderOnboard(el) {
  el.innerHTML = `<div class="onboard">
    <svg class="logo" viewBox="-16 -50 400 400"><defs><linearGradient id="g2" gradientUnits="userSpaceOnUse" x1="70" y1="70" x2="310" y2="270"><stop offset="0" stop-color="#6a44f2"/><stop offset=".55" stop-color="#2f8bff"/><stop offset="1" stop-color="#18e3e8"/></linearGradient></defs><g fill="none" stroke="url(#g2)" stroke-width="44" stroke-linecap="round" stroke-linejoin="round"><path d="M86 248V84l146 124v52"/><path d="M332 78l-88 96"/></g><path fill="url(#g2)" d="M118 192l52 30-52 30z"/></svg>
    <h1>${t('onboard.title')}</h1><p>${t('onboard.desc')}</p>
    <div class="field with-btn"><input type="url" id="ob-url" placeholder="${t('onboard.url')}" autofocus><button class="btn sm ghost" id="ob-paste">${t('common.paste')}</button></div>
    <div class="field"><input type="text" id="ob-name" placeholder="${t('onboard.name')}"></div>
    <button class="btn primary block" id="ob-go">${t('onboard.go')}</button>
    <div id="ob-err" class="small" style="color:var(--danger);min-height:18px"></div>
    <div id="ob-svc"></div>
  </div>`;
  updateOnboardSvc();
  const submit = async () => {
    const url = $('#ob-url').value.trim();
    if (!url) { $('#ob-url').focus(); return; }
    const b = $('#ob-go'); b.disabled = true; b.textContent = t('onboard.adding'); $('#ob-err').textContent = '';
    try { await App().AddProfile($('#ob-name').value, url); toast(t('prof.added'), 'ok'); state = await App().GetState(); nav('home'); }
    catch (e) { $('#ob-err').textContent = errText(e); b.disabled = false; b.textContent = t('onboard.go'); }
  };
  $('#ob-go').addEventListener('click', submit);
  $('#ob-paste').addEventListener('click', () => pasteInto('#ob-url'));
  $('#ob-url').addEventListener('keydown', e => { if (e.key === 'Enter') submit(); });
  setTimeout(() => $('#ob-url').focus(), 120);
}
function updateOnboardSvc() {
  const b = $('#ob-svc'); if (!b || !state) return;
  b.innerHTML = state.service ? '' : `<div class="banner"><span class="grow">${t('banner.svcDown')}</span><button class="btn sm" id="ob-repair">${t('banner.repair')}</button></div>`;
  if (!state.service) $('#ob-repair').addEventListener('click', repairService);
}

// ---- 首页 ----
function renderHome(el) {
  el.innerHTML = `
    <div id="home-banner"></div>
    <div class="hero">
    <div class="power-wrap" id="power-wrap">
      <svg viewBox="0 0 208 208"><defs><linearGradient id="ringGrad" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#6a44f2"/><stop offset=".55" stop-color="#2f8bff"/><stop offset="1" stop-color="#18e3e8"/></linearGradient></defs>
        <circle class="ring-bg" cx="104" cy="104" r="99"/><circle class="ring" cx="104" cy="104" r="99"/></svg>
      <button class="power" id="power"><svg viewBox="0 0 24 24"><path d="M12 3v9"/><path d="M6.3 6.3a8 8 0 1 0 11.4 0"/></svg><b id="power-text"></b></button>
    </div>
    <div class="status-text" id="status"></div>
    <div class="status-sub" id="status-sub"></div>
    </div>
    <div class="stats">
      <div class="stat"><b id="s-down">0 B/s</b><span>↓ ${t('stat.down')}</span></div>
      <div class="stat"><b id="s-up">0 B/s</b><span>↑ ${t('stat.up')}</span></div>
      <div class="stat"><b id="s-ping">–</b><span>${t('stat.ping')}</span></div>
      <div class="stat"><b id="s-time">–</b><span>${t('stat.time')}</span></div>
    </div>
    <div class="pickers">
      <button class="picker" id="pk-mode"><span>${t('pick.mode')}</span><b id="pk-mode-v"></b></button>
      <button class="picker" id="pk-prof"><span>${t('pick.profile')}</span><b id="pk-prof-v"></b></button>
      <button class="picker" id="pk-node"><span>${t('pick.node')}</span><b id="pk-node-v"></b><em id="pk-node-ms"></em></button>
    </div>`;
  $('#power').addEventListener('click', togglePower);
  $('#pk-mode').addEventListener('click', sheetMode);
  $('#pk-prof').addEventListener('click', sheetProfiles);
  $('#pk-node').addEventListener('click', sheetNodes);
  updateHome();
}
async function togglePower() {
  if (!state || !state.service) { toast(t('svc.down'), 'err'); return; }
  try { if (state.view.state.wanted) await App().Disconnect(); else await App().Connect(); }
  catch (e) { toast(errText(e), 'err'); }
}
function updateHome() {
  if (view !== 'home' || !state) return;
  const v = state.view, st = v.state.status, wanted = v.state.wanted;
  const wrap = $('#power-wrap'); if (!wrap) return;
  const on = st === 'connected' || st === 'degraded';
  wrap.className = 'power-wrap ' + (on ? 'on' : (wanted && st !== 'error') ? 'busy' : (st === 'error' || !state.service) ? 'err' : '');
  $('#power-text').textContent = on ? t('home.disconnect') : wanted ? (st === 'stopping' ? t('home.stopping') : t('home.connecting')) : t('home.connect');
  $('#status').textContent = state.service ? t('st.' + st) : t('svc.down');
  let sub = '';
  if (v.state.error) sub = (t('code.' + v.state.code) !== 'code.' + v.state.code ? t('code.' + v.state.code) : v.state.error);
  else if (on) sub = (v.node === 'auto' ? (v.autoNow || t('pick.auto')) : v.node) + ' · ' + t('mode.' + v.mode);
  $('#status-sub').textContent = sub;
  $('#s-time').textContent = on && v.uptime ? fmtDuration(v.uptime) : '–';
  $('#s-ping').textContent = on && lastPing ? lastPing + ' ' + t('common.ms') : '–';
  if (!on) { $('#s-down').textContent = '0 B/s'; $('#s-up').textContent = '0 B/s'; }
  $('#pk-mode-v').textContent = t('mode.' + v.mode);
  $('#pk-prof-v').textContent = v.profile ? v.profile.name : t('prof.empty');
  $('#pk-node-v').textContent = v.node === 'auto' || !v.node ? t('pick.auto') : v.node;
  $('#pk-node-ms').textContent = '';
  const b = $('#home-banner');
  b.innerHTML = state.service ? '' : `<div class="banner"><span class="grow">${t('banner.svcDown')}</span><button class="btn sm" id="repair">${t('banner.repair')}</button></div>`;
  if (!state.service) $('#repair').addEventListener('click', repairService);
}
async function pasteInto(sel) {
  let v = '';
  try { v = window.__web && navigator.clipboard && navigator.clipboard.readText ? (await navigator.clipboard.readText() || '').trim() : ((await App().ReadClipboard()) || '').trim(); } catch (e) { /* 没有剪贴板权限 */ }
  if (v) { $(sel).value = v; $(sel).focus(); } else toast(t('common.clipEmpty'));
}
async function repairService() {
  try { await App().RepairService(); toast(t('about.repairDone'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
}

// ---- 首页三个面板 ----
function sheetMode() {
  const cur = state.view.mode;
  openSheet(t('sheet.mode'), `<div class="list">${['rule', 'global', 'direct'].map(m => `<div class="item ${m === cur ? 'current' : ''}" data-mode="${m}"><span class="check"></span><div class="name"><b>${t('mode.' + m)}</b><span>${t('mode.' + m + '.d')}</span></div></div>`).join('')}</div>`);
  $('#sheet-body').querySelectorAll('.item').forEach(it => it.addEventListener('click', async () => {
    try { await App().SetMode(it.dataset.mode); closeSheet(); } catch (e) { toast(errText(e), 'err'); }
  }));
}
function profileLine(p) {
  const u = p.usage || {}, used = (u.upload || 0) + (u.download || 0);
  const parts = [t('prof.nodes', { n: p.nodeCount })];
  if (u.total) parts.push(t('prof.usage', { u: fmtBytes(used), t: fmtBytes(u.total) }));
  if (u.expire) parts.push(t('prof.expire', { d: fmtDay(u.expire) }));
  return parts.join(' · ');
}
const ICON_REFRESH = '<svg viewBox="0 0 24 24"><path d="M20 12a8 8 0 1 1-2.3-5.7"/><path d="M20 4v5h-5"/></svg>';
function sheetProfiles() {
  openSheet(t('sheet.profile'), '', { action: t('sheet.manage'), onAction: () => { closeSheet(); nav('profiles'); } });
  drawProfiles(state.view.profiles || []);
}
// 订阅面板的列表:点整行切换,右侧小图标刷新这一条(不换当前订阅)
function drawProfiles(list) {
  const body = $('#sheet-body');
  body.innerHTML = list.length ? `<div class="list">${list.map(p => `<div class="item ${p.active ? 'current' : ''}" data-id="${esc(p.id)}"><span class="check"></span>
    <div class="name"><b>${esc(p.name)}</b><span>${esc(profileLine(p))}${p.fetchedAt ? ' · ' + esc(t('prof.updated', { t: fmtTime(p.fetchedAt) })) : ''}${p.error ? ' · <span style="color:var(--danger)">' + esc(p.error) + '</span>' : ''}</span></div>
    <button class="icon-btn xs muted" data-refresh="${esc(p.id)}" title="${t('prof.refresh')}">${ICON_REFRESH}</button></div>`).join('')}</div>` : `<div class="empty">${t('prof.empty')}</div>`;
  body.querySelectorAll('.item').forEach(it => it.addEventListener('click', async () => {
    try { await App().SelectProfile(it.dataset.id); closeSheet(); } catch (e) { toast(errText(e), 'err'); }
  }));
  body.querySelectorAll('button[data-refresh]').forEach(b => b.addEventListener('click', async e => {
    e.stopPropagation();
    b.disabled = true; b.querySelector('svg').classList.add('spin');
    try { await App().RefreshProfile(b.dataset.refresh); toast(t('prof.refreshed'), 'ok'); } catch (err) { toast(errText(err), 'err'); }
    if (!$('#sheet').classList.contains('show')) return;
    let l = [];
    try { l = await App().GetProfiles() || []; } catch (err) { l = state.view.profiles || []; }
    drawProfiles(l);
  }));
}
function msClass(d) { return d < 0 ? 'bad' : !d ? 'none' : d < 150 ? 'good' : d < 400 ? 'mid' : 'bad'; }
function msText(d, testing) { if (testing) return '<span class="spinner"></span>'; if (d < 0) return t('node.fail'); return d ? d + ' ms' : '–'; }
let nodeTesting = false, nodeFilter = '';
async function sheetNodes() {
  nodeFilter = '';
  openSheet(t('sheet.nodes'), `<div class="empty"><span class="spinner"></span></div>`, { action: t('sheet.retest'), onAction: () => testNodes(true) });
  await drawNodes(false);
  testNodes(false);
}
async function drawNodes(testing) {
  let nodes = [];
  try { nodes = await App().GetNodes() || []; } catch (e) { $('#sheet-body').innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
  if (!nodes.length) { $('#sheet-body').innerHTML = `<div class="empty">${t('prof.empty')}</div>`; return; }
  const max = Math.max(1, ...nodes.filter(n => n.delay > 0).map(n => n.delay));
  const st = state && state.view.state.status, online = st === 'connected' || st === 'degraded';
  const hint = online ? '' : `<div class="small muted" style="padding:0 4px 8px">${t('node.offlineHint')}</div>`;
  $('#sheet-body').innerHTML = hint + (nodes.length > 8 ? `<div class="sheet-filter"><input type="text" id="node-filter" placeholder="${t('node.filter')}" value="${esc(nodeFilter)}"></div>` : '') + `<div class="list">${nodes.map((n, i) => `<div class="item ${n.current ? 'current' : ''}" data-name="${esc(n.name)}" style="animation-delay:${i * 25}ms"><span class="check"></span>
    <div class="name"><b>${esc(n.name === 'auto' ? t('node.auto') : n.name)}</b><span>${n.name === 'auto' ? (n.autoNow ? t('node.now', { n: n.autoNow }) : t('node.autoDesc')) : esc(n.type || '')}</span>${n.name !== 'auto' && n.delay > 0 ? `<div class="bar"><i style="width:${Math.max(6, 100 - n.delay / max * 80)}%"></i></div>` : ''}</div>
    <span class="ms ${msClass(n.delay)}">${n.name === 'auto' ? '' : msText(n.delay, testing)}</span></div>`).join('')}</div>`;
  $('#sheet-body').querySelectorAll('.item').forEach(it => it.addEventListener('click', async () => {
    try { await App().SelectNode(it.dataset.name); closeSheet(); } catch (e) { toast(errText(e), 'err'); }
  }));
  const applyFilter = () => { const q = nodeFilter.toLowerCase(); $('#sheet-body').querySelectorAll('.item').forEach(it => { it.hidden = !!q && it.dataset.name !== 'auto' && !it.dataset.name.toLowerCase().includes(q); }); };
  const f = $('#node-filter');
  if (f) { f.addEventListener('input', () => { nodeFilter = f.value.trim(); applyFilter(); }); applyFilter(); }
}
async function testNodes(force) {
  if (nodeTesting && !force) return;
  nodeTesting = true;
  $('#sheet-body').querySelectorAll('.ms').forEach((el, i) => { if (i > 0) el.innerHTML = '<span class="spinner"></span>'; });
  try { await App().TestAll(); } catch (e) { /* 内核没跑时没法测 */ }
  nodeTesting = false;
  if ($('#sheet').classList.contains('show')) await drawNodes(false);
  try { lastPing = await App().TestLatency('proxy'); } catch (e) { lastPing = 0; }
  updateHome();
}

// ---- 订阅管理 ----
function renderProfiles(el, editId) {
  el.innerHTML = `<div id="prof-form"></div><div class="list" id="prof-list"></div><div style="height:12px"></div><button class="btn primary block" id="prof-add">${t('prof.add')}</button>`;
  const showForm = (p) => {
    $('#prof-form').innerHTML = `<div class="card"><div class="field"><label>${t('prof.name')}</label><input type="text" id="pf-name" value="${esc(p ? p.name : '')}"></div>
      <div class="field with-btn"><label>${t('prof.url')}</label><input type="url" id="pf-url" value="${esc(p ? p.url : '')}"><button class="btn sm ghost" id="pf-paste">${t('common.paste')}</button></div>
      <div class="row"><button class="btn primary" id="pf-save">${t('prof.save')}</button><button class="btn" id="pf-cancel">${t('prof.cancel')}</button><span id="pf-busy"></span></div></div>`;
    $('#pf-cancel').addEventListener('click', () => { $('#prof-form').innerHTML = ''; });
    $('#pf-paste').addEventListener('click', () => pasteInto('#pf-url'));
    $('#pf-save').addEventListener('click', async () => {
      const b = $('#pf-save'); b.disabled = true; $('#pf-busy').innerHTML = '<span class="spinner"></span>';
      try {
        if (p) await App().UpdateProfile(p.id, $('#pf-name').value, $('#pf-url').value); else await App().AddProfile($('#pf-name').value, $('#pf-url').value);
        toast(t(p ? 'prof.saved' : 'prof.added'), 'ok'); $('#prof-form').innerHTML = ''; await load();
      } catch (e) { toast(errText(e), 'err'); }
      b.disabled = false; $('#pf-busy').innerHTML = '';
    });
    setTimeout(() => $(p ? '#pf-name' : '#pf-url').focus(), 80);
  };
  $('#prof-add').addEventListener('click', () => showForm(null));
  const load = async () => {
    let list = [];
    try { list = await App().GetProfiles(); } catch (e) { $('#prof-list').innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
    if (!list.length) { $('#prof-list').innerHTML = `<div class="empty">${t('prof.empty')}</div>`; return; }
    $('#prof-list').innerHTML = list.map(p => `<div class="item" style="flex-wrap:wrap">
      <div class="name" style="flex-basis:100%"><b>${esc(p.name)} ${p.active ? `<span class="tag">${t('prof.inUse')}</span>` : ''}</b><span>${esc(profileLine(p))}<br>${p.fetchedAt ? t('prof.updated', { t: fmtTime(p.fetchedAt) }) : t('prof.never')}${p.error ? ' · <span style="color:var(--danger)">' + esc(p.error) + '</span>' : ''}</span></div>
      <div class="row" style="flex-basis:100%;gap:6px">
        ${p.active ? '' : `<button class="btn sm" data-act="use" data-id="${esc(p.id)}">${t('prof.use')}</button>`}
        <button class="btn sm" data-act="refresh" data-id="${esc(p.id)}">${t('prof.refresh')}</button>
        <button class="btn sm" data-act="edit" data-id="${esc(p.id)}">${t('prof.edit')}</button>
        <span class="grow"></span><button class="btn sm danger ghost" data-act="del" data-id="${esc(p.id)}" data-name="${esc(p.name)}">${t('prof.del')}</button>
      </div></div>`).join('');
    $('#prof-list').querySelectorAll('button[data-act]').forEach(b => b.addEventListener('click', async () => {
      const id = b.dataset.id;
      try {
        if (b.dataset.act === 'use') { await App().SelectProfile(id); }
        else if (b.dataset.act === 'refresh') { b.disabled = true; b.innerHTML = '<span class="spinner"></span>'; await App().RefreshProfile(id); toast(t('prof.refreshed'), 'ok'); }
        else if (b.dataset.act === 'edit') { showForm(list.find(x => x.id === id)); return; }
        else if (b.dataset.act === 'del') { if (!confirm(t('prof.delConfirm', { n: b.dataset.name }))) return; await App().RemoveProfile(id); toast(t('prof.deleted'), 'ok'); }
      } catch (e) { toast(errText(e), 'err'); }
      await load();
    }));
  };
  load();
  if (editId) showForm({ id: '', name: '', url: editId });
}

// ---- 设置 ----
async function renderSettings(el) {
  let s;
  try { s = await App().GetSettings(); } catch (e) { el.innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
  const auto = await App().GetAutostart();
  const sw = (id, label, on, help) => `<div class="srow"><div class="lbl">${label}${help ? `<div>${help}</div>` : ''}</div><label class="switch"><input type="checkbox" id="${id}" ${on ? 'checked' : ''}></label></div>`;
  const num = (id, label, val, help) => `<div class="srow"><div class="lbl">${label}${help ? `<div>${help}</div>` : ''}</div><input type="number" id="${id}" value="${esc(val)}"></div>`;
  const txt = (id, label, val, help) => `<div class="srow"><div class="lbl">${label}${help ? `<div>${help}</div>` : ''}</div><input type="text" id="${id}" value="${esc(val)}"></div>`;
  const sel = (id, label, val, opts, help) => `<div class="srow"><div class="lbl">${label}${help ? `<div>${help}</div>` : ''}</div><select id="${id}">${opts.map(o => `<option value="${o[0]}" ${o[0] === val ? 'selected' : ''}>${o[1]}</option>`).join('')}</select></div>`;
  el.innerHTML = `
    <div class="card"><h3>${t('set.g.conn')}</h3>
      ${sw('f-tun', t('set.tun'), s.tun, t('set.tunHelp'))}
      ${sel('f-stack', t('set.tunStack'), s.tunStack, [['mixed', 'mixed'], ['system', 'system'], ['gvisor', 'gvisor']])}
      ${sw('f-strict', t('set.strict'), s.strictRoute, t('set.strictHelp'))}
      ${sw('f-lan', t('set.lan'), s.lanBypass, t('set.lanHelp'))}
      ${num('f-mixed', t('set.mixed'), s.mixedPort, t('set.mixedHelp'))}
      ${num('f-probe', t('set.probe'), s.probeMinutes, t('set.probeHelp'))}
      ${num('f-update', t('set.update'), s.updateHours)}
    </div>
    <div class="card"><h3>${t('set.g.dns')}</h3>
      ${txt('f-rdns', t('set.remoteDns'), s.remoteDns, t('set.remoteDnsHelp'))}
      ${txt('f-ldns', t('set.localDns'), s.localDns, t('set.localDnsHelp'))}
      ${sw('f-fakeip', t('set.fakeip'), s.fakeIp)}
      ${sw('f-ipv6', t('set.ipv6'), s.ipv6, t('set.ipv6Help'))}
    </div>
    <div class="card"><h3>${t('set.g.route')}</h3>
      ${sw('f-ad', t('set.adblock'), s.adBlock)}
      <div class="srow"><div class="lbl">${t('set.rules')}<div>${t('set.rulesHelp', { n: (s.ruleGroups || []).length })}</div></div><button class="btn sm" id="f-rules">${t('set.rulesManage')}</button></div>
      <div class="field" style="margin-top:8px"><label>${t('set.bypass')}</label><textarea id="f-bypass" placeholder="steam.exe">${esc((s.bypassApps || []).join('\n'))}</textarea><span class="help">${t('set.bypassHelp')}</span></div>
    </div>
    ${state.platform === 'linux' ? `<div class="card"><h3>${t('set.g.net')}</h3>
      ${sel('f-netmode', t('set.netMode'), s.netMode || 'local', [['local', t('set.netLocal')], ['gateway', t('set.netGateway')]], t('set.netModeHelp'))}
      ${txt('f-lansub', t('set.lanSubnets'), (s.lanSubnets || []).join(', '), t('set.lanSubnetsHelp'))}
      ${txt('f-weblisten', t('set.webListen'), s.webListen || '', t('set.webListenHelp'))}
    </div>` : ''}
    <div class="card"><h3>${t('set.g.logs')}</h3>
      ${sel('f-log', t('set.logLevel'), s.logLevel, [['debug', 'debug'], ['info', 'info'], ['warn', 'warn'], ['error', 'error']])}
      ${num('f-logdays', t('set.logDays'), s.logDays ?? 7, t('set.logDaysHelp'))}
    </div>
    <div class="card"><h3>${t('set.g.app')} <span class="tag">${t('set.instant')}</span></h3>
      ${sw('f-autostart', t(state.platform === 'linux' ? 'set.autostartLinux' : 'set.autostart'), auto, t(state.platform === 'linux' ? 'set.autostartLinuxHelp' : 'set.autostartHelp'))}
      ${sel('f-lang', t('set.lang'), LANG, [['zh', '中文'], ['en', 'English']])}
      ${sel('f-theme', t('set.theme'), state.theme || 'system', [['system', t('theme.system')], ['light', t('theme.light')], ['dark', t('theme.dark')]])}
    </div>
    <div class="savebar" id="savebar"><div class="note" id="save-note">${t('set.clean')}</div><button class="btn primary" id="save" disabled>${t('set.save')}</button></div>`;
  const watch = ['f-tun', 'f-stack', 'f-strict', 'f-lan', 'f-mixed', 'f-probe', 'f-update', 'f-rdns', 'f-ldns', 'f-fakeip', 'f-ipv6', 'f-ad', 'f-bypass', 'f-log', 'f-logdays', 'f-netmode', 'f-lansub', 'f-weblisten'].filter(id => $('#' + id));
  const dirty = on => { $('#save').disabled = !on; $('#savebar').classList.toggle('dirty', on); $('#save-note').textContent = on ? t('set.unsaved') + ' · ' + t('set.note') : t('set.clean'); $('#save').textContent = t(on ? 'set.saveChanges' : 'set.save'); };
  watch.forEach(id => ['input', 'change'].forEach(ev => $('#' + id).addEventListener(ev, () => dirty(true))));
  $('#save').addEventListener('click', async () => {
    const n = { ...s, tun: $('#f-tun').checked, tunStack: $('#f-stack').value, strictRoute: $('#f-strict').checked, lanBypass: $('#f-lan').checked,
      mixedPort: Number($('#f-mixed').value), probeMinutes: Number($('#f-probe').value), updateHours: Number($('#f-update').value),
      remoteDns: $('#f-rdns').value.trim(), localDns: $('#f-ldns').value.trim(), fakeIp: $('#f-fakeip').checked, ipv6: $('#f-ipv6').checked, adBlock: $('#f-ad').checked,
      bypassApps: $('#f-bypass').value.split(/\r?\n/).map(x => x.trim()).filter(Boolean), logLevel: $('#f-log').value, logDays: Number($('#f-logdays').value) };
    if ($('#f-netmode')) { n.netMode = $('#f-netmode').value; n.lanSubnets = $('#f-lansub').value.split(/[,，\s]+/).map(x => x.trim()).filter(Boolean); n.webListen = $('#f-weblisten').value.trim(); }
    try { s = await App().SaveSettings(n); dirty(false); toast(t('set.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
  });
  $('#f-autostart').addEventListener('change', async e => { try { await App().SetAutostart(e.target.checked); toast(t('set.saved'), 'ok'); } catch (err) { toast(errText(err), 'err'); e.target.checked = !e.target.checked; } });
  $('#f-lang').addEventListener('change', async e => { LANG = e.target.value; await App().SetLang(LANG); nav('settings'); });
  $('#f-theme').addEventListener('change', async e => { applyTheme(e.target.value); await App().SetTheme(e.target.value); });
  $('#f-rules').addEventListener('click', () => nav('rules'));
}


// ---- 路由规则 ----
const OUT_FIXED = ['proxy', 'direct', 'reject', 'auto'];
const RULE_TYPES = ['domain_suffix', 'domain', 'domain_keyword', 'domain_regex', 'ip_cidr', 'port', 'process_name', 'geosite', 'geoip'];
const outLabel = o => OUT_FIXED.includes(o) ? t('out.' + o) : o;
async function renderRules(el) {
  let s;
  try { s = await App().GetSettings(); } catch (e) { el.innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
  const groups = s.ruleGroups || [];
  const save = async next => { try { await App().SaveSettings({ ...s, ruleGroups: next }); toast(t('set.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); } nav('rules'); };
  const summary = g => { const r = g.rules || []; return outLabel(g.outbound) + ' · ' + t('rules.count', { n: r.length }) + (r.length ? ' · ' + r.slice(0, 3).map(x => x.value).join(', ') + (r.length > 3 ? '…' : '') : ''); };
  el.innerHTML = `<p class="small muted" style="margin:2px 0 10px">${t('rules.intro')}</p>
    <div class="list">${groups.map((g, i) => `<div class="item rg ${g.enabled ? '' : 'off'}" style="flex-wrap:wrap;animation-delay:${i * 30}ms">
      <div class="row" style="flex-basis:100%">
        <div class="name"><b>${esc(g.name)}</b><span>${esc(summary(g))}</span></div>
        <label class="switch"><input type="checkbox" data-act="toggle" data-i="${i}" ${g.enabled ? 'checked' : ''}></label>
      </div>
      <div class="row" style="flex-basis:100%;gap:6px">
        <button class="btn sm" data-act="edit" data-i="${i}">${t('rules.editBtn')}</button>
        <button class="btn sm" data-act="up" data-i="${i}" ${i === 0 ? 'disabled' : ''}>↑</button>
        <button class="btn sm" data-act="down" data-i="${i}" ${i === groups.length - 1 ? 'disabled' : ''}>↓</button>
        <span class="grow"></span><button class="btn sm danger ghost" data-act="del" data-i="${i}">${t('prof.del')}</button>
      </div></div>`).join('')}
      <div class="item rg builtin"><div class="name"><b>${t('rules.default')} <span class="tag">${t('rules.builtin')}</span></b><span>${t('rules.defaultDesc')}</span></div></div>
    </div>
    <div style="height:12px"></div><button class="btn primary block" id="rg-add">${t('rules.add')}</button>`;
  $('#rg-add').addEventListener('click', () => nav('ruleEdit', null));
  el.querySelectorAll('[data-act]').forEach(b => b.addEventListener(b.dataset.act === 'toggle' ? 'change' : 'click', async () => {
    const i = Number(b.dataset.i), next = groups.map(g => ({ ...g }));
    switch (b.dataset.act) {
      case 'toggle': next[i].enabled = b.checked; break;
      case 'edit': nav('ruleEdit', groups[i].id); return;
      case 'up': [next[i - 1], next[i]] = [next[i], next[i - 1]]; break;
      case 'down': [next[i + 1], next[i]] = [next[i], next[i + 1]]; break;
      case 'del': if (!confirm(t('rules.delConfirm', { n: groups[i].name }))) return; next.splice(i, 1); break;
    }
    await save(next);
  }));
}
async function renderRuleEdit(el, id) {
  let s, nodes = [];
  try { s = await App().GetSettings(); } catch (e) { el.innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
  try { nodes = (await App().GetNodes() || []).map(n => n.name).filter(n => n !== 'auto'); } catch (e) { /* 内核没跑时只有固定出口 */ }
  const groups = s.ruleGroups || [];
  const orig = groups.find(g => g.id === id);
  const g = orig ? JSON.parse(JSON.stringify(orig)) : { id: '', name: '', enabled: true, outbound: 'proxy', rules: [] };
  g.rules = g.rules || [];
  if (g.outbound && !OUT_FIXED.includes(g.outbound) && !nodes.includes(g.outbound)) nodes.push(g.outbound);
  const outs = [...OUT_FIXED.map(o => [o, t('out.' + o)]), ...nodes.map(n => [n, n])];
  el.innerHTML = `
    <div class="card">
      <div class="field"><label>${t('rules.name')}</label><input type="text" id="rg-name" value="${esc(g.name)}" placeholder="${t('rules.namePh')}"></div>
      <div class="srow"><div class="lbl">${t('rules.out')}<div>${t('rules.outHelp')}</div></div><select id="rg-out" style="width:150px">${outs.map(o => `<option value="${esc(o[0])}" ${o[0] === g.outbound ? 'selected' : ''}>${esc(o[1])}</option>`).join('')}</select></div>
      <div class="srow"><div class="lbl">${t('rules.enabled')}</div><label class="switch"><input type="checkbox" id="rg-on" ${g.enabled ? 'checked' : ''}></label></div>
    </div>
    <div class="card"><h3>${t('rules.conds')}</h3>
      <div class="row" style="gap:6px;margin-bottom:6px"><select id="rg-type" style="width:auto;flex:none;max-width:42%">${RULE_TYPES.map(ty => `<option value="${ty}">${t('rt.' + ty)}</option>`).join('')}</select><input type="text" id="rg-val" placeholder="${t('rt.' + RULE_TYPES[0] + '.ph')}"><button class="btn sm" id="rg-addc">${t('rules.addCond')}</button></div>
      <p class="small muted" style="margin:0 0 8px">${t('rules.condHelp')}</p>
      <div id="rg-rules"></div>
    </div>
    <div class="savebar dirty"><div class="note">${t('set.note')}</div><button class="btn primary" id="rg-save">${t('rules.saveGroup')}</button></div>`;
  const drawRules = () => {
    $('#rg-rules').innerHTML = g.rules.length ? `<div class="conds">${g.rules.map((r, i) => `<div class="cond"><span class="tag">${esc(t('rt.' + r.type))}</span><span class="val sel">${esc(r.value)}</span><button class="icon-btn xs" data-i="${i}" title="${t('prof.del')}"><svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>`).join('')}</div>` : `<div class="empty" style="padding:14px">${t('rules.noCond')}</div>`;
    $('#rg-rules').querySelectorAll('button[data-i]').forEach(b => b.addEventListener('click', () => { g.rules.splice(Number(b.dataset.i), 1); drawRules(); }));
  };
  const addCond = () => {
    const ty = $('#rg-type').value, vals = $('#rg-val').value.split(/[,，;；\s]+/).map(x => x.trim()).filter(Boolean);
    if (!vals.length) { $('#rg-val').focus(); return; }
    vals.forEach(v => { if (!g.rules.some(r => r.type === ty && r.value === v)) g.rules.push({ type: ty, value: v }); });
    $('#rg-val').value = ''; drawRules(); $('#rg-val').focus();
  };
  $('#rg-type').addEventListener('change', () => { $('#rg-val').placeholder = t('rt.' + $('#rg-type').value + '.ph'); });
  $('#rg-addc').addEventListener('click', addCond);
  $('#rg-val').addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addCond(); } });
  $('#rg-save').addEventListener('click', async () => {
    if ($('#rg-val').value.trim()) addCond(); // 输入框里还没点添加的也带上
    const out = { ...g, name: $('#rg-name').value.trim(), outbound: $('#rg-out').value, enabled: $('#rg-on').checked, rules: g.rules };
    if (!out.rules.length) { toast(t('rules.noCond'), 'err'); $('#rg-val').focus(); return; }
    const next = groups.map(x => ({ ...x })), i = next.findIndex(x => x.id === out.id);
    if (i >= 0) next[i] = out; else next.push(out);
    const b = $('#rg-save'); b.disabled = true;
    try { await App().SaveSettings({ ...s, ruleGroups: next }); toast(t('set.saved'), 'ok'); nav('rules'); }
    catch (e) { toast(errText(e), 'err'); b.disabled = false; }
  });
  drawRules();
  setTimeout(() => $(orig ? '#rg-val' : '#rg-name').focus(), 120);
}


// ---- 局域网设备(Linux 网关模式) ----
const DEV_MODES = ['', 'proxy', 'direct', 'reject'];
async function renderDevices(el) {
  el.innerHTML = `<p class="small muted" style="margin:2px 0 10px">${t('dev.intro')}</p><div id="dev-list"><div class="empty"><span class="spinner"></span></div></div>`;
  const draw = list => {
    const box = $('#dev-list'); if (!box) return;
    if (!list.length) { box.innerHTML = `<div class="empty">${t('dev.empty')}</div>`; return; }
    list.sort((a, b) => (b.online - a.online) || (b.saved - a.saved) || a.ip.localeCompare(b.ip, undefined, { numeric: true }));
    box.innerHTML = `<div class="list">${list.map((d, i) => `<div class="item dev" style="flex-wrap:wrap;animation-delay:${i * 20}ms" data-mac="${esc(d.mac)}" data-ip="${esc(d.ip)}">
      <div class="row" style="flex-basis:100%">
        <span class="dot ${d.online ? 'on' : ''}" title="${t(d.online ? 'dev.online' : 'dev.offline')}"></span>
        <div class="name"><b class="sel">${esc(d.name || d.ip)}</b><span>${esc(d.ip)} · ${esc(d.mac)}</span></div>
        <button class="icon-btn xs muted" data-act="rename" title="${t('dev.rename')}"><svg viewBox="0 0 24 24"><path d="M4 20h4l10-10-4-4L4 16z"/><path d="M12 6l4 4"/></svg></button>
      </div>
      <div class="seg dev-seg" style="flex-basis:100%;display:flex"><span class="pill"></span>${DEV_MODES.map(m => `<button data-mode="${m}" class="${(d.mode || '') === m ? 'active' : ''}" style="flex:1">${t('dev.m.' + (m || 'follow'))}</button>`).join('')}</div>
    </div>`).join('')}</div>`;
    box.querySelectorAll('.item.dev').forEach(it => {
      const seg = it.querySelector('.dev-seg');
      segInit(seg, async b => {
        try { draw(await App().SetDevice(it.dataset.mac, it.querySelector('.name b').textContent, b.dataset.mode, it.dataset.ip)); toast(t('set.saved'), 'ok'); }
        catch (e) { toast(errText(e), 'err'); }
      });
      it.querySelector('[data-act=rename]').addEventListener('click', async () => {
        const cur = it.querySelector('.name b').textContent, name = prompt(t('dev.renamePrompt'), cur); if (name === null) return;
        const mode = (it.querySelector('.dev-seg button.active') || {}).dataset ? it.querySelector('.dev-seg button.active').dataset.mode : '';
        try { draw(await App().SetDevice(it.dataset.mac, name.trim(), mode, it.dataset.ip)); } catch (e) { toast(errText(e), 'err'); }
      });
    });
  };
  const load = async () => { try { draw(await App().GetDevices() || []); } catch (e) { const b = $('#dev-list'); if (b) b.innerHTML = `<div class="empty">${esc(errText(e))}</div>`; } };
  load();
  pageTimer = setInterval(load, 10000);
}

// ---- 连接 ----
function renderConns(el) {
  el.innerHTML = `<div class="small muted" id="conns-n" style="margin:2px 0 8px"></div><div id="conns"></div>`;
  const load = async () => {
    const box = $('#conns'); if (!box) return;
    let list;
    try { list = await App().GetConnections() || []; } catch (e) { box.innerHTML = `<div class="empty">${esc(state && (state.view.state.status === 'connected') ? errText(e) : t('conns.needCore'))}</div>`; return; }
    const cn = $('#conns-n'); if (cn) cn.textContent = t('conns.count', { n: list.length });
    if (!list.length) { box.innerHTML = `<div class="empty">${t('conns.empty')}</div>`; return; }
    box.innerHTML = `<div class="list">${list.map(c => `<div class="item"><div class="name"><b class="sel">${esc(c.host)}</b><span>${esc(c.app || '')} ${c.app ? '·' : ''} ${esc(c.chain)} · ${esc(c.net)} · ↓${fmtBytes(c.down)} ↑${fmtBytes(c.up)}</span></div><button class="btn sm ghost" data-id="${esc(c.id)}">${t('conns.close')}</button></div>`).join('')}</div>`;
    box.querySelectorAll('button[data-id]').forEach(b => b.addEventListener('click', async () => { try { await App().CloseConnection(b.dataset.id); load(); } catch (e) { toast(errText(e), 'err'); } }));
  };
  load();
  pageTimer = setInterval(load, 2000);
}

// ---- 日志 ----
function renderLogs(el) {
  el.innerHTML = `<div class="row" style="margin-bottom:10px;flex-wrap:wrap">
      <div class="seg" id="logsrc"><span class="pill"></span><button data-core="0" class="active">${t('logs.service')}</button><button data-core="1">${t('logs.core')}</button></div>
      <button class="btn sm" id="pause">${t('logs.pause')}</button><span class="grow"></span>
      ${window.__web ? '' : `<button class="btn sm" id="open">${t('logs.open')}</button>`}<button class="btn sm" id="diag">${t('logs.diag')}</button>
    </div><pre class="log sel" id="log"></pre>`;
  let core = false, paused = false;
  const colorize = l => { const e = esc(l); if (/ERROR|FATAL|panic/i.test(l)) return `<span class="err">${e}</span>`; if (/WARN/i.test(l)) return `<span class="warn">${e}</span>`; if (/DEBUG/i.test(l)) return `<span class="dim">${e}</span>`; return e; };
  const load = async () => {
    const pre = $('#log'); if (!pre || paused) return;
    try { const lines = await App().GetLogs(300, core) || []; const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 8; pre.innerHTML = lines.map(colorize).join('\n'); if (atBottom) pre.scrollTop = pre.scrollHeight; }
    catch (e) { pre.textContent = errText(e); }
  };
  segInit($('#logsrc'), b => { core = b.dataset.core === '1'; load(); });
  $('#pause').addEventListener('click', () => { paused = !paused; $('#pause').textContent = t(paused ? 'logs.resume' : 'logs.pause'); if (!paused) load(); });
  const op = $('#open'); if (op) op.addEventListener('click', () => App().OpenLogs().catch(e => toast(errText(e), 'err')));
  $('#diag').addEventListener('click', exportDiag);
  load();
  pageTimer = setInterval(load, 3000);
}
function segInit(seg, onPick) {
  const pill = seg.querySelector('.pill');
  const move = b => { pill.style.left = b.offsetLeft + 'px'; pill.style.width = b.offsetWidth + 'px'; };
  const btns = [...seg.querySelectorAll('button')];
  btns.forEach(b => b.addEventListener('click', () => { btns.forEach(x => x.classList.toggle('active', x === b)); move(b); onPick(b); }));
  requestAnimationFrame(() => move(seg.querySelector('button.active')));
}
async function exportDiag() {
  try {
    const r = await App().ExportDiag();
    if (window.__web) { window.open(r, '_blank'); toast(t('logs.downloading'), 'ok'); } else toast(t('logs.exported'), 'ok');
  } catch (e) { toast(errText(e), 'err'); }
}

// ---- 关于 ----
function renderAbout(el) {
  const v = state ? state.view : {};
  el.innerHTML = `<div class="card">
      <div class="srow"><div class="lbl">${t('about.ui')}</div><b>v${esc(state ? state.version : '')}</b></div>
      <div class="srow"><div class="lbl">${t('about.svc')}</div><b>v${esc(v.version || '—')}</b></div>
      <div class="srow"><div class="lbl">${t('about.svcState')}</div><b id="about-svc">${esc(svcText(state))}</b></div>
      <div class="srow"><div class="lbl">sing-box</div><span class="muted small">${t('about.license')}</span></div>
    </div>
    <div class="card" id="upd-card">
      <div class="srow"><div class="lbl"><span id="upd-text">${state && state.update ? t('about.found', { v: state.update.version }) : t('about.updateTitle')}</span><div>${t('about.updateNote')}</div></div><button class="btn sm" id="upd-check">${t('about.check')}</button></div>
      <div id="upd-body"></div>
    </div>
    <div class="card">
      ${window.__web ? '' : `<div class="srow"><div class="lbl">${t('about.repair')}</div><button class="btn sm" id="repair">${t('about.repair')}</button></div>`}
      <div class="srow"><div class="lbl">${t('about.diag')}</div><button class="btn sm" id="diag">${t('about.diag')}</button></div>
      ${window.__web ? `<div class="srow"><div class="lbl">${t('about.web')}<div>${t('about.webHelp')}</div></div><button class="btn sm" id="webpw">${t('about.webPw')}</button></div>` : `<div class="srow"><div class="lbl">${t('about.quit')}<div>${t('about.quitHelp')}</div></div><button class="btn sm danger" id="quit">${t('about.quit')}</button></div>`}
    </div>
    <p class="small muted center"><a id="repo" style="color:var(--brand2)">github.com/Maoyangui/godusevpn</a></p>`;
  const showUpdate = rel => {
    $('#upd-text').textContent = rel ? t('about.found', { v: rel.version }) : t('about.latest');
    $('#upd-body').innerHTML = rel ? `<div style="margin-top:10px"><button class="btn primary block" id="upd-go">${t('about.update')}</button><div class="progress" id="upd-prog" style="margin-top:10px" hidden><i></i></div></div>` : '';
    if (rel) $('#upd-go').addEventListener('click', async () => {
      const b = $('#upd-go'); b.disabled = true; $('#upd-prog').hidden = false;
      try { await App().ApplyUpdate(); $('#upd-text').textContent = t('about.installing'); }
      catch (e) { toast(errText(e), 'err'); b.disabled = false; }
    });
  };
  if (state && state.update) showUpdate(state.update);
  $('#upd-check').addEventListener('click', async () => {
    const b = $('#upd-check'); b.disabled = true; $('#upd-text').textContent = t('about.checking');
    try { showUpdate(await App().CheckUpdate()); } catch (e) { $('#upd-text').textContent = errText(e); }
    b.disabled = false;
  });
  const rp = $('#repair'); if (rp) rp.addEventListener('click', repairService);
  $('#diag').addEventListener('click', exportDiag);
  const q = $('#quit'); if (q) q.addEventListener('click', () => App().QuitApp());
  const wp = $('#webpw'); if (wp) wp.addEventListener('click', async () => {
    const pw = prompt(t('about.webPwPrompt')); if (pw === null) return;
    try { await App().SetWebPassword(pw); toast(t('set.saved'), 'ok'); if (pw) setTimeout(() => location.reload(), 800); } catch (e) { toast(errText(e), 'err'); }
  });
  $('#repo').addEventListener('click', () => { if (window.__web) window.open('https://github.com/Maoyangui/godusevpn', '_blank'); else App().OpenURL('https://github.com/Maoyangui/godusevpn'); });
}

// ---- 浏览器面板:登录 ----
function showLogin() {
  const box = $('#login'); box.hidden = false;
  $('#login-title').textContent = t('login.title'); $('#login-go').textContent = t('login.go');
  setTimeout(() => $('#login-pw').focus(), 100);
}
function initLogin() {
  const go = async () => {
    const b = $('#login-go'); b.disabled = true; $('#login-err').textContent = '';
    try {
      const r = await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: $('#login-pw').value }) });
      const j = await r.json().catch(() => ({}));
      if (j.error) { $('#login-err').textContent = j.error; b.disabled = false; $('#login-pw').select(); return; }
      location.reload();
    } catch (e) { $('#login-err').textContent = errText(e); b.disabled = false; }
  };
  $('#login-go').addEventListener('click', go);
  $('#login-pw').addEventListener('keydown', e => { if (e.key === 'Enter') go(); });
  window.addEventListener('web-auth', showLogin);
}

// ---- 启动 ----
async function init() {
  if (window.__web) { $('#app').classList.add('web'); document.body.classList.add('web'); initLogin(); }
  try { state = await App().GetState(); }
  catch (e) { if (String(e && e.message) === 'AUTH_REQUIRED') { showLogin(); return; } state = { service: false, svcState: 'down', view: { state: { status: 'disconnected' }, profiles: [], nodes: [] }, lang: 'zh', theme: 'system', version: '' }; }
  LANG = state.lang || 'zh';
  applyTheme(state.theme);
  document.documentElement.lang = LANG === 'en' ? 'en' : 'zh';
  $('#menu-btn').addEventListener('click', openDrawer);
  $('#back-btn').addEventListener('click', () => BACK[view] ? nav(BACK[view]) : navHome());
  $('#drawer-backdrop').addEventListener('click', closeDrawer);
  $('#drawer-upd').addEventListener('click', () => { closeDrawer(); nav('about'); });
  $('#sheet-backdrop').addEventListener('click', closeSheet);
  window.runtime.EventsOn('nav', name => { closeDrawer(); closeSheet(); if (PAGES[name]) nav(name); });
  $('#min-btn').addEventListener('click', () => App().Minimize());
  $('#close-btn').addEventListener('click', () => App().HideWindow());
  document.addEventListener('keydown', e => { if (e.key === 'Escape') { closeSheet(); closeDrawer(); } });
  $('#svc-text').textContent = svcText(state);
  navHome();
  window.runtime.EventsOn('state', st => {
    const langChanged = (st.lang || 'zh') !== LANG, hadProfiles = state && state.view.profiles && state.view.profiles.length;
    state = st; LANG = st.lang || 'zh';
    applyTheme(st.theme);
    $('#svc-dot').className = 'dot ' + (st.service ? 'on' : 'err');
    $('#svc-text').textContent = svcText(st);
    if ($('#drawer').classList.contains('show')) renderDrawer();
    if (langChanged) { nav(view); return; }
    const has = st.view.profiles && st.view.profiles.length;
    if (view === 'onboard' && has) nav('home');
    else if (view === 'home' && !has && hadProfiles) nav('onboard');
    updateHome(); updateOnboardSvc();
    if (view === 'about' && $('#about-svc')) $('#about-svc').textContent = svcText(st);
  });
  window.runtime.EventsOn('traffic', tr => {
    if (!state) return;
    const st = state.view.state.status, on = st === 'connected' || st === 'degraded';
    if (!on) { state.down = 0; state.up = 0; return; }
    const pd = $('#s-down'), pu = $('#s-up');
    if (view === 'home' && pd) { tween(pd, state.down, tr.down, fmtSpeed, 600); tween(pu, state.up, tr.up, fmtSpeed, 600); }
    state.down = tr.down; state.up = tr.up;
  });
  window.runtime.EventsOn('update-progress', p => {
    const bar = $('#upd-prog'); if (!bar) return;
    const pct = p.total > 0 ? Math.round(p.done / p.total * 100) : 0;
    bar.hidden = false; bar.querySelector('i').style.width = pct + '%';
    $('#upd-text').textContent = t('about.downloading', { p: pct });
  });
  window.runtime.EventsOn('import', url => { nav('profiles'); setTimeout(() => { const f = $('#prof-add'); if (f) f.click(); const u = $('#pf-url'); if (u) u.value = url; }, 350); });
}
document.addEventListener('DOMContentLoaded', init);
