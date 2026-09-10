<p align="center"><img src="https://raw.githubusercontent.com/Maoyangui/godusevpn/master/brand/logo.svg" width="110" alt="佛跳墙"></p>
<h1 align="center">佛跳墙 · godusevpn</h1>
<p align="center"><a href="https://github.com/Maoyangui/m-ui">m-ui</a> 面板的客户端 · Windows / macOS / Linux / Android 四端一套界面 · 内嵌 sing-box · TUN 全机接管 · 默认关掉 IPv6</p>
<p align="center"><a href="https://maoyangui.github.io/godusevpn/">介绍站</a> · <a href="https://maoyangui.github.io/godusevpn/demo/">在线演示</a> · <a href="https://github.com/Maoyangui/godusevpn/releases">下载</a> · <a href="https://github.com/Maoyangui/godusevpn">仓库</a></p>

这里是**参考手册**:文件放在哪、每个设置项是什么意思、命令行怎么用、出了问题从哪查。
只想快点用起来的话,[介绍站的文档页](https://maoyangui.github.io/godusevpn/docs.html)更短。

<p align="center">
  <img src="https://raw.githubusercontent.com/Maoyangui/godusevpn/master/docs/screenshots/home.png" width="230" alt="首页">
  <img src="https://raw.githubusercontent.com/Maoyangui/godusevpn/master/docs/screenshots/nodes.png" width="230" alt="节点列表">
  <img src="https://raw.githubusercontent.com/Maoyangui/godusevpn/master/docs/screenshots/rules.png" width="230" alt="路由规则">
</p>

## 从哪儿开始

| 我想…… | 去 |
|---|---|
| 装到 Windows | [[安装 Windows]] |
| 装到 Mac | [[安装 macOS]] |
| 装到 Linux 或软路由 | [[安装 Linux]] · [[网关模式]] |
| 装到手机 / 电视 | [[安装 Android]] |
| 知道界面上每个东西是干嘛的 | [[日常使用]] |
| 自己排分流规则 | [[路由规则]] |
| 搞清楚 IPv6 那一套 | [[关于 IPv6]] |
| 查文件放在哪、改哪个设置 | [[设置项与文件位置]] |
| 用命令行排障 | [[命令行]] |
| 出问题了 | [[排障]] · [[常见问题]] |
| 自己编译 | [[从源码构建]] |

## 一句话原理

守护进程(root / 管理员身份)拉起内嵌的 sing-box,建一张 TUN 网卡把整机流量接进去;
界面是一份 HTML,四端共用,通过一条本地管道跟守护进程说话。

```
界面(WebView2 / WKWebView / 浏览器 / Android WebView)
   │  本地管道:Windows 命名管道 · Unix 套接字 · Android 进程内桥
守护进程 godusevpn(设置、订阅、状态机、规则渲染)
   │  内嵌
sing-box(TUN 入站 → 路由规则 → 节点出站)
```

所以:**关掉界面不影响连接**,隧道在守护进程里跑;要彻底断开得用界面里的断开、托盘的退出,或者命令行。

## 版本与反馈

- 版本号在「关于」页,也能用 `godusevpn-cli status` 看。
- 报问题请带上「日志 → 导出诊断包」,里面订阅地址已经打码:[提 Issue](https://github.com/Maoyangui/godusevpn/issues)。
