package core

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	quic "github.com/sagernet/quic-go"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 第一轮审计补的几条:握手催促要透传、对端答复过的错误不算失败、长流持续收数据算会话活着、
// 预热遇到"没开 UDP"直接停、只有当前在用的节点判废才通知守护进程。

// earlyConn 像 hysteria2 的流:第一次 Write 才把请求发出去,所以要告诉内核"还没握手"。
type earlyConn struct {
	net.Conn
	need bool
}

func (c *earlyConn) NeedHandshake() bool { return c.need }

// 内核靠 NeedHandshake 决定要不要先写一个空包把请求送出去;包了一层看不到它,对端先说话的协议就挂死。
func TestWatchedConnPassesNeedHandshake(t *testing.T) {
	w, _, _ := newTestWatch(t, &fakePolicy{})
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	inner := &earlyConn{Conn: a, need: true}
	c := &watchedConn{Conn: inner, w: w, born: w.now()}
	if !c.NeedHandshake() || !N.NeedHandshakeForWrite(c) {
		t.Fatal("内层的流还没握手,看护层必须原样透出")
	}
	inner.need = false
	if c.NeedHandshake() || N.NeedHandshakeForWrite(c) {
		t.Fatal("内层已握手,看护层不该再催")
	}
	plain := &watchedConn{Conn: a, w: w, born: w.now()}
	if plain.NeedHandshake() || N.NeedHandshakeForWrite(plain) {
		t.Fatal("内层没有这个概念时应回 false")
	}
}

// errConn 一读就回指定错误的流。
type errConn struct {
	net.Conn
	err error
}

func (c *errConn) Read([]byte) (int, error)  { return 0, c.err }
func (c *errConn) Write([]byte) (int, error) { return 0, c.err }

// 服务端明确答复"目标连不上"(hysteria2 的 remote error)或把流重置:会话活着,不记失败,反而算一次成功。
func TestWatchedConnServerRepliedIsNotFailure(t *testing.T) {
	for name, err := range map[string]error{
		"hysteria2 remote error": errors.New("remote error: dial tcp 1.2.3.4:443: connection refused"),
		"对端重置的流":                 &quic.StreamError{StreamID: 4, ErrorCode: 0, Remote: true},
	} {
		t.Run(name, func(t *testing.T) {
			w, _, clk := newTestWatch(t, &fakePolicy{})
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			c := &watchedConn{Conn: &errConn{Conn: a, err: err}, w: w, born: w.now()}
			if _, rerr := c.Read(make([]byte, 4)); rerr == nil {
				t.Fatal("应把错误原样返回")
			}
			if failsOf(w) != 0 {
				t.Fatalf("对端答复过的错误不该记失败,记了 %d", failsOf(w))
			}
			if !lastOKOf(w).Equal(clk.Now()) {
				t.Fatal("对端答复过说明会话活着,应刷新 lastOK")
			}
			_ = c.Close()
			if failsOf(w) != 0 {
				t.Fatalf("之后关掉也不该再记,记了 %d", failsOf(w))
			}
		})
	}
	// 本端自己取消的流不是对端的答复,握手窗口内照记失败
	w, _, _ := newTestWatch(t, &fakePolicy{})
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &watchedConn{Conn: &errConn{Conn: a, err: &quic.StreamError{StreamID: 4, Remote: false}}, w: w, born: w.now()}
	_, _ = c.Read(make([]byte, 4))
	if failsOf(w) != 1 {
		t.Fatalf("本端取消的流出错应记一次失败,记了 %d", failsOf(w))
	}
}

// 一条长流持续收数据:即使它的第一个字节是很久以前收到的,后面的失败也不能把会话判废。
func TestLongStreamKeepsSessionAlive(t *testing.T) {
	p := &fakePolicy{wanted: true, inUseTag: "hk"}
	w, fo, clk := newTestWatch(t, p)
	conn, peer := dialOK(t, w, fo)
	defer peer.Close()
	receiveOne(t, conn, peer) // 第一个字节:lastOK = 现在
	clk.Advance(30 * time.Second)
	receiveOne(t, conn, peer) // 30 秒后还在收:会话活着
	fo.setDialErr(errors.New("拒绝"))
	failDial(t, w, sickFailures)
	if fo.updated.Load() != 0 || len(p.sickCalls()) != 0 {
		t.Fatalf("长流刚收到数据,不该判废:拆了 %d 次, sick=%v", fo.updated.Load(), p.sickCalls())
	}
	// 对照:同样的 30 秒里没有任何流收到数据,就该判废
	w2, fo2, clk2 := newTestWatch(t, &fakePolicy{wanted: true, inUseTag: "hk"})
	conn2, peer2 := dialOK(t, w2, fo2)
	defer peer2.Close()
	receiveOne(t, conn2, peer2)
	clk2.Advance(30 * time.Second)
	fo2.setDialErr(errors.New("拒绝"))
	failDial(t, w2, sickFailures)
	if fo2.updated.Load() != 1 {
		t.Fatalf("30 秒没有任何流收到数据、连败 %d 次应判废,拆了 %d 次", sickFailures, fo2.updated.Load())
	}
}

// 出站根本没开 UDP(network 只有 tcp)时 ListenPacket 回 os.ErrInvalid:预热不了,一次就停,别白等三轮。
func TestWarmStopsWhenUDPUnsupported(t *testing.T) {
	p := &fakePolicy{wanted: true, inUseTag: "hk"}
	w, fo, _ := newTestWatch(t, p)
	fo.setListenErr(os.ErrInvalid)
	w.InterfaceUpdated(context.Background())
	waitFor(t, "预热结束", func() bool { return !w.warming.Load() })
	if n := fo.listens.Load(); n != 1 {
		t.Fatalf("没开 UDP 应只试一次就停,试了 %d 次", n)
	}
}

// 判废时只有当前在用的节点才通知守护进程(记数 + 催健康检查);会话照样拆。
func TestSickOnlyNotifiesWhenInUse(t *testing.T) {
	p := &fakePolicy{wanted: true, inUseTag: "jp"} // 当前用的是别的节点
	w, fo, _ := newTestWatch(t, p)
	fo.setDialErr(errors.New("拒绝"))
	failDial(t, w, sickFailures)
	if fo.updated.Load() != 1 {
		t.Fatalf("不在用的节点判废也要拆会话,拆了 %d 次", fo.updated.Load())
	}
	if len(p.sickCalls()) != 0 || len(p.rebuildCalls()) != 0 {
		t.Fatalf("不在用的节点不该通知守护进程: sick=%v rebuild=%v", p.sickCalls(), p.rebuildCalls())
	}
	if !p.hasLog("已废") {
		t.Fatal("日志还是要有")
	}
	// 用户已经点了断开:在用的节点判废也不再通知(没有健康检查可催,也没有重建可记)
	p2 := &fakePolicy{wanted: false, inUseTag: "hk"}
	w2, fo2, _ := newTestWatch(t, p2)
	fo2.setDialErr(errors.New("拒绝"))
	failDial(t, w2, sickFailures)
	if fo2.updated.Load() != 1 || len(p2.sickCalls()) != 0 || len(p2.rebuildCalls()) != 0 {
		t.Fatalf("断开后判废:拆 %d 次, sick=%v rebuild=%v", fo2.updated.Load(), p2.sickCalls(), p2.rebuildCalls())
	}
	_ = M.Socksaddr{}
}
