//go:build linux && !android

package netmode

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"

	"github.com/sagernet/netlink"
	tun "github.com/sagernet/sing-tun"
	"golang.org/x/sys/unix"

	"github.com/Maoyangui/godusevpn/internal/builder"
)

// Linux 用 nftables:一张 inet 表,output 链管本机发出的包,forward 链管经本机转发的包(网关模式的局域网设备,
// 本机模式下的 Docker / 虚拟机 NAT / 热点共享),都只许走隧道。
//
// 本服务按标记放行、不按 uid:内核的出站带 route.default_mark(网关模式下是 auto_redirect 的出站标记),
// 守护进程自己的直连(订阅回退、DoH)在套接字上打同一个标记(见 SelfControl)。root 下别的进程不再整个放行 ——
// NetworkManager 按网卡绑定的联网检测、隧道空档里的 apt / ntpd 都是 root —— 只放行它们回应入站连接的包
// (sshd、本面板),不然开着闸就连不上远程管理。
const guardTable = "godusevpn_guard"

// DNS 进隧道的策略路由:发往 53 端口的包先查这张表,表里只有一条"默认走隧道网卡"。
// 「局域网直通」把私网段排除在隧道外,resolv.conf 直接写路由器的系统连着时 DNS 也直接问路由器;
// 有了这条,连着时它进隧道被内核接住,隧道不在时表是空的、落回主表,再被闸里拦 DNS 的那条丢掉。
// 优先级要在回包规则(5000)和 sing-tun 的规则(9000 起)之前。
const (
	dnsRulePref = 4999
	dnsTable    = 5053
)

var (
	nftMu     sync.Mutex
	dnsSealed bool   // DNS 的策略路由装上了(闸里才拦 DNS、隧道起来后才补那条路由)
	guardWarn string // 闸装上了但没做全的那部分
)

// guardRuleset nft 脚本。先建再删再建:表不存在时 delete 会报错,先 add 一张空的就不会了,整段幂等。
// dns = DNS 已经接进隧道(策略路由装上了),这时才拦 DNS;接不进去还拦,连着时就解析不了。
func guardRuleset(spec GuardSpec, dns bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {}\n", guardTable)
	fmt.Fprintf(&b, "delete table inet %s\n", guardTable)
	fmt.Fprintf(&b, "table inet %s {\n", guardTable)
	b.WriteString("\tchain output {\n\t\ttype filter hook output priority filter; policy accept;\n")
	b.WriteString("\t\toifname \"lo\" accept\n")
	if spec.TunName != "" {
		fmt.Fprintf(&b, "\t\toifname %q accept\n", spec.TunName) // 经隧道出去的流量(按名字比,网卡还没建也认)
	}
	fmt.Fprintf(&b, "\t\tmeta mark %s accept\n", selfMarks(spec))        // 本服务:节点连接、订阅刷新的回退直连、DoH
	b.WriteString("\t\tmeta skuid 0 ct direction reply accept\n")        // root 服务回应入站连接(sshd、本面板)
	b.WriteString("\t\tmeta skuid 0 udp sport 68 udp dport 67 accept\n") // 系统 DHCP 客户端的单播续租
	blockLeaks(&b, dns)
	if spec.LAN {
		fmt.Fprintf(&b, "\t\tip daddr { %s } accept\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "\t\tip6 daddr { %s } accept\n", strings.Join(privateV6, ", "))
		b.WriteString("\t\tmeta skuid 0 ip daddr 168.63.129.16 accept\n") // Azure 平台地址(来宾代理),和它不进隧道是一个道理
	}
	// 只放行 DHCP 客户端的源端口，并限制到广播目的，避免把任意 UDP/67 当成直连例外。
	b.WriteString("\t\tudp sport 68 udp dport 67 ip daddr 255.255.255.255 accept\n") // DHCP 续租
	b.WriteString("\t\ticmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-solicit, nd-router-advert } accept\n")
	b.WriteString("\t\tdrop\n\t}\n")
	// 转发的流量不论网关还是本机模式都要管:本机模式下 Docker、虚拟机 NAT、热点共享也经这里出网,
	// 隧道空档里没有这条链就从物理网卡直出。
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	if spec.TunName != "" {
		fmt.Fprintf(&b, "\t\toifname %q accept\n", spec.TunName)
		fmt.Fprintf(&b, "\t\tiifname %q accept\n", spec.TunName) // 从隧道回来、转给设备的应答(UDP、ping)
	}
	if dns {
		b.WriteString(dnsDrop)
	}
	if spec.LAN {
		fmt.Fprintf(&b, "\t\tip daddr { %s } accept\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "\t\tip6 daddr { %s } accept\n", strings.Join(privateV6, ", "))
	}
	b.WriteString("\t\tdrop\n\t}\n")
	b.WriteString("}\n")
	return b.String()
}

// selfMarks 本服务的套接字带的标记。网关模式下 sing-box 不许设 route.default_mark(和 auto_redirect 冲突),
// 内核出站带的是 auto_redirect 的出站标记;守护进程自己的直连仍打 builder.RouteMark。
func selfMarks(spec GuardSpec) string {
	if spec.Gateway {
		return fmt.Sprintf("{ %#x, %#x }", builder.RouteMark, tun.DefaultAutoRedirectOutputMark)
	}
	return fmt.Sprintf("%#x", builder.RouteMark)
}

// blockLeaks 压在局域网放行之上的几条:DNS(53 / 853,DNS 已接进隧道时才拦)、
// UPnP 发现(SSDP,UDP 1900)与 NAT-PMP / PCP(UDP 5351)—— 本机程序靠后两样能向路由器问到宽带的真实公网地址。
func blockLeaks(b *strings.Builder, dns bool) {
	if dns {
		b.WriteString(dnsDrop)
	}
	b.WriteString(upnpDrop)
}

const (
	dnsDrop  = "\t\ttcp dport { 53, 853 } drop\n\t\tudp dport { 53, 853 } drop\n"
	upnpDrop = "\t\tudp dport { 1900, 5351 } drop\n"
)

func ApplyGuard(spec GuardSpec) error {
	nftMu.Lock()
	defer nftMu.Unlock()
	dns, why := sealDNS()
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(guardRuleset(spec, dns))
	if out, err := cmd.CombinedOutput(); err != nil {
		// nft -f 是整段事务:失败时旧的闸(如果有)原样留着,DNS 的策略路由也跟着留着,不动它
		return nftError("nft 加载规则", err, out)
	}
	dnsSealed = dns
	guardWarn = why
	return nil
}

// GuardTunUp 隧道网卡起来之后,给 DNS 的那张表补上"默认走隧道网卡"。放行本身按网卡名,不用等网卡。
func GuardTunUp(spec GuardSpec) error {
	nftMu.Lock()
	sealed := dnsSealed
	nftMu.Unlock()
	if !sealed || spec.TunName == "" {
		return nil
	}
	return routeDNSToTun(spec.TunName)
}

// ClearGuard 撤闸。表不随进程死(上次强杀留下的也要清),不存在也无妨;删了之后表还在才算失败。
func ClearGuard() error {
	nftMu.Lock()
	defer nftMu.Unlock()
	if out, err := exec.Command("nft", "delete", "table", "inet", guardTable).CombinedOutput(); err != nil && !nftMissing(out, err) {
		return nftError("nft 删除闸", err, out)
	}
	out, err := exec.Command("nft", "list", "table", "inet", guardTable).CombinedOutput()
	if err == nil {
		return fmt.Errorf("nft 表 %s 删不掉,闸还在", guardTable)
	}
	if !nftMissing(out, err) {
		return nftError("nft 确认闸状态失败", err, out)
	}
	unsealDNS() // 闸没了,DNS 的策略路由也撤:留着也不漏(隧道不在时落回主表),只是没用了
	dnsSealed, guardWarn = false, ""
	return nil
}

// GuardStatus 表里现在有多少条规则;0 = 没开。
func GuardStatus() (int, error) {
	out, err := exec.Command("nft", "list", "table", "inet", guardTable).CombinedOutput()
	if err != nil {
		if nftMissing(out, err) {
			return 0, nil // 表不存在,或者根本没装 nft(那也就不可能有我们的表)
		}
		return 0, nftError("nft 查询闸状态", err, out)
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasSuffix(t, "accept") || t == "drop" {
			n++
		}
	}
	return n, nil
}

// nftMissing 表不存在。没装 nft 时 exec 直接返回 ErrNotFound、输出是空的,也算:没有 nft 就不可能有我们的表。
func nftMissing(out []byte, err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	s := strings.ToLower(string(out))
	return strings.Contains(s, "no such file") || strings.Contains(s, "does not exist") || strings.Contains(s, "not found")
}

func nftError(what string, err error, out []byte) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s: 系统里没有 nft 命令,装不上全局禁直连的闸;先装 nftables(OpenWrt: opkg install nftables,Debian / Ubuntu: apt install nftables)", what)
	}
	return fmt.Errorf("%s: %v: %s", what, err, strings.TrimSpace(string(out)))
}

// ---- DNS 进隧道 ----

func dnsRule(family int) *netlink.Rule {
	r := netlink.NewRule()
	r.Family = family
	r.Priority = dnsRulePref
	r.Table = dnsTable
	r.Dport = netlink.NewRulePortRange(53, 53)
	return r
}

// sealDNS 装"发往 53 的包查 dnsTable"。v4 装上才算数;v6 装不上(内核没编 IPv6)不影响。
// 返回装没装上,以及没装上时给人看的原因。
func sealDNS() (bool, string) {
	if err := addDNSRule(unix.AF_INET); err != nil {
		return false, "DNS 没能接进隧道(" + err.Error() + "),闸也就没拦 DNS:「局域网直通」开着时,DNS 可能直接问路由器"
	}
	_ = addDNSRule(unix.AF_INET6)
	return true, ""
}

func addDNSRule(family int) error {
	if err := netlink.RuleAdd(dnsRule(family)); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	// 4.17 以前的内核不认端口条件,会悄悄装成"所有流量都查这张表" —— 装完核对,不对就删掉
	rules, err := netlink.RuleList(family)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if r.Priority == dnsRulePref && r.Table == dnsTable && !dnsPortOnly(r) {
			_ = netlink.RuleDel(&r)
			return errors.New("内核不支持按端口的策略路由(4.17 起才有)")
		}
	}
	return nil
}

// dnsPortOnly 这条规则确实只管 53 端口。
func dnsPortOnly(r netlink.Rule) bool {
	return r.Dport != nil && r.Dport.Start == 53 && r.Dport.End == 53
}

// unsealDNS 撤掉 sealDNS 装的规则和 routeDNSToTun 补的路由。只删我们那一条(按完整条件删),同优先级别人的不碰。
func unsealDNS() {
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		for i := 0; i < 16 && netlink.RuleDel(dnsRule(fam)) == nil; i++ {
		}
		routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: dnsTable}, netlink.RT_FILTER_TABLE)
		if err != nil {
			continue
		}
		for i := range routes {
			_ = netlink.RouteDel(&routes[i])
		}
	}
}

// routeDNSToTun dnsTable 里放一条"默认走隧道网卡"。路由跟着网卡走:内核重启、隧道网卡重建之后要重新补。
func routeDNSToTun(tunName string) error {
	link, err := netlink.LinkByName(tunName)
	if err != nil {
		return fmt.Errorf("找不到隧道网卡 %q: %w", tunName, err)
	}
	idx := link.Attrs().Index
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: idx, Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, Table: dnsTable}); err != nil {
		return fmt.Errorf("DNS 接进隧道: %w", err)
	}
	_ = netlink.RouteReplace(&netlink.Route{LinkIndex: idx, Dst: &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}, Table: dnsTable})
	return nil
}

// GuardWarning 闸装上了、但没做全的那部分(目前只有"DNS 接不进隧道所以没拦")。
func GuardWarning() string {
	nftMu.Lock()
	defer nftMu.Unlock()
	return guardWarn
}

// Linux nftables rules are runtime state.  They are not restored before the
// network stack can emit traffic after a reboot, so strict global mode must
// refuse to start until a boot-time firewall integration is installed.
func BootGuardReady() (bool, error) { return false, nil }

// GuardInstallable 这个平台的闸是不是由我们自己装、并且装完能核查。
// Windows(WFP)、Linux(nftables)、macOS(pf)都是;Android 不是 —— 那边的闸就是宿主
// VpnService 的接口本身,由系统持有,我们既装不了也数不出条数。对这种平台做"闸装没装"的
// 硬核查只会把连接整个挡死,而用户挡不住就会去把「全局禁直连」关掉,反倒更不私密。
func GuardInstallable() bool { return true }

// Linux currently has no daemon-independent boot-time guard.
func GuardPersistentSupported() bool      { return false }
func GuardPersistentReady() (bool, error) { return false, nil }

// GuardBootDisabled 只有 Windows 的 WFP 有"开机时被系统停用"这回事。
func GuardBootDisabled() (bool, error) { return false, nil }
