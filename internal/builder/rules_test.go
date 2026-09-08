package builder

import (
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

func findRule(c cfg, pred func(map[string]any) bool) map[string]any {
	for _, r := range c.Route.Rules {
		if pred(r) {
			return r
		}
	}
	return nil
}

func TestRuleGroupsRendered(t *testing.T) {
	s := settings.Default()
	s.RuleGroups = []settings.RuleGroup{
		{Name: "流媒体", Enabled: true, Outbound: "台湾2", Rules: []settings.Rule{
			{Type: settings.RuleDomainSuffix, Value: "netflix.com"}, {Type: settings.RuleGeosite, Value: "netflix"}, {Type: settings.RulePort, Value: "8000-9000"}, {Type: settings.RulePort, Value: "443"},
		}},
		{Name: "广告", Enabled: true, Outbound: settings.OutReject, Rules: []settings.Rule{{Type: settings.RuleDomainKeyword, Value: "adservice"}}},
		{Name: "游戏直连", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleProcess, Value: "game.exe"}, {Type: settings.RuleIPCIDR, Value: "1.2.3.4"}}},
		{Name: "没了的节点", Enabled: true, Outbound: "不存在", Rules: []settings.Rule{{Type: settings.RuleDomain, Value: "x.example"}}},
		{Name: "关掉的", Enabled: false, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleDomain, Value: "off.example"}}},
		{Name: "空的", Enabled: true, Outbound: settings.OutDirect},
	}
	c, raw := build(t, s)

	stream := findRule(c, func(r map[string]any) bool { return r["outbound"] == "台湾2" })
	if stream == nil || stream["type"] != "logical" || stream["mode"] != "or" {
		t.Fatalf("多种条件应合成 logical/or 规则: %v", stream)
	}
	parts := stream["rules"].([]any)
	if len(parts) != 4 { // domain_suffix、port、port_range、rule_set
		t.Fatalf("应有 4 个分支,实际 %d: %v", len(parts), parts)
	}
	if !strings.Contains(raw, `"8000:9000"`) || !strings.Contains(raw, `"geosite-netflix"`) {
		t.Fatalf("端口范围与 geosite 规则集没渲染对: %s", raw)
	}
	var netflixSet bool
	for _, rs := range c.Route.RuleSet {
		if rs["tag"] == "geosite-netflix" && strings.HasSuffix(rs["url"].(string), "geosite-netflix.srs") {
			netflixSet = true
		}
	}
	if !netflixSet {
		t.Fatal("引用的 geosite 类别应自动加进 rule_set")
	}

	ad := findRule(c, func(r map[string]any) bool { return r["domain_keyword"] != nil })
	if ad == nil || ad["action"] != "reject" || ad["outbound"] != nil {
		t.Fatalf("拒绝出口应渲染成 action reject: %v", ad)
	}

	game := findRule(c, func(r map[string]any) bool { return r["type"] == "logical" && r["outbound"] == "direct" })
	if game == nil || !strings.Contains(raw, `"1.2.3.4/32"`) || !strings.Contains(raw, `"find_process": true`) {
		t.Fatalf("进程 + IP 直连组没渲染对: %v %s", game, raw)
	}

	gone := findRule(c, func(r map[string]any) bool { return r["domain"] != nil })
	if gone == nil || gone["outbound"] != "proxy" {
		t.Fatalf("节点不在订阅里应回落到 proxy: %v", gone)
	}
	if strings.Contains(raw, "off.example") {
		t.Fatal("关掉的组不该出现")
	}

	// 顺序:用户规则在 clash_mode 之后、内置国内直连之前
	var iMode, iUser, iCN = -1, -1, -1
	for i, r := range c.Route.Rules {
		switch {
		case r["clash_mode"] == "Global":
			iMode = i
		case r["outbound"] == "台湾2":
			iUser = i
		case r["outbound"] == "direct" && r["rule_set"] != nil:
			iCN = i
		}
	}
	if !(iMode < iUser && iUser < iCN) {
		t.Fatalf("规则顺序不对: mode=%d user=%d cn=%d", iMode, iUser, iCN)
	}
}

func TestIPv6OnStillResolvesForIPRules(t *testing.T) {
	s := settings.Default()
	s.IPv6 = true
	c, _ := build(t, s)
	if findRule(c, func(r map[string]any) bool { return r["action"] == "resolve" }) == nil {
		t.Fatal("开 IPv6 也要有 resolve 动作,否则 IP 类规则对域名连接不生效")
	}
}
