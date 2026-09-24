# 佛跳墙 真机验收:用**真正的安装包**从旧版原地升级(不断开),全程在后台持续绑物理网卡打直连,断言一次都没通。
#   .\installer-upgrade-test.ps1 -Sub <订阅> -OldSetup <0.7.1 安装包> -NewSetup <新安装包> [-LeakySetup <0.7.3 安装包>]
#
# 0.6.25-m29 ~ 0.7.1 的防火墙提供者绑着服务名:旧服务一停,它名下的过滤器就全部失效。从 0.7.4 起,安装包在停旧服务
# **之前**先用新版 exe 跑 guard arm,把闸装到不绑服务名的第二代提供者下,所以升级全程直连都被拦。
# vps-test.ps1 第 7 段是用命令行照这个顺序模拟的;这里跑的是安装包本身(PrepareToInstall 里的 ExtractTemporaryFile + Exec)。
#
# 给了 -LeakySetup 就先做一遍反向对照:0.7.1 → 0.7.3(没有预装闸)升级期间必须观察到直连漏出去 ——
# 证明探针的采样密度看得见这个窗口;看不见的话,正向那条"0 次漏"就证明不了任何事,按失败报。
#
# 清理不走卸载程序:它在卸载末尾用普通 MsgBox 问"要不要删数据",/SUPPRESSMSGBOXES 压不住,静默卸载会卡在那里。
# 直接跑服务的 uninstall(卸载程序做的也是这一步)。
param(
  [Parameter(Mandatory = $true)][string]$Sub,
  [Parameter(Mandatory = $true)][string]$OldSetup,
  [Parameter(Mandatory = $true)][string]$NewSetup,
  [string]$LeakySetup = ""
)
$ErrorActionPreference = "Continue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
chcp 65001 | Out-Null
$app = Join-Path $env:ProgramFiles "godusevpn"
$svc = Join-Path $app "godusevpn-svc.exe"
$cli = Join-Path $app "godusevpn-cli.exe"
$work = Join-Path $env:TEMP "godusevpn-upgrade-test"
New-Item -ItemType Directory -Force $work | Out-Null
$fail = 0
function Check($name, $ok, $detail) {
  if ($ok) { Write-Host ("[PASS] {0}  {1}" -f $name, $detail) -ForegroundColor Green }
  else { Write-Host ("[FAIL] {0}  {1}" -f $name, $detail) -ForegroundColor Red; $script:fail++ }
}
function Wait-Status($want, $seconds) {
  $deadline = (Get-Date).AddSeconds($seconds)
  while ((Get-Date) -lt $deadline) {
    $out = & $cli status 2>&1 | Out-String
    if ($out -match "状态:\s+$want") { return $out }
    Start-Sleep -Seconds 1
  }
  return (& $cli status 2>&1 | Out-String)
}
function OneLine($s) { return ($s.Trim() -replace "`r?`n", " | ") }
$physIp = (Find-NetRoute -RemoteIPAddress 1.1.1.1 -ErrorAction SilentlyContinue | Select-Object -First 1).IPAddress
function Direct-Http() { # 绑物理网卡发一个明文请求,返回状态码;2xx / 3xx = 通了(1.1.1.1 会把明文 301 到 https),被闸拦下是 000
  if (-not $physIp) { return "" }
  try { return (& curl.exe -s --interface $physIp -m 4 -o NUL -w "%{http_code}" "http://1.1.1.1/cdn-cgi/trace") } catch { return "000" }
}
function Get-WfpState() { # 整份 BFE 状态的小写文本;读不到返回空串
  $x = Join-Path $work "wfpstate.xml"
  Remove-Item $x -ErrorAction SilentlyContinue
  & netsh wfp show state file="$x" | Out-Null
  if (-not (Test-Path $x)) { return "" }
  return [System.IO.File]::ReadAllText($x).ToLower()
}
$gen1 = '6f6d9e2c-3a41-4b8e-9d55-676f64757365'
$gen2 = '6f6d9e2e-3a41-4b8e-9d55-676f64757365'
function Get-OurProviders() { # 名叫 godusevpn 的提供者;$null = 读不到,空数组 = 没有
  $x = Join-Path $work "wfpstate.xml"
  Remove-Item $x -ErrorAction SilentlyContinue
  & netsh wfp show state file="$x" | Out-Null
  if (-not (Test-Path $x)) { return $null }
  $raw = [System.IO.File]::ReadAllText($x)
  $doc = New-Object System.Xml.XmlDocument
  try { $doc.LoadXml("<godusevpn-wrap>" + [regex]::Replace($raw, '<\?xml[^>]*\?>', '') + "</godusevpn-wrap>") } catch { return $null }
  $found = @($doc.SelectNodes("//providers/item") | Where-Object { $_.displayData.name -eq "godusevpn" })
  return , $found
}
function Run-Setup($path, $tag) { # 静默跑一个安装包,返回退出码;Inno 的日志留在 $work 里,失败时打出来
  $log = Join-Path $work "setup-$tag.log"
  $p = Start-Process -FilePath $path -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/LOG=`"$log`"") -Wait -PassThru
  if ($p.ExitCode -ne 0 -and (Test-Path $log)) { Get-Content $log -Tail 40 | ForEach-Object { Write-Host "  | $_" } }
  return $p.ExitCode
}
function Start-Sampler($tag) { # 后台每 ~0.3 秒绑物理网卡打一次直连,记"时刻 状态码",直到停止文件出现
  $log = Join-Path $work "sampler-$tag.txt"
  $stop = Join-Path $work "sampler-$tag.stop"
  Remove-Item $log, $stop -ErrorAction SilentlyContinue
  $job = Start-Job -ArgumentList $physIp, $log, $stop -ScriptBlock {
    param($ip, $log, $stop)
    while (-not (Test-Path $stop)) {
      $c = & curl.exe -s --interface $ip -m 2 -o NUL -w "%{http_code}" "http://1.1.1.1/cdn-cgi/trace"
      Add-Content -Path $log -Value ((Get-Date).ToString("HH:mm:ss.fff") + " " + $c)
      Start-Sleep -Milliseconds 300
    }
  }
  return @{ job = $job; log = $log; stop = $stop }
}
function Stop-Sampler($s) {
  New-Item -ItemType File -Force $s.stop | Out-Null
  Wait-Job $s.job -Timeout 15 | Out-Null
  Remove-Job $s.job -Force -ErrorAction SilentlyContinue
  $lines = @(); if (Test-Path $s.log) { $lines = @(Get-Content $s.log) }
  $open = @($lines | Where-Object { ($_ -split ' ')[1] -match '^[23]' })
  return @{ total = $lines.Count; open = $open.Count; openLines = $open }
}
function Install-OldAndConnect($tag) {
  $rc = Run-Setup $OldSetup "old-$tag"
  Check "装旧版安装包($tag)" ($rc -eq 0) "exit=$rc"
  $v = (& $cli version 2>&1 | Out-String).Trim()
  Check "旧版服务在跑($tag)" ((& $svc status) -eq "running") $v
  & $cli profile $Sub 2>&1 | Out-Null
  & $cli connect | Out-Null
  Wait-Status "connected" 60 | Out-Null
  & $cli mode global | Out-Null
  Start-Sleep -Seconds 2
  $st = Wait-Status "connected" 30
  Check "旧版进入严格全局模式($tag)" (($st -match "状态:\s+connected") -and ($st -match "模式:\s+global")) (OneLine $st)
  $p = Get-OurProviders
  Check "旧版的提供者绑着服务名(场景前提,$tag)" (($null -ne $p) -and ($p.Count -gt 0) -and -not ($p | Where-Object { [string]::IsNullOrEmpty($_.serviceName) })) (($p | ForEach-Object { "key=" + $_.providerKey + " serviceName=[" + $_.serviceName + "]" }) -join ", ")
  $d = Direct-Http
  Check "旧版闸拦着直连($tag)" ($d -and $d -notmatch '^[23]') "http=$d"
}
function Cleanup($tag) {
  & $cli mode rule 2>&1 | Out-Null
  & $cli disconnect 2>&1 | Out-Null
  Start-Sleep -Seconds 2
  & $svc uninstall 2>&1 | Out-Null
  Start-Sleep -Seconds 2
  $s = Get-WfpState
  Check "清理后两代提供者都不在($tag)" ($s -and ($s -notmatch '6f6d9e2[cdef]-3a41-4b8e-9d55-676f64757365')) ""
  $d = Direct-Http
  Check "清理后直连恢复($tag)" ($d -match '^[23]') "http=$d"
}

Write-Host "== 0. 环境" -ForegroundColor Cyan
Write-Host "默认出口网卡地址: $physIp"
if ((& $svc status 2>$null) -eq "running") { & $svc uninstall 2>&1 | Out-Null } # 上一段验收留下的服务(正常情况下已经卸了)
$d0 = Direct-Http
Check "正控制:没装服务时绑物理网卡的直连是通的" ($d0 -match '^[23]') "http=$d0"
if ($d0 -notmatch '^[23]') { Write-Host "探针本身跑不通,下面的断言没有意义,中止"; exit 1 }

if ($LeakySetup) {
  Write-Host "== 1. 反向对照:0.7.1 → 0.7.3 原地升级(没有预装闸),必须看得见直连漏出去" -ForegroundColor Cyan
  Install-OldAndConnect "对照"
  $s = Start-Sampler "leaky"
  Start-Sleep -Seconds 3
  $rc = Run-Setup $LeakySetup "leaky"
  Check "0.7.3 安装包升级完成" ($rc -eq 0) "exit=$rc"
  $st = Wait-Status "connected" 90
  Start-Sleep -Seconds 3
  $r = Stop-Sampler $s
  Write-Host ("  采样 {0} 次,通了 {1} 次:{2}" -f $r.total, $r.open, (($r.openLines | Select-Object -First 8) -join "; "))
  Check "反向对照:没有预装闸的升级路径确实漏了(探针看得见窗口)" ($r.open -gt 0 -and $r.total -ge 10) ("采样 " + $r.total + " 次,通 " + $r.open + " 次")
  Cleanup "对照"
}

Write-Host "== 2. 正向:0.7.1 → 新版原地升级(安装包在停旧服务前预装第二代闸),直连一次都不能通" -ForegroundColor Cyan
Install-OldAndConnect "升级"
$s = Start-Sampler "new"
Start-Sleep -Seconds 3
$rc = Run-Setup $NewSetup "new"
Check "新版安装包升级完成" ($rc -eq 0) "exit=$rc"
$st = Wait-Status "connected" 90
Check "新版接管后自动恢复连接" ($st -match "状态:\s+connected") (OneLine $st)
Start-Sleep -Seconds 3
$r = Stop-Sampler $s
Write-Host ("  采样 {0} 次,通了 {1} 次" -f $r.total, $r.open)
Check "升级全程绑物理网卡的直连一次都没通" ($r.open -eq 0 -and $r.total -ge 10) ("采样 " + $r.total + " 次,通 " + $r.open + " 次" + $(if ($r.open) { ":" + (($r.openLines | Select-Object -First 8) -join "; ") } else { "" }))
$p = Get-OurProviders
Check "升级后只剩一个提供者且没绑服务名" (($null -ne $p) -and ($p.Count -eq 1) -and [string]::IsNullOrEmpty($p[0].serviceName)) (($p | ForEach-Object { "key=" + $_.providerKey + " serviceName=[" + $_.serviceName + "]" }) -join ", ")
$ws = Get-WfpState
Check "升级后第一代已收掉、第二代在" ($ws -and ($ws -notmatch $gen1) -and ($ws -match $gen2)) ""
try { $tc = (& curl.exe -s -m 15 -o NUL -w "%{http_code}" "https://1.1.1.1/cdn-cgi/trace") } catch { $tc = "000" }
Check "升级后经隧道照常" ($tc -eq "200") "http=$tc"
& $svc stop | Out-Null
Start-Sleep -Seconds 3
$d = Direct-Http
Check "升级后停服务闸仍在拦" ($d -and $d -notmatch '^[23]') "http=$d"
& $svc start | Out-Null
Wait-Status "connected" 60 | Out-Null
Cleanup "升级"

Write-Host ""
if ($fail -eq 0) { Write-Host "全部通过" -ForegroundColor Green } else {
  Write-Host "$fail 项失败" -ForegroundColor Red
  if (Test-Path $cli) { & $cli logs 40 }
}
exit $fail
