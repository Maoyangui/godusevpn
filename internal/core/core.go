// Package core 内嵌 sing-box 的生命周期:干跑校验、启动、停止、模式切换、节点切换、延迟测试。
// 一次只跑一个实例;所有方法可并发调用。
package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
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
	mu      sync.Mutex
	box     *sb.Box
	ctx     context.Context
	cancel  context.CancelFunc
	running bool
	logw    log.PlatformWriter
	started time.Time
}

// New logw 收内核日志(nil = 丢弃)。
func New(logw log.PlatformWriter) *Core {
	if logw == nil {
		logw = discard{}
	}
	return &Core{logw: logw}
}

type discard struct{}

func (discard) WriteMessage(log.Level, string) {}

// Writer 把内核日志转成 "LEVEL message" 行交给一个 Printf 风格的函数(服务日志滚动器)。
type Writer struct {
	Printf func(level, format string, a ...any)
}

func (w Writer) WriteMessage(level log.Level, message string) {
	if w.Printf != nil {
		w.Printf(strings.ToUpper(log.FormatLevel(level)), "%s", message)
	}
}

// 大坑:sing-box 的 New 会把注册表放进 ctx;要在 New 之前就把注册表建好,之后才能从同一个 ctx 拿到 Clash 服务等对象。
func newContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = service.ContextWithDefaultRegistry(include.Context(ctx))
	return ctx, cancel
}

func parse(ctx context.Context, raw []byte) (option.Options, error) {
	var opt option.Options
	if err := opt.UnmarshalJSONContext(ctx, raw); err != nil {
		return opt, fmt.Errorf("解析配置: %w", err)
	}
	return opt, nil
}

// Validate 干跑:解析并构造全部对象但不启动,随即关闭。抓解析层抓不到的错误。
func (c *Core) Validate(raw []byte) error {
	ctx, cancel := newContext()
	defer cancel()
	opt, err := parse(ctx, raw)
	if err != nil {
		return err
	}
	box, err := sb.New(sb.Options{Context: ctx, Options: opt, PlatformLogWriter: discard{}})
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
	ctx, cancel := newContext()
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
