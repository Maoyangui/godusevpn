package core

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	quic "github.com/sagernet/quic-go"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/tuic"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 会话看护:给 hysteria2 / tuic 这两种出站套一层。
//
// 为什么非要有它:这两种协议一个节点 = 一条 QUIC 会话,本机全部流量都在这一条上跑。sing-box 的出站
// 只在两种情况下换会话 —— QUIC 自己判定连接死了(30 秒收不到任何包),或者收到"网络变化"通知。
// 一条"活着却不投递"的会话(能收到对端的 ACK / 心跳,开的流却一个都不通)它认不出来,会一直复用;
// 而"网络变化"来一次,它把**所有**这类出站的会话一起拆掉,拆完就不管,要等下一个请求去冷启动,
// 那时候已经有几百条连接排在后面。2026-09-20 这台 Windows 整机断网九分钟,就是这两条凑在一起:
// 服务端空闲、同一台服务器上 TCP 的 anytls 零错误,只有这条 QUIC 会话废了,客户端却既没发现也没重建,
// 健康检查三分钟才一次、还要连败两次才算不对劲。
//
// 这层做三件事,全都在隧道内部,不新增任何出网路径,不碰路由、闸、网卡:
//  1. 记账:开流失败、握手期内就出错、或者一个字节都没收到就被关掉,都算这条会话的一次失败;
//     任何一条流收到过数据就清零。连败到阈值、且这段时间没有任何成功,判定会话已废。
//  2. 判废之后拆掉这条会话(和"网络变化"走同一条路),下一次拨号就对**同一个节点**重新握手;
//     同时通知守护进程立刻做一次健康检查,别再等三分钟 —— 状态机该降级就降级,该临时换线就换线。
//  3. 会话被拆(不管是判废还是网络变化)之后,如果这个节点正是当前在用的、用户也还想连着,
//     后台立刻把会话重新握好(单飞、带退避),让下一个请求直接走热会话,而不是排队等冷启动。
//
// 只看护 hysteria2 与 tuic:anytls 是 TCP 连接池、vless / vmess / trojan 每条连接各自握手,
// 它们没有"一条会话承载一切"的问题,也没有实现"网络变化即拆会话"。

// SessionPolicy 守护进程给会话看护的几个回答与回调。可以为 nil(干跑、测速用的临时实例):那时判废照样拆会话,但不预热、也不通知守护进程。
type SessionPolicy interface {
	// Wanted 用户此刻想连着(没点断开)且内核在跑。拆掉会话后要不要预热,以它为准。
	Wanted() bool
	// InUse 这个出站是不是当前正在用的节点(手动选中的,或自动选择此刻落到的)。只预热在用的那个:
	// 一次网络变化会把几十条会话一起拆掉,全都重新握手就是一场自己制造的连接风暴。
	InUse(tag string) bool
	// OnSick 会话被判定已废。守护进程据此立刻做健康检查并记数。
	OnSick(tag, reason string)
	// OnRebuild 会话被拆掉后开始重建。reason 是拆掉的原因("网络变化" / "判废")。
	OnRebuild(tag, reason string)
	Logf(format string, a ...any)
}

// 判废的阈值。数字偏保守:误判的代价是一次几百毫秒到几秒的重连,漏判的代价是整机断网几分钟。
const (
	sickFailures     = 5                // 连败这么多次(期间没有任何一条流收到过数据)
	sickWindow       = 40 * time.Second // 连败要发生在这么长时间内,更早的失败不算
	sickCooldown     = 45 * time.Second // 两次判废之间至少隔这么久,免得会话刚重建又被判
	handshakeWindow  = 20 * time.Second // 一条流建立后这么久之内出错、或一个字节没收到就被关,算握手失败
	warmBackoffFirst = time.Second      // 预热失败后先等这么久再试,之后翻倍(1 秒、2 秒)
	warmTries        = 3                // 预热最多试这么多次,最后一次失败就不再等
)

// sessionWatch 一个被看护的出站。嵌入原出站:Type / Tag / Network / Dependencies 原样透出。
type sessionWatch struct {
	adapter.Outbound
	inner  adapter.InterfaceUpdateListener // 原出站的"网络变化"入口,判废时也走它
	policy func() SessionPolicy
	tag    string

	mu        sync.Mutex
	fails     int       // 连续失败次数
	firstFail time.Time // 这一串失败从什么时候开始
	lastOK    time.Time // 最近一次有流收到数据
	lastSick  time.Time // 最近一次判废
	// touched 上一次 touch 的墙钟纳秒,只做每秒一次的节流:长流每收一次数据都想刷新 lastOK,不能每次都抢锁。
	// 判废用的时刻始终是 lastOK(带单调读数的 time.Time),墙钟被拨动也不受影响。
	touched atomic.Int64
	warming atomic.Bool
	now     func() time.Time // 测试用
}

var (
	_ adapter.Outbound                = (*sessionWatch)(nil)
	_ adapter.InterfaceUpdateListener = (*sessionWatch)(nil)
)

// outboundRegistry sing-box 的出站注册表,hysteria2 与 tuic 换成带看护的构造函数。
// policy 延迟取值:注册表在内核构造时建好,守护进程的策略随时可能换。
func outboundRegistry(policy func() SessionPolicy) *outbound.Registry {
	registry := include.OutboundRegistry()
	outbound.Register[option.Hysteria2OutboundOptions](registry, C.TypeHysteria2,
		func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.Hysteria2OutboundOptions) (adapter.Outbound, error) {
			ob, err := hysteria2.NewOutbound(ctx, router, logger, tag, options)
			if err != nil {
				return nil, err
			}
			return watchOutbound(ob, tag, policy), nil
		})
	outbound.Register[option.TUICOutboundOptions](registry, C.TypeTUIC,
		func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TUICOutboundOptions) (adapter.Outbound, error) {
			ob, err := tuic.NewOutbound(ctx, router, logger, tag, options)
			if err != nil {
				return nil, err
			}
			return watchOutbound(ob, tag, policy), nil
		})
	return registry
}

func watchOutbound(ob adapter.Outbound, tag string, policy func() SessionPolicy) adapter.Outbound {
	l, ok := ob.(adapter.InterfaceUpdateListener)
	if !ok {
		return ob // 拆不了会话的出站看护不了,原样用
	}
	return &sessionWatch{Outbound: ob, inner: l, policy: policy, tag: tag, now: time.Now}
}

func (w *sessionWatch) pol() SessionPolicy {
	if w.policy == nil {
		return nil
	}
	return w.policy()
}

func (w *sessionWatch) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := w.Outbound.DialContext(ctx, network, destination)
	if err != nil {
		w.noteFail("拨号: " + err.Error())
		return nil, err
	}
	return &watchedConn{Conn: conn, w: w, born: w.now()}, nil
}

func (w *sessionWatch) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	pc, err := w.Outbound.ListenPacket(ctx, destination)
	if err != nil {
		w.noteFail("UDP: " + err.Error())
		return nil, err
	}
	return pc, nil
}

// InterfaceUpdated "网络变化":原出站会把会话拆掉。拆完按需预热。
func (w *sessionWatch) InterfaceUpdated(ctx context.Context) {
	w.inner.InterfaceUpdated(ctx)
	w.resetLocked()
	w.rebuilt("网络变化")
}

func (w *sessionWatch) Close() error {
	if c, ok := w.Outbound.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

func (w *sessionWatch) resetLocked() {
	w.mu.Lock()
	w.fails, w.firstFail = 0, time.Time{}
	w.mu.Unlock()
}

// noteOK 有一条流收到数据了:这条会话活着,清掉失败计数。
func (w *sessionWatch) noteOK() {
	now := w.now()
	w.touched.Store(now.UnixNano())
	w.mu.Lock()
	w.fails, w.firstFail = 0, time.Time{}
	w.lastOK = now
	w.mu.Unlock()
}

// touch 一条已经收到过数据的流又收到了数据:刷新"最近有流收到数据"的时刻,长流持续收数据也算会话活着。
// 每秒最多抢一次锁。
func (w *sessionWatch) touch() {
	now := w.now()
	if now.UnixNano()-w.touched.Load() < int64(time.Second) {
		return
	}
	w.touched.Store(now.UnixNano())
	w.mu.Lock()
	if now.After(w.lastOK) {
		w.lastOK = now
	}
	w.mu.Unlock()
}

// noteFail 记一次失败;攒够了就判废。
func (w *sessionWatch) noteFail(reason string) {
	now := w.now()
	w.mu.Lock()
	if w.fails == 0 || now.Sub(w.firstFail) > sickWindow {
		w.fails, w.firstFail = 0, now
	}
	w.fails++
	sick := w.fails >= sickFailures && now.Sub(w.lastOK) >= sickWindow/2 && now.Sub(w.lastSick) >= sickCooldown
	if sick {
		w.lastSick = now
		w.fails, w.firstFail = 0, time.Time{}
	}
	w.mu.Unlock()
	if !sick {
		return
	}
	p := w.pol()
	if p != nil {
		p.Logf("节点「%s」的会话已废(连续 %d 次失败,最近一次: %s),拆掉重建", w.tag, sickFailures, reason)
		// 只有当前在用的节点才记数、才催健康检查:自动模式下别的出站的会话废了,与用户此刻的网络无关
		if p.Wanted() && p.InUse(w.tag) {
			p.OnSick(w.tag, reason)
		}
	}
	// 和"网络变化"走同一条路拆掉会话:下一次拨号就对同一个节点重新握手。
	w.inner.InterfaceUpdated(context.Background())
	w.rebuilt("判废")
}

// rebuilt 会话刚被拆掉。当前在用的节点、用户又想连着,就后台把会话预热好。
func (w *sessionWatch) rebuilt(reason string) {
	p := w.pol()
	if p == nil || !p.Wanted() || !p.InUse(w.tag) {
		return
	}
	p.OnRebuild(w.tag, reason)
	if !w.warming.CompareAndSwap(false, true) {
		return // 已经有一个预热在跑
	}
	go w.warm(p)
}

// warm 预热:开一条 UDP 会话再立刻关掉,只为让出站把 QUIC 握手做完。不发任何数据,不指向任何目标。
// 单飞 + 退避,最多几次;用户中途点了断开就停。
func (w *sessionWatch) warm(p SessionPolicy) {
	defer w.warming.Store(false)
	delay := warmBackoffFirst
	for i := 0; i < warmTries; i++ {
		if !p.Wanted() || !p.InUse(w.tag) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		pc, err := w.Outbound.ListenPacket(ctx, M.Socksaddr{})
		cancel()
		if err == nil {
			_ = pc.Close()
			w.noteOK()
			p.Logf("节点「%s」的会话已重新握好", w.tag)
			return
		}
		if errors.Is(err, os.ErrInvalid) || strings.Contains(err.Error(), "UDP disabled") {
			return // 服务端不给 UDP、或这条出站根本没开 UDP:预热不了,等真实请求去握手
		}
		if i == warmTries-1 {
			return // 最后一次也失败了,不再白等
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// watchedConn 一条流:头一次收到数据就报"好";握手期内出错、或一个字节没收到就被关,报"坏"。
type watchedConn struct {
	net.Conn
	w    *sessionWatch
	born time.Time
	got  atomic.Bool // 收到过数据
	done atomic.Bool // 已经报过结果
}

// Read / Write 出错只在握手窗口内当场记失败;窗口之后出的错不在这里"占坑",留给 Close 按"无回应即被关闭"记,
// 否则一条等了半分钟才报错、随后被关掉的流一次都不会被数到。
// 对端明确答复过的错误(hysteria2 的 remote error = 服务端说目标连不上;被服务端重置的流)不算失败 ——
// 那恰恰证明会话活着,只是这个目标不通;连着打开几个不通的网站不该把整条隧道判废。
func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		if !c.got.Swap(true) {
			c.done.Store(true)
			c.w.noteOK()
		} else {
			c.w.touch()
		}
	}
	if err != nil && !c.got.Load() {
		c.noteErr("读", err)
	}
	return n, err
}

func (c *watchedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil && !c.got.Load() {
		c.noteErr("写", err)
	}
	return n, err
}

// noteErr 一条还没收到过数据的流出错了:对端答复过的算"活着",别的只在握手窗口内记失败。
func (c *watchedConn) noteErr(op string, err error) {
	if serverReplied(err) {
		if c.done.CompareAndSwap(false, true) {
			c.w.noteOK()
		}
		return
	}
	if errors.Is(err, io.EOF) {
		return
	}
	if c.w.now().Sub(c.born) <= handshakeWindow && c.done.CompareAndSwap(false, true) {
		c.w.noteFail(op + ": " + err.Error())
	}
}

// NeedHandshake 透传给内核的握手催促。hysteria2 的流要到第一次 Write 才把请求发出去,
// 对端先说话的协议(SSH、SMTP 之类)靠内核看到这个方法后先写一个空包把请求送出去;
// 包了一层内核就看不到它,不催,那些协议就挂死 —— 而且挂死的流还会被记成会话失败。
func (c *watchedConn) NeedHandshake() bool {
	if ec, ok := common.Cast[N.EarlyConn](c.Conn); ok {
		return ec.NeedHandshake()
	}
	return false
}

// serverReplied 错误是不是对端明确答复的结果:hysteria2 的 "remote error: …"(服务端拨目标失败后回的状态),
// 或被对端重置的 QUIC 流。这两种只可能来自活着的会话。
func serverReplied(err error) bool {
	var se *quic.StreamError
	if errors.As(err, &se) && se.Remote {
		return true
	}
	return strings.Contains(err.Error(), "remote error")
}

// Close 一个字节都没收到就被关掉、又活过了握手窗口:多半是应用等不到回应自己放弃了 —— 会话"活着却不投递"的典型。
// 握手窗口内被关的不算(浏览器的预连接、取消的请求都是这样),免得把正常波动记成失败。
func (c *watchedConn) Close() error {
	if !c.got.Load() && c.done.CompareAndSwap(false, true) {
		if age := c.w.now().Sub(c.born); age > handshakeWindow {
			c.w.noteFail("无回应即被关闭")
		}
	}
	return c.Conn.Close()
}

func (c *watchedConn) Upstream() any { return c.Conn }
