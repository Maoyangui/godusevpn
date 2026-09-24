//go:build darwin

package netmode

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// macOS 用 pf。规则放在 com.apple/godusevpn 锚点下:系统自带的 /etc/pf.conf 里有一行 anchor "com.apple/*",
// 挂在它下面的锚点会被评估,不用改系统的 pf.conf。pf 本身可能没开,-E 按引用计数打开,撤闸时 -X 还回去。
//
// pf 只能按用户放行、不能按进程:守护进程以 root 跑,所以放行 root —— root 下别的进程也跟着放行,比 Windows 粗一点。
const pfAnchor = "com.apple/godusevpn"

var (
	pfMu    sync.Mutex
	pfToken string
	pfOn    bool
)

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
	// 拦 DNS(53 / 853):必须在局域网放行前面。隧道断开时系统的 DNS 查询要是能发给路由器(私网地址),
	// 就经路由器出了隧道。回环、root(守护进程)、隧道地址上的 DNS 都在上面放行了。
	b.WriteString(dnsBlockRule)
	if spec.LAN {
		fmt.Fprintf(&b, "pass out quick to { %s }\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "pass out quick to { %s }\n", strings.Join(privateV6, ", "))
	}
	b.WriteString("pass out quick proto udp from any port 68 to any port 67\n") // DHCP 续租
	b.WriteString("pass out quick inet6 proto icmp6\n")                         // 邻居发现
	b.WriteString("block drop out quick all\n")
	return b.String()
}

// dnsBlockRule pf 里拦 DNS 的那一条。
const dnsBlockRule = "block drop out quick proto { tcp, udp } from any to any port { 53, 853 }\n"

var pfTokenRe = regexp.MustCompile(`Token\s*:\s*(\d+)`)

func ApplyGuard(spec GuardSpec) error {
	pfMu.Lock()
	defer pfMu.Unlock()
	if !pfOn {
		out, _ := exec.Command("pfctl", "-E").CombinedOutput()
		if m := pfTokenRe.FindStringSubmatch(string(out)); m != nil {
			pfToken = m[1]
		}
	}
	cmd := exec.Command("pfctl", "-a", pfAnchor, "-f", "-")
	cmd.Stdin = strings.NewReader(guardRules(spec))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl 加载规则: %v: %s", err, strings.TrimSpace(string(out)))
	}
	pfOn = true
	return nil
}

// GuardTunUp pf 按地址放行,隧道网卡起不起来无所谓。
func GuardTunUp(GuardSpec) error { return nil }

// ClearGuard 撤闸。规则不随进程死(上次强杀留下的也要清),所以不管本进程有没有装过,锚点一律清空。
// 清完锚点里还有规则才算失败。
func ClearGuard() error {
	pfMu.Lock()
	defer pfMu.Unlock()
	if out, err := exec.Command("pfctl", "-a", pfAnchor, "-F", "all").CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl 清锚点: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if pfToken != "" {
		_ = exec.Command("pfctl", "-X", pfToken).Run()
		pfToken = ""
	}
	pfOn = false
	return nil
}

// GuardStatus 锚点里现在有多少条规则;0 = 没开。
func GuardStatus() (int, error) {
	out, err := exec.Command("pfctl", "-a", pfAnchor, "-sr").Output()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n, nil
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
