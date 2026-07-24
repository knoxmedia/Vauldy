package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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

func TestEncryptMediaConcurrentSingleOutput(t *testing.T) {
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
	plain := filepath.Join(dir, "clip.mkv")
	payload := bytes.Repeat([]byte("video-bytes"), 4096)
	if err := os.WriteFile(plain, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'fid-1', 't', ?, 'video', 'active')`, plain)

	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = enc.EncryptMedia(context.Background(), 10)
		}()
	}
	wg.Wait()

	encDir := filepath.Join(dir, ".encrypted", "video", "stages")
	entries, err := os.ReadDir(encDir)
	if err != nil {
		t.Fatal(err)
	}
	var encFiles int
	for _, e := range entries {
		if e.IsDir() {
			children, _ := os.ReadDir(filepath.Join(encDir, e.Name()))
			for _, child := range children {
				if strings.HasSuffix(child.Name(), ".enc") {
					encFiles++
				}
			}
		} else if strings.HasSuffix(e.Name(), ".enc") {
			encFiles++
		}
	}
	if encFiles != 1 {
		t.Fatalf("expected 1 .enc file, got %d (%v)", encFiles, entries)
	}
}

func TestEncryptMediaRoundTrip(t *testing.T) {
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
	plain := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(plain, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'fid-1', 't', ?, 'video', 'active')`, plain)

	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	if err := enc.EncryptMedia(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var encPath string
	if err := db.QueryRow(`SELECT file_path FROM media WHERE id = 10`).Scan(&encPath); err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(encPath) {
		t.Fatalf("expected enc path, got %s", encPath)
	}
	rc, err := OpenPlaintext(db, vault, 10, encPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake-video-bytes" {
		t.Fatalf("got %q", got)
	}
}

func TestEncryptPlainMissingMarksRow(t *testing.T) {
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
	missing := filepath.Join(dir, "gone.mp4")
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (400, 1, 'fid-1', 't', ?, 'video', 'active')`, missing)

	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	if err := enc.EncryptMedia(context.Background(), 400); err == nil {
		t.Fatal("expected error for missing plain file")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM media_encrypted_assets WHERE media_id = 400`).Scan(&status); err != nil {
		t.Fatalf("missing row: %v", err)
	}
	if status != "plain_missing" {
		t.Fatalf("status=%q want plain_missing", status)
	}
}

func TestEncryptMediaCommitGuardPreventsStaleDatabasePublish(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault(string(bytes.Repeat([]byte{0x42}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(plain, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','video',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id,library_id,file_id,title,file_path,file_type,status) VALUES(10,1,'fid','t',?,'video','active')`, plain)
	want := errors.New("stale lease")
	ctx := WithEncryptCommitGuard(context.Background(), func(context.Context) error { return want })
	enc := &AssetEncryptor{DB: db, Vault: vault}
	if err := enc.EncryptMedia(ctx, 10); !errors.Is(err, want) {
		t.Fatalf("err=%v want %v", err, want)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_encrypted_assets WHERE media_id=10`).Scan(&n)
	if n != 0 {
		t.Fatalf("published rows=%d", n)
	}
	var path string
	_ = db.QueryRow(`SELECT file_path FROM media WHERE id=10`).Scan(&path)
	if path != plain {
		t.Fatalf("media path=%q want plain", path)
	}
}

func newAssetEncryptCase(t *testing.T, id int64) (*sql.DB, *AssetEncryptor, string, string) {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := keystore.NewVault(string(bytes.Repeat([]byte{0x42}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "clip.mkv")
	if err = os.WriteFile(plain, []byte("new-video"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','video',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(?,1,'fid','t',?,'video','active')`, id, plain)
	return db, &AssetEncryptor{DB: db, Vault: vault}, plain, filepath.Join(dir, ".encrypted", "video", "fid.enc")
}

func TestEncryptMedia_ZeroCanonicalWithoutRowIsReplaced(t *testing.T) {
	db, enc, _, out := newAssetEncryptCase(t, 501)
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := enc.EncryptMedia(context.Background(), 501); err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(out) {
		t.Fatal("zero canonical was not replaced")
	}
	var status, path string
	if err := db.QueryRow(`SELECT status,enc_path FROM media_encrypted_assets WHERE media_id=501`).Scan(&status, &path); err != nil || status != "encrypted" || path != out {
		t.Fatalf("row=%q %q err=%v", status, path, err)
	}
}

func TestEncryptMedia_NonemptyOrphanWithoutRowIsReplaced(t *testing.T) {
	_, enc, _, out := newAssetEncryptCase(t, 502)
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := enc.EncryptMedia(context.Background(), 502); err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(out) {
		t.Fatal("nonempty orphan was trusted")
	}
}

func TestEncryptMedia_ValidCanonicalRecordReturnsAlreadyEncrypted(t *testing.T) {
	db, enc, plain, out := newAssetEncryptCase(t, 503)
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(crypto.Magic9527), bytes.Repeat([]byte{1}, crypto.EncHeaderSize)...)
	if err := os.WriteFile(out, before, 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(503,?,'aa','bb',?,'encrypted')`, out, plain)
	if err := enc.EncryptMedia(context.Background(), 503); !errors.Is(err, ErrAlreadyEncrypted) {
		t.Fatalf("err=%v", err)
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(after, before) {
		t.Fatal("valid output rewritten")
	}
}

func TestEncryptMedia_FailureAfterOrphanBackupRestoresExactContent(t *testing.T) {
	_, enc, _, out := newAssetEncryptCase(t, 504)
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("exact orphan bytes")
	if err := os.WriteFile(out, before, 0600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("reject publish")
	ctx := WithEncryptCommitGuard(context.Background(), func(context.Context) error { return want })
	if err := enc.EncryptMedia(ctx, 504); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	after, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("restored=%q err=%v", after, err)
	}
}

func TestEncryptMedia_EncryptedRecordForDifferentPathDoesNotClaimCanonical(t *testing.T) {
	db, enc, plain, out := newAssetEncryptCase(t, 505)
	other := filepath.Join(filepath.Dir(out), "other.enc")
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	valid := append([]byte(crypto.Magic9527), bytes.Repeat([]byte{1}, crypto.EncHeaderSize)...)
	if err := os.WriteFile(other, valid, 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(505,?,'aa','bb',?,'encrypted')`, other, plain)
	if err := enc.EncryptMedia(context.Background(), 505); err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(out) {
		t.Fatal("canonical output not created")
	}
	var got string
	_ = db.QueryRow(`SELECT enc_path FROM media_encrypted_assets WHERE media_id=505`).Scan(&got)
	if got != out {
		t.Fatalf("enc_path=%q want %q", got, out)
	}
}

func TestRestoreEncryptedOutputJoinsOriginalRemoveAndRestoreErrors(t *testing.T) {
	original := errors.New("publish failed")
	removeErr := errors.New("remove failed")
	restoreErr := errors.New("restore failed")
	got := restoreEncryptedOutput(original, "new.enc", "backup.enc", func(string) error { return removeErr }, func(string, string) error { return restoreErr })
	if !errors.Is(got, original) || !errors.Is(got, removeErr) || !errors.Is(got, restoreErr) {
		t.Fatalf("joined error=%v", got)
	}
}

func TestRestoreEncryptedOutputIgnoresMissingNewFile(t *testing.T) {
	original := errors.New("publish failed")
	restoreErr := errors.New("restore failed")
	got := restoreEncryptedOutput(original, "new.enc", "backup.enc", func(string) error { return os.ErrNotExist }, func(string, string) error { return restoreErr })
	if !errors.Is(got, original) || !errors.Is(got, restoreErr) || errors.Is(got, os.ErrNotExist) {
		t.Fatalf("joined error=%v", got)
	}
}

func TestValidEncryptedAssetRecord_NoRowsAndQueryError(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE media_encrypted_assets(media_id INTEGER,enc_path TEXT,status TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := IsEncryptedAssetRecordValid(context.Background(), db, 1)
	if err != nil || valid {
		t.Fatalf("no row valid=%v err=%v", valid, err)
	}
	_, _ = db.Exec(`DROP TABLE media_encrypted_assets`)
	if _, err := IsEncryptedAssetRecordValid(context.Background(), db, 1); err == nil {
		t.Fatal("expected query error")
	}
}

func TestEncryptMedia_CancelledRestoresExistingCanonical(t *testing.T) {
	_, enc, _, out := newAssetEncryptCase(t, 507)
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("existing orphan")
	if err := os.WriteFile(out, before, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := enc.EncryptMedia(ctx, 507)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	after, readErr := os.ReadFile(out)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("after=%q err=%v", after, readErr)
	}
}
func TestEncryptMedia_PreCancelledCreatesNoOutputOrRecord(t *testing.T) {
	db, enc, _, out := newAssetEncryptCase(t, 506)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := enc.EncryptMedia(ctx, 506)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("output stat=%v", statErr)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_encrypted_assets WHERE media_id=506`).Scan(&n)
	if n != 0 {
		t.Fatalf("rows=%d", n)
	}
}

func TestEncryptFlight_WaiterReceivesLeaderError(t *testing.T) {
	leader, flight := acquireEncryptFlight(9001)
	if !leader || flight == nil {
		t.Fatal("first caller is not leader")
	}
	leader2, waiterFlight := acquireEncryptFlight(9001)
	if leader2 || waiterFlight != flight {
		t.Fatal("second caller did not join flight")
	}
	want := errors.New("leader failed")
	done := make(chan error, 1)
	go func() { done <- waitEncryptFlight(context.Background(), waiterFlight) }()
	select {
	case err := <-done:
		t.Fatalf("waiter returned early: %v", err)
	default:
	}
	finishEncryptFlight(9001, flight, want)
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestEncryptFlight_WaiterCancellationDoesNotFinishLeader(t *testing.T) {
	leader, flight := acquireEncryptFlight(9002)
	if !leader {
		t.Fatal("not leader")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitEncryptFlight(ctx, flight); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-flight.done:
		t.Fatal("waiter cancellation finished leader")
	default:
	}
	finishEncryptFlight(9002, flight, nil)
}

func TestEncryptFlight_DifferentMediaAreBothLeaders(t *testing.T) {
	leaderA, a := acquireEncryptFlight(9003)
	leaderB, b := acquireEncryptFlight(9004)
	if !leaderA || !leaderB {
		t.Fatalf("leaders=%v/%v", leaderA, leaderB)
	}
	finishEncryptFlight(9003, a, nil)
	finishEncryptFlight(9004, b, nil)
}

func TestEncryptMedia_WaiterReceivesCommitGuardFailure(t *testing.T) {
	_, enc, _, _ := newAssetEncryptCase(t, 508)
	entered := make(chan struct{})
	release := make(chan struct{})
	want := errors.New("leader publish failed")
	ctx := WithEncryptCommitGuard(context.Background(), func(context.Context) error { close(entered); <-release; return want })
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- enc.EncryptMedia(ctx, 508) }()
	<-entered
	joined := make(chan struct{})
	enc.onFlightJoined = func(mediaID int64) {
		if mediaID == 508 {
			close(joined)
		}
	}
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- enc.EncryptMedia(context.Background(), 508) }()
	<-joined
	select {
	case err := <-waiterDone:
		t.Fatalf("waiter returned before leader: %v", err)
	default:
	}
	close(release)
	if err := <-leaderDone; !errors.Is(err, want) {
		t.Fatalf("leader err=%v", err)
	}
	if err := <-waiterDone; !errors.Is(err, want) {
		t.Fatalf("waiter err=%v", err)
	}
}

func TestEncryptMedia_WaiterSuccessLeavesValidAsset(t *testing.T) {
	db, enc, _, out := newAssetEncryptCase(t, 509)
	entered := make(chan struct{})
	release := make(chan struct{})
	ctx := WithEncryptCommitGuard(context.Background(), func(context.Context) error { close(entered); <-release; return nil })
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- enc.EncryptMedia(ctx, 509) }()
	<-entered
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- enc.EncryptMedia(context.Background(), 509) }()
	select {
	case err := <-waiterDone:
		t.Fatalf("waiter returned before leader: %v", err)
	default:
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader err=%v", err)
	}
	if err := <-waiterDone; err != nil && !errors.Is(err, ErrAlreadyEncrypted) {
		t.Fatalf("waiter err=%v", err)
	}
	valid, err := IsEncryptedAssetRecordValid(context.Background(), db, 509)
	if err != nil || !valid || !crypto.IsEncFile(out) {
		t.Fatalf("valid=%v err=%v", valid, err)
	}
}

func TestStageMediaEncryptionLeavesCatalogAndSelectionUnchanged(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault(string(bytes.Repeat([]byte{0x42}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "photo.jpg")
	if err = os.WriteFile(plain, []byte("photo bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'photos','photo',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'photo-1',?,'image','active')`, plain)
	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	stage, err := enc.StageMediaEncryption(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if stage.OriginalPath != plain || stage.EncPath == "" || stage.WrappedDEK == "" || stage.IV == "" || stage.SHA256 == "" || stage.Size <= 0 {
		t.Fatalf("stage=%+v", stage)
	}
	var selected string
	var records int
	if err = db.QueryRow(`SELECT file_path,(SELECT COUNT(*) FROM media_encrypted_assets WHERE media_id=10) FROM media WHERE id=10`).Scan(&selected, &records); err != nil {
		t.Fatal(err)
	}
	if selected != plain || records != 0 {
		t.Fatalf("selected=%q records=%d", selected, records)
	}
	if ok, _ := ValidEncryptedFile(stage.EncPath); !ok {
		t.Fatalf("invalid staged output %q", stage.EncPath)
	}
}
