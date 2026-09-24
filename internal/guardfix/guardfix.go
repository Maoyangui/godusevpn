// Package guardfix 「恢复网络」:把「全局禁直连」的闸撤掉、把停用的网卡 IPv6 还原,让机器回到没装过的样子。
//
// 托盘菜单、开始菜单快捷方式、godusevpn-svc / CLI 的 guard clear 都走这里,行为一致:
// 服务要是活着,先把「全局禁直连」和「连接时停用网卡 IPv6」两个开关都关掉 —— 不然它下一次重连
// (内核崩了退避重试、切节点)会发现"闸不见了"又装回来,用户刚恢复的网络几秒后再断;网卡 IPv6 那边
// 更快,守护进程每 30 秒巡检一次,看到地址又冒出来就再关回去。开关关掉之后,才轮到把规则和绑定清干净。
package guardfix

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/svc"
)

// Clear 撤闸。返回给用户看的一句话,以及网络是不是真的恢复了。
//
// 这是用户没网时的**最后一条自救路**:托盘菜单、开始菜单快捷方式、CLI 的 guard clear 都走这里。
// 所以它自己绝不能因为某一步没做成就整个放弃 —— m29 在"关不掉隐私开关"时直接 return、什么都不做,
// 等于把唯一的逃生口堵死:用户既没网,也没有任何办法把闸撤掉。
func Clear() (text string, ok bool) {
	// 闸没开着就不该去动用户的隐私开关 —— 关开关唯一的理由是"不关的话守护进程下一轮又把闸装回来",
	// 闸都没装,这个理由不成立。
	//
	// 这里的判断必须只看闸。出厂默认是**规则模式**(settings.go:77 Mode=ModeRule),规则模式不装闸
	// (refresh.go 的 GuardWanted 要求 Mode==Global),但「连接时停用网卡 IPv6」默认开着、连一次就会
	// 落下备份文件。所以"闸没开 + 有网卡备份"恰恰是默认装机连过一次之后的常态 ——
	// 要是把这两个条件用 && 连起来当早退条件,这一路就会落到 switchOff(),
	// 把 NoDirect 和 DisableNICIPv6 一起写成 false 并落盘,而用户只是想把 IPv6 还原回去。
	// m28 在这里是分开处理的,本版照它来。
	if n, err := netmode.GuardStatus(); err == nil && n == 0 {
		if !netmode.NICIPv6Off() {
			if lost := takeNICLoss(); lost != "" {
				return "闸没有开着,网卡 IPv6 的备份也已清掉;但" + lost + " 两个隐私开关都没动。", true
			}
			return "闸没有开着,网卡 IPv6 也没被改过 —— 网络本来就是通的,没有改动任何设置。", true
		}
		if err := netmode.RestoreNICIPv6(); err != nil {
			var inc *netmode.NICRestoreIncomplete
			if !errors.As(err, &inc) {
				return "闸本来就没开;网卡 IPv6 没能还原回去(" + err.Error() + ")。两个隐私开关都没动。", false
			}
		}
		if lost := takeNICLoss(); lost != "" {
			return "闸本来就没开;网卡 IPv6 能还原的都还原了,但" + lost + " 两个隐私开关都没动。", true
		}
		return "闸本来就没开;网卡上被停用的 IPv6 已还原。「全局禁直连」与「连接时停用网卡 IPv6」两个开关都没动。", true
	}

	switchedOff, switchErr := switchOff()
	// 不管开关关没关掉,都先让守护进程断开:「恢复网络」的意思就是回到没装过的样子。
	// 隧道还在跑就去还原系统 DNS(macOS)/ 回包路由(Linux),会出现"界面显示已连接、解析却明文出局域网"
	// 的状态;而开关关不掉时(控制口超时、服务正在崩溃重启)先断开还能少一次"服务重连又把闸装回来"的竞态。
	// 断不断得掉都继续往下走 —— 用户点这个菜单的时候多半已经没网了。
	disconnectQuietly()
	_ = switchErr
	clearErr := netmode.ClearGuard()
	restoreErr := netmode.RestoreNICIPv6() // 网卡 IPv6 和闸一样是持久的,恢复网络就该一并还原,不然用户以为好了、v6 还是没有
	// 还原做完了、但有几张网卡的原值丢了:不算失败,但必须说出来。要说的话从持久记录里取(takeNICLoss):
	// 服务在跑时,上面的 switchOff 会让守护进程先还原,这里再还原时备份已经没了、拿不到 Incomplete ——
	// 0.7.4 的弹窗就是这样照样说"网卡 IPv6 也还原了"。
	var inc *netmode.NICRestoreIncomplete
	if errors.As(restoreErr, &inc) {
		restoreErr = nil
	}
	nicNote := takeNICLoss()
	// macOS 的系统 DNS 被接管到隧道地址、Linux 的回包策略路由也是"持久"的:闸撤了、隧道没了,
	// DNS 还指着隧道就等于没网。恢复网络就是要回到没装过的样子,一并还原(Windows 上是空操作)。
	dnsErr := netmode.UnprotectChecked()
	n, _ := netmode.GuardStatus()

	// 三件事分开说。m29 不管哪件失败都统一报成"过滤器还剩 N 条,没删干净",于是只有网卡 IPv6
	// 没还原时,用户看到的是"过滤器还剩 0 条,没删干净"这种自相矛盾的话,还连累 guard clear 退出码。
	netOK := clearErr == nil && n == 0
	var bad []string
	if !netOK {
		why := "是不是没用管理员身份跑?"
		if clearErr != nil {
			why = clearErr.Error()
		}
		bad = append(bad, fmt.Sprintf("禁直连闸没撤干净,过滤器还剩 %d 条(%s)", n, why))
	}
	if restoreErr != nil {
		bad = append(bad, "网卡 IPv6 没能还原回去("+restoreErr.Error()+"),下次启动服务时会自动再试")
	}
	if dnsErr != nil {
		bad = append(bad, "系统 DNS / 路由没能还原("+dnsErr.Error()+"),下次启动服务时会自动再试")
	}
	if nicNote != "" {
		bad = append(bad, "网卡 IPv6 能还原的都还原了,但"+nicNote)
	}
	if switchErr != nil {
		bad = append(bad, "没能关掉「全局禁直连」/「连接时停用网卡 IPv6」两个开关("+switchErr.Error()+"),服务下一次重连可能又把闸装回来;真装回来就去设置 → 隐私里手动关掉")
	}
	note := ""
	if w := netmode.GuardWarning(); w != "" {
		note = "(" + w + ")"
	}
	if len(bad) > 0 {
		lead := "网络已恢复,但有没做完的:"
		if !netOK {
			lead = "没能完全恢复:"
		}
		text := lead + strings.Join(bad, ";")
		// 两个隐私开关被关掉这件事不能因为别处有没做完的就不说:用户会以为「全局禁直连」还护着他
		if switchedOff {
			text += "。另外已关掉「全局禁直连」与「连接时停用网卡 IPv6」两个开关,免得服务一重连又装回来;要再用,去设置 → 隐私打开"
		}
		return text + note, netOK && restoreErr == nil && dnsErr == nil
	}
	if switchedOff {
		return "禁直连闸已解除,网卡 IPv6 也还原了,网络恢复。已顺手关掉「全局禁直连」与「连接时停用网卡 IPv6」两个开关,免得服务一重连又装回来;要再用,去设置 → 隐私打开。" + note, true
	}
	return "禁直连闸已解除,直连恢复。要再开闸,启动服务并连接即可。" + note, true
}

// takeNICLoss 取出"网卡原值丢失"的持久记录并删掉 —— 「恢复网络」的弹窗就是把它说给用户的地方。
func takeNICLoss() string {
	lost := netmode.NICLossNote()
	if lost != "" {
		_ = netmode.ClearNICLoss()
	}
	return lost
}

// disconnectQuietly 尽力让守护进程先断开,好让它不再按旧设置重新上闸。连不上控制口就算了 ——
// 走到这一步本来就是因为控制口有问题,失败不影响下面真正的撤闸动作。
func disconnectQuietly() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ipc.Call(ctx, ipc.MDisconnect, nil, nil)
}

// switchOff 服务活着就把两个开关都关掉;服务不在、或者两个本来就都关着,返回 false。
func switchOff() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var s settings.Settings
	if err := ipc.Call(ctx, ipc.MGetSettings, nil, &s); err != nil {
		// 服务已明确停止时，恢复网络不能依赖服务控制口；把“用户已明确
		// 放宽保护”的意愿安全落盘，再由本进程清理持久系统状态。服务仍在
		// 跑但控制口暂时不可用则拒绝离线改写，避免它下一轮按旧设置重新上闸。
		// Only a definitively stopped or uninstalled service may be edited
		// offline.  Starting/stopping/unknown are treated as live states: the
		// service may still reconcile the old settings and race the cleanup.
		// 控制口不在(ErrNoService)就说明此刻没有守护进程在应答。m29 还额外要求服务状态必须是
		// stopped / not-installed,而崩溃重启窗口里的 START_PENDING、Windows 那种"已报 Running 但
		// 控制口还没起来"、launchctl 有 pid 但进程卡死,统统被判成"活着"→ 拒绝离线改写 →
		// 「恢复网络」整个失败。而这恰恰是最需要它管用的时刻。
		//
		// 离线改写改的是设置**文件**:守护进程下次起来读的就是新值,所以"正在启动"反而是最该改的状态。
		// 真正剩下的风险只有"进程还活着、内存里揣着旧设置、之后又按旧设置上闸",这一条由上面的
		// disconnectQuietly 和返回文案里的提醒兜住,不值得拿整条逃生路去换。
		if !errors.Is(err, ipc.ErrNoService) {
			return false, fmt.Errorf("读取服务隐私设置失败(服务状态: %s): %w", svc.QueryStatus(), err)
		}
		if err := paths.Ensure(); err != nil {
			return false, fmt.Errorf("准备离线设置目录失败: %w", err)
		}
		var offline settings.Settings
		offline, err = settings.Load(paths.Settings())
		if err != nil {
			// 设置文件坏了就没法安全地"只改两项"(写回去会把别的都抹掉)。但这不该拦住撤闸:
			// Clear 现在会带着这个错误继续往下走,先把网还给用户。
			return false, fmt.Errorf("读取离线隐私设置失败: %w", err)
		}
		if !offline.NoDirect && !offline.DisableNICIPv6 {
			return false, nil
		}
		offline.NoDirect = false
		offline.DisableNICIPv6 = false
		if err := offline.Save(paths.Settings()); err != nil {
			return false, fmt.Errorf("保存离线隐私设置失败: %w", err)
		}
		return true, nil
	}
	if !s.NoDirect && !s.DisableNICIPv6 {
		return false, nil
	}
	s.NoDirect = false
	s.DisableNICIPv6 = false // 不关掉的话守护进程下一轮巡检又把网卡 IPv6 关回去
	if err := ipc.Call(ctx, ipc.MSetSettings, s, nil); err != nil {
		return false, fmt.Errorf("关闭服务隐私设置失败: %w", err)
	}
	return true, nil
}

// Status 闸开着没有:返回规则条数。普通用户身份看不了 Windows 的过滤器,把那句难看的系统错误换成人话。
func Status() (int, error) {
	n, err := netmode.GuardStatus()
	if err != nil && (errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "Access is denied")) {
		return 0, errors.New("看闸的状态要管理员 / root 身份(Windows 右键「以管理员身份运行」,macOS / Linux 加 sudo)")
	}
	return n, err
}
