package daemon

// sysDiag 诊断包里的系统网络信息(Windows):路由表、网卡、接口列表。
func sysDiag(add func(name, content string)) {
	add("route.txt", cmdOut("route.exe", "print"))
	add("ipconfig.txt", cmdOut("ipconfig.exe", "/all"))
	add("netsh-interfaces.txt", cmdOut("netsh.exe", "interface", "ipv4", "show", "interfaces"))
}
