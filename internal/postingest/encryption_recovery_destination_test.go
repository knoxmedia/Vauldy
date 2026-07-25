package postingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func seedRestoreDestinationJournal(t *testing.T, libraryRoot, selected, plainPath, source, quarantineRoot, stage string) (*sql.DB, string) {
	t.Helper()
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'restore','video',?)`, libraryRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(1,1,'restore',?,'video','active','processing',1)`, selected); err != nil {
		t.Fatal(err)
	}
	if plainPath != "" {
		if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(1,?,'wrapped','iv',?,'encrypted')`, selected, plainPath); err != nil {
			t.Fatal(err)
		}
	}
	quarantine := filepath.Join(quarantineRoot, "1", "1", stage, "source")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	enc := filepath.Join(libraryRoot, ".encrypted", "video", "stages", stage, "restore.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state) VALUES(?,1,1,1,1,1,1,'owner',?,?,'fingerprint',?,'wrapped','iv','hash',3,'quarantined')`, stage, source, quarantine, enc); err != nil {
		t.Fatal(err)
	}
	return db, quarantine
}

func TestReconcileEncryptionStagesRejectsNonAuthoritativeRestoreDestinations(t *testing.T) {
	cases := []struct {
		name   string
		source func(root, outside string) string
	}{
		{"external nonexistent", func(_, outside string) string { return filepath.Join(outside, "missing", "sentinel.mp4") }},
		{"traversal outside", func(root, _ string) string { return filepath.Join(root, "..", "outside-sentinel.mp4") }},
		{"reserved encrypted", func(root, _ string) string { return filepath.Join(root, ".encrypted", "sentinel.mp4") }},
		{"reserved quarantine", func(root, _ string) string { return filepath.Join(root, ".quarantine", "sentinel.mp4") }},
		{"reserved derived", func(root, _ string) string { return filepath.Join(root, "derived", "sentinel.mp4") }},
		{"reserved metadata", func(root, _ string) string { return filepath.Join(root, "metadata", "sentinel.mp4") }},
		{"reserved upload", func(root, _ string) string { return filepath.Join(root, "upload", "sentinel.mp4") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, outside, quarantineRoot := t.TempDir(), t.TempDir(), t.TempDir()
			source := tc.source(root, outside)
			db, quarantine := seedRestoreDestinationJournal(t, root, filepath.Join(root, "selected.enc"), "", source, quarantineRoot, "unsafe-stage")
			checked, cleaned, _ := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(filepath.Join(root, ".encrypted", "video", "stages"))}, 100)
			if checked != 1 || cleaned != 0 {
				t.Fatalf("checked=%d cleaned=%d", checked, cleaned)
			}
			var state, marker string
			if err := db.QueryRow(`SELECT state,recovery_error FROM media_encryption_stage_journal WHERE stage_id='unsafe-stage'`).Scan(&state, &marker); err != nil {
				t.Fatal(err)
			}
			if state != "failed_closed" || marker != "unsafe_identity" {
				t.Fatalf("state=%s marker=%q", state, marker)
			}
			if _, err := os.Stat(source); !os.IsNotExist(err) {
				t.Fatalf("unsafe destination created: %v", err)
			}
			if _, err := os.Stat(quarantine); err != nil {
				t.Fatalf("quarantine changed: %v", err)
			}
		})
	}
}

func TestReconcileEncryptionStagesRejectsSymlinkEscapeDestination(t *testing.T) {
	root, outside, quarantineRoot := t.TempDir(), t.TempDir(), t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	source := filepath.Join(link, "sentinel.mp4")
	db, quarantine := seedRestoreDestinationJournal(t, root, filepath.Join(root, "selected.enc"), "", source, quarantineRoot, "symlink-stage")
	_, cleaned, _ := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(filepath.Join(root, ".encrypted", "video", "stages"))}, 100)
	if cleaned != 0 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	var marker string
	if err := db.QueryRow(`SELECT recovery_error FROM media_encryption_stage_journal WHERE stage_id='symlink-stage'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "unsafe_identity" {
		t.Fatalf("marker=%q", marker)
	}
	if _, err := os.Stat(filepath.Join(outside, "sentinel.mp4")); !os.IsNotExist(err) {
		t.Fatalf("external sentinel created: %v", err)
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("quarantine changed: %v", err)
	}
}

func TestReconcileEncryptionStagesRestoresValidLibrarySource(t *testing.T) {
	root, quarantineRoot := t.TempDir(), t.TempDir()
	source := filepath.Join(root, "Movies", "valid.mp4")
	db, _ := seedRestoreDestinationJournal(t, root, source, "", source, quarantineRoot, "00000000-0000-0000-0000-000000000101")
	_, cleaned, err := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(filepath.Join(root, ".encrypted", "video", "stages"))}, 100)
	if err != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "plain" {
		t.Fatalf("restored=%q err=%v", got, err)
	}
}

func TestReconcileEncryptionStagesUsesEncryptedAssetPlainPathExactly(t *testing.T) {
	root, quarantineRoot := t.TempDir(), t.TempDir()
	plain := filepath.Join(root, "Movies", "authoritative.mp4")
	selected := filepath.Join(root, ".encrypted", "video", "committed.enc")
	wrong := filepath.Join(root, "Movies", "other.mp4")
	db, quarantine := seedRestoreDestinationJournal(t, root, selected, plain, wrong, quarantineRoot, "plain-path-stage")
	_, cleaned, _ := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(filepath.Join(root, ".encrypted", "video", "stages"))}, 100)
	if cleaned != 0 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	var marker string
	if err := db.QueryRow(`SELECT recovery_error FROM media_encryption_stage_journal WHERE stage_id='plain-path-stage'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "unsafe_identity" {
		t.Fatalf("marker=%q", marker)
	}
	if _, err := os.Stat(wrong); !os.IsNotExist(err) {
		t.Fatalf("wrong path created: %v", err)
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("quarantine changed: %v", err)
	}
}
