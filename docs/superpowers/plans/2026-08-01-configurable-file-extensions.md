# Configurable File Extensions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let operators replace per-category media file extensions via `scan.file_extensions` in `config.yml`, applied process-wide to `GuessFileType` (scan + upload).

**Architecture:** Keep `GuessFileType` call sites unchanged. Add `fileutil.Configure` that atomically replaces package-level extension maps from optional config slices (nil = keep built-in defaults; non-nil = full replace including empty). Wire from `cmd/server` immediately after `config.Load`.

**Tech Stack:** Go, YAML (`gopkg.in/yaml.v3`), existing `pkg/fileutil` + `internal/config`.

**Spec:** `docs/superpowers/specs/2026-08-01-configurable-file-extensions-design.md`

---

## File map

| File | Responsibility |
|------|----------------|
| `pkg/fileutil/fileutil.go` | Built-in defaults, active maps, `GuessFileType`, `Configure` / `ResetForTest` |
| `pkg/fileutil/configure_test.go` | Unit tests for configure + guess behavior |
| `internal/config/config.go` | `FileExtensionsConfig` on `ScanConfig` |
| `cmd/server/main.go` | Apply config after load; fail startup on configure error |
| `config.yml` (repo sample) and embedded default YAML if present | Document schema under `scan:` |

---

### Task 1: fileutil.Configure + tests

**Files:**
- Modify: `pkg/fileutil/fileutil.go`
- Create: `pkg/fileutil/configure_test.go`

- [ ] **Step 1: Write failing tests**

```go
package fileutil

import "testing"

func TestConfigureReplacesVideoKeepsOtherDefaults(t *testing.T) {
	t.Cleanup(ResetForTest)
	video := []string{"mp4", ".TS"}
	if err := Configure(ExtensionConfig{Video: &video}); err != nil {
		t.Fatal(err)
	}
	if GuessFileType("a.mp4") != "video" || GuessFileType("a.ts") != "video" {
		t.Fatal("expected customized video")
	}
	if GuessFileType("a.mkv") != "other" {
		t.Fatalf("mkv should be other after replace, got %q", GuessFileType("a.mkv"))
	}
	if GuessFileType("a.mp3") != "audio" {
		t.Fatal("audio defaults must remain")
	}
}

func TestConfigureEmptyAudioDisablesAudio(t *testing.T) {
	t.Cleanup(ResetForTest)
	empty := []string{}
	if err := Configure(ExtensionConfig{Audio: &empty}); err != nil {
		t.Fatal(err)
	}
	if GuessFileType("a.mp3") != "other" {
		t.Fatal("empty audio list must match nothing")
	}
}

func TestConfigureRejectsBlankEntry(t *testing.T) {
	t.Cleanup(ResetForTest)
	bad := []string{"mp4", "  "}
	if err := Configure(ExtensionConfig{Video: &bad}); err == nil {
		t.Fatal("expected error")
	}
}

func TestConfigureDuplicatePrefersVideo(t *testing.T) {
	t.Cleanup(ResetForTest)
	video := []string{".dat"}
	audio := []string{".dat"}
	if err := Configure(ExtensionConfig{Video: &video, Audio: &audio}); err != nil {
		t.Fatal(err)
	}
	if GuessFileType("x.dat") != "video" {
		t.Fatalf("got %q", GuessFileType("x.dat"))
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`Configure` / `ResetForTest` / `ExtensionConfig` undefined)

```powershell
$env:PATH = "D:\program files\Go\bin;" + $env:PATH
go test ./pkg/fileutil/ -count=1 -run Configure
```

- [ ] **Step 3: Implement**

In `fileutil.go`:

```go
type ExtensionConfig struct {
	Video    *[]string
	Audio    *[]string
	Image    *[]string
	Document *[]string
}

func ResetForTest() { /* restore built-in maps under mu */ }

func Configure(cfg ExtensionConfig) error {
	// normalize: trim, lower, ensure leading '.'
	// reject blank after trim
	// for each non-nil category pointer, replace that map
	// build set of ext→category; if duplicate, keep first of video,audio,image,document and log.Printf warn
	// swap active maps under sync.RWMutex
	return nil
}

func GuessFileType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	mu.RLock()
	defer mu.RUnlock()
	// same switch against active maps
}
```

Preserve existing default maps as `defaultVideoExts` etc., copy into actives on init and `ResetForTest`.

`IsDocumentExtension` must use the active `docExts` map (same mutex).

- [ ] **Step 4: Tests PASS**

```powershell
go test ./pkg/fileutil/ -count=1
```

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(fileutil): allow configuring media extension maps"
```

---

### Task 2: Config types + load wiring

**Files:**
- Modify: `internal/config/config.go` (`ScanConfig` ~159–166)
- Modify: `cmd/server/main.go` after `config.Load` (~87–89)
- Optional test: `internal/config` YAML decode for pointers if no existing scan decode test

- [ ] **Step 1: Add config structs**

```go
type FileExtensionsConfig struct {
	Video    *[]string `yaml:"video"`
	Audio    *[]string `yaml:"audio"`
	Image    *[]string `yaml:"image"`
	Document *[]string `yaml:"document"`
}

type ScanConfig struct {
	FileHashOnScan                 *bool                  `yaml:"file_hash_on_scan"`
	FastFFprobe                    *bool                  `yaml:"fast_ffprobe"`
	PrecapturePosterTimeoutSeconds *int                   `yaml:"precapture_poster_timeout_seconds"`
	FileExtensions                 *FileExtensionsConfig  `yaml:"file_extensions"`
}
```

Helper on config (keeps main thin):

```go
func (c *Config) ApplyFileExtensions() error {
	if c == nil || c.Scan.FileExtensions == nil {
		return nil
	}
	fe := c.Scan.FileExtensions
	return fileutil.Configure(fileutil.ExtensionConfig{
		Video: fe.Video, Audio: fe.Audio, Image: fe.Image, Document: fe.Document,
	})
}
```

Avoid import cycle: `config` importing `fileutil` is fine if `fileutil` does not import `config`. Prefer applying in `main` instead if any cycle risk — **prefer apply in main**:

```go
cfg, err := config.Load(cfgPath)
// ...
if cfg.Scan.FileExtensions != nil {
	fe := cfg.Scan.FileExtensions
	if err := fileutil.Configure(fileutil.ExtensionConfig{
		Video: fe.Video, Audio: fe.Audio, Image: fe.Image, Document: fe.Document,
	}); err != nil {
		log.Fatalf("config file_extensions: %v", err)
	}
}
```

- [ ] **Step 2: Small YAML decode test** (optional but recommended)

Decode a snippet with `video: [.ts]` and assert `Scan.FileExtensions.Video` non-nil length 1.

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(config): wire scan.file_extensions into GuessFileType"
```

---

### Task 3: Document sample config

**Files:**
- Modify: `config.yml` (repo root sample) under existing `scan:`
- Modify: embedded default config YAML (`internal/config` go:embed source — locate via `defaultConfigYAML` / `//go:embed`)

- [ ] **Step 1: Add commented example** (do not force a full replace list into production samples unless product wants defaults explicit)

```yaml
scan:
  file_hash_on_scan: false
  fast_ffprobe: true
  # file_extensions:           # optional; omit category = built-in defaults; listed category = full replace
  #   video: [.mp4, .mkv, .avi, .mov, .wmv, .flv, .webm, .m4v, .mpeg, .mpg]
  #   audio: [.mp3, .flac, .wav, .aac, .ogg, .m4a, .wma, .aiff, .aif, .ape]
  #   image: [.jpg, .jpeg, .png, .gif, .webp, .bmp, .heic, .heif, .tif, .tiff, .svg, .cr2, .nef, .arw, .dng, .raf, .orf, .rw2]
  #   document: [.pdf, .doc, .docx, .xls, .xlsx, .ppt, .pptx, .txt, .md, .mdx, .html, .htm, .csv, .rtf, .epub, .mobi, .azw, .azw3]
```

- [ ] **Step 2: Commit**

```bash
git commit -m "docs(config): document scan.file_extensions overrides"
```

---

### Task 4: Smoke / acceptance

- [ ] **Step 1: Unit package green**

```powershell
go test ./pkg/fileutil/ ./internal/config/ ./internal/scanner/ -count=1 -timeout 120s
```

- [ ] **Step 2: Manual acceptance (optional)**  
Set `scan.file_extensions.video: [.mp4, .ts]`, restart, scan a folder with `.mkv` + `.ts` → only `.ts`/`.mp4` ingest as video.

- [ ] **Step 3: Final commit if any fixes**

---

## Spec coverage checklist

| Spec item | Task |
|-----------|------|
| Replace semantics per category | 1 |
| Omit keeps defaults | 1 |
| Empty list disables | 1 |
| Normalize `.` / case | 1 |
| Duplicate priority + warn | 1 |
| Config YAML types | 2 |
| Apply at server start | 2 |
| Sample docs | 3 |
| Scan + upload via GuessFileType | 1–2 (no call-site change) |
| Restart required | docs / ops (no hot reload) |

## Self-review notes

- No placeholders.
- `IsDocumentExtension` must follow configured doc map.
- Tests must `t.Cleanup(ResetForTest)` to avoid polluting parallel packages.
