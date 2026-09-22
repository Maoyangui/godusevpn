#!/bin/sh
# 佛跳墙 macOS 一键安装 / 升级。用管理员权限执行:
#   curl -fsSL https://raw.githubusercontent.com/Maoyangui/godusevpn/master/deploy/macos-install.sh | sudo sh
#   或指定版本:  ... | sudo sh -s -- v0.6.2
# 也可以在解开的 tar.gz 目录里直接 sudo sh install.sh(用旁边的文件,不联网)。
#
# 为什么走 curl 装:Gatekeeper 的隔离标记是浏览器下载时打上的,用 curl 拿到的文件没有这个标记,
# 因此不需要花钱买苹果的开发者证书,也不用让你去"系统设置 → 隐私与安全性"里点允许。
#
# 做的事:按芯片下载对应包 → 守护进程放 /usr/local/bin → 图形界面放 /Applications → 注册 launchd 服务并启动。
set -e
REPO="Maoyangui/godusevpn"
WANT="${1:-}"
[ "$(id -u)" = 0 ] || { echo "请用管理员权限运行(sudo sh macos-install.sh)"; exit 1; }
[ "$(uname -s)" = Darwin ] || { echo "这个脚本是给 macOS 用的;Linux 请用 deploy/install.sh"; exit 1; }

case "$(uname -m)" in
  arm64) ARCH=arm64 ;;          # 苹果芯片
  x86_64) ARCH=amd64 ;;         # 英特尔芯片
  *) echo "不支持的芯片: $(uname -m)"; exit 1 ;;
esac

HERE=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
if [ -n "$HERE" ] && [ -f "$HERE/godusevpn" ]; then
  echo "使用本地文件 $HERE"
  SRC="$HERE"
else
  if [ -z "$WANT" ]; then
    # 最新发布(含预发布):读发布页 Atom,第一条就是最新
    WANT=$(curl -fsSL "https://github.com/$REPO/releases.atom" | sed -n 's|.*/releases/tag/\(v[^"<]*\).*|\1|p' | head -1)
    [ -n "$WANT" ] || { echo "取不到最新版本号,请手动指定:sudo sh macos-install.sh vX.Y.Z"; exit 1; }
  fi
  VER="${WANT#v}"
  URL="https://github.com/$REPO/releases/download/v$VER/godusevpn-$VER-macos-$ARCH.tar.gz"
  TMP=$(mktemp -d)
  echo "下载 $URL"
  curl -fsSL "$URL" > "$TMP/pkg.tgz"
  tar -C "$TMP" -xzf "$TMP/pkg.tgz"
  SRC="$TMP"
fi

[ -f "$SRC/godusevpn" ] || { echo "包里没有 godusevpn,可能下错了架构"; exit 1; }

# 先停旧服务再换文件:升级不能走 uninstall,那是用户明确"卸载"语义,
# 会撤持久禁直连闸并恢复网卡 IPv6,在替换文件期间制造直连泄漏窗口。
# macOS 这边 Stop() 是先 needRoot 再 launchctl bootout:作业正在退出、或者 launchd 拒绝时会返回非零。
# 这里是 set -e —— m29 去掉 || true 之后,这一下就能让整个升级脚本无声退出,
# 用户看到的只是"跑完了但版本没变"。(m28 这一行本来是 uninstall,不是 stop。)
# 至于"还在不在跑",不用 status 去猜:覆盖正在运行的可执行文件时系统会返回 ETXTBSY,那才是真的还占着。
if [ -x /usr/local/bin/godusevpn ]; then /usr/local/bin/godusevpn stop >/dev/null 2>&1 || true; fi
mkdir -p /usr/local/bin
if ! install -m 755 "$SRC/godusevpn" /usr/local/bin/godusevpn; then
  echo "写不进 /usr/local/bin/godusevpn,升级中止。"
  echo "提示 Text file busy 的话说明旧的后台服务还占着这个文件,先手动停掉再重跑:"
  echo "  sudo /usr/local/bin/godusevpn stop"
  exit 1
fi

if [ -d "$SRC/godusevpn.app" ]; then
  rm -rf /Applications/godusevpn.app
  cp -R "$SRC/godusevpn.app" /Applications/godusevpn.app
  # 从压缩包解出来的文件本来就没有隔离标记,这一下是为了保险(比如有人先用浏览器下过)
  xattr -dr com.apple.quarantine /Applications/godusevpn.app 2>/dev/null || true
  echo "图形界面已装到 /Applications/godusevpn.app"
fi

/usr/local/bin/godusevpn install
echo
echo "图形界面:在启动台里打开「佛跳墙」"
echo "命令行:  godusevpn status | connect | disconnect | mode rule|global|direct | nodes | passwd | logs"
