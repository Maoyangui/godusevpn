package daemon

import "os"

// sysDiag 诊断包里的系统网络信息(macOS):路由表、网卡、DNS、pf 规则、系统版本。
// macOS 没有 ip / ss / nft,对应换成 netstat / ifconfig / scutil / pfctl。
func sysDiag(add func(name, content string)) {
	add("route.txt", cmdOut("netstat", "-rn"))
	add("ifconfig.txt", cmdOut("ifconfig", "-a"))
	add("dns.txt", cmdOut("scutil", "--dns"))
	add("listen.txt", cmdOut("netstat", "-anv", "-p", "tcp"))
	add("pf.txt", cmdOut("pfctl", "-sr"))
	add("os.txt", cmdOut("sw_vers"))
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		add("etc/resolv.conf.txt", string(b))
	}
}
