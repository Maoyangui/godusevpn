// Package daemon 服务进程的主体:把设置、订阅、配置生成、内核、状态机、控制接口装配起来。
// 托盘客户端与命令行都只通过 ipc 调它,不直接碰任何底层对象。
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/Maoyangui/godusevpn/internal/ruleset"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

const DisplayName = buildinfo.DisplayName

type Daemon struct {
	mu                sync.Mutex
	log               *logx.Rotator
	coreLog           *logx.Rotator
	coreLevel         atomic.Int32 // 内核日志记到哪一级(core.Writer 按它过滤)
	nicOff            atomic.Bool  // 各网卡的 IPv6 绑定已由我们停掉、还没还原(和闸一样,跨内核重启 / 服务重启一直有效)
	nicDisablePending atomic.Bool  // partial disable must be retried even before a public address appears
	shuttingDown      atomic.Bool  // 服务正在停止:闸和网卡 IPv6 这段时间里一律不动(见 Run 的 ctx.Done)
	nicMu             sync.Mutex   // syncNICIPv6 串行化:停用那一步要起 PowerShell,不能两个一起跑
	// lastNICWarn 上一次打过的网卡 IPv6 告警。巡检每 30 秒跑一次,同一句话不重复刷日志;
	// 但产生了就必须有人读到 —— 不然 setNICWarning 等于写进黑洞。
	lastNICWarn string
	probeMu     sync.Mutex
	probeAt     time.Time // auto 组上次测完一轮全部节点的时间,autoProbeLoop 按它算下一次
	core        *core.Core
	settings    settings.Settings
	// settingsGeneration changes on every persisted settings update. A prepare
	// can run for minutes while the user edits settings; the generation binds a
	// rendered config to the exact settings snapshot that produced it.
	configPublishMu            sync.RWMutex // publish settings vs. starting a rendered config
	preparedMu                 sync.Mutex   // bind cfg bytes and metadata through start
	settingsGeneration         uint64
	preparedSettingsGeneration uint64
	preparedSettingsValid      bool
	preparedConfigHash         [sha256.Size]byte
	profiles                   map[string]*profile.Profile // 订阅 id → 节点缓存
	fetchErr                   map[string]string           // 订阅 id → 最近一次拉取失败原因
	fetchLink                  map[string]string           // 订阅 id → 拉取失败(404)时面板随响应给的「选购 / 续费」地址
	running                    *profile.Profile            // 正在跑的内核是按哪份订阅生成的;刷新后拿它和缓存比,决定动不动隧道
	prepared                   *profile.Profile            // prepare 刚按它生成了配置、内核还没起:start 成功后转成 running
	guardOn                    bool                        // 「全局禁直连」的闸此刻开着
	// tunUpPending 隧道网卡的转发层放行没成功(网卡还没注册好之类),下一次同步再试
	tunUpPending atomic.Bool
	guardErr     string     // 闸该开却没开成的原因
	persistedOK  bool       // persisted 状态文件可被可靠读取;未知时不撤保护、不自动连接
	connOpMu     sync.Mutex // MConnect / MDisconnect 串行:两者交错会让落盘意愿与状态机相反
	// settingsOK 设置文件读出来了。读不出来时用的是默认值,而默认值(模式=规则)会让
	// syncGuard 判定"不该有闸"、syncNICIPv6 判定"该还原" —— 等于拿一份猜出来的设置去放宽用户的保护。
	// 所以未知时一律保持现状:不撤闸、不还原网卡 IPv6、不自动连接。用户在界面里保存一次设置就恢复正常。
	settingsOK bool
	// guardApplied 闸现在实际按哪份规格装着。设置里改了「局域网直通」/ 网关模式之后要据此重装 ——
	// 闸是持久的,只看"在不在"的话,连着的时候改这两项永远不生效。
	guardApplied netmode.GuardSpec
	// Clash API 的端口:设置里那个被别的程序占着时会换一个(见 pickClashPort)。
	// preparedClashPort 是 prepare 挑的、clashPort 是正在跑的内核真正监听的 —— 和 prepared / running 同一个道理,
	// Restart 是先备后停再起,备的时候旧内核还占着旧端口,不能提前把界面指到新端口上去。
	preparedClashPort int
	clashPort         atomic.Int32
	guardMu           sync.Mutex // syncGuard 整段串行:判断 + 开 / 撤要一气呵成
	// guardHold 用于“放宽隐私设置”的重启事务:新配置验证并启动成功前，旧闸不能撤。
	// 与 nicHold 一样由 d.mu 保护。
	guardHold    bool
	nicHold      bool
	settingsOpMu sync.Mutex // privacy-setting transactions cannot overlap
	releaseTun   func()     // 见 Options.ReleaseTun
	machine      *state.Machine
	server       *ipc.Server
	secret       string
	http         *http.Client
	delays       map[string]int // 最近一次全节点测速结果
	ping         int            // 当前节点最近一次测得的延迟(毫秒):健康检查本来就要测一次,顺手记下来给界面用
	exitIP       string         // 经当前节点出去时对外露出的地址
	exitLoc      string         // 出口所在国家的两位代码
	exitCity     string         // 出口所在城市
	exitRegion   string         // 出口所在一级行政区
	exitISP      string         // 出口那条线路的运营商 / 机房
	exitNode     string         // 上面那些是哪个节点测出来的:自动选择在后台换了节点就得重测
	exitAt       time.Time      // 上次测出口的时间:节点没变也隔一阵子复查一次
	exitGen      uint64         // 出口查询的代数:换节点 / 重连就加一,慢的旧查询回来发现代数变了就丢弃,不会把旧节点的出口盖到新节点上
	noListen     bool

	missingSets []builder.MissingRuleSet // 上一次生成配置时本地没有、因此摘掉了的规则集(界面要提示,连上之后要去补)
	fillingSets bool                     // 补规则集的活正在跑,别叠第二份
	tunnel      ipc.TunnelView           // 会话看护的记录(见 session_policy.go)
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
	// 内置规则集先铺好。内核在启动阶段就要把规则集全读进来,读不到整个起不来 ——
	// 早先客户端让内核自己去 GitHub 现下,一台全新设备只要第一次下不动就永远连不上(见 internal/ruleset)。
	// 铺不下去也只是少几条规则、连接照常,所以不当致命错误,等日志建好再说一声。
	installErr := ruleset.Install(paths.RuleSets())
	d := &Daemon{
		noListen: o.NoListen, releaseTun: o.ReleaseTun,
		log:      logx.New(filepath.Join(paths.Logs(), "service.log"), 5<<20, 3),
		coreLog:  logx.New(filepath.Join(paths.Logs(), "core.log"), 5<<20, 3),
		http:     &http.Client{Timeout: 30 * time.Second},
		profiles: map[string]*profile.Profile{},
		fetchErr: map[string]string{}, fetchLink: map[string]string{},
	}
	if installErr != nil {
		d.logf("内置规则集铺不下去(少几条规则,连接不受影响): %v", installErr)
	}
	// 数据目录里有节点凭据、订阅地址、面板密码哈希,还有一层规则集会被内核当路由依据读进去,
	// 默认从 %ProgramData% 继承来的权限是"同机任何标准用户都能读、还能往里新建文件"。每次启动都收一遍。
	if err := paths.Harden(); err != nil {
		d.logf("数据目录权限没收紧(里面有节点凭据,建议用管理员身份装一次): %v", err)
	}
	d.coreLevel.Store(core.LevelOf(d.settings.LogLevel))
	d.core = core.New(core.Writer{Printf: d.coreLog.Printf, Level: &d.coreLevel})
	if o.Platform != nil {
		d.core.SetPlatform(o.Platform)
	}
	s, err := settings.Load(paths.Settings())
	d.settingsOK = err == nil
	if err != nil {
		// 设置决定「全局禁直连」与网卡 IPv6 策略,损坏 / 不可读时确实不能拿默认值去覆盖并撤保护。
		// 但 m29 的做法是整个服务拒绝启动 —— 那把用户关在了门外:Windows 上托盘只剩一句"服务未运行",
		// Android 上开机 / 升级广播直接崩进程,而「恢复网络」的离线兜底同样依赖 settings.Load,闸也解不掉。
		// 改成:照常起来(settings.Load 失败时返回的就是默认值),但把 settingsOK 记成 false ——
		// 凡是会**放宽**保护的动作(撤闸、还原网卡 IPv6、还原 DNS/路由、自动连接)一律不做,
		// 现状原样保留。用户在界面里保存一次设置,文件就被覆盖修好,一切恢复正常。
		d.logf("设置读不出来,先用默认值起来,但不会动现有的隐私保护(去设置页保存一次即可修复): %v", err)
	}
	d.settings = s
	d.settingsGeneration = 1
	d.persistedOK = true
	d.applyLogRetention()
	d.loadProfileCaches()
	// 上次异常退出可能留下改过的系统设置(macOS 接管的系统 DNS、Linux 加的策略路由)。
	// 但严格全局模式的连接意愿仍在时，DNS 备份属于隐私保护的一部分，不能在闸重建
	// 前还原成物理网卡解析；状态文件不可读时同样保留现状，待后续明确操作处理。
	prev := d.loadPersisted()
	persistedTrusted := d.persistedStateOK()
	strictPending := persistedTrusted && prev.Wanted && s.TUN && s.NoDirect && s.Mode == settings.ModeGlobal
	if persistedTrusted && !strictPending && d.settingsTrusted() {
		if err := netmode.UnprotectChecked(); err != nil {
			d.logf("上次 DNS/路由保护尚未成功恢复,保留恢复依据并继续重试: %v", err)
		}
	}
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
		Recover: d.recoverRoute,
		OnChange: func(s state.Snapshot) {
			d.logf("状态 → %s%s", s.Status, map[bool]string{true: "(" + s.Code + " " + s.Error + ")", false: ""}[s.Error != ""])
		},
		Logf: d.logf,
	})
	d.core.SetSessionPolicy(d) // 会话看护:判废 / 重建时回到这里问"还想连着吗",并记账
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
	if healed && d.settingsOK {
		// 设置读不出来时手上这份是默认值,写回去等于把用户损坏但仍有救的文件抹成空白。
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
	persisted := d.loadPersisted()
	if d.persistedStateOK() && d.settingsTrusted() && persisted.Wanted {
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
			// 用 Shutdown 而不是 Disconnect:Disconnect 先把"想连"清成假、再等状态机当前这一步跑完,那一步(或别处
			// 正排队等锁的同步)读到假,就按"用户断开"撤闸、还原 IPv6 —— 停服务(升级、net stop、关机、登记账户后
			// 重启服务)就成了撤闸。0.7.4 及以前都是这样。shuttingDown 再加一道:同步函数在入口和读完"想连"之后都查。
			d.shuttingDown.Store(true)
			d.machine.Shutdown()
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
	b, err := os.ReadFile(paths.State())
	if errors.Is(err, os.ErrNotExist) {
		d.mu.Lock()
		// 状态文件缺失不是“已明确断开”:可能是首次启动,也可能是
		// 崩溃/清理过程中丢失。未知意愿下不能撤销残留的闸、DNS 密封
		// 或网卡 IPv6 保护,后续明确连接/断开时再写出新状态。
		d.persistedOK = false
		d.mu.Unlock()
		return p
	}
	if err != nil {
		d.mu.Lock()
		d.persistedOK = false
		d.mu.Unlock()
		d.logf("连接状态读取失败,保留现有隐私保护: %v", err)
		return p
	}
	if err := json.Unmarshal(b, &p); err != nil {
		d.mu.Lock()
		d.persistedOK = false
		d.mu.Unlock()
		d.logf("连接状态损坏,保留现有隐私保护: %v", err)
		return persisted{}
	}
	d.mu.Lock()
	d.persistedOK = true
	d.mu.Unlock()
	return p
}

// settingsTrusted 设置文件是不是真读出来了(而不是退回默认值)。见 settingsOK 的说明。
func (d *Daemon) settingsTrusted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settingsOK
}

func (d *Daemon) persistedStateOK() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.persistedOK
}

func (d *Daemon) savePersisted(p persisted) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := paths.State() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, paths.State()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	d.mu.Lock()
	d.persistedOK = true
	d.mu.Unlock()
	return nil
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
	d.configPublishMu.Lock()
	defer d.configPublishMu.Unlock()
	if err := s.Save(paths.Settings()); err != nil {
		return err
	}
	d.mu.Lock()
	d.settings = s
	d.settingsGeneration++
	d.settingsOK = true // 刚刚整份写成功了,文件从此可信 —— 之前读不出来的那次到此为止
	d.mu.Unlock()
	d.applyLogRetention()
	return nil
}

func (d *Daemon) settingsSnapshot() (settings.Settings, uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings.Clone(), d.settingsGeneration
}

// preparedMatchesCurrent prevents a config rendered before a settings change
// from being started under the new privacy policy. The second check in start
// closes the race where settings change while core.Start is being prepared.
func (d *Daemon) preparedMatchesCurrent(cfg []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.preparedSettingsValid || d.settingsGeneration != d.preparedSettingsGeneration || d.preparedConfigHash != sha256.Sum256(cfg) {
		return state.Errf(state.CodeConfig, "设置在配置启动前发生变化,丢弃旧配置并重新生成")
	}
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
	s, settingsGeneration := d.settingsSnapshot()
	d.coreLevel.Store(core.LevelOf(s.LogLevel)) // 内核日志按设置的级别写,改了设置下次连接生效
	// 订阅刷新和启动解析本身也会联网，必须在任何网络准备前建立保护。
	d.syncGuard()
	if err := d.syncNICIPv6(); err != nil {
		return nil, err
	}
	if err := d.ensurePrivacyReady(); err != nil {
		return nil, err
	}
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
	if s.NetMode == settings.NetGateway {
		d.resolveDeviceIPs(&s) // 设备策略按当前 IP 生效
	}
	clashPort := d.pickClashPort(s.ClashPort)
	cfg, rep, err := builder.BuildEx(builder.Input{Profile: p, Settings: s, DataDir: paths.DataDir(), ClashSecret: d.secret, RuleSetDir: paths.RuleSets(),
		ClashPort: clashPort, NodeIPs: resolveNodeHosts(ctx, p, d.bootResolver(s))})
	if err != nil {
		return nil, state.Errf(state.CodeConfig, "生成配置: %v", err)
	}
	d.noteMissingRuleSets(rep.Missing)
	if err := d.core.Validate(cfg); err != nil {
		return nil, state.Errf(state.CodeConfig, "配置校验失败: %v", err)
	}
	// Do not publish a rendered config if a concurrent settings update raced
	// with the long subscription/build phase. The state machine will retry with
	// the current policy, keeping DNS/route/privacy settings coherent.
	d.mu.Lock()
	if d.settingsGeneration != settingsGeneration {
		d.mu.Unlock()
		return nil, state.Errf(state.CodeConfig, "设置在配置生成期间发生变化,重新生成")
	}
	d.mu.Unlock()
	_ = os.WriteFile(paths.Config(), cfg, 0o600)
	// 只记"这份配置是按哪份订阅备的";要等 start 成功才算"内核在用"。Restart 是先备后停再起,
	// 停那一步会把 running 清掉,这里直接写 running 的话起来之后就是 nil —— 之后刷新发现当前节点
	// 被删也不会重连、选到参数变了的节点也不会重建(m14 到 m19 都有这毛病)。
	d.preparedMu.Lock()
	defer d.preparedMu.Unlock()
	d.mu.Lock()
	d.prepared = p
	d.preparedClashPort = clashPort
	d.preparedSettingsGeneration = settingsGeneration
	d.preparedSettingsValid = true
	d.preparedConfigHash = sha256.Sum256(cfg)
	d.mu.Unlock()
	return cfg, nil
}

// start 启动内核;失败按原因归类。
func (d *Daemon) start(cfg []byte) error {
	// A settings publish and a core start must not interleave. This independent
	// lock is deliberately separate from settingsOpMu: RestartChecked starts
	// while its caller owns settingsOpMu, and must not deadlock on that mutex.
	d.configPublishMu.RLock()
	defer d.configPublishMu.RUnlock()
	d.preparedMu.Lock()
	defer d.preparedMu.Unlock()
	if err := d.preparedMatchesCurrent(cfg); err != nil {
		return err
	}
	// 网卡 IPv6 的停用不在这里做:它跟的是"用户想不想连着"而不是"内核在不在跑"(见 syncNICIPv6),
	// prepare() 里已经对齐过了 —— 那也正好在内核启动之前,改协议绑定会让网卡重新走一遍协议栈,不该去抖刚建好的隧道。
	if err := d.ensurePrivacyReady(); err != nil {
		return err
	}
	s := d.getSettings()
	// 系统 DNS / 本机回包路由必须在数据面启动前完成。尤其是 macOS
	// 的全局模式，接管失败时继续启动会让系统解析绕过隧道；备份和
	// 接管失败都按隐私前置条件失败处理，状态机只会退避重试。
	if s.TUN {
		if err := netmode.Protect(builder.TunName, s.IPv6); err != nil {
			return state.Errf(state.CodePrivacyGuard, "建立系统网络保护失败,拒绝启动数据面: %v", err)
		}
	}
	if err := d.core.Start(cfg); err != nil {
		return d.classifyStart(err)
	}
	// 最后一次检查覆盖“配置验证后闸被外部清掉/网卡状态变化”的竞态。
	// 失败立即停掉刚启动的内核；stop() 在仍想连接时会保留保护状态。
	if err := d.ensurePrivacyReady(); err != nil {
		_ = d.core.Stop()
		return err
	}
	if err := d.preparedMatchesCurrent(cfg); err != nil {
		_ = d.core.Stop()
		return err
	}
	_ = os.WriteFile(paths.LastGood(), cfg, 0o600)
	d.mu.Lock()
	d.running = d.prepared // 起来了,这份订阅才是内核在用的
	d.mu.Unlock()
	d.clashPort.Store(int32(d.preparedClashPortValue())) // 界面按它连 Clash API;同理要等真起来了才算数
	d.markProbed()                                       // sing-box 启动时(PostStart)自己会把 auto 组全测一轮,定时测速从这时候起算
	d.guardTunUp()
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
	go d.fillMissingRuleSets() // 隧道通了才去补规则集:直连多半拿不到 GitHub
	if s.TUN {
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
	d.mu.Lock()
	guardHold := d.guardHold
	d.mu.Unlock()
	// 状态机在服务停止、内核崩溃、重连时会暂时把 machine.Wanted 清空，
	// 但这不是用户的“断开”操作。只要落盘仍记录想连接，或状态文件不可读，
	// 严格全局模式的密封 DNS 必须保留；否则普通 stop 会在重启窗口恢复系统 DNS。
	persisted := d.loadPersisted()
	s := d.getSettings()
	keepSealed := !d.persistedStateOK() || (persisted.Wanted && s.TUN && s.NoDirect && s.Mode == settings.ModeGlobal)
	if !keepSealed && !d.GuardWanted() && !guardHold {
		if restoreErr := netmode.UnprotectChecked(); restoreErr != nil {
			err = errors.Join(err, state.Errf(state.CodePrivacyGuard, "恢复系统 DNS/路由失败,保护仍保留: %v", restoreErr))
		}
	}
	netmode.ClearGateway()
	return err
}

// health 经代理测一次;内核不在了报崩溃码,让状态机重连。
// 测出来的毫秒数顺手记下来:首页要显示当前节点的延迟,这一发本来就要打,不用再单独测。
func (d *Daemon) health(ctx context.Context) error {
	if !d.core.Running() {
		return state.Errf(state.CodeCoreCrash, "内核未运行")
	}
	// 隐私保护是运行期不变量,不只是在启动时检查一次。新网卡、外部改了过滤器、IPv6 绑定又冒出来 ——
	// 这些都在这里被发现。发现了就**原地修**:闸掉了重装、网卡 v6 冒出来再停用,隧道留着;
	// 状态机把它报成 Degraded 而不是拆隧道(拆了只会更漏,见 state.Machine.watch)。
	if perr := d.ensurePrivacyReady(); perr != nil {
		// 闸类问题原地修(重装是事务内替换,幂等);网卡 IPv6 类交给 nicIPv6Loop —— 它有 10 分钟的退避,
		// 这里每 30 秒起一次 PowerShell 会把那个退避绕过去。
		if state.CodeOf(perr) == state.CodePrivacyGuard {
			d.syncGuard()
		}
		// 隐私没过也要探一次节点:节点挂了照样要走正常的阶梯(降级 → 换线 → 重建),
		// 不能被隐私 Degraded 挡住,否则闸修不好的那段时间节点死了也没人管。
		if _, err := d.core.URLTest(ctx, "proxy", builder.TestURL); err != nil {
			d.setPing(0)
			return state.Errf(state.CodeNodeDown, "当前节点不可用: %v", err)
		}
		return perr
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

// restartForPrivacySettings 用新设置重建数据面。放宽全局禁直连时，旧闸在新配置
// 验证并成功启动前始终保留；失败则恢复旧设置和旧闸，避免出现“设置已切换但旧数据面
// 没有对应保护”的窗口。
// privacyRelaxes 从 prev 到 next 是不是在**放宽**保护。
//
// 方向是这里唯一要紧的事。收紧失败可以整份回滚 —— 用户没拿到更松的配置,不吃亏。
// 放宽失败要是也回滚,用户就再也关不掉「全局禁直连」了 —— 而他去关它,十有八九正是因为
// 此刻连不上、想把网先拿回来。m29 让两个方向都必须"重启成功"才算数,于是节点一连不上,
// 逃生口自己就锁死了:关不掉开关 → 闸不撤 → 没网 → 还是关不掉。
func privacyRelaxes(prev, next settings.Settings) bool {
	switch {
	case prev.NoDirect && !next.NoDirect:
		return true
	case prev.Mode == settings.ModeGlobal && next.Mode != settings.ModeGlobal:
		return true
	case !prev.IPv6 && next.IPv6:
		return true
	case prev.DisableNICIPv6 && !next.DisableNICIPv6:
		return true
	case prev.TUN && !next.TUN:
		return true
	}
	return false
}

func (d *Daemon) restartForPrivacySettings(prev settings.Settings) error {
	next := d.getSettings()
	relax := privacyRelaxes(prev, next)
	hold := prev.NoDirect && prev.Mode == settings.ModeGlobal && d.machine.Wanted()
	nicHold := wantNICOff(prev) && d.machine.Wanted()
	d.mu.Lock()
	d.guardHold, d.nicHold = hold, nicHold
	d.mu.Unlock()
	// hold 只在这个事务里有意义,函数退出时一定要放开。m29 在"回滚设置也失败"那条路上直接 return,
	// hold 永远是 true:之后 syncGuard 整段空转,用户点断开也撤不了闸,只能重启服务。
	// 放开 hold 不等于放松保护 —— 闸和网卡 IPv6 本身没动,syncGuard 只是重新按当前设置对齐。
	defer func() {
		d.mu.Lock()
		d.guardHold, d.nicHold = false, false
		d.mu.Unlock()
	}()
	var rollback func() error
	if !relax {
		rollback = func() error { return d.setSettings(prev) }
	}
	err := d.machine.RestartChecked(rollback)
	current := d.getSettings()
	if err != nil && !relax && !reflect.DeepEqual(current, prev) {
		// 收紧方向:重启失败、回滚设置也失败。此刻设置文件是新值、数据面按旧配置在跑(RestartChecked
		// 失败时旧数据面保留)。闸与网卡 IPv6 由 syncGuard / syncNICIPv6 按当前设置继续对齐;
		// 这里把事实报出去,不再用永久 hold 把撤闸路径一起锁死。
		d.logf("隐私设置重启失败且回滚未完成,设置文件已是新值、数据面仍按旧配置运行: %v", err)
		return err
	}
	// 走到这里事务就结束了:**先**放开 hold,再同步。上一版把放开挪进了 defer,于是下面这两次同步
	// 是在 hold 仍为 true 时跑的 —— syncGuard 的 hold 分支规格没变就直接返回,放宽方向(全局→规则、
	// 关掉禁直连)闸根本撤不掉,规则模式下等于断网,直到用户点断开。defer 只兜早返回的路径。
	d.mu.Lock()
	d.guardHold, d.nicHold = false, false
	d.mu.Unlock()
	// 收紧方向:新数据面已成功启动,现在才按新设置调整规格。
	// 放宽方向:哪怕隧道没重建起来也要在这里撤闸 —— 用户要的就是把保护放开,闸留着等于网还是没有。
	d.syncGuard()
	if nicErr := d.syncNICIPv6(); err == nil {
		err = nicErr
	}
	// TUN 关闭后不再需要把系统解析器密封到隧道;只有这条显式设置路径才允许恢复原 DNS。
	// 恢复失败时保留备份,让下一次设置 / 启动继续重试。
	if !current.TUN && (err == nil || relax) {
		if restoreErr := netmode.UnprotectChecked(); restoreErr != nil && err == nil {
			err = state.Errf(state.CodePrivacyGuard, "恢复系统 DNS 失败,保护仍保留: %v", restoreErr)
		}
	}
	if relax && err != nil {
		return state.Errf(state.CodeOf(err), "新的隐私设置已保存、保护已按要求解除,但连接没能重建: %v", err)
	}
	return err
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
func (d *Daemon) syncNICIPv6() error {
	if d.shuttingDown.Load() {
		return nil // 服务正在停止:不动网卡 IPv6(见 Run 的 ctx.Done)。下次启动由 reconcileNICIPv6 按落盘意愿对账
	}
	if !d.settingsTrusted() {
		return nil // 同 syncGuard:设置不可信时不拿默认值去放宽保护
	}
	d.nicMu.Lock()
	defer d.nicMu.Unlock()
	defer d.logNICWarning()
	d.mu.Lock()
	hold := d.nicHold
	d.mu.Unlock()
	// A durable backup means a previous attempt may have changed at least one
	// adapter even if the process never reached the success flag. Treat that as
	// protected/unknown so an explicit disconnect cannot skip restoration.
	on := d.nicOff.Load() || netmode.NICIPv6Off()
	if on {
		d.nicOff.Store(true)
	}
	want := d.nicIPv6Wanted()
	if d.shuttingDown.Load() {
		return nil // 入口查过之后又排队等了锁:停机中照样不动
	}
	// During a privacy-policy transaction the old data plane is still live.
	// Keep its IPv6 protection until the replacement starts successfully; the
	// final sync after RestartChecked then performs the requested restore.
	if hold && on {
		if want && (d.nicDisablePending.Load() || netmode.NICIPv6Leaking(builder.TunName)) {
			if err := netmode.DisableNICIPv6(builder.TunName); err != nil {
				d.nicDisablePending.Store(true)
				d.nicOff.Store(netmode.NICIPv6Off())
				return state.Errf(state.CodePrivacyNIC, "停用网卡 IPv6 失败: %v", err)
			}
			d.nicOff.Store(true)
			d.nicDisablePending.Store(false)
		}
		return nil
	}
	if want && on {
		// Partial adapter failure leaves a backup and may leave a public address.
		// Retry the disable operation while connecting instead of treating the
		// backup as proof that every adapter is protected.
		if d.nicDisablePending.Load() || netmode.NICIPv6Leaking(builder.TunName) {
			if err := netmode.DisableNICIPv6(builder.TunName); err != nil {
				d.nicDisablePending.Store(true)
				d.nicOff.Store(netmode.NICIPv6Off())
				return state.Errf(state.CodePrivacyNIC, "停用网卡 IPv6 失败: %v", err)
			}
			d.nicOff.Store(true)
			d.nicDisablePending.Store(false)
		}
		return nil
	}
	switch nicIPv6Action(want, on) {
	case nicDisable:
		if err := netmode.DisableNICIPv6(builder.TunName); err != nil {
			d.nicDisablePending.Store(true)
			// Disable writes its recovery backup before changing adapters. Keep
			// the state marked as potentially changed when it fails midway.
			d.nicOff.Store(netmode.NICIPv6Off())
			d.logf("停用网卡 IPv6 失败,拒绝启动数据面: %v", err)
			return state.Errf(state.CodePrivacyNIC, "停用网卡 IPv6 失败: %v", err)
		}
		d.nicOff.Store(true)
		d.nicDisablePending.Store(false)
	case nicRestore:
		if err := netmode.RestoreNICIPv6(); err != nil {
			var inc *netmode.NICRestoreIncomplete
			if !errors.As(err, &inc) {
				d.logf("还原网卡 IPv6 失败,保护状态保留: %v", err)
				return state.Errf(state.CodePrivacyNIC, "还原网卡 IPv6 失败: %v", err)
			}
			d.logf("网卡 IPv6:能还原的都还原了;%s", inc.Detail)
		} else {
			d.logf("网卡 IPv6:已按动手前的状态还原")
		}
		d.nicOff.Store(false)
		d.nicDisablePending.Store(false)
	}
	return nil
}

// logNICWarning 把 netmode 那边记下的"哪几张网卡没动成"打出来。变了才打,免得 30 秒一条刷满日志。
func (d *Daemon) logNICWarning() {
	w := netmode.NICWarning()
	d.mu.Lock()
	changed := w != d.lastNICWarn
	d.lastNICWarn = w
	d.mu.Unlock()
	if changed && w != "" {
		d.logf("网卡 IPv6:%s", w)
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
	persisted := d.loadPersisted()
	if !d.settingsTrusted() {
		d.logf("网卡 IPv6:设置读不出来,保留现有状态,不执行还原")
		return
	}
	want := persisted.Wanted && wantNICOff(d.getSettings())
	if !d.persistedStateOK() {
		// 状态文件读不出来(首次启动、被清理、上次崩溃留下 0 字节 —— m28 的 savePersisted 不是原子写,
		// 这很常见)。m29 在这里直接 return,于是 netmode/nic_windows.go 开头那句
		// "守护进程启动时也无条件还原一次,上次崩溃退出也不会把用户的 IPv6 永久关掉"不再成立:
		// 备份还在,网卡就一直关着,而且没有任何自愈路径。
		//
		// 改用一个总是拿得到、而且比状态文件更能说明问题的信号:闸还在不在。闸是持久的,
		// 它还在就说明上次是连着走的(严格全局模式),那就接着关着;闸不在就按文档还原回去 ——
		// 下次连接时 syncNICIPv6 会重新关掉,不会因此漏。
		if n, err := netmode.GuardStatus(); err == nil && n > 0 {
			d.logf("网卡 IPv6:连接状态不可读,但闸还在,按上次连着关机处理,保持关闭")
			return
		}
		d.logf("网卡 IPv6:连接状态不可读且闸不在,按文档还原回去(下次连接会重新停用)")
		want = false
	}
	switch nicIPv6Action(want, on) {
	case nicRestore:
		if err := netmode.RestoreNICIPv6(); err != nil {
			var inc *netmode.NICRestoreIncomplete
			if !errors.As(err, &inc) {
				d.logf("网卡 IPv6:还原失败,保留保护状态: %v", err)
				return
			}
			d.nicOff.Store(false)
			d.logf("网卡 IPv6:上次不是连着关的机(或设置已关掉),能还原的都还原了;%s", inc.Detail)
			return
		}
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
		d.nicDisablePending.Store(true)
		d.nicOff.Store(netmode.NICIPv6Off())
		d.logf("停用网卡 IPv6 失败,连接不会启动: %v", err)
		return
	}
	d.nicOff.Store(true)
	d.nicDisablePending.Store(false)
	d.logf("网卡 IPv6:已停用(上次连着关的机)")
}

// privacyChecks 决定启动前要核查哪些闸相关的不变量。
//
// installable=false 的平台(Android:闸就是宿主 VpnService 的接口,由系统持有)没有可核查的对象;
// 硬要核查只会把连接整个挡死 —— 而用户挡不住就会去关掉「全局禁直连」,反倒更不私密。那种平台上
// 跨进程 / 重启的覆盖靠系统的 Always-on + lockdown,状态由 netmode.GuardWarning 如实报出来。
// boot 只在平台真的有"守护进程之外也在"的闸时才查(目前只有 Windows 的 WFP 持久 + 开机过滤器)。
func privacyChecks(installable, persistent bool) (guard, boot bool) {
	return installable, installable && persistent
}

// ensurePrivacyReady 是数据面启动前的最后一道硬检查。保护状态未知、闸状态查不到、
// 网卡 IPv6 仍有公网地址时，宁可连接失败并退避，也不能让数据面以不完整的隐私保护运行。
func (d *Daemon) ensurePrivacyReady() error {
	s := d.getSettings()
	checkGuard, checkBoot := privacyChecks(netmode.GuardInstallable(), netmode.GuardPersistentSupported())
	if s.NoDirect && s.Mode == settings.ModeGlobal && d.machine.Wanted() && checkGuard {
		if checkBoot {
			if ready, err := netmode.GuardPersistentReady(); err != nil {
				return state.Errf(state.CodePrivacyGuard, "无法确认持久全局禁直连保护: %v", err)
			} else if !ready {
				// 查的是运行期那组(守护进程之外也在的那道闸)。开机那几秒的覆盖不在这条里 ——
				// 它装不上只经 GuardWarning 告警,不挡连接。
				return state.Errf(state.CodePrivacyGuard, "全局禁直连的持久保护未完整就绪,拒绝启动数据面")
			}
		}
		d.mu.Lock()
		on, guardErr := d.guardOn, d.guardErr
		d.mu.Unlock()
		if !on {
			if guardErr == "" {
				guardErr = "闸未安装"
			}
			return state.Errf(state.CodePrivacyGuard, "全局禁直连未就绪: %s", guardErr)
		}
		d.mu.Lock()
		applied := d.guardApplied
		d.mu.Unlock()
		if applied != d.guardSpec() {
			return state.Errf(state.CodePrivacyGuard, "全局禁直连闸规格未同步")
		}
		if n, err := netmode.GuardStatus(); err != nil {
			return state.Errf(state.CodePrivacyGuard, "无法确认全局禁直连状态: %v", err)
		} else if n == 0 {
			d.setGuard(false, "闸状态为空")
			return state.Errf(state.CodePrivacyGuard, "全局禁直连闸未安装")
		}
	}
	if d.nicIPv6Wanted() {
		if d.nicDisablePending.Load() {
			return state.Errf(state.CodePrivacyNIC, "网卡 IPv6 停用操作尚未完整成功")
		}
		if !d.nicOff.Load() {
			return state.Errf(state.CodePrivacyNIC, "网卡 IPv6 未成功停用")
		}
		// 这里是**拒绝连接**的门,必须用确证谓词:NICIPv6Leaking 把"枚举失败 / 某张网卡读不到地址"
		// 也算成在漏,一次瞬时的系统调用失败就能让人连不上,而且没有自愈路径。
		// 别处那几个 NICIPv6Leaking 是"要不要再跑一遍昂贵的停用脚本",宁可多跑,保持不变。
		if netmode.NICIPv6LeakConfirmed(builder.TunName) {
			return state.Errf(state.CodePrivacyNIC, "确认物理网卡上仍挂着公网 IPv6 地址")
		}
	}
	return nil
}

// groupTest 让 auto 组测一轮全部节点并换到最快的,记下时间。sing-box 自己的定时测速在配置里关掉了
// (builder.autoGroup),什么时候测由这里叫:切到自动选择时立刻一次,之后 autoProbeLoop 按"定时测速(分钟)"来。
func (d *Daemon) groupTest(ctx context.Context) {
	// 超时要按节点数给。sing-box 是并发分批测的,节点多时 20 秒根本测不完,而 ctx 一到期
	// 没轮到的那些会被当成"测失败"清掉历史 —— 下次自动选择就只能在测过的那几个里挑。
	// 每个节点算 400ms,下限 20 秒、上限 3 分钟(和定时测速的最小间隔留足余量)。
	budget := 20 * time.Second
	if _, all, err := d.core.Group("auto"); err == nil && len(all) > 0 {
		budget = time.Duration(len(all)) * 400 * time.Millisecond
		if budget < 20*time.Second {
			budget = 20 * time.Second
		}
		if budget > 3*time.Minute {
			budget = 3 * time.Minute
		}
	}
	tctx, cancel := context.WithTimeout(ctx, budget)
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
		// 顺带:隧道网卡的转发层放行上次没成功(网卡晚注册),这里每半分钟补一次,不用等用户动手
		if d.tunUpPending.Load() && d.core.Running() {
			d.guardTunUp()
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
			d.nicDisablePending.Store(true)
			d.nicOff.Store(netmode.NICIPv6Off())
			d.logf("停用新网卡的 IPv6 失败: %v", err)
		} else {
			d.nicOff.Store(true)
			d.nicDisablePending.Store(false)
		}
		// Force the running state machine to validate privacy immediately;
		// a failed adapter operation must not leave the data plane serving
		// until the next multi-minute health interval.
		d.machine.CheckNow()
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
// lookup 是解析函数(见 bootResolver:禁直连下只用加密 DoH);为 nil 表示这一轮不解析。
func resolveNodeHosts(ctx context.Context, p *profile.Profile, lookup func(context.Context, string) ([]string, error)) map[string][]string {
	if p == nil || lookup == nil {
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
			addrs, err := lookup(ctx, h)
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
	for _, m := range d.MissingRuleSets() {
		v.MissingRuleSets = append(v.MissingRuleSets, m.Tag)
	}
	v.Tunnel = d.tunnelView()
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
	v.NICLost = netmode.NICLossNote()
	return v
}

// ---- 控制接口 ----

func (d *Daemon) registerHandlers() {
	h := d.server.Handle
	h(ipc.MPing, func(json.RawMessage) (any, error) {
		return map[string]any{"version": buildinfo.Version, "protocol": ipc.Version, "name": DisplayName}, nil
	})
	h(ipc.MGetState, func(json.RawMessage) (any, error) { return d.stateView(), nil })
	h(ipc.MDismissNICLost, func(json.RawMessage) (any, error) { return nil, netmode.ClearNICLoss() })
	h(ipc.MGetClashInfo, func(json.RawMessage) (any, error) {
		return ipc.ClashInfo{Port: d.activeClashPort(), Secret: d.secret, Running: d.core.Running()}, nil
	})
	h(ipc.MExportDiag, func(json.RawMessage) (any, error) {
		p, err := d.exportDiag()
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": p}, nil
	})
	h(ipc.MConnect, func(json.RawMessage) (any, error) {
		// 连接与断开互斥:两个并发调用会各自 savePersisted(同一个 .tmp 路径)并交错调用状态机,
		// 落盘的 Wanted 可能和状态机的意愿相反,重启后按落盘值重连或撤闸就错了。
		d.connOpMu.Lock()
		defer d.connOpMu.Unlock()
		if a, _ := d.activeProfile(); a == nil {
			return nil, &ipc.CallError{Code: state.CodeProfileMissing, Msg: "还没有添加订阅"}
		}
		if err := d.savePersisted(persisted{Wanted: true}); err != nil {
			return nil, fmt.Errorf("保存连接意愿失败,拒绝启动: %w", err)
		}
		d.machine.Connect()
		d.syncGuard()
		if err := d.syncNICIPv6(); err != nil {
			return nil, err
		}
		return d.stateView(), nil
	})
	h(ipc.MDisconnect, func(json.RawMessage) (any, error) {
		d.connOpMu.Lock()
		defer d.connOpMu.Unlock()
		// 先把"明确断开"的意愿持久化。写不进去时仍按严格连接状态保留闸与网卡 IPv6 保护,
		// 不能出现磁盘仍写着 Wanted=true 但系统保护已经撤掉的重启竞态。
		if err := d.savePersisted(persisted{Wanted: false}); err != nil {
			return nil, fmt.Errorf("保存断开状态失败,保护保持: %w", err)
		}
		d.machine.Disconnect()
		// 用户明确点了断开:隐私事务残留的 hold(回滚失败那种)不能再挡着撤闸,否则断了也断不干净
		d.mu.Lock()
		d.guardHold, d.nicHold = false, false
		d.mu.Unlock()
		d.syncGuard()
		nicErr := d.syncNICIPv6()
		if nicErr != nil {
			return nil, nicErr
		}
		return d.stateView(), nil
	})
	h(ipc.MSetMode, func(p json.RawMessage) (any, error) {
		d.settingsOpMu.Lock()
		defer d.settingsOpMu.Unlock()
		in, err := ipc.Decode[struct {
			Mode string `json:"mode"`
		}](p)
		if err != nil {
			return nil, err
		}
		s := d.getSettings()
		prev := s
		s.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
		if err := d.setSettings(s); err != nil {
			return nil, err
		}
		if d.core.Running() && s.Mode != prev.Mode {
			if err := d.restartForPrivacySettings(prev); err != nil {
				return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: "切换模式失败,已保持旧配置与隐私保护: " + err.Error()}
			}
			return d.stateView(), nil
		}
		d.syncGuard()
		if err := d.syncNICIPv6(); err != nil {
			return nil, err
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
			// 未连接:直连量到节点服务器的往返。但「全局禁直连」的闸还开着的时候不做 —— 那说明用户想连着、
			// 内核只是暂时没起来(在重启 / 在退避),这时从本机直接发 DNS 查询、ICMP、TCP 握手出去,
			// 正是闸要挡的那种东西;闸只按进程放行本服务,拦不住自己。等隧道起来经出站测,或者用户断开后再测。
			if d.guardArmed() {
				return nil, errors.New("全局禁直连的闸还开着、隧道还没起来:这时候不做直连测速(会从本机直接发探测包)。等连上后再测,或先断开连接")
			}
			res = probeDirect(ctx, p, set, d.logf)
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
				if err := d.savePersisted(persisted{Wanted: false}); err != nil {
					return nil, err
				}
				d.machine.Disconnect()
				d.syncGuard()
				if err := d.syncNICIPv6(); err != nil {
					return nil, err
				}
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
		d.settingsOpMu.Lock()
		defer d.settingsOpMu.Unlock()
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
		// Mode / NoDirect 会改变密封 DNS 与全局闸的安全边界，运行中必须整份重建，
		// 不能通过 Clash SetMode 只改内核运行标志。
		if d.core.Running() && (next.Mode != prev.Mode || next.NoDirect != prev.NoDirect || next.IPv6 != prev.IPv6 || next.DisableNICIPv6 != prev.DisableNICIPv6 || next.TUN != prev.TUN) {
			if err := d.restartForPrivacySettings(prev); err != nil {
				msg := "隐私设置切换失败,已保持旧配置与隐私保护: " + err.Error()
				if privacyRelaxes(prev, next) {
					// 放宽方向不回滚,所以别再说"已保持旧配置" —— 那是假话。
					msg = err.Error()
				}
				return nil, &ipc.CallError{Code: state.CodeOf(err), Msg: msg}
			}
			return d.getSettings(), nil
		}
		d.syncGuard()
		if err := d.syncNICIPv6(); err != nil { // 关掉「连接时停用网卡 IPv6」或打开 IPv6 时,这里把绑定还原回去
			return nil, err
		}
		if !next.TUN {
			if err := netmode.UnprotectChecked(); err != nil {
				return nil, &ipc.CallError{Code: state.CodePrivacyGuard, Msg: "恢复系统 DNS/路由失败,保护仍保留: " + err.Error()}
			}
		}
		if d.core.Running() {
			live := prev // 模式、节点、禁直连开关、定时测速间隔是运行时可改的,别的都要重新生成配置
			live.Mode, live.Selected, live.NoDirect, live.ProbeMinutes = next.Mode, next.Selected, next.NoDirect, next.ProbeMinutes
			// 这四项都不进 sing-box 的配置:面板监听地址与密码归内置 HTTP 面板管,
			// 日志保留天数归滚动器管,订阅刷新间隔归 Run 的循环管 —— 改它们不该让隧道断一下
			live.WebListen, live.WebPassword, live.LogDays, live.UpdateHours = next.WebListen, next.WebPassword, next.LogDays, next.UpdateHours
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
					// 和节点列表里切节点保持一致:选择组只对新连接生效,不掐掉老连接的话
					// 用户会看到"选了新节点、连接列表里还是老节点";出口和延迟也得清掉重测
					if err := d.core.CloseAllConnections(); err != nil {
						d.logf("切节点后掐断旧连接失败(旧连接会继续用老节点): %v", err)
					}
					d.clearExit()
					d.setPing(0)
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
