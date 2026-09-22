package netmode

import "testing"

// 平台契约:声称有"守护进程之外也在"的持久 / 开机期闸,前提是这个平台的闸本来就由我们安装、
// 并且装完数得出来。两者不一致会让 daemon.privacyChecks 算出自相矛盾的结论 —— 要么放过了
// 该核查的平台,要么把核查不了的平台挡死(m29 就是后者,除 Windows 外全都连不上)。
//
// 这条测试在每个平台的 CI 作业里各跑一遍(windows / linux / macos),GOOS=android 下由 go vet 编译。
func TestGuardPredicatesAreConsistent(t *testing.T) {
	if GuardPersistentSupported() && !GuardInstallable() {
		t.Fatal("这个平台声称有持久 / 开机期闸,却报告闸不是自己装的:两个谓词必须一致")
	}
}
