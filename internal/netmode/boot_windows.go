package netmode

// ApplyBootGuard / DropNICReboot 只有 Linux / macOS 的 godusevpn-daemon 用(开机单元、卸载);
// 那个程序在 Windows 上也要能编译(go test ./...)。Windows 的闸本身跨重启(持久过滤器 + 开机过滤器),
// 网卡 IPv6 的停用也跨重启,都不需要开机再装一遍。
func ApplyBootGuard() error { return nil }

func DropNICReboot() {}
