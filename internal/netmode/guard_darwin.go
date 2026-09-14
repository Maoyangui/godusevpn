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
	if spec.LAN {
		fmt.Fprintf(&b, "pass out quick to { %s }\n", strings.Join(privateV4, ", "))
		fmt.Fprintf(&b, "pass out quick to { %s }\n", strings.Join(privateV6, ", "))
	}
	b.WriteString("pass out quick proto udp from any port 68 to any port 67\n") // DHCP 续租
	b.WriteString("pass out quick inet6 proto icmp6\n")                         // 邻居发现
	b.WriteString("block drop out quick all\n")
	return b.String()
}

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
func ClearGuard() {
	pfMu.Lock()
	defer pfMu.Unlock()
	_ = exec.Command("pfctl", "-a", pfAnchor, "-F", "all").Run()
	if pfToken != "" {
		_ = exec.Command("pfctl", "-X", pfToken).Run()
		pfToken = ""
	}
	pfOn = false
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
