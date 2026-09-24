# 佛跳墙 真机验收:用**真正的安装包**从旧版原地升级(不断开),全程在后台持续绑物理网卡打直连,断言一次都没通。
#   .\installer-upgrade-test.ps1 -Sub <订阅> -OldSetup <0.7.1 安装包> -NewSetup <新安装包> -NewBin <新版裸二进制目录>
#
# 另带一段**测量**(如实打印,不判成败):装 0.7.1(提供者挂着服务名)、连上严格全局、sc stop,5 秒与 25 秒
# 两个时间点各打几次直连,并用新版的 guard status 数被系统标成停用的过滤器条数。2026-09-24 第一次实测:
# 0 条被停用、直连全拦 —— "绑服务名 → 服务一停闸就失效"这个说法不成立,真实语义见 internal/netmode/wfp 的 baseProvider。
#
# 做不到的:模拟开机(BFE 重新加载持久规则、我们的服务还没起来)。运行中的 Windows 上 BFE 停不下来 ——
# 它的依赖服务 mpssvc(Windows 防火墙)不接受停止控制,改了服务权限也一样。开机这一段只能靠真重启验证。
#
# 最后用真正的卸载程序(unins000.exe /VERYSILENT)、不先断开、从严格全局直接卸载:撤闸在用户确认之后、删文件之前
# (usUninstall);静默卸载不再问"要不要删数据"。找不到卸载程序时退回直接跑服务的 uninstall。
param(
  [Parameter(Mandatory = $true)][string]$Sub,
  [Parameter(Mandatory = $true)][string]$OldSetup,
  [Parameter(Mandatory = $true)][string]$NewSetup,
  [Parameter(Mandatory = $true)][string]$NewBin
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
function Note($name, $detail) { Write-Host ("[MEASURE] {0}  {1}" -f $name, $detail) -ForegroundColor Yellow } # 别叫 Measure:那是 Measure-Object 的内置别名,别名优先于函数
$script:t0 = Get-Date
$script:logDir = Join-Path $env:ProgramData "godusevpn\logs"
$script:crashLog = Join-Path $script:logDir "crash.log"
# crash.log 在同一台机器上反复跑会累积,只看这次验收新增的那部分
$script:crashBase = 0
if (Test-Path $script:crashLog) { $script:crashBase = @(Get-Content $script:crashLog -Encoding UTF8).Count }
# 服务意外退出:服务管理器在系统日志里记 7031 / 7034("意外终止")。服务崩了会被恢复策略几秒后拉起,
# 后面的检查也许照样通过 —— 单独抓出来,不让一次崩溃藏在全绿里。返回 $null 表示读不到日志(不能当成"没崩")。
function Svc-Crashes($since) {
  try {
    $ev = Get-WinEvent -FilterHashtable @{ LogName = 'System'; ProviderName = 'Service Control Manager'; Id = 7031, 7034; StartTime = $since } -ErrorAction Stop |
      Where-Object { $_.Message -match '佛跳墙|godusevpn' }
    return ,@($ev)
  } catch {
    if ($_.FullyQualifiedErrorId -like 'NoMatchingEventsFound*') { return ,@() }
    Write-Host ("  (读系统事件日志失败:{0})" -f $_.Exception.Message)
    return $null
  }
}
# 这次验收期间 crash.log 新增的内容(抬头"== … 启动"与空行除外)。期间要是被轮转过(启动时超过 1MB 挪成 .1)——
# 那恰恰是一次崩溃把文件推过了 1MB,现场在 .1 里:.1 基线之后的部分也算,再加上新 crash.log 的全部。
function New-CrashText() {
  $old = "$script:crashLog.1"
  $lines = @()
  if ((Test-Path $old) -and ((Get-Item $old).LastWriteTime -ge $script:t0)) {
    $lines += @(Get-Content $old -Encoding UTF8 | Select-Object -Skip $script:crashBase)
    if (Test-Path $script:crashLog) { $lines += @(Get-Content $script:crashLog -Encoding UTF8) }
  } elseif (Test-Path $script:crashLog) {
    $all = @(Get-Content $script:crashLog -Encoding UTF8)
    $skip = $script:crashBase
    if ($all.Count -lt $skip) { $skip = 0 }
    $lines = @($all | Select-Object -Skip $skip)
  }
  return ,@($lines | Where-Object { $_.Trim() -ne '' -and $_ -notmatch '^== ' })
}
# 进程级崩溃的标志:Go 的 panic / fatal error、Windows 异常、goroutine 栈(只靠 SetCrashOutput 时 fatal 的原因行
# 不在,栈在)。注意是 "panic: " 带冒号:sing-box 启动失败时 println 的 "panic on early start: …" 不是进程崩溃。
# 其余的是进程没崩时写到标准错误的东西,单独报、不算崩溃。
$script:crashMark = '^(panic: |fatal error:|Exception 0x|goroutine \d+ \[|runtime stack:)'
function Crash-Lines() { $t = New-CrashText; return ,@($t | Where-Object { $_ -match $script:crashMark }) }
function Stderr-Other() { $t = New-CrashText; return ,@($t | Where-Object { $_ -notmatch $script:crashMark }) }
# 失败时的现场:直接读日志文件(服务可能已经卸掉或崩了,不能再靠命令行去问服务),再列服务管理器事件
# crash.log 里每处崩溃标志("Exception 0x…"、"fatal error:"、"panic: ")往后 120 行:出事的协程和它的 Go 调用栈
# 在最前面,后面跟着上千行别的协程 —— 只看文件末尾会把它们截掉(v0.7.5 那次就只剩别的协程)。
function Crash-Heads($p) {
  $all = @(Get-Content $p -Encoding UTF8)
  $hits = @()
  for ($i = 0; $i -lt $all.Count; $i++) { if ($all[$i] -match '^(Exception 0x|fatal error:|panic: )') { $hits += $i } }
  if ($hits.Count -eq 0) { $all | Select-Object -Last 60; return }
  foreach ($h in @($hits | Select-Object -Last 3)) {
    $e = [Math]::Min($all.Count - 1, $h + 119)
    Write-Host ("---- 第 {0} 行起 ----" -f ($h + 1)) -ForegroundColor Cyan
    $all[$h..$e]
  }
}
function Dump-Evidence() {
  foreach ($n in @(@("service.log", 300), @("core.log", 80))) {
    $p = Join-Path $script:logDir $n[0]
    if (Test-Path $p) {
      Write-Host ("== {0}(最近 {1} 行)" -f $n[0], $n[1]) -ForegroundColor Cyan
      Get-Content $p -Tail $n[1] -Encoding UTF8
    }
  }
  foreach ($n in @("crash.log.1", "crash.log")) {
    $p = Join-Path $script:logDir $n
    if ((Test-Path $p) -and ((Get-Item $p).LastWriteTime -ge $script:t0)) {
      Write-Host ("== {0}(这次验收期间写过;崩溃标志往后的部分)" -f $n) -ForegroundColor Cyan
      Crash-Heads $p
    }
  }
  Write-Host "== 服务管理器事件(本次验收期间)" -ForegroundColor Cyan
  try {
    Get-WinEvent -FilterHashtable @{ LogName = 'System'; ProviderName = 'Service Control Manager'; StartTime = $script:t0 } -ErrorAction Stop |
      Where-Object { $_.Message -match '佛跳墙|godusevpn' } | Sort-Object TimeCreated |
      ForEach-Object { "{0} {1} {2}" -f $_.TimeCreated.ToString('HH:mm:ss.fff'), $_.Id, ($_.Message -replace "`r?`n", ' ') }
  } catch { Write-Host ("  (没有 / 读不到:{0})" -f $_.Exception.Message) }
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
function Provider-Flags() { # 我们的提供者各带哪些标志(FWPM_PROVIDER_FLAG_DISABLED = 开机时被 BFE 停用)
  $p = Get-OurProviders
  if ($null -eq $p) { return "(读不到 WFP 状态)" }
  if ($p.Count -eq 0) { return "(没有我们的提供者)" }
  return (($p | ForEach-Object { "key=" + $_.providerKey + " serviceName=[" + $_.serviceName + "] flags=[" + ((@($_.flags.item) | Where-Object { $_ }) -join "+") + "]" }) -join ", ")
}
function Cleanup($tag) {
  $unins = Join-Path $app "unins000.exe"
  if (Test-Path $unins) {
    # 用真正的卸载程序、不先断开,从严格全局直接静默卸载:撤闸在卸载程序的 usUninstall 那一步(用户确认之后、
    # 删文件之前)。Inno 会把自己拷到临时目录再跑,原进程先退出,所以等程序文件真的没了再往下查。
    Start-Process -FilePath $unins -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART') -Wait
    for ($i = 0; $i -lt 90 -and (Test-Path $svc); $i++) { Start-Sleep -Seconds 1 }
    Check "卸载程序删掉了程序文件($tag)" (-not (Test-Path $svc)) ""
    Check "卸载后服务不存在($tag)" ($null -eq (Get-Service -Name godusevpn -ErrorAction SilentlyContinue)) ""
  } else {
    & $cli mode rule 2>&1 | Out-Null
    & $cli disconnect 2>&1 | Out-Null
    Start-Sleep -Seconds 2
    & $svc uninstall 2>&1 | Out-Null
  }
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
Note "停服务前" (GuardProbe)
& sc.exe stop godusevpn | Out-Null
Start-Sleep -Seconds 5
$a = Direct-Many 4
Note "sc stop 后 5 秒" ("服务=" + (& $svc status) + ";直连 " + ($a -join ",") + ";" + (GuardProbe))
Start-Sleep -Seconds 20
$b = Direct-Many 4
Note "sc stop 后 25 秒" ("直连 " + ($b -join ",") + ";" + (GuardProbe))
$leak1 = (Count-Open $a) + (Count-Open $b)
if ($leak1 -gt 0) { Note "结论" ("绑服务名的提供者在服务停止后失效(直连漏了 " + $leak1 + " 次)—— 和 2026-09-24 的实测不一致,要查") }
else { Note "结论" "服务停止后直连全程被拦、0 条被停用 —— 与 2026-09-24 的实测一致:停服务不会让绑服务名的提供者失效" }
& sc.exe start godusevpn | Out-Null
$st = Wait-Status "connected" 60
Check "0.7.1 服务重新起来并连上" ($st -match "状态:\s+connected") (OneLine $st)

Write-Host "== 2. 0.7.1 → 新版原地升级(不断开),直连一次都不能通" -ForegroundColor Cyan
$s = Start-Sampler "upgrade"
Start-Sleep -Seconds 3
$rc = Run-Setup $NewSetup "new"
Check "新版安装包升级完成" ($rc -eq 0) "exit=$rc"
$script:tNew = Get-Date # 这之后服务就是新版的了:它的意外终止要断言(之前那次是安装程序结束旧服务,不算)
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

& $svc start | Out-Null
$st = Wait-Status "connected" 60
Check "新版服务重新起来并连上" ($st -match "状态:\s+connected") (OneLine $st)
Cleanup "升级"
# 整个过程只打印:升级时安装程序可能自己结束旧服务的进程,那也会记成"意外终止"
$ev = Svc-Crashes $script:t0
if ($null -ne $ev) { Note "服务意外终止(系统日志 7031/7034,整个过程)" ("{0} 次 {1}" -f $ev.Count, (($ev | ForEach-Object { $_.TimeCreated.ToString("HH:mm:ss.fff") }) -join ",")) }
# 新版接管之后的要断言:DLL 自己的线程里崩掉时 crash.log 一个字都没有,只有这条系统日志
if ($script:tNew) {
  $evNew = Svc-Crashes $script:tNew
  if ($null -eq $evNew) { Check "新版接管后服务没有意外退出过(系统日志 7031/7034)" $false "读不到系统事件日志" }
  else { Check "新版接管后服务没有意外退出过(系统日志 7031/7034)" ($evNew.Count -eq 0) (($evNew | ForEach-Object { $_.TimeCreated.ToString("HH:mm:ss.fff") + " #" + $_.Id }) -join " | ") }
}
$cl = Crash-Lines
Check "crash.log 里没有新的崩溃" ($cl.Count -eq 0) (($cl | Select-Object -First 3) -join " | ")
$so = Stderr-Other
if ($so.Count -gt 0) { Note "crash.log 里有进程没崩时写的标准错误输出" ("{0} 行:{1}" -f $so.Count, (($so | Select-Object -First 3) -join " | ")) }

Write-Host ""
if ($fail -eq 0) { Write-Host "全部通过" -ForegroundColor Green } else {
  Write-Host "$fail 项失败" -ForegroundColor Red
  Dump-Evidence
}
exit $fail
