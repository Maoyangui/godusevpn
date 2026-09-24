# 佛跳墙 重启压力(在 Windows 上以管理员 PowerShell 运行)
#   .\restart-stress.ps1 -Sub "https://面板/sub/用户名" [-Bin "C:\godusevpn"] [-Rounds 30]
# 严格全局(全局模式 + 全局禁直连)下反复"停服务 → 起服务 → 自动重连",每轮连上后等几秒,看服务有没有意外退出。
# 为什么有这一段:v0.7.5 的真机验收里两次抓到同一个崩溃 —— 服务重启时闸还在、启动时重装闸、自动重连,
# 刚连上 0~1 秒进程就没了(DLL 里写空指针 +0x48 的访问违例),几秒后被服务管理器拉起。普通验收里一次运行
# 只重启一两回,5~10 次运行才撞上一次;这里连着重启几十回。撞上就把 crash.log 那一段的开头打出来(出事的
# 协程和它的 Go 调用栈在最前面)。
param(
  [Parameter(Mandatory = $true)][string]$Sub,
  [string]$Bin = "C:\godusevpn",
  [int]$Rounds = 30
)
$ErrorActionPreference = "Continue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
chcp 65001 | Out-Null
$svc = Join-Path $Bin "godusevpn-svc.exe"
$cli = Join-Path $Bin "godusevpn-cli.exe"
$logDir = Join-Path $env:ProgramData "godusevpn\logs"
$crashLog = Join-Path $logDir "crash.log"

function Wait-Status($want, $seconds) {
  $deadline = (Get-Date).AddSeconds($seconds)
  while ((Get-Date) -lt $deadline) {
    $out = & $cli status 2>&1 | Out-String
    if ($out -match "状态:\s+$want") { return $out }
    Start-Sleep -Milliseconds 500
  }
  return (& $cli status 2>&1 | Out-String)
}
# 服务意外终止(服务管理器在系统日志里记 7031 / 7034)。$null = 读不到日志。
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
# 每处崩溃标志("Exception 0x…"、"fatal error:"、"panic: ")往后打 120 行:出事的协程和它的调用栈在这里。
function Crash-Heads() {
  if (-not (Test-Path $crashLog)) { Write-Host "  (没有 crash.log)"; return }
  $all = @(Get-Content $crashLog -Encoding UTF8)
  $hits = @()
  for ($i = 0; $i -lt $all.Count; $i++) { if ($all[$i] -match '^(Exception 0x|fatal error:|panic: )') { $hits += $i } }
  if ($hits.Count -eq 0) { Write-Host "  (crash.log 里没有崩溃标志;最后 60 行:)"; $all | Select-Object -Last 60; return }
  foreach ($h in @($hits | Select-Object -Last 3)) {
    $e = [Math]::Min($all.Count - 1, $h + 119)
    Write-Host ("---- crash.log 第 {0} 行起 ----" -f ($h + 1)) -ForegroundColor Cyan
    $all[$h..$e]
  }
}

Write-Host "== 准备:装服务、设订阅、连上、切到严格全局" -ForegroundColor Cyan
& $svc uninstall 2>$null | Out-Null
& $svc install | Out-Null
if ((& $svc status) -ne "running") { Write-Host "服务没起来" -ForegroundColor Red; exit 1 }
& $cli profile $Sub 2>&1 | Out-Null
& $cli connect | Out-Null
$st = Wait-Status "connected" 90
if ($st -notmatch "状态:\s+connected") { Write-Host "连不上:$st" -ForegroundColor Red; & $svc uninstall | Out-Null; exit 1 }
& $cli mode global | Out-Null
Start-Sleep -Seconds 2
$st = Wait-Status "connected" 30
$gs = & $svc guard status 2>&1 | Out-String
if (($st -notmatch "模式:\s+global") -or ($gs -notmatch "开着")) { Write-Host ("没进严格全局:" + $st + $gs) -ForegroundColor Red; & $svc uninstall | Out-Null; exit 1 }
Write-Host ("严格全局就绪:" + (($gs -split "`r?`n")[0]))

# 后台一直经隧道发请求:两次崩溃时都有活跃流量(内核在起大量协程、复用栈内存),空转时更难撞上
$traffic = Start-Job -ScriptBlock {
  while ($true) {
    & curl.exe -s -m 5 -o NUL "https://1.1.1.1/cdn-cgi/trace" 2>$null
    & curl.exe -s -m 5 -o NUL "https://www.gstatic.com/generate_204" 2>$null
    Start-Sleep -Milliseconds 200
  }
}
$t0 = Get-Date
$seen = 0
$notConnected = 0
Write-Host ("== 重启 {0} 轮" -f $Rounds) -ForegroundColor Cyan
for ($i = 1; $i -le $Rounds; $i++) {
  & $svc stop | Out-Null
  & $svc start | Out-Null
  $st = Wait-Status "connected" 60
  $ok = $st -match "状态:\s+connected"
  if (-not $ok) { $notConnected++ }
  Start-Sleep -Seconds 3 # 抓到的两次都崩在连上后 0~1 秒
  $ev = Svc-Crashes $t0
  $n = 0
  if ($null -ne $ev) { $n = $ev.Count }
  $mark = ""
  if ($n -gt $seen) {
    $mark = "  ← 服务意外终止 " + (($ev | Select-Object -First ($n - $seen) | ForEach-Object { $_.TimeCreated.ToString('HH:mm:ss.fff') + " #" + $_.Id }) -join ", ")
    $seen = $n
  }
  Write-Host ("第 {0} 轮:{1}{2}" -f $i, $(if ($ok) { "connected" } else { "没连上" }), $mark)
}

Write-Host "== 收尾" -ForegroundColor Cyan
Stop-Job $traffic -ErrorAction SilentlyContinue
Remove-Job $traffic -Force -ErrorAction SilentlyContinue
$ev = Svc-Crashes $t0
& $cli disconnect | Out-Null
Start-Sleep -Seconds 3
& $svc uninstall | Out-Null
if ($null -eq $ev) { Write-Host "读不到系统事件日志,判不了" -ForegroundColor Red; exit 1 }
Write-Host ("{0} 轮重启:服务意外终止 {1} 次;没连上 {2} 轮" -f $Rounds, $ev.Count, $notConnected)
if ($ev.Count -gt 0 -or $notConnected -gt 0) {
  Write-Host "== crash.log 里的崩溃现场" -ForegroundColor Cyan
  Crash-Heads
  Write-Host "== 服务日志最后 80 行" -ForegroundColor Cyan
  Get-Content (Join-Path $logDir "service.log") -Tail 80 -Encoding UTF8 -ErrorAction SilentlyContinue
  exit 1
}
Write-Host "全部通过" -ForegroundColor Green
exit 0
