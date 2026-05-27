package doctrans

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/config"
)

// DetectEngines returns availability for all known engines (ordered by config priority).
func DetectEngines(mediaRoot string, cfg config.DocTransConfig) []EngineStatus {
	order := engineOrderFromConfig(cfg)
	byKind := map[EngineKind]EngineStatus{
		EngineOffice:      detectOffice(mediaRoot, cfg),
		EngineWPS:         detectWPS(mediaRoot, cfg),
		EngineLibreOffice: detectLibreOffice(mediaRoot, cfg),
	}
	out := make([]EngineStatus, 0, len(order))
	for _, k := range order {
		if st, ok := byKind[k]; ok {
			out = append(out, st)
		}
	}
	return out
}

func convertWithEngine(ctx context.Context, mediaRoot string, cfg config.DocTransConfig, kind EngineKind, sourcePath, tmpDir string) (string, error) {
	switch kind {
	case EngineLibreOffice:
		return convertLibreOffice(ctx, mediaRoot, cfg, sourcePath, tmpDir)
	case EngineOffice:
		return convertOffice(ctx, mediaRoot, sourcePath, tmpDir)
	case EngineWPS:
		return convertWPS(ctx, mediaRoot, sourcePath, tmpDir)
	default:
		return "", fmt.Errorf("unknown engine %q", kind)
	}
}

func firstAvailableEngine(mediaRoot string, cfg config.DocTransConfig) (EngineKind, EngineStatus) {
	for _, st := range DetectEngines(mediaRoot, cfg) {
		if st.Available {
			return st.Kind, st
		}
	}
	return "", EngineStatus{}
}

func writeCacheMeta(metaPath string, sourceMtime int64, engine EngineKind) {
	_ = os.WriteFile(metaPath, []byte(fmt.Sprintf("%d\t%s\n", sourceMtime, engine)), 0o644)
}

func readCacheMeta(metaPath string) (mtime int64, engine EngineKind) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return 0, ""
	}
	parts := strings.Fields(strings.TrimSpace(string(data)))
	if len(parts) >= 1 {
		_, _ = fmt.Sscanf(parts[0], "%d", &mtime)
	}
	if len(parts) >= 2 {
		engine = EngineKind(parts[1])
	}
	return mtime, engine
}

func cacheValid(metaPath string, sourceMtime int64, pdfMod time.Time, ttlDays int) bool {
	if ttlDays <= 0 {
		ttlDays = 30
	}
	if ttlDays > 0 && time.Since(pdfMod) > time.Duration(ttlDays)*24*time.Hour {
		return false
	}
	saved, _ := readCacheMeta(metaPath)
	if saved == 0 {
		return sourceMtime == 0
	}
	return saved == sourceMtime
}

func ensureConvertScript(mediaRoot string) error {
	script := filepath.Join(mediaRoot, filepath.FromSlash("tools/doctran/convert_com.ps1"))
	if fileExists(script) {
		return nil
	}
	dir := filepath.Dir(script)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(script, []byte(convertCOMScript), 0o644)
}

const convertCOMScript = `# Knox Media — Office/WPS COM to PDF
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
`
