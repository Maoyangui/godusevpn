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
// 调用方分两类,对"查不出来"的态度正相反,所以拆成两个名字:
//
//   - NICIPv6Leaking —— 要不要再跑一遍那段昂贵的停用脚本。查不出来按"可能在漏"算,宁可多跑一次。
//   - NICIPv6LeakConfirmed —— 要不要因此**拒绝连接**。只有实打实看见公网 v6 地址才算数;
//     查不出来就不算。m29 把"枚举失败"也当成漏,于是一次瞬时的系统调用失败就能让人连不上,
//     而且没有任何自愈路径。
func NICIPv6Leaking(tunName string) bool {
	leaking, known := nicIPv6LeakState(tunName)
	return leaking || !known
}

// NICIPv6LeakConfirmed 确证此刻有网卡挂着公网 IPv6 地址。查不出来一律报 false。
func NICIPv6LeakConfirmed(tunName string) bool {
	leaking, known := nicIPv6LeakState(tunName)
	return leaking && known
}

// nicIPv6LeakState 返回 (看见了吗, 查清楚了吗)。
// 只要有一张网卡看见了公网 v6,就是确凿的"在漏",不必管别的网卡查没查成。
func nicIPv6LeakState(tunName string) (leaking, known bool) {
	ifs, err := net.Interfaces()
	if err != nil {
		return false, false
	}
	known = true
	for _, in := range ifs {
		addrs, err := in.Addrs()
		if err != nil {
			known = false // 这张网卡读不到地址:剩下的照查,但"没看见"不能当成"确实没有"
			continue
		}
		if ifaceLeaksIPv6(in.Name, in.Flags, addrs, tunName) {
			return true, true
		}
	}
	return false, known
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
