//go:build windows

package netmode

import (
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Maoyangui/godusevpn/internal/netmode/wfp"
)

// Windows 用 WFP(Windows 过滤平台,系统防火墙底下那一层),见 wfp 包。规则在一个动态会话里:
// 进程退出、句柄关闭,规则自动消失,服务被强杀也不会把机器留在断网状态。
// 放行按进程(本服务 exe)、按隧道网卡(LUID,网卡起来之后才知道,所以有 GuardTunUp 这一步)、
// 回环、局域网、DHCP、邻居发现;其余全拦。

var (
	iphlpapi                        = windows.NewLazySystemDLL("iphlpapi.dll")
	procConvertInterfaceIndexToLuid = iphlpapi.NewProc("ConvertInterfaceIndexToLuid")
)

func ApplyGuard(spec GuardSpec) error { return wfp.Enable(spec.LAN) }

// GuardTunUp 隧道网卡起来之后按它的 LUID 放行经隧道的流量。网卡每次重建 LUID 可能变,每次起来都要调。
func GuardTunUp(spec GuardSpec) error {
	ifi, err := net.InterfaceByName(spec.TunName)
	if err != nil {
		return fmt.Errorf("找隧道网卡 %s: %w", spec.TunName, err)
	}
	var luid uint64
	if r, _, _ := procConvertInterfaceIndexToLuid.Call(uintptr(ifi.Index), uintptr(unsafe.Pointer(&luid))); r != 0 {
		return fmt.Errorf("ConvertInterfaceIndexToLuid(%d) 失败: %d", ifi.Index, r)
	}
	return wfp.SetTunLUID(luid)
}

func ClearGuard() { wfp.Disable() }
