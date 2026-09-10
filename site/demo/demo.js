// 在线演示用的假后端:界面是客户端里那一份原样,后面这一层把它接到一组编好的数据上。
// 不连任何真实服务,填什么订阅链接都只是走一遍界面流程。
(() => {
  const NODES = [
    { name: '自动选择', tag: 'auto' },
    { name: '香港1-高带宽', type: 'anytls', delay: 97 },
    { name: '香港2-专线 2x', type: 'anytls', delay: 88 },
    { name: '香港3-高带宽', type: 'anytls', delay: 81 },
    { name: '香港4-高带宽', type: 'anytls', delay: -1 },
    { name: '澳门1-高带宽', type: 'anytls', delay: 92 },
    { name: '台湾1-彰化', type: 'anytls', delay: 105 },
    { name: '🇯🇵 日本1-东京', type: 'vless', delay: 132 },
    { name: '🇯🇵 日本2-大阪 0.5倍', type: 'vless', delay: 141 },
    { name: 'JP-Tokyo-03', type: 'vless', delay: 155 },
    { name: '韩国1-首尔', type: 'vless', delay: 143 },
    { name: '新加坡1', type: 'anytls', delay: 120 },
    { name: '新加坡2-专线 x3', type: 'anytls', delay: 118 },
    { name: '西班牙3-高带宽', type: 'anytls', delay: 157 },
    { name: 'US-Los Angeles 01', type: 'trojan', delay: 210 },
    { name: '美国2-圣何塞', type: 'trojan', delay: 233 },
    { name: '英国-伦敦1', type: 'vless', delay: 248 },
    { name: '德国1-法兰克福', type: 'vless', delay: 261 },
    { name: '法国1-巴黎', type: 'vless', delay: 254 },
    { name: '荷兰1-阿姆斯特丹', type: 'vless', delay: 239 },
    { name: '瑞士1-苏黎世', type: 'vless', delay: 266 },
    { name: '加拿大1-多伦多', type: 'vless', delay: 231 },
    { name: '澳大利亚1-悉尼', type: 'vless', delay: 288 },
    { name: '土耳其1', type: 'vless', delay: 302 },
    { name: '印度1-孟买', type: 'vless', delay: 197 },
    { name: '巴西1-圣保罗', type: 'vless', delay: 340 },
  ].filter(n => n.tag !== 'auto');
  // 每个节点编一组像样的出口信息:演示里换节点,首页那一行也跟着变
  const EXIT = {
    '香港1-高带宽': ['119.28.4.71', 'HK', 'Hong Kong', 'Central and Western', 'Tencent Cloud'],
    '香港2-专线 2x': ['119.28.9.130', 'HK', 'Hong Kong', 'Central and Western', 'Tencent Cloud'],
    '香港3-高带宽': ['154.211.7.19', 'HK', 'Hong Kong', 'Kwun Tong', 'CTG Server'],
    '澳门1-高带宽': ['202.175.6.44', 'MO', 'Macao', 'Macau', 'CTM'],
    '台湾1-彰化': ['61.222.18.90', 'TW', 'Changhua', 'Taiwan', 'Chunghwa Telecom'],
    '🇯🇵 日本1-东京': ['133.242.18.6', 'JP', 'Tokyo', 'Tokyo', 'SAKURA Internet'],
    '🇯🇵 日本2-大阪 0.5倍': ['160.16.72.11', 'JP', 'Osaka', 'Osaka', 'SAKURA Internet'],
    'JP-Tokyo-03': ['45.32.42.7', 'JP', 'Tokyo', 'Tokyo', 'Vultr'],
    '韩国1-首尔': ['141.164.34.8', 'KR', 'Seoul', 'Seoul', 'Vultr'],
    '新加坡1': ['139.180.130.22', 'SG', 'Singapore', 'Singapore', 'Vultr'],
    '新加坡2-专线 x3': ['128.199.90.14', 'SG', 'Singapore', 'Singapore', 'DigitalOcean'],
    '西班牙3-高带宽': ['104.28.196.19', 'ES', 'Zaragoza', 'Aragon', 'Cloudflare, Inc.'],
    'US-Los Angeles 01': ['104.244.76.13', 'US', 'Los Angeles', 'California', 'FranTech'],
    '美国2-圣何塞': ['66.42.98.51', 'US', 'San Jose', 'California', 'Vultr'],
    '英国-伦敦1': ['134.209.20.7', 'GB', 'London', 'England', 'DigitalOcean'],
    '德国1-法兰克福': ['116.202.14.9', 'DE', 'Falkenstein', 'Saxony', 'Hetzner'],
    '法国1-巴黎': ['51.15.44.2', 'FR', 'Paris', 'Île-de-France', 'Scaleway'],
    '荷兰1-阿姆斯特丹': ['95.179.130.6', 'NL', 'Amsterdam', 'North Holland', 'Vultr'],
    '瑞士1-苏黎世': ['5.102.150.3', 'CH', 'Zurich', 'Zurich', 'Init7'],
    '加拿大1-多伦多': ['149.248.50.9', 'CA', 'Toronto', 'Ontario', 'Vultr'],
    '澳大利亚1-悉尼': ['45.63.19.4', 'AU', 'Sydney', 'New South Wales', 'Vultr'],
    '土耳其1': ['185.125.190.8', 'TR', 'Istanbul', 'Istanbul', 'Hostinger'],
    '印度1-孟买': ['139.84.130.5', 'IN', 'Mumbai', 'Maharashtra', 'Vultr'],
    '巴西1-圣保罗': ['216.238.99.7', 'BR', 'Sao Paulo', 'Sao Paulo', 'Vultr'],
  };

  let settings = {
    tun: true, tunStack: 'mixed', strictRoute: true, lanBypass: true, mixedPort: 2080,
    probeMinutes: 3, updateHours: 12, remoteDns: 'https://1.1.1.1/dns-query', localDns: 'https://223.5.5.5/dns-query',
    fakeIp: true, ipv6: false, disableNicIpv6: true, adBlock: false, logLevel: 'info', logDays: 7,
    netMode: 'local', lanSubnets: [], webListen: '127.0.0.1:9800', bypassApps: ['steam.exe'],
    mode: 'rule', selected: '香港3-高带宽',
    ruleGroups: [
      { id: 'g1', name: '流媒体', outbound: 'proxy', enabled: true, rules: [{ type: 'domain_suffix', value: 'netflix.com' }, { type: 'domain_suffix', value: 'disneyplus.com' }, { type: 'geosite', value: 'youtube' }] },
      { id: 'g2', name: '公司内网', outbound: 'direct', enabled: true, rules: [{ type: 'domain_suffix', value: 'corp.example.com' }, { type: 'ip_cidr', value: '10.8.0.0/16' }] },
    ],
    defaultRules: { private: 'direct', cn: 'direct', final: 'proxy' },
  };
  const view = {
    state: { status: 'connected', since: 0, wanted: true, retries: 0 },
    mode: 'rule', node: '香港3-高带宽', autoNow: '香港3-高带宽',
    nodes: NODES.map(n => n.name), ping: 81,
    exitIp: '', exitLoc: '', exitCity: '', exitRegion: '', exitIsp: '',
    uptime: 4127,
    profile: null, profiles: [], settings,
  };
  const PROFILE = {
    id: 'p1', name: '演示订阅', url: 'https://panel.example.com/sub/demo', active: true,
    title: '演示订阅', fetchedAt: Math.floor(Date.now() / 1000) - 3600, nodeCount: NODES.length, tags: NODES.map(n => n.name),
    usage: { upload: 4.2e9, download: 6.1e10, total: 2e11, expire: Math.floor(Date.now() / 1000) + 86400 * 46 },
  };
  view.profile = PROFILE; view.profiles = [PROFILE];
  const applyExit = () => {
    const name = view.node === 'auto' ? view.autoNow : view.node;
    const e = EXIT[name];
    if (!e || view.state.status !== 'connected') { view.exitIp = view.exitLoc = view.exitCity = view.exitRegion = view.exitIsp = ''; return; }
    [view.exitIp, view.exitLoc, view.exitCity, view.exitRegion, view.exitIsp] = e;
  };
  applyExit();

  const logs = [
    '服务启动,版本 0.6.4(演示)',
    '订阅「演示订阅」拉取成功,' + NODES.length + ' 个节点',
    '内核启动,TUN=godusevpn 协议栈=mixed 严格路由=开',
    '停用网卡 IPv6:以太网、WLAN(断开时还原)',
  ];
  const stamp = () => new Date().toLocaleString('zh-CN', { hour12: false });
  const log = s => { logs.push(s); if (logs.length > 200) logs.shift(); };
  log('已连接 香港3-高带宽 81 ms');

  const HOSTS = [
    ['claude.ai', 'chrome.exe'], ['api.anthropic.com', 'chrome.exe'], ['ws.chatgpt.com', 'chrome.exe'],
    ['tile.openstreetmap.org', 'chrome.exe'], ['github.com', 'chrome.exe'], ['registry.npmjs.org', 'node.exe'],
    ['steamcdn-a.akamaihd.net', 'steam.exe'], ['weixin.qq.com', 'WeChat.exe'], ['update.microsoft.com', 'svchost.exe'],
  ];
  const conns = HOSTS.map((h, i) => ({
    id: 'c' + i, host: h[0], net: 'tcp', app: h[1],
    chain: /qq|microsoft/.test(h[0]) ? 'direct' : view.node + ' → proxy',
    rule: /qq|microsoft/.test(h[0]) ? 'geosite-cn' : (i % 3 ? 'final' : 'geosite'),
    up: 0, down: 0, start: new Date(Date.now() - (i + 1) * 90000).toISOString(),
  }));

  let up = 0, down = 0, totalUp = 2.14e8, totalDown = 2.25e9;
  let st = {
    service: true, svcState: 'running', view, up, down, totalUp, totalDown,
    lang: 'zh', theme: 'system', version: '0.6.4', platform: 'demo', web: false,
  };
  const L = {};
  const emit = (n, d) => (L[n] || []).forEach(f => { try { f(d); } catch (e) { /* 界面自己的事 */ } });
  const push = () => { st.up = up; st.down = down; st.totalUp = totalUp; st.totalDown = totalDown; emit('state', st); };
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  const on = () => view.state.status === 'connected' || view.state.status === 'degraded';

  async function connect() {
    if (on()) return;
    view.state.wanted = true;
    view.state.status = 'preparing'; push(); await sleep(450);
    view.state.status = 'starting'; push(); await sleep(700);
    view.state.status = 'connected'; view.uptime = 0;
    const n = NODES.find(x => x.name === (view.node === 'auto' ? view.autoNow : view.node));
    view.ping = n && n.delay > 0 ? n.delay : 96;
    applyExit();
    log('已连接 ' + (view.node === 'auto' ? view.autoNow : view.node) + ' ' + view.ping + ' ms');
    if (view.exitIp) log('出口 ' + view.exitIp + ' · ' + view.exitLoc + ' ' + view.exitCity + ' · ' + view.exitIsp);
    push();
  }
  async function disconnect() {
    if (!view.state.wanted) return;
    view.state.wanted = false;
    view.state.status = 'stopping'; push(); await sleep(500);
    view.state.status = 'disconnected'; view.uptime = 0; view.ping = 0;
    up = down = 0; totalUp = totalDown = 0;
    applyExit(); log('已断开,网卡 IPv6 已还原'); push();
  }

  const api = {
    GetState: async () => st,
    GetNodes: async () => {
      const cur = view.node;
      return [{ name: 'auto', type: '', delay: 0, current: cur === 'auto', autoNow: view.autoNow }]
        .concat(NODES.map(n => ({ name: n.name, type: n.type, delay: n.delay, current: n.name === cur })));
    },
    GetProfiles: async () => [PROFILE],
    SelectProfile: async () => { push(); },
    RefreshProfile: async () => { await sleep(600); PROFILE.fetchedAt = Math.floor(Date.now() / 1000); log('订阅已刷新'); push(); },
    AddProfile: async () => { throw new Error('演示里不连真实订阅,这一步只走界面'); },
    UpdateProfile: async () => { throw new Error('演示里不连真实订阅,这一步只走界面'); },
    RemoveProfile: async () => { throw new Error('演示里只有这一条订阅'); },
    SelectNode: async n => {
      view.node = n;
      settings.selected = n === 'auto' ? '' : n;
      const name = n === 'auto' ? view.autoNow : n;
      const nn = NODES.find(x => x.name === name);
      view.ping = nn && nn.delay > 0 ? nn.delay : 0;
      applyExit();
      for (const c of conns) if (c.chain !== 'direct') c.chain = name + ' → proxy';
      log('切换节点 → ' + name);
      push();
    },
    SetMode: async m => { view.mode = m; settings.mode = m; log('模式 → ' + m); push(); },
    Connect: connect,
    Disconnect: disconnect,
    TestAll: async () => {
      await sleep(1100);
      for (const n of NODES) if (n.delay > 0) n.delay = Math.max(38, n.delay + (Math.random() * 46 - 23 | 0));
      const best = NODES.filter(n => n.delay > 0).sort((a, b) => a.delay - b.delay)[0];
      if (best) view.autoNow = best.name;
      if (view.node === 'auto') { view.ping = best.delay; applyExit(); }
      log('全部节点测速完成,最快 ' + view.autoNow);
      push();
    },
    TestLatency: async () => view.ping || 0,
    GetSettings: async () => JSON.parse(JSON.stringify(settings)),
    SaveSettings: async s => { settings = Object.assign(settings, s); view.settings = settings; log('设置已保存'); push(); return JSON.parse(JSON.stringify(settings)); },
    GetAutostart: async () => true,
    SetAutostart: async () => null,
    GetConnections: async () => (on() ? conns.map(c => ({ ...c })) : []),
    CloseConnection: async id => { const i = conns.findIndex(c => c.id === id); if (i >= 0) conns.splice(i, 1); },
    GetLogs: async (n, core) => logs.slice(-(n || 200)).map(x => stamp() + (core ? ' INFO  ' : ' ') + x),
    GetDevices: async () => [],
    ExportDiag: async () => { throw new Error('演示里没有真实日志可导'); },
    RepairService: async () => { log('服务已修复'); push(); },
    CheckUpdate: async () => { await sleep(500); return null; },
    PokeUpdate: async () => null,
    ApplyUpdate: async () => { throw new Error('演示里不做升级'); },
    SetLang: async v => { st.lang = v; push(); },
    SetTheme: async v => { st.theme = v; push(); },
    ReadClipboard: async () => '',
    SetWebPassword: async () => { throw new Error('演示里没有面板密码'); },
    Minimize: async () => null, HideWindow: async () => null, OpenLogs: async () => null,
    OpenURL: async u => { window.open(u, '_blank', 'noopener'); },
    QuitApp: async () => { throw new Error('演示里退不了'); },
  };
  window.go = { main: { App: new Proxy({}, { get: (_, n) => (...a) => (api[n] ? api[n](...a) : Promise.resolve(null)) }) } };
  window.runtime = { EventsOn: (n, f) => { (L[n] = L[n] || []).push(f); } };

  // 速度流:连上就每秒推一条,曲线自己会长出来
  let t = 0;
  setInterval(() => {
    if (!on()) { up = down = 0; return; }
    t++;
    view.uptime++;
    down = Math.max(4e4, 1.6e6 + Math.sin(t / 7) * 1.1e6 + Math.sin(t / 2.3) * 4e5 + Math.random() * 6e5 | 0);
    up = Math.max(8e3, 1.4e5 + Math.sin(t / 5) * 7e4 + Math.random() * 5e4 | 0);
    totalDown += down; totalUp += up;
    for (const c of conns) { c.down += Math.random() * down / conns.length | 0; c.up += Math.random() * up / conns.length | 0; }
    emit('traffic', { up, down, totalUp, totalDown });
  }, 1000);
  setInterval(push, 1500);
  // 一进来先把曲线灌满,不用干等一分钟
  setTimeout(() => { for (let i = 0; i < 60; i++) { const k = i / 59; emit('traffic', { down: 1.4e6 + Math.sin(k * 13) * 1e6 + Math.random() * 5e5 | 0, up: 1.2e5 + Math.sin(k * 8) * 6e4 + Math.random() * 3e4 | 0, totalDown, totalUp }); } }, 400);
})();
