package daemon

import (
	"context"
	"slices"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// ---- 线路自动:同一条线路部署在多台服务器上时,选这条线路、由线路组在这几台里自动用最快的 ----
//
// 线路组来自订阅(profile.Group,m-ui 给多服务器的线路出 urltest 组)。设置里 Selected 可以是节点、线路组或空(自动选择);
// 只有选中的是线路组时,配置里才有线路组(见 builder),所以没用这个功能的连接一个字节都不变。
// 「线路自动」(settings.LineAuto)只是"按线路选"的偏好:列表按线路合并显示,翻转时顺带把当前选择换算一次。
// 不变式(MSelectNode 维护):选了线路组 → 开;选了属于某个线路组的节点 → 关;选自动选择、独立节点 → 不改。

// quietTags 这次连接启动时预先记成"刚测过"、启动那一轮不测的节点(见 core.QuietAuto)。
//   - 直连模式、手动选了节点:全部 —— 连接只碰用得到的节点。
//   - 自动选择:一个不留,auto 组照常把全部节点测一轮。
//   - 线路组:除了这一组的成员 —— 组里的服务器 auto 组和线路组各测一次,别的节点一个不碰;切到别的线路组时再现测那一组。
func quietTags(p *profile.Profile, sel string, direct bool) []string {
	if p == nil {
		return nil
	}
	if direct {
		return p.Tags
	}
	if sel == "" || sel == "auto" {
		return nil
	}
	g, ok := p.Group(sel)
	if !ok {
		return p.Tags
	}
	out := make([]string, 0, len(p.Tags))
	for _, t := range p.Tags {
		if !slices.Contains(g.Members, t) {
			out = append(out, t)
		}
	}
	return out
}

// lineAutoAfterSelect 选了 sel 之后「线路自动」该是什么:选的是线路组 → 开;选的是某个线路组里的节点 → 关(要的就是这一台);
// 自动选择、不在任何组里的节点 → 不变。
func lineAutoAfterSelect(p *profile.Profile, sel string, cur bool) bool {
	if sel == "" || p == nil {
		return cur
	}
	if _, ok := p.Group(sel); ok {
		return true
	}
	if p.GroupOf(sel) != "" {
		return false
	}
	return cur
}

// lineAutoSelection 翻「线路自动」时当前选择怎么换算:
//   - 打开:选的是某个线路组里的节点 → 换成那个线路组;自动选择、不属于任何组的节点不变(只换列表的显示)。
//   - 关闭:选的是线路组 → 固定到组里此刻在用的那台(now 给,内核在跑时才有);内核没跑、或那台已经不在组里,
//     取第一台这一轮连得上的(blocked 是自动选择不用的节点:提示节点、连不上的 IPv6 节点,见 autoSkippedNow;全是的话照样取第一台)。
//     选的线路组在订阅里已经没了(线路只剩一台):固定到这一轮正在用的同线路那一台(fallback = settings.SelectedNode,
//     选的是节点时为空;见 builder.Effective)。
//     选的线路组成员成了全部节点(订阅里只剩这一条线路,见 profile.Profile.Whole):这一轮落在自动选择上,固定到它此刻在用的那台。
func lineAutoSelection(p *profile.Profile, sel string, on bool, now func(group string) string, blocked []string, fallback string) string {
	if p == nil || sel == "" {
		return sel
	}
	if on {
		if g := p.GroupOf(sel); g != "" {
			return g
		}
		return sel
	}
	g, ok := p.Group(sel)
	nowOf := sel
	if !ok {
		if w, whole := p.WholeGroup(sel); whole {
			g, ok, nowOf = w, true, "auto" // 订阅里只剩这条线路:这一轮落在自动选择上,"此刻在用的那台"看它
		}
	}
	// 选的线路组没了:固定到这一轮在用的同线路那台。订阅里冒出个和组同名的普通节点(第三方)也一样,不固定到它上面(见 builder.Present);
	// 选的是节点时调用方给的 fallback 是空的
	if !ok && fallback != "" && fallback != sel && slices.Contains(p.Tags, fallback) {
		return fallback
	}
	if !ok || len(g.Members) == 0 {
		return sel
	}
	if now != nil {
		if n := now(nowOf); n != "" && slices.Contains(g.Members, n) {
			return n
		}
	}
	for _, m := range g.Members {
		if !slices.Contains(blocked, m) {
			return m
		}
	}
	return g.Members[0]
}

// groupNow 内核里这个测速组此刻选中的节点;内核没跑、或者没有这一组返回空。
func (d *Daemon) groupNow(tag string) string {
	now, _, err := d.core.Group(tag)
	if err != nil {
		return ""
	}
	return now
}

// applySelected 让 proxy 组用上设置里选的 sel(空 = 自动选择)。内核没跑:什么都不做,下次连接按设置来。
// 选的节点 / 线路组内核里还没有、或者参数跟内核那份不一样(needRebuildFor):重建配置重连 —— 一直没选过线路组的连接里
// 头一回选线路组走的就是这里,之后线路之间切换、切回自动选择或节点都是就地换。
// 就地换:切 proxy、掐掉老连接(选择组只对新连接生效)、清掉出口与延迟;后台先让新落上的测速组测一轮自己的成员
// (线路组的成员带着启动时预记的假历史,不测就落在随便哪一台上),再测当前延迟、查出口。
func (d *Daemon) applySelected(sel string) error {
	if !d.core.Running() {
		return nil
	}
	if d.needRebuildFor(sel) {
		if err := d.restart(); err != nil {
			return &ipc.CallError{Code: state.CodeOf(err), Msg: "切换失败,保持当前连接: " + err.Error()}
		}
		return nil
	}
	tag := sel
	if tag == "" {
		tag = "auto"
	}
	// 和自救切 proxy 那一下串行(见 recoverRoute):自救要么在这之前切完、被这里盖过去,要么核对代数时就放弃
	d.selMu.Lock()
	err := d.core.Select("proxy", tag)
	if err == nil {
		// 选择组只对新连接生效:不掐掉老连接,用户会看到"选了新节点,连接列表里还是老节点",
		// 长连接(Telegram、推送、anytls 的连接池)能挂十几分钟不断。掐掉后应用自己重连,就都走新节点了。
		if cerr := d.core.CloseAllConnections(); cerr != nil {
			d.logf("切节点后掐断旧连接失败(旧连接会继续用老节点): %v", cerr)
		}
	}
	d.selMu.Unlock()
	if err != nil {
		return err
	}
	// 换了节点,出口多半也变了:先抹掉旧值,再在后台重新测延迟与出口
	d.clearExit()
	d.setPing(0)
	go func() {
		if target := d.probeTarget(); target != "" {
			d.groupTest(context.Background(), target)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if ms, err := d.core.URLTest(ctx, "proxy", builder.TestURL); err == nil {
			d.setPing(ms)
		}
		d.refreshExit(d.beginExit(d.currentNode()))
	}()
	return nil
}

// ---- 关着 IPv6 时不用的 IPv6 节点 ----

// v6BlockedNow 当前订阅里这一轮不用的 IPv6 节点(见 builder.V6Blocked):关着 IPv6 时连它要从本机走 IPv6 的。
// 只有 IPv6 地址的节点域名用最近一次连接时解析出来的那份。
func (d *Daemon) v6BlockedNow(s settings.Settings) []string {
	_, p := d.activeProfile()
	d.mu.Lock()
	v6 := d.v6Only
	d.mu.Unlock()
	return builder.V6Blocked(p, s, v6)
}

// autoSkippedNow 当前订阅里自动选择不用的节点:提示节点(地址只指向本机,见 builder.HintNodes)在前,
// 这一轮连不上的 IPv6 节点在后 —— 和配置里自动选择、线路组剔掉的是同一批。
func (d *Daemon) autoSkippedNow(s settings.Settings) []string {
	_, p := d.activeProfile()
	return append(builder.HintNodes(p), d.v6BlockedNow(s)...)
}

// selectionBlocked 这一轮实际落到的选择(eff,见 builder.Effective)只能用关着 IPv6 时不用的节点:选的就是这种节点、
// 成员全是这种节点的线路组,或者自动选择(包括选的已经不在订阅里、回落到自动选择)而订阅里的节点全是这种 ——
// 自动选择那时只能落在它们上面(见 builder 的 reachable)。
func selectionBlocked(p *profile.Profile, eff string, blocked []string) bool {
	if p == nil || len(blocked) == 0 {
		return false
	}
	all := func(list []string) bool {
		for _, m := range list {
			if !slices.Contains(blocked, m) {
				return false
			}
		}
		return len(list) > 0
	}
	if eff == "" || eff == "auto" {
		return all(p.Tags)
	}
	if g, ok := p.Group(eff); ok {
		return all(g.Members)
	}
	return slices.Contains(blocked, eff)
}

// selectedV6 设置里选的(节点、线路组或自动选择)这一轮连不上,原因是它只能走 IPv6 而现在关着 IPv6。
func (d *Daemon) selectedV6(s settings.Settings) bool {
	_, p := d.activeProfile()
	return selectionBlocked(p, builder.Effective(p, s), d.v6BlockedNow(s))
}

// selNote 设置里选的节点 / 线路组在当前订阅里已经没有了:这一轮实际用的是什么(见 builder.Effective)。还在就返回 nil。
// 选的线路组成员成了全部节点(订阅里只剩这一条线路,见 profile.Profile.Whole)也写一句:这一轮用的是自动选择,
// 不然首页从线路名变成「自动选择」,看着像选择丢了。
func selNote(p *profile.Profile, s settings.Settings) *ipc.SelNote {
	if p == nil || s.Selected == "" || builder.Present(p, s) != "auto" {
		return nil
	}
	if _, whole := p.WholeGroup(s.Selected); whole {
		return &ipc.SelNote{Gone: s.Selected, Group: true, Whole: true}
	}
	n := &ipc.SelNote{Gone: s.Selected, Group: s.SelectedNode != ""}
	if eff := builder.Effective(p, s); eff != "auto" {
		n.Now = eff
	}
	return n
}

// reconcileLineGroup 刷新订阅之后:选中的线路组在新订阅里没了(m-ui 只给部署在多台服务器上的线路出组,线路减到一台组就没了),
// 就给它记一台同线路、还在新订阅里的服务器 —— 优先此刻正在用的那台 —— 这一轮连接就用它(见 builder.Effective),
// 线路、出口不变,不回落到全局自动选择(那会跨线路换出口、换倍率)。设置里选的仍是这条线路:面板恢复多台、组回来了就自动回到线路自动。
// 同线路一台都不剩了才回落自动选择;两种情况界面都写明(见 selNote)。old 是刷新前的那份(知道这条线路原来有哪几台)。
//
// 组成员成了全部节点(订阅里只剩这一条线路:别的线路停了、从这个用户名下拿掉了)的不算没了:解析订阅时不把它当线路组
// (它和自动选择是一回事,见 profile.Profile.Whole),这一轮用自动选择,也就是这条线路的这几台,什么都不用记。
func (d *Daemon) reconcileLineGroup(id string, old, fresh *profile.Profile) {
	s := d.getSettings()
	if id != s.ActiveProfile || old == nil || fresh == nil {
		return
	}
	og, ok := old.Group(s.Selected)
	wasWhole := false
	if !ok {
		og, ok = old.WholeGroup(s.Selected) // 上一份里就已经只剩这一条线路:成员就是那份的全部节点
		wasWhole = ok
	}
	if !ok || builder.Present(fresh, s) != "auto" {
		return // 选的不是线路组,或者这个线路组还在(冒出来的同名普通节点不算,见 builder.Present)
	}
	if _, whole := fresh.WholeGroup(s.Selected); whole {
		if !wasWhole {
			d.logf("订阅里只剩线路「%s」这一条了:这一轮用自动选择,就是在这条线路的服务器里挑;别的线路加回来后自动回到线路自动", s.Selected)
		}
		return
	}
	alive := func(m string) bool { return slices.Contains(og.Members, m) && slices.Contains(fresh.Tags, m) }
	pick := ""
	if now := d.currentNode(); alive(now) {
		pick = now
	} else if alive(s.SelectedNode) {
		pick = s.SelectedNode
	} else {
		for _, m := range og.Members {
			if alive(m) {
				pick = m
				break
			}
		}
	}
	if pick == "" {
		d.logf("线路组「%s」在订阅里没了,这条线路的服务器也一台都不剩:这一轮回落到自动选择", s.Selected)
		return
	}
	d.logf("线路组「%s」在订阅里没了(线路只剩一台服务器):改用同线路的「%s」,线路组回来后自动回到线路自动", s.Selected, pick)
	if pick == s.SelectedNode {
		return
	}
	if err := d.updateSettings(func(cur *settings.Settings) bool {
		if cur.Selected != s.Selected || cur.ActiveProfile != id {
			return false // 这期间用户改了选择:不动
		}
		cur.SelectedNode = pick
		return true
	}); err != nil {
		d.logf("线路组「%s」没了,记同线路那一台失败: %v", s.Selected, err)
	}
}
