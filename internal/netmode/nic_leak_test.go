package netmode

import (
	"net"
	"testing"
)

func cidr(t *testing.T, s string) net.Addr {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	return n
}

// 停用网卡 IPv6 只在连接建立时做一次,连接期间新接一张网卡就会漏。守护进程靠这个判断要不要补做一次,
// 所以"什么算漏"必须判准:判松了每 30 秒白跑一次 PowerShell,判严了就真漏了。
func TestIfaceLeaksIPv6(t *testing.T) {
	const tun = "godusevpn"
	up := net.FlagUp
	cases := []struct {
		name  string
		iface string
		flags net.Flags
		addrs []string
		want  bool
		why   string
	}{
		{"公网 v6 在普通网卡上", "以太网", up, []string{"2408:8214::1/64"}, true, "正是要抓的:这个地址程序枚举得到"},
		{"隧道自己的地址不算", tun, up, []string{"2408:8214::1/64"}, false, "隧道要留着 v6 把流量接进来再拒绝"},
		{"隧道名大小写不同也认", "GODUSEVPN", up, []string{"2408:8214::1/64"}, false, "网卡名匹配不区分大小写"},
		{"链路本地不算", "以太网", up, []string{"fe80::1/64"}, false, "出不了网,也读不出身份"},
		{"唯一本地不算", "以太网", up, []string{"fdfe:dcba:9876::1/48"}, false, "fc00::/7 出不了网"},
		{"回环不算", "lo", up | net.FlagLoopback, []string{"::1/128"}, false, "回环网卡整张跳过"},
		{"没启用的网卡不算", "以太网", 0, []string{"2408:8214::1/64"}, false, "没 up 就没在用"},
		{"只有 v4 不算", "以太网", up, []string{"192.168.1.4/24"}, false, "这条只管 v6"},
		{"v4 与公网 v6 并存要抓", "以太网", up, []string{"192.168.1.4/24", "2408:8214::1/64"}, true, "有一个公网 v6 就算漏"},
		{"没有地址不算", "以太网", up, nil, false, "空网卡"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var addrs []net.Addr
			for _, a := range c.addrs {
				addrs = append(addrs, cidr(t, a))
			}
			if got := ifaceLeaksIPv6(c.iface, c.flags, addrs, tun); got != c.want {
				t.Fatalf("判成 %v,应为 %v —— %s", got, c.want, c.why)
			}
		})
	}
}

// 真机上跑一遍,保证不会 panic、也不会因为拿不到网卡列表就报错。
func TestNICIPv6LeakingRunsOnThisMachine(t *testing.T) {
	_ = NICIPv6Leaking("godusevpn")
}
