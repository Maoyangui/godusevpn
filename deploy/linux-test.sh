#!/bin/sh
# 佛跳墙 Linux 真机验收(本机模式)。以 root 跑:
#   sudo sh linux-test.sh "https://面板/sub/用户名" [/usr/local/bin/godusevpn]
# 步骤:装服务 → 设订阅 → 连接 → 检查 TUN、策略路由、fake-ip、DNS 劫持、出口、IPv6 阻断、三态、直连真实 IP → 断开 → 清理检查。
# 在云主机上跑之前会先布一个"死人开关":420 秒内没跑完就自动断开、停服务、撤闸,防止 SSH 被切断后失联,
# 也防止闸留在机器上把跑机的代理一起断掉(作业会挂到超时)。
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
# 连上以后默认路由已经指向隧道,物理网卡要在这之前记下来;绑它发直连是全局禁直连闸的实测
defif=$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')
dip=$(ip -4 addr show "$defif" 2>/dev/null | awk '/inet /{print $2; exit}' | cut -d/ -f1)
# nft 的闸按标记放行本服务,root 只放行它回应入站连接的包;直连探测普通用户和 root 各试一次。跑机上是 uid 1001,别的机器退到 nobody
U=$(id -un 1001 2>/dev/null || id -un 1000 2>/dev/null || true); [ "$U" = root ] && U=""; [ -z "$U" ] && id nobody >/dev/null 2>&1 && U=nobody # 跑机上 runner 是 1001
echo "连接前默认出口网卡: ${defif:-?} ${dip:-?};直连探测用的普通用户: ${U:-无}"

echo "== 1. 安装"
"$BIN" uninstall >/dev/null 2>&1
"$BIN" install >/dev/null; check "服务运行" "$([ "$("$BIN" status | head -1 | grep -c running)" = 1 ] && echo 1 || echo 0)" "$("$BIN" status | head -1)"
i=0; while [ $i -lt 10 ] && ! curl -s --max-time 2 http://127.0.0.1:9800/api/ping | grep -q version; do sleep 1; i=$((i+1)); done
check "面板可达" "$(curl -s --max-time 5 http://127.0.0.1:9800/api/ping | grep -c version)" "$(curl -s --max-time 5 http://127.0.0.1:9800/api/ping)"

echo "== 2. 订阅与连接"
p=$("$BIN" profile "$SUB" 2>&1); check "订阅拉取" "$(echo "$p" | grep -c '个节点')" "$(echo "$p" | head -1)"
( sleep 420; "$BIN" disconnect >/dev/null 2>&1; systemctl stop godusevpn 2>/dev/null; "$BIN" guard clear >/dev/null 2>&1 || nft delete table inet godusevpn_guard 2>/dev/null; rm -f /var/lib/godusevpn/state.json ) >/dev/null 2>&1 &
deadman=$!
"$BIN" connect >/dev/null
if wait_status connected 60; then check "进入 connected" 1 "$("$BIN" status | sed -n 2p)"; else check "进入 connected" 0 "$("$BIN" status | sed -n 2p)"; fi

echo "== 3. 网络栈"
check "TUN 网卡存在" "$(ip -4 addr show godusevpn 2>/dev/null | grep -c 'inet ')" "$(ip -4 addr show godusevpn 2>/dev/null | grep 'inet ' | awk '{print $2}')"
check "公网地址的路由走 TUN" "$(ip route get 104.26.12.205 2>/dev/null | grep -c 'dev godusevpn')" "$(ip route get 104.26.12.205 2>/dev/null | head -1)"
# 外部连进来的连接(SSH、面板)回包走主表:连接跟踪打标记 + 按标记的策略路由;只把套接字绑在物理网卡地址上的
# (WebRTC 枚举本机地址就这么干)不再绕开隧道 —— 以前按"from 本机地址"放行,规则 / 普通全局下会露真实 IP
check "外部连进来的连接回包走主表:标记规则(SSH 不断)" "$(ip rule show | grep -c 'fwmark 0x10000000/0x10000000 lookup main')" "$(ip rule show | grep -E '^5000:' | head -2 | tr '\n' ' ')"
check "外部连进来的连接回包走主表:nft 按连接方向打标记" "$(nft list table ip godusevpn_reply 2>/dev/null | grep -c 'ct direction reply meta mark set')" "$(nft list table ip godusevpn_reply 2>/dev/null | grep -c 'tcp dport @inbound') 条兜底规则"
check "带回包标记的包走物理网卡" "$(ip route get 1.1.1.1 mark 0x10000000 2>/dev/null | grep -c "dev $defif")" "$(ip route get 1.1.1.1 mark 0x10000000 2>/dev/null | head -1)"
check "只绑物理网卡地址的不再绕开隧道" "$([ -n "$dip" ] && ip route get 1.1.1.1 from "$dip" 2>/dev/null | grep -c 'dev godusevpn' || echo 0)" "$(ip route get 1.1.1.1 from "$dip" 2>/dev/null | head -1)"
g=$(resolve www.google.com); check "代理域名得到 fake-ip(198.18/15)" "$(echo "$g" | grep -c '^198\.1[89]\.')" "$g"
b=$(resolve www.baidu.com); check "国内域名是真实 IP" "$([ -n "$b" ] && echo "$b" | grep -vc '^198\.1[89]\.' || echo 0)" "$b"
a6=$(getent ahostsv6 www.google.com 2>/dev/null | awk '{print $1}' | grep -v '^::ffff' | head -1); check "AAAA 为空(禁 IPv6)" "$([ -z "$a6" ] && echo 1 || echo 0)" "$a6"

# 节点服务器必须直连:内核自己去连节点(定时测速每轮都要连一遍)如果被自己的隧道接住,
# 就会按当前模式再转出去,绕成"本机 → 隧道 → 当前节点 → 目标节点" —— 白绕一跳、流量算两份、
# 测出来的延迟也不是节点的真实延迟。真机上出现过(anytls 这类 TCP 节点),这里长期盯着。
# nodepairs 订阅里所有节点的"地址:端口"(诊断里现成的)。比对要连端口一起比:
# 只按地址比会把远程 DoH(1.1.1.1:443,本来就该经代理走)也算进来。
# 排掉回环和 0.0.0.0:面板的监听地址(Linux 上是 0.0.0.0:9800)也会以 "ip:port" 的样子出现在诊断里,
# 它排在所有节点 IP 前面,拿它当节点地址去打,那条"判给了直连"的检查就永远红。
nodepairs() { "$BIN" diag 2>/dev/null | grep -o '"[0-9]\{1,3\}\.[0-9.]*:[0-9]\{1,5\}"' | tr -d '"' | grep -v '^127\.' | grep -v '^0\.0\.0\.0:' | sort -u; }
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

echo "== 3.7 全局禁直连(nftables 闸:普通用户绑物理网卡直连被拦,经隧道照常;切回规则模式闸表清空)"
# Linux 的闸是 nftables 的一张 inet 表,由守护进程装、只在严格全局模式下存在。这里不验"程序会不会拒绝服务",
# 验的是闸真的拦得住:拿普通用户绑物理网卡实打实打一次。按网卡名绑(SO_BINDTODEVICE,5.7 起普通用户也能用):
# 只绑地址的已经不会从物理网卡出去了(见上面"只绑物理网卡地址的不再绕开隧道"),要强制走物理网卡得按网卡绑 ——
# NetworkManager 按网卡做的联网检测就是这种。
# 正控制:规则模式下(闸没开)普通用户绑物理网卡的直连必须是通的(2xx / 3xx),否则探测本身跑不通,
# 下面"被拦"的断言就没有意义 —— 那种情况按失败报(见下面的 check),而不是让一个空串冒充"被拦"。
direct_ok=0
if [ -n "$U" ] && [ -n "$defif" ]; then
  d0=$(su -s /bin/sh "$U" -c "curl -s --interface $defif -m 6 -o /dev/null -w '%{http_code}' http://1.1.1.1/cdn-cgi/trace" 2>/dev/null || true)
  # 明文 http 会被 1.1.1.1 跳转到 https(301),那也是"通了";闸拦住时 curl 连不上,状态码是 000
  case "$d0" in 2*|3*) direct_ok=1;; esac
fi
# 正控制没过就按失败报,不再静默跳过:这是唯一一条证明 nft 闸"真拦得住"的断言,跳过它作业照样是绿的
check "正控制:规则模式下普通用户绑物理网卡直连是通的" "$direct_ok" "http=${d0:-000}(用户 ${U:-无} 网卡 ${defif:-无} ${dip:-无})"
gres=$("$BIN" mode global 2>&1); grc=$?
check "切到严格全局模式被接受" "$([ "$grc" = 0 ] && echo 1 || echo 0)" "$gres"
sleep 2
check "全局模式下连接仍在" "$(wait_status connected 15 && echo 1 || echo 0)" "$("$BIN" status | sed -n 2p)"
rules=$(nft list table inet godusevpn_guard 2>/dev/null)
check "nft 闸表存在且有 drop 规则" "$(echo "$rules" | grep -c 'drop')" "$(echo "$rules" | grep -c .) 行"
if [ "$direct_ok" = 1 ]; then
  d=$(su -s /bin/sh "$U" -c "curl -s --interface $defif -m 6 -o /dev/null -w '%{http_code}' http://1.1.1.1/cdn-cgi/trace" 2>/dev/null || true)
  case "$d" in 2*|3*) blocked=0;; *) blocked=1;; esac # 2xx/3xx 都是通了;被拦是 000
  check "普通用户绑物理网卡的直连被拦" "$blocked" "http=${d:-000}(网卡 $defif 用户 $U)"
fi
tc=$(curl -s -m 15 -o /dev/null -w '%{http_code}' https://1.1.1.1/cdn-cgi/trace 2>/dev/null || true)
check "经隧道照常" "$([ "$tc" = "200" ] && echo 1 || echo 0)" "http=${tc:-000}"
# 闸按标记放行本服务、不再整个放行 root:root 绑物理网卡的直连(NetworkManager 的联网检测就是这种)也要被拦
check "闸按标记放行本服务" "$(echo "$rules" | grep -c 'meta mark 0x676f0000')" ""
bare=$(echo "$rules" | grep 'skuid' | grep -v 'reply' | grep -v 'dport 67' | grep -v '168.63.129.16' | head -1)
check "闸不再整个放行 root" "$([ -z "$bare" ] && echo 1 || echo 0)" "$bare"
check "本机模式也有 forward 链(Docker / 虚拟机 NAT / 热点)" "$(echo "$rules" | grep -c 'hook forward')" ""
check "拦 UPnP 发现 / NAT-PMP" "$(echo "$rules" | grep -c '1900')" ""
if [ "$direct_ok" = 1 ]; then
  r0=$(curl -s --interface "$defif" -m 6 -o /dev/null -w '%{http_code}' http://1.1.1.1/cdn-cgi/trace 2>/dev/null || true)
  case "$r0" in 2*|3*) rb=0;; *) rb=1;; esac
  check "root 绑物理网卡的直连也被拦" "$rb" "http=${r0:-000}"
fi
# DNS:发往 53 的包按策略路由进隧道,连着时直接问上游 DNS 照样能解析(以前局域网直通把它排在隧道外、又被闸拦下);
# 服务停着的空档里闸把它丢掉,一条都不出物理网卡
check "DNS 进隧道的策略路由" "$(ip rule show | grep -c 'dport 53 lookup 5053')" "$(ip rule show | grep 5053 | tr '\n' ';')"
ns=$(awk '/^nameserver/{print $2}' /run/systemd/resolve/resolv.conf /etc/resolv.conf 2>/dev/null | grep -v '^127\.' | grep -v ':' | head -1)
if [ -n "$ns" ] && [ -n "$U" ] && command -v dig >/dev/null 2>&1; then
  a=$(su -s /bin/sh "$U" -c "dig +short +time=4 +tries=1 @$ns example.com A" 2>/dev/null | grep -E '^[0-9.]+$' | head -1)
  check "连着时直接问 $ns 照样能解析(进了隧道)" "$([ -n "$a" ] && echo 1 || echo 0)" "${a:-无应答}"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop godusevpn; sleep 2
    g=$(su -s /bin/sh "$U" -c "dig +short +time=3 +tries=1 @$ns example.com A" 2>/dev/null | grep -E '^[0-9.]+$' | head -1)
    check "服务停着的空档里 DNS 被拦" "$([ -z "$g" ] && echo 1 || echo 0)" "${g:-无应答}"
    check "服务停着闸仍在" "$(nft list table inet godusevpn_guard 2>/dev/null | grep -c drop)" ""
    systemctl start godusevpn
    check "服务起来后自动重连" "$(wait_status connected 60 && echo 1 || echo 0)" "$("$BIN" status | sed -n 2p)"
  fi
else
  echo "  (没有可用的上游 DNS / dig / 普通用户:跳过 DNS 进隧道与空档拦截的实测)"
fi
# 网卡 IPv6 备份:往里塞一张根本不存在的网卡。断开时的还原必须跳过它、把其它网卡照常还原、并把备份删干净 ——
# m29 曾在这一步整体失败,备份永远删不掉、卸载永远跑不完。
NICB=/var/lib/godusevpn/nic-ipv6-backup.json; nicb=0
if [ -f "$NICB" ] && command -v python3 >/dev/null 2>&1; then
  python3 - "$NICB" <<'PY'
import json, sys
p = sys.argv[1]; d = json.load(open(p)); d["zz-gone-nic"] = "0"; json.dump(d, open(p, "w"))
PY
  nicb=1
  check "网卡 IPv6 备份存在(已注入一张不存在的网卡)" 1 "$(cat "$NICB")"
else
  echo "  (没有网卡 IPv6 备份或没有 python3:跳过消失网卡的还原测试)"
fi
check "切回规则模式成功" "$("$BIN" mode rule >/dev/null 2>&1 && echo 1 || echo 0)" ""
sleep 2
check "切回规则模式后闸表清空" "$([ -z "$(nft list table inet godusevpn_guard 2>/dev/null)" ] && echo 1 || echo 0)" "$(nft list table inet godusevpn_guard 2>/dev/null | head -3 | tr '\n' ';')"
check "撤闸后 DNS 的策略路由也撤了" "$([ "$(ip rule show | grep -c 'lookup 5053')" = 0 ] && echo 1 || echo 0)" "$(ip rule show | grep 5053 | tr '\n' ';')"
check "规则模式恢复连接" "$(wait_status connected 15 && echo 1 || echo 0)" ""

echo "== 4. 出口"
now=$(pub4); check "规则模式出口 IP 变了" "$([ -n "$now" ] && [ "$now" != "$before" ] && echo 1 || echo 0)" "before=$before now=$now"
v6=$(pub6); check "IPv6 出网被阻断" "$([ -z "$v6" ] && echo 1 || echo 0)" "v6=$v6"
via=$(curl -s -4 --max-time 15 --resolve "api.ipify.org:443:$real" https://api.ipify.org); check "直连真实 IP 也走代理" "$([ -n "$via" ] && [ "$via" != "$before" ] && echo 1 || echo 0)" "real=$real got=$via"
if [ -n "$dip" ]; then
  # 套接字绑在物理网卡地址上(WebRTC 的做法)出去的也是节点出口,不再露出本机真实 IP
  if [ -n "$U" ]; then wb=$(su -s /bin/sh "$U" -c "curl -s -4 --interface $dip --max-time 15 https://api.ipify.org" 2>/dev/null); else wb=$(curl -s -4 --interface "$dip" --max-time 15 https://api.ipify.org); fi
  check "绑物理网卡地址出去的也走隧道(WebRTC 不露真实 IP)" "$([ -n "$wb" ] && [ "$wb" != "$before" ] && echo 1 || echo 0)" "before=$before got=$wb(地址 $dip)"
fi
lat=$("$BIN" test 2>&1); check "延迟测试" "$(echo "$lat" | grep -c ' ms')" "$lat"

echo "== 5. 模式切换"
"$BIN" mode direct >/dev/null; sleep 2; d=$(pub4); check "直连模式出口回到本机" "$([ "$d" = "$before" ] && echo 1 || echo 0)" "direct=$d"
"$BIN" mode global >/dev/null; sleep 2; gl=$(pub4); check "全局模式出口是节点" "$([ -n "$gl" ] && [ "$gl" != "$before" ] && echo 1 || echo 0)" "global=$gl"
"$BIN" mode rule >/dev/null
check "节点列表" "$("$BIN" nodes | grep -c '^\*')" "$("$BIN" nodes | tr '\n' ' ')"

echo "== 6. 断开与清理"
kill $deadman 2>/dev/null; pkill -f "sleep 420" 2>/dev/null
"$BIN" disconnect >/dev/null; sleep 3
check "断开后 TUN 网卡消失" "$([ -z "$(ip link show godusevpn 2>/dev/null)" ] && echo 1 || echo 0)" ""
check "断开后策略路由撤掉" "$([ "$(ip rule show | grep -c '^5000:')" = 0 ] && echo 1 || echo 0)" ""
check "断开后回包标记的 nft 表撤掉" "$([ -z "$(nft list table ip godusevpn_reply 2>/dev/null)" ] && echo 1 || echo 0)" ""
after=$(pub4); check "断开后出口恢复" "$([ "$after" = "$before" ] && echo 1 || echo 0)" "after=$after"
if [ "$nicb" = 1 ]; then
  check "断开后网卡 IPv6 备份已清理(含那张不存在的网卡)" "$([ ! -f "$NICB" ] && echo 1 || echo 0)" "$(cat "$NICB" 2>/dev/null)"
  check "服务日志记下了已消失的网卡" "$("$BIN" logs 80 2>/dev/null | grep -c '已经不在了')" ""
fi
echo
if [ $fail = 0 ]; then echo "全部通过"; else echo "$fail 项失败"; echo "== 内核日志"; "$BIN" logs 40 core; echo "== 服务日志"; "$BIN" logs 20; fi
exit $fail
