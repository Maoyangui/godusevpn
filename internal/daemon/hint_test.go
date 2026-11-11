package daemon

import (
	"encoding/json"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// hintProfile 线路「hk」部署在两台服务器上,组里还混着一条指向 127.0.0.1 的「续费提示」;订阅最前面是 m-ui 式的用量提示节点。
func hintProfile(t *testing.T) *profile.Profile {
	t.Helper()
	p := prof(t,
		`{"type":"shadowsocks","tag":"00-勿选-流量:1.0/100.0 重置:3天","server":"127.0.0.1","server_port":1,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"shadowsocks","tag":"hk-续费提示","server":"127.0.0.1","server_port":88,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"socks","tag":"hk-A","server":"192.0.2.1","server_port":1080}`,
		`{"type":"socks","tag":"hk-B","server":"192.0.2.2","server_port":1080}`,
		`{"type":"socks","tag":"v6","server":"2001:db8::1","server_port":1080}`,
	)
	p.Groups = []profile.Group{{Tag: "hk", Members: []string{"hk-续费提示", "hk-A", "hk-B"}}}
	return p
}

// 提示节点(地址只指向本机,见 builder.HintNodes)不进自动选择:生成的配置干跑通过,auto 与线路组里都没有它,
// 测速前 urltest 先用的第一个成员因此不会是它;proxy 里照样列着,手动选得上。
func TestHintNodesConfigValid(t *testing.T) {
	p := hintProfile(t)
	s := settings.Default()
	s.IPv6 = true // 只看提示节点
	s.Selected = "hk"
	cfg, rep, skipped, err := buildValid(core.New(nil), builder.Input{Profile: p, Settings: s, DataDir: t.TempDir(), ClashSecret: "x"})
	if err != nil || len(skipped) != 0 {
		t.Fatalf("带提示节点的订阅应生成出干跑通过的配置: %v %v", err, skipped)
	}
	if want := []string{"00-勿选-流量:1.0/100.0 重置:3天", "hk-续费提示"}; !reflect.DeepEqual(rep.Hints, want) {
		t.Fatalf("Report.Hints: %v", rep.Hints)
	}
	var c struct {
		Outbounds []struct {
			Tag       string   `json:"tag"`
			Outbounds []string `json:"outbounds"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(cfg, &c); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, o := range c.Outbounds {
		got[o.Tag] = o.Outbounds
	}
	if !reflect.DeepEqual(got["auto"], []string{"hk-A", "hk-B", "v6"}) || !reflect.DeepEqual(got["hk"], []string{"hk-A", "hk-B"}) {
		t.Fatalf("自动选择与线路组里不该有提示节点: auto=%v hk=%v", got["auto"], got["hk"])
	}
	if len(got["proxy"]) != 1+1+len(p.Tags) {
		t.Fatalf("proxy 里节点一个不少: %v", got["proxy"])
	}
}

// 关掉「线路自动」(内核没跑)固定到组里第一台:组里排第一的是提示节点时跳过它,和连不上的 IPv6 节点一样。
func TestSetLineAutoSkipsHintNodes(t *testing.T) {
	d := lineDaemon(t)
	d.profiles["p1"] = hintProfile(t)
	d.settings.Selected, d.settings.LineAuto = "hk", true
	wantHints := []string{"00-勿选-流量:1.0/100.0 重置:3天", "hk-续费提示"}
	wantV6 := []string{"v6"} // 关着 IPv6,写成 IPv6 地址的不用(不分平台)
	if hints, v6 := d.autoSkippedNow(d.getSettings()); !reflect.DeepEqual(hints, wantHints) || !reflect.DeepEqual(v6, wantV6) {
		t.Fatalf("自动选择不用的节点: 提示节点 %v、IPv6 节点 %v,应 %v、%v", hints, v6, wantHints, wantV6)
	}
	raw, _ := json.Marshal(map[string]bool{"on": false})
	res, err := d.Dispatch(ipc.MSetLineAuto, raw)
	if err != nil {
		t.Fatal(err)
	}
	if v := res.(ipc.StateView); v.Settings.Selected != "hk-A" || v.Settings.LineAuto {
		t.Fatalf("关掉线路自动应固定到 hk-A(跳过排第一的提示节点): %q %v", v.Settings.Selected, v.Settings.LineAuto)
	}
}

// 列表测速不测提示节点(地址只指向本机,见 builder.HintNodes):一个包都不发,结果和状态里都没有它们的数,界面按
// StateView.Hints 标成「提示」。提示节点指向一个真在听的回环端口 —— 要是还测,握手会落到它上面、量出一两毫秒排到最快。
// 只有提示节点的订阅(到期 / 用尽后有的面板只给一条提示)测速也不报错,结果是空的。
func TestProbeNodesSkipsHintNodes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	// 提示节点写成未指定地址(不看端口,见 builder.HintNodes):Linux 上连 0.0.0.0 就是连本机,要是测了,会落到这个回环端口上
	port := itoa(ln.Addr().(*net.TCPAddr).Port)
	hint1 := `{"type":"shadowsocks","tag":"00-勿选-流量:1.0/100.0 重置:3天","server":"0.0.0.0","server_port":` + port + `,"method":"aes-128-gcm","password":"dummy"}`
	hint2 := `{"type":"trojan","tag":"续费提示","server":"0.0.0.0","server_port":` + port + `,"password":"p"}`
	// 真节点故意不写服务器地址:probeDirect 对它不发包、直接记 -1(测试自己不出网),测没测到它看结果就知道
	real := `{"type":"vless","tag":"香港1","uuid":"x"}`
	d := newPolicyTestDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	d.profiles["p1"] = prof(t, hint1, real, hint2)

	res, err := d.server.Dispatch(ipc.MProbeNodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := res.(map[string]int); !ok || !reflect.DeepEqual(m, map[string]int{"香港1": -1}) {
		t.Fatalf("测速结果里只该有真节点: %v", res)
	}
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Fatalf("提示节点不该测,它指向的本机端口却被连了 %d 次", n)
	}
	v := d.stateView()
	if !reflect.DeepEqual(v.Delays, map[string]int{"香港1": -1}) {
		t.Fatalf("状态里的测速结果也只该有真节点(界面靠它显示): %v", v.Delays)
	}
	if want := []string{"00-勿选-流量:1.0/100.0 重置:3天", "续费提示"}; !reflect.DeepEqual(v.Hints, want) || v.SelectedHint {
		t.Fatalf("状态里要按订阅里的顺序报出提示节点,没选它们时 SelectedHint 为假: %v %v", v.Hints, v.SelectedHint)
	}

	d.profiles["p1"] = prof(t, hint1, hint2)
	res, err = d.server.Dispatch(ipc.MProbeNodes, nil)
	if m, ok := res.(map[string]int); err != nil || !ok || len(m) != 0 {
		t.Fatalf("只有提示节点时一个都不测、也不报错: %v %v", res, err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Fatalf("提示节点不该测,本机端口却被连了 %d 次", n)
	}
}

// 手动选提示节点和以前一样选得上;状态里带 SelectedHint,界面据此说一句它不能上网。成员全是提示节点的线路组同样算;
// 组里混着真节点、选真节点或自动选择都不算。
func TestSelectHintNodeFlagged(t *testing.T) {
	d := lineDaemon(t)
	p := hintProfile(t)
	p.Groups = append(p.Groups, profile.Group{Tag: "续费", Members: []string{"00-勿选-流量:1.0/100.0 重置:3天", "hk-续费提示"}})
	d.profiles["p1"] = p
	for _, c := range []struct {
		tag  string
		want bool
	}{{"hk-续费提示", true}, {"hk-A", false}, {"续费", true}, {"hk", false}, {"00-勿选-流量:1.0/100.0 重置:3天", true}, {"auto", false}} {
		raw, _ := json.Marshal(map[string]string{"tag": c.tag})
		res, err := d.Dispatch(ipc.MSelectNode, raw)
		if err != nil {
			t.Fatalf("选 %q: %v", c.tag, err)
		}
		v := res.(ipc.StateView)
		if v.SelectedHint != c.want {
			t.Fatalf("选 %q:SelectedHint = %v,应 %v", c.tag, v.SelectedHint, c.want)
		}
		if want := map[bool]string{true: "", false: c.tag}[c.tag == "auto"]; v.Settings.Selected != want {
			t.Fatalf("选 %q 应照旧选上: %q", c.tag, v.Settings.Selected)
		}
	}
}

// 选了线路组时记下的「线路组用不了时用哪台」(SelectedNode,退回旧版本时磁盘上 selected 写的就是它)同样跳过提示节点:
// 组里排第一的是「续费提示」时记 hk-A,不记一个怎么都连不上的;只剩提示节点和 IPv6 节点时记 IPv6 节点。
func TestFillSelectedNodeSkipsHintNodes(t *testing.T) {
	p := hintProfile(t)
	s := settings.Default()
	s.Selected = "hk"
	fillSelectedNode(&s, settings.Default(), p, nil)
	if s.SelectedNode != "hk-A" {
		t.Fatalf("记的应是 hk-A(跳过排第一的提示节点): %q", s.SelectedNode)
	}
	p.Groups = []profile.Group{{Tag: "hk", Members: []string{"hk-续费提示", "v6"}}}
	s.SelectedNode = ""
	fillSelectedNode(&s, settings.Default(), p, []string{"v6"})
	if s.SelectedNode != "v6" {
		t.Fatalf("只剩提示节点和 IPv6 节点时记 IPv6 节点: %q", s.SelectedNode)
	}
	if got := pickMember([]string{"a", "b"}, []string{"a", "b"}, nil); got != "a" {
		t.Fatalf("全是提示节点时取第一台: %q", got)
	}
	if got := pickMember(nil, nil, nil); got != "" {
		t.Fatalf("空的组: %q", got)
	}
}

// 写着本机地址、却不是提示节点形状的(本机的 socks 代理、回环高端口上的本机转发)照常测速:列表里有它的数,
// 状态里不报成提示节点。以前凡是回环地址都当提示节点,接本机另一个代理的人选它会被说成"不能上网"、测速也不测。
func TestProbeLocalChainProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	port := itoa(ln.Addr().(*net.TCPAddr).Port)
	d := newPolicyTestDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	d.settings.NoExitLookup = true
	d.profiles["p1"] = prof(t,
		`{"type":"socks","tag":"本机代理","server":"127.0.0.1","server_port":`+port+`}`,
		`{"type":"trojan","tag":"本机转发","server":"127.0.0.1","server_port":`+port+`,"password":"p"}`,
	)
	res, err := d.server.Dispatch(ipc.MProbeNodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.(map[string]int)
	if _, ok := m["本机代理"]; !ok {
		t.Fatalf("本机的 socks 代理要测: %v", res)
	}
	if _, ok := m["本机转发"]; !ok {
		t.Fatalf("回环高端口上的本机转发要测: %v", res)
	}
	if accepted.Load() == 0 {
		t.Fatal("测速应真连到本机端口")
	}
	if v := d.stateView(); len(v.Hints) != 0 {
		t.Fatalf("它们不是提示节点: %v", v.Hints)
	}
}
