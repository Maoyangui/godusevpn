//go:build linux && !android

// Package netmode Linux 上 TUN 之外还要做的路由动作。
//
// 本机模式(auto_route)有个坑:本机上对外提供服务的进程(sshd、面板、RDP 这类)给远端的回包,源地址是物理网卡的 IP,
// 但默认路由已经指向 TUN,回包会被内核当成陌生连接的包丢掉,结果就是连上代理的一瞬间 SSH 全断。
// 处理:本机发出的包,所在连接是对方先发起的(连接跟踪里的"应答方向":SSH、面板)就在 output 上打一个标记,
// 策略路由 "fwmark 标记 lookup main"(优先级高于 sing-box 的规则)让它们走原来的路(见 replyRuleset)。
// 以前是给每个物理网卡地址加 "from <地址> lookup main":任何程序把套接字绑在物理网卡地址上(WebRTC 就会枚举本机地址
// 这么干)都整个绕过隧道,规则 / 普通全局下露出真实 IP。系统里没有 nft(或内核不认这几条)时退回那种做法,并记下原因。
package netmode

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/sagernet/netlink"
	"golang.org/x/sys/unix"

	"github.com/Maoyangui/godusevpn/internal/builder"
)

// 规则优先级:要比 sing-box auto_route 的 9000 段小
const rulePref = 5000

const (
	replyTable = "godusevpn_reply"
	// replyMark 外部连进来的连接在连接跟踪上打的标记:只占一位、按位设按位比,不碰别人用的其它位;
	// 和本服务出站的 0x676f0000(builder.RouteMark)、sing-tun 的 0x2023 / 0x2024 都不重。
	replyMark = 0x10000000
)

var (
	protectMu   sync.Mutex
	protectCT   bool   // 正按连接跟踪保护着
	protectedAt string // 退回按地址时:上次 Protect 时的地址集合(排好序拼起来);空 = 没按地址保护
	protectTun  string
	protectV6   bool
)

// Protect 让外部连进来的连接的回包走主表(见文件头)。可重复调用(先清再加)。
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
	var warns []string
	if err := unprotect(); err != nil {
		warns = append(warns, "清理旧的回包路由规则没做干净(不影响出网): "+err.Error())
	}
	err := protectByConntrack()
	if err == nil {
		// 按连接跟踪,和本机地址无关:PPPoE 重拨、DHCP 换地址都不用重做
		protectCT, protectedAt, protectTun, protectV6 = true, "", tunName, ipv6
		setRouteWarning(strings.Join(warns, ";"))
		return nil
	}
	protectCT = false
	_ = unprotect() // 做了一半的撤掉(表、标记规则),退回按地址
	warns = append(warns, "回包路由退回按本机地址("+err.Error()+"):程序把套接字绑在物理网卡地址上(WebRTC 之类)会绕过隧道")
	setRouteWarning(strings.Join(warns, ";"))
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

// RefreshProtect 连着的时候(巡检每半分钟)看一眼还在不在:按连接跟踪时,表或规则被别人清掉了(重启 nftables 服务、
// 防火墙重载都会先清空全部规则)就重做,不然经 WAN 进来的管理连接(sshd、面板)的回包会被吞进隧道;
// 退回按地址时,地址变了(PPPoE 重拨、DHCP 换了地址)就按新地址重做,旧地址的规则则成了死规则。
// 没在保护(没连着、或者已经断开还原了)就什么都不做。返回这一轮有没有重做。
func RefreshProtect() (bool, error) {
	protectMu.Lock()
	defer protectMu.Unlock()
	if protectCT {
		if conntrackIntact() {
			return false, nil
		}
		return true, protect(protectTun, protectV6)
	}
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
	protectCT, protectedAt = false, ""
	return unprotect()
}

// unprotect 两种做法留下的都删:按连接跟踪的表与标记规则、按地址的规则(老版本、或退回时加的)。
func unprotect() error {
	var errs []string
	if err := unprotectConntrack(); err != nil {
		errs = append(errs, err.Error())
	}
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		rules, err := netlink.RuleList(fam)
		if err != nil {
			// 这个地址族上问不出规则:内核没编 IPv6。那 Protect 那边同样加不进规则,这里没有我们的规则可删。
			// 跳过它,别拿它把连接挡死 —— m29 把这一处非零退出当成硬错误,于是没编 IPv6 的软路由在默认设置下完全连不上。
			continue
		}
		for i := range rules {
			if !isReplyRule(rules[i]) && !isMarkReplyRule(rules[i]) {
				continue
			}
			r := rules[i]
			r.MarkSet = isMarkReplyRule(r) // 列出来的规则不带这个标志,删的时候不带就匹配不上那条按标记的
			if err := netlink.RuleDel(&r); err != nil && !errors.Is(err, unix.ENOENT) {
				errs = append(errs, fmt.Sprintf("ip rule del pref %d from %s fwmark %#x: %v", r.Priority, r.Src, r.Mark, err))
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
			if isReplyRule(rules[i]) || isMarkReplyRule(rules[i]) {
				return true
			}
		}
	}
	for _, fam := range []string{"ip", "ip6"} {
		if exec.Command("nft", "list", "table", fam, replyTable).Run() == nil {
			return true
		}
	}
	return false
}

// ---- 按连接跟踪 ----

// replyRuleset 一个地址族(ip / ip6)的 nft 表,只有 output 上一条 route 链:本机发出的包在连接跟踪里是"应答方向"
// (连接是对方先发起的:SSH、面板)就打上标记,route 链让内核按新标记重新选路。本机发起的连接(含把套接字绑在物理网卡
// 地址上的 WebRTC)是"原始方向",不打,照样走隧道。不靠"外部下一个包进来时再补标记":服务端先往外发(SSH 每秒一行输出)
// 的那一下没标记就进了隧道,隧道协议栈不认识这条连接、回一个重置,会话就断了(真机上撞到过)。
// 连接跟踪起来之前就在的会话,"谁先发起"可能被认反(从中途接手时以先看到的那个包为准):那些会话由 sessions 按四元组
// 兜住(见 inboundSessions)。inet 族不一定支持 route 链,所以 v4 / v6 各一张。先建再删再建,整段幂等。
func replyRuleset(family string, sessions []inboundSession) string {
	addrType, match := "ipv4_addr", "ip"
	if family == "ip6" {
		addrType, match = "ipv6_addr", "ip6"
	}
	var elems []string
	for _, s := range sessions {
		if s.local.Addr().Is6() == (family == "ip6") {
			elems = append(elems, fmt.Sprintf("%s . %d . %s . %d", s.local.Addr(), s.local.Port(), s.remote.Addr(), s.remote.Port()))
		}
	}
	set := ""
	if len(elems) > 0 {
		set = "\n\t\telements = { " + strings.Join(elems, ", ") + " }"
	}
	return fmt.Sprintf(`table %[1]s %[2]s {}
delete table %[1]s %[2]s
table %[1]s %[2]s {
	set inbound {
		type %[4]s . inet_service . %[4]s . inet_service%[6]s
	}
	chain out {
		type route hook output priority mangle; policy accept;
		ct direction reply meta mark set meta mark or %#[3]x
		%[5]s saddr . tcp sport . %[5]s daddr . tcp dport @inbound meta mark set meta mark or %#[3]x
	}
}
`, family, replyTable, replyMark, addrType, match, set)
}

// inboundSession 一条外部连进来、已经建立的 TCP 会话。
type inboundSession struct{ local, remote netip.AddrPort }

// inboundSessions 从 /proc/net/tcp、tcp6 的内容里挑出外部连进来的已建立会话:本机端口正是某个监听端口的(SSH、面板)。
// 回环和隧道地址上的不要(隧道协议栈自己的内部监听)。v4 映射成 v6 的按 v4 记 —— 线上走的是 v4 包。
func inboundSessions(tcp4, tcp6 string) []inboundSession {
	type entry struct {
		local, remote netip.AddrPort
		state         string
	}
	var all []entry
	for _, content := range []string{tcp4, tcp6} {
		for _, line := range strings.Split(content, "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || f[0] == "sl" {
				continue
			}
			l, ok1 := parseProcAddr(f[1])
			r, ok2 := parseProcAddr(f[2])
			if ok1 && ok2 {
				all = append(all, entry{l, r, f[3]})
			}
		}
	}
	listen := map[uint16]bool{}
	for _, e := range all {
		if e.state == "0A" { // LISTEN
			listen[e.local.Port()] = true
		}
	}
	tun4, tun6 := netip.MustParsePrefix(builder.TunAddr4).Masked(), netip.MustParsePrefix(builder.TunAddr6).Masked()
	var out []inboundSession
	for _, e := range all {
		a := e.local.Addr()
		if e.state != "01" || !listen[e.local.Port()] || a.IsLoopback() || tun4.Contains(a) || tun6.Contains(a) { // 01 = ESTABLISHED
			continue
		}
		out = append(out, inboundSession{e.local, e.remote})
	}
	return out
}

// parseProcAddr "0100007F:0820" 这种:地址是按本机字节序打印的 32 位字(v6 是四个),端口是普通的十六进制数。
func parseProcAddr(s string) (netip.AddrPort, bool) {
	host, port, ok := strings.Cut(s, ":")
	if !ok || (len(host) != 8 && len(host) != 32) {
		return netip.AddrPort{}, false
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	b := make([]byte, len(host)/2)
	for i := 0; i < len(host); i += 8 {
		w, err := strconv.ParseUint(host[i:i+8], 16, 32)
		if err != nil {
			return netip.AddrPort{}, false
		}
		binary.NativeEndian.PutUint32(b[i/2:], uint32(w))
	}
	a, _ := netip.AddrFromSlice(b)
	return netip.AddrPortFrom(a.Unmap(), uint16(p)), true
}

// currentInboundSessions 此刻外部连进来的已建立会话;读不出来就当没有(新连接照样由连接跟踪判方向)。
func currentInboundSessions() []inboundSession {
	b4, _ := os.ReadFile("/proc/net/tcp")
	b6, _ := os.ReadFile("/proc/net/tcp6")
	return inboundSessions(string(b4), string(b6))
}

// markRule "fwmark replyMark/replyMark lookup main pref 5000"。
func markRule(family int) *netlink.Rule {
	r := netlink.NewRule()
	r.Family = family
	r.Priority = rulePref
	r.Table = unix.RT_TABLE_MAIN
	r.Mark, r.MarkSet, r.Mask = replyMark, true, replyMark
	return r
}

// isMarkReplyRule 这条规则是不是 markRule 那种:优先级 5000、查主表、只按我们那一位标记匹配,别的条件一概没有。
func isMarkReplyRule(r netlink.Rule) bool {
	return r.Priority == rulePref && r.Table == unix.RT_TABLE_MAIN && !r.Invert &&
		r.Mark == replyMark && r.Mask == replyMark && !r.Src.IsValid() && !r.Dst.IsValid() &&
		r.IifName == "" && r.OifName == "" && r.Goto < 0 &&
		r.Dport == nil && r.Sport == nil && r.IPProto == 0 && r.UIDRange == nil
}

// protectByConntrack 装 nft 表和标记规则。v4 必须成;v6 的表和规则加不上(内核没编 IPv6)不影响。
func protectByConntrack() error {
	sessions := currentInboundSessions()
	if err := nftLoad(replyRuleset("ip", sessions)); err != nil {
		return err
	}
	_ = nftLoad(replyRuleset("ip6", sessions))
	if err := netlink.RuleAdd(markRule(unix.AF_INET)); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("ip rule add fwmark: %w", err)
	}
	_ = netlink.RuleAdd(markRule(unix.AF_INET6))
	return nil
}

func nftLoad(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("系统里没有 nft")
	}
	return fmt.Errorf("nft: %v: %s", err, strings.TrimSpace(string(out)))
}

// unprotectConntrack 删两张 nft 表(标记规则由 unprotect 的规则循环删)。删完还列得出来才算失败:
// 表本来就没有、系统里没有 nft、内核不带 IPv6 的 nft,都列不出来,不算失败。
func unprotectConntrack() error {
	var errs []string
	for _, fam := range []string{"ip", "ip6"} {
		_ = exec.Command("nft", "delete", "table", fam, replyTable).Run()
		if exec.Command("nft", "list", "table", fam, replyTable).Run() == nil {
			errs = append(errs, "nft 表 "+fam+" "+replyTable+" 删不掉")
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// conntrackIntact 按连接跟踪的那套还在不在(v4 的表和标记规则)。规则问不出来时按"在"算,免得每半分钟白重做一遍。
func conntrackIntact() bool {
	if exec.Command("nft", "list", "table", "ip", replyTable).Run() != nil {
		return false
	}
	rules, err := netlink.RuleList(unix.AF_INET)
	if err != nil {
		return true
	}
	for i := range rules {
		if isMarkReplyRule(rules[i]) {
			return true
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
