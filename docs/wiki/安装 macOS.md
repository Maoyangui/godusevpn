# 安装 · macOS

**系统要求:macOS 13(Ventura)或更高**,苹果芯片与英特尔芯片各有一个包(Go 1.27 编出来的程序就是 13 起步)。

## 装

两种装法,都要输一次开机密码(管理员)。

### 不用终端:双击安装

1. 到 [Releases](https://github.com/Maoyangui/godusevpn/releases/latest) 下载对应芯片的包:苹果芯片 `godusevpn-<版本>-macos-arm64.tar.gz`,英特尔 `-amd64`(苹果菜单 →「关于本机」能看到是哪种)。
2. 双击解开,打开文件夹里的「安装说明」(浏览器里看,中英双语),双击「安装佛跳墙.command」。
3. 第一次会被 macOS 拦下 —— 佛跳墙没有苹果开发者证书,这一步绕不过去,但只拦这一个文件、只拦一次:
   - **macOS 15 Sequoia 及更新**:弹窗点「完成」→「系统设置 → 隐私与安全性」→ 拉到「安全性」,点「仍要打开」→ 输密码或触控 ID → 再点「仍要打开」。
   - **macOS 13 / 14**:按住 Control 点「安装佛跳墙.command」(或右键)→「打开」→ 弹窗里再点「打开」。
4. 在弹出的终端里输开机密码(**输入时不显示任何字符**,输完回车)。看到「✅ 安装完成」佛跳墙会自动打开。

放行之后脚本先清掉整个文件夹的隔离标记,再跑 `sudo sh install.sh`,装好的程序不会再被拦。包的芯片和本机不符时会直接提示该下哪个(按硬件判断,终端开着 Rosetta 也认得出苹果芯片)。

### 用终端:一行装

不会被拦,也不用放行:

```bash
curl -fsSL https://raw.githubusercontent.com/Maoyangui/godusevpn/master/deploy/macos-install.sh | sudo sh
```

已经解开了包的话,在那个文件夹里 `sudo sh install.sh` 也行。

### 为什么 curl 那条不会被拦

Gatekeeper 的「来自互联网」隔离标记是**浏览器下载时**打上的,curl 拿到的文件没有这个标记。
所以不用买苹果开发者证书,也不用你去「系统设置 → 隐私与安全性」里点允许。
用浏览器下的包,`install.sh` 也会把装好的守护进程与 `.app` 上的标记清掉。

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
- **连接时会接管系统 DNS**,断开按连接前的原值还原;守护进程启动时也会还原一次,上次崩溃过也不会留下打不开网页的机器 —— 除非上次是在严格全局模式下连着的:那时先保留接管、等闸和隧道重建,否则那段时间的解析会走物理网卡。这时点「断开」或用「恢复网络」都会还原。
  不这么做的话 macOS 一直问路由器(192.168.x.1),而私网段按「局域网直通」在隧道之外,解析记录就泄漏给本地网络了,fake-ip 与 AAAA 屏蔽也一并失效。
- **没有菜单栏图标**。Wails 与 systray 都要占着 Cocoa 主线程,凑一起要走外部事件循环,没有真机盯着调不准。所以关掉窗口只是关界面,隧道照常在后台跑,从启动台再打开就回来了。
- **一键导入**:落地页的 `godusevpn://` 由 `.app` 的 Info.plist 向系统登记。

## 升级

「关于 → 检查更新 → 升级并重启」,弹一次系统的管理员密码框,守护进程与 `.app` 一起换掉。

## 卸载

双击安装包里的「卸载佛跳墙.command」(输 y 确认、再输开机密码);或者在终端里:

```bash
sudo godusevpn uninstall            # 撤掉 launchd 服务
sudo rm -rf /Applications/godusevpn.app /usr/local/bin/godusevpn
```

## 验收

```bash
sudo sh deploy/macos-test.sh <订阅地址>
```

共 53 项;CI 每次提交都在 GitHub 的苹果芯片跑机上真跑一遍,另加一步把 `.app` 装进 `/Applications` 打开看它能不能活下来,再拿打上浏览器隔离标记的安装包走一遍双击安装 / 卸载。

相关:[[日常使用]] · [[排障]]
