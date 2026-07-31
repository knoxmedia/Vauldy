package subtitle

import (
	"database/sql"
	"testing"

	"knox-media/internal/store"
	_ "modernc.org/sqlite"
)

func TestDeleteSubtitleTask(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'sub','video','/sub');
		INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(1,1,'f1',1),(2,1,'f2',1);
		INSERT INTO subtitle_task(media_id,status) VALUES(1,'failed'),(2,'running');
		INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(1,1,'subtitle','failed',3)`); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	if err := s.DeleteSubtitleTask(1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM subtitle_task WHERE media_id = 1`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expected domain row deleted, n=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=1 AND task_type='subtitle'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expected queue row deleted, n=%d err=%v", n, err)
	}
	if err := s.DeleteSubtitleTask(2); err == nil {
		t.Fatal("expected error deleting running task")
	}
}

func TestCleanupSubtitleTasksFailedSyncsPostIngest(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'sub','video','/sub');
		INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(10,1,'f10',1),(11,1,'f11',2);
		INSERT INTO subtitle_task(media_id,status) VALUES(10,'failed'),(11,'failed');
		INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES
			(10,1,'subtitle','failed',3),
			(11,1,'subtitle','done',3),
			(11,2,'subtitle','failed',3)`); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	n, err := s.CleanupSubtitleTasksFailed()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted domain rows want 2 got %d", n)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(1) FROM subtitle_task`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("domain rows left=%d err=%v", left, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=10 AND task_type='subtitle'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("media 10 queue should be gone, left=%d err=%v", left, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=11 AND generation=2 AND task_type='subtitle'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("media 11 current-gen queue should be gone, left=%d err=%v", left, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=11 AND generation=1 AND task_type='subtitle'`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("prior-gen done should remain, left=%d err=%v", left, err)
	}
}

func TestDeleteSubtitleTaskMinimalSchemaStillWorks(t *testing.T) {
	// Keep a lightweight path for environments without full migrations only if needed;
	// full schema is the product path covered above.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE subtitle_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL UNIQUE,
			status TEXT NOT NULL,
			message TEXT,
			created_at TEXT,
			started_at TEXT,
			finished_at TEXT,
			updated_at TEXT
		);
		CREATE TABLE media(id INTEGER PRIMARY KEY, ingest_generation INTEGER DEFAULT 0);
		CREATE TABLE post_ingest_task(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL,
			generation INTEGER NOT NULL DEFAULT 0,
			task_type TEXT NOT NULL,
			status TEXT NOT NULL,
			max_attempts INTEGER NOT NULL DEFAULT 3
		)`); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	if _, err := db.Exec(`INSERT INTO subtitle_task (media_id, status) VALUES (1, 'failed')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtitleTask(1); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
