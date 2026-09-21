//go:build !android && !linux && !darwin && !windows

package netmode

// 非 Android 的其它平台没有实现系统级持久闸。不能把普通进程内 TUN
// 误报成可覆盖崩溃/重启的隐私保护。
func ApplyGuard(GuardSpec) error { return nil }
func GuardTunUp(GuardSpec) error { return nil }
func ClearGuard() error          { return nil }

func GuardStatus() (int, error) { return 0, nil }
func GuardWarning() string      { return "当前平台没有可证明的持久全局禁直连保护" }

func BootGuardReady() (bool, error)       { return false, nil }
func GuardPersistentSupported() bool      { return false }
func GuardPersistentReady() (bool, error) { return false, nil }
