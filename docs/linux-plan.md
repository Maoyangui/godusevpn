# 佛跳墙 Linux 客户端计划(软路由 + 主流发行版)

目标:功能与 Windows 版对齐,主要跑在梅林 / OpenWrt / iStoreOS 这类软路由上,同时兼容主流 Linux 发行版;带可视化面板(浏览器访问,纯 HTTP),UI 与 Windows 版一致。没有路由器真机,验收全部在虚拟环境里做(见第 7 节)。

## 1. 现状盘点:Windows 版能复用什么

| 状态 | 包 | 说明 |
|---|---|---|
| 直接复用 | `settings`(含 `rules.go`)、`profile`、`core`、`state`、`clash`、`logx`、`buildinfo`、`update`、`ipc/protocol.go` + `views.go` | 纯 Go,无平台依赖;`builder` 也大部分复用,只需加 Linux 入站分支 |
| 需抽象 | `paths`、`daemon`、`ipc` 传输层、`daemon/diag.go` | 目录、命名管道、`route print` / `ipconfig` 这些按平台分文件 |
| Windows 专有,Linux 另写对应物 | `svc`、`autostart`、`cmd/godusevpn`(Wails 窗口 + 托盘)、`installer`(Inno) | Linux 对应:init 集成(systemd / procd / Entware)、Web 面板、安装脚本与包 |
| 前端整套复用 | `app.js` / `style.css` / `i18n.js` | 把 `window.go.main.App.*` 换成同名的 HTTP 适配层 `api.js`,事件走 SSE;两端一套 UI,一处改两边生效 |

## 2. 目标平台与运行形态

| 平台 | 架构 | init | 防火墙 | 默认形态 | 验收方式 |
|---|---|---|---|---|---|
| OpenWrt 22.03+ / iStoreOS / FriendlyWrt | x86_64、aarch64、armv7、mipsel | procd | fw4(nftables) | 网关-TUN | Docker 里的 OpenWrt x86_64 rootfs + 一台"LAN 设备"容器 |
| 梅林 Asuswrt-Merlin(Entware) | aarch64、armv7 | `/opt/etc/init.d` + `/jffs/scripts` 钩子 | iptables | 网关-TProxy | 无真机:TProxy 机制在 iptables 的 Debian 容器里验通,Entware 脚本按官方文档写,标"待真机反馈" |
| Debian / Ubuntu / Fedora / Arch 等 | x86_64、aarch64 | systemd | nftables / iptables | 本机 | JP 测试机 |

三种网络模式,设置里一键切换:

1. **本机(local)**:TUN + auto_route,只代理本机流量,与 Windows 版完全相同。桌面 Linux 默认。
2. **网关-TUN(gateway-tun)**:TUN + auto_route + auto_redirect(sing-box 自己用 nftables 把转发流量导进 TUN),LAN 设备把路由器当网关即被代理。OpenWrt / iStoreOS 默认。
3. **网关-TProxy(gateway-tproxy)**:tproxy + mixed 入站,iptables / nftables 做 REDIRECT(TCP)+ TPROXY(UDP)+ 策略路由,不建 TUN。梅林默认,也给老内核、没 nftables 的系统兜底。

LAN 侧 DNS:网关-TUN 模式下 sing-box 的 auto_redirect 自己用 nftables 把 LAN 发往路由器 53 端口的查询 DNAT 到 TUN 地址并接管(实测:我们再自己加 DNAT 反而会抢在它前面把查询改坏,所以不加);TProxy 模式(L2)才需要我们自己 REDIRECT 到内核 DNS 端口;dnsmasq 只保留 DHCP。IPv6 沿用 Windows 决策:默认全链路禁用,网关模式下再加 LAN 侧 AAAA 屏蔽与 v6 转发拒绝,避免设备走 v6 绕过。

## 3. 架构

单一静态二进制 `godusevpn`(`CGO_ENABLED=0`,内嵌 sing-box 与面板资源),子命令:

- `godusevpn run`:守护进程,等价 Windows 的服务:内核、订阅、配置生成、状态机、Unix socket 控制口、Web 面板。
- `godusevpn install | uninstall | start | stop | status`:按平台生成 init 集成并启动。
- `godusevpn connect | mode | nodes | select | test | profile | rules | settings | logs | diag …`:与 Windows cli 同名同义,经 Unix socket。

目录与数据布局和 Windows 完全一致(`settings.json`、`profiles/<id>.json`、`config.json`、`cache.db`、`logs/`、`diag/`),便于日后互相导入:

- 通用 Linux / OpenWrt:`/etc/godusevpn/`(设置)、`/var/lib/godusevpn/`(缓存、规则集、日志)
- 梅林(Entware):`/opt/etc/godusevpn/`、`/opt/var/lib/godusevpn/`

控制协议沿用现在"一行一 JSON"的方法集,传输从命名管道换成 `/var/run/godusevpn.sock`。Web 面板是同一套方法的 HTTP 映射:`POST /api/<Method>`(body 即参数)、`GET /api/events`(SSE 推 state / traffic / update-progress)、`GET /` 静态页。**只做 HTTP,不做 HTTPS**。面板绑定非回环地址时必须有密码(安装时生成并打印,或首访设置),cookie 会话、同源检查、失败限速;CLI 走 socket 不需要密码。默认监听 `0.0.0.0:9800`(路由器)/ `127.0.0.1:9800`(桌面)。

代码落位(包内按 build tag 分平台,Windows 的三个 cmd 不动):

| 包 | 变化 |
|---|---|
| `internal/paths` | `paths_windows.go` / `paths_linux.go`(含 Entware 探测) |
| `internal/ipc` | `pipe_windows.go` / `sock_linux.go`,同一个 Server / Call 接口 |
| `internal/svc` | `service_windows.go` / `init_linux.go`(systemd、procd、Entware 三套模板) |
| `internal/netmode`(新) | 网关模式的防火墙与策略路由:nft 与 iptables 两套,幂等 apply / clear,开机与防火墙重载后重放 |
| `internal/builder` | 输入加 `Platform` / `NetMode`,生成对应入站(tun + auto_redirect 或 tproxy + redirect)、LAN 设备策略(source_ip)、DNS 假地址劫持规则 |
| `internal/uiapi`(新) | 页面方法集与事件流的共用实现(与 Windows 客户端的绑定同名同义),Linux 面板与 Android 桥都用它 |
| `internal/web`(新) | HTTP 服务、会话、SSE、embed 前端;方法转给 uiapi |
| `internal/daemon` | 去掉 Windows 专有调用;设备发现(DHCP 租约 + ARP / neigh) |
| `cmd/godusevpn-daemon`(新入口,Linux 与 macOS 共用) | run / install / cli 一体 |
| 前端 | 移到仓库根 `web/`,Wails 与 Linux embed 都指向它;新增 `api.js`、设备页、登录页 |

## 4. 功能对照

| 功能 | Windows | Linux 桌面 | 路由器 |
|---|---|---|---|
| 多订阅、刷新回退链(代理 → 自动 → 直连,只作用于刷新) | ✓ | ✓ | ✓ |
| 规则 / 全局 / 直连 | ✓ | ✓ | ✓(作用于全 LAN) |
| DoH、fake-ip、禁 IPv6 | ✓ | ✓ | ✓(作用于全 LAN) |
| 规则组(可视化) | ✓ | ✓ | ✓,新增条件类型"来源设备"(IP / MAC) |
| 按进程直连 | ✓ | ✓ | 改为"按设备直连 / 拒绝" |
| 定时测速、自动选择、未连接直连测 | ✓ | ✓ | ✓ |
| 应用内升级并自动重启 | ✓ | 换二进制 + 经 init 重启,配置保留 | 同左 |
| 日志保留、诊断包 | ✓ | ✓(`ip route` / `ip addr` / `nft list` / `iptables-save` 替代 Windows 命令) | ✓ |
| 连接列表 | ✓ | ✓ | ✓(带来源设备) |
| 开机自启 | 服务 | systemd | procd / Entware |
| 托盘 | ✓ | 无,给 .desktop 启动器打开面板 | 无 |

路由器新增:**设备列表**(名字、IP、MAC、在线、当前策略)、**每台设备三态**(代理 / 直连 / 拒绝)、网关模式开关、面板只允许 LAN 访问、防火墙重载后自愈、与 dnsmasq 共存。

## 5. UI 设计

同一套竖版界面直接跑在浏览器里:宽屏时居中一张 420px 的"手机卡片"(淡渐变背景),手机上满屏;深浅色、动画、抽屉、三个底部面板、保存栏全部沿用。新增:登录页;"设备"页(路由器);设置里新增"网络"分组(模式、LAN 网段、DNS 劫持、面板监听地址与密码);首页副标题在网关模式显示"网关 · 已代理 N 台设备"。不改现有交互,Windows 版用户无学习成本。

## 6. 构建、打包、发布

- CI 矩阵:linux/amd64、arm64、arm(v7)、mipsle(softfloat)、mips(softfloat);产物 `godusevpn-<ver>-linux-<arch>.tar.gz`(二进制 + install.sh)、`godusevpn-<ver>-openwrt-<arch>.ipk`、`godusevpn_<ver>_<arch>.deb`(amd64 / arm64),附 SHA256SUMS。
- `install.sh` 一键:探测 OpenWrt / 梅林(Entware)/ systemd,下载对应包,装 init,首启生成面板密码并打印访问地址。
- 应用内升级:按上述资产名下载,校验后替换二进制,经 init 重启。

## 7. 验收环境(没有真机)

全部在 JP 测试机(Linux VPS)上搭,统一用 Docker:

| 环境 | 用途 | 怎么搭 |
|---|---|---|
| 宿主机本身(systemd) | 桌面 Linux 路径:本机模式、systemd 安装、CLI、升级 | 直接装 |
| `openwrt` 容器(官方 `openwrt/rootfs` x86_64,`--cap-add NET_ADMIN --device /dev/net/tun`) | OpenWrt / iStoreOS 路径:procd 脚本、fw4 / nftables、网关-TUN、LAN DNS 劫持、ipk 安装 | docker compose |
| `lan` 容器(alpine,默认路由指向 `openwrt` 容器) | 模拟一台 LAN 设备:验证被代理、设备三态策略、DNS 劫持、IPv6 屏蔽 | 同一个 compose 网络 |
| `debian-iptables` 容器(legacy iptables) | 梅林路径的机制部分:TProxy 模式的 iptables 规则、策略路由、REDIRECT/TPROXY | docker compose |
| qemu-user | arm64 / armv7 / mipsel 二进制能启动、能读配置(不做网络) | 宿主机装 `qemu-user-static` |

梅林专有的部分(Entware init 脚本、`/jffs/scripts/firewall-start` 钩子、Asuswrt 的 dnsmasq 布局)没有真机只能按官方文档写,发布时标"梅林待真机反馈",并给一段自检脚本让用户装完贴输出。

验收脚本 `deploy/linux-test.sh`:与 Windows 脚本同样的检查项(TUN / 路由 / fake-ip / DNS 劫持 / 出口 / IPv6 / 三态 / 断开清理 / 直连真实 IP);网关场景由 `lan` 容器里再跑一遍。

## 8. 里程碑

| 阶段 | 内容 | 验收 |
|---|---|---|
| L0(已完成,v0.5.0-l0) | 平台层抽象、Unix socket、HTTP + SSE 面板复用前端、登录、systemd、本机模式、CLI、CI 出 amd64 / arm64 / armv7 / mipsle / mips tar.gz | 测试机真机 20 项全过 |
| L1(已完成,v0.5.2-l1) | procd、auto_redirect 网关模式(LAN DNS 由 sing-box 自己接管)、设备列表与每设备策略、ipk(opkg 装过)、install.sh | Docker 里的 openwrt + lan 容器 |
| L2 | iptables TProxy 模式、Entware init 与 firewall-start 钩子、dnsmasq 共存、自检脚本 | debian-iptables 容器;梅林标待反馈 |
| L3 | Linux 自更新、诊断包、deb、mips / armv7 构建与 qemu 冒烟、文档;kill switch 仍缓 | 全部 |

## 9. 风险

- 梅林没有真机:Entware 脚本与 dnsmasq 布局可能有出入,只能靠首批用户的自检输出修。
- 容器里的 OpenWrt 没有真实 WAN / 无线,只能验路由、防火墙、DNS、代理这些逻辑,验不了硬件 offload、无线客户端。
- sing-box 的 auto_redirect 依赖 nftables;iptables 的老 OpenWrt(21.02 及以前)走 TProxy 模式。
- MIPS 软路由性能有限:单文件约 40 MB,mips 需 softfloat,hysteria2 这类 QUIC 协议在 MIPS 上 CPU 吃紧。
