package subtitle

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestEncryptedSourceStreamDecryptAfterPlaintextRemoval(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := keystore.NewVault("subtitle-enc-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := []byte("ftypmp42" + string(bytes.Repeat([]byte{0x44}, 2048)))
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

	s := &Service{DB: db, Vault: vault, FFmpegPath: "ffmpeg", FFprobePath: "ffprobe"}
	input, stdin, cleanup, err := s.openVideoPipeInput(1, encPath)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if input != "pipe:0" || stdin == nil {
		t.Fatalf("subtitle extract/recognize stream_decrypt want pipe, got input=%q stdin=%v", input, stdin != nil)
	}
}
