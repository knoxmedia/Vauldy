package postingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/storage"
)

func posterObjectFiles(t *testing.T, upload string) []string {
	t.Helper()
	root := filepath.Join(upload, "posters", "objects", "sha256")
	var files []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func TestPosterCommitPreflightPreventsFilesystemWrites(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, *Task, *StagedPoster)
	}{
		{"invalid identity", func(t *testing.T, _ *sql.DB, _ *Task, s *StagedPoster) { s.Stage.Request.Attempt++ }},
		{"empty staged path", func(t *testing.T, _ *sql.DB, _ *Task, s *StagedPoster) { s.Stage.StagedPath = "" }},
		{"relative staged path", func(t *testing.T, _ *sql.DB, _ *Task, s *StagedPoster) {
			s.Stage.StagedPath = filepath.Join("internal", "postingest", "posters")
		}},
		{"already lost lease", func(t *testing.T, db *sql.DB, task *Task, _ *StagedPoster) {
			if _, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='other' WHERE id=?`, task.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, upload, task := seedCurrentLinkedPosterTask(t)
			stageID := "preflight-" + strings.ReplaceAll(tc.name, " ", "-")
			dir := filepath.Join(upload, "posters", "generation-1", stageID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, posterLogicalName)
			if err := os.WriteFile(path, []byte("candidate"), 0600); err != nil {
				t.Fatal(err)
			}
			size, hash, _ := hashPath(path)
			fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
			req := publication.StageRequest{QueueID: task.ID, MediaID: task.MediaID, RunID: *task.RunID, StepID: *task.StepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, Attempt: task.Attempts, SourceFingerprint: fp}
			staged := StagedPoster{Stage: publication.StageRecord{StageID: stageID, Request: req, Kind: publication.ArtifactPoster, State: "staged", StagedPath: dir}, Path: path, URL: storage.ImmutablePlainPosterURL(task.Generation, stageID), Size: size, Hash: hash}
			if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,'{}')`, stageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, dir); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, db, &task, &staged)
			if err := commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err == nil {
				t.Fatal("expected rejection")
			}
			if got := posterObjectFiles(t, upload); len(got) != 0 {
				t.Fatalf("filesystem objects=%v", got)
			}
		})
	}
}

func TestPosterCommitLeaseLossAfterSealCleansNewObject(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	stageID := "lease-loss-after-preflight"
	dir := filepath.Join(upload, "posters", "generation-1", stageID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, posterLogicalName)
	if err := os.WriteFile(path, []byte("candidate-after-preflight"), 0600); err != nil {
		t.Fatal(err)
	}
	size, hash, _ := hashPath(path)
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	req := publication.StageRequest{QueueID: task.ID, MediaID: task.MediaID, RunID: *task.RunID, StepID: *task.StepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, Attempt: task.Attempts, SourceFingerprint: fp}
	staged := StagedPoster{Stage: publication.StageRecord{StageID: stageID, Request: req, Kind: publication.ArtifactPoster, State: "staged", StagedPath: dir}, Path: path, URL: storage.ImmutablePlainPosterURL(task.Generation, stageID), Size: size, Hash: hash}
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,'{}')`, stageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, dir); err != nil {
		t.Fatal(err)
	}
	old := posterAfterSealHook
	posterAfterSealHook = func() { _, _ = db.Exec(`UPDATE post_ingest_task SET lease_owner='other' WHERE id=?`, task.ID) }
	t.Cleanup(func() { posterAfterSealHook = old })
	requirePosterShutdown(t, commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}))
	if got := posterObjectFiles(t, upload); len(got) != 0 {
		t.Fatalf("orphan objects=%v", got)
	}
}
