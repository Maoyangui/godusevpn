# Android 构建:gomobile bind 出 AAR → Gradle 出 APK。
#   .\deploy\android-build.ps1 [-Version 0.6.0-a0] [-Release] [-Abi arm64-v8a,armeabi-v7a,x86_64]
# 依赖:JDK 17、Android SDK / NDK(见 scratchpad 里的 android-setup2.ps1,装在 D:\Android\Sdk)、gomobile。
param(
  [string]$Version = "0.6.0-a0",
  [switch]$Release,
  [string]$Abi = "arm64,arm,amd64"   # gomobile 的目标名:arm64 / arm / amd64 / 386
)
$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$sdk = if ($env:ANDROID_HOME) { $env:ANDROID_HOME } else { "D:\Android\Sdk" }
$ndk = if ($env:ANDROID_NDK_HOME) { $env:ANDROID_NDK_HOME } else { Get-ChildItem "$sdk\ndk" -Directory | Sort-Object Name -Descending | Select-Object -First 1 -ExpandProperty FullName }
$jdk = if ($env:JAVA_HOME) { $env:JAVA_HOME } else { (Get-ChildItem "C:\Program Files\Microsoft" -Directory -Filter "jdk-17*" | Select-Object -First 1).FullName }
$env:ANDROID_HOME = $sdk; $env:ANDROID_NDK_HOME = $ndk; $env:JAVA_HOME = $jdk
$env:Path = "$jdk\bin;$sdk\platform-tools;$(go env GOPATH)\bin;$env:Path"
if (-not $env:GRADLE_USER_HOME) { $env:GRADLE_USER_HOME = "D:\gradle" }

Write-Host "== gomobile bind ($Abi)"
$targets = ($Abi.Split(",") | ForEach-Object { "android/$_" }) -join ","
$tags = "with_quic,with_utls,with_clash_api,with_gvisor"
$ld = "-s -w -X github.com/Maoyangui/godusevpn/internal/buildinfo.Version=$Version"
New-Item -ItemType Directory -Force "$root\android\app\libs" | Out-Null
Push-Location $root
try {
  & gomobile bind -target $targets -androidapi 26 -javapkg com.maoyangui.godusevpn -tags $tags -ldflags $ld -trimpath -o "$root\android\app\libs\godusevpn.aar" ./mobile
  if ($LASTEXITCODE -ne 0) { throw "gomobile bind 失败" }
} finally { Pop-Location }
Get-Item "$root\android\app\libs\godusevpn.aar" | ForEach-Object { "AAR {0:N1} MB" -f ($_.Length/1MB) }

Write-Host "== gradle"
Push-Location "$root\android"
try {
  if (-not (Test-Path "gradlew.bat")) {
    # 没有 wrapper 就临时下一个 Gradle 8.7 到 D 盘
    $gdir = "D:\gradle\dist\gradle-8.7"
    if (-not (Test-Path "$gdir\bin\gradle.bat")) {
      New-Item -ItemType Directory -Force "D:\gradle\dist" | Out-Null
      Invoke-WebRequest -Uri "https://services.gradle.org/distributions/gradle-8.7-bin.zip" -OutFile "D:\gradle\dist\gradle-8.7-bin.zip"
      Expand-Archive -Force "D:\gradle\dist\gradle-8.7-bin.zip" "D:\gradle\dist"
    }
    $gradle = "$gdir\bin\gradle.bat"
  } else { $gradle = ".\gradlew.bat" }
  $task = if ($Release) { "assembleRelease" } else { "assembleDebug" }
  & $gradle --no-daemon -q $task "-PappVersion=$Version" "-PsplitAbi=$($Release.IsPresent.ToString().ToLower())"
  if ($LASTEXITCODE -ne 0) { throw "gradle 失败" }
  Get-ChildItem "app\build\outputs\apk" -Recurse -Filter *.apk | ForEach-Object { "{0}  {1:N1} MB" -f $_.FullName.Replace("$root\", ""), ($_.Length/1MB) }
} finally { Pop-Location }
