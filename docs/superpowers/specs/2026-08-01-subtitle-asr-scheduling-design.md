# Subtitle ASR scheduling and timeout

Date: 2026-08-01  
Status: approved for implementation (user confirmed formula, ASR shape, concurrency, 8h cap, factor 2.0; continue after legacy-loop analysis)

## Goals

1. Scale subtitle `post_ingest` task timeout by media duration so long films are not aborted at a fixed 60 minutes.
2. Expose first-class Whisper engine/model config, including `faster-whisper`, without requiring operators to hand-edit every CLI flag in `shell`.
3. Cap subtitle concurrency with `post_ingest.subtitle_max_concurrent` (default **1**).
4. Ensure manually submitted subtitle processing always goes through the global `post_ingest` dispatcher (no sync bypass).

## Non-goals

- Changing lyric-task ASR (`TranscribeToVTT` / lyric worker) to use the subtitle queue (different domain).
- Hot-reload of ASR or concurrency without process restart (same as other `post_ingest` knobs).
- GPU auto-detection beyond what the ASR script already accepts via `--whisper-device` / `device`.
- Raising default `max_attempts` for subtitle (remains local default of 1 unless a later change).
- **Reviving `StartSubtitleTaskLoop` / `RunBatch` as a production path** — unsuitable for long ASR (see below).

## Legacy loop analysis (why not)

`StartSubtitleTaskLoop` is not started by the router today. If re-enabled for manual work:

- Uses `context.Background()` → no post_ingest lease/timeout; does not update `post_ingest_task`.
- Every tick resets `subtitle_task` from `running` → `pending` after **20 minutes**, which breaks hour-scale Whisper and can re-queue the same media.
- Startup `go runSubtitleWorkerOnce()` plus a 15s ticker allows **overlapping** `RunBatch` calls (up to 3 media each) with no global/subtitle slot.
- Parallel with the dispatcher causes dual-ledger drift (domain vs queue) and CPU contention.

**Verdict:** short sidecar/embedded jobs may finish; long ASR cannot run reliably. Manual subtitle must stay on post_ingest only; harden with regression tests.

## Confirmed product rules

| Rule | Decision |
|------|----------|
| Timeout formula | `timeout = min(8h, max(60m, duration_seconds × N))` |
| Factor `N` | `post_ingest.subtitle_timeout_realtime_factor`, default **2.0** |
| Missing / ≤0 duration | Use fixed base **60m** (then still capped by 8h, i.e. 60m) |
| Hard cap | **8 hours** |
| Base floor | **60 minutes** (current `TaskSubtitle` default) |
| ASR config shape | First-class `engine` / `model` / `language` / optional `device` under `subtitle.asr` |
| Engines | `whisper` \| `faster-whisper` \| `paraformer` |
| Shell compatibility | Existing `shell` templates keep working; Go injects missing flags from first-class fields |
| Subtitle concurrency | `post_ingest.subtitle_max_concurrent`, default **1**, valid **[1, max_concurrent]** |
| Manual path | Admin/schedule enqueue only; do not start legacy `StartSubtitleTaskLoop` |

## YAML shape

```yaml
post_ingest:
  max_concurrent: 4
  poster_max_concurrent: 2
  preview_max_concurrent: 1
  subtitle_max_concurrent: 1                    # default 1; [1, max_concurrent]
  subtitle_timeout_realtime_factor: 2.0         # default 2.0; must be > 0

subtitle:
  auto_on_scan: true
  asr:
    provider: shell                             # none | whisper_cli | shell
    engine: faster-whisper                      # whisper | faster-whisper | paraformer
    model: base                                 # whisper / faster-whisper model name
    language: zh
    device: ""                                  # optional; empty → script default
    whisper_path: ""                            # whisper_cli only
    extra_args: []
    shell: >-
      "tools/recognition/.venv/Scripts/python.exe"
      "tools/asr/asr_to_vtt.py"
      --input "{input}" --output-vtt "{output_vtt}"
```

Omit new keys → defaults above. Legacy configs that only set `shell` with embedded `--engine whisper --whisper-model base` continue to work unchanged when first-class fields are empty/default and the shell already contains those flags.

## Architecture

### 1. Duration-scaled subtitle timeout

**Where:** `internal/postingest/dispatcher.go` (and tests).

Today only poster/encrypt use size-based scaling via `sizedTaskTimeout`. Subtitle uses a fixed `WithTimeout(60m)`.

**Change:**

- Keep `Timeouts[TaskSubtitle] = 60 * time.Minute` as the **floor / base**.
- Before launching a subtitle worker (or in a subtitle-specific branch of `timeoutForTask`):
  1. Heartbeat-safe lookup: `SELECT COALESCE(duration, 0) FROM media WHERE id=?`.
  2. Compute:
     ```
     base = opts.Timeouts[TaskSubtitle]           // 60m
     factor = opts.SubtitleTimeoutRealtimeFactor // default 2.0
     if durationSec <= 0:
       timeout = base
     else:
       timeout = max(base, time.Duration(float64(durationSec)*factor) * time.Second)
     timeout = min(timeout, 8*time.Hour)
     ```
  3. Use `context.WithTimeout(parent, timeout)` for the task lifecycle (subtitle remains non-`sizedTaskTimeout` for file-size logic; duration path is separate).
- Wire factor from config in `buildDispatcherOptions`.
- Validate: factor must be `> 0` and preferably capped (e.g. `≤ 10`) to reject typos.

**Examples (factor=2.0):**

| Duration | Computed | Applied |
|----------|----------|---------|
| unknown / 0 | — | 60m |
| 45m | 90m | 90m |
| 2h | 4h | 4h |
| 5h | 10h | **8h cap** |

### 2. First-class ASR engine / model (+ faster-whisper)

**Config:** extend `ASRConfig` in `internal/config/config.go` and `internal/subtitle.ASRConfig`:

| Field | YAML | Notes |
|-------|------|--------|
| `Engine` | `engine` | `whisper` \| `faster-whisper` \| `paraformer` |
| `Model` | `model` | Model id for whisper / faster-whisper; paraformer may ignore or map later |
| `Language` | `language` | e.g. `zh` |
| `Device` | `device` | optional; maps to `--whisper-device` or faster-whisper device |

**Shell assembly (`provider: shell`):**

- Start from configured `shell` template (placeholders `{input}`, `{output_vtt}`, `{output_dir}` unchanged).
- If the rendered argv/string does **not** already contain the corresponding flag, append:
  - `--engine <engine>` (default `whisper` if engine empty and not in shell)
  - `--whisper-model <model>` when engine is whisper/faster-whisper and model set
  - `--whisper-language <language>` when language set
  - `--whisper-device <device>` when device set
- Prefer a small helper that parses/appends flags without breaking quoted Windows paths (reuse or extend existing shell build path).

**`whisper_cli` provider:** append model/language via `extra_args` only if not already present (minimal change; primary target is `shell` + `asr_to_vtt.py`).

**Script:** `tools/asr/asr_to_vtt.py`

- Add `--engine faster-whisper`.
- Implement with `faster_whisper.WhisperModel`; write WebVTT compatible with existing consumers.
- Document dependency: `pip install faster-whisper` (in script docstring / tools README if present).
- Keep `whisper` and `paraformer` paths.

**System options:** extend ASR save/load and probe (`CheckASRConfig`) so Admin can set engine/model/language/device; persist via existing yaml patch helpers.

**Defaults in sample config:** update `internal/config/default/config.yml` (and root `config.yml` if present) to document the new keys; do not force-migrate existing user `shell` strings.

### 3. Subtitle max concurrency

**Where:** mirror poster/preview slots in `Dispatcher`.

- Add `Subtitle int` to `DispatcherOptions` (default **1**).
- Add `subtitle chan struct{}` + `subtitleUsed` in acquire/release/`Snapshot`.
- `tryAcquire(TaskSubtitle)`: take global **and** subtitle sub-slot (same pattern as preview).
- Config: `PostIngestConfig.SubtitleMaxConcurrent` + YAML set-tracking + `Validate`:
  - `[1, MaxConcurrent]` (no hard `[1,2]` like preview).
- `buildDispatcherOptions` sets `opts.Subtitle`.
- Admin overview budget exposes `SubtitleLimit` / `SubtitleUsed`.

Priority bands unchanged: subtitle remains lowest band; burst slot still poster/encrypt only.

### 4. Manual subtitle → post_ingest only

**Current state:** Admin process/enqueue/reset/retry and scheduled `subtitle_process` already call `enqueueExplicitPostIngest` / `enqueueScheduledPostIngest`.

**Hardening:**

- Regression tests (extend `manual_postingest_test.go` / `router_loop_test.go`):
  - Router must not call `StartSubtitleTaskLoop`.
  - Manual subtitle handlers must not call `Subtitle.RunBatch` or sync `ProcessMedia` outside the adapter.
- Optionally mark `StartSubtitleTaskLoop` / `RunBatch` as deprecated in comments; leave bodies for tests if still referenced, or delete loop starter if unused and tests agree.
- Out of scope: lyric `TranscribeToVTT`.

## Error handling

| Case | Behavior |
|------|----------|
| Duration lookup fails | Fall back to base 60m; log at debug/warn; still run task |
| Factor invalid in config | Fail config validate at startup |
| `faster-whisper` not installed | ASR subprocess fails; post_ingest marks task failed with stderr (existing path) |
| Subtitle slots exhausted | Task stays waiting; dispatcher polls (existing) |
| Shell already has conflicting `--engine` | Do not duplicate; shell wins for that flag |

## Testing

1. Unit: timeout helper — zero duration → 60m; 3600s × 2.0 → 2h; huge duration → 8h cap.
2. Unit: config validate — subtitle concurrent defaults, bounds vs global, factor > 0.
3. Unit: dispatcher acquire — with `Subtitle=1`, second subtitle cannot acquire while first held (global free).
4. Unit/script: faster-whisper engine path smoke (skip if dependency missing) or mockable flag parse test in Go for shell injection.
5. Handler/router tests: no legacy subtitle loop; manual enqueue path only.

## Acceptance (F:\\tmp\\test)

1. Set `subtitle_max_concurrent: 1`, `subtitle_timeout_realtime_factor: 2.0`, `engine: faster-whisper`, `model: base`.
2. Enqueue a ~2h video with no sidecar: observed timeout budget ≥ 4h (and ≤ 8h); at most one subtitle running under load.
3. Manual “process subtitle” creates/uses `post_ingest_task` type `subtitle` and does not spawn a parallel RunBatch loop.
4. Restart required after config change.

## Implementation approach

**Chosen:** extend the existing post_ingest dispatcher and subtitle ASR shell path (no second worker). Spec → plan → implement in `media` (commercial); cherry-pick/PR to Vauldy if the change is community-safe.
