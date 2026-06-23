package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestOpenFFmpegInputUsesPlainWhenEncCatalogAndPlainExists(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	vault, err := keystore.NewVault("test-main-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	plain := []byte("ftypmp42" + string(bytes.Repeat([]byte{0xAB}, 4096)))
	plainPath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}

	plainIn, err := os.Open(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(dir, "clip.enc")
	encOut, err := os.Create(encPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := crypto.EncryptFile(plainIn, encOut, kek)
	_ = plainIn.Close()
	_ = encOut.Close()
	if err != nil {
		t.Fatal(err)
	}

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, format, status) VALUES (9, 1, 'f', 't', ?, 'video', 'mp4', 'active')`, encPath)
	_, _ = db.Exec(
		`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status)
		 VALUES (9, ?, ?, ?, ?, 'encrypted')`,
		encPath, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV), plainPath,
	)

	in, err := OpenFFmpegInput(db, vault, 9, encPath, 4096)
	if err != nil {
		t.Fatalf("OpenFFmpegInput: %v", err)
	}
	if in.Path != plainPath {
		t.Fatalf("expected plain path %q, got %q", plainPath, in.Path)
	}
	if in.Stdin != nil {
		t.Fatal("expected file path input, not pipe")
	}
	if !in.PlainFallback {
		t.Fatal("expected PlainFallback")
	}
}

func TestOpenFFmpegInputEncryptedISOUsesPipeWhenPlainMissing(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	vault, err := keystore.NewVault("test-main-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	plain := []byte("ftypmp42" + string(bytes.Repeat([]byte{0xAB}, 8192)))
	plainPath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}

	plainIn, err := os.Open(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(dir, "clip.enc")
	encOut, err := os.Create(encPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := crypto.EncryptFile(plainIn, encOut, kek)
	_ = plainIn.Close()
	_ = encOut.Close()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(plainPath)

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, format, status) VALUES (9, 1, 'f', 't', ?, 'video', 'mp4', 'active')`, encPath)
	_, _ = db.Exec(
		`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status)
		 VALUES (9, ?, ?, ?, ?, 'encrypted')`,
		encPath, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV), plainPath,
	)

	in, err := OpenFFmpegInput(db, vault, 9, encPath, 0)
	if err != nil {
		t.Fatalf("OpenFFmpegInput: %v", err)
	}
	defer func() {
		if in.Cleanup != nil {
			in.Cleanup()
		}
	}()
	if in.Path != "" {
		t.Fatalf("expected pipe input for encrypted iso, got path %q", in.Path)
	}
	if in.Stdin == nil {
		t.Fatal("expected decrypt stdin")
	}
	if !in.FromEnc {
		t.Fatal("expected FromEnc")
	}
}

func TestOpenFFmpegInputEncryptedSeekUsesPipeFromKeyframeOffset(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	vault, err := keystore.NewVault("test-main-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	plain := []byte("ftypmp42" + string(bytes.Repeat([]byte{0xAB}, 8192)))
	plainPath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}

	plainIn, err := os.Open(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(dir, "clip.enc")
	encOut, err := os.Create(encPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := crypto.EncryptFile(plainIn, encOut, kek)
	_ = plainIn.Close()
	_ = encOut.Close()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(plainPath)

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, format, status) VALUES (9, 1, 'f', 't', ?, 'video', 'mp4', 'active')`, encPath)
	_, _ = db.Exec(
		`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status)
		 VALUES (9, ?, ?, ?, ?, 'encrypted')`,
		encPath, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV), plainPath,
	)

	in, err := OpenFFmpegInput(db, vault, 9, encPath, 4096)
	if err != nil {
		t.Fatalf("OpenFFmpegInput: %v", err)
	}
	if in.Path != "" {
		t.Fatalf("expected pipe input for encrypted seek, got path %q", in.Path)
	}
	if in.Stdin == nil {
		t.Fatal("expected decrypt stdin")
	}
	if !in.FromEnc {
		t.Fatal("expected FromEnc")
	}
	// Read a slice from the pipe; offset 4096 should skip header bytes.
	buf := make([]byte, 16)
	n, err := io.ReadFull(in.Stdin, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("read pipe: %v", err)
	}
	if n == 0 {
		t.Fatal("expected data from offset pipe")
	}
	if bytes.Equal(buf[:n], plain[:n]) {
		t.Fatal("expected data after byte offset, got file start")
	}
	if !bytes.Equal(buf[:n], plain[4096:4096+n]) {
		t.Fatalf("pipe data %q != plain[%d:%d]", buf[:n], 4096, 4096+n)
	}
	if in.Cleanup != nil {
		in.Cleanup()
	}
}
