package daemon

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/ruleset"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 内核起不来时把原因归个类。界面按错误码显示一句人话,后面再接内核给的原话。
//
// 为什么值得单独一个文件:2026-09-18 用户的索尼电视上,内核因为规则集下不到起不来,
// 而这里的判断只认 tun / route 两个词,于是落进最笼统的那一类,界面上就一句
// 「内核启动失败」—— 既不知道是哪一步、也不知道能做什么。分类要么精确、要么就别装作知道。

var (
	// 规则集:sing-box 报的是 initialize rule-set[N]: …
	reRuleSet = regexp.MustCompile(`rule[_-]?set`)
	// 端口被占:各平台文案不同,挑几个都出现的词,再顺手把端口号抠出来
	rePortBusy = regexp.MustCompile(`address already in use|only one usage of each socket address|bind: |listen tcp|listen udp`)
	rePort     = regexp.MustCompile(`:(\d{2,5})\b`)
	// TUN:原来是 strings.Contains(low, "tun"),连 "fortune"、节点名里的 tun 都算,太松;
	// 这里要求 tun 是个独立的词,外加几个只可能是隧道网卡的写法
	reTun   = regexp.MustCompile(`wintun|utun\d*|\btun\b|tun-in|/dev/net/tun`)
	reRoute = regexp.MustCompile(`auto_route|auto-redirect|set route|add route|route conflict|configure routes`)
)

func (d *Daemon) classifyStart(err error) error {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case reRuleSet.MatchString(low):
		// 本地规则集读不出来。把内置的重铺一遍(坏文件会被覆盖回去),下一轮重试多半就好了;
		// 真正读不动的那份也已经被 ruleset.Find 记下来了,一并说清楚。
		if e := ruleset.Install(paths.RuleSets()); e != nil {
			d.logf("重铺内置规则集失败: %v", e)
		}
		for p, why := range ruleset.Broken() {
			d.logf("规则集 %s 读不出来: %s", p, why)
		}
		return state.Errf(state.CodeRuleSet, "已重置内置的那几份,正在重试: %v", err)
	case rePortBusy.MatchString(low):
		if m := rePort.FindStringSubmatch(msg); m != nil {
			return state.Errf(state.CodePortBusy, "%s 被别的程序占着,换个端口或退掉那个程序: %v", m[1], err)
		}
		return state.Errf(state.CodePortBusy, "要监听的端口被别的程序占着: %v", err)
	case reTun.MatchString(low):
		return state.Errf(state.CodeTunDriver, "需要管理员权限的服务在跑,也可能被安全软件拦住了: %v", err)
	case reRoute.MatchString(low):
		return state.Errf(state.CodeRouteConflict, "%v", err)
	default:
		return state.Errf(state.CodeCoreStart, "%v", err)
	}
}

// recover 线路连着但一直不通时的自救。只有两种情况救得动:用户手动挑了某个节点,而那个节点挂了(夜里被墙、机房掉线);
// 或者挑的是线路组,而组里正在用的那台挂了。自动选择模式下内核的 urltest 组自己会换,轮不到这里。
//
// 线路组先在组里自救:只测这一组自己的服务器(GroupTest 测完组会自己换到通的那台),有通的就留在这条线路上。
// 整组都不通、或者挑的是单个节点,才让 auto 组整测一轮,挑一个通的切过去(线路组的成员不算:刚测过都不通)——
// 切节点是就地生效的,不重启内核、不断隧道。
// **不改设置里的 Selected**:用户挑的那个节点 / 线路是他的意愿,下次重建连接(重连、重启服务)时仍按它来;
// 这里只是临时借一条路。借过去之后不会自己切回。
//
// 测速要等(组里正在测的那一轮测完、再测一轮,最长几十秒),这期间用户可能已经改了选择:每次切 proxy 之前核对选择代数
// (selGen),变了就放弃这次切换 —— 用户的新选择已经就地生效,给它一轮观察期。不拿 settingsOpMu:选节点持着它 restart,
// restart 要等的正是跑这里的状态机 goroutine,拿了就死锁。
// 返回 true 表示确实换了(或者用户换了、组正在自己换),值得再给一轮观察期。
func (d *Daemon) recoverRoute(ctx context.Context) bool {
	gen := d.selGen.Load() // 先取代数再读设置:读到的设置一定不比这个代数旧
	s := d.getSettings()
	if s.Selected == "" {
		return false // 本来就是自动选择:内核自己会换,这里帮不上忙
	}
	if !d.core.Running() {
		return false
	}
	skip := []string{s.Selected}
	if members, ok := d.core.Members(s.Selected); ok {
		// 选的是线路组(内核里有这一组)
		d.logf("线路组「%s」一直不通,先在这一组的服务器里换一台", s.Selected)
		switch d.recoverInGroup(ctx, s.Selected, gen) {
		case groupRecovered, groupYielded:
			return true
		case groupUnknown:
			if ctx.Err() != nil {
				return false // 连接正在拆(断开、重连):不用救了
			}
			// 不知道是"还在测"还是"整组不通",就不能跨线路借道:这一轮测完,组里有通的会自己换过去
			d.logf("线路组「%s」等不到测速结果,先不借别的线路,再观察一轮", s.Selected)
			return true
		}
		skip = append(skip, members...)
		d.logf("线路组「%s」整组不通,临时换一条测得通的线路", s.Selected)
	} else {
		d.logf("手动选定的节点「%s」一直不通,临时换一条测得通的线路", s.Selected)
	}
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	delays, err := d.settledTest(tctx, "auto")
	if err != nil {
		d.logf("临时换线:测速没有结果 %v", err)
		return false
	}
	best, bestMs := "", 0
	for tag, ms := range delays {
		if ms == 0 || slices.Contains(skip, tag) {
			continue
		}
		if best == "" || int(ms) < bestMs {
			best, bestMs = tag, int(ms)
		}
	}
	if best == "" {
		d.logf("临时换线:一个通的节点都没有")
		return false
	}
	yielded, err := d.recoverSwitch(ctx, gen, best)
	switch {
	case yielded:
		d.logf("临时换线:测速期间选择已经改了,不再切到「%s」", best)
		return true
	case err != nil:
		d.logf("临时换线:切到「%s」失败 %v", best, err)
		return false
	}
	d.setPing(bestMs)
	d.clearExit()
	d.logf("临时换线:已切到「%s」(%d ms);设置里选的仍是「%s」,重新连接或手动再选它时才会用回它", best, bestMs, s.Selected)
	return true
}

// groupRecovery 组内自救的结果。
type groupRecovery int

const (
	groupDown      groupRecovery = iota // 真测完了一轮,整组不通
	groupRecovered                      // 组里有通的,已经换过去
	groupYielded                        // 测的这段时间里用户改了选择:没切
	groupUnknown                        // 等不到真测出来的结果(组一直在测、超时、连接在拆):不能当整组不通
)

// recoverInGroup 线路组在组里自救:只测这一组的成员,有通的就由组自己换过去(proxy 先前被临时换线借走了的,切回这一组)。
func (d *Daemon) recoverInGroup(ctx context.Context, group string, gen uint64) groupRecovery {
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	delays, err := d.settledTest(tctx, group)
	if err != nil {
		d.logf("线路组「%s」组内测速没有结果: %v", group, err)
		if errors.Is(err, errStillTesting) || tctx.Err() != nil {
			return groupUnknown
		}
		return groupDown
	}
	if len(delays) == 0 { // 结果里只有测通的那几台
		return groupDown
	}
	// 原来那台已经不通:切回这一组(要是被借走了)并掐掉老连接,应用重连就走组里新换上的那台
	yielded, err := d.recoverSwitch(ctx, gen, group)
	switch {
	case yielded:
		d.logf("线路组「%s」组内有通的,但测速期间选择已经改了,不再切回这一组", group)
		return groupYielded
	case err != nil && ctx.Err() != nil:
		return groupUnknown
	case err != nil:
		d.logf("线路组「%s」组内有通的,但切回这一组失败: %v", group, err)
		return groupDown
	}
	now := d.groupNow(group)
	d.setPing(int(delays[now]))
	d.clearExit()
	d.logf("线路组「%s」换到「%s」(%d ms)", group, now, delays[now])
	return groupRecovered
}

// recoverSwitch 自救把 proxy 切到 tag(已经在它上面就不切),再掐掉老连接。先核对:连接在拆(ctx 结束)就不动;
// 测速这段时间里用户改了选择(代数变了)就放弃,返回 yielded。和就地换选择(applySelected)用同一把锁:
// 核对完、切过去之前,用户的切换插不进来;用户的切换在这之后到,就由它盖过来。
func (d *Daemon) recoverSwitch(ctx context.Context, gen uint64, tag string) (yielded bool, err error) {
	d.selMu.Lock()
	defer d.selMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if d.selGen.Load() != gen {
		return true, nil
	}
	if now, _, err := d.core.Group("proxy"); err != nil || now != tag {
		if err := d.core.Select("proxy", tag); err != nil {
			return false, err
		}
	}
	_ = d.core.CloseAllConnections()
	return false, nil
}

// settledTest 测一轮测速组,拿真测出来的结果(见 settledGroupTest):组里正在测就先等这一轮测完,最多等一轮测速的上限。
func (d *Daemon) settledTest(ctx context.Context, tag string) (map[string]uint16, error) {
	n := 1
	if m, ok := d.core.Members(tag); ok {
		n = len(m)
	}
	return settledGroupTest(ctx,
		func(c context.Context) (map[string]uint16, error) { return d.core.GroupTest(c, tag) },
		func() (bool, bool) { return d.core.GroupBusy(tag) },
		250*time.Millisecond, core.RoundLimit(n))
}

// errStillTesting 等满一轮测速的上限,组还在测:结果未知 —— 不能当"整组不通"。
var errStillTesting = errors.New("测速组一直在测,等不到这一轮的结果")

// quickEmpty 说不清组在不在测时(core.GroupBusy 读不到标记),多快回来的空结果算"撞上了正在测的一轮":
// sing-box 碰上在测的一轮是立刻返回,真测一轮至少要拨一次号。
const quickEmpty = 200 * time.Millisecond

// settledGroupTest 测一轮测速组,返回真测出来的结果。sing-box 碰上组里正在测的一轮(定时测速、点线路之后的现测、
// 网络变化时内核自己的全测)会立刻回一个空结果 —— 和"真测完、全不通"一模一样,凭它跨线路换线就错了。所以:
//   - 先问组是不是在测(busy),在测就等这一轮测完:隔 poll 看一次,最多等 maxWait(一轮测速的上限,见 core.RoundLimit),
//     ctx 结束就停;等满了还在测,返回 errStillTesting(结果未知)。
//   - 然后测一轮自己的。回了空结果、而组此刻没在测:真测完了,整组不通 —— 当场认定,不再重测(重测就是把整组的服务器
//     再挨个带着凭据连一遍)。回空结果时组又在测(撞上了刚开始的一轮):等它测完再来。
//   - busy 说不清时(known 为假)退回保守的做法:很快回来的空结果当作撞上了在测的一轮,隔 poll 再试,直到 maxWait;
//     测了一会儿才回来的空结果算真测过。
//
// 极端情况下"问过不在测"和"开测"之间恰好有一轮瞬间测完(成员全有新鲜的历史、什么都不用测的那种,只在内核启动时出现),
// 会把那一下的空结果当真;自救只在连上好几分钟之后才会走到这里,碰不上。
func settledGroupTest(ctx context.Context, test func(context.Context) (map[string]uint16, error), busy func() (bool, bool), poll, maxWait time.Duration) (map[string]uint16, error) {
	deadline := time.Now().Add(maxWait)
	pause := func() error {
		t := time.NewTimer(poll)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	for {
		for { // 先等正在测的那一轮测完
			b, known := busy()
			if !known || !b {
				break
			}
			if !time.Now().Before(deadline) {
				return nil, errStillTesting
			}
			if err := pause(); err != nil {
				return nil, err
			}
		}
		start := time.Now()
		res, err := test(ctx)
		if err != nil || len(res) > 0 {
			return res, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err // 这一轮被截断了:没测完的不算"不通"
		}
		b, known := busy()
		switch {
		case known && !b:
			return res, nil // 真测完了一轮,此刻也没人在测:整组不通
		case !known && time.Since(start) >= quickEmpty:
			return res, nil // 说不清在不在测:测了一会儿才回来的空结果算真测过
		}
		if !time.Now().Before(deadline) {
			if known {
				return nil, errStillTesting
			}
			return res, nil // 说不清在不在测,等满一轮的上限还是很快回空结果:按不通算
		}
		if err := pause(); err != nil {
			return nil, err
		}
	}
}
