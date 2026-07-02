package storage

import (
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestPreferredFFmpegPathPrefersPlaintext(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	plain := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(plain, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	enc := plain + ".enc"
	if err := os.WriteFile(enc, []byte("cipher"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (7, 1, 'f', 't', ?, 'video', 'active')`, enc)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (7, ?, '00', '00', ?, 'encrypted')`, enc, plain)

	got := PreferredFFmpegPath(db, 7, 1, enc)
	if got != plain {
		t.Fatalf("expected plaintext %q, got %q", plain, got)
	}
}

func TestPosterSeekPreInputSkipsEnc(t *testing.T) {
	if pre := PosterSeekPreInput(44, "/data/x.enc"); pre != nil {
		t.Fatalf("expected nil pre-input for .enc, got %v", pre)
	}
	if pre := PosterSeekPreInput(44, "/data/x.mp4"); len(pre) != 2 || pre[0] != "-ss" || pre[1] != "44" {
		t.Fatalf("unexpected pre-input: %v", pre)
	}
}

func TestResolveMediaAbsolutePath(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib := filepath.Join(t.TempDir(), "libroot")
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (2, 'lib', 'video', ?)`, lib)
	got := ResolveMediaAbsolutePath(db, 2, "sub/video.mp4")
	want := filepath.Join(lib, "sub", "video.mp4")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
