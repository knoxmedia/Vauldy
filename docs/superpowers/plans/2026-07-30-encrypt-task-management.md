# Encrypt Task Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Task Manager encrypt tab (list/abort/reset/remove) + menu encrypt enqueues `post_ingest_task` instead of a manual goroutine.

**Architecture:** Extend `postingest.Queue` + `Dispatcher.CancelTask` (soft→hard); admin HTTP CRUD for encrypt tasks; change `EncryptMediaAssets` to enqueue; add TaskManager tab and widen media-menu gate (global encrypt on, not library flag).

**Tech Stack:** Go (Gin), SQLite `post_ingest_task`, React/Ant Design TaskManager, existing postingest dispatcher.

**Spec:** `docs/superpowers/specs/2026-07-30-encrypt-task-management-design.md`

---

### Task 1: Queue list / remove / reset for encrypt

**Files:**
- Modify: `media/internal/postingest/queue.go`
- Test: `media/internal/postingest/queue_test.go`

- [ ] Add `ListEncrypt(ctx, status, limit)` returning task rows (+ optional media title via join in handler instead).
- [ ] Add `Remove(ctx, id)` for waiting/failed/cancelled; sync linked step + AggregateTx.
- [ ] Add `ResetEncrypt(ctx, id)` forcing failed or stranded running → waiting (lease clear); reuse RetryExplicit patterns where safe.
- [ ] Unit tests for remove status guards and reset.

### Task 2: Dispatcher CancelTask soft→hard

**Files:**
- Modify: `media/internal/postingest/dispatcher.go`
- Test: `media/internal/postingest/dispatcher_test.go`

- [ ] `CancelTask(taskID)` soft-stops worker; after grace, isolate like unresponsive path with `FailureCancelled`, release budget.
- [ ] Test: running task cancelled releases GlobalUsed; status cancelled.

### Task 3: Admin encrypt task HTTP API + menu enqueue

**Files:**
- Create: `media/api/handler/encrypt_task.go` (+ test)
- Modify: `media/api/handler/asset_encrypt.go`, `media/api/router.go`, `media/api/handler/handler.go` (Queue wiring)
- Modify: `media/cmd/server/main.go` if handler deps need Queue/Dispatcher

- [ ] Routes under admin: `GET/POST cancel/reset/DELETE` for encrypt tasks.
- [ ] `EncryptMediaAssets` enqueues via Queue; returns queued/already_queued/already encrypted; no KickEncryptMediaManual.
- [ ] Handler tests.

### Task 4: Frontend TaskManager + menu gate + client

**Files:**
- Modify: `media/web/src/api/client.ts`, `media/web/src/pages/TaskManager.tsx`
- Modify: `media/web/src/components/mediaMenuItems.tsx` consumers (`Browse.tsx`, `Search.tsx`, etc.) for `showEncryptAsset`
- Modify: i18n `zh-CN.json`, `en.json`, `zh-TW.json`, `ja.json`, `ko.json`

- [ ] Client helpers for encrypt task APIs; update `encryptMediaAssets` response handling.
- [ ] Encrypt tab with filter/actions.
- [ ] Show encrypt menu when global encrypt available and not encrypted (ignore library flag).

### Task 5: Verify

- [ ] `go test` postingest + handler encrypt/asset paths.
- [ ] Frontend typecheck / relevant tests if present.
