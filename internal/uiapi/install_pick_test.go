package uiapi

import "testing"

// macOS 的发布包里有两个文件都叫 godusevpn:根目录下的守护进程,和
// godusevpn.app/Contents/MacOS/godusevpn 那个图形界面。早先按 basename 认、第一个命中就用,
// 抽到界面那一份的话,/usr/local/bin/godusevpn 会被换成一个 Cocoa 窗口程序,launchd 以 root
// 反复拉起它,VPN 彻底不能用,连 `godusevpn uninstall` 也没了,用户只能重装。
// 这条判断放在不带构建标签的文件里,Windows 上的 go test ./... 也能跑到。
func TestIsRootBinary(t *testing.T) {
	yes := []string{"godusevpn", "./godusevpn"}
	no := []string{
		"godusevpn.app/Contents/MacOS/godusevpn",
		"./godusevpn.app/Contents/MacOS/godusevpn",
		"bin/godusevpn",
		"./godusevpn.app/Contents/Info.plist",
		"godusevpn-cli",
		"",
		"./",
	}
	for _, n := range yes {
		if !isRootBinary(n) {
			t.Errorf("%q 是根目录下的守护进程,应该认出来", n)
		}
	}
	for _, n := range no {
		if isRootBinary(n) {
			t.Errorf("%q 不是根目录下的守护进程,不该认", n)
		}
	}
}
