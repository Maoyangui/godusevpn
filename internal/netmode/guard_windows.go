//go:build windows

package netmode

import (
	"fmt"
	"net/netip"
	"sync"

	"github.com/Maoyangui/godusevpn/internal/netmode/wfp"
)

// Windows 用 WFP(Windows 过滤平台,系统防火墙底下那一层),见 wfp 包。对象是持久的:进程退出、被强杀、
// 崩溃、升级、重启,闸都在;另有一组开机过滤器堵住开机到 BFE 启动之间那几秒。只有明确撤闸才删。
// 放行按进程(本服务 exe)、按隧道地址、回环、局域网、DHCP、邻居发现;其余全拦。

var (
	warnMu    sync.Mutex
	guardWarn string
)

func ApplyGuard(spec GuardSpec) error {
	var s wfp.Spec
	s.LAN = spec.LAN
	if a, err := netip.ParseAddr(spec.TunAddr4); err == nil && a.Is4() {
		s.Tun4 = a.As4()
	} else {
		return fmt.Errorf("隧道 v4 地址不合法: %q", spec.TunAddr4)
	}
	if a, err := netip.ParseAddr(spec.TunAddr6); err == nil && a.Is6() {
		s.Tun6 = a.As16()
	}
	warn, err := wfp.Enable(s)
	if err != nil {
		return err
	}
	warnMu.Lock()
	guardWarn = warn
	warnMu.Unlock()
	return nil
}

// GuardTunUp 按隧道地址放行,网卡起不起来无所谓。
func GuardTunUp(GuardSpec) error { return nil }

func ClearGuard() { _ = wfp.Disable() }

// GuardStatus 闸里现在有多少条过滤器;0 = 没开。
func GuardStatus() (int, error) { return wfp.Count() }

// GuardWarning 上次开闸时没装全的那部分(比如开机那组),给日志用;空 = 全装上了。
func GuardWarning() string {
	warnMu.Lock()
	defer warnMu.Unlock()
	return guardWarn
}
