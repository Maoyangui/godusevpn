//go:build !windows

package daemon

import "os"

// sysDiag 诊断包里的系统网络信息(Linux):路由与策略路由、网卡、防火墙、DNS、系统版本。
func sysDiag(add func(name, content string)) {
	add("ip-route.txt", cmdOut("ip", "route", "show", "table", "all"))
	add("ip-rule.txt", cmdOut("ip", "rule", "show"))
	add("ip-addr.txt", cmdOut("ip", "addr"))
	add("nft.txt", cmdOut("nft", "list", "ruleset"))
	add("iptables.txt", cmdOut("iptables-save"))
	add("listen.txt", cmdOut("ss", "-lntup"))
	for _, f := range []string{"/etc/resolv.conf", "/etc/os-release", "/etc/openwrt_release"} {
		if b, err := os.ReadFile(f); err == nil {
			add("etc"+f[4:]+".txt", string(b))
		}
	}
}
