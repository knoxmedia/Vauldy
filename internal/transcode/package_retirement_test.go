package transcode

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func openPackageRetirementDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type packageRetirementFixture struct {
	DB         *sql.DB
	UploadDir  string
	SourcePath string
	SourceFP   string
	MediaID    int64
	RunID      int64
	TaskID     int64
	LibraryID  int64
}

func seedPackageRetirementFixture(t *testing.T, db *sql.DB, cleanupFlag int) *packageRetirementFixture {
	t.Helper()
	uploadDir := t.TempDir()
	src := filepath.Join(uploadDir, "movie.mp4")
	if err := os.WriteFile(src, []byte("authoritative-package-source"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := storage.EncryptionSourceFingerprint(src)
	if err != nil {
		t.Fatal(err)
	}
	libRes, err := db.Exec(`INSERT INTO library(name,type,path,drm_enabled,cleanup_local_source_after_package) VALUES('pkg','video',?,1,?)`, uploadDir, cleanupFlag)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation,height) VALUES(?,'f1',?,'video','active','published',1,1080)`, libID, src)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','published','{}',3)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runRes.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'media_visible',1,'done')`, runID, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_plan_completion(run_id,media_id,generation,all_terminal,total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count,completed_at)
VALUES(?,?,1,1,1,1,0,0,1,0,0,0,CURRENT_TIMESTAMP)`, runID, mediaID); err != nil {
		t.Fatal(err)
	}
	taskRes, err := db.Exec(`INSERT INTO package_task(media_id,pipeline_type,status,progress) VALUES(?,'cmaf_drm','waiting',0)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := taskRes.LastInsertId()
	return &packageRetirementFixture{
		DB: db, UploadDir: uploadDir, SourcePath: src, SourceFP: fp,
		MediaID: mediaID, RunID: runID, TaskID: taskID, LibraryID: libID,
	}
}

func TestRunTaskUpsertsPackageRetirementOnCleanupPending(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)

	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	if err := w.RunTask(context.Background(), fx.TaskID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	var status, cleanup string
	if err := db.QueryRow(`SELECT status,source_cleanup_status FROM package_task WHERE id=?`, fx.TaskID).Scan(&status, &cleanup); err != nil {
		t.Fatal(err)
	}
	if status != "done" || cleanup != "pending" {
		t.Fatalf("status=%s cleanup=%s", status, cleanup)
	}
	if _, err := os.Stat(fx.SourcePath); err != nil {
		t.Fatalf("source must remain: %v", err)
	}

	var n int
	var basisKind, state, sourcePath, sourceFP string
	var basisID, pkgTaskID, runID, generation int64
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(basis_kind),''),COALESCE(MAX(basis_id),0),COALESCE(MAX(package_task_id),0),COALESCE(MAX(run_id),0),COALESCE(MAX(generation),0),COALESCE(MAX(source_path),''),COALESCE(MAX(source_fingerprint),''),COALESCE(MAX(state),'') FROM media_plaintext_retirement WHERE media_id=?`, fx.MediaID).
		Scan(&n, &basisKind, &basisID, &pkgTaskID, &runID, &generation, &sourcePath, &sourceFP, &state); err != nil {
		t.Fatal(err)
	}
	if n != 1 || basisKind != "package" || basisID != fx.TaskID || pkgTaskID != fx.TaskID || runID != fx.RunID || generation != 1 || state != "blocked" {
		t.Fatalf("retirement n=%d kind=%s basis=%d pkg=%d run=%d gen=%d state=%s", n, basisKind, basisID, pkgTaskID, runID, generation, state)
	}
	if sourcePath != fx.SourcePath || sourceFP != fx.SourceFP {
		t.Fatalf("identity path=%s fp=%s", sourcePath, sourceFP)
	}
}

func TestRunTaskFailedPackageCreatesNoRetirement(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)

	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   "ffmpeg-does-not-exist",
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	if err := w.RunTask(context.Background(), fx.TaskID); err == nil {
		t.Fatal("expected RunTask failure")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM package_task WHERE id=?`, fx.TaskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("status=%s want failed", status)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=?`, fx.MediaID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("retirement rows=%d want 0", n)
	}
}

func TestRunTaskCleanupSkippedCreatesNoRetirement(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 0)

	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	if err := w.RunTask(context.Background(), fx.TaskID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	var cleanup string
	if err := db.QueryRow(`SELECT source_cleanup_status FROM package_task WHERE id=?`, fx.TaskID).Scan(&cleanup); err != nil {
		t.Fatal(err)
	}
	if cleanup != "skipped" {
		t.Fatalf("cleanup=%s want skipped", cleanup)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=?`, fx.MediaID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("retirement rows=%d want 0", n)
	}
}

func TestUpsertPackageRetirementIdempotentNoDuplicate(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	req := packageRetirementRequest{
		MediaID: fx.MediaID, PackageTaskID: fx.TaskID,
		RunID: fx.RunID, Generation: 1,
		SourcePath: fx.SourcePath, SourceFingerprint: fx.SourceFP,
	}
	if err := upsertPackageRetirementIntentTx(context.Background(), db, req); err != nil {
		t.Fatal(err)
	}
	if err := upsertPackageRetirementIntentTx(context.Background(), db, req); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=? AND generation=1`, fx.MediaID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows=%d want 1", n)
	}
}

func TestUpsertPackageRetirementIdempotentOnMatchingInFlight(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_path,quarantine_evidence_json)
VALUES(?,?,1,?,?,'package',?,NULL,?,0,'deleting','prior-q','{}')`, fx.MediaID, fx.RunID, fx.SourcePath, fx.SourceFP, fx.TaskID, fx.TaskID); err != nil {
		t.Fatal(err)
	}
	req := packageRetirementRequest{
		MediaID: fx.MediaID, PackageTaskID: fx.TaskID,
		RunID: fx.RunID, Generation: 1,
		SourcePath: fx.SourcePath, SourceFingerprint: fx.SourceFP,
	}
	if err := upsertPackageRetirementIntentTx(context.Background(), db, req); err != nil {
		t.Fatalf("matching in-flight must be idempotent: %v", err)
	}
	var state, qPath string
	if err := db.QueryRow(`SELECT state,quarantine_path FROM media_plaintext_retirement WHERE media_id=? AND generation=1`, fx.MediaID).Scan(&state, &qPath); err != nil {
		t.Fatal(err)
	}
	if state != "deleting" || qPath != "prior-q" {
		t.Fatalf("state=%s q=%s (must not regress)", state, qPath)
	}
}

func TestUpsertPackageRetirementFailsClosedOnMismatchedInFlight(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	otherTask, err := db.Exec(`INSERT INTO package_task(media_id,pipeline_type,status) VALUES(?,'cmaf_drm','done')`, fx.MediaID)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := otherTask.LastInsertId()
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json)
VALUES(?,?,1,'other','other-fp','package',?,NULL,?,0,'quarantining','{}')`, fx.MediaID, fx.RunID, otherID, otherID); err != nil {
		t.Fatal(err)
	}
	req := packageRetirementRequest{
		MediaID: fx.MediaID, PackageTaskID: fx.TaskID,
		RunID: fx.RunID, Generation: 1,
		SourcePath: fx.SourcePath, SourceFingerprint: fx.SourceFP,
	}
	err = upsertPackageRetirementIntentTx(context.Background(), db, req)
	if err == nil {
		t.Fatal("expected fail-closed on mismatched in-flight retirement")
	}
	if !errors.Is(err, errPackageRetirementConflict) {
		t.Fatalf("err=%v want conflict", err)
	}
	var state string
	var basisID int64
	if e := db.QueryRow(`SELECT state,basis_id FROM media_plaintext_retirement WHERE media_id=? AND generation=1`, fx.MediaID).Scan(&state, &basisID); e != nil {
		t.Fatal(e)
	}
	if state != "quarantining" || basisID != otherID {
		t.Fatalf("in-flight mutated: state=%s basis=%d", state, basisID)
	}
}

func TestRunTaskPackageCleanupDoesNotDeleteAuthoritativeSource(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	if err := w.RunTask(context.Background(), fx.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fx.SourcePath); err != nil {
		t.Fatalf("package worker must never delete source: %v", err)
	}
	var cleanup string
	if err := db.QueryRow(`SELECT source_cleanup_status FROM package_task WHERE id=?`, fx.TaskID).Scan(&cleanup); err != nil {
		t.Fatal(err)
	}
	if cleanup == "success" || strings.EqualFold(cleanup, "done") {
		t.Fatalf("package must not record cleanup success; got %s", cleanup)
	}
}

// TestRunTaskPackageCleanupNoAuthoritativeGenerationDocumentsSkip documents the
// intentional Phase 1 policy: when publication schema is present but media has
// no matching current media_ingest_run, package stays done+pending with zero
// retirement rows (do not invent a generation).
func TestRunTaskPackageCleanupNoAuthoritativeGenerationDocumentsSkip(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=0 WHERE id=?`, fx.MediaID); err != nil {
		t.Fatal(err)
	}
	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	if err := w.RunTask(context.Background(), fx.TaskID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	var status, cleanup string
	if err := db.QueryRow(`SELECT status,source_cleanup_status FROM package_task WHERE id=?`, fx.TaskID).Scan(&status, &cleanup); err != nil {
		t.Fatal(err)
	}
	if status != "done" || cleanup != "pending" {
		t.Fatalf("status=%s cleanup=%s want done+pending", status, cleanup)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=?`, fx.MediaID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("retirement rows=%d want 0 (no invented generation)", n)
	}
}

func TestRunTaskPackageCleanupMissingSchemaFailsClosed(t *testing.T) {
	db := newPackageWorkerTestDB(t)
	uploadDir := t.TempDir()
	src := filepath.Join(uploadDir, "a.mp4")
	if err := os.WriteFile(src, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library (id, drm_enabled, cleanup_local_source_after_package) VALUES (1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, height) VALUES (301, 1, 'f301', ?, 1080)`, src); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress) VALUES (301, 'cmaf_drm', 'waiting', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    uploadDir,
	}
	err = w.RunTask(context.Background(), taskID)
	if err == nil {
		t.Fatal("cleanup pending without retirement schema must fail handoff")
	}
	if !errors.Is(err, errPackageRetirementSchemaMissing) {
		t.Fatalf("err=%v want schema missing", err)
	}
	var status string
	if e := db.QueryRow(`SELECT status FROM package_task WHERE id=?`, taskID).Scan(&status); e != nil {
		t.Fatal(e)
	}
	if status != "failed" {
		t.Fatalf("status=%s want failed", status)
	}
}

func TestRunTaskPackageCleanupUpsertConflictFailsPackage(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	otherTask, err := db.Exec(`INSERT INTO package_task(media_id,pipeline_type,status) VALUES(?,'cmaf_drm','done')`, fx.MediaID)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := otherTask.LastInsertId()
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json)
VALUES(?,?,1,'other','other-fp','package',?,NULL,?,0,'quarantining','{}')`, fx.MediaID, fx.RunID, otherID, otherID); err != nil {
		t.Fatal(err)
	}
	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	err = w.RunTask(context.Background(), fx.TaskID)
	if err == nil {
		t.Fatal("expected handoff failure on upsert conflict")
	}
	if !errors.Is(err, errPackageRetirementConflict) {
		t.Fatalf("err=%v want conflict", err)
	}
	var status string
	if e := db.QueryRow(`SELECT status FROM package_task WHERE id=?`, fx.TaskID).Scan(&status); e != nil {
		t.Fatal(e)
	}
	if status != "failed" {
		t.Fatalf("status=%s want failed (not done)", status)
	}
	var basisID int64
	var state string
	if e := db.QueryRow(`SELECT basis_id,state FROM media_plaintext_retirement WHERE media_id=? AND generation=1`, fx.MediaID).Scan(&basisID, &state); e != nil {
		t.Fatal(e)
	}
	if basisID != otherID || state != "quarantining" {
		t.Fatalf("in-flight retirement mutated: basis=%d state=%s", basisID, state)
	}
}

func TestRunTaskPackageCleanupGenerationReplaceFailsHandoff(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	t.Cleanup(func() { packageHandoffHook = nil })
	packageHandoffHook = func(mediaID int64, tx store.SQLExecutor) {
		if _, err := tx.ExecContext(context.Background(), `UPDATE media_ingest_run SET superseded_at=CURRENT_TIMESTAMP WHERE id=?`, fx.RunID); err != nil {
			t.Fatalf("supersede run: %v", err)
		}
		if _, err := tx.ExecContext(context.Background(), `UPDATE media SET ingest_generation=2 WHERE id=?`, mediaID); err != nil {
			t.Fatalf("bump generation: %v", err)
		}
		// Intentionally leave gen=2 without a matching non-superseded run, or attach
		// a replacement run that would be the wrong packaging generation.
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,2,'repair','processing','{}',3)`, mediaID); err != nil {
			t.Fatalf("insert gen2 run: %v", err)
		}
	}
	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	err := w.RunTask(context.Background(), fx.TaskID)
	if err == nil {
		t.Fatal("generation replace mid-flight must fail handoff")
	}
	if !errors.Is(err, errPackageRetirementGeneration) {
		t.Fatalf("err=%v want generation fence", err)
	}
	var status string
	if e := db.QueryRow(`SELECT status FROM package_task WHERE id=?`, fx.TaskID).Scan(&status); e != nil {
		t.Fatal(e)
	}
	if status != "failed" {
		t.Fatalf("status=%s want failed", status)
	}
	var n int
	if e := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=?`, fx.MediaID).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatalf("must not attach retirement to wrong generation; rows=%d", n)
	}
}

// TestRunTaskPackageCleanupGenerationZeroToNFailsHandoff ensures a captured start
// generation of 0 is still a fence baseline: mid-flight 0→N must not upsert onto N.
func TestRunTaskPackageCleanupGenerationZeroToNFailsHandoff(t *testing.T) {
	db := openPackageRetirementDB(t)
	fx := seedPackageRetirementFixture(t, db, 1)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=0 WHERE id=?`, fx.MediaID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { packageHandoffHook = nil })
	packageHandoffHook = func(mediaID int64, tx store.SQLExecutor) {
		if _, err := tx.ExecContext(context.Background(), `UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID); err != nil {
			t.Fatalf("bump generation 0→1: %v", err)
		}
		// Reuse the existing gen=1 run (already seeded) so handoff would otherwise upsert.
	}
	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    fx.UploadDir,
	}
	err := w.RunTask(context.Background(), fx.TaskID)
	if err == nil {
		t.Fatal("0→N mid-flight must fail handoff")
	}
	if !errors.Is(err, errPackageRetirementGeneration) {
		t.Fatalf("err=%v want generation fence", err)
	}
	var status string
	if e := db.QueryRow(`SELECT status FROM package_task WHERE id=?`, fx.TaskID).Scan(&status); e != nil {
		t.Fatal(e)
	}
	if status != "failed" {
		t.Fatalf("status=%s want failed", status)
	}
	var n int
	if e := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=?`, fx.MediaID).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatalf("must not upsert retirement onto mid-flight gen N; rows=%d", n)
	}
}
