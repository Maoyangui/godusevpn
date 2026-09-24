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
		d.logf("订阅刷新:%s;已更新到节点列表,当前连接不受影响", why)
	case refreshReconnect:
		d.logf("订阅刷新:%s;重新连接", why)
		if err := d.restart(); err != nil {
			d.logf("重新连接失败,保持当前连接: %v", err)
		}
	}
}

// inCore 内核的 proxy 组里有没有这个节点。列表来自订阅缓存,刚刷新加进来的节点内核里还没有。
func (d *Daemon) inCore(tag string) bool {
	_, all, err := d.core.Group("proxy")
	if err != nil {
		return false
	}
	for _, n := range all {
		if n == tag {
			return true
		}
	}
	return false
}

// splitByCore 把订阅按"内核里有 / 没有"分成两份(下标对齐的子集)。
func (d *Daemon) splitByCore(p *profile.Profile) (in, out *profile.Profile) {
	_, all, err := d.core.Group("proxy")
	have := map[string]bool{}
	if err == nil {
		for _, n := range all {
			have[n] = true
		}
	}
	var ins, outs []string
	for _, t := range p.Tags {
		if have[t] {
			ins = append(ins, t)
		} else {
			outs = append(outs, t)
		}
	}
	return p.Subset(ins), p.Subset(outs)
}

// needRebuildFor 选中的节点能不能就地换。列表来自订阅缓存、内核用的是它启动时那份:
// 节点是刷新后新加的(内核里没有)、或者它的参数跟内核那份不一样,就得重建配置才连得上;
// 空 tag 是「自动选择」,组本身一直在。
func (d *Daemon) needRebuildFor(tag string) bool {
	if tag == "" || !d.core.Running() {
		return false
	}
	_, all, err := d.core.Group("proxy")
	if err != nil {
		return false
	}
	found := false
	for _, n := range all {
		if n == tag {
			found = true
			break
		}
	}
	if !found {
		return true
	}
	d.mu.Lock()
	running, fresh := d.running, d.profiles[d.settings.ActiveProfile]
	d.mu.Unlock()
	if running == nil || fresh == nil {
		return false
	}
	old, hadOld := running.Node(tag)
	nw, hasNew := fresh.Node(tag)
	return hadOld && hasNew && old != nw
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
	if !d.settingsTrusted() {
		// 设置读不出来时手上是默认值(模式=规则),据此判出来的"不该有闸"是猜的,不是用户的意思。
		// 保持现状:该有的闸留着,不该有的也不去装。用户保存一次设置就恢复正常。
		return
	}
	d.guardMu.Lock()
	defer d.guardMu.Unlock()
	want := d.GuardWanted()
	d.mu.Lock()
	on := d.guardOn
	hold := d.guardHold
	d.mu.Unlock()
	if hold && on {
		// 隐私事务进行中:旧数据面还在用,闸必须留着 —— 但"留着"不等于"不能换规格"。
		// 同一次保存里顺手改了「局域网直通」或网关模式,规格就变了;不在这里原地重装(事务内替换,
		// 没有空窗),prepare 里 ensurePrivacyReady 的"规格未同步"检查必然失败,整份设置被回滚,
		// 用户什么都改不了。m29 就是这样。
		if d.appliedGuardSpec() != d.guardSpec() {
			d.applyGuard("隐私事务进行中,闸规格变了,已原地重装")
		}
		return
	}
	switch {
	case want && !on:
		d.applyGuard("已开闸,隧道以外的流量一律拦下")
	case want && on:
		n, err := netmode.GuardStatus()
		if err != nil {
			break // 查不到就别乱动:重装一次的代价比"以为没装"高
		}
		ready := true
		if netmode.GuardPersistentSupported() { // 没有持久闸的平台不存在"装了一半",别每次同步都重装一遍
			var readyErr error
			ready, readyErr = netmode.GuardPersistentReady()
			if readyErr != nil {
				d.setGuard(true, "无法确认持久保护: "+readyErr.Error())
				return
			}
		}
		if msg := guardRedoReason(n, d.appliedGuardSpec(), d.guardSpec()); msg != "" || !ready {
			if msg == "" {
				msg = "持久/启动期保护不完整,已重新装上"
			}
			d.applyGuard(msg)
		} else if d.tunUpPending.Load() && d.core.Running() {
			d.guardTunUp() // 上次隧道网卡还没注册好,现在补放行
		}
	case !want && on:
		// 撤闸失败要如实记着"闸还在":以前这里不看返回值,失败了也记成"没开",于是界面说直连恢复了、
		// 其实过滤器一条没少,托盘的「恢复网络」看到"没开"直接返回 —— 用户在界面里怎么点都救不回来。
		if err := netmode.ClearGuard(); err != nil {
			d.setGuard(true, "撤闸失败: "+err.Error())
			d.logf("全局禁直连:撤闸失败,过滤器还在,直连仍被拦: %v", err)
			return
		}
		d.setGuard(false, "")
		d.logf("全局禁直连:已撤闸")
		if w := netmode.GuardWarning(); w != "" {
			d.logf("全局禁直连:%s", w)
		}
		if d.releaseTun != nil && !d.core.Running() {
			d.releaseTun() // Android:内核没在跑时留着的 VPN 接口是个黑洞,撤闸就得关掉
		}
	}
}

// guardArmed 闸此刻开着(按守护进程自己的记录)。
func (d *Daemon) guardArmed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.guardOn
}

// guardRedoReason 闸已经开着时,要不要重装、为什么。返回空串 = 不用动。
//
// 两种要重装的情况:
//   - 闸不见了(有人跑了恢复命令、或者被别的东西清掉了)。
//   - 规格变了。闸是**持久**的,用户改「局域网直通」或网关模式时它不会自己跟着变。
//     早先这里只判断"在不在",于是连着的时候改这两项完全不生效,必须断开再连。
//     2026-09-18 用户关掉局域网直通之后又打开,手机连不上电脑热点就是这个:
//     闸一直用着"不放行局域网"那份规格,把客户端的 DHCP 广播拦了,设备拿不到 IP。
//
// 重装本身是安全的:wfp.Enable 在一个事务里先删后建,中间没有不设防的窗口。
func guardRedoReason(installed int, applied, want netmode.GuardSpec) string {
	if installed == 0 {
		return "闸不见了,已重新装上"
	}
	if applied != want {
		return "设置变了,闸已按新规格重装"
	}
	return ""
}

// appliedGuardSpec 闸现在**实际**按哪份规格装着。空值表示还没装过 / 装失败了。
func (d *Daemon) appliedGuardSpec() netmode.GuardSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.guardApplied
}

// applyGuard 装闸并把结果记进状态与日志;持久闸或开机闸任一失败都保持未就绪，
// 由 prepare/start 的隐私前置检查拒绝启动数据面。
func (d *Daemon) applyGuard(okMsg string) {
	spec := d.guardSpec()
	d.mu.Lock()
	previousOn, previousSpec := d.guardOn, d.guardApplied
	d.mu.Unlock()
	if err := netmode.ApplyGuard(spec); err != nil {
		d.mu.Lock()
		// 重装失败时不能把仍可能存在的旧闸伪装成“已撤销”。保留旧状态，
		// 让数据面检查拒绝使用未确认的新规格，并继续重试。
		d.guardApplied = previousSpec
		d.mu.Unlock()
		d.setGuard(previousOn, err.Error())
		d.logf("全局禁直连:开闸失败,拒绝启动数据面: %v", err)
		return
	}
	d.mu.Lock()
	d.guardApplied = spec
	d.mu.Unlock()
	d.setGuard(true, "")
	d.logf("全局禁直连:%s", okMsg)
	if w := netmode.GuardWarning(); w != "" {
		d.logf("全局禁直连:%s", w)
	}
	if w := netmode.NICWarning(); w != "" {
		d.logf("网卡 IPv6:%s", w)
	}
	if w := netmode.RouteWarning(); w != "" {
		d.logf("回包路由:%s", w)
	}
	if d.core.Running() {
		d.guardTunUp()
	}
}

// reconcileGuard 服务启动时对账。闸是持久的(进程退出、重启都留着),这一刻该不该在要重新判:
// 上次是断开状态、或不是全局、或开关关了,就把残留的清掉;该在的立刻装上(幂等)——
// 别让"服务起来 → 连上"这几秒漏出去(重启的话开机那组过滤器已经挡到这里了)。
func (d *Daemon) reconcileGuard() {
	if !d.settingsTrusted() {
		d.logf("全局禁直连:设置读不出来,保持现状(不撤闸也不开闸)")
		return
	}
	s := d.getSettings()
	p := d.loadPersisted()
	if !d.persistedStateOK() {
		// 全新安装、或者刚清过数据的机器根本没有状态文件,这是最正常不过的事。
		// m29 在这里无条件 setGuard(true, "连接状态不可读…"),于是第一次打开客户端,
		// 首页就挂着一条"禁直连有异常 · 连接状态不可读"的橙色告警 —— 什么毛病都没有。
		// 只有闸确实还在、真有东西要保留的时候,才值得说这一句。
		if n, err := netmode.GuardStatus(); err == nil && n == 0 {
			return
		}
		if s.NoDirect && s.Mode == settings.ModeGlobal {
			// 设置说就该有闸、闸也确实在:按当前设置原地重装(事务内替换,不放宽任何保护)。
			// 不重装的话,从 0.6.25-m29 ~ 0.7.1 升上来的机器闸会一直挂在绑了服务名的第一代提供者下,
			// 直到用户手动点一次连接 —— 这段时间闸能不能跨过开机取决于服务的启动类型(见 wfp.baseProvider)。
			d.applyGuard("连接状态不可读,闸一直在,已按当前设置重装")
			return
		}
		d.setGuard(true, "连接状态不可读,保留全局禁直连保护")
		d.logf("全局禁直连:连接状态不可读,不撤闸")
		return
	}
	if !p.Wanted || !s.NoDirect || s.Mode != settings.ModeGlobal {
		if err := netmode.ClearGuard(); err != nil {
			d.setGuard(true, "撤闸失败: "+err.Error())
			d.logf("全局禁直连:上次残留的闸清不掉,直连仍被拦: %v", err)
		} else if w := netmode.GuardWarning(); w != "" {
			d.logf("全局禁直连:%s", w)
		}
		return
	}
	if n, err := netmode.GuardStatus(); err == nil && n > 0 {
		d.applyGuard("启动时核对:上次连着关的机,闸一直在,已按当前设置重装")
	} else {
		d.applyGuard("启动时已开闸(上次连着关的机)")
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
	if !d.getSettings().TUN {
		// 没开 TUN 就没有隧道网卡:转发层没有可放行的接口,经本机转发的流量只有「局域网直通」开着时才放行去局域网的,
		// 否则全拦 —— 这是对的,不是故障
		d.tunUpPending.Store(false)
		d.setGuard(true, "")
		return
	}
	if err := netmode.GuardTunUp(d.guardSpec()); err != nil {
		d.tunUpPending.Store(true) // 网卡可能晚几秒才注册好:下一次同步、或半分钟一次的巡检再试
		d.setGuard(true, "转发层没放行隧道网卡(只影响热点 / 网络共享,本机自己的流量不受影响): "+err.Error())
		d.logf("全局禁直连:转发层没放行隧道网卡(只影响热点 / 网络共享,本机自己的流量不受影响): %v", err)
		return
	}
	d.tunUpPending.Store(false)
	d.setGuard(true, "")
}

func (d *Daemon) setGuard(on bool, errText string) {
	d.mu.Lock()
	d.guardOn, d.guardErr = on, errText
	d.mu.Unlock()
}
