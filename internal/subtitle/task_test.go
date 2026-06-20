package subtitle

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDeleteSubtitleTask(t *testing.T) {
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
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM subtitle_task WHERE media_id = 1`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expected row deleted, n=%d err=%v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO subtitle_task (media_id, status) VALUES (2, 'running')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtitleTask(2); err == nil {
		t.Fatal("expected error deleting running task")
	}
}
