//go:build !windows && !darwin

package main

// noPermissionHint 控制口拒绝这个账户时给用户的一句话(Linux:要 root / sudo)。
func noPermissionHint() (string, bool) {
	return "连接控制口需要 root / sudo。", false
}
