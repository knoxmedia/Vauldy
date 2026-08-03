package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigratePostIngestTaskEncryptTypeUpgradesLegacyCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.sqlite")
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`DROP TABLE post_ingest_task`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE post_ingest_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    scan_task_id INTEGER,
    task_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'waiting',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    UNIQUE(media_id, task_type),
    CHECK (task_type IN ('poster','preview','keyframe','subtitle','atrack')),
    CHECK (status IN ('waiting','running','done','failed','cancelled'))
)`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library (name,type,path) VALUES ('lib','video','/media')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media (library_id,file_id) VALUES (?,'legacy')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO post_ingest_task (media_id, task_type) VALUES (?, 'poster')`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task (media_id, task_type) VALUES (?, 'encrypt')`, mediaID); err == nil {
		t.Fatal("legacy schema should reject encrypt task_type")
	}

	if err := MigratePostIngestTaskEncryptType(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task (media_id, task_type) VALUES (?, 'encrypt')`, mediaID); err != nil {
		t.Fatalf("migrated schema should accept encrypt task_type: %v", err)
	}
	allows, err := postIngestTaskSchemaAllowsEncrypt(context.Background(), db)
	if err != nil || !allows {
		t.Fatalf("allows=%v err=%v", allows, err)
	}
}

func TestMigratePostIngestTaskEncryptTypeIsIdempotent(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		if err := MigratePostIngestTaskEncryptType(context.Background(), db); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}
