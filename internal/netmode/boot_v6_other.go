//go:build !linux || android

package netmode

// NICIPv6Drifted 只有 Linux 的停用不过重启、还会被网络管理器改回去(见 nic_linux.go);
// Windows / macOS 的停用跨重启留着,安卓动不了网卡。
func NICIPv6Drifted() bool { return false }

// DropBootRA 只有 Linux 的开机闸会挡路由器通告(见 guard_linux.go)。
func DropBootRA() {}
