#!/bin/sh
# 佛跳墙 Linux 真机验收(本机模式)。以 root 跑:
#   sudo sh linux-test.sh "https://面板/sub/用户名" [/usr/local/bin/godusevpn]
# 步骤:装服务 → 设订阅 → 连接 → 检查 TUN、策略路由、fake-ip、DNS 劫持、出口、IPv6 阻断、三态、直连真实 IP → 断开 → 清理检查。
# 在云主机上跑之前会先布一个"死人开关":150 秒内没跑完就自动停服务,防止 SSH 被切断后失联。
SUB="$1"
BIN="${2:-/usr/local/bin/godusevpn}"
[ -z "$SUB" ] && { echo "用法: $0 <订阅地址> [二进制路径]"; exit 2; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 2; }
fail=0
# check 名字 计数/布尔 说明:第二个参数大于 0 算通过
check() { if [ "${2:-0}" -gt 0 ] 2>/dev/null; then printf '[PASS] %s  %s\n' "$1" "$3"; else printf '[FAIL] %s  %s\n' "$1" "$3"; fail=$((fail+1)); fi; }
# 上次可能还连着(开机自动重连),先停干净再量"连接前"的基线
"$BIN" uninstall >/dev/null 2>&1; rm -f /var/lib/godusevpn/state.json; sleep 2
pub4() { curl -s -4 --max-time 15 https://api.ipify.org; }
pub6() { curl -s -6 --max-time 8 https://api6.ipify.org; }
resolve() { getent hosts "$1" | awk '{print $1; exit}'; }
# 比对状态要整词比:disconnected 里也含 connected,用 grep 会把"未连接"当成"已连接"
wait_status() { i=0; while [ $i -lt "$2" ]; do "$BIN" status 2>/dev/null | awk -v s="$1" '/^状态:/{if ($2==s) f=1} END{exit f?0:1}' && return 0; sleep 1; i=$((i+1)); done; return 1; }

echo "== 0. 环境"
uname -r; grep PRETTY_NAME /etc/os-release
before=$(pub4); echo "连接前公网 IPv4: $before"
real=$(resolve api.ipify.org); echo "api.ipify.org 真实 IP: $real"

echo "== 1. 安装"
"$BIN" uninstall >/dev/null 2>&1
"$BIN" install >/dev/null; check "服务运行" "$([ "$("$BIN" status | head -1 | grep -c running)" = 1 ] && echo 1 || echo 0)" "$("$BIN" status | head -1)"
i=0; while [ $i -lt 10 ] && ! curl -s --max-time 2 http://127.0.0.1:9800/api/ping | grep -q version; do sleep 1; i=$((i+1)); done
check "面板可达" "$(curl -s --max-time 5 http://127.0.0.1:9800/api/ping | grep -c version)" "$(curl -s --max-time 5 http://127.0.0.1:9800/api/ping)"

echo "== 2. 订阅与连接"
p=$("$BIN" profile "$SUB" 2>&1); check "订阅拉取" "$(echo "$p" | grep -c '个节点')" "$(echo "$p" | head -1)"
( sleep 150; systemctl stop godusevpn 2>/dev/null; rm -f /var/lib/godusevpn/state.json ) >/dev/null 2>&1 &
deadman=$!
"$BIN" connect >/dev/null
if wait_status connected 60; then check "进入 connected" 1 "$("$BIN" status | sed -n 2p)"; else check "进入 connected" 0 "$("$BIN" status | sed -n 2p)"; fi

echo "== 3. 网络栈"
check "TUN 网卡存在" "$(ip -4 addr show godusevpn 2>/dev/null | grep -c 'inet ')" "$(ip -4 addr show godusevpn 2>/dev/null | grep 'inet ' | awk '{print $2}')"
check "公网地址的路由走 TUN" "$(ip route get 104.26.12.205 2>/dev/null | grep -c 'dev godusevpn')" "$(ip route get 104.26.12.205 2>/dev/null | head -1)"
check "本机地址回包走主表(SSH 不断)" "$(ip rule show | grep -c 'from .* lookup main')" "$(ip rule show | grep 'lookup main' | grep -v '^0:' | head -2 | tr '\n' ' ')"
g=$(resolve www.google.com); check "代理域名得到 fake-ip(198.18/15)" "$(echo "$g" | grep -c '^198\.1[89]\.')" "$g"
b=$(resolve www.baidu.com); check "国内域名是真实 IP" "$([ -n "$b" ] && echo "$b" | grep -vc '^198\.1[89]\.' || echo 0)" "$b"
a6=$(getent ahostsv6 www.google.com 2>/dev/null | awk '{print $1}' | grep -v '^::ffff' | head -1); check "AAAA 为空(禁 IPv6)" "$([ -z "$a6" ] && echo 1 || echo 0)" "$a6"

echo "== 4. 出口"
now=$(pub4); check "规则模式出口 IP 变了" "$([ -n "$now" ] && [ "$now" != "$before" ] && echo 1 || echo 0)" "before=$before now=$now"
v6=$(pub6); check "IPv6 出网被阻断" "$([ -z "$v6" ] && echo 1 || echo 0)" "v6=$v6"
via=$(curl -s -4 --max-time 15 --resolve "api.ipify.org:443:$real" https://api.ipify.org); check "直连真实 IP 也走代理" "$([ -n "$via" ] && [ "$via" != "$before" ] && echo 1 || echo 0)" "real=$real got=$via"
lat=$("$BIN" test 2>&1); check "延迟测试" "$(echo "$lat" | grep -c ' ms')" "$lat"

echo "== 5. 模式切换"
"$BIN" mode direct >/dev/null; sleep 2; d=$(pub4); check "直连模式出口回到本机" "$([ "$d" = "$before" ] && echo 1 || echo 0)" "direct=$d"
"$BIN" mode global >/dev/null; sleep 2; gl=$(pub4); check "全局模式出口是节点" "$([ -n "$gl" ] && [ "$gl" != "$before" ] && echo 1 || echo 0)" "global=$gl"
"$BIN" mode rule >/dev/null
check "节点列表" "$("$BIN" nodes | grep -c '^\*')" "$("$BIN" nodes | tr '\n' ' ')"

echo "== 6. 断开与清理"
kill $deadman 2>/dev/null; pkill -f "sleep 150" 2>/dev/null
"$BIN" disconnect >/dev/null; sleep 3
check "断开后 TUN 网卡消失" "$([ -z "$(ip link show godusevpn 2>/dev/null)" ] && echo 1 || echo 0)" ""
check "断开后策略路由撤掉" "$([ "$(ip rule show | grep -c '^5000:')" = 0 ] && echo 1 || echo 0)" ""
after=$(pub4); check "断开后出口恢复" "$([ "$after" = "$before" ] && echo 1 || echo 0)" "after=$after"
echo
if [ $fail = 0 ]; then echo "全部通过"; else echo "$fail 项失败"; echo "== 内核日志"; "$BIN" logs 40 core; echo "== 服务日志"; "$BIN" logs 20; fi
exit $fail
