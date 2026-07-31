# Encrypt task management & queue-backed menu encrypt

Date: 2026-07-30  
Status: approved for implementation (user confirmed approach)

## Goals

1. Add an **Encrypt** tab under Admin → Task Management (`/tasks`) for long-running `post_ingest_task` rows with `task_type=encrypt`.
2. Support **abort** (running), **reset** (failed / abnormal running), **remove** (waiting / failed / cancelled).
3. Media menu **Encrypt** enqueues into the unified post-ingest queue (scheduler + priority), instead of starting a fire-and-forget goroutine.

## Non-goals

- Managing all post-ingest types in one tab (poster/subtitle/etc. stay as today).
- Deleting media files or committed ciphertext when removing a queue row.
- Changing encrypt crypto / staging pipeline internals beyond cancelability hooks needed for abort.

## Confirmed product rules

| Action | Meaning |
|--------|---------|
| Abort | Running only: cooperative cancel first; if still not stopped within grace, force-release dispatcher slot and mark `cancelled` (no successful commit from that attempt). |
| Reset | `failed` or abnormal `running` (e.g. expired lease / stranded) → `waiting`, clear lease, re-schedule. |
| Remove | Delete queue row when status is `waiting`, `failed`, or `cancelled`. Do not touch media or `media_encrypted_assets`. |
| Menu Encrypt | Shown when global encrypted assets are configured and the item is **not** already encrypted; enqueues `TaskEncrypt` if not already waiting/running. **Library `encrypted_assets_enabled` is not required** for showing the menu or manual enqueue (user may encrypt a single item even when the library default is off). |

Menu scope: **all media types** the encryptor supports. Scan/ingest **auto**-enqueue may still respect library `encrypted_assets_enabled`; **manual** menu encrypt ignores that library flag (global encrypt must still be on).

## Current gaps

- `TaskManager.tsx`: no encrypt tab.
- `POST /media/:id/encrypt-assets` → `KickEncryptMediaManual` (background goroutine, **not** `post_ingest_task`).
- UI already shows “queued” copy but backend does not use the dispatcher queue.
- No admin list/cancel/reset/remove API for `post_ingest_task`.
- `Dispatcher` can cancel by scan id, not by single task id with soft→hard abort.

## Backend design

### APIs (admin auth)

Prefix suggestion: `/api/v1/post-ingest/encrypt` (or `/api/v1/encrypt/task` mirroring subtitle/transcode).

| Method | Path | Behavior |
|--------|------|----------|
| GET | `/task?status=&limit=` | List encrypt tasks (join media title/path/library when cheap). |
| POST | `/task/:id/cancel` | Abort running (soft then hard). Idempotent if already terminal. |
| POST | `/task/:id/reset` | Reset failed / stranded running → waiting via existing retry semantics (+ explicit lease clear). |
| DELETE | `/task/:id` | Remove waiting/failed/cancelled row; 409 if running. |

Optional bulk cleanup endpoints can mirror subtitle later; not required for v1.

### Menu encrypt path

Change `EncryptMediaAssets` to:

1. Validate access, **global** encrypt configured, not already encrypted (do **not** require library `encrypted_assets_enabled`).
2. Enqueue `TaskEncrypt` via `postingest.Queue.Enqueue` / dedicated helper (idempotent if waiting/running exists).
3. Return `202` with `{ ok, status: "queued"|"already_queued", task_id? }`.
4. Stop calling `KickEncryptMediaManual`.

Keep `KickEncryptMediaManual` only if needed for tests/legacy; prefer delete or leave unused.

### Dispatcher abort (soft → hard)

Add `CancelTask(taskID int64)` (or cancel by ownership token):

1. **Soft:** mark intent / call `workerState.stop(FailureCancelled, …)` so executor context cancels; renew/heartbeat stops treating as active success path.
2. **Hard (after grace, e.g. reuse `ExecutorStopGrace` or a dedicated cancel grace ~10–30s):** if still registered, unregister, `release` budget slot, `Fail(..., FailureCancelled)` or force status `cancelled` + clear lease (same fencing as shutdown isolation).
3. Staged encrypt journal remains recoverable via existing encryption stage reconciler (no commit on cancelled attempt).

### Queue helpers

- List encrypt rows with filters.
- `Remove(id)` for allowed statuses + `syncLinkedStepTx` / aggregate when linked to ingest step.
- Reuse `Retry` / `RetryExplicit` for reset where possible; ensure expired-lease running can reset without waiting for RecoverExpired alone.

## Frontend design

### TaskManager

New tab `encrypt` alongside transcode/scrape/…:

- Status filter + auto-refresh (same 10s pattern).
- Columns: id, media_id, title (if available), status, attempts, started_at, lease_until, last_error, actions.
- Actions: Stop (running), Reset (failed / abnormal running), Remove (waiting/failed/cancelled) via existing `ActionIcon*` + Popconfirm patterns.
- i18n under `pages.task_manager.*` (zh-CN / en / zh-TW / ja / ko).

### Media menu

- Gate `showEncryptAsset` by **global** encrypt capability + not already encrypted (include image where supported); **do not** hide solely because library `encrypted_assets_enabled` is off.
- Keep calling `encryptMediaAssets`; rely on API returning conflict/already_queued for messaging (`encrypt_asset_queued` / `encrypt_asset_already`).

## Testing

- Handler: enqueue vs already encrypted / already queued; cancel soft/hard; reset; remove status guards.
- Dispatcher: CancelTask releases budget; cancelled encrypt does not Complete.
- Frontend: TaskManager encrypt tab smoke / client wrappers (lightweight).
- Regression: scan auto-encrypt path unchanged unless intentionally extended.

## Rollout

Single deploy: menu starts queueing; Task Manager can observe/control. In-flight manual goroutines from old binary may still run until process restart; new cancels only affect dispatcher-owned tasks.
