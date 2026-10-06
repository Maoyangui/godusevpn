#!/bin/sh
# 佛跳墙 macOS 双击安装。打包时改名为「安装佛跳墙.command」,和 install.sh、godusevpn、godusevpn.app 放在同一个文件夹。
#
# 给不用终端的人:访达里双击,系统会打开「终端」执行这个脚本,只需要输一次开机密码。
# 浏览器下载的包带隔离标记,第一次双击会被 Gatekeeper 拦一次(没有苹果开发者证书,绕不过去),
# 放行方法写在同目录的「安装说明.html」里;放行之后这里把整个文件夹的隔离标记清掉,装好的程序不会再被拦。
cd "$(dirname "$0")" || exit 1
DIR=$(pwd)

# 窗口留着给人看结果:是终端里双击打开的才等回车(CI 里跑、或者输入被重定向时不等)
finish() {
  echo
  if [ -t 0 ]; then printf '按回车键关闭窗口…'; read -r _; fi
  exit "$1"
}

clear 2>/dev/null
echo "=============================="
echo "   佛跳墙 · 在这台 Mac 上安装"
echo "=============================="
echo
echo "会装两样东西:"
echo "  · 后台服务(开机自动运行,负责建隧道)"
echo "  · 图形界面(启动台 / 聚焦搜索里叫「佛跳墙」,「应用程序」文件夹里是 godusevpn)"
echo

if [ ! -f "$DIR/godusevpn" ] || [ ! -f "$DIR/install.sh" ]; then
  echo "这个文件夹里缺文件(godusevpn / install.sh)。"
  echo "请把下载的压缩包重新双击解开,在解出来的文件夹里双击「安装佛跳墙.command」。"
  finish 1
fi

# 浏览器下载时打上的隔离标记:不清掉的话,装好的程序第一次打开还会再被拦一次。
# 文件是当前用户解压出来的,自己就能清,不用管理员权限。
xattr -dr com.apple.quarantine "$DIR" 2>/dev/null || true

# 开着旧版界面时先退出:不然装完 open -a 只是把还在跑的旧进程调到前台,看到的还是旧界面。
# 只退界面,后台服务和隧道照常(关窗口本来就不断开);没在跑就不发,免得反而把它拉起来。
if pgrep -f "/Applications/godusevpn.app/Contents/MacOS/godusevpn" >/dev/null 2>&1; then
  osascript -e 'quit app "godusevpn"' >/dev/null 2>&1 || true
fi

echo "接下来要输入这台 Mac 的开机密码(管理员账户的密码)。"
echo "输入时屏幕上不会出现任何字符,连星号也没有 —— 这是正常的,输完直接按回车。"
echo
if ! sudo -p "开机密码:" sh "$DIR/install.sh"; then
  echo
  echo "❌ 安装没有完成,原因见上面几行。"
  echo "   · 提示 is not in the sudoers file:当前账户不是管理员,换管理员账户登录后再装;"
  echo "   · 提示芯片不符:下载和这台 Mac 对应的包(苹果芯片 arm64,英特尔 amd64);"
  echo "   · 别的情况:把这个窗口截图发给我们。"
  finish 1
fi

echo
echo "✅ 安装完成。"
if [ -d /Applications/godusevpn.app ] && [ -z "${GODUSEVPN_NO_OPEN:-}" ]; then
  echo "   正在打开佛跳墙;以后在启动台或聚焦搜索(⌘ 空格)里找「佛跳墙」就行。"
  open -a /Applications/godusevpn.app 2>/dev/null || true
fi
echo "   这个窗口可以关掉了。"
finish 0
