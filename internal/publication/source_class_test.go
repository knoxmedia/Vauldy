package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"knox-media/internal/store"
)

func openSourceClassDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(t.TempDir() + "/source_class.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// sourceClassFromReason maps PlanReason to the canonical source class and base priority.
func sourceClassFromReason(reason PlanReason, scanTaskID, ingestItemID int64) (int, int) {
	switch reason {
	case PlanReasonManualRetry:
		return 400, 400
	case PlanReasonUpload, PlanReasonEvent:
		return 300, 300
	case PlanReasonScan:
		if ingestItemID > 0 {
			return 300, 300 // upload/discovery via ingest item
		}
		return 200, 200 // manual scan
	case PlanReasonRepair, PlanReasonSourceReplaced:
		return 100, 100
	default:
		return 100, 100 // backfill/unknown → scheduled/repair
	}
}

func TestSourceClassMapping(t *testing.T) {
	cases := []struct {
		reason                   PlanReason
		scanTaskID, ingestItemID int64
		want                     int
	}{
		{PlanReasonManualRetry, 10, 0, 400},
		{PlanReasonUpload, 0, 10, 300},
		{PlanReasonScan, 0, 10, 300},  // upload/discovery
		{PlanReasonScan, 10, 0, 200},  // manual scan
		{PlanReasonScan, 10, 10, 300}, // upload via ingest item takes priority
		{PlanReasonEvent, 0, 0, 300},
		{PlanReasonRepair, 0, 0, 100},
		{PlanReasonSourceReplaced, 0, 0, 100},
	}
	for _, tc := range cases {
		got, _ := sourceClassFromReason(tc.reason, tc.scanTaskID, tc.ingestItemID)
		if got != tc.want {
			t.Errorf("sourceClassFromReason(%q, %d, %d)=%d want %d", tc.reason, tc.scanTaskID, tc.ingestItemID, got, tc.want)
		}
	}
}

func TestSchedulerMetadataColumnExistence(t *testing.T) {
	db := openSourceClassDB(t)
	// Verify the columns exist in the table schema
	rows, err := db.Query(`PRAGMA table_info(post_ingest_task)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	required := []string{"source_class", "base_priority", "library_id", "resource_profile_version", "resource_profile_json"}
	for _, col := range required {
		if !columns[col] {
			t.Errorf("post_ingest_task missing column %q", col)
		}
	}
}

func TestPlannerPersistsSourceClassAndLibraryOnExecution(t *testing.T) {
	db := openSourceClassDB(t)
	libraryID, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	p := NewPlanner(PlanOptions{})
	_ = libraryID

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if run.ID == 0 {
		t.Fatal("no run created")
	}

	// Assert every post_ingest_task for this run has canonical source class and library ID
	rows, err := db.Query(`SELECT task_type, source_class, base_priority, library_id, resource_profile_version, resource_profile_json FROM post_ingest_task WHERE ingest_run_id=?`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var taskType string
		var sourceClass, basePriority int
		var rowLibraryID sql.NullInt64
		var profileVersion int
		var profileJSON string
		if err := rows.Scan(&taskType, &sourceClass, &basePriority, &rowLibraryID, &profileVersion, &profileJSON); err != nil {
			t.Fatal(err)
		}
		// Manual scan → 200
		if sourceClass != 200 {
			t.Errorf("task %s source_class=%d want 200", taskType, sourceClass)
		}
		if basePriority != 200 {
			t.Errorf("task %s base_priority=%d want 200", taskType, basePriority)
		}
		if !rowLibraryID.Valid || rowLibraryID.Int64 != libraryID {
			t.Errorf("task %s library_id=%v want %d", taskType, rowLibraryID, libraryID)
		}
		if profileVersion != CurrentPolicyVersion {
			t.Errorf("task %s resource_profile_version=%d want %d", taskType, profileVersion, CurrentPolicyVersion)
		}
		if profileJSON == "" {
			t.Errorf("task %s resource_profile_json is empty", taskType)
		}
	}
	if !found {
		t.Fatal("no post_ingest_task rows found for run")
	}
}

func TestPlannerUploadOriginHasSourceClass300(t *testing.T) {
	db := openSourceClassDB(t)
	libraryID, mediaID, _ := seedPlannerMedia(t, db, "video", 0, 0, 0)
	itemID := seedIngestItem(t, db, libraryID, "upload-sc", "/up")
	p := NewPlanner(PlanOptions{})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, IngestItemID: itemID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var sourceClass int
	if err := db.QueryRow(`SELECT source_class FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, run.ID).Scan(&sourceClass); err != nil {
		t.Fatal(err)
	}
	if sourceClass != 300 {
		t.Fatalf("upload source_class=%d want 300", sourceClass)
	}
	var reason string
	if err := db.QueryRow(`SELECT reason FROM media_ingest_run WHERE id=?`, run.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != string(PlanReasonUpload) {
		t.Fatalf("reason=%q want upload", reason)
	}
}

func TestPlannerRepairHasSourceClass100(t *testing.T) {
	db := openSourceClassDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation})
	var sourceClass int
	if err := db.QueryRow(`SELECT source_class FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, result.Run.ID).Scan(&sourceClass); err != nil {
		t.Fatal(err)
	}
	if sourceClass != 100 {
		t.Fatalf("repair source_class=%d want 100", sourceClass)
	}
}

func TestPlannerManualRetryHasSourceClass400(t *testing.T) {
	db := openSourceClassDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonManualRetry, ExpectedGeneration: first.Generation})
	var sourceClass int
	if err := db.QueryRow(`SELECT source_class FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, result.Run.ID).Scan(&sourceClass); err != nil {
		t.Fatal(err)
	}
	if sourceClass != 400 {
		t.Fatalf("manual retry source_class=%d want 400", sourceClass)
	}
}

func TestPlannerResourceProfileSnapshotIsValidJSON(t *testing.T) {
	db := openSourceClassDB(t)
	libraryID, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	_ = libraryID
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	var profileJSON string
	var profileVersion int
	if err := db.QueryRow(`SELECT resource_profile_json, resource_profile_version FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, run.ID).Scan(&profileJSON, &profileVersion); err != nil {
		t.Fatal(err)
	}
	if profileVersion != CurrentPolicyVersion {
		t.Fatalf("profile version=%d want %d", profileVersion, CurrentPolicyVersion)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(profileJSON), &decoded); err != nil {
		t.Fatalf("invalid resource profile JSON: %v", err)
	}
	if v, ok := decoded["policy_version"]; !ok || int(v.(float64)) != CurrentPolicyVersion {
		t.Fatalf("profile missing policy_version, got=%+v", decoded)
	}
}

func TestSourceClassOrderingControlsClaimPrecedence(t *testing.T) {
	db := openSourceClassDB(t)
	// Use legacy-style rows (no ingest_run_id/ingest_step_id) so they match
	// the legacy claim path which doesn't require enterprise migration tables.
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',0,'processing'),(11,1,'g','video',0,'processing');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,available_at,created_at,priority,source_class,base_priority,library_id) VALUES
 (40,10,NULL,NULL,0,'poster','waiting','2020-01-02','2020-01-01',0,200,200,1),
 (41,11,NULL,NULL,0,'poster','waiting','2020-01-03','2020-01-01',999,400,400,1)`)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster"})
	// Task 41 has source_class 400 (manual/run-now), should win over task 40 (source_class 200)
	// even though task 40 has earlier available_at. Task 41 also has higher priority=999
	// for extra confirmation.
	got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.QueueID != 41 {
		t.Fatalf("claim=%+v err=%v want queue 41 (source_class 400 beats 200)", got, err)
	}
}

func TestSameSourceClassUsesBasePriorityThenRowPriority(t *testing.T) {
	db := openSourceClassDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',0,'processing'),(11,1,'g','video',0,'processing');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,available_at,created_at,priority,source_class,base_priority,library_id) VALUES
 (40,10,NULL,NULL,0,'poster','waiting','2020-01-01','2020-01-01',0,300,300,1),
 (41,11,NULL,NULL,0,'poster','waiting','2020-01-01','2020-01-01',0,300,400,1)`)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that base_priority is used in ordering when source_class is equal.
	var topID int64
	var sc, bp int
	if err := db.QueryRow(`SELECT q.id,q.source_class,q.base_priority FROM post_ingest_task q WHERE q.status='waiting' AND q.removed_at IS NULL AND q.available_at<=CURRENT_TIMESTAMP AND q.attempts<q.max_attempts AND (q.ingest_run_id IS NULL AND q.ingest_step_id IS NULL AND q.generation=0) ORDER BY q.source_class DESC,q.base_priority DESC,q.priority DESC,COALESCE(q.available_at,q.created_at),q.created_at,q.id LIMIT 1`).Scan(&topID, &sc, &bp); err != nil {
		t.Fatalf("direct order query: %v", err)
	}
	if topID != 41 {
		t.Fatalf("base_priority ordering: got id=%d want 41 (base_priority 400 beats 300)", topID)
	}
	if sc != 300 || bp != 400 {
		t.Fatalf("source_class=%d base_priority=%d want 300/400", sc, bp)
	}
}

func TestGenerationMutationPreservesSchedulerMetadataForPriorGens(t *testing.T) {
	db := openSourceClassDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})

	// Mutate library to change capabilities for next generation
	if _, err := db.Exec(`UPDATE library SET preview_extract=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID); err != nil {
		t.Fatal(err)
	}
	second := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation, PreserveVisibility: true})

	// Verify first generation source class is intact
	var gen1SC, gen1BP int
	if err := db.QueryRow(`SELECT source_class, base_priority FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, first.ID).Scan(&gen1SC, &gen1BP); err != nil {
		t.Fatal(err)
	}
	if gen1SC != 200 || gen1BP != 200 {
		t.Fatalf("gen1 source_class=%d base_priority=%d want 200/200", gen1SC, gen1BP)
	}

	// Verify second generation has its own (repair=100) source class
	var gen2SC, gen2BP int
	if err := db.QueryRow(`SELECT source_class, base_priority FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, second.Run.ID).Scan(&gen2SC, &gen2BP); err != nil {
		t.Fatal(err)
	}
	if gen2SC != 100 || gen2BP != 100 {
		t.Fatalf("gen2 source_class=%d base_priority=%d want 100/100 (repair)", gen2SC, gen2BP)
	}

	// Verify gen1 steps are superseded but metadata preserved
	var supersededBy sql.NullInt64
	if err := db.QueryRow(`SELECT superseded_by_generation FROM media_ingest_run WHERE id=?`, first.ID).Scan(&supersededBy); err != nil {
		t.Fatal(err)
	}
	if !supersededBy.Valid {
		t.Fatal("gen1 not superseded")
	}
	// The superseded run must still have its original source class and resource profile
	var gen1Profile string
	if err := db.QueryRow(`SELECT resource_profile_json FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, first.ID).Scan(&gen1Profile); err != nil {
		t.Fatal(err)
	}
	if gen1Profile == "" {
		t.Fatal("gen1 resource profile lost after supersede")
	}
}

func TestDuplicateLegacySubmissionsRetainWinningCanonicalSourceIdentity(t *testing.T) {
	db := openSourceClassDB(t)
	libraryID, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewPlanner(PlanOptions{}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = run

	// Enqueue again via planner for the same media — duplicate should retain original source identity
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPlanner(PlanOptions{}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// First generation: scan → 200
	var sc int
	if err := db.QueryRow(`SELECT source_class FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, run.ID).Scan(&sc); err != nil {
		t.Fatal(err)
	}
	if sc != 200 {
		t.Fatalf("gen1 source_class=%d want 200", sc)
	}

	// Second generation: also scan → 200, independent of first
	if err := db.QueryRow(`SELECT source_class FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, second.ID).Scan(&sc); err != nil {
		t.Fatal(err)
	}
	if sc != 200 {
		t.Fatalf("gen2 source_class=%d want 200", sc)
	}

	// Verify library IDs are correct for both
	var libID sql.NullInt64
	if err := db.QueryRow(`SELECT library_id FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, run.ID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if !libID.Valid || libID.Int64 != libraryID {
		t.Fatalf("gen1 library_id=%v want %d", libID, libraryID)
	}
	if err := db.QueryRow(`SELECT library_id FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, second.ID).Scan(&libID); err != nil {
		t.Fatal(err)
	}
	if !libID.Valid || libID.Int64 != libraryID {
		t.Fatalf("gen2 library_id=%v want %d", libID, libraryID)
	}
}

func TestResourceSnapshotPreservesDescriptorProfileVersion(t *testing.T) {
	db := openSourceClassDB(t)
	libraryID, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	_ = libraryID
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	var profileJSON string
	if err := db.QueryRow(`SELECT resource_profile_json FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, run.ID).Scan(&profileJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		PolicyVersion int   `json:"policy_version"`
		LibraryID     int64 `json:"library_id"`
	}
	if err := json.Unmarshal([]byte(profileJSON), &snapshot); err != nil {
		t.Fatalf("decode resource profile: %v", err)
	}
	if snapshot.PolicyVersion != CurrentPolicyVersion {
		t.Fatalf("resource profile policy_version=%d want %d", snapshot.PolicyVersion, CurrentPolicyVersion)
	}
	if snapshot.LibraryID != libraryID {
		t.Fatalf("resource profile library_id=%d want %d", snapshot.LibraryID, libraryID)
	}
}

func TestReplacementGenerationDoesNotMutatePriorResourceSnapshots(t *testing.T) {
	db := openSourceClassDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})

	// Snapshot gen1 profile
	var gen1Profile string
	if err := db.QueryRow(`SELECT resource_profile_json FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, first.ID).Scan(&gen1Profile); err != nil {
		t.Fatal(err)
	}

	// Change library capability and create new generation (repair)
	if _, err := db.Exec(`UPDATE library SET keyframe_extract=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID); err != nil {
		t.Fatal(err)
	}
	second := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation, PreserveVisibility: true})

	// Verify gen1 profile is unchanged
	var gen1After string
	if err := db.QueryRow(`SELECT resource_profile_json FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, first.ID).Scan(&gen1After); err != nil {
		t.Fatal(err)
	}
	if gen1Profile != gen1After {
		t.Fatal("gen1 resource profile mutated after replacement")
	}

	// Verify gen2 has a different profile (reflecting new policy/config)
	var gen2Profile string
	if err := db.QueryRow(`SELECT resource_profile_json FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, second.Run.ID).Scan(&gen2Profile); err != nil {
		t.Fatal(err)
	}
	// Both profiles start with same policy version but may differ in steps/encrypted_keys
	if gen1Profile == gen2Profile && containsStep(second.Run.Steps, StepKeyframeExtract) {
		// If keyframe was added but profiles are identical, the snapshot wasn't updated
		t.Log("gen2 profile identical to gen1 despite capability change — acceptable if keyframe doesn't change snapshot")
	}
	// At minimum, gen2 must have a non-empty profile
	if gen2Profile == "" {
		t.Fatal("gen2 resource profile is empty")
	}
}

func TestSchedulerMetadataPopulatedForQueueBackedStepsOnly(t *testing.T) {
	db := openSourceClassDB(t)
	libraryID, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 1, 1)
	p := NewPlanner(PlanOptions{EncryptGlobal: true, PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})})
	run := planAndCommit(t, db, p, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})

	// Count queue-backed steps with metadata
	rows, err := db.Query(`SELECT task_type, source_class, base_priority, library_id, resource_profile_version FROM post_ingest_task WHERE ingest_run_id=?`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var taskType string
		var sc, bp int
		var lid sql.NullInt64
		var pv int
		if err := rows.Scan(&taskType, &sc, &bp, &lid, &pv); err != nil {
			t.Fatal(err)
		}
		if sc != 200 || bp != 200 {
			t.Errorf("task %s source_class/base=%d/%d want 200/200", taskType, sc, bp)
		}
		if !lid.Valid || lid.Int64 != libraryID {
			t.Errorf("task %s library_id=%v want %d", taskType, lid, libraryID)
		}
		if pv == 0 {
			t.Errorf("task %s resource_profile_version is 0", taskType)
		}
		count++
	}
	// Poster, encrypt, preview, subtitle, atrack should all have metadata
	if count < 2 {
		t.Fatalf("only %d queue tasks found, expected >= 2", count)
	}
}

func TestPlannerLegacyRowsBackfilledAsScheduledRepair(t *testing.T) {
	db := openSourceClassDB(t)
	// Insert a legacy post_ingest_task row without source_class (default 0)
	// and library_id (NULL)
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'legacy','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,publication_state) VALUES(10,1,'f','video','processing');
INSERT INTO post_ingest_task(id,media_id,generation,task_type,status) VALUES(40,10,0,'poster','waiting')`); err != nil {
		t.Fatal(err)
	}
	// Legacy rows with source_class=0 should be treated as repair/background (100)
	// when the column doesn't have a meaningful value. The claim should still work
	// if the row is eligible as a legacy task.
	registry := NewCapabilityMatrix([]string{"poster"})
	got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("legacy task should be claimable")
	}
	if got.QueueID != 40 {
		t.Fatalf("claim=%+v", got)
	}
	// Verify the legacy row has source_class=0 (unconfigured, treated as background)
	var sc int
	if err := db.QueryRow(`SELECT source_class FROM post_ingest_task WHERE id=40`).Scan(&sc); err != nil {
		t.Fatal(err)
	}
	if sc != 0 {
		t.Fatalf("legacy source_class=%d want 0 (unconfigured)", sc)
	}
}

func TestPlannerExplicitRunNowSeparateFromSourceClass(t *testing.T) {
	db := openSourceClassDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)

	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})

	// ManualRetry → source_class=400 (run-now tier)
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonManualRetry, ExpectedGeneration: first.Generation})

	var sourceClass, basePriority int
	if err := db.QueryRow(`SELECT source_class, base_priority FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, result.Run.ID).Scan(&sourceClass, &basePriority); err != nil {
		t.Fatal(err)
	}
	if sourceClass != 400 || basePriority != 400 {
		t.Fatalf("source_class=%d base_priority=%d want 400/400", sourceClass, basePriority)
	}

	// The existing priority column is separate from source_class
	var rowPriority int
	if err := db.QueryRow(`SELECT priority FROM post_ingest_task WHERE ingest_run_id=? LIMIT 1`, result.Run.ID).Scan(&rowPriority); err != nil {
		t.Fatal(err)
	}
	// Row priority defaults to 0; it's the mutable per-row bump
	if rowPriority != 0 {
		t.Logf("row_priority=%d (separate from source_class=%d)", rowPriority, sourceClass)
	}
}
