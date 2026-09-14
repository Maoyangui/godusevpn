//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// relaunchElevated 没有管理员权限就以管理员身份重新跑一遍自己(弹 UAC),返回 true 表示已经交给新进程、
// 本进程该退出了。「恢复网络」的快捷方式和托盘菜单都走这里:改 WFP 过滤器必须是管理员。
func relaunchElevated() bool {
	if windows.GetCurrentProcessToken().IsElevated() {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	args, _ := syscall.UTF16PtrFromString(strings.Join(os.Args[1:], " "))
	dir, _ := syscall.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, args, dir, windows.SW_SHOWNORMAL) == nil
}
