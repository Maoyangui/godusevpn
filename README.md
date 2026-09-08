# 佛跳墙(godusevpn)

m-ui 面板的多平台客户端:内嵌 sing-box,TUN 模式,规则 / 全局 / 直连三态,DoH + fake-ip,默认禁 IPv6,开机自启;多订阅(刷新走"当前代理 → 自动选择 → 直连"回退链)、定时测速、可视化规则组、按进程 / 应用 / 设备直连、应用内升级、日志保留、诊断包。三端共用同一个守护进程和同一套界面。

| 平台 | 状态 | 形态 |
|---|---|---|
| Windows 10 / 11(x64、ARM64) | 可用 | 后台服务 + 托盘客户端(WebView2 窗口),安装包见 Releases |
| Linux 桌面 / 服务器(systemd) | 可用 | 单一二进制,自带浏览器面板,一键安装脚本 |
| OpenWrt / iStoreOS 等软路由 | 可用(网关模式,容器实验室验证) | 同一二进制,procd 自启,ipk 包;局域网设备策略 |
| 梅林(Asuswrt-Merlin / Entware) | 开发中 | TProxy 模式,无真机待反馈 |
| Android 手机 / TV | 开发中 | WebView 承载同一套页面 + gomobile 引擎 |

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

或直接 `powershell -File build.ps1`,产物在 `dist\`(amd64 与 arm64)。图标、清单与版本信息由 `winres\*.json` 经 go-winres 生成到各 `cmd\*\rsrc_windows_*.syso`,CI 按 tag 重新生成。

测试:`go test -tags with_quic,with_utls,with_clash_api,with_gvisor ./...`(含内嵌 sing-box 对生成配置的干跑校验、管道往返、状态机全路径、设置迁移)。

## 安装与使用

从 [Releases](https://github.com/Maoyangui/godusevpn/releases) 下载安装包运行即可:

| 包 | 说明 |
|---|---|
| `godusevpn-<版本>-x64-setup.exe` | 标准版,缺 WebView2 时联网安装运行时 |
| `godusevpn-<版本>-x64-setup-offline.exe` | 离线完整版,内嵌 WebView2 运行时,给精简系统与内网机器 |
| `godusevpn-<版本>-arm64-setup.exe` | ARM64 设备 |

安装包会注册后台服务(自动启动)、装托盘客户端与命令行,可勾选"登录时自动启动",并注册 `godusevpn://` 协议供落地页一键导入。程序未签名,SmartScreen 提示时点"更多信息 → 仍要运行"。

- **首次打开**先填面板给的订阅链接才能进入。
- **首页**只有连接 / 断开大按钮,下面轻量显示上下行、延迟、连接时长;三个面板:模式(规则 / 全局 / 直连)、订阅(多订阅点击切换)、节点(展开自动全部测速并显示毫秒)。
- **左上角菜单**:设置、订阅管理(新增 / 编辑 / 删除 / 刷新)、路由规则、连接(活动连接与断开)、日志(服务与内核、导出诊断包)、关于(检查更新、修复服务、退出)。
- **路由规则**:内置一组默认规则(局域网与私网直连、国内域名与 IP 直连、其余走代理),用户可以可视化新增规则组:名称、出口(代理 / 直连 / 拒绝 / 自动选择 / 某个节点)、任意多条条件(域名后缀、完整域名、关键词、正则、IP 段、端口、进程名、geosite 类别、geoip 国家),组内任一条件命中即算命中;规则组自上而下依次匹配,排在默认规则之前,只在"规则"模式下生效,可启停、排序、编辑、删除。
- **关窗口**只收到托盘,连接不断;托盘"退出"会断开连接并停掉后台服务,TUN 网卡随之消失。
- **订阅刷新回退**:经当前代理刷新失败时,改用"自动选择"组再试几次,仍失败就直连刷新。回退只作用于订阅刷新这一条请求,选中的节点和其它流量的路由都不受影响。
- **定时测速**:设置里的"定时测速(分钟)",自动选择组按该周期测全部节点并切到最快的。
- **按进程直连**:设置 → 分流,每行一个 exe 名(如 `steam.exe`),这些程序的流量不走代理。
- **升级**:关于 → 检查更新 → 升级并重启;只替换程序,设置与订阅都保留。
- **日志保留**:默认 7 天,超过的滚动日志与诊断包自动删除;设置 → 日志里可改,0 = 一直保留。

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

- TUN:`auto_route` + `strict_route`,协议栈 mixed,私网段直通;另开 127.0.0.1:2080 混合端口。
- DNS:远程 DoH 1.1.1.1 经代理,本地 DoH 223.5.5.5 直连,国内域名与节点域名走本地;fake-ip 只分配 IPv4 段;53 端口与 DNS 协议一律劫持进内核。
- IPv6:默认关闭,四层同时堵:DNS `ipv4_only`、无 IPv6 fake-ip 段、TUN 不配 IPv6 地址、IPv6 目标一律拒绝。
- 模式:路由规则内置三套分支,内核 Clash API 切换,毫秒级不重启。
- 规则集:sing-box 官方 geosite-cn / geoip-cn(以及规则组里引用的其它 geosite / geoip 类别),经代理更新;`%ProgramData%\godusevpn\rulesets\` 下有同名 `.srs` 就用本地文件。
- 用户规则组:每组渲染成一条路由规则(多种条件用 logical/or 组合),放在 clash_mode 分支之后、内置国内直连之前;出口选了节点而节点不在当前订阅里时回落到 proxy。

## 许可证

GPL-3.0,与内嵌的 sing-box 一致。
