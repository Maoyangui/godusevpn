package builder

import (
	"encoding/json"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// Linux 上全局禁直连的闸按标记放行本服务(不再放行整个 root):内核的出站必须带 RouteMark,
// 否则闸一开,节点连接自己就被拦死。网关模式开着 TUN 时 sing-box 不许同时设 default_mark(和 auto_redirect 冲突,
// 内核起不来),那时出站带的是 auto_redirect 的标记,闸另外认它。其它平台 sing-box 不认这个字段。
func TestLinuxRouteMark(t *testing.T) {
	mark := func(in Input) any {
		t.Helper()
		in.Profile, in.DataDir, in.ClashSecret, in.RuleSetDir = sampleProfile(), t.TempDir(), "sec", ruleSetRoot(t)
		raw, err := Build(in)
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Route map[string]any `json:"route"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		return c.Route["default_mark"]
	}
	local := settings.Default()
	global := local
	global.Mode = settings.ModeGlobal
	gw := local
	gw.NetMode = settings.NetGateway
	gwNoTun := gw
	gwNoTun.TUN = false
	gwNoTun.MixedPort = 2080
	for _, c := range []struct {
		name string
		in   Input
		want bool
	}{
		{"Linux 本机模式", Input{Settings: local, Linux: true}, true},
		{"Linux 全局模式(和模式无关,切模式不重建)", Input{Settings: global, Linux: true}, true},
		{"Linux 网关模式开 TUN:交给 auto_redirect", Input{Settings: gw, Linux: true}, false},
		{"Linux 网关模式但没开 TUN:没有 auto_redirect,照样打", Input{Settings: gwNoTun, Linux: true}, true},
		{"macOS", Input{Settings: local, Linux: true, Darwin: true}, false},
		{"Android", Input{Settings: local, Linux: true, Android: true}, false},
	} {
		got := mark(c.in)
		if c.want && got != float64(RouteMark) {
			t.Fatalf("%s:default_mark 应为 %#x,得到 %v", c.name, RouteMark, got)
		}
		if !c.want && got != nil {
			t.Fatalf("%s:不该有 default_mark,得到 %v", c.name, got)
		}
	}
}
