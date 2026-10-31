//go:build linux && !android

package netmode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 设备发给路由器的 DNS:只动局域网发给本机的;链排在 sing-tun 前面;走物理网卡时才做源地址转换。
func TestGatewayDNSRules(t *testing.T) {
	r := gatewayDNSRules("godusevpn", "223.5.5.5")
	for _, want := range []string{
		"table inet godusevpn_dns {}\ndelete table inet godusevpn_dns\n", // 幂等
		"type nat hook prerouting priority -101;",
		"iifname != \"godusevpn\" fib daddr type local ip saddr { " + lanSources + " } meta l4proto { udp, tcp } th dport 53 dnat ip to 223.5.5.5",
		"oifname != \"godusevpn\" ct status dnat ip daddr 223.5.5.5 meta l4proto { udp, tcp } th dport 53 masquerade",
	} {
		if !strings.Contains(r, want) {
			t.Fatalf("缺 %q:\n%s", want, r)
		}
	}
}

func TestResolverFor(t *testing.T) {
	for in, want := range map[string]string{"223.5.5.5": "223.5.5.5", " 119.29.29.29 ": "119.29.29.29", "system": fallbackDNS, "dns.alidns.com": fallbackDNS, "2400:3200::1": fallbackDNS, "": fallbackDNS,
		"192.168.1.1": fallbackDNS, "10.0.0.1": fallbackDNS, "127.0.0.53": fallbackDNS} {
		if got := resolverFor(in); got != want {
			t.Fatalf("%q: %s", in, got)
		}
	}
}

// sing-tun 按「隧道地址的下一个」接 DNS,这里要和它算的一样。
func TestTunDNSAddr(t *testing.T) {
	if got, want := tunDNSAddr(), "172.19.0.2"; got != want {
		t.Fatalf("%s != %s(隧道地址改了的话同步检查 sing-tun 的 Inet4DNSAddress)", got, want)
	}
}

// 本机有 DNS 服务才让路:systemd-resolved 只听回环,局域网设备连不到,不算。
func TestDNSServedLocally(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	head := "   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"
	resolved := write("resolved", head+"  1: 3500007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000 101 0 1 2 0\n  2: 3600007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000 101 0 1 2 0\n")
	if dnsServedLocally(resolved) {
		t.Fatal("只听回环的不算")
	}
	v6loop := write("v6loop", head+"  1: 00000000000000000000000001000000:0035 00000000000000000000000000000000:0000 07\n")
	if dnsServedLocally(v6loop) {
		t.Fatal("::1 也是回环")
	}
	dnsmasq := write("dnsmasq", head+"  1: 0100A8C0:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000 0 0 1 2 0\n")
	if !dnsServedLocally(resolved, dnsmasq) {
		t.Fatal("听局域网地址的 dnsmasq 要算")
	}
	any4 := write("any4", head+"  1: 00000000:0035 00000000:0000 07\n")
	if !dnsServedLocally(any4) {
		t.Fatal("听全部地址的要算")
	}
	other := write("other", head+"  1: 00000000:1F90 00000000:0000 07\n")
	if dnsServedLocally(other, filepath.Join(dir, "missing")) {
		t.Fatal("别的端口、读不到的文件都不算")
	}
}

// 本来关着:连着时打开,不想连了关回去,记录清掉。
func TestForwardRestoredWhenWeEnabledIt(t *testing.T) {
	dir := t.TempDir()
	sysctl, mark := filepath.Join(dir, "ip_forward"), filepath.Join(dir, "mark")
	if err := os.WriteFile(sysctl, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableForward(sysctl, mark); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, sysctl); got != "1\n" {
		t.Fatalf("应打开转发: %q", got)
	}
	// 重连时再来一遍:已经是开的,记录要留着,否则断开时就关不回去了
	if err := enableForward(sysctl, mark); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("记录不该丢")
	}
	restoreForward(sysctl, mark)
	if got := readFile(t, sysctl); got != "0\n" {
		t.Fatalf("应关回去: %q", got)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("记录应清掉")
	}
}

// 本来就开着(OpenWrt):不记录,断开时也不动。
func TestForwardLeftAloneWhenAlreadyOn(t *testing.T) {
	dir := t.TempDir()
	sysctl, mark := filepath.Join(dir, "ip_forward"), filepath.Join(dir, "mark")
	if err := os.WriteFile(sysctl, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableForward(sysctl, mark); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("不是我们开的不该有记录")
	}
	restoreForward(sysctl, mark)
	if got := readFile(t, sysctl); got != "1\n" {
		t.Fatalf("不是我们开的不该关: %q", got)
	}
}

// 记录是上次开机留下的:转发现在的状态和它无关,不能关。
func TestForwardStaleMarkIgnored(t *testing.T) {
	dir := t.TempDir()
	sysctl, mark := filepath.Join(dir, "ip_forward"), filepath.Join(dir, "mark")
	if err := os.WriteFile(sysctl, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mark, []byte("another-boot"), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreForward(sysctl, mark)
	if got := readFile(t, sysctl); got != "1\n" {
		t.Fatalf("上次开机的记录不该生效: %q", got)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("过期记录应清掉")
	}
}
