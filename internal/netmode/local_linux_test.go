//go:build linux && !android

package netmode

import (
	"net/netip"
	"testing"

	"github.com/sagernet/netlink"
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

// 地址集合按内容比,不看顺序:PPPoE 重拨、DHCP 换地址时才重做。
func TestAddrKey(t *testing.T) {
	if addrKey([]string{"b", "a"}) != addrKey([]string{"a", "b"}) || addrKey([]string{"a"}) == addrKey([]string{"a", "c"}) {
		t.Fatal("地址集合比较不对")
	}
	if addrKey(nil) == "" {
		t.Fatal("空集合也要和\"没在保护\"区分开")
	}
}
