//go:build windows || android

package netmode

// Windows 上 sing-box 的 auto_route 自己处理本机服务的回包,这里什么都不用做。
func Protect(string, bool) error { return nil }
func Unprotect()                 {}

// 网关模式只在 Linux 软路由上有。
func ApplyGateway(string, []string, bool) error { return nil }
func ClearGateway()                             {}
