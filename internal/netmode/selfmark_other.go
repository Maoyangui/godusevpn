//go:build !linux || android

package netmode

import "syscall"

// SelfControl 只有 Linux 的闸按套接字标记认本服务;别的平台什么都不做。
func SelfControl(_, _ string, _ syscall.RawConn) error { return nil }
