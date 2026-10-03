package builder

import (
	"encoding/json"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// DNS 规则里不能再有 outbound 项:sing-box 把它排在 1.14.0 移除,升级到真正删掉它的内核时整份配置解析失败、
// 所有人都连不上。节点域名改由 route.default_domain_resolver(local)解析,各模式都一样。
func TestDNSRulesHaveNoOutboundItem(t *testing.T) {
	for _, mode := range []string{settings.ModeGlobal, settings.ModeRule, settings.ModeDirect} {
		for _, noDirect := range []bool{false, true} {
			s := settings.Default()
			s.Mode, s.NoDirect = mode, noDirect
			raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t)})
			if err != nil {
				t.Fatal(err)
			}
			var c struct {
				DNS struct {
					Rules []map[string]any `json:"rules"`
				} `json:"dns"`
				Route struct {
					DefaultDomainResolver string `json:"default_domain_resolver"`
				} `json:"route"`
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			for _, r := range c.DNS.Rules {
				if _, ok := r["outbound"]; ok {
					t.Fatalf("模式 %s 禁直连 %v:DNS 规则还在用已排期移除的 outbound 项: %v", mode, noDirect, r)
				}
			}
			if c.Route.DefaultDomainResolver != "local" {
				t.Fatalf("模式 %s 禁直连 %v:节点域名应由 local 解析,实际 %q", mode, noDirect, c.Route.DefaultDomainResolver)
			}
		}
	}
}
