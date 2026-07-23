package postingest

import (
	"context"
	"database/sql"
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
