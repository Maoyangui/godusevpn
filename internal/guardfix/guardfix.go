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

// Clear 撤闸。返回给用户看的一句话,以及是否真的撤干净了(没撤干净多半是没有管理员 / root 身份)。
func Clear() (text string, ok bool) {
	switchedOff, switchErr := switchOff()
	// 守护进程仍然可能在运行,而且下一轮巡检会按设置重新装闸。
	// 关闭设置失败时不能先删闸,否则会在重连竞态中短暂放行直连。
	if switchErr != nil {
		return "无法先关闭全局禁直连/网卡 IPv6 设置,为保护隐私保留现有闸: " + switchErr.Error(), false
	}
	clearErr := netmode.ClearGuard()
	restoreErr := netmode.RestoreNICIPv6() // 网卡 IPv6 和闸一样是持久的,恢复网络就该一并还原,不然用户以为好了、v6 还是没有
	n, _ := netmode.GuardStatus()
	if n != 0 || clearErr != nil || restoreErr != nil {
		why := "是不是没用管理员身份跑?"
		if clearErr != nil {
			why = clearErr.Error()
		} else if restoreErr != nil {
			why = restoreErr.Error()
		}
		return fmt.Sprintf("过滤器还剩 %d 条,没删干净(%s)", n, why), false
	}
	note := ""
	if w := netmode.GuardWarning(); w != "" {
		note = "(" + w + ")"
	}
	if switchedOff {
		return "禁直连闸已解除,网卡 IPv6 也还原了,网络恢复。已顺手关掉「全局禁直连」与「连接时停用网卡 IPv6」两个开关,免得服务一重连又装回来;要再用,去设置 → 隐私打开。" + note, true
	}
	return "禁直连闸已解除,直连恢复。要再开闸,启动服务并连接即可。" + note, true
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
		status := svc.QueryStatus()
		if !errors.Is(err, ipc.ErrNoService) || (status != "stopped" && status != "not-installed") {
			return false, fmt.Errorf("读取服务隐私设置失败: %w", err)
		}
		if err := paths.Ensure(); err != nil {
			return false, fmt.Errorf("准备离线设置目录失败: %w", err)
		}
		var offline settings.Settings
		offline, err = settings.Load(paths.Settings())
		if err != nil {
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
