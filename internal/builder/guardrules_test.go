package builder

import (
	"net/netip"
	"path/filepath"
	"testing"

	"go4.org/netipx"

	"github.com/Maoyangui/godusevpn/internal/ruleset"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

func guardFixture(t *testing.T) (settings.Settings, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "rulesets")
	if err := ruleset.Install(root); err != nil {
		t.Fatal(err)
	}
	s := settings.Default()
	return s, root
}

func allowed(t *testing.T, r GuardRules) *netipx.IPSet {
	t.Helper()
	var b netipx.IPSetBuilder
	for _, p := range r.Direct {
		b.AddPrefix(p)
	}
	set, err := b.IPSet()
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func wantAddrs(t *testing.T, r GuardRules, in, out []string) {
	t.Helper()
	set := allowed(t, r)
	for _, a := range in {
		if !set.Contains(netip.MustParseAddr(a)) {
			t.Errorf("%s 应该放行", a)
		}
	}
	for _, a := range out {
		if set.Contains(netip.MustParseAddr(a)) {
			t.Errorf("%s 不该放行", a)
		}
	}
}

// 出厂规则:私网直连、国内直连、其余走代理。国内和局域网放行,其余不放行;假地址不放行。
func TestRuleGuardFactoryRules(t *testing.T) {
	s, root := guardFixture(t)
	r := RuleGuard(s, root)
	if n := len(r.Direct); n < 1000 {
		t.Fatalf("应该读到国内地址段,实际只有 %d 段", n)
	}
	wantAddrs(t, r,
		[]string{"223.5.5.5", "114.114.114.114", "192.168.1.1", "10.0.0.1"},
		[]string{"8.8.8.8", "1.1.1.1", "198.18.0.1", "2400:3200::1", "2001:4860:4860::8888"})
}

// 开了 IPv6 才放行国内的 v6;假地址段就算落在私网里也不放行。
func TestRuleGuardIPv6(t *testing.T) {
	s, root := guardFixture(t)
	s.IPv6 = true
	r := RuleGuard(s, root)
	wantAddrs(t, r,
		[]string{"2400:3200::1", "fd00::1", "fe80::1"},
		[]string{"2001:4860:4860::8888", "fc00::1", "fc00:3fff::1"})
}

// 排前面的组先赢:两个方向各验一次。
func TestRuleGuardGroupOrder(t *testing.T) {
	s, root := guardFixture(t)
	s.RuleGroups = []settings.RuleGroup{
		{Name: "a", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "8.8.8.0/24"}}},
		{Name: "b", Enabled: true, Outbound: settings.OutProxy, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "8.8.0.0/16"}}},
		{Name: "c", Enabled: true, Outbound: settings.OutProxy, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "223.5.5.0/24"}}},
		{Name: "d", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "223.5.0.0/16"}}},
		{Name: "e", Enabled: true, Outbound: settings.OutReject, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "114.114.114.114"}}},
		{Name: "off", Enabled: false, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "1.1.1.1"}}},
	}
	r := RuleGuard(s, root)
	wantAddrs(t, r,
		[]string{"8.8.8.8", "223.5.1.1"},
		[]string{"8.8.4.4", "223.5.5.5", "114.114.114.114", "1.1.1.1"})
}

// 国内改成走代理:国内地址也不放行。私网改成走代理且没开局域网直通时,私网也不放行。
func TestRuleGuardDefaultRulesChanged(t *testing.T) {
	s, root := guardFixture(t)
	s.DefaultRules.CN = settings.OutProxy
	s.DefaultRules.Private = settings.OutProxy
	r := RuleGuard(s, root)
	wantAddrs(t, r, nil, []string{"223.5.5.5", "192.168.1.1"})
}

// 其余直连:底色是全放行,只有明确走隧道的段不放行。
func TestRuleGuardFinalDirect(t *testing.T) {
	s, root := guardFixture(t)
	s.DefaultRules.Final = settings.OutDirect
	s.RuleGroups = []settings.RuleGroup{{Name: "x", Enabled: true, Outbound: settings.OutProxy, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "1.1.1.0/24"}}}}
	r := RuleGuard(s, root)
	wantAddrs(t, r, []string{"8.8.8.8", "223.5.5.5"}, []string{"1.1.1.1", "198.18.0.1", "2001:4860:4860::8888"})
}

// 端口:只有走隧道 / 拒绝的组才进 TunnelPorts,直连的不进。
func TestRuleGuardTunnelPorts(t *testing.T) {
	s, root := guardFixture(t)
	s.RuleGroups = []settings.RuleGroup{
		{Name: "p", Enabled: true, Outbound: settings.OutProxy, Rules: []settings.Rule{{Type: settings.RulePort, Value: "8443"}, {Type: settings.RulePort, Value: "9000-9100"}}},
		{Name: "r", Enabled: true, Outbound: settings.OutReject, Rules: []settings.Rule{{Type: settings.RulePort, Value: "25"}}},
		{Name: "d", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RulePort, Value: "22"}}},
		{Name: "off", Enabled: false, Outbound: settings.OutProxy, Rules: []settings.Rule{{Type: settings.RulePort, Value: "23"}}},
	}
	r := RuleGuard(s, root)
	got := map[string]bool{}
	for _, p := range r.TunnelPorts {
		got[p] = true
	}
	if len(got) != 3 || !got["8443"] || !got["9000-9100"] || !got["25"] {
		t.Fatalf("实际 %v", r.TunnelPorts)
	}
}

// 网关模式的设备策略:只在网关模式进来。
func TestRuleGuardDevices(t *testing.T) {
	s, root := guardFixture(t)
	s.Devices = []settings.Device{{MAC: "aa:bb:cc:dd:ee:01", Mode: "proxy"}, {MAC: "aa:bb:cc:dd:ee:02", Mode: "direct"}, {MAC: "aa:bb:cc:dd:ee:03", Mode: "reject"}, {IP: "192.168.1.9", Mode: "direct"}, {MAC: "aa:bb:cc:dd:ee:04"}}
	if r := RuleGuard(s, root); len(r.DeviceBlock)+len(r.DeviceDirect) != 0 {
		t.Fatalf("本机模式不该有设备策略: %+v", r)
	}
	s.NetMode = settings.NetGateway
	r := RuleGuard(s, root)
	if len(r.DeviceBlock) != 2 || len(r.DeviceDirect) != 2 || r.DeviceDirect[1] != "192.168.1.9" {
		t.Fatalf("实际 %+v", r)
	}
}

// 规则集读不到:那一条当没有 —— 少放行,不多放行;别的规则照常。
func TestRuleGuardMissingRuleSet(t *testing.T) {
	s := settings.Default()
	r := RuleGuard(s, filepath.Join(t.TempDir(), "nothing"))
	wantAddrs(t, r, []string{"192.168.1.1"}, []string{"223.5.5.5", "8.8.8.8"})
}

// 指纹:规则变了就变,其它设置变了不变。
func TestRuleGuardKey(t *testing.T) {
	s, root := guardFixture(t)
	k := RuleGuardKey(s, root)
	s.LogDays = 99
	if RuleGuardKey(s, root) != k {
		t.Fatal("无关设置不该改指纹")
	}
	s.DefaultRules.CN = settings.OutProxy
	if RuleGuardKey(s, root) == k {
		t.Fatal("默认规则变了指纹要变")
	}
	s.DefaultRules = settings.FactoryDefaultRules()
	s.RuleGroups = []settings.RuleGroup{{Name: "x", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleIPCIDR, Value: "1.1.1.1"}}}}
	if RuleGuardKey(s, root) == k {
		t.Fatal("规则组变了指纹要变")
	}
}
