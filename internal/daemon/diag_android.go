//go:build android

package daemon

import (
	"fmt"
	"net"
	"strings"
)

// sysDiag Android 上没有 ip / ss / nft 这些命令(以前诊断包里全是"命令找不到"),
// 直接用 Go 读网卡与地址,顺带记下有没有可用的 IPv6,排查"连上没网 / 有没有泄露"时最需要这些。
func sysDiag(add func(name, content string)) {
	var b strings.Builder
	ifaces, err := net.Interfaces()
	if err != nil {
		add("interfaces.txt", "读网卡失败: "+err.Error())
		return
	}
	var globalV6 int
	for _, ifi := range ifaces {
		fmt.Fprintf(&b, "%d %s mtu=%d flags=%s\n", ifi.Index, ifi.Name, ifi.MTU, ifi.Flags)
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			fmt.Fprintf(&b, "    %s\n", a)
			if ip, _, e := net.ParseCIDR(a.String()); e == nil && ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
				globalV6++
			}
		}
	}
	fmt.Fprintf(&b, "\n公网 IPv6 地址数量: %d(大于 0 说明这台设备有 IPv6,禁用 IPv6 时它会被接进隧道再拒绝)\n", globalV6)
	add("interfaces.txt", b.String())
}
