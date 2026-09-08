//go:build !windows

package netmode

import (
	"fmt"
	"os/exec"
	"strings"
)

// 网关模式下的额外防火墙动作(nftables)。
//
// 经本机转发的局域网流量,sing-box 的 auto_route(策略路由 "not iif lo lookup 2022")本来就会送进 TUN,不需要我们再做 NAT;
// 唯一进不了 TUN 的是设备发给路由器自己 53 端口的 DNS 查询(那是本机 INPUT,不是转发),
// 这里在 prerouting 把它们 DNAT 到一个必然经 TUN 的地址(192.0.2.53,文档保留段,公网不路由),内核里 "port 53 → hijack-dns" 就接管了。
// dnsmasq 照常做 DHCP,只是不再答 DNS。

const (
	nftTable = "godusevpn"
	// DNSHijackAddr 劫持目标:任何经默认路由(即 TUN)的地址都行,选文档保留段避免撞上真实主机
	DNSHijackAddr = "192.0.2.53"
)

// ApplyGateway 装上 LAN DNS 劫持规则;lanIfaces 为空表示所有非 TUN 网卡。可重复调用。
func ApplyGateway(tunName string, lanIfaces []string, dnsHijack bool) error {
	ClearGateway()
	if !dnsHijack {
		return nil
	}
	match := fmt.Sprintf(`iifname != "%s"`, tunName)
	if len(lanIfaces) > 0 {
		q := make([]string, len(lanIfaces))
		for i, n := range lanIfaces {
			q[i] = `"` + n + `"`
		}
		match = "iifname { " + strings.Join(q, ", ") + " }"
	}
	script := fmt.Sprintf(`table inet %[1]s {
	chain dns_prerouting {
		type nat hook prerouting priority dstnat - 1; policy accept;
		%[2]s meta l4proto { tcp, udp } th dport 53 fib daddr type local dnat ip to %[3]s:53
	}
}
`, nftTable, match, DNSHijackAddr)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ClearGateway 删掉本程序的 nft 表(不存在也无妨)。
func ClearGateway() {
	_ = exec.Command("nft", "delete", "table", "inet", nftTable).Run()
}
