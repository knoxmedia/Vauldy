# Poster Ingest Stall Design

**Date:** 2026-07-26  
**Status:** Approved  
**Scope:** Poster source fingerprinting, poster execution budgeting, ordinary media visibility, and production capability assembly

## Problem and evidence

The Knox commercial `c598e75` runtime has 57 videos in `processing`; 56 have a required poster task stranded in `running`. The blocking operation is not FFprobe or frame extraction. `publication.SourceFingerprint` hashes the entire 1–16 GiB source with `io.Copy`, while the dispatcher gives both `poster` and `poster_repair` a fixed two-minute deadline. `io.Copy` does not observe the task context, so expiration does not stop hashing. The dispatcher correctly retains the worker budget for an executor that remains alive, which makes the poster executor appear unresponsive. If the hash eventually returns, the task identity may already be stale.

Scrape is behaving correctly: its required step waits behind poster/encryption dependencies and `media_visible`. The deployed runtime database must not be edited. On restart, the existing `Queue.RecoverExpired` path recovers expired running tasks for normal retry.

## Goals

- Retain the exact full-file SHA-256 source identity and its existing canonical format.
- Compute the full source fingerprint once during one poster task execution and reuse it through staging and commit.
- Make source hashing stop promptly when its context is canceled or reaches its deadline.
- Give `poster` and `poster_repair` enough execution time for large files, derived from source size and capped by a finite maximum.
- Keep all ordinary `/api/v1/media` list and get behavior publication-safe even when the caller has admin credentials.
- Keep unpublished inspection available through the dedicated `/api/v1/admin/media` endpoints.
- Advertise `poster_repair` in the production capability registry used by the queue, planner, preflight, and admin overview.

## Non-goals

- No partial, sampled, metadata-only, or cached-across-tasks fingerprint.
- No publication schema or runtime database mutation.
- No change to FFprobe/frame extraction, scrape dependency ordering, lease recovery, retry counts, or publication aggregation.
- No new admin endpoint and no broad media API refactor.
- No change to hashing of generated poster artifacts.

## Architecture

### Context-aware full source fingerprint

Add `publication.SourceFingerprintContext(ctx context.Context, path string) (string, error)`. It preserves the current fingerprint value exactly:

`<clean-absolute-path>|<size>|<mtime-unix-nanos>|sha256:<full-file-sha256>`

The implementation opens and stats the same source and streams every byte through SHA-256, but the reader checks `ctx.Err()` between reads. Cancellation returns the context error, wrapped only with operation/path context where useful so `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` remain true. `SourceFingerprint(path)` remains as a compatibility wrapper using `context.Background()`; existing non-poster callers and persisted evidence retain byte-for-byte identity behavior.

### One fingerprint per poster task

`PosterAdapter.ExecuteWithResult` computes the fingerprint once, after `storage.PreferredFFmpegPath` selects the exact input, by calling the context-aware API. The resulting string is put in `publication.StageRequest.SourceFingerprint`.

`LocalPosterRunner.StagePoster` already carries that request into the ordinary or repair stage journal. `commitStagedPoster` must stop re-hashing the source. It receives the already verified task fingerprint through `staged.Stage.Request`, reloads the current selected source path, and checks path/stat identity against the pre-staging identity. Its SQLite fence still requires the journal's `source_fingerprint` to equal the request value. Generated poster artifact hash/size verification remains unchanged.

This removes the second full source read currently performed by `posterSourceFingerprint(sourcePath)` in `commitStagedPoster` without weakening queue, run, generation, lease-owner, attempt, journal, artifact, or metadata fences. A source path/stat change fails the commit as stale. The full hash establishes source byte identity once; the final stat check detects ordinary in-task replacement/change without another multi-gigabyte read.

### Dynamic bounded poster deadline

The dispatcher remains the owner of task deadlines. Add a source-size lookup to `Dispatcher` through its existing queue database and calculate a per-task timeout before `context.WithTimeout` in `launch`.

For `poster` and `poster_repair`, use:

- base budget: the configured/default poster timeout (two minutes),
- size allowance: one additional minute per started GiB of selected source size,
- maximum: 30 minutes,
- effective timeout: `min(30m, base + ceil(size/GiB)*1m)`.

A zero or unknown size uses the base timeout. The lookup reads `media.library_id` and `media.file_path`, resolves the same input with `storage.PreferredFFmpegPath`, and uses `os.Stat`; failure to resolve or stat does not fail the task or remove its deadline, because the adapter remains responsible for reporting an inaccessible source. Non-poster task timeout behavior is exactly unchanged. Both ordinary poster and `poster_repair` use the same calculation.

The bound is intentionally finite: it handles the observed 1–16 GiB range while preserving the dispatcher's unresponsive-executor isolation. Context-aware hashing should normally terminate at deadline, allowing the worker slot to be released instead of remaining handed off indefinitely.

## Data flow

1. The dispatcher claims a generation-fenced `poster` or `poster_repair` task.
2. It reads the source size and creates a bounded dynamic task context.
3. `PosterAdapter.ExecuteWithResult` validates the lease and media type, selects the FFmpeg input, and calls `publication.SourceFingerprintContext(taskCtx, input)` once.
4. The adapter passes that fingerprint in `StageRequest` to `LocalPosterRunner.StagePoster`.
5. FFprobe/FFmpeg creates the poster; the runner hashes only the generated artifact and writes a staged journal row containing the source fingerprint.
6. `commitStagedPoster` validates task/journal identity, confirms the selected source path and source stat have not changed, verifies the staged artifact, and atomically updates metadata, evidence/journal state, queue/step state, and publication aggregate.
7. Scrape becomes claimable only after its existing poster/encryption and `media_visible` dependencies are satisfied.

## Ordinary and admin media compatibility

`GET /api/v1/media` and `GET /api/v1/media/:id` are ordinary browse endpoints. They always apply `publication_state IN ('published','degraded')`, regardless of whether authentication identifies a viewer, API client, manager, or admin. An unpublished item therefore contributes no list item and ordinary get returns `404`, including for an admin credential. Ordinary responses do not gain unpublished inspection fields.

Dedicated admin inspection remains unchanged:

- `GET /api/v1/admin/media` may include `processing`, `failed`, and `cancelled`, may filter by `publication_state`, and retains cursor/lookahead and inspection fields.
- `GET /api/v1/admin/media/:id/ingest` continues to expose the current run and ordered steps for unpublished media.
- Admin retry endpoints retain their current semantics.

Implementation-wise, only `AdminListMedia` sets `IncludeUnpublished`; `ListMedia` never elevates based on `middleware.IsAdmin`. `GetMedia` passes `false` to `mediaPublicationVisibilityPredicate`. This is an intentional compatibility correction: admin credentials no longer broaden ordinary endpoint visibility, but dedicated admin endpoints retain full inspection.

## Production capability compatibility

The production `publicationSteps` registry in `cmd/server/main.go` adds the exact capability string `poster_repair`. The same `publicationCapabilities` instance continues to be supplied to `postingest.NewQueue`, both planner constructions, publication preflight/enterprise dependencies, handler dependencies, and admin overview. No second registry is introduced. Existing conditional `prepare` registration is unchanged.

## Error handling and recovery

- Hash cancellation returns a context-classifiable error. The dispatcher follows its existing timeout/shutdown classification and queue failure path.
- Source open/read/stat failures remain retryable unless existing poster classification makes them permanent.
- Dynamic timeout size lookup failure falls back to the base budget; it does not hide the adapter's authoritative source error.
- Source path or stat changes between fingerprint and commit fail the fenced commit as stale; no evidence is committed.
- Artifact verification, uncertain immediate-commit reconciliation, cleanup, and stage journal recovery remain unchanged.
- No deployed database is modified manually. Existing `RecoverExpired` executes before dispatcher claims and supplies restart recovery for stranded leases.

## Testing strategy

Strict TDD is required; each behavior starts with a focused failing test:

1. `internal/publication/repair_test.go`: cancellation interrupts full hashing and preserves `errors.Is` context identity; a compatibility assertion proves the context and wrapper APIs produce the same full SHA-256 fingerprint.
2. `internal/postingest/poster_quality_test.go`: the poster commit path does not invoke a second full source fingerprint and still rejects changed source stats/path.
3. `internal/postingest/dispatcher_test.go`: both poster task classes receive size-scaled deadlines, small/unknown sources retain the base, large sources cap at 30 minutes, and non-poster deadlines remain unchanged.
4. `api/handler/media_query_test.go`: an admin calling ordinary list cannot see processing/failed/cancelled media.
5. `api/handler/media_query_test.go`: an admin calling ordinary get receives `404` for unpublished media, while published/degraded remain visible.
6. Existing `api/handler/media_ingest_test.go` coverage confirms dedicated admin list/ingest inspection still sees unpublished records.
7. `cmd/server/main_test.go`: source-level assembly test requires `poster_repair` in `publicationSteps` and verifies the one shared capability matrix remains wired through production assembly.

Focused package tests run first, followed by `go test ./internal/publication ./internal/postingest ./api/handler ./cmd/server -count=1`, then `go test ./... -count=1`.

## Acceptance criteria

- Full source SHA-256 and persisted fingerprint format are unchanged.
- Poster hashing observes cancellation and no poster task hashes its full source more than once.
- Poster timeouts scale by source size and never exceed 30 minutes.
- Ordinary media list/get hide unpublished media for every credential class, including admin.
- Dedicated admin media inspection retains unpublished visibility.
- Production reports `poster_repair` from the shared capability registry.
- No runtime DB mutation, migration, manual task rewrite, or unrelated behavior change is introduced.

