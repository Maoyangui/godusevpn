// 佛跳墙 竖向客户端页面。后端方法在 window.go.main.App;服务端每 1.5 秒推 "state",内核每秒推 "traffic"。
const App = () => window.go.main.App;
const $ = s => document.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
let state = null;          // 最新 UIState
let view = null;           // 当前页面名:onboard / home / settings / profiles / conns / logs / about
let pageTimer = null;      // 页面自己的定时刷新
let lastPing = 0;          // 手动测速刚测出来的延迟;服务每次推状态时会用它自己那份覆盖
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
// ---- 应用内对话框 ----
// 不用浏览器自带的 confirm / prompt:Windows 的 WebView2 会画成「wails.localhost 显示」的系统弹窗贴在窗口左上角,
// 安卓与浏览器面板也各带一行来源地址,样式和位置都跟应用对不上。这里自己画一个,三端一致。
let dialogClose = null;
function closeDialog(result) {
  const f = dialogClose;
  dialogClose = null;
  const box = $('#dialog');
  if (box) box.remove();
  $('#dialog-backdrop')?.remove();
  if (f) f(result);
}
function askDialog(opts) {
  return new Promise(resolve => {
    closeDialog(opts.input !== undefined ? null : false); // 同时只留一个
    const bd = document.createElement('div');
    bd.className = 'backdrop show';
    bd.id = 'dialog-backdrop';
    const box = document.createElement('div');
    box.className = 'dialog';
    box.id = 'dialog';
    box.innerHTML = `<div class="dialog-card">
      <div class="dialog-text">${esc(opts.text)}</div>
      ${opts.input !== undefined ? `<input type="${opts.password ? 'password' : 'text'}" id="dialog-input" value="${esc(opts.input)}">` : ''}
      <div class="row" style="justify-content:flex-end;margin-top:14px">
        <button class="btn" id="dialog-no">${esc(opts.cancel || t('common.cancel'))}</button>
        <button class="btn ${opts.danger ? 'danger' : 'primary'}" id="dialog-yes">${esc(opts.ok || t('common.confirm'))}</button>
      </div></div>`;
    $('#app').append(bd, box);
    const input = $('#dialog-input');
    dialogClose = resolve;
    const done = ok => closeDialog(opts.input === undefined ? ok : (ok ? (input ? input.value : '') : null));
    $('#dialog-yes').addEventListener('click', () => done(true));
    $('#dialog-no').addEventListener('click', () => done(false));
    bd.addEventListener('click', () => done(false));
    box.addEventListener('keydown', e => {
      if (e.key === 'Enter' && input) { e.preventDefault(); done(true); }
      if (e.key === 'Escape') { e.preventDefault(); done(false); }
    });
    setTimeout(() => { (input || $('#dialog-yes')).focus(); if (input) input.select(); }, 40);
  });
}
const askConfirm = (text, opts = {}) => askDialog({ text, danger: true, ...opts });
const askInput = (text, value = '', opts = {}) => askDialog({ text, input: value, ...opts });

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
const THEME_ICON = {
  system: '<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>',
  light: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M19.1 4.9l-1.4 1.4M6.3 17.7l-1.4 1.4"/></svg>',
  dark: '<svg viewBox="0 0 24 24"><path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z"/></svg>',
};
const THEME_NEXT = { system: 'light', light: 'dark', dark: 'system' };
let themeWish = null;           // 刚点下的主题;服务回推同一个值之前它说了算
const curTheme = () => themeWish || (state && state.theme) || 'system';
async function setTheme(v) {
  themeWish = v;
  applyTheme(v);
  updateThemeBtn();
  const sel = $('#f-theme');
  if (sel && sel.value !== v) sel.value = v;
  try { await App().SetTheme(v); }
  catch (e) { themeWish = null; applyTheme(state ? state.theme : 'system'); updateThemeBtn(); toast(errText(e), 'err'); }
}
function updateThemeBtn() {
  const b = $('#theme-btn');
  if (!b) return;
  const th = curTheme();
  if (b.dataset.theme === th) return; // 只在真的变了的时候动 DOM
  b.dataset.theme = th;
  b.innerHTML = THEME_ICON[th] || THEME_ICON.system;
  b.title = t('theme.' + th);
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
// 重绘会把焦点所在的元素换掉(遥控器上焦点就没了):画之前记下 data-* 键,画完再落回同一项
function focusKey(container, attr) {
  const c = document.activeElement, el = c && container && container.contains(c) ? c.closest('[' + attr + ']') : null;
  return el ? el.getAttribute(attr) : null;
}
function refocus(container, attr, key) {
  if (key == null || !container) return;
  const el = [...container.querySelectorAll('[' + attr + ']')].find(x => x.getAttribute(attr) === key);
  if (el) focusEl(el);
}
function renderDrawer() {
  const keep = focusKey($('#drawer-items'), 'data-page');
  $('#drawer-items').innerHTML =menuItems().map(m => `<a data-page="${m}"><svg viewBox="0 0 24 24">${ICON[m]}</svg><span>${t('menu.' + m)}</span>${m === 'about' && state && state.update ? '<span class="grow"></span><span class="pill-badge">NEW</span>' : ''}</a>`).join('');
  $('#drawer-items').querySelectorAll('a').forEach(a => a.addEventListener('click', () => { closeDrawer(); nav(a.dataset.page); }));
  refocus($('#drawer-items'), 'data-page', keep);
  $('#drawer-ver').textContent = 'v' + (state ? state.version : '');
  const u = $('#drawer-upd');
  u.hidden = !(state && state.update);
  if (state && state.update) u.textContent = t('drawer.update', { v: state.update.version });
  updateThemeBtn();
  renderDrawerState();
}
let dsLang = null;
// renderDrawerState 抽屉顶上那张状态卡:接着品牌区往下排,顺手把三种模式摆出来,不用先回首页。
// 卡片只搭一次(换语言才重搭),之后每次状态推送只改文字与 class —— 整块重画会把正按着的按钮换掉。
function renderDrawerState() {
  const box = $('#drawer-state');
  if (!box || !state) return;
  const v = state.view, st = v.state.status, on = st === 'connected' || st === 'degraded';
  box.hidden = !(v.profiles && v.profiles.length); // 还没加订阅时抽屉里没什么可显示的
  if (box.hidden) return;
  if (dsLang !== LANG) {
    dsLang = LANG;
    box.innerHTML = `<div class="ds-top"><span class="dot" id="ds-dot"></span><b id="ds-state"></b><span class="grow"></span><span id="ds-time"></span></div>
      <button class="ds-node" id="ds-node"><span class="flag none sm" id="ds-flag">${ICON_GLOBE}</span><span class="ds-mid"><b id="ds-name"></b><span id="ds-sub"></span></span></button>
      <div class="ds-modes" id="ds-modes">${['rule', 'global', 'direct'].map(m => `<button data-mode="${m}">${t('mode.' + m)}</button>`).join('')}</div>`;
    $('#ds-node').addEventListener('click', () => { closeDrawer(); sheetNodes(); });
    $('#ds-modes').querySelectorAll('button').forEach(b => b.addEventListener('click', async () => {
      if (b.classList.contains('on')) return; // 已经是这个模式就别再打扰后端
      try { await App().SetMode(b.dataset.mode); toast(t('mode.switched', { m: t('mode.' + b.dataset.mode) }), 'ok'); }
      catch (e) { toast(errText(e), 'err'); }
    }));
  }
  $('#ds-dot').className = 'dot ' + (on ? 'on' : (st === 'error' || !state.service) ? 'err' : '');
  $('#ds-state').textContent = state.service ? t('st.' + st) : t('svc.down');
  $('#ds-time').textContent = on && v.uptime ? fmtDuration(v.uptime) : '';
  const auto = v.node === 'auto' || !v.node;
  const name = auto ? (v.autoNow || t('node.auto')) : v.node;
  setFlag($('#ds-flag'), geoCode(name));
  $('#ds-name').textContent = cleanName(name);
  const ping = v.ping || lastPing;
  $('#ds-sub').textContent = !on ? t('home.pickNode')
    : v.mode === 'direct' ? t('home.directAll')
      : ([v.exitIp || '', ping ? ping + ' ' + t('common.ms') : ''].filter(Boolean).join(' · ') || t('mode.' + v.mode));
  $('#ds-modes').querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.mode === v.mode));
}
function openDrawer() {
  renderDrawer(); $('#drawer').classList.add('show'); $('#drawer-backdrop').classList.add('show');
  try { App().PokeUpdate(); } catch (e) { /* 旧服务没有这个方法 */ }
  tvFocus();
}
function closeDrawer() { $('#drawer').classList.remove('show'); $('#drawer-backdrop').classList.remove('show'); }
let sheetOnClose = null;
function openSheet(title, html, opts = {}) {
  $('#sheet-head').innerHTML = `<span class="grow">${esc(title)}</span>${opts.action ? `<button class="btn sm ghost" id="sheet-action">${esc(opts.action)}</button>` : ''}`;
  $('#sheet-body').innerHTML = html;
  $('#sheet').classList.add('show'); $('#sheet-backdrop').classList.add('show');
  sheetOnClose = opts.onClose || null;
  if (opts.action && opts.onAction) $('#sheet-action').addEventListener('click', opts.onAction);
  tvFocus();
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
  // 把已有的页面全部清掉(可能不止一个:引导页加订阅后自己 nav 一次,状态事件又 nav 一次,叠在一起就是两个首页)
  for (const old of stage.querySelectorAll('.view')) {
    if (name === 'home' && view !== 'home' && !old.classList.contains('pop')) { old.classList.add('pop'); setTimeout(() => old.remove(), 240); } else old.remove();
  }
  view = name;
  clearInterval(pageTimer); pageTimer = null;
  const el = document.createElement('div');
  el.className = 'view ' + name + (name === 'settings' || name === 'ruleEdit' ? ' has-bar' : ''); // 页名当类名用;带保存栏的页内容不够高时保存栏也贴底
  stage.appendChild(el);
  PAGES[name](el, arg);
  setTop(name === 'home' || name === 'onboard' ? t('app.name') : t(TITLES[name]), name !== 'home' && name !== 'onboard');
  tvFocus();
}
function navHome() { nav(state && state.view.profiles && state.view.profiles.length ? 'home' : 'onboard'); }

// ---- 引导 ----
function renderOnboard(el) {
  el.innerHTML = `<div class="onboard">
    <svg class="logo" viewBox="-16 -50 400 400"><defs><linearGradient id="g2" gradientUnits="userSpaceOnUse" x1="70" y1="70" x2="310" y2="270"><stop offset="0" stop-color="#6a44f2"/><stop offset=".55" stop-color="#2f8bff"/><stop offset="1" stop-color="#18e3e8"/></linearGradient></defs><g fill="none" stroke="url(#g2)" stroke-width="44" stroke-linecap="round" stroke-linejoin="round"><path d="M86 248V84l146 124v52"/><path d="M332 78l-88 96"/></g><path fill="url(#g2)" d="M118 192l52 30-52 30z"/></svg>
    <h1>${t('onboard.title')}</h1>
    <p>${t('onboard.desc')}</p>
    <div class="field with-btn"><input type="url" id="ob-url" placeholder="${t('onboard.url')}" spellcheck="false" autofocus><button class="btn sm ghost" id="ob-paste">${t('common.paste')}</button></div>
    <input type="hidden" id="ob-name">
    <button class="btn primary block" id="ob-go">${t('onboard.go')}</button>
    <div id="ob-err" class="small" style="color:var(--danger);min-height:18px"></div>
    <div class="or"><span></span><em>${t('onboard.or')}</em><span></span></div>
    <div class="tipcard">
      <span class="tipic"><svg viewBox="0 0 24 24"><path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/></svg></span>
      <span class="tiptxt"><b>${t('onboard.import')}</b><span>${t('onboard.importDesc')}</span></span>
    </div>
    <div class="grow1"></div>
    <div class="notecard">
      <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8v.5"/></svg>
      <span>${t('onboard.privacy')}</span>
    </div>
    <div id="ob-svc"></div>
  </div>`;
  updateOnboardSvc();
  const submit = async () => {
    const url = $('#ob-url').value.trim();
    if (!url) { $('#ob-url').focus(); return; }
    const b = $('#ob-go'); b.disabled = true; b.textContent = t('onboard.adding'); $('#ob-err').textContent = '';
    try { await App().AddProfile($('#ob-name').value, url); toast(t('prof.added'), 'ok'); state = await App().GetState(); if (view === 'onboard') nav('home'); }
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

// ---- 地区牌子 ----
// 节点名里一般写着地区,认出来给一块小牌子,首页和节点列表共用。
// 认的顺序:旗帜 emoji(两个区域指示符本身就是国家代码)→ 中英文地名 → 独立的两位代码。
// 两位代码必须两侧都不是字母数字,否则 "Russia" 里的 us、"Trojan" 里的字母都会被当成国家。
const GEO_NAMES = [
  ['HK', '香港,中國香港,中国香港,hongkong,hong kong'],
  ['MO', '澳门,澳門,macao,macau'],
  ['TW', '台湾,台灣,臺灣,taiwan,taipei,新北'],
  ['JP', '日本,东京,東京,大阪,japan,tokyo,osaka'],
  ['KR', '韩国,韓國,首尔,首爾,korea,seoul'],
  ['SG', '新加坡,狮城,獅城,singapore'],
  ['US', '美国,美國,洛杉矶,洛杉磯,圣何塞,聖何塞,西雅图,西雅圖,达拉斯,達拉斯,纽约,紐約,硅谷,凤凰城,united states,america,los angeles,san jose,seattle,dallas,new york,silicon valley,phoenix'],
  ['GB', '英国,英國,伦敦,倫敦,united kingdom,england,london,britain'],
  ['DE', '德国,德國,法兰克福,法蘭克福,germany,frankfurt'],
  ['FR', '法国,法國,巴黎,france,paris'],
  ['NL', '荷兰,荷蘭,阿姆斯特丹,netherlands,amsterdam'],
  ['RU', '俄罗斯,俄羅斯,莫斯科,russia,moscow'],
  ['CA', '加拿大,多伦多,多倫多,canada,toronto,montreal'],
  ['AU', '澳大利亚,澳大利亞,澳洲,悉尼,australia,sydney'],
  ['IN', '印度,孟买,孟買,india,mumbai'],
  ['TR', '土耳其,伊斯坦布尔,turkey,istanbul'],
  ['BR', '巴西,圣保罗,聖保羅,brazil,sao paulo'],
  ['AR', '阿根廷,argentina'],
  ['ES', '西班牙,马德里,馬德里,spain,madrid'],
  ['IT', '意大利,米兰,米蘭,italy,milan'],
  ['MY', '马来西亚,馬來西亞,吉隆坡,malaysia,kuala lumpur'],
  ['TH', '泰国,泰國,曼谷,thailand,bangkok'],
  ['VN', '越南,vietnam'],
  ['PH', '菲律宾,菲律賓,philippines,manila'],
  ['ID', '印尼,印度尼西亚,雅加达,indonesia,jakarta'],
  ['AE', '阿联酋,阿聯酋,迪拜,杜拜,dubai,emirates'],
  ['IE', '爱尔兰,愛爾蘭,都柏林,ireland,dublin'],
  ['PL', '波兰,波蘭,华沙,poland,warsaw'],
  ['SE', '瑞典,斯德哥尔摩,sweden,stockholm'],
  ['CH', '瑞士,苏黎世,蘇黎世,switzerland,zurich'],
  ['FI', '芬兰,芬蘭,赫尔辛基,finland,helsinki'],
  ['NO', '挪威,norway,oslo'],
  ['MX', '墨西哥,mexico'],
  ['ZA', '南非,south africa,johannesburg'],
  ['CN', '中国,中國,回国,回國,上海,北京,广州,廣州,深圳,china,shanghai,beijing'],
  ['UA', '乌克兰,烏克蘭,ukraine'],
  ['AT', '奥地利,奧地利,austria,vienna'],
  ['BE', '比利时,比利時,belgium'],
  ['DK', '丹麦,丹麥,denmark'],
  ['PT', '葡萄牙,portugal,lisbon'],
  ['CZ', '捷克,czech,prague'],
  ['RO', '罗马尼亚,羅馬尼亞,romania'],
  ['IL', '以色列,israel'],
  ['SA', '沙特,saudi'],
  ['EG', '埃及,egypt'],
  ['CL', '智利,chile'],
  ['NZ', '新西兰,新西蘭,new zealand'],
  ['KZ', '哈萨克斯坦,哈薩克,kazakhstan'],
  ['MT', '马耳他,馬耳他,malta'],
  ['LU', '卢森堡,盧森堡,luxembourg'],
  ['HU', '匈牙利,hungary'],
  ['GR', '希腊,希臘,greece'],
  ['RS', '塞尔维亚,serbia'],
  ['BG', '保加利亚,bulgaria'],
  ['LT', '立陶宛,lithuania'],
  ['LV', '拉脱维亚,latvia'],
  ['EE', '爱沙尼亚,estonia'],
  ['IS', '冰岛,冰島,iceland'],
];
const GEO_SET = new Set(GEO_NAMES.map(x => x[0]));
// 主要地区给一对贴近国旗的颜色,其余按代码散一个色相出来,同一个地区每次都一样
const GEO_COLOR = {
  HK: ['#e11d48', '#f97316'], MO: ['#16a34a', '#34d399'], TW: ['#2f8bff', '#18e3e8'], JP: ['#f43f5e', '#fb7185'],
  KR: ['#3b82f6', '#ef4444'], SG: ['#ef4444', '#fb7185'], US: ['#2563eb', '#ef4444'], GB: ['#1d4ed8', '#dc2626'],
  DE: ['#374151', '#f59e0b'], FR: ['#2563eb', '#dc2626'], NL: ['#f97316', '#2563eb'], RU: ['#2563eb', '#dc2626'],
  CA: ['#dc2626', '#f87171'], AU: ['#1e40af', '#0ea5e9'], IN: ['#f97316', '#16a34a'], TR: ['#dc2626', '#f87171'],
  BR: ['#16a34a', '#facc15'], ES: ['#fbbf24', '#e11d48'], IT: ['#16a34a', '#dc2626'], MY: ['#1d4ed8', '#facc15'],
  TH: ['#dc2626', '#1d4ed8'], AE: ['#16a34a', '#374151'], CN: ['#dc2626', '#f59e0b'], IE: ['#16a34a', '#f97316'],
};
const ICON_GLOBE = '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a15 15 0 0 1 0 18a15 15 0 0 1 0-18"/></svg>';
// flagCode 旗帜 emoji 是两个区域指示符,减掉基址就是 ISO 国家代码,不用查表
function flagCode(s) {
  const m = String(s || '').match(/[\u{1F1E6}-\u{1F1FF}]{2}/u);
  return m ? [...m[0]].map(c => String.fromCharCode(c.codePointAt(0) - 0x1F1E6 + 65)).join('') : '';
}
function geoCode(name) {
  const s = String(name || '');
  const f = flagCode(s);
  if (f) return f;
  const low = s.toLowerCase();
  for (const [code, alias] of GEO_NAMES) for (const a of alias.split(',')) if (low.includes(a)) return code;
  const m = low.match(/(?:^|[^a-z0-9])([a-z]{2})(?=$|[^a-z0-9])/);
  if (m) { const c = m[1].toUpperCase(); if (GEO_SET.has(c)) return c; }
  return '';
}
function geoStyle(code) {
  const c = GEO_COLOR[code];
  if (c) return `--c1:${c[0]};--c2:${c[1]}`;
  let h = 0;
  for (let i = 0; i < code.length; i++) h = (h * 37 + code.charCodeAt(i)) % 360;
  return `--c1:hsl(${h} 66% 52%);--c2:hsl(${(h + 40) % 360} 70% 44%)`;
}
function flagHTML(code, cls = '') {
  const u = code ? flagURL(code) : '';
  if (u) return `<span class="flag fl ${cls}"><img class="flag-svg" src="${u}" alt=""></span>`;
  return code ? `<span class="flag ${cls}" style="${geoStyle(code)}">${code}</span>` : `<span class="flag none ${cls}">${ICON_GLOBE}</span>`;
}
// setFlag 就地换牌子:地区没变就一个字节都不动(状态每 1.5 秒推一次)
function setFlag(el, code) {
  if (!el) return;
  const key = code || '-';
  if (el.dataset.geo === key) return;
  el.dataset.geo = key;
  const sm = el.classList.contains('sm') ? ' sm' : '';
  const u = code ? flagURL(code) : '';
  el.className = 'flag' + (u ? ' fl' : code ? '' : ' none') + sm;
  el.setAttribute('style', u || !code ? '' : geoStyle(code));
  el.innerHTML = u ? `<img class="flag-svg" src="${u}" alt="">` : (code ? esc(code) : ICON_GLOBE);
}
let regionNamer = null, regionLang = '';
// regionName 国家代码 → 短国名。中文一律用上面表里的第一个别名(香港、台湾、澳门……):
// 各版本 WebView 自带的 ICU 数据不一样,有的会把 HK 写成"中国香港特别行政区",筛选条上排不下。
// 英文表里没有短名,交给 Intl 的 short 样式(Hong Kong / US / UK),它也没有就直接显示代码。
function regionName(code) {
  if (!code) return '';
  if (LANG !== 'en') {
    const row = GEO_NAMES.find(x => x[0] === code);
    if (row) return row[1].split(',')[0];
  }
  try {
    if (!regionNamer || regionLang !== LANG) { regionNamer = new Intl.DisplayNames([LANG === 'en' ? 'en' : 'zh-CN'], { type: 'region', style: 'short' }); regionLang = LANG; }
    return regionNamer.of(code) || code;
  } catch (e) { return code; }
}
// cleanName 去掉名字开头的旗帜(牌子已经画出来了),后端拿到的仍是原名
function cleanName(name) {
  return String(name || '').replace(/^[\s\-|·]*[\u{1F1E6}-\u{1F1FF}]{2}[\s\-|·]*/u, '').trim() || String(name || '');
}
// nodeMult 节点名里的倍率(2x / x2 / 0.5倍);一倍和认不出来都返回 0,不显示牌子
function nodeMult(name) {
  const s = String(name || '');
  const m = s.match(/(?:^|[^\d.])(\d+(?:\.\d+)?)\s*[xX×倍]/) || s.match(/[xX×]\s*(\d+(?:\.\d+)?)/);
  if (!m) return 0;
  const v = parseFloat(m[1]);
  return v > 0 && v !== 1 ? v : 0;
}

// ---- 首页 ----
let hist = [];        // 最近一分钟的速度采样:首页那条曲线就是它画出来的
const HIST_N = 60;
function pushHist(d, u) { hist.push([d, u]); while (hist.length > HIST_N) hist.shift(); }
function clearHist() { hist = []; drawSpark(); }
// drawSpark 只改三条 path 的 d,不动 DOM:速度每秒推一次,重建元素会把动画和焦点都打断。
function drawSpark() {
  const fill = $('#spark-fill');
  if (!fill) return;
  const W = 356, H = 56;
  if (hist.length < 2) { fill.setAttribute('d', ''); $('#spark-d').setAttribute('d', ''); $('#spark-u').setAttribute('d', ''); return; }
  const step = W / (hist.length - 1);
  let max = 1;
  for (const p of hist) max = Math.max(max, p[0], p[1]);
  const pts = k => hist.map((p, i) => (i * step).toFixed(1) + ' ' + (H - 3 - p[k] / max * (H - 9)).toFixed(1));
  const line = a => 'M' + a.join(' L');
  const d = pts(0);
  fill.setAttribute('d', line(d) + ' L' + W + ' ' + H + ' L0 ' + H + ' Z');
  $('#spark-d').setAttribute('d', line(d));
  $('#spark-u').setAttribute('d', line(pts(1)));
}
function renderHome(el) {
  el.innerHTML = `
    <div id="home-banner"></div>
    <div class="hero">
      <div class="power-wrap" id="power-wrap">
        <svg viewBox="0 0 208 208"><defs><linearGradient id="ringGrad" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#6a44f2"/><stop offset=".55" stop-color="#2f8bff"/><stop offset="1" stop-color="#18e3e8"/></linearGradient></defs>
          <circle class="ring-bg" cx="104" cy="104" r="99"/><circle class="ring" cx="104" cy="104" r="99"/></svg>
        <button class="power" id="power"><svg viewBox="0 0 24 24"><path d="M12 3v9"/><path d="M6.3 6.3a8 8 0 1 0 11.4 0"/></svg><b id="power-text"></b></button>
      </div>
      <div class="uptime" id="uptime" hidden><b id="s-time">–</b><span>${t('home.uptime')}</span></div>
      <div class="status-text" id="status"></div>
      <div class="status-sub" id="status-sub"></div>
    </div>
    <button class="idcard" id="id-card">
      <span class="flag none" id="id-flag">${ICON_GLOBE}</span>
      <span class="idmid">
        <span class="idtop"><b id="id-name">–</b><em class="tag" id="id-auto" hidden>${t('pick.auto')}</em><em class="tag warn" id="id-mult" hidden></em></span>
        <span class="idsub" id="id-sub"></span>
      </span>
      <span class="idms" id="id-ms"><b id="s-ping">–</b><span id="id-unit"></span></span>
    </button>
    <div class="speed">
      <div class="speed-row">
        <span class="sp"><span class="k"><i class="sq d"></i>${t('stat.down')}</span><b id="s-down">0 B/s</b></span>
        <span class="sp"><span class="k"><i class="sq u"></i>${t('stat.up')}</span><b id="s-up">0 B/s</b></span>
        <span class="sp end"><span class="k">${t('stat.session')}</span><b id="s-total">0 B</b></span>
      </div>
      <svg class="spark" viewBox="0 0 356 56" preserveAspectRatio="none" aria-hidden="true">
        <defs><linearGradient id="sparkFill" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#2f8bff" stop-opacity=".26"/><stop offset="1" stop-color="#2f8bff" stop-opacity="0"/></linearGradient></defs>
        <path id="spark-fill" fill="url(#sparkFill)" d=""/>
        <path id="spark-d" fill="none" stroke="#2f8bff" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" d=""/>
        <path id="spark-u" fill="none" stroke="#18e3e8" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round" d=""/>
      </svg>
    </div>
    <div class="pickers">
      <button class="picker" id="pk-mode"><span>${t('pick.mode')}</span><b id="pk-mode-v"></b></button>
      <button class="picker" id="pk-prof"><span>${t('pick.profile')}</span><b id="pk-prof-v"></b></button>
      <button class="picker" id="pk-node"><span>${t('pick.node')}</span><b id="pk-node-v"></b><em id="pk-node-ms"></em></button>
    </div>`;
  $('#power').addEventListener('click', togglePower);
  $('#id-card').addEventListener('click', sheetNodes);
  $('#pk-mode').addEventListener('click', sheetMode);
  $('#pk-prof').addEventListener('click', sheetProfiles);
  $('#pk-node').addEventListener('click', sheetNodes);
  updateHome();
  drawSpark();
}
async function togglePower() {
  if (!state || !state.service) { toast(t('svc.down'), 'err'); return; }
  try { if (state.view.state.wanted) await App().Disconnect(); else await App().Connect(); }
  catch (e) { toast(errText(e), 'err'); }
}
// updateHome 状态每 1.5 秒推一次,这里只改文字和 class,不重建任何节点
function updateHome() {
  if (view !== 'home' || !state) return;
  const v = state.view, st = v.state.status, wanted = v.state.wanted;
  const wrap = $('#power-wrap');
  if (!wrap) return;
  const on = st === 'connected' || st === 'degraded';
  wrap.className = 'power-wrap ' + (on ? 'on' : (wanted && st !== 'error') ? 'busy' : (st === 'error' || !state.service) ? 'err' : '');
  $('#power-text').textContent = on ? t('home.disconnect') : wanted ? (st === 'stopping' ? t('home.stopping') : t('home.connecting')) : t('home.connect');

  // 连上了:大字显示时长,状态那两行让给下面的出口卡片;没连上或出错:照旧显示状态与原因
  const err = v.state.error ? (t('code.' + v.state.code) !== 'code.' + v.state.code ? t('code.' + v.state.code) : v.state.error) : '';
  $('#uptime').hidden = !(on && v.uptime);
  $('#s-time').textContent = on && v.uptime ? fmtDuration(v.uptime) : '–';
  const quiet = on && !err;
  $('#status').hidden = quiet;
  $('#status').textContent = state.service ? t('st.' + st) : t('svc.down');
  $('#status-sub').hidden = !err;
  $('#status-sub').textContent = err;

  // 出口卡片:自动选择时显示实际落到的那个节点,底下是这条线路真正的出口地址
  const auto = v.node === 'auto' || !v.node;
  const name = auto ? (v.autoNow || t('node.auto')) : v.node;
  setFlag($('#id-flag'), on || !auto ? geoCode(name) : geoCode(v.node));
  $('#id-name').textContent = cleanName(name);
  $('#id-auto').hidden = !auto;
  const mult = nodeMult(name);
  $('#id-mult').hidden = !mult;
  if (mult) $('#id-mult').textContent = t('node.mult', { n: mult });
  let sub;
  if (!on) sub = t('home.pickNode');
  else if (v.mode === 'direct') sub = t('home.directAll'); // 直连模式的流量根本不经节点,别挂着节点的出口
  else if (v.exitIp) {
    const place = [v.exitLoc ? regionName(v.exitLoc) : '', v.exitCity || ''].filter(Boolean).join(' ');
    sub = t('home.exit') + ' ' + v.exitIp + (place ? ' · ' + place : '');
  } else sub = t('mode.' + v.mode) + ' · ' + t('home.exitWait');
  $('#id-sub').textContent = sub;
  // 一行摆不下的(一级行政区、运营商)挂成悬停提示,不挤在卡片上
  $('#id-sub').title = on && v.exitIp
    ? [v.exitIp, [regionName(v.exitLoc), v.exitRegion, v.exitCity].filter(Boolean).join(' '), v.exitIsp].filter(Boolean).join(' · ')
    : '';
  const ping = v.ping || lastPing; // 服务每次健康检查都会测当前节点;没有就用刚手动测的那次
  $('#id-ms').className = 'idms ' + (on && ping ? msClass(ping) : 'none');
  $('#s-ping').textContent = on && ping ? ping : '–';
  $('#id-unit').textContent = on && ping ? t('common.ms') : '';

  if (!on) { $('#s-down').textContent = '0 B/s'; $('#s-up').textContent = '0 B/s'; }
  $('.spark').hidden = !on; // 没连的时候那块空图看着像没画完
  $('#s-total').textContent = fmtBytes((on ? (state.totalDown || 0) + (state.totalUp || 0) : 0));

  $('#pk-mode-v').textContent = t('mode.' + v.mode);
  $('#pk-prof-v').textContent = v.profile ? v.profile.name : t('prof.empty');
  $('#pk-node-v').textContent = auto ? t('pick.auto') : cleanName(v.node);
  $('#pk-node-ms').textContent = '';
  const b = $('#home-banner');
  const wantBanner = !state.service;
  if (wantBanner !== !!b.firstChild) { // 只有横幅出现/消失时才动 DOM,免得每次推送都把按钮换掉
    b.innerHTML = wantBanner ? `<div class="banner"><span class="grow">${t('banner.svcDown')}</span><button class="btn sm" id="repair">${t('banner.repair')}</button></div>` : '';
    if (wantBanner) $('#repair').addEventListener('click', repairService);
  }
}

// openExternal 打开外部地址:桌面壳与安卓交给系统浏览器,网页面板自己开新标签。
// 只放行 http(s) —— 续费地址来自订阅响应,不能让它塞别的协议进来交给系统去开。
function openExternal(url) {
  const u = String(url || '');
  if (!/^https?:\/\//i.test(u)) return;
  if (window.__web) window.open(u, '_blank', 'noopener');
  else App().OpenURL(u);
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
  const keep = focusKey(body, 'data-id');
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
  refocus(body, 'data-id', keep);
}
function msClass(d) { return d < 0 ? 'bad' : !d ? 'none' : d < 150 ? 'good' : d < 400 ? 'mid' : 'bad'; }
function msText(d, testing) {
  if (testing && !d) return '<span class="spinner"></span>'; // 还没轮到的转圈,测到的立刻亮出来
  if (d < 0) return t('node.fail');
  return d ? d + ' ms' : '–';
}
let nodeTesting = false, nodeFilter = '', nodeRegion = '', nodeFetchTried = false, nodeSig = '';
async function sheetNodes() {
  nodeFilter = ''; nodeRegion = '';
  openSheet(t('sheet.nodes'), `<div class="empty"><span class="spinner"></span></div>`, { action: t('sheet.retest'), onAction: () => testNodes(true) });
  await drawNodes(false);
  testNodes(false);
}
// applyNodeFilter 搜索框与地区标签只是把行藏起来,不重画列表:测速正在跑时列表也不会闪
function applyNodeFilter() {
  const body = $('#sheet-body');
  if (!body) return;
  const q = nodeFilter.toLowerCase();
  body.querySelectorAll('.item[data-name]').forEach(it => {
    const name = it.dataset.name || '';
    if (name === 'auto') { it.hidden = false; return; } // 自动选择永远留在最上面
    it.hidden = !!((q && !name.toLowerCase().includes(q)) || (nodeRegion && it.dataset.geo !== nodeRegion));
  });
  body.querySelectorAll('.chip').forEach(c => c.classList.toggle('on', (c.dataset.geo || '') === nodeRegion));
}
// regionChips 按地区归堆,节点多的排前面;点一下只是筛选,不碰后端
function regionChips(nodes) {
  const n = new Map();
  for (const x of nodes) {
    if (x.name === 'auto') continue;
    const c = geoCode(x.name);
    if (c) n.set(c, (n.get(c) || 0) + 1);
  }
  const list = [...n.entries()].sort((a, b) => b[1] - a[1]).slice(0, 8); // 换行排布,太多会把列表挤下去
  if (list.length < 2) return ''; // 只有一个地区就不用筛了
  return `<div class="chips"><button class="chip on" data-geo="">${t('node.all')}</button>`
    + list.map(([c, k]) => `<button class="chip" data-geo="${c}">${esc(regionName(c))} ${k}</button>`).join('') + '</div>';
}
function nodeRow(n, i, max, testing) {
  if (n.name === 'auto') {
    return `<div class="item auto ${n.current ? 'current' : ''}" data-name="auto"><span class="check"></span>
      <div class="name"><b>${t('node.auto')}</b><span>${n.autoNow ? t('node.now', { n: esc(n.autoNow) }) : t('node.autoDesc')}</span></div>
      <span class="ms ${msClass(n.delay)}"></span></div>`;
  }
  const code = geoCode(n.name), mult = nodeMult(n.name);
  return `<div class="item ${n.current ? 'current' : ''}" data-name="${esc(n.name)}" data-geo="${code}" style="animation-delay:${Math.min(i, 12) * 25}ms"><span class="check"></span>
    ${flagHTML(code, 'sm')}
    <div class="name">
      <div class="ntop"><b>${esc(cleanName(n.name))}</b>${mult ? `<em class="tag warn">${t('node.mult', { n: mult })}</em>` : ''}${n.type ? `<em class="ntype">${esc(n.type)}</em>` : ''}</div>
      <div class="bar"><i style="width:${barWidth(n.delay, max)}"></i></div>
    </div>
    <span class="ms ${msClass(n.delay)}">${msText(n.delay, testing)}</span></div>`;
}
async function drawNodes(testing) {
  let nodes = [];
  try { nodes = await App().GetNodes() || []; } catch (e) { $('#sheet-body').innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
  if (!nodes.length) {
    const p = state && state.view.profile;
    if (p && !nodeFetchTried) { // 有订阅却没有节点 = 还没拉取过(比如刚导入设置),拉一次再画
      nodeFetchTried = true;
      $('#sheet-body').innerHTML = `<div class="empty"><span class="spinner"></span> ${t('node.fetching')}</div>`;
      try { await App().RefreshProfile(p.id); } catch (e) { $('#sheet-body').innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
      return drawNodes(testing);
    }
    $('#sheet-body').innerHTML = `<div class="empty">${p ? t('node.notFetched') : t('prof.empty')}</div>`; return;
  }
  const max = Math.max(1, ...nodes.filter(n => n.delay > 0).map(n => n.delay));
  // 节点集合没变就只改数字,别整块重画:一百来个节点重画一次会先空一下,入场动画还要错峰放完,
  // 滚动位置和筛选框里的字也跟着丢 —— 测速结束那一下看着就是"闪一下白再出来"。
  const sig = nodes.map(n => n.name).join(' ');
  if (sig === nodeSig && patchNodes(nodes, max, testing)) return;
  nodeSig = sig;
  const st = state && state.view.state.status, online = st === 'connected' || st === 'degraded';
  const hint = online ? '' : `<div class="small muted" style="padding:0 4px 8px">${t('node.offlineHint')}</div>`;
  const keep = focusKey($('#sheet-body'), 'data-name');
  $('#sheet-body').innerHTML = hint
    + (nodes.length > 8 ? `<div class="sheet-filter"><input type="text" id="node-filter" placeholder="${t('node.filter')}" value="${esc(nodeFilter)}"></div>` : '')
    + regionChips(nodes)
    + `<div class="list">${nodes.map((n, i) => nodeRow(n, i, max, testing)).join('')}</div>`;
  $('#sheet-body').querySelectorAll('.item').forEach(it => it.addEventListener('click', async () => {
    try { await App().SelectNode(it.dataset.name); closeSheet(); } catch (e) { toast(errText(e), 'err'); }
  }));
  $('#sheet-body').querySelectorAll('.chip').forEach(c => c.addEventListener('click', () => { nodeRegion = c.dataset.geo || ''; applyNodeFilter(); }));
  refocus($('#sheet-body'), 'data-name', keep);
  const f = $('#node-filter');
  if (f) f.addEventListener('input', () => { nodeFilter = f.value.trim(); applyNodeFilter(); });
  applyNodeFilter();
}
// barWidth 延迟条的宽度:没测出来就是 0。条子一直留着,测完只改宽度,CSS 自带的过渡会把它抹开。
function barWidth(delay, max) { return delay > 0 ? Math.max(6, 100 - delay / max * 80) + '%' : '0'; }

// patchNodes 就地更新延迟数字、进度条与当前项标记,不动 DOM 结构;结构对不上就返回 false,交给整块重画。
function patchNodes(nodes, max, testing) {
  const body = $('#sheet-body');
  const items = new Map([...body.querySelectorAll('.item')].map(it => [it.dataset.name, it]));
  if (items.size !== nodes.length) return false;
  for (const n of nodes) {
    const it = items.get(n.name);
    if (!it) return false;
    it.classList.toggle('current', !!n.current);
    const ms = it.querySelector('.ms');
    if (ms) { ms.className = 'ms ' + msClass(n.delay); ms.innerHTML = n.name === 'auto' ? '' : msText(n.delay, testing); }
    const bar = it.querySelector('.bar i');
    if (bar) bar.style.width = barWidth(n.delay, max);
    if (n.name === 'auto') {
      const sub = it.querySelector('.name span');
      if (sub) sub.textContent = n.autoNow ? t('node.now', { n: n.autoNow }) : t('node.autoDesc');
    }
  }
  return true;
}

async function testNodes(force) {
  if (nodeTesting && !force) return;
  nodeTesting = true;
  const act = $('#sheet-action');
  if (act) { act.disabled = true; act.textContent = t('sheet.testing'); }
  // 一边测一边刷:后端是测出一个记一个的,这里每 400 毫秒取一次就能让延迟自上而下一个个冒出来。
  // 原来是等 TestAll 整个返回再画,上百个节点要干等好几秒,那几秒里界面看着像卡住了。
  let live = true;
  const poll = (async () => {
    while (live) {
      await new Promise(r => setTimeout(r, 400));
      if (!live || !$('#sheet').classList.contains('show')) break;
      await drawNodes(true);
    }
  })();
  try { await App().TestAll(); } catch (e) { /* 内核没跑时没法测 */ }
  live = false;
  await poll;
  nodeTesting = false;
  if (act) { act.disabled = false; act.textContent = t('sheet.retest'); }
  if ($('#sheet').classList.contains('show')) await drawNodes(false);
  try { lastPing = await App().TestLatency('proxy'); } catch (e) { lastPing = 0; }
  updateHome();
}

// ---- 订阅管理 ----
// fmtAgo "3 分钟前更新" 这类相对时间;超过一天就写日期,免得算成"29 小时前"
function fmtAgo(ts) {
  if (!ts) return t('prof.never');
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
  if (s < 90) return t('ago.now');
  if (s < 3600) return t('ago.min', { n: Math.floor(s / 60) });
  if (s < 86400) return t('ago.hour', { n: Math.floor(s / 3600) });
  return t('prof.updated', { t: fmtTime(ts) });
}
// regionCount 订阅里一共几个地区:标签就是节点名,认得出地区的去重数一下
function regionCount(p) {
  const set = new Set();
  for (const n of p.tags || []) { const c = geoCode(n); if (c) set.add(c); }
  return set.size;
}
function renderProfiles(el, editId) {
  el.innerHTML = `<div id="prof-form"></div><div id="prof-list"></div>
    <button class="addcard" id="prof-add">
      <span class="addic"><svg viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg></span>
      <span class="addtxt"><b>${t('prof.add')}</b><span>${t('prof.addHint')}</span></span>
    </button>`;
  const showForm = (p) => {
    $('#prof-form').innerHTML = `<div class="card"><div class="field"><label>${t('prof.name')}</label><input type="text" id="pf-name" value="${esc(p ? p.name : '')}"></div>
      <div class="field with-btn"><label>${t('prof.url')}</label><input type="url" id="pf-url" spellcheck="false" value="${esc(p ? p.url : '')}"><button class="btn sm ghost" id="pf-paste">${t('common.paste')}</button></div>
      <div class="row"><button class="btn primary" id="pf-save">${t('prof.save')}</button><button class="btn" id="pf-cancel">${t('prof.cancel')}</button><span id="pf-busy"></span></div></div>`;
    $('#pf-cancel').addEventListener('click', () => { $('#prof-form').innerHTML = ''; });
    $('#pf-paste').addEventListener('click', () => pasteInto('#pf-url'));
    $('#pf-save').addEventListener('click', async () => {
      const b = $('#pf-save'); b.disabled = true; $('#pf-busy').innerHTML = '<span class="spinner"></span>';
      try {
        if (p && p.id) await App().UpdateProfile(p.id, $('#pf-name').value, $('#pf-url').value); else await App().AddProfile($('#pf-name').value, $('#pf-url').value);
        toast(t(p && p.id ? 'prof.saved' : 'prof.added'), 'ok'); $('#prof-form').innerHTML = ''; await load();
      } catch (e) { toast(errText(e), 'err'); }
      b.disabled = false; $('#pf-busy').innerHTML = '';
    });
    setTimeout(() => $(p && p.id ? '#pf-name' : '#pf-url').focus(), 80);
  };
  $('#prof-add').addEventListener('click', () => showForm(null));

  // 续费按钮:面板给了地址才画,没给就什么都不显示 —— 画一个点不出东西的按钮更糟。
  // 到期不到七天或流量用掉九成时换成橙色并写明还剩多少,该催的时候才催。
  const renewBtn = (p, cls) => {
    if (!p.webPage) return '';
    const u = p.usage || {}, used = (u.upload || 0) + (u.download || 0);
    const days = u.expire ? Math.floor((u.expire - Date.now() / 1000) / 86400) : -1;
    const low = (days >= 0 && days <= 7) || (u.total > 0 && used / u.total >= 0.9);
    const txt = !low ? t('prof.renew') : days >= 0 ? t('prof.renewLeft', { n: days }) : t('prof.renewSoon');
    return `<button class="renewbtn ${cls} ${low ? 'warn' : ''}" data-act="renew" data-id="${esc(p.id)}"><svg viewBox="0 0 24 24"><path d="M14 5h5v5"/><path d="M19 5l-7.5 7.5"/><path d="M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4"/></svg>${esc(txt)}</button>`;
  };
  // 当前这条摊开成一张大卡(用量条、节点数、到期、三个动作),其余的收成一列小卡
  const bigCard = p => {
    const u = p.usage || {}, used = (u.upload || 0) + (u.download || 0);
    const pct = u.total ? Math.min(100, used / u.total * 100) : 0;
    const regions = regionCount(p);
    return `<div class="profcard">
      <div class="pc-top"><span class="dot ${p.error ? 'err' : 'on'}"></span><b>${esc(p.name)}</b><span class="pc-ago">${esc(fmtAgo(p.fetchedAt))}</span></div>
      <div class="pc-use"><b>${esc(fmtBytes(used))}</b><span>${u.total ? t('prof.usedOf', { t: fmtBytes(u.total) }) : t('prof.usedFree')}</span>${renewBtn(p, 'lg')}</div>
      <div class="pc-bar"><i style="width:${u.total ? pct.toFixed(1) : 0}%"></i></div>
      <div class="pc-meta">
        <span>${t('prof.nodes', { n: p.nodeCount })}${regions ? ' · ' + t('prof.regions', { n: regions }) : ''}</span>
        <span>${u.expire ? t('prof.expire', { d: fmtDay(u.expire) }) : ''}</span>
      </div>
      ${p.error ? `<div class="pc-err">${esc(p.error)}</div>` : ''}
      <div class="pc-act">
        <button class="btn sm" data-act="refresh" data-id="${esc(p.id)}">${t('prof.refresh')}</button>
        <button class="btn sm" data-act="edit" data-id="${esc(p.id)}">${t('prof.edit')}</button>
        <button class="btn sm icon danger" data-act="del" data-id="${esc(p.id)}" data-name="${esc(p.name)}" title="${t('prof.del')}"><svg viewBox="0 0 24 24"><path d="M4 7h16M9 7V5h6v2M6 7l1 13h10l1-13"/></svg></button>
      </div></div>`;
  };
  // 其余订阅也是完整一张小卡:点整行切换过去,底下四个动作一个不少
  // (改版时这里一度只剩刷新,编辑和删除没了路径)
  const miniCard = p => `<div class="profmini">
    <div class="pm-top" data-act="use" data-id="${esc(p.id)}"><span class="check"></span><b>${esc(p.name)}</b></div>
    <div class="pm-meta"><span>${p.error ? `<em class="bad">${esc(p.error)}</em>` : t('prof.nodes', { n: p.nodeCount }) + ' · ' + esc(fmtAgo(p.fetchedAt))}</span>${renewBtn(p, 'sm')}</div>
    <div class="pm-act">
      <button class="btn sm" data-act="use" data-id="${esc(p.id)}">${t('prof.use')}</button>
      <button class="btn sm" data-act="refresh" data-id="${esc(p.id)}">${t('prof.refresh')}</button>
      <button class="btn sm" data-act="edit" data-id="${esc(p.id)}">${t('prof.edit')}</button>
      <button class="btn sm icon danger" data-act="del" data-id="${esc(p.id)}" data-name="${esc(p.name)}" title="${t('prof.del')}"><svg viewBox="0 0 24 24"><path d="M4 7h16M9 7V5h6v2M6 7l1 13h10l1-13"/></svg></button>
    </div></div>`;
  const load = async () => {
    let list = [];
    try { list = await App().GetProfiles() || []; } catch (e) { $('#prof-list').innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
    if (!list.length) { $('#prof-list').innerHTML = `<div class="empty">${t('prof.empty')}</div>`; return; }
    const cur = list.find(p => p.active) || list[0], rest = list.filter(p => p !== cur);
    $('#prof-list').innerHTML = bigCard(cur)
      + (rest.length ? `<div class="sechead">${t('prof.others')}</div><div class="pmwrap">${rest.map(miniCard).join('')}</div>` : '');
    $('#prof-list').querySelectorAll('[data-act]').forEach(b => b.addEventListener('click', async e => {
      e.stopPropagation();
      const id = b.dataset.id, act = b.dataset.act;
      try {
        if (act === 'renew') { openExternal((list.find(x => x.id === id) || {}).webPage); return; }
        if (act === 'use') { await App().SelectProfile(id); }
        else if (act === 'refresh') { b.disabled = true; b.classList.add('busy'); await App().RefreshProfile(id); toast(t('prof.refreshed'), 'ok'); }
        else if (act === 'edit') { showForm(list.find(x => x.id === id)); return; }
        else if (act === 'del') { if (!await askConfirm(t('prof.delConfirm', { n: b.dataset.name }), { ok: t('prof.del') })) return; await App().RemoveProfile(id); toast(t('prof.deleted'), 'ok'); }
      } catch (err) { toast(errText(err), 'err'); }
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
  // 一组 = 组名 + 一张卡;组名放在卡外面,和别的页的分节标题一个样式
  const group = (title, rows, tag) => rows.trim() ? `<div class="sgroup"><div class="sechead">${title}${tag ? ` <span class="tag">${tag}</span>` : ''}</div><div class="card tight">${rows}</div></div>` : '';
  el.innerHTML = `
    <div class="field with-ic"><svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/></svg><input type="search" id="set-search" placeholder="${t('set.search')}" spellcheck="false"></div>
    <div id="set-nores" class="empty sm" hidden>${t('set.noMatch')}</div>
    ${group(t('set.g.conn'), `
      ${sw('f-tun', t('set.tun'), s.tun, t('set.tunHelp'))}
      <!-- macOS 上只有 gvisor 能用:系统协议栈在 macOS 握不了手,选了也会被改回 gvisor,不如别给选 -->
      ${sel('f-stack', t('set.tunStack'), state.platform === 'darwin' ? 'gvisor' : s.tunStack, state.platform === 'darwin' ? [['gvisor', 'gvisor']] : [['mixed', 'mixed'], ['system', 'system'], ['gvisor', 'gvisor']])}
      ${sw('f-strict', t('set.strict'), s.strictRoute, t('set.strictHelp'))}
      ${sw('f-lan', t('set.lan'), s.lanBypass, t('set.lanHelp'))}
      ${num('f-mixed', t('set.mixed'), s.mixedPort, t('set.mixedHelp'))}
      ${num('f-probe', t('set.probe'), s.probeMinutes, t('set.probeHelp'))}
      ${num('f-update', t('set.update'), s.updateHours)}
    `)}
    ${group(t('set.g.privacy'), `
      ${sw('f-ipv6', t('set.ipv6'), s.ipv6, t('set.ipv6Help'))}
      ${state.platform === 'android' ? '' : `<div class="srow"><div class="lbl">${t('set.nicv6')} <span class="tag warn">${t('set.nicv6Tag')}</span><div>${t('set.nicv6Help')}</div></div><label class="switch"><input type="checkbox" id="f-nicv6" ${s.disableNicIpv6 ? 'checked' : ''}></label></div>`}
    `)}
    ${group(t('set.g.dns'), `
      ${txt('f-rdns', t('set.remoteDns'), s.remoteDns, t('set.remoteDnsHelp'))}
      ${txt('f-ldns', t('set.localDns'), s.localDns, t('set.localDnsHelp'))}
      ${sw('f-fakeip', t('set.fakeip'), s.fakeIp)}
    `)}
    ${group(t('set.g.route'), `
      ${sw('f-ad', t('set.adblock'), s.adBlock)}
      <div class="srow"><div class="lbl">${t('set.rules')}<div>${t('set.rulesHelp', { n: (s.ruleGroups || []).length })}</div></div><button class="btn sm" id="f-rules">${t('set.rulesManage')}</button></div>
      <div class="field" style="margin:8px 0 12px"><label>${t('set.bypass')}</label><textarea id="f-bypass" placeholder="${t('rt.process_name.ph')}">${esc((s.bypassApps || []).join('\n'))}</textarea><span class="help">${t('set.bypassHelp')}</span></div>
    `)}
    ${state.platform === 'linux' ? group(t('set.g.net'), `
      ${sel('f-netmode', t('set.netMode'), s.netMode || 'local', [['local', t('set.netLocal')], ['gateway', t('set.netGateway')]], t('set.netModeHelp'))}
      ${txt('f-lansub', t('set.lanSubnets'), (s.lanSubnets || []).join(', '), t('set.lanSubnetsHelp'))}
      ${txt('f-weblisten', t('set.webListen'), s.webListen || '', t('set.webListenHelp'))}
    `) : ''}
    ${group(t('set.g.logs'), `
      ${sel('f-log', t('set.logLevel'), s.logLevel, [['debug', 'debug'], ['info', 'info'], ['warn', 'warn'], ['error', 'error']])}
      ${num('f-logdays', t('set.logDays'), s.logDays ?? 7, t('set.logDaysHelp'))}
    `)}
    ${group(t('set.g.app'), `
      ${state.platform === 'android' ? `<div class="srow"><div class="lbl">${t('set.autostart')}<div>${t('set.autostartAndroidHelp')}</div></div></div>` : sw('f-autostart', t(state.platform === 'linux' ? 'set.autostartLinux' : 'set.autostart'), auto, t(state.platform === 'linux' ? 'set.autostartLinuxHelp' : 'set.autostartHelp'))}
      ${sel('f-lang', t('set.lang'), LANG, [['zh', '中文'], ['en', 'English']])}
      ${sel('f-theme', t('set.theme'), curTheme(), [['system', t('theme.system')], ['light', t('theme.light')], ['dark', t('theme.dark')]])}
    `, t('set.instant'))}
    <div class="savebar" id="savebar"><div class="note" id="save-note">${t('set.clean')}</div><button class="btn primary" id="save" disabled>${t('set.save')}</button></div>`;
  // 搜索只是把对不上的行藏起来,整组都藏光了连组名一起收掉
  $('#set-search').addEventListener('input', () => {
    const q = $('#set-search').value.trim().toLowerCase();
    let hit = 0;
    el.querySelectorAll('.sgroup').forEach(g => {
      let n = 0;
      g.querySelectorAll('.srow, .field').forEach(r => {
        const ok = !q || r.textContent.toLowerCase().includes(q);
        r.hidden = !ok;
        if (ok) n++;
      });
      g.hidden = !!q && !n;
      hit += n;
    });
    $('#set-nores').hidden = !q || hit > 0;
  });
  const watch = ['f-tun', 'f-stack', 'f-strict', 'f-lan', 'f-mixed', 'f-probe', 'f-update', 'f-rdns', 'f-ldns', 'f-fakeip', 'f-ipv6', 'f-nicv6', 'f-ad', 'f-bypass', 'f-log', 'f-logdays', 'f-netmode', 'f-lansub', 'f-weblisten'].filter(id => $('#' + id));
  const dirty = on => { $('#save').disabled = !on; $('#savebar').classList.toggle('dirty', on); $('#save-note').textContent = on ? t('set.unsaved') + ' · ' + t('set.note') : t('set.clean'); $('#save').textContent = t(on ? 'set.saveChanges' : 'set.save'); };
  watch.forEach(id => ['input', 'change'].forEach(ev => $('#' + id).addEventListener(ev, () => dirty(true))));
  $('#save').addEventListener('click', async () => {
    const n = { ...s, tun: $('#f-tun').checked, tunStack: $('#f-stack').value, strictRoute: $('#f-strict').checked, lanBypass: $('#f-lan').checked,
      mixedPort: Number($('#f-mixed').value), probeMinutes: Number($('#f-probe').value), updateHours: Number($('#f-update').value),
      remoteDns: $('#f-rdns').value.trim(), localDns: $('#f-ldns').value.trim(), fakeIp: $('#f-fakeip').checked, ipv6: $('#f-ipv6').checked, disableNicIpv6: $('#f-nicv6') ? $('#f-nicv6').checked : s.disableNicIpv6, adBlock: $('#f-ad').checked,
      bypassApps: $('#f-bypass').value.split(/\r?\n/).map(x => x.trim()).filter(Boolean), logLevel: $('#f-log').value, logDays: Number($('#f-logdays').value) };
    if ($('#f-netmode')) { n.netMode = $('#f-netmode').value; n.lanSubnets = $('#f-lansub').value.split(/[,，\s]+/).map(x => x.trim()).filter(Boolean); n.webListen = $('#f-weblisten').value.trim(); }
    try { s = await App().SaveSettings(n); dirty(false); toast(t('set.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); }
  });
  const fa = $('#f-autostart'); if (fa) fa.addEventListener('change', async e => { try { await App().SetAutostart(e.target.checked); toast(t('set.saved'), 'ok'); } catch (err) { toast(errText(err), 'err'); e.target.checked = !e.target.checked; } });
  $('#f-lang').addEventListener('change', async e => { LANG = e.target.value; await App().SetLang(LANG); nav('settings'); });
  $('#f-theme').addEventListener('change', e => setTheme(e.target.value));
  $('#f-rules').addEventListener('click', () => nav('rules'));
}


// ---- 路由规则 ----
const OUT_FIXED = ['proxy', 'direct', 'reject', 'auto'];
const RULE_TYPES = ['domain_suffix', 'domain', 'domain_keyword', 'domain_regex', 'ip_cidr', 'port', 'process_name', 'geosite', 'geoip'];
const outLabel = o => OUT_FIXED.includes(o) ? t('out.' + o) : o;
// condChip 一条条件缩成一枚小标签:geosite / geoip 带上类型前缀,其余直接显示值
function condChip(r) {
  const v = String(r.value || '');
  return esc(r.type === 'geosite' || r.type === 'geoip' ? r.type + ':' + v : v);
}
async function renderRules(el) {
  let s;
  try { s = await App().GetSettings(); } catch (e) { el.innerHTML = `<div class="empty">${esc(errText(e))}</div>`; return; }
  const groups = s.ruleGroups || [];
  const save = async next => { try { await App().SaveSettings({ ...s, ruleGroups: next }); toast(t('set.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); } nav('rules'); };
  const dr = Object.assign({ private: 'direct', cn: 'direct', final: 'proxy' }, s.defaultRules || {});
  const drRow = (id, key, hint, val, opts) => `<div class="drow"><div class="dtxt"><b>${t(key)}</b><span>${t(hint)}</span></div>
    <select id="${id}" data-dr="${id.slice(3)}">${opts.map(o => `<option value="${o}" ${o === val ? 'selected' : ''}>${t('out.' + o)}</option>`).join('')}</select></div>`;
  const card = (g, i) => {
    const rs = g.rules || [], shown = rs.slice(0, 3);
    return `<div class="rgcard ${g.enabled ? '' : 'off'}" data-i="${i}">
      <div class="rg-head">
        <div class="rg-name"><b>${esc(g.name)}</b><em class="tag ${g.enabled ? 'brand' : ''}">${g.enabled ? t('rules.goes', { o: outLabel(g.outbound) }) : t('rules.disabled')}</em></div>
        <label class="switch"><input type="checkbox" data-act="toggle" data-i="${i}" ${g.enabled ? 'checked' : ''}></label>
      </div>
      <div class="rg-conds">${rs.length ? shown.map(r => `<span class="cchip">${condChip(r)}</span>`).join('') + (rs.length > 3 ? `<span class="cchip more">${t('rules.more', { n: rs.length - 3 })}</span>` : '') : `<span class="cchip more">${t('rules.noCond')}</span>`}</div>
      <div class="rg-act">
        <button class="btn sm" data-act="edit" data-i="${i}">${t('rules.editBtn')}</button>
        <button class="btn sm icon" data-act="up" data-i="${i}" ${i === 0 ? 'disabled' : ''} title="↑"><svg viewBox="0 0 24 24"><path d="M12 19V6M6 12l6-6 6 6"/></svg></button>
        <button class="btn sm icon" data-act="down" data-i="${i}" ${i === groups.length - 1 ? 'disabled' : ''} title="↓"><svg viewBox="0 0 24 24"><path d="M12 5v13M6 12l6 6 6-6"/></svg></button>
        <span class="grow"></span>
        <button class="btn sm danger ghost" data-act="del" data-i="${i}">${t('prof.del')}</button>
      </div></div>`;
  };
  const clean = dr.private === 'direct' && dr.cn === 'direct' && dr.final === 'proxy';
  el.innerHTML = `<p class="pagehint">${t('rules.intro')}</p>
    <div class="sechead">${t('rules.mine')}</div>
    ${groups.length ? `<div class="rgwrap">${groups.map(card).join('')}</div>` : `<div class="empty sm">${t('rules.emptyGroups')}</div>`}
    <button class="addcard" id="rg-add">
      <span class="addic"><svg viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg></span>
      <span class="addtxt"><b>${t('rules.add')}</b><span>${t('rules.addHint')}</span></span>
    </button>
    <div class="sechead row"><span class="grow">${t('rules.default')}${clean ? '' : ` <span class="tag">${t('rules.changed')}</span>`}</span><button class="linkbtn" id="dr-reset">${t('rules.restore')}</button></div>
    <div class="card tight">
      ${drRow('dr-private', 'rules.dPrivate', 'rules.dPrivateHint', dr.private, ['direct', 'proxy', 'reject'])}
      ${drRow('dr-cn', 'rules.dCN', 'rules.dCNHint', dr.cn, ['direct', 'proxy', 'reject'])}
      ${drRow('dr-final', 'rules.dFinal', 'rules.dFinalHint', dr.final, ['proxy', 'direct'])}
    </div>
    <p class="pagehint">${t('rules.defaultHint')}</p>`;
  const saveDR = async next => { try { await App().SaveSettings({ ...s, defaultRules: next }); toast(t('set.saved'), 'ok'); } catch (e) { toast(errText(e), 'err'); } nav('rules'); };
  el.querySelectorAll('select[data-dr]').forEach(sel => sel.addEventListener('change', () => saveDR({ ...dr, [sel.dataset.dr]: sel.value })));
  $('#dr-reset').addEventListener('click', async () => { if (await askConfirm(t('rules.restoreConfirm'), { danger: false, ok: t('rules.restore') })) saveDR({ private: 'direct', cn: 'direct', final: 'proxy' }); });
  $('#rg-add').addEventListener('click', () => nav('ruleEdit', null));
  el.querySelectorAll('[data-act]').forEach(b => b.addEventListener(b.dataset.act === 'toggle' ? 'change' : 'click', async () => {
    const i = Number(b.dataset.i), next = groups.map(g => ({ ...g }));
    switch (b.dataset.act) {
      case 'toggle': next[i].enabled = b.checked; break;
      case 'edit': nav('ruleEdit', groups[i].id); return;
      case 'up': [next[i - 1], next[i]] = [next[i], next[i - 1]]; break;
      case 'down': [next[i + 1], next[i]] = [next[i], next[i + 1]]; break;
      case 'del': if (!await askConfirm(t('rules.delConfirm', { n: groups[i].name }), { ok: t('prof.del') })) return; next.splice(i, 1); break;
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
        const cur = it.querySelector('.name b').textContent, name = await askInput(t('dev.renamePrompt'), cur); if (name === null) return;
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
let connFilter = ''; // '' 全部 / proxy 经代理 / direct 直连
// connDirect 判断这条连接走没走代理:链路是内核给的出站链,直连那条就叫 direct
function connDirect(c) {
  return String(c.chain || '').split('→').map(x => x.trim()).includes('direct');
}
// connAge 起始时间是 ISO 串,换算成"12 分钟"这种好读的时长
function connAge(start) {
  const ms = Date.parse(start);
  if (!ms) return '';
  const s = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (s < 60) return t('conns.sec', { n: s });
  if (s < 3600) return t('conns.min', { n: Math.floor(s / 60) });
  return t('conns.hour', { n: Math.floor(s / 3600) });
}
// connChain 内核给的链路是「实际出站 → 选择组 → …」,第一段才是真正出去的那个节点,
// 最后一段是 proxy / auto 这种组名,显示出来看不出走的是哪儿。
function connChain(c) {
  const parts = String(c.chain || '').split('→').map(x => x.trim()).filter(Boolean);
  return parts.length ? parts[0] : '';
}
let connSig = ''; // 上一次画出来的连接集合(与顺序无关),用来判断能不能只改数字
// patchConns 就地更新流量与时长,不动 DOM 结构;对不上就返回 false 交给整块重画。
function patchConns(shown) {
  const box = $('#conns');
  const items = new Map([...box.querySelectorAll('.item.conn')].map(it => [it.dataset.id, it]));
  if (items.size !== shown.length) return false;
  for (const c of shown) {
    const it = items.get(c.id);
    if (!it) return false;
    const b = it.querySelector('.cn-b');
    if (b) b.textContent = '↓' + fmtBytes(c.down) + ' ↑' + fmtBytes(c.up);
    const sub = it.querySelector('.cn-sub span');
    if (sub) sub.textContent = [c.net || '', connAge(c.start), c.app || ''].filter(Boolean).join(' · ');
  }
  return true;
}
function renderConns(el) {
  connFilter = '';
  connSig = '';
  el.innerHTML = `<div class="tiles">
      <div class="tile"><span>${t('conns.active')}</span><b id="cn-all">0</b></div>
      <div class="tile"><span>${t('conns.viaProxy')}</span><b id="cn-proxy" class="brand">0</b></div>
      <div class="tile"><span>${t('conns.direct')}</span><b id="cn-direct" class="dim">0</b></div>
    </div>
    <div class="chips" id="conn-chips">
      <button class="chip on" data-f="">${t('node.all')}</button>
      <button class="chip" data-f="proxy">${t('conns.viaProxy')}</button>
      <button class="chip" data-f="direct">${t('conns.direct')}</button>
    </div>
    <div id="conns"></div>`;
  $('#conn-chips').querySelectorAll('.chip').forEach(c => c.addEventListener('click', () => {
    connFilter = c.dataset.f || '';
    connSig = ''; // 换了筛选就重画,不然会拿旧集合去对
    $('#conn-chips').querySelectorAll('.chip').forEach(x => x.classList.toggle('on', x === c));
    load();
  }));
  const load = async () => {
    const box = $('#conns'); if (!box) return;
    let list;
    try { list = await App().GetConnections() || []; }
    catch (e) { box.innerHTML = `<div class="empty">${esc(state && (state.view.state.status === 'connected') ? errText(e) : t('conns.needCore'))}</div>`; return; }
    const direct = list.filter(connDirect).length;
    const set = (id, n) => { const x = $(id); if (x) x.textContent = n; };
    set('#cn-all', list.length); set('#cn-proxy', list.length - direct); set('#cn-direct', direct);
    // 按流量从大到小排,想看的那几条总在最上面
    const shown = list.filter(c => !connFilter || (connFilter === 'direct') === connDirect(c))
      .sort((a, b) => (b.down + b.up) - (a.down + a.up));
    if (!shown.length) { connSig = ''; box.innerHTML = `<div class="empty">${t(list.length ? 'conns.noneHere' : 'conns.empty')}</div>`; return; }
    // 集合没变就只改数字:两秒一轮的整块重画会让列表周期性闪一下(行上有入场动画),
    // 还会把滚动位置和选中的文字弄丢。集合变了才重画,那时才重新按流量排序 ——
    // 流量每两秒都在涨,跟着排的话行序会一直跳。
    const sig = shown.map(c => c.id).sort().join(',');
    if (sig === connSig && patchConns(shown)) return;
    connSig = sig;
    box.innerHTML = `<div class="list">${shown.map(c => {
      const d = connDirect(c), chain = connChain(c);
      return `<div class="item conn" data-id="${esc(c.id)}"><div class="name">
        <div class="cn-top"><span class="dot ${d ? '' : 'on2'}"></span><b>${esc(c.host)}</b><span class="cn-b">↓${fmtBytes(c.down)} ↑${fmtBytes(c.up)}</span></div>
        <div class="cn-sub">${d ? `<em class="tag">${t('conns.direct')}</em>` : `<em class="tag brand">${esc(chain)}</em>`}<span>${[esc(c.net || ''), connAge(c.start), esc(c.app || '')].filter(Boolean).join(' · ')}</span></div>
      </div><button class="icon-btn xs muted" data-id="${esc(c.id)}" title="${t('conns.close')}"><svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>`;
    }).join('')}</div>`;
    box.querySelectorAll('button[data-id]').forEach(b => b.addEventListener('click', async () => {
      try { await App().CloseConnection(b.dataset.id); load(); } catch (e) { toast(errText(e), 'err'); }
    }));
  };
  load();
  pageTimer = setInterval(load, 2000);
}

// ---- 日志 ----
// copyText 剪贴板:Wails / WebView 里 navigator.clipboard 不一定给用,退回老办法
async function copyText(s) {
  try { await navigator.clipboard.writeText(s); return true; } catch (e) { /* 换下面那条路 */ }
  try {
    const ta = document.createElement('textarea');
    ta.value = s; ta.style.cssText = 'position:fixed;opacity:0';
    document.body.appendChild(ta); ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  } catch (e) { return false; }
}
// logParse 把一行拆成 时间 / 级别 / 正文 三段,拆不出来就整行当正文
const LOG_LV = /\b(ERROR|ERR|FATAL|PANIC|WARN|WARNING|INFO|DEBUG|TRACE)\b/i;
function logParse(line) {
  const s = String(line);
  const tm = s.match(/\d{2}:\d{2}:\d{2}/);
  const lv = s.slice(0, 64).match(LOG_LV);
  if (!tm && !lv) return { time: '', lv: '', text: s };
  let text = s;
  if (lv) text = s.slice(s.indexOf(lv[0]) + lv[0].length).trim();
  else if (tm) text = s.slice(s.indexOf(tm[0]) + tm[0].length).trim();
  const L = lv ? lv[0].toUpperCase() : '';
  return { time: tm ? tm[0] : '', lv: L === 'WARNING' ? 'WARN' : L === 'ERR' ? 'ERROR' : L, text };
}
function logClass(lv) {
  if (lv === 'ERROR' || lv === 'FATAL' || lv === 'PANIC') return 'err';
  if (lv === 'WARN') return 'warn';
  if (lv === 'DEBUG' || lv === 'TRACE') return 'dim';
  return '';
}
function renderLogs(el) {
  let core = false, paused = false, errOnly = false, raw = [];
  el.innerHTML = `<div class="chips" id="log-chips">
      <button class="chip on" data-src="0">${t('logs.service')}</button>
      <button class="chip" data-src="1">${t('logs.core')}</button>
      <span class="grow"></span>
      <button class="chip" id="log-err">${t('logs.errOnly')}</button>
      <button class="chip" id="log-pause">${t('logs.pause')}</button>
      ${window.__web || window.__android ? '' : `<button class="chip" id="log-open">${t('logs.open')}</button>`}
    </div>
    <div class="logbox" id="log"></div>
    <div class="logact">
      <button class="btn" id="log-copy">${t('logs.copy')}</button>
      <button class="btn primary" id="diag">${t('logs.diag')}</button>
    </div>
    <p class="pagehint center">${t('logs.diagNote')}</p>`;
  const draw = () => {
    const box = $('#log'); if (!box) return;
    const rows = raw.map(logParse).filter(r => !errOnly || logClass(r.lv) === 'err' || logClass(r.lv) === 'warn');
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 10;
    const html = rows.length
      ? rows.map(r => `<div class="lrow ${logClass(r.lv)}"><span class="lt">${esc(r.time)}</span><span class="ll">${esc(r.lv)}</span><span class="lx">${esc(r.text)}</span></div>`).join('')
      : `<div class="lempty">${t(errOnly ? 'logs.noErr' : 'logs.empty')}</div>`;
    if (box.__html === html) return; // 没有新行就什么都别动,免得三秒清一次选中的文字
    box.__html = html;
    box.innerHTML = html;
    if (atBottom) box.scrollTop = box.scrollHeight;
  };
  const load = async () => {
    if (paused) return;
    try { raw = await App().GetLogs(300, core) || []; draw(); }
    catch (e) { const box = $('#log'); if (box) box.innerHTML = `<div class="lempty">${esc(errText(e))}</div>`; }
  };
  $('#log-chips').querySelectorAll('.chip[data-src]').forEach(c => c.addEventListener('click', () => {
    core = c.dataset.src === '1';
    $('#log-chips').querySelectorAll('.chip[data-src]').forEach(x => x.classList.toggle('on', x === c));
    load();
  }));
  $('#log-err').addEventListener('click', () => { errOnly = !errOnly; $('#log-err').classList.toggle('on', errOnly); draw(); });
  $('#log-pause').addEventListener('click', () => {
    paused = !paused;
    $('#log-pause').textContent = t(paused ? 'logs.resume' : 'logs.pause');
    $('#log-pause').classList.toggle('on', paused);
    if (!paused) load();
  });
  const op = $('#log-open'); if (op) op.addEventListener('click', () => App().OpenLogs().catch(e => toast(errText(e), 'err')));
  $('#log-copy').addEventListener('click', async () => {
    const ok = await copyText(raw.join('\n'));
    toast(t(ok ? 'logs.copied' : 'logs.copyFail'), ok ? 'ok' : 'err');
  });
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
  const v = state ? state.view : {}, up = state && state.update;
  const stack = (v.settings && v.settings.tunStack) || '';
  el.innerHTML = `<div class="abrand">
      <svg viewBox="-16 -50 400 400"><defs><linearGradient id="ag" gradientUnits="userSpaceOnUse" x1="70" y1="70" x2="310" y2="270"><stop offset="0" stop-color="#6a44f2"/><stop offset=".55" stop-color="#2f8bff"/><stop offset="1" stop-color="#18e3e8"/></linearGradient></defs><g fill="none" stroke="url(#ag)" stroke-width="44" stroke-linecap="round" stroke-linejoin="round"><path d="M86 248V84l146 124v52"/><path d="M332 78l-88 96"/></g><path fill="url(#ag)" d="M118 192l52 30-52 30z"/></svg>
      <b>${t('app.name')}</b>
      <span>${esc(state ? state.version : '')}${state && state.platform ? ' · ' + esc(state.platform) : ''}</span>
    </div>
    <div class="updcard ${up ? 'has' : ''}" id="upd-card">
      <div class="upd-head">${up ? '<span class="dot err"></span>' : ''}<b id="upd-text">${up ? t('about.found', { v: up.version }) : t('about.updateTitle')}</b><span class="grow"></span><button class="btn sm" id="upd-check">${t('about.check')}</button></div>
      <div class="upd-note">${t('about.updateNote')}</div>
      <div id="upd-body"></div>
    </div>
    <div class="list">
      <div class="item"><span class="dot ${state && state.service ? 'on' : 'err'}"></span>
        <div class="name"><b>${t('about.svc')}</b><span id="about-svc">${esc(svcText(state))}</span></div>
        ${window.__web || window.__android ? '' : `<button class="btn sm" id="repair">${t('about.repair')}</button>`}</div>
      <div class="item"><div class="name"><b>${t('about.kernel')}</b><span>sing-box${stack ? ' · ' + esc(stack) : ''}${v.version ? ' · ' + t('about.svcVer', { v: v.version }) : ''}</span></div></div>
    </div>
    <div class="list">
      <div class="item" id="diag"><svg class="rowic" viewBox="0 0 24 24"><path d="M6 3h9l5 5v13H6z"/><path d="M14 3v6h6M9 13h6M9 17h6"/></svg>
        <div class="name"><b>${t('about.diag')}</b><span>${t('logs.diagNote')}</span></div>
        <svg class="rowgo" viewBox="0 0 24 24"><path d="M9 5l7 7-7 7"/></svg></div>
      <div class="item" id="repo"><svg class="rowic" viewBox="0 0 24 24"><path d="M4 6h16v12H4z"/><path d="M4 8l8 5 8-5"/></svg>
        <div class="name"><b>${t('about.repo')}</b><span>github.com/Maoyangui/godusevpn</span></div>
        <svg class="rowgo" viewBox="0 0 24 24"><path d="M9 5l7 7-7 7"/></svg></div>
      ${window.__web
      ? `<div class="item" id="webpw"><svg class="rowic" viewBox="0 0 24 24"><rect x="4" y="10" width="16" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>
          <div class="name"><b>${t('about.web')}</b><span>${t('about.webHelp')}</span></div>
          <svg class="rowgo" viewBox="0 0 24 24"><path d="M9 5l7 7-7 7"/></svg></div>`
      : `<div class="item danger" id="quit"><svg class="rowic" viewBox="0 0 24 24"><path d="M12 3v9"/><path d="M6.3 6.8a8 8 0 1 0 11.4 0"/></svg>
          <div class="name"><b>${t('about.quit')}</b><span>${t('about.quitHelp')}</span></div></div>`}
    </div>
    <p class="pagehint center">${t('about.license')}</p>`;
  const showUpdate = rel => {
    $('#upd-card').classList.toggle('has', !!rel);
    $('#upd-text').textContent = rel ? t('about.found', { v: rel.version }) : t('about.latest');
    $('#upd-body').innerHTML = rel ? `<button class="btn primary block" id="upd-go">${t('about.update')}</button><div class="progress" id="upd-prog" style="margin-top:10px" hidden><i></i></div>` : '';
    if (rel) $('#upd-go').addEventListener('click', async () => {
      const b = $('#upd-go'); b.disabled = true; $('#upd-prog').hidden = false;
      try { await App().ApplyUpdate(); $('#upd-text').textContent = t('about.installing'); }
      catch (e) { toast(errText(e), 'err'); b.disabled = false; }
    });
  };
  if (up) showUpdate(up);
  $('#upd-check').addEventListener('click', async () => {
    const b = $('#upd-check'); b.disabled = true; $('#upd-text').textContent = t('about.checking');
    try { showUpdate(await App().CheckUpdate()); } catch (e) { $('#upd-text').textContent = errText(e); }
    b.disabled = false;
  });
  const rp = $('#repair'); if (rp) rp.addEventListener('click', async e => { e.stopPropagation(); repairService(); });
  $('#diag').addEventListener('click', exportDiag);
  const q = $('#quit'); if (q) q.addEventListener('click', () => App().QuitApp());
  const wp = $('#webpw'); if (wp) wp.addEventListener('click', async () => {
    const pw = await askInput(t('about.webPwPrompt'), '', { password: true }); if (pw === null) return;
    try { await App().SetWebPassword(pw); toast(t('set.saved'), 'ok'); if (pw) setTimeout(() => location.reload(), 800); } catch (e) { toast(errText(e), 'err'); }
  });
  $('#repo').addEventListener('click', () => openExternal('https://github.com/Maoyangui/godusevpn'));
}

// ---- 浏览器面板:登录 ----
function showLogin() {
  const box = $('#login'); box.hidden = false;
  $('#login-title').textContent = t('login.title'); $('#login-go').textContent = t('login.go');
  setTimeout(() => $('#login-pw').focus(), 100);
}
// 对外监听但还没设密码:外来访问只给一句提示,不给入口
function showNeedPassword() {
  const box = $('#login'); box.hidden = false;
  $('#login-title').textContent = t('login.needPw');
  $('#login-pw').hidden = true; $('#login-go').hidden = true;
}
function initLogin() {
  window.addEventListener('web-needpw', showNeedPassword);
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
  if (window.__android) {
    PLATFORM = 'android';
    $('#app').classList.add('web', 'android'); document.body.classList.add('android');
    try { if (window.GodusevpnBridge.isTV()) document.body.classList.add('tv'); } catch (e) { /* 旧壳没有这个方法 */ }
  }
  try { state = await App().GetState(); }
  catch (e) {
    const m = String(e && e.message);
    if (m === 'AUTH_REQUIRED') { showLogin(); return; }
    if (m === 'NEED_PASSWORD') { showNeedPassword(); return; }
    state = { service: false, svcState: 'down', view: { state: { status: 'disconnected' }, profiles: [], nodes: [] }, lang: 'zh', theme: 'system', version: '' };
  }
  // macOS 的原生窗口没有标题栏,红绿灯按钮浮在左上角,给顶栏左边让出位置(网页面板和安卓壳里没有这回事)
  if (!window.__web && !window.__android && state.platform === 'darwin') document.body.classList.add('mac');
  LANG = state.lang || 'zh';
  applyTheme(state.theme);
  updateThemeBtn();
  document.documentElement.lang = LANG === 'en' ? 'en' : 'zh';
  $('#menu-btn').addEventListener('click', openDrawer);
  $('#back-btn').addEventListener('click', () => BACK[view] ? nav(BACK[view]) : navHome());
  $('#drawer-backdrop').addEventListener('click', closeDrawer);
  $('#theme-btn').addEventListener('click', () => setTheme(THEME_NEXT[curTheme()] || 'system'));
  $('#drawer-upd').addEventListener('click', () => { closeDrawer(); nav('about'); });
  $('#sheet-backdrop').addEventListener('click', closeSheet);
  window.runtime.EventsOn('nav', name => { closeDrawer(); closeSheet(); if (PAGES[name]) nav(name); });
  $('#min-btn').addEventListener('click', () => App().Minimize());
  $('#close-btn').addEventListener('click', () => App().HideWindow());
  document.addEventListener('keydown', e => { if (e.key === 'Escape') { if ($('#dialog')) closeDialog(false); else { closeSheet(); closeDrawer(); } } });
  $('#svc-text').textContent = svcText(state);
  navHome();
  window.runtime.EventsOn('state', st => {
    const langChanged = (st.lang || 'zh') !== LANG, hadProfiles = state && state.view.profiles && state.view.profiles.length;
    state = st; LANG = st.lang || 'zh';
    if (st.view.state.status !== 'connected' && st.view.state.status !== 'degraded' && hist.length) clearHist();
    if (themeWish !== null && st.theme === themeWish) themeWish = null;
    applyTheme(curTheme());
    updateThemeBtn();
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
    state.totalUp = tr.totalUp || 0; state.totalDown = tr.totalDown || 0;
    pushHist(tr.down, tr.up); // 曲线一直攒着,翻到别的页再回来还是那条线
    if (view === 'home') {
      drawSpark();
      const tot = $('#s-total');
      if (tot) tot.textContent = fmtBytes(state.totalDown + state.totalUp);
    }
  });
  window.runtime.EventsOn('update-progress', p => {
    const bar = $('#upd-prog'); if (!bar) return;
    const pct = p.total > 0 ? Math.round(p.done / p.total * 100) : 0;
    bar.hidden = false; bar.querySelector('i').style.width = pct + '%';
    $('#upd-text').textContent = t('about.downloading', { p: pct });
  });
  // 落地页一键导入:老版本外壳传的是一个地址字符串,新版本传 {url, name}
  window.runtime.EventsOn('import', p => {
    const url = typeof p === 'string' ? p : (p && p.url) || '', name = typeof p === 'string' ? '' : (p && p.name) || '';
    if (!url) return;
    nav('profiles');
    setTimeout(() => {
      const f = $('#prof-add'); if (f) f.click();
      const u = $('#pf-url'); if (u) u.value = url;
      const n = $('#pf-name'); if (n && name) n.value = name;
    }, 350);
  });
}
// ---- 遥控器 / 方向键(Android):WebView 不自带空间导航,按元素位置找下一个焦点;Enter 等于点击;返回键交给 __godBack ----
const FOCUS_SEL = 'button:not([disabled]), input:not([type=hidden]):not([disabled]), select:not([disabled]), textarea:not([disabled]), a, .item';
function focusLayer() {
  if ($('#dialog')) return [$('#dialog')];
  if ($('#sheet').classList.contains('show')) return [$('#sheet')];
  if ($('#drawer').classList.contains('show')) return [$('#drawer')];
  const views = [...$('#stage').querySelectorAll('.view:not(.pop)')];
  return [$('.topbar'), views[views.length - 1]].filter(Boolean);
}
function focusables(layers) {
  const out = [];
  for (const layer of layers) for (const el of layer.querySelectorAll(FOCUS_SEL)) {
    if (el.closest('[hidden]')) continue;
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.height > 0) out.push(el);
  }
  return out;
}
function focusEl(el) {
  // 没有 href 的 a、div 行本来不可聚焦(a 的 tabIndex 属性却读出 0),显式给个 tabindex 才能 focus
  if (!el.hasAttribute('tabindex')) el.setAttribute('tabindex', '0');
  el.focus({ preventScroll: true });
  el.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}
function spatialMove(dir) {
  const layers = focusLayer(), list = focusables(layers);
  if (!list.length) return;
  const cur = document.activeElement;
  if (!cur || cur === document.body || !layers.some(l => l.contains(cur))) { focusEl(list[0]); return; }
  const a = cur.getBoundingClientRect(), ax = (a.left + a.right) / 2, ay = (a.top + a.bottom) / 2;
  let best = null, bestScore = Infinity;
  for (const el of list) {
    if (el === cur || cur.contains(el)) continue;
    const b = el.getBoundingClientRect(), bx = (b.left + b.right) / 2, by = (b.top + b.bottom) / 2;
    let primary, ortho;
    if (dir === 'down') { if (by <= ay) continue; primary = b.top - a.bottom; ortho = Math.abs(bx - ax); }
    else if (dir === 'up') { if (by >= ay) continue; primary = a.top - b.bottom; ortho = Math.abs(bx - ax); }
    else if (dir === 'right') { if (bx <= ax) continue; primary = b.left - a.right; ortho = Math.abs(by - ay); }
    else { if (bx >= ax) continue; primary = a.left - b.right; ortho = Math.abs(by - ay); }
    const score = Math.max(primary, 0) + ortho * 1.6;
    if (score < bestScore) { bestScore = score; best = el; }
  }
  if (best) focusEl(best);
}
function tvFocus() { // 电视上换页 / 开面板后把焦点放到第一个可选项,遥控器才有落点
  if (!document.body.classList.contains('tv')) return;
  setTimeout(() => {
    const layers = focusLayer(), cur = document.activeElement;
    if (cur && cur !== document.body && layers.some(l => l.contains(cur))) return;
    const power = $('#power'); // 首页先落在连接按钮上,其余页面落在第一个可选项
    if (power && layers.some(l => l.contains(power))) focusEl(power); else spatialMove('down');
  }, 120);
}
document.addEventListener('keydown', e => {
  if (!document.body.classList.contains('android')) return;
  const el = document.activeElement, tag = el ? el.tagName : '';
  const editing = (tag === 'INPUT' && el.type !== 'checkbox') || tag === 'TEXTAREA' || tag === 'SELECT';
  const dir = { ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right' }[e.key];
  if (dir) {
    if (editing && (dir === 'left' || dir === 'right' || tag !== 'INPUT')) return; // 输入框里左右移光标;多行框与下拉框上下也归它们
    e.preventDefault(); spatialMove(dir);
  } else if (e.key === 'Enter' && el && el !== document.body) {
    const native = tag === 'BUTTON' || (tag === 'A' && el.href) || editing;
    if (!native) { e.preventDefault(); el.click(); }
  }
});
document.addEventListener('focusin', e => { // 电视上焦点落点写进 logcat(chromium 的 CONSOLE 行),排查遥控器导航用
  if (document.body.classList.contains('tv')) console.log('focus ' + e.target.tagName + '#' + (e.target.id || '') + '.' + (e.target.className || '') + ' ' + (e.target.textContent || '').trim().slice(0, 12));
});
window.__godBack = () => { // 系统返回键:先关对话框与面板抽屉,再退回上一页;首页返回 false 让壳把应用放后台
  if ($('#dialog')) { closeDialog(false); return true; }
  if ($('#sheet').classList.contains('show')) { closeSheet(); return true; }
  if ($('#drawer').classList.contains('show')) { closeDrawer(); return true; }
  if (view && view !== 'home' && view !== 'onboard') { if (BACK[view]) nav(BACK[view]); else navHome(); return true; }
  return false;
};

document.addEventListener('DOMContentLoaded', init);
