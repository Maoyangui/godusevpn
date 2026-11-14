package builder

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

func profileOf(t *testing.T, nodes ...string) *profile.Profile {
	t.Helper()
	p := &profile.Profile{}
	for _, n := range nodes {
		var m struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal([]byte(n), &m); err != nil {
			t.Fatal(err)
		}
		p.Outbounds = append(p.Outbounds, json.RawMessage(n))
		p.Tags = append(p.Tags, m.Tag)
	}
	return p
}

// 地址只指向本机的才算提示节点:回环、未指定地址、localhost;局域网、链路本地、普通域名都不算。
func TestLocalServer(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, " 127.0.0.1 ": true, "127.8.9.10": true, "::1": true, "[::1]": true, "::ffff:127.0.0.1": true,
		"0.0.0.0": true, "::": true, "localhost": true, "LOCALHOST.": true, "hint.localhost": true,
		"": false, "1.2.3.4": false, "10.0.0.1": false, "192.168.1.1": false, "169.254.1.1": false, "fe80::1": false,
		"2001:db8::1": false, "example.com": false, "localhost.example.com": false, "127.0.0.1.example.com": false, "notlocalhost": false,
	} {
		if got := localServer(host); got != want {
			t.Fatalf("localServer(%q) = %v,应 %v", host, got, want)
		}
	}
}

// 提示节点只认 m-ui 的形状:地址只指向本机、不是 socks / http、回环 / localhost 上端口小于 1024(未指定地址不看端口)。
// 写着本机地址的 socks / http 是接本机另一个代理(Tor 9050、本机 Clash 7890、公司代理),回环高端口多半是本机转发
// (kcptun、ssh -L):都是真能用的节点,进自动选择、照常测速,不标「提示」。
func TestHintShape(t *testing.T) {
	for _, c := range []struct {
		typ, server string
		port        int
		want        bool
	}{
		{"shadowsocks", "127.0.0.1", 1, true},    // m-ui「00-勿选-流量…」
		{"shadowsocks", "127.0.0.1", 88, true},   // 生产上管理员自建的「续费提示」线路
		{"vmess", "localhost", 443, true},        // 机场常见写法
		{"trojan", "::1", 0, true},               // 没写端口
		{"trojan", "0.0.0.0", 443, true},         // 未指定地址
		{"vless", "0.0.0.0", 8443, true},         // 未指定地址不看端口
		{"Shadowsocks", "127.0.0.1", 1023, true}, // 类型不分大小写
		{"socks", "127.0.0.1", 1080, false},      // 本机的 socks 代理
		{"socks", "127.0.0.1", 9050, false},      // Tor
		{"http", "127.0.0.1", 7890, false},       // 本机 Clash 的 HTTP 口
		{"http", "localhost", 80, false},         // socks / http 端口再低也不算
		{"socks", "0.0.0.0", 1, false},
		{"SOCKS", "127.0.0.1", 1, false},
		{"shadowsocks", "127.0.0.1", 12948, false}, // kcptun 本机端
		{"vless", "127.0.0.1", 1024, false},        // ssh -L 之类的本机转发
		{"trojan", "1.2.3.4", 1, false},
		{"trojan", "example.com", 1, false},
	} {
		if got := hintShape(c.typ, c.server, c.port); got != c.want {
			t.Errorf("hintShape(%q, %q, %d) = %v,应 %v", c.typ, c.server, c.port, got, c.want)
		}
	}
}

// 本机链式代理不当提示节点:进自动选择和线路组,Report.Hints 里没有它们;同一份订阅里 m-ui 的提示节点照样剔掉。
func TestLocalChainProxiesAreNotHints(t *testing.T) {
	p := profileOf(t,
		`{"type":"shadowsocks","tag":"00-勿选-流量:1.2/100GB 重置:12天","server":"127.0.0.1","server_port":1,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"socks","tag":"本机 Tor","server":"127.0.0.1","server_port":9050}`,
		`{"type":"http","tag":"公司代理","server":"localhost","server_port":8080}`,
		`{"type":"shadowsocks","tag":"kcptun","server":"127.0.0.1","server_port":12948,"method":"aes-128-gcm","password":"p"}`,
		`{"type":"trojan","tag":"经本机 Tor","server":"hk.example","server_port":443,"password":"p","detour":"本机 Tor"}`,
	)
	if got := HintNodes(p); !reflect.DeepEqual(got, []string{"00-勿选-流量:1.2/100GB 重置:12天"}) {
		t.Fatalf("只有 m-ui 的提示节点算: %v", got)
	}
	c, _ := buildProfile(t, p, settings.Default())
	if got := anyStrings(outboundByTag(c)["auto"]["outbounds"]); !reflect.DeepEqual(got, p.Tags[1:]) {
		t.Fatalf("本机链式代理照常进自动选择: %v", got)
	}
}

// 续费 / 到期 / 用量的提示节点(地址只指向本机)不进自动选择和线路组,报在 Report.Hints 里(按订阅里的顺序);
// proxy 里照样列着,手动选它照样选得上(和以前一样)。成员全是提示节点的组原样留着(空的测速组整份配置起不来)。
func TestHintNodesExcludedFromAutoSelection(t *testing.T) {
	p := profileOf(t,
		`{"type":"shadowsocks","tag":"00-勿选-流量:1.2/100GB 重置:12天","server":"127.0.0.1","server_port":1,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"shadowsocks","tag":"续费提示","server":"127.0.0.1","server_port":88,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"trojan","tag":"香港1-A","server":"1.2.3.4","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"香港1-B","server":"hk-b.example","server_port":443,"password":"p"}`,
		`{"type":"vmess","tag":"套餐到期:2026-12-01","server":"localhost","server_port":443,"uuid":"b831381d-6324-4d53-ad4f-8cda48b30811"}`,
		`{"type":"trojan","tag":"剩余流量","server":"0.0.0.0","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"日本","server":"1.2.3.6","server_port":443,"password":"p"}`,
	)
	p.Groups = []profile.Group{
		{Tag: "香港1", Members: []string{"续费提示", "香港1-A", "香港1-B"}},
		{Tag: "全是提示", Members: []string{"00-勿选-流量:1.2/100GB 重置:12天", "续费提示"}},
	}
	hints := []string{"00-勿选-流量:1.2/100GB 重置:12天", "续费提示", "套餐到期:2026-12-01", "剩余流量"}
	if got := HintNodes(p); !reflect.DeepEqual(got, hints) {
		t.Fatalf("提示节点: %v", got)
	}

	for _, sel := range []string{"", "香港1", "续费提示"} {
		s := settings.Default()
		s.Selected = sel
		raw, rep, err := BuildEx(Input{Profile: p, Settings: s, DataDir: "/data", ClashSecret: "sec", RuleSetDir: ruleSetRoot(t)})
		if err != nil {
			t.Fatal(err)
		}
		var c cfg
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rep.Hints, hints) {
			t.Fatalf("Report.Hints: %v", rep.Hints)
		}
		by := outboundByTag(c)
		if got := anyStrings(by["auto"]["outbounds"]); !reflect.DeepEqual(got, []string{"香港1-A", "香港1-B", "日本"}) {
			t.Fatalf("选 %q:自动选择里不该有提示节点(测速前 urltest 先用第一个成员): %v", sel, got)
		}
		proxy := c.Outbounds[0]
		if got := anyStrings(proxy["outbounds"]); !reflect.DeepEqual(got[len(got)-len(p.Tags):], p.Tags) {
			t.Fatalf("proxy 里节点一个不少(手动选它照样选得上): %v", got)
		}
		if sel == "续费提示" && proxy["default"] != "续费提示" {
			t.Fatalf("手动选了提示节点:和以前一样就用它: %v", proxy["default"])
		}
		if sel != "香港1" {
			continue
		}
		if got := anyStrings(by["香港1"]["outbounds"]); !reflect.DeepEqual(got, []string{"香港1-A", "香港1-B"}) {
			t.Fatalf("线路组里的提示节点同样剔掉: %v", got)
		}
		if got := anyStrings(by["全是提示"]["outbounds"]); !reflect.DeepEqual(got, []string{"00-勿选-流量:1.2/100GB 重置:12天", "续费提示"}) {
			t.Fatalf("成员全是提示节点的组原样留着,配置才建得起来: %v", got)
		}
	}

	// 订阅里只有提示节点(到期 / 用尽后有的面板只给一条提示):auto 原样留着,配置照样生成得出来
	only := p.Subset([]string{"00-勿选-流量:1.2/100GB 重置:12天", "续费提示"})
	c, _ := buildProfile(t, only, settings.Default())
	if got := anyStrings(outboundByTag(c)["auto"]["outbounds"]); len(got) != 2 {
		t.Fatalf("全是提示节点时 auto 原样留着: %v", got)
	}
}

// 提示节点先于连不上的 IPv6 节点剔:剔完一个不剩时留 IPv6 节点(打开 IPv6、换个网络就能用),不留提示节点。
func TestHintNodesYieldToV6Nodes(t *testing.T) {
	p := profileOf(t,
		`{"type":"shadowsocks","tag":"续费提示","server":"127.0.0.1","server_port":88,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"trojan","tag":"v6","server":"2001:db8::1","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"v6b","server":"2001:db8::2","server_port":443,"password":"p"}`,
	)
	p.Groups = []profile.Group{{Tag: "线路", Members: []string{"续费提示", "v6"}}, {Tag: "线路2", Members: []string{"v6", "v6b"}}}
	s := settings.Default() // 关着 IPv6、开着 TUN、开着「连接时停用网卡 IPv6」:写成 IPv6 地址的连不上
	s.Selected = "线路"
	raw, rep, err := BuildEx(Input{Profile: p, Settings: s, DataDir: "/data", ClashSecret: "sec", RuleSetDir: ruleSetRoot(t), Linux: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Hints, []string{"续费提示"}) || !reflect.DeepEqual(rep.V6Blocked, []string{"v6", "v6b"}) {
		t.Fatalf("前提: %v %v", rep.Hints, rep.V6Blocked)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	by := outboundByTag(c)
	if got := anyStrings(by["auto"]["outbounds"]); !reflect.DeepEqual(got, []string{"v6", "v6b"}) {
		t.Fatalf("只剩 IPv6 节点时留它们,不留提示节点: %v", got)
	}
	if got := anyStrings(by["线路"]["outbounds"]); !reflect.DeepEqual(got, []string{"v6"}) {
		t.Fatalf("线路组同理: %v", got)
	}
}

// 订阅里没有提示节点时什么都不变:Report.Hints 为空,auto 照旧是全部节点、顺序不变。
func TestNoHintNodesNoChange(t *testing.T) {
	p := groupProfile()
	if h := HintNodes(p); h != nil {
		t.Fatalf("没有提示节点: %v", h)
	}
	_, rep, err := BuildEx(Input{Profile: p, Settings: settings.Default(), DataDir: "/data", ClashSecret: "sec", RuleSetDir: ruleSetRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hints != nil {
		t.Fatalf("Report.Hints 应为空: %v", rep.Hints)
	}
	c, _ := buildProfile(t, p, settings.Default())
	if got := anyStrings(outboundByTag(c)["auto"]["outbounds"]); !reflect.DeepEqual(got, p.Tags) {
		t.Fatalf("auto 照旧是全部节点: %v", got)
	}
	if HintNodes(nil) != nil {
		t.Fatal("没有订阅时没有提示节点")
	}
}

// 带 detour 的出站经 detour 那一头出去:detour 是 shadowtls 时它的拨号器不看目标地址,自己的 server 只是个占位;
// detour 是别的代理时,server 是交给那一头去连的(写 127.0.0.1 连的是那台服务器自己身上的服务)。两种都和本机无关,
// 写成 127.0.0.1 照样能用 —— 不是提示节点:进自动选择和线路组、照常测速、不标"不能上网"。
// 所以照 detour 一路找下去,按真正从本机拨出去的那一个判:它写着本机地址的(连同经它出去的)才是提示节点。
// detour 指向订阅外的出站(内置的 direct 之类)时,自己的 server 就是从本机拨的,照旧按它判;绕成圈的不当提示节点。
func TestHintNodesSkipDetour(t *testing.T) {
	p := profileOf(t,
		`{"type":"shadowsocks","tag":"日本-stls","server":"127.0.0.1","server_port":8388,"method":"2022-blake3-aes-128-gcm","password":"cGFzc3dvcmRwYXNzd29yZA==","detour":"stls-out"}`,
		`{"type":"shadowtls","tag":"stls-out","server":"1.2.3.4","server_port":443,"version":3,"password":"p","tls":{"enabled":true,"server_name":"www.example.com"}}`,
		`{"type":"shadowsocks","tag":"续费提示","server":"127.0.0.1","server_port":1,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"trojan","tag":"本机占位链","server":"localhost","server_port":443,"password":"p","detour":"本机出口"}`,
		`{"type":"shadowsocks","tag":"本机出口","server":"127.0.0.1","server_port":1,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"trojan","tag":"经 direct","server":"127.0.0.1","server_port":443,"password":"p","detour":"direct"}`,
		`{"type":"trojan","tag":"圈A","server":"127.0.0.1","server_port":443,"password":"p","detour":"圈B"}`,
		`{"type":"trojan","tag":"圈B","server":"127.0.0.1","server_port":443,"password":"p","detour":"圈A"}`,
	)
	if got, want := HintNodes(p), []string{"续费提示", "本机占位链", "本机出口", "经 direct"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("带 detour 的出站不按自己的 server 判提示节点: 得 %v,应 %v", got, want)
	}
}
