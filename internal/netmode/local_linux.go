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
	// 清不掉旧规则不该挡住连接:留下来的是 "from <旧地址> lookup main",那个地址要是没了,规则就是条死规则,
	// 不构成泄漏;而下面要加的新规则才是本机服务不断线的关键。m29 把它改成"清理失败即拒绝连接",
	// 于是在没编 IPv6 的内核上(ip -6 rule list 直接非零退出)整台机器都连不上。
	// 真正需要知道"有没有清干净"的是断开那条路径,那边调的是 UnprotectChecked,错误照旧往上报。
	if err := UnprotectChecked(); err != nil {
		setRouteWarning("清理旧的回包路由规则没做干净(不影响出网): " + err.Error())
	} else {
		setRouteWarning("")
	}
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

// UnprotectChecked 删掉 Protect 加的规则(按优先级反复删到没有为止)。
// 查询失败或删除失败都返回错误，调用方不能把未知状态当成已恢复。
func UnprotectChecked() error {
	var errs []string
	for _, fam := range []string{"-4", "-6"} {
		if !ruleFamilyUsable(fam) {
			// 这个地址族上问不出规则:内核没编 IPv6(ip -6 直接非零退出),或者 busybox 的 ip 不认
			// "rule list pref"。两种情况下 Protect 那边同样加不进规则(localAddrs 也拿不到地址),
			// 所以这里没有我们的规则可删。跳过它,别拿它把连接挡死 —— m29 把这一处非零退出当成硬错误,
			// 于是没编 IPv6 的软路由在默认设置下完全连不上。
			continue
		}
		if err := delRulesAt(fam); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ruleFamilyUsable 这个地址族上 "ip rule list pref" 问得通吗。
func ruleFamilyUsable(fam string) bool {
	return exec.Command("ip", fam, "rule", "list", "pref", rulePref).Run() == nil
}

// delRulesAt 把该优先级上的规则删光:每删一条重新查一次,查到空为止。
// 上限只是防死循环用的,取得足够大 —— 规则条数等于本机全局地址数,带一整个 v6 段的 VPS 或者
// 挂了一堆别名的网关很容易超过几十条。m29 把上限定在 64,超了就报"超过最大重试次数"并永久失败。
func delRulesAt(fam string) error {
	const maxRules = 4096
	for i := 0; i < maxRules; i++ {
		out, err := exec.Command("ip", fam, "rule", "list", "pref", rulePref).CombinedOutput()
		if err != nil {
			return fmt.Errorf("ip %s rule list pref %s: %s", fam, rulePref, strings.TrimSpace(string(out)))
		}
		if len(strings.TrimSpace(string(out))) == 0 {
			return nil
		}
		if out, err := exec.Command("ip", fam, "rule", "del", "pref", rulePref).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %s rule del pref %s: %s", fam, rulePref, strings.TrimSpace(string(out)))
		}
	}
	return fmt.Errorf("ip %s rule del pref %s: 删了 %d 次还没删完", fam, rulePref, maxRules)
}

func Unprotect() { _ = UnprotectChecked() }

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
