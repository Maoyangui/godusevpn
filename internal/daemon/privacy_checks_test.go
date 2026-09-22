package daemon

import "testing"

// 闸的核查只能在"我们自己装得上、也数得出来"的平台做。
//
// m29 曾经把这条做成无条件硬门:`!GuardPersistentSupported()` 就拒绝连接。而这个谓词只有
// Windows 返回真 —— macOS、Linux、OpenWrt 网关是硬编码 false(没有任何设置能打开),
// Android 要系统的 Always-on + lockdown 且 API≥29(本应用 minSdk=26,Android 8~9 的设备
// 和电视根本没有那两个接口)。结果是除 Windows 外的所有平台在「全局模式 + 全局禁直连」下
// 一律连不上,用户只能去**关掉**「全局禁直连」才能上网 —— 一道以隐私为名的检查,
// 实际把人推向了更不私密的配置。这条钉住:能核查才核查,核查不了就如实报告而不是挡死。
func TestPrivacyChecks(t *testing.T) {
	for _, c := range []struct {
		name                    string
		installable, persistent bool
		wantGuard, wantBoot     bool
	}{
		{"Windows:闸自己装,且有持久 + 开机过滤器", true, true, true, true},
		{"macOS / Linux:闸自己装,但没有开机期覆盖", true, false, true, false},
		{"Android:闸是系统持有的 VPN 接口,没有可核查对象", false, false, false, false},
		{"不该出现的组合:装不上闸却声称有持久保护", false, true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			guard, boot := privacyChecks(c.installable, c.persistent)
			if guard != c.wantGuard || boot != c.wantBoot {
				t.Fatalf("privacyChecks(%v, %v) = (%v, %v),想要 (%v, %v)",
					c.installable, c.persistent, guard, boot, c.wantGuard, c.wantBoot)
			}
		})
	}
}
