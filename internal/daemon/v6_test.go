package daemon

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 关着 IPv6 时要走 IPv6 的节点:状态里列出来(不分平台,也不看网卡 IPv6 停没停);选的就是它(或成员全是它的线路组,
// 或者自动选择而节点全是这种)时标出来;健康检查不通时报单独的错误码,界面据此说清楚原因,不是笼统一句"当前节点不可用"。
func TestV6NodesInStateAndHealth(t *testing.T) {
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
		t.Fatalf("默认设置(关 IPv6):%v %v", v.V6Blocked, v.SelectedV6)
	}
	for sel, want := range map[string]bool{"全v6": true, "混": false, "v4": false, "名字": true, "": false, "已经没了": false} {
		d.settings.Selected = sel
		if got := d.stateView().SelectedV6; got != want {
			t.Fatalf("选 %q:SelectedV6 应为 %v", sel, want)
		}
	}
	// 不看网卡 IPv6 停没停、开没开 TUN(Android 也一样):关着 IPv6 就不走 IPv6
	d.settings.DisableNICIPv6, d.settings.TUN = false, false
	if v := d.stateView(); !reflect.DeepEqual(v.V6Blocked, []string{"v6", "v6b", "名字"}) {
		t.Fatalf("网卡 IPv6 不停用、不开 TUN 时照样不用: %v", v.V6Blocked)
	}
	d.settings.IPv6 = true
	if v := d.stateView(); len(v.V6Blocked) != 0 || v.SelectedV6 {
		t.Fatalf("开着 IPv6 时一个都不标: %v", v.V6Blocked)
	}
	// 选的是自动选择、而节点全是这种(订阅里只有一条 IPv6 线路):自动选择也只能落在它们上面,同样标出来
	d.settings.IPv6 = false
	d.profiles["p1"] = prof(t, `{"type":"socks","tag":"甲","server":"2001:db8::1","server_port":1}`, `{"type":"socks","tag":"乙","server":"2001:db8::2","server_port":1}`)
	for _, sel := range []string{"", "已经没了"} {
		d.settings.Selected = sel
		if !d.stateView().SelectedV6 || !d.selectedV6(d.getSettings()) {
			t.Fatalf("自动选择(选的 %q)而节点全是 IPv6 节点:要标出来,连上马上检查", sel)
		}
	}

	// 健康检查:正在用的是这种节点(配置里经 block 出站,见 builder)→ 单独的错误码
	d.settings = settings.Default()
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	d.profiles["p1"] = prof(t, `{"type":"socks","tag":"v4","server":"127.0.0.1","server_port":1}`, `{"type":"socks","tag":"v6","server":"::1","server_port":1}`)
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["v4","v6"],"default":"v6"},`+
		`{"type":"socks","tag":"v4","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"v6","server":"::1","server_port":1,"detour":"block"},{"type":"block","tag":"block"}`)
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeV6 {
		t.Fatalf("正在用的是关着 IPv6 时不用的节点:应报 %s,得 %v", state.CodeNodeV6, err)
	}
	_ = d.core.Select("proxy", "v4")
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeDown {
		t.Fatalf("普通节点不通照旧报 %s,得 %v", state.CodeNodeDown, err)
	}
}

// 节点列表测速同样不走 IPv6:关着 IPv6 时,要走 IPv6 的节点不测(直接记不通),一个包都不往 IPv6 地址发;
// 域名解析出来的地址只用 IPv4 的。连着(经网卡直连握手)和没连(直连握手)两条路都一样。
func TestProbeSkipsV6Nodes(t *testing.T) {
	ln6, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("这台机器没有 IPv6 回环")
	}
	defer ln6.Close()
	var hits6 atomic.Int32
	go func() {
		for {
			c, err := ln6.Accept()
			if err != nil {
				return
			}
			hits6.Add(1)
			_ = c.Close()
		}
	}()
	ln4, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln4.Close()
	go func() {
		for {
			c, err := ln4.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	p6, p4 := ln6.Addr().(*net.TCPAddr).Port, ln4.Addr().(*net.TCPAddr).Port
	d := lineDaemon(t)
	d.settings.NoExitLookup = true
	d.profiles["p1"] = prof(t,
		`{"type":"trojan","tag":"v4","server":"127.0.0.1","server_port":`+strconv.Itoa(p4)+`,"password":"p"}`,
		`{"type":"trojan","tag":"v6","server":"::1","server_port":`+strconv.Itoa(p6)+`,"password":"p"}`,
	)
	probe := func() map[string]int {
		t.Helper()
		res, err := d.Dispatch(ipc.MProbeNodes, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]int)
	}
	res := probe()
	if res["v6"] != -1 || res["v4"] <= 0 || hits6.Load() != 0 {
		t.Fatalf("没连接时:IPv6 节点不测、记不通,一个包都不发: %v,IPv6 那头收到 %d 次", res, hits6.Load())
	}
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["v4","v6"],"default":"v4"},`+
		`{"type":"trojan","tag":"v4","server":"127.0.0.1","server_port":`+strconv.Itoa(p4)+`,"password":"p"},`+
		`{"type":"trojan","tag":"v6","server":"::1","server_port":`+strconv.Itoa(p6)+`,"password":"p","detour":"block"},{"type":"block","tag":"block"}`)
	res = probe()
	if res["v6"] != -1 || res["v4"] <= 0 || hits6.Load() != 0 {
		t.Fatalf("连着时:同样不测 IPv6 节点: %v,IPv6 那头收到 %d 次", res, hits6.Load())
	}
	// 开着 IPv6 时照常测
	s := d.getSettings()
	s.IPv6 = true
	d.settings = s
	_ = d.core.Stop()
	if res = probe(); res["v6"] <= 0 || hits6.Load() == 0 {
		t.Fatalf("开着 IPv6 时 IPv6 节点照常测: %v", res)
	}
}

// 测速时解析节点域名:关着 IPv6 只取 IPv4 地址(系统解析器可能把 IPv6 排在前面);只有 IPv6 地址就报错,不去连。
func TestLookupNodeIPv4Only(t *testing.T) {
	resolve := func(_ context.Context, h string) ([]string, error) {
		if h == "v6only.example" {
			return []string{"2001:db8::1"}, nil
		}
		return []string{"2001:db8::2", "1.2.3.4"}, nil
	}
	if got, err := lookupNode(context.Background(), "dual.example", resolve, false); err != nil || got != "1.2.3.4" {
		t.Fatalf("双栈取 IPv4: %q %v", got, err)
	}
	if _, err := lookupNode(context.Background(), "v6only.example", resolve, false); err == nil {
		t.Fatal("关着 IPv6、只有 IPv6 地址:不去连")
	}
	if got, err := lookupNode(context.Background(), "v6only.example", resolve, true); err != nil || got != "2001:db8::1" {
		t.Fatalf("开着 IPv6 时只有 IPv6 地址就用它: %q %v", got, err)
	}
	if got, _ := lookupNode(context.Background(), "dual.example", resolve, true); got != "1.2.3.4" {
		t.Fatalf("开着 IPv6 也是 IPv4 优先(和内核的 prefer_ipv4 一样): %q", got)
	}
	// 本来就写成 IPv6 地址的:关着 IPv6 时同样不去连(连着时拨号走内核的直连出站,没有 tcp4 兜着),有没有解析器、带不带方括号都一样
	for _, host := range []string{"::1", "[2001:db8::1]", " 2001:db8::3 "} {
		for _, r := range []func(context.Context, string) ([]string, error){nil, resolve} {
			if got, err := lookupNode(context.Background(), host, r, false); err == nil {
				t.Fatalf("关着 IPv6,写成 IPv6 地址的 %q 不该拿去连: %q", host, got)
			}
		}
	}
	if got, err := lookupNode(context.Background(), "[2001:db8::1]", nil, true); err != nil || got != "2001:db8::1" {
		t.Fatalf("开着 IPv6 时 IPv6 地址照用(去掉方括号): %q %v", got, err)
	}
	if got, err := lookupNode(context.Background(), "::ffff:1.2.3.4", nil, false); err != nil || got != "1.2.3.4" {
		t.Fatalf("IPv4 映射地址就是 IPv4: %q %v", got, err)
	}
	if got, err := lookupNode(context.Background(), "5.6.7.8", resolve, false); err != nil || got != "5.6.7.8" {
		t.Fatalf("IPv4 地址原样用,不去解析: %q %v", got, err)
	}
}

// 带中转的节点(detour):节点列表测速不从本机直接碰落地。落地写成 IPv6 的,从本机拨它就是走 IPv6(关着 IPv6 也一样,
// Android、桌面不开 TUN 时 SYN 真的经网卡发出去);写成 IPv4 的,落地服务器也会直接看到本机地址 —— 用中转往往正是为了不让它看到,
// 量出来的也不是经中转的延迟。连着、没连着都经它自己的出站(经中转)真连一次。
func TestProbeChainedNodesGoViaRelay(t *testing.T) {
	serve := func(ln net.Listener, onConn func(net.Conn)) {
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { onConn(c); _ = c.Close() }()
			}
		}()
	}
	ln6, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("这台机器没有 IPv6 回环")
	}
	defer ln6.Close()
	var land6Hits, land4Hits, greetings atomic.Int32
	serve(ln6, func(net.Conn) { land6Hits.Add(1) })
	ln4, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln4.Close()
	serve(ln4, func(net.Conn) { land4Hits.Add(1) })
	// 中转:只记 SOCKS 握手(经它拨出去的才会发 0x05;本机测它自己的握手连上就关,什么都不发),然后就关 —— 链测出来是不通,
	// 要看的是有没有经它
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	serve(relay, func(c net.Conn) {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, 1)
		if n, _ := c.Read(b); n == 1 && b[0] == 0x05 {
			greetings.Add(1)
		}
	})
	port := func(ln net.Listener) string { return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) }
	relayNode := `{"type":"socks","tag":"relay","server":"127.0.0.1","server_port":` + port(relay) + `}`
	land6 := `{"type":"socks","tag":"land6","server":"::1","server_port":` + port(ln6) + `,"detour":"relay"}`
	land4 := `{"type":"socks","tag":"land4","server":"127.0.0.1","server_port":` + port(ln4) + `,"detour":"relay"}`
	d := lineDaemon(t)
	d.settings.NoExitLookup = true
	d.profiles["p1"] = prof(t, relayNode, land6, land4)
	if b := d.v6BlockedNow(d.getSettings()); len(b) != 0 {
		t.Fatalf("前提:中转是 IPv4 的,落地写成 IPv6 也照样能用,不在不用的那批里: %v", b)
	}
	probe := func() map[string]int {
		t.Helper()
		res, err := d.Dispatch(ipc.MProbeNodes, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]int)
	}
	waitGreetings := func(n int32) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); greetings.Load() < n && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
	}
	res := probe()
	waitGreetings(2)
	if land6Hits.Load() != 0 || land4Hits.Load() != 0 {
		t.Fatalf("没连接时:落地一次都不该从本机直接连(IPv6 落地 %d 次、IPv4 落地 %d 次): %v", land6Hits.Load(), land4Hits.Load(), res)
	}
	if greetings.Load() < 2 || res["relay"] <= 0 || res["land6"] != -1 || res["land4"] != -1 {
		t.Fatalf("没连接时:两条链都经中转真连一次(中转收到 %d 次握手),中转自己照常量握手: %v", greetings.Load(), res)
	}

	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["relay","land6","land4"],"default":"relay"},`+relayNode+`,`+land6+`,`+land4)
	before := greetings.Load()
	res = probe()
	waitGreetings(before + 2)
	if land6Hits.Load() != 0 || land4Hits.Load() != 0 {
		t.Fatalf("连着时:落地一次都不该从本机直接连(IPv6 落地 %d 次、IPv4 落地 %d 次): %v", land6Hits.Load(), land4Hits.Load(), res)
	}
	if greetings.Load()-before < 2 || res["relay"] <= 0 || res["land6"] != -1 || res["land4"] != -1 {
		t.Fatalf("连着时:两条链经内核里它们自己的出站(经中转)测(中转收到 %d 次握手): %v", greetings.Load()-before, res)
	}
}

// 用域名写的节点只有 IPv6 地址:系统解析器一并给出 IPv6 的,全是 IPv6 的再单问一次 IPv4 确认;
// DoH 只问了 A、答"没有 A 记录"的再问一次 A 确认、再问 AAAA。偶发一次空答复(上游抖动、A 查询超时)不能把双栈域名判成只有 IPv6。
func TestV6OnlyHosts(t *testing.T) {
	p := prof(t,
		`{"type":"trojan","tag":"a","server":"sys6.example","server_port":443}`,
		`{"type":"trojan","tag":"b","server":"dual.example","server_port":443}`,
		`{"type":"trojan","tag":"c","server":"doh6.example","server_port":443}`,
		`{"type":"trojan","tag":"d","server":"gone.example","server_port":443}`,
		`{"type":"trojan","tag":"e","server":"1.2.3.4","server_port":443}`,
		`{"type":"trojan","tag":"f","server":"flaky.example","server_port":443}`,
		`{"type":"trojan","tag":"g","server":"sysflaky.example","server_port":443}`,
	)
	ips := map[string][]string{"sys6.example": {"2001:db8::1"}, "dual.example": {"1.2.3.5", "2001:db8::2"}, "sysflaky.example": {"2001:db8::4"}}
	var mu sync.Mutex
	var asked4, asked6 []string
	lookup4 := func(_ context.Context, h string) ([]string, error) {
		mu.Lock()
		asked4 = append(asked4, h)
		mu.Unlock()
		switch h {
		case "flaky.example", "sysflaky.example": // 上一次是偶发的空答复 / A 超时:再问一次就有 A 了
			return []string{"1.2.3.6"}, nil
		case "gone.example":
			return nil, errors.New("超时")
		}
		return nil, errNoARecord
	}
	lookup6 := func(_ context.Context, h string) ([]string, error) {
		mu.Lock()
		asked6 = append(asked6, h)
		mu.Unlock()
		if h == "doh6.example" || h == "flaky.example" {
			return []string{"2001:db8::3"}, nil
		}
		return nil, nil
	}
	noA := map[string]bool{"doh6.example": true, "gone.example": true, "flaky.example": true} // DoH 答了但没有 A 记录
	got := v6OnlyHosts(context.Background(), p, ips, noA, lookup4, lookup6)
	if !reflect.DeepEqual(got, map[string]bool{"sys6.example": true, "doh6.example": true}) {
		t.Fatalf("得 %v", got)
	}
	if len(asked6) != 1 || asked6[0] != "doh6.example" {
		t.Fatalf("只有两次都确认没有 A 记录的才问 AAAA: %v", asked6)
	}
	asked4, asked6 = nil, nil
	if got := v6OnlyHosts(context.Background(), p, ips, nil, nil, lookup6); len(got) != 0 {
		t.Fatalf("没法再问一次确认的就不下结论: %v", got)
	}
	if v6OnlyHosts(context.Background(), p, nil, nil, nil, nil) != nil {
		t.Fatal("不解析的那一轮什么都认不出来")
	}
	// 防逃逸下本地 DNS 是 system / 域名 DoH 时不解析,也就不补问;按地址连的 DoH 才问 AAAA
	d := &Daemon{}
	s := settings.Default()
	s.LocalDNS = "system"
	if d.bootLookup6(s) != nil {
		t.Fatal("系统解析器本来就把 IPv6 地址一起给了,不用另外问")
	}
	if d.bootLookup4(s) == nil {
		t.Fatal("规则模式下系统解析器要能单问 IPv4 确认")
	}
	s.LocalDNS = "223.5.5.5"
	if d.bootLookup6(s) == nil || d.bootLookup4(s) == nil {
		t.Fatal("按地址连的 DoH 要能补问")
	}
	s.LocalDNS, s.Mode = "system", settings.ModeGlobal // 严格全局:不解析
	if d.bootLookup4(s) != nil {
		t.Fatal("严格全局下不解析,也不补问")
	}
	// prepare 只在关着 IPv6 时去找
	prep := funcBody(t, readDaemonSource(t), "func (d *Daemon) prepare(ctx context.Context) ([]byte, error) {")
	if !strings.Contains(prep, "if !s.IPv6 {") || !strings.Contains(prep, "V6Only: v6Only") {
		t.Fatal("prepare 关着 IPv6 时要找出只有 IPv6 地址的节点域名并交给配置生成")
	}
}

// DoH 的答复要看 RCODE:SERVFAIL / REFUSED 是上游出错,不是"没有 A 记录";只有 NOERROR 而答案里没有 A 才算(NODATA)。
func TestDoHAnswerRcode(t *testing.T) {
	pack := func(rc dnsmessage.RCode, answers ...dnsmessage.Resource) []byte {
		t.Helper()
		name := dnsmessage.MustNewName("x.example.")
		m := dnsmessage.Message{Header: dnsmessage.Header{Response: true, RCode: rc},
			Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}, Answers: answers}
		b, err := m.Pack()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a := dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("x.example."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		Body: &dnsmessage.AResource{A: [4]byte{1, 2, 3, 4}}}
	if addrs, err := parseDoHAnswer(pack(dnsmessage.RCodeSuccess, a), dnsmessage.TypeA); err != nil || !reflect.DeepEqual(addrs, []string{"1.2.3.4"}) {
		t.Fatalf("正常答复: %v %v", addrs, err)
	}
	if addrs, err := parseDoHAnswer(pack(dnsmessage.RCodeSuccess), dnsmessage.TypeA); err != nil || len(addrs) != 0 {
		t.Fatalf("NODATA:没有这种记录,不是错: %v %v", addrs, err)
	}
	for _, rc := range []dnsmessage.RCode{dnsmessage.RCodeServerFailure, dnsmessage.RCodeRefused, dnsmessage.RCodeNameError} {
		if _, err := parseDoHAnswer(pack(rc), dnsmessage.TypeA); err == nil {
			t.Fatalf("%v 不能当成没有 A 记录", rc)
		}
	}
	if _, err := parseDoHAnswer([]byte("乱的"), dnsmessage.TypeA); err == nil {
		t.Fatal("解不开就是错")
	}
}

// 关着 IPv6 时地址写成 IPv6 的节点改经 block 出站:这样的配置内核照样起得来(干跑通过),各种协议都一样。
func TestBuildValidWithV6Block(t *testing.T) {
	p := prof(t,
		`{"type":"hysteria2","tag":"hy6","server":"2001:db8::1","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`,
		`{"type":"anytls","tag":"any6","server":"[2001:db8::2]","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`,
		`{"type":"tuic","tag":"tuic6","server":"2001:db8::3","server_port":443,"uuid":"2dd61d93-75d8-4da4-ac0e-6aece7eac365","password":"p","tls":{"enabled":true,"server_name":"c.example"}}`,
		`{"type":"vless","tag":"vless4","server":"1.2.3.4","server_port":443,"uuid":"2dd61d93-75d8-4da4-ac0e-6aece7eac365","tls":{"enabled":true,"server_name":"d.example"}}`,
	)
	cfg, rep, skipped, err := buildValid(core.New(nil), builder.Input{Profile: p, Settings: settings.Default(), DataDir: t.TempDir(), ClashSecret: "x"})
	if err != nil || len(skipped) != 0 {
		t.Fatalf("经 block 的配置应能过干跑: %v %v", err, skipped)
	}
	if !reflect.DeepEqual(rep.V6Blocked, []string{"hy6", "any6", "tuic6"}) || strings.Count(string(cfg), `"detour": "block"`) != 3 {
		t.Fatalf("三个 IPv6 节点都经 block: %v\n%s", rep.V6Blocked, cfg)
	}
}
