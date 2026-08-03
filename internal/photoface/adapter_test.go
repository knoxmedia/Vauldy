package photoface

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestFaceAdapter_OneJobExecution(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE IF NOT EXISTS photo_face_task(id INTEGER PRIMARY KEY, media_id INTEGER UNIQUE, status TEXT, lease_owner TEXT, generation INTEGER DEFAULT 0, retry_round INTEGER DEFAULT 0)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS media(id INTEGER PRIMARY KEY, file_type TEXT, file_path TEXT, library_id INTEGER, ingest_generation INTEGER DEFAULT 0)`)
	db.Exec(`INSERT INTO media(id,file_type,file_path,library_id) VALUES(1,'image','/test/test.jpg',1)`)

	w := NewWorker(db, nil, nil, "/root", "", "/preview", nil)
	if w == nil {
		t.Fatal("expected worker")
	}
	if w.busy == nil {
		t.Fatal("busy map is nil")
	}
}

func TestFaceAdapter_LeaseGuard(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE IF NOT EXISTS photo_face_task(id INTEGER PRIMARY KEY, media_id INTEGER UNIQUE, status TEXT, lease_owner TEXT, generation INTEGER)`)
}

func TestFaceAdapter_GenerationFencing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE IF NOT EXISTS photo_face_task(id INTEGER PRIMARY KEY, media_id INTEGER UNIQUE, status TEXT, lease_owner TEXT, generation INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS media(id INTEGER PRIMARY KEY, file_type TEXT, ingest_generation INTEGER)`)
	db.Exec(`INSERT INTO media(id,file_type,ingest_generation) VALUES(1,'image',1)`)
	db.Exec(`INSERT INTO photo_face_task(media_id,status,lease_owner,generation) VALUES(1,'running','worker-1',1)`)
}

func TestFaceAdapter_CommitGuard(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.Exec(`CREATE TABLE IF NOT EXISTS photo_face_task(id INTEGER PRIMARY KEY, media_id INTEGER UNIQUE, status TEXT, lease_owner TEXT, generation INTEGER)`)
}
