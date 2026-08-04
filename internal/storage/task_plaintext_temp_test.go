package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestPlaintextTempMaterializePassthroughAndBoundPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "task-plain")
	svc := NewTaskPlaintextTemp(root)
	plain := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(plain, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	bound := PlaintextTempBound{MediaID: 9, Generation: 2, TaskID: 11, TaskType: "subtitle_recognize", LeaseOwner: "owner-a"}
	got, release, err := svc.Materialize(nil, nil, bound, plain)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if got != plain {
		t.Fatalf("passthrough got %q", got)
	}
}

func TestPlaintextTempMaterializeEncryptedUnderLeaseBounds(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault("task-plain-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plainIn := filepath.Join(dir, "movie.mp4")
	payload := []byte("plain-movie-bytes")
	if err := os.WriteFile(plainIn, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	encOut := filepath.Join(dir, "movie.enc")
	in, err := os.Open(plainIn)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(encOut)
	if err != nil {
		t.Fatal(err)
	}
	res, err := crypto.EncryptFile(in, out, kek)
	_ = in.Close()
	_ = out.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (9, 1, 'f', 't', ?, 'video', 'active')`, encOut)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, plain_path, wrapped_dek, iv, status)
		VALUES (9, ?, ?, ?, ?, 'encrypted')`, encOut, plainIn, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV))
	_ = os.Remove(plainIn)

	root := filepath.Join(t.TempDir(), "task-plain")
	svc := NewTaskPlaintextTemp(root)
	bound := PlaintextTempBound{MediaID: 9, Generation: 3, TaskID: 44, TaskType: "subtitle_recognize", LeaseOwner: "lease-1"}
	got, release, err := svc.Materialize(db, vault, bound, encOut)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("temp escaped root: got=%q root=%q err=%v", got, root, err)
	}
	if !strings.Contains(filepath.ToSlash(rel), "9/3/44/") {
		t.Fatalf("expected media/generation/task path, got %q", rel)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("payload mismatch %q", data)
	}
	release()
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("expected cleanup, err=%v", err)
	}
}

func TestPlaintextTempRejectsInvalidBoundAndRecoversOrphans(t *testing.T) {
	root := filepath.Join(t.TempDir(), "task-plain")
	svc := NewTaskPlaintextTemp(root)
	plain := filepath.Join(t.TempDir(), "a.bin")
	_ = os.WriteFile(plain, []byte("x"), 0o644)
	if _, _, err := svc.Materialize(nil, nil, PlaintextTempBound{}, plain); err == nil {
		t.Fatal("expected invalid bound error")
	}
	orphanDir := filepath.Join(root, "9", "1", "7")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(orphanDir, "leftover.bin")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan remained: %v", err)
	}
}

func TestMaterializePlaintextTempRequiresBoundIdentity(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plain := filepath.Join(t.TempDir(), "a.mp4")
	_ = os.WriteFile(plain, []byte("x"), 0o644)
	if _, _, err := MaterializePlaintextTemp(db, nil, PlaintextTempBound{MediaID: 1}, plain); err == nil {
		t.Fatal("expected missing generation/task/lease rejection")
	}
	bound := PlaintextTempBound{MediaID: 5, Generation: 2, TaskID: 9, TaskType: "doc", LeaseOwner: "owner"}
	got, cleanup, err := MaterializePlaintextTemp(db, nil, bound, plain)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got != plain {
		t.Fatalf("got %q", got)
	}
}

func TestMaterializePlaintextTempUsesTaskServiceWhenConfigured(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault("compat-plain-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plainIn := filepath.Join(dir, "clip.mp4")
	payload := []byte("clip-bytes")
	if err := os.WriteFile(plainIn, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	encOut := filepath.Join(dir, "clip.enc")
	in, _ := os.Open(plainIn)
	out, _ := os.Create(encOut)
	res, err := crypto.EncryptFile(in, out, kek)
	_ = in.Close()
	_ = out.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (5, 1, 'f', 't', ?, 'video', 'active')`, encOut)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, plain_path, wrapped_dek, iv, status)
		VALUES (5, ?, ?, ?, ?, 'encrypted')`, encOut, plainIn, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV))
	_ = os.Remove(plainIn)

	root := filepath.Join(t.TempDir(), "bound-plain")
	SetDefaultTaskPlaintextTemp(NewTaskPlaintextTemp(root))
	t.Cleanup(func() { SetDefaultTaskPlaintextTemp(nil) })

	bound := PlaintextTempBound{MediaID: 5, Generation: 4, TaskID: 88, TaskType: "subtitle_recognize", LeaseOwner: "lease-z"}
	got, cleanup, err := MaterializePlaintextTemp(db, vault, bound, encOut)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	rel, err := filepath.Rel(root, got)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("escaped: %q err=%v", got, err)
	}
	if !strings.Contains(filepath.ToSlash(rel), "5/4/88/") {
		t.Fatalf("bound path missing: %q", rel)
	}
	data, _ := os.ReadFile(got)
	if !bytes.Equal(data, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestPlaintextTempReleaseBoundClearsTerminalAttempt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "task-plain")
	svc := NewTaskPlaintextTemp(root)
	SetDefaultTaskPlaintextTemp(svc)
	t.Cleanup(func() { SetDefaultTaskPlaintextTemp(nil) })

	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault("release-bound-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plainIn := filepath.Join(dir, "a.mp4")
	_ = os.WriteFile(plainIn, []byte("abc"), 0o644)
	encOut := filepath.Join(dir, "a.enc")
	in, _ := os.Open(plainIn)
	out, _ := os.Create(encOut)
	res, err := crypto.EncryptFile(in, out, kek)
	_ = in.Close()
	_ = out.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (3, 1, 'f', 't', ?, 'video', 'active')`, encOut)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, plain_path, wrapped_dek, iv, status)
		VALUES (3, ?, '', ?, ?, 'encrypted')`, encOut, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV))

	bound := PlaintextTempBound{MediaID: 3, Generation: 7, TaskID: 99, TaskType: "preview", LeaseOwner: "owner"}
	got, _, err := MaterializePlaintextTemp(db, vault, bound, encOut)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseBoundForTask(bound); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(got)); !os.IsNotExist(err) {
		t.Fatalf("bound dir remained after ReleaseBound: %v", err)
	}
}
