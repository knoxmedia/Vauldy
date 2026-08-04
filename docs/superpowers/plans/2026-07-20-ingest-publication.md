# Ingest Publication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep newly discovered videos hidden until their required ingest generation completes, then publish normally or degraded after retry exhaustion, while preserving visibility of legacy media.

**Architecture:** Add a generation-fenced publication aggregate and plan new video work inside the scanner's existing media transaction. Existing execution queues remain, but each task links to an immutable ingest run/step snapshot and aggregates completion atomically; ordinary APIs filter to published/degraded while admin APIs expose all states. SQLite remains WAL with operation-specific retry budgets and durable scan-finalize recovery.

**Tech Stack:** Go 1.22, `database/sql`, modernc SQLite, Gin, React 19, TypeScript 6, Vitest/Testing Library, PowerShell build scripts.

---

## File map

- Create `internal/publication/types.go`: publication/run/step states and aggregate input types.
- Create `internal/publication/planner.go`: immutable plan construction and `PlanNewMediaTx`.
- Create `internal/publication/aggregate.go`: generation-fenced state aggregation and retry transitions.
- Create `internal/publication/repair.go`: startup invariant and legacy media repair.
- Create `internal/publication/*_test.go`: focused planner, aggregate, and repair tests.
- Create `internal/store/migrations_ingest_publication.go` and `_test.go`: idempotent schema migration and legacy backfill.
- Modify `internal/store/db.go`, `db_test.go`: register schema/migration and validate constraints/indexes.
- Modify `internal/scanner/scanner.go`, `scanner_test.go`: transaction callback and rollback semantics.
- Modify `internal/postingest/types.go`, `enqueue.go`, `queue.go` and tests: generation/step links and aggregate transitions.
- Modify `api/handler/scrape_task.go`, scrape tests: linked scrape execution and aggregation.
- Modify `api/handler/media_query.go`, `media.go`, `library.go`, access/query tests: ordinary visibility filter and fields.
- Create `api/handler/media_ingest.go`, `_test.go`; modify `api/router.go`: admin inspection/retry endpoints.
- Modify `web/src/api/client.ts`, `components/MediaPosterImg.tsx`, `pages/Home.tsx`; create matching tests.
- Modify `internal/sqliteretry/retry.go`, `internal/store/sqlite_retry.go`, tests: policy-based retry and diagnostics.
- Modify `internal/scancoord/coordinator.go`, tests; create `finalize_recovery.go`, `_test.go`: lease-budget heartbeat and durable finalize.
- Modify `internal/monitor/service.go`, tests; `api/handler/schedule_task.go`, scan tests: submission coalescing.
- Create `internal/buildinfo/buildinfo.go`, `_test.go`; modify `cmd/server/main.go`, `main_test.go`, `build.ps1`.
- Add end-to-end tests in `cmd/server/scan_media_added_test.go` and retain the scanner callback regression.

## Phase 1: Schema and migration

### Task 1: Persist publication generations

**Files:**
- Create: `internal/store/migrations_ingest_publication.go`
- Create: `internal/store/migrations_ingest_publication_test.go`
- Modify: `internal/store/db.go:44-63,234-257,760-880`
- Modify: `internal/store/db_test.go:1-210`

- [ ] **Step 1: Write failing migration tests**

Add `TestMigrateIngestPublicationBackfillsExistingMediaVisible`, `TestMigrateIngestPublicationCreatesRunStepConstraints`, `TestMigrateIngestPublicationRebuildsPostIngestGenerationUniqueness`, and `TestMigrateIngestPublicationIsIdempotent`. Seed a pre-migration `media` row and duplicate task types in different generations; assert existing media becomes `published`, `published_at` is non-null, invalid states/step types are rejected, and `(media_id,generation,task_type)` permits one row per generation.

- [ ] **Step 2: Run migration tests and verify RED**

Run: `go test ./internal/store -run 'TestMigrateIngestPublication|TestOpenSQLiteSchema' -count=1 -v`

Expected: FAIL because `migrateIngestPublication` and the publication columns/tables do not exist.

- [ ] **Step 3: Implement the transactional migration**

Define `migrateIngestPublication(ctx context.Context, db *sql.DB) error`. Within one transaction, add the four media columns, create `media_ingest_run` and `media_ingest_step` with the exact checks from the design, rebuild `post_ingest_task` with nullable `ingest_run_id`/`ingest_step_id`, `generation NOT NULL DEFAULT 0`, and `UNIQUE(media_id,generation,task_type)`, copy existing rows as generation zero, validate row counts and `PRAGMA foreign_key_check`, then commit. Register it in `OpenSQLiteContext` before the database is returned.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `go test ./internal/store -run 'TestMigrateIngestPublication|TestOpenSQLiteSchema' -count=1 -v`

Expected: PASS, including a second migration invocation with unchanged row counts.

- [ ] **Step 5: Run the store package suite**

Run: `go test ./internal/store -count=1`

Expected: PASS.

## Phase 2: Planner and scanner atomicity

### Task 2: Build immutable ingest plans

**Files:**
- Create: `internal/publication/types.go`
- Create: `internal/publication/planner.go`
- Create: `internal/publication/planner_test.go`
- Modify: `internal/postingest/types.go:11-64`

- [ ] **Step 1: Write planner matrix tests**

Add `TestPlannerVideoSnapshotsRequiredSteps`, `TestPlannerDisabledFeaturesAreOmitted`, and `TestPlannerCommunityBuildOmitsPrepare`. Use `PlanOptions{SubtitleAuto, ATrackAuto, EncryptGlobal, PrepareAvailable bool}` and a library row containing `preview_extract`, `encrypted_assets_enabled`, and `jit_prepare_on_ingest`; assert exact ordered step sets and the decoded `config_snapshot_json`.

- [ ] **Step 2: Run planner tests and verify RED**

Run: `go test ./internal/publication -run TestPlanner -count=1 -v`

Expected: FAIL because package `internal/publication` and `Planner.PlanNewMediaTx` do not exist.

- [ ] **Step 3: Implement planner types and transaction API**

Define `StateProcessing`, `StatePublished`, `StateDegraded`, `StateFailed`, `StateCancelled`; step types `poster`, `scrape`, `preview`, `keyframe`, `subtitle`, `atrack`, `encrypt`, `prepare`; and:

```go
type PlanOptions struct { SubtitleAuto, ATrackAuto, EncryptGlobal, PrepareAvailable bool }
type NewMedia struct { MediaID, ScanTaskID int64; FileType string }
func (p *Planner) PlanNewMediaTx(ctx context.Context, tx *sql.Tx, media NewMedia) (Run, error)
```

`PlanNewMediaTx` increments `media.ingest_generation`, inserts the run/steps, inserts linked `post_ingest_task` rows, and calls an optional transaction-safe prepare planner. For video, poster and scrape are always required; conditional steps use the snapshot rules in the design.

- [ ] **Step 4: Run planner tests and verify GREEN**

Run: `go test ./internal/publication -run TestPlanner -count=1 -v`

Expected: PASS with exact step order and snapshot values.

### Task 3: Commit media and plan atomically

**Files:**
- Modify: `internal/scanner/scanner.go:38-85,351-406`
- Modify: `internal/scanner/scanner_test.go:85-141`
- Modify: `internal/postingest/scan_callback.go`
- Modify: `internal/postingest/scan_callback_test.go`
- Modify: `cmd/server/main.go:226-288`

- [ ] **Step 1: Write scanner atomicity and callback regressions**

Add `TestScanNewVideoRollsBackMediaWhenPlanFails`, `TestScanNewVideoCommitsMediaAndPlanTogether`, and `TestScanCallbacksUsesArgumentNotScannerField`. The last test sets `s.OnMediaAdded` to panic, passes `ScanCallbacks.OnMediaAdded`, and asserts the argument callback runs once. The rollback test injects `errors.New("plan rejected")` from the transaction callback and asserts zero `media`, run, step, and queue rows.

- [ ] **Step 2: Run scanner tests and verify RED**

Run: `go test ./internal/scanner -run 'TestScanNewVideo|TestScanCallbacksUsesArgumentNotScannerField' -count=1 -v`

Expected: FAIL because planning still runs after `tx.Commit` and cannot roll back the media row.

- [ ] **Step 3: Add the transaction callback**

Extend `ScanCallbacks` with:

```go
OnMediaDiscoveredTx func(context.Context, *sql.Tx, int64, string, string) error
```

Invoke it for newly inserted media after `relationshipsync.SyncTx` and before `tx.Commit`. Return its error so the file transaction rolls back. Keep the post-commit callback only for compatibility with non-publication callers, and ensure the coordinator path supplies only `OnMediaDiscoveredTx`. Wire `publication.Planner` in `cmd/server/main.go` using effective config flags and the enterprise prepare capability.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `go test ./internal/scanner ./internal/postingest ./cmd/server -run 'TestScanNewVideo|TestScanCallbacksUsesArgumentNotScannerField|TestScan100Media' -count=1 -v`

Expected: PASS; `TestScan100MediaEnqueuesWithoutFFmpegOrGoroutineFanout` is updated to assert 100 runs and exact linked steps instead of post-commit enqueuer calls.

## Phase 3: Task completion aggregation

### Task 4: Aggregate linked step outcomes

**Files:**
- Create: `internal/publication/aggregate.go`
- Create: `internal/publication/aggregate_test.go`
- Modify: `internal/postingest/queue.go:177-487`
- Modify: `internal/postingest/queue_test.go`
- Modify: `internal/postingest/dispatcher.go`

- [ ] **Step 1: Write state-transition and generation-fence tests**

Add `TestAggregatePublishesWhenRequiredStepsDone`, `TestAggregateDegradesWhenRequiredStepExhausted`, `TestAggregateOptionalFailureDoesNotBlock`, `TestAggregateStaleGenerationCannotPublish`, `TestRetryDegradedStepKeepsVisible`, and `TestRetryDegradedRunsRequeuesExhaustedRequiredStep`. Assert `published_at` is set once and `publication_error` clears after successful retry.

- [ ] **Step 2: Run aggregation tests and verify RED**

Run: `go test ./internal/publication -run TestAggregate -count=1 -v`

Expected: FAIL because `AggregateTx` does not exist.

- [ ] **Step 3: Implement generation-fenced aggregation**

Implement `AggregateTx(ctx, tx, runID)` to read required step counts, update the run, and update media only when `run.generation=media.ingest_generation`. Implement `RetryDegradedRuns(ctx, db, limit)` to requeue exhausted required steps with bounded availability and preserve degraded visibility while retrying. Update `Queue.Claim`, `Complete`, `Fail`, `RecoverExpired`, `CancelScan`, `Retry`, and `RetryExplicit` so queue and linked step transitions occur in one transaction and call `AggregateTx` before commit. Preserve owner-token checks; start the bounded degraded-retry loop from `cmd/server/main.go`.

- [ ] **Step 4: Verify GREEN and queue compatibility**

Run: `go test ./internal/publication ./internal/postingest -run 'TestAggregate|TestQueue|TestRecover|TestDispatcher' -count=1 -v`

Expected: PASS, including retry exhaustion transitioning new media to degraded.

## Phase 4: Scrape and prepare integration

### Task 5: Link automatic scrape to publication

**Files:**
- Modify: `api/handler/scrape_task.go:26-170,355-520`
- Modify: `api/handler/scrape_state_test.go`
- Modify: `api/handler/scrape_context_test.go`
- Modify: `api/handler/scrape_merge_test.go`
- Modify: `internal/store/db.go` scrape schema section

- [ ] **Step 1: Write linked scrape tests**

Add `TestClaimScrapeTaskClaimsIngestStep`, `TestCompleteScrapeTaskAggregatesPublication`, `TestScrapeExhaustionDegradesMedia`, and `TestStartupScrapeLoopProcessesScannerPlan`. Assert owner/generation fencing and that a stale scrape completion cannot publish a newer generation.

- [ ] **Step 2: Run scrape tests and verify RED**

Run: `go test ./api/handler -run 'Test.*Scrape.*(Ingest|Publication|Degrades|ScannerPlan)' -count=1 -v`

Expected: FAIL because `scrape_task` has no run/step/generation link and completion does not aggregate.

- [ ] **Step 3: Implement linked scrape execution**

Add `ingest_run_id`, `ingest_step_id`, `generation`, `lease_owner`, and `lease_until` to scrape migration/schema. Change planner insertion to create the scrape row transactionally. Change claim, `completeScrapeTaskTx`, and `failScrapeTask` to update the linked step and call `publication.AggregateTx` in the same transaction. Keep manual scrape rows nullable and behavior-compatible.

- [ ] **Step 4: Run scrape suites and verify GREEN**

Run: `go test ./api/handler -run 'Test.*Scrape|TestApplyScrapeLocalImages' -count=1`

Expected: PASS; automatic scrape works without a scanner mtime change.

### Task 6: Represent applicable enterprise prepare

**Files:**
- Modify: `internal/coreiface/enterprise.go`
- Modify: `internal/pretranscode/task.go:45-140`
- Create: `internal/publication/prepare_test.go`
- Modify: `cmd/server/main.go:318-350`

- [ ] **Step 1: Write prepare capability tests**

Add `TestPlannerRequiresPrepareWhenEnterpriseCapabilityAndLibraryFlagEnabled` and `TestPrepareCompletionPublishesFinalRequiredStep`. Test the community registry with no capability and an injected enterprise planner that creates a linked pretranscode task.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/publication ./internal/pretranscode -run 'Test.*Prepare' -count=1 -v`

Expected: FAIL because no transaction-safe ingest prepare capability exists.

- [ ] **Step 3: Add the narrow capability**

Define `coreiface.IngestPreparePlanner` with `PlanIngestPrepareTx(context.Context,*sql.Tx,mediaID,runID,stepID,generation int64) error` and a completion hook that updates the linked step. Register it only in the enterprise pretranscode module. Do not add prepare to community plans.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/publication ./internal/pretranscode ./cmd/server -run 'Test.*Prepare|TestEnterprise' -count=1`

Expected: PASS in both empty-registry and injected-capability cases.

## Phase 5: API visibility and admin contract

### Task 7: Filter ordinary media queries and counts

**Files:**
- Modify: `api/handler/media_query.go:36-46,131-230`
- Modify: `api/handler/media.go:35-155`
- Modify: `api/handler/library.go:45-198`
- Modify: `api/handler/user_permission.go`
- Modify: `api/handler/media_query_test.go`
- Modify: `api/handler/library_list_performance_test.go`
- Modify: `api/handler/access_control_test.go`

- [ ] **Step 1: Write ordinary/admin visibility tests**

Add `TestListMediaHidesUnpublishedAndReturnsDegraded`, `TestGetMediaReturns404ForProcessingToOrdinaryUser`, `TestGetMediaAdminCanInspectProcessing`, `TestListLibrariesCountsOnlyPublishedAndDegraded`, and `TestFolderScopedCountFiltersPublicationState`.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./api/handler -run 'Test(ListMediaHides|GetMedia.*Processing|ListLibrariesCounts|FolderScopedCountFilters)' -count=1 -v`

Expected: FAIL because all active media are currently returned and counted.

- [ ] **Step 3: Add a single visibility predicate**

Add `mediaListSpec.IncludeUnpublished bool`, set it only for admin handlers, and append `AND m.publication_state IN ('published','degraded')` to the materialized candidate CTE otherwise. Apply the same predicate to `GetMedia`, `requireMediaAccess`, grouped library counts, folder-scoped counts, and direct Home/play lookups. Return `publication_state`, `published_at`, `publication_error`, and `ingest_generation` in admin responses; ordinary list responses include `publication_state` only.

- [ ] **Step 4: Verify GREEN and query shape**

Run: `go test ./api/handler -run 'Test(ListMedia|GetMedia|ListLibraries|FolderScoped|BuildMediaQuery)' -count=1`

Expected: PASS with bounded candidate CTE and constant query-count tests unchanged.

### Task 8: Add admin ingest inspection and retry

**Files:**
- Create: `api/handler/media_ingest.go`
- Create: `api/handler/media_ingest_test.go`
- Modify: `api/router.go:245-370`
- Modify: `web/src/api/client.ts`

- [ ] **Step 1: Write endpoint tests**

Add `TestAdminListMediaCanFilterPublicationState`, `TestAdminGetMediaIngestReturnsCurrentOrderedSteps`, `TestAdminRetryDegradedIngestRequeuesFailedRequiredSteps`, and `TestOrdinaryUserCannotAccessMediaIngestAdminRoutes`.

- [ ] **Step 2: Run endpoint tests and verify RED**

Run: `go test ./api/handler -run 'TestAdmin.*(Publication|Ingest)|TestOrdinaryUserCannotAccessMediaIngest' -count=1 -v`

Expected: FAIL with missing routes/handlers.

- [ ] **Step 3: Implement admin contract**

Register `GET /api/admin/media`, `GET /api/admin/media/:id/ingest`, and `POST /api/admin/media/:id/ingest/retry` under `RequireAdmin`. Validate state values, return ordered run/step fields, and retry only failed/cancelled current-generation steps or create a new manual-retry generation for terminal failed/cancelled media.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./api/handler -run 'TestAdmin.*(Publication|Ingest)|TestOrdinaryUserCannotAccessMediaIngest' -count=1`

Expected: PASS with `403` for non-admin access.

## Phase 6: Frontend recovery and status

### Task 9: Recover poster rendering when URLs change

**Files:**
- Modify: `web/src/api/client.ts` `MediaItem`
- Modify: `web/src/components/MediaPosterImg.tsx`
- Create: `web/src/components/MediaPosterImg.test.tsx`
- Modify: `web/src/pages/Home.tsx:99-170,254-320`
- Create: `web/src/pages/__tests__/Home.posterRecovery.test.tsx`

- [ ] **Step 1: Write failing UI tests**

Add tests named `restores hidden image when poster source changes`, `retries Home recent poster after generation changes`, and `renders degraded badge`. Rerender the same media ID with a new `poster_url`/`ingest_generation` after firing `error`; assert the image is visible and the new URL loads.

- [ ] **Step 2: Run UI tests and verify RED**

Run: `npm test -- --run src/components/MediaPosterImg.test.tsx src/pages/__tests__/Home.posterRecovery.test.tsx` from `web`.

Expected: FAIL because inline `display:none` and `posterFailed` survive prop changes.

- [ ] **Step 3: Implement source-key resets**

Use `useEffect` keyed by resolved source and `ingest_generation` to reset `scrapedFailed`, `posterFailed`, and landscape fallback. Use an image ref to clear `style.display`. Add processing/degraded tags to management cards; Home renders only the degraded warning because processing is API-filtered.

- [ ] **Step 4: Run UI tests and build**

Run from `web`: `npm test -- --run src/components/MediaPosterImg.test.tsx src/pages/__tests__/Home.posterRecovery.test.tsx src/pages/__tests__/Home.requests.test.tsx`

Expected: PASS.

Run from `web`: `npm run build`

Expected: exit 0 with TypeScript and Vite build successful.

## Phase 7: SQLite reliability

### Task 10: Add operation-specific retry policies and diagnostics

**Files:**
- Modify: `internal/sqliteretry/retry.go`
- Modify: `internal/store/sqlite_retry.go`
- Modify: `internal/store/sqlite_retry_test.go`
- Create: `internal/store/sqlite_diagnostics.go`
- Create: `internal/store/sqlite_diagnostics_test.go`

- [ ] **Step 1: Write retry-budget tests**

Add `TestWithBusyRetryPolicyStopsAtBudget`, `TestWithBusyRetryPolicyUsesHeartbeatLeaseBudget`, `TestSQLiteDiagnosticIncludesPathRevisionOwnerAndExtendedCode`, and preserve all existing fixed-policy compatibility tests.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/store ./internal/sqliteretry -run 'TestWithBusyRetryPolicy|TestSQLiteDiagnostic' -count=1 -v`

Expected: FAIL because `RetryPolicy`, operation labels, and structured diagnostics do not exist.

- [ ] **Step 3: Implement policy-based retry**

Define `RetryPolicy{Operation string; MaxElapsed time.Duration; BaseBackoff, MaxBackoff time.Duration}` and `WithBusyRetryPolicy`. Derive attempts from elapsed/context budget, retain jitter, and expose modernc primary/extended code. Add DB identity initialized by `OpenSQLiteContext`: absolute path, `PRAGMA schema_version`, and `PRAGMA user_version`. Keep `WithBusyRetry` as the short-policy wrapper for compatibility.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/store ./internal/sqliteretry -count=1`

Expected: PASS, including cancellation and existing five-attempt tests.

### Task 11: Make scan heartbeat and finalization durable

**Files:**
- Modify: `internal/scancoord/coordinator.go:18-20,264-447`
- Modify: `internal/scancoord/coordinator_test.go`
- Create: `internal/scancoord/finalize_recovery.go`
- Create: `internal/scancoord/finalize_recovery_test.go`
- Modify: `internal/store/db.go` scan schema section

- [ ] **Step 1: Write heartbeat, missing-task, and recovery tests**

Add `TestCoordinatorBusyHeartbeatRetriesUntilLeaseBudget`, `TestCoordinatorBusyHeartbeatDoesNotKillScanBeforeDeadline`, `TestReadCancellationReturnsErrScanTaskMissing`, `TestFinalizeFailurePersistsRecovery`, and `TestRecoverPendingFinalizationsCompletesAndDeletesRecord`.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/scancoord -run 'TestCoordinatorBusyHeartbeat|TestReadCancellationReturns|TestFinalizeFailure|TestRecoverPendingFinalizations' -count=1 -v`

Expected: FAIL because one busy heartbeat cancels scanning, missing rows return raw `sql.ErrNoRows`, and finalize errors are only logged.

- [ ] **Step 3: Implement lease-budget retries and recovery**

Track the last confirmed lease deadline. Retry cancellation read and renewal while deadline minus a safety margin remains. Define `type ErrScanTaskMissing struct{ TaskID int64 }`. Add `scan_finalize_recovery` and persist desired final status/error/owner when `finalizeAndRelease` fails; retry at startup and in a bounded background loop. Emit all diagnostic fields defined in the design.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/scancoord -count=1`

Expected: PASS with injected busy periods and restart recovery.

### Task 12: Coalesce monitor and scheduled submissions

**Files:**
- Modify: `internal/monitor/service.go:59-112`
- Modify: `internal/monitor/service_test.go:73-149`
- Modify: `internal/scancoord/coordinator.go:133-247`
- Modify: `internal/scancoord/coordinator_test.go`
- Modify: `api/handler/schedule_task.go:185-250`
- Modify: `api/handler/scan_task_test.go`

- [ ] **Step 1: Write coalescing tests**

Replace the old two-submit expectation with `TestScanSourcesCoalesceRealtimeAndAutoScanPerTick`; add `TestCoordinatorActiveLeaseReturnsExistingWithoutCancelledTaskRow` and `TestScheduledTaskUsesSharedCoordinatorCoalescing`.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/monitor ./internal/scancoord ./api/handler -run 'Test.*Coalesc|TestScheduledTaskUsesShared' -count=1 -v`

Expected: FAIL because realtime+auto submits twice and coordinator stores a cancelled loser row.

- [ ] **Step 3: Implement one-request coalescing**

In `monitor.Service.tick`, choose one source per library (`monitor` when realtime is enabled, otherwise `scheduled`). In `Coordinator.Submit`, inspect/acquire the lease before inserting a new scan task; when an unexpired lease exists return `ExistingTaskID` with no new row. Keep `scheduled_task` routed through `submitLibraryScan`.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/monitor ./internal/scancoord ./api/handler -run 'Test.*Scan|Test.*Coalesc|TestScheduledTaskUsesShared' -count=1`

Expected: PASS and one task row per accepted submission.

## Phase 8: Legacy repair

### Task 13: Repair missing legacy work without hiding media

**Files:**
- Create: `internal/publication/repair.go`
- Create: `internal/publication/repair_test.go`
- Modify: `cmd/server/main.go:84-110,249-267`
- Modify: `internal/store/tasks_recovery.go`

- [ ] **Step 1: Write repair tests**

Add `TestRepairLegacyMediaCreatesGenerationForMissingPoster`, `TestRepairLegacyMediaIncludesUnchangedMtime`, `TestRepairLegacyMediaIsIdempotent`, `TestRepairLegacyMediaPreservesPublishedVisibility`, and `TestRepairLegacyMediaSkipsCompleteEvidence`.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/publication -run TestRepairLegacyMedia -count=1 -v`

Expected: FAIL because legacy repair does not exist.

- [ ] **Step 3: Implement paged repair**

Implement `RepairLegacyMedia(ctx, db, planner, batchSize)` using keyset pagination on active video IDs. Detect poster through `media_derived_assets`/existing poster metadata and task evidence for currently required settings. Create `reason='repair'`, `preserve_visibility=1` runs transactionally; never set an existing published media to processing. Start it after queue/scrape workers are initialized and rerun safely on every startup.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/publication ./internal/store -run 'TestRepairLegacyMedia|TestResetInterrupted' -count=1`

Expected: PASS; repeated repair creates no duplicate generation.

## Phase 9: Build metadata and end-to-end validation

### Task 14: Identify deployed revision and dirty builds

**Files:**
- Create: `internal/buildinfo/buildinfo.go`
- Create: `internal/buildinfo/buildinfo_test.go`
- Modify: `cmd/server/main.go:62-88`
- Modify: `cmd/server/main_test.go`
- Modify: `build.ps1:1-39`

- [ ] **Step 1: Write metadata policy tests**

Add `TestBuildInfoValidateReleaseRejectsDirty`, `TestBuildInfoRejectsVCSModified`, `TestBuildInfoWarnsOnInjectedVCSRevisionMismatch`, `TestBuildInfoValidateDevelopmentWarnsMissingMetadata`, and `TestServerStartupBuildLogFields`. Test pure validation functions without launching the server.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/buildinfo ./cmd/server -run 'TestBuildInfo|TestServerStartupBuildLogFields' -count=1 -v`

Expected: FAIL because package/variables do not exist.

- [ ] **Step 3: Implement injection and release guard**

Expose `Version`, `Commit`, `BuildTime`, and `Dirty` string variables plus parsed `Info`; read `vcs.revision`, `vcs.time`, and `vcs.modified` through `debug.ReadBuildInfo` and reject/warn on mismatch according to release/development mode. In `build.ps1`, read `git describe --tags --always`, `git rev-parse HEAD`, UTC time, and `git status --porcelain`; fail dirty release builds unless `-AllowDirty` is supplied, then pass four quoted `-X knox-media/internal/buildinfo...` values alongside `-s -w`. Startup logs version, commit, build time, dirty state, Go VCS settings, DB path, and revision.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/buildinfo ./cmd/server -run 'TestBuildInfo|TestServerStartupBuildLogFields' -count=1`

Expected: PASS.

### Task 15: Validate the complete publication path

**Files:**
- Modify: `cmd/server/scan_media_added_test.go`
- Modify: `internal/scanner/scanner_test.go`
- Create: `api/handler/ingest_publication_e2e_test.go`

- [ ] **Step 1: Write end-to-end tests first**

Add `TestNewVideoHiddenUntilRequiredIngestCompletes`, `TestNewVideoBecomesDegradedVisibleAfterExhaustion`, `TestRestartRecoversRunningGeneration`, and `TestScannerCallbackRegressionUsesCallbacksField`. Use real SQLite, scanner, planner, queue transitions, and Gin handlers; fake only external FFmpeg/provider execution at adapter boundaries.

- [ ] **Step 2: Run end-to-end tests and verify RED**

Run: `go test ./cmd/server ./api/handler -run 'Test(NewVideo|RestartRecovers|ScannerCallbackRegression)' -count=1 -v`

Expected: at least one assertion FAIL until all integration wiring is present; a compile-only failure must be fixed and rerun until the test fails on the intended visibility/state assertion.

- [ ] **Step 3: Add the minimum missing integration wiring**

Ensure `cmd/server/main.go` constructs one planner/aggregator, injects it into scanner, post-ingest, scrape, prepare, repair, and handlers, starts finalize/legacy recovery after schema initialization, and shuts loops down through `serverCtx`. Do not add alternate queue paths.

- [ ] **Step 4: Run focused end-to-end tests and verify GREEN**

Run: `go test ./cmd/server ./api/handler -run 'Test(NewVideo|RestartRecovers|ScannerCallbackRegression)' -count=1 -v`

Expected: PASS.

- [ ] **Step 5: Run all backend and frontend checks**

Run: `go test ./... -count=1`

Expected: PASS.

Run from `web`: `npm test -- --run`

Expected: PASS.

Run from `web`: `npm run build`

Expected: exit 0.

Run: `git diff --check`

Expected: no output and exit 0.

## Phase 10: Community synchronization preparation

### Task 16: Prepare a precise upstream sync record

**Files:**
- Create: `docs/superpowers/specs/2026-07-20-ingest-publication-community-sync.md`
- Verify: `internal/scanner/scanner.go:402`
- Verify: `internal/scanner/scanner_test.go`

- [ ] **Step 1: Record the scanner regression delta**

Document Knox baseline `087d9c6`, Vauldy observed revision `657a2dd`, the exact faulty condition (`s.OnMediaAdded`) and corrected condition (`callbacks.OnMediaAdded`), plus the focused regression command `go test ./internal/scanner -run TestScannerCallbackRegressionUsesCallbacksField -count=1 -v` and expected PASS.

- [ ] **Step 2: Record portable publication changes**

List community-safe files (schema, publication package, scanner callback/atomic planner, post-ingest links, API filters, frontend recovery, SQLite reliability, build metadata) separately from enterprise-only prepare registration. State that community plans omit `prepare` when capability registration is absent.

- [ ] **Step 3: Verify the sync record has concrete revisions and tests**

Run: `Select-String -Path docs/superpowers/specs/2026-07-20-ingest-publication-community-sync.md -Pattern '087d9c6','657a2dd','callbacks.OnMediaAdded','go test ./internal/scanner'`

Expected: four matches. This step prepares synchronization only; it does not modify the community repository or create a commit.

## Final acceptance checklist

- [ ] New videos and their complete immutable plans commit atomically.
- [ ] Required poster, scrape, configured preview/keyframe/subtitle/atrack/encrypt, and applicable prepare steps gate first visibility.
- [ ] Retry exhaustion yields degraded-visible and background retry remains possible.
- [ ] Legacy active media remain visible while idempotent repair generations run.
- [ ] Ordinary lists/details/counts/Home hide processing/failed/cancelled; admin endpoints expose them.
- [ ] Poster components recover after URL/generation changes and degraded state is visible.
- [ ] Busy retry budgets, heartbeat lease budgeting, typed missing-task diagnostics, submission coalescing, and durable finalize recovery are covered by tests.
- [ ] Build logs identify version/commit/time/dirty state and release packaging rejects dirty trees by default.
- [ ] Scanner callback regression is retained and community synchronization is documented.
- [ ] No PostgreSQL migration, full writer actor, production-code commit, or Git commit is included by this plan.
