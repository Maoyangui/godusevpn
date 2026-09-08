// 浏览器环境(Linux 面板 / Android WebView 之外的纯网页):用 HTTP + SSE 模拟 Wails 注入的 window.go.main.App 与 window.runtime。
// Windows 客户端由 Wails 注入真正的桥,这里检测到就什么都不做。
(() => {
  if (window.go && window.go.main && window.go.main.App) return;
  // Android:Kotlin 注入的桥,事件由宿主经 window.__godEvent 推进来
  if (window.GodusevpnBridge) {
    window.__android = true;
    const listeners = {};
    window.__godEvent = (name, dataJSON) => { let d; try { d = JSON.parse(dataJSON); } catch (x) { d = dataJSON; } (listeners[name] || []).forEach(f => f(d)); };
    // 异步桥:同步桥会把页面线程堵住(测全部节点要好几秒,面板就卡着弹不出来),
    // 所以交给宿主在后台线程跑完再回调 __godResolve;老外壳没有 callAsync 时退回同步。
    const pending = new Map();
    let seq = 0;
    window.__godResolve = (id, payloadJSON) => {
      const p = pending.get(id);
      if (!p) return;
      pending.delete(id);
      let j;
      try { j = JSON.parse(payloadJSON); } catch (e) { p.reject(new Error(String(payloadJSON))); return; }
      if (j && j.error) p.reject(new Error(j.error)); else p.resolve(j ? j.result : null);
    };
    const callSync = (name, args) => new Promise((resolve, reject) => {
      let j;
      try { j = JSON.parse(window.GodusevpnBridge.call(String(name), JSON.stringify(args || []))); } catch (e) { reject(new Error(String(e && e.message || e))); return; }
      if (j.error) reject(new Error(j.error)); else resolve(j.result);
    });
    const call = typeof window.GodusevpnBridge.callAsync !== 'function' ? callSync : (name, args) => new Promise((resolve, reject) => {
      const id = 'r' + (++seq);
      pending.set(id, { resolve, reject });
      try { window.GodusevpnBridge.callAsync(id, String(name), JSON.stringify(args || [])); }
      catch (e) { pending.delete(id); reject(new Error(String(e && e.message || e))); }
    });
    window.go = { main: { App: new Proxy({}, { get: (_, name) => (...args) => call(name, args) }) } };
    window.runtime = { EventsOn: (n, f) => { (listeners[n] = listeners[n] || []).push(f); } };
    return;
  }
  window.__web = true;
  const call = async (name, args) => {
    let r;
    try {
      r = await fetch('/api/' + name, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(args || []), credentials: 'same-origin' });
    } catch (e) { throw new Error('SERVICE_DOWN'); }
    if (r.status === 401) { window.dispatchEvent(new CustomEvent('web-auth')); throw new Error('AUTH_REQUIRED'); }
    if (r.status === 403) { const j = await r.json().catch(() => ({})); if (j.error === 'NEED_PASSWORD') { window.dispatchEvent(new CustomEvent('web-needpw')); throw new Error('NEED_PASSWORD'); } }
    const j = await r.json().catch(() => ({}));
    if (!r.ok || j.error) throw new Error(j.error || ('HTTP ' + r.status));
    return j.result;
  };
  window.go = { main: { App: new Proxy({}, { get: (_, name) => (...args) => call(String(name), args) }) } };
  const listeners = {};
  let es = null;
  const connect = () => {
    es = new EventSource('/api/events');
    for (const n of ['state', 'traffic', 'update-progress', 'nav']) {
      es.addEventListener(n, e => { let d; try { d = JSON.parse(e.data); } catch (x) { d = e.data; } (listeners[n] || []).forEach(f => f(d)); });
    }
    es.onerror = () => { es.close(); es = null; setTimeout(() => { if (!es) connect(); }, 2000); };
  };
  window.runtime = { EventsOn: (n, f) => { (listeners[n] = listeners[n] || []).push(f); if (!es) connect(); } };
  window.__webReconnect = () => { if (es) { es.close(); es = null; } connect(); };
})();
