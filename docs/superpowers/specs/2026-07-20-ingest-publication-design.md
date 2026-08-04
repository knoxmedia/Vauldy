# Ingest Publication Design

**Date:** 2026-07-20
**Branch baseline:** `feature/ingest-publication` at Knox `087d9c6`
**Scope:** publication gating for newly discovered video media, plus the SQLite and build-identification reliability needed to make that gate trustworthy.

## Problem

`internal/scanner.Scanner.ScanLibraryFoldersWithContextAndCallbacks` currently commits a new `media` row with `status='active'`, then invokes `ScanCallbacks.OnMediaAdded`. `cmd/server/main.go` wires that callback through `scancoord.Coordinator` to `postingest.Enqueuer`, which creates only `poster`, `preview`, `keyframe`, `subtitle`, `atrack`, and `encrypt` rows. This has three observable gaps:

1. the media commit can succeed while planning fails, leaving a visible row with no ingest plan;
2. online scrape and enterprise ingest preparation are outside the aggregate;
3. `ListMedia`, `GetMedia`, library counts, and Home consumers do not distinguish processing media from published media.

The scanner callback path in this baseline is already the Knox-main fixed form: line 402 checks `callbacks.OnMediaAdded`, not `s.OnMediaAdded`. The regression must remain covered because Vauldy `657a2dd` still contains the old mismatch and community synchronization follows this work.

## Chosen architecture

Use an in-database publication aggregate and make scanner insertion plus plan creation one SQLite transaction. The scanner receives an `OnMediaDiscoveredTx(context.Context, *sql.Tx, mediaID int64, title, fileType string) error` planner callback. For a new video, the callback snapshots library/system settings, increments `media.ingest_generation`, creates one `media_ingest_run`, creates all `media_ingest_step` rows, and creates queue rows for queue-backed steps before the transaction commits. A planner error rolls back the media row, relationship updates, run, steps, and queue rows together.

This is the concrete reliability choice: **same-transaction planning**, not eventual repair as the primary guarantee. Startup repair remains a defensive invariant check for interrupted migrations, legacy rows, or manually modified databases; it is not allowed to excuse a failed scanner transaction.

Existing `post_ingest_task` remains the execution queue. It gains `ingest_run_id`, `ingest_step_id`, and `generation`; its uniqueness changes from `(media_id, task_type)` to `(media_id, generation, task_type)`. Scrape uses the existing `scrape_task` worker but links each row to an ingest step. Enterprise prepare is represented by a required `prepare` step only when the registered enterprise capability and `library.jit_prepare_on_ingest` snapshot require it; community builds do not plan that step.

## Publication state machine

`media.publication_state` has these values:

- `processing`: the current generation has at least one required step that is waiting or running. Ordinary APIs hide the media.
- `published`: every required step in the current generation is `done` or `skipped`. Ordinary APIs show the media.
- `degraded`: at least one required step is terminal `failed` after its retry budget, while no required step is waiting or running. Ordinary APIs show the media with an incomplete-processing marker; explicit/background retry may move it back to `processing`.
- `failed`: planning cannot be reconstructed automatically, the source vanished before planning, or an administrator permanently cancels the generation before visibility. Ordinary APIs hide it; admin APIs show it.
- `cancelled`: an administrator explicitly cancels an unpublished generation. Ordinary APIs hide it. Scanner cancellation alone cancels only work owned by that scan; the aggregator derives `cancelled` when that leaves no viable required work.

Transitions are generation-fenced:

```text
new video -> processing
processing --all required done/skipped--> published
processing --required retries exhausted--> degraded
processing --permanent plan/source failure--> failed
processing --explicit admin cancel--> cancelled
degraded --retry required step--> processing
degraded --all required done/skipped--> published
failed/cancelled --explicit repair generation--> processing
published --repair generation--> published (visibility retained while repair runs)
```

A repair generation for already visible legacy media does not change `publication_state` to `processing`; `media_ingest_run.preserve_visibility=1` keeps the media published or degraded until the repair finishes. If repair succeeds, it becomes `published`; if repair exhausts, it becomes `degraded` and records the new error.

`published_at` is first-publication time: set once on the first transition to `published` or `degraded`, never cleared by repair. `publication_error` is empty for `published`, contains a bounded aggregate of terminal required-step errors for `degraded`/`failed`/`cancelled`, and is cleared when a later generation fully succeeds.

## Schema and constraints

### `media` additions

- `publication_state TEXT NOT NULL DEFAULT 'published' CHECK (... IN ('processing','published','degraded','failed','cancelled'))`
- `published_at TIMESTAMP NULL`
- `publication_error TEXT NOT NULL DEFAULT ''`
- `ingest_generation INTEGER NOT NULL DEFAULT 0 CHECK (ingest_generation >= 0)`

Migration backfills all existing rows to `publication_state='published'` and `published_at=COALESCE(created_at,CURRENT_TIMESTAMP)` before ordinary visibility predicates are enabled. New non-video rows remain immediately `published`; only newly discovered videos enter `processing`.

### `media_ingest_run`

- `id INTEGER PRIMARY KEY AUTOINCREMENT`
- `media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE`
- `generation INTEGER NOT NULL CHECK (generation > 0)`
- `scan_task_id INTEGER NULL REFERENCES scan_task(id) ON DELETE SET NULL`
- `reason TEXT NOT NULL CHECK (reason IN ('scan','repair','manual_retry'))`
- `status TEXT NOT NULL CHECK (status IN ('processing','published','degraded','failed','cancelled'))`
- `preserve_visibility INTEGER NOT NULL DEFAULT 0 CHECK (preserve_visibility IN (0,1))`
- `config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json))`
- `error_message TEXT NOT NULL DEFAULT ''`
- `created_at`, `updated_at`, `finished_at`
- `UNIQUE(media_id,generation)`

Indexes: `(status,updated_at)` and `(scan_task_id,status)`.

### `media_ingest_step`

- `id INTEGER PRIMARY KEY AUTOINCREMENT`
- `run_id INTEGER NOT NULL REFERENCES media_ingest_run(id) ON DELETE CASCADE`
- `media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE`
- `generation INTEGER NOT NULL CHECK (generation > 0)`
- `step_type TEXT NOT NULL CHECK (step_type IN ('poster','scrape','preview','keyframe','subtitle','atrack','encrypt','prepare'))`
- `required INTEGER NOT NULL CHECK (required IN (0,1))`
- `status TEXT NOT NULL CHECK (status IN ('waiting','running','done','skipped','failed','cancelled'))`
- `attempts INTEGER NOT NULL DEFAULT 0`, `max_attempts INTEGER NOT NULL DEFAULT 3`
- `available_at`, `lease_owner`, `lease_until`, `last_error`, `started_at`, `finished_at`, `created_at`, `updated_at`
- `UNIQUE(run_id,step_type)`
- composite foreign key `(media_id,generation)` references `media_ingest_run(media_id,generation)`

Required is computed from the immutable snapshot. Poster and online scrape are required for newly discovered videos. Preview is required when `library.preview_extract=1`; subtitle and atrack follow `Config.SubtitleAutoOnScan()` and `Config.ATrackAutoOnScan()`; encrypt requires both global encryption and `library.encrypted_assets_enabled=1`; prepare follows the enterprise capability and `library.jit_prepare_on_ingest=1`. A disabled feature is omitted rather than inserted as skipped. Optional steps never block publication, but their terminal error is exposed to admins.

Queue links use foreign keys to the step and generation. Completion updates the linked step and then calls the aggregator in the same transaction. A stale worker whose generation is no longer current may finish its own run, but cannot mutate `media.publication_state`.

## Data flow

1. `scanner` probes and computes metadata, begins its existing transaction, and inserts the new media as `processing` with generation zero.
2. `publication.Planner.PlanNewMediaTx` reads library flags through the transaction, snapshots effective global flags supplied at startup, advances generation to one, inserts the run and steps, and inserts linked queue rows. Scrape is inserted into `scrape_task`; prepare is delegated through a registered transaction-safe planner capability. The transaction commits only after every required execution row exists.
3. Dispatchers claim queue work. Claim changes both queue and step to `running` under the same owner token. Completion/failure changes both records and runs `publication.AggregateTx`.
4. `AggregateTx` considers only required steps of the run matching `media.ingest_generation`. Waiting/running keeps processing; all done/skipped publishes; any terminal failure with no work pending degrades visibility.
5. Ordinary API SQL includes `m.publication_state IN ('published','degraded')`. Admin media endpoints explicitly opt into all states and return run/step details.

Scrape integration reuses `runScrapeTasksWithLimit`, `claimScrapeTask`, `completeScrapeTaskTx`, and `failScrapeTask`, adding owner/generation fencing and linked-step transitions. This repairs the disconnected auto-scrape path without relying on mtime callbacks: startup legacy repair queries all relevant existing videos, including unchanged files.

## Failure, cancellation, and restart semantics

- A planner or queue insert failure rolls back the new media transaction. Scanner reports the file error; a later scan retries discovery.
- Retryable worker failures retain waiting state until `max_attempts`; exhaustion sets the step failed and immediately re-aggregates to degraded-visible.
- Background retry calls `publication.RetryDegradedRuns` to scan degraded runs and requeue failed steps with a bounded backoff. It first transitions the media to `processing` only for never-visible media; visible degraded media stays visible during retry.
- Process shutdown leaves leased steps recoverable. Startup recovery returns expired `running` rows to waiting or terminal failure according to attempts, updates linked steps, and aggregates affected runs.
- Scan cancellation cancels waiting steps belonging to that `scan_task_id`; running workers are fenced by owner/generation. If cancellation occurs before first visibility, aggregate becomes cancelled. Repair runs with `preserve_visibility=1` retain prior visibility.
- Source deletion uses existing media cleanup and cascading run/step deletion. A worker that later commits fails its ownership/generation guard.
- Finalization failures are written to a new `scan_finalize_recovery` row in the same database with task/library/owner/desired status/error and retried by startup/background recovery until `finalizeAndRelease` succeeds or the lease is proven lost.

## SQLite reliability contract

Keep SQLite WAL and multiple writers. Do not add PostgreSQL or a process-wide writer actor.

Replace the single fixed half-second policy with `store.RetryPolicy` and operation names. Interactive HTTP writes use a short budget; planning/finalize use a multi-second context budget; heartbeat retry sleeps only while `time.Until(leaseUntil)-safetyMargin` remains. `readCancellation` uses the read policy. A busy heartbeat does not cancel a scan while the lease remains safely renewable.

`readCancellation` maps `sql.ErrNoRows` to a typed `ErrScanTaskMissing{TaskID}`. Logs for busy exhaustion, missing tasks, heartbeat, submit, and finalize include operation, absolute DB path, schema/user revision, owner token, task/library IDs, SQLite primary and extended codes, attempts, elapsed time, and remaining lease budget.

`monitor.Service.tick` coalesces realtime and due auto-scan into one request per library per tick, preferring `SourceMonitor`; coordinator submit coalesces against an active lease rather than persisting a second cancelled task. `scheduled_task` continues through `submitLibraryScan` and the same coordinator. Finalize recovery closes the current durability gap.

## API contract

Ordinary authenticated and API-client browsing endpoints return only published/degraded media:

- `GET /api/media` and every query variant;
- `GET /api/media/:id`, metadata/stats/play endpoints through `requireMediaAccess`;
- library `media_count` and folder-scoped counts;
- Home shelves/history joins and other direct media list queries.

A processing/failed/cancelled ID behaves as not found (`404`) to a non-admin, preventing existence leakage. Admins may use `GET /api/admin/media?publication_state=...` and `GET /api/admin/media/:id/ingest`; responses include `publication_state`, `published_at`, `publication_error`, `ingest_generation`, current run, and ordered steps. Ordinary list items include `publication_state`; practically this is `published` or `degraded`, allowing a degraded badge. Admin retry creates a new generation or requeues the current degraded generation with an audit reason.

## Frontend behavior

`MediaItem` gains the publication fields. `MediaPosterImg` resets `scrapedFailed` and removes inline `display:none` whenever the resolved poster URL or media generation changes. `HistoryContinueCard` and `RecentShelfCard` reset `posterFailed`/landscape fallback on URL or item generation changes. Processing and degraded tags are rendered in management views; ordinary Home can only receive degraded and shows a warning badge. Image retry is driven by changed URL/generation, not an unbounded timer.

## Migration and legacy repair

The migration is idempotent and transactional: add columns/tables/indexes, rebuild `post_ingest_task` where SQLite requires changing checks/uniqueness, copy rows as generation zero legacy tasks, and backfill existing media as published. Rollback on any validation failure leaves the pre-migration schema intact.

After workers start, `publication.RepairLegacyMedia` pages through existing active video rows without a run. It checks derived poster presence and current settings, creates a `reason='repair'`, `preserve_visibility=1` generation only when required output/task evidence is missing, and is idempotent under `UNIQUE(media_id,generation)` plus transactional generation increment. It does not depend on file mtime or scanner callbacks and never hides existing media.

## Build identity

`internal/buildinfo` exposes version, commit, build time, and dirty state injected by `build.ps1` `-ldflags -X`, and also reads Go build settings through `debug.ReadBuildInfo`, including `vcs.revision` and `vcs.modified`. Startup logs both sources and warns when injected metadata disagrees with Go VCS metadata. Release packaging rejects either a non-empty `git status --porcelain` or `vcs.modified=true` unless an explicit development switch is supplied; direct developer builds without metadata log a warning. This makes a deployed Vauldy/Knox revision diagnosable.

## Testing strategy

Every behavior follows RED/GREEN TDD: add one failing test, run the exact focused command and confirm the expected assertion failure, add the minimum implementation, then rerun focused and package suites. Core tests cover migration compatibility, atomic rollback, generation fencing, all state transitions, scrape linking, ordinary/admin visibility, poster recovery, SQLite busy budgets, coalescing, finalize recovery, legacy repair, build metadata, and a scanner-to-publication end-to-end path. The existing scanner callback regression remains and a coordinator callback test asserts the callback argument—not `Scanner.OnMediaAdded`—is invoked.

## Non-goals

- PostgreSQL migration or a complete single-writer actor.
- Replacing existing poster/preview/keyframe/subtitle/atrack/encrypt workers.
- Hiding legacy media while repair runs.
- Reprocessing unchanged media through scanner mtime callbacks.
- Making optional ingest work block publication.
- Changing playback transcoding policy beyond the existing applicable prepare hook.
- Committing changes as part of this documentation or implementation plan.
