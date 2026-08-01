# ASR engine select + one-click install (System Options)

Date: 2026-08-01  
Status: approved approach A (user confirmed)

## Goals

1. System Options → 语音识别：可选引擎（faster-whisper / whisper）与模型、语言、设备。
2. 一键安装按**当前所选引擎**安装 Python 依赖，并自动写回 `config.yml` + 内存配置。
3. 安装后配置可直接用于字幕 ASR（`provider=shell` + 一等字段 + 精简 shell）。

## Non-goals (v1)

- Paraformer / FunASR one-click install (UI may hide or show disabled “coming soon”).
- Downloading Whisper model weights during install (first ASR run downloads).
- Changing `whisper_cli` provider UX beyond keeping existing advanced fields.

## Product rules

| Rule | Decision |
|------|----------|
| Engines (installable) | `faster-whisper` (default), `whisper` (openai-whisper) |
| Models | `tiny`, `base`, `small`, `medium` (default `base`) |
| Language | free text / select common: `zh`, `en`, empty=auto; default `zh` |
| Device | `""` (auto), `cpu`, `cuda` |
| Install API | POST body includes selected `engine` (+ optional model/language/device); if omitted, use form/current config |
| Auto-config after install | `provider=shell`, set `engine`/`model`/`language`/`device`, shell = python + asr_to_vtt with placeholders only (no hardcoded --engine in shell; Go injects flags) |
| Preserve | `auto_on_scan`, `ai_proofread`, OCR untouched; merge into existing ASR not wipe unrelated |
| Probe | After install / connection test: for shell, verify placeholders + `python -c "import …"` for chosen engine |

## UI

- Engine `Select`: Faster-Whisper / OpenAI Whisper  
- Model `Select`: tiny / base / small / medium  
- Language + Device fields  
- Advanced collapse: provider, whisper_path, extra_args, shell (optional)  
- One-click install uses current form engine/model/language/device  

## Backend

- `InstallASR(ctx, mediaRoot, opts InstallASROptions)` with Engine  
- pip: `faster-whisper` vs `openai-whisper>=20231117`  
- `ASRDeploy` includes Engine, Model, Language, Device, Shell, Provider  
- `deployASRToOptions` maps all fields  
- Extend `CheckASRConfig` import probe for shell+engine  

## Files

- `internal/recognition/installer.go`  
- `api/handler/system_options_install.go`  
- `internal/subtitle/recognition_probe.go`  
- `web/src/api/client.ts`, `web/src/pages/SystemOptions.tsx`, i18n  
