package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 第三方订阅在出站上写的 domain_resolver 指向订阅自己的 DNS 服务器(客户端不要订阅的 DNS 段),
// 留着节点就建不起来:读订阅时去掉;不带它的节点原样保留,一个字节都不动。
func TestParseDropsDomainResolver(t *testing.T) {
	body := `{"dns":{"servers":[{"type":"https","tag":"sub-dns","server":"1.1.1.1"}]},"outbounds":[
		{"type":"trojan","tag":"a","server":"a.example.com","server_port":443,"password":"p","domain_resolver":"sub-dns"},
		{"type":"trojan","tag":"b","server":"b.example.com","server_port":443,"password":"p","domain_resolver":{"server":"sub-dns","strategy":"ipv4_only"}},
		{"type":"trojan","tag":"c","server":"c.example.com","server_port":443,"password":"p"}
	]}`
	p, err := Parse([]byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Outbounds) != 3 {
		t.Fatalf("应保留 3 个节点,得到 %d", len(p.Outbounds))
	}
	for i, raw := range p.Outbounds[:2] {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["domain_resolver"]; ok {
			t.Fatalf("节点 %s 的 domain_resolver 应去掉: %s", p.Tags[i], raw)
		}
		if m["server"] == nil || m["password"] != "p" {
			t.Fatalf("其余字段不能丢: %s", raw)
		}
	}
	if want := `{"type":"trojan","tag":"c","server":"c.example.com","server_port":443,"password":"p"}`; string(p.Outbounds[2]) != want {
		t.Fatalf("不带 domain_resolver 的节点应原样保留:\n%s", p.Outbounds[2])
	}
}

// 订阅节点上遗留的 domain_strategy(sing-box 已废弃的写法,Sub-Store 会把 Clash 的 ip-version 转成它,旧模板也常写 prefer_ipv4):
// 出站写了它又没写 domain_resolver 时,sing-box 拿它盖掉解析策略 —— 关着 IPv6(dns.strategy = ipv4_only)时写了 ipv6_only /
// prefer_ipv6 的节点照样解析 AAAA、经 IPv6 去连。读订阅时去掉;旧版本存下的缓存(那时只去 domain_resolver)读进来也去掉,
// 节点和名字一一对应、一条不丢;不带它的节点一个字节都不动。
func TestDropsDomainStrategy(t *testing.T) {
	body := `{"outbounds":[
		{"type":"trojan","tag":"六","server":"a.example.com","server_port":443,"password":"p","domain_strategy":"ipv6_only"},
		{"type":"trojan","tag":"偏四","server":"b.example.com","server_port":443,"password":"p","domain_strategy":"prefer_ipv4","domain_resolver":"sub-dns"},
		{"type":"trojan","tag":"c","server":"c.example.com","server_port":443,"password":"p"}
	]}`
	p, err := Parse([]byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, p *Profile, plainC string) {
		t.Helper()
		if len(p.Outbounds) != 3 || len(p.Tags) != 3 {
			t.Fatalf("%s:节点一条不丢: %d / %d", what, len(p.Outbounds), len(p.Tags))
		}
		for i, raw := range p.Outbounds[:2] {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if _, ok := m["domain_strategy"]; ok {
				t.Fatalf("%s:节点 %s 的 domain_strategy 应去掉: %s", what, p.Tags[i], raw)
			}
			if _, ok := m["domain_resolver"]; ok {
				t.Fatalf("%s:节点 %s 的 domain_resolver 应去掉: %s", what, p.Tags[i], raw)
			}
			if m["tag"] != p.Tags[i] || m["server"] == nil || m["password"] != "p" || m["server_port"] != float64(443) {
				t.Fatalf("%s:其余字段不能丢、名字对得上: %s", what, raw)
			}
		}
		if string(p.Outbounds[2]) != plainC {
			t.Fatalf("%s:不带这些字段的节点应原样保留:\n%s", what, p.Outbounds[2])
		}
	}
	check("读订阅", p, `{"type":"trojan","tag":"c","server":"c.example.com","server_port":443,"password":"p"}`)

	// 旧版本存的缓存:节点上还带着 domain_strategy
	old := &Profile{URL: "https://example.invalid/sub", FetchedAt: 1, Tags: []string{"六", "偏四", "c"}, Outbounds: []json.RawMessage{
		json.RawMessage(`{"type":"trojan","tag":"六","server":"a.example.com","server_port":443,"password":"p","domain_strategy":"ipv6_only"}`),
		json.RawMessage(`{"type":"trojan","tag":"偏四","server":"b.example.com","server_port":443,"password":"p","domain_strategy":"prefer_ipv4"}`),
		json.RawMessage(`{"type":"trojan","tag":"c","server":"c.example.com","server_port":443,"password":"p"}`),
	}}
	path := filepath.Join(t.TempDir(), "old.json")
	if err := old.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plain Profile // 不经 Load、原样读出来的那份:不带这些字段的节点 Load 一个字节都不该动
	if err := json.Unmarshal(raw, &plain); err != nil {
		t.Fatal(err)
	}
	check("读旧缓存", back, string(plain.Outbounds[2]))
}
