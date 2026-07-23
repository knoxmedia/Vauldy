package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/imagethumb"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

func TestThumbnailAtomicCommitSelectsBothPointersEvidenceJournalAndCompletion(t *testing.T) {
	db, mediaID, runID := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	if task.RunID == nil || task.StepID == nil || *task.RunID != runID || task.Generation <= 0 {
		t.Fatalf("identity=%+v", task)
	}
	worker := realThumbnailStager(t, db)
	result, err := NewThumbnailAdapter(db, worker).(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	}).ExecuteWithResult(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("result=%+v", result)
	}
	var taskStatus, stepStatus, owner, state string
	var evidence int
	var refsRaw, stageID string
	if err = db.QueryRow(`SELECT p.status,s.status,COALESCE(p.lease_owner,''),j.state,e.id,e.artifact_refs_json,e.stage_id FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_evidence e ON e.step_id=s.id AND e.kind='thumbnail' JOIN media_asset_stage_journal j ON j.stage_id=e.stage_id WHERE p.id=?`, task.ID).Scan(&taskStatus, &stepStatus, &owner, &state, &evidence, &refsRaw, &stageID); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "done" || stepStatus != "done" || owner != "" || state != "committed" || evidence <= 0 || stageID == "" {
		t.Fatalf("rows=%s/%s/%q/%s/%d/%q", taskStatus, stepStatus, owner, state, evidence, stageID)
	}
	var refs map[string]any
	if err = json.Unmarshal([]byte(refsRaw), &refs); err != nil {
		t.Fatal(err)
	}
	variants, _ := refs["variants"].([]any)
	if len(variants) != 2 {
		t.Fatalf("refs=%s", refsRaw)
	}
	var metaRaw string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&metaRaw)
	if !strings.Contains(metaRaw, stageID) {
		t.Fatalf("metadata does not select immutable stage: %s", metaRaw)
	}
}

func TestThumbnailAtomicCommitEncryptedSelectsBothDerivedPointersAndMetadata(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, true)
	vault, err := keystore.NewVault("thumbnail-atomic-key", "")
	if err != nil {
		t.Fatal(err)
	}
	derived := &storage.DerivedAssetStore{DB: db, Vault: vault, BaseDir: t.TempDir()}
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	worker := realThumbnailStager(t, db)
	worker.Vault, worker.Derived = vault, derived
	result, err := NewThumbnailAdapter(db, worker).(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	}).ExecuteWithResult(context.Background(), *task)
	if err != nil || result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	rows, err := db.Query(`SELECT artifact_kind,logical_name,enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind IN ('photo_thumb','photo_medium') ORDER BY artifact_kind`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var kind, name, path string
		if err = rows.Scan(&kind, &name, &path); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(path, ".enc") {
			t.Fatalf("%s/%s path=%s", kind, name, path)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("derived pointers=%d", count)
	}
	var raw string
	if err = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Count(raw, ".enc") < 2 {
		t.Fatalf("metadata=%s", raw)
	}
}

func TestVerifyCommittedThumbnailRejectsJournalMetadataAndPointerCorruption(t *testing.T) {
	mutations := []struct{ name, sql string }{
		{"journal owner", `UPDATE media_asset_stage_journal SET owner_token='wrong' WHERE stage_id=?`},
		{"evidence linkage", `UPDATE media_ingest_evidence SET source_fingerprint='wrong' WHERE stage_id=?`},
		{"plain metadata", `UPDATE media SET meta_json='{}' WHERE id=(SELECT media_id FROM media_ingest_evidence WHERE stage_id=?)`},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
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
			if _, err = db.Exec(tc.sql, staged.Stage.StageID); err != nil {
				t.Fatal(err)
			}
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err = verifyCommittedThumbnailTx(context.Background(), conn, *task, staged); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}

func TestThumbnailAtomicCommitRollbackSelectsNeitherVariant(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	if _, err := db.Exec(`CREATE TRIGGER reject_thumbnail_evidence BEFORE INSERT ON media_ingest_evidence WHEN NEW.kind='thumbnail' BEGIN SELECT RAISE(FAIL,'reject evidence'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := NewThumbnailAdapter(db, realThumbnailStager(t, db)).(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	}).ExecuteWithResult(context.Background(), *task)
	if err == nil {
		t.Fatal("expected commit failure")
	}
	var raw, status, step string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw)
	_ = db.QueryRow(`SELECT p.status,s.status FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id WHERE p.id=?`, task.ID).Scan(&status, &step)
	if raw != "{}" || status != "running" || step != "running" {
		t.Fatalf("rollback meta=%s task=%s step=%s", raw, status, step)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_evidence WHERE step_id=?`, *task.StepID).Scan(&n)
	if n != 0 {
		t.Fatalf("evidence=%d", n)
	}
}

func TestThumbnailAtomicCommitLostResponseReconcilesSameStageAndRejectsConflict(t *testing.T) {
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
	if err = commitStagedThumbnail(context.Background(), db, *task, staged); err != nil {
		t.Fatalf("same stage retry: %v", err)
	}
	conflict := staged
	conflict.Stage.StageID = "conflict-stage"
	if err = commitStagedThumbnail(context.Background(), db, *task, conflict); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestThumbnailAtomicCommitRejectsStaleSourceFingerprintAndExactAttempt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *sql.DB, *Task)
	}{
		{"source", func(t *testing.T, db *sql.DB, task *Task) {
			if err := os.WriteFile(taskSource(t, db, task.MediaID), []byte("changed"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"attempt", func(t *testing.T, db *sql.DB, task *Task) {
			_, _ = db.Exec(`UPDATE post_ingest_task SET attempts=attempts+1 WHERE id=?`, task.ID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mediaID, _ := planThumbnailFixture(t, false)
			q := NewQueue(db, "thumbnail-owner", nil)
			task, _ := q.Claim(context.Background(), TaskThumbnail)
			worker := realThumbnailStager(t, db)
			staged, err := worker.Stage(context.Background(), *task)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, db, task)
			err = commitStagedThumbnail(context.Background(), db, *task, staged)
			if err == nil {
				t.Fatal("expected stale rejection")
			}
			var raw string
			_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw)
			if raw != "{}" {
				t.Fatalf("mutated=%s", raw)
			}
		})
	}
}

func realThumbnailStager(t *testing.T, db *sql.DB) *LocalThumbnailWorker {
	t.Helper()
	source := taskSource(t, db, 1)
	_ = source
	return &LocalThumbnailWorker{DB: db, FFmpegPath: writeStageFFmpegPostingest(t, db), PreviewDir: t.TempDir()}
}
func taskSource(t *testing.T, db *sql.DB, mediaID int64) string {
	t.Helper()
	var p string
	if err := db.QueryRow(`SELECT file_path FROM media WHERE id=?`, mediaID).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}
func writeStageFFmpegPostingest(t *testing.T, db *sql.DB) string {
	t.Helper()
	var source string
	if err := db.QueryRow(`SELECT file_path FROM media ORDER BY id DESC LIMIT 1`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ffmpeg.bat")
	script := "@echo off\r\nset last=\r\n:loop\r\nif \"%~1\"==\"\" goto done\r\nset last=%~1\r\nshift\r\ngoto loop\r\n:done\r\ncopy /Y \"" + source + "\" \"%last%\" >nul\r\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var _ = errors.New
var _ = imagethumb.StagedThumbnail{}
