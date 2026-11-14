package daemon

import (
	"context"
	"testing"
	"time"
)

// 在用的那台其实是通的(健康检查那一下是偶发失败、或者是测速地址的问题):组里测完还在它上面,
// 不换、不掐连接,照常报这一次不通,交给状态机的节奏。
func TestLineFailoverKeepsMemberThatTestsFine(t *testing.T) {
	a, b := newFakeSocksNode(t, 0), newFakeSocksNode(t, 100*time.Millisecond)
	d := lineDaemon(t)
	lineFailoverCore(t, d, a, b)
	if _, ok := d.lineFailover(context.Background()); ok {
		t.Fatal("在用的 A 测得通:不算换过去")
	}
	if now := d.groupNow("G"); now != "A" {
		t.Fatalf("A 测得通,不该换到 %q", now)
	}
}

// 落在自动选择或单个节点上时不做组内换线(自动选择一组可能上百台,全测一轮要好几分钟,还照旧走状态机那条路)。
func TestLineFailoverOnlyForLineGroups(t *testing.T) {
	a, b := newFakeSocksNode(t, 0), newFakeSocksNode(t, 100*time.Millisecond)
	d := lineDaemon(t)
	lineFailoverCore(t, d, a, b)
	for _, sel := range []string{"auto", "A"} {
		if err := d.core.Select("proxy", sel); err != nil {
			t.Fatal(err)
		}
		before := b.accepted.Load()
		if _, ok := d.lineFailover(context.Background()); ok {
			t.Fatalf("proxy 在 %s 上:不该做组内换线", sel)
		}
		if n := b.accepted.Load(); n != before {
			t.Fatalf("proxy 在 %s 上:不该去测线路组的成员", sel)
		}
	}
}

// 不等三分钟一次的健康检查:内核里线路组正在用的那台一连接失败(sing-box 会把它的测速记录删掉),
// 巡检看到了就马上敲一次健康检查;同一台挂着只敲一次,恢复(又测通)之后再挂再敲。不发任何网络请求。
func TestLineWatchKicksHealthCheck(t *testing.T) {
	a, b := newFakeSocksNode(t, 0), newFakeSocksNode(t, 100*time.Millisecond)
	d := lineDaemon(t)
	lineFailoverCore(t, d, a, b)
	if d.lineWatchTick() {
		t.Fatal("在用的那台好好的:不该敲")
	}
	a.state.Store(nodeDrop)
	before := a.accepted.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = d.core.URLTest(ctx, "proxy", "http://127.0.0.1:1/") // 一条经线路组的连接失败了
	cancel()
	if a.accepted.Load() == before {
		t.Fatal("前提:那条连接应当走到 A")
	}
	sent := a.accepted.Load()
	if !d.lineWatchTick() {
		t.Fatal("线路组在用的那台连接失败了:应马上敲一次健康检查")
	}
	if d.lineWatchTick() {
		t.Fatal("同一台挂着只敲一次")
	}
	if n := a.accepted.Load(); n != sent {
		t.Fatal("巡检本身不发网络请求")
	}
	a.state.Store(nodeUp)
	waitTested(t, d, "G")
	if d.lineWatchTick() {
		t.Fatal("又测通了:不该敲")
	}
	_ = d.core.Select("proxy", "A")
	if d.lineWatchTick() {
		t.Fatal("不在线路组上(手动节点):不管")
	}
}

// 11-12 核验发现的盲区:经在用那台的一次拨号偶然失败(目标连不上、网络抖一下)删掉了它的测速记录,巡检敲了一次健康检查,
// 健康检查经 proxy 测通了却不写记录 —— 这台一直"没有记录"、巡检认为已经敲过,之后它真挂了巡检也不再敲,
// 只能等三分钟一次的定时健康检查。现在健康检查经线路组测通时把记录补上(core.Remember),巡检就重新待命。
func TestLineWatchRearmsAfterHealthyCheck(t *testing.T) {
	a, b := newFakeSocksNode(t, 0), newFakeSocksNode(t, 100*time.Millisecond)
	d := lineDaemon(t)
	lineFailoverCore(t, d, a, b)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = d.core.URLTest(ctx, "proxy", "http://127.0.0.1:1/") // A 好好的,只是这次的目标连不上:记录被删
	cancel()
	if !d.lineWatchTick() {
		t.Fatal("前提:记录被删,应敲一次")
	}
	if err := d.health(context.Background()); err != nil {
		t.Fatalf("A 是好的,健康检查应通过: %v", err)
	}
	if _, untested, _ := d.core.Untested("G"); untested {
		t.Fatal("健康检查经线路组测通了,应把在用那台的记录补上")
	}
	if d.lineWatchTick() {
		t.Fatal("记录回来了:不该再敲")
	}
	a.state.Store(nodeDrop) // 现在 A 真挂了
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = d.core.URLTest(ctx, "proxy", "http://www.gstatic.com/generate_204")
	cancel()
	if !d.lineWatchTick() {
		t.Fatal("盲区:A 真挂了,巡检却不再敲")
	}
}

// 健康检查那一下偶发失败,而在用的 A 这一轮其实测得通、只是组里 B 快出 tolerance 以上:sing-box 测完这一轮自己会把组换到 B,
// 但 A 上的老连接是好的,不能为此把所有连接都掐掉(以前会走 recoverSwitch → CloseAllConnections,还当成"换线成功")。
func TestLineFailoverNoKillWhenCurrentStillWorks(t *testing.T) {
	a, b := newFakeSocksNode(t, 0), newFakeSocksNode(t, 100*time.Millisecond)
	d := lineDaemon(t)
	lineFailoverCore(t, d, a, b)
	a.state.Store(nodeSlow) // A 还通,只是现在比 B 慢 300 毫秒
	if _, ok := d.lineFailover(context.Background()); ok {
		t.Fatal("在用的 A 这一轮测得通:不算换线、不掐连接")
	}
	if now := d.groupNow("G"); now != "B" {
		t.Logf("组这一轮落在 %q(sing-box 按 tolerance 自己决定换不换,这里只关心没有掐连接)", now)
	}
}
