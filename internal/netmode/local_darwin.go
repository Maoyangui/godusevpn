// macOS:sing-tun 自己建 utun 并把默认路由(IPv4 与 IPv6)指进隧道,不需要像 Linux 那样再加策略路由。
// 网关模式(代理经本机转发的局域网设备)是软路由的功能,macOS 上不提供。
//
// 这里要做的是另一件事:接管系统 DNS。
// macOS 的解析器问的是网络服务上配的 DNS,通常就是路由器(192.168.x.1 这类),而"局域网直连"会把私网段
// 排除在隧道之外 —— 于是每一次域名解析都绕过隧道直接问路由器:既拿不到 fake-ip(域名分流退化成只能靠嗅探),
// 关掉 IPv6 时 AAAA 也照样返回,更要紧的是解析记录实打实泄漏给了本地网络。
// 处理:连上时把各网络服务的 DNS 改成一个会走隧道的地址(进了隧道就被 hijack-dns 接住),
// 断开时按连接前存下的原值还原。备份落盘,守护进程崩了下次启动也能还原回去。
//
// 关于 IPv6:关掉 IPv6 时 TUN 照样声明 v6 地址,auto_route 会把 v6 也指进隧道,
// 进来之后由路由规则里的 ip_version=6 拒绝掉 —— 也就是"接进来再拒绝",而不是放它从物理网卡出去。
// 这条在 macOS 上是否真的成立由 deploy/macos-test.sh 在真机上核验。

package netmode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/paths"
)

// dnsBackup 连接前各网络服务的 DNS 原值:服务名 → 地址列表(空列表表示原本就是"跟随 DHCP")。
func dnsBackup() string { return paths.DataDir() + "/dns-backup.json" }

// Protect 把系统 DNS 接进隧道。ipv6 参数用不上:隧道里的 DNS 服务按设置决定要不要回 AAAA。
func Protect(_ string, _ bool) error {
	svcs, err := networkServices()
	if err != nil {
		return err
	}
	saved := map[string][]string{}
	haveBackup := false
	backupChanged := false
	if b, readErr := os.ReadFile(dnsBackup()); readErr == nil {
		if err := json.Unmarshal(b, &saved); err != nil || saved == nil {
			// 备份坏了(0 字节、被截断、手工改过)不能两头堵死:Protect 因此拒绝连接、
			// UnprotectChecked 因此永远还不了 DNS,用户既上不了网也修不好,而且没有任何自愈路径。
			// 挪到 .bad 留证,然后按"没有备份"重建 —— currentDNS 会把我们自己的隧道地址滤掉,
			// 所以重建出来的要么是用户真正的 DNS,要么是空(跟随 DHCP),后者正是 macOS 的出厂状态。
			_ = os.Rename(dnsBackup(), dnsBackup()+".bad")
			saved = map[string][]string{}
		} else {
			haveBackup = true
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("读取已有 DNS 备份失败: %w", readErr)
	}
	// 已经密封时保留首次接管前的原值;新出现的网络服务才从当前状态补一条,
	// 避免重复重连把原始 DNS 覆盖成隧道地址/空值。
	for _, s := range svcs {
		if _, ok := saved[s]; !ok {
			saved[s] = currentDNS(s)
			backupChanged = true
		}
	}
	if !haveBackup || backupChanged {
		// 先落盘再改:万一改到一半进程没了,下次启动还能照着还原。
		b, err := json.Marshal(saved)
		if err != nil {
			return fmt.Errorf("序列化 DNS 备份: %w", err)
		}
		tmp := dnsBackup() + ".tmp"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("打开 DNS 备份: %w", err)
		}
		if _, err = f.Write(b); err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(tmp, dnsBackup())
		}
		if err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("保存 DNS 备份: %w", err)
		}
	}
	var errs []string
	for _, s := range svcs {
		if out, err := exec.Command("networksetup", "-setdnsservers", s, builder.HijackDNS).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", s, strings.TrimSpace(string(out))))
		}
	}
	flushDNS()
	if len(errs) > 0 {
		return fmt.Errorf("接管系统 DNS 失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

// UnprotectChecked 还原系统 DNS。没有备份就什么都不做(没接管过,别去动用户的设置)。
// 失败时保留备份，调用方可以继续重试，避免把恢复依据删掉。
func UnprotectChecked() error {
	b, err := os.ReadFile(dnsBackup())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取 DNS 备份失败: %w", err)
	}
	var saved map[string][]string
	if err := json.Unmarshal(b, &saved); err != nil || saved == nil {
		// 备份坏了就再也还不了 DNS,用户断开之后解析器指着隧道地址、而隧道已经没了 —— 等于没网,
		// 且怎么点都修不好。原值是找不回来了,但至少要把解析恢复成能用的:凡是此刻还指着我们
		// 隧道 DNS 的服务,一律改回"跟随 DHCP"(macOS 的出厂状态),然后把坏备份挪走让它自愈。
		return restoreHijackedDNSBlind()
	}
	var errs []string
	for s, addrs := range saved {
		args := []string{"-setdnsservers", s}
		if len(addrs) == 0 {
			args = append(args, "Empty") // 原本跟随 DHCP
		} else {
			args = append(args, addrs...)
		}
		if out, err := exec.Command("networksetup", args...).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", s, strings.TrimSpace(string(out))))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("还原系统 DNS 失败: %s", strings.Join(errs, "; "))
	}
	flushDNS()
	if err := os.Remove(dnsBackup()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 DNS 备份失败: %w", err)
	}
	return nil
}

// restoreHijackedDNSBlind 没有可用备份时的兜底还原:把 DNS 仍指着隧道地址的网络服务改回跟随 DHCP。
// 只动"确实是我们改过的"那些(当前值就是 builder.HijackDNS),绝不碰用户自己配的 DNS。
func restoreHijackedDNSBlind() error {
	svcs, err := networkServices()
	if err != nil {
		return fmt.Errorf("DNS 备份已损坏,且列不出网络服务: %w", err)
	}
	var errs []string
	for _, s := range svcs {
		out, err := exec.Command("networksetup", "-getdnsservers", s).Output()
		// builder.HijackDNS 是 223.5.5.5 —— 一个真实存在的公共 DNS,用户完全可能自己就填了它。
		// 所以不能用 Contains 去猜"这是我们改的":只有整份列表**恰好就这一条**才算数。
		// 我们接管时写的就是单独一条(Protect 里的 -setdnsservers <svc> <HijackDNS>),
		// 而用户自己填 223.5.5.5 时几乎总会再配一条备用,不会只有一条。
		if err != nil || !onlyHijackDNS(string(out)) {
			continue
		}
		if out, err := exec.Command("networksetup", "-setdnsservers", s, "Empty").CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", s, strings.TrimSpace(string(out))))
		}
	}
	flushDNS()
	if len(errs) > 0 {
		return fmt.Errorf("DNS 备份已损坏,兜底还原也没做完: %s", strings.Join(errs, "; "))
	}
	// 坏备份挪走,下一次接管就是干净的一轮。
	_ = os.Rename(dnsBackup(), dnsBackup()+".bad")
	return nil
}

// onlyHijackDNS networksetup -getdnsservers 的输出是不是"只有我们那一条隧道 DNS"。
// 没配 DNS 时它回的是一句带空格的提示(There aren't any DNS Servers set on ...),不会被误判。
func onlyHijackDNS(out string) bool {
	n := 0
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.ContainsAny(ln, " ") {
			continue
		}
		if ln != builder.HijackDNS {
			return false
		}
		n++
	}
	return n == 1
}

// Unprotect 保持历史调用方的幂等接口；需要向用户报告结果的路径使用
// UnprotectChecked，失败时备份仍会保留。
func Unprotect() { _ = UnprotectChecked() }

// networkServices 所有网络服务名(Wi-Fi、Ethernet…)。首行是说明文字,停用的服务前面带 *。
func networkServices() ([]string, error) {
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return nil, fmt.Errorf("列网络服务失败: %w", err)
	}
	var svcs []string
	for i, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "*"))
		if i == 0 || ln == "" { // 首行是 "An asterisk (*) denotes..."
			continue
		}
		svcs = append(svcs, ln)
	}
	if len(svcs) == 0 {
		return nil, fmt.Errorf("没有找到任何网络服务")
	}
	return svcs, nil
}

// currentDNS 某个服务当前配的 DNS;没配时 networksetup 回一句 "There aren't any DNS Servers set on ..."。
func currentDNS(svc string) []string {
	out, err := exec.Command("networksetup", "-getdnsservers", svc).Output()
	if err != nil {
		return nil
	}
	var addrs []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.ContainsAny(ln, " ") { // 提示句里有空格,地址没有
			continue
		}
		if ln == builder.HijackDNS { // 上一次没还原干净,别把隧道地址当成原值存下来
			continue
		}
		addrs = append(addrs, ln)
	}
	return addrs
}

func flushDNS() {
	_ = exec.Command("dscacheutil", "-flushcache").Run()
	_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
}

// ApplyGateway / ClearGateway 网关模式只在 Linux 软路由上有。
func ApplyGateway(string, []string, bool) error { return nil }

func ClearGateway() {}
