# Configurable media file extensions

Date: 2026-08-01  
Status: approved for implementation (user confirmed approach)

## Goals

1. Allow operators to customize which file extensions map to `video` / `audio` / `image` / `document` via global `config.yml`.
2. Keep scan and upload classification consistent (both use `fileutil.GuessFileType`).
3. Preserve built-in defaults when a category is omitted from config.

## Non-goals

- Per-library extension lists.
- Admin UI / hot-reload without restart.
- Changing library-type filters (`ShouldScanFile`: photo→image, music→audio, document→document, default→video|audio).
- MIME maps beyond what is needed for document guessing (optional follow-up if a new doc ext needs MIME).

## Confirmed product rules

| Rule | Decision |
|------|----------|
| Config location | Global `config.yml` under `scan:` |
| Semantics when a category is present | **Full replace** of that category’s built-in list |
| Category omitted / null | Keep built-in defaults for that category |
| Empty list `[]` | Category matches no extensions |
| Scope | All `GuessFileType` callers (scan + upload) |
| Reload | Restart required |
| Duplicate ext across categories | First match wins: video → audio → image → document; log a warning at configure time |
| Ext format | Case-insensitive; leading `.` optional |

## YAML shape

```yaml
scan:
  file_hash_on_scan: false
  fast_ffprobe: true
  file_extensions:
    video: [.mp4, .m4v, .mkv, .webm, .avi, .mov, .wmv, .flv, .f4v, .mpeg, .mpg, .mpe, .m2v, .mpv, .ts, .m2ts, .mts, .tp, .trp, .vob, .mod, .tod, .3gp, .3g2, .ogv, .divx, .xvid, .asf, .rm, .rmvb, .mxf, .wtv, .dvr-ms]
    audio: [.mp3, .flac, .wav, .aac, .ogg, .oga, .opus, .m4a, .wma, .aiff, .aif, .ape, .wv, .mka, .ac3, .eac3, .dts, .dtshd, .mp2, .amr, .ra, .tak, .tta, .caf, .dsf, .dff]
    image: [.jpg, .jpeg, .png, .gif, .webp, .bmp, .heic, .heif, .tif, .tiff, .svg, .cr2, .nef, .arw, .dng, .raf, .orf, .rw2]
    document: [.pdf, .doc, .docx, .xls, .xlsx, .ppt, .pptx, .txt, .md, .mdx, .html, .htm, .csv, .rtf, .epub, .mobi, .azw, .azw3]
```

Omit `file_extensions` entirely → identical to today’s hardcoded defaults.

Partial override example (only video customized):

```yaml
scan:
  file_extensions:
    video: [.mp4, .mkv, .ts]
```

## Implementation approach

**Approach A (chosen):** at config load, call `fileutil.Configure(FileExtensionConfig)` which replaces package-level maps used by `GuessFileType`. Call sites stay unchanged.

### Config

Extend `ScanConfig`:

```go
type ScanConfig struct {
    // ...
    FileExtensions *FileExtensionsConfig `yaml:"file_extensions"`
}

type FileExtensionsConfig struct {
    Video    *[]string `yaml:"video"`
    Audio    *[]string `yaml:"audio"`
    Image    *[]string `yaml:"image"`
    Document *[]string `yaml:"document"`
}
```

Pointers distinguish “omit” (nil → default) from “empty replace” (`[]`).

### fileutil

- Keep built-in default maps as private constants / copies.
- `Configure(cfg FileExtensionConfig) error` (or accept the four optional slices) normalizes extensions and installs active maps (mutex or sync.Once-friendly atomic swap).
- `GuessFileType` reads active maps.
- Invalid entries (empty string after trim) skipped or rejected with error — prefer **reject config load** on empty/whitespace-only entries after normalize.
- Tests: omit → defaults; replace video; empty audio; `.TS` / `ts` normalize; duplicate across categories warns + priority.

### Wiring

- After successful config load in `cmd/server` (and any other binary that scans/uploads), apply `cfg.Scan.FileExtensions` before workers start.
- Update sample `config.yml` / default template comments with the schema and a short note that listed categories replace defaults.

## Acceptance

- No `file_extensions` in config → scan/upload behavior unchanged vs current defaults.
- `video: [.mp4, .ts]` → `.mkv` not ingested as video; `.ts` is.
- `audio: []` → music library scan adds no audio files.
- Upload of a customized extension is classified the same as scan.
- Restart required to pick up YAML changes.

## Risks

- Process-global mutate of `fileutil` maps: fine for single-process server; tests must reset defaults in `t.Cleanup`.
- Document MIME (`GuessDocumentMIME`) may not know newly added doc extensions — preview/transcode paths may fall back to generic MIME (acceptable for phase 1; document in comments).

## Follow-ups (deferred)

- Admin API to edit and persist extensions.
- Per-library overrides.
- Hot reload.
