//go:build !windows

// Linux 上"登录时自动启动"等价于守护进程的开机自启(systemd enable / procd enable / Entware ENABLED)。
package autostart

import "github.com/Maoyangui/godusevpn/internal/svc"

func Enabled() bool     { return svc.Enabled() }
func Set(on bool) error { return svc.SetEnabled(on) }
