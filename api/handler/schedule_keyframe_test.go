package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

func TestScheduledKeyframeUsesUnifiedPostIngestQueue(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scheduled-keyframe.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('lib','movie',?)`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := lib.LastInsertId()
	for _, fileType := range []string{"video", "audio"} {
		if _, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status) VALUES(?,?,?,?,?,'active')`, libraryID, fileType, fileType, fileType, fileType); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	msg, err := h.executeScheduledTask(context.Background(), "keyframe_process", map[string]any{"library_id": float64(libraryID), "limit": float64(10)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1") {
		t.Fatalf("message=%q", msg)
	}
	var queued, domain int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE task_type=?`, postingest.TaskKeyframe).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM keyframe_task WHERE status='waiting'`).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || domain != 1 {
		t.Fatalf("queued=%d domain=%d", queued, domain)
	}
}

func TestScheduledKeyframeSourceHasNoDirectWorkerOrBackground(t *testing.T) {
	raw, err := os.ReadFile("schedule_task.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, `case "keyframe_process":`)
	end := strings.Index(source[start:], `case "lyric_process":`)
	if start < 0 || end < 0 {
		t.Fatal("keyframe scheduled case not found")
	}
	block := source[start : start+end]
	for _, forbidden := range []string{"RunBatch(", "RunOne(", "context.Background()"} {
		if strings.Contains(block, forbidden) {
			t.Fatalf("keyframe scheduled case contains %q", forbidden)
		}
	}
	if !strings.Contains(block, "enqueueScheduledPostIngest") || !strings.Contains(block, "postingest.TaskKeyframe") {
		t.Fatalf("keyframe scheduled case bypasses unified queue:\n%s", block)
	}
}

func TestScheduledLyricUsesPassedContext(t *testing.T) {
	raw, err := os.ReadFile("schedule_task.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, `case "lyric_process":`)
	end := strings.Index(source[start:], "default:")
	if start < 0 || end < 0 {
		t.Fatal("lyric scheduled case not found")
	}
	block := source[start : start+end]
	if strings.Contains(block, "context.Background()") || !strings.Contains(block, "RunBatch(ctx, limit)") {
		t.Fatalf("lyric case detaches context:\n%s", block)
	}
}
