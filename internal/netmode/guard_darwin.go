//go:build darwin

package netmode

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/paths"
)

// macOS 用 pf。规则放在 com.apple/godusevpn 锚点下:系统自带的 /etc/pf.conf 里有一行 anchor "com.apple/*",
// 挂在它下面的锚点会被评估,不用改系统的 pf.conf。pf 本身可能没开,-E 按引用计数打开,撤闸时 -X 还回去。
//
// pf 只能按用户 / 组放行,不认进程也不认套接字标记(Linux 那边按标记放行本服务):守护进程要 root 才能建 utun、
// 改路由和 pf,内核又跑在它里面,所以只能放行 root —— root 下别的进程也跟着放行,比 Windows / Linux 粗一点。
// 要收窄只能把内核拆成独立的低权限进程,或者按节点地址维护 pf 表(节点域名由内核运行时解析、auto 组要测全部节点,
// 表跟不上就把自己拦死),两样都不是改几条规则的事。
const pfAnchor = "com.apple/godusevpn"

var pfMu sync.Mutex

// guardRules pf 规则文本:从上到下第一条 quick 命中的说了算,最后一条把剩下的全丢。
func guardRules(spec GuardSpec) string {
	var b strings.Builder
	b.WriteString("pass out quick on lo0 all\n")
	b.WriteString("pass out quick user root\n") // 守护进程(内核在它里面):节点连接、订阅刷新的回退直连
	if spec.TunAddr4 != "" {
		fmt.Fprintf(&b, "pass out quick from %s\n", spec.TunAddr4) // 经隧道出去的流量
	}
	if spec.TunAddr6 != "" {
		fmt.Fprintf(&b, "pass out quick from %s\n", spec.TunAddr6)
	}
	// 拦 DNS(53 / 853)与 UPnP 发现 / NAT-PMP:必须在局域网放行前面。隧道断开时系统的 DNS 查询要是能发给路由器(私网地址),
	// 就经路由器出了隧道;后两样能让本机程序向路由器问到宽带的真实公网地址。回环、root(守护进程)、隧道地址上的都在上面放行了。
	b.WriteString(dnsBlockRule)
	b.WriteString(upnpBlockRule)
	// v6 假地址段落在下面局域网放行的 fc00::/7 里:隧道断开时发往它的包不能当局域网放出去(经隧道的上面已放行)
	fmt.Fprintf(&b, "block drop out quick inet6 to %s\n", builder.FakeIP6)
	if spec.LAN {
		fmt.Fprintf(&b, "pass out quick to { %s }\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "pass out quick to { %s }\n", strings.Join(privateV6, ", "))
	}
	// 系统的 DHCP 客户端是 root,上面已经放行;这里只给广播留口子,和 Linux / Windows 一样限定目的地址,
	// 不然任何程序绑个 68 端口就能把 UDP 发到任意地址的 67。邻居发现同理只放那四种类型。
	b.WriteString("pass out quick inet proto udp from any port 68 to 255.255.255.255 port 67\n")
	b.WriteString("pass out quick inet6 proto icmp6 icmp6-type { neighbrsol, neighbradv, routersol, routeradv }\n")
	b.WriteString("block drop out quick all\n")
	return b.String()
}

// dnsBlockRule pf 里拦 DNS 的那一条。
const dnsBlockRule = "block drop out quick proto { tcp, udp } from any to any port { 53, 853 }\n"

// upnpBlockRule 拦 UPnP 发现(SSDP,UDP 1900,含 239.255.255.250 / ff02::c 组播)与 NAT-PMP / PCP(UDP 5351)。
const upnpBlockRule = "block drop out quick proto udp from any to any port { 1900, 5351 }\n"

var pfTokenRe = regexp.MustCompile(`Token\s*:\s*(\d+)`)

// pfTokens pfctl -E 拿到的引用记号,落盘:服务重启、或者由命令行撤闸时,都要拿得到上一个进程的记号才还得回去。
func pfTokens() string { return filepath.Join(paths.DataDir(), "pf-tokens") }

func ApplyGuard(spec GuardSpec) error {
	pfMu.Lock()
	defer pfMu.Unlock()
	cmd := exec.Command("pfctl", "-a", pfAnchor, "-f", "-")
	cmd.Stdin = strings.NewReader(guardRules(spec))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl 加载规则: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return enablePF()
}

// enablePF 确认 pf 真的开着。以前只在本进程第一次装闸时 -E 一次、还不看结果:别的程序 pfctl -d 之后,
// 锚点里的规则还在却不再被评估,重装也不会再打开 pf,首页却一直显示闸在。
func enablePF() error {
	b, _ := os.ReadFile(pfTokens())
	if pfEnabled() && strings.TrimSpace(string(b)) != "" {
		return nil // 开着,而且我们手里有一份引用:不再多拿(每次 -E 都会多一份,撤闸时得一份份还)
	}
	out, err := exec.Command("pfctl", "-E").CombinedOutput()
	if err != nil {
		return fmt.Errorf("pfctl -E 打开 pf: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if m := pfTokenRe.FindStringSubmatch(string(out)); m != nil {
		if f, err := os.OpenFile(pfTokens(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(m[1] + "\n")
			_ = f.Close()
		}
	}
	if !pfEnabled() {
		return errors.New("pfctl -E 之后 pf 仍然没开")
	}
	return nil
}

// GuardTunUp pf 按地址放行,隧道网卡起不起来无所谓。
func GuardTunUp(GuardSpec) error { return nil }
func GuardTunDown() error        { return nil }

// ClearGuard 撤闸。规则不随进程死(上次强杀留下的也要清),所以不管本进程有没有装过,锚点一律清空。
// 清完锚点里还有规则才算失败。
func ClearGuard() error {
	pfMu.Lock()
	defer pfMu.Unlock()
	if out, err := exec.Command("pfctl", "-a", pfAnchor, "-F", "all").CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl 清锚点: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// 把我们拿过的 pf 引用一份份还回去(别的程序拿的不动;还不掉的多半是 pf 已被 -d 过,记号早作废了)
	if b, err := os.ReadFile(pfTokens()); err == nil {
		for _, tok := range strings.Fields(string(b)) {
			_ = exec.Command("pfctl", "-X", tok).Run()
		}
		_ = os.Remove(pfTokens())
	}
	return nil
}

// GuardStatus 锚点里现在有多少条规则;0 = 没开。规则在、但 pf 没开或者主规则集不再挂着 com.apple 锚点,
// 规则就不会被评估,也按 0 算 —— 这样守护进程会重装(重装时重新打开 pf)并如实告警。
func GuardStatus() (int, error) {
	out, err := exec.Command("pfctl", "-a", pfAnchor, "-sr").Output()
	if err != nil {
		return 0, err
	}
	n := countLines(string(out))
	if n == 0 {
		return 0, nil
	}
	info, err := exec.Command("pfctl", "-s", "info").Output()
	if err != nil {
		return 0, fmt.Errorf("pfctl -s info: %w", err)
	}
	main, err := exec.Command("pfctl", "-sr").Output()
	if err != nil {
		return 0, fmt.Errorf("pfctl -sr: %w", err)
	}
	if !pfInfoEnabled(string(info)) || !mainRulesHookAnchor(string(main)) {
		return 0, nil
	}
	return n, nil
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func pfEnabled() bool {
	out, _ := exec.Command("pfctl", "-s", "info").Output()
	return pfInfoEnabled(string(out))
}

// pfInfoEnabled pfctl -s info 的输出里 pf 是不是开着("Status: Enabled for ..." / "Status: Disabled")。
func pfInfoEnabled(out string) bool { return strings.Contains(out, "Status: Enabled") }

// mainRulesHookAnchor 主规则集里有没有一行把我们的锚点挂进来(anchor "com.apple/*" 或直接点名)。
// scrub-anchor 那一行不算:它只管分片重组,不评估过滤规则。
func mainRulesHookAnchor(rules string) bool {
	for _, l := range strings.Split(rules, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, `anchor "com.apple/*"`) || strings.HasPrefix(l, `anchor "`+pfAnchor+`"`) {
			return true
		}
	}
	return false
}

// GuardWarning macOS 一步装完,没有"装了一半"的情况。
func GuardWarning() string { return "" }

// pf anchor rules are runtime state and this application does not own a
// pre-network launchd hook.  Treat boot protection as unavailable rather than
// claiming that a normal runtime anchor covers the reboot window.
func BootGuardReady() (bool, error) { return false, nil }

// GuardInstallable 这个平台的闸是不是由我们自己装、并且装完能核查。
// Windows(WFP)、Linux(nftables)、macOS(pf)都是;Android 不是 —— 那边的闸就是宿主
// VpnService 的接口本身,由系统持有,我们既装不了也数不出条数。对这种平台做"闸装没装"的
// 硬核查只会把连接整个挡死,而用户挡不住就会去把「全局禁直连」关掉,反倒更不私密。
func GuardInstallable() bool { return true }

// pf is installed by the running daemon and has no boot-time anchor guarantee.
func GuardPersistentSupported() bool      { return false }
func GuardPersistentReady() (bool, error) { return false, nil }

// GuardBootDisabled 只有 Windows 的 WFP 有"开机时被系统停用"这回事。
func GuardBootDisabled() (bool, error) { return false, nil }
