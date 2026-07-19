package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestDerivedAssetRoundTrip(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	kek := bytes.Repeat([]byte{0x42}, 32)
	vault, err := keystore.NewVault(string(kek), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'fid-1', 't', 'x.enc', 'video', 'active')`)

	store := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(dir, ".derived")}
	encPath, err := store.Write(context.Background(), 10, "preview_vtt", "thumbs.vtt", bytes.NewReader([]byte("WEBVTT\n")))
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(encPath) {
		t.Fatalf("expected enc path, got %s", encPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "preview", "thumbs.vtt")); !os.IsNotExist(err) {
		t.Fatalf("plaintext should not be written by Write()")
	}
	seeker, err := OpenDerivedSeeker(db, vault, 10, encPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seeker.Close()
	got, err := io.ReadAll(seeker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "WEBVTT\n" {
		t.Fatalf("got %q", got)
	}
}

func TestDerivedFinalizePathPlainLibrary(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	plain := filepath.Join(dir, "sprite.jpg")
	if err := os.WriteFile(plain, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 0)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_path, file_type, status) VALUES (10, 1, ?, 'video', 'active')`, plain)

	store := &DerivedAssetStore{DB: db, Vault: nil, BaseDir: filepath.Join(dir, ".derived")}
	out, err := store.FinalizePath(context.Background(), 10, "preview_sprite", "sprite.jpg", plain)
	if err != nil {
		t.Fatal(err)
	}
	if out != plain {
		t.Fatalf("expected plain path unchanged, got %s", out)
	}
}

func TestResolveDerivedEncPathFallbackLayout(t *testing.T) {
	dir := t.TempDir()
	derivedBase := filepath.Join(dir, ".derived")
	enc := filepath.Join(derivedBase, "10", "doc_cover", "cover.jpg.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := ResolveDerivedEncPath(nil, derivedBase, 10, "doc_cover", "cover.jpg")
	if !ok || got != enc {
		t.Fatalf("ResolveDerivedEncPath() = (%q, %v), want (%q, true)", got, ok, enc)
	}
}

func TestLookupDerivedWrappedDEKByKind(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'docs', 'document', '/x')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, file_type, status) VALUES (10, 1, 'f', 'a.pdf', 'document', 'active')`)
	enc := `F:\data\.derived\10\doc_cover\cover.jpg.enc`
	if _, err := db.Exec(`
		INSERT INTO media_derived_assets (media_id, artifact_kind, logical_name, enc_path, wrapped_dek, iv)
		VALUES (10, 'doc_cover', 'cover.jpg', ?, '6161', '6262')`, enc); err != nil {
		t.Fatal(err)
	}
	wh, err := lookupDerivedWrappedDEK(db, 10, `f:/data/.derived/10/doc_cover/cover.jpg.enc`, "doc_cover", "cover.jpg")
	if err != nil || wh != "6161" {
		t.Fatalf("lookupDerivedWrappedDEK() = (%q, %v)", wh, err)
	}
}

func TestNeedsDerivedEncryption(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', '/x', 1)`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_path, file_type, status) VALUES (10, 1, '/x/a.mp4', 'video', 'active')`)
	if !NeedsDerivedEncryption(db, 10) {
		t.Fatal("expected true")
	}
	if NeedsDerivedEncryption(db, 0) {
		t.Fatal("expected false for invalid id")
	}
}

func TestDerivedAssetWritePreservesOldOnDBFailure(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','video',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,file_type) VALUES(10,1,'atomic','video')`)
	vault, err := keystore.NewVault("atomic-test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(dir, "derived")}
	encPath := ExpectedDerivedEncPath(s.BaseDir, 10, "poster", "poster.jpg")
	if err := os.MkdirAll(filepath.Dir(encPath), 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("old-encrypted")
	if err := os.WriteFile(encPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(10,'poster','poster.jpg',?,'old-wrap','old-iv')`, encPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER reject_derived_update BEFORE UPDATE ON media_derived_assets BEGIN SELECT RAISE(FAIL,'reject upsert'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Write(context.Background(), 10, "poster", "poster.jpg", bytes.NewReader([]byte("new poster")))
	if err == nil {
		t.Fatal("expected upsert error")
	}
	got, err := os.ReadFile(encPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, old) {
		t.Fatalf("final changed: %q", got)
	}
	var wrap, iv string
	if err := db.QueryRow(`SELECT wrapped_dek,iv FROM media_derived_assets WHERE media_id=10 AND artifact_kind='poster'`).Scan(&wrap, &iv); err != nil {
		t.Fatal(err)
	}
	if wrap != "old-wrap" || iv != "old-iv" {
		t.Fatalf("row changed: %s %s", wrap, iv)
	}
	matches, _ := filepath.Glob(encPath + ".*")
	if len(matches) != 0 {
		t.Fatalf("temporary artifacts remain: %v", matches)
	}
}

func TestDerivedAssetWriteReplacesWithoutArtifacts(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','video',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,file_type) VALUES(10,1,'atomic-success','video')`)
	vault, err := keystore.NewVault("atomic-success-key", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(dir, "derived")}
	legacy := ExpectedDerivedEncPath(s.BaseDir, 10, "poster", "poster.jpg")
	_ = os.MkdirAll(filepath.Dir(legacy), 0700)
	_ = os.WriteFile(legacy, []byte("old"), 0600)
	path, err := s.Write(context.Background(), 10, "poster", "poster.jpg", bytes.NewReader([]byte("new poster")))
	if err != nil {
		t.Fatal(err)
	}
	if path == legacy || !strings.HasSuffix(strings.ToLower(path), ".enc") {
		t.Fatalf("path=%q legacy=%q", path, legacy)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(path + ".tmp-*")
	if len(matches) != 0 {
		t.Fatalf("temporary artifacts remain: %v", matches)
	}
}

func TestDerivedAssetWriteAcrossStoresKeepsDBAndFileConsistent(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "derived-concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','video',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,file_type) VALUES(10,1,'cross-store','video')`)
	vault, err := keystore.NewVault("cross-store-key", "")
	if err != nil {
		t.Fatal(err)
	}
	a := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(dir, "derived")}
	b := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: a.BaseDir}
	inputs := [][]byte{bytes.Repeat([]byte("A"), 4096), bytes.Repeat([]byte("B"), 4096)}
	for i := 0; i < 20; i++ {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, e := a.Write(context.Background(), 10, "poster", "poster.jpg", bytes.NewReader(inputs[0]))
			errs <- e
		}()
		go func() {
			defer wg.Done()
			_, e := b.Write(context.Background(), 10, "poster", "poster.jpg", bytes.NewReader(inputs[1]))
			errs <- e
		}()
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		var path string
		var rows int
		if err := db.QueryRow(`SELECT COUNT(*), MAX(enc_path) FROM media_derived_assets WHERE media_id=10 AND artifact_kind='poster' AND logical_name='poster.jpg'`).Scan(&rows, &path); err != nil {
			t.Fatal(err)
		}
		if rows != 1 || !strings.HasSuffix(strings.ToLower(path), ".enc") {
			t.Fatalf("rows=%d path=%q", rows, path)
		}
		seeker, e := OpenDerivedArtifactSeeker(db, vault, 10, path, "poster", "poster.jpg")
		if e != nil {
			t.Fatal(e)
		}
		got, e := io.ReadAll(seeker)
		_ = seeker.Close()
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(got, inputs[0]) && !bytes.Equal(got, inputs[1]) {
			t.Fatalf("corrupt output len=%d", len(got))
		}
		files, e := filepath.Glob(filepath.Join(a.BaseDir, "10", "poster", "*.enc"))
		if e != nil {
			t.Fatal(e)
		}
		if len(files) != 1 || files[0] != path {
			t.Fatalf("active=%q files=%v", path, files)
		}
	}
}

func TestStagedDerivedAssetsCommitAtomically(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'l','video',?,1); INSERT INTO media(id,library_id,file_id,file_type) VALUES(10,1,'f','video')`, root)
	vault, _ := keystore.NewVault("stage-key", "")
	s := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(root, "derived")}
	p1 := filepath.Join(root, "a")
	p2 := filepath.Join(root, "b")
	_ = os.WriteFile(p1, []byte("a"), 0644)
	_ = os.WriteFile(p2, []byte("b"), 0644)
	a, err := s.StagePath(context.Background(), 10, "atrack_playlist", "0/index.m3u8", p1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.StagePath(context.Background(), 10, "atrack_segment", "0/seg.ts", p2)
	if err != nil {
		t.Fatal(err)
	}
	defer s.AbortStaged(a, b)
	var before int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_derived_assets WHERE media_id=10`).Scan(&before)
	if before != 0 {
		t.Fatalf("rows before commit=%d", before)
	}
	tx, _ := db.BeginTx(context.Background(), nil)
	old, err := s.CommitStagedTx(context.Background(), tx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(old) != 0 {
		t.Fatalf("old=%v", old)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_derived_assets WHERE media_id=10`).Scan(&rows)
	if rows != 2 {
		t.Fatalf("rows=%d", rows)
	}
}
func TestStagedDerivedAssetsRollbackLeavesOldRowsAndFiles(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'l','video',?,1); INSERT INTO media(id,library_id,file_id,file_type) VALUES(10,1,'f','video')`, root)
	vault, _ := keystore.NewVault("stage-key", "")
	s := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(root, "derived")}
	old := filepath.Join(root, "old.enc")
	_ = os.WriteFile(old, []byte("old"), 0600)
	_, _ = db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(10,'atrack_playlist','0/index.m3u8',?,'w','i')`, old)
	plain := filepath.Join(root, "new")
	_ = os.WriteFile(plain, []byte("new"), 0644)
	a, err := s.StagePath(context.Background(), 10, "atrack_playlist", "0/index.m3u8", plain)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.BeginTx(context.Background(), nil)
	_, err = s.CommitStagedTx(context.Background(), tx, a)
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	s.AbortStaged(a)
	var got string
	_ = db.QueryRow(`SELECT enc_path FROM media_derived_assets WHERE media_id=10`).Scan(&got)
	if got != old {
		t.Fatalf("path=%q", got)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.EncPath()); !os.IsNotExist(err) {
		t.Fatalf("new remains: %v", err)
	}
}
