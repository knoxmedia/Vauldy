package postingest

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/keystore"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
)

func TestEncryptedPhotoRecoveryRealAdaptersEndToEnd(t *testing.T) {
	db, _ := openQueueTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "legacy.jpg")
	f, _ := os.Create(source)
	_ = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)
	_ = f.Close()
	res, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled,encrypted_assets_dir_mode) VALUES('photos','photo',?,1,'library')`, root)
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,meta_json,status,publication_state,published_at) VALUES(?,'legacy',?,'image','{}','active','published',CURRENT_TIMESTAMP)`, lib, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	planner := publication.NewPlanner(publication.PlanOptions{EncryptGlobal: true})
	if n, e := publication.RepairLegacyMedia(context.Background(), db, planner, 10); e != nil || n != 1 {
		t.Fatalf("repair=%d err=%v", n, e)
	}
	vault, e := keystore.NewVault(string(bytes.Repeat([]byte{0x42}, 32)), "")

	if e != nil {
		t.Fatal(e)
	}

	derived := &storage.DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(root, "derived")}

	thumbWorker := realThumbnailStager(t, db)
	thumbWorker.Vault = vault
	thumbWorker.Derived = derived

	q1 := NewQueue(db, "restart-before-encrypt", nil)
	thumb, e := q1.Claim(context.Background(), TaskThumbnail)
	if e != nil || thumb == nil {
		t.Fatalf("thumbnail=%+v err=%v", thumb, e)
	}
	result, e := NewThumbnailAdapter(db, thumbWorker).(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	}).ExecuteWithResult(context.Background(), *thumb)
	if e != nil || result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("thumbnail result=%+v err=%v", result, e)
	}
	q2 := NewQueue(db, "restart-encrypt", nil)
	encryptTask, e := q2.Claim(context.Background(), TaskEncrypt)
	if e != nil || encryptTask == nil {
		t.Fatalf("encrypt=%+v err=%v", encryptTask, e)
	}
	enc := &storage.AssetEncryptor{DB: db, Vault: vault, DataDir: root, BasePath: filepath.Join(root, "encrypted")}
	result, e = NewEncryptAdapter(enc).(interface {
		ExecuteWithResult(context.Context, Task) (ExecutionResult, error)
	}).ExecuteWithResult(context.Background(), *encryptTask)
	if e != nil {
		t.Fatal(e)
	}
	var selected, state, journal string
	var evidence int
	if e = db.QueryRow(`SELECT m.file_path,m.publication_state,j.state,(SELECT COUNT(*) FROM media_ingest_evidence e WHERE e.media_id=m.id AND e.generation=m.ingest_generation AND e.kind='encrypt') FROM media m JOIN media_encryption_stage_journal j ON j.media_id=m.id WHERE m.id=?`, mediaID).Scan(&selected, &state, &journal, &evidence); e != nil {
		t.Fatal(e)
	}
	if filepath.Ext(selected) != ".enc" || state != "published" || journal != "committed" || evidence != 1 {
		t.Fatalf("selected=%s state=%s journal=%s evidence=%d", selected, state, journal, evidence)
	}
	// Encryption completion hands plaintext cleanup to retirement; without an
	// explicit cleanup request the source must remain present.
	if _, e = os.Stat(source); e != nil {
		t.Fatalf("plaintext should remain after encryption handoff: %v", e)
	}
	if n, e := publication.RepairLegacyMedia(context.Background(), db, publication.NewPlanner(publication.PlanOptions{EncryptGlobal: true}), 10); e != nil || n != 0 {
		t.Fatalf("restart repair=%d err=%v", n, e)
	}
}

var _ *sql.DB
