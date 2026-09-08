// 浏览器环境(Linux 面板 / Android WebView 之外的纯网页):用 HTTP + SSE 模拟 Wails 注入的 window.go.main.App 与 window.runtime。
// Windows 客户端由 Wails 注入真正的桥,这里检测到就什么都不做。
(() => {
  if (window.go && window.go.main && window.go.main.App) return;
  window.__web = true;
  const call = async (name, args) => {
    let r;
    try {
      r = await fetch('/api/' + name, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(args || []), credentials: 'same-origin' });
    } catch (e) { throw new Error('SERVICE_DOWN'); }
    if (r.status === 401) { window.dispatchEvent(new CustomEvent('web-auth')); throw new Error('AUTH_REQUIRED'); }
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
