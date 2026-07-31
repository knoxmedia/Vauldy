# Task Alignment (Display) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Admin overview and TaskManager report the same per-media, current-generation counts and display statuses for `subtitle`, `preview`, `atrack`, `keyframe`, and `encrypt`.

**Architecture:** Add `internal/taskalign` that synthesizes a unified `display_status` from current-generation `post_ingest_task` plus domain `*_task` rows, expose `task_alignment` on admin overview, keep dual tables but fix enqueue/cleanup write paths so both sides stay coherent.

**Tech Stack:** Go 1.22+, SQLite, Gin, React/TypeScript (existing AdminConsole + TaskManager).

**Spec:** `docs/superpowers/specs/2026-07-31-task-alignment-display-design.md`

---

## File map

| File | Responsibility |
|------|----------------|
| Create `internal/taskalign/status.go` | Domain/queue → display mapping + synthesis priority |
| Create `internal/taskalign/status_test.go` | Unit tests for mapping/synthesis |
| Create `internal/taskalign/compute.go` | `Compute(ctx, db)` → `Alignment` aggregates |
| Create `internal/taskalign/compute_test.go` | DB-backed aggregation tests |
| Modify `api/handler/admin_overview.go` | Attach `task_alignment` in `Build` |
| Modify `api/handler/admin_overview_test.go` | Assert `task_alignment` in overview JSON |
| Modify `internal/publication/planner.go` | After inserting queue-backed optional steps, Ensure domain rows in same tx when possible |
| Modify `api/handler/postingest_retry.go` | After enqueue, Ensure domain waiting/pending |
| Modify `api/handler/subtitle_task.go` (and preview/atrack/keyframe cleanup) | Cleanup/delete also cancel/delete current-gen `post_ingest_task` |
| Modify `web/src/api/client.ts` | `AdminOverview.task_alignment` type |
| Modify `web/src/pages/AdminConsole.tsx` | Show alignment counts for the five types |
| Modify `web/src/pages/TaskManager.tsx` | Tab summaries/filters use display vocabulary + overview alignment when available |

---

### Task 1: Display status synthesis (pure Go)

**Files:**
- Create: `internal/taskalign/status.go`
- Create: `internal/taskalign/status_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package taskalign

import "testing"

func TestMapDomainStatus(t *testing.T) {
	cases := []struct {
		typ, in, want string
	}{
		{"subtitle", "pending", "waiting"},
		{"subtitle", "running", "running"},
		{"subtitle", "done", "done"},
		{"subtitle", "failed", "failed"},
		{"preview", "ready", "done"},
		{"preview", "waiting", "waiting"},
		{"preview", "failed", "failed"},
		{"atrack", "waiting", "waiting"},
		{"atrack", "done", "done"},
		{"keyframe", "failed", "failed"},
		{"encrypt", "waiting", "waiting"},
	}
	for _, tc := range cases {
		if got := MapDomainOrQueue(tc.typ, tc.in); got != tc.want {
			t.Fatalf("%s %q → %q want %q", tc.typ, tc.in, got, tc.want)
		}
	}
}

func TestSynthesizePriority(t *testing.T) {
	if got := Synthesize("waiting", "running", "subtitle"); got != "running" {
		t.Fatalf("got %q", got)
	}
	if got := Synthesize("failed", "pending", "subtitle"); got != "failed" {
		t.Fatalf("failed+pending → %q want failed", got)
	}
	if got := Synthesize("cancelled", "waiting", "subtitle"); got != "cancelled" {
		t.Fatalf("cancelled+waiting → %q", got)
	}
	if got := Synthesize("", "pending", "subtitle"); got != "waiting" {
		t.Fatalf("domain-only pending → %q", got)
	}
	if got := Synthesize("done", "", "subtitle"); got != "done" {
		t.Fatalf("queue-only done → %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/taskalign/ -count=1 -run 'TestMapDomainStatus|TestSynthesizePriority'`

Expected: FAIL (package or symbols missing)

- [ ] **Step 3: Minimal implementation**

Update Step 1 calls to `Synthesize(queueStatus, domainStatus, taskType string)` if needed, then implement:

```go
package taskalign

import "strings"

func MapDomainOrQueue(taskType, status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	taskType = strings.ToLower(strings.TrimSpace(taskType))
	switch taskType {
	case "subtitle":
		switch status {
		case "pending":
			return "waiting"
		case "running", "done", "failed":
			return status
		}
	case "preview":
		switch status {
		case "ready":
			return "done"
		case "waiting", "failed":
			return status
		}
	default: // atrack, keyframe, encrypt, and queue statuses
		switch status {
		case "pending":
			return "waiting"
		case "ready":
			return "done"
		case "waiting", "running", "done", "failed", "cancelled":
			return status
		}
	}
	return ""
}

func priority(s string) int {
	switch s {
	case "running":
		return 5
	case "waiting":
		return 4
	case "failed":
		return 3
	case "cancelled":
		return 2
	case "done":
		return 1
	default:
		return 0
	}
}

func Synthesize(queueStatus, domainStatus, taskType string) string {
	q := MapDomainOrQueue(taskType, queueStatus)
	d := MapDomainOrQueue(taskType, domainStatus)
	if (q == "failed" || q == "cancelled") && d == "waiting" {
		return q
	}
	if priority(q) >= priority(d) {
		if q != "" {
			return q
		}
		return d
	}
	return d
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/taskalign/ -count=1 -run 'TestMapDomainStatus|TestSynthesizePriority'`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/taskalign/status.go internal/taskalign/status_test.go
git commit -m "feat(taskalign): add display status mapping and synthesis"
```

---

### Task 2: Compute alignment aggregates

**Files:**
- Create: `internal/taskalign/compute.go`
- Create: `internal/taskalign/compute_test.go`

- [ ] **Step 1: Write failing DB test**

Use `store.OpenSQLite(":memory:")` (or the same helper other packages use). Seed:

- media id=1 `ingest_generation=2`
- `post_ingest_task` subtitle g1 done + g2 waiting
- `subtitle_task` pending for media 1
- media id=2 generation=1, queue failed, domain pending

Assert:

```go
a, err := Compute(context.Background(), db)
// subtitle waiting == 1 (media 1 current gen waiting wins over domain pending? queue waiting + domain pending → waiting)
// subtitle failed == 1 (media 2)
// subtitle done == 0 (g1 done ignored)
```

Also seed encrypt-only row and assert encrypt bucket.

- [ ] **Step 2: Run test — expect FAIL**

Run: `go test ./internal/taskalign/ -count=1 -run TestCompute`

- [ ] **Step 3: Implement `Compute`**

Types:

```go
type Counts map[string]int64 // display_status → count

type Alignment struct {
	ByType map[string]Counts `json:"by_type"`
}

func emptyAlignment() Alignment {
	types := []string{"subtitle", "preview", "atrack", "keyframe", "encrypt"}
	out := Alignment{ByType: map[string]Counts{}}
	for _, t := range types {
		out.ByType[t] = Counts{"waiting": 0, "running": 0, "done": 0, "failed": 0, "cancelled": 0}
	}
	return out
}
```

Algorithm (Go-side join is fine for clarity/tests):

1. Query current-gen queue rows for the five types:

```sql
SELECT p.media_id, p.task_type, p.status, p.generation
FROM post_ingest_task p
JOIN media m ON m.id = p.media_id
WHERE p.task_type IN ('subtitle','preview','atrack','keyframe','encrypt')
  AND p.generation = COALESCE(m.ingest_generation, 0)
```

2. For each type, left-join domain table by `media_id` (separate queries or one UNION). Domain tables: `subtitle_task`, `preview_task`, `atrack_task`, `keyframe_task`; encrypt has none.

3. Per `(task_type, media_id)` keep one row (max `p.id` if duplicates).

4. `display := Synthesize(queueStatus, domainStatus, taskType)`; increment `ByType[type][display]`.

Legacy: `COALESCE(m.ingest_generation,0)` covers generation-0 unlinkedenqueue.

- [ ] **Step 4: Tests PASS**

Run: `go test ./internal/taskalign/ -count=1`

- [ ] **Step 5: Commit**

```bash
git add internal/taskalign/
git commit -m "feat(taskalign): compute per-media current-generation alignment counts"
```

---

### Task 3: Wire `task_alignment` into admin overview

**Files:**
- Modify: `api/handler/admin_overview.go`
- Modify: `api/handler/admin_overview_test.go`
- Modify: `web/src/api/client.ts` (type only in this task if preferred with Task 6)

- [ ] **Step 1: Extend overview test expectations**

In `admin_overview_test.go`, after building overview, assert:

```go
align, ok := data["task_alignment"].(map[string]any)
// or unmarshal into struct
// require by_type.subtitle keys waiting/running/done/failed/cancelled
```

Seed enough rows that raw `post_ingest_queue.by_type.subtitle.done` row count ≠ aligned done media count (multi-gen), and assert alignment matches media count.

- [ ] **Step 2: Run test — FAIL (missing field)**

- [ ] **Step 3: In `Build`, after `loadQueue`:**

```go
align, err := taskalign.Compute(ctx, b.DB)
if err != nil {
	return nil, err
}
// ...
out["task_alignment"] = align
```

Keep existing `post_ingest_queue` unchanged for non-aligned consumers; UI will prefer `task_alignment` for the five types.

- [ ] **Step 4: Tests PASS**

Run: `go test ./api/handler/ -count=1 -run AdminOverview`

- [ ] **Step 5: Commit**

```bash
git add api/handler/admin_overview.go api/handler/admin_overview_test.go
git commit -m "feat(admin): expose task_alignment on overview"
```

---

### Task 4: Ensure domain rows at enqueue time

**Files:**
- Modify: `internal/publication/planner.go` (after successful `INSERT INTO post_ingest_task` for subtitle/preview/atrack/keyframe)
- Modify: `api/handler/postingest_retry.go` (`enqueueExplicitPostIngest`)
- Modify: `internal/postingest/enqueue.go` if still used in tests/legacy
- Tests: planner test or postingest enqueue test asserting domain row exists immediately

- [ ] **Step 1: Failing test**

Plan a media with subtitle optional step (or call `enqueueExplicitPostIngest` for subtitle). Before any adapter run:

```sql
SELECT status FROM subtitle_task WHERE media_id=?
```

Expect `pending`. Today this fails (no row).

- [ ] **Step 2: Run — FAIL**

- [ ] **Step 3: Implementation**

Helper in `internal/taskalign/ensure.go` or postingest:

```go
func EnsureDomainWaiting(ctx context.Context, tx *sql.Tx, taskType string, mediaID int64) error
```

- subtitle: `INSERT OR IGNORE INTO subtitle_task(...,'pending',...)`
- preview: `EnsureWaitingTask` (needs duration — use library default or `0`/`120` consistent with adapter)
- atrack/keyframe: `INSERT OR IGNORE ... status='waiting'`

Call from:

1. `planner.persistPlanTx` inside the same tx after each queue-backed insert for those types.
2. `enqueueExplicitPostIngest` before `tx.Commit` when inserting/updating to waiting.

Encrypt: no-op.

- [ ] **Step 4: Tests PASS**

- [ ] **Step 5: Commit**

```bash
git commit -m "fix(ingest): create domain task rows when enqueueing aligned types"
```

---

### Task 5: Cleanup / delete syncs post_ingest

**Files:**
- Modify: `api/handler/subtitle_task.go` — `CleanupSubtitleTasksFailed`, `DeleteSubtitleTask`
- Modify: preview/atrack/keyframe cleanup handlers equivalently
- Tests: handler or service tests

- [ ] **Step 1: Failing test**

Seed media with `post_ingest_task` subtitle `failed` + `subtitle_task` `failed`. Call cleanup-failed. Assert **both** gone (or queue `cancelled` and domain deleted — pick **delete domain + cancel/delete current-gen queue row** as spec).

- [ ] **Step 2: Run — FAIL (queue row remains)**

- [ ] **Step 3: Implementation**

After domain delete/cleanup, for each affected `media_id`:

```sql
DELETE FROM post_ingest_task
WHERE media_id=? AND task_type=? AND generation=(SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?)
  AND status IN ('failed','cancelled','waiting')
```

Or `UPDATE ... SET status='cancelled'` if delete is too aggressive for audit — prefer **DELETE** for cleanup-failed to match domain delete semantics; for single delete API same.

Do not delete `running` queue rows (return error or skip).

- [ ] **Step 4: Tests PASS for subtitle; mirror for preview/atrack/keyframe**

- [ ] **Step 5: Commit**

```bash
git commit -m "fix(tasks): sync post_ingest rows when cleaning domain task tables"
```

---

### Task 6: AdminConsole UI uses alignment for five types

**Files:**
- Modify: `web/src/api/client.ts` — add `task_alignment` to `AdminOverview`
- Modify: `web/src/pages/AdminConsole.tsx`
- Modify: `web/src/pages/__tests__/AdminConsole.test.tsx`

- [ ] **Step 1: Update fixture + assertion in AdminConsole test** for `task_alignment` presence / rendering

- [ ] **Step 2: Run frontend test — FAIL**

- [ ] **Step 3: Implement**

- Extend type:

```ts
task_alignment: {
  by_type: Record<string, Record<string, number>>;
};
```

- Where tags render `post_ingest_queue.by_type`, for keys in `subtitle|preview|atrack|keyframe|encrypt` use `overview.task_alignment.by_type[type]` instead.
- Label optionally: `{type} / {status}: {count}` unchanged.
- Validator `isValidOverview` must accept `task_alignment`.

- [ ] **Step 4: Tests PASS**

Run: `npm test -- --run AdminConsole` (or project’s equivalent)

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(web): show task_alignment counts on admin console"
```

---

### Task 7: TaskManager summaries / filters use display vocabulary

**Files:**
- Modify: `web/src/pages/TaskManager.tsx`
- Optional: small helper `web/src/lib/taskDisplayStatus.ts`

- [ ] **Step 1: Identify subtitle/preview status filter chips** (`pending` vs `waiting`, `ready` vs `done`)

- [ ] **Step 2: Map list row status through the same rules as backend for filter matching**

```ts
export function toDisplayStatus(type: string, status: string): string {
  if (type === "subtitle" && status === "pending") return "waiting";
  if (type === "preview" && status === "ready") return "done";
  return status;
}
```

Filter UI options for subtitle: `waiting|running|done|failed` (not `pending`).  
Preview: `waiting|done|failed` (map `ready` → done in display).

For tab header counts: if overview/SSE already loaded, prefer `task_alignment.by_type[tab]`; else compute from loaded list via `toDisplayStatus` (list is capped at limit — document that full truth is overview).

- [ ] **Step 3: Add/adjust component test if one exists for filters**

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(web): align TaskManager status filters with display vocabulary"
```

---

### Task 8: End-to-end acceptance check

**Files:**
- Create: `internal/taskalign/acceptance_test.go` (or extend compute_test)

- [ ] **Step 1: Script the prod skew scenario**

Seed:

- 10 waiting queue subtitle without domain → after Ensure fix, domain exists; alignment waiting=10
- 12 cancelled queue without domain → alignment cancelled=12  
- 8 failed queue + pending domain → alignment failed=8 (not waiting)
- 6 media with two done generations → alignment done=6 not 12

- [ ] **Step 2: `Compute` assertions match**

- [ ] **Step 3: Commit**

```bash
git commit -m "test(taskalign): acceptance cases for dual-table count skew"
```

---

## Spec coverage checklist

| Spec requirement | Task |
|------------------|------|
| Display alignment mode | 1–7 |
| Per-media current generation | 2 |
| Scope five types | 2–7 |
| Status mapping + priority + failed+pending rule | 1 |
| `task_alignment` on overview/SSE | 3 (SSE reuses `Build`) |
| AdminConsole consumption | 6 |
| TaskManager summaries/filters | 7 |
| Enqueue Ensure domain | 4 |
| Cleanup sync | 5 |
| Encrypt queue-only | 1–2 |
| Non-goals (scrape/etc.) | not implemented |
| Acceptance skew | 8 |

## Placeholder / consistency self-review

- No TBD steps; `Synthesize` signature unified as `Synthesize(queueStatus, domainStatus, taskType string)` in Task 1 implementation note.
- Overview field name fixed: `task_alignment`.
- Display labels fixed: `waiting|running|done|failed|cancelled`.
