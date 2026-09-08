package settings

import (
	"strings"
	"testing"
)

func TestRuleGroupsNormalize(t *testing.T) {
	s := Default()
	s.RuleGroups = []RuleGroup{{Name: " ", Outbound: "", Rules: []Rule{
		{Type: RuleDomainSuffix, Value: "*.Google.com, youtube.com；x.example\nlast.example"},
		{Type: RuleIPCIDR, Value: "8.8.8.8 10.0.0.0/8"},
		{Type: RulePort, Value: "443 6000-7000"},
		{Type: RuleGeosite, Value: "geosite-Netflix"},
		{Type: RuleDomain, Value: "   "},
	}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	g := s.RuleGroups[0]
	if g.ID == "" || g.Name != "规则组 1" || g.Outbound != OutProxy {
		t.Fatalf("应补 id、名字与默认出口: %+v", g)
	}
	want := []Rule{
		{RuleDomainSuffix, "google.com"}, {RuleDomainSuffix, "youtube.com"}, {RuleDomainSuffix, "x.example"}, {RuleDomainSuffix, "last.example"},
		{RuleIPCIDR, "8.8.8.8/32"}, {RuleIPCIDR, "10.0.0.0/8"},
		{RulePort, "443"}, {RulePort, "6000-7000"},
		{RuleGeosite, "netflix"},
	}
	if len(g.Rules) != len(want) {
		t.Fatalf("拆分后应有 %d 条,实际 %d: %v", len(want), len(g.Rules), g.Rules)
	}
	for i := range want {
		if g.Rules[i] != want[i] {
			t.Fatalf("第 %d 条不对: %v ≠ %v", i, g.Rules[i], want[i])
		}
	}
}

func TestRuleGroupsReject(t *testing.T) {
	bad := []struct {
		name string
		rule Rule
	}{
		{"坏 IP 段", Rule{RuleIPCIDR, "1.2.3.400/24"}},
		{"坏端口", Rule{RulePort, "70000"}},
		{"倒序端口", Rule{RulePort, "9000-8000"}},
		{"坏正则", Rule{RuleDomainRegex, "("}},
		{"进程带路径", Rule{RuleProcess, `C:\x\a.exe`}},
		{"坏类别", Rule{RuleGeosite, "net/flix"}},
		{"未知类型", Rule{"host", "a"}},
	}
	for _, b := range bad {
		s := Default()
		s.RuleGroups = []RuleGroup{{Name: "g", Enabled: true, Rules: []Rule{b.rule}}}
		if err := s.Validate(); err == nil {
			t.Fatalf("%s 应被拒绝", b.name)
		} else if !strings.Contains(err.Error(), "规则组「g」") {
			t.Fatalf("%s 的错误应带组名: %v", b.name, err)
		}
	}
	s := Default()
	s.RuleGroups = []RuleGroup{{ID: "a", Name: "1"}, {ID: "a", Name: "2"}}
	if err := s.Validate(); err == nil {
		t.Fatal("重复 id 应被拒绝")
	}
}
