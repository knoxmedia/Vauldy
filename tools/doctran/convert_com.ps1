# Knox Media — Office/WPS COM to PDF
param(
  [Parameter(Mandatory=$true)][ValidateSet('office','wps')][string]$Engine,
  [Parameter(Mandatory=$true)][string]$InputPath,
  [Parameter(Mandatory=$true)][string]$OutputPath
)
$ErrorActionPreference = 'Stop'
$InputPath = (Resolve-Path -LiteralPath $InputPath).Path
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
$ext = [IO.Path]::GetExtension($InputPath).ToLower()
$outDir = Split-Path -Parent $OutputPath
if (-not (Test-Path $outDir)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }

function Convert-WordLike($progId) {
  $app = New-Object -ComObject $progId
  $app.Visible = $false
  $app.DisplayAlerts = 0
  try {
    $doc = $app.Documents.Open($InputPath, $false, $true)
    try { $doc.SaveAs2([ref]$OutputPath, 17) } finally { $doc.Close($false) | Out-Null }
  } finally { $app.Quit() | Out-Null }
}

function Convert-ExcelLike($progId) {
  $app = New-Object -ComObject $progId
  $app.Visible = $false
  $app.DisplayAlerts = $false
  try {
    $wb = $app.Workbooks.Open($InputPath, $null, $true)
    try { $wb.ExportAsFixedFormat(0, $OutputPath) } finally { $wb.Close($false) | Out-Null }
  } finally { $app.Quit() | Out-Null }
}

function Convert-PPTLike($progId) {
  $app = New-Object -ComObject $progId
  try {
    $pres = $app.Presentations.Open($InputPath, $true, $true, $false)
    try { $pres.SaveAs($OutputPath, 32) } finally { $pres.Close() | Out-Null }
  } finally { $app.Quit() | Out-Null }
}

if ($Engine -eq 'office') {
  switch ($ext) {
    { $_ -in '.doc','.docx' } { Convert-WordLike 'Word.Application'; break }
    { $_ -in '.xls','.xlsx' } { Convert-ExcelLike 'Excel.Application'; break }
    { $_ -in '.ppt','.pptx' } { Convert-PPTLike 'PowerPoint.Application'; break }
    default { throw "unsupported extension $ext" }
  }
} else {
  switch ($ext) {
    { $_ -in '.doc','.docx' } { Convert-WordLike 'Kwps.Application'; break }
    { $_ -in '.xls','.xlsx' } { Convert-ExcelLike 'Ket.Application'; break }
    { $_ -in '.ppt','.pptx' } { Convert-PPTLike 'Kwpp.Application'; break }
    default { throw "unsupported extension $ext" }
  }
}
if (-not (Test-Path -LiteralPath $OutputPath)) { throw "output not created" }
