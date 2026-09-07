// Package daemon 服务进程的主体:把设置、订阅、配置生成、内核、状态机、控制接口装配起来。
// 托盘客户端与命令行都只通过 ipc 调它,不直接碰任何底层对象。
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

const DisplayName = buildinfo.DisplayName

type Daemon struct {
	mu       sync.Mutex
	log      *logx.Rotator
	coreLog  *logx.Rotator
	core     *core.Core
	settings settings.Settings
	profile  *profile.Profile
	machine  *state.Machine
	server   *ipc.Server
	secret   string
	http     *http.Client
}

type persisted struct {
	Wanted bool `json:"wanted"`
}

// New 读设置与订阅缓存,装配各部件;不启动任何东西。
func New() (*Daemon, error) {
	if err := paths.Ensure(); err != nil {
		return nil, fmt.Errorf("建数据目录: %w", err)
	}
	d := &Daemon{
		log:     logx.New(filepath.Join(paths.Logs(), "service.log"), 5<<20, 3),
		coreLog: logx.New(filepath.Join(paths.Logs(), "core.log"), 5<<20, 3),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	d.core = core.New(core.Writer{Printf: d.coreLog.Printf})
	s, err := settings.Load(paths.Settings())
	if err != nil {
		d.logf("设置加载失败,用默认值: %v", err)
	}
	d.settings = s
	if p, err := profile.Load(paths.ProfileCache()); err == nil {
		d.profile = p
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	d.secret = hex.EncodeToString(b)
	d.machine = state.New(state.Deps{
		Prepare: d.prepare,
		Start:   d.start,
		Stop:    d.core.Stop,
		Alive:   d.core.Running,
		Health:  d.health,
		OnChange: func(s state.Snapshot) {
			d.logf("状态 → %s%s", s.Status, map[bool]string{true: "(" + s.Code + " " + s.Error + ")", false: ""}[s.Error != ""])
		},
		Logf: d.logf,
	})
	d.server = ipc.NewServer(d.logf)
	d.registerHandlers()
	return d, nil
}

func (d *Daemon) logf(format string, a ...any) { d.log.Printf("INFO", format, a...) }

// Run 起控制接口,按上次状态自动连接,定时刷新订阅;ctx 结束时全部停掉。
func (d *Daemon) Run(ctx context.Context) error {
	d.logf("%s 服务启动 v%s (%s/%s)", DisplayName, buildinfo.Version, runtime.GOOS, runtime.GOARCH)
	if err := d.server.Listen(); err != nil {
		return fmt.Errorf("监听控制管道: %w", err)
	}
	if d.loadPersisted().Wanted {
		d.logf("上次是已连接状态,自动连接")
		d.machine.Connect()
	}
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.logf("服务停止")
			d.machine.Disconnect()
			_ = d.server.Close()
			d.log.Close()
			d.coreLog.Close()
			return nil
		case <-t.C:
			d.maybeRefresh(ctx)
		}
	}
}

func (d *Daemon) loadPersisted() persisted {
	var p persisted
	if b, err := os.ReadFile(paths.State()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func (d *Daemon) savePersisted(p persisted) {
	b, _ := json.Marshal(p)
	_ = os.WriteFile(paths.State(), b, 0o600)
}

func (d *Daemon) getSettings() settings.Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings
}

func (d *Daemon) setSettings(s settings.Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := s.Save(paths.Settings()); err != nil {
		return err
	}
	d.mu.Lock()
	d.settings = s
	d.mu.Unlock()
	return nil
}

func (d *Daemon) getProfile() *profile.Profile {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.profile
}

func (d *Daemon) setProfile(p *profile.Profile) {
	d.mu.Lock()
	d.profile = p
	d.mu.Unlock()
	if err := p.Save(paths.ProfileCache()); err != nil {
		d.logf("写订阅缓存失败: %v", err)
	}
}

// fetchProfile 拉订阅并把拉取错误映射成错误码。
func (d *Daemon) fetchProfile(ctx context.Context, url string) (*profile.Profile, error) {
	p, err := profile.Fetch(ctx, url, d.http)
	if err == nil {
		return p, nil
	}
	var fe *profile.FetchError
	switch {
	case errors.Is(err, profile.ErrNoNodes):
		return nil, state.Errf(state.CodeProfileParse, "订阅里没有任何节点(面板还没给这个用户分配线路)")
	case errors.As(err, &fe) && (fe.Status == 404 || fe.Status == 410):
		return nil, state.Errf(state.CodeProfileAuth, "%s", fe.Msg)
	case errors.As(err, &fe):
		return nil, state.Errf(state.CodeProfileNet, "%s", fe.Msg)
	default:
		return nil, state.Errf(state.CodeProfileParse, "%v", err)
	}
}

// prepare 状态机的第一步:订阅过期就刷新(失败用缓存),生成配置,干跑。
func (d *Daemon) prepare(ctx context.Context) ([]byte, error) {
	s := d.getSettings()
	if strings.TrimSpace(s.ProfileURL) == "" {
		return nil, state.Errf(state.CodeProfileMissing, "还没有设置订阅地址")
	}
	p := d.getProfile()
	if p == nil || p.URL != s.ProfileURL || p.Stale(time.Duration(s.UpdateHours)*time.Hour) {
		np, err := d.fetchProfile(ctx, s.ProfileURL)
		if err != nil {
			if p == nil || p.URL != s.ProfileURL {
				return nil, err
			}
			d.logf("订阅刷新失败,先用缓存: %v", err)
		} else {
			d.logf("订阅已更新:%d 个节点", len(np.Outbounds))
			d.setProfile(np)
			p = np
		}
	}
	cfg, err := builder.Build(builder.Input{Profile: p, Settings: s, DataDir: paths.DataDir(), ClashSecret: d.secret, RuleSetDir: paths.RuleSets()})
	if err != nil {
		return nil, state.Errf(state.CodeConfig, "生成配置: %v", err)
	}
	if err := d.core.Validate(cfg); err != nil {
		return nil, state.Errf(state.CodeConfig, "配置校验失败: %v", err)
	}
	_ = os.WriteFile(paths.Config(), cfg, 0o600)
	return cfg, nil
}

// start 启动内核;失败按原因归类。
func (d *Daemon) start(cfg []byte) error {
	if err := d.core.Start(cfg); err != nil {
		low := strings.ToLower(err.Error())
		switch {
		case strings.Contains(low, "wintun") || strings.Contains(low, "adapter") || strings.Contains(low, "tun"):
			return state.Errf(state.CodeTunDriver, "TUN 网卡创建失败(需要管理员权限的服务在跑,或被安全软件拦住): %v", err)
		case strings.Contains(low, "route"):
			return state.Errf(state.CodeRouteConflict, "路由设置失败(可能与其它 VPN 冲突): %v", err)
		default:
			return state.Errf(state.CodeCoreStart, "%v", err)
		}
	}
	_ = os.WriteFile(paths.LastGood(), cfg, 0o600)
	// 内核里的当前节点跟着设置走(cache_file 也会记,这里是双保险)
	if s := d.getSettings(); s.Selected != "" {
		_ = d.core.Select("proxy", s.Selected)
	}
	return nil
}

// health 经代理测一次;内核不在了报崩溃码,让状态机重连。
func (d *Daemon) health(ctx context.Context) error {
	if !d.core.Running() {
		return state.Errf(state.CodeCoreCrash, "内核未运行")
	}
	if _, err := d.core.URLTest(ctx, "proxy", builder.TestURL); err != nil {
		return state.Errf(state.CodeNodeDown, "当前节点不可用: %v", err)
	}
	return nil
}

// maybeRefresh 定时刷新订阅;节点变了就重连(下一版改成热换出站)。
func (d *Daemon) maybeRefresh(ctx context.Context) {
	s := d.getSettings()
	p := d.getProfile()
	if s.ProfileURL == "" || (p != nil && p.URL == s.ProfileURL && !p.Stale(time.Duration(s.UpdateHours)*time.Hour)) {
		return
	}
	np, err := d.fetchProfile(ctx, s.ProfileURL)
	if err != nil {
		d.logf("定时刷新订阅失败: %v", err)
		return
	}
	changed := p == nil || !sameNodes(p, np)
	d.setProfile(np)
	if changed {
		d.logf("订阅节点有变化,重新应用")
		d.machine.Restart()
	}
}

func sameNodes(a, b *profile.Profile) bool {
	if len(a.Outbounds) != len(b.Outbounds) {
		return false
	}
	for i := range a.Outbounds {
		if string(a.Outbounds[i]) != string(b.Outbounds[i]) {
			return false
		}
	}
	return true
}

// ---- 控制接口 ----

func (d *Daemon) profileView() *ipc.ProfileView {
	p := d.getProfile()
	if p == nil {
		return nil
	}
	return &ipc.ProfileView{URL: p.URL, Title: p.Title, FetchedAt: p.FetchedAt, NodeCount: len(p.Outbounds), Tags: p.Tags, Usage: p.Usage}
}

func (d *Daemon) stateView() ipc.StateView {
	s := d.getSettings()
	v := ipc.StateView{Version: buildinfo.Version, Protocol: ipc.Version, State: d.machine.Snapshot(), Mode: s.Mode, Node: s.Selected, Profile: d.profileView(), Settings: s, Uptime: int64(d.core.Uptime().Seconds())}
	if v.Node == "" {
		v.Node = "auto"
	}
	if d.core.Running() {
		if m := d.core.Mode(); m != "" {
			v.Mode = builder.SettingMode(m)
		}
		if now, all, err := d.core.Group("proxy"); err == nil {
			v.Node, v.Nodes = now, all
		}
	} else if p := d.getProfile(); p != nil {
		v.Nodes = append([]string{"auto"}, p.Tags...)
	}
	return v
}

func (d *Daemon) registerHandlers() {
	h := d.server.Handle
	h(ipc.MPing, func(json.RawMessage) (any, error) {
		return map[string]any{"version": buildinfo.Version, "protocol": ipc.Version, "name": DisplayName}, nil
	})
	h(ipc.MGetState, func(json.RawMessage) (any, error) { return d.stateView(), nil })
	h(ipc.MConnect, func(json.RawMessage) (any, error) {
		if strings.TrimSpace(d.getSettings().ProfileURL) == "" {
			return nil, &ipc.CallError{Code: state.CodeProfileMissing, Msg: "还没有设置订阅地址"}
		}
		d.savePersisted(persisted{Wanted: true})
		d.machine.Connect()
		return d.stateView(), nil
	})
	h(ipc.MDisconnect, func(json.RawMessage) (any, error) {
		d.savePersisted(persisted{Wanted: false})
		d.machine.Disconnect()
		return d.stateView(), nil
	})
	h(ipc.MSetMode, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Mode string `json:"mode"`
		}](p)
		if err != nil {
			return nil, err
		}
		s := d.getSettings()
		s.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		if d.core.Running() {
			if err := d.core.SetMode(builder.ModeName(s.Mode)); err != nil {
				return nil, err
			}
		}
		return d.stateView(), nil
	})
	h(ipc.MSelectNode, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Tag string `json:"tag"`
		}](p)
		if err != nil {
			return nil, err
		}
		tag := strings.TrimSpace(in.Tag)
		if d.core.Running() {
			if err := d.core.Select("proxy", tag); err != nil {
				return nil, err
			}
		}
		s := d.getSettings()
		s.Selected = tag
		if tag == "auto" {
			s.Selected = ""
		}
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		return d.stateView(), nil
	})
	h(ipc.MTestLatency, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Tag string `json:"tag"`
		}](p)
		if err != nil {
			return nil, err
		}
		if in.Tag == "" {
			in.Tag = "proxy"
		}
		ms, err := d.core.URLTest(context.Background(), in.Tag, builder.TestURL)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tag": in.Tag, "ms": ms}, nil
	})
	h(ipc.MGetProfile, func(json.RawMessage) (any, error) { return d.profileView(), nil })
	h(ipc.MSetProfileURL, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			URL string `json:"url"`
		}](p)
		if err != nil {
			return nil, err
		}
		s := d.getSettings()
		s.ProfileURL = strings.TrimSpace(in.URL)
		if err := s.Validate(); err != nil {
			return nil, err
		}
		np, err := d.fetchProfile(context.Background(), s.ProfileURL)
		if err != nil {
			return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: err.Error()}
		}
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		d.setProfile(np)
		d.machine.Restart()
		return d.profileView(), nil
	})
	h(ipc.MRefreshProfile, func(json.RawMessage) (any, error) {
		s := d.getSettings()
		if s.ProfileURL == "" {
			return nil, &ipc.CallError{Code: state.CodeProfileMissing, Msg: "还没有设置订阅地址"}
		}
		np, err := d.fetchProfile(context.Background(), s.ProfileURL)
		if err != nil {
			return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: err.Error()}
		}
		old := d.getProfile()
		d.setProfile(np)
		if old == nil || !sameNodes(old, np) {
			d.machine.Restart()
		}
		return d.profileView(), nil
	})
	h(ipc.MGetSettings, func(json.RawMessage) (any, error) { return d.getSettings(), nil })
	h(ipc.MSetSettings, func(p json.RawMessage) (any, error) {
		next, err := ipc.Decode[settings.Settings](p)
		if err != nil {
			return nil, err
		}
		prev := d.getSettings()
		next.Schema = settings.Schema
		if err := d.setSettings(next); err != nil {
			return nil, err
		}
		if d.core.Running() {
			// 模式与节点是运行时可改的,别的都要重新生成配置
			live := prev
			live.Mode, live.Selected = next.Mode, next.Selected
			if live != next {
				d.machine.Restart()
			} else {
				if next.Mode != prev.Mode {
					_ = d.core.SetMode(builder.ModeName(next.Mode))
				}
				if next.Selected != prev.Selected {
					sel := next.Selected
					if sel == "" {
						sel = "auto"
					}
					_ = d.core.Select("proxy", sel)
				}
			}
		}
		return d.getSettings(), nil
	})
	h(ipc.MGetLogs, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Lines int  `json:"lines"`
			Core  bool `json:"core"`
		}](p)
		if err != nil {
			return nil, err
		}
		if in.Lines <= 0 || in.Lines > 2000 {
			in.Lines = 200
		}
		path := d.log.Path()
		if in.Core {
			path = d.coreLog.Path()
		}
		return logx.Tail(path, in.Lines), nil
	})
	h(ipc.MDiagnose, func(json.RawMessage) (any, error) {
		p := d.getProfile()
		out := map[string]any{
			"version": buildinfo.Version, "os": runtime.GOOS + "/" + runtime.GOARCH,
			"state": d.stateView(), "dataDir": paths.DataDir(),
			"serviceLog": logx.Tail(d.log.Path(), 100), "coreLog": logx.Tail(d.coreLog.Path(), 100),
		}
		if p != nil {
			out["servers"] = p.Servers()
		}
		return out, nil
	})
}
