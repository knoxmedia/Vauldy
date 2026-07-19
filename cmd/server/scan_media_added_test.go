package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/scancoord"
	"knox-media/internal/scanner"
	"knox-media/internal/store"
	"knox-media/pkg/ffprobe"
)

type recordingMediaTaskEnqueuer struct {
	delegate *postingest.Enqueuer
	calls    atomic.Int64
	active   atomic.Int64
	peak     atomic.Int64
}

func (e *recordingMediaTaskEnqueuer) EnqueueMedia(ctx context.Context, mediaID int64, scanTaskID *int64, fileType string) ([]postingest.TaskType, error) {
	n := e.active.Add(1)
	defer e.active.Add(-1)
	for old := e.peak.Load(); n > old && !e.peak.CompareAndSwap(old, n); old = e.peak.Load() {
	}
	e.calls.Add(1)
	return e.delegate.EnqueueMedia(ctx, mediaID, scanTaskID, fileType)
}

func TestScan100MediaEnqueuesWithoutFFmpegOrGoroutineFanout(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "production-scan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	res, err := db.Exec(`INSERT INTO library(name,type,path,preview_extract,encrypted_assets_enabled) VALUES('production','video',?,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(root, "video-"+strconv.Itoa(i)+".mp4"), []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	yes := true
	cfg := &config.Config{Subtitle: config.SubtitleProcessingConfig{AutoOnScan: &yes}, ATrack: config.ATrackConfig{AutoOnScan: &yes}, EncryptedAssets: config.EncryptedAssetsConfig{Enabled: &yes}}
	recorder := &recordingMediaTaskEnqueuer{delegate: postingest.NewEnqueuer(db, cfg, nil)}
	var probes atomic.Int64
	sc := &scanner.Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		probes.Add(1)
		return &ffprobe.Summary{}, nil
	}}
	coordinator, err := scancoord.New(db, scancoord.Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "production-test", Scanner: sc, OnMediaAdded: scancoord.MediaAddedFunc(postingest.NewScanMediaAddedEnqueueCallback(recorder))})
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	result, err := coordinator.Submit(context.Background(), scancoord.ScanRequest{LibraryID: libraryID, Source: scancoord.SourceManual, Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	waitForScanTask(t, db, result.TaskID)
	if recorder.calls.Load() != 100 || recorder.peak.Load() != 1 || probes.Load() != 100 {
		t.Fatalf("calls=%d peak=%d probes=%d want 100,1,100", recorder.calls.Load(), recorder.peak.Load(), probes.Load())
	}
	assertPostIngestOwnership(t, db, result.TaskID, 600)

	second, err := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	secondTaskID, _ := second.LastInsertId()
	rows, err := db.Query(`SELECT id,file_type FROM media WHERE library_id=?`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	repeat := scancoord.MediaAddedFunc(postingest.NewScanMediaAddedEnqueueCallback(recorder))
	for rows.Next() {
		var mediaID int64
		var fileType string
		if err := rows.Scan(&mediaID, &fileType); err != nil {
			t.Fatal(err)
		}
		if err := repeat(context.Background(), secondTaskID, mediaID, "", fileType); err != nil {
			t.Fatal(err)
		}
	}
	_ = rows.Close()
	assertPostIngestOwnership(t, db, result.TaskID, 600)
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+5 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if delta := runtime.NumGoroutine() - before; delta > 5 {
		_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 1)
		t.Fatalf("goroutine delta=%d want <=5 after convergence wait", delta)
	}
}

func waitForScanTask(t *testing.T, db *sql.DB, taskID int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var status string
		if err := db.QueryRow(`SELECT status FROM scan_task WHERE id=?`, taskID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "done" {
			return
		}
		if status == "failed" || status == "cancelled" {
			t.Fatalf("scan status=%s", status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan task %d timed out in %s", taskID, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertPostIngestOwnership(t *testing.T, db *sql.DB, taskID int64, want int) {
	t.Helper()
	var total, owned int
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN scan_task_id=? THEN 1 ELSE 0 END),0) FROM post_ingest_task`, taskID).Scan(&total, &owned); err != nil {
		t.Fatal(err)
	}
	if total != want || owned != want {
		t.Fatalf("rows=%d owned=%d want %d", total, owned, want)
	}
	for _, typ := range []postingest.TaskType{postingest.TaskPoster, postingest.TaskPreview, postingest.TaskKeyframe, postingest.TaskSubtitle, postingest.TaskAtrack, postingest.TaskEncrypt} {
		var count, typeOwned int
		if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN scan_task_id=? THEN 1 ELSE 0 END),0) FROM post_ingest_task WHERE task_type=?`, taskID, typ).Scan(&count, &typeOwned); err != nil {
			t.Fatal(err)
		}
		if count != 100 || typeOwned != 100 {
			t.Fatalf("type=%s rows=%d owned=%d want 100", typ, count, typeOwned)
		}
	}
}
