//go:build darwin

package netmode

import "testing"

// builder.HijackDNS 是 223.5.5.5 —— 一个真实存在的公共 DNS,用户自己完全可能填它。
// 兜底还原会把匹配到的服务改成"跟随 DHCP",所以判据必须是"整份列表恰好只有我们那一条",
// 不能是 Contains,否则会把用户自己配的 DNS 抹掉。
func TestOnlyHijackDNS(t *testing.T) {
	for _, c := range []struct {
		name string
		out  string
		want bool
	}{
		{"就是我们接管时写的那一条", "223.5.5.5", true},
		{"前后有空白", "\n 223.5.5.5 \n", true},
		{"用户自己填了它,还配了备用", "223.5.5.5\n223.6.6.6", false},
		{"用户配的是别的", "1.1.1.1", false},
		{"我们那条排在别人后面", "1.1.1.1\n223.5.5.5", false},
		{"根本没配(networksetup 的提示句)", "There aren't any DNS Servers set on Wi-Fi.", false},
		{"空输出", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := onlyHijackDNS(c.out); got != c.want {
				t.Fatalf("onlyHijackDNS(%q) = %v,想要 %v", c.out, got, c.want)
			}
		})
	}
}
