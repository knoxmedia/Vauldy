package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"knox-media/internal/publication"
)

func planThumbnailFixture(t *testing.T, encrypted bool) (*sql.DB, int64, int64) {
	t.Helper()
	db, _ := openQueueTestDB(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "photo.jpg")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	if err = jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	enc := 0
	if encrypted {
		enc = 1
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled) VALUES('photos','photo',?,?)`, dir, enc)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,meta_json,publication_state) VALUES(?,'photo-fixture',?,'image','{}','published')`, libraryID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','thumbnail-test')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := res.LastInsertId()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := publication.NewPlanner(publication.PlanOptions{EncryptGlobal: encrypted}).PlanNewMediaTx(context.Background(), tx, publication.NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "image"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db, mediaID, run.ID
}

func waitPublicationState(t *testing.T, db *sql.DB, mediaID int64, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var got string
		if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&got); err == nil && got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got string
	_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&got)
	t.Fatalf("publication_state=%q want %q", got, want)
}

func TestThumbnailDispatcherExecutesPlannedPhotoAndPublishes(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, false)
	worker := realThumbnailStager(t, db)
	q := NewQueue(db, "thumbnail-owner", nil)
	opts := DefaultDispatcherOptions()
	opts.OwnerID = "thumbnail-owner"
	opts.PollInterval = 5 * time.Millisecond
	opts.HeartbeatInterval = time.Second
	dispatcher, err := NewDispatcher(q, AdapterSet{Thumbnail: NewThumbnailAdapter(db, worker)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Start(ctx) }()
	waitPublicationState(t, db, mediaID, "published")
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var evidence int
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_evidence WHERE media_id=? AND kind='thumbnail'`, mediaID).Scan(&evidence); err != nil || evidence != 1 {
		t.Fatalf("thumbnail evidence=%d err=%v", evidence, err)
	}
	var raw string
	if err = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err = json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	photo, _ := meta["photo"].(map[string]any)
	if photo["thumb_path"] == "" || photo["medium_path"] == "" {
		t.Fatalf("photo metadata=%v", photo)
	}
}

func TestThumbnailCompletionMakesEncryptDependencyReadyThenEncryptPublishes(t *testing.T) {
	db, mediaID, runID := planThumbnailFixture(t, true)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	worker := realThumbnailStager(t, db)
	result, err := NewThumbnailAdapter(db, worker).(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	}).ExecuteWithResult(context.Background(), *task)
	if err != nil || result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("thumbnail execute=%+v err=%v", result, err)
	}
	var state string
	if err = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&state); err != nil || state != "processing" {
		t.Fatalf("after thumbnail state=%q err=%v", state, err)
	}
	var ready int
	if err = db.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM media_ingest_step_dependency d JOIN media_ingest_step dep ON dep.id=d.depends_on_step_id WHERE d.step_id=s.id AND d.dependency_kind='step_done' AND dep.status<>'done') FROM media_ingest_step s WHERE s.run_id=? AND s.step_type='encrypt'`, runID).Scan(&ready); err != nil || ready != 1 {
		t.Fatalf("encrypt ready=%d err=%v", ready, err)
	}
	encrypt, err := q.Claim(context.Background(), TaskEncrypt)
	if err != nil || encrypt == nil {
		t.Fatalf("encrypt claim=%+v err=%v", encrypt, err)
	}
	if err = q.Complete(context.Background(), *encrypt); err != nil {
		t.Fatal(err)
	}
	waitPublicationState(t, db, mediaID, "published")
}

func TestThumbnailAdapterStaleGenerationCannotMutate(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	if _, err = db.Exec(`UPDATE media SET ingest_generation=ingest_generation+1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	worker := realThumbnailStager(t, db)
	err = NewThumbnailAdapter(db, worker).Execute(context.Background(), *task)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "stale") {
		t.Fatalf("err=%v", err)
	}
	var journals int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_asset_stage_journal WHERE media_id=?`, mediaID).Scan(&journals)
	if journals != 0 {
		t.Fatalf("stale worker persisted journals=%d", journals)
	}
	var raw string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw)
	if raw != "{}" {
		t.Fatalf("stale metadata=%s", raw)
	}
}

func TestThumbnailInvalidImageFailsPermanentlyWithoutRetry(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, false)
	source := taskSource(t, db, mediaID)
	if err := os.WriteFile(source, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	err = NewThumbnailAdapter(db, realThumbnailStager(t, db)).Execute(context.Background(), *task)
	if err == nil || failureKind(err) != FailurePermanent {
		t.Fatalf("err=%v kind=%v", err, failureKind(err))
	}
	if err = q.Fail(context.Background(), task, failureKind(err), err); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err = db.QueryRow(`SELECT status,attempts FROM post_ingest_task WHERE id=?`, task.ID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 1 {
		t.Fatalf("status=%s attempts=%d", status, attempts)
	}
}

func TestThumbnailDispatcherRepairsLegacyPhotoAndPublishes(t *testing.T) {
	db, _ := openQueueTestDB(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.jpg")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 6)), nil); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('legacy photos','photo',?)`, dir)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,meta_json,status,publication_state,published_at) VALUES(?,'legacy-photo',?,'image','{}','active','published',CURRENT_TIMESTAMP)`, libraryID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	if n, repairErr := publication.RepairLegacyMedia(context.Background(), db, publication.NewPlanner(publication.PlanOptions{}), 1); repairErr != nil || n != 1 {
		t.Fatalf("repair=%d err=%v", n, repairErr)
	}
	worker := realThumbnailStager(t, db)
	q := NewQueue(db, "legacy-thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	result, err := NewThumbnailAdapter(db, worker).(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	}).ExecuteWithResult(context.Background(), *task)
	if err != nil || result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	waitPublicationState(t, db, mediaID, "published")
	var reason string
	var preserve int
	if err = db.QueryRow(`SELECT reason,preserve_visibility FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&reason, &preserve); err != nil || reason != "repair" || preserve != 1 {
		t.Fatalf("reason=%q preserve=%d err=%v", reason, preserve, err)
	}
}

func TestEncryptedLegacyPhotoRepairHidesThenPublishesEncryptedSelection(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, true)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=0,publication_state='published',published_at=CURRENT_TIMESTAMP; DELETE FROM media_ingest_step; DELETE FROM media_ingest_run; DELETE FROM post_ingest_task; DELETE FROM scrape_task`); err != nil {
		t.Fatal(err)
	}
	if n, err := publication.RepairLegacyMedia(context.Background(), db, publication.NewPlanner(publication.PlanOptions{EncryptGlobal: true}), 1); err != nil || n != 1 {
		t.Fatalf("repair=%d err=%v", n, err)
	}
	var state string
	var published sql.NullTime
	if err := db.QueryRow(`SELECT publication_state,published_at FROM media WHERE id=?`, mediaID).Scan(&state, &published); err != nil {
		t.Fatal(err)
	}
	if state != "processing" || !published.Valid {
		t.Fatalf("state=%s published=%v", state, published)
	}
}

func TestEncryptAtomicFinalizerFencesStaleRetryRound(t *testing.T) {
	db, mediaID, runID := planThumbnailFixture(t, true)
	q := NewQueue(db, "encrypt-atomic-owner", nil)
	thumb, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || thumb == nil {
		t.Fatalf("thumbnail claim=%+v err=%v", thumb, err)
	}
	if _, err = db.Exec(`UPDATE post_ingest_task SET status='done',lease_owner=NULL,lease_until=NULL WHERE id=?; UPDATE media_ingest_step SET status='done',lease_owner=NULL,lease_until=NULL WHERE id=?; UPDATE media_ingest_run SET status='processing' WHERE id=?`, thumb.ID, *thumb.StepID, runID); err != nil {
		t.Fatal(err)
	}
	var source string
	if err = db.QueryRow(`SELECT file_path FROM media WHERE id=?`, mediaID).Scan(&source); err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(filepath.Dir(source), "photo.enc")
	data := append([]byte(storageMagic9527ForTest), make([]byte, 32)...)
	data[4], data[5] = 1, 1
	if err = os.WriteFile(encPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,'wrapped','iv',?,'encrypted')`, mediaID, encPath, source); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media SET file_path=? WHERE id=?`, encPath, mediaID); err != nil {
		t.Fatal(err)
	}
	thumbPath, mediumPath := filepath.Join(filepath.Dir(source), "thumb.enc"), filepath.Join(filepath.Dir(source), "medium.enc")
	for _, p := range []string{thumbPath, mediumPath} {
		if err = os.WriteFile(p, []byte("encrypted derivative"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	meta, _ := json.Marshal(map[string]any{"photo": map[string]any{"thumb_path": thumbPath, "medium_path": mediumPath}})
	if _, err = db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, string(meta), mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'photo_thumb','thumb.jpg',?,'tw','ti'),(?,'photo_medium','medium.jpg',?,'mw','mi')`, mediaID, thumbPath, mediaID, mediumPath); err != nil {
		t.Fatal(err)
	}
	task, err := q.Claim(context.Background(), TaskEncrypt)
	if err != nil || task == nil {
		t.Fatalf("encrypt claim=%+v err=%v", task, err)
	}
	var qs, ss, rs, owner string
	var attempts int
	if err = db.QueryRow(`SELECT p.status,s.status,r.status,p.lease_owner,p.attempts FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run r ON r.id=p.ingest_run_id WHERE p.id=?`, task.ID).Scan(&qs, &ss, &rs, &owner, &attempts); err != nil {
		t.Fatal(err)
	}
	if qs != "running" || ss != "running" || rs != "processing" || owner != task.LeaseOwner || attempts != task.Attempts {
		t.Fatalf("fixture q=%s s=%s r=%s owner=%s/%s attempts=%d/%d", qs, ss, rs, owner, task.LeaseOwner, attempts, task.Attempts)
	}
	var selected, recorded, assetStatus string
	if err = db.QueryRow(`SELECT m.file_path,a.enc_path,a.status FROM media m JOIN media_encrypted_assets a ON a.media_id=m.id WHERE m.id=?`, mediaID).Scan(&selected, &recorded, &assetStatus); err != nil {
		t.Fatal(err)
	}
	if selected != recorded || assetStatus != "encrypted" {
		t.Fatalf("selected=%q recorded=%q status=%q", selected, recorded, assetStatus)
	}
	var stepOwner string
	var stepAttempts int
	var superseded sql.NullInt64
	var supersededAt sql.NullString
	if err = db.QueryRow(`SELECT s.lease_owner,s.attempts,r.superseded_by_generation,r.superseded_at FROM media_ingest_step s JOIN media_ingest_run r ON r.id=s.run_id WHERE s.id=?`, *task.StepID).Scan(&stepOwner, &stepAttempts, &superseded, &supersededAt); err != nil {
		t.Fatal(err)
	}
	if stepOwner != task.LeaseOwner || stepAttempts != task.Attempts || superseded.Valid || supersededAt.Valid {
		t.Fatalf("step owner=%s/%s attempts=%d/%d superseded=%v at=%v", stepOwner, task.LeaseOwner, stepAttempts, task.Attempts, superseded, supersededAt)
	}
	var gen, mgen int64
	if err = db.QueryRow(`SELECT p.generation,m.ingest_generation FROM post_ingest_task p JOIN media m ON m.id=p.media_id WHERE p.id=?`, task.ID).Scan(&gen, &mgen); err != nil {
		t.Fatal(err)
	}
	if gen != mgen {
		t.Fatalf("generation=%d media=%d", gen, mgen)
	}
	adapter := NewEncryptAdapter(&encryptorWithDB{recordingEncryptor: &recordingEncryptor{}, db: db})
	atomic, ok := adapter.(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	})
	if !ok {
		t.Fatal("encrypt adapter is not atomic result executor")
	}
	if _, err = db.Exec(`UPDATE post_ingest_task SET retry_round=retry_round+1 WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = atomic.ExecuteWithResult(context.Background(), *task); err == nil {
		t.Fatal("stale retry round committed")
	}
	var staleQueue, staleStep, staleSelection string
	var staleEvidence int
	if err = db.QueryRow(`SELECT p.status,s.status,m.file_path,(SELECT COUNT(*) FROM media_ingest_evidence WHERE step_id=s.id AND kind='encrypt') FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media m ON m.id=p.media_id WHERE p.id=?`, task.ID).Scan(&staleQueue, &staleStep, &staleSelection, &staleEvidence); err != nil {
		t.Fatal(err)
	}
	if staleQueue != "running" || staleStep != "running" || staleSelection != encPath || staleEvidence != 0 {
		t.Fatalf("stale round mutated queue=%s step=%s selection=%s evidence=%d", staleQueue, staleStep, staleSelection, staleEvidence)
	}
	task.RetryRound++
	result, err := atomic.ExecuteWithResult(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("completion=%v", result.Completion)
	}
	var queueStatus, stepStatus, runStatus, mediaState string
	var evidence int
	if err = db.QueryRow(`SELECT p.status,s.status,r.status,m.publication_state,(SELECT COUNT(*) FROM media_ingest_evidence e WHERE e.run_id=? AND e.step_id=s.id AND e.kind='encrypt') FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run r ON r.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.id=?`, runID, task.ID).Scan(&queueStatus, &stepStatus, &runStatus, &mediaState, &evidence); err != nil {
		t.Fatal(err)
	}
	if queueStatus != "done" || stepStatus != "done" || runStatus != "published" || mediaState != "published" || evidence != 1 {
		t.Fatalf("queue=%s step=%s run=%s media=%s evidence=%d", queueStatus, stepStatus, runStatus, mediaState, evidence)
	}
}
func TestThumbnailAtomicFinalizerFencesStaleRetryRound(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	worker := realThumbnailStager(t, db)
	staged, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET retry_round=retry_round+1 WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := commitStagedThumbnail(context.Background(), db, *task, staged); err == nil {
		t.Fatal("stale retry round committed")
	}
	var meta, taskStatus, stepStatus, journalState string
	var evidence, thumbPointers int
	if err := db.QueryRow(`SELECT m.meta_json,p.status,s.status,j.state,(SELECT COUNT(*) FROM media_ingest_evidence WHERE step_id=s.id),(SELECT COUNT(*) FROM media_derived_assets WHERE media_id=m.id) FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media m ON m.id=p.media_id JOIN media_asset_stage_journal j ON j.stage_id=? WHERE p.id=?`, staged.Stage.StageID, task.ID).Scan(&meta, &taskStatus, &stepStatus, &journalState, &evidence, &thumbPointers); err != nil {
		t.Fatal(err)
	}
	if meta != "{}" || taskStatus != "running" || stepStatus != "running" || journalState != "staged" || evidence != 0 || thumbPointers != 0 {
		t.Fatalf("stale round mutated meta=%s task=%s step=%s journal=%s evidence=%d pointers=%d", meta, taskStatus, stepStatus, journalState, evidence, thumbPointers)
	}
	task.RetryRound++
	staged, err = worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatalf("current round stage: %v", err)
	}
	if err := commitStagedThumbnail(context.Background(), db, *task, staged); err != nil {
		t.Fatalf("current round finalizer: %v", err)
	}
	var selected string
	if err := db.QueryRow(`SELECT json_extract(meta_json,'$.photo.thumb_path') FROM media WHERE id=?`, mediaID).Scan(&selected); err != nil || selected == "" {
		t.Fatalf("current round pointer=%q err=%v", selected, err)
	}
}
