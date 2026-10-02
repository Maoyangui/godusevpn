package redact

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 地址只留协议与主机,看得出原来有 userinfo / 路径;把凭据整段 base64 进主机位的链接整段打码。
func TestURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "",
		"https://panel.example:2056":          "https://panel.example:2056",
		"http://0.0.0.0:9800/":                "http://0.0.0.0:9800/",
		"https://panel.example:2056/sub/abc":  "https://panel.example:2056/***",
		"https://panel.example/?token=abc123": "https://panel.example/***",
		"https://alice:pw@panel.example/sub/secret-token?access_token=x#frag":                    "https://***@panel.example/***",
		"HTTPS://Panel.Example/api/v1/client/subscribe?token=0123456789":                         "https://Panel.Example/***",
		"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@1.2.3.4:8388#HK":                                       "ss://***@1.2.3.4:8388/***",
		"ss://aes-256-gcm:password@1.2.3.4:8388":                                                 "ss://***@1.2.3.4:8388",
		"ss://YWVzLTI1Ni1nY206cGFzc0Bob3N0OjQ0Mw==#old":                                          "ss://***",
		"vmess://eyJ2IjoiMiIsInBzcyI6InNlY3JldCJ9":                                               "vmess://***",
		"vless://0f1e2d3c-1111-2222-3333-444455556666@node.example:443?security=reality&sni=x#n": "vless://***@node.example:443/***",
		"trojan://pw@node.example:443?sni=a.example#HK":                                          "trojan://***@node.example:443/***",
		"hysteria2://auth@[2001:db8::1]:443/?obfs=salamander&obfs-password=xx":                   "hysteria2://***@[2001:db8::1]:443/***",
		"tuic://uuid:pw@node.example:443?congestion_control=bbr":                                 "tuic://***@node.example:443/***",
		"socks5://user:pass@10.0.0.1:1080":                                                       "socks5://***@10.0.0.1:1080",
		"anytls://pw@node.example:443":                                                           "anytls://***@node.example:443",
		"wireguard://cHJpdmF0ZQ@node.example:51820?publickey=abc":                                "wireguard://***@node.example:51820/***",
		"tg://proxy?server=1.2.3.4&port=443&secret=ee00":                                         "tg://***",
		"not a url /sub/secret":                                                                  "***",
	} {
		if got := URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
}

// 拉订阅失败的错误文本(*url.Error 带着完整请求地址)、续费地址、JSON 转义过的地址、中文标点紧跟的地址。
func TestTextURLs(t *testing.T) {
	for in, want := range map[string]string{
		`拉取订阅失败: Get "https://panel.example:2056/sub/TOKEN123?format=json": context deadline exceeded`: `拉取订阅失败: Get "https://panel.example:2056/***": context deadline exceeded`,
		`面板不认识这条订阅链接(HTTP 404,panel.example) [renew=https://panel.example/buy/abc]`:                    `面板不认识这条订阅链接(HTTP 404,panel.example) [renew=https://panel.example/***]`,
		`{"url":"https:\/\/panel.example\/sub\/TOKEN123"}`:                                             `{"url":"https://panel.example/***"}`,
		`订阅地址 https://panel.example/sub/TOKEN123，已更新`:                                                  `订阅地址 https://panel.example/***，已更新`,
		`见 https://panel.example/sub/TOKEN123.`:                                                        `见 https://panel.example/***.`,
		`面板监听 http://0.0.0.0:9800/`:                                                                    `面板监听 http://0.0.0.0:9800/`,
		`relative /sub/TOKEN123 path`:                                                                  `relative /sub/*** path`,
		`Authorization: Bearer abc.def`:                                                                `Authorization: Bearer ***`,
		`proxy header Bearer abc.def`:                                                                  `proxy header Bearer ***`,
	} {
		if got := Text(in); got != want {
			t.Errorf("Text(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// key: value / key=value 形式的凭据:值换成 ***,引号风格原样保留,空值照旧留空,对象不被当成值吃掉。
func TestTextCredentials(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`"psk": "abc"`, `"psk": "***"`},
		{`psk='abc'`, `psk='***'`},
		{`psk=abc`, `psk=***`},
		{`token: abc, next`, `token: ***, next`},
		{`"password": ""`, `"password": ""`},
		{`obfs-password=xyz next`, `obfs-password=*** next`},
		{`"obfs": "OBFSSECRET"`, `"obfs": "***"`},
		{`"obfs": {`, `"obfs": {`},
		{`"username": "alice"`, `"username": "***"`},
		{`"private_key": "PRIV"`, `"private_key": "***"`},
		{`"private_key_passphrase": "PASS"`, `"private_key_passphrase": "***"`},
		{`"pre_shared_key": "PSK1"`, `"pre_shared_key": "***"`},
		{`client_key=CK`, `client_key=***`},
		{`"auth_str": "A1"`, `"auth_str": "***"`},
		{`uuid=0f1e2d3c`, `uuid=***`},
		{`web_password: hunter2`, `web_password: ***`},
		{`token_count=5`, `token_count=5`},
	} {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 诊断包里整段 JSON 也过 Text:打码之后仍然是合法 JSON,明文不留。
func TestTextKeepsJSONValid(t *testing.T) {
	in := map[string]any{
		"nodes":      []string{"HK-01 psk=SuperSecret123", "token: abcdef"},
		"lastReason": "token=zzz-secret",
		"cfg":        map[string]any{"password": "p@ss", "uuid": "0f1e2d3c", "note": "authorization: Bearer xyz", "obfs": map[string]any{"type": "salamander"}},
		"url":        "https://panel.example/sub/TOKEN123",
	}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := Text(string(b))
	var back map[string]any
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("打码后不再是合法 JSON: %v\n%s", err, out)
	}
	for _, secret := range []string{"SuperSecret123", "abcdef", "zzz-secret", "p@ss", "0f1e2d3c", "xyz", "TOKEN123"} {
		if strings.Contains(out, secret) {
			t.Fatalf("明文 %q 还在:\n%s", secret, out)
		}
	}
}

func TestSecretKey(t *testing.T) {
	for _, k := range []string{"password", "webPassword", "obfs-password", "uuid", "username", "user", "private_key", "pre_shared_key",
		"client_key", "userkey", "static_key", "private_key_passphrase", "auth", "auth_str", "psk", "token", "secret", "api-key", "Authorization"} {
		if !SecretKey(k) {
			t.Errorf("%q 应算凭据", k)
		}
	}
	for _, k := range []string{"public_key", "server", "server_port", "tag", "type", "mac", "name", "url", "method", "short_id", "obfs"} {
		if SecretKey(k) {
			t.Errorf("%q 不该算凭据", k)
		}
	}
}

// 各类节点出站:凭据整值打码,排障要看的结构与字段照旧。
func TestMaskerTreeOutbounds(t *testing.T) {
	var cfg any
	raw := `{"outbounds":[
	 {"type":"hysteria","tag":"hy1","server":"1.2.3.4","server_port":443,"auth_str":"AUTH1","obfs":"OBFSSECRET"},
	 {"type":"hysteria2","tag":"hy2","server":"1.2.3.4","password":"HY2PW","obfs":{"type":"salamander","password":"OBFS2PW"}},
	 {"type":"ssh","tag":"ssh","user":"root","private_key":"-----BEGIN OPENSSH PRIVATE KEY-----KEY1","private_key_passphrase":"PASSPHRASE1"},
	 {"type":"trojan","tag":"tj","password":"TJPW","tls":{"enabled":true,"server_name":"a.example","client_key":["-----BEGIN PRIVATE KEY-----","CLIENTKEY1"],"reality":{"public_key":"PUBKEY","short_id":"ab12"}}},
	 {"type":"socks","tag":"s5","username":"alice","password":"S5PW"},
	 {"type":"vless","tag":"vl","uuid":"0f1e2d3c-1111-2222-3333-444455556666","flow":""},
	 {"type":"shadowsocks","tag":"ss","method":"2022-blake3-aes-128-gcm","password":"SSPW"},
	 {"type":"wireguard","tag":"wg","private_key":"WGPRIV","peers":[{"public_key":"WGPUB","pre_shared_key":"WGPSK"}]},
	 {"type":"snell","tag":"sn","userkey":"USERKEY1"}
	]}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(NewMasker().Tree(cfg))
	s := string(out)
	for _, secret := range []string{"AUTH1", "OBFSSECRET", "HY2PW", "OBFS2PW", "root", "KEY1", "PASSPHRASE1", "TJPW", "CLIENTKEY1",
		"alice", "S5PW", "0f1e2d3c", "SSPW", "WGPRIV", "WGPSK", "USERKEY1"} {
		if strings.Contains(s, secret) {
			t.Errorf("明文 %q 还在: %s", secret, s)
		}
	}
	for _, keep := range []string{`"type":"salamander"`, `"server_name":"a.example"`, `"public_key":"PUBKEY"`, `"method":"2022-blake3-aes-128-gcm"`,
		`"server":"1.2.3.4"`, `"server_port":443`, `"public_key":"WGPUB"`, `"flow":""`, `"private_key":"***"`} {
		if !strings.Contains(s, keep) {
			t.Errorf("该留着的 %s 没了: %s", keep, s)
		}
	}
}

// 订阅令牌在地址以外的地方出现(订阅名、标题、日志):盖掉,但不误伤包含它的长词;短的查询值(format=json)不当令牌。
func TestMaskerTokens(t *testing.T) {
	m := NewMasker("https://panel.example:2056/sub/alice", "https://h.example/api/v1/client/subscribe?token=0123456789abcdef&format=json", "https://h.example/sub")
	for in, want := range map[string]string{
		`订阅「alice」已更新`:                         `订阅「<订阅令牌>」已更新`,
		`malice 与 alice2 不是它`:                  `malice 与 alice2 不是它`,
		`key 0123456789abcdef`:                 `key <订阅令牌>`,
		`info.json sub/ subscribe 保留`:          `info.json sub/ subscribe 保留`,
		`https://panel.example:2056/sub/alice`: `https://panel.example:2056/***`,
	} {
		if got := m.Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
	tree := m.Tree(map[string]any{"profiles": []any{map[string]any{"name": "alice", "title": "alice", "url": "https://panel.example:2056/sub/alice", "error": ""}}})
	b, _ := json.Marshal(tree)
	if strings.Contains(string(b), "alice") || !strings.Contains(string(b), `"error":""`) {
		t.Fatalf("订阅名里的令牌没盖住,或空值被改了: %s", b)
	}
}

func TestMaskerMAC(t *testing.T) {
	m := NewMasker()
	for in, want := range map[string]string{
		"Physical Address. . . : 00-15-5D-01-02-03":          "Physical Address. . . : 00-15-5D-xx-xx-xx",
		"link/ether 00:15:5d:01:02:03 brd ff:ff:ff:ff:ff:ff": "link/ether 00:15:5d:xx:xx:xx brd ff:ff:ff:ff:ff:ff",
		"DUID 00-01-00-01-2A-3B-4C-5D-00-15-5D-01-02-03":     "DUID 00-01-00-xx-xx-xx-xx-xx-xx-xx-xx-xx-xx-xx",
		`"mac": "aa:bb:cc:dd:ee:ff"`:                         `"mac": "aa:bb:cc:xx:xx:xx"`,
		"2001:db8:aa:bb:cc:dd:ee:ff 不是 MAC":                  "2001:db8:aa:bb:cc:dd:ee:ff 不是 MAC",
		"uuid 0f1e2d3c-1111-2222-3333-444455556666":          "uuid 0f1e2d3c-1111-2222-3333-444455556666",
	} {
		if got := m.Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

// Tree 拿到的是副本:入参一个字节都不能变(导出诊断包曾经把运行中的订阅链接改坏)。
func TestTreeDoesNotMutateInput(t *testing.T) {
	in := map[string]any{"nested": map[string]any{"url": "https://x.test/sub/token", "password": "pw"}, "list": []any{"Bearer abc"}}
	original := map[string]any{"nested": map[string]any{"url": "https://x.test/sub/token", "password": "pw"}, "list": []any{"Bearer abc"}}
	_ = NewMasker("https://x.test/sub/token").Tree(in)
	if !reflect.DeepEqual(in, original) {
		t.Fatalf("Tree 改了入参: %#v", in)
	}
}

// 打码结果再打一遍不变:诊断包里 info.json 先按树、再按整段文本各过一遍。
func TestIdempotent(t *testing.T) {
	m := NewMasker("https://panel.example/sub/alice")
	for _, s := range []string{
		`Get "https://***@panel.example/***": timeout`, `"password": "***"`, "00-15-5D-xx-xx-xx", "<订阅令牌>", "vmess://***",
	} {
		if got := m.Text(s); got != s {
			t.Errorf("再打一遍变了: %q → %q", s, got)
		}
	}
}

// Text 先按子串预筛再跑凭据正则(日志每行都过这里,全跑正则太慢)。预筛漏掉的键名就等于不打码,
// 所以正则认的每一类键名,大小写混写也好,都得真被打成 ***。
func TestCredentialHintsCoverKeys(t *testing.T) {
	for _, k := range []string{"password", "Passwd", "passphrase", "token", "access_token", "client_secret", "x-password",
		"private_key", "Private-Key", "privatekey", "pre_shared_key", "preshared-key", "client_key", "user_key", "api-key",
		"Authorization", "auth_str", "authstr", "UUID", "psk", "username", "obfs"} {
		if got, want := Text(k+"=abc123"), k+"=***"; got != want {
			t.Errorf("%s:得到 %q,应为 %q", k, got, want)
		}
	}
	// 预筛不中的行原样返回
	l := "outbound/hysteria2[香港1]: outbound connection to www.example.com:443"
	if Text(l) != l {
		t.Fatalf("普通连接行被改了: %q", Text(l))
	}
}
