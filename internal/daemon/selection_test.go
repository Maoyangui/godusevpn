package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// oldSettingsFile 旧版本(v0.7.17 及更早)读这份设置文件看到的 selected:同一个 schema,不认识的键(lineAuto、selectedGroup)一律忽略。
func oldSettingsFile(t *testing.T) (schema int, selected string) {
	t.Helper()
	raw, err := os.ReadFile(paths.Settings())
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Schema   int    `json:"schema"`
		Selected string `json:"selected"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	return old.Schema, old.Selected
}

// 选了线路组之后退回旧版本:旧版拿 selected 当节点,得是组里的一台(它就按手动选了这台连,线路不变),
// 而不是旧版不认识的组名(那会回落自动选择、还把它当手动选了节点,定时测速和离开直连时的现测都停掉)。
// 新版读回来照旧是线路组。
func TestSelectionStoredForOldVersions(t *testing.T) {
	d := lineDaemon(t)
	sel := func(tag string) {
		t.Helper()
		if err := call(t, d, ipc.MSelectNode, map[string]string{"tag": tag}); err != nil {
			t.Fatal(err)
		}
	}
	p := lineProfile(t)
	sel("hk")
	schema, old := oldSettingsFile(t)
	if schema != settings.Schema || builder.DefaultSelected(p.Tags, old) != "hk-A" {
		t.Fatalf("选线路组时旧版读到的应是组里的一台(没连接:第一台): schema=%d selected=%q", schema, old)
	}
	if s, err := settings.Load(paths.Settings()); err != nil || s.Selected != "hk" || s.SelectedNode != "hk-A" {
		t.Fatalf("新版读回来还是线路组: %q %q %v", s.Selected, s.SelectedNode, err)
	}
	if s := d.getSettings(); s.Selected != "hk" || !s.LineAuto {
		t.Fatalf("内存里、控制口上照旧是线路组: %+v", s.Selected)
	}
	// 第一台这一轮连不上(关着 IPv6 时的 IPv6 节点):旧版那一台写第二台
	d.profiles["p1"].Outbounds[0] = json.RawMessage(`{"type":"socks","tag":"hk-A","server":"2001:db8::1","server_port":1}`)
	sel("jp")
	sel("hk")
	if _, old = oldSettingsFile(t); old != "hk-B" {
		t.Fatalf("第一台连不上时写能用的那台: %q", old)
	}
	// 从组里的某一台打开「线路自动」:旧版那一台就是用户原来在用的这台
	sel("jp-B")
	raw, _ := json.Marshal(map[string]bool{"on": true})
	if _, err := d.Dispatch(ipc.MSetLineAuto, raw); err != nil {
		t.Fatal(err)
	}
	if _, old = oldSettingsFile(t); old != "jp-B" || d.getSettings().Selected != "jp" {
		t.Fatalf("打开线路自动:旧版那一台是原来在用的: %q / %q", old, d.getSettings().Selected)
	}
	// 选回节点 / 自动选择:原样写,不再带组名
	for _, tag := range []string{"us", "auto"} {
		sel(tag)
		b, _ := os.ReadFile(paths.Settings())
		if strings.Contains(string(b), "selectedGroup") {
			t.Fatalf("选 %s 之后不该再有 selectedGroup: %s", tag, b)
		}
	}
	// 保存整份设置(设置页拿着旧快照,经控制口过来的设置里没有那一台):选择没变就沿用记着的那台,不换成第一台
	sel("jp-B")
	raw, _ = json.Marshal(map[string]bool{"on": true})
	if _, err := d.Dispatch(ipc.MSetLineAuto, raw); err != nil {
		t.Fatal(err)
	}
	next := d.getSettings()
	next.LogDays = 3
	raw, _ = json.Marshal(next)
	if _, err := d.Dispatch(ipc.MSetSettings, raw); err != nil {
		t.Fatal(err)
	}
	if _, old = oldSettingsFile(t); old != "jp-B" || d.getSettings().LogDays != 3 {
		t.Fatalf("保存设置后旧版那一台还是原来记的: %q", old)
	}
}

// 选中的线路组从订阅里消失了(线路只剩一台服务器:服务器下线、入口停用、只分到一台):落到同一线路剩下的那台,
// 不回落全局自动选择(那会跨线路换出口、换倍率);确实找不到同线路的才回落自动选择,两种情况界面都写明。
// 线路组回来了就自动回到线路自动。
func TestVanishedLineGroupKeepsLine(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body.Load().(string))) }))
	defer srv.Close()
	node := func(tag string, port int) string {
		return `{"type":"socks","tag":"` + tag + `","server":"127.0.0.1","server_port":` + itoa(port) + `}`
	}
	group := func(tag string, members ...string) string {
		return `{"type":"urltest","tag":"` + tag + `","outbounds":["` + strings.Join(members, `","`) + `"]}`
	}
	sub := func(outs ...string) string { return `{"outbounds":[` + strings.Join(outs, ",") + `]}` }
	both := sub(node("hk-A", 1), node("hk-B", 2), node("jp-A", 3), node("jp-B", 4), node("us", 5), group("hk", "hk-A", "hk-B"), group("jp", "jp-A", "jp-B"))
	onlyA := sub(node("hk-A", 1), node("jp-A", 3), node("jp-B", 4), node("us", 5), group("jp", "jp-A", "jp-B"))
	noHK := sub(node("jp-A", 3), node("jp-B", 4), node("us", 5), group("jp", "jp-A", "jp-B"))

	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: srv.URL + "/sub"}}
	d.settings.ActiveProfile = "p1"
	refresh := func(b string) {
		t.Helper()
		body.Store(b)
		if _, err := d.refreshProfile(context.Background(), "p1"); err != nil {
			t.Fatal(err)
		}
	}
	refresh(both)
	if err := call(t, d, ipc.MSelectNode, map[string]string{"tag": "hk-B"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]bool{"on": true})
	if _, err := d.Dispatch(ipc.MSetLineAuto, raw); err != nil { // 在 hk-B 上打开线路自动:选的是 hk 线路
		t.Fatal(err)
	}
	if s := d.getSettings(); s.Selected != "hk" || !s.LineAuto {
		t.Fatalf("前提: %+v", s.Selected)
	}

	// 台湾那台入口停了:订阅里只剩 hk-A,没有 hk 组。记着的 hk-B 也没了 → 同线路剩下的 hk-A
	refresh(onlyA)
	s := d.getSettings()
	_, p := d.activeProfile()
	if s.Selected != "hk" || !s.LineAuto || builder.Effective(p, s) != "hk-A" {
		t.Fatalf("线路只剩一台:落到同线路剩下的 hk-A,不回落自动选择: 选 %q,实际 %q", s.Selected, builder.Effective(p, s))
	}
	v := d.stateView()
	if v.Node != "hk-A" || v.SelNote == nil || v.SelNote.Gone != "hk" || v.SelNote.Now != "hk-A" || !v.SelNote.Group {
		t.Fatalf("状态里写明线路只剩一台、现在用的是哪台: node=%q note=%+v", v.Node, v.SelNote)
	}
	if _, old := oldSettingsFile(t); old != "hk-A" {
		t.Fatalf("旧版那一台跟着换成还在的: %q", old)
	}

	// 这条线路在订阅里一台都不剩了:才回落自动选择,同样写明
	refresh(noHK)
	s = d.getSettings()
	_, p = d.activeProfile()
	v = d.stateView()
	if builder.Effective(p, s) != "auto" || v.Node != "auto" || v.SelNote == nil || v.SelNote.Gone != "hk" || v.SelNote.Now != "" {
		t.Fatalf("找不到同线路的才回落自动选择并写明: %q %+v", builder.Effective(p, s), v.SelNote)
	}

	// 面板恢复了两台:线路组回来,自动回到线路自动,提示消失
	refresh(both)
	s = d.getSettings()
	_, p = d.activeProfile()
	if builder.Effective(p, s) != "hk" || d.stateView().SelNote != nil {
		t.Fatalf("线路组回来了就回到线路自动: %q", builder.Effective(p, s))
	}
	// 关掉「线路自动」时组还没回来:固定到正在用的那台(同线路剩下的),不是组名
	refresh(onlyA)
	raw, _ = json.Marshal(map[string]bool{"on": false})
	if _, err := d.Dispatch(ipc.MSetLineAuto, raw); err != nil {
		t.Fatal(err)
	}
	if s := d.getSettings(); s.Selected != "hk-A" || s.LineAuto {
		t.Fatalf("关掉线路自动:固定到正在用的那台: %q", s.Selected)
	}
	// 选的单个节点被面板删了:回落自动选择,同样写明(不是线路)
	refresh(both)
	if err := call(t, d, ipc.MSelectNode, map[string]string{"tag": "hk-B"}); err != nil {
		t.Fatal(err)
	}
	refresh(onlyA)
	if v := d.stateView(); v.Node != "auto" || v.SelNote == nil || v.SelNote.Gone != "hk-B" || v.SelNote.Group {
		t.Fatalf("选的节点没了:回落自动选择并写明: %q %+v", v.Node, v.SelNote)
	}
}

// 旧版本存的订阅缓存(只是没认出线路组)的补拉:只经隧道拉一次。拉不到不强测全部节点、不在隧道外直连面板,
// 也不每 10 分钟重来 —— 每次连接只补一次(为了认出线路组这种可有可无的东西,不值得付出全量测速和隧道外直连)。
func TestOutdatedRefetchTunnelOnlyOnce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // 面板这会儿拉不到
	}))
	defer srv.Close()
	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: srv.URL + "/sub"}}
	d.settings.ActiveProfile = "p1"
	d.settings.Selected = "香港1" // 手动选了节点:原来的回退链这时还会强测 auto
	old := prof(t, `{"type":"anytls","tag":"香港1","server":"192.0.2.1","server_port":8443,"password":"p"}`)
	old.URL, old.FetchedAt = srv.URL+"/sub", time.Now().Unix() // 刚拉过,只是旧版本存的
	d.profiles["p1"] = old
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["out"]},{"type":"direct","tag":"out"}`)
	d.maybeRefresh(context.Background())
	if n := hits.Load(); n != 1 {
		t.Fatalf("只经隧道拉一次(不走直连那一跳): 拉了 %d 次", n)
	}
	d.maybeRefresh(context.Background()) // 10 分钟后定时那一轮
	if n := hits.Load(); n != 1 {
		t.Fatalf("同一次连接里补过了就不再补: 拉了 %d 次", n)
	}
	d.connGen.Add(1) // 重新连上了
	d.maybeRefresh(context.Background())
	if n := hits.Load(); n != 2 {
		t.Fatalf("下一次连接再补一次: 拉了 %d 次", n)
	}
	// 真过期了照旧走完整的回退链(当前代理 → auto → 直连)
	d.profiles["p1"].FetchedAt = time.Now().Add(-48 * time.Hour).Unix()
	d.maybeRefresh(context.Background())
	if n := hits.Load(); n < 4 {
		t.Fatalf("过期的照旧走完整回退链: 拉了 %d 次", n)
	}
}

// 翻「线路自动」/ 选节点要重建配置、而新配置备不出来(Prepare 失败,旧内核照跑):报错「切换失败,保持当前连接」,
// 落盘的选择也退回去 —— 不然界面把开关拨回去了,设置里却已经是新选择:下一次重连悄悄按它连,再拨一次开关又成了空操作。
func TestSelectionRollsBackWhenSwitchFails(t *testing.T) {
	d := lineDaemon(t)
	d.settings.Selected, d.settings.NoExitLookup = "hk-B", true
	d.machine = state.New(state.Deps{ // 想连着、但怎么都备不出配置的状态机(不碰任何系统状态)
		Prepare: func(context.Context) ([]byte, error) { return nil, errors.New("备不出配置") },
		Start:   func([]byte) error { return nil },
		Stop:    func() error { return nil },
		Logf:    d.logf,
		Backoff: []time.Duration{time.Hour},
	})
	d.machine.Connect()
	defer d.machine.Disconnect()
	startCore(t, d, lineOutbounds("hk-B", false, "http://127.0.0.1:1/")) // 内核里还没有线路组:选线路组要重建
	raw, _ := json.Marshal(map[string]bool{"on": true})
	if _, err := d.Dispatch(ipc.MSetLineAuto, raw); err == nil {
		t.Fatal("新配置备不出来应当报错")
	}
	if s := d.getSettings(); s.Selected != "hk-B" || s.LineAuto {
		t.Fatalf("开关失败:选择和开关都退回去,和正在跑的对得上: %q %v", s.Selected, s.LineAuto)
	}
	if got, err := settings.Load(paths.Settings()); err != nil || got.Selected != "hk-B" || got.LineAuto {
		t.Fatalf("落盘的也退回去: %q %v %v", got.Selected, got.LineAuto, err)
	}
	if err := call(t, d, ipc.MSelectNode, map[string]string{"tag": "hk"}); err == nil {
		t.Fatal("选线路组同样要重建,备不出来应当报错")
	}
	if s := d.getSettings(); s.Selected != "hk-B" || s.LineAuto {
		t.Fatalf("选节点失败也退回去: %q %v", s.Selected, s.LineAuto)
	}
	next := d.getSettings()
	next.Selected, next.LogDays = "hk", 5
	raw, _ = json.Marshal(next)
	if _, err := d.Dispatch(ipc.MSetSettings, raw); err == nil {
		t.Fatal("保存设置时改的选择要重建,备不出来应当报错")
	}
	if s := d.getSettings(); s.Selected != "hk-B" || s.LogDays != 5 {
		t.Fatalf("选择退回去,别的设置照存: %q %d", s.Selected, s.LogDays)
	}
}
