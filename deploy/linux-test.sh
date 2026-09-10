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

# 节点服务器必须直连:内核自己去连节点(定时测速每轮都要连一遍)如果被自己的隧道接住,
# 就会按当前模式再转出去,绕成"本机 → 隧道 → 当前节点 → 目标节点" —— 白绕一跳、流量算两份、
# 测出来的延迟也不是节点的真实延迟。真机上出现过(anytls 这类 TCP 节点),这里长期盯着。
# nodepairs 订阅里所有节点的"地址:端口"(诊断里现成的)。比对要连端口一起比:
# 只按地址比会把远程 DoH(1.1.1.1:443,本来就该经代理走)也算进来。
nodepairs() { "$BIN" diag 2>/dev/null | grep -o '"[0-9]\{1,3\}\.[0-9.]*:[0-9]\{1,5\}"' | tr -d '"' | grep -v '^127\.' | sort -u; }
loopcheck() {
  pairs=$(nodepairs)
  [ -z "$pairs" ] && { echo 0; return; }
  lines=$("$BIN" logs 400 core 2>/dev/null | grep "outbound connection to" | grep -v "outbound/direct")
  n=0
  for p in $pairs; do
    c=$(printf '%s\n' "$lines" | grep -cF "outbound connection to $p")
    n=$((n + c))
  done
  echo "$n"
}
n=$(loopcheck)
check "节点服务器没被套一层代理" "$([ "${n:-0}" -eq 0 ] && echo 1 || echo 0)" "绕圈的连接 ${n:-0} 条"
# 再主动打一发:朝节点服务器建个 TCP 连接,看内核把它判给谁。判给 direct 才对 ——
# 上面那条只能证明"这一轮没发生",这条能证明规则真的命中(测速用的协议不一定是 TCP,不主动打就测不到)。
np=$(nodepairs | head -1)
if [ -n "$np" ]; then
  # 打一发就走的日志不一定马上落到能读到的那一段(内核日志很吵),多试几次再判失败
  hit=0; i=0
  while [ $i -lt 3 ] && [ "$hit" -eq 0 ]; do
    nc -w 3 -z "${np%:*}" "${np##*:}" >/dev/null 2>&1
    sleep 2
    hit=$("$BIN" logs 400 core 2>/dev/null | grep "outbound/direct" | grep -cF "outbound connection to $np")
    i=$((i+1))
  done
  check "朝节点服务器发起的连接判给了直连" "$hit" "$np(试了 $i 次)"
fi

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
