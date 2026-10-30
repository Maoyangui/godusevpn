//go:build linux && !android

package netmode

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/sagernet/netlink"

	"github.com/Maoyangui/godusevpn/internal/builder"
)

// chain 取出 nft 脚本里某条链的正文。
func chain(t *testing.T, rules, name string) string {
	t.Helper()
	i := strings.Index(rules, "chain "+name+" {")
	if i < 0 {
		t.Fatalf("没有 %s 链:\n%s", name, rules)
	}
	body := rules[i:]
	return body[:strings.Index(body, "\n\t}\n")]
}

func before(t *testing.T, body, a, b string) {
	t.Helper()
	ia, ib := strings.Index(body, a), strings.Index(body, b)
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("%q 要在 %q 之前:\n%s", a, b, body)
	}
}

var testSpec = GuardSpec{TunName: "godusevpn", TunAddr4: "172.19.0.1", TunAddr6: "fdfe:dcba:9876::1", LAN: true}

// 放行本服务靠标记,root 只放行它回应入站连接的包;按隧道源地址放行这种不看出口网卡的规则不能有
// (普通程序绑上隧道地址再指定物理网卡就能出去)。
func TestGuardOutputAllowsSelfByMarkNotRoot(t *testing.T) {
	out := chain(t, guardRuleset(testSpec, true), "output")
	if !strings.Contains(out, fmt.Sprintf("meta mark %#x accept", builder.RouteMark)) {
		t.Fatalf("没按标记放行本服务:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "meta skuid 0") && !strings.Contains(l, "ct direction reply") && !strings.Contains(l, "dport 67") && !strings.Contains(l, "168.63.129.16") {
			t.Fatalf("root 不能整个放行:%q", l)
		}
		if strings.Contains(l, "saddr") {
			t.Fatalf("按源地址放行不看出口网卡,伪造隧道地址就能从物理网卡出去:%q", l)
		}
	}
	gw := testSpec
	gw.Gateway = true
	if !strings.Contains(chain(t, guardRuleset(gw, true), "output"), "0x2024") {
		t.Fatal("网关模式下内核出站带的是 auto_redirect 的标记,闸要认它")
	}
}

// 拦 DNS(只在 DNS 已接进隧道时)和 UPnP / NAT-PMP 要压在局域网放行之上、本服务放行之下。
func TestGuardBlocksDNSAndUPnPBeforeLAN(t *testing.T) {
	out := chain(t, guardRuleset(testSpec, true), "output")
	mark := fmt.Sprintf("meta mark %#x", builder.RouteMark)
	before(t, out, mark, "udp dport { 53, 853 } drop")
	before(t, out, "tcp dport { 53, 853 } drop", "ip daddr {")
	before(t, out, mark, "udp dport { 1900, 5351 } drop")
	before(t, out, "udp dport { 1900, 5351 } drop", "ip daddr {")
	before(t, out, "ip6 daddr "+builder.FakeIP6+" drop", "ip6 daddr {") // v6 假地址段不能当局域网放出去
	spec := testSpec
	spec.LAN = true
	before(t, chain(t, guardRuleset(spec, true), "forward"), "ip6 daddr "+builder.FakeIP6+" drop", "ip6 daddr {")
	if strings.Contains(guardRuleset(testSpec, false), "dport { 53, 853 }") {
		t.Fatal("DNS 没接进隧道时不能拦:连着时就解析不了")
	}
	if !strings.Contains(guardRuleset(GuardSpec{TunName: "godusevpn"}, false), "udp dport { 1900, 5351 } drop") {
		t.Fatal("UPnP / NAT-PMP 不论局域网直通开没开都拦")
	}
}

// forward 链不分网关 / 本机模式都要有(Docker、虚拟机 NAT、热点共享也经这里出网),
// 而且要放行从隧道回来、转给设备的应答,否则局域网直通关着时设备的 UDP / ping 全断。
func TestGuardForwardChain(t *testing.T) {
	for _, gw := range []bool{false, true} {
		for _, lan := range []bool{false, true} {
			spec := testSpec
			spec.Gateway, spec.LAN = gw, lan
			fwd := chain(t, guardRuleset(spec, true), "forward")
			for _, want := range []string{`oifname "godusevpn" accept`, `iifname "godusevpn" accept`, "drop"} {
				if !strings.Contains(fwd, want) {
					t.Fatalf("网关=%v 局域网=%v:forward 链缺 %q:\n%s", gw, lan, want, fwd)
				}
			}
			if strings.Contains(fwd, "ip daddr {") != lan {
				t.Fatalf("网关=%v 局域网=%v:私网放行应跟随局域网直通:\n%s", gw, lan, fwd)
			}
			if lan {
				before(t, fwd, "udp dport { 53, 853 } drop", "ip daddr {")
			}
		}
	}
}

// 没装 nft 时 exec 返回 ErrNotFound、输出为空:要当成"没有我们的表",而不是查询失败。
// 规则模式:放行段在局域网后面、最后的 drop 前面;不拦 DNS / UPnP;入站回包全放;v6 假地址段照拦。
func TestGuardRuleMode(t *testing.T) {
	r := guardRuleset(GuardSpec{TunName: "tun0", LAN: true, Rule: true, Direct: "1.0.1.0/24,223.5.5.0/24,2400:3200::/32", TunnelPorts: "8443,9000-9100"}, true)
	for _, want := range []string{
		"set direct4 {", "elements = { 1.0.1.0/24, 223.5.5.0/24 }", "set direct6 {", "elements = { 2400:3200::/32 }",
		"ct direction reply accept", "ip daddr @direct4 accept", "ip6 daddr @direct6 accept",
		"tcp dport { 8443, 9000-9100 } drop", "udp dport { 8443, 9000-9100 } drop",
	} {
		if !strings.Contains(r, want) {
			t.Fatalf("少了 %q:\n%s", want, r)
		}
	}
	for _, gone := range []string{"dport { 53, 853 } drop", "dport { 1900, 5351 } drop", "meta skuid 0 ct direction reply accept"} {
		if strings.Contains(r, gone) {
			t.Fatalf("规则模式不该有 %q:\n%s", gone, r)
		}
	}
	out := r[strings.Index(r, "chain output"):strings.Index(r, "chain forward")]
	fake, lan, ports, direct, last := strings.Index(out, builder.FakeIP6), strings.Index(out, "10.0.0.0/8"), strings.Index(out, "tcp dport { 8443"), strings.Index(out, "@direct4"), strings.LastIndex(out, "drop")
	if !(fake < lan && lan < ports && ports < direct && direct < last) {
		t.Fatalf("顺序不对:假地址 %d 局域网 %d 端口 %d 放行 %d 兜底 %d\n%s", fake, lan, ports, direct, last, out)
	}
	// 没有放行段时不引用不存在的集合
	r = guardRuleset(GuardSpec{TunName: "tun0", Rule: true}, true)
	if strings.Contains(r, "@direct") || strings.Contains(r, "set direct") {
		t.Fatalf("没有放行段不该有集合:\n%s", r)
	}
	// 全局模式一点不变
	g := guardRuleset(GuardSpec{TunName: "tun0", LAN: true}, true)
	if strings.Contains(g, "direct4") || !strings.Contains(g, "dport { 53, 853 } drop") || !strings.Contains(g, "meta skuid 0 ct direction reply accept") {
		t.Fatalf("全局模式变了:\n%s", g)
	}
}

// 网关设备策略只进转发链,排在放行段前面。
func TestGuardRuleModeDevices(t *testing.T) {
	r := guardRuleset(GuardSpec{TunName: "tun0", Gateway: true, Rule: true, Direct: "1.0.1.0/24", DevBlock: "aa:bb:cc:dd:ee:01", DevDirect: "aa:bb:cc:dd:ee:02,192.168.1.9"}, true)
	out, fwd := r[strings.Index(r, "chain output"):strings.Index(r, "chain forward")], r[strings.Index(r, "chain forward"):]
	if strings.Contains(out, "ether saddr") {
		t.Fatalf("输出链不该看设备:\n%s", out)
	}
	block, direct, ipd, set := strings.Index(fwd, "ether saddr { aa:bb:cc:dd:ee:01 } drop"), strings.Index(fwd, "ether saddr { aa:bb:cc:dd:ee:02 } accept"), strings.Index(fwd, "ip saddr { 192.168.1.9 } accept"), strings.Index(fwd, "@direct4")
	if block < 0 || direct < 0 || ipd < 0 || !(block < direct && direct < set && ipd < set) {
		t.Fatalf("转发链不对:\n%s", fwd)
	}
}

func TestNftMissingBinary(t *testing.T) {
	out, err := exec.Command("godusevpn-no-such-nft-binary").CombinedOutput()
	if !nftMissing(out, err) {
		t.Fatalf("找不到 nft 应当算表不存在: out=%q err=%v", out, err)
	}
	if nftMissing([]byte("Error: Could not process rule: Operation not permitted"), fmt.Errorf("exit status 1")) {
		t.Fatal("没权限不是表不存在")
	}
	if !strings.Contains(nftError("x", err, out).Error(), "nftables") {
		t.Fatal("没装 nft 要说清楚去装 nftables")
	}
}

// 4.17 以前的内核不认端口条件:装上的规则没有端口,等于所有流量查那张表,必须认出来。
func TestDNSPortOnly(t *testing.T) {
	r := *dnsRule(2)
	if !dnsPortOnly(r) {
		t.Fatal("带 53 端口的规则认错了")
	}
	r.Dport = nil
	if dnsPortOnly(r) {
		t.Fatal("没带端口条件的规则会把所有流量带进那张表,必须认出来")
	}
	r.Dport = netlink.NewRulePortRange(53, 54)
	if dnsPortOnly(r) {
		t.Fatal("端口范围不对也要认出来")
	}
}

// 开机挡路由器通告:只挡进来的通告(别的 ICMPv6、邻居发现照常),单独一张表,守护进程对完账整张删掉。
func TestBootRARuleset(t *testing.T) {
	for _, want := range []string{
		"table inet godusevpn_boot_ra {}\ndelete table inet godusevpn_boot_ra\ntable inet godusevpn_boot_ra {",
		"type filter hook input priority filter; policy accept;",
		"icmpv6 type nd-router-advert drop",
	} {
		if !strings.Contains(bootRARuleset, want) {
			t.Fatalf("少了 %q:\n%s", want, bootRARuleset)
		}
	}
	if strings.Count(bootRARuleset, "drop") != 1 || strings.Contains(bootRARuleset, "hook output") {
		t.Fatalf("只挡进来的路由器通告:\n%s", bootRARuleset)
	}
}
