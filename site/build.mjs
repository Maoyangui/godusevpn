// 介绍站 / 文档站 / 在线演示的组装脚本,不依赖任何包。
//
//   node site/build.mjs        →  site/dist/
//
// 站点本身就是 site/ 下的几张手写 HTML,拷过去即可;要做的事只有三件:
//   1. 截图从 docs/screenshots 拷到 assets/shots(README 与站点共用同一批图,不留两份);
//   2. 在线演示 = 客户端那一份 web/dist 原样拷进 demo/app,再把 demo.js 插在 api.js 前面
//      —— api.js 见到 window.go 已经有人注入就直接退出,于是页面接到的是演示数据而不是 HTTP;
//   3. 版本号从 git tag 取一次,填进页面里的 {{version}}。
import { execSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SITE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(SITE, '..');
const DIST = path.join(SITE, 'dist');

const copyDir = (from, to, skip = () => false) => {
  fs.mkdirSync(to, { recursive: true });
  for (const e of fs.readdirSync(from, { withFileTypes: true })) {
    if (skip(e.name)) continue;
    const a = path.join(from, e.name), b = path.join(to, e.name);
    if (e.isDirectory()) copyDir(a, b, skip);
    else fs.copyFileSync(a, b);
  }
};

let version = '';
try {
  version = execSync('git describe --tags --abbrev=0', { cwd: ROOT, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim();
} catch { /* 还没打过 tag */ }

fs.rmSync(DIST, { recursive: true, force: true });
// 站点本体(dist 自己、构建脚本、演示用的那份 demo.js 不进产物根目录)
copyDir(SITE, DIST, n => n === 'dist' || n === 'build.mjs');

// 截图
const shots = path.join(ROOT, 'docs/screenshots');
if (fs.existsSync(shots)) copyDir(shots, path.join(DIST, 'assets/shots'));

// 在线演示:客户端页面原样一份 + 演示数据
const app = path.join(DIST, 'demo/app');
copyDir(path.join(ROOT, 'web/dist'), app);
fs.copyFileSync(path.join(SITE, 'demo/demo.js'), path.join(app, 'demo.js'));
fs.rmSync(path.join(DIST, 'demo/demo.js'), { force: true });
const idx = path.join(app, 'index.html');
let html = fs.readFileSync(idx, 'utf8');
const tag = '<script src="api.js"></script>';
if (!html.includes(tag)) throw new Error('web/dist/index.html 里找不到 api.js 那一行,演示注入不上去');
html = html.replace(tag, '<script src="demo.js"></script>\n' + tag);
fs.writeFileSync(idx, html);

// {{version}}
for (const f of ['index.html', 'docs.html', 'demo/index.html']) {
  const p = path.join(DIST, f);
  if (!fs.existsSync(p)) continue;
  fs.writeFileSync(p, fs.readFileSync(p, 'utf8').replaceAll('{{version}}', version));
}

// Pages 不要 Jekyll 插手(_ 开头的目录会被它吞掉)
fs.writeFileSync(path.join(DIST, '.nojekyll'), '');

// 站内引用逐个查一遍:少拷一张图、路径写错,这里就报出来,不用等上线才发现
const bad = [];
const scan = (dir) => {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const f = path.join(dir, e.name);
    if (e.isDirectory()) { scan(f); continue; }
    if (!e.name.endsWith('.html')) continue;
    const html = fs.readFileSync(f, 'utf8');
    for (const m of html.matchAll(/(?:href|src)="([^"#?]+)"/g)) {
      const u = m[1];
      if (/^(https?:|mailto:|data:|\/\/)/.test(u)) continue;
      let t = u.startsWith('/') ? path.join(DIST, u) : path.join(path.dirname(f), u);
      if (u.endsWith('/')) t = path.join(t, 'index.html');
      if (!fs.existsSync(t)) bad.push(path.relative(DIST, f) + ' → ' + u);
    }
  }
};
scan(DIST);
if (bad.length) {
  console.error('站内引用打不开:\n  ' + bad.join('\n  '));
  process.exit(1);
}

const count = (d) => fs.readdirSync(d, { withFileTypes: true }).reduce((n, e) => n + (e.isDirectory() ? count(path.join(d, e.name)) : 1), 0);
console.log(`site/dist 就绪:${count(DIST)} 个文件${version ? ',版本 ' + version : ''}`);
