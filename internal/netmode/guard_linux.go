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
	b.WriteString("\t\tudp dport 67 accept\n") // DHCP 续租
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

// ClearGuard 撤闸。表不随进程死(上次强杀留下的也要清),不存在也无妨。
func ClearGuard() {
	nftMu.Lock()
	defer nftMu.Unlock()
	_ = exec.Command("nft", "delete", "table", "inet", guardTable).Run()
	nftOn = false
}

// GuardStatus 表里现在有多少条规则;0 = 没开。
func GuardStatus() (int, error) {
	out, err := exec.Command("nft", "list", "table", "inet", guardTable).Output()
	if err != nil {
		return 0, nil // 表不存在
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

// GuardWarning Linux 一步装完,没有"装了一半"的情况。
func GuardWarning() string { return "" }
