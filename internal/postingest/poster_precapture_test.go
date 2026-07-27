package postingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"knox-media/internal/keystore"
	"knox-media/internal/publication"
)

func seedPreCapturePlan(t *testing.T) (*sql.DB, string, string, publication.Run) {
	t.Helper()
	db, _ := openQueueTestDB(t)
	upload := t.TempDir()
	source := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(source, []byte("video-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path,image_providers) VALUES('precapture','video',?,'screen_grabber')`, filepath.Dir(source))
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanTaskID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,file_path,duration,meta_json,publication_state) VALUES(?,'precapture-media','video',?,60,'{}','processing')`, libraryID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := publication.NewPlanner(publication.PlanOptions{}).PlanNewMediaTx(context.Background(), tx, publication.NewMedia{MediaID: mediaID, ScanTaskID: scanTaskID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db, upload, source, run
}

func TestCapturePosterDoesNotHoldWriterTransaction(t *testing.T) {
	db, upload, _, run := seedPreCapturePlan(t)
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake"}
	runner.RunFFmpeg = func(ctx context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, os.WriteFile(post[len(post)-1], []byte("poster"), 0o600)
	}
	result := make(chan error, 1)
	go func() {
		_, err := CapturePoster(context.Background(), run.MediaID, run, PreCaptureConfig{DB: db, Runner: runner, UploadDir: upload})
		result <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `UPDATE library SET name='writer-succeeded' WHERE id=?`, run.LibraryID); err != nil {
		t.Fatalf("independent writer blocked during capture: %v", err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestCaptureFinalizePublishesPosterEvidenceAndPlan(t *testing.T) {
	db, upload, _, run := seedPreCapturePlan(t)
	runner := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake"}
	runner.RunFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("poster"), 0o600)
	}
	captured, err := CapturePoster(context.Background(), run.MediaID, run, PreCaptureConfig{DB: db, Runner: runner, UploadDir: upload})
	if err != nil {
		t.Fatal(err)
	}
	if captured.MediaID != run.MediaID || captured.RunID != run.ID || captured.StepID == 0 || captured.TaskID == 0 || captured.Generation != run.Generation || captured.ArtifactPath == "" || captured.SourcePath == "" {
		t.Fatalf("incomplete captured identity: %+v", captured)
	}
	if err = FinalizeCapturedPoster(context.Background(), db, captured); err != nil {
		t.Fatal(err)
	}
	var mediaState, runState, stepState, taskState, meta, reason string
	var published sql.NullTime
	if err = db.QueryRow(`SELECT m.publication_state,m.published_at,m.meta_json,r.status,s.status,p.status,e.reason FROM media m JOIN media_ingest_run r ON r.id=? JOIN media_ingest_step s ON s.id=? JOIN post_ingest_task p ON p.id=? JOIN media_ingest_evidence e ON e.step_id=s.id AND e.kind='poster' WHERE m.id=?`, captured.RunID, captured.StepID, captured.TaskID, captured.MediaID).Scan(&mediaState, &published, &meta, &runState, &stepState, &taskState, &reason); err != nil {
		t.Fatal(err)
	}
	if mediaState != "published" || !published.Valid || runState != "published" || stepState != "done" || taskState != "done" || reason != "precapture" || !strings.Contains(meta, captured.ArtifactURL) {
		t.Fatalf("state media=%s published=%v run=%s step=%s task=%s reason=%s meta=%s", mediaState, published.Valid, runState, stepState, taskState, reason, meta)
	}
}

func TestRejectCapturedPosterDeletesOnlyMatchingUnpublishedProcessingMedia(t *testing.T) {
	db, upload, _, run := seedPreCapturePlan(t)
	runner := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake"}
	runner.RunFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("poster"), 0o600)
	}
	captured, err := CapturePoster(context.Background(), run.MediaID, run, PreCaptureConfig{DB: db, Runner: runner, UploadDir: upload})
	if err != nil {
		t.Fatal(err)
	}
	if err = RejectCapturedPoster(context.Background(), db, captured); err != nil {
		t.Fatal(err)
	}
	if err = RejectCapturedPoster(context.Background(), db, captured); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM media WHERE id=?`, run.MediaID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("media count=%d err=%v", count, err)
	}
	for _, table := range []string{"media_ingest_run", "media_ingest_step", "post_ingest_task"} {
		if err = db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE media_id=?`, run.MediaID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if _, err = os.Stat(captured.ArtifactPath); !os.IsNotExist(err) {
		t.Fatalf("artifact still exists: %v", err)
	}
}

func TestFinalizeStaleSourceOrGenerationRejectsAndCleans(t *testing.T) {
	for _, stale := range []string{"source", "generation"} {
		t.Run(stale, func(t *testing.T) {
			db, upload, source, run := seedPreCapturePlan(t)
			runner := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake"}
			runner.RunFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
				return nil, os.WriteFile(post[len(post)-1], []byte("poster"), 0o600)
			}
			captured, err := CapturePoster(context.Background(), run.MediaID, run, PreCaptureConfig{DB: db, Runner: runner, UploadDir: upload})
			if err != nil {
				t.Fatal(err)
			}
			if stale == "source" {
				if err = os.WriteFile(source, []byte("changed-source"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				_, err = db.Exec(`UPDATE media SET ingest_generation=ingest_generation+1 WHERE id=?`, run.MediaID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = FinalizeCapturedPoster(context.Background(), db, captured); err == nil {
				t.Fatal("stale capture finalized")
			}
			if err = RejectCapturedPoster(context.Background(), db, captured); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(captured.ArtifactPath); !os.IsNotExist(err) {
				t.Fatalf("artifact still exists: %v", err)
			}
			var count int
			_ = db.QueryRow(`SELECT COUNT(*) FROM media WHERE id=?`, run.MediaID).Scan(&count)
			if stale == "source" && count != 0 {
				t.Fatalf("matching stale-source media retained")
			}
			if stale == "generation" && count != 1 {
				t.Fatalf("new generation media deleted")
			}
		})
	}
}
