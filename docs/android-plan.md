# 佛跳墙 Android 客户端计划(手机 + Android TV)

目标:功能、规则与 Windows / Linux 版一致(多订阅与刷新回退链、规则 / 全局 / 直连、DoH + fake-ip + 禁 IPv6、可视化规则组、定时测速、应用内升级、日志保留、诊断包),跑在主流安卓手机(Android 8+)与 Android TV / 电视盒子上。

## 1. 复用什么

| 层 | 复用 | 说明 |
|---|---|---|
| Go 引擎 | `daemon`(整个守护进程:设置、订阅与刷新回退链、配置生成、状态机、测速、规则组)+ `internal/uiapi`(页面方法集与事件流,Linux 面板已在用) | 守护进程在 Android 上跑在 VPN 服务进程里,不开 socket 与 HTTP,页面经 WebView 桥直接调 `uiapi.Service.CallJSON`;用 gomobile 编成 AAR;TUN 由 VpnService 建立后把 fd 交给内核(sing-box 的 `platform` 接口) |
| 数据布局 | `settings.json`、`profiles/<id>.json`、`config.json`、`cache.db`、`logs/` | 与 Windows / Linux 完全一致,将来三端可互相导入 |
| 界面 | 现有竖版页面(`app.js` / `style.css` / `i18n.js`) | 方案 A(推荐):WebView 承载同一套页面 + JS 桥接引擎,三端一套 UI;方案 B:Kotlin Compose 原生重写,见第 5 节 |

订阅刷新回退链在 Android 上与 Windows 完全相同:引擎与内核跑在同一个进程里,能拿到内核里每个出站的 HTTP 客户端,所以还是"当前代理 → 自动选择组三次(隔两秒)→ 直连",只作用于刷新订阅这一条请求,不改选中节点、不动其它流量;未连接时走系统网络;订阅无效 / 到期不回退。

## 2. 架构

```
Kotlin 外壳(app 进程)                    Go 引擎(gomobile AAR,同进程)
├─ VpnService(前台服务,通知栏开关)  ←→  engine.Start / Stop(libbox + PlatformInterface:openTun、包名查询、网络变化)
├─ 快捷设置磁贴、开机自启接收器           engine.State 事件流(state / traffic / update-progress)
├─ 深链接 godusevpn:// 与 clash://       engine.Profiles / Rules / Settings / Nodes / Probe / Logs / Diag / Update
├─ 升级安装器(下载 APK → 安装意图)
├─ TV:leanback 启动器入口、遥控器焦点
└─ UI 容器:WebView(方案 A)或 Compose(方案 B)
```

- 内核:sing-box `libbox`(与桌面同一版本 1.14),`with_quic,with_utls,with_clash_api,with_gvisor` 同一套标签;TUN 由 VpnService 建立,fd 交给 libbox;`auto_route` 在 Android 上由 VpnService 的路由表实现(`addRoute 0.0.0.0/0`,不加 v6 路由与地址即全链路禁 IPv6)。
- 按应用直连:Windows 的"按进程直连"在 Android 对应 `VpnService.Builder.addDisallowedApplication`(应用整个绕过 VPN)与 sing-box 规则里的 `package_name`;规则组的"进程名"条件在 Android 显示为"应用"(带包名选择器)。
- 模式切换、节点选择、连接列表、实时速度:同桌面,经内核 Clash API(进程内回环)。
- 测速:已连接经出站做 URL 测试;未连接直连测(Android 无原始套接字,ICMP 不可用:TCP 类协议量握手,hysteria2 / tuic 这类用"只带出站的临时实例"经出站测一次,与 Linux 的兜底路径相同)。
- 自启:开机广播 + 系统"始终开启的 VPN";按上次状态自动连接。
- 升级:GitHub Releases 按 ABI(arm64-v8a、armeabi-v7a、x86_64)取 APK,校验 SHA256 后拉起系统安装器;APK 用自建密钥签名(免费,密钥放 CI 机密,每版必须同一把否则装不上),不上 Play 商店。
- 日志:同 logx,默认保留 7 天;诊断包走系统分享。

## 3. 功能对照

| 功能 | Windows | Android 手机 | Android TV |
|---|---|---|---|
| 多订阅、刷新回退链 | ✓ | ✓ | ✓ |
| 规则 / 全局 / 直连 | ✓ | ✓ | ✓ |
| DoH、fake-ip、禁 IPv6 | ✓ | ✓ | ✓ |
| 规则组(可视化) | ✓ | ✓,"进程名"改"应用" | ✓,输入靠遥控器,建议少改多导入 |
| 按进程 / 应用直连 | ✓ | ✓(绕过 VPN 的应用列表,带图标) | ✓ |
| 定时测速、自动选择、未连接测速 | ✓ | ✓ | ✓ |
| 应用内升级并自动重启 | ✓ | ✓(系统安装器,装完自动回到应用) | ✓(同左;部分盒子需允许未知来源) |
| 日志保留、诊断包 | ✓ | ✓(分享) | ✓ |
| 连接列表 | ✓ | ✓ | ✓ |
| 开机自启 | 服务 | 开机广播 + 始终开启 VPN | 同左 |
| 托盘 | ✓ | 通知栏常驻 + 快捷设置磁贴 | 无 |
| 深链接导入 | ✓ | ✓(godusevpn:// 与 clash://,落地页一键导入) | 遥控器不便,提供"从手机推送":手机上长按订阅→"发给电视",局域网内发现电视端并推送 |

## 4. UI 设计

手机:与 Windows 竖版界面一模一样(大连接按钮、三个底部面板、抽屉菜单、设置、订阅管理、路由规则、连接、日志、关于),深浅色跟随系统,手势返回。

TV:同一套视觉语言换成横版布局:左栏是连接按钮与状态,右栏是模式 / 订阅 / 节点三张卡,抽屉改为左侧固定导航;所有可点元素有明显焦点态,遥控器方向键按空间关系移动,返回键关面板;文字输入(订阅链接、规则)尽量用剪贴板粘贴与手机推送代替。

## 5. UI 实现方案(需要你定)

| | 方案 A:WebView 承载现有页面 | 方案 B:Kotlin Compose 原生 |
|---|---|---|
| 一致性 | 三端同一套代码,像素级一致,改一处三端生效 | 需按 Compose 重画,尽量贴近 |
| 工作量 | 小:补一个 JS 桥、TV 横版 CSS、焦点样式 | 大:手机 + TV 两套 Compose 布局 |
| 体验 | 手机上与原生无差;TV 靠 WebView 的方向键焦点导航,需自己写空间导航脚本 | 手感最原生,TV 焦点体系成熟(Compose for TV) |
| 包体 | 小(系统 WebView) | 小 |
| 风险 | 老盒子的 WebView 版本过旧(Android 8 以下盒子)可能渲染异常 | 无 |

建议先做方案 A,把引擎、VPN、升级这些底层打通并用起来;TV 端若遥控器体验不达标,再把 TV 界面换成 Compose(引擎与桥接不变)。

## 6. 构建与验收

- 构建:Go 1.27 + gomobile(Android NDK)出 AAR;Gradle 出 APK(按 ABI 分包 + 一个通用包);GitHub Actions ubuntu 跑机装 NDK;签名密钥与口令放仓库机密;资产名 `godusevpn-<ver>-android-<abi>.apk` + SHA256SUMS。
- 冒烟:本机 Android 模拟器(手机 AVD + Android TV AVD)跑连接、三态、规则组、升级;VpnService 在模拟器里可用。
- 验收:你的手机与电视 / 盒子侧载 APK,按 Windows 的清单核对(出口 IP、fake-ip、IPv6 屏蔽、三态、断开清理、直连真实 IP、回退链、升级重开)。

## 7. 里程碑

| 阶段 | 内容 | 验收 |
|---|---|---|
| A0 | `internal/engine` 抽出(去 IPC / Windows 依赖)、gomobile AAR、libbox 接入、VpnService、通知栏开关、状态事件 | 模拟器能连通 |
| A1 | 手机端全部页面(WebView + 桥)、多订阅、规则组、按应用直连、深链接、开机自启、测速 | 你的手机 |
| A2 | TV 横版布局、遥控器焦点导航、leanback 入口、手机推送订阅到电视 | 你的电视 / 盒子 |
| A3 | 应用内升级、诊断分享、日志保留、CI 签名发布、文档 | 全部 |

## 8. 风险

- gomobile 与 libbox 的编译链条(NDK 版本、Go 版本)要对齐,首次搭建耗时;之后稳定。
- Android 各厂商的后台限制(小米 / 华为 / OPPO)会杀 VPN 服务,需引导用户关电池优化并用"始终开启的 VPN"。
- 电视盒子 Android 版本参差(不少停在 7.1 / 9),WebView 方案要在老盒子上实测。
- APK 未上商店,首次安装要允许未知来源;升级必须同一签名密钥。

## 9. 进度(2026-09-08)

- A0 完成:`mobile` 包自己实现 sing-box 平台接口(不依赖 libbox),gomobile 出 AAR;Kotlin 外壳(VpnService、宿主代理、WebView + 同步桥、开机接收器、通知栏);模拟器(phone34,x86_64)上页面加载、状态机、VPN 授权、TUN 参数交接、protect 全部走通。
  - 踩过的坑:WebView 资源路径要把 `/` 整个交给 AssetsPathHandler;`protect` 不能只依赖服务实例(内核起步先下规则集,服务还没起);盯默认网络要带 NOT_VPN 的请求并在注册时同步报一次,否则内核起步时"没有可用网络";守护进程按上次状态自动连时不要再点一次连接。
  - 未完成:开发机的流量整体走本机的 Clash Verge 隧道,模拟器里的 hysteria2(QUIC / UDP)出不去,"内核连通并出口"这一步要在没有本机隧道的环境或真机上再验。
- A3 提前做了:CI 的 android 作业(gomobile bind + Gradle,按 ABI 分包 + universal,自建密钥签名,资产 `godusevpn-<版本>-android-<abi>.apk`),应用内升级下载后交给系统安装器,升级 / 开机广播里先起前台服务。
- 剩下:A1 手机端逐页核验(桥的每个方法、按应用直连的包名选择器)、A2 电视横版与遥控器焦点、真机验收。
- A2 已做(2026-09-08,tv33 模拟器验证):电视横版布局(body.tv:左连接按钮、右四项状态 + 三张面板卡,面板与页面居中放大)、遥控器空间导航(app.js 自己按元素位置找焦点,Enter = 点击,返回键经 `__godBack` 先收面板 / 抽屉再退页,首页返回把应用放后台)、电视 banner、leanback 入口。
  - 坑:页面资源要用 AGP 的 `addGeneratedSourceDirectory` 挂进 assets,手工 srcDir 改了页面不会重新合并(打出旧页面),release 的 lintVital 还会报隐式依赖;WebView 关掉 HTTP 缓存(升级后才不会用旧 JS / CSS);没有 href 的 `a` 的 tabIndex 属性读出 0 但并不可聚焦,要显式 setAttribute;抽屉 / 面板按状态事件重绘会把焦点元素换掉,重绘前后按 data-* 键把焦点落回去;`:focus` 别改 position(连接按钮是 absolute 定位);列表 overflow hidden 会裁外描边,描在内侧。
  - Android 上"进程名"= 应用包名:builder 按平台出 `package_name` 规则,按应用直连的应用写进 TUN 的 `exclude_package`(整个绕过 VPN);页面文案按平台切换。
- 真机反馈修正(2026-09-08,v0.6.0-a2):引导页加订阅后首页叠了两层(nav 只清第一个旧页面)→ nav 清掉全部旧页面;删除订阅 / 规则组没反应 → WebView 没设 WebChromeClient 时 confirm() 直接当取消,已补;标题偏右 → 没有窗口按钮的壳右侧补等宽占位;三条杠与刷新小图标放大;断开后 VPN 标记还在、连接后断网 → 服务留着的 TUN fd 没关,现在内核关 TUN 时经宿主接口关掉(嵌 *tun.NativeTun 的包装,LinuxTUN 断言不受影响),状态变断开 / 出错时再兜底;关于页"退出"在 Android 真正断开并结束进程,"打开目录"/"修复服务"/开机自启开关不再显示;Go 崩溃经 debug.SetCrashOutput、Kotlin 崩溃经默认异常处理器写 logs/crash.log,诊断包带上;桌面图标改成自适应图标(白色圆角底 + logo)。
  - 模拟器连通已验(EMU_FULL=1 + 假订阅指向宿主机代理端口经 adb reverse + 本地规则集):拉订阅 → 内核起 → tun0 → Chrome 流量按包名分流、fake-ip DNS → 断开后 tun0 与通知消失。
  - 订阅报"订阅无效、已用完或已到期"= 面板对这条链接回了 404:m-ui 在令牌对不上、用户停用、流量用完、到期、代理池停用 / 耗尽时都回 404;有缓存时客户端会继续用缓存连,没缓存(新导入)就只能等面板恢复。
