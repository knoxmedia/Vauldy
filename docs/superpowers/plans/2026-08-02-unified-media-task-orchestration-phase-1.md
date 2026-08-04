# Unified Media Task Orchestration Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the immutable media plan with library-selected processing nodes and safe dependency semantics, then make plaintext deletion a separate durable, crash-recoverable operation that cannot run before the full frozen plan is terminal.

**Architecture:** Add pure processing-option and graph validation first, migrate the Publication V2 graph through its existing canonical rebuild path, and centralize lifecycle effects in transaction-local helpers. Preserve existing media executors behind adapter contracts. Encryption commits ciphertext selection and requests retirement; a new out-of-DAG retirement service alone quarantines and deletes authoritative sources.

**Tech Stack:** Go 1.22, `database/sql`, modernc SQLite, Gin, React 19, TypeScript 6, Ant Design, Vitest/Testing Library, PowerShell.

**Specification:** `docs/superpowers/specs/2026-08-02-unified-media-task-orchestration-design.md`

---

## File map

### Create

- `internal/libraryprocessing/options.go`: explicit/effective library choices, dependency closure, lock reasons, provenance.
- `internal/libraryprocessing/options_test.go`: exhaustive closure matrix and idempotence.
- `web/src/lib/libraryProcessingOptions.ts`: UI closure and lock-reason helpers.
- `web/src/lib/__tests__/libraryProcessingOptions.test.ts`: frontend helper tests.
- `web/src/pages/__tests__/Library.processingOptions.test.tsx`: form behavior and payload tests.
- `internal/publication/plan_graph.go`: immutable known-node/edge registry and DAG validation.
- `internal/publication/plan_graph_test.go`: cycles, unknown types, duplicate/cross-generation edges.
- `internal/publication/dependencies.go`: typed dependency satisfaction and transactional impossible-descendant skip propagation.
- `internal/publication/dependencies_test.go`: success/terminal semantics and recognition-to-AI propagation.
- `internal/publication/plan_completion.go`: all-plan-terminal projection updater/query.
- `internal/publication/plan_completion_test.go`: optional terminal matrix and reopen behavior.
- `internal/publication/source_strategy.go`: encrypted-source strategy registry and validation.
- `internal/publication/source_strategy_test.go`: coverage and frozen-snapshot contracts.
- `internal/retirement/types.go`: retirement states, blockers, identity, errors.
- `internal/retirement/barrier.go`: authoritative eligibility evaluation.
- `internal/retirement/barrier_test.go`: full barrier matrix.
- `internal/retirement/filesystem.go`: quarantine/move/fsync/delete/verify primitives.
- `internal/retirement/filesystem_test.go`: path/symlink/identity/crash-seam tests.
- `internal/retirement/worker.go`: claim, renew, execute, retry, and operator escalation.
- `internal/retirement/worker_test.go`: lease, retry, crash, and restart tests.
- `internal/retirement/recovery.go`: startup reconciliation.
- `internal/retirement/recovery_test.go`: interrupted-state matrix.
- `internal/storage/task_plaintext_temp.go`: protected lease/generation-bound temporary plaintext.
- `internal/storage/task_plaintext_temp_test.go`: bounds, cleanup, recovery, and escape tests.

### Modify

- `internal/store/db.go`: library columns and fresh-database schema registration.
- `internal/store/migrations_ingest_publication.go`: canonical plan/journal/retirement schema rebuild and validation.
- `internal/store/migrations_ingest_publication_test.go`: exact DDL/FK/index/rollback/idempotence tests.
- `api/handler/library.go`, `api/handler/library_test.go`: processing-option API and canonical closure.
- `web/src/api/client.ts`: library fields and new step/task unions.
- `web/src/pages/Library.tsx`: processing controls and dependency locks.
- `web/src/i18n/locales/{en,zh-CN,zh-TW,ja,ko}.json`: option labels and dependency reasons.
- `internal/publication/types.go`: policy version, node kinds, dependency kinds, snapshot fields.
- `internal/publication/planner.go`, `planner_test.go`: effective library policy and exact DAG persistence.
- `internal/publication/eligibility.go`, `eligibility_test.go`: `success`/`terminal` dependency claim semantics.
- `internal/publication/aggregate.go`, `aggregate_test.go`: publication remains separate from plan completion.
- `internal/publication/retry.go`, `retry_test.go`: monotonic node reopen and explicit AI reopen.
- `internal/publication/reconcile_startup.go`, tests: graph/queue/projection repair and validation.
- `internal/postingest/types.go`, `queue.go`, `queue_test.go`: new task/status identity and common lifecycle finalization.
- `internal/postingest/adapters.go`, tests: executor adapters, encryption/retirement handoff.
- `internal/postingest/encryption_state_machine.go`, tests: legacy stage recovery versus retirement ownership.
- `internal/postingest/encryption_recovery.go`, tests: no final plaintext deletion from encryption recovery.
- `internal/storage/plaintext_cleanup.go`, tests: compatibility-only during migration, then removal.
- `internal/transcode/package_worker.go`, package tests: no direct source deletion; retirement request.
- `api/handler/encrypt_task.go`, tests: monotonic reset and logical remove compatibility routes.
- `api/handler/task_batch.go`, tests: same encryption reset/remove semantics.
- `cmd/server/startup_recovery.go`, tests: retirement reconciliation ordering.
- `cmd/server/main.go`, tests: retirement worker wiring and old cleanup-loop removal.

## Explicit Phase 1 exclusions

- Durable upload/event `ingest_item` (Phase 2).
- Resource tokens, priority aging, fairness, and runtime concurrency overrides (Phase 3).
- Task Manager projection/tabs/actions migration (Phase 4).
- Full audio/image/document templates and person-scrape worker (Phase 5).
- General physical task purge/retention policy.
- Distributed scheduling.

---

## Task 1: Stop package workers deleting authoritative sources

**Files:**
- Modify: `internal/transcode/package_worker.go` (`PackageWorker.RunTask` cleanup branch)
- Modify: `internal/transcode/package_worker_test.go`
- Modify: `internal/transcode/package_worker_cleanup_test.go`

- [ ] **Step 1: Write the failing safety test**

Change/add tests so successful package output with `cleanup_local_source_after_package=1` must leave the source file present and persist `source_cleanup_status='pending'`. An ineligible path remains `skipped`. Assert package status/output/DRM evidence still commit.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```powershell
go test ./internal/transcode -run 'TestRunTaskUpdatesPackageStatusAndAsset|TestCleanupRunsOnlyForUploadLocalPath' -count=1 -v
```

Expected: FAIL because `PackageWorker.RunTask` calls `os.Remove` and records immediate `success`.

- [ ] **Step 3: Remove direct source deletion**

Replace the deletion branch with policy recording only:

```go
cleanupStatus := "skipped"
if cleanupFlag.Int64 == 1 && shouldCleanup(w.UploadDir, sourcePath.String) {
    cleanupStatus = "pending"
}
```

Do not enqueue retirement yet; Task 11 adds durable retirement intent. Package completion must stay independent of cleanup completion.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run the Step 2 command. Expected: PASS; source exists, package is done, cleanup is pending/skipped.

- [ ] **Step 5: Commit the safety stop**

```powershell
git add internal/transcode/package_worker.go internal/transcode/package_worker_test.go internal/transcode/package_worker_cleanup_test.go
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "fix(transcode): stop package worker deleting media source"
```

## Task 2: Define pure library processing-option closure

**Files:**
- Create: `internal/libraryprocessing/options.go`
- Create: `internal/libraryprocessing/options_test.go`

- [ ] **Step 1: Write the failing closure matrix**

Define tests for independent preview/keyframe choices, recognition adding subtitle/audio extraction, AI adding recognition and both prerequisites, idempotence, direct lock reasons, and provenance.

Use this contract:

```go
type Options struct {
    Preview, SubtitleExtract, ATrackExtract bool
    SubtitleRecognize, KeyframeExtract, AIAnalysis bool
}

type Provenance struct {
    Explicit        []string `json:"explicit"`
    DependencyAdded []string `json:"dependency_added"`
}

func Close(explicit Options) (effective Options, provenance Provenance)
func RequiredBy(effective Options, option string) []string
```

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/libraryprocessing -count=1 -v
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement minimal deterministic closure**

Apply exactly:

```go
if effective.AIAnalysis { effective.SubtitleRecognize = true }
if effective.SubtitleRecognize {
    effective.SubtitleExtract = true
    effective.ATrackExtract = true
}
```

Sort provenance/lock-reason outputs to keep snapshots and tests deterministic.

- [ ] **Step 4: Verify GREEN**

Run Step 2. Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/libraryprocessing
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(library): define processing option dependency closure"
```

## Task 3: Persist library choices and expose canonical API

**Files:**
- Modify: `internal/store/db.go`
- Modify: `api/handler/library.go`
- Modify: `api/handler/library_test.go`

- [ ] **Step 1: Write failing schema/API tests**

Add tests for these explicit columns with `NOT NULL DEFAULT 0`: `subtitle_extract`, `atrack_extract`, `subtitle_recognize`, `keyframe_extract`, `ai_analysis`; keep existing `preview_extract`. Create/update with recognition or AI, list the canonical explicit/effective choices, and cover non-video handling and omitted update fields.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/store ./api/handler -run 'LibraryProcessing|CreateLibrary|UpdateLibrary|ListLibraries' -count=1 -v
```

Expected: FAIL because columns and API fields do not exist.

- [ ] **Step 3: Add columns and API fields**

Use explicit library columns as user intent. Compute effective closure on read/planning; do not overwrite explicit prerequisite provenance. Update `libraryBody`, list query/scans, create insert, and update statement. Reject non-binary values; for non-video libraries persist options but return `effective=false` and do not plan them.

- [ ] **Step 4: Verify GREEN and compatibility**

Run Step 2, then:

```powershell
go test ./api/handler -run 'Library|ScanLibrary' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/store/db.go api/handler/library.go api/handler/library_test.go
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(library): persist media processing selections"
```

## Task 4: Add Library UI dependency behavior

**Files:**
- Create: `web/src/lib/libraryProcessingOptions.ts`
- Create: `web/src/lib/__tests__/libraryProcessingOptions.test.ts`
- Create: `web/src/pages/__tests__/Library.processingOptions.test.tsx`
- Modify: `web/src/pages/Library.tsx`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/i18n/locales/en.json`
- Modify: `web/src/i18n/locales/zh-CN.json`
- Modify: `web/src/i18n/locales/zh-TW.json`
- Modify: `web/src/i18n/locales/ja.json`
- Modify: `web/src/i18n/locales/ko.json`

- [ ] **Step 1: Write failing helper/form tests**

Test recognition auto-selects and locks extraction/atrack; AI auto-selects and locks recognition transitively; disabling AI unlocks but does not clear recognition; disabling recognition unlocks but does not clear extraction; edit hydration and submit payload preserve explicit choices; controls only show for video-capable types.

- [ ] **Step 2: Verify RED**

```powershell
npm --prefix web test -- --run src/lib/__tests__/libraryProcessingOptions.test.ts src/pages/__tests__/Library.processingOptions.test.tsx
```

Expected: FAIL because helpers and fields do not exist.

- [ ] **Step 3: Implement event-driven closure UX**

Add typed helpers and switches. On enabling dependents, set prerequisites. Use derived `disabled` and tooltip reasons; avoid an unconditional watched-value `useEffect` loop. Send explicit values expected by the backend.

- [ ] **Step 4: Verify GREEN and build**

```powershell
npm --prefix web test -- --run src/lib/__tests__/libraryProcessingOptions.test.ts src/pages/__tests__/Library.processingOptions.test.tsx src/pages/__tests__/Library.polling.test.tsx
npm --prefix web run build
```

Expected: tests and TypeScript/Vite build PASS.

- [ ] **Step 5: Commit**

```powershell
git add web/src/api/client.ts web/src/pages/Library.tsx web/src/lib web/src/pages/__tests__/Library.processingOptions.test.tsx web/src/i18n/locales
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(web): configure library processing dependencies"
```

## Task 5: Migrate expanded immutable graph and safe execution identity

**Files:**
- Modify: `internal/store/migrations_ingest_publication.go`
- Modify: `internal/store/migrations_ingest_publication_test.go`
- Modify: `internal/store/db.go`

- [ ] **Step 1: Write failing canonical migration tests**

Require a new policy/schema version; rebuild canonical `media_ingest_step`, `post_ingest_task`, and `media_ingest_step_dependency` to accept the selected new logical nodes, `skipped`, and typed dependency kinds. Add `retry_round` to the encryption journal, uniqueness `(task_id,retry_round,attempt)`, and change task FK deletion from cascade to restrict. Add `media_plan_completion`, `media_plaintext_retirement`, and `media_plaintext_retirement_attempt` with exact checks/indexes/FKs. Assert old rows/journals survive, custom indexes follow existing policy, fault injection rolls back, repeat/reopen is a no-op, and unknown types fail closed.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/store -run 'Test.*Migration|Test.*Schema|TestMigrationCreatesDedicatedEncryptionStageJournal' -count=1 -v
```

Expected: FAIL on missing tables/columns/types/FK behavior.

- [ ] **Step 3: Extend the existing publication-graph rebuild**

Do not add ad-hoc drop/rename SQL in `OpenSQLite`. Use canonical graph migration ordering and exact-schema validation already in `migrations_ingest_publication.go`. Retirement remains outside `media_ingest_step` and cannot be included in all-plan counts.

- [ ] **Step 4: Verify GREEN and full store package**

```powershell
go test ./internal/store -run 'Test.*Migration|Test.*Schema' -count=1
go test ./internal/store -count=1
```

Expected: PASS, including legacy journal preservation and idempotence.

- [ ] **Step 5: Commit**

```powershell
git add internal/store/db.go internal/store/migrations_ingest_publication.go internal/store/migrations_ingest_publication_test.go
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(store): migrate expanded plan and retirement schema"
```

## Task 6: Validate immutable DAG and build effective plans

**Files:**
- Create: `internal/publication/plan_graph.go`
- Create: `internal/publication/plan_graph_test.go`
- Modify: `internal/publication/types.go`
- Modify: `internal/publication/planner.go`
- Modify: `internal/publication/planner_test.go`
- Modify: `internal/publication/retry_test.go`

- [ ] **Step 1: Write failing graph/planner tests**

Replace tests that require keyframe/atrack omission. Test exact explicit/effective/provenance snapshots, acyclicity, known nodes/edges, no duplicate logical node, cross-generation rejection, unknown type rejection, rollback, and these edges:

```text
subtitle_extract --success--> subtitle_recognize
atrack_extract   --success--> subtitle_recognize
subtitle_recognize --success--> ai_analysis
```

Every selected asynchronous processing node in Phase 1 has a `media_visible` dependency. Success edges add stricter prerequisites for recognition and AI. A new generation uses current library choices; old topology remains unchanged.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/publication -run 'TestPlanner|TestPlanGraph|TestRetryIngest' -count=1 -v
```

Expected: FAIL because policy/types/snapshot/planner omit nodes and dependency semantics.

- [ ] **Step 3: Implement policy version and graph validation**

Extend `ConfigSnapshot` with explicit/effective/provenance and encrypted-source strategy metadata. Validate before persistence and during startup. Keep unsupported AI execution disabled until Task 9 registers its minimal adapter; do not write an unclaimable queue row without an explicit unavailable-capability state.

- [ ] **Step 4: Verify GREEN**

Run Step 2. Expected: PASS with exact deterministic topology.

- [ ] **Step 5: Commit**

```powershell
git add internal/publication/types.go internal/publication/planner.go internal/publication/planner_test.go internal/publication/retry_test.go internal/publication/plan_graph.go internal/publication/plan_graph_test.go
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(publication): freeze library-selected processing DAG"
```

## Task 7: Add typed dependency propagation and plan completion

**Files:**
- Create: `internal/publication/dependencies.go`
- Create: `internal/publication/dependencies_test.go`
- Create: `internal/publication/plan_completion.go`
- Create: `internal/publication/plan_completion_test.go`
- Modify: `internal/publication/eligibility.go`
- Modify: `internal/publication/eligibility_test.go`
- Modify: `internal/publication/aggregate.go`
- Modify: `internal/publication/aggregate_test.go`
- Modify: `internal/publication/retry.go`
- Modify: `internal/publication/retry_test.go`

- [ ] **Step 1: Write failing lifecycle matrix tests**

Test `success` accepts only `done`; `terminal` accepts done/skipped/failed/cancelled; recognition permanent failure/cancellation atomically skips AI; retryable recognition waiting does not skip AI; worker absence does not skip; publication may finish while plan completion is processing; reopening a node flips all-terminal false; explicit AI reopen alone creates its retry round/audit and does not change topology.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/publication -run 'Dependency|PlanCompletion|Aggregate|RetryOptional' -count=1 -v
```

Expected: FAIL on missing kinds/propagation/projection.

- [ ] **Step 3: Implement transaction-local helpers**

Implement:

```go
func PropagateImpossibleDependenciesTx(ctx context.Context, tx store.SQLExecutor, runID int64) error
func RecomputePlanCompletionTx(ctx context.Context, tx store.SQLExecutor, runID int64) error
func ReopenNodeTx(ctx context.Context, tx store.SQLExecutor, req ReopenRequest) error
```

Use structured skip reasons. Never derive plan completion from run publication status.

- [ ] **Step 4: Verify GREEN**

Run Step 2. Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/publication
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(publication): propagate dependencies and track plan completion"
```

## Task 8: Route every queue transition through one lifecycle finalizer

**Files:**
- Modify: `internal/postingest/queue.go`
- Modify: `internal/postingest/queue_test.go`
- Modify: `internal/publication/cancel.go`
- Modify: `internal/publication/reconcile_startup.go`
- Modify: `internal/publication/reconcile_startup_test.go`

- [ ] **Step 1: Write failing atomic-transition tests**

For complete, exhausted/permanent fail, cancel, skip propagation, optional reopen, expired recovery, and startup repair, assert queue row, step, dependent AI, plan projection, and publication aggregate commit or roll back together.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/postingest ./internal/publication -run 'LinkedLifecycle|Dependency|PlanCompletion|Reconcile|Cancel|Recover' -count=1 -v
```

Expected: FAIL because lifecycle effects are spread across paths.

- [ ] **Step 3: Centralize finalization**

Refactor `syncLinkedStepTx` or add `FinalizeNodeTransitionTx` to call step sync, impossible-dependency propagation, plan-completion recompute, retirement barrier recompute hook, and publication aggregate in one caller-owned transaction. Keep startup repair idempotent.

- [ ] **Step 4: Verify GREEN**

Run Step 2, then full packages:

```powershell
go test ./internal/publication ./internal/postingest -count=1
```

- [ ] **Step 5: Commit**

```powershell
git add internal/postingest/queue.go internal/postingest/queue_test.go internal/publication
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "refactor(tasks): centralize plan lifecycle transitions"
```

## Task 9: Split subtitle extraction/recognition and register source strategies

**Files:**
- Create: `internal/publication/source_strategy.go`
- Create: `internal/publication/source_strategy_test.go`
- Create: `internal/storage/task_plaintext_temp.go`
- Create: `internal/storage/task_plaintext_temp_test.go`
- Modify: `internal/subtitle/service.go`
- Modify: `internal/subtitle/service_test.go`
- Modify: `internal/subtitle/video_io.go`
- Modify: `internal/subtitle/video_io_test.go`
- Modify: `internal/postingest/types.go`
- Modify: `internal/postingest/adapters.go`
- Modify: `internal/postingest/adapters_test.go`
- Modify: preview/atrack/keyframe worker tests as listed in the file map.

- [ ] **Step 1: Write failing executor/strategy tests**

Assert extraction discovers sidecars and extracts embedded/OCR-capable artifacts without invoking ASR; recognition consumes prerequisite artifacts/audio and performs OCR/ASR without repeating extraction; each cleanup-compatible task has exactly one strategy (`stream_decrypt`, `materialize_temp`, `encrypted_derivative`); encrypted input works after plaintext removal; protected temp is generation/lease-bound and cleaned/recovered on every terminal path.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/subtitle ./internal/storage ./internal/postingest ./internal/preview ./internal/atrack ./internal/keyframe -run 'Extract|Recogn|SourceStrategy|Encrypted|PlaintextTemp|Adapter' -count=1 -v
```

Expected: FAIL because subtitle processing is fused and strategy registry/temp service do not exist.

- [ ] **Step 3: Implement focused executor split and registry**

Expose separate extraction and recognition adapter entry points while reusing existing service internals. Move external CLI plaintext materialization to protected task temp. Register preview, extraction, atrack, recognition, keyframe, and other verified strategies. Runtime worker absence remains an admission blocker, never a skip.

For Phase 1 AI, define the minimal text/subtitle-result analysis adapter contract and register it before enabling AI plan rows. Do not attempt full media-specific AI decomposition here.

- [ ] **Step 4: Verify GREEN and encrypted-source contracts**

Run Step 2, then:

```powershell
go test ./internal/storage ./internal/subtitle ./internal/preview ./internal/atrack ./internal/keyframe ./internal/postingest -count=1
```

- [ ] **Step 5: Commit**

```powershell
git add internal/publication/source_strategy* internal/storage/task_plaintext_temp* internal/subtitle internal/postingest internal/preview internal/atrack internal/keyframe
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(tasks): split subtitle stages and declare encrypted sources"
```

## Task 10: Make encryption retry identity monotonic and removal journal-safe

**Files:**
- Modify: `internal/postingest/queue.go`
- Modify: `internal/postingest/queue_test.go`
- Modify: `internal/postingest/adapters.go`
- Modify: `internal/postingest/encryption_state_machine.go`
- Modify: `internal/postingest/encryption_commit_identity_test.go`
- Modify: `internal/postingest/encryption_recovery_terminal_test.go`
- Modify: `api/handler/encrypt_task.go`
- Modify: `api/handler/encrypt_task_test.go`
- Modify: `api/handler/task_batch.go`

- [ ] **Step 1: Write failing reset/tombstone identity tests**

Assert reset increments retry round 0→1→2; `(task_id,retry_round,attempt)` never repeats; stale rounds cannot commit; old journals remain; remove sets tombstone fields and default list hides it; include-removed shows it; recovery continues through tombstone; physical purge is rejected while journal/retirement/dependency/audit references remain.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/postingest ./api/handler -run 'EncryptAdmin|RetryRound|Tombstone|Remove|Encryption.*Identity|BatchEncrypt' -count=1 -v
```

Expected: FAIL because reset reuses attempts and remove physically deletes.

- [ ] **Step 3: Implement fenced admin mutations**

Use `BEGIN IMMEDIATE`, current-generation validation, inactive/expired owner fencing, monotonic retry round, append-only audit, and logical tombstone columns. Keep DELETE route as a compatibility logical-remove endpoint.

- [ ] **Step 4: Verify GREEN**

Run Step 2 and migration journal tests.

- [ ] **Step 5: Commit**

```powershell
git add internal/postingest api/handler/encrypt_task.go api/handler/encrypt_task_test.go api/handler/task_batch.go
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "fix(encrypt): preserve retry and recovery identity"
```

## Task 11: Decouple encryption commit and create retirement intents

**Files:**
- Modify: `internal/postingest/adapters.go`
- Modify: `internal/postingest/encryption_state_machine.go`
- Modify: `internal/postingest/encryption_recovery.go`
- Modify: encryption commit/recovery tests.

- [ ] **Step 1: Write failing handoff tests**

Assert encryption completion commits ciphertext/evidence and remains `done`; cleanup requested creates/upserts an exact generation/source-fingerprint retirement row; source remains present or durably quarantined for legacy in-flight states; blocked/retrying cleanup never fails encryption; committed encryption recovery hands off instead of deleting.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/postingest -run 'Encryption.*Commit|Encryption.*Recovery|EncryptedPhoto' -count=1 -v
```

Expected: FAIL because encryption currently owns final plaintext deletion.

- [ ] **Step 3: Implement the handoff**

Preserve legacy staged/quarantined recovery. For new policy generations, finish encryption selection and upsert retirement intent in the same fenced transaction, then return atomic success. Remove new committed-cleanup execution from encryption reconciler.

- [ ] **Step 4: Verify GREEN**

```powershell
go test ./internal/postingest ./internal/storage -run 'Encryption|StagedEncrypt|EncryptResume' -count=1
```

- [ ] **Step 5: Commit**

```powershell
git add internal/postingest
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(encrypt): hand plaintext cleanup to retirement"
```

## Task 12: Implement plaintext-retirement barrier and filesystem state machine

**Files:**
- Create: `internal/retirement/types.go`
- Create: `internal/retirement/barrier.go`
- Create: `internal/retirement/barrier_test.go`
- Create: `internal/retirement/filesystem.go`
- Create: `internal/retirement/filesystem_test.go`
- Create: `internal/retirement/worker.go`
- Create: `internal/retirement/worker_test.go`
- Create: `internal/retirement/recovery.go`
- Create: `internal/retirement/recovery_test.go`

- [ ] **Step 1: Write failing barrier and crash matrix**

Cover all approved preconditions and crash points: optional waiting/running blocks; permanent failure/cancel/skip advances; retryable waiting blocks; current generation and fingerprint fence; ciphertext/key/evidence readable; all retryable task strategies covered; policy enabled; no active consumer; crash before/after move/state commit/delete/verification; exhausted retry becomes operator-required; encryption stays done.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/retirement -count=1 -v
```

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement durable states and leases**

Implement `blocked`, `ready`, `quarantining`, `quarantined`, `deleting`, `verified`, `retryable_failed`, `operator_required`. Reserve quarantine identity with media/generation/retirement/retry/attempt; validate path layout and source fingerprint; fsync file/parents; make recovery idempotent. Retirement is outside the frozen DAG.

- [ ] **Step 4: Verify GREEN and stress transitions**

```powershell
go test ./internal/retirement -count=1
go test ./internal/retirement -run 'Crash|Generation|Fingerprint|Barrier|Recovery' -count=25
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/retirement
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(storage): add durable plaintext retirement"
```

## Task 13: Route package cleanup into retirement

**Files:**
- Modify: `internal/transcode/package_worker.go`
- Modify: `internal/transcode/package_worker_test.go`
- Modify: `internal/transcode/package_worker_cleanup_test.go`
- Modify: retirement barrier/tests for package basis.

- [ ] **Step 1: Write failing integration tests**

Package success creates/upserts one retirement intent and keeps package `done` with cleanup pending. Failed package creates none. Retry does not duplicate. Retirement removes source only after output/key/source/generation and all-plan barriers; package work outside an authoritative frozen generation stays blocked.

- [ ] **Step 2: Verify RED**

```powershell
go test ./internal/transcode ./internal/retirement -run 'Package|Cleanup|Retirement' -count=1 -v
```

Expected: FAIL because Task 1 only records pending.

- [ ] **Step 3: Upsert package-basis retirement**

Use `basis_kind='package'`, `basis_id=package_task.id`. Package worker never records cleanup success; retirement projection does.

- [ ] **Step 4: Verify GREEN**

Run Step 2, then full packages.

- [ ] **Step 5: Commit**

```powershell
git add internal/transcode internal/retirement
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(package): request safe source retirement"
```

## Task 14: Wire startup recovery and remove legacy cleanup goroutines

**Files:**
- Modify: `cmd/server/startup_recovery.go`
- Modify: `cmd/server/startup_recovery_test.go`
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/main_test.go`
- Modify/delete: `internal/storage/plaintext_cleanup.go`
- Modify: `internal/storage/plaintext_cleanup_test.go`

- [ ] **Step 1: Write failing startup-order tests**

Require retirement filesystem reconciliation before task claims; legacy encryption quarantine stabilizes before retirement claims; startup failure blocks claims; operator-required rows do not starve due retryable rows; no `KickPendingPlaintextCleanups` goroutine starts.

- [ ] **Step 2: Verify RED**

```powershell
go test ./cmd/server ./internal/storage ./internal/retirement -run 'StartupRecovery|Retirement|PlaintextCleanup' -count=1 -v
```

Expected: FAIL because retirement is not wired and old cleanup starts.

- [ ] **Step 3: Wire retirement and retire compatibility code**

Start reconciler/worker through the existing background group after DB migration and artifact recovery, before normal claimers. Remove scheduling/deletion functions from `plaintext_cleanup.go`; retain one read-only active-source-consumer callback because the retirement barrier requires that predicate.

- [ ] **Step 4: Verify GREEN**

Run Step 2, then:

```powershell
go test ./cmd/server ./internal/storage ./internal/retirement ./internal/postingest -count=1
```

- [ ] **Step 5: Commit**

```powershell
git add cmd/server internal/storage internal/retirement
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "feat(server): recover and run plaintext retirement"
```

## Task 15: End-to-end Phase 1 verification and source-deletion audit

**Files:**
- Modify/add: `api/handler/ingest_publication_e2e_test.go`
- Modify/add: `api/handler/media_ingest_test.go`
- Modify/add: `cmd/server/scan_media_added_test.go`
- Modify: `docs/superpowers/specs/2026-08-02-unified-media-task-orchestration-design.md` only if implementation reveals an approved clarification.

- [ ] **Step 1: Add end-to-end acceptance cases**

Cover library closure-driven plan, recognition failure → AI skipped, publication visible while optional work remains, all-terminal retirement release, encrypted-source retry after source removal, generation replacement during retirement, tombstoned journal recovery, and package cleanup delegation.

- [ ] **Step 2: Run focused Phase 1 packages**

```powershell
go test ./internal/store ./internal/libraryprocessing ./internal/publication ./internal/storage ./internal/postingest ./internal/retirement ./internal/transcode ./internal/subtitle ./internal/preview ./internal/atrack ./internal/keyframe ./api/handler ./cmd/server -count=1
```

Expected: PASS.

- [ ] **Step 3: Run frontend verification**

```powershell
npm --prefix web test -- --run
npm --prefix web run build
```

Expected: PASS.

- [ ] **Step 4: Run repository and stress verification**

```powershell
go test ./... -count=1
go test ./internal/postingest ./internal/retirement -run 'RetryRound|Tombstone|Crash|Recovery|Generation|Fingerprint' -count=25
```

Where supported:

```powershell
go test -race ./internal/publication ./internal/postingest ./internal/retirement ./internal/transcode
```

Expected: PASS. On Windows without race support, require the race command in CI/Linux.

- [ ] **Step 5: Audit source deletion calls**

```powershell
rg -n 'os\.Remove|os\.RemoveAll|removePlaintextFile|schedulePlaintextCleanup|KickPendingPlaintextCleanups' internal api cmd
```

Classify every match. Acceptance: no call outside `internal/retirement` deletes an authoritative media source. Temp, staging, derived-artifact, key, and partial-output cleanup remains executor-owned.

- [ ] **Step 6: Commit acceptance coverage**

```powershell
git add api/handler cmd/server docs/superpowers/specs/2026-08-02-unified-media-task-orchestration-design.md
git commit --trailer "Co-authored-by: Cursor <cursoragent@cursor.com>" -m "test(orchestration): verify Phase 1 safety boundaries"
```

---

## Phase 1 completion gate

Do not start Phase 2 until all are true:

- Library options and backend closure produce deterministic frozen DAGs.
- Recognition permanent failure/cancellation transactionally skips AI; retryable work does not.
- Publication state and all-plan completion remain separate.
- Encryption journal identity includes monotonic retry rounds and survives logical removal.
- Encryption/package completion never directly deletes authoritative source media.
- Retirement alone owns authoritative source quarantine/delete/verify.
- Retirement cannot run until every frozen node is terminal and every retryable type has a tested encrypted-source strategy.
- Startup recovers every interrupted encryption/retirement state before claims.
- Focused, full, stress, frontend, and source-deletion audit checks pass.

