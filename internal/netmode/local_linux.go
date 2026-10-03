//go:build linux && !android

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
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/sagernet/netlink"
	"golang.org/x/sys/unix"
)

// 规则优先级:要比 sing-box auto_route 的 9000 段小
const rulePref = 5000

var (
	protectMu   sync.Mutex
	protectedAt string // 上次 Protect 时的地址集合(排好序拼起来);空 = 没在保护
	protectTun  string
	protectV6   bool
)

// Protect 给本机所有非 TUN 的全局地址加"回包走主表"规则。可重复调用(先清再加)。
func Protect(tunName string, ipv6 bool) error {
	protectMu.Lock()
	defer protectMu.Unlock()
	return protect(tunName, ipv6)
}

func protect(tunName string, ipv6 bool) error {
	// 清不掉旧规则不该挡住连接:留下来的是 "from <旧地址> lookup main",那个地址要是没了,规则就是条死规则,
	// 不构成泄漏;而下面要加的新规则才是本机服务不断线的关键。m29 把它改成"清理失败即拒绝连接",
	// 于是在没编 IPv6 的内核上(ip -6 rule list 直接非零退出)整台机器都连不上。
	// 真正需要知道"有没有清干净"的是断开那条路径,那边调的是 UnprotectChecked,错误照旧往上报。
	if err := unprotect(); err != nil {
		setRouteWarning("清理旧的回包路由规则没做干净(不影响出网): " + err.Error())
	} else {
		setRouteWarning("")
	}
	var errs, all []string
	for _, fam := range families(ipv6) {
		for _, addr := range localAddrs(fam, tunName) {
			all = append(all, addr)
			a, err := netip.ParseAddr(addr)
			if err != nil {
				continue
			}
			// 已经有一条一模一样的(上一轮没删掉)就算加上了:再加一次只会报 File exists,别拿它挡住连接
			if err := netlink.RuleAdd(replyRule(a)); err != nil && !errors.Is(err, unix.EEXIST) {
				errs = append(errs, fmt.Sprintf("ip rule add from %s: %v", addr, err))
			}
		}
	}
	protectedAt, protectTun, protectV6 = addrKey(all), tunName, ipv6
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RefreshProtect 连着的时候地址变了(PPPoE 重拨、DHCP 换了地址)就按新地址重做一遍:
// 新地址没有规则的话,经 WAN 进来的管理连接(sshd、面板)的回包会被吞进隧道;旧地址的规则则成了死规则。
// 没在保护(没连着、或者已经断开还原了)就什么都不做。返回这一轮有没有重做。
func RefreshProtect() (bool, error) {
	protectMu.Lock()
	defer protectMu.Unlock()
	if protectedAt == "" {
		return false, nil
	}
	var now []string
	for _, fam := range families(protectV6) {
		now = append(now, localAddrs(fam, protectTun)...)
	}
	if addrKey(now) == protectedAt {
		return false, nil
	}
	return true, protect(protectTun, protectV6)
}

func addrKey(addrs []string) string {
	s := append([]string(nil), addrs...)
	sort.Strings(s)
	return "|" + strings.Join(s, "|")
}

// replyRule "from <地址> lookup main pref 5000"。
func replyRule(a netip.Addr) *netlink.Rule {
	r := netlink.NewRule()
	r.Family = unix.AF_INET
	if a.Is6() {
		r.Family = unix.AF_INET6
	}
	r.Priority = rulePref
	r.Table = unix.RT_TABLE_MAIN
	r.Src = netip.PrefixFrom(a, a.BitLen())
	return r
}

// isReplyRule 这条规则是不是 Protect 加的那种:优先级 5000、查主表、只按单个源地址匹配,别的条件一概没有。
// 删的时候只认这种 —— 以前按优先级整段删,别人放在 5000 的规则也被一起删光。
func isReplyRule(r netlink.Rule) bool {
	return r.Priority == rulePref && r.Table == unix.RT_TABLE_MAIN && !r.Invert &&
		r.Src.IsValid() && r.Src.Bits() == r.Src.Addr().BitLen() && !r.Dst.IsValid() &&
		r.IifName == "" && r.OifName == "" && r.Mark == 0 && r.Goto < 0 &&
		r.Dport == nil && r.Sport == nil && r.IPProto == 0 && r.UIDRange == nil
}

// UnprotectChecked 删掉 Protect 加的规则。
// 查询失败或删除失败都返回错误，调用方不能把未知状态当成已恢复。
func UnprotectChecked() error {
	protectMu.Lock()
	defer protectMu.Unlock()
	protectedAt = ""
	return unprotect()
}

func unprotect() error {
	var errs []string
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		rules, err := netlink.RuleList(fam)
		if err != nil {
			// 这个地址族上问不出规则:内核没编 IPv6。那 Protect 那边同样加不进规则,这里没有我们的规则可删。
			// 跳过它,别拿它把连接挡死 —— m29 把这一处非零退出当成硬错误,于是没编 IPv6 的软路由在默认设置下完全连不上。
			continue
		}
		for i := range rules {
			if !isReplyRule(rules[i]) {
				continue
			}
			if err := netlink.RuleDel(&rules[i]); err != nil && !errors.Is(err, unix.ENOENT) {
				errs = append(errs, fmt.Sprintf("ip rule del from %s: %v", rules[i].Src, err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func Unprotect() { _ = UnprotectChecked() }

// Protected 系统里还有没有我们加的回包规则。
func Protected() bool {
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		rules, _ := netlink.RuleList(fam)
		for i := range rules {
			if isReplyRule(rules[i]) {
				return true
			}
		}
	}
	return false
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
