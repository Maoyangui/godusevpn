//go:build windows

package ipc

import "testing"

// 控制管道的 ACL 是一份名单:SYSTEM 与管理员全权,登记过的每个账户可读写;名单为空只许管理员。
// m29 只存一个 SID、每次覆盖 —— 同机第二个账户的托盘永远连不上,界面还谎报"服务未运行"。
func TestSDDLForListsEveryRegisteredAccount(t *testing.T) {
	if got := sddlFor(nil); got != sddlFallback {
		t.Fatalf("名单为空应退回只许管理员: %q", got)
	}
	got := sddlFor([]string{"S-1-5-21-1-2-3-1001", "S-1-5-21-1-2-3-1002"})
	want := sddlFallback + "(A;;GRGW;;;S-1-5-21-1-2-3-1001)(A;;GRGW;;;S-1-5-21-1-2-3-1002)"
	if got != want {
		t.Fatalf("两个账户都要在 ACL 里:\n got %s\nwant %s", got, want)
	}
}
