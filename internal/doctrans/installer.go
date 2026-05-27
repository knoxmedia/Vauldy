package doctrans

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"knox-media/internal/config"
)

// Install prepares tools/doctran and detects all engines.
func Install(mediaRoot string) (Deploy, error) {
	mediaRoot = filepath.Clean(mediaRoot)
	dir := filepath.Join(mediaRoot, filepath.FromSlash(DefaultDirRel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Deploy{}, fmt.Errorf("create %s: %w", DefaultDirRel, err)
	}
	readme := filepath.Join(dir, "README.txt")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		_ = os.WriteFile(readme, []byte(installReadme()), 0o644)
	}
	_ = ensureConvertScript(mediaRoot)
	deploy := DetectLibreOffice(mediaRoot)
	deploy.EngineOrder = []string{string(EngineOffice), string(EngineWPS), string(EngineLibreOffice)}
	empty := config.DocTransConfig{}
	if office := resolveOfficePath(mediaRoot, empty); office != "" {
		deploy.OfficePath = relPathUnder(mediaRoot, office)
		if deploy.OfficePath == "" {
			deploy.OfficePath = office
		}
	}
	if wps := resolveWPSDir(mediaRoot, empty); wps != "" {
		deploy.WPSPath = relPathUnder(mediaRoot, wps)
		if deploy.WPSPath == "" {
			deploy.WPSPath = wps
		}
	}
	return deploy, nil
}

// InstallLibreOffice attempts one-click LibreOffice setup.
func InstallLibreOffice(ctx context.Context, mediaRoot string) (Deploy, error) {
	mediaRoot = filepath.Clean(mediaRoot)
	dir := filepath.Join(mediaRoot, filepath.FromSlash(DefaultDirRel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Deploy{}, err
	}
	deploy := DetectLibreOffice(mediaRoot)
	cfg := DeployToConfig(deploy)
	if st := detectLibreOffice(mediaRoot, cfg); st.Available {
		return deploy, nil
	}
	if runtime.GOOS == "windows" {
		if err := runLibreOfficeInstallScript(ctx, mediaRoot); err != nil {
			return deploy, err
		}
	} else {
		return deploy, fmt.Errorf("请通过包管理器安装 libreoffice，或将 soffice 放到 %s", DefaultDirRel)
	}
	deploy = DetectLibreOffice(mediaRoot)
	cfg = DeployToConfig(deploy)
	if !detectLibreOffice(mediaRoot, cfg).Available {
		return deploy, fmt.Errorf("安装后仍未找到 LibreOffice")
	}
	return deploy, nil
}

func runLibreOfficeInstallScript(ctx context.Context, mediaRoot string) error {
	// PortableApps PAF silent extract to tools/doctran when winget fails.
	scriptPath := filepath.Join(mediaRoot, filepath.FromSlash("tools/doctran/install_libreoffice.ps1"))
	if err := os.WriteFile(scriptPath, []byte(installLibreOfficeScript), 0o644); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", scriptPath, "-MediaRoot", mediaRoot)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOut(out))
	}
	return nil
}

func installReadme() string {
	return `Knox Media — 文档转换引擎

支持引擎（按优先级）:
  1. Microsoft Office (COM, Windows)
  2. WPS Office (COM, Windows)
  3. LibreOffice (无头 soffice)

LibreOffice 便携版:
  tools/doctran/LibreOffice/program/soffice.exe

系统选项 -> 文档转换：检测引擎、调整优先级、一键安装 LibreOffice。
`
}

const installLibreOfficeScript = `
param([Parameter(Mandatory=$true)][string]$MediaRoot)
$ErrorActionPreference = 'Stop'
$doctran = Join-Path $MediaRoot 'tools\doctran'
$dest = Join-Path $doctran 'LibreOffice\program\soffice.exe'
if (Test-Path -LiteralPath $dest) { exit 0 }

foreach ($p in @(
  'C:\Program Files\LibreOffice\program\soffice.exe',
  'C:\Program Files (x86)\LibreOffice\program\soffice.exe'
)) {
  if (Test-Path -LiteralPath $p) { exit 0 }
}

$found = Get-ChildItem -Path $doctran -Filter soffice.exe -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
if ($found) { exit 0 }

$winget = Get-Command winget -ErrorAction SilentlyContinue
if ($winget) {
  winget install --id TheDocumentFoundation.LibreOffice --accept-package-agreements --accept-source-agreements --silent 2>&1 | Out-Null
  Start-Sleep -Seconds 10
  foreach ($p in @(
    'C:\Program Files\LibreOffice\program\soffice.exe',
    'C:\Program Files (x86)\LibreOffice\program\soffice.exe'
  )) {
    if (Test-Path -LiteralPath $p) { exit 0 }
  }
}

$paf = Join-Path $doctran 'LibreOfficePortable.paf.exe'
if (-not (Test-Path -LiteralPath $paf)) {
  $url = 'https://download.documentfoundation.org/libreoffice/portable/26.2.1/LibreOfficePortable_26.2.1_MultilingualStandard.paf.exe'
  Write-Host "Downloading portable LibreOffice ..."
  Invoke-WebRequest -Uri $url -OutFile $paf -UseBasicParsing
}
if (-not (Test-Path -LiteralPath $paf)) { throw 'portable download failed' }

Write-Host "Extracting portable LibreOffice to $doctran ..."
$proc = Start-Process -FilePath $paf -ArgumentList "/D=$doctran","/VERYSILENT","/SUPPRESSMSGBOXES","/NORESTART" -Wait -PassThru
if ($proc.ExitCode -ne 0) { throw "portable installer exit $($proc.ExitCode)" }

$found = Get-ChildItem -Path $doctran -Filter soffice.exe -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $found) { throw 'portable install finished but soffice.exe not found under tools/doctran' }
exit 0
`
