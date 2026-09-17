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
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

const DisplayName = buildinfo.DisplayName

type Daemon struct {
	mu         sync.Mutex
	log        *logx.Rotator
	coreLog    *logx.Rotator
	coreLevel  atomic.Int32 // 内核日志记到哪一级(core.Writer 按它过滤)
	nicOff     atomic.Bool  // 各网卡的 IPv6 绑定已由我们停掉、还没还原(和闸一样,跨内核重启 / 服务重启一直有效)
	nicMu      sync.Mutex   // syncNICIPv6 串行化:停用那一步要起 PowerShell,不能两个一起跑
	probeMu    sync.Mutex
	probeAt    time.Time // auto 组上次测完一轮全部节点的时间,autoProbeLoop 按它算下一次
	core       *core.Core
	settings   settings.Settings
	profiles   map[string]*profile.Profile // 订阅 id → 节点缓存
	fetchErr   map[string]string           // 订阅 id → 最近一次拉取失败原因
	fetchLink  map[string]string           // 订阅 id → 拉取失败(404)时面板随响应给的「选购 / 续费」地址
	running    *profile.Profile            // 正在跑的内核是按哪份订阅生成的;刷新后拿它和缓存比,决定动不动隧道
	prepared   *profile.Profile            // prepare 刚按它生成了配置、内核还没起:start 成功后转成 running
	guardOn    bool                        // 「全局禁直连」的闸此刻开着
	guardErr   string                      // 闸该开却没开成的原因
	guardMu    sync.Mutex                  // syncGuard 整段串行:判断 + 开 / 撤要一气呵成
	releaseTun func()                      // 见 Options.ReleaseTun
	machine    *state.Machine
	server     *ipc.Server
	secret     string
	http       *http.Client
	delays     map[string]int // 最近一次全节点测速结果
	ping       int            // 当前节点最近一次测得的延迟(毫秒):健康检查本来就要测一次,顺手记下来给界面用
	exitIP     string         // 经当前节点出去时对外露出的地址
	exitLoc    string         // 出口所在国家的两位代码
	exitCity   string         // 出口所在城市
	exitRegion string         // 出口所在一级行政区
	exitISP    string         // 出口那条线路的运营商 / 机房
	exitNode   string         // 上面那些是哪个节点测出来的:自动选择在后台换了节点就得重测
	exitAt     time.Time      // 上次测出口的时间:节点没变也隔一阵子复查一次
	exitGen    uint64         // 出口查询的代数:换节点 / 重连就加一,慢的旧查询回来发现代数变了就丢弃,不会把旧节点的出口盖到新节点上
	noListen   bool
}

type persisted struct {
	Wanted bool `json:"wanted"`
}

// Options 各平台不同的装配项。
type Options struct {
	Platform adapter.PlatformInterface // Android:TUN、网络接口、连接归属由宿主提供
	NoListen bool                      // 不开本机控制口(Android 只在进程内 Dispatch)
	// ReleaseTun Android:「全局禁直连」撤闸时把留着的 VPN 接口关掉(内核没在跑时它是个黑洞);别的平台为 nil
	ReleaseTun func()
}

// New 读设置与订阅缓存,装配各部件;不启动任何东西。
func New() (*Daemon, error) { return NewWithOptions(Options{}) }

// NewWithOptions 带平台选项的装配。
func NewWithOptions(o Options) (*Daemon, error) {
	if err := paths.Ensure(); err != nil {
		return nil, fmt.Errorf("建数据目录: %w", err)
	}
	d := &Daemon{
		noListen: o.NoListen, releaseTun: o.ReleaseTun,
		log:      logx.New(filepath.Join(paths.Logs(), "service.log"), 5<<20, 3),
		coreLog:  logx.New(filepath.Join(paths.Logs(), "core.log"), 5<<20, 3),
		http:     &http.Client{Timeout: 30 * time.Second},
		profiles: map[string]*profile.Profile{},
		fetchErr: map[string]string{}, fetchLink: map[string]string{},
	}
	d.coreLevel.Store(core.LevelOf(d.settings.LogLevel))
	d.core = core.New(core.Writer{Printf: d.coreLog.Printf, Level: &d.coreLevel})
	if o.Platform != nil {
		d.core.SetPlatform(o.Platform)
	}
	s, err := settings.Load(paths.Settings())
	if err != nil {
		d.logf("设置加载失败,用默认值: %v", err)
	}
	d.settings = s
	d.applyLogRetention()
	d.loadProfileCaches()
	// 上次异常退出可能留下改过的系统设置(macOS 接管的系统 DNS、Linux 加的策略路由、网卡上被停用的 IPv6),启动时先还原一次,
	// 免得服务没连上、机器却因为 DNS 指着不存在的隧道打不开网页。
	netmode.Unprotect()
	// 网卡上被停用的 IPv6 不在这里无条件还原:它和「全局禁直连」的闸一样是持久的,上次连着关的机就该一直关着
	// (见 reconcileNICIPv6)。在这儿还原的话,开机到服务重新关上它之间,公网 v6 地址就白白露了几十秒。
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	d.secret = hex.EncodeToString(b)
	d.machine = state.New(state.Deps{
		Prepare: d.prepare,
		Start:   d.start,
		Stop:    d.stop,
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
// 顺带自愈:0.6.0-a2 及以前导出诊断包会把设置里的订阅链接改成脱敏形式(.../sub/***)并可能存进磁盘,
// 之后刷新一律 404。缓存里存着拉取成功时的原始链接,能对上就把设置改回去。
func (d *Daemon) loadProfileCaches() {
	var healed bool
	for i, p := range d.settings.Profiles {
		c, err := profile.Load(paths.ProfileCache(p.ID))
		if err != nil {
			continue
		}
		d.profiles[p.ID] = c
		if strings.Contains(p.URL, "***") && c.URL != "" && !strings.Contains(c.URL, "***") {
			d.settings.Profiles[i].URL = c.URL
			healed = true
			d.logf("订阅「%s」的链接曾被脱敏写坏,已按缓存恢复", p.Name)
		}
	}
	if healed {
		if err := d.settings.Save(paths.Settings()); err != nil {
			d.logf("恢复订阅链接后写设置失败: %v", err)
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

// Close 放掉守护进程占着的文件(两份日志)。Run 结束之后调;进程要退出时不调也行,系统会收。
func (d *Daemon) Close() error {
	err := d.log.Close()
	if err2 := d.coreLog.Close(); err == nil {
		err = err2
	}
	return err
}

// Run 起控制接口,按上次状态自动连接,定时刷新订阅;ctx 结束时全部停掉。
func (d *Daemon) Run(ctx context.Context) error {
	d.logf("%s 服务启动 v%s (%s/%s)", DisplayName, buildinfo.Version, runtime.GOOS, runtime.GOARCH)
	if !d.noListen {
		if err := d.server.Listen(); err != nil {
			return fmt.Errorf("监听控制管道: %w", err)
		}
	}
	d.reconcileGuard()
	d.reconcileNICIPv6()
	go d.autoProbeLoop(ctx)
	if netmode.NICIPv6Manageable() {
		go d.nicIPv6Loop(ctx)
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
			// 服务停止 / 关机:只停内核,闸不撤。撤闸只认四件事(点断开、切模式、关开关、卸载),
			// 那些都各自走处理器;这里要是也撤,net stop、关机、升级换文件的空档就全是直连,
			// 开机那组过滤器也会跟着被删掉,"持久"就成了空话。上次连着关的机,下次启动会自动重连。
			d.logf("服务停止(闸留着,下次启动接着用)")
			d.machine.Disconnect()
			_ = d.server.Close()
			d.log.Close()
			d.coreLog.Close()
			return nil
		case <-t.C:
			d.maybeRefresh(ctx)
			d.deviceRescan()
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

// Dispatch 进程内直接调控制口方法(Linux 的 Web 面板用,不经 socket)。
func (d *Daemon) Dispatch(method string, params json.RawMessage) (any, error) {
	return d.server.Dispatch(method, params)
}

// Methods 控制口已注册的方法名。
func (d *Daemon) Methods() map[string]bool { return d.server.Methods() }

// Logf 写服务日志(外层组件用)。
func (d *Daemon) Logf(format string, a ...any) { d.logf(format, a...) }

// getSettings 返回设置的深拷贝:切片字段(订阅、规则组、设备…)不能和运行中的设置共享底层数组,
// 否则拿到副本的人一改(比如诊断包把订阅链接脱敏成 /sub/***)就把真实设置改坏了。
func (d *Daemon) getSettings() settings.Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings.Clone()
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
	delete(d.fetchLink, id)
	d.mu.Unlock()
	if err := p.Save(paths.ProfileCache(id)); err != nil {
		d.logf("写订阅缓存失败: %v", err)
	}
}

func (d *Daemon) noteFetchErr(id string, err error) {
	// 404 的错误文本末尾可能挂着面板给的续费地址:拆下来单独记,给人看的文本不带它
	text, link := profile.SplitRenew(err.Error())
	d.mu.Lock()
	d.fetchErr[id] = text
	if link != "" {
		d.fetchLink[id] = link
	}
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
		// 内容解析不了换路径也没用;"订阅无效"(404)经代理拿到的不算数——中间可能是别的服务器在应答,直连确认过才算真的失效
		if state.CodeOf(err) == state.CodeProfileParse || (state.CodeOf(err) == state.CodeProfileAuth && tag == "direct") {
			return nil, err
		}
		d.logf("订阅经 %s 拉取失败: %v", tag, err)
		last = err
		if ctx.Err() != nil || i == len(routes)-1 {
			break
		}
		select { // 隔两秒再试下一条路径;调用方等不及了就别再往下试
		case <-ctx.Done():
			return nil, last
		case <-time.After(2 * time.Second):
		}
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
	if strings.Contains(url, "***") { // 老版本导出诊断包会把内存里的链接脱敏成 /sub/***,存下来就再也拉不到了
		err := state.Errf(state.CodeProfileURL, "订阅链接不完整(含 ***),请到订阅管理重新填写这条链接")
		d.noteFetchErr(id, err)
		return false, err
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
	d.coreLevel.Store(core.LevelOf(s.LogLevel)) // 内核日志按设置的级别写,改了设置下次连接生效
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
	d.syncGuard()   // 闸要在内核起来之前就到位:准备阶段本身可能要几秒,这几秒也不许漏
	d.syncNICIPv6() // 同理;而且改网卡绑定会让网卡重走一遍协议栈,放在内核启动之前才不会去抖刚建好的隧道
	if s.NetMode == settings.NetGateway {
		d.resolveDeviceIPs(&s) // 设备策略按当前 IP 生效
	}
	cfg, err := builder.Build(builder.Input{Profile: p, Settings: s, DataDir: paths.DataDir(), ClashSecret: d.secret, RuleSetDir: paths.RuleSets(),
		NodeIPs: resolveNodeHosts(ctx, p)})
	if err != nil {
		return nil, state.Errf(state.CodeConfig, "生成配置: %v", err)
	}
	if err := d.core.Validate(cfg); err != nil {
		return nil, state.Errf(state.CodeConfig, "配置校验失败: %v", err)
	}
	_ = os.WriteFile(paths.Config(), cfg, 0o600)
	// 只记"这份配置是按哪份订阅备的";要等 start 成功才算"内核在用"。Restart 是先备后停再起,
	// 停那一步会把 running 清掉,这里直接写 running 的话起来之后就是 nil —— 之后刷新发现当前节点
	// 被删也不会重连、选到参数变了的节点也不会重建(m14 到 m19 都有这毛病)。
	d.mu.Lock()
	d.prepared = p
	d.mu.Unlock()
	return cfg, nil
}

// start 启动内核;失败按原因归类。
func (d *Daemon) start(cfg []byte) error {
	// 网卡 IPv6 的停用不在这里做:它跟的是"用户想不想连着"而不是"内核在不在跑"(见 syncNICIPv6),
	// prepare() 里已经对齐过了 —— 那也正好在内核启动之前,改协议绑定会让网卡重新走一遍协议栈,不该去抖刚建好的隧道。
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
	d.mu.Lock()
	d.running = d.prepared // 起来了,这份订阅才是内核在用的
	d.mu.Unlock()
	d.markProbed() // sing-box 启动时(PostStart)自己会把 auto 组全测一轮,定时测速从这时候起算
	d.guardTunUp()
	s := d.getSettings()
	if s.Selected != "" {
		_ = d.core.Select("proxy", s.Selected) // 内核里的当前节点跟着设置走(cache_file 也会记,双保险)
	}
	// 连上就先测一次当前节点,再查一次出口地址:首页那两项要马上有数,
	// 不然延迟得等到第一次健康检查(三分钟后),出口地址则一直空着。
	// (自动选择时不用在这里叫 auto 组测一轮:sing-box 的 urltest 组 PostStart 会自己把全部成员测一遍。)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if ms, err := d.core.URLTest(ctx, "proxy", builder.TestURL); err == nil {
			d.setPing(ms)
		}
		d.refreshExit(d.beginExit(d.currentNode()))
	}()
	if s.TUN {
		// Linux:本机对外服务(SSH 等)的回包不能进 TUN,否则一连上远程管理就断;Windows 上是空操作
		if err := netmode.Protect(builder.TunName, s.IPv6); err != nil {
			d.logf("保护本机服务回包的路由规则失败: %v", err)
		}
		if s.NetMode == settings.NetGateway {
			if err := netmode.ApplyGateway(builder.TunName, lanInterfaces(), s.DNSHijack); err != nil {
				d.logf("网关模式的 DNS 劫持规则失败(局域网设备的 DNS 不会被接管): %v", err)
			}
		}
	}
	return nil
}

// stop 先停内核(TUN 随之消失),再撤路由规则;顺序反了会有一瞬间 TUN 还在而规则没了,远程会话可能掉。
func (d *Daemon) stop() error {
	d.setPing(0)
	d.clearExit()
	d.mu.Lock()
	d.running = nil
	d.mu.Unlock()
	err := d.core.Stop()
	// 网卡 IPv6 不在这里还原:内核停了不代表用户不想连了(崩了在退避重试、切订阅重连、服务被杀、关机),
	// 这些时候地址一冒出来就能被程序读走。只有用户真的断开 / 关掉开关 / 卸载才还原,和闸一个道理。
	netmode.Unprotect()
	netmode.ClearGateway()
	return err
}

// health 经代理测一次;内核不在了报崩溃码,让状态机重连。
// 测出来的毫秒数顺手记下来:首页要显示当前节点的延迟,这一发本来就要打,不用再单独测。
func (d *Daemon) health(ctx context.Context) error {
	if !d.core.Running() {
		return state.Errf(state.CodeCoreCrash, "内核未运行")
	}
	ms, err := d.core.URLTest(ctx, "proxy", builder.TestURL)
	if err != nil {
		d.setPing(0)
		return state.Errf(state.CodeNodeDown, "当前节点不可用: %v", err)
	}
	d.setPing(ms)
	// 出口跟着"此刻实际在用哪个节点"走。自动选择模式下内核会自己切到更快的一个,
	// 切了出口多半就变了,界面上却还挂着上一条线路的地址 —— 所以这里按节点名比一比,
	// 变了就重测;第一次没查着(刚连上线路还没热)也在这儿补。
	now := d.currentNode()
	d.mu.Lock()
	need := d.exitIP == "" || d.exitNode != now || time.Since(d.exitAt) > exitMaxAge
	d.mu.Unlock()
	if need {
		go d.refreshExit(d.beginExit(now))
	}
	return nil
}

// restart 重建配置重连:内核停了再起,隧道要断几秒,所以能就地改的(切节点、自动 / 手动、切模式、定时测速)
// 都不走这里。网卡的 IPv6 绑定期间一直关着不动 —— 停 / 起各改一次协议绑定,网卡各重走一遍协议栈,
// 三秒的重连会拖成六七秒,局域网也跟着抖;而且那几秒地址露出来就能被读走。
func (d *Daemon) restart() error {
	return d.machine.Restart()
}

// wantNICOff 这份设置要不要停掉各网卡的 IPv6 协议绑定。
// 安卓上应用没权限动物理网卡,整套逻辑在那边一律不做(NICIPv6Manageable 为假),
// 免得记假账、打"已停用"的假日志,又对着关不掉的移动网络反复重试。
func wantNICOff(s settings.Settings) bool {
	return netmode.NICIPv6Manageable() && s.TUN && !s.IPv6 && s.DisableNICIPv6
}

// nicIPv6Wanted 网卡的 IPv6 此刻该不该关着。**和「全局禁直连」的闸同一套判断**:设置要求关、且用户想连着
// (没点断开)。内核停了、崩了在重试、切订阅在重连、服务被杀、关机重启,只要这两条还成立就得一直关着 ——
// 挡数据包挡不住"程序枚举网卡读走地址再报出去",地址一旦冒出来,哪怕只有几十秒也够被读走留到以后用。
func (d *Daemon) nicIPv6Wanted() bool {
	return wantNICOff(d.getSettings()) && d.machine.Wanted()
}

// nicAction 该对网卡 IPv6 做什么。
type nicAction int

const (
	nicNoop    nicAction = iota // 现状已经对了,别动(停用 / 还原都要起子进程,很贵)
	nicDisable                  // 该关还没关
	nicRestore                  // 不该关了,按动手前的状态还原
)

// nicIPv6Action 抽成纯函数是为了能穷举测试:这套判断错一格,轻则用户的 IPv6 永久回不来,
// 重则该藏起来的公网地址露在外面 —— 两种都是不能靠"看着像对的"来保证的。
//
//	want = 此刻该不该关着(设置要求关 + 用户想连着 + 这个平台动得了物理网卡)
//	on   = 现在是不是我们关着的
func nicIPv6Action(want, on bool) nicAction {
	switch {
	case want && !on:
		return nicDisable
	case !want && on:
		return nicRestore
	default:
		return nicNoop
	}
}

// syncNICIPv6 把网卡 IPv6 的状态和"该不该关"对齐;幂等。连接意愿、设置变了都要调一次。
// 停用 / 还原都要起 PowerShell(Windows)或改 sysctl,挺慢,所以用 nicOff 记着当前状态,状态没变就什么都不做。
func (d *Daemon) syncNICIPv6() {
	d.nicMu.Lock()
	defer d.nicMu.Unlock()
	switch nicIPv6Action(d.nicIPv6Wanted(), d.nicOff.Load()) {
	case nicDisable:
		if err := netmode.DisableNICIPv6(builder.TunName); err != nil {
			d.logf("停用网卡 IPv6 失败(不影响连接,但网卡上的公网 IPv6 地址还在): %v", err)
			return
		}
		d.nicOff.Store(true)
	case nicRestore:
		netmode.RestoreNICIPv6()
		d.nicOff.Store(false)
		d.logf("网卡 IPv6:已按动手前的状态还原")
	}
}

// reconcileNICIPv6 启动时核对一次,和 reconcileGuard 对称:上次连着关的机就接着关着,否则还原。
// 机器刚启动时状态机还没 Connect,所以这里看的是落盘的连接意愿而不是 machine.Wanted()。
func (d *Daemon) reconcileNICIPv6() {
	d.nicMu.Lock()
	defer d.nicMu.Unlock()
	d.nicOff.Store(netmode.NICIPv6Off()) // 有备份就说明上次关过还没还原
	on := d.nicOff.Load()
	// 机器刚启动时状态机还没 Connect,所以这里用落盘的连接意愿代替 machine.Wanted()
	want := d.loadPersisted().Wanted && wantNICOff(d.getSettings())
	switch nicIPv6Action(want, on) {
	case nicRestore:
		netmode.RestoreNICIPv6()
		d.nicOff.Store(false)
		d.logf("网卡 IPv6:上次不是连着关的机(或设置已关掉),已还原")
		return
	case nicNoop:
		if !want {
			return // 本来就不该关,也没关着
		}
		// 该关、也记着关过了:再看一眼真没漏(比如关机期间插了张新网卡),没漏就不必再跑一遍那段慢脚本
		if !netmode.NICIPv6Leaking(builder.TunName) {
			d.logf("网卡 IPv6:上次连着关的机,一直关着")
			return
		}
	}
	if err := netmode.DisableNICIPv6(builder.TunName); err != nil {
		d.logf("停用网卡 IPv6 失败(不影响连接,但网卡上的公网 IPv6 地址还在): %v", err)
		return
	}
	d.nicOff.Store(true)
	d.logf("网卡 IPv6:已停用(上次连着关的机)")
}

// groupTest 让 auto 组测一轮全部节点并换到最快的,记下时间。sing-box 自己的定时测速在配置里关掉了
// (builder.autoGroup),什么时候测由这里叫:切到自动选择时立刻一次,之后 autoProbeLoop 按"定时测速(分钟)"来。
func (d *Daemon) groupTest(ctx context.Context) {
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := d.core.GroupTest(tctx, "auto"); err != nil {
		d.logf("自动选择组测速失败: %v", err)
		return
	}
	d.markProbed()
}

func (d *Daemon) markProbed() {
	d.probeMu.Lock()
	d.probeAt = time.Now()
	d.probeMu.Unlock()
}

// autoProbeLoop "定时测速(分钟)":自动选择时每隔这么久让 auto 组测一轮全部节点并换到最快的。手动指定节点时不测
// (后台每几分钟把上百个节点全连一遍没有意义,打开节点列表时会现测)。以前这个间隔写在 auto 组的配置里,
// 自动 / 手动之间切一次就得重建配置重连,断几秒网;现在配置不随模式变,切换就地完成,改间隔也即刻生效。
func (d *Daemon) autoProbeLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := d.getSettings()
		if s.Selected != "" || !d.core.Running() {
			continue
		}
		every := time.Duration(s.ProbeMinutes) * time.Minute
		if every < time.Minute {
			every = 3 * time.Minute
		}
		d.probeMu.Lock()
		due := time.Since(d.probeAt) >= every
		d.probeMu.Unlock()
		if due {
			d.groupTest(ctx)
		}
	}
}

// nicIPv6Loop 连接期间盯着网卡:新插一张网卡、开个热点、起个虚拟机,那张新网卡上的 IPv6 没人管,
// 公网 v6 地址就又能被程序读走了(数据包有闸挡着不会真漏流量,但"地址读不到"才是这个功能的意义)。
// 停用那一步要起 PowerShell,挺贵;所以先用标准库枚举一遍地址,真发现漏了才去跑。
func (d *Daemon) nicIPv6Loop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	// 关不掉的情况是存在的(权限不够、或者某些虚拟网卡自己又开回来)。真碰上就别每半分钟白跑一次
	// PowerShell 还把日志刷满:连着几轮没治好就退避,只在第一次和恢复时各说一句。
	fails, quiet := 0, false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !d.nicIPv6Wanted() {
			fails, quiet = 0, false
			continue
		}
		if !netmode.NICIPv6Leaking(builder.TunName) {
			if quiet {
				d.logf("网卡的 IPv6 地址没有了,恢复正常盯着")
			}
			fails, quiet = 0, false
			continue
		}
		if quiet && fails%20 != 0 { // 退避后改成每 10 分钟试一次
			fails++
			continue
		}
		if !quiet {
			d.logf("发现网卡上又有公网 IPv6 地址(多半是新接了一张网卡),重新停用")
		}
		d.nicMu.Lock()
		err := netmode.DisableNICIPv6(builder.TunName)
		d.nicMu.Unlock()
		if err != nil {
			d.logf("停用新网卡的 IPv6 失败: %v", err)
		} else {
			d.nicOff.Store(true)
		}
		fails++
		if fails >= 3 && !quiet {
			quiet = true
			d.logf("连着几次都没能停掉这张网卡的 IPv6,改成每 10 分钟再试;先去设置里看看「连接时停用网卡 IPv6」这一项")
		}
	}
}

// currentNode 内核里 proxy 组此刻实际落在哪个节点;自动选择时是 auto 组选中的那个。
func (d *Daemon) currentNode() string {
	now, _, err := d.core.Group("proxy")
	if err != nil {
		return ""
	}
	if now == "auto" {
		if inner, _, err := d.core.Group("auto"); err == nil {
			return inner
		}
	}
	return now
}

func (d *Daemon) setPing(ms int) {
	d.mu.Lock()
	d.ping = ms
	d.mu.Unlock()
}

// 查出口用的两个地址。两个都是"对方看到的你是谁",不需要我再拿这个 IP 去别处查一次归属地,
// 也不用申请密钥。请求都经当前节点发出去,对方看到的是节点的出口地址,不是用户本机的。
//
//	ipwho.is  一次 JSON,除了地址还给国家 / 一级行政区 / 城市 / 运营商,首页那一行要的就是它;
//	cdn-cgi/trace  Cloudflare 自家的诊断端点,只给地址与国家代码,但几乎不会连不上,当兜底。
//
// 前者不通(限额、被墙、超时)就退到后者;两个都不通就什么都不记,界面上那一行显示"正在查出口",
// 下一轮健康检查再来一次 —— 查不到不算错,不弹提示也不反复重试骚扰。
const (
	exitWhoURL   = "https://ipwho.is/"
	exitTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
)

// exitInfo 一次查询的结果;ip 为空表示这次没查着。
type exitInfo struct {
	ip, loc, city, region, isp string
}

// refreshExit 经当前节点查一次出口,首页要显示"我现在从哪儿出去"。
// 节点名写的是机房位置,真正的出口未必在那儿(节点自己再套一层就不是了),所以这个值得单独查。
// beginExit 开始一轮对 node 的出口查询:记下是哪个节点、代数加一,返回这一轮的代数。
// 连接、切节点、健康检查各自都会起查询,先起的那个可能后回来 —— 没有代数的话它会把旧节点的
// 出口盖到新节点头上,而健康检查看到"节点对得上、地址也有、时间也新"就十分钟不再重测。
func (d *Daemon) beginExit(node string) uint64 {
	d.mu.Lock()
	d.exitGen++
	d.exitNode = node
	gen := d.exitGen
	d.mu.Unlock()
	return gen
}

// refreshExit 查一次出口;只有代数还是 gen(中途没换节点、没断开)才把结果写进去。
func (d *Daemon) refreshExit(gen uint64) {
	c, err := d.core.HTTPClient("proxy", 10*time.Second)
	if err != nil {
		return
	}
	info := d.exitFromWho(c)
	if info.ip == "" {
		info = d.exitFromTrace(c)
	}
	if info.ip == "" {
		return
	}
	d.mu.Lock()
	if d.exitGen == gen {
		d.exitIP, d.exitLoc, d.exitCity, d.exitRegion, d.exitISP = info.ip, info.loc, info.city, info.region, info.isp
		d.exitAt = time.Now()
	}
	d.mu.Unlock()
}

func (d *Daemon) exitGet(c *http.Client, url string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return b
}

func (d *Daemon) exitFromWho(c *http.Client) exitInfo {
	b := d.exitGet(c, exitWhoURL)
	if len(b) == 0 {
		return exitInfo{}
	}
	var r struct {
		Success     bool   `json:"success"`
		IP          string `json:"ip"`
		CountryCode string `json:"country_code"`
		Region      string `json:"region"`
		City        string `json:"city"`
		Connection  struct {
			ISP string `json:"isp"`
			Org string `json:"org"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.IP == "" {
		return exitInfo{}
	}
	isp := r.Connection.ISP
	if isp == "" {
		isp = r.Connection.Org
	}
	return exitInfo{ip: r.IP, loc: r.CountryCode, city: r.City, region: r.Region, isp: isp}
}

func (d *Daemon) exitFromTrace(c *http.Client) exitInfo {
	b := d.exitGet(c, exitTraceURL)
	if len(b) == 0 {
		return exitInfo{}
	}
	var out exitInfo
	for _, ln := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(ln, "ip="); ok {
			out.ip = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(ln, "loc="); ok {
			out.loc = strings.TrimSpace(v)
		}
	}
	return out
}

// clearExit 断开或换节点时先把旧的出口信息抹掉,免得界面上挂着上一个节点的地址。
func (d *Daemon) clearExit() {
	d.mu.Lock()
	d.exitIP, d.exitLoc, d.exitCity, d.exitRegion, d.exitISP, d.exitNode = "", "", "", "", "", ""
	d.exitAt = time.Time{}
	d.exitGen++ // 还在路上的查询作废
	d.mu.Unlock()
}

// exitMaxAge 节点没换也隔这么久复查一次:节点自己的上游偶尔会变,总不能一直挂着旧地址。
// 不做得更勤是因为没必要 —— 出口真变了几乎都是因为换了节点,而换节点是立刻就重测的。
const exitMaxAge = 10 * time.Minute

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
		d.afterRefresh(a.ID)
	}
}

// resolveNodeHosts 把订阅里用域名写的节点服务器解析成地址,给"节点服务器直连"那条规则兜底。
//
// 用域名写的节点,在隧道里是靠嗅探到的 SNI 命中直连规则的;不带 TLS 的协议嗅不出域名,那时就只能按地址认。
// 解析不出来不算错(退回只按域名匹配),所以整体给一个短超时,不让它拖慢连接。
func resolveNodeHosts(ctx context.Context, p *profile.Profile) map[string][]string {
	if p == nil {
		return nil
	}
	hosts := map[string]bool{}
	for _, hp := range p.Servers() {
		h := hp
		if i := strings.LastIndex(h, ":"); i > 0 {
			h = h[:i]
		}
		if h == "" || settings.IsIP(h) {
			continue // 本来就是地址,规则里已经按地址写了
		}
		hosts[h] = true
	}
	if len(hosts) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out := map[string][]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for h := range hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			addrs, err := net.DefaultResolver.LookupHost(ctx, h)
			if err != nil || len(addrs) == 0 {
				return
			}
			mu.Lock()
			out[h] = addrs
			mu.Unlock()
		}(h)
	}
	wg.Wait()
	if len(out) == 0 {
		return nil
	}
	return out
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
			v.WebPage = c.WebPage
		}
		if l := d.fetchLink[sp.ID]; l != "" {
			v.WebPage = l // 缓存里的可能是旧的,面板刚随 404 给的更准;订阅到期后也照样有续费入口
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
		if now, _, err := d.core.Group("proxy"); err == nil {
			v.Node = now
		}
		if now, _, err := d.core.Group("auto"); err == nil {
			v.AutoNow = now
		}
	}
	// 节点列表直接来自最新的订阅缓存,不来自内核:刷新拿到的增删立刻反映在列表里,正在用的连接一点不动。
	// 列表里内核还没有的节点(刚刷新加进来的)、或参数已经变了的,选中时再重建配置(见 MSelectNode)。
	if active != nil && len(active.Tags) > 0 {
		v.Nodes = append([]string{"auto"}, active.Tags...)
	} else if d.core.Running() {
		if _, all, err := d.core.Group("proxy"); err == nil {
			v.Nodes = all
		}
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
	v.Ping, v.ExitIP, v.ExitLoc = d.ping, d.exitIP, d.exitLoc
	v.ExitCity, v.ExitRegion, v.ExitISP = d.exitCity, d.exitRegion, d.exitISP
	if d.guardOn {
		v.Guard = "on"
	}
	v.GuardError = d.guardErr
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
		d.syncGuard()
		go d.syncNICIPv6() // 起 PowerShell 要一两秒,别把这次调用卡住;prepare 里还会再对齐一次
		return d.stateView(), nil
	})
	h(ipc.MDisconnect, func(json.RawMessage) (any, error) {
		d.savePersisted(persisted{Wanted: false})
		d.machine.Disconnect()
		d.syncGuard()
		go d.syncNICIPv6() // 断开才还原网卡 IPv6;慢活扔后台,别卡住这次调用
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
		d.syncGuard()
		go d.syncNICIPv6()
		if d.core.Running() {
			if err := d.core.SetMode(builder.ModeName(s.Mode)); err != nil {
				return nil, err
			}
			// 模式同样只对新连接生效:切成直连了,已经在代理里的连接还在代理里走,得掐掉重来
			if err := d.core.CloseAllConnections(); err != nil {
				d.logf("切模式后掐断旧连接失败(旧连接会继续按老模式走): %v", err)
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
		s := d.getSettings()
		s.Selected = tag
		if tag == "auto" {
			s.Selected = ""
		}
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		if !d.core.Running() {
			return d.stateView(), nil
		}
		// 自动 / 手动之间切换也是就地换:定时测速不再写在配置里(autoProbeLoop 按设置叫),配置不随模式变,
		// 不用重建重连。以前这里要重启内核,每切一次断几秒网。
		// 选的节点内核里还没有(刚刷新加进来的)、或者参数已经跟内核用的那份不一样:就地换不了,重建配置重连
		if d.needRebuildFor(s.Selected) {
			if err := d.restart(); err != nil {
				return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: "切换失败,保持当前连接: " + err.Error()}
			}
			return d.stateView(), nil
		}
		if err := d.core.Select("proxy", tag); err != nil {
			return nil, err
		}
		// 选择组只对新连接生效:不掐掉老连接,用户会看到"选了新节点,连接列表里还是老节点",
		// 长连接(Telegram、推送、anytls 的连接池)能挂十几分钟不断。掐掉后应用自己重连,就都走新节点了。
		if err := d.core.CloseAllConnections(); err != nil {
			d.logf("切节点后掐断旧连接失败(旧连接会继续用老节点): %v", err)
		}
		// 换了节点,出口多半也变了:先抹掉旧值,再在后台重新测延迟与出口
		d.clearExit()
		d.setPing(0)
		go func() {
			// 切到自动选择:先让 auto 组现测一轮换到最快的,首页的延迟 / 出口才是新节点的
			if s.Selected == "" {
				d.groupTest(context.Background())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			if ms, err := d.core.URLTest(ctx, "proxy", builder.TestURL); err == nil {
				d.setPing(ms)
			}
			d.refreshExit(d.beginExit(d.currentNode()))
		}()
		return d.stateView(), nil
	})
	h(ipc.MProbeNodes, func(json.RawMessage) (any, error) {
		_, p := d.activeProfile()
		if p == nil || len(p.Tags) == 0 {
			return nil, errors.New("还没有订阅")
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeBudget(len(p.Tags)))
		defer cancel()
		// 测出一个就写进去一个:界面在测速期间会轮询 GetNodes,这样延迟是一个一个冒出来的,
		// 而不是干等好几秒然后整列一起亮。
		set := func(tag string, ms int) {
			d.mu.Lock()
			if d.delays == nil {
				d.delays = map[string]int{}
			}
			d.delays[tag] = ms
			d.mu.Unlock()
		}
		var res map[string]int
		if d.core.Running() {
			// 已连接:只测内核里有的(经出站做 URL 测试)。列表来自订阅缓存,刚刷新加进来的节点内核里还没有,
			// 这些不测、也不给数 —— 全局模式下守护进程自己的探测包会进 TUN,量出来是假"不通"。选它连上后自然有数。
			in, _ := d.splitByCore(p)
			res = d.core.ProbeRunning(ctx, in.Tags, builder.TestURL, set)
		} else {
			res = probeDirect(ctx, p, set) // 未连接:直连量到节点服务器的往返
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
		if in.Tag != "proxy" && d.core.Running() && !d.inCore(in.Tag) {
			// 刚刷新加进来、内核里还没有的节点:全局模式下守护进程自己的探测包会进 TUN,量不准,不如说清楚
			return nil, errors.New("这个节点是刚刷新加进来的,内核里还没有;选它连上后再测")
		}
		ms, err := d.core.URLTest(context.Background(), in.Tag, builder.TestURL)
		if err == nil && in.Tag == "proxy" {
			d.setPing(ms)
		}
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
			_ = d.restart() // 还没连着,只是把想连的状态接上;真要连是用户点连接
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
		delete(d.fetchLink, in.ID)
		d.mu.Unlock()
		_ = os.Remove(paths.ProfileCache(in.ID))
		if wasActive {
			if len(kept) == 0 {
				d.savePersisted(persisted{Wanted: false})
				d.machine.Disconnect()
				d.syncGuard()
				go d.syncNICIPv6()
			} else if err := d.restart(); err != nil {
				d.logf("删掉当前订阅后切换失败: %v", err)
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
			if err := d.restart(); err != nil {
				return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: "切换订阅失败,保持当前连接: " + err.Error()}
			}
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
				if err := d.restart(); err != nil {
					return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: "新地址的配置生成失败,保持当前连接: " + err.Error()}
				}
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
		if changed {
			d.afterRefresh(id)
		} else {
			d.logf("订阅刷新:无变化")
		}
		views, _ := d.profileViews()
		return views, nil
	})
	// ApplyProfile 把刷新后还没用上的节点列表用起来:用户明确点的「现在应用」,会重连
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

	// ---- 局域网设备(网关模式) ----
	h(ipc.MGetDevices, func(json.RawMessage) (any, error) { return d.deviceViews(), nil })
	h(ipc.MSetDevice, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			MAC  string `json:"mac"`
			IP   string `json:"ip"`
			Name string `json:"name"`
			Mode string `json:"mode"`
		}](p)
		if err != nil {
			return nil, err
		}
		mac := settings.NormalizeMAC(in.MAC)
		if mac == "" {
			return nil, errors.New("MAC 地址无效")
		}
		s := d.getSettings()
		found := false
		for i := range s.Devices {
			if s.Devices[i].MAC == mac {
				s.Devices[i].Name, s.Devices[i].Mode, found = in.Name, in.Mode, true
				if in.IP != "" {
					s.Devices[i].IP = in.IP
				}
			}
		}
		if !found {
			s.Devices = append(s.Devices, settings.Device{MAC: mac, IP: in.IP, Name: in.Name, Mode: in.Mode})
		}
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		if d.core.Running() && s.NetMode == settings.NetGateway {
			if err := d.restart(); err != nil {
				d.logf("重新应用配置失败,保持当前连接: %v", err)
			}
		}
		return d.deviceViews(), nil
	})
	h(ipc.MRemoveDevice, func(p json.RawMessage) (any, error) {
		in, err := ipc.Decode[struct {
			MAC string `json:"mac"`
		}](p)
		if err != nil {
			return nil, err
		}
		mac := settings.NormalizeMAC(in.MAC)
		s := d.getSettings()
		kept := s.Devices[:0]
		removedPolicy := false
		for _, dev := range s.Devices {
			if dev.MAC == mac {
				removedPolicy = dev.Mode != ""
				continue
			}
			kept = append(kept, dev)
		}
		s.Devices = kept
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		if removedPolicy && d.core.Running() && s.NetMode == settings.NetGateway {
			if err := d.restart(); err != nil {
				d.logf("重新应用配置失败,保持当前连接: %v", err)
			}
		}
		return d.deviceViews(), nil
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
		d.syncGuard()
		go d.syncNICIPv6() // 关掉「连接时停用网卡 IPv6」或打开 IPv6 时,这里把绑定还原回去
		if d.core.Running() {
			live := prev // 模式、节点、禁直连开关、定时测速间隔是运行时可改的,别的都要重新生成配置
			live.Mode, live.Selected, live.NoDirect, live.ProbeMinutes = next.Mode, next.Selected, next.NoDirect, next.ProbeMinutes
			if !reflect.DeepEqual(live, next) {
				if err := d.restart(); err != nil {
					return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: "新设置的配置生成失败,保持当前连接: " + err.Error()}
				}
			} else {
				if next.Mode != prev.Mode {
					_ = d.core.SetMode(builder.ModeName(next.Mode))
				}
				if next.Selected != prev.Selected {
					if d.needRebuildFor(next.Selected) {
						if err := d.restart(); err != nil {
							return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: "切换失败,保持当前连接: " + err.Error()}
						}
						return d.getSettings(), nil
					}
					sel := next.Selected
					if sel == "" {
						sel = "auto"
						go d.groupTest(context.Background()) // 切到自动选择:现测一轮换到最快的
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
