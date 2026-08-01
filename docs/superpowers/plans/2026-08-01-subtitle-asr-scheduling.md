# Subtitle ASR Scheduling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Duration-scaled subtitle timeouts, first-class Whisper/faster-whisper config, subtitle concurrency cap (default 1), and harden manual subtitle to post_ingest-only.

**Architecture:** Extend `postingest.Dispatcher` with duration-based timeout and a subtitle semaphore (mirror preview). Extend `ASRConfig` + shell flag injection + `asr_to_vtt.py` faster-whisper engine. Regression tests block legacy subtitle loop.

**Tech Stack:** Go, SQLite `media.duration`, Python `faster-whisper`, existing yaml config / system options.

**Spec:** `docs/superpowers/specs/2026-08-01-subtitle-asr-scheduling-design.md`

---

## File map

| File | Responsibility |
|------|----------------|
| `internal/postingest/dispatcher.go` | Subtitle slot, duration timeout helper, Snapshot |
| `internal/postingest/dispatcher_test.go` | Timeout + acquire tests |
| `internal/config/config.go` + `post_ingest_test.go` | New post_ingest + ASR fields/validate |
| `cmd/server/main.go` | Wire dispatcher + subtitle ASR fields |
| `internal/subtitle/service.go` + shell helper | Inject engine/model/language/device flags |
| `internal/subtitle/*_test.go` | Flag injection tests |
| `tools/asr/asr_to_vtt.py` | faster-whisper engine |
| `api/handler/system_options.go` + yaml_patch | Persist new ASR fields |
| `api/handler/admin_overview.go` | Budget subtitle used/limit |
| `api/router_loop_test.go` / manual tests | No StartSubtitleTaskLoop |
| `internal/config/default/config.yml` (+ root sample) | Document keys |

---

### Task 1: Subtitle timeout by duration

**Files:**
- Modify: `internal/postingest/dispatcher.go`
- Modify: `internal/config/config.go`, `post_ingest_test.go`
- Modify: `cmd/server/main.go`
- Test: `internal/postingest/dispatcher_test.go`

- [ ] Add `SubtitleTimeoutRealtimeFactor float64` to `DispatcherOptions` (default 2.0) and `subtitleTimeoutMax = 8 * time.Hour`.
- [ ] Add pure helper `subtitleTaskTimeout(base time.Duration, durationSec int64, factor float64) time.Duration` implementing `min(8h, max(base, durationSec*factor seconds))`; duration≤0 → base; factor≤0 → treat as 2.0 in helper or rely on config validate.
- [ ] In launch path for `TaskSubtitle`, look up `COALESCE(duration,0)` (heartbeat-safe like size lookup), apply helper, `WithTimeout`.
- [ ] Config: `subtitle_timeout_realtime_factor` default 2.0, validate `(0, 10]`.
- [ ] Tests for helper + config defaults.

### Task 2: subtitle_max_concurrent

**Files:**
- Modify: `internal/postingest/dispatcher.go`, `BudgetSnapshot`, `tryAcquire`/`release`, `DefaultDispatcherOptions`
- Modify: `internal/config/config.go`, tests
- Modify: `cmd/server/main.go` `buildDispatcherOptions`
- Modify: `api/handler/admin_overview.go` if it exposes budget JSON

- [ ] `Subtitle int` default 1; channel + used counters; acquire/release for `TaskSubtitle`.
- [ ] Validate `[1, MaxConcurrent]`.
- [ ] Test: with Subtitle=1, second subtitle cannot acquire while first held.

### Task 3: ASR first-class fields + faster-whisper

**Files:**
- Modify: `internal/config/config.go` `ASRConfig`
- Modify: `internal/subtitle/service.go` (+ new `shell_flags.go` if cleaner)
- Modify: `tools/asr/asr_to_vtt.py`
- Modify: system options / `yaml_patch.go`
- Modify: sample config.yml

- [ ] Fields: `engine`, `model`, `language`, `device`.
- [ ] When building shell ASR command, append missing `--engine` / `--whisper-model` / `--whisper-language` / `--whisper-device`.
- [ ] Implement `faster-whisper` in Python script; docstring `pip install faster-whisper`.
- [ ] Wire ApplyRecognition / NewService / system options save.

### Task 4: Manual path hardening

**Files:**
- Modify: `api/router_loop_test.go` or new test
- Modify: `api/handler/subtitle_task.go` (deprecate comment on loop)
- Modify: `api/handler/manual_postingest_test.go` / subtitle handlers scan

- [ ] Assert router source does not call `StartSubtitleTaskLoop`.
- [ ] Assert subtitle_task.go handlers do not call `RunBatch` (loop body may still contain it for dead code; handlers must enqueue only — already true; test Enqueue/Reset/Retry use enqueueExplicitPostIngest).

### Task 5: Verify

- [ ] `go test ./internal/postingest/ ./internal/config/ ./internal/subtitle/ ./api/handler/ -count=1` (or package-scoped as needed).
- [ ] Do not commit unless user asks.

---

## Timeout formula (canonical)

```
timeout = min(8h, max(60m, duration_sec * factor * time.Second))
factor default = 2.0
duration_sec <= 0 → 60m
```
