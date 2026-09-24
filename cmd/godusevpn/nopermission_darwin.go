//go:build darwin

package main

import "errors"

// noPermissionHint macOS 的控制 socket 只对 root 与 admin 组开放,没有 Windows 那种名单,
// 界面替用户登记不了 —— 要么用管理员账户登录,要么把本账户加进 admin 组。
func noPermissionHint() (string, bool) { return "admin", false }

func registerController(string) error { return errors.New("E_NO_PERMISSION: ") }
