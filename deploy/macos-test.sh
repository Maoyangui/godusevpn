#!/bin/sh
# 佛跳墙 macOS 真机验收。以 root 跑:
#   sudo sh macos-test.sh "https://面板/sub/用户名" [/usr/local/bin/godusevpn]
# 步骤:装服务(launchd)→ 设订阅 → 连接 → 查 utun、路由、fake-ip、出口、IPv6 是否真被拦住 → 三态 → 页面接口 → 断开清理。
# macOS 与 Linux 的差别:隧道网卡是内核分配的 utunN(不是配置里的名字),命令换成 ifconfig / netstat / route / dscacheutil。
# 跑之前先布一个"死人开关":420 秒内没跑完就自动停服务,免得改坏路由后连不上机器。
SUB="$1"
BIN="${2:-/usr/local/bin/godusevpn}"
[ -z "$SUB" ] && { echo "用法: $0 <订阅地址> [二进制路径]"; exit 2; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 2; }
fail=0
check() { if [ "${2:-0}" -gt 0 ] 2>/dev/null; then printf '[PASS] %s  %s\n' "$1" "$3"; else printf '[FAIL] %s  %s\n' "$1" "$3"; fail=$((fail+1)); fi; }

"$BIN" uninstall >/dev/null 2>&1; rm -f "/Library/Application Support/godusevpn/data/state.json"; sleep 2
pub4() { curl -s -4 --max-time 15 https://api.ipify.org; }
pub6() { curl -s -6 --max-time 8 https://api6.ipify.org; }
# 系统解析器(fake-ip 要看的就是它):dscacheutil 走的是 macOS 的解析链路,和应用看到的一致
resolve4() { dscacheutil -q host -a name "$1" 2>/dev/null | awk '/^ip_address:/{print $2; exit}'; }
resolve6() { dscacheutil -q host -a name "$1" 2>/dev/null | awk '/^ipv6_address:/{print $2; exit}'; }
# tun4 找到我们那条隧道网卡:按 TUN 的地址 172.19.0.1 反查 utunN
tun4() { ifconfig | awk '/^utun/{i=$1} /inet 172\.19\.0\.1 /{sub(":","",i); print i; exit}'; }
# ifaceFor 某个目标地址实际会从哪个网卡出去
ifaceFor() { route -n get "$1" 2>/dev/null | awk '/interface:/{print $2; exit}'; }
ifaceFor6() { route -n get -inet6 "$1" 2>/dev/null | awk '/interface:/{print $2; exit}'; }
api() { curl -s --max-time 12 -X POST -H 'Content-Type: application/json' -d "${2:-[]}" "http://127.0.0.1:9800/api/$1"; }
# 比对状态要整词比:disconnected 里也含 connected,用 grep 会把"未连接"当成"已连接"
wait_status() { i=0; while [ $i -lt "$2" ]; do "$BIN" status 2>/dev/null | awk -v s="$1" '/^状态:/{if ($2==s) f=1} END{exit f?0:1}' && return 0; sleep 1; i=$((i+1)); done; return 1; }

echo "== 0. 环境"
sw_vers | tr '\n' ' '; echo; uname -m
before=$(pub4); echo "连接前公网 IPv4: $before"
before6=$(pub6); echo "连接前公网 IPv6: ${before6:-(本机没有 IPv6 出口)}"
real=$(resolve4 api.ipify.org); echo "api.ipify.org 真实 IP: $real"
defif=$(ifaceFor 1.1.1.1); echo "连接前默认出口网卡: $defif"
dns0=$(scutil --dns 2>/dev/null | awk '/nameserver/{print $3}' | sort -u | xargs echo); echo "连接前系统 DNS: $dns0"

echo "== 1. 安装(launchd)"
"$BIN" install >/dev/null
check "服务运行" "$("$BIN" status | head -1 | grep -c running)" "$("$BIN" status | head -1)"
check "launchd 里能查到" "$(launchctl print system/com.maoyangui.godusevpn >/dev/null 2>&1 && echo 1 || echo 0)" "$(launchctl print system/com.maoyangui.godusevpn 2>/dev/null | awk '/state = /{print $3; exit}')"
i=0; while [ $i -lt 15 ] && ! curl -s --max-time 2 http://127.0.0.1:9800/api/ping | grep -q version; do sleep 1; i=$((i+1)); done
check "面板可达" "$(curl -s --max-time 5 http://127.0.0.1:9800/api/ping | grep -c version)" "$(curl -s --max-time 5 http://127.0.0.1:9800/api/ping)"
# 图形界面是当前用户跑的,守护进程是 root 的 launchd:控制口必须让 admin 组连得上,
# 否则界面打开只有一句"服务未运行"。这里就用普通用户身份问一次状态来验。
U="${SUDO_USER:-$(stat -f %Su /dev/console 2>/dev/null)}"
echo "  控制口: $(ls -l /var/run/godusevpn.sock 2>&1 | head -1)  (以 ${U:-未知} 的身份试连)"
check "普通用户也能连上控制口" "$([ -n "$U" ] && sudo -u "$U" "$BIN" status 2>&1 | grep -c '^状态:' || echo 0)" "$([ -n "$U" ] && sudo -u "$U" "$BIN" status 2>&1 | head -1)"

echo "== 2. 订阅与连接"
p=$("$BIN" profile "$SUB" 2>&1); check "订阅拉取" "$(echo "$p" | grep -c '个节点')" "$(echo "$p" | head -1)"
# 订阅都没拉到节点,后面的连接 / 隧道 / IPv6 / 出口全都无从谈起,直接收尾,免得一堆连带失败盖住真正的原因
if ! echo "$p" | grep -q '个节点'; then echo "订阅没拿到节点,后面的项跳过"; "$BIN" uninstall >/dev/null 2>&1; exit 1; fi
( sleep 420; "$BIN" disconnect >/dev/null 2>&1; launchctl bootout system/com.maoyangui.godusevpn >/dev/null 2>&1 ) >/dev/null 2>&1 &
deadman=$!
"$BIN" connect >/dev/null
# 连不上就地把日志抓出来:等跑到第 8 段卸载完,守护进程没了就再也问不到日志了
dumplogs() { echo "---- 内核日志"; "$BIN" logs 60 core 2>&1 | tail -40; echo "---- 服务日志"; "$BIN" logs 40 2>&1 | tail -25; }
if wait_status connected 90; then
  check "进入 connected" 1 "$("$BIN" status | sed -n 2p)"
else
  check "进入 connected" 0 "$("$BIN" status | sed -n 2p)"
  dumplogs
fi

echo "== 3. 网络栈"
TUN=$(tun4)
echo "  (诊断) utun: $(ifconfig -l | xargs -n1 echo | grep '^utun' | xargs echo) / 连接后系统 DNS: $(scutil --dns 2>/dev/null | awk '/nameserver/{print $3}' | sort -u | xargs echo)"
check "隧道网卡存在(utun)" "$([ -n "$TUN" ] && echo 1 || echo 0)" "${TUN:-没找到 172.19.0.1 的 utun}"
check "公网地址的路由走隧道" "$([ -n "$TUN" ] && [ "$(ifaceFor 104.26.12.205)" = "$TUN" ] && echo 1 || echo 0)" "104.26.12.205 -> $(ifaceFor 104.26.12.205)(隧道 $TUN)"
check "默认路由已切到隧道" "$([ -n "$TUN" ] && [ "$(ifaceFor 1.1.1.1)" = "$TUN" ] && echo 1 || echo 0)" "1.1.1.1 -> $(ifaceFor 1.1.1.1)"
g=$(resolve4 www.google.com); check "代理域名得到 fake-ip(198.18/15)" "$(echo "$g" | grep -c '^198\.1[89]\.')" "$g"
b=$(resolve4 www.baidu.com); check "国内域名是真实 IP" "$([ -n "$b" ] && echo "$b" | grep -vc '^198\.1[89]\.' || echo 0)" "$b"

echo "== 3.5 数据面自检(包进得去,回得来吗)"
dnsq() { if command -v dig >/dev/null 2>&1; then dig +time=4 +tries=1 "@$1" example.com +short 2>&1 | head -2 | xargs echo; else nslookup -timeout=4 example.com "$1" 2>&1 | tail -3 | xargs echo; fi; }
echo "  隧道网卡: $(ifconfig "$TUN" 2>/dev/null | xargs echo | cut -c1-200)"
echo "  路由 172.19.0.2: $(route -n get 172.19.0.2 2>&1 | xargs echo | cut -c1-140)"
echo "  v4 路由表(前 10 条):"; netstat -rn -f inet 2>/dev/null | head -8 | sed 's/^/    /'
echo "  TCP 握手 104.26.13.205:443: $( (nc -v -G 8 -w 8 -z 104.26.13.205 443 && echo 通) 2>&1 | xargs echo)"
echo "  UDP DNS 经隧道(@223.5.5.5): $(dnsq 223.5.5.5)"
echo "  UDP DNS 经隧道(@1.1.1.1): $(dnsq 1.1.1.1)"
echo "  内核看到的入站连接数: $("$BIN" logs 400 core 2>/dev/null | grep -c 'inbound connection from')"
"$BIN" logs 400 core 2>/dev/null | grep 'inbound connection\|outbound connection' | tail -6 | sed 's/^/    /'

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
    nc -G 3 -w 3 -z "${np%:*}" "${np##*:}" >/dev/null 2>&1
    sleep 2
    hit=$("$BIN" logs 400 core 2>/dev/null | grep "outbound/direct" | grep -cF "outbound connection to $np")
    i=$((i+1))
  done
  check "朝节点服务器发起的连接判给了直连" "$hit" "$np(试了 $i 次)"
fi

echo "== 4. IPv6:关闭时必须是真拒绝,且不能绕过隧道出去"
a6=$(resolve6 www.google.com); check "AAAA 为空" "$([ -z "$a6" ] && echo 1 || echo 0)" "${a6:-(空)}"
t6=$(ifaceFor6 2001:4860:4860::8888)
# 这台机器本来有 IPv6 出口 → 必须看到 v6 被隧道接管;本来就没有 → 只能验证它没从物理网卡出去。
# 两种情况都不允许出口网卡等于物理网卡,那才是真泄漏。
if [ -n "$before6" ]; then
  check "v6 公网地址被隧道接管(不从物理网卡出去)" "$([ -n "$TUN" ] && [ "$t6" = "$TUN" ] && echo 1 || echo 0)" "2001:4860:4860::8888 -> ${t6:-无路由}(隧道 $TUN,物理 $defif)"
else
  check "v6 没有绕开隧道的出路(本机无 v6 出口)" "$([ -z "$t6" ] || [ "$t6" = "$TUN" ] && echo 1 || echo 0)" "2001:4860:4860::8888 -> ${t6:-无路由}(隧道 $TUN,物理 $defif)"
fi
# 不依赖跑机自己有没有 v6:直接看隧道网卡上有没有 v6 地址、v6 默认路由是不是指向它——
# 这两条成立就说明 v6 是"被接进隧道再拒绝",不可能从物理网卡漏出去。
check "隧道网卡带 v6 地址" "$(ifconfig "$TUN" 2>/dev/null | grep -c 'inet6 fdfe:dcba:9876')" "$(ifconfig "$TUN" 2>/dev/null | awk '/inet6/{printf "%s ", $2}')"
# sing-tun 在 macOS 上不是改 default,而是铺一片前缀(100::/8 200::/7 … 8000::/2)把整个 v6 空间盖住,
# 所以直接问两个差得很远的公网 v6 地址走哪张网卡,比数 default 那一行靠谱。
check "v6 公网地址都落在隧道上" "$([ -n "$TUN" ] && [ "$(ifaceFor6 2606:4700:4700::1111)" = "$TUN" ] && [ "$(ifaceFor6 2400:3200::1)" = "$TUN" ] && echo 1 || echo 0)" "2606:4700:4700::1111 -> $(ifaceFor6 2606:4700:4700::1111) / 2400:3200::1 -> $(ifaceFor6 2400:3200::1)"
v6=$(pub6); check "v6 出网拿不到地址" "$([ -z "$v6" ] && echo 1 || echo 0)" "${v6:-(拿不到,符合预期)}"
check "内核配置里有 v6 拒绝规则" "$(grep -c '"ip_version": 6' "/Library/Application Support/godusevpn/data/config.json" 2>/dev/null)" "$(grep -c '"ip_version": 6' "/Library/Application Support/godusevpn/data/config.json" 2>/dev/null) 条"

echo "== 5. 出口"
now=$(pub4); check "规则模式出口 IP 变了" "$([ -n "$now" ] && [ "$now" != "$before" ] && echo 1 || echo 0)" "before=$before now=$now"
via=$(curl -s -4 --max-time 15 --resolve "api.ipify.org:443:$real" https://api.ipify.org); check "直连真实 IP 也走代理" "$([ -n "$via" ] && [ "$via" != "$before" ] && echo 1 || echo 0)" "real=$real got=$via"
# 出网拿不到结果时当场细查一次:分清是 TCP 根本没进隧道、还是进了隧道出不去
if [ -z "$now" ] || [ -z "$via" ]; then
  echo "  (诊断) 明文 HTTP:"; curl -s -4 -o /dev/null -w '    code=%{http_code} 连上=%{time_connect}s 总=%{time_total}s\n' --max-time 12 http://cp.cloudflare.com/generate_204
  echo "  (诊断) HTTPS 直接给 IP:"; curl -s -4 -o /dev/null -w '    code=%{http_code} 连上=%{time_connect}s TLS=%{time_appconnect}s 总=%{time_total}s\n' --max-time 12 --resolve "api.ipify.org:443:$real" https://api.ipify.org
  echo "  (诊断) 这几秒的内核日志:"; "$BIN" logs 25 core 2>/dev/null | tail -18
fi
lat=$("$BIN" test 2>&1); check "延迟测试" "$(echo "$lat" | grep -c ' ms')" "$lat"

echo "== 6. 模式切换"
# 出口 IP 比的是"还是不是节点的",不是"等不等于连接前":云主机的出网地址来自一个地址池,
# 前后两次拿到的末段可能不同(跑机上实测 .161 变 .160),拿它当相等条件会无谓地红。
"$BIN" mode direct >/dev/null; sleep 3; d=$(pub4); check "直连模式不再走节点" "$([ -n "$d" ] && [ "$d" != "$now" ] && echo 1 || echo 0)" "direct=$d 节点出口=$now 连接前=$before"
"$BIN" mode global >/dev/null; sleep 3; gl=$(pub4); check "全局模式出口是节点" "$([ -n "$gl" ] && [ "$gl" != "$before" ] && echo 1 || echo 0)" "global=$gl"
"$BIN" mode rule >/dev/null
check "节点列表" "$("$BIN" nodes | grep -c '^\*')" "$("$BIN" nodes | tr '\n' ' ' | cut -c1-80)"

echo "== 7. 各功能页面的数据接口"
for m in GetState GetSettings GetProfiles GetNodes GetLogs GetConnections GetAutostart CheckUpdate; do
  r=$(api "$m")
  ok=$(echo "$r" | grep -c '"result"')
  [ "$m" = CheckUpdate ] && [ -z "$r" ] && ok=1   # 没有新版本时返回空也算正常
  check "页面接口 $m" "$ok" "$(echo "$r" | cut -c1-70)"
done
r=$(api SetMode '["rule"]'); check "页面接口 SetMode" "$(echo "$r" | grep -c '"result"\|^$')" "$(echo "$r" | cut -c1-40)"

echo "== 8. 断开与清理"
kill $deadman 2>/dev/null
logs_tail=$(dumplogs 2>&1)   # 卸载前先留一份,卸载后就问不到守护进程了
"$BIN" disconnect >/dev/null; sleep 4
check "断开后隧道网卡消失" "$([ -z "$(tun4)" ] && echo 1 || echo 0)" "$(tun4)"
check "断开后默认路由回到物理网卡" "$([ "$(ifaceFor 1.1.1.1)" = "$defif" ] && echo 1 || echo 0)" "1.1.1.1 -> $(ifaceFor 1.1.1.1)(原 $defif)"
after=$(pub4); check "断开后出口不再是节点" "$([ -n "$after" ] && [ "$after" != "$now" ] && echo 1 || echo 0)" "after=$after 节点出口=$now 连接前=$before"
"$BIN" uninstall >/dev/null 2>&1
check "卸载后 launchd 里没有了" "$(launchctl print system/com.maoyangui.godusevpn >/dev/null 2>&1 && echo 0 || echo 1)" ""
check "卸载后 plist 删掉了" "$([ ! -f /Library/LaunchDaemons/com.maoyangui.godusevpn.plist ] && echo 1 || echo 0)" ""

echo
if [ $fail = 0 ]; then echo "全部通过"; else echo "$fail 项失败"; echo "== 断开前留的日志"; echo "$logs_tail"; fi
exit $fail
