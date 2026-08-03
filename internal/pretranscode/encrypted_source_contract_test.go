package pretranscode

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

// Prepare claims Validated stream_decrypt; prove encrypted input opens after plaintext removal.
func TestEncryptedSourceStreamDecryptAfterPlaintextRemoval(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault("prepare-enc-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := []byte("ftypmp42" + string(bytes.Repeat([]byte{0x55}, 2048)))
	plainPath := filepath.Join(dir, "v.mp4")
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	encPath := filepath.Join(dir, "v.enc")
	out, err := os.Create(encPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := crypto.EncryptFile(in, out, kek)
	_ = in.Close()
	_ = out.Close()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(plainPath)

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, format, status) VALUES (1, 1, 'f', 't', ?, 'video', 'mp4', 'active')`, encPath)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (1, ?, ?, ?, ?, 'encrypted')`,
		encPath, hex.EncodeToString(res.WrappedDEK), hex.EncodeToString(res.IV), plainPath)

	// Mirror worker.go prepare path: OpenFFmpegInput on catalog .enc after plaintext retirement.
	ff, err := storage.OpenFFmpegInput(db, vault, 1, encPath, 0)
	if err != nil {
		t.Fatalf("prepare stream_decrypt OpenFFmpegInput after plaintext removal: %v", err)
	}
	defer func() {
		if ff.Cleanup != nil {
			ff.Cleanup()
		}
	}()
	if ff.Stdin == nil {
		t.Fatal("prepare stream_decrypt requires decrypt pipe after plaintext removal")
	}
	buf := make([]byte, 8)
	if _, err := ff.Stdin.Read(buf); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if !bytes.Equal(buf, plain[:8]) {
		t.Fatalf("pipe mismatch %q", buf)
	}
}
