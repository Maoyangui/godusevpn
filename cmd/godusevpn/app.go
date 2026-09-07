package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"

	"github.com/Maoyangui/godusevpn/internal/autostart"
	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/clash"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/svc"
	"github.com/Maoyangui/godusevpn/internal/update"
)

const testURL = "http://www.gstatic.com/generate_204"

// UIState 推给页面与托盘的一份快照:服务在不在、服务给的状态、实时速度、界面偏好。
type UIState struct {
	Service  bool            `json:"service"`  // 控制管道可达
	SvcState string          `json:"svcState"` // running / stopped / not-installed / unknown(服务管理器视角)
	View     ipc.StateView   `json:"view"`
	Up       int64           `json:"up"`   // 字节/秒
	Down     int64           `json:"down"` // 字节/秒
	Lang     string          `json:"lang"`
	Theme    string          `json:"theme"` // system / light / dark
	Version  string          `json:"version"`
	Update   *update.Release `json:"update,omitempty"` // 有新版本时带上
}

// uiPrefs 托盘客户端自己的偏好(和服务的设置分开,放当前用户目录)。
type uiPrefs struct {
	Lang  string `json:"lang"`
	Theme string `json:"theme"`
}

type App struct {
	ctx         context.Context
	mu          sync.Mutex
	minimized   bool
	pendingLink string
	domReadyCh  chan struct{}
	state       UIState
	prefs       uiPrefs
	clashKey    string // port+secret,变了就重连速度流
	trafficStop context.CancelFunc
	tray        *trayUI
	updating    bool
}

func newApp(minimized bool, link string) *App {
	a := &App{minimized: minimized, pendingLink: link, domReadyCh: make(chan struct{})}
	a.prefs = loadPrefs()
	a.state = UIState{Lang: a.prefs.Lang, Theme: a.prefs.Theme, Version: buildinfo.Version, SvcState: "unknown"}
	return a
}

func prefsPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return filepath.Join(base, paths.AppName, "ui.json")
}

func loadPrefs() uiPrefs {
	p := uiPrefs{Lang: "zh", Theme: "system"}
	if b, err := os.ReadFile(prefsPath()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	if p.Lang != "en" {
		p.Lang = "zh"
	}
	if p.Theme != "light" && p.Theme != "dark" {
		p.Theme = "system"
	}
	return p
}

func (a *App) savePrefs() {
	_ = os.MkdirAll(filepath.Dir(prefsPath()), 0o700)
	b, _ := json.Marshal(a.prefs)
	_ = os.WriteFile(prefsPath(), b, 0o600)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.tray = newTray(a)
	go a.tray.run()
	// 服务被上次"退出"停掉了:登录后由托盘拉起(安装时已放开普通用户启停权限)
	if st := svc.QueryStatus(); st == "stopped" {
		if err := svc.StartUser(); err != nil {
			a.logf("启动服务失败: %v", err)
		}
	}
	go a.loop()
	go a.updateLoop()
}

func (a *App) domReady(context.Context) {
	select {
	case <-a.domReadyCh:
	default:
		close(a.domReadyCh)
	}
	if a.pendingLink != "" {
		link := a.pendingLink
		a.pendingLink = ""
		go a.handleDeepLink(link)
	}
}

func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	stop := a.trafficStop
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
	a.tray.quit()
}

func (a *App) logf(format string, v ...any) {
	runtime.LogInfof(a.ctx, format, v...)
}

// secondInstance 第二个实例启动了(双击图标或点了 godusevpn:// 链接):把窗口带到前台,有链接就导入。
func (a *App) secondInstance(link string) {
	a.showWindow()
	if link != "" {
		go a.handleDeepLink(link)
	}
}

// handleDeepLink godusevpn://import?url=<订阅地址>
func (a *App) handleDeepLink(link string) {
	u, err := url.Parse(link)
	if err != nil || !strings.EqualFold(u.Host, "import") {
		return
	}
	sub := u.Query().Get("url")
	if sub == "" {
		return
	}
	<-a.domReadyCh
	a.showWindow()
	runtime.EventsEmit(a.ctx, "import", sub)
}

// ---- 轮询服务、维护速度流 ----

func (a *App) loop() {
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	for {
		a.refresh()
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) refresh() {
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	var view ipc.StateView
	err := ipc.Call(ctx, ipc.MGetState, nil, &view)
	a.mu.Lock()
	a.state.Service = err == nil
	if err == nil {
		a.state.View = view
	} else {
		a.state.View.State.Status = "disconnected"
		a.state.Up, a.state.Down = 0, 0
	}
	a.state.SvcState = svc.QueryStatus()
	a.state.Lang, a.state.Theme = a.prefs.Lang, a.prefs.Theme
	st := a.state
	a.mu.Unlock()
	a.manageTraffic(st)
	a.tray.update(st)
	runtime.EventsEmit(a.ctx, "state", st)
}

// manageTraffic 内核在跑就保持一条速度流;停了就断开并把速度归零。
func (a *App) manageTraffic(st UIState) {
	running := st.Service && (st.View.State.Status == "connected" || st.View.State.Status == "degraded")
	if !running {
		a.mu.Lock()
		stop := a.trafficStop
		a.trafficStop, a.clashKey = nil, ""
		a.mu.Unlock()
		if stop != nil {
			stop()
		}
		return
	}
	var info ipc.ClashInfo
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	err := ipc.Call(ctx, ipc.MGetClashInfo, nil, &info)
	cancel()
	if err != nil || !info.Running {
		return
	}
	key := fmt.Sprintf("%d:%s", info.Port, info.Secret)
	a.mu.Lock()
	if a.clashKey == key && a.trafficStop != nil {
		a.mu.Unlock()
		return
	}
	if a.trafficStop != nil {
		a.trafficStop()
	}
	sctx, stop := context.WithCancel(a.ctx)
	a.trafficStop, a.clashKey = stop, key
	a.mu.Unlock()
	go func() {
		c := clash.New(info.Port, info.Secret)
		for sctx.Err() == nil {
			_ = c.Traffic(sctx, func(up, down int64) {
				a.mu.Lock()
				a.state.Up, a.state.Down = up, down
				a.mu.Unlock()
				runtime.EventsEmit(a.ctx, "traffic", map[string]int64{"up": up, "down": down})
			})
			if sctx.Err() == nil {
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

func (a *App) clashClient() (*clash.Client, error) {
	var info ipc.ClashInfo
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	if err := ipc.Call(ctx, ipc.MGetClashInfo, nil, &info); err != nil {
		return nil, err
	}
	if !info.Running {
		return nil, errors.New("内核未运行")
	}
	return clash.New(info.Port, info.Secret), nil
}

func (a *App) call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
	defer cancel()
	err := ipc.Call(ctx, method, params, result)
	if errors.Is(err, ipc.ErrNoService) {
		return errors.New("SERVICE_DOWN")
	}
	return err
}

// ---- 自更新 ----

func (a *App) updateLoop() {
	time.Sleep(20 * time.Second)
	for {
		if rel, err := update.Check(a.ctx, buildinfo.Version, true, nil); err == nil {
			a.mu.Lock()
			a.state.Update = rel
			a.mu.Unlock()
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

// CheckUpdate 手动检查,有就返回。
func (a *App) CheckUpdate() (*update.Release, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	rel, err := update.Check(ctx, buildinfo.Version, true, nil)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.state.Update = rel
	a.mu.Unlock()
	return rel, nil
}

// ApplyUpdate 下载安装包(进度经 update-progress 事件推给页面),校验后以管理员身份静默安装并重新拉起。
func (a *App) ApplyUpdate() error {
	a.mu.Lock()
	rel := a.state.Update
	if a.updating || rel == nil {
		a.mu.Unlock()
		return errors.New("没有可用的更新")
	}
	a.updating = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.updating = false; a.mu.Unlock() }()
	dir := filepath.Join(os.TempDir(), "godusevpn-update")
	path, err := update.Download(a.ctx, rel, dir, nil, func(done, total int64) {
		runtime.EventsEmit(a.ctx, "update-progress", map[string]int64{"done": done, "total": total})
	})
	if err != nil {
		return err
	}
	if err := runElevated(path, "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /RELAUNCH=1"); err != nil {
		return err
	}
	// 安装包会杀掉本进程再重新拉起;这里主动退出,别让它等
	go func() { time.Sleep(2 * time.Second); runtime.Quit(a.ctx) }()
	return nil
}

// ---- 窗口 ----

func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}

func (a *App) toggleWindow() { a.showWindow() }

func (a *App) HideWindow() { runtime.WindowHide(a.ctx) }

func (a *App) Minimize() { runtime.WindowMinimise(a.ctx) }

// QuitApp 托盘"退出":断开连接(TUN 随之拆除)、停掉后台服务进程,再退出自己。
// 下次登录托盘自启时会把服务再拉起来;上次是手动断开的,不会自动连。
func (a *App) QuitApp() {
	if a.GetState().Service {
		var v ipc.StateView
		_ = a.call(ipc.MDisconnect, nil, &v)
	}
	if err := svc.StopUser(); err != nil {
		a.logf("停止服务: %v", err)
	}
	a.shutdown(a.ctx)
	runtime.Quit(a.ctx)
}

// ---- 绑定给页面的方法(window.go.main.App.*) ----

func (a *App) GetState() UIState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *App) Connect() (ipc.StateView, error) {
	var v ipc.StateView
	err := a.call(ipc.MConnect, nil, &v)
	go a.refresh()
	return v, err
}

func (a *App) Disconnect() (ipc.StateView, error) {
	var v ipc.StateView
	err := a.call(ipc.MDisconnect, nil, &v)
	go a.refresh()
	return v, err
}

func (a *App) SetMode(mode string) (ipc.StateView, error) {
	var v ipc.StateView
	err := a.call(ipc.MSetMode, map[string]string{"mode": mode}, &v)
	go a.refresh()
	return v, err
}

func (a *App) SelectNode(tag string) (ipc.StateView, error) {
	var v ipc.StateView
	err := a.call(ipc.MSelectNode, map[string]string{"tag": tag}, &v)
	go a.refresh()
	return v, err
}

// NodeInfo 节点页一行。
type NodeInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Delay   int    `json:"delay"` // 0 = 未测;-1 = 上次失败
	Current bool   `json:"current"`
	AutoNow string `json:"autoNow,omitempty"` // auto 组当前落在哪个节点
}

// GetNodes 节点列表(带最近一次延迟);内核没跑时只给订阅里的名字。
func (a *App) GetNodes() ([]NodeInfo, error) {
	st := a.GetState()
	c, err := a.clashClient()
	if err != nil {
		out := []NodeInfo{}
		for _, n := range st.View.Nodes {
			out = append(out, NodeInfo{Name: n, Current: n == st.View.Node})
		}
		return out, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	proxies, err := c.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	group, ok := proxies["proxy"]
	if !ok {
		return nil, errors.New("内核里没有 proxy 组")
	}
	out := make([]NodeInfo, 0, len(group.All))
	for _, name := range group.All {
		p := proxies[name]
		ni := NodeInfo{Name: name, Type: p.Type, Delay: p.LastDelay(), Current: name == group.Now}
		if name == "auto" {
			ni.AutoNow = p.Now
		}
		out = append(out, ni)
	}
	return out, nil
}

// TestAll 给全部节点测一次延迟,返回 节点 → 毫秒。
func (a *App) TestAll() (map[string]int, error) {
	c, err := a.clashClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 40*time.Second)
	defer cancel()
	return c.GroupDelay(ctx, "proxy", testURL, 8*time.Second)
}

func (a *App) TestLatency(tag string) (int, error) {
	var r struct {
		Ms int `json:"ms"`
	}
	err := a.call(ipc.MTestLatency, map[string]string{"tag": tag}, &r)
	return r.Ms, err
}

func (a *App) SetProfileURL(url string) (*ipc.ProfileView, error) {
	var p *ipc.ProfileView
	err := a.call(ipc.MSetProfileURL, map[string]string{"url": strings.TrimSpace(url)}, &p)
	go a.refresh()
	return p, err
}

// RefreshProfile 刷新一条订阅(id 为空 = 当前),返回全部订阅。
func (a *App) RefreshProfile(id string) ([]ipc.ProfileView, error) {
	var out []ipc.ProfileView
	err := a.call(ipc.MRefreshProfile, map[string]string{"id": id}, &out)
	go a.refresh()
	return out, err
}

func (a *App) GetProfiles() ([]ipc.ProfileView, error) {
	var out []ipc.ProfileView
	err := a.call(ipc.MGetProfiles, nil, &out)
	if out == nil {
		out = []ipc.ProfileView{}
	}
	return out, err
}

// AddProfile 新增订阅(服务会先拉一次,拉不到就不加);第一条会自动成为当前订阅。
func (a *App) AddProfile(name, url string) ([]ipc.ProfileView, error) {
	var out []ipc.ProfileView
	err := a.call(ipc.MAddProfile, map[string]string{"name": name, "url": strings.TrimSpace(url)}, &out)
	go a.refresh()
	return out, err
}

func (a *App) RemoveProfile(id string) ([]ipc.ProfileView, error) {
	var out []ipc.ProfileView
	err := a.call(ipc.MRemoveProfile, map[string]string{"id": id}, &out)
	go a.refresh()
	return out, err
}

func (a *App) SelectProfile(id string) (ipc.StateView, error) {
	var v ipc.StateView
	err := a.call(ipc.MSelectProfile, map[string]string{"id": id}, &v)
	go a.refresh()
	return v, err
}

// UpdateProfile 改名或改地址(空字段表示不改)。
func (a *App) UpdateProfile(id, name, url string) ([]ipc.ProfileView, error) {
	var out []ipc.ProfileView
	err := a.call(ipc.MRenameProfile, map[string]string{"id": id, "name": name, "url": strings.TrimSpace(url)}, &out)
	go a.refresh()
	return out, err
}

func (a *App) GetSettings() (settings.Settings, error) {
	var s settings.Settings
	err := a.call(ipc.MGetSettings, nil, &s)
	return s, err
}

func (a *App) SaveSettings(s settings.Settings) (settings.Settings, error) {
	var out settings.Settings
	err := a.call(ipc.MSetSettings, s, &out)
	go a.refresh()
	return out, err
}

func (a *App) GetLogs(lines int, core bool) ([]string, error) {
	var out []string
	err := a.call(ipc.MGetLogs, map[string]any{"lines": lines, "core": core}, &out)
	return out, err
}

func (a *App) Diagnose() (string, error) {
	var out map[string]any
	if err := a.call(ipc.MDiagnose, nil, &out); err != nil {
		return "", err
	}
	out["ui"] = map[string]any{"version": buildinfo.Version, "autostart": autostart.Enabled(), "svcState": svc.QueryStatus()}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

// ExportDiag 让服务生成脱敏诊断包,复制到桌面并在资源管理器里选中,返回桌面上的路径。
func (a *App) ExportDiag() (string, error) {
	var r struct {
		Path string `json:"path"`
	}
	if err := a.call(ipc.MExportDiag, nil, &r); err != nil {
		return "", err
	}
	desktop := filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
	dst := filepath.Join(desktop, filepath.Base(r.Path))
	if err := copyFile(r.Path, dst); err != nil {
		return r.Path, nil // 复制不了就给原路径
	}
	_ = exec.Command("explorer.exe", "/select,", dst).Start()
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ConnRow 连接页一行。
type ConnRow struct {
	ID    string `json:"id"`
	Host  string `json:"host"`
	Net   string `json:"net"`
	Chain string `json:"chain"`
	Rule  string `json:"rule"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Start string `json:"start"`
	App   string `json:"app"`
}

func (a *App) GetConnections() ([]ConnRow, error) {
	c, err := a.clashClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	cs, err := c.Connections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConnRow, 0, len(cs.Connections))
	for _, x := range cs.Connections {
		host := x.Metadata.Host
		if host == "" {
			host = x.Metadata.DestinationIP
		}
		out = append(out, ConnRow{ID: x.ID, Host: host + ":" + x.Metadata.DestinationPort, Net: x.Metadata.Network,
			Chain: strings.Join(x.Chains, " → "), Rule: x.Rule, Up: x.Upload, Down: x.Download, Start: x.Start,
			App: filepath.Base(x.Metadata.ProcessPath)})
	}
	return out, nil
}

func (a *App) CloseConnection(id string) error {
	c, err := a.clashClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	return c.CloseConnection(ctx, id)
}

func (a *App) GetAutostart() bool { return autostart.Enabled() }

func (a *App) SetAutostart(on bool) error { return autostart.Set(on) }

func (a *App) SetLang(lang string) {
	if lang != "en" {
		lang = "zh"
	}
	a.mu.Lock()
	a.prefs.Lang, a.state.Lang = lang, lang
	a.mu.Unlock()
	a.savePrefs()
	a.tray.relabel()
	go a.refresh()
}

func (a *App) SetTheme(theme string) {
	if theme != "light" && theme != "dark" {
		theme = "system"
	}
	a.mu.Lock()
	a.prefs.Theme, a.state.Theme = theme, theme
	a.mu.Unlock()
	a.savePrefs()
}

// RepairService 以管理员身份重新注册并启动后台服务(弹一次 UAC)。
func (a *App) RepairService() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	svcExe := filepath.Join(filepath.Dir(exe), "godusevpn-svc.exe")
	if _, err := os.Stat(svcExe); err != nil {
		return errors.New("找不到 godusevpn-svc.exe,请重新安装")
	}
	if st := svc.QueryStatus(); st == "stopped" {
		if err := svc.StartUser(); err == nil {
			return nil
		}
	}
	if st := svc.QueryStatus(); st != "not-installed" {
		if err := runElevated(svcExe, "uninstall"); err != nil {
			return err
		}
		time.Sleep(time.Second)
	}
	return runElevated(svcExe, "install")
}

func runElevated(exe, args string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	arg, _ := syscall.UTF16PtrFromString(args)
	dir, _ := syscall.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, arg, dir, windows.SW_HIDE)
}

func (a *App) OpenLogs() error {
	return exec.Command("explorer.exe", paths.Logs()).Start()
}

func (a *App) OpenURL(url string) {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		runtime.BrowserOpenURL(a.ctx, url)
	}
}

func (a *App) Version() map[string]string {
	return map[string]string{"ui": buildinfo.Version, "name": buildinfo.DisplayName}
}
