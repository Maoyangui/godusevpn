package daemon

import (
	"fmt"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// ---- 刷新订阅之后动不动隧道 ----

// refreshOutcome 刷新拿到新列表之后,对正在跑的隧道做什么。
type refreshOutcome int

const (
	refreshNoop      refreshOutcome = iota // 没变化,或者内核没在跑:什么都不用做
	refreshDeferred                        // 变了,但当前节点还在、参数没变:不动隧道,新列表等下次连接生效
	refreshReconnect                       // 当前节点没了 / 参数变了:隧道注定要断,自动重连
)

// decideRefresh running 是正在跑的内核按哪份订阅生成的,fresh 是刚刷新到的,current 是此刻用着的节点。
// 刷新订阅不应该掐断正在用的连接 —— 用户正在用的节点一个字没改,没理由让他断一次;只有那个节点
// 本身被面板删了或改了参数,才值得重连。
func decideRefresh(running, fresh *profile.Profile, current string) (refreshOutcome, string) {
	if running == nil {
		return refreshNoop, "内核没在跑"
	}
	change := profile.Diff(running, fresh)
	if change.Empty() {
		return refreshNoop, fmt.Sprintf("%d 个节点,无变化", len(fresh.Outbounds))
	}
	summary := fmt.Sprintf("有变化(%s)", changeText(change))
	if current == "" {
		return refreshDeferred, summary + ",查不到当前节点,按保持处理"
	}
	old, hadOld := running.Node(current)
	nw, hasNew := fresh.Node(current)
	switch {
	case !hasNew:
		return refreshReconnect, fmt.Sprintf("%s,当前节点「%s」已被面板移除", summary, current)
	case hadOld && old != nw:
		return refreshReconnect, fmt.Sprintf("%s,当前节点「%s」参数已变", summary, current)
	}
	return refreshDeferred, fmt.Sprintf("%s,当前节点「%s」未受影响", summary, current)
}

func changeText(c profile.Change) string {
	var parts []string
	if c.Added > 0 {
		parts = append(parts, fmt.Sprintf("+%d", c.Added))
	}
	if c.Removed > 0 {
		parts = append(parts, fmt.Sprintf("−%d", c.Removed))
	}
	if c.Changed > 0 {
		parts = append(parts, fmt.Sprintf("改 %d", c.Changed))
	}
	return strings.Join(parts, " ")
}

// afterRefresh 刷新成功之后调:只关心当前订阅、且内核在跑的情况。别的订阅刷新只是更新缓存。
func (d *Daemon) afterRefresh(id string) {
	if id != d.getSettings().ActiveProfile || !d.core.Running() {
		return
	}
	d.mu.Lock()
	running, fresh := d.running, d.profiles[id]
	d.mu.Unlock()
	out, why := decideRefresh(running, fresh, d.currentNode())
	switch out {
	case refreshNoop:
		d.logf("订阅刷新:%s", why)
	case refreshDeferred:
		d.logf("订阅刷新:%s;当前连接保持,新列表重连后生效", why)
	case refreshReconnect:
		d.logf("订阅刷新:%s;重新连接", why)
		if err := d.machine.Restart(); err != nil {
			d.logf("重新连接失败,保持当前连接: %v", err)
		}
	}
}

// pendingChange 刷新拿到、但正在跑的内核还没用上的节点变化;没有就是 nil。
func (d *Daemon) pendingChange() *profile.Change {
	if !d.core.Running() {
		return nil
	}
	d.mu.Lock()
	running, fresh := d.running, d.profiles[d.settings.ActiveProfile]
	d.mu.Unlock()
	if running == nil || fresh == nil {
		return nil
	}
	c := profile.Diff(running, fresh)
	if c.Empty() {
		return nil
	}
	return &c
}

// ---- 「全局禁直连」的闸 ----

// GuardWanted 闸此刻该不该开着:开关开、模式是全局、用户想连着(没点断开)。
// 内核停了、崩了、在重启,只要这三条还成立,闸就该在。Android 的 VPN 接口按它决定要不要留着。
func (d *Daemon) GuardWanted() bool {
	s := d.getSettings()
	return s.NoDirect && s.Mode == settings.ModeGlobal && d.machine.Wanted()
}

func (d *Daemon) guardSpec() netmode.GuardSpec {
	s := d.getSettings()
	return netmode.GuardSpec{
		TunName:  builder.TunName,
		TunAddr4: strings.Split(builder.TunAddr4, "/")[0],
		TunAddr6: strings.Split(builder.TunAddr6, "/")[0],
		LAN:      s.LANBypass, // 跟随「局域网直通」:关了它,内核不在的那几秒连局域网也不放
		Gateway:  s.NetMode == settings.NetGateway,
	}
}

// syncGuard 把闸的状态和"该不该开"对齐。连接意愿、模式、设置变了都要调一次;幂等。
func (d *Daemon) syncGuard() {
	d.guardMu.Lock()
	defer d.guardMu.Unlock()
	want := d.GuardWanted()
	d.mu.Lock()
	on := d.guardOn
	d.mu.Unlock()
	switch {
	case want && !on:
		spec := d.guardSpec()
		if err := netmode.ApplyGuard(spec); err != nil {
			d.setGuard(false, err.Error())
			d.logf("全局禁直连:开闸失败,这段时间直连不会被拦: %v", err)
			return
		}
		d.setGuard(true, "")
		d.logf("全局禁直连:已开闸,隧道以外的流量一律拦下")
		if d.core.Running() {
			d.guardTunUp()
		}
	case !want && on:
		netmode.ClearGuard()
		d.setGuard(false, "")
		d.logf("全局禁直连:已撤闸")
		if d.releaseTun != nil && !d.core.Running() {
			d.releaseTun() // Android:内核没在跑时留着的 VPN 接口是个黑洞,撤闸就得关掉
		}
	}
}

// guardTunUp 隧道网卡起来之后再放行它(Windows 要按网卡放行;别的平台按地址,无操作)。
func (d *Daemon) guardTunUp() {
	d.mu.Lock()
	on := d.guardOn
	d.mu.Unlock()
	if !on {
		return
	}
	if err := netmode.GuardTunUp(d.guardSpec()); err != nil {
		d.setGuard(true, "隧道网卡放行失败: "+err.Error())
		d.logf("全局禁直连:隧道网卡放行失败(经隧道的流量也会被拦): %v", err)
		return
	}
	d.setGuard(true, "")
}

func (d *Daemon) setGuard(on bool, errText string) {
	d.mu.Lock()
	d.guardOn, d.guardErr = on, errText
	d.mu.Unlock()
}
