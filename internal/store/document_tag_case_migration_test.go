package store

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"path/filepath"
	"testing"
)

func TestOpenSQLiteReconcilesDocumentTagsCaseInsensitively(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-document-tags.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec(`CREATE TABLE document_tag(id INTEGER PRIMARY KEY AUTOINCREMENT, media_id INTEGER NOT NULL, tag TEXT NOT NULL, UNIQUE(media_id,tag)); INSERT INTO document_tag(media_id,tag) VALUES(1,'Alpha'),(1,'alpha'),(1,'BETA')`); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT tag FROM document_tag WHERE media_id=1 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatal(err)
		}
		tags = append(tags, tag)
	}
	if len(tags) != 2 || tags[0] != "Alpha" || tags[1] != "BETA" {
		t.Fatalf("tags=%v want [Alpha BETA]", tags)
	}
	if _, err = db.Exec(`INSERT INTO document_tag(media_id,tag) VALUES(1,'ALPHA')`); err == nil {
		t.Fatal("expected NOCASE uniqueness violation")
	}
}
