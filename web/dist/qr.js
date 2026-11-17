// 订阅二维码:从图片里认(拖进来、粘贴、选图片、手机相册),或者用相机扫(Android 手机)。
// 识别在页面里做(vendor/jsqr.js,用到时才加载),图片和相机画面都不出本机。
// 认出来的文字按「新增订阅」认的那几种写法取出订阅地址(见 parse);界面怎么摆在 app.js。
//
// 这份脚本和 dist 下别的一样,只用 Chrome 66 认识的写法(电视 / 盒子的系统 WebView,见 web/compat_test.go)。
(function () {
  let loading = null;
  // 解码库 257 KB,用到时才装:平时打开界面不用为它多解析这么大一份脚本
  function loadDecoder() {
    if (window.jsQR) return Promise.resolve(window.jsQR);
    if (!loading) {
      loading = new Promise((ok, fail) => {
        const s = document.createElement('script');
        s.src = 'vendor/jsqr.js';
        s.onload = () => { if (window.jsQR) ok(window.jsQR); else { loading = null; fail(new Error('decoder')); } };
        s.onerror = () => { loading = null; fail(new Error('decoder')); };
        document.head.appendChild(s);
      });
    }
    return loading;
  }

  // ---- 认出来的文字 → 订阅地址 ----
  const b64 = s => {
    let v = String(s).replace(/-/g, '+').replace(/_/g, '/').replace(/\s+/g, '');
    while (v.length % 4) v += '=';
    const bin = atob(v);
    try { return decodeURIComponent(bin.split('').map(c => '%' + ('0' + c.charCodeAt(0).toString(16)).slice(-2)).join('')); } catch (e) { return bin; }
  };
  const dec = s => { try { return decodeURIComponent(String(s).replace(/\+/g, ' ')); } catch (e) { return String(s); } };
  const query = q => {
    const out = {};
    String(q || '').split('&').forEach(kv => { const i = kv.indexOf('='); if (i > 0) out[kv.slice(0, i).toLowerCase()] = dec(kv.slice(i + 1)); });
    return out;
  };
  const isHTTP = u => /^https?:\/\/[^\s/?#]+/i.test(u) && !/\s/.test(u);
  const NODE = /^(vless|vmess|trojan|ss|ssr|hysteria2|hy2|hysteria|tuic|anytls|socks|socks5|wireguard):\/\//i;
  // 小火箭的 sub://<base64 的地址>,后面可以带 #名字 或 ?remarks=名字
  function fromSub(rest) {
    let name = '';
    const h = rest.indexOf('#');
    if (h >= 0) { name = dec(rest.slice(h + 1)); rest = rest.slice(0, h); }
    const q = rest.indexOf('?');
    if (q >= 0) { const p = query(rest.slice(q + 1)); name = p.remarks || p.remark || p.name || name; rest = rest.slice(0, q); }
    let url = '';
    try { url = b64(rest).trim(); } catch (e) { return null; }
    return isHTTP(url) ? { kind: 'sub', url, name } : null;
  }
  // parse 二维码里的文字是什么:
  //   { kind: 'sub', url, name }   订阅 —— http(s) 地址本身,或各家客户端的导入链接里带着的那个地址:
  //                                godusevpn://import?url=…&name=…、clash://install-config?url=…&name=…、
  //                                sing-box://import-remote-profile?url=…#名字、sub://<base64>、shadowrocket://add/sub://<base64>
  //   { kind: 'node', links }      节点分享链接(vless:// 这类,可以有几行):不是订阅,去「手动添加节点」
  //   { kind: 'none', text }       都不是
  function parse(text) {
    const raw = String(text || '').trim();
    if (!raw) return { kind: 'none', text: '' };
    const lines = raw.split(/\r?\n/).map(x => x.trim()).filter(Boolean);
    const nodes = lines.filter(x => NODE.test(x));
    if (nodes.length && nodes.length === lines.length) return { kind: 'node', links: nodes };
    const one = lines[0];
    const m = /^([a-z][a-z0-9+.-]*):\/\/(.*)$/i.exec(one);
    if (!m) return { kind: 'none', text: raw };
    const scheme = m[1].toLowerCase(), rest = m[2];
    if (scheme === 'http' || scheme === 'https') return isHTTP(one) ? { kind: 'sub', url: one, name: '' } : { kind: 'none', text: raw };
    if (scheme === 'sub') return fromSub(rest) || { kind: 'none', text: raw };
    if (scheme === 'shadowrocket') {
      const i = rest.toLowerCase().indexOf('sub://');
      return (i >= 0 && fromSub(rest.slice(i + 6))) || { kind: 'none', text: raw };
    }
    if (scheme === 'godusevpn' || scheme === 'clash' || scheme === 'clashmeta' || scheme === 'mihomo' || scheme === 'stash' || scheme === 'sing-box' || scheme === 'singbox' || scheme === 'hiddify' || scheme === 'v2rayn' || scheme === 'nekobox') {
      let name = '', body = rest;
      const h = body.indexOf('#');
      if (h >= 0) { name = dec(body.slice(h + 1)); body = body.slice(0, h); }
      const q = body.indexOf('?');
      const p = q >= 0 ? query(body.slice(q + 1)) : {};
      const url = (p.url || '').trim();
      if (isHTTP(url)) return { kind: 'sub', url, name: (p.name || p.remarks || name || '').trim() };
    }
    return { kind: 'none', text: raw };
  }

  // ---- 从图片里认 ----
  // 画到画布上取像素。透明底的图先铺白;按给的边长上限缩放(也可以放大:很小的截图每个格子不到两个像素时认不出)。
  function pixels(src, w0, h0, side) {
    const k = side / Math.max(w0, h0);
    const w = Math.max(1, Math.round(w0 * k)), h = Math.max(1, Math.round(h0 * k));
    const c = document.createElement('canvas');
    c.width = w; c.height = h;
    const g = c.getContext('2d');
    g.fillStyle = '#fff'; g.fillRect(0, 0, w, h);
    g.imageSmoothingEnabled = k < 1;
    g.drawImage(src, 0, 0, w, h);
    return g.getImageData(0, 0, w, h);
  }
  // 一张图试几种尺寸:二维码占满画面的缩小了反而好认(也快),整屏截图里一小块的要原样甚至放大。认出第一个就停。
  async function decodeSource(src, w0, h0) {
    const jsQR = await loadDecoder();
    const big = Math.max(w0, h0), tried = {};
    const sides = [Math.min(big, 1000), Math.min(big, 1800), 600, Math.min(big * 2, 1400), 400].filter(s => s > 0);
    for (let i = 0; i < sides.length; i++) {
      const side = Math.round(sides[i]);
      if (tried[side]) continue;
      tried[side] = true;
      const d = pixels(src, w0, h0, side);
      const r = jsQR(d.data, d.width, d.height, { inversionAttempts: 'attemptBoth' });
      if (r && r.data) return r.data;
    }
    return '';
  }
  const MAX_FILE = 30 * 1024 * 1024;
  const looksImage = f => !!f && (/^image\//i.test(f.type || '') || (!f.type && /\.(png|jpe?g|gif|webp|bmp)$/i.test(f.name || '')));
  // fromFile 认一个图片文件(拖进来的、粘贴的、选的)。返回认出的文字;不是图片 / 打不开 / 没认出二维码,抛带 code 的错:
  //   notImage 不是图片;tooBig 太大;badImage 图片打不开;noCode 图里没认出二维码;decoder 识别组件没装上
  function fromFile(file) {
    const err = code => Object.assign(new Error(code), { code });
    if (!looksImage(file)) return Promise.reject(err('notImage'));
    if (file.size > MAX_FILE) return Promise.reject(err('tooBig'));
    return new Promise((ok, fail) => {
      const url = URL.createObjectURL(file), img = new Image();
      const done = () => URL.revokeObjectURL(url);
      img.onload = () => {
        decodeSource(img, img.naturalWidth, img.naturalHeight).then(text => { done(); if (text) ok(text); else fail(err('noCode')); }, e => { done(); fail(err(e && e.message === 'decoder' ? 'decoder' : 'badImage')); });
      };
      img.onerror = () => { done(); fail(err('badImage')); };
      img.src = url;
    });
  }
  // imageFrom 拖放 / 粘贴带来的东西里的第一张图片(没有返回 null)。
  function imageFrom(dt) {
    if (!dt) return null;
    const files = dt.files || [];
    for (let i = 0; i < files.length; i++) if (looksImage(files[i])) return files[i];
    const items = dt.items || [];
    for (let i = 0; i < items.length; i++) {
      if (items[i].kind === 'file' && /^image\//i.test(items[i].type || '')) { const f = items[i].getAsFile(); if (f) return f; }
    }
    return null;
  }
  // hasFiles 拖着的是不是文件(拖文字、拖链接的不管)。
  function hasFiles(dt) {
    if (!dt) return false;
    const types = dt.types || [];
    for (let i = 0; i < types.length; i++) if (types[i] === 'Files') return true;
    return false;
  }

  // ---- 用相机扫 ----
  // canScan 这里能不能开相机:要安全上下文里的 mediaDevices(应用自己的页面是;局域网里用 http 打开的面板不是)。
  const canScan = () => !!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia);
  // scan 打开后置相机,把画面放进 video,每隔一小会儿取一帧认一次;认出一个就交给 onText。
  // onText 返回 true = 就是要这个(停);返回 false = 不是要的(比如不是订阅),接着扫。
  // 返回一个停止函数(关相机)。开不了相机时抛带 code 的错:denied 没给权限;noCamera 没有相机;unsupported 这里开不了。
  async function scan(video, onText) {
    const err = (code, e) => Object.assign(new Error(code), { code, cause: e });
    if (!canScan()) throw err('unsupported');
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: false, video: { facingMode: { ideal: 'environment' }, width: { ideal: 1280 }, height: { ideal: 720 } } });
    } catch (e) {
      const n = (e && e.name) || '';
      throw err(n === 'NotAllowedError' || n === 'SecurityError' || n === 'PermissionDeniedError' ? 'denied' : n === 'NotFoundError' || n === 'OverconstrainedError' || n === 'DevicesNotFoundError' ? 'noCamera' : 'unsupported', e);
    }
    let stopped = false, timer = 0, busy = false, last = '', lastAt = 0, n = 0;
    const c = document.createElement('canvas'); // 每一帧都画到同一张画布上
    const stop = () => {
      if (stopped) return;
      stopped = true;
      clearTimeout(timer);
      try { stream.getTracks().forEach(t => t.stop()); } catch (e) { /* 已经停了 */ }
      try { video.srcObject = null; } catch (e) { /* 老内核 */ }
    };
    try {
      video.setAttribute('playsinline', '');
      video.muted = true;
      video.srcObject = stream;
      await video.play();
    } catch (e) { stop(); throw err('unsupported', e); }
    let jsQR;
    try { jsQR = await loadDecoder(); } catch (e) { stop(); throw err('decoder', e); }
    const tick = async () => {
      if (stopped) return;
      if (!busy && video.readyState >= 2 && video.videoWidth > 0) {
        busy = true;
        try {
          // 只认画面中间的方块(取景框那一块):像素少一半,认得更快;放得太远的码本来也该往中间凑
          const vw = video.videoWidth, vh = video.videoHeight, side = Math.min(vw, vh);
          const out = Math.min(side, 640);
          if (c.width !== out) { c.width = out; c.height = out; }
          const g = c.getContext('2d');
          g.drawImage(video, (vw - side) / 2, (vh - side) / 2, side, side, 0, 0, out, out);
          const d = g.getImageData(0, 0, out, out);
          // 平常只按黑码白底认(快);每三帧里有一帧连反色的也试(深色页面上白码黑底的那种)
          const r = jsQR(d.data, out, out, { inversionAttempts: n++ % 3 === 2 ? 'attemptBoth' : 'dontInvert' });
          const now = Date.now();
          if (r && r.data && (r.data !== last || now - lastAt > 3000)) { // 同一个不是订阅的码隔几秒才再报一次
            last = r.data; lastAt = now;
            if (onText(r.data) === true) { stop(); return; }
          }
        } catch (e) { /* 这一帧没取成,下一帧再来 */ }
        busy = false;
      }
      timer = setTimeout(tick, 180);
    };
    tick();
    return stop;
  }

  window.GVQR = { parse, fromFile, imageFrom, hasFiles, canScan, scan, loadDecoder };
})();
