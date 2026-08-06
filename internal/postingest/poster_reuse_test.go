package postingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/storage"
)

func seedEncryptedReuseFixture(t *testing.T, sourceName string) (*sql.DB, string, Task) {
	t.Helper()
	db, upload, mediaID, _ := seedPosterTest(t, `{}`, "video")
	source := filepath.Join(t.TempDir(), sourceName)
	if err := os.WriteFile(source, []byte("source-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=?,ingest_generation=1,publication_state='processing' WHERE id=?`, source, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,'w','i',?,'encrypted')`, mediaID, source, filepath.Join(t.TempDir(), "missing-plain.mp4")); err != nil {
		t.Fatal(err)
	}
	posterEnc := filepath.Join(t.TempDir(), "poster.jpg.abc.enc")
	if err := os.WriteFile(posterEnc, []byte("existing-poster"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg',?,'w','i')`, mediaID, posterEnc); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"scrape":{"poster":%q,"extra":{"poster":%q}}}`, storage.DerivedPosterAPIPath(mediaID), storage.DerivedPosterAPIPath(mediaID))
	if _, err := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, meta, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(8801,?,1,'manual_retry','processing','{}',2)`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,lease_owner) VALUES(8802,8801,?,1,'poster',1,'running',1,'poster-reuse-owner')`, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,lease_owner) VALUES(?,8801,8802,1,'poster','running',1,'poster-reuse-owner')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	run, step := int64(8801), int64(8802)
	return db, upload, Task{ID: id, MediaID: mediaID, RunID: &run, StepID: &step, Generation: 1, Type: TaskPoster, Status: StatusRunning, Attempts: 1, RetryRound: 0, LeaseOwner: "poster-reuse-owner"}
}

func TestPosterReuseExistingDerivedForEncryptedSource(t *testing.T) {
	db, upload, task := seedEncryptedReuseFixture(t, "movie.enc")
	runner := &stagedPosterFake{}
	result, err := NewPosterAdapter(db, upload, nil, runner).ExecuteWithResult(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("completion=%v", result.Completion)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls=%d, want 0 (reused existing poster)", runner.calls)
	}
	var qs, ss string
	if err := db.QueryRow(`SELECT p.status,s.status FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id WHERE p.id=?`, task.ID).Scan(&qs, &ss); err != nil {
		t.Fatal(err)
	}
	if qs != "done" || ss != "done" {
		t.Fatalf("queue=%s step=%s", qs, ss)
	}
	var kind, reason, refs, stageID string
	if err := db.QueryRow(`SELECT kind,reason,artifact_refs_json,stage_id FROM media_ingest_evidence WHERE step_id=?`, *task.StepID).Scan(&kind, &reason, &refs, &stageID); err != nil {
		t.Fatal(err)
	}
	if kind != "poster" || reason != "reused" {
		t.Fatalf("kind=%s reason=%s", kind, reason)
	}
	if !strings.Contains(refs, "poster.jpg.abc.enc") {
		t.Fatalf("refs=%s", refs)
	}
	var journalState string
	if err := db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, stageID).Scan(&journalState); err != nil {
		t.Fatal(err)
	}
	if journalState != "committed" {
		t.Fatalf("journal state=%s", journalState)
	}
}

func TestPosterReuseNotTriggeredForPlaintextSource(t *testing.T) {
	db, upload, task := seedEncryptedReuseFixture(t, "movie.mp4")
	runner := &stagedPosterFake{}
	_, err := NewPosterAdapter(db, upload, nil, runner).ExecuteWithResult(context.Background(), task)
	// The runner path is exercised (reuse did not short-circuit); the exact
	// commit outcome is not the point of this test.
	if runner.calls == 0 {
		t.Fatal("runner was not called for a plaintext source; reuse short-circuited")
	}
	if err == nil {
		// A full success would only happen with a properly staged fake; reaching
		// the runner is the assertion here.
		return
	}
}

func TestPosterReuseSkipsWhenNoDurablePoster(t *testing.T) {
	db, upload, task := seedEncryptedReuseFixture(t, "movie.enc")
	if _, err := db.Exec(`DELETE FROM media_derived_assets WHERE media_id=?`, task.MediaID); err != nil {
		t.Fatal(err)
	}
	runner := &stagedPosterFake{}
	_, err := NewPosterAdapter(db, upload, nil, runner).ExecuteWithResult(context.Background(), task)
	if runner.calls == 0 {
		t.Fatal("runner was not called even though no durable poster exists")
	}
	if err == nil {
		return
	}
}
