package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReconcileThumbnailStagesRetainsActiveAndCleansStaleUnreferenced(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	worker := realThumbnailStager(t, db)
	active, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	staleDir := filepath.Join(t.TempDir(), "stale")
	_ = os.MkdirAll(staleDir, 0o755)
	stalePath := filepath.Join(staleDir, "thumb.jpg")
	_ = os.WriteFile(stalePath, []byte("stale"), 0o644)
	staleID := "stale-thumbnail-stage"
	hashes := `{"thumb":{"path":"` + filepath.ToSlash(stalePath) + `"},"medium":{"path":"` + filepath.ToSlash(stalePath) + `"}}`
	_, err = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,'old-owner','old-fp','thumbnail','staged','',?,?)`, staleID, task.MediaID, *task.RunID, *task.StepID, task.Generation, staleDir, hashes)
	if err != nil {
		t.Fatal(err)
	}
	checked, cleaned, err := ReconcileThumbnailStages(context.Background(), db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 2 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d", checked, cleaned)
	}
	if _, err = os.Stat(active.Thumb.Path); err != nil {
		t.Fatalf("active removed: %v", err)
	}
	if _, err = os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale retained: %v", err)
	}
	var recovery string
	_ = db.QueryRow(`SELECT recovery_error FROM media_asset_stage_journal WHERE stage_id=?`, staleID).Scan(&recovery)
	if recovery != "cleaned_unreferenced" {
		t.Fatalf("recovery=%q", recovery)
	}
}

func TestReconcileThumbnailStagesQuarantinesBeforeCleanupAndBlocksCommit(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	staged, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE media_asset_stage_journal SET owner_token='stale-owner' WHERE stage_id=?`, staged.Stage.StageID)
	if err != nil {
		t.Fatal(err)
	}
	checked, cleaned, err := ReconcileThumbnailStages(context.Background(), db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d", checked, cleaned)
	}
	var state string
	_ = db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, staged.Stage.StageID).Scan(&state)
	if state != "quarantined" {
		t.Fatalf("state=%q", state)
	}
	if err = commitStagedThumbnail(context.Background(), db, *task, staged); err == nil {
		t.Fatal("quarantined stage was adopted")
	}
}
func TestReconcileThumbnailStagesRetainsPlainMetadataReferenceWithoutEvidence(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	staged, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"photo": map[string]any{"thumb_path": staged.Thumb.Path, "medium_path": staged.Medium.Path}})
	if _, err = db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, string(meta), task.MediaID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_asset_stage_journal SET owner_token='stale-owner' WHERE stage_id=?`, staged.Stage.StageID); err != nil {
		t.Fatal(err)
	}
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 0 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	if _, err = os.Stat(staged.Thumb.Path); err != nil {
		t.Fatalf("metadata reference removed: %v", err)
	}
}

func TestReconcileThumbnailStagesQuarantineInterleavingRejectsWorker(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	staged, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_asset_stage_journal SET owner_token='stale-owner' WHERE stage_id=?`, staged.Stage.StageID); err != nil {
		t.Fatal(err)
	}
	original := afterThumbnailStageQuarantined
	var commitErr error
	afterThumbnailStageQuarantined = func() { commitErr = commitStagedThumbnail(context.Background(), db, *task, staged) }
	t.Cleanup(func() { afterThumbnailStageQuarantined = original })
	_, _, err = ReconcileThumbnailStages(context.Background(), db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if commitErr == nil {
		t.Fatal("worker committed after quarantine")
	}
}
func TestReconcileThumbnailStagesCorruptCommittedReportsAndRetains(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	staged, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if err = commitStagedThumbnail(context.Background(), db, *task, staged); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media SET meta_json='{}' WHERE id=?`, task.MediaID); err != nil {
		t.Fatal(err)
	}
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, 10)
	if err == nil {
		t.Fatal("corrupt committed accepted")
	}
	if cleaned != 0 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	if _, err = os.Stat(staged.Thumb.Path); err != nil {
		t.Fatalf("corrupt committed path removed: %v", err)
	}
}

func TestReconcileThumbnailStagesRetainsCommittedAndReferenced(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	worker := realThumbnailStager(t, db)
	staged, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if err = commitStagedThumbnail(context.Background(), db, *task, staged); err != nil {
		t.Fatal(err)
	}
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, 10)
	if err != nil || cleaned != 0 {
		t.Fatalf("cleaned=%d err=%v", cleaned, err)
	}
	for _, path := range []string{staged.Thumb.Path, staged.Medium.Path} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("referenced removed %s: %v", path, err)
		}
	}
}

func TestReconcileThumbnailStagesQueryFailurePreservesFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stage")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	db, err := sql.Open("sqlite", "file:reconcile-query-error?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err = ReconcileThumbnailStages(context.Background(), db, 10); err == nil {
		t.Fatal("expected query error")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("file removed on query failure: %v", err)
	}
}
