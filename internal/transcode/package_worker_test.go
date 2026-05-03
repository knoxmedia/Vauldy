package transcode

import (
	"context"
	"database/sql"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newPackageWorkerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "package-worker.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
CREATE TABLE library (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	drm_enabled INTEGER DEFAULT 0,
	encryption_mode TEXT DEFAULT 'drm',
	cleanup_local_source_after_package INTEGER DEFAULT 0
);
CREATE TABLE media (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	library_id INTEGER,
	file_id TEXT,
	file_path TEXT,
	height INTEGER
);
CREATE TABLE package_task (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	media_id INTEGER NOT NULL,
	pipeline_type TEXT NOT NULL,
	status TEXT DEFAULT 'waiting',
	progress INTEGER DEFAULT 0
	,output_path TEXT
	,drm_status TEXT
	,source_cleanup_status TEXT DEFAULT 'pending'
	,error_message TEXT
	,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE drm_asset (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	media_id INTEGER NOT NULL UNIQUE,
	kid TEXT NOT NULL,
	key_ref TEXT NOT NULL,
	manifest_path TEXT NOT NULL,
	license_policy_json TEXT DEFAULT '{}',
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func TestEnqueuePackageTaskWhenLibraryDRMEnabled(t *testing.T) {
	t.Parallel()
	db := newPackageWorkerTestDB(t)

	if _, err := db.Exec(`INSERT INTO library (id, drm_enabled) VALUES (1, 1), (2, 0)`); err != nil {
		t.Fatalf("insert libraries: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, height) VALUES (101, 1, 'f101', 'E:/v/101.mp4', 1080), (102, 2, 'f102', 'E:/v/102.mp4', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	w := &PackageWorker{DB: db}
	taskID, err := w.EnqueueForMedia(101)
	if err != nil {
		t.Fatalf("enqueue drm-enabled media: %v", err)
	}
	if taskID <= 0 {
		t.Fatalf("task id should be > 0, got %d", taskID)
	}

	taskID2, err := w.EnqueueForMedia(102)
	if err != nil {
		t.Fatalf("enqueue drm-disabled media: %v", err)
	}
	if taskID2 != 0 {
		t.Fatalf("task id should be 0 for drm-disabled media, got %d", taskID2)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM package_task WHERE media_id = ? AND pipeline_type = 'cmaf_drm'`, 101).Scan(&n); err != nil {
		t.Fatalf("count task: %v", err)
	}
	if n != 1 {
		t.Fatalf("task count=%d want 1", n)
	}
}

func TestEnqueuePackageTaskReusesRunningTask(t *testing.T) {
	t.Parallel()
	db := newPackageWorkerTestDB(t)
	if _, err := db.Exec(`INSERT INTO library (id, drm_enabled) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, height) VALUES (301, 1, 'f301', 'E:/v/301.mp4', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress) VALUES (301, 'cmaf_drm', 'running', 20)`)
	if err != nil {
		t.Fatalf("seed running task: %v", err)
	}
	existingID, _ := res.LastInsertId()

	w := &PackageWorker{DB: db}
	gotID, err := w.EnqueueForMedia(301)
	if err != nil {
		t.Fatalf("enqueue err: %v", err)
	}
	if gotID != existingID {
		t.Fatalf("got task id=%d want existing id=%d", gotID, existingID)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM package_task WHERE media_id = ?`, 301).Scan(&n); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if n != 1 {
		t.Fatalf("task rows=%d want 1", n)
	}
}

func TestRunTaskUpdatesPackageStatusAndAsset(t *testing.T) {
	t.Parallel()
	db := newPackageWorkerTestDB(t)

	uploadDir := t.TempDir()
	srcFile := filepath.Join(uploadDir, "a.mp4")
	if err := os.WriteFile(srcFile, []byte("video"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO library (id, drm_enabled, cleanup_local_source_after_package) VALUES (1, 1, 1)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, height) VALUES (201, 1, 'f201', ?, 1080)`, srcFile); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress) VALUES (201, 'cmaf_drm', 'waiting', 0)`)
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

	var status, drmStatus, cleanupStatus sql.NullString
	var progress int
	var outPath sql.NullString
	if err := db.QueryRow(`SELECT status, progress, drm_status, source_cleanup_status, output_path FROM package_task WHERE id = ?`, taskID).
		Scan(&status, &progress, &drmStatus, &cleanupStatus, &outPath); err != nil {
		t.Fatalf("query package task: %v", err)
	}
	if status.String != "done" || progress != 100 || drmStatus.String != "done" || cleanupStatus.String != "success" {
		t.Fatalf("unexpected task status=%s progress=%d drm=%s cleanup=%s", status.String, progress, drmStatus.String, cleanupStatus.String)
	}
	if !outPath.Valid || !strings.HasSuffix(strings.ToLower(outPath.String), "master.m3u8") {
		t.Fatalf("unexpected output path: %#v", outPath)
	}
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("expected source to be deleted, stat err=%v", err)
	}

	var kid, keyRef string
	if err := db.QueryRow(`SELECT kid, key_ref FROM drm_asset WHERE media_id = ?`, 201).Scan(&kid, &keyRef); err != nil {
		t.Fatalf("query drm_asset: %v", err)
	}
	if strings.TrimSpace(kid) == "" || strings.TrimSpace(keyRef) == "" {
		t.Fatalf("invalid drm asset kid=%q key_ref=%q", kid, keyRef)
	}
}

func TestRunTaskSkipsWhenAlreadyRunning(t *testing.T) {
	t.Parallel()
	db := newPackageWorkerTestDB(t)
	if _, err := db.Exec(`INSERT INTO library (id, drm_enabled, cleanup_local_source_after_package) VALUES (1, 1, 0)`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, height) VALUES (401, 1, 'f401', 'E:/v/401.mp4', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress, drm_status) VALUES (401, 'cmaf_drm', 'running', 33, 'running')`)
	if err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	taskID, _ := res.LastInsertId()

	w := &PackageWorker{
		DB:           db,
		FFmpegPath:   "ffmpeg-not-exists",
		TranscodeDir: t.TempDir(),
		UploadDir:    t.TempDir(),
	}
	if err := w.RunTask(context.Background(), taskID); err != nil {
		t.Fatalf("RunTask should skip running task, got err: %v", err)
	}

	var status string
	var progress int
	if err := db.QueryRow(`SELECT status, progress FROM package_task WHERE id = ?`, taskID).Scan(&status, &progress); err != nil {
		t.Fatalf("query task: %v", err)
	}
	if status != "running" || progress != 33 {
		t.Fatalf("unexpected task mutated status=%s progress=%d", status, progress)
	}
}

func TestRunCMAFHLSIncludesCENCEncryptionArgs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg-args.log")
	ffmpegPath := writeMockFFmpegRunnerWithArgsLog(t, argsLog)
	w := &PackageWorker{
		FFmpegPath: ffmpegPath,
	}

	outDir := filepath.Join(dir, "out")
	_, err := w.runCMAFHLS(context.Background(), 0, 0, filepath.Join(dir, "input.mp4"), outDir, []Rendition{
		{Name: "360p", Height: 360, VideoRate: "850k", AudioRate: "96k"},
	}, strings.Repeat("a", 32), strings.Repeat("b", 32))
	if err != nil {
		t.Fatalf("runCMAFHLS: %v", err)
	}

	raw, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	args := string(raw)
	mustContain := []string{
		"-encryption_scheme",
		"cenc-aes-ctr",
		"-encryption_key",
		strings.Repeat("a", 32),
		"-encryption_kid",
		strings.Repeat("b", 32),
	}
	for _, m := range mustContain {
		if !strings.Contains(args, m) {
			t.Fatalf("ffmpeg args missing %q, got: %s", m, args)
		}
	}
}

func TestHealLegacyInitFilesRepairsMissingInitFromWorkingDir(t *testing.T) {
	t.Parallel()
	db := newPackageWorkerTestDB(t)
	base := t.TempDir()
	outDir := filepath.Join(base, "drm", "f1", "720p")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir outDir: %v", err)
	}
	master := filepath.Join(outDir, "master.m3u8")
	variant := filepath.Join(outDir, "720p.m3u8")
	if err := os.WriteFile(master, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("write master: %v", err)
	}
	if err := os.WriteFile(variant, []byte("#EXTM3U\n#EXT-X-MAP:URI=\"720p_init.mp4\"\n"), 0o644); err != nil {
		t.Fatalf("write variant: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', ?)`, master); err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	legacy := filepath.Join(cwd, "720p_init.mp4")
	if err := os.WriteFile(legacy, []byte("legacy-init"), 0o644); err != nil {
		t.Fatalf("write legacy init: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(legacy) })

	w := &PackageWorker{DB: db}
	scanned, fixed, err := w.HealLegacyInitFiles()
	if err != nil {
		t.Fatalf("HealLegacyInitFiles: %v", err)
	}
	if scanned != 1 {
		t.Fatalf("scanned=%d want 1", scanned)
	}
	if fixed != 1 {
		t.Fatalf("fixed=%d want 1", fixed)
	}
	target := filepath.Join(outDir, "720p_init.mp4")
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read repaired init: %v", err)
	}
	if string(raw) != "legacy-init" {
		t.Fatalf("unexpected repaired init content=%q", string(raw))
	}
}

func TestRepairBrokenTasksMarksMissingOutputFailed(t *testing.T) {
	t.Parallel()
	db := newPackageWorkerTestDB(t)
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress, drm_status, output_path) VALUES (1, 'cmaf_drm', 'done', 100, 'done', ?)`, filepath.Join(t.TempDir(), "missing", "master.m3u8")); err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	w := &PackageWorker{DB: db}
	scanned, broken, retried, err := w.RepairBrokenTasks(context.Background(), 10, false)
	if err != nil {
		t.Fatalf("RepairBrokenTasks: %v", err)
	}
	if scanned != 1 || broken != 1 || retried != 0 {
		t.Fatalf("unexpected result scanned=%d broken=%d retried=%d", scanned, broken, retried)
	}
	var status, drmStatus string
	var progress int
	var errMsg string
	if err := db.QueryRow(`SELECT status, progress, drm_status, COALESCE(error_message,'') FROM package_task ORDER BY id DESC LIMIT 1`).Scan(&status, &progress, &drmStatus, &errMsg); err != nil {
		t.Fatalf("query package_task: %v", err)
	}
	if status != "failed" || progress != 0 || drmStatus != "failed" {
		t.Fatalf("unexpected status=%s progress=%d drm=%s", status, progress, drmStatus)
	}
	if !strings.Contains(errMsg, "incomplete or missing") {
		t.Fatalf("unexpected error message: %q", errMsg)
	}
}

func writeMockFFmpegRunnerWithArgsLog(t *testing.T, argsLogPath string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "ffmpeg-args.bat")
		content := "@echo off\r\n" +
			"setlocal EnableDelayedExpansion\r\n" +
			"set \"out=\"\r\n" +
			"set \"all=\"\r\n" +
			":next\r\n" +
			"if \"%~1\"==\"\" goto done\r\n" +
			"set \"all=!all! %~1\"\r\n" +
			"set \"out=%~1\"\r\n" +
			"shift\r\n" +
			"goto next\r\n" +
			":done\r\n" +
			"echo !all!> \"" + argsLogPath + "\"\r\n" +
			"echo #EXTM3U> \"%out%\"\r\n" +
			"exit /b 0\r\n"
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatalf("write args runner: %v", err)
		}
		return p
	}
	p := filepath.Join(dir, "ffmpeg-args.sh")
	content := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"all=\"\"\n" +
		"for arg in \"$@\"; do all=\"$all $arg\"; out=\"$arg\"; done\n" +
		"echo \"$all\" > \"" + path.Clean(argsLogPath) + "\"\n" +
		"echo \"#EXTM3U\" > \"$out\"\n" +
		"exit 0\n"
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("write args runner: %v", err)
	}
	return p
}
