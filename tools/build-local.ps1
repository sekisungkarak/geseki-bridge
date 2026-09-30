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
  [switch]$SkipSidecar,
  [switch]$Installer
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

  $dest = "release\obs-plugins\64bit"
  Copy-Item "bin\geseki-bridge-tiktok.exe" $dest -Force
  Write-Host "Plugin + sidecar in $dest" -ForegroundColor Green

  if ($Install) {
    Write-Host "== Installing into $Install ==" -ForegroundColor Cyan
    # Flat OBS layout: works for both portable and installer OBS because the
    # archive mirrors the OBS tree (obs-plugins/64bit + data/obs-plugins/...).
    Copy-Item "release\obs-plugins\64bit\*" "$Install\obs-plugins\64bit\" -Force
    New-Item -ItemType Directory -Force -Path "$Install\data\obs-plugins\geseki-bridge\locale" | Out-Null
    Copy-Item "release\data\obs-plugins\geseki-bridge\locale\en-US.ini" `
              "$Install\data\obs-plugins\geseki-bridge\locale\" -Force
    Write-Host "Installed into $Install (restart OBS)" -ForegroundColor Green
  }

  if ($Installer) {
    Write-Host "== Building Windows installer ==" -ForegroundColor Cyan
    $iscc = Join-Path $env:LOCALAPPDATA "Programs\Inno Setup 6\ISCC.exe"
    if (-not (Test-Path $iscc)) { $iscc = "C:\Program Files (x86)\Inno Setup 6\ISCC.exe" }
    if (-not (Test-Path $iscc)) {
      throw "Inno Setup 6 (ISCC.exe) not found. Install it, or skip -Installer."
    }
    $version = (Get-Content buildspec.json -Raw | ConvertFrom-Json).version
    Remove-Item "package" -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path "package" | Out-Null
    Copy-Item "release\obs-plugins" "package\obs-plugins" -Recurse -Force
    Copy-Item "release\data" "package\data" -Recurse -Force
    $env:GESEKI_VERSION = $version
    & $iscc /Qp installer.iss
    $exe = "package\geseki-bridge-$version-windows-installer.exe"
    if (-not (Test-Path $exe)) { throw "Installer compile failed" }
    Write-Host "Installer: $exe" -ForegroundColor Green
  }
} finally {
  Pop-Location
}
