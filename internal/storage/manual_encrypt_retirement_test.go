package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

func TestEncryptMediaManualCleanupUpsertsRetirement(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	kek := bytes.Repeat([]byte{0x42}, 32)
	vault, err := keystore.NewVault(string(kek), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "clip.mkv")
	payload := []byte("manual-encrypt-retirement-bytes")
	if err := os.WriteFile(plain, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES(1,'lib','video',?,1,1)`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,ingest_generation,publication_state) VALUES(20,1,'fid-m','t',?,'video','active',1,'processing')`, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(30,20,1,'scan','processing','{}',?)`, publication.CurrentPolicyVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES(31,30,20,1,'encrypt',1,'waiting',0,3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round) VALUES(32,20,30,31,1,'encrypt','waiting',0,3,0)`); err != nil {
		t.Fatal(err)
	}

	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	if err := enc.EncryptMediaManual(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plain); err != nil {
		t.Fatalf("source must remain for retirement: %v", err)
	}
	var encPath string
	if err := db.QueryRow(`SELECT file_path FROM media WHERE id=20`).Scan(&encPath); err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(encPath) {
		t.Fatalf("expected encrypted media path, got %s", encPath)
	}
	var n int
	var state, basisKind, stageID string
	var basisID, generation int64
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(state),''),COALESCE(MAX(basis_kind),''),COALESCE(MAX(basis_id),0),COALESCE(MAX(generation),0),COALESCE(MAX(encryption_stage_id),'') FROM media_plaintext_retirement WHERE media_id=20`).
		Scan(&n, &state, &basisKind, &basisID, &generation, &stageID); err != nil {
		t.Fatal(err)
	}
	if n != 1 || basisKind != "encryption" || basisID != 32 || generation != 1 || stageID == "" {
		t.Fatalf("retirement n=%d state=%s kind=%s basis=%d gen=%d stage=%s", n, state, basisKind, basisID, generation, stageID)
	}
	if state != "blocked" && state != "ready" {
		t.Fatalf("state=%s want blocked|ready", state)
	}
}

func TestEncryptMediaManualCleanupDisabledCreatesNoRetirement(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	kek := bytes.Repeat([]byte{0x41}, 32)
	vault, err := keystore.NewVault(string(kek), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(plain, []byte("no-cleanup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES(1,'lib','video',?,1,0)`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,ingest_generation) VALUES(21,1,'fid-n','t',?,'video','active',1)`, plain); err != nil {
		t.Fatal(err)
	}
	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	if err := enc.EncryptMediaManual(context.Background(), 21); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=21`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("retirement rows=%d want 0", n)
	}
}

func TestEncryptMediaManualCleanupFailsClosedWithoutPublicationIdentity(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	kek := bytes.Repeat([]byte{0x43}, 32)
	vault, err := keystore.NewVault(string(kek), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(plain, []byte("orphan-cleanup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES(1,'lib','video',?,1,1)`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,ingest_generation) VALUES(22,1,'fid-o','t',?,'video','active',1)`, plain); err != nil {
		t.Fatal(err)
	}
	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	err = enc.EncryptMediaManual(context.Background(), 22)
	if err == nil {
		t.Fatal("expected fail-closed when cleanup requested without retirement identity")
	}
	if _, statErr := os.Stat(plain); statErr != nil {
		t.Fatalf("source must remain after fail-closed cleanup path: %v", statErr)
	}
	var path string
	if err := db.QueryRow(`SELECT file_path FROM media WHERE id=22`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != plain {
		t.Fatalf("media path mutated on fail-closed encrypt: %s", path)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=22`).Scan(&n)
	if n != 0 {
		t.Fatalf("retirement rows=%d", n)
	}
}

func TestPlaintextCleanupManualEncryptNeverRemovesSource(t *testing.T) {
	data, err := os.ReadFile("asset_encrypt.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, banned := range []string{"cleanupPlaintextAfterEncrypt", "os.Remove(plainPath)", "removePlaintextFile(plainPath)"} {
		if strings.Contains(src, banned) {
			t.Fatalf("manual encrypt path still deletes plaintext via %q", banned)
		}
	}
}
