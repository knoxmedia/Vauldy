package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"knox-media/internal/store"
)

func TestRunSmallWorkloadConvergesWithinBudgets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result, err := run(ctx, Options{
		Media:         10,
		HoldWriteLock: time.Millisecond,
		Timeout:       20 * time.Second,
		ExecutorDelay: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DirectFFmpeg != 0 {
		t.Fatalf("direct ffmpeg starts = %d, want 0", result.DirectFFmpeg)
	}
	if result.PeakGlobal > result.GlobalLimit || result.PeakPoster > result.PosterLimit || result.PeakPreview > result.PreviewLimit {
		t.Fatalf("budget exceeded: peaks global/poster/preview=%d/%d/%d limits=%d/%d/%d", result.PeakGlobal, result.PeakPoster, result.PeakPreview, result.GlobalLimit, result.PosterLimit, result.PreviewLimit)
	}
	if result.Duplicates != 0 {
		t.Fatalf("duplicate executions = %d, want 0", result.Duplicates)
	}
	if got := result.Statuses["done"]; got != 50 {
		t.Fatalf("done tasks = %d, want 50; statuses=%v", got, result.Statuses)
	}
	for _, status := range []string{"waiting", "running", "failed", "cancelled"} {
		if result.Statuses[status] != 0 {
			t.Fatalf("status %s = %d, want 0; statuses=%v", status, result.Statuses[status], result.Statuses)
		}
	}
	if result.GoroutinePeak < result.GoroutineBaseline {
		t.Fatalf("goroutine peak %d below baseline %d", result.GoroutinePeak, result.GoroutineBaseline)
	}
	if result.GoroutineFinal > result.GoroutineBaseline+10 {
		t.Fatalf("goroutines did not converge: baseline=%d final=%d peak=%d", result.GoroutineBaseline, result.GoroutineFinal, result.GoroutinePeak)
	}
}

func TestValidateResultRejectsUnconvergedGoroutines(t *testing.T) {
	result := Result{
		GlobalLimit: 4, PosterLimit: 2, PreviewLimit: 1,
		Statuses:          map[string]int{"done": 60},
		GoroutineBaseline: 1, GoroutinePeak: 20, GoroutineFinal: 12,
	}
	if err := validateResult(result, 60, 0, 0); err == nil {
		t.Fatal("validateResult accepted final goroutines above baseline allowance")
	}
}

func TestLoadDuplicateTasksReportsDatabaseRows(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "duplicates.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	library, _ := db.Exec(`INSERT INTO library(name,type,path) VALUES('duplicates','video','x')`)
	libraryID, _ := library.LastInsertId()
	for i := 0; i < 100; i++ {
		media, err := db.Exec(`INSERT INTO media(library_id,file_id,file_type) VALUES(?,?,'video')`, libraryID, fmt.Sprintf("m-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		mediaID, _ := media.LastInsertId()
		for _, taskType := range []string{"poster", "preview", "keyframe", "subtitle", "atrack"} {
			if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type) VALUES(?,?)`, mediaID, taskType); err != nil {
				t.Fatal(err)
			}
		}
	}
	duplicates, total, err := loadDuplicateTasks(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if duplicates != 0 || total != 500 {
		t.Fatalf("duplicates=%d total=%d want 0/500", duplicates, total)
	}
}
