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
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// Clear 撤闸。返回给用户看的一句话,以及是否真的撤干净了(没撤干净多半是没有管理员 / root 身份)。
func Clear() (text string, ok bool) {
	if n, err := netmode.GuardStatus(); err == nil && n == 0 {
		if netmode.NICIPv6Off() {
			netmode.RestoreNICIPv6()
			return "闸本来就没开;网卡上被停用的 IPv6 已还原。「全局禁直连」开关没动。", true
		}
		return "闸本来就没开,直连不受限;「全局禁直连」开关没动。", true
	}
	switchedOff := switchOff()
	clearErr := netmode.ClearGuard()
	netmode.RestoreNICIPv6() // 网卡 IPv6 和闸一样是持久的,恢复网络就该一并还原,不然用户以为好了、v6 还是没有
	n, _ := netmode.GuardStatus()
	if n != 0 || clearErr != nil {
		why := "是不是没用管理员身份跑?"
		if clearErr != nil {
			why = clearErr.Error()
		}
		return fmt.Sprintf("过滤器还剩 %d 条,没删干净(%s)", n, why), false
	}
	if switchedOff {
		return "禁直连闸已解除,网卡 IPv6 也还原了,网络恢复。已顺手关掉「全局禁直连」与「连接时停用网卡 IPv6」两个开关,免得服务一重连又装回来;要再用,去设置 → 隐私打开。", true
	}
	return "禁直连闸已解除,直连恢复。要再开闸,启动服务并连接即可。", true
}

// switchOff 服务活着就把两个开关都关掉;服务不在、或者两个本来就都关着,返回 false。
func switchOff() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var s settings.Settings
	if err := ipc.Call(ctx, ipc.MGetSettings, nil, &s); err != nil {
		return false
	}
	if !s.NoDirect && !s.DisableNICIPv6 {
		return false
	}
	s.NoDirect = false
	s.DisableNICIPv6 = false // 不关掉的话守护进程下一轮巡检又把网卡 IPv6 关回去
	return ipc.Call(ctx, ipc.MSetSettings, s, nil) == nil
}

// Status 闸开着没有:返回规则条数。普通用户身份看不了 Windows 的过滤器,把那句难看的系统错误换成人话。
func Status() (int, error) {
	n, err := netmode.GuardStatus()
	if err != nil && (errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "Access is denied")) {
		return 0, errors.New("看闸的状态要管理员 / root 身份(Windows 右键「以管理员身份运行」,macOS / Linux 加 sudo)")
	}
	return n, err
}
