package taskcontrol

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// setupQueryTestDB creates an in-memory SQLite DB with post_ingest_task and
// projection tables, seeded with varied test data.
func setupQueryTestDB(t *testing.T) (*sql.DB, *QueryService) {
	t.Helper()
	db, builder := setupProjectionTestDB(t)
	ctx := context.Background()

	// Seed 50 tasks with varied attributes
	types := []string{"poster", "thumbnail", "preview", "keyframe", "transcode",
		"subtitle_extract", "subtitle_recognize", "package", "encrypt", "ai_analysis"}
	statuses := []string{"waiting", "running", "done", "failed", "cancelled", "skipped"}

	for i := 0; i < 50; i++ {
		typ := types[i%len(types)]
		st := statuses[i%len(statuses)]
		opts := map[string]any{
			"media_id":      int64(100 + i),
			"base_priority": int64(i * 10),
			"priority":      int64(i % 5),
		}
		if i%7 == 0 {
			opts["removed_at"] = "2024-01-01T00:00:00Z"
			opts["removed_by"] = "admin"
			opts["remove_reason"] = "cleanup"
		}
		if i%3 == 0 {
			opts["lease_owner"] = "worker-" + fmt.Sprintf("%d", i)
		}
		if i%4 == 0 {
			opts["library_id"] = int64(i % 3)
		}
		insertOracleTask(t, db, typ, st, opts)
	}

	// Store a few projection revisions
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	for i := int64(1); i <= 5; i++ {
		if _, err := builder.StoreRevision(ctx, tx, BuildIdentity("orchestration", i)); err != nil {
			tx.Rollback()
			t.Fatalf("StoreRevision: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return db, NewQueryService(builder)
}

// --- Filter Tests ---

func TestQueryFilterTaskTypeAlone(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(),
		QueryFilter{TaskType: "poster"}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if result.Total == 0 {
		t.Error("expected non-zero total for poster tasks")
	}
	for _, item := range result.Items {
		if item.TaskType != "poster" {
			t.Errorf("unexpected task type %q in poster filter", item.TaskType)
		}
	}
	// Total must match number of poster rows
	var expectedTotal int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE task_type='poster' AND removed_at IS NULL`).Scan(&expectedTotal); err != nil {
		t.Fatal(err)
	}
	if result.Total != expectedTotal {
		t.Errorf("total = %d, want %d", result.Total, expectedTotal)
	}
}

func TestQueryFilterStatusAlone(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(),
		QueryFilter{Status: "running"}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range result.Items {
		if item.RawStatus != "running" {
			t.Errorf("unexpected raw status %q in running filter", item.RawStatus)
		}
	}
}

func TestQueryFilterCombinedTypeAndStatus(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(),
		QueryFilter{TaskType: "keyframe", Status: "waiting"}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range result.Items {
		if item.TaskType != "keyframe" || item.RawStatus != "waiting" {
			t.Errorf("unexpected type=%q status=%q in combined filter", item.TaskType, item.RawStatus)
		}
	}
}

func TestQueryFilterRemovedExclude(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(),
		QueryFilter{Removed: "exclude"}, "", 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range result.Items {
		if item.RemovedAt != nil {
			t.Errorf("removed_at should be nil with exclude mode, got %v", item.RemovedAt)
		}
	}
}

func TestQueryFilterRemovedInclude(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	excludeRes, _ := qs.List(context.Background(),
		QueryFilter{Removed: "exclude"}, "", 200)
	includeRes, err := qs.List(context.Background(),
		QueryFilter{Removed: "include"}, "", 200)
	if err != nil {
		t.Fatalf("List include: %v", err)
	}
	if includeRes.Total < excludeRes.Total {
		t.Errorf("include total (%d) should be >= exclude total (%d)",
			includeRes.Total, excludeRes.Total)
	}
}

func TestQueryFilterRemovedOnly(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(),
		QueryFilter{Removed: "only"}, "", 200)
	if err != nil {
		t.Fatalf("List only: %v", err)
	}
	for _, item := range result.Items {
		if item.RemovedAt == nil {
			t.Errorf("removed_at should be set with only mode")
		}
	}
}

func TestQueryFilterGeneration(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	gen := int64(0)
	result, err := qs.List(context.Background(),
		QueryFilter{Generation: &gen}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range result.Items {
		if item.Generation != 0 {
			t.Errorf("unexpected generation %d for filter gen=0", item.Generation)
		}
	}
}

func TestQueryFilterLibraryID(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	libID := int64(0)
	result, err := qs.List(context.Background(),
		QueryFilter{LibraryID: &libID}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range result.Items {
		if item.LibraryID == nil || *item.LibraryID != 0 {
			t.Errorf("unexpected library_id %v for filter lib=0", item.LibraryID)
		}
	}
}

func TestQueryFilterOwner(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(),
		QueryFilter{Owner: "worker-3"}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range result.Items {
		if item.OwnerLease == nil || item.OwnerLease.Owner != "worker-3" {
			t.Errorf("unexpected owner %v for owner filter", item.OwnerLease)
		}
	}
}

// --- Pagination Tests ---

func TestQueryEmptyPage(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(),
		QueryFilter{TaskType: "nonexistent"}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) != 0 {
		t.Errorf("expected 0 items for nonexistent type, got %d", len(result.Items))
	}
	if result.Total != 0 {
		t.Errorf("expected total 0, got %d", result.Total)
	}
	if result.HasMore {
		t.Error("expected has_more=false for empty result")
	}
}

func TestQueryLimitOne(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(), QueryFilter{}, "", 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) > 1 {
		t.Errorf("expected at most 1 item with limit=1, got %d", len(result.Items))
	}
}

func TestQueryLimit50(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(), QueryFilter{}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) > 50 {
		t.Errorf("expected at most 50 items, got %d", len(result.Items))
	}
}

func TestQueryLimit200(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	result, err := qs.List(context.Background(), QueryFilter{}, "", 200)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Items) > 200 {
		t.Errorf("expected at most 200 items, got %d", len(result.Items))
	}
}

func TestQueryExactTotalMatchesFilterCount(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	filter := QueryFilter{Status: "waiting", Removed: "exclude"}
	result, err := qs.List(context.Background(), filter, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var expectedTotal int64
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM post_ingest_task WHERE status='waiting' AND removed_at IS NULL`,
	).Scan(&expectedTotal); err != nil {
		t.Fatal(err)
	}

	if result.Total != expectedTotal {
		t.Errorf("total = %d, want %d", result.Total, expectedTotal)
	}
}

// --- Cursor Tests ---

func TestQueryCursorPaginationNoDuplicates(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	seen := map[string]bool{}
	cursor := ""
	for {
		result, err := qs.List(context.Background(), QueryFilter{Removed: "exclude"}, cursor, 5)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, item := range result.Items {
			if seen[item.TaskID] {
				t.Errorf("duplicate item %s across pages", item.TaskID)
			}
			seen[item.TaskID] = true
		}
		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
		if cursor == "" {
			t.Fatal("has_more=true but next_cursor is empty")
		}
	}
	if len(seen) == 0 {
		t.Error("expected non-zero items across pages")
	}
}

func TestQueryCursorFilterDigestMismatch(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	// Get a cursor with one filter using a small page size
	r1, _ := qs.List(context.Background(),
		QueryFilter{TaskType: "poster"}, "", 2)
	if r1 == nil || r1.NextCursor == "" {
		t.Skip("no cursor produced (not enough poster tasks)")
	}

	// Try to use cursor with a different filter
	_, err := qs.List(context.Background(),
		QueryFilter{TaskType: "thumbnail"}, r1.NextCursor, 2)
	if err == nil {
		t.Error("expected cursor_filter_mismatch error")
	}
	if err != nil && !contains(err.Error(), "cursor_filter_mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestQueryCursorInvalidEncoding(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	_, err := qs.List(context.Background(), QueryFilter{}, "!!!not-valid-base64!!!", 5)
	if err == nil {
		t.Error("expected invalid_cursor error for bad base64")
	}
}

func TestQueryCursorStableClaimOrder(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	// Run the same list twice without data changes and verify order is stable
	r1, err := qs.List(context.Background(), QueryFilter{Removed: "exclude"}, "", 50)
	if err != nil {
		t.Fatalf("List 1: %v", err)
	}
	r2, err := qs.List(context.Background(), QueryFilter{Removed: "exclude"}, "", 50)
	if err != nil {
		t.Fatalf("List 2: %v", err)
	}

	if len(r1.Items) != len(r2.Items) {
		t.Fatalf("page sizes differ: %d vs %d", len(r1.Items), len(r2.Items))
	}
	for i := range r1.Items {
		if r1.Items[i].TaskID != r2.Items[i].TaskID {
			t.Errorf("order differs at position %d: %s vs %s", i, r1.Items[i].TaskID, r2.Items[i].TaskID)
		}
	}
}

func TestQueryInsertionBetweenPages(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	// First page
	r1, err := qs.List(context.Background(), QueryFilter{Removed: "exclude"}, "", 5)
	if err != nil {
		t.Fatalf("List 1: %v", err)
	}
	if !r1.HasMore {
		t.Skip("not enough items for pagination test")
	}

	// Insert a new high-priority task
	insertOracleTask(t, db, "poster", "waiting", map[string]any{
		"media_id":      9999,
		"base_priority": 99999,
	})

	// Second page with same cursor - should continue from where we left off
	r2, err := qs.List(context.Background(), QueryFilter{Removed: "exclude"}, r1.NextCursor, 5)
	if err != nil {
		t.Fatalf("List 2: %v", err)
	}

	// Items in r2 should not overlap with r1
	seen := map[string]bool{}
	for _, item := range r1.Items {
		seen[item.TaskID] = true
	}
	for _, item := range r2.Items {
		if seen[item.TaskID] {
			t.Errorf("duplicate item %s after insertion", item.TaskID)
		}
	}
}

// --- Detail Tests ---

func TestQueryDetailReturnsRow(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "poster", "done", map[string]any{
		"media_id": 777,
	})
	taskID := BuildIdentity("orchestration", id)

	detail, err := qs.Detail(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if detail == nil {
		t.Fatal("expected detail result")
	}
	if detail.Row.TaskID != taskID {
		t.Errorf("task_id = %q, want %q", detail.Row.TaskID, taskID)
	}
	if detail.Row.NormalizedStatus != StatusDone {
		t.Errorf("normalized_status = %q, want %q", detail.Row.NormalizedStatus, StatusDone)
	}
}

func TestQueryDetailMissingIdentity(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	detail, err := qs.Detail(context.Background(), "orchestration:99999")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if detail != nil {
		t.Fatal("expected nil detail for missing identity")
	}
}

func TestQueryDetailIncludesAttempts(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "thumbnail", "running", map[string]any{
		"attempts":     3,
		"max_attempts": 5,
		"last_error":   "timeout",
		"lease_owner":  "worker-1",
	})
	taskID := BuildIdentity("orchestration", id)

	detail, err := qs.Detail(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if detail.Row.Attempt != 3 {
		t.Errorf("attempt = %d, want 3", detail.Row.Attempt)
	}
	if detail.Row.MaxAttempts != 5 {
		t.Errorf("max_attempts = %d, want 5", detail.Row.MaxAttempts)
	}
	if detail.Row.TerminalReason != "timeout" {
		t.Errorf("terminal_reason = %q, want timeout", detail.Row.TerminalReason)
	}
}

// --- Cursor Encode/Decode Tests ---

func TestCursorEncodeDecodeRoundTrip(t *testing.T) {
	cp := CursorPayload{
		Version:    CursorVersion,
		Order:      "claim_order",
		FilterHash: "abc123",
		SnapshotAt: 500,
		ID:         42,
		Priority:   300,
	}
	encoded := EncodeCursor(cp)
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if decoded.Version != cp.Version {
		t.Errorf("version mismatch: %d vs %d", decoded.Version, cp.Version)
	}
	if decoded.FilterHash != cp.FilterHash {
		t.Errorf("filter_hash mismatch: %s vs %s", decoded.FilterHash, cp.FilterHash)
	}
	if decoded.SnapshotAt != cp.SnapshotAt {
		t.Errorf("snapshot_at mismatch: %d vs %d", decoded.SnapshotAt, cp.SnapshotAt)
	}
	if decoded.ID != cp.ID {
		t.Errorf("id mismatch: %d vs %d", decoded.ID, cp.ID)
	}
}

func TestCursorDecodingInvalidBase64(t *testing.T) {
	_, err := DecodeCursor("!!!invalid")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestCursorDecodingInvalidJSON(t *testing.T) {
	encoded := base64Str("this is not json")
	_, err := DecodeCursor(encoded)
	if err == nil {
		t.Error("expected error for invalid JSON in cursor")
	}
}

func base64Str(s string) string {
	return base64Encoder(s)
}

func base64Encoder(s string) string {
	b := []byte(s)
	result := make([]byte, ((len(b)+2)/3)*4)
	encodeBase64(result, b)
	return string(result)
}

func encodeBase64(dst, src []byte) {
	// Simple base64 encoding using standard alphabet
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	di := 0
	for si := 0; si < len(src); si += 3 {
		var val uint32
		n := 0
		for i := 0; i < 3; i++ {
			val <<= 8
			if si+i < len(src) {
				val |= uint32(src[si+i])
				n++
			}
		}
		for i := 0; i < 4; i++ {
			if i*6 < n*8 {
				dst[di] = alphabet[(val>>uint(18-i*6))&0x3F]
			} else {
				dst[di] = '='
			}
			di++
		}
	}
}

func TestCursorFilterHashDeterministic(t *testing.T) {
	f1 := QueryFilter{TaskType: "poster", Status: "waiting", Removed: "exclude"}
	f2 := QueryFilter{TaskType: "poster", Status: "waiting", Removed: "exclude"}
	h1 := f1.filterHash()
	h2 := f2.filterHash()
	if h1 != h2 {
		t.Errorf("filter hashes differ for identical filters: %s vs %s", h1, h2)
	}
}

func TestCursorFilterHashDiffersWhenFilterDiffers(t *testing.T) {
	f1 := QueryFilter{TaskType: "poster", Status: "waiting"}
	f2 := QueryFilter{TaskType: "thumbnail", Status: "waiting"}
	h1 := f1.filterHash()
	h2 := f2.filterHash()
	if h1 == h2 {
		t.Error("filter hashes should differ for different task types")
	}
}

// --- Total Tests ---

func TestQueryTotalIndependent(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	filter := QueryFilter{Removed: "exclude"}
	total, err := qs.Total(context.Background(), filter)
	if err != nil {
		t.Fatalf("Total: %v", err)
	}

	result, err := qs.List(context.Background(), filter, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if total != result.Total {
		t.Errorf("independent Total (%d) != List.Total (%d)", total, result.Total)
	}
}

func TestQueryTotalMatchesAllPages(t *testing.T) {
	db, qs := setupQueryTestDB(t)
	defer db.Close()

	filter := QueryFilter{Removed: "exclude"}
	pageSize := 5
	var allItems []ProjectionRow
	cursor := ""

	for {
		result, err := qs.List(context.Background(), filter, cursor, pageSize)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		allItems = append(allItems, result.Items...)
		if !result.HasMore {
			if int64(len(allItems)) != result.Total {
				t.Errorf("total items collected (%d) != Total (%d)", len(allItems), result.Total)
			}
			break
		}
		cursor = result.NextCursor
		if result.NextCursor == "" {
			break
		}
	}
}

func TestQueryPretranscodeUsesTranscodeSource(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path) VALUES(42,7,'standalone-file','Standalone title','/media/standalone.mp4')`); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,media_id,lease_owner) VALUES('standalone-file','running','pretranscode',NULL,'optimizer')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	qs := NewQueryService(builder)

	total, err := qs.Total(context.Background(), QueryFilter{TaskType: "pretranscode", Status: "running"})
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 1 {
		t.Fatalf("pretranscode total = %d, want 1", total)
	}

	list, err := qs.List(context.Background(), QueryFilter{TaskType: "pretranscode", Status: "running"}, "", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Items))
	}
	item := list.Items[0]
	wantIdentity := BuildIdentity("transcode_task", id)
	if item.TaskID != wantIdentity || item.SourceKind != "transcode_task" {
		t.Errorf("identity = %q source = %q, want %q/transcode_task", item.TaskID, item.SourceKind, wantIdentity)
	}
	if item.TaskType != "pretranscode" || item.NormalizedStatus != StatusRunning {
		t.Errorf("type/status = %q/%q", item.TaskType, item.NormalizedStatus)
	}
	if item.MediaID == nil || *item.MediaID != 42 || item.MediaTitle != "Standalone title" || item.MediaFilePath != "/media/standalone.mp4" || item.LibraryID == nil || *item.LibraryID != 7 {
		t.Errorf("media metadata not resolved: %+v", item)
	}

	detail, err := qs.Detail(context.Background(), wantIdentity)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if detail == nil || detail.Row.TaskID != wantIdentity {
		t.Fatalf("detail identity mismatch: %+v", detail)
	}
}

func TestQueryTranscodeClassificationDisjointWithLegacyMetadata(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	insertTask := func(taskType string) int64 {
		t.Helper()
		result, err := db.Exec(`INSERT INTO transcode_task(status,task_type) VALUES('waiting',?)`, taskType)
		if err != nil {
			t.Fatalf("insert transcode task %q: %v", taskType, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("last insert id: %v", err)
		}
		return id
	}

	explicitID := insertTask(" PreTranscode ")
	legacyID := insertTask("batch")
	regularID := insertTask("batch")
	if _, err := db.Exec(`INSERT INTO pretranscode_task_meta(task_id) VALUES(?)`, legacyID); err != nil {
		t.Fatalf("insert legacy pretranscode metadata: %v", err)
	}

	qs := NewQueryService(builder)
	pretranscode, err := qs.List(context.Background(), QueryFilter{TaskType: "pretranscode"}, "", 10)
	if err != nil {
		t.Fatalf("list pretranscode: %v", err)
	}
	transcode, err := qs.List(context.Background(), QueryFilter{TaskType: "transcode"}, "", 10)
	if err != nil {
		t.Fatalf("list transcode: %v", err)
	}

	if pretranscode.Total != 2 || len(pretranscode.Items) != 2 {
		t.Fatalf("pretranscode total/items = %d/%d, want 2/2", pretranscode.Total, len(pretranscode.Items))
	}
	if transcode.Total != 1 || len(transcode.Items) != 1 {
		t.Fatalf("transcode total/items = %d/%d, want 1/1", transcode.Total, len(transcode.Items))
	}

	pretranscodeIDs := map[int64]bool{}
	for _, item := range pretranscode.Items {
		pretranscodeIDs[item.SourceID] = true
		if item.TaskType != "pretranscode" {
			t.Errorf("pretranscode list projected task %d as %q", item.SourceID, item.TaskType)
		}
	}
	if !pretranscodeIDs[explicitID] || !pretranscodeIDs[legacyID] || pretranscodeIDs[regularID] {
		t.Errorf("pretranscode IDs = %v, want explicit %d and legacy %d only", pretranscodeIDs, explicitID, legacyID)
	}
	for _, item := range transcode.Items {
		if pretranscodeIDs[item.SourceID] {
			t.Errorf("source ID %d appears in both lists", item.SourceID)
		}
		if item.SourceID != regularID {
			t.Errorf("transcode source ID = %d, want %d", item.SourceID, regularID)
		}
		if item.TaskType != "transcode" {
			t.Errorf("transcode list projected task %d as %q", item.SourceID, item.TaskType)
		}
	}
}
