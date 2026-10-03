package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

const subBody = `{"outbounds":[{"type":"anytls","tag":"香港1","server":"192.0.2.1","server_port":8443,"password":"p"}]}`

// profileOpsDaemon 能存设置的测试守护进程(setSettings 要两份日志);内核不跑,拉订阅走本机回环。
func profileOpsDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newPolicyTestDaemon(t)
	d.coreLog = logx.New(filepath.Join(t.TempDir(), "core.log"), 1<<20, 1)
	t.Cleanup(func() { _ = d.coreLog.Close() })
	d.http = &http.Client{Timeout: 5 * time.Second}
	d.settingsOK = true
	return d
}

func call(t *testing.T, d *Daemon, method string, in any) error {
	t.Helper()
	raw, _ := json.Marshal(in)
	_, err := d.Dispatch(method, raw)
	return err
}

// 改订阅地址:新地址拉不到就什么都不改;拉到了才落盘并换缓存。
// 先存后拉的话,拉不到时新地址已经落盘、缓存还是旧地址的,之后任何一次重建都硬失败。
func TestRenameProfileFetchesBeforeSaving(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/good" {
			_, _ = w.Write([]byte(subBody))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "旧", URL: srv.URL + "/old"}}
	d.settings.ActiveProfile = "p1"

	if err := call(t, d, ipc.MRenameProfile, map[string]string{"id": "p1", "name": "新", "url": srv.URL + "/bad"}); err == nil {
		t.Fatal("新地址拉不到应当报错")
	}
	if p := d.getSettings().Profiles[0]; p.URL != srv.URL+"/old" || p.Name != "旧" {
		t.Fatalf("新地址拉不到时设置不该变,实际 %+v", p)
	}
	if err := call(t, d, ipc.MRenameProfile, map[string]string{"id": "p1", "url": srv.URL + "/good"}); err != nil {
		t.Fatal(err)
	}
	if got := d.getSettings().Profiles[0].URL; got != srv.URL+"/good" {
		t.Fatalf("拉到了就该落盘新地址,实际 %s", got)
	}
	if _, p := d.activeProfile(); p == nil || p.URL != srv.URL+"/good" {
		t.Fatal("拉到了就该换成新地址的缓存")
	}
}

// 切换订阅:新订阅备不出配置时旧内核照跑,设置也要退回去,和正在跑的对得上。
func TestSelectProfileRollsBackOnFailure(t *testing.T) {
	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: "https://a.example/sub"}, {ID: "p2", Name: "二", URL: "https://b.example/sub"}}
	d.settings.ActiveProfile = "p1"
	// 想连着、但怎么都备不出配置的状态机(假的 Prepare,不碰任何系统状态)
	d.machine = state.New(state.Deps{
		Prepare: func(context.Context) ([]byte, error) { return nil, errors.New("拉不到") },
		Start:   func([]byte) error { return nil },
		Stop:    func() error { return nil },
		Logf:    d.logf,
		Backoff: []time.Duration{time.Hour},
	})
	d.machine.Connect()
	defer d.machine.Disconnect()

	if err := call(t, d, ipc.MSelectProfile, map[string]string{"id": "p2"}); err == nil {
		t.Fatal("新订阅备不出配置应当报错")
	}
	if got := d.getSettings().ActiveProfile; got != "p1" {
		t.Fatalf("切换失败要退回原订阅,实际 %s", got)
	}
}

// 同一条订阅正在刷新时再来一次(界面超时后用户重点、定时刷新撞上):等那一次的结果,不另走一遍回退链
// —— 严格全局下每多走一遍,最后一跳就多一次直连。
func TestRefreshProfileCoalesces(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		<-release
		_, _ = w.Write([]byte(subBody))
	}))
	defer srv.Close()
	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: srv.URL + "/sub"}}
	d.settings.ActiveProfile = "p1"

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := d.refreshProfile(context.Background(), "p1"); errs <- err }()
	}
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // 给第二次一点时间:没合并的话它此刻也已经打到服务端了
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("同一条订阅并发刷新打了 %d 次服务端,应合并成 1 次", n)
	}
}

// 设置文件读不出来时手上是默认值,第一次保存会把原文件整份覆盖(订阅、规则组、设备策略全没):覆盖前先另存一份。
func TestSetSettingsPreservesUnreadableFile(t *testing.T) {
	dir := t.TempDir() // Windows 上设置文件在数据目录里,两个都指过来
	t.Setenv("GODUSEVPN_CONF", dir)
	t.Setenv("GODUSEVPN_DATA", dir)
	bad := []byte(`{"schema":4,"profiles":[{"id":"p1","name":"我的订阅","url":"https://sub.example/x"}],"mode":`) // 截断
	if err := os.WriteFile(paths.Settings(), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	d := profileOpsDaemon(t)
	d.settingsOK = false
	if err := d.setSettings(settings.Default()); err != nil {
		t.Fatal(err)
	}
	baks, _ := filepath.Glob(paths.Settings() + ".bad-*")
	if len(baks) != 1 {
		t.Fatalf("覆盖前应当另存一份原文件,找到 %d 份", len(baks))
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != string(bad) {
		t.Fatal("另存的内容和原文件不一样")
	}
	if err := d.setSettings(settings.Default()); err != nil { // 文件已可信:之后的保存不再另存
		t.Fatal(err)
	}
	if baks, _ = filepath.Glob(paths.Settings() + ".bad-*"); len(baks) != 1 {
		t.Fatalf("文件可信之后的保存不该再另存,现在有 %d 份", len(baks))
	}
}

// 界面点的刷新在后台做:立刻返回、这条订阅标成"正在刷新";刷完标记落下 —— 成功没有失败原因,失败带着原因。
// 回退链最长两分多钟,控制管道一次调用 60 秒就断,同步等的话界面报超时而后台还在刷。
func TestRefreshProfileAsync(t *testing.T) {
	release := make(chan struct{})
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		if fail.Load() {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(subBody))
	}))
	defer srv.Close()
	d := profileOpsDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: srv.URL + "/sub"}}
	d.settings.ActiveProfile = "p1"
	view := func() ipc.ProfileView { v, _ := d.profileViews(); return v[0] }
	settle := func() ipc.ProfileView {
		deadline := time.Now().Add(5 * time.Second)
		for view().Refreshing && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		return view()
	}
	raw, _ := json.Marshal(map[string]any{"id": "p1", "async": true})
	began := time.Now()
	res, err := d.Dispatch(ipc.MRefreshProfile, raw)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("后台刷新应立刻返回,不等拉取")
	}
	if v, ok := res.([]ipc.ProfileView); !ok || len(v) != 1 || !v[0].Refreshing {
		t.Fatalf("返回里这条订阅应标成正在刷新: %#v", res)
	}
	close(release)
	if v := settle(); v.Refreshing || v.Error != "" || v.NodeCount != 1 {
		t.Fatalf("刷成功后标记落下、没有失败原因、节点到手: %+v", v)
	}
	fail.Store(true)
	if _, err := d.Dispatch(ipc.MRefreshProfile, raw); err != nil {
		t.Fatal(err)
	}
	if v := settle(); v.Refreshing || v.Error == "" {
		t.Fatalf("刷失败后标记照样落下,并带着原因: %+v", v)
	}
}
