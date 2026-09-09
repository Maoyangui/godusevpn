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
	for _, s := range svcs {
		saved[s] = currentDNS(s)
	}
	// 先落盘再改:万一改到一半进程没了,下次启动还能照着还原
	if b, err := json.Marshal(saved); err == nil {
		_ = os.WriteFile(dnsBackup(), b, 0o600)
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

// Unprotect 还原系统 DNS。没有备份就什么都不做(没接管过,别去动用户的设置)。
// 守护进程启动时也会调一次:上次异常退出留下的隧道 DNS 会在这里被还原,不至于开不了网页。
func Unprotect() {
	b, err := os.ReadFile(dnsBackup())
	if err != nil {
		return
	}
	var saved map[string][]string
	if json.Unmarshal(b, &saved) != nil {
		_ = os.Remove(dnsBackup())
		return
	}
	for s, addrs := range saved {
		args := []string{"-setdnsservers", s}
		if len(addrs) == 0 {
			args = append(args, "Empty") // 原本跟随 DHCP
		} else {
			args = append(args, addrs...)
		}
		_ = exec.Command("networksetup", args...).Run()
	}
	flushDNS()
	_ = os.Remove(dnsBackup())
}

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
