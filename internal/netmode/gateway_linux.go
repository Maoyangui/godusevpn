//go:build linux && !android

package netmode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/paths"
)

// 网关模式下的防火墙:sing-box 的 auto_redirect 自己用 nftables 把经本机转发的 TCP / UDP 收进来,
// 连局域网设备发给路由器 53 端口的 DNS 查询也由它 DNAT 到 TUN 地址(table inet sing-box 的 prerouting 链),
// 我们不需要再加规则;早期版本自己建过一张 inet godusevpn 表做 DNS 劫持,反而抢在它前面把查询改坏了,这里只负责清掉残留。
//
// IP 转发要我们管:路由系统(OpenWrt 等)本来开着,普通 Linux(Debian、Ubuntu 上搭的旁路由)默认关着。关着时 TCP 还能用
// (auto_redirect 把它改成发给本机),UDP 要经转发进隧道就全丢了 —— 设备上表现为网页能开,QUIC、语音、游戏都不通。

const nftTable = "godusevpn"

const forwardSysctl = "/proc/sys/net/ipv4/ip_forward"

// forwardMark 记下"转发是我们打开的":内容是开机号,重启后转发早回到了系统配置,旧记录就作废,免得把用户后来自己开的关掉。
func forwardMark() string { return filepath.Join(paths.DataDir(), "ip-forward-on") }

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// ApplyGateway 打开 IP 转发,并清理旧版本留下的表;参数保留给以后的 TProxy 模式。局域网设备的 DNS 由 auto_redirect 接管,没有开关。
func ApplyGateway(string, []string) error {
	ClearGateway()
	return enableForward(forwardSysctl, forwardMark())
}

// ClearGateway 删掉本程序的 nft 表(不存在也无妨)。
func ClearGateway() {
	_ = exec.Command("nft", "delete", "table", "inet", nftTable).Run()
}

// RestoreForward 不再当网关时把转发关回去:只关我们打开的,本来就开着的不动。
func RestoreForward() { restoreForward(forwardSysctl, forwardMark()) }

func enableForward(sysctl, mark string) error {
	b, err := os.ReadFile(sysctl)
	if err != nil {
		return fmt.Errorf("读 IP 转发开关: %w", err)
	}
	if strings.TrimSpace(string(b)) == "1" {
		return nil
	}
	// 先记后改:改完没记上的话,这台机器就永远留着转发了
	if err := os.WriteFile(mark, []byte(bootID()), 0o600); err != nil {
		return fmt.Errorf("记录 IP 转发状态: %w", err)
	}
	if err := os.WriteFile(sysctl, []byte("1\n"), 0o644); err != nil {
		_ = os.Remove(mark)
		return fmt.Errorf("打开 IP 转发(可手动 sysctl -w net.ipv4.ip_forward=1): %w", err)
	}
	return nil
}

func restoreForward(sysctl, mark string) {
	b, err := os.ReadFile(mark)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(b)) == bootID() {
		_ = os.WriteFile(sysctl, []byte("0\n"), 0o644)
	}
	_ = os.Remove(mark)
}
