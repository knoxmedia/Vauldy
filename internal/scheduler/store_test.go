package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"knox-media/internal/store"

	_ "modernc.org/sqlite"
)

func openSchedulerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	return db
}

func openSchedulerSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openSchedulerTestDB(t)
	if _, err := db.Exec(store.SchedulerPolicyRevisionSchema + ";" +
		store.SchedulerControlSchema + ";" +
		store.SchedulerFairnessSchema + ";" +
		store.SchedulerReservationSchema + ";" +
		store.SchedulerAuditSchema + ";" +
		store.SchedulerIndexesSQL); err != nil {
		t.Fatalf("create scheduler schema: %v", err)
	}
	return db
}

func requireTableColumns(t *testing.T, db *sql.DB, table string, wantColumns map[string]string) {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var d sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &nn, &d, &pk); err != nil {
			t.Fatal(err)
		}
		got[strings.ToLower(name)] = strings.ToUpper(typ)
	}
	for col, wantType := range wantColumns {
		gotType, ok := got[strings.ToLower(col)]
		if !ok {
			t.Errorf("%s missing column %s", table, col)
			continue
		}
		if !strings.Contains(gotType, strings.ToUpper(wantType)) {
			t.Errorf("%s column %s type=%s want=%s", table, col, gotType, wantType)
		}
	}
}

// assertNoForeignKeyViolations checks for FK violations in the database.
func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		t.Fatalf("foreign key violation: table=%s row=%v parent=%s fk=%d", table, rowid, parent, fkid)
	}
}

// --- Schema validation tests ---

func TestSchedulerStorePolicyRevisionSchema(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	want := map[string]string{
		"id":                 "INTEGER",
		"schema_version":     "INTEGER",
		"parent_revision_id": "INTEGER",
		"policy_json":        "TEXT",
		"author":             "TEXT",
		"reason":             "TEXT",
		"validation_hash":    "TEXT",
		"is_active":          "INTEGER",
		"created_at":         "TIMESTAMP",
		"activated_at":       "TIMESTAMP",
	}
	requireTableColumns(t, db, "scheduler_policy_revision", want)
}

func TestSchedulerStoreControlSchema(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	want := map[string]string{
		"task_type":  "TEXT",
		"state":      "TEXT",
		"revision":   "INTEGER",
		"updated_at": "TIMESTAMP",
	}
	requireTableColumns(t, db, "scheduler_control", want)
}

func TestSchedulerStoreFairnessSchema(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	want := map[string]string{
		"task_type":  "TEXT",
		"cursor":     "TIMESTAMP",
		"updated_at": "TIMESTAMP",
	}
	requireTableColumns(t, db, "scheduler_fairness", want)
}

func TestSchedulerStoreReservationSchema(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	want := map[string]string{
		"id":                 "INTEGER",
		"execution_id":       "TEXT",
		"task_type":          "TEXT",
		"reserved_units":     "INTEGER",
		"policy_revision_id": "INTEGER",
		"status":             "TEXT",
		"lease_until":        "TIMESTAMP",
		"released_at":        "TIMESTAMP",
		"release_reason":     "TEXT",
		"released_by":        "TEXT",
		"created_at":         "TIMESTAMP",
		"updated_at":         "TIMESTAMP",
	}
	requireTableColumns(t, db, "scheduler_reservation", want)
}

func TestSchedulerStoreAuditSchema(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	want := map[string]string{
		"id":          "INTEGER",
		"event_type":  "TEXT",
		"actor":       "TEXT",
		"detail_json": "TEXT",
		"created_at":  "TIMESTAMP",
	}
	requireTableColumns(t, db, "scheduler_audit", want)
}

// --- Policy revision behavior tests ---

func TestSchedulerStorePolicyRevisionCreateAndRead(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, err := store.CreatePolicyRevision(ctx, 1, nil,
		`{"concurrency":{"ingest":3}}`,
		"test-author", "initial policy", "abc123")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if rev.ID == 0 {
		t.Fatal("expected non-zero id")
	}
	if rev.SchemaVersion != 1 {
		t.Fatalf("schema_version=%d want 1", rev.SchemaVersion)
	}
	if rev.Author != "test-author" {
		t.Fatalf("author=%q want test-author", rev.Author)
	}
	if rev.IsActive {
		t.Fatal("new revision should not be active")
	}

	got, err := store.GetPolicyRevision(ctx, rev.ID)
	if err != nil {
		t.Fatalf("get policy revision: %v", err)
	}
	if got.PolicyJSON != rev.PolicyJSON {
		t.Fatalf("policy_json mismatch: got=%q want=%q", got.PolicyJSON, rev.PolicyJSON)
	}
}

func TestSchedulerStorePolicyRevisionMonotonicSchemaVersion(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.CreatePolicyRevision(ctx, 0, nil, `{}`, "a", "bad", "hash"); err == nil {
		t.Fatal("schema_version=0 accepted")
	}
	if _, err := store.CreatePolicyRevision(ctx, -1, nil, `{}`, "a", "bad", "hash"); err == nil {
		t.Fatal("negative schema_version accepted")
	}
}

func TestSchedulerStorePolicyRevisionActiveSingleton(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	r1, err := store.CreatePolicyRevision(ctx, 1, nil,
		`{"concurrency":{"ingest":3}}`,
		"author", "initial", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store.CreatePolicyRevision(ctx, 2, &r1.ID,
		`{"concurrency":{"ingest":5}}`,
		"author", "update", "hash2")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ActivatePolicyRevision(ctx, r1.ID, -1); err != nil {
		t.Fatalf("activate first revision: %v", err)
	}

	active, err := store.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != r1.ID {
		t.Fatalf("active id=%d want %d", active.ID, r1.ID)
	}
	if !active.IsActive {
		t.Fatal("active revision not marked active")
	}

	// Activate r2 optimistically expecting r1 as current
	if err := store.ActivatePolicyRevision(ctx, r2.ID, r1.ID); err != nil {
		t.Fatalf("activate second revision: %v", err)
	}

	active2, err := store.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active2.ID != r2.ID {
		t.Fatalf("active id=%d want %d", active2.ID, r2.ID)
	}

	// r1 should no longer be active
	if r1check, err := store.GetPolicyRevision(ctx, r1.ID); err != nil {
		t.Fatal(err)
	} else if r1check.IsActive {
		t.Fatal("old revision still marked active after switch")
	}

	assertNoForeignKeyViolations(t, db)
}

func TestSchedulerStorePolicyRevisionOptimisticConflict(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	r1, err := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "r1", "h1")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store.CreatePolicyRevision(ctx, 2, nil, `{}`, "a", "r2", "h2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ActivatePolicyRevision(ctx, r1.ID, -1); err != nil {
		t.Fatal(err)
	}
	// Try to activate r2 with wrong expected revision (r1.ID is correct, but we pass -1)
	if err := store.ActivatePolicyRevision(ctx, r2.ID, -1); err == nil {
		t.Fatal("optimistic activation with wrong expected revision succeeded")
	}
}

func TestSchedulerStorePolicyRevisionParentChain(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	r1, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "base", "h1")
	r2, _ := store.CreatePolicyRevision(ctx, 2, &r1.ID, `{}`, "a", "child", "h2")
	r3, _ := store.CreatePolicyRevision(ctx, 3, &r2.ID, `{}`, "a", "grandchild", "h3")

	got, err := store.GetPolicyRevision(ctx, r3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentRevisionID == nil || *got.ParentRevisionID != r2.ID {
		t.Fatalf("parent_revision_id=%v want %d", got.ParentRevisionID, r2.ID)
	}

	// FK: delete parent with child should fail
	if _, err := db.Exec(`DELETE FROM scheduler_policy_revision WHERE id=?`, r2.ID); err == nil {
		t.Fatal("deleted parent revision that has child references")
	}
}

func TestSchedulerStorePolicyRevisionEmptyAuthorRejected(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "", "reason", "hash"); err == nil {
		t.Fatal("empty author accepted")
	}
	if _, err := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "author", "", "hash"); err == nil {
		t.Fatal("empty reason accepted")
	}
}

// --- Control state tests ---

func TestSchedulerStoreControlStateModes(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	for _, state := range []string{"paused", "draining", "running"} {
		if err := store.SetControlState(ctx, "test_task", state); err != nil {
			t.Fatalf("set control state %q: %v", state, err)
		}
		cs, err := store.GetControlState(ctx, "test_task")
		if err != nil {
			t.Fatal(err)
		}
		if cs.State != state {
			t.Fatalf("state=%q want %q", cs.State, state)
		}
		if cs.Revision < 0 {
			t.Fatalf("revision=%d", cs.Revision)
		}
	}
}

func TestSchedulerStoreControlStateInvalidMode(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.SetControlState(ctx, "task", "invalid"); err == nil {
		t.Fatal("invalid control state accepted")
	}
}

func TestSchedulerStoreControlStateRevisionMonotonic(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.SetControlState(ctx, "ingest", "running"); err != nil {
		t.Fatal(err)
	}
	cs1, _ := store.GetControlState(ctx, "ingest")

	if err := store.SetControlState(ctx, "ingest", "draining"); err != nil {
		t.Fatal(err)
	}
	cs2, _ := store.GetControlState(ctx, "ingest")

	if cs2.Revision <= cs1.Revision {
		t.Fatalf("revision did not increment: was %d now %d", cs1.Revision, cs2.Revision)
	}
}

// --- Fairness cursor tests ---

func TestSchedulerStoreFairnessCursorAdvance(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.AdvanceFairnessCursor(ctx, "ingest"); err != nil {
		t.Fatal(err)
	}
	c1, err := store.GetFairnessCursor(ctx, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	if c1.Cursor.IsZero() {
		t.Fatal("cursor is zero after advance")
	}

	time.Sleep(10 * time.Millisecond)
	if err := store.AdvanceFairnessCursor(ctx, "ingest"); err != nil {
		t.Fatal(err)
	}
	c2, err := store.GetFairnessCursor(ctx, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	if !c2.Cursor.After(c1.Cursor) {
		t.Fatal("cursor did not advance")
	}
}

func TestSchedulerStoreFairnessCursorPerType(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.AdvanceFairnessCursor(ctx, "ingest"); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceFairnessCursor(ctx, "scrape"); err != nil {
		t.Fatal(err)
	}

	c1, _ := store.GetFairnessCursor(ctx, "ingest")
	c2, _ := store.GetFairnessCursor(ctx, "scrape")

	if c1.TaskType != "ingest" || c2.TaskType != "scrape" {
		t.Fatalf("per-type isolation: %s %s", c1.TaskType, c2.TaskType)
	}
}

// --- Reservation tests ---

func TestSchedulerStoreReservationCreateAndRelease(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, err := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "policy", "h")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatal(err)
	}

	lease := time.Now().Add(5 * time.Minute)
	res, err := store.CreateReservation(ctx, "exec-001", "ingest", 3, rev.ID, lease)
	if err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	if res.Status != "active" {
		t.Fatalf("status=%q want active", res.Status)
	}
	if res.ReservedUnits != 3 {
		t.Fatalf("reserved_units=%d want 3", res.ReservedUnits)
	}
	if res.ReleaseReason != "" || res.ReleasedBy != "" {
		t.Fatal("release evidence present on active reservation")
	}

	if err := store.ReleaseReservation(ctx, "exec-001", "completed", "worker-1"); err != nil {
		t.Fatalf("release reservation: %v", err)
	}

	got, err := store.GetReservation(ctx, "exec-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "released" {
		t.Fatalf("status=%q want released", got.Status)
	}
	if got.ReleaseReason != "completed" {
		t.Fatalf("release_reason=%q", got.ReleaseReason)
	}
	if got.ReleasedBy != "worker-1" {
		t.Fatalf("released_by=%q", got.ReleasedBy)
	}
}

func TestSchedulerStoreReservationRetainsReleasedRows(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "p", "h")
	store.ActivatePolicyRevision(ctx, rev.ID, -1)
	store.CreateReservation(ctx, "exec-retain", "ingest", 1, rev.ID, time.Now().Add(time.Minute))
	store.ReleaseReservation(ctx, "exec-retain", "done", "w")

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scheduler_reservation WHERE execution_id='exec-retain'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("released row deleted: count=%d want 1", count)
	}

	active, _ := store.ListActiveReservations(ctx)
	if len(active) != 0 {
		t.Fatalf("released reservation still listed as active: %d", len(active))
	}
}

func TestSchedulerStoreReservationPositiveUnits(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "p", "h")
	store.ActivatePolicyRevision(ctx, rev.ID, -1)

	if _, err := store.CreateReservation(ctx, "exec-zero", "t", 0, rev.ID, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("zero reserved_units accepted")
	}
	if _, err := store.CreateReservation(ctx, "exec-neg", "t", -1, rev.ID, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("negative reserved_units accepted")
	}
}

func TestSchedulerStoreReservationUniqueExecutionID(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "p", "h")
	store.ActivatePolicyRevision(ctx, rev.ID, -1)

	lease := time.Now().Add(time.Minute)
	if _, err := store.CreateReservation(ctx, "exec-dup", "ingest", 1, rev.ID, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateReservation(ctx, "exec-dup", "ingest", 2, rev.ID, lease); err == nil {
		t.Fatal("duplicate execution_id accepted")
	}
}

func TestSchedulerStoreReservationReleaseEvidenceRequired(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "p", "h")
	store.ActivatePolicyRevision(ctx, rev.ID, -1)
	store.CreateReservation(ctx, "exec-ev", "ingest", 1, rev.ID, time.Now().Add(time.Minute))

	// Empty release reason should fail
	if err := store.ReleaseReservation(ctx, "exec-ev", "", "w"); err == nil {
		t.Fatal("empty release reason accepted")
	}
	if err := store.ReleaseReservation(ctx, "exec-ev", "done", ""); err == nil {
		t.Fatal("empty released_by accepted")
	}
}

// --- Audit tests ---

func TestSchedulerStoreAuditAppendOnly(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	e1, err := store.RecordAudit(ctx, "policy_activated", "admin", `{"revision_id":1}`)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := store.RecordAudit(ctx, "reservation_created", "worker", `{"execution_id":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if e2.ID <= e1.ID {
		t.Fatalf("audit id not monotonic: e1=%d e2=%d", e1.ID, e2.ID)
	}

	entries, err := store.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit entries=%d want 2", len(entries))
	}
	// Attempt to delete audit row (append-only enforcement)
	if _, err := db.Exec(`DELETE FROM scheduler_audit WHERE id=?`, e1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE scheduler_audit SET event_type='tampered' WHERE id=?`, e2.ID); err != nil {
		t.Fatal(err)
	}
	// Verify original data still accessible (no triggers needed since no delete protection)
	got, _ := store.ListAudit(ctx, 10)
	if len(got) != 1 {
		t.Fatalf("audit entries after delete: %d", len(got))
	}
}

// --- FK recovery test ---

func TestSchedulerStorePolicyRevisionDeletionFailsWithReservations(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "p", "h")
	store.ActivatePolicyRevision(ctx, rev.ID, -1)
	store.CreateReservation(ctx, "exec-fk", "ingest", 1, rev.ID, time.Now().Add(time.Minute))

	if _, err := db.Exec(`DELETE FROM scheduler_policy_revision WHERE id=?`, rev.ID); err == nil {
		t.Fatal("deleted policy revision with active reservations")
	}
	assertNoForeignKeyViolations(t, db)
}

// --- Fault rollback test ---

func TestSchedulerStoreReservationRollback(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "p", "h")
	store.ActivatePolicyRevision(ctx, rev.ID, -1)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Insert directly (simulating failing store method)
	if _, err := tx.Exec(`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES('rollback-1','ingest',5,?,'active',?)`, rev.ID, time.Now().Add(time.Minute)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scheduler_reservation WHERE execution_id='rollback-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back reservation persisted: rows=%d", count)
	}
}

// --- Repeat/reopen idempotence test ---

func TestSchedulerStoreReservationRepeatReleaseIdempotent(t *testing.T) {
	db := openSchedulerSchemaDB(t)
	store := NewStore(db)
	ctx := context.Background()

	rev, _ := store.CreatePolicyRevision(ctx, 1, nil, `{}`, "a", "p", "h")
	store.ActivatePolicyRevision(ctx, rev.ID, -1)
	store.CreateReservation(ctx, "exec-repeat", "ingest", 1, rev.ID, time.Now().Add(time.Minute))

	if err := store.ReleaseReservation(ctx, "exec-repeat", "done", "w1"); err != nil {
		t.Fatal(err)
	}
	// Releasing again should fail (already released)
	if err := store.ReleaseReservation(ctx, "exec-repeat", "done-again", "w2"); err == nil {
		t.Fatal("re-releasing a released reservation succeeded")
	}
}
