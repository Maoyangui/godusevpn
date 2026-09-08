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
	"reflect"
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
	profiles map[string]*profile.Profile // 订阅 id → 节点缓存
	fetchErr map[string]string           // 订阅 id → 最近一次拉取失败原因
	machine  *state.Machine
	server   *ipc.Server
	secret   string
	http     *http.Client
	delays   map[string]int // 最近一次全节点测速结果
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
		log:      logx.New(filepath.Join(paths.Logs(), "service.log"), 5<<20, 3),
		coreLog:  logx.New(filepath.Join(paths.Logs(), "core.log"), 5<<20, 3),
		http:     &http.Client{Timeout: 30 * time.Second},
		profiles: map[string]*profile.Profile{},
		fetchErr: map[string]string{},
	}
	d.core = core.New(core.Writer{Printf: d.coreLog.Printf})
	s, err := settings.Load(paths.Settings())
	if err != nil {
		d.logf("设置加载失败,用默认值: %v", err)
	}
	d.settings = s
	d.applyLogRetention()
	d.loadProfileCaches()
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

// loadProfileCaches 读每条订阅的缓存;schema 1 留下的单文件缓存归到当前订阅名下。
func (d *Daemon) loadProfileCaches() {
	for _, p := range d.settings.Profiles {
		if c, err := profile.Load(paths.ProfileCache(p.ID)); err == nil {
			d.profiles[p.ID] = c
		}
	}
	if legacy, err := profile.Load(paths.LegacyProfileCache()); err == nil {
		if a := d.settings.Active(); a != nil && d.profiles[a.ID] == nil && legacy.URL == a.URL {
			d.profiles[a.ID] = legacy
			_ = legacy.Save(paths.ProfileCache(a.ID))
		}
		_ = os.Remove(paths.LegacyProfileCache())
	}
}

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
	lastPrune := time.Now()
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
			if time.Since(lastPrune) > 6*time.Hour {
				lastPrune = time.Now()
				d.applyLogRetention()
			}
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
	d.applyLogRetention()
	return nil
}

// applyLogRetention 把"日志保留天数"套到两份日志上,并顺手清一次旧文件(滚动出去的日志与诊断包)。
func (d *Daemon) applyLogRetention() {
	age := time.Duration(d.getSettings().LogDays) * 24 * time.Hour
	d.log.SetMaxAge(age)
	d.coreLog.SetMaxAge(age)
	if n := logx.Prune(paths.Logs(), age) + logx.Prune(filepath.Join(paths.DataDir(), "diag"), age); n > 0 {
		d.logf("清理了 %d 个过期日志 / 诊断文件", n)
	}
}

// activeProfile 当前订阅的设置项与缓存(任一为空返回 nil)。
func (d *Daemon) activeProfile() (*settings.Profile, *profile.Profile) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.settings.Active()
	if a == nil {
		return nil, nil
	}
	return a, d.profiles[a.ID]
}

func (d *Daemon) setProfileCache(id string, p *profile.Profile) {
	d.mu.Lock()
	d.profiles[id] = p
	delete(d.fetchErr, id)
	d.mu.Unlock()
	if err := p.Save(paths.ProfileCache(id)); err != nil {
		d.logf("写订阅缓存失败: %v", err)
	}
}

func (d *Daemon) noteFetchErr(id string, err error) {
	d.mu.Lock()
	d.fetchErr[id] = err.Error()
	d.mu.Unlock()
}

// fetchProfile 拉订阅:内核在跑时先经当前代理;失败就经 auto 组再试三次;auto 也不行走直连。
// 这条回退链只用于刷新订阅,不影响任何其它流量,也不改用户选中的节点。内核没跑时直接用系统网络。
// 订阅无效 / 到期这类错误不换路径,直接返回。
func (d *Daemon) fetchProfile(ctx context.Context, url string) (*profile.Profile, error) {
	if !d.core.Running() {
		return d.fetchWith(ctx, url, d.http)
	}
	routes := []string{"proxy", "auto", "auto", "auto", "direct"} // 当前代理一次、auto 三次、最后直连
	var last error
	for i, tag := range routes {
		cl, err := d.core.HTTPClient(tag, 30*time.Second)
		if err != nil {
			continue
		}
		p, err := d.fetchWith(ctx, url, cl)
		if err == nil {
			if tag != "proxy" {
				d.logf("订阅经 %s 拉取成功(当前代理不通)", tag)
			}
			return p, nil
		}
		if state.CodeOf(err) == state.CodeProfileAuth || state.CodeOf(err) == state.CodeProfileParse {
			return nil, err
		}
		d.logf("订阅经 %s 拉取失败: %v", tag, err)
		last = err
		if ctx.Err() != nil || i == len(routes)-1 {
			break
		}
		time.Sleep(2 * time.Second) // 隔两秒再试下一条路径
	}
	return nil, last
}

// fetchWith 用给定客户端拉一次,并把错误映射成错误码。
func (d *Daemon) fetchWith(ctx context.Context, url string, client *http.Client) (*profile.Profile, error) {
	p, err := profile.Fetch(ctx, url, client)
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

// refreshProfile 拉指定订阅并更新缓存,返回节点是否有变化。
func (d *Daemon) refreshProfile(ctx context.Context, id string) (changed bool, err error) {
	d.mu.Lock()
	var url string
	for _, p := range d.settings.Profiles {
		if p.ID == id {
			url = p.URL
		}
	}
	old := d.profiles[id]
	d.mu.Unlock()
	if url == "" {
		return false, state.Errf(state.CodeProfileMissing, "没有这条订阅")
	}
	np, err := d.fetchProfile(ctx, url)
	if err != nil {
		d.noteFetchErr(id, err)
		return false, err
	}
	d.setProfileCache(id, np)
	return old == nil || !sameNodes(old, np), nil
}

// prepare 状态机的第一步:当前订阅过期就刷新(失败用缓存),生成配置,干跑。
func (d *Daemon) prepare(ctx context.Context) ([]byte, error) {
	s := d.getSettings()
	a, p := d.activeProfile()
	if a == nil {
		return nil, state.Errf(state.CodeProfileMissing, "还没有添加订阅")
	}
	if p == nil || p.URL != a.URL || p.Stale(time.Duration(s.UpdateHours)*time.Hour) {
		if _, err := d.refreshProfile(ctx, a.ID); err != nil {
			if p == nil || p.URL != a.URL {
				return nil, err
			}
			d.logf("订阅刷新失败,先用缓存: %v", err)
		} else {
			_, p = d.activeProfile()
			d.logf("订阅「%s」已更新:%d 个节点", a.Name, len(p.Outbounds))
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
	if s := d.getSettings(); s.Selected != "" {
		_ = d.core.Select("proxy", s.Selected) // 内核里的当前节点跟着设置走(cache_file 也会记,双保险)
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

// maybeRefresh 定时刷新当前订阅;节点变了就重连。
func (d *Daemon) maybeRefresh(ctx context.Context) {
	s := d.getSettings()
	a, p := d.activeProfile()
	if a == nil || (p != nil && p.URL == a.URL && !p.Stale(time.Duration(s.UpdateHours)*time.Hour)) {
		return
	}
	changed, err := d.refreshProfile(ctx, a.ID)
	if err != nil {
		d.logf("定时刷新订阅失败: %v", err)
		return
	}
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

// ---- 视图 ----

func (d *Daemon) profileViews() ([]ipc.ProfileView, *ipc.ProfileView) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var active *ipc.ProfileView
	out := make([]ipc.ProfileView, 0, len(d.settings.Profiles))
	for _, sp := range d.settings.Profiles {
		v := ipc.ProfileView{ID: sp.ID, Name: sp.Name, URL: sp.URL, Active: sp.ID == d.settings.ActiveProfile, Error: d.fetchErr[sp.ID]}
		if c := d.profiles[sp.ID]; c != nil {
			v.Title, v.FetchedAt, v.NodeCount, v.Tags, v.Usage = c.Title, c.FetchedAt, len(c.Outbounds), c.Tags, c.Usage
		}
		out = append(out, v)
		if v.Active {
			vv := v
			active = &vv
		}
	}
	return out, active
}

func (d *Daemon) stateView() ipc.StateView {
	s := d.getSettings()
	profiles, active := d.profileViews()
	v := ipc.StateView{Version: buildinfo.Version, Protocol: ipc.Version, State: d.machine.Snapshot(), Mode: s.Mode, Node: s.Selected, Profile: active, Profiles: profiles, Settings: s, Uptime: int64(d.core.Uptime().Seconds())}
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
	} else if active != nil && len(active.Tags) > 0 {
		v.Nodes = append([]string{"auto"}, active.Tags...)
	}
	if v.Nodes == nil {
		v.Nodes = []string{}
	}
	d.mu.Lock()
	if len(d.delays) > 0 {
		v.Delays = make(map[string]int, len(d.delays))
		for k, ms := range d.delays {
			v.Delays[k] = ms
		}
	}
	d.mu.Unlock()
	return v
}

// ---- 控制接口 ----

func (d *Daemon) registerHandlers() {
	h := d.server.Handle
	h(ipc.MPing, func(json.RawMessage) (any, error) {
		return map[string]any{"version": buildinfo.Version, "protocol": ipc.Version, "name": DisplayName}, nil
	})
	h(ipc.MGetState, func(json.RawMessage) (any, error) { return d.stateView(), nil })
	h(ipc.MGetClashInfo, func(json.RawMessage) (any, error) {
		return ipc.ClashInfo{Port: d.getSettings().ClashPort, Secret: d.secret, Running: d.core.Running()}, nil
	})
	h(ipc.MExportDiag, func(json.RawMessage) (any, error) {
		p, err := d.exportDiag()
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": p}, nil
	})
	h(ipc.MConnect, func(json.RawMessage) (any, error) {
		if a, _ := d.activeProfile(); a == nil {
			return nil, &ipc.CallError{Code: state.CodeProfileMissing, Msg: "还没有添加订阅"}
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
	h(ipc.MProbeNodes, func(json.RawMessage) (any, error) {
		_, p := d.activeProfile()
		if p == nil || len(p.Tags) == 0 {
			return nil, errors.New("还没有订阅")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		var res map[string]int
		if d.core.Running() {
			res = d.core.ProbeRunning(ctx, p.Tags, builder.TestURL)
		} else {
			res = core.Probe(ctx, p.Outbounds, p.Tags, builder.TestURL)
		}
		d.mu.Lock()
		d.delays = res
		d.mu.Unlock()
		return res, nil
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

	// ---- 订阅 ----
	h(ipc.MGetProfiles, func(json.RawMessage) (any, error) { v, _ := d.profileViews(); return v, nil })
	h(ipc.MGetProfile, func(json.RawMessage) (any, error) { _, a := d.profileViews(); return a, nil })
	h(ipc.MAddProfile, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}](p)
		if err != nil {
			return nil, err
		}
		url := strings.TrimSpace(in.URL)
		if !settings.ValidURL(url) {
			return nil, errors.New("订阅地址必须以 http:// 或 https:// 开头")
		}
		np, err := d.fetchProfile(context.Background(), url) // 先拉一次,拉不到就不加
		if err != nil {
			return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: err.Error()}
		}
		s := d.getSettings()
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = np.Title
		}
		if name == "" {
			name = fmt.Sprintf("订阅 %d", len(s.Profiles)+1)
		}
		sp := settings.Profile{ID: settings.NewID(), Name: name, URL: url}
		s.Profiles = append(s.Profiles, sp)
		first := len(s.Profiles) == 1
		if first {
			s.ActiveProfile = sp.ID
		}
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		d.setProfileCache(sp.ID, np)
		if first {
			d.machine.Restart()
		}
		views, _ := d.profileViews()
		return views, nil
	})
	h(ipc.MRemoveProfile, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			ID string `json:"id"`
		}](p)
		if err != nil {
			return nil, err
		}
		s := d.getSettings()
		kept := s.Profiles[:0:0]
		for _, sp := range s.Profiles {
			if sp.ID != in.ID {
				kept = append(kept, sp)
			}
		}
		if len(kept) == len(s.Profiles) {
			return nil, errors.New("没有这条订阅")
		}
		wasActive := s.ActiveProfile == in.ID
		s.Profiles = kept
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		d.mu.Lock()
		delete(d.profiles, in.ID)
		delete(d.fetchErr, in.ID)
		d.mu.Unlock()
		_ = os.Remove(paths.ProfileCache(in.ID))
		if wasActive {
			if len(kept) == 0 {
				d.savePersisted(persisted{Wanted: false})
				d.machine.Disconnect()
			} else {
				d.machine.Restart()
			}
		}
		views, _ := d.profileViews()
		return views, nil
	})
	h(ipc.MSelectProfile, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			ID string `json:"id"`
		}](p)
		if err != nil {
			return nil, err
		}
		s := d.getSettings()
		found := false
		for _, sp := range s.Profiles {
			if sp.ID == in.ID {
				found = true
			}
		}
		if !found {
			return nil, errors.New("没有这条订阅")
		}
		if s.ActiveProfile != in.ID {
			s.ActiveProfile = in.ID
			if err := d.setSettings(s); err != nil {
				return nil, err
			}
			d.machine.Restart()
		}
		return d.stateView(), nil
	})
	h(ipc.MRenameProfile, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
		}](p)
		if err != nil {
			return nil, err
		}
		s := d.getSettings()
		idx := -1
		for i, sp := range s.Profiles {
			if sp.ID == in.ID {
				idx = i
			}
		}
		if idx < 0 {
			return nil, errors.New("没有这条订阅")
		}
		urlChanged := false
		if n := strings.TrimSpace(in.Name); n != "" {
			s.Profiles[idx].Name = n
		}
		if u := strings.TrimSpace(in.URL); u != "" && u != s.Profiles[idx].URL {
			if !settings.ValidURL(u) {
				return nil, errors.New("订阅地址必须以 http:// 或 https:// 开头")
			}
			s.Profiles[idx].URL = u
			urlChanged = true
		}
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		if urlChanged {
			if _, err := d.refreshProfile(context.Background(), in.ID); err != nil {
				return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: err.Error()}
			}
			if s.ActiveProfile == in.ID {
				d.machine.Restart()
			}
		}
		views, _ := d.profileViews()
		return views, nil
	})
	h(ipc.MRefreshProfile, func(p json.RawMessage) (any, error) {
		in, _ := ipc.Decode[struct {
			ID string `json:"id"`
		}](p)
		id := in.ID
		if id == "" {
			id = d.getSettings().ActiveProfile
		}
		if id == "" {
			return nil, &ipc.CallError{Code: state.CodeProfileMissing, Msg: "还没有添加订阅"}
		}
		changed, err := d.refreshProfile(context.Background(), id)
		if err != nil {
			return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: err.Error()}
		}
		if changed && id == d.getSettings().ActiveProfile {
			d.machine.Restart()
		}
		views, _ := d.profileViews()
		return views, nil
	})
	// SetProfileURL 兼容命令行:有当前订阅就改它的地址,没有就新增一条
	h(ipc.MSetProfileURL, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			URL string `json:"url"`
		}](p)
		if err != nil {
			return nil, err
		}
		s := d.getSettings()
		var raw json.RawMessage
		if a := s.Active(); a != nil {
			raw, _ = json.Marshal(map[string]string{"id": a.ID, "url": in.URL})
			if _, err := d.server.Dispatch(ipc.MRenameProfile, raw); err != nil {
				return nil, err
			}
		} else {
			raw, _ = json.Marshal(map[string]string{"url": in.URL})
			if _, err := d.server.Dispatch(ipc.MAddProfile, raw); err != nil {
				return nil, err
			}
		}
		_, a := d.profileViews()
		return a, nil
	})

	// ---- 设置 ----
	h(ipc.MGetSettings, func(json.RawMessage) (any, error) { return d.getSettings(), nil })
	h(ipc.MSetSettings, func(p json.RawMessage) (any, error) {
		next, err := ipc.Decode[settings.Settings](p)
		if err != nil {
			return nil, err
		}
		prev := d.getSettings()
		next.Schema = settings.Schema
		next.Profiles, next.ActiveProfile = prev.Profiles, prev.ActiveProfile // 订阅另有接口管,这里不动
		if err := d.setSettings(next); err != nil {
			return nil, err
		}
		if d.core.Running() {
			live := prev // 模式与节点是运行时可改的,别的都要重新生成配置
			live.Mode, live.Selected = next.Mode, next.Selected
			if !reflect.DeepEqual(live, next) {
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
		_, p := d.activeProfile()
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
