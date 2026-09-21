//go:build linux && !android

package netmode

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Linux 用 nftables:一张 inet 表,output 链放行该放的、其余丢掉;网关模式再加一条 forward 链,
// 经本机转发的局域网流量只许走隧道。nft 只能按 uid 放行、不能按进程:守护进程以 root 跑,
// 所以放行 uid 0 —— root 下别的进程也跟着放行,比 Windows 粗一点。
const guardTable = "godusevpn_guard"

var (
	nftMu sync.Mutex
	nftOn bool
)

// guardRuleset nft 脚本。先建再删再建:表不存在时 delete 会报错,先 add 一张空的就不会了,整段幂等。
func guardRuleset(spec GuardSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {}\n", guardTable)
	fmt.Fprintf(&b, "delete table inet %s\n", guardTable)
	fmt.Fprintf(&b, "table inet %s {\n", guardTable)
	b.WriteString("\tchain output {\n\t\ttype filter hook output priority filter; policy accept;\n")
	b.WriteString("\t\toifname \"lo\" accept\n")
	if spec.TunName != "" {
		fmt.Fprintf(&b, "\t\toifname %q accept\n", spec.TunName) // 经隧道出去的流量
	}
	if spec.TunAddr4 != "" {
		fmt.Fprintf(&b, "\t\tip saddr %s accept\n", spec.TunAddr4)
	}
	if spec.TunAddr6 != "" {
		fmt.Fprintf(&b, "\t\tip6 saddr %s accept\n", spec.TunAddr6)
	}
	b.WriteString("\t\tmeta skuid 0 accept\n") // 守护进程(内核在它里面):节点连接、订阅刷新的回退直连
	if spec.LAN {
		fmt.Fprintf(&b, "\t\tip daddr { %s } accept\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "\t\tip6 daddr { %s } accept\n", strings.Join(privateV6, ", "))
	}
	// 只放行 DHCP 客户端的源端口，并限制到广播目的，避免把任意 UDP/67 当成直连例外。
	b.WriteString("\t\tudp sport 68 udp dport 67 ip daddr 255.255.255.255 accept\n") // DHCP 续租
	b.WriteString("\t\ticmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-solicit, nd-router-advert } accept\n")
	b.WriteString("\t\tdrop\n\t}\n")
	if spec.Gateway {
		b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
		if spec.TunName != "" {
			fmt.Fprintf(&b, "\t\toifname %q accept\n", spec.TunName)
		}
		if spec.LAN {
			fmt.Fprintf(&b, "\t\tip daddr { %s } accept\n", strings.Join(privateV4, ", "))
			fmt.Fprintf(&b, "\t\tip6 daddr { %s } accept\n", strings.Join(privateV6, ", "))
		}
		b.WriteString("\t\tdrop\n\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func ApplyGuard(spec GuardSpec) error {
	nftMu.Lock()
	defer nftMu.Unlock()
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(guardRuleset(spec))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft 加载规则: %v: %s", err, strings.TrimSpace(string(out)))
	}
	nftOn = true
	return nil
}

// GuardTunUp nft 按网卡名和地址放行,隧道起不起来无所谓。
func GuardTunUp(GuardSpec) error { return nil }

// ClearGuard 撤闸。表不随进程死(上次强杀留下的也要清),不存在也无妨;删了之后表还在才算失败。
func ClearGuard() error {
	nftMu.Lock()
	defer nftMu.Unlock()
	if out, err := exec.Command("nft", "delete", "table", "inet", guardTable).CombinedOutput(); err != nil && !nftMissing(out) {
		return fmt.Errorf("nft 删除闸: %v: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := exec.Command("nft", "list", "table", "inet", guardTable).CombinedOutput()
	if err == nil {
		return fmt.Errorf("nft 表 %s 删不掉,闸还在", guardTable)
	}
	if !nftMissing(out) {
		return fmt.Errorf("nft 确认闸状态失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	nftOn = false
	return nil
}

// GuardStatus 表里现在有多少条规则;0 = 没开。
func GuardStatus() (int, error) {
	out, err := exec.Command("nft", "list", "table", "inet", guardTable).CombinedOutput()
	if err != nil {
		if nftMissing(out) {
			return 0, nil // 表不存在
		}
		return 0, fmt.Errorf("nft 查询闸状态: %v: %s", err, strings.TrimSpace(string(out)))
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

func nftMissing(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "no such file") || strings.Contains(s, "does not exist") || strings.Contains(s, "not found")
}

// GuardWarning Linux 一步装完,没有"装了一半"的情况。
func GuardWarning() string { return "" }

// Linux nftables rules are runtime state.  They are not restored before the
// network stack can emit traffic after a reboot, so strict global mode must
// refuse to start until a boot-time firewall integration is installed.
func BootGuardReady() (bool, error) { return false, nil }

// Linux currently has no daemon-independent boot-time guard.
func GuardPersistentSupported() bool      { return false }
func GuardPersistentReady() (bool, error) { return false, nil }
