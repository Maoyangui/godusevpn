package builder

import (
	"net/netip"
	"testing"

	N "github.com/sagernet/sing/common/network"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 严格全局下隧道外只放「局域网直通」开着时的局域网,而且只放真正发往局域网的连接:
// 域名的解析结果里只要混着公网地址,就不能借"私网直连"被直连出去(直连出站会把那组地址挨个拨,
// 公网那个一拨就通,对方看到的是真实 IP)。全是局域网地址的域名(NAS 的域名)、IP 字面量照旧直连。
func TestStrictGlobalLANOnlyForRealLAN(t *testing.T) {
	s := settings.Default()
	s.Mode, s.NoDirect = settings.ModeGlobal, true
	c, _ := build(t, s)
	for _, tc := range []struct {
		name string
		cn   conn
		want string
	}{
		{"域名应答混了 0.0.0.0", conn{dst: "x.attacker.example", answers: []string{"203.0.113.7", "0.0.0.0"}}, "proxy"},
		{"域名应答混了私网地址", conn{dst: "x.attacker.example", answers: []string{"10.0.0.5", "203.0.113.7"}}, "proxy"},
		{"域名应答混了回环", conn{dst: "x.attacker.example", answers: []string{"127.0.0.1", "203.0.113.7"}}, "proxy"},
		{"NAS 的域名(全是局域网地址)", conn{dst: "nas.home.example", answers: []string{"192.168.1.10"}}, "direct"},
		{"局域网 IP(经混合端口)", conn{dst: "192.168.1.1", port: 80}, "direct"},
		{"公网 IP", conn{dst: "1.1.1.1"}, "proxy"},
		{"公网域名", conn{dst: "www.example.com", answers: []string{"93.184.216.34"}}, "proxy"},
	} {
		if got, at := route(t, c, "Global", tc.cn); got != tc.want {
			t.Errorf("%s:应走 %s,实际 %s(规则 %d)", tc.name, tc.want, got, at)
		}
	}

	// 局域网直通关着:严格全局下局域网也照样走隧道(和闸同一个开关)
	s.LANBypass = false
	c, _ = build(t, s)
	for _, cn := range []conn{{dst: "192.168.1.1", port: 80}, {dst: "nas.home.example", answers: []string{"192.168.1.10"}}} {
		if got, at := route(t, c, "Global", cn); got != "proxy" {
			t.Errorf("局域网直通关着时 %s 应走代理,实际 %s(规则 %d)", cn.dst, got, at)
		}
	}
}

// 默认规则(私网 / 国内 / 其余)排在用户规则组之后,只在规则模式下生效 —— 界面与文档一直是这么写的。
func TestDefaultPrivateRuleOnlyInRuleModeAfterGroups(t *testing.T) {
	s := settings.Default()
	s.LANBypass = false // 私网段进 TUN,由规则决定
	s.RuleGroups = []settings.RuleGroup{{Name: "公司内网", Enabled: true, Outbound: settings.OutProxy,
		Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "10.8.0.0/16"}}}}
	c, _ := build(t, s)
	for _, tc := range []struct {
		name string
		cn   conn
		want string
	}{
		{"规则组盖过默认规则「私网」", conn{dst: "10.8.1.1"}, "proxy"},
		{"其余私网按默认规则直连", conn{dst: "192.168.1.1"}, "direct"},
		{"全是局域网地址的域名", conn{dst: "nas.home.example", answers: []string{"192.168.1.10"}}, "direct"},
		{"混着公网地址的域名不算私网", conn{dst: "x.attacker.example", answers: []string{"203.0.113.7", "10.0.0.5"}}, "proxy"},
	} {
		if got, at := route(t, c, "Rule", tc.cn); got != tc.want {
			t.Errorf("%s:应走 %s,实际 %s(规则 %d)", tc.name, tc.want, got, at)
		}
	}

	// 默认规则「私网」改成拒绝:全局 / 直连模式不受它影响
	s.LANBypass = true
	s.DefaultRules.Private = settings.OutReject
	for _, m := range []struct{ mode, clash, want string }{
		{settings.ModeRule, "Rule", "reject"},
		{settings.ModeGlobal, "Global", "direct"}, // 局域网直通开着
		{settings.ModeDirect, "Direct", "direct"},
	} {
		s.Mode = m.mode
		c, _ := build(t, s)
		if got, at := route(t, c, m.clash, conn{dst: "192.168.1.1", port: 80}); got != m.want {
			t.Errorf("%s 模式下默认规则「私网」=拒绝时,局域网 IP 应走 %s,实际 %s(规则 %d)", m.mode, m.want, got, at)
		}
	}
}

// 开着 IPv6 时应答里也有 v6 地址,判法一样。
func TestLANRuleWithIPv6Answers(t *testing.T) {
	s := settings.Default()
	s.IPv6 = true
	c, _ := build(t, s)
	if got, _ := route(t, c, "Rule", conn{dst: "x.attacker.example", answers: []string{"2001:db8::7", "fd00::1"}}); got != "proxy" {
		t.Errorf("v6 应答混着公网地址不该算私网,实际 %s", got)
	}
	if got, _ := route(t, c, "Rule", conn{dst: "nas.home.example", answers: []string{"fd00::1", "192.168.1.10"}}); got != "direct" {
		t.Errorf("全是局域网地址应按默认规则直连,实际 %s", got)
	}
}

// publicCIDRs 必须盖住 sing-box 眼里的每一个公网地址:漏一个,那个地址混进应答就能借"私网直连"被直连出去。
// 反过来,真正的局域网地址一个都不能在里面。
func TestPublicCIDRsCoverEveryPublicAddr(t *testing.T) {
	var prefixes []netip.Prefix
	for _, s := range publicCIDRs {
		prefixes = append(prefixes, netip.MustParsePrefix(s))
	}
	in := func(a netip.Addr) bool {
		for _, p := range prefixes {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	var samples []netip.Addr
	for i := 0; i < 256; i++ { // 每个 /8 的头尾,再加各私网段边界两侧
		samples = append(samples, netip.AddrFrom4([4]byte{byte(i), 0, 0, 0}), netip.AddrFrom4([4]byte{byte(i), 255, 255, 255}),
			netip.AddrFrom4([4]byte{byte(i), 168, 0, 1}), netip.AddrFrom4([4]byte{byte(i), 254, 1, 1}), netip.AddrFrom4([4]byte{byte(i), 16, 0, 1}), netip.AddrFrom4([4]byte{byte(i), 31, 255, 255}), netip.AddrFrom4([4]byte{byte(i), 32, 0, 0}))
	}
	for _, s := range []string{"::", "::1", "::2", "::ffff:1.2.3.4", "64:ff9b::1.2.3.4", "2001:db8::1", "2400:cb00::1", "fbff:ffff::1",
		"fc00::1", "fd12:3456::1", "fdff:ffff::1", "fe00::1", "fe7f::1", "fe80::1", "febf::1", "fec0::1", "ff02::1", "ffff::1"} {
		samples = append(samples, netip.MustParseAddr(s))
	}
	for _, a := range samples {
		if N.IsPublicAddr(a) && !in(a) {
			t.Errorf("sing-box 认为 %s 是公网地址,publicCIDRs 却没盖住", a)
		}
	}
	for _, s := range []string{"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.1.1", "127.0.0.1", "224.0.0.251", "fd00::1", "fe80::1", "ff02::fb"} {
		if in(netip.MustParseAddr(s)) {
			t.Errorf("局域网地址 %s 不该算公网", s)
		}
	}
}
