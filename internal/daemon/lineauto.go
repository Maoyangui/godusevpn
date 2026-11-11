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
//     取第一台这一轮连得上的(blocked 是连不上的 IPv6 节点,全是的话照样取第一台)。
func lineAutoSelection(p *profile.Profile, sel string, on bool, now func(group string) string, blocked []string) string {
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
	if !ok || len(g.Members) == 0 {
		return sel
	}
	if now != nil {
		if n := now(sel); n != "" && slices.Contains(g.Members, n) {
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

// ---- 关着 IPv6 时连不上的 IPv6 节点 ----

// v6BlockedNow 当前订阅里这一轮连不上的 IPv6 节点(见 builder.V6Blocked)。网卡 IPv6 停没停按设置判(wantNICOff),
// 只有 IPv6 地址的节点域名用最近一次连接时解析出来的那份。
func (d *Daemon) v6BlockedNow(s settings.Settings) []string {
	_, p := d.activeProfile()
	d.mu.Lock()
	v6 := d.v6Only
	d.mu.Unlock()
	return builder.V6Blocked(p, s, wantNICOff(s), v6)
}

// selectionBlocked 选中的就是这一轮连不上的 IPv6 节点,或者是成员全是这种节点的线路组。
func selectionBlocked(p *profile.Profile, sel string, blocked []string) bool {
	if p == nil || sel == "" || len(blocked) == 0 {
		return false
	}
	if g, ok := p.Group(sel); ok {
		for _, m := range g.Members {
			if !slices.Contains(blocked, m) {
				return false
			}
		}
		return len(g.Members) > 0
	}
	return slices.Contains(blocked, sel)
}

// selectedV6 设置里选的节点(或线路组)这一轮连不上,原因是它只能走 IPv6 而现在关着 IPv6。
func (d *Daemon) selectedV6(s settings.Settings) bool {
	_, p := d.activeProfile()
	return selectionBlocked(p, s.Selected, d.v6BlockedNow(s))
}
