# Install ASR/OCR Python deps + Windows Tesseract into media/tools/.
# Run from media: pwsh -File tools/install_recognition_tools.ps1 [-Target asr|ocr|all]
param(
    [ValidateSet("asr", "ocr", "all")]
    [string]$Target = "all"
)
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$mediaRoot = if ((Split-Path -Leaf $here) -eq "tools") { Split-Path -Parent $here } else { $here }

$venvDir = Join-Path $mediaRoot "tools/recognition/.venv"
$py = Join-Path $venvDir "Scripts/python.exe"

function Ensure-Venv {
    if (Test-Path $py) { return }
    $sys = Get-Command python -ErrorAction SilentlyContinue
    if (-not $sys) { throw "Python not found on PATH" }
    New-Item -ItemType Directory -Force -Path (Split-Path $venvDir) | Out-Null
    & python -m venv $venvDir
}

Ensure-Venv

if ($Target -eq "asr" -or $Target -eq "all") {
    Write-Host "Installing openai-whisper..."
    & $py -m pip install --upgrade pip openai-whisper
}

if ($Target -eq "ocr" -or $Target -eq "all") {
    Write-Host "Installing pgsrip..."
    & $py -m pip install --upgrade pip pgsrip
    $tessDir = Join-Path $mediaRoot "tools/tesseract"
    $tessExe = Join-Path $tessDir "tesseract.exe"
    if (-not (Test-Path $tessExe)) {
        Write-Host "Installing Tesseract OCR (silent)..."
        New-Item -ItemType Directory -Force -Path $tessDir | Out-Null
        $installer = Join-Path $tessDir "tesseract-setup.exe"
        Invoke-WebRequest -Uri "https://github.com/tesseract-ocr/tesseract/releases/download/5.5.0/tesseract-ocr-w64-setup-5.5.0.20241111.exe" -OutFile $installer -UseBasicParsing
        Start-Process -FilePath $installer -ArgumentList "/S", "/D=$tessDir" -Wait
        Remove-Item $installer -Force -ErrorAction SilentlyContinue
    }
    $tessdata = Join-Path $tessDir "tessdata"
    New-Item -ItemType Directory -Force -Path $tessdata | Out-Null
    foreach ($lang in @("chi_sim", "eng")) {
        $dest = Join-Path $tessdata "$lang.traineddata"
        if (-not (Test-Path $dest)) {
            Write-Host "Downloading tessdata $lang..."
            Invoke-WebRequest -Uri "https://github.com/tesseract-ocr/tessdata/raw/main/$lang.traineddata" -OutFile $dest -UseBasicParsing
        }
    }
}

Write-Host "Done. Venv: $venvDir"
Write-Host "Configure in 管理 -> 系统选项 -> 识别, or use API POST /api/v1/admin/system-options/install/*"
