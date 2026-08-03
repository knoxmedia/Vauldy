package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	if steps != 3 || post != 1 || scrape != 1 {
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
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 2); err != nil || n != 1 {
		t.Fatalf("completed repair without evidence=%d err=%v", n, err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 2); err != nil || n != 0 {
		t.Fatalf("pending evidence repair duplicated=%d err=%v", n, err)
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

func seedCompliantEncryptedVideoEvidence(t *testing.T, db *sql.DB) (int64, string) {
	t.Helper()
	mediaID, source := seedEncryptionRequiredLegacyVideo(t, db)
	enc := filepath.Join(filepath.Dir(source), "legacy.enc")
	poster := filepath.Join(filepath.Dir(source), "poster.jpg.fixture.enc")
	if err := os.WriteFile(enc, []byte("encrypted video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poster, []byte("encrypted poster"), 0o600); err != nil {
		t.Fatal(err)
	}
	posterURL := fmt.Sprintf("/api/v1/media/%d/poster.jpg", mediaID)
	meta := fmt.Sprintf(`{"scrape":{"poster":%q,"extra":{"poster":%q}}}`, posterURL, posterURL)
	if _, err := db.Exec(`UPDATE media SET file_path=?,ingest_generation=1,meta_json=? WHERE id=?`, enc, meta, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,'wrapped','iv',?,'encrypted')`, mediaID, enc, source); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg',?,'poster-wrapped','poster-iv')`, mediaID, poster); err != nil {
		t.Fatal(err)
	}
	fp, err := SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	encHash := sha256.Sum256([]byte("encrypted video"))
	posterHash := sha256.Sum256([]byte("encrypted poster"))
	if _, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(?,1,'scan','published',0,'{}',2)`, mediaID); err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err = db.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	for _, step := range []StepType{StepPoster, StepEncrypt} {
		res, e := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,?,1,'done')`, runID, mediaID, step)
		if e != nil {
			t.Fatal(e)
		}
		stepID, _ := res.LastInsertId()
		refs := fmt.Sprintf(`{"path":%q,"url":%q,"source":%q,"size":%d,"sha256":%q,"generation":1,"stage_id":"poster-stage"}`, poster, posterURL, source, len("encrypted poster"), hex.EncodeToString(posterHash[:]))
		if step == StepEncrypt {
			refs = fmt.Sprintf(`{"path":%q,"size":%d,"sha256":%q,"wrapped_dek":"wrapped","iv":"iv"}`, enc, len("encrypted video"), hex.EncodeToString(encHash[:]))
		}
		if _, e = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,?,?,?,'test',CURRENT_TIMESTAMP,?)`, runID, stepID, mediaID, 1, step, fp, refs, string(step)+"-stage"); e != nil {
			t.Fatal(e)
		}
	}
	return mediaID, source
}

func TestRepairLegacyMediaSkipsCompleteEvidence(t *testing.T) {
	db := openRepairTestDB(t)
	_, _ = seedCompliantEncryptedVideoEvidence(t, db)
	if repaired, err := RepairLegacyMedia(context.Background(), db, allRepairPlanner(t), 4); err != nil || repaired != 0 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
}

func TestRepairLegacyAcceptsPrecaptureZeroHashPosterAfterPlaintextCleanup(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID, source := seedCompliantEncryptedVideoEvidence(t, db)
	fp, err := SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := fingerprintIdentityKey(fp)
	if !ok {
		t.Fatalf("fingerprint identity: %q", fp)
	}
	placeholder := identity + "|sha256:" + strings.Repeat("0", 64)
	var encPath string
	if err = db.QueryRow(`SELECT enc_path FROM media_encrypted_assets WHERE media_id=?`, mediaID).Scan(&encPath); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_ingest_evidence SET source_fingerprint=?,reason='precapture' WHERE media_id=? AND kind='poster'`, placeholder, mediaID); err != nil {
		t.Fatal(err)
	}
	var runID, encryptStepID, encryptTaskID int64
	if err = db.QueryRow(`SELECT r.id,s.id FROM media_ingest_run r JOIN media_ingest_step s ON s.run_id=r.id AND s.step_type='encrypt' WHERE r.media_id=? AND r.generation=1`, mediaID).Scan(&runID, &encryptStepID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt' AND generation=1`, mediaID).Scan(&encryptTaskID); err != nil {
		// seedCompliantEncryptedVideoEvidence does not always create queue rows; insert a stub task for FK.
		res, e := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(?,?,?,1,'encrypt','done')`, mediaID, runID, encryptStepID)
		if e != nil {
			t.Fatal(e)
		}
		encryptTaskID, _ = res.LastInsertId()
	}
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state) VALUES('precapture-repair-stage',?,1,?,?,?,1,'owner',?,?,?,'wrapped','iv','encsha',16,'committed')`, encryptTaskID, mediaID, runID, encryptStepID, source, fp, encPath); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if repaired, err := RepairLegacyMedia(context.Background(), db, allRepairPlanner(t), 4); err != nil || repaired != 0 {
		t.Fatalf("repaired=%d err=%v want 0 (precapture poster + journal encrypt must skip repair)", repaired, err)
	}
}

func TestRepairLegacyUnencryptedVideoAcceptsPlaintextPosterEvidence(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID := seedLegacyVideo(t, db, 1, "published")
	source := filepath.Join(t.TempDir(), "legacy.mp4")
	posterBytes := []byte("plain CAS poster")
	hash := sha256.Sum256(posterBytes)
	poster := filepath.Join(t.TempDir(), hex.EncodeToString(hash[:])+".jpg")
	if err := os.WriteFile(source, []byte("legacy source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poster, posterBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/uploads/posters/objects/sha256/%s/%s.jpg", hex.EncodeToString(hash[:2]), hex.EncodeToString(hash[:]))
	if _, err := db.Exec(`UPDATE media SET file_path=?,ingest_generation=1,meta_json=? WHERE id=?`, source, fmt.Sprintf(`{"scrape":{"poster":%q}}`, url), mediaID); err != nil {
		t.Fatal(err)
	}
	fp, err := SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(?,1,'scan','published',1,'{}',2)`, mediaID); err != nil {
		t.Fatal(err)
	}
	var runID int64
	if err = db.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'poster',1,'done')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	refs := fmt.Sprintf(`{"path":%q,"url":%q,"size":%d,"sha256":%q}`, poster, url, len(posterBytes), hex.EncodeToString(hash[:]))
	if _, err = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,1,'poster',?,?,'test',CURRENT_TIMESTAMP,'plain-poster-stage')`, runID, stepID, mediaID, fp, refs); err != nil {
		t.Fatal(err)
	}
	if repaired, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 4); err != nil || repaired != 0 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
}

func TestRepairLegacyEncryptedVideoRejectsPlaintextPosterEvidence(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID, _ := seedCompliantEncryptedVideoEvidence(t, db)
	plain := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(plain, []byte("plaintext poster"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("plaintext poster"))
	refs := fmt.Sprintf(`{"path":%q,"url":%q,"size":%d,"sha256":%q}`, plain, fmt.Sprintf("/api/v1/media/%d/poster.jpg", mediaID), len("plaintext poster"), hex.EncodeToString(hash[:]))
	if _, err := db.Exec(`UPDATE media_ingest_evidence SET artifact_refs_json=? WHERE media_id=? AND kind='poster'`, refs, mediaID); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, allRepairPlanner(t), 4); err != nil || n != 1 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
}

func TestRepairLegacyEncryptedVideoRejectsMismatchedPosterCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, update string
	}{
		{"path", `UPDATE media_derived_assets SET enc_path='mismatched.enc' WHERE media_id=? AND artifact_kind='poster'`},
		{"key", `UPDATE media_derived_assets SET logical_name='wrong.jpg' WHERE media_id=? AND artifact_kind='poster'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openRepairTestDB(t)
			mediaID, _ := seedCompliantEncryptedVideoEvidence(t, db)
			if _, err := db.Exec(tc.update, mediaID); err != nil {
				t.Fatal(err)
			}
			if n, err := RepairLegacyMedia(context.Background(), db, allRepairPlanner(t), 4); err != nil || n != 1 {
				t.Fatalf("repaired=%d err=%v", n, err)
			}
		})
	}
}

func TestRepairLegacyEncryptedVideoMissingPosterPreservesVisibility(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID, _ := seedCompliantEncryptedVideoEvidence(t, db)
	if _, err := db.Exec(`DELETE FROM media_ingest_evidence WHERE media_id=? AND kind='poster'`, mediaID); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, allRepairPlanner(t), 4); err != nil || n != 1 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
	var state string
	var preserve int
	if err := db.QueryRow(`SELECT m.publication_state,r.preserve_visibility FROM media m JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation WHERE m.id=?`, mediaID).Scan(&state, &preserve); err != nil {
		t.Fatal(err)
	}
	if state != "published" || preserve != 1 {
		t.Fatalf("state=%s preserve=%d", state, preserve)
	}
}

func TestRepairLegacyEncryptedVideoSourceMismatchHidesRepair(t *testing.T) {
	db := openRepairTestDB(t)
	mediaID, source := seedCompliantEncryptedVideoEvidence(t, db)
	if err := os.WriteFile(source, []byte("changed plaintext source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, allRepairPlanner(t), 4); err != nil || n != 1 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
	var state string
	var preserve int
	if err := db.QueryRow(`SELECT m.publication_state,r.preserve_visibility FROM media m JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation WHERE m.id=?`, mediaID).Scan(&state, &preserve); err != nil {
		t.Fatal(err)
	}
	if state != "processing" || preserve != 0 {
		t.Fatalf("state=%s preserve=%d", state, preserve)
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
	mediaID, _, _, _ := seedPublishedPlainVideoEvidence(t, db)
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
	if generation != 1 || runs != 1 || current != 1 || steps != 3 || post != 1 || scrape != 1 {
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

func seedEncryptionRequiredLegacyVideo(t *testing.T, db *sql.DB) (int64, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "legacy.mp4")
	if err := os.WriteFile(source, []byte("legacy video source"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := seedLegacyVideo(t, db, 1, "published")
	if _, err := db.Exec(`UPDATE media SET file_path=? WHERE id=?`, source, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE library SET encrypted_assets_enabled=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, id); err != nil {
		t.Fatal(err)
	}
	return id, source
}

func TestRepairLegacyVideoNewEncryptionHidesPlaintextSelection(t *testing.T) {
	db := openRepairTestDB(t)
	id, _ := seedEncryptionRequiredLegacyVideo(t, db)
	var publishedAt string
	if err := db.QueryRow(`SELECT published_at FROM media WHERE id=?`, id).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true}), 8); err != nil || n != 1 {
		t.Fatalf("repair=%d err=%v", n, err)
	}
	var state, after string
	var preserve int
	if err := db.QueryRow(`SELECT m.publication_state,m.published_at,r.preserve_visibility FROM media m JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation WHERE m.id=?`, id).Scan(&state, &after, &preserve); err != nil {
		t.Fatal(err)
	}
	if state != "processing" || preserve != 0 || after != publishedAt {
		t.Fatalf("state=%s preserve=%d published=%q want processing/0/%q", state, preserve, after, publishedAt)
	}
}

func TestRepairLegacyVideoOrphanEncryptedRowDoesNotPreservePlaintext(t *testing.T) {
	db := openRepairTestDB(t)
	id, source := seedEncryptionRequiredLegacyVideo(t, db)
	enc := filepath.Join(filepath.Dir(source), "legacy.enc")
	if err := os.WriteFile(enc, []byte("orphan encrypted bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,'wrapped','iv',?,'encrypted')`, id, enc, source); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true}), 8); err != nil || n != 1 {
		t.Fatalf("repair=%d err=%v", n, err)
	}
	var state string
	var preserve int
	if err := db.QueryRow(`SELECT m.publication_state,r.preserve_visibility FROM media m JOIN media_ingest_run r ON r.media_id=m.id WHERE m.id=?`, id).Scan(&state, &preserve); err != nil {
		t.Fatal(err)
	}
	if state != "processing" || preserve != 0 {
		t.Fatalf("state=%s preserve=%d", state, preserve)
	}
}

func TestRepairLegacyVideoEncryptionFailureStaysHiddenAndRetainsPublishedAt(t *testing.T) {
	db := openRepairTestDB(t)
	id, source := seedEncryptionRequiredLegacyVideo(t, db)
	var publishedAt string
	if err := db.QueryRow(`SELECT published_at FROM media WHERE id=?`, id).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true}), 8); err != nil || n != 1 {
		t.Fatalf("repair=%d err=%v", n, err)
	}
	var runID int64
	if err := db.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=? AND generation=(SELECT ingest_generation FROM media WHERE id=?)`, id, id).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='done' WHERE run_id=? AND step_type='poster'; UPDATE media_ingest_step SET status='failed',last_error='encrypt failed' WHERE run_id=? AND step_type='encrypt'`, runID, runID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = AggregateTx(context.Background(), tx, runID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state, selected, after string
	if err = db.QueryRow(`SELECT publication_state,file_path,published_at FROM media WHERE id=?`, id).Scan(&state, &selected, &after); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || selected != source || after != publishedAt {
		t.Fatalf("state=%s selected=%q published=%q want failed/%q/%q", state, selected, after, source, publishedAt)
	}
}

func TestSourceFingerprintContextPreservesFormatAndWrapperCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.bin")
	contents := []byte("fingerprint the complete source, including this tail")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	want := fmt.Sprintf("%s|%d|%d|sha256:%s", filepath.Clean(absolute), info.Size(), info.ModTime().UnixNano(), hex.EncodeToString(digest[:]))

	got, err := SourceFingerprintContext(context.Background(), path)
	if err != nil {
		t.Fatalf("SourceFingerprintContext: %v", err)
	}
	if got != want {
		t.Fatalf("fingerprint = %q, want %q", got, want)
	}
	legacy, err := SourceFingerprint(path)
	if err != nil {
		t.Fatalf("SourceFingerprint: %v", err)
	}
	if legacy != got {
		t.Fatalf("legacy fingerprint = %q, context fingerprint = %q", legacy, got)
	}
}

func TestSourceFingerprintContextCanceledBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := SourceFingerprintContext(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestSourceFingerprintContextCancelsDuringRealFileRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte("source bytes "), 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceFingerprintReadMu.Lock()
	original := sourceFingerprintRead
	readCalls := 0
	sourceFingerprintRead = func(r io.Reader, p []byte) (int, error) {
		readCalls++
		n, err := original(r, p[:min(len(p), 16)])
		cancel()
		return n, err
	}
	sourceFingerprintReadMu.Unlock()
	t.Cleanup(func() {
		sourceFingerprintReadMu.Lock()
		sourceFingerprintRead = original
		sourceFingerprintReadMu.Unlock()
	})

	_, err := SourceFingerprintContext(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if readCalls != 1 {
		t.Fatalf("read calls = %d, want 1", readCalls)
	}
}

type cancelingFingerprintReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *cancelingFingerprintReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("reader called after cancellation")
	}
	r.read = true
	n := copy(p, "first chunk")
	r.cancel()
	return n, nil
}

func TestCopyFingerprintContextStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingFingerprintReader{cancel: cancel}
	var dst bytes.Buffer

	n, err := copyFingerprintContext(ctx, &dst, reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if n != int64(len("first chunk")) {
		t.Fatalf("bytes copied = %d, want %d", n, len("first chunk"))
	}
	if got := dst.String(); got != "first chunk" {
		t.Fatalf("copied bytes = %q, want %q", got, "first chunk")
	}
	if !reader.read {
		t.Fatal("reader was not called")
	}
}
