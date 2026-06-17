package storage

import (
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestResolveKeyframeProbePathUsesPlainWhenEncCatalog(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	plain := filepath.Join(dir, "movie.mp4")
	enc := filepath.Join(dir, "movie.enc")
	if err := os.WriteFile(plain, []byte("not-really-mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .enc extension + Knox magic so IsEncFile matches catalog path semantics.
	if err := os.WriteFile(enc, append([]byte("9527"), []byte("enc-payload")...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (9, 1, 'f', 't', ?, 'video', 'active')`, enc)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, plain_path, wrapped_dek, iv, status)
		VALUES (9, ?, ?, '00', '00', 'encrypted')`, enc, plain)

	got := ResolveKeyframeProbePath(db, 9, enc)
	if got != plain {
		t.Fatalf("got %q want plain %q", got, plain)
	}
}

func TestResolveKeyframeProbePathKeepsEncWhenPlainMissing(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	enc := filepath.Join(t.TempDir(), "movie.enc")
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, plain_path, wrapped_dek, iv, status)
		VALUES (9, ?, 'C:\\missing.mp4', '00', '00', 'encrypted')`, enc)

	got := ResolveKeyframeProbePath(db, 9, enc)
	if got != enc {
		t.Fatalf("got %q want enc", got)
	}
}
