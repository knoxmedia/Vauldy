package scancoord

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"knox-media/internal/store"
)

type progressClock struct{ now time.Time }

func (c *progressClock) Now() time.Time          { return c.now }
func (c *progressClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newProgressTestWriter(t *testing.T, opts ProgressWriterOptions) (*ProgressWriter, *store.SQLiteMetrics, func() (int64, int64, int64)) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "progress.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lib, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('progress','video',?)`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := lib.LastInsertId()
	task, err := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := task.LastInsertId()
	metrics := &store.SQLiteMetrics{}
	writer := NewProgressWriter(db, metrics, taskID, libraryID, opts)
	read := func() (int64, int64, int64) {
		var processed, failed, added int64
		if err := db.QueryRow(`SELECT processed_count,failed_count,added_count FROM scan_task WHERE id=?`, taskID).Scan(&processed, &failed, &added); err != nil {
			t.Fatal(err)
		}
		return processed, failed, added
	}
	return writer, metrics, read
}

func TestProgressWriter_ThrottlesAtTimeAndFileBoundaries(t *testing.T) {
	clock := &progressClock{now: time.Unix(1000, 0)}
	writer, metrics, read := newProgressTestWriter(t, ProgressWriterOptions{FlushInterval: 500 * time.Millisecond, FileThreshold: 100, Now: clock.Now})
	for i := 0; i < 99; i++ {
		writer.File(fmt.Sprintf("f-%d", i), nil)
	}
	clock.Advance(499 * time.Millisecond)
	if err := writer.Flush(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := read(); p != 0 || metrics.ProgressBatches.Load() != 0 {
		t.Fatalf("before boundary processed=%d batches=%d", p, metrics.ProgressBatches.Load())
	}
	clock.Advance(time.Millisecond)
	if err := writer.Flush(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := read(); p != 99 || metrics.ProgressBatches.Load() != 1 {
		t.Fatalf("time boundary processed=%d batches=%d", p, metrics.ProgressBatches.Load())
	}
	for i := 0; i < 100; i++ {
		writer.File(fmt.Sprintf("g-%d", i), nil)
	}
	if p, _, _ := read(); p != 199 || metrics.ProgressBatches.Load() != 2 {
		t.Fatalf("file boundary processed=%d batches=%d", p, metrics.ProgressBatches.Load())
	}
}

func TestProgressWriter_ForceFlushesCountsBelowThreshold(t *testing.T) {
	clock := &progressClock{now: time.Unix(1000, 0)}
	writer, _, read := newProgressTestWriter(t, ProgressWriterOptions{Now: clock.Now})
	writer.File("bad.mp4", errors.New("probe failed"))
	writer.MediaAdded(42, "movie", "video")
	if err := writer.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if p, f, a := read(); p != 1 || f != 1 || a != 1 {
		t.Fatalf("counts=(%d,%d,%d), want (1,1,1)", p, f, a)
	}
}

func TestProgressWriter_BatchesLogsAtMostOneHundred(t *testing.T) {
	clock := &progressClock{now: time.Unix(1000, 0)}
	writer, metrics, _ := newProgressTestWriter(t, ProgressWriterOptions{FlushInterval: 500 * time.Millisecond, FileThreshold: 10000, LogBatchSize: 100, MaxBufferedLogs: 1000, Now: clock.Now})
	for i := 0; i < 250; i++ {
		writer.Log(ScanLog{FilePath: fmt.Sprintf("f-%d", i), Action: "seen"})
	}
	if err := writer.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := metrics.LogBatches.Load(); got != 3 {
		t.Fatalf("log batches=%d want 3", got)
	}
	if got := writer.BufferedLogs(); got != 0 {
		t.Fatalf("buffered=%d want 0", got)
	}
}

func TestProgressWriter_BoundsBufferAndDropsFailedBatch(t *testing.T) {
	writer, metrics, _ := newProgressTestWriter(t, ProgressWriterOptions{LogBatchSize: 100, MaxBufferedLogs: 1000})
	if _, err := writer.db.Exec(`CREATE TRIGGER reject_scan_log BEFORE INSERT ON scan_log BEGIN SELECT RAISE(ABORT,'reject log'); END`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		writer.Log(ScanLog{FilePath: fmt.Sprintf("f-%d", i), Action: "seen"})
	}
	if got := writer.BufferedLogs(); got > 1000 {
		t.Fatalf("buffered=%d exceeds 1000", got)
	}
	if metrics.LogBatchFailures.Load() == 0 {
		t.Fatal("log failure metric was not incremented")
	}
}

func TestProgressWriter_IsolatesLogFailureFromProgress(t *testing.T) {
	writer, metrics, read := newProgressTestWriter(t, ProgressWriterOptions{})
	if _, err := writer.db.Exec(`CREATE TRIGGER reject_scan_log BEFORE INSERT ON scan_log BEGIN SELECT RAISE(ABORT,'reject log'); END`); err != nil {
		t.Fatal(err)
	}
	writer.File("movie.mp4", nil)
	writer.MediaAdded(7, "movie", "video")
	if err := writer.Flush(context.Background(), true); err != nil {
		t.Fatalf("Flush returned isolated log error: %v", err)
	}
	if p, _, a := read(); p != 1 || a != 1 {
		t.Fatalf("progress=(%d,%d), want (1,1)", p, a)
	}
	if metrics.LogBatchFailures.Load() != 1 {
		t.Fatalf("failures=%d want 1", metrics.LogBatchFailures.Load())
	}
}

func TestProgressWriter_NormalizesBufferAndBatchLimits(t *testing.T) {
	writer, _, _ := newProgressTestWriter(t, ProgressWriterOptions{MaxBufferedLogs: 2000, LogBatchSize: 500})
	if writer.opts.MaxBufferedLogs != 1000 {
		t.Fatalf("MaxBufferedLogs=%d want 1000", writer.opts.MaxBufferedLogs)
	}
	if writer.opts.LogBatchSize != 100 {
		t.Fatalf("LogBatchSize=%d want 100", writer.opts.LogBatchSize)
	}
	writer = NewProgressWriter(writer.db, nil, writer.taskID, writer.libraryID, ProgressWriterOptions{MaxBufferedLogs: 50, LogBatchSize: 100})
	if writer.opts.LogBatchSize != 50 {
		t.Fatalf("LogBatchSize=%d want max buffer 50", writer.opts.LogBatchSize)
	}
}

func TestProgressWriter_CapacityFlushesWithoutEviction(t *testing.T) {
	writer, metrics, _ := newProgressTestWriter(t, ProgressWriterOptions{MaxBufferedLogs: 2000, LogBatchSize: 100})
	for i := 0; i < 1001; i++ {
		writer.Log(ScanLog{FilePath: fmt.Sprintf("capacity-%d", i), Action: "seen"})
		if got := writer.BufferedLogs(); got > 1000 {
			t.Fatalf("buffer exceeded hard cap: %d", got)
		}
	}
	var rows int
	if err := writer.db.QueryRow(`SELECT COUNT(*) FROM scan_log WHERE scan_task_id=?`, writer.taskID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows < 100 {
		t.Fatalf("persisted rows=%d want at least one synchronous batch", rows)
	}
	if got := metrics.DroppedLogs.Load(); got != 0 {
		t.Fatalf("capacity incorrectly dropped %d logs", got)
	}
}

func TestProgressWriter_LogFailureDropsAttemptedBatchOnly(t *testing.T) {
	writer, metrics, _ := newProgressTestWriter(t, ProgressWriterOptions{LogBatchSize: 2, MaxBufferedLogs: 10})
	writer.writeLogs = func(context.Context, []progressLogEntry) error { return errors.New("log db unavailable") }
	writer.Log(ScanLog{FilePath: "one", Action: "seen"})
	writer.Log(ScanLog{FilePath: "two", Action: "seen"})
	if got := writer.BufferedLogs(); got != 0 {
		t.Fatalf("failed attempted batch retained: %d", got)
	}
	if got := metrics.LogBatchFailures.Load(); got != 1 {
		t.Fatalf("failures=%d want 1", got)
	}
	if got := metrics.DroppedLogs.Load(); got != 2 {
		t.Fatalf("dropped=%d want attempted batch size 2", got)
	}
}

func TestProgressWriter_ForceWaitsForFlusherAndIncludesConcurrentWork(t *testing.T) {
	writer, _, read := newProgressTestWriter(t, ProgressWriterOptions{FileThreshold: 100, LogBatchSize: 100})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	realWrite := writer.writeLogs
	writer.writeLogs = func(ctx context.Context, entries []progressLogEntry) error {
		calls++
		if calls == 1 {
			close(started)
			<-release
		}
		return realWrite(ctx, entries)
	}

	writer.File("first", nil)
	firstDone := make(chan error, 1)
	go func() { firstDone <- writer.Flush(context.Background(), true) }()
	<-started

	forceDone := make(chan error, 1)
	go func() { forceDone <- writer.Flush(context.Background(), true) }()
	select {
	case err := <-forceDone:
		t.Fatalf("force returned before active flusher completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	writer.File("second", nil)
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-forceDone; err != nil {
		t.Fatal(err)
	}
	if p, _, _ := read(); p != 2 {
		t.Fatalf("processed=%d want 2", p)
	}
	var rows, distinct int
	if err := writer.db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT file_path) FROM scan_log WHERE scan_task_id=?`, writer.taskID).Scan(&rows, &distinct); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || distinct != 2 {
		t.Fatalf("logs rows=%d distinct=%d want 2,2", rows, distinct)
	}
}

func TestProgressWriter_ReentrantWriteAtCapacityDoesNotDeadlock(t *testing.T) {
	writer, _, _ := newProgressTestWriter(t, ProgressWriterOptions{MaxBufferedLogs: 1, LogBatchSize: 1})
	realWrite := writer.writeLogs
	var once sync.Once
	writer.writeLogs = func(ctx context.Context, entries []progressLogEntry) error {
		once.Do(func() { writer.Log(ScanLog{FilePath: "second", Action: "seen"}) })
		return realWrite(ctx, entries)
	}
	done := make(chan struct{})
	go func() {
		writer.Log(ScanLog{FilePath: "first", Action: "seen"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant write deadlocked at full capacity")
	}
	if err := writer.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	var rows, distinct int
	if err := writer.db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT file_path) FROM scan_log WHERE scan_task_id=?`, writer.taskID).Scan(&rows, &distinct); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || distinct != 2 {
		t.Fatalf("rows=%d distinct=%d want 2,2", rows, distinct)
	}
}
