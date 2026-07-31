# Task alignment (display) design

**Date:** 2026-07-31  
**Status:** Spec ready for user review (sections §1–§3 confirmed in chat)  
**Goal:** Make TaskManager and Admin post-ingest overview report **consistent counts and statuses** for dual-table task types, without collapsing to a single storage table.

## Decisions (locked)

| Topic | Choice |
|-------|--------|
| Alignment mode | **Display alignment** — shared counting/status口径; keep domain `*_task` tables |
| Count unit | **One per `media_id`** for the **current ingest generation** |
| Scope (phase 1) | Dual-table / shared-queue types only: `subtitle`, `preview`, `atrack`, `keyframe`, `encrypt` |
| Approach | **Shared alignment view** consumed by Admin overview + TaskManager summaries |

Out of scope for phase 1: scrape, scan, transcode, lyric, poster/thumbnail display alignment; deleting domain tables; changing encrypt execution model; auto-reopening historical `failed`+`pending` pairs (display must show `failed`; manual retry remains available).

## Problem

Two UIs observe different tables:

- **Admin / 后处理:** `post_ingest_task` grouped by `task_type` + `status` (row-oriented; multi-generation).
- **TaskManager / 任务管理:** domain tables (`subtitle_task`, `preview_task`, …) usually **one row per media**.

Observed divergences (production sample):

- Lazy create: `post_ingest` `waiting`/`cancelled` with **no** domain row yet.
- Status vocab: `pending` vs `waiting`; preview `ready` vs queue `done`.
- Multi-generation: multiple `post_ingest` `done` rows → one domain `done`.
- Split lifecycle: queue `failed` while domain still `pending` after restart reset / cancel.
- One-sided cleanup: TaskManager delete/cleanup touches domain only.

Encrypt already lists `post_ingest_task` in TaskManager; it still participates in the shared display vocabulary for consistency.

## Current-generation rule

For media `m`:

1. Prefer `post_ingest_task` rows where `generation = m.ingest_generation`.
2. **Legacy compatibility:** if `m.ingest_generation` is 0/NULL and only `generation=0` queue rows exist (unlinkedenqueue), include those.
3. If multiple queue rows somehow match, pick the one with greatest `id` (deterministic); counts still dedupe to one media.

Alignment **never** sums historical superseded generations into the live totals.

## Display status vocabulary

Unified labels used by both UIs for the five types:

| `display_status` | Meaning |
|------------------|---------|
| `waiting` | Queued / not started (includes domain `pending`) |
| `running` | Actively executing on either side |
| `done` | Successful terminal (includes preview domain `ready`) |
| `failed` | Failed and not re-queued |
| `cancelled` | Post-ingest cancelled |

### Domain → display (single side)

| Type | Domain table | Mapping |
|------|--------------|---------|
| subtitle | `subtitle_task` | `pending→waiting`, `running→running`, `done→done`, `failed→failed` |
| preview | `preview_task` | `waiting→waiting`, `ready→done`, `failed→failed` (no native `running`) |
| atrack | `atrack_task` | `waiting→waiting`, `done→done`, `failed→failed` |
| keyframe | `keyframe_task` | same as atrack |
| encrypt | _(none)_ | use `post_ingest_task.status` as display |

### Synthesis when both queue `Q` and domain `D` exist

1. Map each side to a display candidate.
2. Take the higher priority: **`running` > `waiting` > `failed` > `cancelled` > `done`**.
3. Hard rule: if `Q ∈ {failed, cancelled}` and `D ∈ {pending, waiting}`, result is **`failed` / `cancelled`** (do not treat restart residue as queued work).
4. If domain row missing: use queue only (after mapping).
5. Encrypt: queue only.

## Read API

Extend `GET /api/v1/admin/overview` and the overview SSE payload:

```json
"task_alignment": {
  "by_type": {
    "subtitle": { "waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0 },
    "preview":  { "waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0 },
    "atrack":   { "waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0 },
    "keyframe": { "waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0 },
    "encrypt":  { "waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0 }
  }
}
```

Semantics: after current-generation filter + `media_id` dedupe, group by synthesized `display_status`.

### UI consumption

- **AdminConsole:** For the five types, show `task_alignment.by_type` (replace or clearly supersede raw `post_ingest_queue.by_type` for those keys). Other task types keep existing row counts.
- **TaskManager:** Tab summary chips/filters for the five types use the same alignment totals (via overview field or a thin `GET /api/v1/admin/task-alignment` that shares the package). List rows render `display_status`; filters match display labels (`waiting` includes former `pending`).

Optional later: list endpoints return `display_status` explicitly so clients need no local mapping.

## Write-path requirements (so counts stay true)

| Event | Required behavior |
|-------|-------------------|
| Create `post_ingest_task` (planner / enqueue / manual) | Synchronously `Ensure*` domain row in waiting/pending when a domain table exists |
| Adapter starts work | Domain → `running` when applicable |
| Success / fail / cancel | Update **both** sides to matching terminal display intent |
| TaskManager cleanup-failed / delete | Also cancel or delete **current-generation** `post_ingest_task` (or mark `cancelled`) |
| Startup domain reset (`running`→`pending`/`waiting`) | Synthesis already yields `failed` if queue exhausted; do not invent phantom waiting counts |

Encrypt: no dual write beyond existing queue transitions.

## Implementation sketch

1. Package `internal/taskalign` (preferred) with:
   - status mappers + synthesis function (unit-tested),
   - `Compute(ctx, db) (Alignment, error)` for overview.
2. Wire into `api/handler/admin_overview.go` (+ stream).
3. Frontend: AdminConsole + TaskManager + `api/client.ts` types.
4. Write-path hooks: planner/postingest enqueue, adapters’ Ensure*, subtitle/preview/atrack/keyframe cleanup & retry handlers, `postingest_retry`.

Indexes: rely on existing `(task_type, status)` / media FKs; add only if profiling shows need for `(task_type, generation, status)`.

## Testing

**Unit**

- Priority order and hard rule `failed+pending→failed`.
- `ready→done`, `pending→waiting`.
- Missing domain row; encrypt queue-only.
- Multi-generation: only current generation counted once per media.

**API / integration**

- Overview includes `task_alignment` with expected buckets for a seeded DB.
- After enqueue (before claim): both UIs’ waiting media count match.
- After TaskManager cleanup-failed: both sides drop the same media from failed/waiting buckets.

**Acceptance (against known prod skew)**

- Subtitle waiting media count equals TaskManager “queued” media count under aligned filters.
- Subtitle done counts by media (not raw queue rows).
- Queue-failed + domain-pending media appear under **failed**, not waiting.

## Non-goals

- Aligning scrape / scan / transcode / lyric / poster in phase 1.
- Single-table migration or removing `subtitle_task` et al.
- Changing encrypt staging/commit protocol.
- Automatically re-queue historically split failed/pending rows.

## Risks

- Slightly more writes at enqueue (Ensure*).
- Overview query cost for five joins — mitigate with focused SQL and tests on realistic sizes.
- Filter label rename (`pending`→`waiting`) needs i18n copy check in TaskManager.

## Follow-ups (explicitly deferred)

- Phase 2: same alignment pattern for scrape/scan/transcode/lyric if product wants one dashboard vocabulary.
- List APIs returning `display_status` on every row.
- Optional “repair” job to reopen or reconcile historical split pairs.
