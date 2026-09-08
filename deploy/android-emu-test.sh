#!/bin/sh
# 模拟器冒烟(Git Bash / Linux 都能跑):装 APK → 预授权 VPN → 预置订阅 → 打开界面截图 → 点连接 → 看状态与 VPN 网卡 → 截图。
#   sh deploy/android-emu-test.sh "https://面板/sub/用户名" [APK 路径] [序列号]
export MSYS_NO_PATHCONV=1
SUB="$1"; APK="$2"; SERIAL="${3:-emulator-5554}"
[ -n "$SUB" ] || { echo "用法: $0 <订阅地址> [APK] [序列号]"; exit 2; }
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SDK="${ANDROID_HOME:-D:/Android/Sdk}"
ADB="$SDK/platform-tools/adb.exe"; [ -x "$ADB" ] || ADB="$SDK/platform-tools/adb"
PKG=com.maoyangui.godusevpn
OUT="$ROOT/dist/android-emu"; mkdir -p "$OUT"
[ -n "$APK" ] || APK=$(ls "$ROOT"/android/app/build/outputs/apk/debug/*.apk | head -1)
a() { "$ADB" -s "$SERIAL" "$@"; }
# Git Bash 下给 adb.exe 的本地路径要转成 Windows 写法
w() { cygpath -m "$1" 2>/dev/null || echo "$1"; }
shot() { a exec-out screencap -p > "$OUT/$1.png"; echo "截图 $OUT/$1.png"; }

echo "== 安装 $APK"
a install -r -g "$(w "$APK")" | tail -1
a shell appops set $PKG ACTIVATE_VPN allow
a shell pm grant $PKG android.permission.POST_NOTIFICATIONS 2>/dev/null

echo "== 预置订阅"
cat > "$OUT/settings.json" <<EOF
{"schema":2,"profiles":[{"id":"emu1","name":"测试","url":"$SUB"}],"activeProfile":"emu1","mode":"rule","tun":true,"tunStack":"mixed","strictRoute":true,"lanBypass":true,"mixedPort":2080,"remoteDns":"1.1.1.1","localDns":"223.5.5.5","fakeIp":true,"ipv6":false,"updateHours":6,"probeMinutes":3,"logLevel":"info","logDays":7,"clashPort":9090,"netMode":"local","dnsHijack":true,"webListen":""}
EOF
a push "$(w "$OUT/settings.json")" /data/local/tmp/settings.json >/dev/null
# 顺手清掉上次的 state.json:不然守护进程一起来就按"上次已连接"自动连,下面这一下点击反而把它断了
a shell "run-as $PKG sh -c 'mkdir -p files/conf files/data && cp /data/local/tmp/settings.json files/conf/settings.json && rm -f files/data/state.json && ls -l files/conf'"
a shell am force-stop $PKG

echo "== 打开界面"
a shell am start -n "$PKG/.MainActivity" >/dev/null
sleep 7
shot 01-home

echo "== 点连接(首页大按钮)"
SIZE=$(a shell wm size | sed 's/.*: //' | tr -d '\r')
W=${SIZE%x*}; H=${SIZE#*x}
a shell input tap $((W / 2)) $((H * 36 / 100))
# 第一次要下规则集(走代理),给足时间;看到 connected / error 就停
svclog() { a shell "run-as $PKG sh -c 'tail -${1:-8} files/data/logs/service.log'" | tr -d '\r'; }
i=0
while [ $i -lt 30 ]; do
  sleep 2; i=$((i + 1))
  case "$(svclog 3 | grep -o '状态 → [a-z]*' | tail -1)" in *connected|*error) break ;; esac
done
sleep 3
shot 02-after-connect

echo "== 抽屉与设置页(看页面在 WebView 里能不能正常渲染)"
a shell input tap $((W * 6 / 100)) $((H * 8 / 100)); sleep 2; shot 03-drawer
a shell input tap $((W * 20 / 100)) $((H * 17 / 100)); sleep 2; shot 04-settings
a shell input keyevent KEYCODE_BACK; sleep 1

echo "== 状态"
svclog 10
echo "-- VPN 网卡:"; a shell ip addr show tun0 2>/dev/null | grep "inet "
echo "-- dumpsys:"; a shell dumpsys connectivity | grep -i "vpn\|tun0" | grep -v "NetworkAgentInfo\|NetworkRequest" | head -4
echo "-- 内核日志(非 TRACE 尾部):"; a shell "run-as $PKG sh -c 'tail -40 files/data/logs/core.log'" | tr -d '\r' | grep -v TRACE | tail -12
echo "-- logcat(引擎 / 崩溃):"; a logcat -d -s godusevpn:* AndroidRuntime:E | tail -20
