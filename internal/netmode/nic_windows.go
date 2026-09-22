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
// 断开时按备份还原;守护进程启动时对账一次:上次不是连着关的机(或者设置已关掉这一项)就还原回去,
// 上次是连着关的机就接着关着 —— 它和「全局禁直连」的闸一样是持久的。连接状态读不出来时改看闸还在不在。
// 本来就是关闭状态的网卡记下来但不动,还原时也不去开它 —— 那是用户自己关的。

func nicBackup() string { return filepath.Join(paths.DataDir(), "nic-ipv6-backup.txt") }

// DisableNICIPv6 停用各网卡的 IPv6 绑定,隧道自己那张除外 —— 隧道要靠 v6 地址把 v6 流量接进来再拒绝,
// 关了它反而少一层防护。
//
// 备份是**并进去**而不是覆盖:已有备份说明上次停了还没还原(重建配置重连时守护进程故意不还原,
// 或者连接期间又冒出一张新网卡),这时候原来那些网卡都是关着的,整份重记会记成一片"关",
// 最后什么都开不回来;而整份跳过又会漏掉新网卡的原始状态,那张就被永久关着了。只补没记过的那些才两头都对。
func DisableNICIPv6(tunName string) error {
	script := `
$ErrorActionPreference = 'Stop'
$tun = '` + psQuote(tunName) + `'
$all = @(Get-NetAdapterBinding -ComponentID ms_tcpip6 | Where-Object { $_.Name -ne $tun })
$b = '` + psQuote(nicBackup()) + `'
$known = @{}
$bad = $false
$old = @()
if (Test-Path -LiteralPath $b) {
  $old = @(Get-Content -Encoding utf8 -LiteralPath $b)
  foreach ($line in $old) {
    $p = $line -split "` + "\t" + `", -1
    if ($p.Count -ne 2 -or [string]::IsNullOrWhiteSpace($p[0]) -or $p[1] -notmatch '^(True|False)$' -or $known.ContainsKey($p[0])) { $bad = $true; break }
    $known[$p[0]] = $true
  }
}
if ($bad) {
  Move-Item -LiteralPath $b -Destination ($b + '.bad') -Force -ErrorAction SilentlyContinue
  Write-Output 'GODUSEVPN-PARTIAL: 网卡 IPv6 备份文件已损坏,已挪到 nic-ipv6-backup.txt.bad。里面记的原始状态没了 —— 如果某些网卡的 IPv6 现在是关着的,需要手动在「网络适配器属性」里把「Internet 协议版本 6」勾回来。这一轮不动任何网卡。'
  exit 0
}
$new = @($all | Where-Object { -not $known.ContainsKey($_.Name) } | ForEach-Object { $_.Name + "` + "\t" + `" + $_.Enabled })
if ($new.Count -gt 0) {
  $tmp = $b + '.tmp'
  $utf8 = New-Object System.Text.UTF8Encoding($false)
  $fs = New-Object System.IO.FileStream($tmp, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
  $sw = New-Object System.IO.StreamWriter($fs, $utf8)
  foreach ($line in @($old + $new)) { $sw.WriteLine($line) }
  $sw.Flush(); $fs.Flush($true); $sw.Dispose()
  Move-Item -LiteralPath $tmp -Destination $b -Force
  $check = @(Get-Content -Encoding utf8 -LiteralPath $b)
  if ($check.Count -ne @($old + $new).Count) { throw 'IPv6 备份落盘校验失败' }
  for ($i = 0; $i -lt $check.Count; $i++) { if ($check[$i] -ne @($old + $new)[$i]) { throw 'IPv6 备份内容校验失败' } }
}
$failed = @()
foreach ($a in $all) {
  if (-not $a.Enabled) { continue }
  try { Disable-NetAdapterBinding -Name $a.Name -ComponentID ms_tcpip6 -ErrorAction Stop }
  catch { $failed += ($a.Name + ': ' + $_.Exception.Message) }
}
foreach ($a in $all) {
  try { $st = Get-NetAdapterBinding -Name $a.Name -ComponentID ms_tcpip6 -ErrorAction Stop } catch { continue }
  if ($st.Enabled) { $failed += ($a.Name + ': 停用后仍是启用状态') }
}
if ($failed.Count -gt 0) { Write-Output ('GODUSEVPN-PARTIAL: ' + ($failed -join '; ')) }
`
	out, err := runPS(script)
	if err != nil {
		return nicResult(tunName, []string{fmt.Sprintf("%v(%s)", err, out)})
	}
	if p := psMark(out, markPartial); p != "" {
		// 有网卡没停成(在脚本跑的这几秒里被拔掉 / 被系统销毁,或者驱动不让改绑定)。
		// m29 在这里整体抛错,而 prepare 拿它当连接前置 —— 于是一张 Wi-Fi Direct 虚拟网卡的生灭
		// 就能让人连不上。交给 nicResult:真有公网 v6 露在外面才算失败。
		return nicResult(tunName, []string{p})
	}
	setNICWarning("")
	return nil
}

// runPS 脚本里用这两个记号把"部分失败"和"网卡已消失"带回来 —— PowerShell 的退出码只有一个,
// 区分不了"整件事崩了"和"有几张网卡没动成",而这两者在这里的处理完全不同。
const (
	markPartial = "GODUSEVPN-PARTIAL: "
	markFailed  = "GODUSEVPN-FAILED: "
	markGone    = "GODUSEVPN-GONE: "
)

// psMark 从脚本输出里取出某个记号后面的内容;没有就返回空串。
func psMark(out, mark string) string {
	for _, ln := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(ln), mark); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// RestoreNICIPv6 按备份把 IPv6 绑定装回原样。没有备份就什么都不做(没动过,别去碰用户自己的设置)。
func RestoreNICIPv6() error {
	if _, err := os.Stat(nicBackup()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// 只把记成 True 的网卡开回去。记成 False 的是"动手之前它本来就是关的"——
	// 停用那一步压根没碰过它们(只 Disable 了 Enabled 的),还原时更不该去动:
	// 文件开头写的就是"本来就是关闭状态的网卡记下来但不动,还原时也不去开它"。
	// m29 给 False 行加了主动 Disable,除了凭空多出一堆失败点,没有任何作用。
	//
	// 备份里的网卡消失是常事:拔掉 USB 网卡、Wi-Fi Direct 虚拟网卡被系统销毁、Wintun 被删、网卡改名。
	// m29 在 $ErrorActionPreference='Stop' 下让它中断整个循环,于是**排在它后面的网卡一张都不还原**,
	// 而且 Go 侧提前 return 让备份永远删不掉 —— 用户的 IPv6 被永久关着,产品也永远卸不干净。
	// 这里逐张 try/catch:网卡已经不在了就当无需还原,只有"网卡在、但还原失败"才留在备份里下次重试。
	script := `
$ErrorActionPreference = 'Stop'
$f = '` + psQuote(nicBackup()) + `'
$lines = @(Get-Content -Encoding utf8 -LiteralPath $f)
$seen = @{}
$left = @()
$failed = @()
$gone = @()
foreach ($line in $lines) {
  if ([string]::IsNullOrWhiteSpace($line)) { continue }
  $p = $line -split "` + "\t" + `", -1
  if ($p.Count -ne 2 -or [string]::IsNullOrWhiteSpace($p[0]) -or $p[1] -notmatch '^(True|False)$' -or $seen.ContainsKey($p[0])) {
    $failed += ('备份行无效: ' + $line)
    $left += $line
    continue
  }
  $seen[$p[0]] = $true
  if ($p[1] -ne 'True') { continue }
  try {
    Enable-NetAdapterBinding -Name $p[0] -ComponentID ms_tcpip6 -ErrorAction Stop
    $st = Get-NetAdapterBinding -Name $p[0] -ComponentID ms_tcpip6 -ErrorAction Stop
    if (-not $st.Enabled) { $failed += ($p[0] + ': 还原后仍是停用状态'); $left += $line }
  } catch {
    if (Get-NetAdapter -Name $p[0] -ErrorAction SilentlyContinue) {
      $failed += ($p[0] + ': ' + $_.Exception.Message)
      $left += $line
    } else {
      $gone += $p[0]
    }
  }
}
if ($left.Count -gt 0) {
  $tmp = $f + '.tmp'
  $utf8 = New-Object System.Text.UTF8Encoding($false)
  $fs = New-Object System.IO.FileStream($tmp, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
  $sw = New-Object System.IO.StreamWriter($fs, $utf8)
  foreach ($line in $left) { $sw.WriteLine($line) }
  $sw.Flush(); $fs.Flush($true); $sw.Dispose()
  Move-Item -LiteralPath $tmp -Destination $f -Force
} else {
  Remove-Item -LiteralPath $f -Force -ErrorAction SilentlyContinue
}
if ($failed.Count -gt 0) { Write-Output ('GODUSEVPN-FAILED: ' + ($failed -join '; ')) }
if ($gone.Count -gt 0) { Write-Output ('GODUSEVPN-GONE: ' + ($gone -join ', ')) }
`
	out, err := runPS(script)
	if err != nil {
		return fmt.Errorf("还原网卡 IPv6: %w(%s)", err, out)
	}
	if f := psMark(out, markFailed); f != "" {
		return fmt.Errorf("还原网卡 IPv6: %s", f)
	}
	if g := psMark(out, markGone); g != "" {
		setNICWarning("这些网卡已经不在了,当作无需还原: " + g)
	}
	// 脚本里已经删过一次;这里兜一下,免得脚本那步被杀掉之后下次启动反复还原。
	if err := os.Remove(nicBackup()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 IPv6 备份: %w", err)
	}
	return nil
}

func runPS(script string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// psQuote PowerShell 单引号字符串里的转义:单引号写两遍。网卡名里有中文、空格和星号(比如「本地连接* 12」),
// 全程用 -LiteralPath / -Name 传,不让它当通配符解释。
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// validNICBackupLines is the non-platform validation mirrored by the PowerShell
// script. Keeping this logic pure gives us deterministic tests without touching
// adapter bindings or the host firewall.
func validNICBackupLines(lines []string) bool {
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		p := strings.Split(line, "\t")
		if len(p) != 2 || strings.TrimSpace(p[0]) == "" || (p[1] != "True" && p[1] != "False") {
			return false
		}
		if _, ok := seen[p[0]]; ok {
			return false
		}
		seen[p[0]] = struct{}{}
	}
	return true
}

// NICIPv6Off 网卡的 IPv6 此刻是不是被我们关着的(有备份 = 关过还没还原)。
func NICIPv6Off() bool {
	_, err := os.Stat(nicBackup())
	return err == nil
}

// NICIPv6Manageable 这台机器能不能动物理网卡的 IPv6。
func NICIPv6Manageable() bool { return true }
