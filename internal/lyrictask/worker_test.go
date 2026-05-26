package lyrictask

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEnsurePendingIfNoLyrics_skipsWhenSidecarExists(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	lrc := filepath.Join(dir, "song.lrc")
	if err := os.WriteFile(audio, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lrc, []byte("[00:01.00]hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE library (id INTEGER PRIMARY KEY, type TEXT);
CREATE TABLE media (id INTEGER PRIMARY KEY, library_id INTEGER, file_type TEXT, file_path TEXT, meta_json TEXT);
CREATE TABLE lyric_task (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  media_id INTEGER NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'pending',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO library (id, type) VALUES (1, 'music');
INSERT INTO media (id, library_id, file_type, file_path, meta_json) VALUES (10, 1, 'audio', ?, '');
`, audio); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(db, dir, "", nil)
	if err := w.EnsurePendingIfNoLyrics(10, "audio"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM lyric_task WHERE media_id = 10`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected no task when lrc exists, got %d", n)
	}
}

func TestEnsurePendingIfNoLyrics_enqueuesForMusicAudio(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(audio, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE library (id INTEGER PRIMARY KEY, type TEXT);
CREATE TABLE media (id INTEGER PRIMARY KEY, library_id INTEGER, file_type TEXT, file_path TEXT, meta_json TEXT);
CREATE TABLE lyric_task (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  media_id INTEGER NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'pending',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO library (id, type) VALUES (1, 'music');
INSERT INTO media (id, library_id, file_type, file_path, meta_json) VALUES (11, 1, 'audio', ?, '');
`, audio); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(db, dir, "", nil)
	if err := w.EnsurePendingIfNoLyrics(11, "audio"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM lyric_task WHERE media_id = 11`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status=%q want pending", status)
	}
}
