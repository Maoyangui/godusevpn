//go:build linux && !android

package netmode

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/sagernet/netlink"
	tun "github.com/sagernet/sing-tun"
	"golang.org/x/sys/unix"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/paths"
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
	v4, v6 := spec.directFamilies()
	if len(v4) > 0 {
		fmt.Fprintf(&b, "\tset direct4 {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\telements = { %s }\n\t}\n", strings.Join(v4, ", "))
	}
	if len(v6) > 0 {
		fmt.Fprintf(&b, "\tset direct6 {\n\t\ttype ipv6_addr\n\t\tflags interval\n\t\telements = { %s }\n\t}\n", strings.Join(v6, ", "))
	}
	b.WriteString("\tchain output {\n\t\ttype filter hook output priority filter; policy accept;\n")
	b.WriteString("\t\toifname \"lo\" accept\n")
	if spec.TunName != "" {
		fmt.Fprintf(&b, "\t\toifname %q accept\n", spec.TunName) // 经隧道出去的流量(按名字比,网卡还没建也认)
	}
	fmt.Fprintf(&b, "\t\tmeta mark %s accept\n", selfMarks(spec)) // 本服务:节点连接、订阅刷新的回退直连、DoH
	if spec.Gateway {
		// 网关模式下路由器自己发的包是出来之后才被 sing-tun 改道的:UDP、ICMP 打标记走隧道,TCP 改发给本机的转发端口,
		// TCP 的 DNS 改发给隧道里的地址。这条链看到的网卡还是改道前的物理网卡,按网卡名放行不了,路由器自己就上不了网。
		// 打了标记的包要是其实没进隧道,由下面的 postrouting 链拦(那里看到的才是真正出去的网卡)。
		fmt.Fprintf(&b, "\t\tmeta mark 0x%x accept\n", tun.DefaultAutoRedirectInputMark)
		b.WriteString("\t\tip daddr 127.0.0.0/8 accept\n\t\tip6 daddr ::1 accept\n")
		fmt.Fprintf(&b, "\t\tip daddr %s accept\n\t\tip6 daddr %s accept\n", builder.TunAddr4, builder.TunAddr6)
	}
	if spec.Rule {
		b.WriteString("\t\tct direction reply accept\n") // 规则模式不拦入站:规则管的是本机去哪儿,不是谁连进来
	} else {
		b.WriteString("\t\tmeta skuid 0 ct direction reply accept\n") // root 服务回应入站连接(sshd、本面板)
	}
	b.WriteString("\t\tmeta skuid 0 udp sport 68 udp dport 67 accept\n") // 系统 DHCP 客户端的单播续租
	if spec.Rule {
		b.WriteString(fakeDrop) // 规则模式不拦 DNS 和 UPnP:隧道一断,直连的网站还要解析(见 ruleAllow)
	} else {
		blockLeaks(&b, dns)
	}
	if spec.LAN {
		fmt.Fprintf(&b, "\t\tip daddr { %s } accept\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "\t\tip6 daddr { %s } accept\n", strings.Join(privateV6, ", "))
		b.WriteString("\t\tmeta skuid 0 ip daddr 168.63.129.16 accept\n") // Azure 平台地址(来宾代理),和它不进隧道是一个道理
	}
	ruleAllow(&b, spec, false)
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
	if spec.Rule {
		b.WriteString("\t\tct direction reply accept\n")
	} else if dns {
		b.WriteString(dnsDrop)
	}
	if spec.LAN {
		b.WriteString(fakeDrop) // 网关模式下局域网设备也拿到假地址
		fmt.Fprintf(&b, "\t\tip daddr { %s } accept\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "\t\tip6 daddr { %s } accept\n", strings.Join(privateV6, ", "))
	}
	ruleAllow(&b, spec, true)
	b.WriteString("\t\tdrop\n\t}\n")
	if spec.Gateway && spec.TunName != "" {
		// 内核刚被杀的那一小会儿:sing-tun 的规则还在、隧道网卡已经没了,打了标记的包查不到隧道的路由,会从物理网卡出去
		b.WriteString("\tchain postrouting {\n\t\ttype filter hook postrouting priority filter; policy accept;\n")
		fmt.Fprintf(&b, "\t\tmeta mark 0x%x oifname != %q drop\n", tun.DefaultAutoRedirectInputMark, spec.TunName)
		fmt.Fprintf(&b, "\t\tip daddr %s oifname != %q drop\n", builder.TunAddr4, spec.TunName)
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// ruleAllow 规则模式放行直连的那几条(全局模式什么都不写)。forward = 转发链,多一层局域网设备的策略:
// 设成走代理 / 拒绝的设备不看放行段,设成直连的不看规则。
func ruleAllow(b *strings.Builder, spec GuardSpec, forward bool) {
	if !spec.Rule {
		return
	}
	if forward {
		deviceMatch(b, splitList(spec.DevBlock), "drop")
		deviceMatch(b, splitList(spec.DevDirect), "accept")
	}
	if ports := splitList(spec.TunnelPorts); len(ports) > 0 {
		list := strings.Join(ports, ", ")
		fmt.Fprintf(b, "\t\ttcp dport { %s } drop\n\t\tudp dport { %s } drop\n", list, list)
	}
	// systemd-resolved 把名字以网卡搜索域结尾的查询绑在物理网卡上发,不进隧道:查不到的域名、只查 IPv6 时,系统会再试
	// 「www.x.com.<搜索域>」,这个名字就明文发给了网卡配的解析器。这里丢掉它的 UDP,它会立刻改走 TCP,TCP 不绑网卡:
	// 隧道在时进隧道由内核回答(DNS 的策略路由),隧道不在时由下面那条放行、发给网卡配的解析器。局域网里的解析器在上面就放行了,
	// nas.lan 这种名字本来就该问它。网关模式不管:路由器自己的解析另有一条路。
	if uid := resolvedUID(); uid >= 0 && !forward && !spec.Gateway {
		fmt.Fprintf(b, "\t\tmeta skuid %d udp dport 53 drop\n", uid)
	}
	// DNS 不看目的地址:系统配的解析器不一定在放行段里(8.8.8.8、云厂商的解析器),拦了隧道一断直连的网站就解析不了。
	b.WriteString("\t\ttcp dport 53 accept\n\t\tudp dport 53 accept\n")
	v4, v6 := spec.directFamilies()
	if len(v4) > 0 {
		b.WriteString("\t\tip daddr @direct4 accept\n")
	}
	if len(v6) > 0 {
		b.WriteString("\t\tip6 daddr @direct6 accept\n")
	}
}

// resolvedUID systemd-resolved 的用户;没有这个用户返回 -1。写进规则的是数字:nft 遇到不存在的用户名整段规则都加载不了。
var resolvedUID = func() int {
	u, err := user.Lookup("systemd-resolve")
	if err != nil {
		return -1
	}
	id, err := strconv.Atoi(u.Uid)
	if err != nil {
		return -1
	}
	return id
}

// deviceMatch 按来源匹配设备:有 MAC 按 MAC,手改设置文件才有的只有 IP 的按 IP。
func deviceMatch(b *strings.Builder, who []string, verdict string) {
	var macs, ips []string
	for _, w := range who {
		if strings.Contains(w, ":") && len(w) == 17 {
			macs = append(macs, w)
		} else if !strings.Contains(w, ":") {
			ips = append(ips, w)
		}
	}
	if len(macs) > 0 {
		fmt.Fprintf(b, "\t\tether saddr { %s } %s\n", strings.Join(macs, ", "), verdict)
	}
	if len(ips) > 0 {
		fmt.Fprintf(b, "\t\tip saddr { %s } %s\n", strings.Join(ips, ", "), verdict)
	}
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
// UPnP 发现(SSDP,UDP 1900)与 NAT-PMP / PCP(UDP 5351)—— 本机程序靠后两样能向路由器问到宽带的真实公网地址;
// 还有 v6 假地址段(见 fakeDrop)。
func blockLeaks(b *strings.Builder, dns bool) {
	if dns {
		b.WriteString(dnsDrop)
	}
	b.WriteString(upnpDrop)
	b.WriteString(fakeDrop)
}

const (
	dnsDrop  = "\t\ttcp dport { 53, 853 } drop\n\t\tudp dport { 53, 853 } drop\n"
	upnpDrop = "\t\tudp dport { 1900, 5351 } drop\n"
)

// fakeDrop 发往 v6 假地址段的包:它落在局域网放行的 fc00::/7 里,隧道断开的空档里应用还往记下的假地址发包,
// 会被当成局域网流量发给路由器(假地址没有真实目的地,内容出不去,但包不该离开本机)。经隧道的在上面已放行。
var fakeDrop = "\t\tip6 daddr " + builder.FakeIP6 + " drop\n"

func ApplyGuard(spec GuardSpec) error {
	nftMu.Lock()
	defer nftMu.Unlock()
	// 规则模式同样把 DNS 接进隧道:「局域网直通」把私网段排除在隧道外,resolv.conf 写路由器的系统,连着时所有域名都明文问路由器。
	// 内核自己问系统解析器(本地 DNS 填 system)时套接字绑着物理网卡,表里那条按隧道网卡走的路由对它不生效,不会绕回自己。
	// 网关模式不用:路由器自己的解析由 auto_redirect 接住。
	dns, why := false, ""
	if spec.Rule && spec.Gateway {
		unsealDNS()
	} else {
		dns, why = sealDNS()
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(guardRuleset(spec, dns))
	if out, err := cmd.CombinedOutput(); err != nil {
		// nft -f 是整段事务:失败时旧的闸(如果有)原样留着,DNS 的策略路由也跟着留着,不动它
		return nftError("nft 加载规则", err, out)
	}
	dnsSealed = dns
	guardWarn = why
	// 开机版落盘(这也是闸的备份):nft 的表不过重启,开机到服务起来之间由开机单元先装上它(见 BootGuardFile)。
	// 全局模式拦 DNS 那几条总是带上:开机时没有隧道,DNS 本来就不该出去
	if err := writeAtomic(BootGuardFile(), []byte(guardRuleset(spec, true)), 0o600); err != nil {
		if guardWarn != "" {
			guardWarn += ";"
		}
		guardWarn += "开机闸没落盘(" + err.Error() + "):重启后到服务起来之前没有闸"
	}
	return nil
}

// BootGuardFile 开机版的闸。闸装上时写、撤闸时删(手动断开、换模式、改设置、卸载才撤闸;崩溃、断电不会),
// 开机时早于联网的单元(godusevpn-guard,见 svc)看到它就先装上;服务起来后自己的 ApplyGuard 用同名表
// 整表替换(nft 事务),中间没有空档。
func BootGuardFile() string { return filepath.Join(paths.DataDir(), "guard-boot.nft") }

// ApplyBootGuard 开机单元调:有开机版的闸就装上;网卡 IPv6 该关着(有备份)就把此刻已经在的网卡先停掉 v6,
// 并挡住路由器通告(见 bootRARuleset)。
func ApplyBootGuard() error {
	nftMu.Lock()
	defer nftMu.Unlock()
	var errs []string
	if NICIPv6Off() {
		BootNICIPv6()
		cmd := exec.Command("nft", "-f", "-")
		cmd.Stdin = strings.NewReader(bootRARuleset)
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, nftError("nft 挡路由器通告", err, out).Error())
		}
	}
	if _, err := os.Stat(BootGuardFile()); err == nil {
		if out, err := exec.Command("nft", "-f", BootGuardFile()).CombinedOutput(); err != nil {
			errs = append(errs, nftError("nft 装开机闸", err, out).Error())
			// 落盘的那份读不了(文件坏了):按最严的装,不留空档。服务起来后按当前设置整表换掉
			cmd := exec.Command("nft", "-f", "-")
			cmd.Stdin = strings.NewReader(guardRuleset(strictFallback(), true))
			if out, err := cmd.CombinedOutput(); err != nil {
				errs = append(errs, nftError("nft 装最严的闸", err, out).Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// bootRATable 开机时网卡 IPv6 该关着,却会被 systemd-networkd / NetworkManager 配置网卡时改回开(disable_ipv6=0);
// 到守护进程起来重新停用之前这几秒,路由器通告一到,网卡就拿到公网 v6 地址。开机单元先把通告挡在门外,
// 守护进程把网卡重新停用之后撤掉(DropBootRA);停用没做成就一直挡着。
const bootRATable = "godusevpn_boot_ra"

var bootRARuleset = "table inet " + bootRATable + " {}\ndelete table inet " + bootRATable + "\ntable inet " + bootRATable + " {\n" +
	"\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n\t\ticmpv6 type nd-router-advert drop\n\t}\n}\n"

// DropBootRA 撤掉开机时挡路由器通告的表(没有也无妨)。
func DropBootRA() { _ = exec.Command("nft", "delete", "table", "inet", bootRATable).Run() }

// strictFallback 开机版的闸读不了时用的规格:隧道网卡名是定的,局域网放行(不然连路由器都连不上,没法排障)。
func strictFallback() GuardSpec { return GuardSpec{TunName: builder.TunName, LAN: true} }

// GuardPresent 闸的表还在不在。只看一条链:表里的放行段有几千条,这个每十几秒查一次,不该每次都把它们打出来。
// 查不了(没有 nft)不算不在:那样谈不上"被清掉"。
func GuardPresent() bool {
	out, err := exec.Command("nft", "list", "chain", "inet", guardTable, "output").CombinedOutput()
	return err == nil || !nftMissing(out, err) || errors.Is(err, exec.ErrNotFound)
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

// GuardTunDown nft 按网卡名和地址放行,隧道网卡没了规则自然对不上,没有要撤的。
func GuardTunDown() error { return nil }

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
	// 开机版也删:不然下次开机闸又回来了(那时服务对账会再撤,但开机到服务起来之间白断一阵网)
	if err := os.Remove(BootGuardFile()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除开机闸 %s: %w", BootGuardFile(), err)
	}
	DropBootRA() // 守护进程没起来过就卸载 / 恢复网络时,开机挡路由器通告的表也一起撤
	unsealDNS()  // 闸没了,DNS 的策略路由也撤:留着也不漏(隧道不在时落回主表),只是没用了
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
		return fmt.Errorf("%s: 系统里没有 nft 命令,装不上防流量逃逸的闸;先装 nftables(OpenWrt: opkg install nftables,Debian / Ubuntu: apt install nftables)", what)
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
		for i := 0; i < 16; i++ { // 同一条可能被装过不止一次(老内核不去重),删到没有为止
			if netlink.RuleDel(dnsRule(fam)) != nil {
				break
			}
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

// SealTunDNS 连着时把 53 端口接进隧道,和闸无关:没开防流量逃逸也一样,连着的时候 DNS 不该直接问路由器。
// 路由跟着隧道网卡走,网卡一没表就空了,DNS 落回主表,所以崩溃时留下这条规则也不碍事。
func SealTunDNS(tunName string) error {
	nftMu.Lock()
	dns, why := sealDNS()
	if dns {
		dnsSealed = true
	}
	nftMu.Unlock()
	if !dns {
		return errors.New(why)
	}
	return routeDNSToTun(tunName)
}

// UnsealTunDNS 撤掉 SealTunDNS 装的。闸还在就不动:那边的规格管着它。
func UnsealTunDNS() {
	if n, err := GuardStatus(); err != nil || n > 0 {
		return
	}
	nftMu.Lock()
	defer nftMu.Unlock()
	unsealDNS()
	dnsSealed = false
}

// LANResolvers 系统配的解析器里在局域网内的那些(通常是路由器),内核拿它们回答 nas.lan 这种局域网名字。
// 有 systemd-resolved 时 /etc/resolv.conf 只写着 127.0.0.53,真正的上游在 /run/systemd/resolve/resolv.conf。
func LANResolvers() []string {
	for _, p := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		if b, err := os.ReadFile(p); err == nil {
			if out := lanResolvers(string(b)); len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func lanResolvers(conf string) []string {
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		if ip := net.ParseIP(f[1]); ip != nil && ip.To4() != nil && ip.IsPrivate() {
			out = append(out, f[1])
		}
	}
	return out
}

// GuardWarning 闸装上了、但没做全的那部分(目前只有"DNS 接不进隧道所以没拦")。
func GuardWarning() string {
	nftMu.Lock()
	defer nftMu.Unlock()
	return guardWarn
}

// BootGuardReady 只有 Windows 拿它核查开机那组过滤器。Linux 的 nft 表不过重启,开机到服务起来之间由
// godusevpn-guard 单元按 BootGuardFile 先装上(OpenWrt 是同名的 init 脚本);Entware(梅林)的 /opt 挂得比联网晚,做不到。
func BootGuardReady() (bool, error) { return false, nil }

// GuardInstallable 这个平台的闸是不是由我们自己装、并且装完能核查。
// Windows(WFP)、Linux(nftables)、macOS(pf)都是;Android 不是 —— 那边的闸就是宿主
// VpnService 的接口本身,由系统持有,我们既装不了也数不出条数。对这种平台做"闸装没装"的
// 硬核查只会把连接整个挡死,而用户挡不住就会去把「防流量逃逸」关掉,反倒更不私密。
func GuardInstallable() bool { return true }

// GuardPersistentSupported Linux 没有 Windows 那种由系统保管、能核查装没装全的持久组(开机那段见 BootGuardFile)。
func GuardPersistentSupported() bool      { return false }
func GuardPersistentReady() (bool, error) { return false, nil }

// GuardBootDisabled 只有 Windows 的 WFP 有"开机时被系统停用"这回事。
func GuardBootDisabled() (bool, error) { return false, nil }
