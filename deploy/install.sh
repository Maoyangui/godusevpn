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

# 旧版二进制的 stop 在"服务本来就没跑"时未必返回 0:0.6.25-m28 及更早的 Linux 版 Stop() 就是
# 一句 systemctl / init.d stop 的原样透传,OpenWrt 的 init 脚本、systemd 里 unit 已不存在,都会非零。
# 这里是 set -e —— m29 去掉 || true 之后,这种完全正常的情况会让整个升级脚本无声退出,
# 用户看到的只是"跑完了但版本没变"。
#
# 也不要拿 `godusevpn status` 去判"还在不在跑":Entware(梅林这类路由器)那边的 QueryStatus 是
# `pidof godusevpn`,而此刻正在跑这条命令的**就是** godusevpn 自己 —— 它永远报 running,
# 升级会被永久挡死。改用一个不会自指的判据:直接写文件,Linux / macOS 在覆盖正在运行的可执行文件时
# 会返回 ETXTBSY(Text file busy),那才是真的"还占着"。
if [ -x "$BIN_DIR/godusevpn" ]; then "$BIN_DIR/godusevpn" stop >/dev/null 2>&1 || true; fi
if ! install -m 755 "$SRC" "$BIN_DIR/godusevpn"; then
  echo "写不进 $BIN_DIR/godusevpn,升级中止。"
  echo "提示 Text file busy 的话说明旧的后台服务还占着这个文件,先手动停掉再重跑:"
  echo "  $BIN_DIR/godusevpn stop"
  exit 1
fi
"$BIN_DIR/godusevpn" install
echo
echo "常用命令: godusevpn status | connect | disconnect | mode rule|global|direct | nodes | passwd | logs"
