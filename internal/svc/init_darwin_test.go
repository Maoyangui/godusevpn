package svc

import (
	"strings"
	"testing"
)

// 开机闸的 LaunchDaemon:开机时跑一次本程序的 boot-guard,不常驻、不重拉。
func TestGuardPlistText(t *testing.T) {
	s := guardPlistText("/usr/local/bin/godusevpn")
	for _, want := range []string{
		"<string>com.maoyangui.godusevpn.guard</string>",
		"<string>/usr/local/bin/godusevpn</string>\n\t\t<string>boot-guard</string>",
		"<key>RunAtLoad</key><true/>", "<key>LaunchOnlyOnce</key><true/>",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("开机闸 plist 少了 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "KeepAlive") {
		t.Fatal("开机闸跑一次就完,不能让 launchd 反复拉")
	}
}
