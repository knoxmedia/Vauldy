package atrack

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

func TestEncryptedSourceStreamDecryptAfterPlaintextRemoval(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault("atrack-enc-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := []byte("ftypmp42" + string(bytes.Repeat([]byte{0x22}, 2048)))
	plainPath := filepath.Join(dir, "v.mp4")
	_ = os.WriteFile(plainPath, plain, 0o644)
	in, _ := os.Open(plainPath)
	encPath := filepath.Join(dir, "v.enc")
	out, _ := os.Create(encPath)
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

	raw, cleanup, err := storage.FFprobeOutputContext(context.Background(), db, vault, "ffprobe", 1, encPath, 0, 0, []string{"-v", "quiet", "-show_entries", "format=duration", "-of", "csv=p=0"})
	if cleanup != nil {
		cleanup()
	}
	// ffprobe binary may be absent; contract is that encrypted input opens without needing plaintext.
	_ = raw
	ff, err := storage.OpenFFmpegInput(db, vault, 1, encPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if ff.Cleanup != nil {
			ff.Cleanup()
		}
	}()
	if ff.Stdin == nil {
		t.Fatal("atrack stream_decrypt requires pipe after plaintext removal")
	}
}
