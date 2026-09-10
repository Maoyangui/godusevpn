package netmode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/paths"
)

// 停用网卡上的 IPv6 协议绑定,等同于在"网络适配器属性"里取消勾选「Internet 协议版本 6」。
//
// 为什么要有这一步:关掉 IPv6 的那几层(DNS 只回 A、不分配 v6 fake-ip、隧道里拒绝 v6 目标)挡的都是
// "IPv6 数据包出网",挡不住"读取本机网卡配置" —— 运营商用路由器通告发下来的公网 IPv6 地址就明明白白
// 配在物理网卡上,任何原生程序枚举一遍网卡就能读走,再经隧道报给自己的服务器。那个地址的前缀对应到
// 具体一条宽带线路,定位价值不比公网 IPv4 差。地址本身不存在,才是真的读不到。
//
// 备份记的是"动手之前每张网卡各是什么状态",不是"我关掉了哪几张" —— 本机实测发现关掉某张网卡会连带
// 让 Hyper-V 的虚拟网卡也变成关闭,只记自己动过的那几张,还原时会漏掉被连累的。按全量状态还原就不会漏。
// 断开时按备份还原;守护进程启动时也无条件还原一次,上次崩溃退出也不会把用户的 IPv6 永久关掉。
// 本来就是关闭状态的网卡记下来但不动,还原时也不去开它 —— 那是用户自己关的。

func nicBackup() string { return filepath.Join(paths.DataDir(), "nic-ipv6-backup.txt") }

// DisableNICIPv6 停用各网卡的 IPv6 绑定,隧道自己那张除外 —— 隧道要靠 v6 地址把 v6 流量接进来再拒绝,
// 关了它反而少一层防护。
func DisableNICIPv6(tunName string) error {
	script := `
$ErrorActionPreference = 'SilentlyContinue'
$tun = '` + psQuote(tunName) + `'
$all = @(Get-NetAdapterBinding -ComponentID ms_tcpip6 | Where-Object { $_.Name -ne $tun })
@($all | ForEach-Object { $_.Name + "` + "\t" + `" + $_.Enabled }) | Set-Content -Encoding utf8 -LiteralPath '` + psQuote(nicBackup()) + `'
foreach ($a in $all) { if ($a.Enabled) { Disable-NetAdapterBinding -Name $a.Name -ComponentID ms_tcpip6 -ErrorAction SilentlyContinue } }
`
	if out, err := runPS(script); err != nil {
		return fmt.Errorf("停用网卡 IPv6: %w(%s)", err, out)
	}
	return nil
}

// RestoreNICIPv6 按备份把 IPv6 绑定装回原样。没有备份就什么都不做(没动过,别去碰用户自己的设置)。
func RestoreNICIPv6() {
	if _, err := os.Stat(nicBackup()); err != nil {
		return
	}
	script := `
$ErrorActionPreference = 'SilentlyContinue'
$f = '` + psQuote(nicBackup()) + `'
foreach ($line in @(Get-Content -Encoding utf8 -LiteralPath $f)) {
  $p = $line -split "` + "\t" + `"
  if ($p.Count -ge 2 -and $p[1] -eq 'True' -and $p[0].Trim()) {
    Enable-NetAdapterBinding -Name $p[0] -ComponentID ms_tcpip6 -ErrorAction SilentlyContinue
  }
}
Remove-Item -LiteralPath $f -Force -ErrorAction SilentlyContinue
`
	_, _ = runPS(script)
	_ = os.Remove(nicBackup()) // 脚本没删成也兜一下,免得下次启动反复还原
}

func runPS(script string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// psQuote PowerShell 单引号字符串里的转义:单引号写两遍。网卡名里有中文、空格和星号(比如「本地连接* 12」),
// 全程用 -LiteralPath / -Name 传,不让它当通配符解释。
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }
