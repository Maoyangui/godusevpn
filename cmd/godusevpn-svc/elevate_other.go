//go:build !windows

package main

// 别的平台没有 UAC,恢复命令直接用 sudo 跑。
func relaunchElevated() bool { return false }

func notify(string) {}
