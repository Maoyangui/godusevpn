package profile

import (
	"encoding/json"
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
