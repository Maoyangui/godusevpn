package builder

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// v6Profile 地址写法各不相同的一份订阅,外加几种链式节点(detour:本机只连第一跳,后面由上一跳的服务器去连、去解析)。
func v6Profile() *profile.Profile {
	p := &profile.Profile{}
	for _, n := range []string{
		`{"type":"trojan","tag":"v4","server":"1.2.3.4","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"v6","server":"2001:db8::1","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"v6括号","server":"[2001:db8::2]","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"只有AAAA","server":"v6only.example","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"双栈","server":"dual.example","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"映射","server":"::ffff:1.2.3.9","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"v4中转v6落地","server":"2001:db8::5","server_port":443,"password":"p","detour":"v4"}`,
		`{"type":"trojan","tag":"经v6中转","server":"1.2.3.10","server_port":443,"password":"p","detour":"v6"}`,
		`{"type":"trojan","tag":"链里的v6域名","server":"v6only.example","server_port":443,"password":"p","detour":"v4"}`,
		`{"type":"naive","tag":"naive链","server":"v6only.example","server_port":443,"username":"u","password":"p","detour":"v4"}`,
		`{"type":"trojan","tag":"指向不存在的","server":"2001:db8::7","server_port":443,"password":"p","detour":"没有这个"}`,
	} {
		var m struct {
			Tag string `json:"tag"`
		}
		_ = json.Unmarshal([]byte(n), &m)
		p.Outbounds = append(p.Outbounds, json.RawMessage(n))
		p.Tags = append(p.Tags, m.Tag)
	}
	p.Groups = []profile.Group{{Tag: "线路", Members: []string{"v4", "v6"}}, {Tag: "全 v6", Members: []string{"v6", "只有AAAA"}}}
	return p
}

// 关着 IPv6(「允许 IPv6」关)时,连节点要从本机走 IPv6 的一概不用 —— 不分平台:Android、桌面不开 TUN、
// 关掉「连接时停用网卡 IPv6」时也一样(手机网络、网卡有 IPv6 的话本来连得上,可那就是在走 IPv6)。
// 这些节点不进自动选择和线路组;手动选它时内核也不去拨(出站经 block,一个包都不发),健康检查不通、界面说清原因。
// 链式节点按第一跳算:中转是 IPv4 的,落地写成 IPv6 也照样能用(本机不碰 IPv6)。
func TestV6NodesExcludedWhenIPv6Off(t *testing.T) {
	p := v6Profile()
	v6Only := map[string]bool{"v6only.example": true}
	build := func(s settings.Settings, android bool) (cfg, Report) {
		t.Helper()
		raw, rep, err := BuildEx(Input{Profile: p, Settings: s, DataDir: "/data", ClashSecret: "sec", RuleSetDir: ruleSetRoot(t), V6Only: v6Only, Android: android, Linux: !android})
		if err != nil {
			t.Fatal(err)
		}
		var c cfg
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		return c, rep
	}
	want := []string{"v6", "v6括号", "只有AAAA", "经v6中转", "naive链"}

	s := settings.Default() // 关着 IPv6、开着 TUN、开着「连接时停用网卡 IPv6」
	s.Selected = "线路"
	c, rep := build(s, false)
	if !reflect.DeepEqual(rep.V6Blocked, want) {
		t.Fatalf("关着 IPv6 时要走 IPv6 的节点: %v", rep.V6Blocked)
	}
	by := outboundByTag(c)
	if got := anyStrings(by["auto"]["outbounds"]); !reflect.DeepEqual(got, []string{"v4", "双栈", "映射", "v4中转v6落地", "链里的v6域名", "指向不存在的"}) {
		t.Fatalf("自动选择里不该有要走 IPv6 的节点: %v", got)
	}
	if got := anyStrings(by["线路"]["outbounds"]); !reflect.DeepEqual(got, []string{"v4"}) {
		t.Fatalf("线路组里的 IPv6 成员同样剔掉: %v", got)
	}
	if got := anyStrings(by["全 v6"]["outbounds"]); !reflect.DeepEqual(got, []string{"v6", "只有AAAA"}) {
		t.Fatalf("成员全是 IPv6 节点的组原样留着,配置才建得起来: %v", got)
	}
	if got := anyStrings(c.Outbounds[0]["outbounds"]); len(got) != 1+2+len(p.Tags) {
		t.Fatalf("proxy 里节点一个不少(手动选它照样能选上,只是连不通): %v", got)
	}
	// 地址写成 IPv6、本机直接拨的:出站改经 block,手动选它内核也不会从本机发一个 IPv6 包
	if by["block"] == nil || by["block"]["type"] != "block" {
		t.Fatalf("要有一个 block 出站兜住它们: %v", by["block"])
	}
	for _, tag := range []string{"v6", "v6括号"} {
		if by[tag]["detour"] != "block" {
			t.Fatalf("%s 地址写成 IPv6:出站应经 block: %v", tag, by[tag])
		}
	}
	for tag, d := range map[string]any{"v4": nil, "只有AAAA": nil, "v4中转v6落地": "v4", "经v6中转": "v6", "链里的v6域名": "v4", "指向不存在的": "没有这个"} {
		if by[tag]["detour"] != d {
			t.Fatalf("%s 不该改 detour(域名只有 IPv6 的本来就解析不出地址;链式节点经第一跳): %v", tag, by[tag]["detour"])
		}
	}

	// 不分平台、不看网卡 IPv6 停没停:Android、关掉「连接时停用网卡 IPv6」、不开 TUN 都一样
	if _, rep = build(s, true); !reflect.DeepEqual(rep.V6Blocked, want) {
		t.Fatalf("Android 上也一样(手机网络有 IPv6 也不走): %v", rep.V6Blocked)
	}
	s2 := s
	s2.DisableNICIPv6 = false
	if _, rep = build(s2, false); !reflect.DeepEqual(rep.V6Blocked, want) {
		t.Fatalf("网卡 IPv6 没停用时也一样: %v", rep.V6Blocked)
	}
	s2.TUN = false
	if _, rep = build(s2, false); !reflect.DeepEqual(rep.V6Blocked, want) {
		t.Fatalf("不开 TUN 时也一样: %v", rep.V6Blocked)
	}
	// 打开 IPv6:都用,配置里 auto 覆盖全部节点,一个出站都不改,也没有 block
	s3 := s
	s3.IPv6 = true
	c, rep = build(s3, false)
	by = outboundByTag(c)
	if len(rep.V6Blocked) != 0 || len(anyStrings(by["auto"]["outbounds"])) != len(p.Tags) || by["block"] != nil || by["v6"]["detour"] != nil {
		t.Fatalf("开着 IPv6 时一个都不剔、不改: %v", rep.V6Blocked)
	}
}

// 没有要挡的 IPv6 节点时,配置里不多出 block 出站、节点一个字不改(和以前逐字节一样)。
func TestNoV6NodesConfigUnchanged(t *testing.T) {
	p := groupProfile()
	s := settings.Default()
	raw, rep, err := BuildEx(Input{Profile: p, Settings: s, DataDir: "/data", ClashSecret: "sec", RuleSetDir: ruleSetRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.V6Blocked) != 0 || strings.Contains(string(raw), `"block"`) || strings.Contains(string(raw), `"detour": "block"`) {
		t.Fatalf("没有 IPv6 节点时配置里不该有 block: %v", rep.V6Blocked)
	}
}

// V6Blocked 本身:链式节点按第一跳判,链成环、指向不存在的出站都不算(判不了);开着 IPv6 时一个不算。
func TestV6BlockedChains(t *testing.T) {
	p := v6Profile()
	p.Outbounds = append(p.Outbounds,
		json.RawMessage(`{"type":"trojan","tag":"环A","server":"2001:db8::8","server_port":443,"detour":"环B"}`),
		json.RawMessage(`{"type":"trojan","tag":"环B","server":"2001:db8::9","server_port":443,"detour":"环A"}`),
		json.RawMessage(`{"type":"trojan","tag":"两跳","server":"1.2.3.11","server_port":443,"detour":"经v6中转"}`),
	)
	p.Tags = append(p.Tags, "环A", "环B", "两跳")
	s := settings.Default()
	got := V6Blocked(p, s, map[string]bool{"v6only.example": true})
	if want := []string{"v6", "v6括号", "只有AAAA", "经v6中转", "naive链", "两跳"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("得 %v,应 %v", got, want)
	}
	s.IPv6 = true
	if got := V6Blocked(p, s, map[string]bool{"v6only.example": true}); len(got) != 0 {
		t.Fatalf("开着 IPv6 时一个不算: %v", got)
	}
}
