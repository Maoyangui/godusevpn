//go:build linux && !android

package netmode

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/paths"
)

// 网关模式下的防火墙:sing-box 的 auto_redirect 自己用 nftables 把经本机转发的 TCP / UDP 收进来,发往公网地址 53 端口的
// DNS 查询也由它 DNAT 到 TUN 地址(table inet sing-box 的 prerouting 链)。早期版本自己建过一张 inet godusevpn 表做 DNS 劫持,
// 抢在它前面把查询改坏了,这里负责清掉残留。
//
// 发给路由器自己 53 端口的不归它管:「局域网直通」把私网段列进了 route_exclude_address,这条排除排在它的 DNS 劫持前面。
// 路由器上有 DNS 服务(OpenWrt 的 dnsmasq)时正好,查询到 dnsmasq,dnsmasq 再问上游时被接住。没有的话(Debian、Ubuntu 搭的旁路由),
// 设备按说明把 DNS 指向这台机器就是连接被拒,什么都解析不了 —— 这种情况见 gatewayDNSRules。
// 内核被杀或崩溃时它的转发重定向也留着,局域网设备一条连接都建不起来,见 ClearStaleRedirect。
//
// IP 转发要我们管:路由系统(OpenWrt 等)本来开着,普通 Linux(Debian、Ubuntu 上搭的旁路由)默认关着。关着时 TCP 还能用
// (auto_redirect 把它改成发给本机),UDP 要经转发进隧道就全丢了 —— 设备上表现为网页能开,QUIC、语音、游戏都不通。

const nftTable = "godusevpn"

// gatewayDNSTable 路由器自己没有 DNS 服务时接住设备 DNS 的表。
const gatewayDNSTable = "godusevpn_dns"

// fallbackDNS 设置里的本地 DNS 不是公网 IPv4 地址("system"、域名、路由器地址)时用它。只有隧道不在时才真的发给它。
const fallbackDNS = "223.5.5.5"

// lanSources 算局域网设备的来源地址。
const lanSources = "10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10"

// singboxTable sing-tun 的 auto_redirect 用的表,见 ClearStaleRedirect。
const singboxTable = "sing-box"

const forwardSysctl = "/proc/sys/net/ipv4/ip_forward"

// forwardMark 记下"转发是我们打开的":内容是开机号,重启后转发早回到了系统配置,旧记录就作废,免得把用户后来自己开的关掉。
func forwardMark() string { return filepath.Join(paths.DataDir(), "ip-forward-on") }

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// ApplyGateway 内核起来之后:打开 IP 转发,清理旧版本留下的表,设备发给这台机器的 DNS 交给隧道。
// 第二个参数保留给以后的 TProxy 模式。
func ApplyGateway(tunName string, _ []string, _ string) error {
	ClearGateway()
	if err := enableForward(forwardSysctl, forwardMark()); err != nil {
		return err
	}
	return applyGatewayDNS(tunName, tunDNSAddr())
}

// GatewayDown 还想当网关、但隧道不在(服务刚起、内核崩了在重试、节点连不上):转发照常开着,设备的 DNS 改问公共 DNS。
// 规则模式下这个地址在直连段里就放行,设备照常能解析、能上直连的网站;严格全局下闸拦下,不会有查询跑到隧道外面。
func GatewayDown(tunName, localDNS string) error {
	if err := enableForward(forwardSysctl, forwardMark()); err != nil {
		return err
	}
	return applyGatewayDNS(tunName, resolverFor(localDNS))
}

// PostStop 服务进程没了之后由服务单元调(systemd 的 ExecStopPost,不管进程是正常退出、被杀还是崩了):
// 清掉内核来不及清的转发重定向;设备 DNS 的表还在说明还想连着,改问公共 DNS。
func PostStop(tunName, localDNS string) {
	ClearStaleRedirect()
	if exec.Command("nft", "list", "table", "inet", gatewayDNSTable).Run() == nil {
		_ = applyGatewayDNS(tunName, resolverFor(localDNS))
	}
}

// gatewayDNSRules 局域网设备发给这台机器 53 端口的 DNS 改发给 target。
//
// 隧道在时 target 是隧道里的 DNS 地址,内核接住,照常 fake-ip、防泄漏;隧道不在时是公共 DNS,从物理网卡出去,再过闸的转发链。
// 这个切换只能靠我们:防火墙看不出隧道在不在。
//
// 只管局域网来的:路由器 WAN 口是公网地址时,不能让外面的人把它当开放 DNS 用。
// 回包要经这台机器才改得回来(旁路由的回包默认不经过它),所以走物理网卡时要做一次源地址转换。
// 优先级排在 sing-tun 的链前面:它对发往私网地址的包什么都不做,但一条连接只做一次目标地址转换,谁先谁定。
func gatewayDNSRules(tunName, target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {}\n", gatewayDNSTable)
	fmt.Fprintf(&b, "delete table inet %s\n", gatewayDNSTable)
	fmt.Fprintf(&b, "table inet %s {\n", gatewayDNSTable)
	b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority -101; policy accept;\n")
	fmt.Fprintf(&b, "\t\tiifname != %q fib daddr type local ip saddr { %s } meta l4proto { udp, tcp } th dport 53 dnat ip to %s\n", tunName, lanSources, target)
	b.WriteString("\t}\n")
	b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority 100; policy accept;\n")
	fmt.Fprintf(&b, "\t\toifname != %q ct status dnat ip daddr %s meta l4proto { udp, tcp } th dport 53 masquerade\n", tunName, target)
	b.WriteString("\t}\n}\n")
	return b.String()
}

// tunDNSAddr 隧道里的 DNS 地址:sing-tun 用隧道地址的下一个。
func tunDNSAddr() string {
	p, err := netip.ParsePrefix(builder.TunAddr4)
	if err != nil {
		return strings.Split(builder.TunAddr4, "/")[0]
	}
	return p.Addr().Next().String()
}

func applyGatewayDNS(tunName, target string) error {
	if dnsServedLocally("/proc/net/udp", "/proc/net/udp6") {
		clearGatewayDNS() // dnsmasq 这类自己的 DNS 服务在:设备的查询归它
		return nil
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(gatewayDNSRules(tunName, target))
	if out, err := cmd.CombinedOutput(); err != nil {
		return nftError("nft 加载设备 DNS 规则", err, out)
	}
	return nil
}

func clearGatewayDNS() {
	_ = exec.Command("nft", "delete", "table", "inet", gatewayDNSTable).Run()
}

// resolverFor 隧道不在时问谁。设置里的本地 DNS 是公网 IPv4 才用它(路由器地址、域名、"system" 都用不了)。
func resolverFor(localDNS string) string {
	if ip := net.ParseIP(strings.TrimSpace(localDNS)); ip != nil && ip.To4() != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() {
		return ip.String()
	}
	return fallbackDNS
}

// dnsServedLocally 本机有没有服务在非回环地址上听 UDP 53。systemd-resolved 的 127.0.0.53 不算:局域网设备连不到。
func dnsServedLocally(files ...string) bool {
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			addr, port, ok := strings.Cut(fields[1], ":")
			if !ok || port != "0035" { // 53 的十六进制
				continue
			}
			if !strings.HasSuffix(addr, "7F") && addr != "00000000000000000000000001000000" {
				return true
			}
		}
	}
	return false
}

// ClearStaleRedirect 上一次的内核没走到退出流程就没了(被杀、崩溃):它的 auto_redirect 表还留着,经本机转发的 TCP 全被
// 改发到一个已经没人听的端口,局域网设备连接直接被拒 —— 连闸放行的直连都走不了。服务一起来先清掉;
// 进程死了到服务重启之间的那段,systemd 由单元里的 ExecStopPost 清(见 PostStop)。
func ClearStaleRedirect() {
	_ = exec.Command("nft", "delete", "table", "inet", singboxTable).Run()
}

// ClearGateway 删掉本程序的 nft 表(不存在也无妨)。
func ClearGateway() {
	_ = exec.Command("nft", "delete", "table", "inet", nftTable).Run()
}

// RestoreForward 不再当网关时把转发关回去:只关我们打开的,本来就开着的不动。设备 DNS 的规则一起撤。
// 崩了在重试、服务重启时不调它:那些时候还"想连着",这两样都该留着。
func RestoreForward() {
	restoreForward(forwardSysctl, forwardMark())
	clearGatewayDNS()
}

func enableForward(sysctl, mark string) error {
	b, err := os.ReadFile(sysctl)
	if err != nil {
		return fmt.Errorf("读 IP 转发开关: %w", err)
	}
	if strings.TrimSpace(string(b)) == "1" {
		return nil
	}
	// 先记后改:改完没记上的话,这台机器就永远留着转发了
	if err := os.WriteFile(mark, []byte(bootID()), 0o600); err != nil {
		return fmt.Errorf("记录 IP 转发状态: %w", err)
	}
	if err := os.WriteFile(sysctl, []byte("1\n"), 0o644); err != nil {
		_ = os.Remove(mark)
		return fmt.Errorf("打开 IP 转发(可手动 sysctl -w net.ipv4.ip_forward=1): %w", err)
	}
	return nil
}

func restoreForward(sysctl, mark string) {
	b, err := os.ReadFile(mark)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(b)) == bootID() {
		_ = os.WriteFile(sysctl, []byte("0\n"), 0o644)
	}
	_ = os.Remove(mark)
}
