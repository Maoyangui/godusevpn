package daemon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// fakeHostV6 把"本机有没有 IPv6"换成给定的答复(并让缓存作废),测试结束还原。返回一个再换一次的函数。
func fakeHostV6(t *testing.T, d *Daemon, has, known bool) func(has, known bool) {
	t.Helper()
	old := hostIPv6
	t.Cleanup(func() { hostIPv6 = old })
	set := func(has, known bool) {
		hostIPv6 = func() (bool, bool) { return has, known }
		d.hostV6Mu.Lock()
		d.hostV6At = time.Time{}
		d.hostV6Mu.Unlock()
	}
	set(has, known)
	return set
}

// 「允许 IPv6」开着、可本机网络没有 IPv6(网卡上没有公网 IPv6 地址)时,只能走 IPv6 的节点同样连不上。以前这种情况什么都不说:
// 选了这样的节点,看着就是"节点不可用",像断网了。现在状态里把这些节点列出来、选中的是它时标出来,健康检查报单独的错误码;
// 网络有 IPv6、或者网卡读不出来(下不了结论)时一个都不标;关着 IPv6 的情况仍归 V6Blocked,不重复。
func TestV6NoNetInStateAndHealth(t *testing.T) {
	d := lineDaemon(t)
	d.profiles["p1"] = prof(t,
		`{"type":"socks","tag":"v4","server":"127.0.0.1","server_port":1}`,
		`{"type":"socks","tag":"v6","server":"::1","server_port":1}`,
		`{"type":"socks","tag":"v6b","server":"::1","server_port":2}`,
		`{"type":"socks","tag":"名字","server":"v6only.example","server_port":1}`,
	)
	d.profiles["p1"].Groups = []profile.Group{{Tag: "全v6", Members: []string{"v6", "v6b"}}, {Tag: "混", Members: []string{"v4", "v6"}}}
	d.v6Only = map[string]bool{"v6only.example": true}
	d.settings.IPv6 = true
	d.settings.Selected = "v6"
	set := fakeHostV6(t, d, true, true)

	if v := d.stateView(); len(v.V6NoNet) != 0 || v.SelectedV6NoNet || len(v.V6Blocked) != 0 || v.SelectedV6 {
		t.Fatalf("网络有 IPv6 时一个都不标: %v %v", v.V6NoNet, v.SelectedV6NoNet)
	}
	set(false, true)
	v := d.stateView()
	if !reflect.DeepEqual(v.V6NoNet, []string{"v6", "v6b", "名字"}) || !v.SelectedV6NoNet {
		t.Fatalf("网络没有 IPv6:只能走 IPv6 的节点要列出来、选中的要标: %v %v", v.V6NoNet, v.SelectedV6NoNet)
	}
	if len(v.V6Blocked) != 0 || v.SelectedV6 {
		t.Fatalf("这不是「允许 IPv6」关着的那种,不能混进 V6Blocked: %v", v.V6Blocked)
	}
	for sel, want := range map[string]bool{"全v6": true, "混": false, "v4": false, "名字": true, "": false, "已经没了": false} {
		d.settings.Selected = sel
		if got := d.stateView().SelectedV6NoNet; got != want {
			t.Fatalf("选 %q:SelectedV6NoNet 应为 %v", sel, want)
		}
	}
	set(false, false)
	if v := d.stateView(); len(v.V6NoNet) != 0 || v.SelectedV6NoNet {
		t.Fatalf("网卡读不出来(Android 11 起)时不下结论: %v", v.V6NoNet)
	}
	// 关着 IPv6:归 V6Blocked 管(设置挡着),这边不重复
	set(false, true)
	d.settings.IPv6 = false
	d.settings.Selected = "v6"
	if v := d.stateView(); len(v.V6NoNet) != 0 || v.SelectedV6NoNet || !reflect.DeepEqual(v.V6Blocked, []string{"v6", "v6b", "名字"}) || !v.SelectedV6 {
		t.Fatalf("关着 IPv6 时只标 V6Blocked: %v / %v", v.V6NoNet, v.V6Blocked)
	}

	// 读网卡的结果留几秒:状态查询每秒都来,不是每次都去读一遍
	d.settings.IPv6 = true
	calls := 0
	hostIPv6 = func() (bool, bool) { calls++; return false, true }
	d.hostV6Mu.Lock()
	d.hostV6At = time.Time{}
	d.hostV6Mu.Unlock()
	for i := 0; i < 5; i++ {
		_ = d.stateView()
	}
	if calls != 1 {
		t.Fatalf("5 次状态查询应只读一次网卡,读了 %d 次", calls)
	}

	// 健康检查:正在用的是这种节点 → 单独的错误码(界面说"当前网络没有 IPv6",不是笼统的节点不可用);普通节点、网络有 IPv6 时照旧
	d.settings = settings.Default()
	d.settings.IPv6 = true
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	d.profiles["p1"] = prof(t, `{"type":"socks","tag":"v4","server":"127.0.0.1","server_port":1}`, `{"type":"socks","tag":"v6","server":"::1","server_port":1}`)
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["v4","v6"],"default":"v6"},`+
		`{"type":"socks","tag":"v4","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"v6","server":"::1","server_port":1}`)
	set(false, true)
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeV6Net {
		t.Fatalf("正在用的节点只能走 IPv6、网络没有 IPv6:应报 %s,得 %v", state.CodeNodeV6Net, err)
	}
	_ = d.core.Select("proxy", "v4")
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeDown {
		t.Fatalf("普通节点不通照旧报 %s,得 %v", state.CodeNodeDown, err)
	}
	_ = d.core.Select("proxy", "v6")
	set(true, true)
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeDown {
		t.Fatalf("网络有 IPv6 时 IPv6 节点不通就是节点不通,报 %s,得 %v", state.CodeNodeDown, err)
	}
}

// v6NoNet 本身:只在「允许 IPv6」开着、而且确知网络没有 IPv6 时给出节点;判哪些节点要走 IPv6 和 V6Blocked 是同一套(按第一跳)。
func TestV6NoNetList(t *testing.T) {
	p := prof(t,
		`{"type":"socks","tag":"v4","server":"1.2.3.4","server_port":1}`,
		`{"type":"socks","tag":"v6","server":"2001:db8::1","server_port":1}`,
		`{"type":"socks","tag":"v4中转v6落地","server":"2001:db8::2","server_port":1,"detour":"v4"}`,
		`{"type":"socks","tag":"经v6中转","server":"1.2.3.5","server_port":1,"detour":"v6"}`,
	)
	on, off := settings.Default(), settings.Default()
	on.IPv6, off.IPv6 = true, false
	if got := v6NoNet(p, on, nil, true); !reflect.DeepEqual(got, []string{"v6", "经v6中转"}) {
		t.Fatalf("开着 IPv6、网络没有 IPv6: %v", got)
	}
	if got := v6NoNet(p, on, nil, false); got != nil {
		t.Fatalf("网络有 IPv6(或读不出来)时为空: %v", got)
	}
	if got := v6NoNet(p, off, nil, true); got != nil {
		t.Fatalf("关着 IPv6 时归 V6Blocked,这里为空: %v", got)
	}
	if got := v6NoNet(nil, on, nil, true); got != nil {
		t.Fatalf("没有订阅: %v", got)
	}
}
