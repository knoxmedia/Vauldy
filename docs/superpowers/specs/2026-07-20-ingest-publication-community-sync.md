# Ingest publication community synchronization record

**Status:** synchronization preparation only; no Vauldy checkout, commit, push, or pull request was created.

**Authority:** Task 16 of `docs/superpowers/plans/2026-07-20-ingest-publication.md`.

**Repository topology:** Knox commercial repository is `origin` (`github.com/knoxlab/knox-media.git`); Vauldy community repository is `upstream` (`github.com/knoxmedia/Vauldy.git`). Prepare any community work from an up-to-date `upstream/main` branch and port only the community-safe slices below. Do not merge the Knox feature branch wholesale.

## 1. Incident delta and scope

- Knox baseline for this worktree: `087d9c6`.
- Observed deployed Vauldy revision: `657a2dd` (`Merge pull request #11 from knoxmedia/vauldy/integrate-community`).
- Regression: the callback-enabled scanner path tested the scanner field, `s.OnMediaAdded`, rather than the callback supplied in the `ScanCallbacks` argument. A caller that supplied the argument callback while leaving the scanner field nil silently skipped it. If the scanner field held an unrelated callback, the wrong callback ran.
- Correct condition and invocation: check `callbacks.OnMediaAdded != nil` and invoke that argument callback with context, media ID, title, and file type after commit. The current implementation is at `internal/scanner/scanner.go`; the focused regression is `TestScannerCallbackRegressionUsesCallbacksField` in `internal/scanner/scanner_test.go`. The test deliberately makes `s.OnMediaAdded` panic, supplies the callback argument, and requires exactly one argument-callback invocation.

Focused verification:

```powershell
go test ./internal/scanner -run TestScannerCallbackRegressionUsesCallbacksField -count=1 -v
```

Expected result: PASS.

This feature branch is not merely that one-line callback fix. It implements a full generation-fenced publication gate, transaction planning, queue/scrape aggregation, visibility filtering, recovery, diagnostics, and UI behavior. Upstream should review the callback regression as its own first slice and must not describe the entire branch as a small scanner fix.

## 2. Community-portable implementation

The following logical batches are suitable for Vauldy cherry-pick after commits exist, or for reimplementation against current `upstream/main`. Paths are references, not a promise that Vauldy has identical surrounding code.

### Publication schema and migration

- `internal/store/migrations_ingest_publication.go`
- `internal/store/migrations_ingest_publication_test.go`
- community migration registration and schema checks in `internal/store/db.go` and `internal/store/db_test.go`
- publication states and generation columns in the media schema

The migration adds `publication_state`, `published_at`, `publication_error`, and `ingest_generation`; creates `media_ingest_run` and `media_ingest_step`; rebuilds `post_ingest_task` around `UNIQUE(media_id,generation,task_type)` and generation-linked foreign keys; and upgrades `scrape_task` with ingest/lease links. Existing media is backfilled as published so deployment does not hide the legacy library.

### Immutable planner and aggregate

- `internal/publication/types.go`
- `internal/publication/planner.go`
- `internal/publication/aggregate.go`
- `internal/publication/retry.go`
- focused tests under `internal/publication/*_test.go`

Planner implementation and tests confirm that every video plan always contains `poster`, `scrape`, and `keyframe`, all persisted with `required=1`. It additionally plans `preview` when the library enables preview extraction, `subtitle` and `atrack` when their process options are enabled, `encrypt` only when both the process-wide encryption option and library encrypted-assets setting are enabled, and `prepare` only when both the library flag and a real prepare capability are present. Every conditional step that is planned is also persisted as required and gates first visibility. The exact ordered step set is stored in the immutable configuration snapshot. Run completion updates media only when the run generation still equals `media.ingest_generation`; exhausted required work becomes degraded-visible, and retry can repair it without hiding it again.

### Scanner atomicity and queue links

- `internal/scanner/scanner.go`, `internal/scanner/scanner_test.go`
- `internal/postingest/enqueue.go`, `queue.go`, `scan_callback.go`, and matching tests
- planner/coordinator wiring in `cmd/server/main.go`

New media insertion, relationship synchronization, publication generation creation, step creation, and queue/scrape insertion occur in the caller-owned scanner transaction. A planning error rolls all of them back. The post-commit callback remains only for compatible non-publication callers.

### Scrape integration

- `api/handler/scrape_task.go`
- `api/handler/scrape_state_test.go` and scrape context/merge coverage already present in the handler package

Automatic scrape rows carry run, step, generation, owner, and lease fields. Claim/completion/failure are generation-fenced and aggregate in the same transaction. Manual scrape remains nullable and behavior-compatible.

### API visibility and administration

- `api/handler/media_query.go`, `media.go`, `library.go`, `user_permission.go`
- direct browse/read paths updated in `api/handler/` so alternate endpoints do not bypass publication visibility
- `api/handler/media_ingest.go`, `media_ingest_test.go`, `publication_browse_test.go`, `ingest_publication_e2e_test.go`
- route registration in `api/router.go`

Ordinary lists, details, counts, Home/read/play lookups return only `published` and `degraded` media. Admin routes can filter all publication states, inspect the current ordered run/steps, and retry current failed/cancelled work. Admin media fields include `publication_state`, `publication_error`, `published_at`, and `ingest_generation`; step inspection includes required/status, attempts/max attempts, availability, owner/lease, error, and timestamps.

### Frontend recovery and status

- `web/src/api/client.ts`
- `web/src/components/MediaPosterImg.tsx` and `MediaPosterImg.test.tsx`
- `web/src/pages/Home.tsx`, `Home.module.css`, and `pages/__tests__/Home.posterRecovery.test.tsx`
- `web/src/pages/MediaManager.tsx` and `pages/__tests__/MediaManager.publication.test.tsx`
- matching locale keys and i18n tests

Poster failure state resets when the source URL or ingest generation changes. Home can display degraded status; processing media is absent from ordinary API results. Management views expose publication state without making ordinary browse visibility depend on frontend-only filtering.

### SQLite reliability, recovery, and coalescing

- `internal/sqliteretry/retry.go` and tests
- `internal/store/sqlite_retry.go`, `sqlite_diagnostics.go`, and tests
- `internal/scancoord/coordinator.go`, `finalize_recovery.go`, `task11_test.go`, and coordinator tests
- scan finalize schema in `internal/store/db.go`
- `internal/monitor/service.go`, `api/handler/schedule_task.go`, and tests

These changes provide operation-specific busy budgets, primary/extended SQLite codes, DB identity, heartbeat retry bounded by the confirmed lease, typed missing-task behavior, durable finalize recovery, and one-request scan coalescing. Realtime and scheduled triggers share coordinator submission; an active lease returns the existing task without creating a cancelled loser row.

### Legacy repair

- `internal/publication/repair.go` and `repair_test.go`
- startup order in `cmd/server/main.go`
- interrupted task recovery in `internal/store/tasks_recovery.go`

Startup repair pages through visible legacy videos, creates idempotent `reason='repair'` generations only for missing required evidence, and preserves current visibility. Finalize recovery and interrupted queue recovery run before workers claim work; repair starts only after schema initialization and worker plumbing is available.

### Build identity and end-to-end coverage

- `internal/buildinfo/buildinfo.go` and tests
- `cmd/buildinfo-check/main.go`
- startup logging in `cmd/server/main.go` and tests
- `build.ps1`, `Dockerfile`, `.dockerignore`, and release documentation in `README.md`
- `cmd/server/scan_media_added_test.go`
- `api/handler/ingest_publication_e2e_test.go`

Build metadata records version, commit, UTC build time, dirty state, and Go VCS metadata. Release validation rejects dirty/unknown/mismatched source metadata by default. The E2E tests exercise hidden-until-complete, degraded visibility after exhaustion, restart recovery, and the scanner callback regression with real SQLite and fake external adapters only.

## 3. Knox-only / enterprise boundary

The narrow shared contract is `coreiface.IngestPreparePlanner`, whose transaction method plans an ingest prepare task against media/run/step/generation identifiers. The publication planner obtains an optional capability handle and adds the `prepare` step only when both the library setting is enabled and a real capability is registered.

Knox-only implementation and registration live under `internal/pretranscode/` plus Knox-specific schema/module wiring. Those implementation details must not be copied into the community PR. Vauldy should retain one of these equivalent community behaviors:

1. leave the prepare capability unregistered/nil (current intended no-op degradation), or
2. compile a narrow no-op adapter that advertises no capability.

With no capability, community plans omit `prepare`; media is not blocked on unavailable enterprise work. Do not set `PrepareAvailable` by feature flag alone: production planning requires a real planner handle. Do not publish private worker policy, proprietary pretranscode internals, license logic, deployment credentials, provider keys, or secrets.

## 4. Migration, ordering, compatibility, and rollback warnings

1. Back up the SQLite database before deploying this schema. Stop all old server processes and ensure only one migrator starts.
2. Apply/open the database with publication migration before starting scanner, scrape, post-ingest, monitor, repair, or finalize-recovery loops. Start recovery before normal workers claim work.
3. The migration is additive for `media` and new run/step/finalize tables, but it rebuilds `post_ingest_task` and may rebuild `scrape_task` to establish exact constraints and indexes. Treat this as a forward migration, not an instant binary rollback.
4. Legacy DB startup is deliberately strict. It verifies required CHECK/foreign-key/unique definitions, replaces stale uniqueness such as `(media_id,task_type)` with `(media_id,generation,task_type)`, validates copied row counts, recreates claim/run/step/media indexes, and runs `PRAGMA foreign_key_check`. Schema drift can make startup fail rather than silently run with unsafe constraints.
5. Existing media is backfilled to `published` with generation zero. Legacy repair uses visibility-preserving generations; do not change backfill to `processing`.
6. Do not run the older binary against the migrated production DB unless its schema behavior has been explicitly tested. A code rollback does not remove added columns/tables or restore rebuilt constraints. Preferred rollback is stop service, restore the pre-deploy DB backup, and redeploy the prior binary.
7. Deploy schema/planner before visibility filters. Applying filters without backfill can hide media; applying queue/scrape aggregation without generation constraints can let stale completion publish a newer generation.
8. Keep WAL and foreign keys enabled. Confirm publication, queue, scrape, and scan indexes exist on upgraded legacy databases, not only fresh databases.
9. `internal/config/default/config.yml` also removes a UTF-8 BOM and a stray literal `\n` token/trailing junk. Port this small YAML cleanup if Vauldy contains the same defect; it is not part of publication semantics, but it can break or confuse default configuration parsing. Keep the file UTF-8.

## 5. Verification and release commands

Task 15 complete-path verification commands, preserved exactly:

From the repository root:

```powershell
go test ./cmd/server ./api/handler -run 'Test(NewVideo|RestartRecovers|ScannerCallbackRegression)' -count=1 -v
go test ./... -count=1
git diff --check
```

From `web`:

```powershell
npm test -- --run
npm run build
```

The first command is the focused E2E gate. Expected results are PASS, frontend build exit code 0, and no output from `git diff --check`.

The focused Task 16 callback command and expected PASS are recorded once in section 1.


PowerShell release packaging from a clean, full repository checkout:

```powershell
.\build.ps1
```

For an explicitly non-release development artifact from a dirty tree:

```powershell
.\build.ps1 -AllowDirty
```

Trusted Docker release builds require a full clone with `.git` included in the build context (linked Git worktrees are not accepted by the current release guard). Supply source-matching values and leave `ALLOW_DIRTY` at its default false:

```powershell
docker build --build-arg VERSION=<git-describe> --build-arg COMMIT=<full-HEAD> --build-arg BUILD_TIME=<UTC-RFC3339> --build-arg DIRTY=false -t knox-media .
```

A dirty development Docker artifact must opt in explicitly and report `DIRTY=true`:

```powershell
docker build --build-arg ALLOW_DIRTY=true --build-arg VERSION=development --build-arg COMMIT=<full-HEAD> --build-arg BUILD_TIME=<UTC-RFC3339> --build-arg DIRTY=true -t knox-media .
```

## 6. Deployment operational checks

Before exposing traffic:

- Confirm startup emits `build_info` containing `version`, `commit`, `build_time`, `dirty`, `vcs_revision`, `vcs_time`, `vcs_modified`, `db_path`, `schema_version`, and `user_version`. Treat revision mismatch, unknown metadata, or unexpected dirty state as a release blocker.
- Check startup for `scan finalize recovery`, `publication legacy repair`, `publication degraded retry`, `post-ingest dispatcher`, and `scan coordinator` errors. A successful repair may log `publication legacy repair scheduled: <count> media`.
- For SQLite failures, retain structured fields: `operation`, `owner`, `path`, `schema_revision`, `user_revision`, `primary_code`, `extended_code`, `elapsed`, `attempts`, plus `task_id`, `library_id`, and `remaining_lease_budget` when applicable.
- In admin overview, inspect `post_ingest_queue.by_status`, `by_type`, `oldest_waiting_seconds`, `expired_lease_count`, running task `attempts`/`max_attempts`/`run_seconds`/`lease_owner`/`lease_until`, scan lease owner/expiry, resource budget usage, and process-scoped `sqlite_metrics` (`busy_retries`, `busy_exhausted`, progress/log batches and dropped logs).
- Sample ordinary and admin APIs: processing media must be absent/404 for ordinary users, visible through admin inspection, published/degraded counts must match browse results, and non-admin access to ingest administration must return 403.
- Exercise one required failure to exhaustion in staging: media should become degraded-visible, preserve `publication_error`, and be retryable. Exercise a stale-generation completion and verify it cannot publish the current generation.

## 7. Pending commit map and recommended upstream PR slices

There are currently no Task 1–16 commits on this worktree: the branch remains at the baseline named in section 1, and all implementation/document changes are uncommitted. Therefore there are no truthful Knox SHAs to cherry-pick. Commit separation is pending; never substitute the baseline revision or fabricated hashes as feature commits.

Recommended small, reviewable upstream sequence:

1. **Scanner callback regression** — `internal/scanner/scanner.go` plus the focused regression test. No publication dependency.
2. **SQLite reliability foundation** — policy retry, diagnostics, DB identity, and focused tests. Keep behavior-compatible `WithBusyRetry` wrapper.
3. **Publication schema migration** — media/run/step schema, queue/scrape rebuild validation, legacy published backfill, and migration tests. Depends on current Vauldy schema review.
4. **Planner + scanner transaction atomicity** — publication types/planner, transaction callback, linked queue/scrape insertion, rollback tests. Depends on schema.
5. **Aggregation + queue/scrape state links** — generation fencing, degraded retry, scrape ownership/leases. Depends on planner/schema.
6. **Ordinary visibility + admin API** — shared predicate, counts/read paths, inspect/retry routes. Depends on aggregation semantics.
7. **Frontend poster recovery and status** — API types, source/generation reset, degraded/admin presentation. Depends on API fields.
8. **Scan reliability** — heartbeat lease budgeting, durable finalize recovery, submission coalescing. Can be reviewed after SQLite reliability; coordinate schema ordering with publication migration.
9. **Legacy repair + startup order** — visibility-preserving repair and recovery sequencing. Depends on planner, queue/scrape workers, and migrations.
10. **Build identity + E2E/release gates** — build metadata, release guard, operational fields, full-path tests. Keep Docker release constraints explicit.

Vauldy should omit Knox pretranscode implementation and registration. If it retains the shared prepare interface for source compatibility, absence of registration must be a no-op and plans must omit `prepare`.

After commits are intentionally separated, replace the placeholders below with real reviewed SHAs; until then they are not executable instructions:

```text
callback regression: <pending commit separation>
SQLite reliability: <pending commit separation>
publication schema/planner/aggregation: <pending commit separation>
API/frontend/recovery/build/E2E: <pending commit separation>
enterprise prepare implementation: OMIT from Vauldy
```

## 8. Documentation checks

Run the exact Task 16 marker command from the authoritative plan; expected output is exactly four lines, one per required marker. Also run a static local-link check and `git diff --check`. This record intentionally contains no community mutation or invented cherry-pick SHA.
