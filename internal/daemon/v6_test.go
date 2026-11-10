package daemon

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 关着 IPv6 时只能走 IPv6 的节点:状态里列出来;选的就是它(或成员全是它的线路组)时标出来;
// 健康检查不通时报单独的错误码,界面据此说清楚原因,不是笼统一句"当前节点不可用"。
func TestV6NodesInStateAndHealth(t *testing.T) {
	if !netmode.NICIPv6Manageable() {
		t.Skip("这个平台不停用网卡 IPv6:地址写成 IPv6 的节点照样连得上")
	}
	d := lineDaemon(t)
	d.profiles["p1"] = prof(t,
		`{"type":"socks","tag":"v4","server":"127.0.0.1","server_port":1}`,
		`{"type":"socks","tag":"v6","server":"::1","server_port":1}`,
		`{"type":"socks","tag":"v6b","server":"::1","server_port":2}`,
		`{"type":"socks","tag":"名字","server":"v6only.example","server_port":1}`,
	)
	d.profiles["p1"].Groups = []profile.Group{{Tag: "全v6", Members: []string{"v6", "v6b"}}, {Tag: "混", Members: []string{"v4", "v6"}}}
	d.v6Only = map[string]bool{"v6only.example": true}
	d.settings.Selected = "v6"
	v := d.stateView()
	if !reflect.DeepEqual(v.V6Blocked, []string{"v6", "v6b", "名字"}) || !v.SelectedV6 {
		t.Fatalf("默认设置(关 IPv6、连接时停用网卡 IPv6):%v %v", v.V6Blocked, v.SelectedV6)
	}
	for sel, want := range map[string]bool{"全v6": true, "混": false, "v4": false, "名字": true, "": false} {
		d.settings.Selected = sel
		if got := d.stateView().SelectedV6; got != want {
			t.Fatalf("选 %q:SelectedV6 应为 %v", sel, want)
		}
	}
	d.settings.DisableNICIPv6 = false
	if v := d.stateView(); !reflect.DeepEqual(v.V6Blocked, []string{"名字"}) {
		t.Fatalf("网卡 IPv6 不停用时,只剩域名解析不出 IPv4 的那种: %v", v.V6Blocked)
	}
	d.settings.IPv6 = true
	if v := d.stateView(); len(v.V6Blocked) != 0 || v.SelectedV6 {
		t.Fatalf("开着 IPv6 时一个都不标: %v", v.V6Blocked)
	}

	d.settings = settings.Default()
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["v4","v6"],"default":"v6"},`+
		`{"type":"socks","tag":"v4","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"v6","server":"::1","server_port":1}`)
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeV6 {
		t.Fatalf("正在用的是关着 IPv6 时连不上的节点:应报 %s,得 %v", state.CodeNodeV6, err)
	}
	_ = d.core.Select("proxy", "v4")
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeDown {
		t.Fatalf("普通节点不通照旧报 %s,得 %v", state.CodeNodeDown, err)
	}
}

// 用域名写的节点只有 IPv6 地址:系统解析器一并给出 IPv6 的,全是 IPv6 就算;DoH 只问了 A、没问到的再问一次 AAAA。
func TestV6OnlyHosts(t *testing.T) {
	p := prof(t,
		`{"type":"trojan","tag":"a","server":"sys6.example","server_port":443}`,
		`{"type":"trojan","tag":"b","server":"dual.example","server_port":443}`,
		`{"type":"trojan","tag":"c","server":"doh6.example","server_port":443}`,
		`{"type":"trojan","tag":"d","server":"gone.example","server_port":443}`,
		`{"type":"trojan","tag":"e","server":"1.2.3.4","server_port":443}`,
	)
	ips := map[string][]string{"sys6.example": {"2001:db8::1"}, "dual.example": {"1.2.3.5", "2001:db8::2"}}
	var mu sync.Mutex
	var asked []string
	lookup6 := func(_ context.Context, h string) ([]string, error) {
		mu.Lock()
		asked = append(asked, h)
		mu.Unlock()
		if h == "doh6.example" {
			return []string{"2001:db8::3"}, nil
		}
		return nil, nil
	}
	noA := map[string]bool{"doh6.example": true, "gone.example": true} // DoH 答了但没有 A 记录;别的(连不上 DoH)不补问
	got := v6OnlyHosts(context.Background(), p, ips, noA, lookup6)
	if !reflect.DeepEqual(got, map[string]bool{"sys6.example": true, "doh6.example": true}) {
		t.Fatalf("得 %v", got)
	}
	if len(asked) != 2 {
		t.Fatalf("只有问到了却没有 A 记录的才补问 AAAA: %v", asked)
	}
	asked = nil
	if got := v6OnlyHosts(context.Background(), p, ips, nil, lookup6); len(got) != 1 || len(asked) != 0 {
		t.Fatalf("DoH 连不上时不补问(否则每次连接多等一轮超时): %v %v", got, asked)
	}
	if v6OnlyHosts(context.Background(), p, nil, nil, nil) != nil {
		t.Fatal("不解析的那一轮什么都认不出来")
	}
	// 防逃逸下本地 DNS 是 system / 域名 DoH 时不解析,也就不补问;按地址连的 DoH 才问
	d := &Daemon{}
	s := settings.Default()
	s.LocalDNS = "system"
	if d.bootLookup6(s) != nil {
		t.Fatal("系统解析器本来就把 IPv6 地址一起给了,不用另外问")
	}
	s.LocalDNS = "223.5.5.5"
	if d.bootLookup6(s) == nil {
		t.Fatal("按地址连的 DoH 要能补问 AAAA")
	}
	// prepare 只在关着 IPv6 时去找
	prep := funcBody(t, readDaemonSource(t), "func (d *Daemon) prepare(ctx context.Context) ([]byte, error) {")
	if !strings.Contains(prep, "if !s.IPv6 {") || !strings.Contains(prep, "V6Only: v6Only") {
		t.Fatal("prepare 关着 IPv6 时要找出只有 IPv6 地址的节点域名并交给配置生成")
	}
}
