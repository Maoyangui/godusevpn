package core

import (
	"encoding/json"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 生成出来的配置必须能被内嵌 sing-box 干跑通过:字段名、类型、废弃项都会在这里暴露。
func TestBuiltConfigPassesDryRun(t *testing.T) {
	p := &profile.Profile{
		Tags: []string{"hk", "tw"},
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"type":"hysteria2","tag":"hk","server":"1.2.3.4","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
			json.RawMessage(`{"type":"anytls","tag":"tw","server":"1.2.3.5","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
		},
	}
	c := New(nil)
	for _, s := range []settings.Settings{settings.Default(), func() settings.Settings {
		s := settings.Default()
		s.IPv6, s.AdBlock, s.TUNStack, s.RemoteDNS, s.LocalDNS = true, true, "gvisor", "dns.google", "system"
		return s
	}(), func() settings.Settings {
		s := settings.Default()
		s.TUN, s.FakeIP = false, false
		return s
	}(), func() settings.Settings {
		s := settings.Default()
		s.RuleGroups = []settings.RuleGroup{
			{Name: "流媒体", Enabled: true, Outbound: "tw", Rules: []settings.Rule{{Type: settings.RuleDomainSuffix, Value: "netflix.com"}, {Type: settings.RuleGeosite, Value: "netflix"}, {Type: settings.RuleGeoIP, Value: "us"}, {Type: settings.RulePort, Value: "8000-9000"}, {Type: settings.RulePort, Value: "443"}, {Type: settings.RuleDomainRegex, Value: "^cdn[0-9]+\\."}}},
			{Name: "广告", Enabled: true, Outbound: settings.OutReject, Rules: []settings.Rule{{Type: settings.RuleDomainKeyword, Value: "adservice"}}},
			{Name: "游戏", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleProcess, Value: "game.exe"}, {Type: settings.RuleIPCIDR, Value: "1.2.3.0/24"}}},
			{Name: "单条", Enabled: true, Outbound: "auto", Rules: []settings.Rule{{Type: settings.RuleDomain, Value: "one.example"}}},
		}
		return s
	}(), func() settings.Settings {
		// 手动指定节点时自动选择组换成很长的测速间隔:间隔与 idle_timeout 的关系写错,内核会直接拒绝启动
		s := settings.Default()
		s.Selected = "hk"
		return s
	}()} {
		raw, err := builder.Build(builder.Input{Profile: p, Settings: s, DataDir: t.TempDir(), ClashSecret: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(raw); err != nil {
			t.Fatalf("干跑失败: %v\n%s", err, raw)
		}
	}
	if err := c.Validate([]byte(`{"inbounds":[{"type":"nope"}]}`)); err == nil {
		t.Fatal("坏配置应被拒绝")
	}
}
