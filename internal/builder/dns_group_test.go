package builder

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	R "github.com/sagernet/sing-box/route/rule"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

const (
	qtA     = 1
	qtAAAA  = 28
	qtHTTPS = 65
)

// dnsRoute 用 sing-box 自己的 DNS 规则引擎把生成的 DNS 规则从上往下过一遍(引用规则集的跳过,
// 测试里的域名都不在规则集里)。返回交给哪台服务器("reject" = 拒绝)与命中规则的下标(-1 = final)。
func dnsRoute(t *testing.T, c cfg, mode, domain string, qtype uint16) (string, int) {
	t.Helper()
	ctx := clashModeCtx(mode)
	logger := log.NewNOPFactory().Logger()
	md := adapter.InboundContext{Domain: domain, QueryType: qtype}
	for i, raw := range c.DNS.Rules {
		b, _ := json.Marshal(raw)
		if strings.Contains(string(b), `"rule_set"`) {
			continue
		}
		var opt option.DNSRule
		if err := opt.UnmarshalJSONContext(ctx, b); err != nil {
			t.Fatalf("DNS 规则 %d 解不开: %v\n%s", i, err, b)
		}
		r, err := R.NewDNSRule(ctx, logger, opt, false, false)
		if err != nil {
			t.Fatalf("DNS 规则 %d 建不起来: %v\n%s", i, err, b)
		}
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		md.ResetRuleCache()
		matched := r.Match(&md)
		_ = r.Close()
		if !matched {
			continue
		}
		switch a := r.Action().(type) {
		case *R.RuleActionDNSRoute:
			return a.Server, i
		case *R.RuleActionReject:
			return "reject", i
		default:
			t.Fatalf("DNS 规则 %d 的动作没料到: %T", i, a)
		}
	}
	return c.DNS.Final, -1
}

// 规则组里走代理 / 拒绝的域名不能先命中"国内域名交给本地 DNS":想从海外出口看的国内站,以前照样先在国内直连解析、
// 拿到国内地址。现在走代理的按其余域名处理(A 给 fake-ip、别的查询交给远程 DNS),拒绝的连查询一起拒掉,
// 直连的不生成;只在规则模式生效。路由是排前面的组先赢:前面的直连组写过的域名,后面的组在 DNS 里也让开。
func TestRuleGroupDomainsSkipLocalDNS(t *testing.T) {
	s := settings.Default()
	s.RuleGroups = []settings.RuleGroup{
		{Name: "先直连", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleDomainSuffix, Value: "live.bilibili.com"}}},
		{Name: "海外看国内站", Enabled: true, Outbound: settings.OutProxy, Rules: []settings.Rule{{Type: settings.RuleDomainSuffix, Value: "bilibili.com"}, {Type: settings.RuleProcess, Value: "x.exe"}}},
		{Name: "拦截", Enabled: true, Outbound: settings.OutReject, Rules: []settings.Rule{{Type: settings.RuleDomainKeyword, Value: "tracker"}}},
		{Name: "直连", Enabled: true, Outbound: settings.OutDirect, Rules: []settings.Rule{{Type: settings.RuleDomainSuffix, Value: "direct.example"}, {Type: settings.RuleDomain, Value: "x.tracker.net"}, {Type: settings.RuleDomain, Value: "both.example"}}},
		{Name: "指定节点", Enabled: true, Outbound: "台湾2", Rules: []settings.Rule{{Type: settings.RuleDomain, Value: "abc.example"}, {Type: settings.RuleDomain, Value: "both.example"}}},
		{Name: "停用的", Enabled: false, Outbound: settings.OutProxy, Rules: []settings.Rule{{Type: settings.RuleDomain, Value: "off.example"}}},
	}
	c, raw := build(t, s)
	fakeAt := -1
	cnAt := -1
	for i, r := range c.DNS.Rules {
		if r["server"] == "fakeip" && r["query_type"] != nil {
			fakeAt = i
		}
		if fmt.Sprint(r["rule_set"]) == "[geosite-cn]" {
			cnAt = i
		}
	}
	if cnAt < 0 || fakeAt < 0 {
		t.Fatalf("前提:应有国内 → 本地与通用 fake-ip 两条 DNS 规则\n%s", raw)
	}
	for _, tc := range []struct {
		mode, domain string
		qtype        uint16
		want         string
		group        bool // 应命中规则组生成的那几条(排在国内 → 本地之前)
	}{
		{"Rule", "www.bilibili.com", qtA, "fakeip", true},
		{"Rule", "www.bilibili.com", qtHTTPS, "remote", true},
		{"Rule", "x.tracker.net", qtA, "reject", true},        // 后面的直连组也写了它:排前面的拒绝组照样先赢
		{"Rule", "x.live.bilibili.com", qtA, "fakeip", false}, // 排在前面的直连组写了它:路由归直连,DNS 让开按通用规则走
		{"Rule", "x.live.bilibili.com", qtHTTPS, "remote", false},
		{"Rule", "both.example", qtA, "fakeip", false},
		{"Rule", "abc.example", qtA, "fakeip", true},
		{"Rule", "a.direct.example", qtA, "fakeip", false}, // 直连的组不生成 DNS 规则,按通用规则走
		{"Rule", "off.example", qtA, "fakeip", false},
		{"Global", "www.bilibili.com", qtHTTPS, "remote", false}, // 全局模式规则组不生效
		{"Global", "x.tracker.net", qtA, "fakeip", false},
	} {
		got, at := dnsRoute(t, c, tc.mode, tc.domain, tc.qtype)
		if got != tc.want || (tc.group != (at >= 0 && at < cnAt)) {
			t.Fatalf("%s 模式 %s(类型 %d):应 %s(规则组=%v),实际 %s(第 %d 条,国内 → 本地在第 %d 条)\n%s", tc.mode, tc.domain, tc.qtype, tc.want, tc.group, got, at, cnAt, raw)
		}
	}

	// 不开 fake-ip:走代理的组交给远程 DNS;开 IPv6:AAAA 也给 fake-ip
	s.FakeIP = false
	c2, raw2 := build(t, s)
	if got, _ := dnsRoute(t, c2, "Rule", "www.bilibili.com", qtA); got != "remote" {
		t.Fatalf("不开 fake-ip 时走代理的组应交给远程 DNS,实际 %s\n%s", got, raw2)
	}
	if got, _ := dnsRoute(t, c2, "Rule", "x.tracker.net", qtA); got != "reject" {
		t.Fatalf("拒绝的组连查询一起拒掉,实际 %s", got)
	}
	s.FakeIP, s.IPv6 = true, true
	c3, raw3 := build(t, s)
	if got, _ := dnsRoute(t, c3, "Rule", "www.bilibili.com", qtAAAA); got != "fakeip" {
		t.Fatalf("开 IPv6 时 AAAA 也给 fake-ip,实际 %s\n%s", got, raw3)
	}
}
