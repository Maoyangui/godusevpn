// Package core 内嵌 sing-box 的生命周期:干跑校验、启动、停止、模式切换、节点切换、延迟测试。
// 一次只跑一个实例;所有方法可并发调用。
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	sb "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"
)

// Core 数据面。
type Core struct {
	mu       sync.Mutex
	box      *sb.Box
	ctx      context.Context
	cancel   context.CancelFunc
	running  bool
	logw     log.PlatformWriter
	started  time.Time
	platform adapter.PlatformInterface // Android:TUN 与网络接口由宿主提供
	policy   SessionPolicy             // 会话看护的策略(守护进程);nil = 判废照样拆会话,但不预热、不通知
}

// New logw 收内核日志(nil = 丢弃)。
func New(logw log.PlatformWriter) *Core {
	if logw == nil {
		logw = discard{}
	}
	return &Core{logw: logw}
}

// SetPlatform 挂上平台接口(Android 的 VpnService);sing-box 从上下文里取它。
func (c *Core) SetPlatform(p adapter.PlatformInterface) {
	c.mu.Lock()
	c.platform = p
	c.mu.Unlock()
}

// newContext 调用方可能已持有 c.mu(Start),这里不加锁;platform 只在启动前设一次。
// 会话看护的策略按需取(c.sessionPolicy 自己加锁,但这里可能已持有 c.mu,所以直接读字段)。
func (c *Core) newContext() (context.Context, context.CancelFunc) {
	policy := c.policy
	ctx, cancel := newContext(func() SessionPolicy { return policy })
	if c.platform != nil {
		ctx = service.ContextWith[adapter.PlatformInterface](ctx, c.platform)
	}
	return ctx, cancel
}

type discard struct{}

func (discard) WriteMessage(log.Level, string) {}

// Writer 把内核日志转成 "LEVEL message" 行交给一个 Printf 风格的函数(服务日志滚动器)。
// sing-box 把每一级的消息都交给平台写入器,配置里的 level 只管它自己的默认输出 —— 不在这里按级别过滤的话,
// 设置 info 时 core.log 里照样全是 DEBUG,每小时十来 MB 落盘。Level 为 nil 时不过滤。
type Writer struct {
	Printf func(level, format string, a ...any)
	Level  *atomic.Int32 // 记到这一级为止(sing-box 的 log.Level 数值:panic 0 … info 4, debug 5, trace 6)
}

func (w Writer) WriteMessage(level log.Level, message string) {
	if w.Level != nil && int32(level) > w.Level.Load() {
		return
	}
	if w.Printf != nil {
		w.Printf(strings.ToUpper(log.FormatLevel(level)), "%s", message)
	}
}

// LevelOf 设置里的级别名转成 sing-box 的数值;认不出按 info。
func LevelOf(name string) int32 {
	lv, err := log.ParseLevel(name)
	if err != nil {
		lv = log.LevelInfo
	}
	return int32(lv)
}

// 大坑:sing-box 的 New 会把注册表放进 ctx;要在 New 之前就把注册表建好,之后才能从同一个 ctx 拿到 Clash 服务等对象。
//
// 出站注册表不用 sing-box 自带的那份,而是把 hysteria2 / tuic 换成带会话看护的(见 session_watch.go)。
// policy 为 nil 时看护判废照样拆会话,只是不预热、也不通知守护进程 —— 干跑与测速用的临时实例就是这样。
func newContext(policy func() SessionPolicy) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = sb.Context(ctx, include.InboundRegistry(), outboundRegistry(policy), include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())
	ctx = service.ContextWithDefaultRegistry(ctx)
	return ctx, cancel
}

// SetSessionPolicy 挂上会话看护的策略(守护进程实现)。启动前设一次;为 nil 则判废仍拆会话,但不预热、不通知。
func (c *Core) SetSessionPolicy(p SessionPolicy) {
	c.mu.Lock()
	c.policy = p
	c.mu.Unlock()
}

// dryContext 干跑用的上下文:平台接口套一层只读壳,别让临时实例改到正在跑的那个内核的东西。
func (c *Core) dryContext() (context.Context, context.CancelFunc) {
	ctx, cancel := newContext(nil)
	c.mu.Lock()
	p := c.platform
	c.mu.Unlock()
	if p != nil {
		ctx = service.ContextWith[adapter.PlatformInterface](ctx, dryPlatform{p})
	}
	return ctx, cancel
}

// dryPlatform 只改一件事:Initialize 什么都不做。
// 其余方法原样转发给真的平台接口 —— 干跑要的就是"和真实启动同一套构造过程",只是不许留下痕迹。
// 真正会写共享状态的只有 Initialize(存 networkManager)和 OpenInterface(建 TUN、记接口名),
// 而 OpenInterface 只在 box.Start() 里调,干跑从不 Start。
type dryPlatform struct {
	adapter.PlatformInterface
}

func (dryPlatform) Initialize(adapter.NetworkManager) error { return nil }

func parse(ctx context.Context, raw []byte) (option.Options, error) {
	var opt option.Options
	if err := opt.UnmarshalJSONContext(ctx, raw); err != nil {
		return opt, fmt.Errorf("解析配置: %w", err)
	}
	return opt, nil
}

// Validate 干跑:解析并构造全部对象但不启动,随即关闭。抓解析层抓不到的错误。
//
// 注意干跑用的是**只读的平台壳**(dryPlatform)。直接把真的平台接口带进来的话,sb.New 会调它的
// Initialize 把 networkManager 换成这个临时实例的 —— 而此刻正跑着的内核用的是同一个平台对象,
// 于是它从这一刻起就收不到"默认网络变了"的回调了(Android 上就是 ConnectivityManager 那条)。
// 干跑发生在每次改设置 / 切节点 / 刷订阅之前,窗口最长能有几十秒;这段时间里拔网线、切 Wi-Fi,
// 正在跑的隧道会通着却一点流量都过不去。干跑按定义不该对活着的实例有任何副作用。
//
// 也不传 PlatformLogWriter:干跑用不上平台日志。(注意这一条并不能少构造什么 —— 我们的配置本来就
// 显式开了 cache_file 和 clash_api,needCacheFile / needClashAPI 无论如何都是真;
// 真正靠它省事的是 Probe 那份没有 experimental 段的临时配置,见那边的注释。)
func (c *Core) Validate(raw []byte) error {
	ctx, cancel := c.dryContext()
	defer cancel()
	opt, err := parse(ctx, raw)
	if err != nil {
		return err
	}
	box, err := sb.New(sb.Options{Context: ctx, Options: opt})
	if err != nil {
		return err
	}
	return box.Close()
}

// Start 启动;已在运行则报错。
func (c *Core) Start(raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return errors.New("内核已在运行")
	}
	ctx, cancel := c.newContext()
	opt, err := parse(ctx, raw)
	if err != nil {
		cancel()
		return err
	}
	box, err := sb.New(sb.Options{Context: ctx, Options: opt, PlatformLogWriter: c.logw})
	if err != nil {
		cancel()
		return fmt.Errorf("构造内核: %w", err)
	}
	if err := box.Start(); err != nil {
		_ = box.Close()
		cancel()
		return fmt.Errorf("启动内核: %w", err)
	}
	c.box, c.ctx, c.cancel, c.running, c.started = box, ctx, cancel, true, time.Now()
	return nil
}

// Stop 停止;未运行时无操作。
func (c *Core) Stop() error {
	c.mu.Lock()
	box, cancel := c.box, c.cancel
	c.box, c.ctx, c.cancel, c.running = nil, nil, nil, false
	c.mu.Unlock()
	if box == nil {
		return nil
	}
	err := box.Close()
	if cancel != nil {
		cancel()
	}
	return err
}

func (c *Core) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Uptime 本次运行时长(未运行为 0)。
func (c *Core) Uptime() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return 0
	}
	return time.Since(c.started)
}

func (c *Core) snapshot() (*sb.Box, context.Context, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.box, c.ctx, c.running
}

func (c *Core) clash() (adapter.ClashServer, error) {
	_, ctx, ok := c.snapshot()
	if !ok {
		return nil, errors.New("内核未运行")
	}
	cs := service.FromContext[adapter.ClashServer](ctx)
	if cs == nil {
		return nil, errors.New("内核没有开启 Clash API")
	}
	return cs, nil
}

// Mode 当前模式(内核里的名字:Rule / Global / Direct)。
func (c *Core) Mode() string {
	cs, err := c.clash()
	if err != nil {
		return ""
	}
	return cs.Mode()
}

// SetMode 切换模式,即时生效不重启。
func (c *Core) SetMode(mode string) error {
	cs, err := c.clash()
	if err != nil {
		return err
	}
	for _, m := range cs.ModeList() {
		if strings.EqualFold(m, mode) {
			cs.SetMode(m)
			return nil
		}
	}
	return fmt.Errorf("内核不认识模式 %q(可选: %s)", mode, strings.Join(cs.ModeList(), " / "))
}

type selector interface {
	SelectOutbound(tag string) bool
}

// Group 选择组的当前项与全部选项。
func (c *Core) Group(tag string) (now string, all []string, err error) {
	box, _, ok := c.snapshot()
	if !ok {
		return "", nil, errors.New("内核未运行")
	}
	ob, found := box.Outbound().Outbound(tag)
	if !found {
		return "", nil, fmt.Errorf("没有出站 %q", tag)
	}
	g, isGroup := ob.(adapter.OutboundGroup)
	if !isGroup {
		return "", nil, fmt.Errorf("%q 不是选择组", tag)
	}
	return g.Now(), g.All(), nil
}

// Select 在选择组里选一个节点。
func (c *Core) Select(group, tag string) error {
	box, _, ok := c.snapshot()
	if !ok {
		return errors.New("内核未运行")
	}
	ob, found := box.Outbound().Outbound(group)
	if !found {
		return fmt.Errorf("没有出站 %q", group)
	}
	sel, isSel := ob.(selector)
	if !isSel {
		return fmt.Errorf("%q 不是选择组", group)
	}
	if !sel.SelectOutbound(tag) {
		return fmt.Errorf("选择组里没有 %q", tag)
	}
	return nil
}

// HTTPClient 经某个出站(节点、选择组或 direct)发 HTTP 请求的客户端。
// direct 出站绑定物理网卡,所以即使 TUN 在跑,经它的请求也不会绕回内核。
func (c *Core) HTTPClient(tag string, timeout time.Duration) (*http.Client, error) {
	box, _, ok := c.snapshot()
	if !ok {
		return nil, errors.New("内核未运行")
	}
	ob, found := box.Outbound().Outbound(tag)
	if !found {
		return nil, fmt.Errorf("没有出站 %q", tag)
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ob.DialContext(ctx, "tcp", M.ParseSocksaddr(addr))
		},
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// URLTest 经某个出站(节点或选择组)测一次延迟,毫秒。
func (c *Core) URLTest(ctx context.Context, tag, link string) (int, error) {
	box, _, ok := c.snapshot()
	if !ok {
		return 0, errors.New("内核未运行")
	}
	ob, found := box.Outbound().Outbound(tag)
	if !found {
		return 0, fmt.Errorf("没有出站 %q", tag)
	}
	if link == "" {
		link = "http://www.gstatic.com/generate_204"
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ms, err := urltest.URLTest(tctx, link, ob)
	if err != nil {
		return 0, err
	}
	return int(ms), nil
}

// GroupTest 让自动选择组测一轮全部成员并按结果换到最快的(等价于 Clash API 的 /group/{tag}/delay)。
// 配置里把 sing-box 自己的定时测速关了(builder.autoGroup),什么时候测由守护进程按"定时测速(分钟)"来叫。
// 组里正在测就直接返回空结果(sing-box 自己有个 checking 标记),不会叠着测两轮。
func (c *Core) GroupTest(ctx context.Context, tag string) (map[string]uint16, error) {
	box, _, ok := c.snapshot()
	if !ok {
		return nil, errors.New("内核未运行")
	}
	ob, found := box.Outbound().Outbound(tag)
	if !found {
		return nil, fmt.Errorf("没有出站 %q", tag)
	}
	g, isGroup := ob.(adapter.URLTestGroup)
	if !isGroup {
		return nil, fmt.Errorf("%q 不是自动选择组", tag)
	}
	return g.URLTest(ctx)
}

// ProbeRunning 内核在跑时给一批出站各测一次延迟(并发 8 路),返回 节点 → 毫秒,-1 = 不通。
// onEach 不为空时每测出一个就先报一次:上百个节点全测完要好几秒,界面得能一个一个显示出来。
func (c *Core) ProbeRunning(ctx context.Context, tags []string, link string, onEach func(tag string, ms int)) map[string]int {
	box, _, ok := c.snapshot()
	if !ok {
		return map[string]int{}
	}
	return probeBox(ctx, box, tags, link, onEach)
}

// Probe 内核没跑时测速:只用订阅里的出站起一个临时实例(没有入站,不碰路由、DNS 与 TUN),测完即关。
//
// 两处是踩过坑才写成现在这样的:
//
//   - **不传 PlatformLogWriter。** sing-box 的 needCacheFile 是
//     `CacheFile.Enabled || options.PlatformLogWriter != nil`,而这份临时配置里没有 experimental 段,
//     于是缓存文件会落到默认路径 "cache.db" —— 相对进程当前目录。Android 上当前目录是 /,写不进去,
//     box.Start() 直接失败,测速一个数都出不来;Windows 上服务以 SYSTEM 跑,则会往 System32 里丢文件。
//   - **只放要测的那几个出站,而且逐个先验一遍。** 原来是把订阅里全部出站一股脑塞进去,
//     只要有一个内核解不动(比如 1.14 已经把 wireguard 从 outbounds 挪走了),整份配置解析失败,
//     **所有**节点都测不出来。现在坏的那个自己跳过,别的照测。
//
// 返回的 error 只表示"这一轮整个没跑起来";单个节点测不通体现为结果里的 -1。
func Probe(ctx context.Context, outbounds []json.RawMessage, tags []string, link string, onEach func(tag string, ms int)) (map[string]int, error) {
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}
	bctx, cancel := newContext(nil)
	defer cancel()

	list := make([]any, 0, len(tags)+1)
	var bad []string
	for _, o := range outbounds {
		var head struct {
			Tag string `json:"tag"`
		}
		if json.Unmarshal(o, &head) != nil || !want[head.Tag] {
			continue
		}
		if err := parseOutbound(bctx, o); err != nil {
			bad = append(bad, head.Tag)
			continue
		}
		list = append(list, o)
	}
	list = append(list, map[string]any{"type": "direct", "tag": "direct"})

	raw, err := json.Marshal(map[string]any{"log": map[string]any{"level": "warn"}, "outbounds": list})
	if err != nil {
		return map[string]int{}, err
	}
	opt, err := parse(bctx, raw)
	if err != nil {
		return map[string]int{}, err
	}
	box, err := sb.New(sb.Options{Context: bctx, Options: opt})
	if err != nil {
		return map[string]int{}, fmt.Errorf("构造测速实例: %w", err)
	}
	if err := box.Start(); err != nil {
		_ = box.Close()
		return map[string]int{}, fmt.Errorf("启动测速实例: %w", err)
	}
	defer box.Close()
	res := probeBox(ctx, box, tags, link, onEach)
	for _, tag := range bad { // 内核解不动的节点报 -1,而不是在界面上留个空白让人以为"还没测"
		if _, ok := res[tag]; !ok {
			res[tag] = -1
			if onEach != nil {
				onEach(tag, -1)
			}
		}
	}
	if len(bad) > 0 {
		return res, fmt.Errorf("这些节点内核解不动,已跳过: %s", strings.Join(bad, "、"))
	}
	return res, nil
}

// parseOutbound 单独验一个出站,用来找出"内核解不动"的那一个。
func parseOutbound(ctx context.Context, raw json.RawMessage) error {
	var one option.Outbound
	return one.UnmarshalJSONContext(ctx, raw)
}

func probeBox(ctx context.Context, box *sb.Box, tags []string, link string, onEach func(tag string, ms int)) map[string]int {
	if link == "" {
		link = "http://www.gstatic.com/generate_204"
	}
	res := make(map[string]int, len(tags))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, tag := range tags {
		ob, found := box.Outbound().Outbound(tag)
		if !found {
			continue
		}
		wg.Add(1)
		go func(tag string, ob adapter.Outbound) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			ms, err := urltest.URLTest(tctx, link, ob)
			v := int(ms)
			if err != nil {
				v = -1
			}
			mu.Lock()
			res[tag] = v
			mu.Unlock()
			if onEach != nil {
				onEach(tag, v)
			}
		}(tag, ob)
	}
	wg.Wait()
	return res
}

// CloseAllConnections 掐断当前所有连接,并让各出站重建自己的长连接(与面板上"全部断开"同一套动作)。
//
// 切节点或切模式之后要调一次:选择组只对新连接生效,已经建立的 TCP 会一直挂在老节点上 ——
// 用户看到的就是"我明明选了新节点,连接列表里还是老的"。长连接(Telegram、推送、anytls 的连接池)
// 尤其明显,能挂十几分钟不断。掐掉之后应用自己会重连,新连接就走新节点了。
func (c *Core) CloseAllConnections() error {
	_, ctx, ok := c.snapshot()
	if !ok {
		return errors.New("内核未运行")
	}
	// 只掐连接,不走 ResetNetwork:后者还会通知所有出站"网络变了",而 urltest 组收到通知就把
	// 全部节点重测一轮 —— 切个节点而已,没必要顺带把上百个节点全连一遍。
	if cm := service.FromContext[adapter.ConnectionManager](ctx); cm != nil {
		cm.CloseAll()
		return nil
	}
	nm := service.FromContext[adapter.NetworkManager](ctx)
	if nm == nil {
		return errors.New("拿不到连接管理器")
	}
	nm.ResetNetwork(ctx)
	return nil
}
