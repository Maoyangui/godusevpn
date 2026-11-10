package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// lineProfile 两条线路各部署在两台服务器上(m-ui 的订阅就是这样),外加一个独立节点。地址都是连不上的本机端口。
func lineProfile(t *testing.T) *profile.Profile {
	t.Helper()
	p := prof(t,
		`{"type":"socks","tag":"hk-A","server":"127.0.0.1","server_port":1}`,
		`{"type":"socks","tag":"hk-B","server":"127.0.0.1","server_port":2}`,
		`{"type":"socks","tag":"jp-A","server":"127.0.0.1","server_port":3}`,
		`{"type":"socks","tag":"jp-B","server":"127.0.0.1","server_port":4}`,
		`{"type":"socks","tag":"us","server":"127.0.0.1","server_port":5}`,
	)
	p.Groups = []profile.Group{{Tag: "hk", Members: []string{"hk-A", "hk-B"}}, {Tag: "jp", Members: []string{"jp-A", "jp-B"}}}
	return p
}

// lineDaemon 能存设置的测试守护进程,当前订阅是 lineProfile;内核没在跑。
func lineDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	d.profiles["p1"] = lineProfile(t)
	return d
}

// startCore 给测试守护进程起一个只有出站的内核(不碰路由、不监听入站)。
func startCore(t *testing.T, d *Daemon, outbounds string) {
	t.Helper()
	cache := filepath.ToSlash(filepath.Join(t.TempDir(), "cache.db"))
	cfg := `{"log":{"level":"error"},"outbounds":[` + outbounds + `,{"type":"direct","tag":"direct"}],
"route":{"final":"proxy"},"experimental":{"clash_api":{"default_mode":"Rule"},"cache_file":{"enabled":true,"path":"` + cache + `"}}}`
	if err := d.core.Start([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.core.Stop() })
}

// liveURL 本机回环上的测速地址:慢 20 毫秒,测得通的成员测出来的延迟不会是 0(0 在换线时当"没测出来")。
func liveURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// waitTested 等测速组真测完一轮(组里得有测得通的成员,真测了才有结果)。sing-box 的测速组读当前项(Now)和它自己测完
// 换选择之间没有同步:测试里读当前项之前先这样和它启动时那一轮对上(GroupTest 起头的原子标记),-race 才不会报 sing-box 内部的竞争。
func waitTested(t *testing.T, d *Daemon, group string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		res, err := d.core.GroupTest(ctx, group)
		cancel()
		if err == nil && len(res) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("测速组 %s 一直没测出结果", group)
}

// lineOutbounds lineProfile 对应的内核出站;withGroups 时按选中线路组生成的样子放进 hk / jp 两组。
func lineOutbounds(def string, withGroups bool, url string) string {
	grp := func(tag, members string) string {
		return `{"type":"urltest","tag":"` + tag + `","outbounds":[` + members + `],"url":"` + url + `","interval":"24h","idle_timeout":"25h"}`
	}
	list := `"auto","hk-A","hk-B","jp-A","jp-B","us"`
	extra := ""
	if withGroups {
		list = `"auto","hk","jp","hk-A","hk-B","jp-A","jp-B","us"`
		extra = grp("hk", `"hk-A","hk-B"`) + "," + grp("jp", `"jp-A","jp-B"`) + ","
	}
	return `{"type":"selector","tag":"proxy","outbounds":[` + list + `],"default":"` + def + `"},` +
		grp("auto", `"hk-A","hk-B","jp-A","jp-B","us"`) + "," + extra +
		`{"type":"socks","tag":"hk-A","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"hk-B","server":"127.0.0.1","server_port":2},` +
		`{"type":"socks","tag":"jp-A","server":"127.0.0.1","server_port":3},{"type":"socks","tag":"jp-B","server":"127.0.0.1","server_port":4},` +
		`{"type":"socks","tag":"us","server":"127.0.0.1","server_port":5}`
}

// 启动时哪些节点预记"刚测过"(不测):直连模式、手动节点 → 全部;自动选择 → 一个不留;线路组 → 除了这一组的成员。
func TestQuietTags(t *testing.T) {
	p := lineProfile(t)
	for _, c := range []struct {
		name, sel string
		direct    bool
		want      []string
	}{
		{"自动选择", "auto", false, nil},
		{"自动选择(空)", "", false, nil},
		{"手动节点", "us", false, p.Tags},
		{"线路组", "hk", false, []string{"jp-A", "jp-B", "us"}},
		{"直连模式", "hk", true, p.Tags},
		{"不在订阅里的选择", "旧线路", false, p.Tags},
	} {
		if got := quietTags(p, c.sel, c.direct); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s:得 %v,应 %v", c.name, got, c.want)
		}
	}
	if quietTags(nil, "hk", false) != nil {
		t.Fatal("没有订阅时照常测")
	}
	// start() 按 SelectedFor 认线路组、按 quietTags 只放开要用的那几台;不能再只认节点
	start := funcBody(t, readDaemonSource(t), "func (d *Daemon) start(cfg []byte) error {")
	if !strings.Contains(start, "builder.SelectedFor(") || !strings.Contains(start, "quietTags(") || strings.Contains(start, "DefaultSelected(") {
		t.Fatal("start() 应按 SelectedFor 对齐缓存、按 quietTags 安静启动")
	}
}

// 「线路自动」的不变式与翻转时的换算。
func TestLineAutoConversions(t *testing.T) {
	p := lineProfile(t)
	for _, c := range []struct {
		sel       string
		cur, want bool
	}{
		{"hk", false, true},   // 选了线路组 → 开
		{"hk-B", true, false}, // 选了组里的某一台 → 关
		{"us", true, true},    // 独立节点 → 不改
		{"us", false, false},
		{"", true, true}, // 自动选择 → 不改
	} {
		if got := lineAutoAfterSelect(p, c.sel, c.cur); got != c.want {
			t.Fatalf("选 %q(原来 %v)之后应为 %v", c.sel, c.cur, c.want)
		}
	}
	now := func(g string) string { return map[string]string{"hk": "hk-B"}[g] }
	for _, c := range []struct {
		sel     string
		on      bool
		blocked []string
		want    string
	}{
		{"hk-B", true, nil, "hk"},               // 打开:组里的节点 → 它所在的线路组
		{"us", true, nil, "us"},                 // 打开:独立节点不变
		{"", true, nil, ""},                     // 打开:自动选择不变
		{"hk", false, nil, "hk-B"},              // 关闭:固定到组里此刻在用的那台
		{"jp", false, nil, "jp-A"},              // 关闭:内核里查不到 → 第一台
		{"jp", false, []string{"jp-A"}, "jp-B"}, // 关闭:第一台这一轮连不上(IPv6 节点)→ 下一台
		{"jp", false, []string{"jp-A", "jp-B"}, "jp-A"},
		{"us", false, nil, "us"},
	} {
		if got := lineAutoSelection(p, c.sel, c.on, now, c.blocked); got != c.want {
			t.Fatalf("选 %q 时把开关拨到 %v:得 %q,应 %q", c.sel, c.on, got, c.want)
		}
	}
	// 内核里那一台已经不在组里(订阅刷新后换了):取第一台
	if got := lineAutoSelection(p, "hk", false, func(string) string { return "已删" }, nil); got != "hk-A" {
		t.Fatalf("得 %q", got)
	}
}

// 选节点维护「线路自动」:选线路组 → 开,选组里的节点 → 关,选自动选择、独立节点 → 不动。内核没跑时只改设置。
func TestSelectNodeMaintainsLineAuto(t *testing.T) {
	d := lineDaemon(t)
	sel := func(tag string) settings.Settings {
		t.Helper()
		if err := call(t, d, ipc.MSelectNode, map[string]string{"tag": tag}); err != nil {
			t.Fatal(err)
		}
		return d.getSettings()
	}
	if s := sel("jp"); s.Selected != "jp" || !s.LineAuto {
		t.Fatalf("选了线路组:%+v", s)
	}
	if s := sel("auto"); s.Selected != "" || !s.LineAuto {
		t.Fatalf("选自动选择不动开关:%q %v", s.Selected, s.LineAuto)
	}
	if s := sel("us"); s.Selected != "us" || !s.LineAuto {
		t.Fatalf("选独立节点不动开关:%q %v", s.Selected, s.LineAuto)
	}
	if s := sel("hk-A"); s.Selected != "hk-A" || s.LineAuto {
		t.Fatalf("选了组里的某一台,开关关掉:%q %v", s.Selected, s.LineAuto)
	}
	if v := d.stateView(); v.Node != "hk-A" || len(v.Groups) != 2 || v.Groups[0].Tag != "hk" {
		t.Fatalf("状态里带着当前订阅的线路组: %+v / %+v", v.Node, v.Groups)
	}
}

// 翻「线路自动」顺带换算当前选择(内核没跑):打开时组里的节点换成线路组;关掉时线路组固定到第一台。
// 保存设置不改它(设置页拿着打开时的旧快照整份保存)。
func TestSetLineAuto(t *testing.T) {
	d := lineDaemon(t)
	d.settings.Selected = "hk-B"
	flip := func(on bool) ipc.StateView {
		t.Helper()
		raw, _ := json.Marshal(map[string]bool{"on": on})
		res, err := d.Dispatch(ipc.MSetLineAuto, raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.(ipc.StateView)
	}
	if v := flip(true); v.Settings.Selected != "hk" || !v.Settings.LineAuto || v.Node != "hk" {
		t.Fatalf("打开:应换成所在的线路组: %+v", v.Settings)
	}
	if v := flip(false); v.Settings.Selected != "hk-A" || v.Settings.LineAuto {
		t.Fatalf("关掉(内核没跑):固定到第一台: %+v", v.Settings)
	}
	// 保存设置沿用原值
	d.settings.LineAuto = true
	next := d.getSettings()
	next.LineAuto = false
	next.LogDays = 9
	raw, _ := json.Marshal(next)
	if _, err := d.Dispatch(ipc.MSetSettings, raw); err != nil {
		t.Fatal(err)
	}
	if s := d.getSettings(); !s.LineAuto || s.LogDays != 9 {
		t.Fatalf("保存设置不该改「线路自动」: %+v", s.LineAuto)
	}
}

// 内核在跑时关掉「线路自动」:固定到线路组里此刻在用的那台(不是第一台),就地换,不重连。
func TestSetLineAutoPinsMemberInUse(t *testing.T) {
	d := lineDaemon(t)
	d.settings.Selected, d.settings.LineAuto = "hk", true
	url := liveURL(t)
	grp := func(tag, members string) string {
		return `{"type":"urltest","tag":"` + tag + `","outbounds":[` + members + `],"url":"` + url + `","interval":"24h","idle_timeout":"25h"}`
	}
	// hk 组里测得通的只有第二台 hk-B:组里此刻在用的就是它
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","hk","jp","hk-A","hk-B","jp-A","jp-B","us"],"default":"hk"},`+
		grp("auto", `"hk-A","hk-B","jp-A","jp-B","us"`)+","+grp("hk", `"hk-A","hk-B"`)+","+grp("jp", `"jp-A","jp-B"`)+","+
		`{"type":"socks","tag":"hk-A","server":"127.0.0.1","server_port":1},{"type":"direct","tag":"hk-B"},`+
		`{"type":"socks","tag":"jp-A","server":"127.0.0.1","server_port":3},{"type":"socks","tag":"jp-B","server":"127.0.0.1","server_port":4},`+
		`{"type":"socks","tag":"us","server":"127.0.0.1","server_port":5}`)
	waitTested(t, d, "hk")
	if got := d.groupNow("hk"); got != "hk-B" {
		t.Fatalf("前提:组里选中的应是 hk-B,得 %q", got)
	}
	raw, _ := json.Marshal(map[string]bool{"on": false})
	if _, err := d.Dispatch(ipc.MSetLineAuto, raw); err != nil {
		t.Fatal(err)
	}
	if s := d.getSettings(); s.Selected != "hk-B" || s.LineAuto {
		t.Fatalf("应固定到组里此刻在用的那台: %+v", s.Selected)
	}
	if now, _, _ := d.core.Group("proxy"); now != "hk-B" {
		t.Fatalf("就地换过去: %q", now)
	}
}

// 能不能就地换到线路组:内核里没有(这次连接没选过线路组)要重建;有、而且成员和参数都和最新订阅一样才能就地换。
func TestNeedRebuildForLineGroup(t *testing.T) {
	d := lineDaemon(t)
	startCore(t, d, lineOutbounds("auto", false, "http://127.0.0.1:1/"))
	if !d.needRebuildFor("hk") {
		t.Fatal("内核里没有线路组时选它要重建配置")
	}
	_ = d.core.Stop()
	startCore(t, d, lineOutbounds("hk", true, "http://127.0.0.1:1/"))
	d.running = lineProfile(t)
	if d.needRebuildFor("jp") || d.needRebuildFor("hk") {
		t.Fatal("内核里有、成员与参数都没变:就地换")
	}
	// 订阅刷新后 jp 多了一台
	fresh := lineProfile(t)
	fresh.Groups[1].Members = append(fresh.Groups[1].Members, "us")
	d.profiles["p1"] = fresh
	if !d.needRebuildFor("jp") || d.needRebuildFor("hk") {
		t.Fatal("成员变了的那一组要重建,另一组不用")
	}
	// hk 的一台改了参数
	fresh = lineProfile(t)
	fresh.Outbounds[1] = json.RawMessage(`{"type":"socks","tag":"hk-B","server":"127.0.0.1","server_port":9}`)
	d.profiles["p1"] = fresh
	if !d.needRebuildFor("hk") || d.needRebuildFor("jp") {
		t.Fatal("组里某一台参数变了要重建")
	}
}

// 定时测速 / 切过去之后的现测测哪一组:按内核里 proxy 实际落在哪 —— 自动选择测 auto,线路组只测这一组,单个节点不测。
func TestProbeTarget(t *testing.T) {
	d := lineDaemon(t)
	if d.probeTarget() != "" {
		t.Fatal("内核没跑时不测")
	}
	startCore(t, d, lineOutbounds("auto", true, "http://127.0.0.1:1/"))
	for sel, want := range map[string]string{"auto": "auto", "jp": "jp", "us": "", "hk-A": ""} {
		if err := d.core.Select("proxy", sel); err != nil {
			t.Fatal(err)
		}
		if got := d.probeTarget(); got != want {
			t.Fatalf("proxy 在 %s 上:测 %q,应 %q", sel, got, want)
		}
	}
	loop := funcBody(t, readDaemonSource(t), "func (d *Daemon) autoProbeLoop(ctx context.Context) {")
	if !strings.Contains(loop, "d.probeTarget()") || strings.Contains(loop, "s.Selected != \"\"") {
		t.Fatal("定时测速按 probeTarget 决定测哪一组")
	}
}

// AutoNow 只在 proxy 落在测速组(自动选择、线路组)上时才有:手动选节点时 auto 组带着假历史,它"选中"的那个根本没在用。
func TestStateViewAutoNow(t *testing.T) {
	d := lineDaemon(t)
	url := liveURL(t)
	grp := func(tag, members string) string {
		return `{"type":"urltest","tag":"` + tag + `","outbounds":[` + members + `],"url":"` + url + `","interval":"24h","idle_timeout":"25h"}`
	}
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","jp","jp-A","jp-B","us"],"default":"us"},`+
		grp("auto", `"jp-A","jp-B","us"`)+","+grp("jp", `"jp-A","jp-B"`)+","+
		`{"type":"socks","tag":"jp-A","server":"127.0.0.1","server_port":3},{"type":"direct","tag":"jp-B"},`+
		`{"type":"socks","tag":"us","server":"127.0.0.1","server_port":5}`)
	if v := d.stateView(); v.Node != "us" || v.AutoNow != "" {
		t.Fatalf("手动选节点时不该有 autoNow: %q / %q", v.Node, v.AutoNow)
	}
	waitTested(t, d, "jp")
	_ = d.core.Select("proxy", "jp")
	if v := d.stateView(); v.Node != "jp" || v.AutoNow != "jp-B" {
		t.Fatalf("线路组:autoNow 是组里实际那台: %q / %q", v.Node, v.AutoNow)
	}
	if got := d.currentNode(); got != "jp-B" {
		t.Fatalf("currentNode 也按组里实际那台: %q", got)
	}
	waitTested(t, d, "auto")
	_ = d.core.Select("proxy", "auto")
	if v := d.stateView(); v.AutoNow != "jp-B" {
		t.Fatalf("自动选择:autoNow 是 auto 组选中的那台: %q", v.AutoNow)
	}
}

// 线路组先在组里自救:组里有通的就换过去、留在这条线路上;整组不通才临时借别的线路(组里的成员不算)。设置里的选择不动。
func TestRecoverRouteLineGroup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond) // 测出来的延迟不会是 0(0 在换线时当"没测出来")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	url := srv.URL + "/"
	grp := func(tag, members string) string {
		return `{"type":"urltest","tag":"` + tag + `","outbounds":[` + members + `],"url":"` + url + `","interval":"24h","idle_timeout":"25h"}`
	}
	nodes := `{"type":"socks","tag":"dead1","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"dead2","server":"127.0.0.1","server_port":2},` +
		`{"type":"direct","tag":"live"},{"type":"direct","tag":"other"}`

	t.Run("组里有通的", func(t *testing.T) {
		d := lineDaemon(t)
		d.settings.Selected = "G"
		startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","G","dead1","live","other"],"default":"dead1"},`+
			grp("auto", `"dead1","live","other"`)+","+grp("G", `"dead1","live"`)+","+nodes)
		if !d.recoverRoute(context.Background()) {
			t.Fatal("组里有通的那台,应在组里自救")
		}
		if now, _, _ := d.core.Group("proxy"); now != "G" {
			t.Fatalf("proxy 应回到线路组上(之前被借走了): %q", now)
		}
		if now := d.groupNow("G"); now != "live" {
			t.Fatalf("组里应换到通的那台: %q", now)
		}
		if d.getSettings().Selected != "G" {
			t.Fatal("自救不改设置")
		}
	})
	t.Run("整组不通", func(t *testing.T) {
		d := lineDaemon(t)
		d.settings.Selected = "G"
		startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","G","dead1","dead2","other"],"default":"G"},`+
			grp("auto", `"dead1","dead2","other"`)+","+grp("G", `"dead1","dead2"`)+","+nodes)
		if !d.recoverRoute(context.Background()) {
			t.Fatal("整组不通时应临时借别的线路")
		}
		if now, _, _ := d.core.Group("proxy"); now != "other" {
			t.Fatalf("应借到组外通的那台: %q", now)
		}
		if d.getSettings().Selected != "G" {
			t.Fatal("临时换线不改设置")
		}
	})
}

// 组里正在测的那一轮,sing-box 直接回空结果:等它测完再看,不能凭一个空结果就判整组不通、跨线路换线。
func TestSettledGroupTest(t *testing.T) {
	n := 0
	res, err := settledGroupTest(context.Background(), func(context.Context) (map[string]uint16, error) {
		n++
		if n < 3 {
			return map[string]uint16{}, nil
		}
		return map[string]uint16{"a": 80}, nil
	}, time.Millisecond, 5)
	if err != nil || res["a"] != 80 || n != 3 {
		t.Fatalf("空结果要隔一会儿再测: %v %v 测了 %d 次", res, err, n)
	}
	n = 0
	res, _ = settledGroupTest(context.Background(), func(context.Context) (map[string]uint16, error) {
		n++
		return map[string]uint16{}, nil
	}, time.Millisecond, 3)
	if len(res) != 0 || n != 3 {
		t.Fatalf("真的整组不通:测满次数就认: %v %d", res, n)
	}
	if _, err := settledGroupTest(context.Background(), func(context.Context) (map[string]uint16, error) {
		return nil, errors.New("内核未运行")
	}, time.Millisecond, 3); err == nil {
		t.Fatal("出错直接返回")
	}
}

// 选中线路组时配置照样过得了内核干跑(和生成时一样剔掉建不起来的节点、线路组跟着剪)。
func TestBuildValidWithLineGroup(t *testing.T) {
	p := &profile.Profile{Tags: []string{"hk-A", "hk-B", "nv"}, Outbounds: []json.RawMessage{
		json.RawMessage(`{"type":"hysteria2","tag":"hk-A","server":"1.2.3.4","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
		json.RawMessage(`{"type":"anytls","tag":"hk-B","server":"1.2.3.5","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
		json.RawMessage(`{"type":"naive","tag":"nv","server":"1.2.3.7","server_port":443,"username":"u","password":"p","tls":{"enabled":true,"server_name":"n.example"}}`),
	}, Groups: []profile.Group{{Tag: "hk", Members: []string{"hk-A", "hk-B"}}, {Tag: "坏", Members: []string{"nv", "hk-A"}}}}
	s := settings.Default()
	s.Selected = "hk"
	cfg, _, skipped, err := buildValid(core.New(nil), builder.Input{Profile: p, Settings: s, DataDir: t.TempDir(), ClashSecret: "x"})
	if err != nil {
		t.Fatalf("选中线路组的配置应能过干跑: %v", err)
	}
	if skipped["nv"] == "" || !strings.Contains(string(cfg), `"default": "hk"`) {
		t.Fatalf("剔掉建不起来的节点,proxy 仍落在线路组上: %v\n%s", skipped, cfg)
	}
}

// 手动节点没有线路组:一个一个加的节点,贴进来的 sing-box 配置里就算带着测速组也不认。
func TestManualNodesHaveNoGroups(t *testing.T) {
	p, err := buildManual([]json.RawMessage{
		json.RawMessage(`{"type":"trojan","tag":"a","server":"1.2.3.4","server_port":443,"password":"p"}`),
		json.RawMessage(`{"type":"trojan","tag":"b","server":"1.2.3.5","server_port":443,"password":"p"}`),
		json.RawMessage(`{"type":"trojan","tag":"c","server":"1.2.3.6","server_port":443,"password":"p"}`),
		json.RawMessage(`{"type":"urltest","tag":"g","outbounds":["a","b"]}`),
	})
	if err != nil || len(p.Groups) != 0 || len(p.Tags) != 3 || !p.Manual {
		t.Fatalf("手动节点不该有线路组: %+v %v", p.Groups, err)
	}
}

// 旧版本存的订阅缓存(没有 parser,没认出线路组):连上之后经隧道补拉一次,线路组不用等满刷新间隔;
// 没连上时不为它拉(连上之前直连拉订阅会挡着第一次连接,严格全局下还是一次直连),补过之后就不再拉。
func TestOutdatedCacheRefreshesAfterConnect(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(subBody))
	}))
	defer srv.Close()
	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: srv.URL + "/sub"}}
	d.settings.ActiveProfile = "p1"
	old := prof(t, `{"type":"anytls","tag":"香港1","server":"192.0.2.1","server_port":8443,"password":"p"}`)
	old.URL, old.FetchedAt = srv.URL+"/sub", time.Now().Unix() // 刚拉过,只是旧版本存的
	d.profiles["p1"] = old
	d.maybeRefresh(context.Background())
	if n := hits.Load(); n != 0 {
		t.Fatalf("没连上时不为它拉: %d", n)
	}
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["out"]},{"type":"direct","tag":"out"}`)
	d.maybeRefresh(context.Background())
	if n := hits.Load(); n != 1 {
		t.Fatalf("连上之后补拉一次: %d", n)
	}
	if _, p := d.activeProfile(); p == nil || p.Outdated() {
		t.Fatal("补拉之后就不陈旧了")
	}
	d.maybeRefresh(context.Background())
	if n := hits.Load(); n != 1 {
		t.Fatalf("补过就不再拉: %d", n)
	}
	if strings.Contains(funcBody(t, readDaemonSource(t), "func (d *Daemon) prepare(ctx context.Context) ([]byte, error) {"), "Outdated()") {
		t.Fatal("连上之前不为陈旧的缓存拉订阅")
	}
}
