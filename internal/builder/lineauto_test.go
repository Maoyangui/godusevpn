package builder

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// groupProfile 两条线路各部署在两台服务器上(m-ui 的订阅就是这样),外加一个独立节点。
func groupProfile() *profile.Profile {
	p := &profile.Profile{}
	for _, n := range []string{
		`{"type":"hysteria2","tag":"香港1-A","server":"1.2.3.4","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`,
		`{"type":"hysteria2","tag":"香港1-B x2","server":"1.2.3.5","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`,
		`{"type":"anytls","tag":"日本-A","server":"1.2.3.6","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`,
		`{"type":"anytls","tag":"日本-B","server":"1.2.3.7","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`,
		`{"type":"trojan","tag":"美国","server":"1.2.3.8","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"c.example"}}`,
	} {
		var m struct {
			Tag string `json:"tag"`
		}
		_ = json.Unmarshal([]byte(n), &m)
		p.Outbounds = append(p.Outbounds, json.RawMessage(n))
		p.Tags = append(p.Tags, m.Tag)
	}
	p.Groups = []profile.Group{
		{Tag: "香港1", Members: []string{"香港1-A", "香港1-B x2"}},
		{Tag: "日本", Members: []string{"日本-A", "日本-B"}},
	}
	return p
}

func buildProfile(t *testing.T, p *profile.Profile, s settings.Settings) (cfg, string) {
	t.Helper()
	raw, rep, err := BuildEx(Input{Profile: p, Settings: s, DataDir: "/data", ClashSecret: "sec", RuleSetDir: ruleSetRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	_ = rep
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c, string(raw)
}

func outboundByTag(c cfg) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, o := range c.Outbounds {
		m[o["tag"].(string)] = o
	}
	return m
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// 没选线路组(自动选择、某个节点)时,订阅里有没有线路组,生成的配置逐字节一样:用不上的东西不进配置。
func TestLineGroupsAbsentUnlessSelected(t *testing.T) {
	withGroups := groupProfile()
	without := groupProfile()
	without.Groups = nil
	root := ruleSetRoot(t)
	for _, sel := range []string{"", "香港1-A", "美国", "已经不在的线路"} {
		s := settings.Default()
		s.Selected = sel
		a, _, err := BuildEx(Input{Profile: withGroups, Settings: s, DataDir: "/data", ClashSecret: "sec", RuleSetDir: root})
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := BuildEx(Input{Profile: without, Settings: s, DataDir: "/data", ClashSecret: "sec", RuleSetDir: root})
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("选的是 %q(不是线路组)时,带不带线路组配置应逐字节一样", sel)
		}
	}
	// 选的线路组已经不在订阅里、也没记着同线路的那一台(见 TestEffectiveSelection):回落到自动选择,配置里一个线路组都没有
	s := settings.Default()
	s.Selected = "旧线路"
	c, _ := buildProfile(t, withGroups, s)
	if c.Outbounds[0]["default"] != "auto" {
		t.Fatalf("不在的线路组应回落到 auto: %v", c.Outbounds[0]["default"])
	}
	for _, o := range c.Outbounds {
		if o["type"] == "urltest" && o["tag"] != "auto" {
			t.Fatalf("没选线路组时配置里不该有线路组: %v", o)
		}
	}
}

// 选中线路组:proxy = auto + 全部线路组 + 全部节点,默认是这一组;每条线路组都是测速组,
// 测速参数用我们自己的(TestURL、容差 50、sing-box 自己的定时测速关掉),和 auto 组一样;auto 照旧覆盖全部节点。
func TestLineGroupSelected(t *testing.T) {
	s := settings.Default()
	s.Selected = "日本"
	c, _ := buildProfile(t, groupProfile(), s)
	by := outboundByTag(c)
	proxy := c.Outbounds[0]
	if proxy["tag"] != "proxy" || proxy["default"] != "日本" {
		t.Fatalf("proxy 默认应是选中的线路组: %v", proxy)
	}
	if got := anyStrings(proxy["outbounds"]); !reflect.DeepEqual(got, []string{"auto", "香港1", "日本", "香港1-A", "香港1-B x2", "日本-A", "日本-B", "美国"}) {
		t.Fatalf("proxy 应列 auto、全部线路组、全部节点: %v", got)
	}
	for tag, members := range map[string][]string{"香港1": {"香港1-A", "香港1-B x2"}, "日本": {"日本-A", "日本-B"}} {
		g := by[tag]
		if g == nil || g["type"] != "urltest" || !reflect.DeepEqual(anyStrings(g["outbounds"]), members) {
			t.Fatalf("线路组 %s 不对: %v", tag, g)
		}
		if g["url"] != TestURL || g["tolerance"] != float64(50) || g["interval"] != "24h" || g["idle_timeout"] != "25h" {
			t.Fatalf("线路组的测速参数应和 auto 组一样(我们自己的地址、定时测速交给守护进程): %v", g)
		}
	}
	if got := anyStrings(by["auto"]["outbounds"]); len(got) != 5 {
		t.Fatalf("auto 照旧覆盖全部节点: %v", got)
	}
	// 线路组排在 auto 之后、节点之前
	var order []string
	for _, o := range c.Outbounds[:4] {
		order = append(order, o["tag"].(string))
	}
	if !reflect.DeepEqual(order, []string{"proxy", "auto", "香港1", "日本"}) {
		t.Fatalf("出站顺序: %v", order)
	}
}

// SelectedFor:节点或线路组还在订阅里就是它,否则自动选择;DefaultSelected 只认节点。
func TestSelectedFor(t *testing.T) {
	p := groupProfile()
	for sel, want := range map[string]string{"": "auto", "日本": "日本", "美国": "美国", "旧线路": "auto"} {
		if got := SelectedFor(p, sel); got != want {
			t.Fatalf("SelectedFor(%q) = %q,应 %q", sel, got, want)
		}
	}
	if SelectedFor(nil, "日本") != "auto" {
		t.Fatal("没有订阅时回落到自动选择")
	}
	if DefaultSelected(p.Tags, "日本") != "auto" || DefaultSelected(p.Tags, "美国") != "美国" {
		t.Fatal("DefaultSelected 只认节点")
	}
}

// 选的线路组在订阅里没了(线路改成只部署一台:服务器下线、入口停用、只分到一台):用记着的同线路那一台,
// 线路、出口不变;那台也不在了才回落自动选择。没选线路组时和 SelectedFor 一样。
func TestEffectiveSelection(t *testing.T) {
	p := groupProfile()
	p.Groups = p.Groups[1:] // 「香港1」这一组没了,两台成员还在(或只剩一台)
	for _, c := range []struct{ sel, node, want string }{
		{"香港1", "香港1-B x2", "香港1-B x2"}, // 组没了:用记着的那一台
		{"香港1", "已删", "auto"},           // 记着的那台也没了:自动选择
		{"香港1", "", "auto"},
		{"日本", "日本-B", "日本"}, // 组还在:就是这一组
		{"美国", "", "美国"},
		{"", "", "auto"},
		{"已删的节点", "", "auto"},
	} {
		s := settings.Default()
		s.Selected, s.SelectedNode = c.sel, c.node
		if got := Effective(p, s); got != c.want {
			t.Fatalf("选 %q(记着 %q):得 %q,应 %q", c.sel, c.node, got, c.want)
		}
	}
	s := settings.Default()
	s.Selected, s.SelectedNode = "香港1", "香港1-A"
	c, _ := buildProfile(t, p, s)
	if c.Outbounds[0]["default"] != "香港1-A" {
		t.Fatalf("组没了时配置按同线路那一台连: %v", c.Outbounds[0]["default"])
	}
}
