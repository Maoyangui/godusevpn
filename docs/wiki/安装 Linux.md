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

> 面板对外监听时**必须**有密码;没设密码的话,非本机来的请求一律挡掉(本机与命令行照常)。

## 文件放在哪

| 路径 | 内容 |
|---|---|
| `/etc/godusevpn/` | 设置(`settings.json`)、订阅配置 |
| `/var/lib/godusevpn/` | 订阅缓存、规则集 `.srs`、`config.json`、日志、诊断包 |
| Entware(梅林等) | 对应 `/opt/etc/` 与 `/opt/var/lib/` |

自启按初始化系统落地:systemd 单元、OpenWrt 的 procd 脚本、Entware 的 init.d 脚本。

## 本机模式(默认)

TUN 只代理本机流量,与 Windows 相同。

**远程 SSH 不会被切断**:连上后会给每个物理网卡地址加一条「回包走主表」的策略路由,本机对外服务(SSH、面板本身)的回包不进 TUN。

## 网关模式

软路由用的,见 [[网关模式]]。

## 验收

```bash
sh deploy/linux-test.sh <订阅地址>
```

检查项与 Windows 那份相同。网关模式在 Docker 里的 OpenWrt 23.05 + 一台 LAN 容器上验证过(设备被代理、fake-ip、IPv6 屏蔽、三种设备策略)。

相关:[[网关模式]] · [[命令行]] · [[设置项与文件位置]]
