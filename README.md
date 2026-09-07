# 佛跳墙(godusevpn)

m-ui 面板的 Windows 客户端:内嵌 sing-box,TUN 模式,规则 / 全局 / 直连三态,DoH + fake-ip,默认禁 IPv6,后台服务开机自启。

## 结构

| 进程 | 身份 | 职责 |
|---|---|---|
| `godusevpn-svc.exe` | Windows 服务(SYSTEM,自动启动) | 内核、订阅、配置生成与干跑、TUN / 路由 / DNS、控制管道 |
| `godusevpn.exe` | 当前用户(登录自启) | 托盘与主窗口(M1 阶段) |
| `godusevpn-cli.exe` | 当前用户 | 排障与脚本:状态、连接、模式、节点、订阅、日志、诊断 |

服务与客户端之间走命名管道 `\\.\pipe\godusevpn`,一行一个 JSON。数据在 `%ProgramData%\godusevpn\`:
`settings.json`(设置)、`profile.json`(订阅缓存)、`config.json`(最近生成的配置)、`config.last-good.json`(最近一次成功启动的配置)、`cache.db`(fake-ip 与节点选择)、`logs\`。

## 构建

内嵌 sing-box 需要这几个构建标签:

```bash
go build -tags with_quic,with_utls,with_clash_api,with_gvisor -trimpath -ldflags "-s -w -X github.com/Maoyangui/godusevpn/internal/buildinfo.Version=0.1.0" ./cmd/godusevpn-svc
go build -tags with_quic,with_utls,with_clash_api,with_gvisor -trimpath -ldflags "-s -w" ./cmd/godusevpn-cli
```

或直接 `powershell -File build.ps1`,产物在 `dist\`(amd64 与 arm64)。

测试:`go test -tags with_quic,with_utls,with_clash_api,with_gvisor ./...`(含内嵌 sing-box 对生成配置的干跑校验、管道往返、状态机全路径)。

## 使用(M0,命令行)

以管理员身份:

```
godusevpn-svc install                          注册服务并启动
godusevpn-cli profile https://面板/sub/用户名    设置订阅(自动补 format=json)
godusevpn-cli connect
godusevpn-cli status
godusevpn-cli mode global | rule | direct
godusevpn-cli nodes / select <节点> / test
godusevpn-cli settings ipv6=on tunStack=system
godusevpn-cli logs 200 core
godusevpn-cli diag
godusevpn-svc uninstall
```

## 默认策略

- TUN:`auto_route` + `strict_route`,协议栈 mixed,私网段直通;另开 127.0.0.1:2080 混合端口。
- DNS:远程 DoH 1.1.1.1 经代理,本地 DoH 223.5.5.5 直连,国内域名与节点域名走本地;fake-ip 只分配 IPv4 段;53 端口与 DNS 协议一律劫持进内核。
- IPv6:默认关闭,四层同时堵:DNS `ipv4_only`、无 IPv6 fake-ip 段、TUN 不配 IPv6 地址、IPv6 目标一律拒绝。
- 模式:路由规则内置三套分支,内核 Clash API 切换,毫秒级不重启。
- 规则集:sing-box 官方 geosite-cn / geoip-cn,经代理更新;`%ProgramData%\godusevpn\rulesets\` 下有同名 `.srs` 就用本地文件。

## 许可证

GPL-3.0,与内嵌的 sing-box 一致。
