package daemon

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/state"
)

// 节点服务器的三种状态:正常转发、挂了但连接收下不回(被墙、丢包:要等到超时才失败)、挂了而且一收就断(秒失败)。
const (
	nodeUp int32 = iota
	nodeHang
	nodeDrop
	nodeSlow // 照常转发,只是每条连接多憋 slowExtra 才答复(测出来比平时慢,但是通的)
)

// slowExtra nodeSlow 时每条连接多憋的时间:比组的 tolerance(50 毫秒)大得多。
const slowExtra = 400 * time.Millisecond

// fakeSocksNode 本机回环上的假节点:最简单的 SOCKS5(不认证、只做 CONNECT),socks 出站连它。state 随时可改,
// 下一条连接起生效;delay 是每条连接答复前先憋这么久(让这台测出来更慢)。accepted 数一共收下了多少条连接。
type fakeSocksNode struct {
	port     string
	state    atomic.Int32
	accepted atomic.Int32
}

func newFakeSocksNode(t *testing.T, delay time.Duration) *fakeSocksNode {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	n := &fakeSocksNode{port: strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.accepted.Add(1)
			switch n.state.Load() {
			case nodeHang:
				mu.Lock()
				held = append(held, c)
				mu.Unlock()
				continue
			case nodeDrop:
				_ = c.Close()
				continue
			case nodeSlow:
				go serveSocks(c, delay+slowExtra)
				continue
			}
			go serveSocks(c, delay)
		}
	}()
	return n
}

// serveSocks 一条 SOCKS5 连接:问候 → CONNECT → 双向转发。
func serveSocks(c net.Conn, delay time.Duration) {
	defer c.Close()
	buf := make([]byte, 262)
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	if _, err := io.ReadFull(c, buf[:buf[1]]); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return
	}
	var host string
	switch buf[3] {
	case 1:
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 3:
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return
		}
		l := int(buf[0])
		if _, err := io.ReadFull(c, buf[:l]); err != nil {
			return
		}
		host = string(buf[:l])
	case 4:
		if _, err := io.ReadFull(c, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		return
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[:2])
	time.Sleep(delay)
	up, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))), 5*time.Second)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
	_, _ = io.Copy(c, up)
}

// lineFailoverCore 线路组 G = [A, B](A 快、B 慢 100 毫秒,组先落在 A 上),proxy 选着 G;组外还有一台通的 other。
func lineFailoverCore(t *testing.T, d *Daemon, a, b *fakeSocksNode) {
	t.Helper()
	url := liveURL(t)
	d.settings.Selected, d.settings.LineAuto, d.settings.NoExitLookup = "G", true, true
	d.core.QuietAuto([]string{"other"})
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","G","A","B","other"],"default":"G"},`+
		urltestOut("auto", `"A","B","other"`, url)+","+urltestOut("G", `"A","B"`, url)+","+
		`{"type":"socks","tag":"A","server":"127.0.0.1","server_port":`+a.port+`},`+
		`{"type":"socks","tag":"B","server":"127.0.0.1","server_port":`+b.port+`},{"type":"direct","tag":"other"}`)
	waitTested(t, d, "G")
	if now := d.groupNow("G"); now != "A" {
		t.Fatalf("前提:组先落在快的 A 上,实际 %q", now)
	}
	if err := d.health(context.Background()); err != nil {
		t.Fatalf("前提:A 通时健康检查应当通过: %v", err)
	}
}

// 冒烟发现的问题:线路自动下正在用的那台挂了(HY2 的 UDP 被封),组里另一台好好的,守护进程却报 E_NODE_DOWN、
// 整机断网三分多钟才在组里换过去 —— 测速组只在定时测速时换选择(sing-box 自己的定时测速关掉了),
// 健康检查不通只会一格一格往"降级 → 自救"走(每 30 秒一次,第 4 次才自救)。
// 现在:健康检查一发现在用的那台不通,马上只测这一组、换到组里通的那台,这一次健康检查就算过;不跨出这一组。
func TestHealthFailsOverInsideLineGroup(t *testing.T) {
	for _, c := range []struct {
		name string
		dead int32
	}{{"挂了要等超时", nodeHang}, {"挂了秒断", nodeDrop}} {
		t.Run(c.name, func(t *testing.T) {
			a, b := newFakeSocksNode(t, 0), newFakeSocksNode(t, 100*time.Millisecond)
			d := lineDaemon(t)
			lineFailoverCore(t, d, a, b)
			a.state.Store(c.dead)
			start := time.Now()
			err := d.health(context.Background())
			took := time.Since(start)
			if err != nil {
				t.Fatalf("组里 B 是通的,这一次健康检查就该换过去并通过,却报 %v(%s)", err, took)
			}
			if now := d.groupNow("G"); now != "B" {
				t.Fatalf("应在组里换到 B: %q", now)
			}
			if now, _, _ := d.core.Group("proxy"); now != "G" {
				t.Fatalf("不能跨出线路组: proxy 落在 %q", now)
			}
			// 健康检查自己最多 10 秒,组里挂着的那台测到超时 15 秒:一共不到 30 秒(冒烟里是三分多钟)
			if took > 30*time.Second {
				t.Fatalf("从健康检查开始到换过去用了 %s", took)
			}
			if s := d.getSettings(); s.Selected != "G" || !s.LineAuto {
				t.Fatalf("不改设置: %q / %v", s.Selected, s.LineAuto)
			}
			t.Logf("健康检查开始到换到 B 并复测通过: %s", took)
		})
	}
}

// 整组都不通:不跨线路借道(那是状态机走到自救时的事),照常报节点不可用;一分钟内再不通也不再把这一组重测一遍
// (状态机降级后每 30 秒查一次,每次都测整组就是对着几台挂了的服务器一直敲,手机上也费电)。
func TestLineFailoverWholeGroupDownAndGap(t *testing.T) {
	a, b := newFakeSocksNode(t, 0), newFakeSocksNode(t, 100*time.Millisecond)
	d := lineDaemon(t)
	lineFailoverCore(t, d, a, b)
	a.state.Store(nodeDrop)
	b.state.Store(nodeDrop)
	bBefore := b.accepted.Load()
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeDown {
		t.Fatalf("整组不通应报 %s: %v", state.CodeNodeDown, err)
	}
	if now, _, _ := d.core.Group("proxy"); now != "G" {
		t.Fatalf("健康检查里不跨线路借道: proxy 落在 %q", now)
	}
	tested := b.accepted.Load()
	if tested == bBefore {
		t.Fatal("在用的那台不通时应把组里别的服务器测一遍")
	}
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeDown {
		t.Fatalf("还是整组不通: %v", err)
	}
	if n := b.accepted.Load(); n != tested {
		t.Fatalf("一分钟内不该再把这一组重测一遍: B 又被连了 %d 次", n-tested)
	}
}
