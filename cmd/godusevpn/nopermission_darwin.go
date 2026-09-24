//go:build darwin

package main

// noPermissionHint 控制口拒绝这个账户时给用户的一句话。macOS 的控制 socket 只对 root 与 admin 组开放,
// 没有 Windows 那种名单,界面替用户登记不了 —— 要么用管理员账户登录,要么把本账户加进 admin 组。
func noPermissionHint() (string, bool) {
	return "控制口只对管理员组开放:用管理员账户登录,或在「系统设置 → 用户与群组」里把本账户设为管理员后重新登录。", false
}
