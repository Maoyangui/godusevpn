//go:build android

package netmode

import "fmt"

// Android's TUN protects traffic only while the app/service is alive.  It is
// not a process-crash or reboot guard.  Strict global mode is therefore
// allowed only when Android itself reports Always-on VPN with lockdown.
func androidVPNProtection() (AndroidVPNProtectionStatus, error) {
	probe := androidVPNProtectionProbe()
	if probe == nil {
		return AndroidVPNProtectionStatus{}, fmt.Errorf("Android 宿主未提供系统 VPN 保护状态查询")
	}
	s, err := probe()
	if err != nil {
		return AndroidVPNProtectionStatus{}, fmt.Errorf("查询 Android 系统 VPN 保护状态失败: %w", err)
	}
	ready, err := androidVPNProtectionReady(s)
	if err != nil {
		return s, err
	}
	if !ready {
		return s, nil
	}
	return s, nil
}

// Android 的数据面仍然使用宿主 VpnService 的 TUN；这里没有 Go 侧闸可
// 安装，所有方法保持幂等空操作，状态只由系统 lockdown 查询决定。
func ApplyGuard(GuardSpec) error { return nil }
func GuardTunUp(GuardSpec) error { return nil }
func ClearGuard() error          { return nil }

// GuardInstallable 这个平台的闸是不是由我们自己装、并且装完能核查。
// Windows(WFP)、Linux(nftables)、macOS(pf)都是;Android 不是 —— 那边的闸就是宿主
// VpnService 的接口本身,由系统持有,我们既装不了也数不出条数。对这种平台做"闸装没装"的
// 硬核查只会把连接整个挡死,而用户挡不住就会去把「全局禁直连」关掉,反倒更不私密。
func GuardInstallable() bool { return false }

// GuardStatus 报告系统层面的跨进程保护:只有开了 Always-on + lockdown 才算 1。
// 注意它**不是**"此刻有没有闸" —— 数据面在跑的时候闸就是 VpnService 的接口本身。
// 所以别拿它当启动前的硬门(见 daemon.ensurePrivacyReady 里的 GuardInstallable 判断)。
func GuardStatus() (int, error) {
	s, err := androidVPNProtection()
	if err != nil {
		return 0, err
	}
	if s.AlwaysOn && s.Lockdown {
		return 1, nil
	}
	return 0, nil
}

func GuardWarning() string {
	s, err := androidVPNProtection()
	if err != nil || !s.Queried {
		return "Android 的跨进程 / 重启保护要在系统设置里把本应用设为「始终开启 VPN」并打开「阻止未经 VPN 的连接」"
	}
	if s.AlwaysOn && s.Lockdown {
		return ""
	}
	return "系统的「始终开启 VPN」/「阻止未经 VPN 的连接」没全开:客户端进程被杀或重启之后,这段时间的流量不受保护"
}

func BootGuardReady() (bool, error) {
	s, err := androidVPNProtection()
	if err != nil {
		return false, err
	}
	return s.AlwaysOn && s.Lockdown, nil
}

func GuardPersistentSupported() bool {
	s, err := androidVPNProtection()
	return err == nil && s.Queried && s.AlwaysOn && s.Lockdown
}

func GuardPersistentReady() (bool, error) { return BootGuardReady() }

// GuardBootDisabled 只有 Windows 的 WFP 有"开机时被系统停用"这回事。
func GuardBootDisabled() (bool, error) { return false, nil }
