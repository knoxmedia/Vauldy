package publication

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"knox-media/internal/coreiface"
	"knox-media/internal/store"
)

func openRepairTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedLegacyVideo(t *testing.T, db *sql.DB, mtime int64, state string) int64 {
	t.Helper()
	r, err := db.Exec(`INSERT INTO library(name,type,path,preview_extract,encrypted_assets_enabled,jit_prepare_on_ingest) VALUES('legacy','video','/legacy',0,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_mtime,file_type,status,publication_state,published_at) VALUES(?,?,?,?,?,'active',?,CURRENT_TIMESTAMP)`, libraryID, fmt.Sprintf("legacy-%d", libraryID), fmt.Sprintf("/legacy/%d.mp4", libraryID), mtime, "video", state)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func repairRunCount(t *testing.T, db *sql.DB, mediaID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=? AND reason='repair'`, mediaID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRepairLegacyMediaCreatesGenerationForMissingPoster(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedLegacyVideo(t, db, 1710000000, "published")

	repaired, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 1)
	if err != nil || repaired != 1 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}

	var generation, runGeneration, preserve int64
	var reason, state string
	var scanTask sql.NullInt64
	if err = db.QueryRow(`SELECT m.ingest_generation,m.publication_state,r.generation,r.reason,r.preserve_visibility,r.scan_task_id FROM media m JOIN media_ingest_run r ON r.media_id=m.id WHERE m.id=?`, mediaID).Scan(&generation, &state, &runGeneration, &reason, &preserve, &scanTask); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || runGeneration != 1 || reason != "repair" || preserve != 1 || scanTask.Valid || state != "published" {
		t.Fatalf("generation=%d/%d reason=%s preserve=%d scan=%v state=%s", generation, runGeneration, reason, preserve, scanTask, state)
	}
	var steps, post, scrape int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=?`, mediaID).Scan(&steps)
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND generation=1`, mediaID).Scan(&post)
	_ = db.QueryRow(`SELECT COUNT(*) FROM scrape_task WHERE media_id=? AND generation=1`, mediaID).Scan(&scrape)
	if steps != 2 || post != 1 || scrape != 1 {
		t.Fatalf("steps=%d post=%d scrape=%d", steps, post, scrape)
	}
}

func TestRepairLegacyMediaIncludesUnchangedMtime(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedLegacyVideo(t, db, 123, "published")
	if _, err := db.Exec(`UPDATE media SET created_at='2020-01-01 00:00:00' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	repaired, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8)
	if err != nil || repaired != 1 || repairRunCount(t, db, mediaID) != 1 {
		t.Fatalf("repaired=%d runs=%d err=%v", repaired, repairRunCount(t, db, mediaID), err)
	}
}

func TestRepairLegacyMediaIsIdempotent(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedLegacyVideo(t, db, 1, "published")
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 2); err != nil || n != 1 {
		t.Fatalf("first=%d err=%v", n, err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 2); err != nil || n != 0 {
		t.Fatalf("second=%d err=%v", n, err)
	}
	if repairRunCount(t, db, mediaID) != 1 {
		t.Fatalf("repair runs=%d", repairRunCount(t, db, mediaID))
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='done' WHERE media_id=?; UPDATE media_ingest_run SET status='published' WHERE media_id=?`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 2); err != nil || n != 0 {
		t.Fatalf("after completed repair=%d err=%v", n, err)
	}
}

func TestRepairLegacyMediaPreservesPublishedVisibility(t *testing.T) {
	db := openRepairTestDB(t)
	published := seedLegacyVideo(t, db, 1, "published")
	degraded := seedLegacyVideo(t, db, 1, "degraded")
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 1); err != nil || n != 2 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
	for id, want := range map[int64]string{published: "published", degraded: "degraded"} {
		var got string
		if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("media=%d state=%s want=%s", id, got, want)
		}
	}
}

type repairPreparePlanner struct{}

func (repairPreparePlanner) PlanIngestPrepareTx(ctx context.Context, tx store.SQLExecutor, mediaID, runID, stepID, generation int64) error {
	var fileID string
	if err := tx.QueryRowContext(ctx, `SELECT file_id FROM media WHERE id=?`, mediaID).Scan(&fileID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO transcode_task(file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting','pretranscode',?,?,?,?)`, fileID, mediaID, runID, stepID, generation)
	return err
}

func addRepairEvidence(t *testing.T, db *sql.DB, mediaID int64, step StepType) {
	t.Helper()
	var query string
	switch step {
	case StepPoster:
		query = `INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg','poster.enc','wrapped','iv')`
	case StepScrape:
		query = `INSERT INTO scrape_task(media_id,source,status,progress) VALUES(?,'legacy','done',100)`
	case StepPreview:
		query = `INSERT INTO preview_task(media_id,status,thumb_count,sprite_path,vtt_path) VALUES(?,'done',2,'sprite.jpg','preview.vtt')`
	case StepKeyframe:
		query = `INSERT INTO keyframe_task(media_id,status,output_dir,keyframe_count) VALUES(?,'done','keyframes',4)`
	case StepSubtitle:
		query = `INSERT INTO media_subtitle(media_id,dedupe_key,source_kind,vtt_path,status) VALUES(?,'legacy-en','embedded','subtitle.vtt','ready')`
	case StepAtrack:
		query = `INSERT INTO atrack_task(media_id,status,output_dir) VALUES(?,'done','atracks')`
	case StepEncrypt:
		query = `INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,'video.enc','wrapped','iv','video.mp4','encrypted')`
	case StepPrepare:
		query = `INSERT INTO transcode_task(file_id,status,task_type) SELECT file_id,'done','pretranscode' FROM media WHERE id=?`
	default:
		t.Fatalf("unsupported evidence step %q", step)
	}
	if _, err := db.Exec(query, mediaID); err != nil {
		t.Fatalf("add %s evidence: %v", step, err)
	}
}

func allRepairPlanner(t *testing.T) *Planner {
	t.Helper()
	restore := coreiface.RegisterIngestPreparePlanner(repairPreparePlanner{})
	t.Cleanup(restore)
	return NewPlanner(PlanOptions{
		SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true,
		PreparePlanner: coreiface.IngestPreparePlannerHandle(), Capabilities: NewCapabilityMatrix([]string{"prepare"}),
	})
}

func seedAllRequiredLegacyVideo(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	mediaID := seedLegacyVideo(t, db, 1, "published")
	if _, err := db.Exec(`UPDATE library SET preview_extract=1,encrypted_assets_enabled=1,jit_prepare_on_ingest=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID); err != nil {
		t.Fatal(err)
	}
	return mediaID
}

func TestRepairLegacyMediaSkipsCompleteEvidence(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedAllRequiredLegacyVideo(t, db)
	addRepairEvidence(t, db, mediaID, StepPoster)
	addRepairEvidence(t, db, mediaID, StepEncrypt)
	if repaired, err := RepairLegacyMedia(context.Background(), db, allRepairPlanner(t), 4); err != nil || repaired != 0 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
}

func TestRepairLegacyMediaCreatesNextGenerationWhenRequirementsExpand(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedLegacyVideo(t, db, 1, "published")
	addRepairEvidence(t, db, mediaID, StepPoster)
	if _, err := db.Exec(`UPDATE library SET encrypted_assets_enabled=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true}), 4); err != nil || n != 1 {
		t.Fatalf("expanded=%d err=%v", n, err)
	}
}

func TestRepairLegacyMediaPendingCurrentRepairSkipsDuplicate(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedLegacyVideo(t, db, 1, "published")
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 4); err != nil || n != 1 {
		t.Fatalf("first=%d err=%v", n, err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 4); err != nil || n != 0 {
		t.Fatalf("second=%d err=%v", n, err)
	}
	if repairRunCount(t, db, mediaID) != 1 {
		t.Fatalf("runs=%d", repairRunCount(t, db, mediaID))
	}
}

func TestRepairLegacyMediaDoesNotRequireOptionalStepEvidence(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedLegacyVideo(t, db, 1, "published")
	for _, step := range []StepType{StepPoster, StepScrape, StepKeyframe} {
		addRepairEvidence(t, db, mediaID, step)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,'scan','published',0,'{}')`, mediaID); err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err := db.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'preview',0,'failed')`, runID, mediaID); err != nil {
		t.Fatal(err)
	}
	repaired, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 4)
	if err != nil || repaired != 0 {
		t.Fatalf("optional evidence repaired=%d err=%v", repaired, err)
	}
}

func TestRepairLegacyMediaConcurrentCallsCreateOneGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-concurrent.sqlite")
	db1, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	mediaID := seedLegacyVideo(t, db1, 1, "published")

	const callers = 20
	start := make(chan struct{})
	results := make(chan struct {
		n   int
		err error
	}, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		db := db1
		if i%2 == 1 {
			db = db2
		}
		go func() {
			defer wg.Done()
			<-start
			n, callErr := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 1)
			results <- struct {
				n   int
				err error
			}{n, callErr}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	for result := range results {
		if result.err != nil {
			t.Errorf("concurrent repair: %v", result.err)
		}
		created += result.n
	}
	if created != 1 {
		t.Fatalf("created=%d want 1", created)
	}

	var generation, runs, current, steps, post, scrape int
	if err := db1.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	_ = db1.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=? AND reason='repair'`, mediaID).Scan(&runs)
	_ = db1.QueryRow(`SELECT COUNT(*) FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.media_id=? AND r.reason='repair'`, mediaID).Scan(&current)
	_ = db1.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=?`, mediaID).Scan(&steps)
	_ = db1.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&post)
	_ = db1.QueryRow(`SELECT COUNT(*) FROM scrape_task WHERE media_id=? AND ingest_run_id IS NOT NULL`, mediaID).Scan(&scrape)
	if generation != 1 || runs != 1 || current != 1 || steps != 2 || post != 1 || scrape != 1 {
		t.Fatalf("generation=%d runs=%d current=%d steps=%d post=%d scrape=%d", generation, runs, current, steps, post, scrape)
	}
}

func TestRepairLegacyMediaAggregateSuccessAndFailureVisibility(t *testing.T) {
	db := openRepairTestDB(t)
	success := seedLegacyVideo(t, db, 1, "degraded")
	failure := seedLegacyVideo(t, db, 1, "published")
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 2 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
	for _, tc := range []struct {
		id               int64
		stepStatus, want string
	}{{success, "done", "published"}, {failure, "failed", "degraded"}} {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var runID int64
		if err = tx.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=? AND reason='repair'`, tc.id).Scan(&runID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE media_ingest_step SET status=?,last_error=CASE WHEN ?='failed' THEN 'repair failed' ELSE '' END WHERE run_id=?`, tc.stepStatus, tc.stepStatus, runID); err != nil {
			t.Fatal(err)
		}
		if err = AggregateTx(context.Background(), tx, runID); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var got, publicationError string
		if err = db.QueryRow(`SELECT publication_state,publication_error FROM media WHERE id=?`, tc.id).Scan(&got, &publicationError); err != nil {
			t.Fatal(err)
		}
		if got != tc.want || (tc.want == "published" && publicationError != "") || (tc.want == "degraded" && publicationError == "") {
			t.Fatalf("media=%d state=%s error=%q want=%s", tc.id, got, publicationError, tc.want)
		}
	}
}
