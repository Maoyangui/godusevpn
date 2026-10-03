//go:build windows

package wfp

import (
	"strings"
	"testing"
)

// 热点那几条放行只许局域网内往来:DHCP 回包限定发往私网 / 广播(不限的话,任何程序绑个 67 端口就能往公网发 UDP),
// DNS 只放私网来的。名单里不能混进公网段。
func TestHotspotPermitsStayInLAN(t *testing.T) {
	src := sourceFunc(t, "rules_lan.go", "func permitHotspot(")
	out := src[strings.Index(src, "hotspotDHCPOutName,"):]
	out = out[:strings.Index(out, "hotspotDNSName")]
	if !strings.Contains(out, "peers(len(hotspotPeers))") {
		t.Fatal("DHCP 回包放行没限定目的地址")
	}
	if dns := src[strings.Index(src, "hotspotDNSName,"):]; !strings.Contains(dns, "peers(len(hotspotPeers)-1)") {
		t.Fatal("DNS 代理放行没限定来源(或把广播也算进来了)")
	}
	private := func(a, m uint32) bool {
		for _, p := range [][2]uint32{{0x0A000000, 0xFF000000}, {0xAC100000, 0xFFF00000}, {0xC0A80000, 0xFFFF0000}} {
			if m >= p[1] && a&p[1] == p[0] {
				return true
			}
		}
		return false
	}
	for i, p := range hotspotPeers {
		last := i == len(hotspotPeers)-1
		if last && (p.addr != 0xFFFFFFFF || p.mask != 0xFFFFFFFF) {
			t.Fatal("名单最后一项应是 DHCP 广播 255.255.255.255(DNS 那条按 len-1 把它排除在外)")
		}
		if !last && !private(p.addr, p.mask) {
			t.Fatalf("热点名单第 %d 项 %#x/%#x 不是私网段", i, p.addr, p.mask)
		}
	}
}
