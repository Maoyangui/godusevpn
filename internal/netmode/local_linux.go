//go:build !windows && !android && !darwin

// Package netmode Linux 上 TUN 之外还要做的路由动作。
//
// 本机模式(auto_route)有个坑:本机上对外提供服务的进程(sshd、面板、RDP 这类)给远端的回包,源地址是物理网卡的 IP,
// 但默认路由已经指向 TUN,回包会被内核当成陌生连接的包丢掉,结果就是连上代理的一瞬间 SSH 全断。
// 处理:给每个物理网卡地址加一条策略路由 "from <地址> lookup main"(优先级高于 sing-box 的规则),
// 源地址已经绑定在物理网卡上的流量走原来的路;普通应用发起的连接源地址未定,仍走 TUN 被代理。
package netmode

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// 规则优先级:要比 sing-box auto_route 的 9000 段小
const rulePref = "5000"

// Protect 给本机所有非 TUN 的全局地址加"回包走主表"规则。可重复调用(先清再加)。
func Protect(tunName string, ipv6 bool) error {
	Unprotect()
	var errs []string
	for _, fam := range families(ipv6) {
		for _, addr := range localAddrs(fam, tunName) {
			if out, err := exec.Command("ip", fam, "rule", "add", "from", addr, "lookup", "main", "pref", rulePref).CombinedOutput(); err != nil {
				errs = append(errs, fmt.Sprintf("ip %s rule add from %s: %s", fam, addr, strings.TrimSpace(string(out))))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Unprotect 删掉 Protect 加的规则(按优先级反复删到没有为止)。
func Unprotect() {
	for _, fam := range []string{"-4", "-6"} {
		for i := 0; i < 64; i++ {
			if err := exec.Command("ip", fam, "rule", "del", "pref", rulePref).Run(); err != nil {
				break
			}
		}
	}
}

func families(ipv6 bool) []string {
	if ipv6 {
		return []string{"-4", "-6"}
	}
	return []string{"-4"}
}

// localAddrs `ip -4/-6 -o addr show scope global` 里除 TUN 外的地址(带前缀长度去掉,只取地址本身)。
func localAddrs(fam, tunName string) []string {
	out, err := exec.Command("ip", fam, "-o", "addr", "show", "scope", "global").Output()
	if err != nil {
		return nil
	}
	var addrs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// 形如: 2: eth0    inet 172.16.0.4/24 metric 100 brd ... scope global eth0
		if len(f) < 4 || f[1] == tunName {
			continue
		}
		addr := f[3]
		if i := strings.IndexByte(addr, '/'); i > 0 {
			addr = addr[:i]
		}
		if fam == "-6" && strings.HasPrefix(addr, "fe80") {
			continue
		}
		addrs = append(addrs, addr)
	}
	return addrs
}
