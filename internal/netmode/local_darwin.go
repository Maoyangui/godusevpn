// macOS:sing-tun 自己建 utun 并把默认路由(IPv4 与 IPv6)指进隧道,不需要像 Linux 那样再加策略路由。
// 网关模式(代理经本机转发的局域网设备)是软路由的功能,macOS 上不提供。
//
// 关于 IPv6:关掉 IPv6 时 TUN 照样声明 v6 地址,auto_route 会把 v6 默认路由也指进隧道,
// 进来之后由路由规则里的 ip_version=6 拒绝掉 —— 也就是"接进来再拒绝",而不是放它从物理网卡出去。
// 这条在 macOS 上是否真的成立由 deploy/macos-test.sh 在真机上核验(查 v6 默认路由的下一跳与出网结果)。

package netmode

// Protect macOS 上无需额外处理:本机服务的回包沿原接口返回,不会被隧道吞掉。
func Protect(string, bool) error { return nil }

func Unprotect() {}

// ApplyGateway / ClearGateway 网关模式只在 Linux 软路由上有。
func ApplyGateway(string, []string, bool) error { return nil }

func ClearGateway() {}
