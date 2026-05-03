# Re-download DRM / transcode binaries into media/tools/ (useful after clone: *.exe is gitignored).
# Run from repo:  pwsh -File media/tools/download_media_tools.ps1
# Or from media: pwsh -File tools/download_media_tools.ps1
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$mediaRoot = if ((Split-Path -Leaf $here) -eq "tools") { Split-Path -Parent $here } else { $here }

$shakaDir = Join-Path $mediaRoot "tools/shaka-packager"
$shakaExe = Join-Path $shakaDir "packager.exe"
$ffmpegBin = Join-Path $mediaRoot "tools/ffmpeg/bin"
New-Item -ItemType Directory -Force -Path $shakaDir, $ffmpegBin | Out-Null

Write-Host "Downloading Shaka Packager v3.7.2..."
Invoke-WebRequest -Uri "https://github.com/shaka-project/shaka-packager/releases/download/v3.7.2/packager-win-x64.exe" -OutFile $shakaExe -UseBasicParsing

$zip = Join-Path $mediaRoot "tools/ffmpeg-win64-gpl.zip"
Write-Host "Downloading FFmpeg (BtbN win64 gpl, may take several minutes)..."
Invoke-WebRequest -Uri "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip" -OutFile $zip -UseBasicParsing
$extract = Join-Path $mediaRoot "tools/ffmpeg-extract"
Expand-Archive -Path $zip -DestinationPath $extract -Force
$root = Get-ChildItem $extract -Directory | Select-Object -First 1
Copy-Item (Join-Path $root.FullName "bin/ffmpeg.exe") (Join-Path $ffmpegBin "ffmpeg.exe") -Force
Copy-Item (Join-Path $root.FullName "bin/ffprobe.exe") (Join-Path $ffmpegBin "ffprobe.exe") -Force
Remove-Item $zip -Force
Remove-Item $extract -Recurse -Force

Write-Host "Done. Shaka: $shakaExe"; Write-Host "FFmpeg: $(Join-Path $ffmpegBin 'ffmpeg.exe')"
