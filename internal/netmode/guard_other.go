//go:build android || (!linux && !darwin && !windows)

package netmode

// Android 的闸不在这里:VPN 接口本身就是闸,内核重启时不关它、只有用户点断开才关(见 mobile 包)。
// 别的平台没有实现。
func ApplyGuard(GuardSpec) error { return nil }
func GuardTunUp(GuardSpec) error { return nil }
func ClearGuard()                {}
