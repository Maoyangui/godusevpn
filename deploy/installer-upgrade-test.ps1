# 佛跳墙 真机验收:用**真正的安装包**从旧版原地升级(不断开),全程在后台持续绑物理网卡打直连,断言一次都没通。
#   .\installer-upgrade-test.ps1 -Sub <订阅> -OldSetup <0.7.1 安装包> -NewSetup <新安装包> -NewBin <新版裸二进制目录> [-BfeRestart]
#
# 另带两段**测量**(把结果如实打出来,不预设结论):
#   测量一  0.6.25-m29 ~ 0.7.1 的防火墙提供者绑着服务名。0.7.2 起的修复都建立在"服务一停,BFE 就把它名下的过滤器
#          全部停用"这个前提上,但这个前提从没在真机上直接看过。这里装 0.7.1、连上严格全局、直接 sc stop,
#          分两个时间点打直连,并用新版的 guard status 数一下被系统标成停用的过滤器有几条。
#   测量二  (-BfeRestart)"电脑重启也不能漏"。开机时 BFE 会重新加载持久规则;有资料说没挂服务名、或服务不是
#          自动启动的提供者,它名下的过滤器在 BFE 启动时会被停用。这里让新版连上严格全局后停掉服务(模拟开机时
#          服务还没起来),再重启 BFE,全程采样看直连漏不漏,并数被停用的条数。
#
# 清理不走卸载程序:它在卸载末尾用普通 MsgBox 问"要不要删数据",/SUPPRESSMSGBOXES 压不住,静默卸载会卡在那里。
# 直接跑服务的 uninstall(卸载程序做的也是这一步)。
param(
  [Parameter(Mandatory = $true)][string]$Sub,
  [Parameter(Mandatory = $true)][string]$OldSetup,
  [Parameter(Mandatory = $true)][string]$NewSetup,
  [Parameter(Mandatory = $true)][string]$NewBin,
  [switch]$BfeRestart
)
$ErrorActionPreference = "Continue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
chcp 65001 | Out-Null
$app = Join-Path $env:ProgramFiles "godusevpn"
$svc = Join-Path $app "godusevpn-svc.exe"
$cli = Join-Path $app "godusevpn-cli.exe"
$probe = Join-Path $NewBin "godusevpn-svc.exe" # 新版的 guard status 两代过滤器都认,并会报被停用的条数(只读)
$work = Join-Path $env:TEMP "godusevpn-upgrade-test"
New-Item -ItemType Directory -Force $work | Out-Null
$fail = 0
function Check($name, $ok, $detail) {
  if ($ok) { Write-Host ("[PASS] {0}  {1}" -f $name, $detail) -ForegroundColor Green }
  else { Write-Host ("[FAIL] {0}  {1}" -f $name, $detail) -ForegroundColor Red; $script:fail++ }
}
function Measure($name, $detail) { Write-Host ("[MEASURE] {0}  {1}" -f $name, $detail) -ForegroundColor Yellow }
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
function GuardProbe() { return (OneLine (& $probe guard status 2>&1 | Out-String)) }
$physIp = (Find-NetRoute -RemoteIPAddress 1.1.1.1 -ErrorAction SilentlyContinue | Select-Object -First 1).IPAddress
function Direct-Http() { # 绑物理网卡发一个明文请求,返回状态码;2xx / 3xx = 通了(1.1.1.1 会把明文 301 到 https),被闸拦下是 000
  if (-not $physIp) { return "" }
  try { return (& curl.exe -s --interface $physIp -m 4 -o NUL -w "%{http_code}" "http://1.1.1.1/cdn-cgi/trace") } catch { return "000" }
}
function Direct-Many($n) { $r = @(); for ($i = 0; $i -lt $n; $i++) { $r += (Direct-Http); Start-Sleep -Milliseconds 500 }; return , $r }
function Count-Open($codes) { return @($codes | Where-Object { $_ -match '^[23]' }).Count }
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
function Providers-Detail($p) {
  if ($null -eq $p) { return "(读不到 WFP 状态)" }
  if ($p.Count -eq 0) { return "(没有我们的提供者)" }
  return (($p | ForEach-Object { "key=" + $_.providerKey + " serviceName=[" + $_.serviceName + "]" }) -join ", ")
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

Write-Host "== 0. 环境与探针自检" -ForegroundColor Cyan
Write-Host ("默认出口网卡地址: " + $physIp)
if ((& $svc status 2>$null) -eq "running") { & $svc uninstall 2>&1 | Out-Null } # 上一段验收留下的服务(正常情况下已经卸了)
$d0 = Direct-Http
Check "正控制:没装服务时绑物理网卡的直连是通的" ($d0 -match '^[23]') "http=$d0"
if ($d0 -notmatch '^[23]') { Write-Host "探针本身跑不通,下面的断言没有意义,中止"; exit 1 }
# 后台采样器自己也要验:没闸的时候它必须采到"通"。否则下面"0 次漏"可能只是采样器坏了
$s = Start-Sampler "selftest"
Start-Sleep -Seconds 4
$r = Stop-Sampler $s
Check "采样器自检:没闸时采得到直连通" ($r.total -ge 5 -and $r.open -ge 5) ("采样 " + $r.total + " 次,通 " + $r.open + " 次")

Write-Host "== 1. 装 0.7.1(提供者绑服务名)并连上严格全局" -ForegroundColor Cyan
$rc = Run-Setup $OldSetup "old"
Check "装 0.7.1 安装包" ($rc -eq 0) "exit=$rc"
Check "0.7.1 服务在跑" ((& $svc status) -eq "running") ((& $cli version 2>&1 | Out-String).Trim())
& $cli profile $Sub 2>&1 | Out-Null
& $cli connect | Out-Null
Wait-Status "connected" 60 | Out-Null
& $cli mode global | Out-Null
Start-Sleep -Seconds 2
$st = Wait-Status "connected" 30
Check "0.7.1 进入严格全局模式" (($st -match "状态:\s+connected") -and ($st -match "模式:\s+global")) (OneLine $st)
$p = Get-OurProviders
Check "0.7.1 的提供者绑着服务名(场景前提)" (($null -ne $p) -and ($p.Count -gt 0) -and -not ($p | Where-Object { [string]::IsNullOrEmpty($_.serviceName) })) (Providers-Detail $p)
$d = Direct-Http
Check "0.7.1 的闸拦着直连" ($d -and $d -notmatch '^[23]') "http=$d"

Write-Host "== 测量一:绑服务名的提供者,服务停了以后过滤器还拦不拦" -ForegroundColor Cyan
Measure "停服务前" (GuardProbe)
& sc.exe stop godusevpn | Out-Null
Start-Sleep -Seconds 5
$a = Direct-Many 4
Measure "sc stop 后 5 秒" ("服务=" + (& $svc status) + ";直连 " + ($a -join ",") + ";" + (GuardProbe))
Start-Sleep -Seconds 20
$b = Direct-Many 4
Measure "sc stop 后 25 秒" ("直连 " + ($b -join ",") + ";" + (GuardProbe))
$leak1 = (Count-Open $a) + (Count-Open $b)
if ($leak1 -gt 0) { Measure "结论" "绑服务名的提供者在服务停止后失效(直连漏了 $leak1 次)—— 0.7.2 起的修复针对的是真问题" }
else { Measure "结论" "服务停止后直连全程被拦 —— 这台机器上没有复现「绑服务名 → 服务一停闸就失效」" }
& sc.exe start godusevpn | Out-Null
$st = Wait-Status "connected" 60
Check "0.7.1 服务重新起来并连上" ($st -match "状态:\s+connected") (OneLine $st)

Write-Host "== 2. 0.7.1 → 新版原地升级(安装包在停旧服务前预装第二代闸),直连一次都不能通" -ForegroundColor Cyan
$s = Start-Sampler "upgrade"
Start-Sleep -Seconds 3
$rc = Run-Setup $NewSetup "new"
Check "新版安装包升级完成" ($rc -eq 0) "exit=$rc"
$st = Wait-Status "connected" 90
Check "新版接管后自动恢复连接" ($st -match "状态:\s+connected") (OneLine $st)
Start-Sleep -Seconds 3
$r = Stop-Sampler $s
Check "升级全程绑物理网卡的直连一次都没通" ($r.open -eq 0 -and $r.total -ge 10) ("采样 " + $r.total + " 次,通 " + $r.open + " 次" + $(if ($r.open) { ":" + (($r.openLines | Select-Object -First 8) -join "; ") } else { "" }))
$p = Get-OurProviders
Check "升级后只剩一个提供者且没绑服务名" (($null -ne $p) -and ($p.Count -eq 1) -and [string]::IsNullOrEmpty($p[0].serviceName)) (Providers-Detail $p)
$ws = Get-WfpState
Check "升级后第一代已收掉、第二代在" ($ws -and ($ws -notmatch $gen1) -and ($ws -match $gen2)) ""
try { $tc = (& curl.exe -s -m 15 -o NUL -w "%{http_code}" "https://1.1.1.1/cdn-cgi/trace") } catch { $tc = "000" }
Check "升级后经隧道照常" ($tc -eq "200") "http=$tc"
& $svc stop | Out-Null
Start-Sleep -Seconds 3
$d = Direct-Http
Check "升级后停服务闸仍在拦" ($d -and $d -notmatch '^[23]') ("http=" + $d + ";" + (GuardProbe))

if ($BfeRestart) {
  Write-Host "== 测量二:服务停着的时候 BFE 重新加载持久规则(相当于开机那一刻),闸还拦不拦" -ForegroundColor Cyan
  Measure "BFE 重启前(服务停着)" (GuardProbe)
  $s = Start-Sampler "bfe"
  Start-Sleep -Seconds 2
  $t0 = Get-Date
  try { Restart-Service -Name BFE -Force -ErrorAction Stop } catch { Measure "重启 BFE 出错" $_.Exception.Message }
  $bfe = (Get-Service BFE).Status
  Start-Sleep -Seconds 15
  $r = Stop-Sampler $s
  Measure "BFE 重启" ("状态=" + $bfe + ";耗时 " + [int]((Get-Date) - $t0).TotalSeconds + " 秒;采样 " + $r.total + " 次,通 " + $r.open + " 次" + $(if ($r.open) { ":" + (($r.openLines | Select-Object -First 8) -join "; ") } else { "" }))
  Measure "BFE 重启后" (GuardProbe)
  $d = Direct-Many 4
  Measure "BFE 重启后直连" ($d -join ",")
  Check "BFE 重新加载持久规则的过程中与之后,直连一次都没通(服务停着)" ($r.total -ge 10 -and $r.open -eq 0 -and (Count-Open $d) -eq 0) ("采样 " + $r.total + " 次,通 " + $r.open + " 次;之后 " + ($d -join ","))
  foreach ($n in @("mpssvc", "IKEEXT", "PolicyAgent")) { Start-Service -Name $n -ErrorAction SilentlyContinue } # Restart-Service -Force 不会把依赖它的服务拉回来
}
& $svc start | Out-Null
$st = Wait-Status "connected" 60
Check "新版服务重新起来并连上" ($st -match "状态:\s+connected") (OneLine $st)
Cleanup "升级"

Write-Host ""
if ($fail -eq 0) { Write-Host "全部通过" -ForegroundColor Green } else {
  Write-Host "$fail 项失败" -ForegroundColor Red
  if (Test-Path $cli) { & $cli logs 40 }
}
exit $fail
