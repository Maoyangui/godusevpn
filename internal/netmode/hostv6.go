package netmode

// HostIPv6 本机现在有没有能出网的 IPv6:隧道自己那张、别的 VPN 的虚拟网卡、回环、没启用的之外,有没有哪张网卡带着公网 IPv6 地址。
// known = false:网卡读不出来(Android 11 起应用读不了网卡地址),下不了结论 —— 调用方那时什么都不说。
//
// 和 NICIPv6Leaking 看的是同一件事(nicIPv6LeakState),用意相反:那边嫌它在,这边要它在。
//
// 判的是"有没有地址",不是"通不通":路由器用 NAT66、只发唯一本地地址(fd00::)的网络会被说成没有 IPv6。
// 所以它只用来解释"这个只能走 IPv6 的节点为什么连不上",不拿它去拦任何连接、也不改配置。
func HostIPv6(tunName string) (has, known bool) {
	leakers, known := nicIPv6LeakState(tunName)
	return len(leakers) > 0, known
}
