# Build Geseki Bridge locally (Windows x64) and install into a portable OBS.
#
#   powershell -ExecutionPolicy Bypass -File tools\build-local.ps1
#   powershell -ExecutionPolicy Bypass -File tools\build-local.ps1 -Install E:\OBS-Testing
#
# First run downloads the OBS sources + prebuilt deps + Qt6 (~1 GB) into
# .deps/; later runs are incremental. Requires VS 2022 Build Tools (VCTools),
# CMake 3.28+ and Go 1.23 on PATH (or set $env:GOROOT).
param(
  [string]$Install = "",
  [switch]$SkipSidecar
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)

# Make CMake and the portable Go toolchain discoverable even when this script
# is launched from a shell that does not export them.
$env:Path = "C:\Program Files\CMake\bin;$env:Path"
$goRoot = "E:\Sekisungkarak\gotool\go"
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  if (Test-Path "$goRoot\bin\go.exe") {
    $env:GOROOT = $goRoot
    $env:Path  = "$goRoot\bin;$env:Path"
    if (-not $env:GOCACHE) { $env:GOCACHE = "E:\Sekisungkarak\gotool\gocache" }
    if (-not $env:GOPATH)  { $env:GOPATH  = "E:\Sekisungkarak\gotool\gopath" }
  }
}

Push-Location $root
try {
  if (-not $SkipSidecar) {
    Write-Host "== Building TikTok sidecar ==" -ForegroundColor Cyan
    Push-Location sidecar\tiktok
    $env:GOOS = "windows"; $env:GOARCH = "amd64"
    go build -trimpath -ldflags "-s -w" -o ..\..\bin\geseki-bridge-tiktok.exe .
    Pop-Location
  }

  Write-Host "== Configuring plugin ==" -ForegroundColor Cyan
  cmake --preset windows-x64

  Write-Host "== Building plugin ==" -ForegroundColor Cyan
  cmake --build --preset windows-x64 --config RelWithDebInfo --parallel

  Write-Host "== Installing to release\ ==" -ForegroundColor Cyan
  cmake --install build_x64 --prefix release --config RelWithDebInfo

  $dest = "release\geseki-bridge\bin\64bit"
  Copy-Item "bin\geseki-bridge-tiktok.exe" $dest -Force
  Write-Host "Plugin + sidecar in $dest" -ForegroundColor Green

  if ($Install) {
    Write-Host "== Installing into $Install ==" -ForegroundColor Cyan
    Copy-Item "$dest\geseki-bridge.dll"        "$Install\obs-plugins\64bit\" -Force
    Copy-Item "$dest\geseki-bridge-tiktok.exe" "$Install\obs-plugins\64bit\" -Force
    New-Item -ItemType Directory -Force -Path "$Install\data\obs-plugins\geseki-bridge\locale" | Out-Null
    Copy-Item "release\geseki-bridge\data\locale\en-US.ini" `
              "$Install\data\obs-plugins\geseki-bridge\locale\" -Force
    Write-Host "Installed into $Install (restart OBS)" -ForegroundColor Green
  }
} finally {
  Pop-Location
}
