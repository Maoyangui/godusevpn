# 安装 · macOS

**系统要求:macOS 13(Ventura)或更高**,苹果芯片与英特尔芯片各有一个包(Go 1.27 编出来的程序就是 13 起步)。

## 装

要管理员密码:

```bash
curl -fsSL https://raw.githubusercontent.com/Maoyangui/godusevpn/master/deploy/macos-install.sh | sudo sh
```

也可以自己下 `godusevpn-<版本>-macos-<架构>.tar.gz`,解开后 `sudo sh install.sh`。

### 为什么用 curl 装

Gatekeeper 的「来自互联网」隔离标记是**浏览器下载时**打上的,curl 拿到的文件没有这个标记。
所以不用买苹果开发者证书,也不用你去「系统设置 → 隐私与安全性」里点允许。
已经用浏览器下过包也没关系,`install.sh` 会顺手把标记清掉。

## 装完是什么

| 路径 | 是什么 |
|---|---|
| `/usr/local/bin/godusevpn` | 守护进程兼命令行,注册成 launchd 服务(`/Library/LaunchDaemons/com.maoyangui.godusevpn.plist`)开机自启 |
| `/Applications/godusevpn.app` | 图形界面,启动台里叫「佛跳墙」 |
| `/Library/Application Support/godusevpn/` | 设置、订阅缓存、`config.json`、日志、诊断包 |
| `~/Library/Application Support/godusevpn/ui.json` | 界面偏好(语言、主题) |

## 这一端的几件特殊事

- **隧道网卡是系统分配的 `utunN`** —— macOS 只认这个名字,自己起名内核起不来。
- **协议栈固定 gvisor**。系统协议栈在 macOS 上握不了手(内核收不到入站 TCP),设置页因此只给 gvisor 一个选项。
- **连接时会接管系统 DNS**,断开按连接前的原值还原;守护进程启动时也无条件还原一次,上次崩溃过也不会留下打不开网页的机器。
  不这么做的话 macOS 一直问路由器(192.168.x.1),而私网段按「局域网直通」在隧道之外,解析记录就泄漏给本地网络了,fake-ip 与 AAAA 屏蔽也一并失效。
- **没有菜单栏图标**。Wails 与 systray 都要占着 Cocoa 主线程,凑一起要走外部事件循环,没有真机盯着调不准。所以关掉窗口只是关界面,隧道照常在后台跑,从启动台再打开就回来了。
- **一键导入**:落地页的 `godusevpn://` 由 `.app` 的 Info.plist 向系统登记。

## 升级

「关于 → 检查更新 → 升级并重启」,弹一次系统的管理员密码框,守护进程与 `.app` 一起换掉。

## 卸载

```bash
sudo godusevpn uninstall            # 撤掉 launchd 服务
sudo rm -rf /Applications/godusevpn.app /usr/local/bin/godusevpn
```

## 验收

```bash
sudo sh deploy/macos-test.sh <订阅地址>
```

共 32 项;CI 每次提交都在 GitHub 的苹果芯片跑机上真跑一遍,另加一步把 `.app` 装进 `/Applications` 打开看它能不能活下来。

相关:[[日常使用]] · [[排障]]
