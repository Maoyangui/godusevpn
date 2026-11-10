package builder

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 关着 IPv6 时一定连不上的 IPv6 节点:地址直接写成 IPv6 的(连接期间网卡 IPv6 被停用时)、域名只有 IPv6 地址的,
// 不进自动选择和线路组,报在 Report.V6Blocked 里;手动选的照样留在 proxy 里。全被剔光的组原样留着(空组整份配置起不来)。
func TestV6NodesExcludedWhenIPv6Off(t *testing.T) {
	p := &profile.Profile{}
	for _, n := range []string{
		`{"type":"trojan","tag":"v4","server":"1.2.3.4","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"v6","server":"2001:db8::1","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"v6括号","server":"[2001:db8::2]","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"只有AAAA","server":"v6only.example","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"双栈","server":"dual.example","server_port":443,"password":"p"}`,
		`{"type":"trojan","tag":"映射","server":"::ffff:1.2.3.9","server_port":443,"password":"p"}`,
	} {
		var m struct {
			Tag string `json:"tag"`
		}
		_ = json.Unmarshal([]byte(n), &m)
		p.Outbounds = append(p.Outbounds, json.RawMessage(n))
		p.Tags = append(p.Tags, m.Tag)
	}
	p.Groups = []profile.Group{{Tag: "线路", Members: []string{"v4", "v6"}}, {Tag: "全 v6", Members: []string{"v6", "只有AAAA"}}}
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

	s := settings.Default() // 关着 IPv6、开着 TUN、开着「连接时停用网卡 IPv6」
	s.Selected = "线路"
	c, rep := build(s, false)
	if want := []string{"v6", "v6括号", "只有AAAA"}; !reflect.DeepEqual(rep.V6Blocked, want) {
		t.Fatalf("桌面端默认设置下连不上的 IPv6 节点: %v", rep.V6Blocked)
	}
	by := outboundByTag(c)
	if got := anyStrings(by["auto"]["outbounds"]); !reflect.DeepEqual(got, []string{"v4", "双栈", "映射"}) {
		t.Fatalf("自动选择里不该有连不上的 IPv6 节点: %v", got)
	}
	if got := anyStrings(by["线路"]["outbounds"]); !reflect.DeepEqual(got, []string{"v4"}) {
		t.Fatalf("线路组里的 IPv6 成员同样剔掉: %v", got)
	}
	if got := anyStrings(by["全 v6"]["outbounds"]); !reflect.DeepEqual(got, []string{"v6", "只有AAAA"}) {
		t.Fatalf("成员全是 IPv6 节点的组原样留着,配置才建得起来: %v", got)
	}
	if got := anyStrings(c.Outbounds[0]["outbounds"]); len(got) != 1+2+6 {
		t.Fatalf("proxy 里节点一个不少(手动选它照样能选上): %v", got)
	}

	// Android 不停用网卡:地址写成 IPv6 的照样连得上(手机网络有 IPv6 时);只有 IPv6 地址的域名照样解析不出来
	_, rep = build(s, true)
	if want := []string{"只有AAAA"}; !reflect.DeepEqual(rep.V6Blocked, want) {
		t.Fatalf("Android 上只有域名解析不出来的那种连不上: %v", rep.V6Blocked)
	}
	// 关掉「连接时停用网卡 IPv6」:同上
	s2 := s
	s2.DisableNICIPv6 = false
	if _, rep = build(s2, false); !reflect.DeepEqual(rep.V6Blocked, []string{"只有AAAA"}) {
		t.Fatalf("网卡 IPv6 没停用时只剩域名那种: %v", rep.V6Blocked)
	}
	// 打开 IPv6:都连得上,配置里 auto 覆盖全部节点
	s3 := s
	s3.IPv6 = true
	c, rep = build(s3, false)
	if len(rep.V6Blocked) != 0 || len(anyStrings(outboundByTag(c)["auto"]["outbounds"])) != 6 {
		t.Fatalf("开着 IPv6 时一个都不剔: %v", rep.V6Blocked)
	}
}
