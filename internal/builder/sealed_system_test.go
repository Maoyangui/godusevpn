package builder

import (
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// tag=system 只用来解析 DoH 服务器自己的域名。全局禁直连下它也不能是明文 53:换成按地址连的 DoH。
func TestSealedSystemResolverBecomesDoH(t *testing.T) {
	s := sealedSettings()
	s.LocalDNS = "doh.example" // 域名 DoH:解析它自己要用 system
	c := sealedBuild(t, s)
	sys := dnsServerByTag(t, c, "system")
	if sys["type"] != "https" || sys["server"] != "223.5.5.5" {
		t.Fatalf("全局禁直连下 system 解析器应是 223.5.5.5 的 DoH,实际 %v", sys)
	}
	if sys["domain_resolver"] != nil || sys["detour"] != nil {
		t.Fatalf("替身按地址直连,不该带 domain_resolver / detour: %v", sys)
	}
	local := dnsServerByTag(t, c, "local")
	if local["server"] != "doh.example" || local["domain_resolver"] != "system" {
		t.Fatalf("用户填的域名 DoH 应原样保留、经 system 解析自己的域名: %v", local)
	}
	for _, sv := range c.DNS.Servers {
		if sv["type"] == "local" {
			t.Fatalf("全局禁直连下不该还有任何 type=local 的解析器: %v", c.DNS.Servers)
		}
	}
}

// 不在「全局 + 禁直连」下,system 解析器照旧是系统的(那些模式本来就不承诺不直连)。
func TestSystemResolverStaysLocalOutsideSealed(t *testing.T) {
	for name, mut := range map[string]func(*settings.Settings){
		"禁直连关着": func(s *settings.Settings) { s.NoDirect = false },
		"规则模式":  func(s *settings.Settings) { s.Mode = settings.ModeRule },
	} {
		t.Run(name, func(t *testing.T) {
			s := sealedSettings()
			s.LocalDNS = "doh.example"
			mut(&s)
			c := sealedBuild(t, s)
			sys := dnsServerByTag(t, c, "system")
			if sys["type"] != "local" {
				t.Fatalf("非封闭模式下 system 解析器应保持 local,实际 %v", sys)
			}
		})
	}
}
