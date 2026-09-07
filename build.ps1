# 构建 佛跳墙 的服务与命令行:dist\<arch>\godusevpn-svc.exe / godusevpn-cli.exe
param([string]$Version = "0.1.0-dev")
$ErrorActionPreference = "Stop"
$tags = "with_quic,with_utls,with_clash_api,with_gvisor"
$ld = "-s -w -X github.com/Maoyangui/godusevpn/internal/buildinfo.Version=$Version"
foreach ($arch in @("amd64", "arm64")) {
  $out = "dist\$arch"
  New-Item -ItemType Directory -Force $out | Out-Null
  $env:GOOS = "windows"; $env:GOARCH = $arch; $env:CGO_ENABLED = "0"
  go build -tags $tags -trimpath -ldflags $ld -o "$out\godusevpn-svc.exe" ./cmd/godusevpn-svc
  if ($LASTEXITCODE -ne 0) { exit 1 }
  go build -tags $tags -trimpath -ldflags $ld -o "$out\godusevpn-cli.exe" ./cmd/godusevpn-cli
  if ($LASTEXITCODE -ne 0) { exit 1 }
  Write-Host "built $arch -> $out"
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
