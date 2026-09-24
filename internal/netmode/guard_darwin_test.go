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
