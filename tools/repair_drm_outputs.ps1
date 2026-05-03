# Repair broken DRM packaging tasks (missing master/variant/segments) via admin API.
# Example:
#   pwsh -File tools/repair_drm_outputs.ps1 -BaseUrl "http://127.0.0.1:8200" -Token "<admin_jwt>" -Retry

param(
  [string]$BaseUrl = "http://127.0.0.1:8200",
  [Parameter(Mandatory = $true)][string]$Token,
  [int]$Limit = 200,
  [switch]$Retry
)

$ErrorActionPreference = "Stop"
$url = "$BaseUrl/api/v1/transcode/drm/repair"
$headers = @{
  Authorization = "Bearer $Token"
  "Content-Type" = "application/json"
}
$body = @{
  limit = $Limit
  retry = [bool]$Retry
} | ConvertTo-Json

Write-Host "POST $url"
$resp = Invoke-RestMethod -Method Post -Uri $url -Headers $headers -Body $body
$resp | ConvertTo-Json -Depth 4
