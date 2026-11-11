package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// ---- 线路组里正在用的那台挂了:马上在组里换一台 ----
//
// 线路组(和自动选择一样)是 sing-box 的测速组,配置里关掉了它自己的定时测速(builder.urltestGroup),
// 而测速组**只在测完一轮之后**才换选择:经某台连接失败时它只把那台的测速记录删掉,选择不动。
// 早先能让它换的只有两条路:定时测速(默认 10 分钟一轮,而且没有流量不测 —— 断网时流量正好几乎为零),
// 或者健康检查一格一格走到自救(第一次不通 → 30 秒后第二次进「节点不稳」→ 第四次,约一分半,才组内测速)。
// 健康检查本身三分钟一次,于是 2026-11-11 的冒烟里 HY2 的 UDP 一封,整机断网三分多钟才换到组里另一台。
//
// 现在两处加快,都只对线路组,都不出这一组:
//   - 发现:lineWatchLoop 每 5 秒看一眼内存里"组落在的那台还有没有测速记录"(core.Untested,不发任何网络请求),
//     没了就是那台刚连不上,马上敲一次健康检查,不等三分钟的定时器。
//   - 换线:健康检查经 proxy 不通时(probeProxy),当场只测这一组(lineFailover),组里有通的就换过去并掐掉老连接,
//     复测通了这一次健康检查就算过,不进「节点不稳」。
//
// 防抖:只有健康检查真不通才测;一分钟内最多测一次(lineFailoverGap,整组都挂时状态机每 30 秒一查,不跟着每次都测);
// 换过去之后不会因为原来那台恢复就马上换回 —— 下一轮定时测速时它快出 50 毫秒(组的 tolerance)以上才换回。
// 整组都不通:这里什么都不做,照常报节点不可用,由状态机走原来的自救(那时才临时借别的线路)。
// 自动选择不走这里:一组可能上百台,全测一轮要几分钟,健康检查等不起;仍旧走状态机那条路(自救不了就重建连接)。

// lineFailoverGap 两次组内换线测速之间至少隔多久。
const lineFailoverGap = time.Minute

// lineWatchEvery 巡检线路组在用的那台还有没有测速记录的间隔(只读内存)。
const lineWatchEvery = 5 * time.Second

// lineGroupNow proxy 此刻落在的线路组;落在自动选择、单个节点上,或内核没跑,返回空。
func (d *Daemon) lineGroupNow() string {
	if g := d.probeTarget(); g != "auto" {
		return g
	}
	return ""
}

// probeProxy 经 proxy 测一次(健康检查用)。不通而 proxy 落在线路组上时,当场在组里换一台(lineFailover),换过去复测通了算通。
func (d *Daemon) probeProxy(ctx context.Context) (int, error) {
	ms, err := d.core.URLTest(ctx, "proxy", builder.TestURL)
	if err == nil {
		return ms, nil
	}
	if ms, ok := d.lineFailover(ctx); ok {
		return ms, nil
	}
	return 0, err
}

// lineFailover 线路组正在用的那台测不通:只测这一组,组里有通的就由组换过去,掐掉老连接(应用重连就走新的那台),
// 再经 proxy 复测一次。返回复测的毫秒数和是否换成了。
// 不换的情况:proxy 没落在线路组上;离上一次不到 lineFailoverGap;组里一台都不通;在用的那台这一轮其实测得通
// (健康检查那一下是偶发失败,不换、不掐连接);测速期间用户改了选择(按用户的来)。
func (d *Daemon) lineFailover(ctx context.Context) (int, bool) {
	group := d.lineGroupNow()
	if group == "" {
		return 0, false
	}
	d.probeMu.Lock()
	if !d.failoverAt.IsZero() && time.Since(d.failoverAt) < lineFailoverGap {
		d.probeMu.Unlock()
		return 0, false
	}
	d.failoverAt = time.Now()
	d.probeMu.Unlock()
	gen := d.selGen.Load() // 先取代数:测速期间用户改了选择就不切(见 recoverSwitch)
	was := d.groupNow(group)
	n := 1
	if m, ok := d.core.Members(group); ok {
		n = len(m)
	}
	// 组里正在测的一轮(定时测速)要先等它测完,再测自己这一轮:各一轮的上限
	tctx, cancel := context.WithTimeout(ctx, 2*core.RoundLimit(n))
	defer cancel()
	delays, err := d.settledTest(tctx, group)
	if err != nil {
		d.logf("线路组「%s」正在用的「%s」不通,组内测速没有结果: %v", group, was, err)
		return 0, false
	}
	if len(delays) == 0 {
		d.logf("线路组「%s」正在用的「%s」不通,组里别的服务器也都测不通", group, was)
		return 0, false
	}
	now := d.groupNow(group)
	if now == was || delays[now] == 0 {
		return 0, false // 在用的那台这一轮测得通(或者换上的那台没测出来):不换、不掐连接,交给状态机的节奏
	}
	yielded, err := d.recoverSwitch(ctx, gen, group)
	switch {
	case yielded:
		d.logf("线路组「%s」组内有通的,但测速期间选择已经改了,不再切", group)
		return 0, false
	case err != nil:
		d.logf("线路组「%s」组内换到「%s」后掐断旧连接失败: %v", group, now, err)
		return 0, false
	}
	d.clearExit()
	ms, err := d.core.URLTest(ctx, "proxy", builder.TestURL)
	if err != nil {
		d.logf("线路组「%s」正在用的「%s」不通,已在组里换到「%s」,复测仍不通: %v", group, was, now, err)
		return 0, false
	}
	d.logf("线路组「%s」正在用的「%s」不通,已在组里换到「%s」(%d ms)", group, was, now, ms)
	return ms, true
}

// lineWatchLoop 每 lineWatchEvery 看一次线路组在用的那台(见 lineWatchTick)。
func (d *Daemon) lineWatchLoop(ctx context.Context) {
	t := time.NewTicker(lineWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.lineWatchTick()
	}
}

// lineWatchTick proxy 落在线路组上、组落在的那台已经没有测速记录(经它的连接刚失败过,见 core.Untested)时,
// 马上敲一次健康检查(不通就会当场组内换线,见 probeProxy)。同一台挂着只敲一次,它又测通(记录回来)之后再挂再敲;
// 组正在测的时候不看(测完组自己会换,记录也会更新)。只读内存,不发网络请求。返回这一次有没有敲。
func (d *Daemon) lineWatchTick() bool {
	group := ""
	if d.core.Running() && !strings.EqualFold(d.core.Mode(), builder.ModeName(settings.ModeDirect)) {
		group = d.lineGroupNow()
	}
	if group == "" {
		d.setLostKicked("")
		return false
	}
	if busy, _ := d.core.GroupBusy(group); busy {
		return false
	}
	member, untested, ok := d.core.Untested(group)
	if !ok || !untested {
		d.setLostKicked("")
		return false
	}
	key := group + "\x00" + member
	d.probeMu.Lock()
	if d.lostKicked == key {
		d.probeMu.Unlock()
		return false
	}
	d.lostKicked = key
	d.probeMu.Unlock()
	d.logf("线路组「%s」正在用的「%s」连接失败,马上做一次健康检查", group, member)
	d.machine.CheckNow()
	return true
}

func (d *Daemon) setLostKicked(key string) {
	d.probeMu.Lock()
	d.lostKicked = key
	d.probeMu.Unlock()
}
