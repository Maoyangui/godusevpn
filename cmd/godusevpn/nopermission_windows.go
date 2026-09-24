//go:build windows

package main

// noPermissionHint 控制口拒绝这个账户时给用户的一句话,以及界面能不能替用户登记。
// Windows:控制管道有一份账户名单,界面可以提权跑 register-controller --restart 把本账户加进去。
func noPermissionHint() (string, bool) {
	return "当前 Windows 账户没登记为控制用户。点「登记本账户」会提权登记并重启服务(闸不撤、隧道自动重连);或者用管理员身份运行 godusevpn-svc.exe register-controller,再重启服务。", true
}
