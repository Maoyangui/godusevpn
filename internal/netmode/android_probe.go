package netmode

import "sync"

// AndroidVPNProtectionStatus is a read-only snapshot supplied by the Android
// host.  The Go engine must never infer reboot protection from a TUN file
// descriptor: only the operating system's Always-on + lockdown state can
// cover the process-crash and reboot cases.
type AndroidVPNProtectionStatus struct {
	Queried   bool
	AlwaysOn  bool
	Lockdown  bool
	ErrorText string
}

var androidProbe struct {
	sync.RWMutex
	fn func() (AndroidVPNProtectionStatus, error)
}

// SetAndroidVPNProtectionProbe installs the host query used by the Android
// netmode implementation.  It is deliberately a read-only callback: it must
// not open/close a VPN or change any system setting.
func SetAndroidVPNProtectionProbe(fn func() (AndroidVPNProtectionStatus, error)) {
	androidProbe.Lock()
	androidProbe.fn = fn
	androidProbe.Unlock()
}

func androidVPNProtectionProbe() func() (AndroidVPNProtectionStatus, error) {
	androidProbe.RLock()
	defer androidProbe.RUnlock()
	return androidProbe.fn
}

func androidVPNProtectionReady(s AndroidVPNProtectionStatus) (bool, error) {
	if s.ErrorText != "" {
		return false, &privacyProbeError{message: s.ErrorText}
	}
	if !s.Queried {
		return false, &privacyProbeError{message: "Android 宿主没有返回系统 VPN 保护状态"}
	}
	return s.AlwaysOn && s.Lockdown, nil
}

type privacyProbeError struct{ message string }

func (e *privacyProbeError) Error() string { return e.message }
