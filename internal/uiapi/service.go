// Package uiapi 页面(web/dist)调用的方法集与事件流的共用实现:Linux 的 HTTP 面板与 Android 的 WebView 桥都用它,
// 方法名、参数顺序、返回结构与 Windows 客户端(cmd/godusevpn/app.go 里 Wails 绑定的方法)一致,
// 所以同一套页面在三端行为相同。后端是守护进程的进程内控制口(Dispatch),不经 socket。
package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/clash"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/svc"
	"github.com/Maoyangui/godusevpn/internal/update"
)

const testURL = "http://www.gstatic.com/generate_204"

// Backend 守护进程给的接口:进程内直接调控制口方法。
type Backend interface {
	Dispatch(method string, params json.RawMessage) (any, error)
	Logf(format string, a ...any)
}

// Options 各平台不同的那几件事。
type Options struct {
	Platform      string                                                       // 报给页面的平台名:linux / android;空 = runtime.GOOS
	PrefsPath     string                                                       // 语言与外观偏好文件
	Autostart     func() bool                                                  // 开机自启状态;nil = 不支持
	SetAutostart  func(bool) error                                             // 开关开机自启;nil = 不支持
	InstallUpdate func(string) error                                           // 下载好安装包后怎么装(Android 交给系统安装器);nil = 平台默认(Linux 换二进制重启)
	Extra         func(name string, args []json.RawMessage) (any, bool, error) // 平台自己的额外方法;第二个返回值为假表示不认识
}

// Event 推给页面的事件:state / traffic / update-progress / nav。
type Event struct {
	Name string
	Data any
}

type prefs struct {
	Lang  string `json:"lang"`
	Theme string `json:"theme"`
}

// State 与 Windows 客户端的 UIState 字段一致,多 web / platform / init 让页面按环境调整。
type State struct {
	Service   bool            `json:"service"`
	SvcState  string          `json:"svcState"`
	View      ipc.StateView   `json:"view"`
	Up        int64           `json:"up"`
	TotalUp   int64           `json:"totalUp"`   // 本次连接累计上行(内核停了归零)
	TotalDown int64           `json:"totalDown"` // 本次连接累计下行
	Down      int64           `json:"down"`
	Lang      string          `json:"lang"`
	Theme     string          `json:"theme"`
	Version   string          `json:"version"`
	Update    *update.Release `json:"update,omitempty"`
	Web       bool            `json:"web"`
	Platform  string          `json:"platform"`
	Init      string          `json:"init"`
}

type NodeInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Delay   int    `json:"delay"`
	Current bool   `json:"current"`
	AutoNow string `json:"autoNow,omitempty"`
}

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

type Service struct {
	b   Backend
	o   Options
	mu  sync.Mutex
	ctx context.Context

	subs map[chan Event]struct{}

	up, down       int64
	totUp, totDown int64 // 本次连接累计:速度流每秒一条,累加得来
	trafficStop    context.CancelFunc
	clashKey       string

	prefs     prefs
	update    *update.Release
	lastCheck time.Time
	updating  bool
	lastState string
}

func New(b Backend, o Options) *Service {
	s := &Service{b: b, o: o, subs: map[chan Event]struct{}{}, ctx: context.Background()}
	if s.o.Platform == "" {
		s.o.Platform = runtime.GOOS
	}
	if o.PrefsPath != "" {
		if raw, err := os.ReadFile(o.PrefsPath); err == nil {
			_ = json.Unmarshal(raw, &s.prefs)
		}
	}
	if s.prefs.Lang == "" {
		s.prefs.Lang = "zh" // 页面把"语言变了"当作要整页重绘,空值不能给出去
	}
	if s.prefs.Theme == "" {
		s.prefs.Theme = "system"
	}
	return s
}

// Run 每 1.5 秒推一次状态(有人订阅时),按内核状态开关速度流,定时查更新;阻塞到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.ctx = ctx
	go s.updateLoop(ctx)
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			stop := s.trafficStop
			s.mu.Unlock()
			if stop != nil {
				stop()
			}
			return
		case <-t.C:
			st := s.State()
			s.manageTraffic(ctx, st)
			s.mu.Lock()
			justConnected := st.View.State.Status == "connected" && s.lastState != "connected"
			s.lastState = string(st.View.State.Status)
			n := len(s.subs)
			s.mu.Unlock()
			if n > 0 {
				s.Broadcast("state", st)
			}
			if justConnected {
				go s.pokeUpdate(false)
			}
		}
	}
}

// Subscribe 订阅事件;用完调返回的函数退订。慢消费者会丢事件而不是阻塞推送。
func (s *Service) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// Broadcast 给所有订阅者推一条事件。
func (s *Service) Broadcast(name string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- Event{name, data}:
		default:
		}
	}
}

// ---- 与守护进程的交互 ----

func (s *Service) dispatch(method string, params any, out any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	res, err := s.b.Dispatch(method, raw)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// Settings 守护进程当前设置(含密码哈希;给页面前要抹掉)。
func (s *Service) Settings() settings.Settings {
	var st settings.Settings
	_ = s.dispatch(ipc.MGetSettings, nil, &st)
	return st
}

func (s *Service) clashInfo() (ipc.ClashInfo, error) {
	var info ipc.ClashInfo
	err := s.dispatch(ipc.MGetClashInfo, nil, &info)
	return info, err
}

func (s *Service) clashClient() (*clash.Client, error) {
	info, err := s.clashInfo()
	if err != nil {
		return nil, err
	}
	if !info.Running {
		return nil, errors.New("内核未运行")
	}
	return clash.New(info.Port, info.Secret), nil
}

// State 当前状态快照。
func (s *Service) State() State {
	var view ipc.StateView
	err := s.dispatch(ipc.MGetState, nil, &view)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Service: err == nil, SvcState: "running", View: view, Up: s.up, Down: s.down, TotalUp: s.totUp, TotalDown: s.totDown, Lang: s.prefs.Lang, Theme: s.prefs.Theme,
		Version: buildinfo.Version, Update: s.update, Web: true, Platform: s.o.Platform, Init: svc.Kind()}
	if err != nil {
		st.View.State.Status = "disconnected"
	}
	return st
}

// manageTraffic 内核在跑就保持一条速度流;停了就断开并把速度归零。
func (s *Service) manageTraffic(ctx context.Context, st State) {
	running := st.View.State.Status == "connected" || st.View.State.Status == "degraded"
	if !running {
		s.mu.Lock()
		stop := s.trafficStop
		s.trafficStop, s.clashKey, s.up, s.down = nil, "", 0, 0
		s.totUp, s.totDown = 0, 0 // 断开就重新计次:首页那格显示的是"本次连接"
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
		return
	}
	info, err := s.clashInfo()
	if err != nil || !info.Running {
		return
	}
	key := fmt.Sprintf("%d:%s", info.Port, info.Secret)
	s.mu.Lock()
	if s.clashKey == key && s.trafficStop != nil {
		s.mu.Unlock()
		return
	}
	if s.trafficStop != nil {
		s.trafficStop()
	}
	sctx, stop := context.WithCancel(ctx)
	s.trafficStop, s.clashKey = stop, key
	s.mu.Unlock()
	go func() {
		c := clash.New(info.Port, info.Secret)
		for sctx.Err() == nil {
			_ = c.Traffic(sctx, func(up, down int64) {
				// 每秒一条,给的是这一秒的字节数;累加即本次连接的总量,放服务端好让页面刷新后接着数。
				s.mu.Lock()
				s.up, s.down = up, down
				s.totUp, s.totDown = s.totUp+up, s.totDown+down
				tu, td := s.totUp, s.totDown
				n := len(s.subs)
				s.mu.Unlock()
				if n > 0 {
					s.Broadcast("traffic", map[string]int64{"up": up, "down": down, "totalUp": tu, "totalDown": td})
				}
			})
			if sctx.Err() == nil {
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

// ---- 方法分发 ----

func arg[T any](args []json.RawMessage, i int) T {
	var v T
	if i < len(args) && len(args[i]) > 0 {
		_ = json.Unmarshal(args[i], &v)
	}
	return v
}

// CallJSON 给不方便传结构的桥(Android)用:参数是 JSON 数组,结果是 JSON。
func (s *Service) CallJSON(name, argsJSON string) (string, error) {
	var args []json.RawMessage
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("参数不是 JSON 数组: %w", err)
		}
	}
	res, err := s.Call(name, args)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	return string(b), err
}

// Call 执行一个页面方法(参数按位置)。
func (s *Service) Call(name string, args []json.RawMessage) (any, error) {
	ctx := s.ctx
	view := func(method string, params any) (any, error) {
		var v ipc.StateView
		err := s.dispatch(method, params, &v)
		return v, err
	}
	profiles := func(method string, params any) (any, error) {
		var out []ipc.ProfileView
		err := s.dispatch(method, params, &out)
		if out == nil {
			out = []ipc.ProfileView{}
		}
		return out, err
	}
	devices := func(method string, params any) (any, error) {
		var out []ipc.DeviceView
		err := s.dispatch(method, params, &out)
		if out == nil {
			out = []ipc.DeviceView{}
		}
		return out, err
	}
	if s.o.Extra != nil {
		if res, ok, err := s.o.Extra(name, args); ok {
			return res, err
		}
	}
	switch name {
	case "GetState":
		return s.State(), nil
	case "Connect":
		return view(ipc.MConnect, nil)
	case "Disconnect":
		return view(ipc.MDisconnect, nil)
	case "SetMode":
		return view(ipc.MSetMode, map[string]string{"mode": arg[string](args, 0)})
	case "SelectNode":
		return view(ipc.MSelectNode, map[string]string{"tag": arg[string](args, 0)})
	case "GetNodes":
		return s.nodes(ctx)
	case "TestAll":
		if c, err := s.clashClient(); err == nil {
			tctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			return c.GroupDelay(tctx, "proxy", testURL, 8*time.Second)
		}
		var res map[string]int
		err := s.dispatch(ipc.MProbeNodes, nil, &res)
		return res, err
	case "TestLatency":
		var r struct {
			Ms int `json:"ms"`
		}
		err := s.dispatch(ipc.MTestLatency, map[string]string{"tag": arg[string](args, 0)}, &r)
		return r.Ms, err
	case "SetProfileURL":
		var p *ipc.ProfileView
		err := s.dispatch(ipc.MSetProfileURL, map[string]string{"url": strings.TrimSpace(arg[string](args, 0))}, &p)
		return p, err
	case "RefreshProfile":
		return profiles(ipc.MRefreshProfile, map[string]string{"id": arg[string](args, 0)})
	case "GetProfiles":
		return profiles(ipc.MGetProfiles, nil)
	case "AddProfile":
		return profiles(ipc.MAddProfile, map[string]string{"name": arg[string](args, 0), "url": strings.TrimSpace(arg[string](args, 1))})
	case "RemoveProfile":
		return profiles(ipc.MRemoveProfile, map[string]string{"id": arg[string](args, 0)})
	case "SelectProfile":
		return view(ipc.MSelectProfile, map[string]string{"id": arg[string](args, 0)})
	case "UpdateProfile":
		return profiles(ipc.MRenameProfile, map[string]string{"id": arg[string](args, 0), "name": arg[string](args, 1), "url": strings.TrimSpace(arg[string](args, 2))})
	case "GetDevices":
		return devices(ipc.MGetDevices, nil)
	case "SetDevice": // (mac, name, mode, ip)
		return devices(ipc.MSetDevice, map[string]string{"mac": arg[string](args, 0), "name": arg[string](args, 1), "mode": arg[string](args, 2), "ip": arg[string](args, 3)})
	case "RemoveDevice":
		return devices(ipc.MRemoveDevice, map[string]string{"mac": arg[string](args, 0)})
	case "GetSettings":
		st := s.Settings()
		st.WebPassword = "" // 不把哈希给页面
		return st, nil
	case "SaveSettings":
		in := arg[settings.Settings](args, 0)
		in.WebPassword = s.Settings().WebPassword // 密码另有接口改
		var out settings.Settings
		err := s.dispatch(ipc.MSetSettings, in, &out)
		out.WebPassword = ""
		return out, err
	case "SetWebPassword":
		st := s.Settings()
		if err := st.SetWebPassword(arg[string](args, 0)); err != nil {
			return nil, err
		}
		var out settings.Settings
		return nil, s.dispatch(ipc.MSetSettings, st, &out)
	case "GetLogs":
		var lines []string
		err := s.dispatch(ipc.MGetLogs, map[string]any{"lines": arg[int](args, 0), "core": arg[bool](args, 1)}, &lines)
		if lines == nil {
			lines = []string{}
		}
		return lines, err
	case "Diagnose":
		var out map[string]any
		if err := s.dispatch(ipc.MDiagnose, nil, &out); err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return string(b), nil
	case "ExportDiag": // 返回 zip 路径;各平台外壳再决定怎么交给用户(下载 / 分享)
		var r struct {
			Path string `json:"path"`
		}
		err := s.dispatch(ipc.MExportDiag, nil, &r)
		return r.Path, err
	case "GetConnections":
		return s.connections(ctx)
	case "CloseConnection":
		c, err := s.clashClient()
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return nil, c.CloseConnection(cctx, arg[string](args, 0))
	case "GetAutostart":
		if s.o.Autostart == nil {
			return false, nil
		}
		return s.o.Autostart(), nil
	case "SetAutostart":
		if s.o.SetAutostart == nil {
			return nil, errors.New("这个平台不支持")
		}
		return nil, s.o.SetAutostart(arg[bool](args, 0))
	case "SetLang":
		s.mu.Lock()
		s.prefs.Lang = arg[string](args, 0)
		s.mu.Unlock()
		s.savePrefs()
		return nil, nil
	case "SetTheme":
		s.mu.Lock()
		s.prefs.Theme = arg[string](args, 0)
		s.mu.Unlock()
		s.savePrefs()
		return nil, nil
	case "Version":
		return map[string]string{"ui": buildinfo.Version, "platform": runtime.GOOS + "/" + runtime.GOARCH, "init": svc.Kind(), "sock": ipc.Address()}, nil
	case "PokeUpdate":
		go s.pokeUpdate(false)
		return nil, nil
	case "CheckUpdate":
		return s.checkUpdate(true)
	case "ApplyUpdate":
		return nil, s.applyUpdate(ctx)
	case "HideWindow", "Minimize", "OpenURL", "OpenLogs":
		return nil, nil // 没有窗口的环境:页面自己处理
	case "ReadClipboard":
		return "", nil // 浏览器 / WebView 用宿主的剪贴板接口
	case "RepairService", "QuitApp":
		return nil, errors.New("这个环境不支持这个操作")
	}
	return nil, fmt.Errorf("没有这个方法: %s", name)
}

func (s *Service) savePrefs() {
	if s.o.PrefsPath == "" {
		return
	}
	s.mu.Lock()
	b, _ := json.Marshal(s.prefs)
	s.mu.Unlock()
	_ = os.WriteFile(s.o.PrefsPath, b, 0o600)
}

func (s *Service) nodes(ctx context.Context) ([]NodeInfo, error) {
	st := s.State()
	c, err := s.clashClient()
	if err != nil {
		out := []NodeInfo{}
		for _, n := range st.View.Nodes {
			out = append(out, NodeInfo{Name: n, Delay: st.View.Delays[n], Current: n == st.View.Node})
		}
		return out, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	proxies, err := c.Proxies(cctx)
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

func (s *Service) connections(ctx context.Context) ([]ConnRow, error) {
	c, err := s.clashClient()
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cs, err := c.Connections(cctx)
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

// ---- 更新 ----

func (s *Service) updateLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	for {
		s.pokeUpdate(true)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func (s *Service) pokeUpdate(force bool) {
	s.mu.Lock()
	if !force && time.Since(s.lastCheck) < 15*time.Minute {
		s.mu.Unlock()
		return
	}
	s.lastCheck = time.Now()
	s.mu.Unlock()
	rel, err := s.checkUpdate(false)
	if err != nil {
		return
	}
	s.mu.Lock()
	old := s.update
	changed := (rel == nil) != (old == nil) || (rel != nil && old != nil && rel.Version != old.Version)
	s.mu.Unlock()
	if changed {
		s.Broadcast("state", s.State())
	}
}

func (s *Service) checkUpdate(manual bool) (*update.Release, error) {
	cctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	rel, err := update.Check(cctx, buildinfo.Version, true, nil)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.update = rel
	if manual {
		s.lastCheck = time.Now()
	}
	s.mu.Unlock()
	return rel, nil
}

// applyUpdate 下载安装包(进度经 update-progress 事件推给页面),然后交给平台安装。
func (s *Service) applyUpdate(ctx context.Context) error {
	s.mu.Lock()
	rel := s.update
	if s.updating || rel == nil {
		s.mu.Unlock()
		return errors.New("没有可用的更新")
	}
	s.updating = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.updating = false; s.mu.Unlock() }()
	dir := filepath.Join(os.TempDir(), "godusevpn-update")
	_ = os.RemoveAll(dir)
	path, err := update.Download(ctx, rel, dir, nil, func(done, total int64) {
		s.Broadcast("update-progress", map[string]int64{"done": done, "total": total})
	})
	if err != nil {
		return err
	}
	if s.o.InstallUpdate != nil {
		return s.o.InstallUpdate(path)
	}
	return installUpdate(s.b, rel, path)
}
