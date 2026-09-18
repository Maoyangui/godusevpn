package daemon

import (
	"context"
	"regexp"
	"strings"
	"time"

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

// recover 线路连着但一直不通时的自救。只有一种情况救得动:用户手动挑了某个节点,而那个节点挂了
// (夜里被墙、机房掉线)。自动选择模式下内核的 urltest 组自己会换,轮不到这里。
//
// 做法是让 auto 组整测一轮,挑一个通的切过去 —— 切节点是就地生效的,不重启内核、不断隧道。
// **不改设置里的 Selected**:用户挑的那个节点是他的意愿,等它活过来还要用;这里只是临时借一条路。
// 返回 true 表示确实换了,值得再给一轮观察期。
func (d *Daemon) recoverRoute(ctx context.Context) bool {
	s := d.getSettings()
	if s.Selected == "" {
		return false // 本来就是自动选择:内核自己会换,这里帮不上忙
	}
	if !d.core.Running() {
		return false
	}
	d.logf("手动选定的节点「%s」一直不通,临时换一条测得通的线路", s.Selected)
	tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	delays, err := d.core.GroupTest(tctx, "auto")
	if err != nil {
		d.logf("临时换线:测速失败 %v", err)
		return false
	}
	best, bestMs := "", 0
	for tag, ms := range delays {
		if ms == 0 || tag == s.Selected {
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
	if err := d.core.Select("proxy", best); err != nil {
		d.logf("临时换线:切到「%s」失败 %v", best, err)
		return false
	}
	_ = d.core.CloseAllConnections()
	d.setPing(bestMs)
	d.clearExit()
	d.logf("临时换线:已切到「%s」(%d ms);设置里选的仍是「%s」,它恢复后会自动用回去", best, bestMs, s.Selected)
	return true
}
