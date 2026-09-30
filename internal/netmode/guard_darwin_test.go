//go:build darwin

package netmode

import (
	"strings"
	"testing"
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
}

// pf 没开、或主规则集不再挂 com.apple 锚点时,锚点里的规则不被评估:闸必须按"没开"算,守护进程才会重装并告警。
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
