//go:build !windows

package main

// guardDetail 只有 Windows 的 WFP 有"被系统停用"这回事。
func guardDetail() string { return "" }
