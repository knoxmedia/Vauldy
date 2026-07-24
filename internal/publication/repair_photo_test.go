package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"knox-media/internal/store"
)

func seedLegacyPhoto(t *testing.T, db *sql.DB, root, state string, encrypted bool) int64 {
	t.Helper()
	enc := 0
	if encrypted {
		enc = 1
	}
	r, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled) VALUES('photos','photo',?,?)`, root, enc)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := r.LastInsertId()
	source := filepath.Join(root, "photo.jpg")
	if err = os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,published_at,publication_error,meta_json) VALUES(?,?,?,'image','active',?,CURRENT_TIMESTAMP,'legacy error','{}')`, libraryID, filepath.Base(source), source, state)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func addLegacyPhotoThumbnail(t *testing.T, db *sql.DB, mediaID int64, root string, encrypted bool) {
	t.Helper()
	ext := ".jpg"
	if encrypted {
		ext = ".enc"
	}
	thumb := filepath.Join(root, "thumb"+ext)
	medium := filepath.Join(root, "medium"+ext)
	if err := os.WriteFile(thumb, []byte("thumb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(medium, []byte("medium"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"photo": map[string]any{"thumb_path": thumb, "medium_path": medium}})
	if _, err := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, string(meta), mediaID); err != nil {
		t.Fatal(err)
	}
	if encrypted {
		if _, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'photo_thumb','thumb.jpg',?,'wrapped','iv'),(?,'photo_medium','medium.jpg',?,'wrapped','iv')`, mediaID, thumb, mediaID, medium); err != nil {
			t.Fatal(err)
		}
	}
}

func photoRepairSteps(t *testing.T, db *sql.DB, mediaID int64) []StepType {
	t.Helper()
	rows, err := db.Query(`SELECT step_type FROM media_ingest_step WHERE media_id=? ORDER BY id`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []StepType
	for rows.Next() {
		var step StepType
		if err = rows.Scan(&step); err != nil {
			t.Fatal(err)
		}
		got = append(got, step)
	}
	return got
}

func TestRepairLegacyPhotoMissingThumbnailPlansPreservingRepair(t *testing.T) {
	db := openRepairTestDB(t)
	id := seedLegacyPhoto(t, db, t.TempDir(), "published", false)
	var publishedAt, oldError string
	if err := db.QueryRow(`SELECT published_at,publication_error FROM media WHERE id=?`, id).Scan(&publishedAt, &oldError); err != nil {
		t.Fatal(err)
	}
	n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8)
	if err != nil || n != 1 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
	if got := photoRepairSteps(t, db, id); !reflect.DeepEqual(got, []StepType{StepThumbnail, StepScrape}) {
		t.Fatalf("steps=%v", got)
	}
	var generation, preserve, policy int
	var state, afterPublished, afterError, reason string
	if err = db.QueryRow(`SELECT m.ingest_generation,m.publication_state,m.published_at,m.publication_error,r.reason,r.preserve_visibility,r.policy_version FROM media m JOIN media_ingest_run r ON r.media_id=m.id WHERE m.id=?`, id).Scan(&generation, &state, &afterPublished, &afterError, &reason, &preserve, &policy); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || state != "published" || afterPublished != publishedAt || afterError != oldError || reason != "repair" || preserve != 1 || policy != PolicyV2 {
		t.Fatalf("gen=%d state=%s dates=%q/%q errors=%q/%q reason=%s preserve=%d policy=%d", generation, state, publishedAt, afterPublished, oldError, afterError, reason, preserve, policy)
	}
}

func TestRepairLegacyPhotoCompleteFilesWithoutEvidenceRepairs(t *testing.T) {
	db := openRepairTestDB(t)
	root := t.TempDir()
	id := seedLegacyPhoto(t, db, root, "published", false)
	addLegacyPhotoThumbnail(t, db, id, root, false)
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 1 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
}

func TestRepairLegacyPhotoNewEncryptionPlansThumbnailThenEncrypt(t *testing.T) {
	db := openRepairTestDB(t)
	root := t.TempDir()
	id := seedLegacyPhoto(t, db, root, "published", true)
	addLegacyPhotoThumbnail(t, db, id, root, false)
	n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true}), 8)
	if err != nil || n != 1 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
	if got := photoRepairSteps(t, db, id); !reflect.DeepEqual(got, []StepType{StepThumbnail, StepEncrypt, StepScrape}) {
		t.Fatalf("steps=%v", got)
	}
	var deps int
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE s.media_id=? AND s.step_type='encrypt' AND p.step_type='thumbnail' AND d.dependency_kind='step_done'`, id).Scan(&deps); err != nil || deps != 1 {
		t.Fatalf("deps=%d err=%v", deps, err)
	}
}

func TestRepairLegacyEncryptedPhotoCompleteFilesWithoutEvidenceRepairs(t *testing.T) {
	db := openRepairTestDB(t)
	root := t.TempDir()
	id := seedLegacyPhoto(t, db, root, "published", true)
	addLegacyPhotoThumbnail(t, db, id, root, true)
	sourceEnc := filepath.Join(root, "photo.enc")
	if err := os.WriteFile(sourceEnc, []byte("encrypted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) SELECT id,?,'wrapped','iv',file_path,'encrypted' FROM media WHERE id=?`, sourceEnc, id); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true}), 8); err != nil || n != 1 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
}

func TestRepairLegacyPhotoDegradedTerminalDoesNotReopenOnRestart(t *testing.T) {
	db := openRepairTestDB(t)
	id := seedLegacyPhoto(t, db, t.TempDir(), "degraded", false)
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 1 {
		t.Fatalf("first=%d err=%v", n, err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='failed',last_error='thumbnail failed' WHERE media_id=? AND required=1; UPDATE media_ingest_run SET status='degraded',error_message='thumbnail failed',finished_at=CURRENT_TIMESTAMP WHERE media_id=?`, id, id); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 0 {
		t.Fatalf("restart=%d err=%v", n, err)
	}
	if runs := repairRunCount(t, db, id); runs != 1 {
		t.Fatalf("runs=%d", runs)
	}
}

func TestRepairLegacyPhotoConcurrentCreatesOneGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photos.sqlite")
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
	id := seedLegacyPhoto(t, db1, t.TempDir(), "published", false)
	start := make(chan struct{})
	var wg sync.WaitGroup
	counts := make(chan int, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		db := db1
		if i%2 == 1 {
			db = db2
		}
		go func() {
			defer wg.Done()
			<-start
			n, e := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 2)
			counts <- n
			errs <- e
		}()
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for e := range errs {
		if e != nil {
			t.Errorf("repair: %v", e)
		}
	}
	if total != 1 || repairRunCount(t, db1, id) != 1 {
		t.Fatalf("created=%d runs=%d", total, repairRunCount(t, db1, id))
	}
}

func TestRepairLegacyMediaMixedBatchRepairsVideoAndImageAndSkipsDocument(t *testing.T) {
	db := openRepairTestDB(t)
	video := seedLegacyVideo(t, db, 1, "published")
	image := seedLegacyPhoto(t, db, t.TempDir(), "published", false)
	r, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('docs','docs','/docs')`)
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,published_at) VALUES(?,'doc','/docs/a.pdf','document','active','published',CURRENT_TIMESTAMP)`, lid)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := r.LastInsertId()
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 1); err != nil || n != 2 {
		t.Fatalf("repaired=%d err=%v", n, err)
	}
	if repairRunCount(t, db, video) != 1 || repairRunCount(t, db, image) != 1 || repairRunCount(t, db, doc) != 0 {
		t.Fatalf("runs video=%d image=%d doc=%d", repairRunCount(t, db, video), repairRunCount(t, db, image), repairRunCount(t, db, doc))
	}
}

func TestRepairLegacyEncryptedPhotoRequiresCanonicalSelectedEncryptedPath(t *testing.T) {
	db := openRepairTestDB(t)
	root := t.TempDir()
	id := seedLegacyPhoto(t, db, root, "published", true)
	addLegacyPhotoThumbnail(t, db, id, root, true)
	plain, enc := filepath.Join(root, "photo.jpg"), filepath.Join(root, "photo.enc")
	if err := os.WriteFile(enc, []byte("encrypted source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,'wrapped','iv',?,'encrypted')`, id, enc, plain); err != nil {
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

func TestRepairLegacyEncryptedPhotoSecureSelectionPreservesVisibility(t *testing.T) {
	db := openRepairTestDB(t)
	root := t.TempDir()
	id := seedLegacyPhoto(t, db, root, "published", true)
	plain, enc := filepath.Join(root, "photo.jpg"), filepath.Join(root, "photo.enc")
	if err := os.WriteFile(enc, []byte("encrypted source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,'wrapped','iv',?,'encrypted')`, id, enc, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=? WHERE id=?`, enc, id); err != nil {
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
	if state != "published" || preserve != 1 {
		t.Fatalf("state=%s preserve=%d", state, preserve)
	}
}

func TestRepairLegacyPhotoPublishedWithoutGenerationEvidenceReopens(t *testing.T) {
	db := openRepairTestDB(t)
	root := t.TempDir()
	id := seedLegacyPhoto(t, db, root, "published", false)
	addLegacyPhotoThumbnail(t, db, id, root, false)
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 1 {
		t.Fatalf("repair=%d err=%v", n, err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='done' WHERE media_id=? AND required=1; UPDATE media_ingest_run SET status='published',finished_at=CURRENT_TIMESTAMP WHERE media_id=?`, id, id); err != nil {
		t.Fatal(err)
	}
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 1 {
		t.Fatalf("restart=%d err=%v", n, err)
	}
}
