package nodelink

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func get(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, p := range path {
		o, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%v 不是对象: %v", path, cur)
		}
		cur = o[p]
	}
	return cur
}

func TestParseEveryScheme(t *testing.T) {
	vm, _ := json.Marshal(map[string]any{"v": "2", "ps": "VM 节点", "add": "vm.example.com", "port": "8443", "id": "11111111-1111-1111-1111-111111111111", "aid": "0", "scy": "aes-128-gcm", "net": "ws", "host": "cdn.example.com", "path": "/ws", "tls": "tls", "sni": "vm.example.com"})
	cases := []struct {
		link, typ, tag, server string
		port                   int
		check                  func(t *testing.T, n map[string]any)
	}{
		{"vless://11111111-1111-1111-1111-111111111111@1.2.3.4:443?security=reality&sni=www.apple.com&pbk=PUBKEY&sid=ab&fp=safari&flow=xtls-rprx-vision#%E8%8A%82%E7%82%B9", "vless", "节点", "1.2.3.4", 443, func(t *testing.T, n map[string]any) {
			if get(t, n, "flow") != "xtls-rprx-vision" || get(t, n, "tls", "reality", "public_key") != "PUBKEY" || get(t, n, "tls", "reality", "short_id") != "ab" || get(t, n, "tls", "server_name") != "www.apple.com" || get(t, n, "tls", "utls", "fingerprint") != "safari" {
				t.Fatalf("%v", n)
			}
		}},
		{"vless://u@h.example.com:8080?type=ws&path=%2Fa&host=c.example.com#w", "vless", "w", "h.example.com", 8080, func(t *testing.T, n map[string]any) {
			if get(t, n, "transport", "type") != "ws" || get(t, n, "transport", "path") != "/a" || get(t, n, "transport", "headers", "Host") != "c.example.com" || n["tls"] != nil {
				t.Fatalf("没写 security 就是不加密,传输照常: %v", n)
			}
		}},
		{"trojan://pw@t.example.com:443?sni=s.example.com&allowInsecure=1&type=grpc&serviceName=svc#t", "trojan", "t", "t.example.com", 443, func(t *testing.T, n map[string]any) {
			if get(t, n, "password") != "pw" || get(t, n, "tls", "server_name") != "s.example.com" || get(t, n, "tls", "insecure") != true || get(t, n, "transport", "service_name") != "svc" {
				t.Fatalf("%v", n)
			}
		}},
		{"hysteria2://pw@h.example.com:8443?sni=s.example.com&insecure=1&obfs=salamander&obfs-password=op&mport=20000-30000#h", "hysteria2", "h", "h.example.com", 8443, func(t *testing.T, n map[string]any) {
			if get(t, n, "obfs", "password") != "op" || get(t, n, "tls", "insecure") != true {
				t.Fatalf("%v", n)
			}
			if p := n["server_ports"].([]any); len(p) != 1 || p[0] != "20000:30000" {
				t.Fatalf("端口跳跃: %v", p)
			}
		}},
		{"hy2://pw@h.example.com#h2", "hysteria2", "h2", "h.example.com", 443, nil},
		{"tuic://11111111-1111-1111-1111-111111111111:pw@t.example.com:443?congestion_control=bbr&udp_relay_mode=quic&alpn=h3,h3-29#t", "tuic", "t", "t.example.com", 443, func(t *testing.T, n map[string]any) {
			if get(t, n, "congestion_control") != "bbr" || get(t, n, "udp_relay_mode") != "quic" || len(get(t, n, "tls", "alpn").([]any)) != 2 {
				t.Fatalf("%v", n)
			}
		}},
		{"anytls://pw@a.example.com:443?sni=s.example.com#a", "anytls", "a", "a.example.com", 443, nil},
		{"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:p@ss")) + "@s.example.com:8388#s1", "shadowsocks", "s1", "s.example.com", 8388, func(t *testing.T, n map[string]any) {
			if get(t, n, "method") != "aes-256-gcm" || get(t, n, "password") != "p@ss" {
				t.Fatalf("%v", n)
			}
		}},
		{"ss://aes-128-gcm:plain@s.example.com:8388#s2", "shadowsocks", "s2", "s.example.com", 8388, nil},
		{"ss://" + base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pw@s.example.com:1234")) + "#s3", "shadowsocks", "s3", "s.example.com", 1234, nil},
		{"socks5://user:pw@p.example.com:1080#s", "socks", "s", "p.example.com", 1080, func(t *testing.T, n map[string]any) {
			if get(t, n, "username") != "user" || get(t, n, "password") != "pw" || get(t, n, "version") != "5" {
				t.Fatalf("%v", n)
			}
		}},
		{"socks://" + base64.StdEncoding.EncodeToString([]byte("u2:p2")) + "@p.example.com:1081", "socks", "p.example.com", "p.example.com", 1081, func(t *testing.T, n map[string]any) {
			if get(t, n, "username") != "u2" || get(t, n, "password") != "p2" {
				t.Fatalf("v2rayN 的 base64 用户名: %v", n)
			}
		}},
		{"http://u:p@p.example.com:3128#h", "http", "h", "p.example.com", 3128, nil},
		{"https://p.example.com:443#hs", "http", "hs", "p.example.com", 443, func(t *testing.T, n map[string]any) {
			if get(t, n, "tls", "enabled") != true {
				t.Fatalf("https 代理要带 TLS: %v", n)
			}
		}},
		{"vless://u@[2001:db8::1]:443#v6", "vless", "v6", "2001:db8::1", 443, nil},
		{"vmess://" + base64.StdEncoding.EncodeToString(vm), "vmess", "VM 节点", "vm.example.com", 8443, func(t *testing.T, n map[string]any) {
			if get(t, n, "security") != "aes-128-gcm" || get(t, n, "transport", "headers", "Host") != "cdn.example.com" || get(t, n, "tls", "server_name") != "vm.example.com" {
				t.Fatalf("%v", n)
			}
		}},
	}
	for _, c := range cases {
		n, err := Parse(c.link)
		if err != nil {
			t.Fatalf("%s: %v", c.link, err)
		}
		if n["type"] != c.typ || n["tag"] != c.tag || n["server"] != c.server || n["server_port"] != c.port {
			t.Fatalf("%s: %v", c.link, n)
		}
		if c.check != nil {
			c.check(t, n)
		}
	}
}

func TestParseErrorsNeverEchoTheLink(t *testing.T) {
	const secret = "SECRETPW"
	cases := map[string]string{
		"https://panel.example.com/sub/" + secret:                     "subscription",
		"https://panel.example.com/sub?token=" + secret:               "subscription",
		"http://www.example.com":                                      "subscription",
		"vless://" + secret + "@:443":                                 "server",
		"vless://@h.example.com:443":                                  "uuid",
		"trojan://h.example.com:443":                                  "password",
		"hy2://h.example.com:443":                                     "password",
		"anytls://h.example.com:443":                                  "password",
		"tuic://h.example.com:443":                                    "uuid",
		"vless://" + secret + "@h.example.com:99999":                  "port",
		"vless://" + secret + "@h.example.com:abc":                    "format",
		"wireguard://" + secret + "@h.example.com:1":                  "scheme",
		"not a link " + secret:                                        "format",
		"ss://" + secret:                                              "format",
		"vmess://" + secret:                                           "format",
		"vless://" + secret + "@h.example.com:443?security=reality#x": "pbk",
	}
	for link, code := range cases {
		_, err := Parse(link)
		if err == nil {
			t.Fatalf("%s 应该报错", link)
		}
		if CodeOf(err) != code {
			t.Fatalf("%s: 代码 %q 想要 %q (%v)", link, CodeOf(err), code, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("错误里带了链接内容: %v", err)
		}
	}
}

func TestParseAll(t *testing.T) {
	nodes, errs := ParseAll("\n# 注释\nhy2://pw@a.example.com#a\n\nbad\n  trojan://pw@b.example.com  \n")
	if len(nodes) != 2 || nodes[0]["tag"] != "a" || nodes[1]["tag"] != "b.example.com" {
		t.Fatalf("%v", nodes)
	}
	if len(errs) != 1 || errs[0].Line != 5 || errs[0].Code != "format" {
		t.Fatalf("要指出第几行: %+v", errs)
	}
}

func TestParseAllOtherForms(t *testing.T) {
	// 订阅内容:整段 base64
	blob := base64.StdEncoding.EncodeToString([]byte("hy2://pw@a.example.com#a\ntrojan://pw@b.example.com#b\n"))
	nodes, errs := ParseAll(blob[:10] + "\n" + blob[10:])
	if len(nodes) != 2 || len(errs) != 0 {
		t.Fatalf("%v %v", nodes, errs)
	}
	// sing-box 单个出站
	nodes, errs = ParseAll(`{"type":"trojan","tag":"j","server":"j.example.com","server_port":443,"password":"p","detour":"x"}`)
	if len(nodes) != 1 || len(errs) != 0 || nodes[0]["tag"] != "j" || nodes[0]["detour"] != nil {
		t.Fatalf("%v %v", nodes, errs)
	}
	// 整份配置:不是节点的跳过,填错的报出来
	nodes, errs = ParseAll(`{"outbounds":[{"type":"selector","tag":"s","outbounds":["a"]},{"type":"direct","tag":"d"},
		{"type":"hysteria2","tag":"a","server":"a.example.com","server_port":443,"password":"p"},{"type":"trojan","tag":"b","server":"b.example.com","server_port":443}]}`)
	if len(nodes) != 1 || len(errs) != 1 || errs[0].Code != "password" || errs[0].Line != 4 {
		t.Fatalf("%v %v", nodes, errs)
	}
	if get(t, nodes[0], "tls", "server_name") != "a.example.com" || len(get(t, nodes[0], "tls", "alpn").([]any)) != 1 {
		t.Fatalf("hysteria2 要带 TLS 和 h3: %v", nodes[0])
	}
	if _, errs = ParseAll("{bad"); len(errs) != 1 || errs[0].Code != "format" {
		t.Fatalf("%v", errs)
	}
	if nodes, errs = ParseAll(`{"outbounds":[{"type":"direct"}]}`); len(nodes) != 0 || len(errs) != 1 {
		t.Fatalf("什么节点都没有要说一声: %v %v", nodes, errs)
	}
}

func TestNormalize(t *testing.T) {
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"type": "trojan", "server": " h.example.com ", "server_port": float64(443), "password": "pw"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	n, err := Normalize(base(map[string]any{"tag": "  ", "detour": "x", "domain_resolver": "y", "evil": 1}))
	if err != nil {
		t.Fatal(err)
	}
	if n["tag"] != "h.example.com" || n["server"] != "h.example.com" || n["server_port"] != 443 {
		t.Fatalf("名字留空用地址,地址去空格,端口转整数: %v", n)
	}
	for _, k := range []string{"detour", "domain_resolver", "evil"} {
		if _, ok := n[k]; ok {
			t.Fatalf("不认识的字段要丢掉: %v", n)
		}
	}
	if get(t, n, "tls", "server_name") != "h.example.com" {
		t.Fatalf("trojan 没给 TLS 也要补上: %v", n)
	}

	for _, c := range []struct {
		m    map[string]any
		code string
	}{
		{map[string]any{"type": "wireguard", "server": "h", "server_port": 1}, "scheme"},
		{map[string]any{"type": "selector", "server": "h", "server_port": 1}, "scheme"},
		{map[string]any{"type": "vless", "server": "", "server_port": 1, "uuid": "u"}, "server"},
		{map[string]any{"type": "vless", "server": "a b", "server_port": 1, "uuid": "u"}, "server"},
		{map[string]any{"type": "vless", "server": "h", "server_port": 0, "uuid": "u"}, "port"},
		{map[string]any{"type": "vless", "server": "h", "server_port": 70000, "uuid": "u"}, "port"},
		{map[string]any{"type": "vless", "server": "h", "server_port": "x", "uuid": "u"}, "port"},
		{map[string]any{"type": "vless", "server": "h", "server_port": 1}, "uuid"},
		{map[string]any{"type": "shadowsocks", "server": "h", "server_port": 1, "password": "p"}, "method"},
		{map[string]any{"type": "shadowsocks", "server": "h", "server_port": 1, "method": "none"}, "password"},
		{map[string]any{"type": "hysteria2", "server": "h", "server_port": 1}, "password"},
	} {
		if _, err := Normalize(c.m); CodeOf(err) != c.code {
			t.Fatalf("%v: %v 想要 %s", c.m, err, c.code)
		}
	}

	// socks 不要密码,TLS 关着就不带
	n, err = Normalize(map[string]any{"type": "socks", "server": "2001:db8::1", "server_port": "1080", "tls": map[string]any{"enabled": false}})
	if err != nil || n["version"] != "5" || n["tls"] != nil || n["server_port"] != 1080 {
		t.Fatalf("%v %v", n, err)
	}
	// QUIC 协议用不了 uTLS:链接里带了 fp 也要去掉
	n, err = Parse("hysteria2://p@h.example.com:443?fp=chrome#h")
	if err != nil || get(t, n, "tls", "utls") != nil {
		t.Fatalf("%v %v", n, err)
	}
	n, err = Parse("tuic://11111111-1111-1111-1111-111111111111:p@h.example.com:443?fp=chrome#t")
	if err != nil || get(t, n, "tls", "utls") != nil {
		t.Fatalf("%v %v", n, err)
	}
	// Reality 自动带上 uTLS
	n, err = Normalize(map[string]any{"type": "vless", "server": "h", "server_port": 443, "uuid": "u", "tls": map[string]any{"reality": map[string]any{"enabled": true, "public_key": "k"}}})
	if err != nil || get(t, n, "tls", "utls", "enabled") != true || get(t, n, "tls", "server_name") != "h" {
		t.Fatalf("%v %v", n, err)
	}
}
