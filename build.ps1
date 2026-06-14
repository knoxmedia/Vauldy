# Build knox-media with web/dist embedded into the executable (go:embed).
# Outputs: bin/knox-media.exe (Windows) and bin/knox-media-linux (linux/amd64).
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
    [string]$GoOS = "",
    [string]$GoArch = ""
  )
  if ($GoOS) { $env:GOOS = $GoOS } else { Remove-Item Env:GOOS -ErrorAction SilentlyContinue }
  if ($GoArch) { $env:GOARCH = $GoArch } else { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue }
  & go build -tags embedweb "-ldflags=-s -w" -o $Output ./cmd/server
  if ($LASTEXITCODE -ne 0) {
    throw "go build failed for $Output"
  }
}

Invoke-GoBuild -Output (Join-Path $binDir "knox-media.exe")
Invoke-GoBuild -Output (Join-Path $binDir "knox-media-linux") -GoOS linux -GoArch amd64
Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue

Write-Host "Built:"
Write-Host "  bin/knox-media.exe"
Write-Host "  bin/knox-media-linux"
Write-Host "(embedded web/dist - no external web/dist folder required at runtime)"
