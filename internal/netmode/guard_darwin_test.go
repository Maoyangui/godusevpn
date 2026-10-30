//go:build darwin

package netmode

import (
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/builder"
)

// 拦 DNS 那一条要在局域网放行之前(pf 从上到下第一条 quick 命中的说了算),在回环、root、隧道地址放行之后。
func TestGuardRulesBlockDNSBeforeLAN(t *testing.T) {
	r := guardRules(GuardSpec{LAN: true, TunAddr4: "172.19.0.1", TunAddr6: "fdfe:dcba:9876::1"})
	dns := strings.Index(r, dnsBlockRule)
	if dns < 0 || !strings.Contains(dnsBlockRule, "53") || !strings.Contains(dnsBlockRule, "853") {
		t.Fatalf("没有拦 DNS(53 / 853):\n%s", r)
	}
	for _, before := range []string{"pass out quick on lo0", "pass out quick user root", "pass out quick from 172.19.0.1", "pass out quick from fdfe:dcba:9876::1"} {
		if i := strings.Index(r, before); i < 0 || i > dns {
			t.Fatalf("%q 要在拦 DNS 之前:\n%s", before, r)
		}
	}
	if lan := strings.Index(r, "pass out quick to {"); lan < 0 || lan < dns {
		t.Fatalf("局域网放行要在拦 DNS 之后:\n%s", r)
	}
	// 局域网直通关着时也拦(反正最后一条全拦,这条在不在都不漏;留着让规则与判定一致)
	if !strings.Contains(guardRules(GuardSpec{}), dnsBlockRule) {
		t.Fatal("局域网直通关着时也应当有这一条")
	}
}

// 回环那条必须 no state:pf 默认的"flags S/SA keep state"只认 TCP 首包,本机回环里服务端回的 SYN-ACK 也是从 lo0 出去,
// 会落到最后一条被丢,严格全局下 127.0.0.1 上的 TCP 全卡死。
func TestGuardRulesLoopbackNoState(t *testing.T) {
	r := guardRules(GuardSpec{TunAddr4: "172.19.0.1"})
	if !strings.HasPrefix(r, "pass out quick on lo0 all no state\n") {
		t.Fatalf("第一条要是不建状态的回环放行:\n%s", r)
	}
	// 局域网直通同理:设备连进来时本机的回包不是首包
	for _, l := range strings.Split(guardRules(GuardSpec{LAN: true}), "\n") {
		if strings.HasPrefix(l, "pass out quick to {") && !strings.HasSuffix(l, "} no state") {
			t.Fatalf("局域网放行要 no state:%q", l)
		}
	}
}

// DHCP 与邻居发现的例外要和 Linux / Windows 一样收窄:不限目的的 UDP 68→67、不限类型的 ICMPv6,
// 任何普通程序都能借它们从物理网卡直接发包。
func TestGuardRulesNarrowDHCPAndICMPv6(t *testing.T) {
	r := guardRules(GuardSpec{LAN: true, TunAddr4: "172.19.0.1"})
	for _, l := range strings.Split(r, "\n") {
		if strings.Contains(l, "port 67") && !strings.Contains(l, "to 255.255.255.255 port 67") {
			t.Fatalf("DHCP 放行没限定广播目的:%q", l)
		}
		if strings.Contains(l, "icmp6") && !strings.Contains(l, "icmp6-type { neighbrsol, neighbradv, routersol, routeradv }") {
			t.Fatalf("ICMPv6 只该放邻居发现的四种:%q", l)
		}
	}
}

// 严格全局下局域网直通开着也要拦 UPnP 发现(SSDP)与 NAT-PMP / PCP:本机程序靠它们能向路由器问到真实公网地址。
func TestGuardRulesBlockUPnPBeforeLAN(t *testing.T) {
	r := guardRules(GuardSpec{LAN: true, TunAddr4: "172.19.0.1"})
	up := strings.Index(r, upnpBlockRule)
	if up < 0 || !strings.Contains(upnpBlockRule, "1900") || !strings.Contains(upnpBlockRule, "5351") {
		t.Fatalf("没有拦 SSDP / NAT-PMP:\n%s", r)
	}
	if lan := strings.Index(r, "pass out quick to {"); lan < up {
		t.Fatalf("要压在局域网放行之上:\n%s", r)
	}
	// v6 假地址段落在局域网放行的 fc00::/7 里:同样要在局域网放行之前拦掉
	if fake := strings.Index(r, "block drop out quick inet6 to "+builder.FakeIP6); fake < 0 || fake > strings.Index(r, "pass out quick to {") {
		t.Fatalf("v6 假地址段要在局域网放行之前拦掉:\n%s", r)
	}
}

// pf 没开、或主规则集不再挂 com.apple 锚点时,锚点里的规则不被评估:闸必须按"没开"算,守护进程才会重装并告警。
// 规则模式:表写在最前面,放行段在局域网后面、最后一条前面;不拦 DNS / UPnP;入站建状态。
func TestGuardRulesRuleMode(t *testing.T) {
	r := guardRules(GuardSpec{LAN: true, Rule: true, TunAddr4: "172.19.0.1", Direct: "1.0.1.0/24,2400:3200::/32", TunnelPorts: "8443,9000-9100"})
	if !strings.HasPrefix(r, "table <godusevpn_direct> const { 1.0.1.0/24, 2400:3200::/32 }\n") {
		t.Fatalf("表要写在最前面:\n%s", r)
	}
	for _, want := range []string{"pass in quick all\n", "port { 8443, 9000:9100 }", "pass out quick to <godusevpn_direct> no state\n"} {
		if !strings.Contains(r, want) {
			t.Fatalf("少了 %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, dnsBlockRule) || strings.Contains(r, upnpBlockRule) {
		t.Fatalf("规则模式不该拦 DNS / UPnP:\n%s", r)
	}
	lan, ports, direct, last := strings.Index(r, "10.0.0.0/8"), strings.Index(r, "port { 8443"), strings.Index(r, "pass out quick to <"), strings.Index(r, "block drop out quick all")
	if !(lan < ports && ports < direct && direct < last) {
		t.Fatalf("顺序不对:\n%s", r)
	}
	g := guardRules(GuardSpec{LAN: true, TunAddr4: "172.19.0.1"})
	if strings.Contains(g, "godusevpn_direct") || strings.Contains(g, "pass in") || !strings.Contains(g, dnsBlockRule) {
		t.Fatalf("全局模式变了:\n%s", g)
	}
}

func TestPFStatusParsing(t *testing.T) {
	if !pfInfoEnabled("Status: Enabled for 0 days 00:01:02           Debug: Urgent\n") || pfInfoEnabled("Status: Disabled                              Debug: Urgent\n") {
		t.Fatal("pfctl -s info 的开关状态认错了")
	}
	main := "scrub-anchor \"com.apple/*\" all fragment reassemble\nanchor \"com.apple/*\" all\n"
	if !mainRulesHookAnchor(main) {
		t.Fatal("系统默认的主规则集挂着 com.apple/*,应认作挂上了")
	}
	if mainRulesHookAnchor("scrub-anchor \"com.apple/*\" all fragment reassemble\npass out all\n") {
		t.Fatal("只有 scrub-anchor 不算挂上:它不评估过滤规则")
	}
}
