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
	return "Android 严格全局模式依赖系统 Always-on VPN 与阻止无 VPN 连接(lockdown)"
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
