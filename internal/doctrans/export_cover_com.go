package doctrans

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func exportCoverCOM(ctx context.Context, mediaRoot string, kind EngineKind, sourcePath, outPath string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("com cover requires windows")
	}
	if kind != EngineWPS && kind != EngineOffice {
		return fmt.Errorf("com cover: unsupported engine %s", kind)
	}
	if err := ensureExportCoverScript(mediaRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	engine := string(kind)
	script := ResolvePath(mediaRoot, "tools/doctran/export_cover_com.ps1")
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", script, "-Engine", engine, "-InputPath", sourcePath, "-OutputPath", outPath, "-MaxEdge", "480")
	if mediaRoot != "" {
		cmd.Dir = mediaRoot
	}
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s cover com: %w: %s", engine, err, trimOut(out))
	}
	if st, statErr := os.Stat(outPath); statErr != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("%s cover com: empty output", engine)
	}
	return nil
}

func ensureExportCoverScript(mediaRoot string) error {
	script := filepath.Join(mediaRoot, filepath.FromSlash("tools/doctran/export_cover_com.ps1"))
	if fileExists(script) {
		return nil
	}
	dir := filepath.Dir(script)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(script, []byte(exportCoverCOMScript), 0o644)
}

const exportCoverCOMScript = `# Knox Media — Office/WPS COM cover export
param(
  [Parameter(Mandatory=$true)][ValidateSet('office','wps')][string]$Engine,
  [Parameter(Mandatory=$true)][string]$InputPath,
  [Parameter(Mandatory=$true)][string]$OutputPath,
  [int]$MaxEdge = 480
)
$ErrorActionPreference = 'Stop'
$InputPath = (Resolve-Path -LiteralPath $InputPath).Path
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
$ext = [IO.Path]::GetExtension($InputPath).ToLower()
$outDir = Split-Path -Parent $OutputPath
if (-not (Test-Path $outDir)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }

function Get-PPTProgId {
  if ($Engine -eq 'office') { return 'PowerPoint.Application' }
  return 'Kwpp.Application'
}

function Export-PPTCover {
  $app = New-Object -ComObject (Get-PPTProgId)
  try {
    $pres = $app.Presentations.Open($InputPath, $true, $true, $false)
    try {
      $slide = $pres.Slides.Item(1)
      $w = $MaxEdge
      $h = [int]([Math]::Max(1, [Math]::Round($MaxEdge * 0.75)))
      $slide.Export($OutputPath, 'JPG', $w, $h)
    } finally { $pres.Close() | Out-Null }
  } finally { $app.Quit() | Out-Null }
}

switch ($ext) {
  { $_ -in '.ppt','.pptx' } { Export-PPTCover; break }
  default { throw "unsupported extension for direct cover export: $ext" }
}
if (-not (Test-Path -LiteralPath $OutputPath)) { throw 'output not created' }
`
