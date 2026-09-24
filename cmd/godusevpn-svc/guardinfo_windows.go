//go:build windows

package main

import (
	"fmt"

	"github.com/Maoyangui/godusevpn/internal/netmode/wfp"
)

// guardDetail guard status 的第二行:几条被系统标成停用、运行期那组全不全。读不到就不说。
func guardDetail() string {
	total, disabled, ready, err := wfp.Breakdown()
	if err != nil || total == 0 {
		return ""
	}
	state := "齐全"
	if !ready {
		state = "不全"
	}
	boot := ""
	if dis, err := wfp.ProviderBootDisabled(); err == nil && dis {
		boot = ";提供者带着系统的「开机时已停用」标志(上次开机时这套规则被防火墙引擎停用过)"
	}
	return fmt.Sprintf("  其中被系统标为停用(不拦任何包)的 %d 条;运行期那组覆盖%s%s", disabled, state, boot)
}
