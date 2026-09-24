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
	s.SelfPath = spec.SelfPath
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
	// 开机那组只覆盖开机到 BFE 启动之间那几秒。查不到 / 没装全都只记成警告,不拿它挡住开闸:
	// 挡住了用户就只能去关「全局禁直连」,一道以隐私为名的检查反而把人推向更不私密的配置。
	// 运行期那组是闸本身,它的失败在 wfp.Enable 里已经是硬错误了。
	if ready, berr := wfp.BootGuardReady(); berr != nil {
		warn = joinWarn(warn, "确认开机那组过滤器时出错(开机那几秒是否受保护未知): "+berr.Error())
	} else if !ready {
		warn = joinWarn(warn, "开机那组过滤器没装全:开机到防火墙引擎启动之间那几秒不受保护")
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

func joinWarn(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + ";" + b
	}
}

// GuardStatus 闸里现在有多少条过滤器;0 = 没开。
func GuardStatus() (int, error) { return wfp.Count() }

// BootGuardReady 查开机那组(BOOTTIME)装齐了没有。它覆盖的只是"内核网络起来到 BFE 启动"那几秒,
// **不是**启动前的硬条件 —— 拿它挡住开闸的话,一台装不上开机过滤器的机器就永远进不了全局模式,
// 而用户唯一的绕法是去关掉「全局禁直连」,反倒更不私密。结果经 ApplyGuard 折进 GuardWarning 如实报出来。
// 运行期那组(PERSISTENT)才是闸本身,它的就绪由 GuardPersistentReady 硬查。
func BootGuardReady() (bool, error) { return wfp.BootGuardReady() }

// GuardWarning 上次开闸时没装全的那部分(比如开机那组),给日志用;空 = 全装上了。
func GuardWarning() string {
	warnMu.Lock()
	defer warnMu.Unlock()
	return guardWarn
}

// GuardInstallable 这个平台的闸是不是由我们自己装、并且装完能核查。
// Windows(WFP)、Linux(nftables)、macOS(pf)都是;Android 不是 —— 那边的闸就是宿主
// VpnService 的接口本身,由系统持有,我们既装不了也数不出条数。对这种平台做"闸装没装"的
// 硬核查只会把连接整个挡死,而用户挡不住就会去把「全局禁直连」关掉,反倒更不私密。
func GuardInstallable() bool { return true }

// GuardPersistentSupported WFP 的对象是持久的:进程退出、被强杀、崩溃、升级换文件、重启,闸都还在。
func GuardPersistentSupported() bool { return true }

// GuardPersistentReady 查的是**运行期**那组过滤器覆盖全不全 —— 也就是"守护进程之外也在"的那道闸。
// 开机那组不在这条里(见 BootGuardReady)。
func GuardPersistentReady() (bool, error) { return wfp.PersistentGuardReady() }
