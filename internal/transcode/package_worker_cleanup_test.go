package transcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupRunsOnlyForUploadLocalPath(t *testing.T) {
	t.Parallel()
	base := filepath.Clean(`E:\uploads`)

	inUpload := filepath.Join(base, "movies", "a.mp4")
	if !shouldCleanup(base, inUpload) {
		t.Fatalf("expected cleanup allowed for upload path")
	}

	outside := filepath.Clean(`E:\external\movies\a.mp4`)
	if shouldCleanup(base, outside) {
		t.Fatalf("expected cleanup denied for external path")
	}
}

func TestRunTaskSkipsCleanupForSourceOutsideUploadPath(t *testing.T) {
	t.Parallel()
	db := newPackageWorkerTestDB(t)

	uploadDir := t.TempDir()
	srcFile := filepath.Join(t.TempDir(), "authoritative.mp4")
	if err := os.WriteFile(srcFile, []byte("video"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO library (id, drm_enabled, cleanup_local_source_after_package) VALUES (1, 1, 1)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, height) VALUES (202, 1, 'f202', ?, 1080)`, srcFile); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress) VALUES (202, 'cmaf_drm', 'waiting', 0)`)
	if err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	taskID, _ := res.LastInsertId()

	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   writeMockFFmpegRunner(t, false),
		TranscodeDir: t.TempDir(),
		UploadDir:    uploadDir,
	}
	if err := w.RunTask(context.Background(), taskID); err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	var status, drmStatus, cleanupStatus, outPath string
	var progress int
	if err := db.QueryRow(`SELECT status, progress, drm_status, source_cleanup_status, output_path FROM package_task WHERE id = ?`, taskID).
		Scan(&status, &progress, &drmStatus, &cleanupStatus, &outPath); err != nil {
		t.Fatalf("query package task: %v", err)
	}
	if status != "done" || progress != 100 || drmStatus != "done" || cleanupStatus != "skipped" {
		t.Fatalf("unexpected task status=%s progress=%d drm=%s cleanup=%s", status, progress, drmStatus, cleanupStatus)
	}
	if !strings.HasSuffix(strings.ToLower(outPath), "master.m3u8") {
		t.Fatalf("unexpected output path: %q", outPath)
	}
	if _, err := os.Stat(srcFile); err != nil {
		t.Fatalf("expected authoritative source to remain, stat err=%v", err)
	}

	var kid, keyRef string
	if err := db.QueryRow(`SELECT kid, key_ref FROM drm_asset WHERE media_id = ?`, 202).Scan(&kid, &keyRef); err != nil {
		t.Fatalf("query drm_asset: %v", err)
	}
	if strings.TrimSpace(kid) == "" || strings.TrimSpace(keyRef) == "" {
		t.Fatalf("invalid drm asset kid=%q key_ref=%q", kid, keyRef)
	}
}
