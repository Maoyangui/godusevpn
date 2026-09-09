package builder

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

func sampleProfile() *profile.Profile {
	return &profile.Profile{
		Tags: []string{"香港1", "台湾2"},
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"type":"hysteria2","tag":"香港1","server":"1.2.3.4","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
			json.RawMessage(`{"type":"anytls","tag":"台湾2","server":"1.2.3.5","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
		},
	}
}

type cfg struct {
	DNS struct {
		Servers  []map[string]any `json:"servers"`
		Rules    []map[string]any `json:"rules"`
		Strategy string           `json:"strategy"`
		Final    string           `json:"final"`
	} `json:"dns"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Route     struct {
		Rules   []map[string]any `json:"rules"`
		RuleSet []map[string]any `json:"rule_set"`
		Final   string           `json:"final"`
	} `json:"route"`
	Experimental struct {
		ClashAPI map[string]any `json:"clash_api"`
	} `json:"experimental"`
}

func build(t *testing.T, s settings.Settings) (cfg, string) {
	t.Helper()
	raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec"})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c, string(raw)
}

func TestDefaultsNoIPv6FakeIPTun(t *testing.T) {
	c, raw := build(t, settings.Default())
	if c.DNS.Strategy != "ipv4_only" {
		t.Fatalf("默认禁 IPv6:DNS 策略应为 ipv4_only,实际 %s", c.DNS.Strategy)
	}
	// 默认禁 IPv6:不给 fake-ip 分配 v6 段;但 TUN 仍要有 v6 地址,把 v6 接进来再拒绝,免得它绕过隧道直接出网
	if strings.Contains(raw, "inet6_range") {
		t.Fatal("默认不该给 fake-ip 分配 IPv6 段")
	}
	if !strings.Contains(raw, "fdfe:") {
		t.Fatal("TUN 应带 IPv6 地址以便捕获并拒绝 v6,否则会泄露")
	}
	var v6reject, hijack53, fake bool
	for _, r := range c.Route.Rules {
		if r["ip_version"] == float64(6) && r["action"] == "reject" {
			v6reject = true
		}
		if r["port"] == float64(53) && r["action"] == "hijack-dns" {
			hijack53 = true
		}
	}
	for _, sv := range c.DNS.Servers {
		if sv["type"] == "fakeip" {
			fake = true
		}
	}
	if !v6reject || !hijack53 || !fake {
		t.Fatalf("缺规则:v6reject=%v hijack53=%v fakeip=%v", v6reject, hijack53, fake)
	}
	if c.Inbounds[0]["type"] != "tun" || c.Inbounds[0]["strict_route"] != true || c.Inbounds[0]["stack"] != "mixed" {
		t.Fatalf("TUN 入站不对: %v", c.Inbounds[0])
	}
	if len(c.Inbounds) != 2 || c.Inbounds[1]["listen_port"] != float64(2080) {
		t.Fatalf("应有混合端口入站: %v", c.Inbounds)
	}
	if c.Outbounds[0]["tag"] != "proxy" || c.Outbounds[0]["default"] != "auto" || c.Outbounds[len(c.Outbounds)-1]["tag"] != "direct" {
		t.Fatalf("出站顺序: %v", c.Outbounds)
	}
	if c.Experimental.ClashAPI["default_mode"] != "Rule" || c.Experimental.ClashAPI["secret"] != "sec" {
		t.Fatalf("clash api: %v", c.Experimental.ClashAPI)
	}
	if c.Route.Final != "proxy" || c.DNS.Final != "remote" {
		t.Fatal("final 不对")
	}
	// 远程 DoH 经代理,本地 DoH 直连
	for _, sv := range c.DNS.Servers {
		if sv["tag"] == "remote" && sv["detour"] != "proxy" {
			t.Fatal("远程 DNS 应经代理")
		}
		if sv["tag"] == "local" && sv["detour"] != nil {
			t.Fatal("本地 DNS 应直连")
		}
	}
}

func TestIPv6OnAndOptions(t *testing.T) {
	s := settings.Default()
	s.IPv6, s.FakeIP, s.LANBypass, s.AdBlock, s.MixedPort = true, false, false, true, 0
	s.Mode, s.Selected, s.RemoteDNS = settings.ModeGlobal, "台湾2", "dns.google"
	c, raw := build(t, s)
	if c.DNS.Strategy != "prefer_ipv4" || !strings.Contains(raw, "fdfe:dcba:9876::1/126") {
		t.Fatal("开 IPv6 后应有 v6 地址与 prefer_ipv4")
	}
	for _, r := range c.Route.Rules {
		if r["ip_version"] == float64(6) {
			t.Fatal("开 IPv6 时不该有 v6 reject 规则")
		}
	}
	if strings.Contains(raw, `"fakeip"`) || strings.Contains(raw, "route_exclude_address") {
		t.Fatal("关掉 fake-ip / 局域网直通后不该出现")
	}
	if !strings.Contains(raw, "geosite-category-ads-all") {
		t.Fatal("开广告拦截应有广告规则集")
	}
	if len(c.Inbounds) != 1 {
		t.Fatal("混合端口 0 时只有 TUN 入站")
	}
	if c.Outbounds[0]["default"] != "台湾2" || c.Experimental.ClashAPI["default_mode"] != "Global" {
		t.Fatalf("选中节点 / 模式没带上: %v %v", c.Outbounds[0]["default"], c.Experimental.ClashAPI["default_mode"])
	}
	for _, sv := range c.DNS.Servers {
		if sv["tag"] == "remote" && sv["domain_resolver"] != "system" {
			t.Fatal("DoH 是域名时要给它配解析器")
		}
	}
}

func TestSelectedUnknownFallsBackToAuto(t *testing.T) {
	s := settings.Default()
	s.Selected = "已经不存在的节点"
	c, _ := build(t, s)
	if c.Outbounds[0]["default"] != "auto" {
		t.Fatal("订阅里没有的节点应回退到 auto")
	}
}

func TestLocalRuleSetPreferred(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/geosite-cn.srs", []byte("x")); err != nil {
		t.Fatal(err)
	}
	raw, err := Build(Input{Profile: sampleProfile(), Settings: settings.Default(), DataDir: t.TempDir(), RuleSetDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	_ = json.Unmarshal(raw, &c)
	var local, remote bool
	for _, rs := range c.Route.RuleSet {
		if rs["tag"] == "geosite-cn" && rs["type"] == "local" {
			local = true
		}
		if rs["tag"] == "geoip-cn" && rs["type"] == "remote" {
			remote = true
		}
	}
	if !local || !remote {
		t.Fatalf("有离线文件的用本地、没有的走远程: %v", c.Route.RuleSet)
	}
}

func TestModeNames(t *testing.T) {
	if ModeName("global") != "Global" || ModeName("direct") != "Direct" || ModeName("rule") != "Rule" || ModeName("") != "Rule" {
		t.Fatal("ModeName")
	}
	if SettingMode("GLOBAL") != "global" || SettingMode("Rule") != "rule" || SettingMode("x") != "rule" {
		t.Fatal("SettingMode")
	}
}

func TestBypassAppsRule(t *testing.T) {
	s := settings.Default()
	s.BypassApps = []string{"steam.exe", "qbittorrent.exe"}
	c, raw := build(t, s)
	var found bool
	for _, r := range c.Route.Rules {
		if r["outbound"] == "direct" && r["process_name"] != nil {
			found = true
		}
	}
	if !found || !strings.Contains(raw, "\"find_process\": true") {
		t.Fatalf("按进程直连应有 process_name 规则并开 find_process: %s", raw)
	}
	s.BypassApps = nil
	_, raw = build(t, s)
	if strings.Contains(raw, "process_name") || strings.Contains(raw, "find_process") {
		t.Fatal("没有进程规则时不该开 find_process")
	}
}

func TestProbeInterval(t *testing.T) {
	s := settings.Default()
	s.ProbeMinutes = 7
	_, raw := build(t, s)
	if !strings.Contains(raw, "\"interval\": \"7m\"") {
		t.Fatalf("auto 组的测速间隔应跟设置走: %s", raw)
	}
}

func TestAndroidPackageRules(t *testing.T) {
	s := settings.Default()
	s.BypassApps = []string{"com.tencent.mm"}
	s.RuleGroups = []settings.RuleGroup{{ID: "g1", Name: "游戏", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleProcess, Value: "com.example.game"}}}}
	raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", Android: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Contains(out, "process_name") {
		t.Fatalf("Android 上不该出现 process_name: %s", out)
	}
	for _, want := range []string{`"package_name": [`, `"com.example.game"`, `"exclude_package": [`, `"com.tencent.mm"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("Android 配置应含 %s: %s", want, out)
		}
	}
	raw, err = Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "android" && (strings.Contains(string(raw), "package_name") || strings.Contains(string(raw), "exclude_package")) {
		t.Fatal("桌面配置不该出现 package_name / exclude_package")
	}
}

// 关掉 IPv6 时,v6 也必须被接进隧道再拒绝;否则 v6 流量从物理网卡直接出网,等于绕过代理。
func TestIPv6CapturedEvenWhenDisabled(t *testing.T) {
	s := settings.Default()
	s.IPv6 = false
	c, raw := build(t, s)
	var tun map[string]any
	for _, in := range c.Inbounds {
		if in["type"] == "tun" {
			tun = in
		}
	}
	if tun == nil {
		t.Fatal("没有 TUN 入站")
	}
	addrs, _ := json.Marshal(tun["address"])
	if !strings.Contains(string(addrs), ":") {
		t.Fatalf("关闭 IPv6 时也要给 TUN 配 v6 地址,现在是 %s", addrs)
	}
	var rejected bool
	for _, r := range c.Route.Rules {
		if r["action"] == "reject" && fmt.Sprint(r["ip_version"]) == "6" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("缺少 IPv6 拒绝规则: %s", raw)
	}
}

// 默认规则可改:改成"国内也走代理、其余直连"后配置要跟着变;还原后回到出厂。
func TestDefaultRulesConfigurable(t *testing.T) {
	s := settings.Default()
	s.DefaultRules = settings.DefaultRules{Private: settings.OutReject, CN: settings.OutProxy, Final: settings.OutDirect}
	c, raw := build(t, s)
	if c.Route.Final != "direct" {
		t.Fatalf("其余流量应走 direct,实际 %q", c.Route.Final)
	}
	var privReject, cnProxy bool
	for _, r := range c.Route.Rules {
		if r["ip_is_private"] == true && r["action"] == "reject" {
			privReject = true
		}
		if fmt.Sprint(r["rule_set"]) == "[geosite-cn geoip-cn]" && r["outbound"] == "proxy" {
			cnProxy = true
		}
	}
	if !privReject || !cnProxy {
		t.Fatalf("默认规则没按设置生成: priv=%v cn=%v %s", privReject, cnProxy, raw)
	}
	s.DefaultRules = settings.FactoryDefaultRules()
	c2, _ := build(t, s)
	if c2.Route.Final != "proxy" {
		t.Fatalf("还原后其余流量应走 proxy,实际 %q", c2.Route.Final)
	}
}

// macOS 上隧道网卡名只能是内核分配的 utunN:配置里写死 interface_name 会让内核直接起不来
// (sing-tun 的 darwin 实现按 "utun%d" 解析名字,对不上就报 bad tun name)。
// 其它平台仍要写死名字,Linux 的策略路由、局域网扫描都按这个名字找网卡。
func TestTunInterfaceNameOnlyOffDarwin(t *testing.T) {
	for _, darwin := range []bool{true, false} {
		if !darwin && runtime.GOOS == "darwin" {
			continue // 就在 Mac 上跑时,平台自动判定会把它又变回 darwin,这一半测不了
		}
		raw, err := Build(Input{Profile: sampleProfile(), Settings: settings.Default(), DataDir: t.TempDir(), ClashSecret: "sec", Darwin: darwin})
		if err != nil {
			t.Fatal(err)
		}
		var c cfg
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		var tun map[string]any
		for _, in := range c.Inbounds {
			if in["type"] == "tun" {
				tun = in
			}
		}
		if tun == nil {
			t.Fatal("没有 TUN 入站")
		}
		name, ok := tun["interface_name"]
		if darwin && ok {
			t.Fatalf("macOS 上不能写死网卡名,现在是 %v", name)
		}
		if !darwin && name != TunName {
			t.Fatalf("非 macOS 要写死网卡名 %s,现在是 %v", TunName, name)
		}
	}
}

// macOS 上不管设置里选的是 mixed 还是 system,配置里都得落成 gvisor:
// 系统协议栈在 macOS 上握不了手(真机验收实测 TCP 全超时),换 gvisor 才通。其它平台照设置走。
func TestDarwinAlwaysGvisorStack(t *testing.T) {
	for _, want := range []struct{ set, darwin, off string }{
		{"mixed", "gvisor", "mixed"}, {"system", "gvisor", "system"}, {"gvisor", "gvisor", "gvisor"},
	} {
		s := settings.Default()
		s.TUNStack = want.set
		for _, darwin := range []bool{true, false} {
			if !darwin && runtime.GOOS == "darwin" {
				continue // 在 Mac 上跑时平台判定会把它变回 darwin,这一半测不了
			}
			raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", Darwin: darwin})
			if err != nil {
				t.Fatal(err)
			}
			var c cfg
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			exp := want.off
			if darwin {
				exp = want.darwin
			}
			for _, in := range c.Inbounds {
				if in["type"] == "tun" && in["stack"] != exp {
					t.Fatalf("设置 %s,darwin=%v 时协议栈应为 %s,实际 %v", want.set, darwin, exp, in["stack"])
				}
			}
		}
	}
}
