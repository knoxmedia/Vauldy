# Build knox-media with web/dist embedded into the executable (go:embed).
# Outputs: Windows/Linux/macOS × amd64/arm64 (6 binaries).
# Run from media/ (repo root for this project).
param(
  [switch]$AllowDirty
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

function Resolve-GoExe {
  $cmd = Get-Command go -ErrorAction SilentlyContinue
  if ($cmd -and $cmd.Source) { return $cmd.Source }
  $candidates = [System.Collections.Generic.List[string]]::new()
  if (-not [string]::IsNullOrWhiteSpace($env:GOROOT)) {
    $candidates.Add((Join-Path $env:GOROOT "bin\go.exe"))
  }
  $candidates.Add("D:\program files\Go\bin\go.exe")
  $candidates.Add("C:\Program Files\Go\bin\go.exe")
  if (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
    $candidates.Add((Join-Path $env:LOCALAPPDATA "Programs\Go\bin\go.exe"))
  }
  $found = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
  if ($found) { return $found }
  throw "go not found on PATH; install Go or add its bin directory to PATH (e.g. D:\program files\Go\bin)"
}

$GoExe = Resolve-GoExe
$goDir = Split-Path -Parent $GoExe
if ($env:PATH -notlike "*$goDir*") {
  $env:PATH = "$goDir;$env:PATH"
}

$savedEnvironment = @{}
foreach ($name in @("CGO_ENABLED", "GOOS", "GOARCH")) {
  $path = "Env:$name"
  if (Test-Path "Env:$name") {
    $savedEnvironment[$name] = @{ Exists = $true; Value = (Get-Item -Path $path).Value }
  } else {
    $savedEnvironment[$name] = @{ Exists = $false; Value = $null }
  }
}
$buildCheck = Join-Path ([System.IO.Path]::GetTempPath()) ("knox-buildinfo-check-{0}-{1}.exe" -f $PID, [guid]::NewGuid())

try {

Push-Location web
npm run build
Pop-Location

$embedDist = Join-Path $root "internal/webembed/dist"
$webDist = Join-Path $root "web/dist"
if (-not (Test-Path $webDist)) {
  throw "web/dist not found after npm run build"
}
if (Test-Path $embedDist) {
  Remove-Item $embedDist -Recurse -Force
}
Copy-Item $webDist $embedDist -Recurse

$binDir = Join-Path $root "bin"
New-Item -ItemType Directory -Path $binDir -Force | Out-Null

$version = (& git describe --tags --always).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($version)) { throw "git describe failed" }
$commit = (& git rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($commit)) { throw "git rev-parse HEAD failed" }
$dirtyOutput = @(& git status --porcelain --ignore-submodules=dirty)
if ($LASTEXITCODE -ne 0) { throw "git status --porcelain failed" }
$dirty = if ($dirtyOutput.Count -gt 0) { "true" } else { "false" }
if ($dirty -eq "true" -and -not $AllowDirty) { throw "refusing dirty release build; pass -AllowDirty for a development artifact" }

if ($env:SOURCE_DATE_EPOCH) {
  $buildTime = [DateTimeOffset]::FromUnixTimeSeconds([Int64]$env:SOURCE_DATE_EPOCH).UtcDateTime.ToString("yyyy-MM-dd'T'HH:mm:ss'Z'")
} else {
  $buildTime = [DateTime]::UtcNow.ToString("yyyy-MM-dd'T'HH:mm:ss'Z'")
}
$ldflags = "-s -w -X knox-media/internal/buildinfo.Version=$version -X knox-media/internal/buildinfo.Commit=$commit -X knox-media/internal/buildinfo.BuildTime=$buildTime -X knox-media/internal/buildinfo.Dirty=$dirty"

# Validate the exact injected metadata and Go VCS settings before packaging.
& $GoExe build -ldflags $ldflags -o $buildCheck ./cmd/buildinfo-check
if ($LASTEXITCODE -ne 0) { throw "build metadata checker build failed" }
if ($AllowDirty) { & $buildCheck --allow-dirty } else { & $buildCheck }
if ($LASTEXITCODE -ne 0) { throw "build metadata validation failed" }

$env:CGO_ENABLED = "0"

function Invoke-GoBuild {
  param(
    [string]$Output,
    [string]$GoOS,
    [string]$GoArch
  )
  $env:GOOS = $GoOS
  $env:GOARCH = $GoArch
  & $GoExe build -tags embedweb -ldflags $ldflags -o $Output ./cmd/server
  if ($LASTEXITCODE -ne 0) {
    throw "go build failed for $Output"
  }
}

$builds = @(
  @{ OS = "windows"; Arch = "amd64"; Suffix = ".exe" },
  @{ OS = "windows"; Arch = "arm64"; Suffix = ".exe" },
  @{ OS = "linux";   Arch = "amd64"; Suffix = "" },
  @{ OS = "linux";   Arch = "arm64"; Suffix = "" },
  @{ OS = "darwin";  Arch = "amd64"; Suffix = "" },
  @{ OS = "darwin";  Arch = "arm64"; Suffix = "" }
)

$total = $builds.Count
$sw = [System.Diagnostics.Stopwatch]::StartNew()

for ($i = 0; $i -lt $total; $i++) {
  $b = $builds[$i]
  $name = "vauldy-$($b.OS)-$($b.Arch)$($b.Suffix)"
  $out = Join-Path $binDir $name
  Write-Host "[$($i+1)/$total] Building $name ..."
  Invoke-GoBuild -Output $out -GoOS $b.OS -GoArch $b.Arch
}

$sw.Stop()

Write-Host ""
Write-Host "Built $total binaries in $([math]::Round($sw.Elapsed.TotalSeconds, 1))s:"
Get-ChildItem $binDir | ForEach-Object {
  $sizeMB = [math]::Round($_.Length / 1MB, 1)
  Write-Host "  $($_.Name)  ($sizeMB MB)"
}
Write-Host "(embedded web/dist - no external web/dist folder required at runtime)"
} finally {
  Remove-Item $buildCheck -Force -ErrorAction SilentlyContinue
  foreach ($name in @("CGO_ENABLED", "GOOS", "GOARCH")) {
    if ($savedEnvironment[$name].Exists) {
      Set-Item -Path "Env:$name" -Value $savedEnvironment[$name].Value
    } else {
      Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
    }
  }
}
