package daemon

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestParseDnsmasqLeases(t *testing.T) {
	now := time.Now().Unix()
	b := fmt.Sprintf("%d aa:bb:cc:dd:ee:01 192.168.1.10 tv 01:aa:bb\n"+
		"%d AA-BB-CC-DD-EE-02 192.168.1.11 * *\n"+
		"0 aa:bb:cc:dd:ee:03 192.168.1.12 nas *\n"+
		"%d aa:bb:cc:dd:ee:04 192.168.1.13 gone *\n"+
		"%d 00:11:22:33:44:55 2001:db8::5 v6host 00:03\n"+
		"duid 00:01:00:01\n"+
		"garbage\n", now+3600, now+3600, now-3600, now+3600)
	out := map[string]*seen{}
	parseLeases("/tmp/dhcp.leases", []byte(b), now, out)
	want := map[string]*seen{
		"aa:bb:cc:dd:ee:01": {ip: "192.168.1.10", name: "tv"},
		"aa:bb:cc:dd:ee:02": {ip: "192.168.1.11"},
		"aa:bb:cc:dd:ee:03": {ip: "192.168.1.12", name: "nas"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("实际 %+v", out)
	}
}

// ISC 的文件以前按 dnsmasq 的格式读,一行都读不出来;同一个地址靠后的那段才算数,释放了的、到期的不要。
func TestParseISCLeases(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).Unix()
	b := `# The format of this file is documented in the dhcpd.leases(5) manual page.
lease 192.168.1.10 {
  starts 5 2026/10/30 10:00:00;
  ends 5 2026/10/30 14:00:00;
  binding state active;
  hardware ethernet aa:bb:cc:dd:ee:01;
  client-hostname "tv";
}
lease 192.168.1.11 {
  ends 5 2026/10/30 14:00:00;
  binding state active;
  hardware ethernet aa:bb:cc:dd:ee:02;
}
lease 192.168.1.11 {
  ends 5 2026/10/30 11:00:00;
  binding state free;
  hardware ethernet aa:bb:cc:dd:ee:02;
}
lease 192.168.1.12 {
  ends 5 2026/10/30 11:00:00;
  binding state active;
  hardware ethernet aa:bb:cc:dd:ee:03;
}
lease 192.168.1.13 {
  ends epoch 1924992000; # Sun Jan 01 00:00:00 2031
  binding state active;
  hardware ethernet aa:bb:cc:dd:ee:04;
}
lease 192.168.1.14 {
  ends never;
  binding state active;
  hardware ethernet aa:bb:cc:dd:ee:05;
}
`
	out := map[string]*seen{}
	parseLeases("/var/lib/dhcp/dhcpd.leases", []byte(b), now, out)
	want := map[string]*seen{
		"aa:bb:cc:dd:ee:01": {ip: "192.168.1.10", name: "tv"},
		"aa:bb:cc:dd:ee:04": {ip: "192.168.1.13"},
		"aa:bb:cc:dd:ee:05": {ip: "192.168.1.14"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("实际 %+v", out)
	}
}

func TestParseNeighbors(t *testing.T) {
	got := parseIPNeigh([]byte(`10.99.0.50 dev eth1 lladdr 02:42:0a:63:00:32 REACHABLE
10.99.0.51 dev eth1 lladdr 02:42:0a:63:00:33 router STALE
10.99.0.52 dev eth1 FAILED
10.99.0.53 dev eth1 INCOMPLETE
10.99.0.54 dev eth1 lladdr 02:42:0a:63:00:34 PERMANENT
`))
	want := []neighbor{
		{ip: "10.99.0.50", dev: "eth1", mac: "02:42:0a:63:00:32", online: true},
		{ip: "10.99.0.51", dev: "eth1", mac: "02:42:0a:63:00:33", online: true},
		{ip: "10.99.0.54", dev: "eth1", mac: "02:42:0a:63:00:34"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ip neigh 实际 %+v", got)
	}

	got = parseProcARP([]byte(`IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         AA:BB:CC:DD:EE:01     *        br-lan
192.168.1.2      0x1         0x0         00:00:00:00:00:00     *        br-lan
`))
	want = []neighbor{{ip: "192.168.1.1", dev: "br-lan", mac: "aa:bb:cc:dd:ee:01", online: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("/proc/net/arp 实际 %+v", got)
	}
}

func TestParseDefaultRoute(t *testing.T) {
	// 192.168.1.1 = 0101A8C0;第二条默认路由跃点数更小,要选它
	b := []byte(`Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	00000000	0101A8C0	0003	0	0	600	00000000	0	0	0
wlan0	00000000	0102A8C0	0003	0	0	100	00000000	0	0	0
eth0	0001A8C0	00000000	0001	0	0	0	00FFFFFF	0	0	0
`)
	if dev, gw := parseDefaultRoute(b); dev != "wlan0" || gw != "192.168.2.1" {
		t.Fatalf("实际 %s %s", dev, gw)
	}
	// PPPoE 没有网关
	if dev, gw := parseDefaultRoute([]byte("pppoe-wan\t00000000\t00000000\t0001\t0\t0\t0\t00000000\t0\t0\t0\n")); dev != "pppoe-wan" || gw != "" {
		t.Fatalf("实际 %s %q", dev, gw)
	}
	if dev, _ := parseDefaultRoute([]byte("eth0\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n")); dev != "" {
		t.Fatalf("没有默认路由却认出了 %s", dev)
	}
}

func TestVirtualIface(t *testing.T) {
	for name, want := range map[string]bool{
		"lo": true, "docker0": true, "veth12ab": true, "wg0": true, "tun0": true, "tailscale0": true,
		"br-3f2a9c1d0e4b": true, "virbr0": true, "zt1234": true,
		"br-lan": false, "eth0": false, "eth1.10": false, "wlan0": false, "enp3s0": false, "pppoe-wan": false, "bond0": false,
		"br-lan000000000": false, // 长度凑巧也是 12 位,但不是十六进制
	} {
		if virtualIface(name) != want {
			t.Errorf("%s 应为 %v", name, want)
		}
	}
}

// 这次要修的第二个毛病:旁路由只有一张网卡,默认路由也走它,以前它上面的邻居全被当成"上游"排除,设备页一台设备都没有。
func TestLanFilterSingleArm(t *testing.T) {
	neigh := []neighbor{
		{ip: "192.168.1.1", dev: "eth0", mac: "aa:aa:aa:aa:aa:01", online: true},  // 主路由
		{ip: "192.168.1.20", dev: "eth0", mac: "aa:aa:aa:aa:aa:02", online: true}, // 设备
		{ip: "172.17.0.2", dev: "docker0", mac: "aa:aa:aa:aa:aa:03", online: true},
	}
	skipDev, skipIP := lanFilter("eth0", "192.168.1.1", []string{"eth0"})
	out := map[string]*seen{}
	mergeNeighbors(out, neigh, skipDev, skipIP)
	want := map[string]*seen{"aa:aa:aa:aa:aa:02": {ip: "192.168.1.20", online: true}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("单网口:实际 %+v", out)
	}
}

// 正常的双网口软路由:上游网卡上的邻居(运营商网关、同网段的邻居)一律不算。
func TestLanFilterDualNIC(t *testing.T) {
	neigh := []neighbor{
		{ip: "100.64.0.1", dev: "eth0", mac: "aa:aa:aa:aa:aa:01", online: true},
		{ip: "100.64.0.9", dev: "eth0", mac: "aa:aa:aa:aa:aa:04", online: true},
		{ip: "192.168.1.20", dev: "br-lan", mac: "aa:aa:aa:aa:aa:02", online: true},
	}
	skipDev, skipIP := lanFilter("eth0", "100.64.0.1", []string{"eth0", "br-lan"})
	if skipIP != "" {
		t.Fatalf("双网口不用再按地址排除: %s", skipIP)
	}
	out := map[string]*seen{}
	mergeNeighbors(out, neigh, skipDev, skipIP)
	want := map[string]*seen{"aa:aa:aa:aa:aa:02": {ip: "192.168.1.20", online: true}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("双网口:实际 %+v", out)
	}
}

// 租约给名字,邻居表给在线状态和当前地址:设备换了 IP 以邻居表为准;离线的留租约里的地址。
func TestMergeNeighbors(t *testing.T) {
	out := map[string]*seen{
		"aa:aa:aa:aa:aa:01": {ip: "192.168.1.10", name: "tv"},
		"aa:aa:aa:aa:aa:02": {ip: "192.168.1.11", name: "nas"},
	}
	mergeNeighbors(out, []neighbor{
		{ip: "192.168.1.99", dev: "br-lan", mac: "aa:aa:aa:aa:aa:01", online: true},
		{ip: "192.168.1.50", dev: "br-lan", mac: "aa:aa:aa:aa:aa:02"},
		{ip: "192.168.1.60", dev: "br-lan", mac: "aa:aa:aa:aa:aa:03"},
	}, func(string) bool { return false }, "")
	want := map[string]*seen{
		"aa:aa:aa:aa:aa:01": {ip: "192.168.1.99", name: "tv", online: true},
		"aa:aa:aa:aa:aa:02": {ip: "192.168.1.11", name: "nas"},
		"aa:aa:aa:aa:aa:03": {ip: "192.168.1.60"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("实际 %+v", out)
	}
}
