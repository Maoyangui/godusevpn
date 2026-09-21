//go:build windows

package netmode

import (
	"errors"
	"fmt"
	"net"
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

// GuardTunUp 隧道网卡起来之后:本机 socket 那四层按隧道地址放行,网卡起不起来无所谓;
// 但**经本机转发**的流量(热点共享、ICS)不走 socket,只在 IP 转发层能拦 —— 那一层按网卡放行,
// 得等网卡真的在了才知道它的接口号。查不到网卡就报错,守护进程会记进状态并在下一次同步时重试。
func GuardTunUp(spec GuardSpec) error {
	ifi, err := net.InterfaceByName(spec.TunName)
	if err != nil {
		return fmt.Errorf("找不到隧道网卡 %q: %w", spec.TunName, err)
	}
	return wfp.TunUp(uint32(ifi.Index))
}

// ClearGuard 撤闸。失败要报出来:守护进程据此把状态记成"闸还在",不能记成"没开"。
// 过滤器已经删干净、只是子层 / 提供者收尾没做完的不算失败(联网已经恢复),记成警告给日志看。
func ClearGuard() error {
	err := wfp.Disable()
	var ce *wfp.CleanupError
	if errors.As(err, &ce) {
		warnMu.Lock()
		guardWarn = "撤闸收尾没做完(不影响联网,下次开闸接着用): " + ce.Error()
		warnMu.Unlock()
		return nil
	}
	if err == nil {
		warnMu.Lock()
		guardWarn = ""
		warnMu.Unlock()
	}
	return err
}

// GuardStatus 闸里现在有多少条过滤器;0 = 没开。
func GuardStatus() (int, error) { return wfp.Count() }

// GuardWarning 上次开闸时没装全的那部分(比如开机那组),给日志用;空 = 全装上了。
func GuardWarning() string {
	warnMu.Lock()
	defer warnMu.Unlock()
	return guardWarn
}
