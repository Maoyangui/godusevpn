package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/tuic"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 这里全是假出站、假策略、假时钟:不开任何真实网络连接,不起内核。
// 会话看护的判定全靠 sessionWatch.now,把它换成可控时钟就能精确走到每一个边界。

// fakeOutbound 假的 hysteria2 出站:拨号成功给一条 net.Pipe(对端留给测试喂数据 / 挂断),
// 失败按 dialErr 返回;记下"网络变化"被调了几次 —— 判废与网络变化都要走到这里。
type fakeOutbound struct {
	mu         sync.Mutex
	dialErr    error         // 非 nil 时 DialContext 直接失败
	listenErr  error         // 非 nil 时 ListenPacket 直接失败
	listenGate chan struct{} // 非 nil 时 ListenPacket 先卡到它关闭,用来让预热"跑着"
	peers      []net.Conn    // 每条 pipe 的对端
	lastPC     *fakePacketConn
	updated    atomic.Int32 // InterfaceUpdated 被调次数
	listens    atomic.Int32 // ListenPacket 被调次数(预热每试一次加一)
	closed     atomic.Int32
}

var (
	_ adapter.Outbound                = (*fakeOutbound)(nil)
	_ adapter.InterfaceUpdateListener = (*fakeOutbound)(nil)
)

func (f *fakeOutbound) Type() string           { return C.TypeHysteria2 }
func (f *fakeOutbound) Tag() string            { return "hk" }
func (f *fakeOutbound) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (f *fakeOutbound) Dependencies() []string { return nil }
func (f *fakeOutbound) InterfaceUpdated(context.Context) {
	f.updated.Add(1)
}
func (f *fakeOutbound) Close() error {
	f.closed.Add(1)
	return nil
}

func (f *fakeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	c, peer := net.Pipe()
	f.peers = append(f.peers, peer)
	return c, nil
}

func (f *fakeOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	f.listens.Add(1)
	f.mu.Lock()
	gate, err := f.listenGate, f.listenErr
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	pc := &fakePacketConn{}
	f.mu.Lock()
	f.lastPC = pc
	f.mu.Unlock()
	return pc, nil
}

func (f *fakeOutbound) setDialErr(err error) {
	f.mu.Lock()
	f.dialErr = err
	f.mu.Unlock()
}

func (f *fakeOutbound) setListenErr(err error) {
	f.mu.Lock()
	f.listenErr = err
	f.mu.Unlock()
}

func (f *fakeOutbound) setListenGate(gate chan struct{}) {
	f.mu.Lock()
	f.listenGate = gate
	f.mu.Unlock()
}

// lastPeer 最近一次拨号那条 pipe 的对端。
func (f *fakeOutbound) lastPeer() net.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peers[len(f.peers)-1]
}

func (f *fakeOutbound) lastPacketConn() *fakePacketConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPC
}

// fakePacketConn 预热开的"UDP 会话":什么都不做,只记有没有被关掉。
type fakePacketConn struct{ closed atomic.Bool }

func (*fakePacketConn) ReadFrom([]byte) (int, net.Addr, error)    { return 0, nil, io.EOF }
func (*fakePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (*fakePacketConn) LocalAddr() net.Addr                       { return nil }
func (*fakePacketConn) SetDeadline(time.Time) error               { return nil }
func (*fakePacketConn) SetReadDeadline(time.Time) error           { return nil }
func (*fakePacketConn) SetWriteDeadline(time.Time) error          { return nil }
func (c *fakePacketConn) Close() error {
	c.closed.Store(true)
	return nil
}

// fakePolicy 假的守护进程策略:Wanted / InUse 可控,记下 OnSick / OnRebuild 的每一次调用。
// 预热在别的 goroutine 里问 Wanted / InUse,所以要加锁。
type fakePolicy struct {
	mu       sync.Mutex
	wanted   bool
	inUseTag string // InUse 只对这个 tag 回答"是"
	sick     []string
	rebuild  []string
	logs     []string
}

func (p *fakePolicy) Wanted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.wanted
}

func (p *fakePolicy) InUse(tag string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return tag == p.inUseTag
}

func (p *fakePolicy) OnSick(tag, reason string) {
	p.mu.Lock()
	p.sick = append(p.sick, tag+"|"+reason)
	p.mu.Unlock()
}

func (p *fakePolicy) OnRebuild(tag, reason string) {
	p.mu.Lock()
	p.rebuild = append(p.rebuild, tag+"|"+reason)
	p.mu.Unlock()
}

func (p *fakePolicy) Logf(format string, a ...any) {
	p.mu.Lock()
	p.logs = append(p.logs, fmt.Sprintf(format, a...))
	p.mu.Unlock()
}

func (p *fakePolicy) setWanted(v bool) {
	p.mu.Lock()
	p.wanted = v
	p.mu.Unlock()
}

func (p *fakePolicy) sickCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sick...)
}

func (p *fakePolicy) rebuildCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.rebuild...)
}

func (p *fakePolicy) hasLog(sub string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// fakeClock 可控时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newTestWatch 用假出站直接构造一个看护实例,时钟换成可控的。p 为 nil 表示策略函数返回空(内核在跑但没挂策略)。
func newTestWatch(t *testing.T, p SessionPolicy) (*sessionWatch, *fakeOutbound, *fakeClock) {
	t.Helper()
	fo := &fakeOutbound{}
	clk := &fakeClock{t: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}
	ob := watchOutbound(fo, "hk", func() SessionPolicy { return p })
	w, ok := ob.(*sessionWatch)
	if !ok {
		t.Fatalf("实现了 InterfaceUpdated 的出站应被套上看护,得到 %T", ob)
	}
	w.now = clk.Now
	return w, fo, clk
}

// failDial 让拨号失败 n 次(假出站已设成失败)。
func failDial(t *testing.T, w *sessionWatch, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := w.DialContext(context.Background(), N.NetworkTCP, M.Socksaddr{}); err == nil {
			t.Fatal("假出站已设成拨号失败,不该成功")
		}
	}
}

// dialOK 拨一条能通的流,返回流和它的对端。
func dialOK(t *testing.T, w *sessionWatch, fo *fakeOutbound) (net.Conn, net.Conn) {
	t.Helper()
	fo.setDialErr(nil)
	conn, err := w.DialContext(context.Background(), N.NetworkTCP, M.Socksaddr{})
	if err != nil {
		t.Fatalf("假出站没设失败,拨号不该出错: %v", err)
	}
	if _, ok := conn.(*watchedConn); !ok {
		t.Fatalf("看护出站拨出的流应被包一层,得到 %T", conn)
	}
	return conn, fo.lastPeer()
}

// receiveOne 让对端喂一个字节、流读到它:这条会话"活着"的证据。
func receiveOne(t *testing.T, conn, peer net.Conn) {
	t.Helper()
	go func() { _, _ = peer.Write([]byte("x")) }()
	buf := make([]byte, 8)
	n, err := conn.Read(buf)
	if n != 1 || err != nil {
		t.Fatalf("应读到对端写的那一个字节,得到 n=%d err=%v", n, err)
	}
}

func failsOf(w *sessionWatch) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fails
}

func lastOKOf(w *sessionWatch) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastOK
}

// waitFor 等一个后台条件成立,最多 5 秒。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等了 5 秒还没等到: %s", what)
}

// 连续 sickFailures 次拨号失败才判废,且只判一次:原出站的"网络变化"被调一次、OnSick 一次。
// 差一次都不能判 —— 误判一次就是一次重连。
func TestSessionWatchSickAfterConsecutiveFailures(t *testing.T) {
	p := &fakePolicy{} // 用户没想连着:判废后不预热,本测试只看判废本身
	w, fo, _ := newTestWatch(t, p)
	fo.setDialErr(errors.New("拒绝"))

	failDial(t, w, sickFailures-1)
	if fo.updated.Load() != 0 || len(p.sickCalls()) != 0 {
		t.Fatalf("连败 %d 次还差一次,不该判废: 拆会话 %d 次, OnSick %v", sickFailures-1, fo.updated.Load(), p.sickCalls())
	}
	failDial(t, w, 1)
	if got := fo.updated.Load(); got != 1 {
		t.Fatalf("连败 %d 次应判废并拆一次会话,拆了 %d 次", sickFailures, got)
	}
	sick := p.sickCalls()
	if len(sick) != 1 || !strings.HasPrefix(sick[0], "hk|拨号: 拒绝") {
		t.Fatalf("OnSick 应被调一次、带节点 tag 与拨号原因,得到 %v", sick)
	}
	if !p.hasLog("已废") {
		t.Fatalf("判废要写日志,得到 %v", p.logs)
	}
	if got := p.rebuildCalls(); len(got) != 0 {
		t.Fatalf("用户没想连着,不该开始重建: %v", got)
	}
	if failsOf(w) != 0 {
		t.Fatal("判废之后失败计数该清零")
	}
}

// 期间有一条流收到数据,失败计数清零:之前攒的失败作废,要重新攒满才判废。
func TestSessionWatchDataResetsFailures(t *testing.T) {
	p := &fakePolicy{}
	w, fo, clk := newTestWatch(t, p)
	fo.setDialErr(errors.New("拒绝"))
	failDial(t, w, sickFailures-1)
	if failsOf(w) != sickFailures-1 {
		t.Fatalf("应记下 %d 次失败,得到 %d", sickFailures-1, failsOf(w))
	}

	conn, peer := dialOK(t, w, fo)
	receiveOne(t, conn, peer)
	if failsOf(w) != 0 {
		t.Fatalf("一条流收到数据后失败计数该清零,还是 %d", failsOf(w))
	}
	if !lastOKOf(w).Equal(clk.Now()) {
		t.Fatal("收到数据要记下时间")
	}
	_ = conn.Close()
	_ = peer.Close()

	// 把"最近有成功"这道闸放开(推过 sickWindow/2),单独看计数有没有清零
	clk.Advance(sickWindow / 2)
	fo.setDialErr(errors.New("拒绝"))
	failDial(t, w, sickFailures-1)
	if fo.updated.Load() != 0 {
		t.Fatal("计数没清零:成功之前的失败还被算进去了")
	}
	failDial(t, w, 1)
	if fo.updated.Load() != 1 {
		t.Fatal("清零后重新攒满应判废")
	}
}

// 刚收到过数据(sickWindow/2 之内)就算连败到阈值也不判:那多半是瞬时抖动,不是会话废了。
func TestSessionWatchRecentSuccessBlocksSick(t *testing.T) {
	p := &fakePolicy{}
	w, fo, clk := newTestWatch(t, p)
	conn, peer := dialOK(t, w, fo)
	receiveOne(t, conn, peer)
	_ = conn.Close()
	_ = peer.Close()

	fo.setDialErr(errors.New("拒绝"))
	failDial(t, w, sickFailures)
	if fo.updated.Load() != 0 {
		t.Fatal("刚收到过数据就连败,不该立刻判废")
	}
	clk.Advance(sickWindow / 2)
	failDial(t, w, 1)
	if fo.updated.Load() != 1 {
		t.Fatal("离最近一次成功够久了,连败应判废")
	}
}

// 失败要发生在 sickWindow 内才连成一串;更早的失败不算。
func TestSessionWatchStaleFailuresExpire(t *testing.T) {
	p := &fakePolicy{}
	w, fo, clk := newTestWatch(t, p)
	fo.setDialErr(errors.New("拒绝"))
	failDial(t, w, sickFailures-1)
	clk.Advance(sickWindow + time.Second)
	failDial(t, w, 1)
	if fo.updated.Load() != 0 {
		t.Fatal("窗口之前的失败不该还算数")
	}
	if failsOf(w) != 1 {
		t.Fatalf("过期后应从 1 重新数,得到 %d", failsOf(w))
	}
	failDial(t, w, sickFailures-1)
	if fo.updated.Load() != 1 {
		t.Fatal("窗口内重新攒满应判废")
	}
}

// 判废后 sickCooldown 内再连败不重复判(会话刚重建又被判就是自己制造的抖动);冷却期过了可以再判。
func TestSessionWatchSickCooldown(t *testing.T) {
	p := &fakePolicy{}
	w, fo, clk := newTestWatch(t, p)
	fo.setDialErr(errors.New("拒绝"))
	failDial(t, w, sickFailures)
	if fo.updated.Load() != 1 {
		t.Fatal("第一次应判废")
	}
	failDial(t, w, sickFailures)
	if fo.updated.Load() != 1 || len(p.sickCalls()) != 1 {
		t.Fatalf("冷却期内不该重复判废: 拆会话 %d 次, OnSick %v", fo.updated.Load(), p.sickCalls())
	}
	clk.Advance(sickCooldown - time.Second)
	failDial(t, w, sickFailures)
	if fo.updated.Load() != 1 {
		t.Fatal("差一秒才过冷却期,不该判")
	}
	clk.Advance(time.Second)
	failDial(t, w, 1)
	if fo.updated.Load() != 2 || len(p.sickCalls()) != 2 {
		t.Fatalf("冷却期过了应能再判: 拆会话 %d 次, OnSick %v", fo.updated.Load(), p.sickCalls())
	}
}

// 一条流怎样才算失败:握手窗口内出错算;窗口内一个字节没收到就被关不算;
// 活过了窗口还是一个字节没收到就被关才算;收到过数据的流怎么都不算。
func TestWatchedConnFailureRules(t *testing.T) {
	pastDeadline := time.Now().Add(-time.Second)

	t.Run("握手窗口内读出错算失败", func(t *testing.T) {
		w, fo, _ := newTestWatch(t, &fakePolicy{})
		conn, peer := dialOK(t, w, fo)
		defer peer.Close()
		_ = conn.SetReadDeadline(pastDeadline)
		if _, err := conn.Read(make([]byte, 8)); err == nil {
			t.Fatal("读超时应出错")
		}
		if failsOf(w) != 1 {
			t.Fatalf("握手期内读出错应记一次失败,得到 %d", failsOf(w))
		}
		_ = conn.Close()
		if failsOf(w) != 1 {
			t.Fatalf("同一条流只报一次结果,关掉后得到 %d", failsOf(w))
		}
	})

	t.Run("握手窗口内写出错算失败", func(t *testing.T) {
		w, fo, _ := newTestWatch(t, &fakePolicy{})
		conn, peer := dialOK(t, w, fo)
		_ = peer.Close()
		if _, err := conn.Write([]byte("x")); err == nil {
			t.Fatal("对端已关,写应出错")
		}
		if failsOf(w) != 1 {
			t.Fatalf("握手期内写出错应记一次失败,得到 %d", failsOf(w))
		}
	})

	t.Run("对端正常挂断不算失败", func(t *testing.T) {
		w, fo, _ := newTestWatch(t, &fakePolicy{})
		conn, peer := dialOK(t, w, fo)
		_ = peer.Close()
		if _, err := conn.Read(make([]byte, 8)); !errors.Is(err, io.EOF) {
			t.Fatalf("对端关了应读到 EOF,得到 %v", err)
		}
		_ = conn.Close()
		if failsOf(w) != 0 {
			t.Fatalf("EOF 不是会话的错,不该记失败,得到 %d", failsOf(w))
		}
	})

	t.Run("握手窗口内没收到字节就关不算失败", func(t *testing.T) {
		w, fo, clk := newTestWatch(t, &fakePolicy{})
		conn, peer := dialOK(t, w, fo)
		defer peer.Close()
		clk.Advance(handshakeWindow / 2)
		_ = conn.Close()
		if failsOf(w) != 0 {
			t.Fatalf("窗口内被关(预连接、取消的请求)不该记失败,得到 %d", failsOf(w))
		}
	})

	t.Run("超过握手窗口没收到字节才关算失败", func(t *testing.T) {
		w, fo, clk := newTestWatch(t, &fakePolicy{})
		conn, peer := dialOK(t, w, fo)
		defer peer.Close()
		clk.Advance(handshakeWindow + time.Second)
		_ = conn.Close()
		if failsOf(w) != 1 {
			t.Fatalf("活过了窗口还一个字节没收到就被关,应记一次失败,得到 %d", failsOf(w))
		}
		_ = conn.Close()
		if failsOf(w) != 1 {
			t.Fatalf("重复 Close 不该重复记,得到 %d", failsOf(w))
		}
	})

	t.Run("超过握手窗口后再出错不算握手失败", func(t *testing.T) {
		w, fo, clk := newTestWatch(t, &fakePolicy{})
		conn, peer := dialOK(t, w, fo)
		defer peer.Close()
		clk.Advance(handshakeWindow + time.Second)
		_ = conn.SetReadDeadline(pastDeadline)
		if _, err := conn.Read(make([]byte, 8)); err == nil {
			t.Fatal("读超时应出错")
		}
		if failsOf(w) != 0 {
			t.Fatalf("窗口之后的错误不是握手失败,得到 %d", failsOf(w))
		}
	})

	t.Run("收到过数据的流怎么都不算失败", func(t *testing.T) {
		w, fo, clk := newTestWatch(t, &fakePolicy{})
		conn, peer := dialOK(t, w, fo)
		receiveOne(t, conn, peer)
		_ = conn.SetReadDeadline(pastDeadline)
		if _, err := conn.Read(make([]byte, 8)); err == nil {
			t.Fatal("读超时应出错")
		}
		_ = peer.Close()
		_, _ = conn.Write([]byte("x"))
		clk.Advance(handshakeWindow + time.Second)
		_ = conn.Close()
		if failsOf(w) != 0 {
			t.Fatalf("收到过数据说明会话活着,后面的错误、关闭都不该记,得到 %d", failsOf(w))
		}
	})

	t.Run("窗口后读出错再被关掉算一次无回应", func(t *testing.T) {
		p := &fakePolicy{}
		w, fo, clk := newTestWatch(t, p)
		conn, peer := dialOK(t, w, fo)
		defer peer.Close()
		clk.Advance(handshakeWindow + time.Second)
		_ = conn.SetReadDeadline(pastDeadline)
		if _, err := conn.Read(make([]byte, 8)); err == nil {
			t.Fatal("读超时应出错")
		}
		if failsOf(w) != 0 {
			t.Fatalf("握手窗口之后读出错不当场记,得到 %d", failsOf(w))
		}
		_ = conn.Close()
		if failsOf(w) != 1 {
			t.Fatalf("随后被关掉应记一次「无回应即被关闭」,得到 %d", failsOf(w))
		}
		_ = conn.Close()
		if failsOf(w) != 1 {
			t.Fatalf("重复关不重复记,得到 %d", failsOf(w))
		}
	})

	t.Run("流失败也能攒到判废", func(t *testing.T) {
		p := &fakePolicy{}
		w, fo, clk := newTestWatch(t, p)
		// 几条流同时开、活过窗口后一起被关:失败要落在同一个 sickWindow 里才连得成一串
		var conns, peers []net.Conn
		for i := 0; i < sickFailures; i++ {
			conn, peer := dialOK(t, w, fo)
			conns, peers = append(conns, conn), append(peers, peer)
		}
		clk.Advance(handshakeWindow + time.Second)
		for i := range conns {
			_ = conns[i].Close()
			_ = peers[i].Close()
		}
		if fo.updated.Load() != 1 {
			t.Fatalf("%d 条流无回应被关应判废一次,拆了 %d 次", sickFailures, fo.updated.Load())
		}
		if sick := p.sickCalls(); len(sick) != 1 || sick[0] != "hk|无回应即被关闭" {
			t.Fatalf("OnSick 应带上原因,得到 %v", sick)
		}
	})
}

// "网络变化"要传到原出站;之后当前在用、用户也想连着,才预热,而且预热单飞。
func TestSessionWatchInterfaceUpdated(t *testing.T) {
	t.Run("在用且想连着:拆完预热且单飞", func(t *testing.T) {
		p := &fakePolicy{wanted: true, inUseTag: "hk"}
		w, fo, clk := newTestWatch(t, p)
		gate := make(chan struct{})
		fo.setListenGate(gate)

		w.InterfaceUpdated(context.Background())
		if fo.updated.Load() != 1 {
			t.Fatalf("网络变化没有传到原出站: %d", fo.updated.Load())
		}
		if got := p.rebuildCalls(); len(got) != 1 || got[0] != "hk|网络变化" {
			t.Fatalf("OnRebuild 应被调一次、原因是网络变化,得到 %v", got)
		}
		waitFor(t, "预热开始", func() bool { return fo.listens.Load() == 1 })
		if !w.warming.Load() {
			t.Fatal("预热跑着的时候 warming 应为真")
		}

		// 预热还卡着,又来一次网络变化:拆会话、报重建照做,但不能再起一个预热
		w.InterfaceUpdated(context.Background())
		if fo.updated.Load() != 2 || len(p.rebuildCalls()) != 2 {
			t.Fatalf("第二次网络变化也要拆会话并报重建: 拆 %d 次, OnRebuild %v", fo.updated.Load(), p.rebuildCalls())
		}
		time.Sleep(100 * time.Millisecond)
		if fo.listens.Load() != 1 {
			t.Fatalf("预热应单飞:已有一个在跑就不该再起一个,ListenPacket 被调了 %d 次", fo.listens.Load())
		}

		close(gate)
		waitFor(t, "预热结束", func() bool { return !w.warming.Load() })
		if fo.listens.Load() != 1 {
			t.Fatalf("第一次就握好了,不该再试: %d", fo.listens.Load())
		}
		if pc := fo.lastPacketConn(); pc == nil || !pc.closed.Load() {
			t.Fatal("预热开的 UDP 会话只为握手,握完要立刻关掉")
		}
		if !lastOKOf(w).Equal(clk.Now()) {
			t.Fatal("预热握好算一次成功,要记下时间")
		}
		if !p.hasLog("重新握好") {
			t.Fatalf("预热成功要写日志,得到 %v", p.logs)
		}

		// 上一个预热结束了,再来一次网络变化就能再预热
		fo.setListenGate(nil)
		w.InterfaceUpdated(context.Background())
		waitFor(t, "第二次预热", func() bool { return fo.listens.Load() == 2 })
		waitFor(t, "第二次预热结束", func() bool { return !w.warming.Load() })
	})

	t.Run("用户没想连着:不预热不报重建", func(t *testing.T) {
		p := &fakePolicy{wanted: false, inUseTag: "hk"}
		w, fo, _ := newTestWatch(t, p)
		w.InterfaceUpdated(context.Background())
		if fo.updated.Load() != 1 {
			t.Fatal("网络变化仍要传到原出站")
		}
		time.Sleep(50 * time.Millisecond)
		if got := p.rebuildCalls(); len(got) != 0 {
			t.Fatalf("用户没想连着,不该报重建: %v", got)
		}
		if fo.listens.Load() != 0 || w.warming.Load() {
			t.Fatal("用户没想连着,不该预热")
		}
	})

	t.Run("不是在用的节点:不预热不报重建", func(t *testing.T) {
		p := &fakePolicy{wanted: true, inUseTag: "tw"}
		w, fo, _ := newTestWatch(t, p)
		w.InterfaceUpdated(context.Background())
		if fo.updated.Load() != 1 {
			t.Fatal("网络变化仍要传到原出站")
		}
		time.Sleep(50 * time.Millisecond)
		if got := p.rebuildCalls(); len(got) != 0 {
			t.Fatalf("不是在用的节点,不该报重建: %v", got)
		}
		if fo.listens.Load() != 0 || w.warming.Load() {
			t.Fatal("几十条会话一起拆时只预热在用的那个,别的不该预热")
		}
	})

	t.Run("判废后也预热", func(t *testing.T) {
		p := &fakePolicy{wanted: true, inUseTag: "hk"}
		w, fo, _ := newTestWatch(t, p)
		fo.setDialErr(errors.New("拒绝"))
		failDial(t, w, sickFailures)
		if got := p.rebuildCalls(); len(got) != 1 || got[0] != "hk|判废" {
			t.Fatalf("判废拆掉会话后应报重建、原因是判废,得到 %v", got)
		}
		waitFor(t, "预热结束", func() bool { return !w.warming.Load() })
		if fo.listens.Load() != 1 {
			t.Fatalf("判废后应预热一次,ListenPacket 被调了 %d 次", fo.listens.Load())
		}
	})

	t.Run("服务端不给 UDP:预热放弃不重试", func(t *testing.T) {
		p := &fakePolicy{wanted: true, inUseTag: "hk"}
		w, fo, _ := newTestWatch(t, p)
		fo.setListenErr(errors.New("UDP disabled by server"))
		w.InterfaceUpdated(context.Background())
		waitFor(t, "预热结束", func() bool { return !w.warming.Load() })
		if fo.listens.Load() != 1 {
			t.Fatalf("UDP disabled 就该放弃,不该退避重试: %d 次", fo.listens.Load())
		}
		if !lastOKOf(w).IsZero() {
			t.Fatal("预热没成功不该记成功")
		}
	})

	t.Run("用户中途点了断开:预热停下", func(t *testing.T) {
		p := &fakePolicy{wanted: true, inUseTag: "hk"}
		w, fo, _ := newTestWatch(t, p)
		fo.setListenErr(errors.New("超时"))
		w.InterfaceUpdated(context.Background())
		waitFor(t, "第一次预热尝试", func() bool { return fo.listens.Load() == 1 })
		p.setWanted(false) // 退避等待期间用户点了断开
		waitFor(t, "预热停下", func() bool { return !w.warming.Load() })
		if fo.listens.Load() != 1 {
			t.Fatalf("用户点了断开,预热就该停,不该再试: %d 次", fo.listens.Load())
		}
	})
}

// policy 为 nil(干跑、测速用的临时实例,或者内核跑着但守护进程没挂策略):判废只拆会话,不预热,不能 panic。
func TestSessionWatchNilPolicy(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}
	for name, pf := range map[string]func() SessionPolicy{
		"没挂策略函数":  nil,
		"策略函数返回空": func() SessionPolicy { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			fo := &fakeOutbound{}
			w := watchOutbound(fo, "hk", pf).(*sessionWatch)
			w.now = clk.Now
			fo.setDialErr(errors.New("拒绝"))
			failDial(t, w, sickFailures)
			if fo.updated.Load() != 1 {
				t.Fatalf("没策略也要拆会话,拆了 %d 次", fo.updated.Load())
			}
			time.Sleep(50 * time.Millisecond)
			if fo.listens.Load() != 0 || w.warming.Load() {
				t.Fatal("没策略不知道用户想不想连,不该预热")
			}
			w.InterfaceUpdated(context.Background())
			if fo.updated.Load() != 2 {
				t.Fatal("网络变化仍要传到原出站")
			}
			if err := w.Close(); err != nil || fo.closed.Load() != 1 {
				t.Fatalf("Close 要传到原出站: err=%v closed=%d", err, fo.closed.Load())
			}
		})
	}
}

// plainOutbound 没有 InterfaceUpdated 的出站:拆不了会话,看护不了。
type plainOutbound struct{ fakeOutbound }

func (*plainOutbound) InterfaceUpdated() {} // 签名不对,不算 InterfaceUpdateListener

func TestWatchOutboundSkipsNonSessionOutbound(t *testing.T) {
	po := &plainOutbound{}
	if _, ok := adapter.Outbound(po).(adapter.InterfaceUpdateListener); ok {
		t.Fatal("测试自己写错了:plainOutbound 不该实现 InterfaceUpdateListener")
	}
	if got := watchOutbound(po, "x", nil); got != adapter.Outbound(po) {
		t.Fatalf("拆不了会话的出站应原样返回,得到 %T", got)
	}
}

// 注册表里 hysteria2 / tuic 换成了带看护的构造函数,其它类型照旧;真的构造出来的是 *sessionWatch,里面包着原出站。
// 构造只建客户端不握手,不开任何网络连接;这里也不去拨它。
func TestOutboundRegistryWatchesQUICOutbounds(t *testing.T) {
	reg := outboundRegistry(nil)
	if o, ok := reg.CreateOptions(C.TypeHysteria2); !ok {
		t.Fatal("注册表里应有 hysteria2")
	} else if _, ok := o.(*option.Hysteria2OutboundOptions); !ok {
		t.Fatalf("hysteria2 的选项类型不对: %T", o)
	}
	if o, ok := reg.CreateOptions(C.TypeTUIC); !ok {
		t.Fatal("注册表里应有 tuic")
	} else if _, ok := o.(*option.TUICOutboundOptions); !ok {
		t.Fatalf("tuic 的选项类型不对: %T", o)
	}
	if _, ok := reg.CreateOptions(C.TypeAnyTLS); !ok {
		t.Fatal("换掉两个类型不能把自带的其它类型弄丢")
	}

	ctx, cancel := newContext(nil)
	defer cancel()
	logger := log.NewNOPFactory().NewLogger("")
	tlsOn := option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: "a.example"}}

	ob, err := reg.CreateOutbound(ctx, nil, logger, "hk", C.TypeHysteria2, &option.Hysteria2OutboundOptions{
		ServerOptions: option.ServerOptions{Server: "192.0.2.1", ServerPort: 443}, Password: "p", OutboundTLSOptionsContainer: tlsOn,
	})
	if err != nil {
		t.Fatalf("构造 hysteria2 出站不该出错: %v", err)
	}
	w, ok := ob.(*sessionWatch)
	if !ok {
		t.Fatalf("hysteria2 出站没套上看护: %T", ob)
	}
	if w.Tag() != "hk" || w.Type() != C.TypeHysteria2 || w.tag != "hk" {
		t.Fatalf("Tag / Type 要原样透出: %s %s %s", w.Tag(), w.Type(), w.tag)
	}
	if _, ok := w.Outbound.(*hysteria2.Outbound); !ok {
		t.Fatalf("看护里面包的应是原 hysteria2 出站: %T", w.Outbound)
	}
	if w.inner == nil || w.now == nil {
		t.Fatal("看护实例的会话拆除入口与时钟都要装好")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("没握过手的出站关掉不该出错: %v", err)
	}

	ob, err = reg.CreateOutbound(ctx, nil, logger, "tw", C.TypeTUIC, &option.TUICOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "192.0.2.2", ServerPort: 443}, UUID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Password: "p", OutboundTLSOptionsContainer: tlsOn,
	})
	if err != nil {
		t.Fatalf("构造 tuic 出站不该出错: %v", err)
	}
	w, ok = ob.(*sessionWatch)
	if !ok {
		t.Fatalf("tuic 出站没套上看护: %T", ob)
	}
	if _, ok := w.Outbound.(*tuic.Outbound); !ok {
		t.Fatalf("看护里面包的应是原 tuic 出站: %T", w.Outbound)
	}
	if w.Tag() != "tw" || w.Type() != C.TypeTUIC {
		t.Fatalf("Tag / Type 要原样透出: %s %s", w.Tag(), w.Type())
	}
	_ = w.Close()
}
