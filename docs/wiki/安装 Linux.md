# 安装 · Linux(桌面 / 服务器 / 软路由)

同一套守护进程与页面跑在 Linux 上,一个静态二进制 `godusevpn` 既是服务也是命令行,自带浏览器面板(纯 HTTP)。

## 装

root 执行:

```bash
curl -fsSL https://raw.githubusercontent.com/Maoyangui/godusevpn/master/deploy/install.sh | sh
```

装完终端会打印面板地址(局域网 / 内网地址、云主机的公网地址、本机地址)和**随机生成的初始密码**。

默认监听 `0.0.0.0:9800`,局域网其它设备直接打开即可;云主机要在安全组 / 防火墙放行 TCP 9800。

| 要做的事 | 命令 |
|---|---|
| 改面板密码 | `godusevpn passwd` |
| 只给本机看 | `godusevpn settings webListen=127.0.0.1:9800` |
| 服务开关 | `godusevpn install \| start \| stop \| status \| uninstall` |

> 面板**必须**有密码才能用;没设密码的话面板一律不给用,本机来的也一样(命令行走控制口,照常)。

## 文件放在哪

| 路径 | 内容 |
|---|---|
| `/etc/godusevpn/` | 设置(`settings.json`)、订阅配置 |
| `/var/lib/godusevpn/` | 订阅缓存、规则集 `.srs`、`config.json`、日志、诊断包 |
| Entware(梅林等) | 对应 `/opt/etc/` 与 `/opt/var/lib/` |

自启按初始化系统落地:systemd 单元、OpenWrt 的 procd 脚本、Entware 的 init.d 脚本。
服务崩了 systemd / procd 会自己拉起;梅林没有这种机制,启动时挂一条每分钟的定时任务(`cru`)代替 ——
`godusevpn stop` 主动停掉的不拉,再 `start` 或重启路由器后恢复。

## 本机模式(默认)

TUN 只代理本机流量,与 Windows 相同。

**远程 SSH 不会被切断**:外部连进来的连接(SSH、面板本身)由连接跟踪认出来,它们的回包打上标记走原来的路由、不进 TUN;连上之前就开着的 SSH 会话也一样。只把套接字绑在物理网卡地址上往外连的程序(浏览器的 WebRTC 会这么做)照样走隧道,不会露出真实 IP。这一套要用 `nft`;系统里没有时退回按本机地址放行(服务日志里会写明),那种情况下绑物理地址的连接会绕开隧道。

## 网关模式

软路由用的,见 [[网关模式]]。

## 验收

```bash
sh deploy/linux-test.sh <订阅地址>
```

检查项与 Windows 那份相同。网关模式在 Docker 里的 OpenWrt 23.05 + 一台 LAN 容器上验证过(设备被代理、fake-ip、IPv6 屏蔽、三种设备策略)。

相关:[[网关模式]] · [[命令行]] · [[设置项与文件位置]]
