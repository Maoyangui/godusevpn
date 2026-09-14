// Package guardfix 「恢复网络」:把「全局禁直连」的闸撤掉,让机器能直连。
//
// 托盘菜单、开始菜单快捷方式、godusevpn-svc / CLI 的 guard clear 都走这里,行为一致:
// 服务要是活着,先把「全局禁直连」开关关掉 —— 不然它下一次重连(内核崩了退避重试、切节点)
// 会发现"闸不见了"又装回来,用户刚恢复的网络几秒后再断;然后不管服务在不在,都把规则删干净。
package guardfix

import (
	"context"
	"fmt"
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// Clear 撤闸。返回给用户看的一句话,以及是否真的撤干净了(没撤干净多半是没有管理员 / root 身份)。
func Clear() (text string, ok bool) {
	switchedOff := switchOff()
	netmode.ClearGuard()
	n, _ := netmode.GuardStatus()
	if n != 0 {
		return fmt.Sprintf("过滤器还剩 %d 条,没删干净(是不是没用管理员身份跑?)", n), false
	}
	if switchedOff {
		return "禁直连闸已解除,直连恢复。已顺手关掉「全局禁直连」开关,免得服务一重连又把闸装回来;要再用,去设置 → 隐私打开。", true
	}
	return "禁直连闸已解除,直连恢复。要再开闸,启动服务并连接即可。", true
}

// switchOff 服务活着就把开关关掉;服务不在、或者开关本来就关着,返回 false。
func switchOff() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var s settings.Settings
	if err := ipc.Call(ctx, ipc.MGetSettings, nil, &s); err != nil || !s.NoDirect {
		return false
	}
	s.NoDirect = false
	return ipc.Call(ctx, ipc.MSetSettings, s, nil) == nil
}
