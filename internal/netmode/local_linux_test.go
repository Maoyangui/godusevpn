//go:build linux && !android

package netmode

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"

	"github.com/sagernet/netlink"
	"golang.org/x/sys/unix"

	"github.com/Maoyangui/godusevpn/internal/builder"
)

// 断开时只删 Protect 加的那种规则。以前按优先级整段删,别人放在 5000 的规则也被删光。
func TestIsReplyRule(t *testing.T) {
	ours := *replyRule(netip.MustParseAddr("192.168.1.5"))
	if !isReplyRule(ours) {
		t.Fatal("Protect 自己加的规则认不出来")
	}
	if !isReplyRule(*replyRule(netip.MustParseAddr("2408:8214::1"))) {
		t.Fatal("v6 的也要认")
	}
	for name, mod := range map[string]func(r *netlink.Rule){
		"别的优先级": func(r *netlink.Rule) { r.Priority = 5001 },
		"别的表":   func(r *netlink.Rule) { r.Table = 100 },
		"按网段匹配": func(r *netlink.Rule) { r.Src = netip.MustParsePrefix("192.168.1.0/24") },
		"带标记条件": func(r *netlink.Rule) { r.Mark = 0x1 },
		"带入口网卡": func(r *netlink.Rule) { r.IifName = "eth0" },
		"带端口条件": func(r *netlink.Rule) { r.Dport = netlink.NewRulePortRange(22, 22) },
		"取反":    func(r *netlink.Rule) { r.Invert = true },
		"不看源地址": func(r *netlink.Rule) { r.Src = netip.Prefix{} },
	} {
		r := ours
		mod(&r)
		if isReplyRule(r) {
			t.Fatalf("%s:这不是我们的规则,不能删", name)
		}
	}
}

// 按标记的那条:删 / 认的时候只认我们那一位,别人放在 5000 的按标记规则不碰;两种认法互不串。
func TestIsMarkReplyRule(t *testing.T) {
	ours := *markRule(unix.AF_INET)
	ours.MarkSet = false // 列出来的规则不带这个标志
	if !isMarkReplyRule(ours) || isReplyRule(ours) {
		t.Fatal("按标记的规则要认成我们的、且不是按地址的那种")
	}
	if isMarkReplyRule(*replyRule(netip.MustParseAddr("192.168.1.5"))) {
		t.Fatal("按地址的规则不是按标记的那种")
	}
	for name, mod := range map[string]func(r *netlink.Rule){
		"别的标记":   func(r *netlink.Rule) { r.Mark, r.Mask = 0x2023, 0x2023 },
		"整值比的标记": func(r *netlink.Rule) { r.Mask = -1 },
		"别的优先级":  func(r *netlink.Rule) { r.Priority = 5001 },
		"别的表":    func(r *netlink.Rule) { r.Table = 2022 },
		"带源地址":   func(r *netlink.Rule) { r.Src = netip.MustParsePrefix("10.0.0.1/32") },
		"取反":     func(r *netlink.Rule) { r.Invert = true },
	} {
		r := ours
		mod(&r)
		if isMarkReplyRule(r) {
			t.Fatalf("%s:这不是我们的规则,不能删", name)
		}
	}
}

// nft 表:只给"从隧道网卡、回环以外进来、发往本机地址的外部连接"打标记(按位设),往外的包按位比、按位还原,
// 还原要在 route 链里(内核才会按新标记重新选路);v4 / v6 各一张,先建再删再建。
func TestReplyRuleset(t *testing.T) {
	ses := []inboundSession{
		{netip.MustParseAddrPort("172.31.6.96:22"), netip.MustParseAddrPort("1.2.3.4:55555")},
		{netip.MustParseAddrPort("[2001:db8::5]:22"), netip.MustParseAddrPort("[2001:db8::9]:40000")},
	}
	s := replyRuleset("ip", ses)
	for _, want := range []string{
		"table ip godusevpn_reply {}\ndelete table ip godusevpn_reply\ntable ip godusevpn_reply {",
		"type ipv4_addr . inet_service . ipv4_addr . inet_service\n\t\telements = { 172.31.6.96 . 22 . 1.2.3.4 . 55555 }",
		"type route hook output priority mangle;",
		"ct direction reply meta mark set meta mark or 0x10000000",
		"ip saddr . tcp sport . ip daddr . tcp dport @inbound meta mark set meta mark or 0x10000000",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("少了 %q:\n%s", want, s)
		}
	}
	// 不靠"外部下一个包进来时再补标记":服务端先往外发的那一下会进隧道、被重置(真机上 SSH 会话就这么断过)
	if strings.Contains(s, "prerouting") || strings.Contains(s, "2001:db8") {
		t.Fatalf("不该有进站补标记的链;v6 的会话不进 v4 的表:\n%s", s)
	}
	s6 := replyRuleset("ip6", ses)
	if !strings.Contains(s6, "table ip6 godusevpn_reply {") || !strings.Contains(s6, "elements = { 2001:db8::5 . 22 . 2001:db8::9 . 40000 }") ||
		!strings.Contains(s6, "ip6 saddr . tcp sport . ip6 daddr . tcp dport @inbound") {
		t.Fatalf("v6 那张表:\n%s", s6)
	}
	if strings.Contains(replyRuleset("ip", nil), "elements") {
		t.Fatal("没有会话时集合留空(nft 不认空的 elements)")
	}
	if replyMark&builder.RouteMark != 0 {
		t.Fatal("回包标记和本服务出站的标记不能有重叠的位")
	}
}

// 从 /proc/net/tcp 挑外部连进来的已建立会话:本机端口是监听端口的才算;本机连出去的、回环上的、隧道协议栈内部的不算;
// v4 映射成 v6 的按 v4 记。/proc 里的地址按本机字节序打印,测试数据按小端写,只在小端机器上比对。
func TestInboundSessions(t *testing.T) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("测试数据按小端写")
	}
	tcp4 := "  sl  local_address rem_address   st\n" +
		"   0: 00000000:0016 00000000:0000 0A 00000000:00000000\n" + // 0.0.0.0:22 监听
		"   1: 60061FAC:0016 04030201:D903 01 00000000:00000000\n" + // 172.31.6.96:22 ← 1.2.3.4:55555(外部连进来)
		"   2: 60061FAC:9C40 05030201:01BB 01 00000000:00000000\n" + // 172.31.6.96:40000 → 1.2.3.5:443(本机连出去)
		"   3: 0100007F:0016 0100007F:A000 01 00000000:00000000\n" + // 回环
		"   4: 010013AC:A91F 00000000:0000 0A 00000000:00000000\n" + // 隧道协议栈的内部监听
		"   5: 010013AC:A91F 020013AC:B000 01 00000000:00000000\n"
	tcp6 := "  sl  local_address rem_address   st\n" +
		"   0: 0000000000000000FFFF000060061FAC:2654 0000000000000000FFFF000004030201:C350 01 00000000:00000000\n" + // v4 映射:9812 ← 1.2.3.4:50000
		"   1: 00000000000000000000000000000000:2654 00000000000000000000000000000000:0000 0A 00000000:00000000\n" // [::]:9812 监听
	var lines []string
	for _, s := range inboundSessions(tcp4, tcp6) {
		lines = append(lines, s.local.String()+" "+s.remote.String())
	}
	if got, want := strings.Join(lines, "|"), "172.31.6.96:22 1.2.3.4:55555|172.31.6.96:9812 1.2.3.4:50000"; got != want {
		t.Fatalf("挑出来的会话不对:\n%s\n应为\n%s", got, want)
	}
}

// 重启后保持停用的 sysctl 配置:按名字排好、用斜杠写法(VLAN 网卡名里的点按原样)。
func TestNICSysctlText(t *testing.T) {
	s := nicSysctlText(map[string]string{"eth0.100": "0", "ens3": "0"})
	want := "net/ipv6/conf/ens3/disable_ipv6 = 1\nnet/ipv6/conf/eth0.100/disable_ipv6 = 1\n"
	if !strings.HasSuffix(s, want) || !strings.HasPrefix(s, "#") {
		t.Fatalf("配置内容不对:\n%s", s)
	}
}

// 地址集合按内容比,不看顺序:PPPoE 重拨、DHCP 换地址时才重做。
func TestAddrKey(t *testing.T) {
	if addrKey([]string{"b", "a"}) != addrKey([]string{"a", "b"}) || addrKey([]string{"a"}) == addrKey([]string{"a", "c"}) {
		t.Fatal("地址集合比较不对")
	}
	if addrKey(nil) == "" {
		t.Fatal("空集合也要和\"没在保护\"区分开")
	}
}
