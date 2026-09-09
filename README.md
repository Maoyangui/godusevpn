# 佛跳墙(godusevpn)

m-ui 面板的多平台客户端:内嵌 sing-box,TUN 模式,规则 / 全局 / 直连三态,DoH + fake-ip,默认禁 IPv6(v6 也接进隧道再拒绝,不让它绕过代理),开机自启;多订阅(刷新走"当前代理 → 自动选择 → 直连"回退链)、定时测速、可视化规则组、按进程 / 应用 / 设备直连、应用内升级、日志保留、诊断包。四端共用同一个守护进程和同一套界面。

| 平台 | 状态 | 形态 |
|---|---|---|
| Windows 10 1809 及以上 / 11(x64、ARM64) | 可用 | 后台服务 + 托盘客户端(WebView2 窗口),安装包见 Releases |
| macOS 13 及以上(苹果芯片、英特尔芯片) | 可用 | launchd 服务 + .app 窗口,curl 一行装,免开发者证书 |
| Linux 桌面 / 服务器(systemd) | 可用 | 单一二进制,自带浏览器面板,一键安装脚本 |
| OpenWrt / iStoreOS 等软路由 | 可用(网关模式,容器实验室验证) | 同一二进制,procd 自启,ipk 包;局域网设备策略 |
| 梅林(Asuswrt-Merlin / Entware) | 开发中 | TProxy 模式,无真机待反馈 |
| Android 手机 / TV | 测试版(手机已真机验证,电视仅模拟器) | WebView 承载同一套页面 + gomobile 引擎,VpnService 建隧道,按 ABI 分包的 APK |

## 结构

| 进程 | 身份 | 职责 |
|---|---|---|
| `godusevpn-svc.exe` | Windows 服务(SYSTEM,自动启动) | 内核、订阅、配置生成与干跑、TUN / 路由 / DNS、控制管道、诊断包 |
| `godusevpn.exe` | 当前用户(登录自启) | 托盘与主窗口(Wails + WebView2) |
| `godusevpn-cli.exe` | 当前用户 | 排障与脚本:状态、连接、模式、节点、订阅、日志、诊断 |

服务与客户端之间走命名管道 `\.\pipe\godusevpn`,一行一个 JSON;服务的访问控制允许普通用户查询、启动和停止它,托盘"退出"因此不用提权。数据在 `%ProgramData%\godusevpn\`:
`settings.json`(设置)、`profiles\<id>.json`(各订阅缓存)、`config.json`(最近生成的配置)、`config.last-good.json`(最近一次成功启动的配置)、`cache.db`(fake-ip 与节点选择)、`rulesets\`(本地规则集)、`logs\`、`diag\`(诊断包)。客户端自己的偏好(语言、外观)在 `%APPDATA%\godusevpn\ui.json`。

## 构建

内嵌 sing-box 需要这几个构建标签:

```bash
go build -tags with_quic,with_utls,with_clash_api,with_gvisor -trimpath -ldflags "-s -w -X github.com/Maoyangui/godusevpn/internal/buildinfo.Version=0.3.0" ./cmd/godusevpn-svc
go build -trimpath -ldflags "-s -w" ./cmd/godusevpn-cli
go build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" ./cmd/godusevpn
```

macOS 的图形界面要连 WebKit,得开 CGO,还得自己补上 Wails 没声明的 UniformTypeIdentifiers 框架;编好的二进制放进手工拼的 `.app`(模板在 `cmd/godusevpn/build/darwin/Info.plist`,图标用 `sips` + `iconutil` 现生成 icns):

```bash
CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o godusevpn.app/Contents/MacOS/godusevpn ./cmd/godusevpn
```

或直接 `powershell -File build.ps1`,产物在 `dist\`(amd64 与 arm64)。图标、清单与版本信息由 `winres\*.json` 经 go-winres 生成到各 `cmd\*\rsrc_windows_*.syso`,CI 按 tag 重新生成。

测试:`go test -tags with_quic,with_utls,with_clash_api,with_gvisor ./...`(含内嵌 sing-box 对生成配置的干跑校验、管道往返、状态机全路径、设置迁移)。

## 安装与使用

从 [Releases](https://github.com/Maoyangui/godusevpn/releases) 下载。资产统一叫 `godusevpn-<版本>-<系统>-<架构>.<后缀>`,发布页里同一系统的包挨在一起,`SHA256SUMS` 是全部包的校验和:

| 系统 | 包 | 说明 |
|---|---|---|
| Windows x64 | `godusevpn-<版本>-windows-x64-setup.exe` | 标准版,缺 WebView2 时联网安装运行时 |
| Windows x64 | `godusevpn-<版本>-windows-x64-setup-offline.exe` | 离线完整版,内嵌 WebView2 运行时,给精简系统与内网机器 |
| Windows ARM64 | `godusevpn-<版本>-windows-arm64-setup.exe` | ARM64 设备 |
| Windows | `godusevpn-<版本>-windows-<架构>-bin.zip` | 三个裸 exe,手工替换与排障用 |
| macOS | `godusevpn-<版本>-macos-<架构>.tar.gz` | arm64(苹果芯片)/ amd64(英特尔),内含守护进程、`godusevpn.app` 与 `install.sh` |
| Linux | `godusevpn-<版本>-linux-<架构>.tar.gz` | amd64 / arm64 / armv7 / mipsle / mips,内含二进制与 `install.sh` |
| OpenWrt / iStoreOS | `godusevpn-<版本>-openwrt-<架构>.ipk` | `opkg install` 装完自动起服务并打印面板地址 |
| Android | `godusevpn-<版本>-android-<ABI>.apk` | arm64(绝大多数手机 / 电视)、armv7(老设备)、x86_64(模拟器 / 少数盒子)、universal(全架构合一) |

### Windows

**系统要求:Windows 10 1809(内部版本 17763)或更高,x64 / ARM64**。安装包会拦住更低的版本。Windows 7 / 8 用不了,而且不打算支持:Go 从 1.21 起就不再支持它们(内嵌的 sing-box 需要更高的 Go),微软也已停止为它们提供 WebView2。

安装包会注册后台服务(自动启动)、装托盘客户端与命令行,可勾选"登录时自动启动",并注册 `godusevpn://` 协议供落地页一键导入(客户端每次启动也会自己确认一遍这个协议,老版本升上来不用重装)。程序未签名,SmartScreen 提示时点"更多信息 → 仍要运行"。

- **首次打开**先填面板给的订阅链接才能进入。
- **首页**只有连接 / 断开大按钮,下面轻量显示上下行、延迟、连接时长;三个面板:模式(规则 / 全局 / 直连)、订阅(多订阅点击切换)、节点(展开自动全部测速并显示毫秒)。
- **左上角菜单**:设置、订阅管理(新增 / 编辑 / 删除 / 刷新)、路由规则、连接(活动连接与断开)、日志(服务与内核、导出诊断包)、关于(检查更新、修复服务、退出)。
- **路由规则**:内置一组默认规则(局域网与私网直连、国内域名与 IP 直连、其余走代理),这三项各自可改(直连 / 代理 / 拒绝;"其余流量"只能选代理或直连),改乱了点"还原默认"一步回出厂。用户还可以可视化新增规则组:名称、出口(代理 / 直连 / 拒绝 / 自动选择 / 某个节点)、任意多条条件(域名后缀、完整域名、关键词、正则、IP 段、端口、进程名、geosite 类别、geoip 国家),组内任一条件命中即算命中;规则组自上而下依次匹配,排在默认规则之前,只在"规则"模式下生效,可启停、排序、编辑、删除。
- **关窗口**只收到托盘,连接不断;托盘"退出"会断开连接并停掉后台服务,TUN 网卡随之消失。
- **订阅刷新回退**:经当前代理刷新失败时,改用"自动选择"组再试几次,仍失败就直连刷新。回退只作用于订阅刷新这一条请求,选中的节点和其它流量的路由都不受影响。
- **定时测速**:设置里的"定时测速(分钟)",自动选择组按该周期测全部节点并切到最快的。
- **按进程直连**:设置 → 分流,每行一个 exe 名(如 `steam.exe`),这些程序的流量不走代理。
- **升级**:关于 → 检查更新 → 升级并重启;只替换程序,设置与订阅都保留。
- **日志保留**:默认 7 天,超过的滚动日志与诊断包自动删除;设置 → 日志里可改,0 = 一直保留。

## macOS

**系统要求:macOS 13(Ventura)或更高**,苹果芯片与英特尔芯片各有一个包(Go 1.27 编出来的程序就是 13 起步)。要管理员密码:

```bash
curl -fsSL https://raw.githubusercontent.com/Maoyangui/godusevpn/master/deploy/macos-install.sh | sudo sh
```

也可以自己下 `godusevpn-<版本>-macos-<架构>.tar.gz`,解开后 `sudo sh install.sh`。

**为什么用 curl 装**:Gatekeeper 的"来自互联网"隔离标记是浏览器下载时打上的,curl 拿到的文件没有这个标记,所以不用买苹果开发者证书,也不用你去"系统设置 → 隐私与安全性"里点允许。要是已经用浏览器下过包,`install.sh` 会顺手把标记清掉。

装完是两样东西:`/usr/local/bin/godusevpn` 是守护进程兼命令行,注册成 launchd 服务(`/Library/LaunchDaemons/com.maoyangui.godusevpn.plist`)开机自启;`/Applications/godusevpn.app` 是图形界面,启动台里叫「佛跳墙」,页面与 Windows / Linux 是同一套。

- 数据在 `/Library/Application Support/godusevpn/`(设置、订阅缓存、`config.json`、日志、诊断包),界面偏好在 `~/Library/Application Support/godusevpn/ui.json`。
- 隧道网卡是系统分配的 `utunN`(macOS 只认这个名字),**协议栈固定 gvisor** —— 系统协议栈在 macOS 上握不了手(内核收不到入站 TCP),设置页因此只给 gvisor 一个选项。
- **DNS 会被接管**:连上时把各网络服务的 DNS 改成一个走隧道的地址,断开时按连接前的原值还原(守护进程启动时也无条件还原一次,崩溃过也不会留下打不开网页的机器)。不这么做的话 macOS 一直问路由器(192.168.x.1),而私网段按"局域网直通"在隧道之外,解析记录就泄漏给本地网络了,fake-ip 与 AAAA 屏蔽也一并失效。
- 这一版**没有菜单栏图标**:Wails 与 systray 都要占着 Cocoa 主线程,凑一起要走外部事件循环,没有真机盯着调不准。所以关掉窗口只是关界面,隧道在后台服务里照常跑,从启动台再打开就回来了。
- 一键导入:落地页的 `godusevpn://` 由 `.app` 的 Info.plist 向系统登记,点一下直接把订阅交给客户端。
- 升级:关于 → 检查更新 → 升级并重启,弹一次系统的管理员密码框,守护进程与 `.app` 一起换掉。
- 卸载:`sudo godusevpn uninstall` 撤掉服务,再删 `/Applications/godusevpn.app` 与 `/usr/local/bin/godusevpn`。
- 验收:`sudo sh deploy/macos-test.sh <订阅地址>` 共 31 项;CI 每次提交都在 GitHub 的苹果芯片跑机上真跑一遍(装服务、拉订阅、建隧道、按规则跑流量、查 IPv6 是不是真被拦住且没绕过隧道、逐个调各功能页面的接口),另加一步把 `.app` 装进 `/Applications` 打开看它能不能活下来。

## Linux(桌面发行版 / 软路由)

同一套守护进程与页面跑在 Linux 上,一个静态二进制 `godusevpn` 既是服务也是命令行,自带浏览器面板(纯 HTTP)。root 执行:

```bash
curl -fsSL https://raw.githubusercontent.com/Maoyangui/godusevpn/master/deploy/install.sh | sh
```

装完终端会打印面板地址(局域网 / 内网地址、云主机的公网地址、本机地址)和随机生成的初始密码。Linux 上面板默认监听 `0.0.0.0:9800`,局域网其它设备直接打开即可;云主机要在安全组 / 防火墙放行 TCP 9800。改密码 `godusevpn passwd`,只给本机看就 `godusevpn settings webListen=127.0.0.1:9800`。面板与 Windows 客户端是同一套页面,功能一致(订阅、三态、规则组、节点测速、日志、升级)。

- 数据布局与 Windows 一致:设置在 `/etc/godusevpn/`,缓存、规则集、日志在 `/var/lib/godusevpn/`(梅林等 Entware 环境在 `/opt/etc` 与 `/opt/var/lib` 下)。
- 自启按初始化系统落地:systemd 单元、OpenWrt 的 procd 脚本、Entware 的 init.d 脚本;`godusevpn install | uninstall | start | stop | status`。
- 本机模式(默认):TUN 只代理本机流量,与 Windows 相同;连上后会给每个物理网卡地址加一条"回包走主表"的策略路由,远程 SSH 不会被切断。
- 网关模式(OpenWrt / iStoreOS 等软路由):设置 → 网络 → 网络模式选"网关"(或 `godusevpn settings netMode=gateway`),局域网设备把网关和 DNS 指向这台机器即被代理,设备的 DNS 查询由内核接管(fake-ip、防泄漏)。菜单里多出"设备"页:自动发现在线设备(DHCP 租约 + 邻居表),每台可设跟随规则 / 强制代理 / 直连 / 拒绝上网,按 MAC 记住;命令行 `godusevpn devices`、`godusevpn device <MAC> <follow|proxy|direct|reject> [名字]`。需要内核带 nftables(OpenWrt 22.03 起的 fw4 都有)。
- 验收脚本 `deploy/linux-test.sh`,与 Windows 的检查项相同;网关模式在 Docker 里的 OpenWrt 23.05 + 一台 LAN 容器上验证过(设备被代理、fake-ip、IPv6 屏蔽、三种设备策略)。

## Android(手机 / 电视)

测试版:手机上已经真机跑通(连接、分流、断开、升级),电视端目前只在模拟器上验证过;欢迎反馈。

- **安装**:侧载 `godusevpn-<版本>-android-<ABI>.apk`(手机与电视基本都是 arm64;不确定就装 universal),首次安装要允许"未知来源"。最低 Android 8.0。
- **同一套界面与功能**:守护进程(设置、多订阅与刷新回退链、规则组、状态机、定时测速)和页面与 Windows / Linux 是同一份代码,数据布局也一样(`settings.json`、`profiles/`、`config.json`、`logs/`),只是放在应用私有目录。
- **隧道**:第一次点"连接"会弹系统的 VPN 授权;之后 VpnService 按引擎给的参数建隧道(地址、路由、DNS、按应用直连),通知栏常驻状态并带"断开"。应用自身默认绕过隧道(订阅刷新直连、升级下载不绕回内核)。
- **按应用直连**:Windows 的"按进程直连"在 Android 上就是应用包名(设置 → 分流),这些应用整个绕过 VPN。
- **开机自启**:引擎按上次状态自动连;Android 12 起后台起前台服务受限,开机与升级完成的广播窗口里会先把服务拉起来。系统设置里也可以把它设为"始终开启的 VPN"。
- **升级**:关于 → 检查更新 → 下载对应 ABI 的 APK、校验 SHA256 后拉起系统安装器;装完引擎按上次状态重新连接。每一版都用同一把签名密钥,否则系统不让覆盖安装。
- **电视**:同一个包在 Android TV / 盒子上以横屏显示,遥控器方向键移动焦点;订阅链接建议用剪贴板粘贴。
- **诊断**:日志 → 导出诊断包,经系统分享发出去。

构建:`deploy\android-build.ps1`(gomobile bind 出 AAR → Gradle 出 APK,需要 JDK 17 与 Android SDK / NDK);模拟器冒烟 `deploy/android-emu-test.sh <订阅地址>`。

## 命令行(排障)

以管理员身份:

```
godusevpn-svc install                          注册服务并启动
godusevpn-cli profile https://面板/sub/用户名    设置订阅(自动补 format=json)
godusevpn-cli connect
godusevpn-cli status
godusevpn-cli mode global | rule | direct
godusevpn-cli nodes / select <节点> / test
godusevpn-cli settings ipv6=on tunStack=system probeMinutes=5 logDays=14
godusevpn-cli rules                            列出规则组
godusevpn-cli logs 200 core
godusevpn-cli diag                             导出诊断包(订阅地址已打码)
godusevpn-svc uninstall
```

## 默认策略

- TUN:`auto_route` + `strict_route`,协议栈 mixed(macOS 上固定 gvisor,系统协议栈在那儿握不了手),私网段直通;另开 127.0.0.1:2080 混合端口。
- DNS:远程 DoH 1.1.1.1 经代理,本地 DoH 223.5.5.5 直连,国内域名与节点域名走本地;fake-ip 只分配 IPv4 段;53 端口与 DNS 协议一律劫持进内核。macOS 上还会在连接期间接管系统 DNS,否则解析绕过隧道直接问路由器。
- IPv6:默认关闭,四层同时堵:DNS `ipv4_only`、无 IPv6 fake-ip 段、IPv6 目标一律拒绝;并且 TUN 照样声明 IPv6 地址,把 v6 流量接进隧道再丢掉 —— 不接进来的话它会绕过隧道从物理网卡直接出网,等于泄露(局域网的 `fc00::/7`、`fe80::/10`、`ff00::/8` 仍留在隧道外)。
- 模式:路由规则内置三套分支,内核 Clash API 切换,毫秒级不重启。
- 规则集:sing-box 官方 geosite-cn / geoip-cn(以及规则组里引用的其它 geosite / geoip 类别),经代理更新;`%ProgramData%\godusevpn\rulesets\` 下有同名 `.srs` 就用本地文件。
- 用户规则组:每组渲染成一条路由规则(多种条件用 logical/or 组合),放在 clash_mode 分支之后、内置国内直连之前;出口选了节点而节点不在当前订阅里时回落到 proxy。

## 许可证

GPL-3.0,与内嵌的 sing-box 一致。
