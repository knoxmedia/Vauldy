# Build knox-media with web/dist embedded into the executable (go:embed).
# Outputs: Windows/Linux/macOS × amd64/arm64 (6 binaries).
# Run from media/ (repo root for this project).
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

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

$env:CGO_ENABLED = "0"

function Invoke-GoBuild {
  param(
    [string]$Output,
    [string]$GoOS,
    [string]$GoArch
  )
  $env:GOOS = $GoOS
  $env:GOARCH = $GoArch
  & go build -tags embedweb "-ldflags=-s -w" -o $Output ./cmd/server
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
  $name = "knox-media-$($b.OS)-$($b.Arch)$($b.Suffix)"
  $out = Join-Path $binDir $name
  Write-Host "[$($i+1)/$total] Building $name ..."
  Invoke-GoBuild -Output $out -GoOS $b.OS -GoArch $b.Arch
}

$sw.Stop()
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "Built $total binaries in $([math]::Round($sw.Elapsed.TotalSeconds, 1))s:"
Get-ChildItem $binDir | ForEach-Object {
  $sizeMB = [math]::Round($_.Length / 1MB, 1)
  Write-Host "  $($_.Name)  ($sizeMB MB)"
}
Write-Host "(embedded web/dist - no external web/dist folder required at runtime)"
