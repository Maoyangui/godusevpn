package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/autostart"
	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/svc"
	"github.com/Maoyangui/godusevpn/internal/update"
)

// 页面调用的方法与 Windows 客户端(cmd/godusevpn/app.go)同名同义,参数按位置传(api.js 把 JS 实参数组原样发过来)。

const testURL = "http://www.gstatic.com/generate_204"

// uiState 与 Windows 客户端的 UIState 字段一致,多一个 web 标记让页面隐藏窗口按钮等桌面专有功能。
type uiState struct {
	Service  bool            `json:"service"`
	SvcState string          `json:"svcState"`
	View     ipc.StateView   `json:"view"`
	Up       int64           `json:"up"`
	Down     int64           `json:"down"`
	Lang     string          `json:"lang"`
	Theme    string          `json:"theme"`
	Version  string          `json:"version"`
	Update   *update.Release `json:"update,omitempty"`
	Web      bool            `json:"web"`
	Platform string          `json:"platform"`
	Init     string          `json:"init"`
}

type nodeInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Delay   int    `json:"delay"`
	Current bool   `json:"current"`
	AutoNow string `json:"autoNow,omitempty"`
}

type connRow struct {
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

func (s *Server) state() uiState {
	var view ipc.StateView
	err := s.dispatch(ipc.MGetState, nil, &view)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := uiState{Service: err == nil, SvcState: "running", View: view, Up: s.up, Down: s.down, Lang: s.prefs.Lang, Theme: s.prefs.Theme,
		Version: buildinfo.Version, Update: s.update, Web: true, Platform: runtime.GOOS, Init: svc.Kind()}
	if err != nil {
		st.View.State.Status = "disconnected"
	}
	return st
}

func arg[T any](args []json.RawMessage, i int) T {
	var v T
	if i < len(args) && len(args[i]) > 0 {
		_ = json.Unmarshal(args[i], &v)
	}
	return v
}

// api 分发一个页面方法。
func (s *Server) api(name string, args []json.RawMessage) (any, error) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
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
	switch name {
	case "GetState":
		return s.state(), nil
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
		var out []ipc.DeviceView
		err := s.dispatch(ipc.MGetDevices, nil, &out)
		if out == nil {
			out = []ipc.DeviceView{}
		}
		return out, err
	case "SetDevice": // (mac, name, mode, ip)
		var out []ipc.DeviceView
		err := s.dispatch(ipc.MSetDevice, map[string]string{"mac": arg[string](args, 0), "name": arg[string](args, 1), "mode": arg[string](args, 2), "ip": arg[string](args, 3)}, &out)
		return out, err
	case "RemoveDevice":
		var out []ipc.DeviceView
		err := s.dispatch(ipc.MRemoveDevice, map[string]string{"mac": arg[string](args, 0)}, &out)
		return out, err
	case "GetSettings":
		st := s.settings()
		st.WebPassword = "" // 不把哈希给页面
		return st, nil
	case "SaveSettings":
		in := arg[settings.Settings](args, 0)
		in.WebPassword = s.settings().WebPassword // 密码另有接口改
		var out settings.Settings
		err := s.dispatch(ipc.MSetSettings, in, &out)
		out.WebPassword = ""
		return out, err
	case "SetWebPassword":
		st := s.settings()
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
	case "ExportDiag":
		var r struct {
			Path string `json:"path"`
		}
		if err := s.dispatch(ipc.MExportDiag, nil, &r); err != nil {
			return nil, err
		}
		return "/api/diag/" + filepath.Base(r.Path), nil // 页面在浏览器里打开这个地址即下载
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
		return autostart.Enabled(), nil
	case "SetAutostart":
		return nil, autostart.Set(arg[bool](args, 0))
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
		return nil, nil // 浏览器里没有窗口;页面自己处理
	case "ReadClipboard":
		return "", nil // 浏览器里用 navigator.clipboard
	case "RepairService", "QuitApp":
		return nil, errors.New("浏览器面板不支持这个操作,请在系统上用 godusevpn start / stop")
	}
	return nil, fmt.Errorf("没有这个方法: %s", name)
}

func (s *Server) savePrefs() {
	s.mu.Lock()
	b, _ := json.Marshal(s.prefs)
	s.mu.Unlock()
	_ = os.WriteFile(paths.UIPrefs(), b, 0o600)
}

func (s *Server) nodes(ctx context.Context) ([]nodeInfo, error) {
	st := s.state()
	c, err := s.clashClient()
	if err != nil {
		out := []nodeInfo{}
		for _, n := range st.View.Nodes {
			out = append(out, nodeInfo{Name: n, Delay: st.View.Delays[n], Current: n == st.View.Node})
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
	out := make([]nodeInfo, 0, len(group.All))
	for _, name := range group.All {
		p := proxies[name]
		ni := nodeInfo{Name: name, Type: p.Type, Delay: p.LastDelay(), Current: name == group.Now}
		if name == "auto" {
			ni.AutoNow = p.Now
		}
		out = append(out, ni)
	}
	return out, nil
}

func (s *Server) connections(ctx context.Context) ([]connRow, error) {
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
	out := make([]connRow, 0, len(cs.Connections))
	for _, x := range cs.Connections {
		host := x.Metadata.Host
		if host == "" {
			host = x.Metadata.DestinationIP
		}
		out = append(out, connRow{ID: x.ID, Host: host + ":" + x.Metadata.DestinationPort, Net: x.Metadata.Network,
			Chain: strings.Join(x.Chains, " → "), Rule: x.Rule, Up: x.Upload, Down: x.Download, Start: x.Start,
			App: filepath.Base(x.Metadata.ProcessPath)})
	}
	return out, nil
}

// ---- 更新 ----

func (s *Server) updateLoop(ctx context.Context) {
	time.Sleep(20 * time.Second)
	for {
		s.pokeUpdate(true)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func (s *Server) pokeUpdate(force bool) {
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
		s.broadcast("state", s.state())
	}
}

func (s *Server) checkUpdate(manual bool) (*update.Release, error) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
