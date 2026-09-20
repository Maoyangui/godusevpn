package builder

import (
	"encoding/json"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 全局禁直连下「本地 DNS = system」的替换(builder.go 里 localDNS 那一段):
// 全局模式里本地 DNS 只剩一个用处 —— 给节点服务器自己的域名做解析(default_domain_resolver)。
// 设成 system 就是明文 53 发到路由器 / 运营商,与"全局禁直连"的承诺相悖,要换成按地址连的加密 DoH。
// 规则 / 直连模式、或者没开禁直连时不改:那本来就不承诺不直连。

// sealedDNSCfg 只解这组测试关心的几个字段:builder_test.go 里的 cfg 没取 route.default_domain_resolver。
type sealedDNSCfg struct {
	DNS struct {
		Servers []map[string]any `json:"servers"`
	} `json:"dns"`
	Route struct {
		DefaultDomainResolver string `json:"default_domain_resolver"`
	} `json:"route"`
}

func sealedBuild(t *testing.T, s settings.Settings) sealedDNSCfg {
	t.Helper()
	raw, err := Build(Input{Profile: sampleProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	var c sealedDNSCfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// dnsServerByTag 取指定 tag 的 DNS 服务器;同一个 tag 出现两次内核会起不来,所以也顺手挡住重复。
func dnsServerByTag(t *testing.T, c sealedDNSCfg, tag string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, sv := range c.DNS.Servers {
		if sv["tag"] != tag {
			continue
		}
		if found != nil {
			t.Fatalf("DNS 服务器 tag=%s 出现了不止一次: %v", tag, c.DNS.Servers)
		}
		found = sv
	}
	if found == nil {
		t.Fatalf("DNS 服务器里没有 tag=%s: %v", tag, c.DNS.Servers)
	}
	return found
}

// sealedSettings 全局 + 禁直连 + 本地 DNS 用系统解析:正是要被替换的那组。
func sealedSettings() settings.Settings {
	s := settings.Default()
	s.LocalDNS, s.NoDirect, s.Mode = "system", true, settings.ModeGlobal
	return s
}

// 全局禁直连 + 本地 DNS 设成 system:tag=local 必须换成按地址连的加密 DoH,不能再是明文的系统解析。
func TestSealedGlobalSystemLocalDNSBecomesDoH(t *testing.T) {
	c := sealedBuild(t, sealedSettings())
	local := dnsServerByTag(t, c, "local")
	if local["type"] != "https" {
		t.Fatalf("全局禁直连下本地 DNS 应换成 DoH(https),实际 type=%v: %v", local["type"], local)
	}
	if local["server"] != "223.5.5.5" {
		t.Fatalf("替身 DoH 应是 223.5.5.5,实际 %v", local["server"])
	}
	// 按地址连:不能再依赖任何别的解析器,也不能绕经代理(它就是给节点域名解析用的,代理那时还没起来)
	if local["domain_resolver"] != nil {
		t.Fatalf("替身 DoH 按地址连,不该再带 domain_resolver: %v", local)
	}
	if local["detour"] != nil {
		t.Fatalf("本地 DNS 应直连,不该带 detour: %v", local)
	}
	// 明文的 type=local / tag=local 一个都不能留(留着就是没换干净,两个同名内核也起不来)
	for _, sv := range c.DNS.Servers {
		if sv["type"] == "local" && sv["tag"] == "local" {
			t.Fatalf("全局禁直连下不该还有 type=local 的 \"local\" 服务器: %v", c.DNS.Servers)
		}
	}
}

// 替换只发生在「全局 + 禁直连」这一种组合下:规则 / 直连模式、或者禁直连关着时,tag=local 照旧是系统解析。
func TestSealedRewriteOnlyWhenGlobalAndNoDirect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		noDirect bool
	}{
		{"规则模式+禁直连", settings.ModeRule, true},
		{"直连模式+禁直连", settings.ModeDirect, true},
		{"全局模式+不禁直连", settings.ModeGlobal, false},
		{"规则模式+不禁直连", settings.ModeRule, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := settings.Default()
			s.LocalDNS, s.NoDirect, s.Mode = "system", tc.noDirect, tc.mode
			c := sealedBuild(t, s)
			local := dnsServerByTag(t, c, "local")
			if local["type"] != "local" {
				t.Fatalf("%s:本地 DNS 应保持系统解析(type=local),实际 %v", tc.name, local)
			}
			if local["server"] != nil {
				t.Fatalf("%s:系统解析不该带 server 字段: %v", tc.name, local)
			}
		})
	}
}

// 本地 DNS 本来就填的是 DoH 地址(IP 或域名)时不受影响:替换只针对 "system" 这个字面值。
func TestSealedLocalDNSAddressUntouched(t *testing.T) {
	for _, tc := range []struct {
		name         string
		localDNS     string
		wantResolver any // 域名时要给它配解析器,IP 时不能有
	}{
		{"IP", "119.29.29.29", nil},
		{"域名", "doh.pub", "system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := sealedSettings()
			s.LocalDNS = tc.localDNS
			c := sealedBuild(t, s)
			local := dnsServerByTag(t, c, "local")
			if local["type"] != "https" || local["server"] != tc.localDNS {
				t.Fatalf("本地 DNS 是 %s 时应原样写进配置,实际 %v", tc.localDNS, local)
			}
			if local["domain_resolver"] != tc.wantResolver {
				t.Fatalf("本地 DNS 是 %s 时 domain_resolver 应为 %v,实际 %v", tc.localDNS, tc.wantResolver, local["domain_resolver"])
			}
			if local["detour"] != nil {
				t.Fatalf("本地 DNS 应直连: %v", local)
			}
		})
	}
	// 反过来也要成立:不在全局禁直连下,填的地址同样原样保留(没被误改成 system 或替身)
	s := settings.Default()
	s.LocalDNS, s.Mode = "119.29.29.29", settings.ModeRule
	local := dnsServerByTag(t, sealedBuild(t, s), "local")
	if local["type"] != "https" || local["server"] != "119.29.29.29" {
		t.Fatalf("规则模式下填的本地 DoH 也该原样保留,实际 %v", local)
	}
}

// 隐私边界不能被这次替换动到:节点域名仍由 local 解析(default_domain_resolver),
// 其它域名仍走经代理的 remote DoH —— 替身只是换掉了 local 自己的实现,不是把解析都改成直连。
func TestSealedPrivacyBoundaryUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    settings.Settings
	}{
		{"全局禁直连+system", sealedSettings()},
		{"出厂默认", settings.Default()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sealedBuild(t, tc.s)
			if c.Route.DefaultDomainResolver != "local" {
				t.Fatalf("default_domain_resolver 应仍是 local,实际 %q", c.Route.DefaultDomainResolver)
			}
			remote := dnsServerByTag(t, c, "remote")
			if remote["type"] != "https" || remote["server"] != tc.s.RemoteDNS {
				t.Fatalf("远程 DNS 应仍是设置里的 DoH %s,实际 %v", tc.s.RemoteDNS, remote)
			}
			if remote["detour"] != "proxy" {
				t.Fatalf("远程 DNS 应仍经代理(detour=proxy),实际 %v", remote)
			}
		})
	}
}
