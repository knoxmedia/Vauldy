# Build knox-media with bundled web frontend + PowerPlayer static assets.
# Run from media/ (repo root for this project).
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

Push-Location web
npm run build
Pop-Location

$out = if ($args.Count -gt 0) { $args[0] } else { "knox-media.exe" }
go build -o $out ./cmd/server
Write-Host "Built $out (serve with web/dist alongside the executable)"
