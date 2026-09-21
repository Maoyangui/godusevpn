package netmode

import (
	"net"
	"strings"
)

// NICIPv6Leaking 有没有哪张网卡还挂着公网 IPv6 地址(隧道自己那张、回环、没启用的都不算)。
//
// 为什么需要它:停用网卡 IPv6 只在连接建立时做一次。连接期间新冒出来的网卡 —— 插 USB 网卡、
// 手机 USB 网络共享、开热点、起虚拟机或 WSL —— 不会被处理,那张网卡上的公网 v6 地址照样能被程序
// 枚举读走,而"地址不存在才是真的读不到"正是这个功能存在的理由(数据包由闸挡着,不会真的漏流量)。
//
// 用标准库枚举,不起子进程:守护进程会定期问一次,便宜才问得起;真发现了才去跑那段昂贵的停用脚本。
func NICIPv6Leaking(tunName string) bool {
	ifs, err := net.Interfaces()
	if err != nil {
		// 无法枚举网卡时不能把“未知”当成安全；调用方会保持 IPv6 保护并重试。
		return true
	}
	for _, in := range ifs {
		addrs, err := in.Addrs()
		if err != nil {
			return true
		}
		if ifaceLeaksIPv6(in.Name, in.Flags, addrs, tunName) {
			return true
		}
	}
	return false
}

// ifaceLeaksIPv6 单张网卡算不算"漏着 v6"。拆出来是为了能用构造的数据做单测 —— 真机上没法说造一张网卡就造一张。
func ifaceLeaksIPv6(name string, flags net.Flags, addrs []net.Addr, tunName string) bool {
	if flags&net.FlagUp == 0 || flags&net.FlagLoopback != 0 {
		return false
	}
	if strings.EqualFold(name, tunName) {
		return false // 隧道那张本来就要留着 v6:靠它把 v6 流量接进来再拒绝,关了反而少一层防护
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP
		if ip.To4() != nil {
			continue // v4 不在这条的管辖内
		}
		// 链路本地(fe80::)、回环、唯一本地(fc00::/7)都出不了网,也读不出有意义的身份,不算漏
		if !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		return true
	}
	return false
}
