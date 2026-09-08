#!/bin/sh
# 打 OpenWrt 的 ipk:  mkipk.sh <版本> <架构名 amd64|arm64|armv7|mipsle|mips> <二进制路径> <输出 .ipk>
# 静态二进制不依赖 libc,Architecture 写 all,由 postinst 按 uname -m 核对架构,装错包会拒绝。
set -e
VER="$1"; ARCH="$2"; BIN="$3"; OUT="$4"
[ -n "$OUT" ] || { echo "用法: $0 <版本> <架构> <二进制> <输出.ipk>"; exit 2; }
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
case "$ARCH" in
  amd64) MACH="x86_64" ;;
  arm64) MACH="aarch64" ;;
  armv7) MACH="armv7l armv7 armv6l" ;;
  mipsle) MACH="mips mipsel" ;;   # 小端 MIPS 的 uname -m 也报 mips,再看字节序
  mips) MACH="mips" ;;
  *) echo "未知架构 $ARCH"; exit 2 ;;
esac
T=$(mktemp -d)
mkdir -p "$T/data/usr/bin" "$T/ctrl"
install -m 755 "$BIN" "$T/data/usr/bin/godusevpn"
cat > "$T/ctrl/control" <<EOF
Package: godusevpn
Version: $VER
Architecture: all
Maintainer: Maoyangui
Section: net
Priority: optional
Description: 佛跳墙 (godusevpn) - m-ui 客户端:TUN / 网关模式、规则分流、Web 面板
EOF
cat > "$T/ctrl/postinst" <<EOF
#!/bin/sh
m=\$(uname -m)
ok=0; for x in $MACH; do [ "\$m" = "\$x" ] && ok=1; done
if [ \$ok = 0 ]; then echo "这个包是给 $ARCH 的,本机是 \$m,请装对应架构的包"; exit 1; fi
[ -n "\$IPKG_INSTROOT" ] || /usr/bin/godusevpn install
exit 0
EOF
cat > "$T/ctrl/prerm" <<'EOF'
#!/bin/sh
[ -n "$IPKG_INSTROOT" ] || /usr/bin/godusevpn uninstall >/dev/null 2>&1
exit 0
EOF
chmod 755 "$T/ctrl/postinst" "$T/ctrl/prerm"
echo "2.0" > "$T/debian-binary"
TAROWN="--owner=0 --group=0 --numeric-owner"
tar --version 2>/dev/null | grep -q GNU || TAROWN=""   # busybox tar 没这些参数
tar $TAROWN -C "$T/ctrl" -czf "$T/control.tar.gz" .
tar $TAROWN -C "$T/data" -czf "$T/data.tar.gz" .
( cd "$T" && tar -czf "$OUT" ./debian-binary ./control.tar.gz ./data.tar.gz )
rm -rf "$T"
echo "打好 $OUT"
