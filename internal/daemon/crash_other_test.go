//go:build !windows

package daemon

// crashInNativeCode 只在 Windows 的测试里用得上(那边模拟 DLL 里的访问违例)。
func crashInNativeCode() {}
