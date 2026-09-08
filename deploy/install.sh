#!/bin/sh
# 佛跳墙 Linux 一键安装 / 升级。root 执行:
#   curl -fsSL https://raw.githubusercontent.com/Maoyangui/godusevpn/master/deploy/install.sh | sh
#   或指定版本:  ... | sh -s -- v0.5.0
# 也可以在解开的 tar.gz 目录里直接 sh install.sh(用旁边的 godusevpn 二进制,不联网)。
# 做的事:按架构下载对应包 → 放到 /usr/local/bin(Entware 放 /opt/bin)→ godusevpn install(注册自启并启动,打印面板地址与密码)。
set -e
REPO="Maoyangui/godusevpn"
WANT="${1:-}"
[ "$(id -u)" = 0 ] || { echo "请用 root 运行(sudo sh install.sh)"; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7*|armv6*|armhf) ARCH=armv7 ;;
  mipsel|mipsle) ARCH=mipsle ;;
  mips) ARCH=mips ;;
  *) echo "不支持的架构: $(uname -m)"; exit 1 ;;
esac

BIN_DIR=/usr/local/bin
if [ -f /etc/openwrt_release ]; then BIN_DIR=/usr/bin; fi   # OpenWrt 的 PATH 里没有 /usr/local/bin
if [ -f /opt/etc/entware_release ] || { [ -x /opt/bin/opkg ] && [ ! -f /etc/openwrt_release ]; }; then BIN_DIR=/opt/bin; fi
mkdir -p "$BIN_DIR"

HERE=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
if [ -n "$HERE" ] && [ -f "$HERE/godusevpn" ]; then
  echo "使用本地文件 $HERE/godusevpn"
  SRC="$HERE/godusevpn"
else
  fetch() { if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"; else wget -qO- "$1"; fi; }
  if [ -z "$WANT" ]; then
    # 最新发布(含预发布):读发布页 Atom,第一条就是最新
    WANT=$(fetch "https://github.com/$REPO/releases.atom" | sed -n 's|.*/releases/tag/\(v[^"<]*\).*|\1|p' | head -1)
    [ -n "$WANT" ] || { echo "取不到最新版本号,请手动指定:sh install.sh vX.Y.Z"; exit 1; }
  fi
  VER="${WANT#v}"
  URL="https://github.com/$REPO/releases/download/v$VER/godusevpn-$VER-linux-$ARCH.tar.gz"
  TMP=$(mktemp -d)
  echo "下载 $URL"
  fetch "$URL" > "$TMP/pkg.tgz"
  tar -C "$TMP" -xzf "$TMP/pkg.tgz"
  SRC="$TMP/godusevpn"
fi

if [ -x "$BIN_DIR/godusevpn" ]; then "$BIN_DIR/godusevpn" stop >/dev/null 2>&1 || true; fi
install -m 755 "$SRC" "$BIN_DIR/godusevpn"
"$BIN_DIR/godusevpn" install
echo
echo "常用命令: godusevpn status | connect | disconnect | mode rule|global|direct | nodes | passwd | logs"
