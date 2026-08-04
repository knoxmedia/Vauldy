# Poster Ingest Stall Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent large-video poster tasks from outliving a fixed deadline while preserving full SHA-256 identity, publication fencing, ordinary API visibility, and production repair capability.

**Architecture:** Make publication source hashing context-aware and reuse the one task fingerprint through poster staging and commit. Compute a bounded size-based poster deadline in the dispatcher, keep ordinary media endpoints publication-filtered independent of role, and register `poster_repair` in the one production capability matrix.

**Tech Stack:** Go 1.22, `context`, `crypto/sha256`, `database/sql`, modernc SQLite, Gin, Go `testing`.

---

## File map

- Modify `internal/publication/repair.go`: add context-aware full fingerprinting while preserving the existing wrapper and exact fingerprint format.
- Modify `internal/publication/repair_test.go`: prove cancellation and wrapper compatibility.
- Modify `internal/postingest/poster_atomic.go`: call context-aware fingerprinting once in execution and remove the duplicate full source hash from commit.
- Modify `internal/postingest/poster_quality_test.go`: prove commit reuses the staged fingerprint and retains source-change fencing.
- Modify `internal/postingest/dispatcher.go`: derive bounded poster/poster-repair task deadlines from source file size.
- Modify `internal/postingest/dispatcher_test.go`: test base, scaled, capped, repair, unknown-size, and non-poster deadlines.
- Modify `api/handler/media.go`: stop admin role from elevating ordinary list/get visibility.
- Modify `api/handler/media_query_test.go`: test ordinary list/get with admin credentials and dedicated admin compatibility.
- Modify `cmd/server/main.go`: add `poster_repair` to the production capability list.
- Modify `cmd/server/main_test.go`: assert production capability registration and shared assembly wiring.

## Task 1: Make full source fingerprinting cancellable

**Files:**
- Modify: `internal/publication/repair_test.go`
- Modify: `internal/publication/repair.go:445-465`

- [ ] **Step 1: Write failing context-cancellation and compatibility tests**

Add these tests beside the existing repair fingerprint fixtures. Use an internal read hook so the cancellation test is deterministic and does not create a multi-gigabyte file:

```go
func TestSourceFingerprintContextStopsOnCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 256*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	old := sourceFingerprintRead
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	sourceFingerprintRead = func(r io.Reader, p []byte) (int, error) {
		reads++
		if reads == 2 {
			cancel()
		}
		return r.Read(p)
	}
	t.Cleanup(func() { sourceFingerprintRead = old })

	_, err := SourceFingerprintContext(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context.Canceled", err)
	}
}

func TestSourceFingerprintContextMatchesCompatibilityWrapper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(path, []byte("full source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SourceFingerprintContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := SourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !strings.Contains(got, "|sha256:") {
		t.Fatalf("context=%q wrapper=%q", got, want)
	}
}
```

Add `bytes` and `errors` imports. The package-private hook has the exact signature `var sourceFingerprintRead = func(r io.Reader, p []byte) (int, error) { return r.Read(p) }` and exists only to make cancellation timing deterministic.

- [ ] **Step 2: Run the new tests and verify RED**

Run: `go test ./internal/publication -run 'TestSourceFingerprintContext' -count=1 -v`

Expected: FAIL to compile because `SourceFingerprintContext` and `sourceFingerprintRead` do not exist.

- [ ] **Step 3: Implement the minimal cancellable full hash**

In `internal/publication/repair.go`, retain `SourceFingerprint` and add:

```go
var sourceFingerprintRead = func(r io.Reader, p []byte) (int, error) { return r.Read(p) }

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := sourceFingerprintRead(r.r, p)
	if err == nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}

func SourceFingerprint(path string) (string, error) {
	return SourceFingerprintContext(context.Background(), path)
}

func SourceFingerprintContext(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, contextReader{ctx: ctx, r: f}); err != nil {
		return "", err
	}
	canonical, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%d|%d|sha256:%s", filepath.Clean(canonical), info.Size(), info.ModTime().UnixNano(), hex.EncodeToString(h.Sum(nil))), nil
}
```

Do not change separators, absolute-path normalization, size, nanosecond mtime, SHA-256 algorithm, or hex encoding.

- [ ] **Step 4: Run publication fingerprint tests and verify GREEN**

Run: `go test ./internal/publication -run 'TestSourceFingerprint|TestRepairLegacy' -count=1 -v`

Expected: PASS; cancellation remains detectable through `errors.Is`, and all existing evidence fixtures retain the same fingerprint values.

## Task 2: Reuse one source fingerprint through poster commit

**Files:**
- Modify: `internal/postingest/poster_quality_test.go`
- Modify: `internal/postingest/poster_atomic.go:28-35,60-132,169-204,272-329`

- [ ] **Step 1: Write a failing no-duplicate-fingerprint commit test**

Extend the existing commit fixture in `poster_quality_test.go`. After the adapter/stage fingerprint is present in `staged.Stage.Request.SourceFingerprint`, install a `posterSourceFingerprint` function that fails if called, then commit:

```go
func TestCommitStagedPosterDoesNotRehashFullSource(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	old := posterSourceFingerprint
	calls := 0
	posterSourceFingerprint = func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("duplicate full source fingerprint")
	}
	t.Cleanup(func() { posterSourceFingerprint = old })

	if err := commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("source fingerprint calls during commit=%d want 0", calls)
	}
}
```

Build the fixture exactly with `seedCurrentLinkedPosterTask(t)`, `realPosterStageRunner(t, db, upload)`, and `runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())`, matching `TestCommitHashesBeforeImmediateTransaction`. Keep `TestPosterCommitRejectsSourceStatMutation` and assert `poster commit: source stat changed` or `poster commit: source path changed` without relying on a second hash.

- [ ] **Step 2: Run the commit tests and verify RED**

Run: `go test ./internal/postingest -run 'TestCommitStagedPosterDoesNotRehashFullSource|TestPosterCommit.*Source' -count=1 -v`

Expected: FAIL because `commitStagedPoster` invokes `posterSourceFingerprint(sourcePath)`.

- [ ] **Step 3: Use the context-aware fingerprint once in execution**

Change the production hook to accept context:

```go
var posterSourceFingerprint = publication.SourceFingerprintContext
```

In `PosterAdapter.ExecuteWithResult`, replace the fingerprint call with:

```go
fp, err := posterSourceFingerprint(ctx, input)
if err != nil {
	return ordinary, err
}
```

Update test overrides to `func(context.Context, string) (string, error)`.

- [ ] **Step 4: Remove the second full source hash from commit**

In `commitStagedPoster`, load the selected source path as today, set:

```go
sourceFP := req.SourceFingerprint
if strings.TrimSpace(sourceFP) == "" {
	return fmt.Errorf("poster commit: missing source fingerprint")
}
```

Delete the call that recalculates `posterSourceFingerprint(sourcePath)` and the comparison against `req.SourceFingerprint`. Before opening the final immediate transaction, stat `sourcePath` and record path, size, and mtime in `preverifiedPosterIdentity`. Inside the transaction retain the exact selected-path comparison and `verified.verifyStats()`. Keep journal queries bound to `sourceFP`, artifact hash verification, queue/step fences, aggregation, reconciliation, and cleanup unchanged.

- [ ] **Step 5: Run focused poster tests and verify GREEN**

Run: `go test ./internal/postingest -run 'TestCommitStagedPoster|TestPosterCommit|TestPoster.*Fingerprint' -count=1 -v`

Expected: PASS; commit performs zero full source fingerprint calls while stale path/stat and artifact mutations remain rejected.

- [ ] **Step 6: Run the full postingest suite**

Run: `go test ./internal/postingest -count=1`

Expected: PASS.

## Task 3: Budget poster deadlines from source size

**Files:**
- Modify: `internal/postingest/dispatcher_test.go`
- Modify: `internal/postingest/dispatcher.go:50-69,94-107,280-299`

- [ ] **Step 1: Write failing timeout-calculation unit tests**

Add table tests for a package-private pure helper:

```go
func TestPosterTaskTimeoutScalesAndCapsBySourceSize(t *testing.T) {
	base := 2 * time.Minute
	cases := []struct {
		name string
		typ TaskType
		size int64
		want time.Duration
	}{
		{"ordinary zero", TaskPoster, 0, 2 * time.Minute},
		{"ordinary one byte", TaskPoster, 1, 3 * time.Minute},
		{"ordinary sixteen gib", TaskPoster, 16 << 30, 18 * time.Minute},
		{"repair two gib plus one", TaskPosterRepair, (2 << 30) + 1, 5 * time.Minute},
		{"ordinary capped", TaskPoster, 64 << 30, 30 * time.Minute},
		{"preview unchanged", TaskPreview, 64 << 30, base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskTimeoutForSource(tc.typ, base, tc.size); got != tc.want {
				t.Fatalf("timeout=%v want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Write a failing dispatcher integration test for both poster classes**

Seed real source files with controlled sparse sizes, enqueue one `TaskPoster` and one `TaskPosterRepair`, and capture executor deadlines. Configure a small test base timeout and assert the observed remaining duration matches `taskTimeoutForSource` within 100 ms. Add a row whose source path is missing and assert it receives the base timeout rather than failing before executor invocation. Do not sleep until the production-scale deadline; cancel each executor after recording its deadline.

- [ ] **Step 3: Run dynamic timeout tests and verify RED**

Run: `go test ./internal/postingest -run 'TestPosterTaskTimeout|TestDispatcher_Poster.*Deadline' -count=1 -v`

Expected: FAIL because `taskTimeoutForSource` is undefined and `launch` still uses the fixed map timeout.

- [ ] **Step 4: Implement the bounded timeout helper**

In `dispatcher.go`, add:

```go
const (
	posterTimeoutPerGiB = time.Minute
	posterTimeoutMax    = 30 * time.Minute
	gib                 = int64(1 << 30)
)

func taskTimeoutForSource(typ TaskType, base time.Duration, size int64) time.Duration {
	if (typ != TaskPoster && typ != TaskPosterRepair) || size <= 0 {
		return base
	}
	units := (size + gib - 1) / gib
	budget := base + time.Duration(units)*posterTimeoutPerGiB
	if budget > posterTimeoutMax {
		return posterTimeoutMax
	}
	return budget
}
```

Use overflow-safe ceiling logic if adding `gib-1` could overflow: divide first and increment when `size%gib != 0`.

- [ ] **Step 5: Resolve source size and apply it before launch**

Add:

```go
func (d *Dispatcher) timeoutForTask(task Task) time.Duration {
	base := d.opts.Timeouts[task.Type]
	if task.Type != TaskPoster && task.Type != TaskPosterRepair {
		return base
	}
	var libraryID int64
	var catalog string
	if err := d.q.db.QueryRowContext(context.Background(), `SELECT library_id,COALESCE(file_path,'') FROM media WHERE id=?`, task.MediaID).Scan(&libraryID, &catalog); err != nil {
		return base
	}
	sourcePath := storage.PreferredFFmpegPath(d.q.db, task.MediaID, libraryID, catalog)
	info, err := os.Stat(sourcePath)
	if err != nil {
		return base
	}
	return taskTimeoutForSource(task.Type, base, info.Size())
}
```

Add `os` and `knox-media/internal/storage` to imports. In `launch`, calculate `timeout := d.timeoutForTask(task)` and pass it to `context.WithTimeout`. Keep all non-poster timeout lookup, heartbeat, stop grace, budget retention, and failure classification unchanged. Resolve the same source choice as `PosterAdapter.ExecuteWithResult` through `storage.PreferredFFmpegPath`; inaccessible sources fall back safely to the base and are handled by the adapter.

- [ ] **Step 6: Run dispatcher tests and verify GREEN**

Run: `go test ./internal/postingest -run 'TestPosterTaskTimeout|TestDispatcher_.*Timeout|TestDispatcher_.*Deadline|TestDefaultDispatcherOptions' -count=1 -v`

Expected: PASS for base/scaled/capped poster and repair deadlines, unknown-source fallback, and unchanged non-poster deadlines.

- [ ] **Step 7: Run the postingest package suite**

Run: `go test ./internal/postingest -count=1`

Expected: PASS.

## Task 4: Keep ordinary media endpoints publication-safe for admins

**Files:**
- Modify: `api/handler/media_query_test.go:908-980`
- Modify: `api/handler/media.go:35-115,136-189`

- [ ] **Step 1: Replace the obsolete ordinary-admin compatibility test with failing visibility tests**

The existing `TestGetMediaAdminCanInspectProcessing` encodes behavior that now belongs only to `/api/v1/admin/media/:id/ingest`. Replace it with:

```go
func TestOrdinaryMediaListAdminStillHidesUnpublished(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET publication_state='processing' WHERE id=10;
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,publication_state) VALUES
		(11,1,'published-11','Published','E:/lib1/published.mp4','video','published'),
		(12,1,'degraded-12','Degraded','E:/lib1/degraded.mp4','video','degraded'),
		(13,1,'failed-13','Failed','E:/lib1/failed.mp4','video','failed')`); err != nil {
		t.Fatal(err)
	}
	c, w := listMediaTestContext("/api/v1/media?library_id=1&limit=10", 2)
	h.ListMedia(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct { Items []struct { PublicationState string `json:"publication_state"` } `json:"items"` }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Items {
		if item.PublicationState != "published" && item.PublicationState != "degraded" {
			t.Fatalf("ordinary admin list exposed %q", item.PublicationState)
		}
	}
}
func TestOrdinaryGetMediaAdminCannotInspectProcessing(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET publication_state='processing' WHERE id=10`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 2, "admin", "admin")
	h.GetMedia(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
```

Use `setupAccessTestDB(t)`, `listMediaTestContext`, `responseMediaIDs`, and the direct Gin context pattern already used by `TestGetMediaReturns404ForProcessingToOrdinaryUser`. Assert published and degraded controls still return 200. Rename `TestListMediaAdminIncludesUnpublishedInspectionFields` to `TestAdminListMediaIncludesUnpublishedInspectionFields`, change its URL to `/api/v1/admin/media?library_id=1&limit=1`, and call `h.AdminListMedia(c)`.

- [ ] **Step 2: Run ordinary-admin tests and verify RED**

Run: `go test ./api/handler -run 'TestOrdinaryMediaListAdmin|TestOrdinaryGetMediaAdmin' -count=1 -v`

Expected: FAIL because `ListMedia` sets `IncludeUnpublished` for admins and `GetMedia` passes admin status into the visibility predicate.

- [ ] **Step 3: Remove role-based elevation from ordinary list/get**

In `listMediaObserved`, remove the `middleware.IsAdmin(c)` branch. Instead, permit unpublished rows only when the dedicated caller supplied a publication state:

```go
if publicationState != "" {
	spec.IncludeUnpublished = true
	spec.PublicationState = publicationState
}
```

Because `AdminListMedia` also needs all states when its filter is empty, pass an explicit dedicated-admin signal rather than inferring it from credentials. The focused signature is:

```go
func (h *Handler) listMediaObserved(c *gin.Context, afterBatch func(mediaListStats), publicationState string, includeUnpublished bool)
```

Call it as `h.listMediaObserved(c, nil, "", false)` from `ListMedia` and `h.listMediaObserved(c, nil, state, true)` from `AdminListMedia`; update any observed-list test call sites. Set `spec.IncludeUnpublished` and `spec.PublicationState` from those arguments.

In `GetMedia`, change:

```go
WHERE m.id = ? AND `+mediaPublicationVisibilityPredicate("m", false)
```

Keep admin-only response fields for media that is already published/degraded; the requirement changes row visibility, not those fields.

- [ ] **Step 4: Run ordinary and dedicated admin tests and verify GREEN**

Run: `go test ./api/handler -run 'TestOrdinaryMedia|TestGetMedia|TestListMediaAdmin|TestAdminListMedia|TestAdminGetMediaIngest' -count=1 -v`

Expected: PASS. Ordinary list/get hide processing/failed/cancelled for admin credentials; dedicated admin list and ingest detail still expose them.

- [ ] **Step 5: Run the handler package suite**

Run: `go test ./api/handler -count=1`

Expected: PASS.

## Task 5: Register production poster repair capability

**Files:**
- Modify: `cmd/server/main_test.go:332-347`
- Modify: `cmd/server/main.go:242-279`

- [ ] **Step 1: Write the failing assembly-level capability assertion**

Extend `TestMainWiresOneSharedPublicationCapabilityRegistry`:

```go
steps := strings.Index(src, `publicationSteps := []string{`)
registry := strings.Index(src, `publicationCapabilities := publication.NewCapabilityMatrix(publicationSteps)`)
if steps < 0 || registry < 0 || steps > registry {
	t.Fatal("publication capability list is not assembled before the shared registry")
}
capabilityBlock := src[steps:registry]
if !strings.Contains(capabilityBlock, `"poster_repair"`) {
	t.Fatal("production capability registry omits poster_repair")
}
```

Keep the current assertions that the exact same `publicationCapabilities` variable is passed to `postingest.NewQueue`, planner options, handler dependencies, and overview assembly.

- [ ] **Step 2: Run the assembly test and verify RED**

Run: `go test ./cmd/server -run TestMainWiresOneSharedPublicationCapabilityRegistry -count=1 -v`

Expected: FAIL with `production capability registry omits poster_repair`.

- [ ] **Step 3: Add the production capability**

Change the list in `main.go` to:

```go
publicationSteps := []string{"poster", "poster_repair", "thumbnail", "preview", "keyframe", "subtitle", "atrack", "encrypt", "scrape"}
```

Do not create another matrix and do not change conditional `prepare` registration.

- [ ] **Step 4: Run server tests and verify GREEN**

Run: `go test ./cmd/server -run 'TestMainWiresOneSharedPublicationCapabilityRegistry|TestMainInjectsRegisteredIngestPrepareCapability|TestSharedResourceControlAssemblyMainOrder' -count=1 -v`

Expected: PASS.

## Task 6: Verify focused behavior and repository compatibility

**Files:**
- Verify only: all files listed above

- [ ] **Step 1: Run focused affected packages**

Run: `go test ./internal/publication ./internal/postingest ./api/handler ./cmd/server -count=1`

Expected: PASS.

- [ ] **Step 2: Run the full Go suite**

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 3: Run race-sensitive poster and dispatcher tests**

Run: `go test -race ./internal/publication ./internal/postingest -run 'TestSourceFingerprintContext|TestCommitStagedPoster|TestDispatcher_' -count=1`

Expected: PASS with no race reports.

- [ ] **Step 4: Inspect the final diff for scope and database safety**

Run: `git diff -- internal/publication/repair.go internal/publication/repair_test.go internal/postingest/poster_atomic.go internal/postingest/poster_quality_test.go internal/postingest/dispatcher.go internal/postingest/dispatcher_test.go api/handler/media.go api/handler/media_query_test.go cmd/server/main.go cmd/server/main_test.go`

Expected: only the approved hash, poster timeout, ordinary visibility, capability, and test changes. There must be no migration, SQL schema change, deployed DB file, generated binary, or unrelated refactor.

- [ ] **Step 5: Confirm exact compatibility behavior**

Run: `go test ./api/handler -run 'TestOrdinaryMedia|TestAdminListMedia|TestAdminGetMediaIngest' -count=1 -v && go test ./internal/postingest -run 'TestPosterTaskTimeout|TestCommitStagedPosterDoesNotRehashFullSource' -count=1 -v`

Expected: PASS, demonstrating that ordinary admin calls hide unpublished media, dedicated admin inspection remains available, poster deadlines are bounded and dynamic, and the source is fully fingerprinted once per task.




