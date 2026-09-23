# 佛跳墙 真机验收脚本(在 Windows VPS 上以管理员 PowerShell 运行)
#   .\vps-test.ps1 -Sub "https://面板/sub/用户名" [-Bin "C:\godusevpn"]
# 步骤:装服务 → 设订阅 → 连接 → 检查 TUN 网卡、默认路由、fake-ip、DNS 劫持、出口 IP、IPv6 阻断、三态模式 → 断开 → 检查清理。
param(
  [Parameter(Mandatory = $true)][string]$Sub,
  [string]$Bin = "C:\godusevpn",
  [switch]$KeepInstalled
)
$ErrorActionPreference = "Continue"
# Go 程序输出 UTF-8;PowerShell 5.1 默认按本地代码页解码会把中文弄乱,这里统一成 UTF-8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
chcp 65001 | Out-Null
$svc = Join-Path $Bin "godusevpn-svc.exe"
$cli = Join-Path $Bin "godusevpn-cli.exe"
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
function Public-IP4() {
  ipconfig /flushdns | Out-Null
  try { return (& curl.exe -s -4 --max-time 15 "https://api.ipify.org") } catch { return "" }
}
function Public-IP6() {
  ipconfig /flushdns | Out-Null
  try { return (& curl.exe -s --max-time 10 "https://api6.ipify.org") } catch { return "" }
}

Write-Host "== 0. 环境" -ForegroundColor Cyan
$osv = (Get-CimInstance Win32_OperatingSystem)
Write-Host ("{0} {1} {2}" -f $osv.Caption, $osv.Version, $env:PROCESSOR_ARCHITECTURE)
$ipBefore = Public-IP4
Write-Host "连接前公网 IPv4: $ipBefore"
# 先记下一个境外域名的真实 IP,连上后直接连这个 IP(不经 fake-ip)验证也走代理
$realIp = (Resolve-DnsName -Name "api.ipify.org" -Type A -ErrorAction SilentlyContinue | Where-Object { $_.Type -eq "A" } | Select-Object -First 1).IPAddress
Write-Host "api.ipify.org 真实 IP: $realIp"
# 连上以后默认路由指向 TUN,物理网卡的地址要现在记下来:绑它发直连是全局禁直连闸的实测
$physIp = (Find-NetRoute -RemoteIPAddress 1.1.1.1 -ErrorAction SilentlyContinue | Select-Object -First 1).IPAddress
Write-Host "连接前默认出口网卡地址: $physIp"
function Direct-Http() { # 绑物理网卡发一个明文请求,返回状态码;被闸拦下时是 000
  if (-not $physIp) { return "" }
  try { return (& curl.exe -s --interface $physIp -m 6 -o NUL -w "%{http_code}" "http://1.1.1.1/cdn-cgi/trace") } catch { return "000" }
}

Write-Host "== 1. 安装服务" -ForegroundColor Cyan
& $svc uninstall 2>$null | Out-Null
& $svc install
Check "服务运行" ((& $svc status) -eq "running") (& $svc status)
Check "管道可达" ((& $cli version 2>&1) -match "佛跳墙") ""

Write-Host "== 2. 订阅与连接" -ForegroundColor Cyan
$p = & $cli profile $Sub 2>&1 | Out-String
Check "订阅拉取" ($p -match "个节点") ($p.Trim())
& $cli connect | Out-Null
$st = Wait-Status "connected" 60
Check "进入 connected" ($st -match "状态:\s+connected") ($st.Trim())

Write-Host "== 3. 网络栈" -ForegroundColor Cyan
$tun = Get-NetAdapter | Where-Object { $_.Name -like "*godusevpn*" -or $_.InterfaceDescription -like "*Wintun*" }
Check "TUN 网卡存在" ($null -ne $tun) ($(if ($tun) { $tun.Name + " " + $tun.Status } else { "" }))
# auto_route 不改 0.0.0.0/0,而是加一组更精确的分段路由,所以要问"到某个公网地址走哪张网卡"
$rt = Find-NetRoute -RemoteIPAddress 104.26.12.205 -ErrorAction SilentlyContinue | Select-Object -Last 1
Check "公网地址的路由走 TUN" ($tun -and $rt -and $rt.InterfaceIndex -eq $tun.ifIndex) ($(if ($rt) { "ifIndex=" + $rt.InterfaceIndex + " alias=" + $rt.InterfaceAlias } else { "no route" }))
$tunRoutes = @(Get-NetRoute -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $tun -and $_.InterfaceIndex -eq $tun.ifIndex })
Check "TUN 上有分段路由" ($tunRoutes.Count -gt 5) ("count=" + $tunRoutes.Count)
$dns = Resolve-DnsName -Name "www.google.com" -Type A -ErrorAction SilentlyContinue | Where-Object { $_.Type -eq "A" } | Select-Object -First 1
Check "代理域名得到 fake-ip(198.18/15)" ($dns -and $dns.IPAddress -like "198.1[89].*") ($(if ($dns) { $dns.IPAddress } else { "no answer" }))
$cn = Resolve-DnsName -Name "www.baidu.com" -Type A -ErrorAction SilentlyContinue | Where-Object { $_.Type -eq "A" } | Select-Object -First 1
Check "国内域名是真实 IP(不走 fake-ip)" ($cn -and $cn.IPAddress -notlike "198.1[89].*") ($(if ($cn) { $cn.IPAddress } else { "no answer" }))
$hij = Resolve-DnsName -Name "www.google.com" -Server 8.8.8.8 -Type A -ErrorAction SilentlyContinue | Where-Object { $_.Type -eq "A" } | Select-Object -First 1
Check "直接问 8.8.8.8 也被劫持(答案仍是 fake-ip)" ($hij -and $hij.IPAddress -like "198.1[89].*") ($(if ($hij) { $hij.IPAddress } else { "no answer" }))
$aaaa = Resolve-DnsName -Name "www.google.com" -Type AAAA -ErrorAction SilentlyContinue | Where-Object { $_.Type -eq "AAAA" }
Check "AAAA 为空(禁 IPv6)" ($null -eq $aaaa) ($(if ($aaaa) { ($aaaa | Select-Object -First 1).IPAddress } else { "" }))

Write-Host "== 3.7 全局禁直连(WFP 闸:绑物理网卡的直连被拦,经隧道照常;停服务闸仍在;切回规则模式闸清空)" -ForegroundColor Cyan
# 正控制:规则模式下(闸没开)绑物理网卡的直连必须是 200,否则探测本身跑不通,下面"被拦"的断言没有意义
$d0 = Direct-Http
$script:directOK = ($d0 -eq "200")
if (-not $script:directOK) { Write-Host "  (正控制没过:规则模式下绑物理网卡直连 http=$d0,被拦断言改为跳过)" }
& $cli mode global | Out-Null
Start-Sleep -Seconds 2
$stg = Wait-Status "connected" 15
Check "切到严格全局模式且连接仍在" (($stg -match "状态:\s+connected") -and ($stg -match "模式:\s+global")) ($stg.Trim() -replace "`r?`n", " | ")
$gs = & $svc guard status 2>&1 | Out-String
Check "闸开着(WFP 过滤器非空)" ($gs -match "开着") $gs.Trim()
# 提供者绝不能绑 Windows 服务名:绑了的话服务一停,BFE 就把它名下全部过滤器置为 DISABLED,
# 而且服务 ACL 允许普通用户启停 —— 任何本机账户一句 sc stop 就能关掉闸。这里直接看 BFE 的状态。
$stateXml = Join-Path $env:TEMP "godusevpn-wfpstate.xml"
Remove-Item $stateXml -ErrorAction SilentlyContinue
& netsh wfp show state file="$stateXml" | Out-Null
$bound = "(读不到 WFP 状态)"
if (Test-Path $stateXml) {
  [xml]$wfp = Get-Content $stateXml -Encoding UTF8
  $ours = @($wfp.SelectNodes("//providers/item") | Where-Object { $_.displayData.name -eq "godusevpn" })
  if ($ours.Count -gt 0) { $bound = ($ours | ForEach-Object { "serviceName=[" + $_.serviceName + "]" }) -join ", " }
  Check "WFP 提供者存在且没有绑服务名" (($ours.Count -gt 0) -and -not ($ours | Where-Object { -not [string]::IsNullOrEmpty($_.serviceName) })) $bound
} else { Check "WFP 提供者没有绑服务名" $false $bound }
if ($script:directOK) { $d = Direct-Http; Check "绑物理网卡的直连被拦" ($d -and $d -ne "200") "http=$d(网卡 $physIp)" }
try { $tc = (& curl.exe -s -m 15 -o NUL -w "%{http_code}" "https://1.1.1.1/cdn-cgi/trace") } catch { $tc = "000" }
Check "经隧道照常" ($tc -eq "200") "http=$tc"
# 闸是持久的:服务停了也必须还在拦。这是 m29 绑服务名之后失效的那条性质,直接停服务实测。
& $svc stop | Out-Null
Start-Sleep -Seconds 3
if ($script:directOK) { $d2 = Direct-Http; Check "服务停止后闸仍在拦直连" ($d2 -and $d2 -ne "200") "http=$d2" }
& $svc start | Out-Null
$stb = Wait-Status "connected" 60
Check "服务重启后自动恢复连接(落盘的连接意愿)" ($stb -match "状态:\s+connected") ($stb.Trim() -replace "`r?`n", " | ")
# 网卡 IPv6 备份:塞一张不存在的网卡进去。断开时的还原脚本必须跳过它、把其它网卡照常还原、并把备份删干净 ——
# m29 的脚本在 $ErrorActionPreference='Stop' 下会在这一行中断,排在后面的网卡一张都不还原,备份永远删不掉。
$nicb = Join-Path $env:ProgramData "godusevpn\nic-ipv6-backup.txt"
$script:nicInjected = $false
if (Test-Path $nicb) {
  Add-Content -Path $nicb -Value ("ZZ-Gone-Adapter-For-Test" + "`t" + "True") -Encoding UTF8
  $script:nicInjected = $true
  Check "网卡 IPv6 备份存在(已注入一张不存在的网卡)" $true ((Get-Content $nicb) -join " | ")
} else { Write-Host "  (没有网卡 IPv6 备份:跳过消失网卡的还原测试)" }
& $cli mode rule | Out-Null
Start-Sleep -Seconds 2
$gs2 = & $svc guard status 2>&1 | Out-String
Check "切回规则模式后闸清空" ($gs2 -match "没开") $gs2.Trim()
$str = Wait-Status "connected" 15
Check "规则模式恢复连接" ($str -match "状态:\s+connected") ""

Write-Host "== 4. 出口" -ForegroundColor Cyan
$ipProxy = Public-IP4
Check "规则模式出口 IP 变了" ($ipProxy -and $ipProxy -ne $ipBefore) "before=$ipBefore now=$ipProxy"
$v6 = Public-IP6
Check "IPv6 出网被阻断(含经代理的远端解析)" ([string]::IsNullOrEmpty($v6)) "v6=$v6"
$viaReal = ""
if ($realIp) { try { $viaReal = (& curl.exe -s -4 --max-time 15 --resolve "api.ipify.org:443:$realIp" "https://api.ipify.org") } catch { $viaReal = "" } }
Check "直连真实 IP(绕过 fake-ip)也走代理" ($viaReal -and $viaReal -ne $ipBefore) "real=$realIp got=$viaReal"
$lat = & $cli test 2>&1 | Out-String
Check "延迟测试" ($lat -match "\d+ ms") ($lat.Trim())

Write-Host "== 5. 模式切换" -ForegroundColor Cyan
& $cli mode direct | Out-Null
Start-Sleep -Seconds 2
$ipDirect = Public-IP4
Check "直连模式出口回到本机" ($ipDirect -eq $ipBefore) "direct=$ipDirect"
& $cli mode global | Out-Null
Start-Sleep -Seconds 2
$ipGlobal = Public-IP4
Check "全局模式出口是节点" ($ipGlobal -and $ipGlobal -ne $ipBefore) "global=$ipGlobal"
& $cli mode rule | Out-Null
$nodes = & $cli nodes 2>&1 | Out-String
Check "节点列表" ($nodes -match "\*") ($nodes.Trim() -replace "`r?`n", " | ")

Write-Host "== 6. 断开与清理" -ForegroundColor Cyan
& $cli disconnect | Out-Null
Start-Sleep -Seconds 3
$tun2 = Get-NetAdapter | Where-Object { $_.Name -like "*godusevpn*" -and $_.Status -eq "Up" }
Check "断开后 TUN 网卡不再 Up" ($null -eq $tun2) ""
$ipAfter = Public-IP4
Check "断开后出口恢复" ($ipAfter -eq $ipBefore) "after=$ipAfter"
if ($script:nicInjected) {
  Start-Sleep -Seconds 5
  Check "断开后网卡 IPv6 备份已清理(含那张不存在的网卡)" (-not (Test-Path $nicb)) ($(if (Test-Path $nicb) { (Get-Content $nicb) -join " | " } else { "" }))
  $slog = Join-Path $env:ProgramData "godusevpn\logs\service.log"
  $gone = if (Test-Path $slog) { @(Select-String -Path $slog -Pattern "已经不在了" -SimpleMatch).Count } else { 0 }
  Check "服务日志记下了已消失的网卡" ($gone -gt 0) "matches=$gone"
}
if (-not $KeepInstalled) {
  & $svc uninstall | Out-Null
  Check "服务已卸载" ((& $svc status) -eq "not-installed") (& $svc status)
}
Write-Host ""
if ($fail -eq 0) { Write-Host "全部通过" -ForegroundColor Green } else {
  Write-Host "$fail 项失败" -ForegroundColor Red
  Write-Host "== 内核日志(最近 60 行)" -ForegroundColor Cyan
  & $cli logs 60 core
  Write-Host "== 服务日志(最近 30 行)" -ForegroundColor Cyan
  & $cli logs 30
}
Write-Host "日志: $env:ProgramData\godusevpn\logs\"
exit $fail
