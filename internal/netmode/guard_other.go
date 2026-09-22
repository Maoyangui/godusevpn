//go:build !android && !linux && !darwin && !windows

package netmode

// 非 Android 的其它平台没有实现系统级持久闸。不能把普通进程内 TUN
// 误报成可覆盖崩溃/重启的隐私保护。
func ApplyGuard(GuardSpec) error { return nil }
func GuardTunUp(GuardSpec) error { return nil }
func ClearGuard() error          { return nil }

func GuardStatus() (int, error) { return 0, nil }
func GuardWarning() string      { return "当前平台没有可证明的持久全局禁直连保护" }

// GuardInstallable 这个平台的闸是不是由我们自己装、并且装完能核查。
// Windows(WFP)、Linux(nftables)、macOS(pf)都是;Android 不是 —— 那边的闸就是宿主
// VpnService 的接口本身,由系统持有,我们既装不了也数不出条数。对这种平台做"闸装没装"的
// 硬核查只会把连接整个挡死,而用户挡不住就会去把「全局禁直连」关掉,反倒更不私密。
func GuardInstallable() bool { return false }

func BootGuardReady() (bool, error)       { return false, nil }
func GuardPersistentSupported() bool      { return false }
func GuardPersistentReady() (bool, error) { return false, nil }
