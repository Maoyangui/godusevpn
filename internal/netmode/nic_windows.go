package netmode

import (
	"errors"
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

// nicPending 待还原清单:还原时不在的网卡(拔掉的 USB 网卡、手机 USB 共享、找不到唯一一张同名的),动手前是开着的。
// 协议绑定的停用状态存在注册表里,网卡插回来时 IPv6 仍是关着的 —— 以前这一行直接丢掉,下次连接又把它如实记成
// "本来就是关的",从此永远开不回来。现在留在这里:它再出现时,还原(断开、恢复网络、卸载)把它开回去;
// 还没来得及还原就又连上时,停用那一步按清单把它的原值记成开着。和备份分开放,不让 NICIPv6Off 一直为真。
func nicPending() string { return filepath.Join(paths.DataDir(), "nic-ipv6-pending.txt") }

// psWriteLines 两段脚本共用:整份写到 .tmp、落盘,再换名顶替 —— 中途被杀也不会留下半份文件。
const psWriteLines = `
function Write-Lines($path, $lines) {
  $tmp = $path + '.tmp'
  $utf8 = New-Object System.Text.UTF8Encoding($false)
  $fs = New-Object System.IO.FileStream($tmp, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
  $sw = New-Object System.IO.StreamWriter($fs, $utf8)
  foreach ($line in $lines) { $sw.WriteLine($line) }
  $sw.Flush(); $fs.Flush($true); $sw.Dispose()
  Move-Item -LiteralPath $tmp -Destination $path -Force
}
`

// DisableNICIPv6 停用各网卡的 IPv6 绑定,隧道自己那张除外 —— 隧道要靠 v6 地址把 v6 流量接进来再拒绝,
// 关了它反而少一层防护。
//
// 备份是**并进去**而不是覆盖:已有备份说明上次停了还没还原(重建配置重连时守护进程故意不还原,
// 或者连接期间又冒出一张新网卡),这时候原来那些网卡都是关着的,整份重记会记成一片"关",
// 最后什么都开不回来;而整份跳过又会漏掉新网卡的原始状态,那张就被永久关着了。只补没记过的那些才两头都对。
//
// 同理,备份里某一行无效(手工改坏、截断、同名两行)也只能剔掉那一行:0.7.3 及以前是整份挪到 .bad 再重记,
// 于是我们之前关掉的网卡全被记成"本来就是关的",永远开不回来。无效行挪到 .bad、告警,其余照旧。
//
// 待还原清单里的网卡再出现、而备份里还没有它:它现在关着是我们上次关的,原值按清单记成开着(见 nicPending)。
func DisableNICIPv6(tunName string) error {
	var out string
	err := withNICLock(func() (err error) {
		out, err = runPS(disableScript(tunName))
		return err
	})
	if err != nil {
		return nicResult(tunName, []string{fmt.Sprintf("%v(%s)", err, out)})
	}
	return disableOutcome(tunName, out)
}

// disableScript 停用脚本。网卡名一律按 -ceq 精确比对后把绑定对象经管道交给 Disable-NetAdapterBinding:
// -Name 在这组命令里是通配符(WQL LIKE)—— 「本地连接* 2」会连带匹配「本地连接 2」,名字带 [ ] 的又匹配不到自己。
//
// 列网卡带 -IncludeHidden:Teredo(地址里编着公网 IPv4)、6to4、IP-HTTPS、Wi-Fi Direct 虚拟网卡这些隐藏接口
// 不在默认列表里,而判"确证在漏"的 net.Interfaces 看得见它们 —— 只停可见的那几张,隐藏接口上的公网 v6
// 就既停不掉又挡着连接(审计 G003)。还原那边本来就在 -IncludeHidden 的列表里找,两边这才对称。
// Not Present 的(拔掉的 USB 网卡留下的登记)跳过:它们没有地址,停了也不在复核里显示,只会每次报一条假的"没停成"。
func disableScript(tunName string) string {
	return psWriteLines + `
$ErrorActionPreference = 'Stop'
$tun = '` + psQuote(tunName) + `'
$absent = @{}
foreach ($x in @(Get-NetAdapter -IncludeHidden)) { if ($x.Status -eq 'Not Present') { $absent[$x.Name] = $true } }
$all = @(Get-NetAdapterBinding -ComponentID ms_tcpip6 -IncludeHidden | Where-Object { $_.Name -ne $tun -and -not $absent.ContainsKey($_.Name) })
$b = '` + psQuote(nicBackup()) + `'
$pf = '` + psQuote(nicPending()) + `'
$known = @{}
$corrupt = @()
$old = @()
if (Test-Path -LiteralPath $b) {
  foreach ($line in @(Get-Content -Encoding utf8 -LiteralPath $b)) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    $p = $line -split "` + "\t" + `", -1
    if ($p.Count -ne 2 -or [string]::IsNullOrWhiteSpace($p[0]) -or $p[1] -notmatch '^(True|False)$' -or $known.ContainsKey($p[0])) { $corrupt += $line; continue }
    $known[$p[0]] = $true
    $old += $line
  }
}
if ($corrupt.Count -gt 0) {
  try { Add-Content -LiteralPath ($b + '.bad') -Value $corrupt -Encoding UTF8; Write-Output ('GODUSEVPN-CORRUPT: ' + $corrupt.Count) }
  catch { Write-Output ('GODUSEVPN-CORRUPT-LOST: ' + $corrupt.Count) }
}
$pend = @{}
if (Test-Path -LiteralPath $pf) {
  foreach ($line in @(Get-Content -Encoding utf8 -LiteralPath $pf)) {
    $p = $line -split "` + "\t" + `", -1
    if ($p.Count -eq 2 -and $p[1] -eq 'True' -and -not [string]::IsNullOrWhiteSpace($p[0])) { $pend[$p[0]] = $true }
  }
}
$fresh = @($all | Where-Object { -not $known.ContainsKey($_.Name) })
$taken = @($fresh | Where-Object { $pend.ContainsKey($_.Name) } | ForEach-Object { $_.Name })
$new = @($fresh | ForEach-Object { if ($pend.ContainsKey($_.Name)) { $_.Name + "` + "\t" + `True" } else { $_.Name + "` + "\t" + `" + $_.Enabled } })
if ($new.Count -gt 0 -or $corrupt.Count -gt 0) {
  Write-Lines $b @($old + $new)
  $check = @(Get-Content -Encoding utf8 -LiteralPath $b)
  if ($check.Count -ne @($old + $new).Count) { throw 'IPv6 备份落盘校验失败' }
  for ($i = 0; $i -lt $check.Count; $i++) { if ($check[$i] -ne @($old + $new)[$i]) { throw 'IPv6 备份内容校验失败' } }
}
if ($taken.Count -gt 0) {
  # 原值已经进了备份,才从清单里拿掉(反过来的话中间被杀掉,这几张的原值就没了)
  $rest = @(Get-Content -Encoding utf8 -LiteralPath $pf | Where-Object { $taken -notcontains ($_ -split "` + "\t" + `", -1)[0] })
  if ($rest.Count -gt 0) { Write-Lines $pf $rest } else { Remove-Item -LiteralPath $pf -Force }
}
$failed = @()
foreach ($a in $all) {
  if (-not $a.Enabled) { continue }
  try { $a | Disable-NetAdapterBinding -ErrorAction Stop }
  catch { $failed += ($a.Name + ': ' + $_.Exception.Message) }
}
$now = @(Get-NetAdapterBinding -ComponentID ms_tcpip6 -IncludeHidden)
foreach ($a in $all) {
  if (@($now | Where-Object { $_.Name -ceq $a.Name -and $_.Enabled }).Count -gt 0) { $failed += ($a.Name + ': 停用后仍是启用状态') }
}
if ($failed.Count -gt 0) { Write-Output ('GODUSEVPN-PARTIAL: ' + ($failed -join '; ')) }
`
}

// disableOutcome 停用脚本跑完之后的收尾。
func disableOutcome(tunName, out string) error {
	corrupt := nicCorruptWarning(out)
	if corrupt != "" {
		recordNICLoss(corrupt) // 停用这一路剔掉的坏行:之后的还原看不见它们了,得落盘等用户看到
	}
	var res error
	if p := psMark(out, markPartial); p != "" {
		// 有网卡没停成(在脚本跑的这几秒里被拔掉 / 被系统销毁,或者驱动不让改绑定)。
		// m29 在这里整体抛错,而 prepare 拿它当连接前置 —— 于是一张 Wi-Fi Direct 虚拟网卡的生灭
		// 就能让人连不上。交给 nicResult:真有公网 v6 露在外面才算失败。
		res = nicResult(tunName, []string{p})
	} else {
		setNICWarning("")
	}
	if corrupt != "" {
		setNICWarning(joinWarn(NICWarning(), corrupt))
	}
	return res
}

// restoreOutcome 把还原脚本的输出翻成结果(纯函数,可测):
//   - 有 FAILED:普通错误(备份留着下次重试);
//   - 有无效行或挪进待还原清单的网卡:*NICRestoreIncomplete —— 能做的做完了,但那几张网卡的 IPv6 可能还关着,
//     调用方要说给用户(以前"网卡不在了"只进内存告警,日志与「恢复网络」弹窗照样说"已还原");
//   - 什么都没有:nil。
//
// warn 是要写进 NICWarning 的那句话。无效行与待还原那两句另由 RestoreNICIPv6 落盘(recordNICLoss)。
func restoreOutcome(out string) (warn string, err error) {
	var notes []string
	if c := nicCorruptWarning(out); c != "" {
		notes = append(notes, c)
	}
	if p := nicPendingWarning(out); p != "" {
		notes = append(notes, p)
	}
	warn = strings.Join(notes, ";")
	if f := psMark(out, markFailed); f != "" {
		// 无效行 / 待还原的说明不放进这条错误:它们已经单独落盘,「恢复网络」弹窗会单独说,
		// 两处都放的话同一段话会出现两遍、而且一句说"下次会自动再试"一句说"能还原的都还原了"。
		return warn, fmt.Errorf("还原网卡 IPv6: %s", f)
	}
	if warn != "" {
		return warn, &NICRestoreIncomplete{Detail: warn}
	}
	return "", nil
}

// nicPendingWarning 脚本报了"这几张网卡还原时不在、挪进了待还原清单"时给用户的那句话;没有就是空串。
func nicPendingWarning(out string) string {
	if names := psMark(out, markPending); names != "" {
		return "还原时这些网卡不在(已拔掉或找不到),IPv6 没能开回去: " + names + "。它们动手前是开着的,再插上后" +
			"下次断开连接(或「恢复网络」)时会自动开回;等不及就在「网络适配器属性」里把「Internet 协议版本 6」勾回来"
	}
	return ""
}

// nicCorruptWarning 脚本报了"备份里有几行无效"时给用户的那句话;没有就是空串。
// 挪到 .bad 成功与否分开说:挪失败的那几行原值真的丢了,不能骗人说"已挪到 .bad"。
func nicCorruptWarning(out string) string {
	if n := psMark(out, markCorrupt); n != "" {
		return "网卡 IPv6 备份里有 " + n + " 行无效,已挪到 " + nicBackup() + ".bad。那几行记的原始状态没了 —— " +
			"如果某些网卡的 IPv6 现在是关着的,需要手动在「网络适配器属性」里把「Internet 协议版本 6」勾回来"
	}
	if n := psMark(out, markCorruptLost); n != "" {
		return "网卡 IPv6 备份里有 " + n + " 行无效,已从备份里剔掉(挪到 .bad 也失败,内容没保住)。那几行记的原始状态没了 —— " +
			"如果某些网卡的 IPv6 现在是关着的,需要手动在「网络适配器属性」里把「Internet 协议版本 6」勾回来"
	}
	return ""
}

// runPS 脚本里用这几个记号把"部分失败""挪进待还原清单""无效行"带回来 —— PowerShell 的退出码只有一个,
// 区分不了"整件事崩了"和"有几张网卡没动成",而这两者在这里的处理完全不同。
const (
	markPartial     = "GODUSEVPN-PARTIAL: "
	markFailed      = "GODUSEVPN-FAILED: "
	markPending     = "GODUSEVPN-PENDING: "      // 这一轮才挪进待还原清单的网卡(还原时不在)
	markCorrupt     = "GODUSEVPN-CORRUPT: "      // 备份里有几行无效(已挪到 .bad),不算失败
	markCorruptLost = "GODUSEVPN-CORRUPT-LOST: " // 同上,但挪到 .bad 失败了,那几行没保住
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

// RestoreNICIPv6 按备份把 IPv6 绑定装回原样,顺带把待还原清单里又出现了的网卡开回去。
// 两样都没有就什么都不做(没动过,别去碰用户自己的设置)。
func RestoreNICIPv6() error {
	if !fileExists(nicBackup()) && !fileExists(nicPending()) {
		return nil // 没动过:连锁都不必拿
	}
	return withNICLock(restoreNICIPv6)
}

func restoreNICIPv6() error {
	if !fileExists(nicBackup()) && !fileExists(nicPending()) {
		return nil // 等锁的工夫,另一个进程已经还原完了
	}
	out, err := runPS(restoreScript())
	if err != nil {
		return fmt.Errorf("还原网卡 IPv6: %w(%s)", err, out)
	}
	warn, rerr := restoreOutcome(out)
	if warn != "" {
		setNICWarning(warn)
	}
	// 坏行、挪进清单的网卡一旦出现就已经从备份里拿掉了:哪怕同一轮还有网卡没还原成(返回普通错误、下次重试),
	// 也得现在就记,下次重试时备份里已经没有它们,不会再报。
	for _, note := range []string{nicCorruptWarning(out), nicPendingWarning(out)} {
		if note != "" {
			recordNICLoss(note)
		}
	}
	var inc *NICRestoreIncomplete
	if rerr != nil && !errors.As(rerr, &inc) {
		return rerr // 真失败:备份留着,下次重试
	}
	// 脚本里已经删过一次;这里兜一下,免得脚本那步被杀掉之后下次启动反复还原。
	if err := os.Remove(nicBackup()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 IPv6 备份: %w", err)
	}
	return rerr
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil || !os.IsNotExist(err) // 读不出来(权限等)也按"在"处理,交给脚本去报
}

// restoreScript 还原脚本。
//
// 只把记成 True 的网卡开回去。记成 False 的是"动手之前它本来就是关的"——停用那一步压根没碰过它们
// (只 Disable 了 Enabled 的),还原时更不该去动。m29 给 False 行加了主动 Disable,除了凭空多出一堆失败点,
// 没有任何作用。
//
// 网卡按 -ceq 精确比对名字,在 -IncludeHidden 的列表里找(见 disableScript:-Name 是通配符):
//   - 找到唯一一张就把绑定对象经管道开回去,隐藏 / Not Present 的也试;
//   - 开不成:网卡此刻在(不是 Not Present)就算失败,留在备份里下次重试;
//   - 找不到、找到不止一张、或者 Not Present 而开不成:挪进待还原清单(nicPending),等它再出现。
//     以前这一行直接丢掉("网卡已经不在了,无需还原"),插回来 IPv6 仍关着,下次连接又被记成"本来就是关的"。
//
// 逐张 try/catch:m29 在 $ErrorActionPreference='Stop' 下让一张网卡的失败中断整个循环,排在后面的一张都不还原。
// 备份行无效(手工改过、被截断、两行同名)的挪到 .bad 留证、如实告警、不挡任何事 —— 那几行的原值已经随文件
// 一起丢了,留在备份里只会让还原永远"失败"、备份永远删不掉、卸载永远跑不完。
func restoreScript() string {
	return psWriteLines + `
$ErrorActionPreference = 'Stop'
$f = '` + psQuote(nicBackup()) + `'
$pf = '` + psQuote(nicPending()) + `'
$lines = @()
if (Test-Path -LiteralPath $f) { $lines = @(Get-Content -Encoding utf8 -LiteralPath $f) }
$seen = @{}
$want = @()
$corrupt = @()
foreach ($line in $lines) {
  if ([string]::IsNullOrWhiteSpace($line)) { continue }
  $p = $line -split "` + "\t" + `", -1
  if ($p.Count -ne 2 -or [string]::IsNullOrWhiteSpace($p[0]) -or $p[1] -notmatch '^(True|False)$' -or $seen.ContainsKey($p[0])) {
    $corrupt += $line
    continue
  }
  $seen[$p[0]] = $true
  if ($p[1] -eq 'True') { $want += [pscustomobject]@{ Name = $p[0]; Line = $line; Pending = $false } }
}
if (Test-Path -LiteralPath $pf) {
  foreach ($line in @(Get-Content -Encoding utf8 -LiteralPath $pf)) {
    $p = $line -split "` + "\t" + `", -1
    if ($p.Count -ne 2 -or $p[1] -ne 'True' -or [string]::IsNullOrWhiteSpace($p[0]) -or $seen.ContainsKey($p[0])) { continue }
    $seen[$p[0]] = $true
    $want += [pscustomobject]@{ Name = $p[0]; Line = $line; Pending = $true }
  }
}
$binds = @(Get-NetAdapterBinding -ComponentID ms_tcpip6 -IncludeHidden)
$ads = @(Get-NetAdapter -IncludeHidden)
$left = @()
$pending = @()
$moved = @()
$failed = @()
$done = @()
foreach ($w in $want) {
  $m = @($binds | Where-Object { $_.Name -ceq $w.Name })
  $why = '找不到这张网卡'
  if ($m.Count -eq 1) {
    try { $m[0] | Enable-NetAdapterBinding -ErrorAction Stop; $done += $w; continue } catch { $why = $_.Exception.Message }
  }
  $present = @($ads | Where-Object { $_.Name -ceq $w.Name -and $_.Status -ne 'Not Present' }).Count -eq 1
  if ($m.Count -eq 1 -and $present -and -not $w.Pending) { $failed += ($w.Name + ': ' + $why); $left += $w.Line; continue }
  $pending += ($w.Name + "` + "\t" + `True")
  if (-not $w.Pending) { $moved += $w.Name }
}
if ($done.Count -gt 0) {
  $now = @(Get-NetAdapterBinding -ComponentID ms_tcpip6 -IncludeHidden)
  foreach ($w in $done) {
    if (@($now | Where-Object { $_.Name -ceq $w.Name -and $_.Enabled }).Count -eq 1) { continue }
    if ($w.Pending) { $pending += ($w.Name + "` + "\t" + `True") } else { $failed += ($w.Name + ': 还原后仍是停用状态'); $left += $w.Line }
  }
}
if ($corrupt.Count -gt 0) {
  # 先把无效行留证、再动备份:反过来的话中间被杀掉,那几行就无声无息没了
  try { Add-Content -LiteralPath ($f + '.bad') -Value $corrupt -Encoding UTF8; Write-Output ('GODUSEVPN-CORRUPT: ' + $corrupt.Count) }
  catch { Write-Output ('GODUSEVPN-CORRUPT-LOST: ' + $corrupt.Count) }
}
# 同理:先落待还原清单、再动备份
if ($pending.Count -gt 0) { Write-Lines $pf $pending } elseif (Test-Path -LiteralPath $pf) { Remove-Item -LiteralPath $pf -Force }
if ($left.Count -gt 0) { Write-Lines $f $left } else { Remove-Item -LiteralPath $f -Force -ErrorAction SilentlyContinue }
if ($failed.Count -gt 0) { Write-Output ('GODUSEVPN-FAILED: ' + ($failed -join '; ')) }
if ($moved.Count -gt 0) { Write-Output ('GODUSEVPN-PENDING: ' + ($moved -join ', ')) }
`
}

func runPS(script string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// psQuote PowerShell 单引号字符串里的转义:单引号写两遍。文件路径全程用 -LiteralPath 传;网卡名里有中文、空格、
// 星号和方括号(比如「本地连接* 12」),而 NetAdapter 那组命令的 -Name 按通配符解释,所以网卡一律按 -ceq 精确比对。
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
