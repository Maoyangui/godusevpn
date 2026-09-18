package daemon

import (
	"testing"

	"github.com/Maoyangui/godusevpn/internal/netmode"
)

// 「全局禁直连」的闸是持久的:装上之后不会自己跟着设置变。
// 早先 syncGuard 只判断"闸还在不在",于是**连着的时候改「局域网直通」或网关模式完全不生效** ——
// 闸一直用着装上那一刻的规格,用户必须断开再连。
//
// 2026-09-18 用户就是这么被卡住的:先关掉局域网直通(闸随之不放行私网),手机连电脑热点拿不到 IP;
// 再把开关打回去,却什么也没发生 —— 闸还是旧规格,客户端的 DHCP 广播照拦。
func TestGuardRedoReason(t *testing.T) {
	base := netmode.GuardSpec{TunName: "godusevpn", TunAddr4: "172.19.0.1", TunAddr6: "fdfe:dcba:9876::1", LAN: true}
	lanOff := base
	lanOff.LAN = false
	gateway := base
	gateway.Gateway = true

	cases := []struct {
		name      string
		installed int
		applied   netmode.GuardSpec
		want      netmode.GuardSpec
		redo      bool
	}{
		{"规格没变,别瞎折腾", 50, base, base, false},
		{"闸不见了,补上", 0, base, base, true},
		{"关掉局域网直通", 50, base, lanOff, true},
		{"打开局域网直通(用户的那一次)", 50, lanOff, base, true},
		{"切到网关模式", 50, base, gateway, true},
		{"没记录装过什么(服务刚起来还没装)", 50, netmode.GuardSpec{}, base, true},
		{"闸不见了且规格也变了", 0, base, lanOff, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := guardRedoReason(c.installed, c.applied, c.want)
			if (got != "") != c.redo {
				t.Fatalf("重装判断错了:拿到 %q,应该 %v", got, c.redo)
			}
		})
	}
}
