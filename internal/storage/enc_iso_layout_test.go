package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func writeBox(typ string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf[0:4], size)
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}

func TestIsoBMFFMoovBeforeMDAT(t *testing.T) {
	faststart := bytes.NewBuffer(nil)
	faststart.Write(writeBox("ftyp", []byte("mp42")))
	faststart.Write(writeBox("moov", []byte{0x01, 0x02}))
	faststart.Write(writeBox("mdat", []byte{0xAA, 0xBB}))

	ok, err := isoBMFFMoovBeforeMDAT(bytes.NewReader(faststart.Bytes()))
	if err != nil || !ok {
		t.Fatalf("faststart: ok=%v err=%v", ok, err)
	}

	moovAtEnd := bytes.NewBuffer(nil)
	moovAtEnd.Write(writeBox("ftyp", []byte("mp42")))
	mdatPayload := bytes.Repeat([]byte{0xCD}, 32)
	moovAtEnd.Write(writeBox("mdat", mdatPayload))
	moovAtEnd.Write(writeBox("moov", []byte{0x03, 0x04}))

	ok, err = isoBMFFMoovBeforeMDAT(bytes.NewReader(moovAtEnd.Bytes()))
	if err != nil || ok {
		t.Fatalf("moov-at-end: ok=%v err=%v", ok, err)
	}
}

func TestEncryptedISOMoovBeforeMDAT(t *testing.T) {
	db, vault, _, encPath, _ := setupEncryptedMP4LayoutTest(t, true)
	defer func() { _ = db.Close() }()

	if !encryptedISOMoovBeforeMDAT(db, vault, 9, encPath) {
		t.Fatal("expected pipe-safe layout in encrypted faststart mp4")
	}
}

func TestEncryptedISOMoovBeforeMDATMoovAtEnd(t *testing.T) {
	db, vault, _, encPath, _ := setupEncryptedMP4LayoutTest(t, false)
	defer func() { _ = db.Close() }()

	if encryptedISOMoovBeforeMDAT(db, vault, 9, encPath) {
		t.Fatal("expected moov-at-end encrypted mp4 to fail layout check")
	}
}

func setupEncryptedMP4LayoutTest(t *testing.T, faststart bool) (*sql.DB, *keystore.Vault, []byte, string, []byte) {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	vault, err := keystore.NewVault("test-main-key", "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	var plain []byte
	if faststart {
		buf := bytes.NewBuffer(nil)
		buf.Write(writeBox("ftyp", []byte("mp42")))
		buf.Write(writeBox("moov", bytes.Repeat([]byte{0xAB}, 64)))
		buf.Write(writeBox("mdat", bytes.Repeat([]byte{0xCD}, 128)))
		plain = buf.Bytes()
	} else {
		buf := bytes.NewBuffer(nil)
		buf.Write(writeBox("ftyp", []byte("mp42")))
		buf.Write(writeBox("mdat", bytes.Repeat([]byte{0xCD}, 128)))
		buf.Write(writeBox("moov", bytes.Repeat([]byte{0xAB}, 64)))
		plain = buf.Bytes()
	}

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
	return db, vault, kek, encPath, plain
}
