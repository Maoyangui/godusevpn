#!/bin/sh
# Android TV 模拟器冒烟:装包 → 预授权 → 预置订阅 → 打开 → 只用遥控器按键(方向键 / 确定 / 返回)走一遍首页、节点面板、抽屉、设置,每步截图。
#   sh deploy/android-tv-test.sh "https://面板/sub/用户名" [APK 路径] [序列号(默认 emulator-5556)]
export MSYS_NO_PATHCONV=1
SUB="$1"; APK="$2"; SERIAL="${3:-emulator-5556}"
[ -n "$SUB" ] || { echo "用法: $0 <订阅地址> [APK] [序列号]"; exit 2; }
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SDK="${ANDROID_HOME:-D:/Android/Sdk}"
ADB="$SDK/platform-tools/adb.exe"; [ -x "$ADB" ] || ADB="$SDK/platform-tools/adb"
PKG=com.maoyangui.godusevpn
OUT="$ROOT/dist/android-tv"; mkdir -p "$OUT"
[ -n "$APK" ] || APK=$(ls "$ROOT"/android/app/build/outputs/apk/debug/*.apk | head -1)
a() { "$ADB" -s "$SERIAL" "$@"; }
w() { cygpath -m "$1" 2>/dev/null || echo "$1"; }
shot() { a exec-out screencap -p > "$OUT/$1.png"; echo "截图 $OUT/$1.png"; }
key() { for k in "$@"; do a shell input keyevent "$k"; sleep 0.6; done; }

echo "== 安装 $APK"
a install -r -g "$(w "$APK")" | tail -1
a shell appops set $PKG ACTIVATE_VPN allow
echo "== 预置订阅"
cat > "$OUT/settings.json" <<EOF
{"schema":2,"profiles":[{"id":"emu1","name":"测试","url":"$SUB"}],"activeProfile":"emu1","mode":"rule","tun":true,"tunStack":"mixed","strictRoute":true,"lanBypass":true,"mixedPort":2080,"remoteDns":"1.1.1.1","localDns":"223.5.5.5","fakeIp":true,"ipv6":false,"updateHours":6,"probeMinutes":3,"logLevel":"info","logDays":7,"clashPort":9090,"netMode":"local","dnsHijack":true,"webListen":""}
EOF
a push "$(w "$OUT/settings.json")" /data/local/tmp/settings.json >/dev/null
a shell "run-as $PKG sh -c 'mkdir -p files/conf files/data && cp /data/local/tmp/settings.json files/conf/settings.json && rm -f files/data/state.json'"
a shell am force-stop $PKG

echo "== 打开(横版首页)"
a shell am start -n "$PKG/.MainActivity" >/dev/null
sleep 8
shot 01-home
echo "== 方向键:焦点应落在连接按钮,再右移到面板卡"
key KEYCODE_DPAD_DOWN; shot 02-focus-power
key KEYCODE_DPAD_RIGHT KEYCODE_DPAD_DOWN KEYCODE_DPAD_DOWN; shot 03-focus-picker
echo "== 确定:打开节点面板,再返回"
key KEYCODE_DPAD_CENTER; sleep 2; shot 04-sheet
key KEYCODE_BACK; sleep 1
echo "== 上移到菜单键,确定开抽屉,进设置,返回"
key KEYCODE_DPAD_UP KEYCODE_DPAD_UP KEYCODE_DPAD_UP KEYCODE_DPAD_LEFT KEYCODE_DPAD_LEFT; shot 05-focus-menu
key KEYCODE_DPAD_CENTER; sleep 1.5; shot 06-drawer
key KEYCODE_DPAD_CENTER; sleep 2; shot 07-settings
key KEYCODE_BACK; sleep 1; shot 08-back-home
echo "== 首页再按返回:应该退到后台(应用不退出)"
key KEYCODE_BACK; sleep 1
a shell dumpsys activity activities | grep -m1 "topResumedActivity\|ResumedActivity" | tr -d '\r'
echo "-- logcat(引擎 / 崩溃):"; a logcat -d -s godusevpn:* AndroidRuntime:E chromium:E | tail -12
