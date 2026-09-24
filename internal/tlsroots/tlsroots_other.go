//go:build !windows

package tlsroots

// Status 只在 Windows 上换(出问题的是 Windows 的平台校验)。
func Status() string { return "不适用(只在 Windows 上换)" }

// Active 非 Windows 上恒为 false。
func Active() bool { return false }
