package builder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/ruleset"
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

// ruleSetRoot 造一个"装好之后"的规则集目录:内置的三个都在,extra 里的当成用户下下来的那一层。
// 测试默认都用它,免得测出来的是一台还没装规则集的机器上的样子。
func ruleSetRoot(t *testing.T, extra ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := ruleset.Install(root); err != nil {
		t.Fatal(err)
	}
	for _, tag := range extra {
		// 内容得是内核真读得动的:查找那一步会整份解一遍,解不开就当它不存在(见 ruleset.Find)
		if err := writeFile(filepath.Join(root, tag+".srs"), ruleset.Bytes("geosite-cn")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func build(t *testing.T, s settings.Settings) (cfg, string) {
	t.Helper()
	return buildWith(t, s, ruleSetRoot(t))
}

func buildWith(t *testing.T, s settings.Settings, root string) (cfg, string) {
	t.Helper()
	raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: root})
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
	wantStack := "mixed" // macOS 上会被强制改成 gvisor(系统协议栈在那儿握不了手)
	if runtime.GOOS == "darwin" {
		wantStack = "gvisor"
	}
	if c.Inbounds[0]["type"] != "tun" || c.Inbounds[0]["strict_route"] != true || c.Inbounds[0]["stack"] != wantStack {
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
	// 全局禁直连下:远程 DNS 是域名时,它的域名经代理解析(remote-boot,按地址连),不走隧道外的 system
	var boot bool
	for _, sv := range c.DNS.Servers {
		if sv["tag"] == "remote" && sv["domain_resolver"] != "remote-boot" {
			t.Fatalf("全局禁直连下远程 DNS 的域名要经代理解析,得到 %v", sv["domain_resolver"])
		}
		if sv["tag"] == "remote-boot" {
			boot = true
			if sv["detour"] != "proxy" || sv["server"] != RemoteBootstrapDoH {
				t.Fatalf("remote-boot 要经代理、按地址连: %v", sv)
			}
		}
	}
	if !boot {
		t.Fatal("缺 remote-boot")
	}
	// 不是禁直连时照旧用 system
	s2 := s
	s2.NoDirect = false
	c2, _ := build(t, s2)
	for _, sv := range c2.DNS.Servers {
		if sv["tag"] == "remote" && sv["domain_resolver"] != "system" {
			t.Fatalf("禁直连关着时远程 DNS 的域名照旧用 system 解析,得到 %v", sv["domain_resolver"])
		}
		if sv["tag"] == "remote-boot" {
			t.Fatal("禁直连关着时不该有 remote-boot")
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

// 规则集永远不能写成 type: remote。sing-box 在没有缓存时是**在启动阶段同步下载**远程规则集的,
// 一条下不到,box.Start() 就整个失败(route/router.go 那组是 FastFail),客户端于是变成
// "能不能连取决于此刻能不能访问 GitHub"。2026-09-18 用户的索尼电视就是这样卡死的:
// 界面只显示「内核启动失败,自动重试中」,全局和规则模式都连不上。
// 这条测试是那次的回归闸门 —— 改配置生成时如果又想用远程规则集,先想清楚上面这段。
func TestRuleSetsNeverRemote(t *testing.T) {
	s := settings.Default()
	s.AdBlock = true
	s.RuleGroups = []settings.RuleGroup{{Name: "流媒体", Enabled: true, Outbound: settings.OutProxy,
		Rules: []settings.Rule{{Type: settings.RuleGeosite, Value: "netflix"}, {Type: settings.RuleGeoIP, Value: "jp"}}}}
	c, _ := buildWith(t, s, ruleSetRoot(t, "geosite-netflix", "geoip-jp"))
	if len(c.Route.RuleSet) == 0 {
		t.Fatal("这组设置下应该有规则集")
	}
	for _, rs := range c.Route.RuleSet {
		if rs["type"] != "local" {
			t.Fatalf("规则集 %v 不是 local —— 内核启动会去联网下,下不到就整个起不来", rs)
		}
		// remote 才有的几个键一个都不能留下
		for _, k := range []string{"url", "download_detour", "update_interval"} {
			if rs[k] != nil {
				t.Fatalf("本地规则集不该带 %s: %v", k, rs)
			}
		}
		// path 必须是一个**真的存在**的文件。只判 != nil 挡不住空串:
		// 规则集没找到却照样写进配置时,path 是 "",内核启动时按空路径去读,一样起不来。
		// (这条是自己做变异测试时发现的:把"找不到就摘掉"那一步弄坏,原来的断言居然还是绿的。)
		p, _ := rs["path"].(string)
		if p == "" {
			t.Fatalf("本地规则集的 path 是空的: %v", rs)
		}
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("规则集 %v 指向的文件不存在或是空的: %v", rs["tag"], err)
		}
	}
}

// 内置的三个在,配置就应该引用它们。
func TestBuiltinRuleSetsUsed(t *testing.T) {
	s := settings.Default()
	s.AdBlock = true
	c, _ := build(t, s)
	got := map[string]bool{}
	for _, rs := range c.Route.RuleSet {
		got[rs["tag"].(string)] = true
	}
	for _, tag := range ruleset.Builtin() {
		if !got[tag] {
			t.Fatalf("内置规则集 %s 没被用上: %v", tag, c.Route.RuleSet)
		}
	}
}

// 一个规则集都没有的机器(全新安装、内置文件被杀毒软件删了、目录被清过)也必须能生成出
// 一份内核起得来的配置:少几条规则可以,连不上不行。
func TestNoRuleSetsStillBuilds(t *testing.T) {
	s := settings.Default()
	s.AdBlock = true
	raw, rep, err := BuildEx(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: t.TempDir()})
	if err != nil {
		t.Fatalf("一个规则集都没有就生成不出配置了: %v", err)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Route.RuleSet) != 0 {
		t.Fatalf("没有本地文件却还是写了规则集: %v", c.Route.RuleSet)
	}
	// 配置里任何地方都不能再引用这些标签,引用一个不存在的规则集同样会让内核起不来
	if strings.Contains(string(raw), "geosite-cn") || strings.Contains(string(raw), "geoip-cn") {
		t.Fatalf("规则集摘掉了,却还有地方引用它: %s", raw)
	}
	if len(rep.Missing) != 3 {
		t.Fatalf("缺了的三个应该都报出来,实际: %v", rep.Missing)
	}
	for _, m := range rep.Missing {
		if !strings.HasSuffix(m.URL, m.Tag+".srs") {
			t.Fatalf("缺失项要带上能去下载的地址: %v", m)
		}
	}
}

// 规则组只写了 geosite,而那个规则集本地没有:整条规则要摘掉,不能留一条空的 logical/or。
func TestRuleGroupDroppedWhenItsOnlyRuleSetMissing(t *testing.T) {
	s := settings.Default()
	s.RuleGroups = []settings.RuleGroup{{Name: "流媒体", Enabled: true, Outbound: settings.OutProxy,
		Rules: []settings.Rule{{Type: settings.RuleGeosite, Value: "netflix"}}}}
	raw, rep, err := BuildEx(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "geosite-netflix") {
		t.Fatalf("本地没有的规则集不该出现在配置里: %s", raw)
	}
	var c cfg
	_ = json.Unmarshal(raw, &c)
	for _, r := range c.Route.Rules {
		if r["type"] == "logical" {
			if rules, ok := r["rules"].([]any); ok && len(rules) == 0 {
				t.Fatalf("留下了一条没有任何条件的规则: %v", r)
			}
		}
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Tag != "geosite-netflix" {
		t.Fatalf("应当只报 geosite-netflix 缺失,实际: %v", rep.Missing)
	}
}

// 用户自己往 rulesets/ 里放的同名文件优先级最高,盖过内置的那份。
func TestUserRuleSetWins(t *testing.T) {
	root := ruleSetRoot(t)
	if err := writeFile(root+"/geosite-cn.srs", ruleset.Bytes("geoip-cn")); err != nil { // 内容换一份,只要内核读得动就行
		t.Fatal(err)
	}
	raw, _, err := BuildEx(Input{Profile: sampleProfile(), Settings: settings.Default(), DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: root})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	_ = json.Unmarshal(raw, &c)
	for _, rs := range c.Route.RuleSet {
		if rs["tag"] == "geosite-cn" {
			if p, _ := rs["path"].(string); !strings.HasSuffix(filepath.ToSlash(p), "/geosite-cn.srs") || strings.Contains(filepath.ToSlash(p), "/builtin/") {
				t.Fatalf("应该用用户放的那份,实际: %s", p)
			}
			return
		}
	}
	t.Fatal("配置里没有 geosite-cn")
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

// "定时测速(分钟)"不写进配置:由守护进程按它定时叫 auto 组测一轮(Core.GroupTest),配置里 sing-box 自己的
// 定时测速一律关掉。写进配置的话自动 / 手动之间切换就得重建配置重连,断几秒网。
func TestProbeInterval(t *testing.T) {
	s := settings.Default()
	s.ProbeMinutes = 7
	_, raw := build(t, s)
	if strings.Contains(raw, "\"interval\": \"7m\"") {
		t.Fatalf("测速间隔不该写进配置(由守护进程定时叫): %s", raw)
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

// 节点服务器必须直连,而且要排在模式规则前面:否则内核自己去连节点(定时测速每轮都要连一遍)
// 会被自己的 TUN 接住再按模式转出去,全局模式下就绕成"本机 → 隧道 → 当前节点 → 目标节点"。
func TestNodeServersBypassProxy(t *testing.T) {
	p := sampleProfile()
	p.Tags = append(p.Tags, "日本3")
	p.Outbounds = append(p.Outbounds, json.RawMessage(`{"type":"anytls","tag":"日本3","server":"az.example.com","server_port":42222,"password":"p","tls":{"enabled":true,"server_name":"az.example.com"}}`))
	raw, err := Build(Input{Profile: p, Settings: settings.Default(), DataDir: t.TempDir(), ClashSecret: "sec"})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	domainAt, cidrAt, modeAt := -1, -1, -1
	for i, r := range c.Route.Rules {
		switch {
		case r["outbound"] == "direct" && r["domain"] != nil:
			if fmt.Sprint(r["domain"]) != "[az.example.com]" || fmt.Sprint(r["port"]) != "[42222]" {
				t.Fatalf("直连的节点域名 / 端口不对: %v %v", r["domain"], r["port"])
			}
			domainAt = i
		case r["outbound"] == "direct" && r["ip_cidr"] != nil:
			// 一台服务器一条规则,只放行它自己那些端口 —— 面板、订阅地址常与节点同 IP,不能整台放行
			got := fmt.Sprint(r["ip_cidr"]) + " " + fmt.Sprint(r["port"])
			if got != "[1.2.3.4/32] [443]" && got != "[1.2.3.5/32] [8443]" {
				t.Fatalf("直连的节点 IP / 端口不对: %s", got)
			}
			cidrAt = i
		case r["clash_mode"] != nil && modeAt < 0:
			modeAt = i
		}
	}
	if domainAt < 0 || cidrAt < 0 {
		t.Fatalf("缺少节点服务器直连规则(域名 %d,IP %d)", domainAt, cidrAt)
	}
	if modeAt < 0 || domainAt > modeAt || cidrAt > modeAt {
		t.Fatalf("节点直连规则必须排在模式规则前面:域名 %d,IP %d,模式 %d", domainAt, cidrAt, modeAt)
	}
	// 同一台服务器上的其它端口(面板、订阅)不能被放行
	for _, r := range c.Route.Rules {
		if r["outbound"] == "direct" && fmt.Sprint(r["ip_cidr"]) == "[1.2.3.4/32]" {
			if p := fmt.Sprint(r["port"]); strings.Contains(p, "2053") {
				t.Fatalf("规则把面板端口也放行了: %s", p)
			}
		}
	}
}

// 换一份完全不同的订阅(域名 + 别的端口)照样要盖住:规则是从订阅里现算的,没有写死任何地址。
// 域名节点靠嗅探到的 SNI 命中;嗅不出域名的协议(比如不带 TLS 的 shadowsocks)靠解析出来的地址兜底。
func TestNodeRulesFollowProfile(t *testing.T) {
	p := &profile.Profile{
		Tags: []string{"甲", "乙"},
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"type":"shadowsocks","tag":"甲","server":"a.new-provider.net","server_port":8388,"method":"aes-128-gcm","password":"p"}`),
			json.RawMessage(`{"type":"anytls","tag":"乙","server":"a.new-provider.net","server_port":9443,"password":"p"}`),
		},
	}
	raw, err := Build(Input{Profile: p, Settings: settings.Default(), DataDir: t.TempDir(), ClashSecret: "sec",
		NodeIPs: map[string][]string{"a.new-provider.net": {"203.0.113.7", "198.18.0.9", "127.0.0.1", "10.1.2.3"}}})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	var byDomain, byIP map[string]any
	modeAt := -1
	for i, r := range c.Route.Rules {
		switch {
		case r["outbound"] == "direct" && r["domain"] != nil:
			byDomain = r
		case r["outbound"] == "direct" && r["ip_cidr"] != nil:
			byIP = r
		case r["clash_mode"] != nil && modeAt < 0:
			modeAt = i
		}
	}
	if byDomain == nil || byIP == nil {
		t.Fatalf("域名节点要同时有按域名与按地址两条规则:%v %v", byDomain, byIP)
	}
	if fmt.Sprint(byDomain["domain"]) != "[a.new-provider.net]" {
		t.Fatalf("域名不对: %v", byDomain["domain"])
	}
	// 两个端口归到同一台服务器;解析结果里的 fake-ip、回环、私网都要挡掉
	for _, r := range []map[string]any{byDomain, byIP} {
		if got := fmt.Sprint(r["port"]); got != "[8388 9443]" {
			t.Fatalf("端口应归并成两个: %s", got)
		}
	}
	if got := fmt.Sprint(byIP["ip_cidr"]); got != "[203.0.113.7/32]" {
		t.Fatalf("只应保留真实公网地址(fake-ip / 回环 / 私网要挡掉): %s", got)
	}
}

// auto 组的配置不随"自动 / 手动"变(两者之间切换才能就地完成、不重连):sing-box 自己的定时测速一律关掉,
// 关掉的写法是给一个很长的间隔 —— interval 留空会退回 sing-box 默认的三分钟,idle_timeout 必须不小于它。
func TestProbeOnlyWhenAuto(t *testing.T) {
	find := func(s settings.Settings) map[string]any {
		raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec"})
		if err != nil {
			t.Fatal(err)
		}
		var c cfg
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		for _, o := range c.Outbounds {
			if o["tag"] == "auto" {
				return o
			}
		}
		t.Fatal("没有自动选择组")
		return nil
	}

	s := settings.Default()
	s.ProbeMinutes = 5
	auto := find(s) // Selected 为空 = 自动选择
	if auto["interval"] != "24h" || auto["idle_timeout"] != "25h" {
		t.Fatalf("自动选择时 sing-box 自己的定时测速也要关掉(由守护进程定时叫),实际 %v / %v", auto["interval"], auto["idle_timeout"])
	}

	s.Selected = "香港1"
	fixed := find(s)
	if fixed["interval"] != "24h" || fixed["idle_timeout"] != "25h" {
		t.Fatalf("手动指定节点时 auto 组应与自动选择时一模一样(切换才能不重建配置),实际 %v / %v", fixed["interval"], fixed["idle_timeout"])
	}
}

// Android(开着 TUN)上不生成混合入站:流量全走 TUN,没人会去连它,它却是启动期硬依赖。
//
// 但 **Clash API 必须照样监听**。v0.6.23-m26 在 Android 上把它也关了,理由是「守护进程在进程内取用」——
// 那只对切模式、选节点成立;界面服务层(internal/uiapi)的实时网速、连接列表、断开连接、连着时测延迟
// 全是经这个 HTTP 口取的,用户手机上「连接」页直接报
// get http://127.0.0.1:9090/connections: connection refused,网速恒为 0。
// 端口被占的问题改由守护进程挑一个空闲端口解决(Input.ClashPort),而不是不监听。
func TestAndroidListeners(t *testing.T) {
	s := settings.Default()
	raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t), Android: true})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for _, in := range c.Inbounds {
		if in["type"] == "mixed" {
			t.Fatalf("开着 TUN 的 Android 上不该有混合入站: %v", in)
		}
	}
	if got, _ := c.Experimental.ClashAPI["external_controller"].(string); got != "127.0.0.1:9090" {
		t.Fatalf("Android 上 Clash API 必须监听(连接页、网速、测延迟都靠它),实际 %q", got)
	}

	// 守护进程挑了别的端口(设置里那个被占着)时,配置要跟着它走
	raw, err = Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t), Android: true, ClashPort: 51234})
	if err != nil {
		t.Fatal(err)
	}
	var d cfg
	_ = json.Unmarshal(raw, &d)
	if got, _ := d.Experimental.ClashAPI["external_controller"].(string); got != "127.0.0.1:51234" {
		t.Fatalf("应该监听守护进程挑的端口,实际 %q", got)
	}
}

// Android 上关掉 TUN(设置里允许)时,混合端口必须留着 —— 否则渲染出来的是一份一个入站都没有的配置:
// 内核起得来、通知栏也在,一点流量都不过,而且没有任何报错。settings.Validate 的
// 「TUN 关了就必须开混合端口,否则没有任何入口」这条不变量,靠的就是它一定会被生成。
func TestAndroidKeepsMixedInboundWhenTunOff(t *testing.T) {
	s := settings.Default()
	s.TUN = false
	s.MixedPort = 2080
	raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t), Android: true})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Inbounds) == 0 {
		t.Fatal("一个入站都没有:内核会起来,但一点流量都不过,而且不报错")
	}
	if c.Inbounds[0]["type"] != "mixed" {
		t.Fatalf("关掉 TUN 后唯一的入口应该是混合端口: %v", c.Inbounds)
	}
}
