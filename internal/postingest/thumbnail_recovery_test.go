package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

func TestEncryptedThumbnailRecoveryUsesDerivedRoot(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, true)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	vault, _ := keystore.NewVault("recovery-derived-key", "")
	preview := t.TempDir()
	derivedRoot := t.TempDir()
	worker := realThumbnailStager(t, db)
	worker.PreviewDir = preview
	worker.Vault = vault
	worker.Derived = &storage.DerivedAssetStore{DB: db, Vault: vault, BaseDir: derivedRoot}
	staged, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_asset_stage_journal SET owner_token='stale' WHERE stage_id=?`, staged.Stage.StageID); err != nil {
		t.Fatal(err)
	}
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: filepath.Join(preview, "photos"), Derived: derivedRoot}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	for _, p := range []string{staged.Thumb.Path, staged.Medium.Path} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("encrypted stale retained %s: %v", p, err)
		}
	}
}
func TestEncryptedThumbnailRecoveryRejectsExternalDerivedPath(t *testing.T) {
	if err := validateDerivedThumbnailPath(t.TempDir(), 1, "photo_thumb", "thumb.jpg", filepath.Join(t.TempDir(), "1", "photo_thumb", "thumb.jpg.deadbeef.enc")); err == nil {
		t.Fatal("accepted external derived path")
	}
}

func TestThumbnailManagedPathsRequireConfiguredRootAndGeneration(t *testing.T) {
	trusted := t.TempDir()
	outside := t.TempDir()
	stage := "stage-id"
	outsidePath := filepath.Join(outside, "generation-4", stage, "thumb.jpg")
	if err := os.MkdirAll(filepath.Dir(outsidePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsidePath, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateThumbnailManagedPath(trusted, 4, stage, "thumb.jpg", outsidePath); err == nil {
		t.Fatal("accepted matching external layout")
	}
	wrong := filepath.Join(trusted, "generation-5", stage, "thumb.jpg")
	if err := os.MkdirAll(filepath.Dir(wrong), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateThumbnailManagedPath(trusted, 4, stage, "thumb.jpg", wrong); err == nil {
		t.Fatal("accepted wrong generation")
	}
}
func TestEncryptedJournalMetadataOmitsSecrets(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, true)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	vault, _ := keystore.NewVault("journal-secret-test", "")
	worker := realThumbnailStager(t, db)
	worker.Vault = vault
	worker.Derived = &storage.DerivedAssetStore{DB: db, Vault: vault, BaseDir: t.TempDir()}
	staged, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	raw := staged.Stage.HashesSizesJSON
	if strings.Contains(raw, "wrapped_dek") || strings.Contains(raw, `"iv"`) {
		t.Fatalf("journal leaked encryption metadata: %s", raw)
	}
}

func TestThumbnailManagedPathsRejectExternalTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, filepath.Join(root, "generation-1", "stage", "..", "..", "sentinel")} {
		if err := validateThumbnailManagedPath(root, 1, "stage", "thumb.jpg", path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	link := filepath.Join(root, "generation-1", "stage")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), link); err == nil {
		if err = validateThumbnailManagedPath(root, 1, "stage", "thumb.jpg", filepath.Join(link, "thumb.jpg")); err == nil {
			t.Fatal("accepted symlink escape")
		}
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep" {
		t.Fatal("external sentinel changed")
	}
}
func TestReconcileThumbnailStagesMarksCommittedAndReachesStaleAfterHundred(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	for i := 0; i < 150; i++ {
		id := fmt.Sprintf("verified-%03d", i)
		_, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json,recovery_error) VALUES(?,1,1,1,1,'o','f','thumbnail','committed','','','{}','verified_committed')`, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	staged, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_asset_stage_journal SET owner_token='stale' WHERE stage_id=?`, staged.Stage.StageID); err != nil {
		t.Fatal(err)
	}
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: filepath.Dir(filepath.Dir(staged.Stage.StagedPath))}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned=%d", cleaned)
	}
}

func TestReconcileThumbnailStagesActiveRowsDoNotConsumeRecoveryLimit(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	active, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		_, err = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json,updated_at) SELECT ?,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json,datetime(updated_at,?) FROM media_asset_stage_journal WHERE stage_id=?`, fmt.Sprintf("active-copy-%02d", i), fmt.Sprintf("-%d seconds", 100+i), active.Stage.StageID)
		if err != nil {
			t.Fatal(err)
		}
	}
	staleRoot := t.TempDir()
	staleID := "stale-behind-active"
	staleDir := filepath.Join(staleRoot, "generation-1", staleID)
	if err = os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	thumb, medium := filepath.Join(staleDir, "thumb.jpg"), filepath.Join(staleDir, "medium.jpg")
	if err = os.WriteFile(thumb, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(medium, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	hashes := `{"thumb":{"path":"` + filepath.ToSlash(thumb) + `"},"medium":{"path":"` + filepath.ToSlash(medium) + `"}}`
	_, err = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json,updated_at) VALUES(?,?,?,?,?,'stale-owner','stale-fp','thumbnail','staged','',?,?,CURRENT_TIMESTAMP)`, staleID, task.MediaID, *task.RunID, *task.StepID, task.Generation, staleDir, hashes)
	if err != nil {
		t.Fatal(err)
	}
	checked, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: staleRoot}, 10)
	if err != nil || checked != 1 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, err)
	}
	if _, err = os.Stat(thumb); !os.IsNotExist(err) {
		t.Fatalf("stale retained: %v", err)
	}
	if _, err = os.Stat(active.Thumb.Path); err != nil {
		t.Fatalf("active removed: %v", err)
	}
}

func TestReconcileThumbnailStagesRetainsActiveAndCleansStaleUnreferenced(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	worker := realThumbnailStager(t, db)
	active, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	staleRoot := t.TempDir()
	staleID := "stale-thumbnail-stage"
	staleDir := filepath.Join(staleRoot, "generation-1", staleID)
	_ = os.MkdirAll(staleDir, 0o755)
	stalePath := filepath.Join(staleDir, "thumb.jpg")
	_ = os.WriteFile(stalePath, []byte("stale"), 0o644)
	mediumPath := filepath.Join(staleDir, "medium.jpg")
	_ = os.WriteFile(mediumPath, []byte("stale"), 0o644)
	hashes := `{"thumb":{"path":"` + filepath.ToSlash(stalePath) + `"},"medium":{"path":"` + filepath.ToSlash(mediumPath) + `"}}`
	_, err = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,'old-owner','old-fp','thumbnail','staged','',?,?)`, staleID, task.MediaID, *task.RunID, *task.StepID, task.Generation, staleDir, hashes)
	if err != nil {
		t.Fatal(err)
	}
	checked, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: staleRoot}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 || cleaned != 1 {
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
	checked, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: thumbnailTestRoot(t, db)}, 10)
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
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: thumbnailTestRoot(t, db)}, 10)
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
	_, _, err = ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: thumbnailTestRoot(t, db)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if commitErr == nil {
		t.Fatal("worker committed after quarantine")
	}
}

func TestReconcileThumbnailStagesRetriesQuarantinedCleanup(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	staged, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_asset_stage_journal SET state='quarantined',recovery_error='cleanup_failed' WHERE stage_id=?`, staged.Stage.StageID); err != nil {
		t.Fatal(err)
	}
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: thumbnailTestRoot(t, db)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	if _, err = os.Stat(staged.Thumb.Path); !os.IsNotExist(err) {
		t.Fatalf("quarantined cleanup not retried: %v", err)
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
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: thumbnailTestRoot(t, db)}, 10)
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
	_, cleaned, err := ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: thumbnailTestRoot(t, db)}, 10)
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
	if _, _, err = ReconcileThumbnailStages(context.Background(), db, ThumbnailRecoveryRoots{Preview: thumbnailTestRoot(t, db)}, 10); err == nil {
		t.Fatal("expected query error")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("file removed on query failure: %v", err)
	}
}

func thumbnailTestRoot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var p string
	if err := db.QueryRow(`SELECT staged_path FROM media_asset_stage_journal WHERE artifact_kind='thumbnail' ORDER BY created_at DESC LIMIT 1`).Scan(&p); err != nil {
		return t.TempDir()
	}
	return filepath.Dir(filepath.Dir(p))
}
