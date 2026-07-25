package publication

import (
	"context"
	"errors"
	"knox-media/internal/store"
	"path/filepath"
	"testing"
)

type recordingResourceValidator struct {
	calls []EncryptedLibrary
	err   error
}

func (r *recordingResourceValidator) ValidateEncryptedLibrary(_ context.Context, _ store.SQLExecutor, lib EncryptedLibrary) error {
	r.calls = append(r.calls, lib)
	return r.err
}
func (r *recordingResourceValidator) ProbePosterResolver(context.Context) error    { return nil }
func (r *recordingResourceValidator) ProbeThumbnailResolver(context.Context) error { return nil }

func TestPreflightEnumeratesEncryptedLibrariesThroughExecutableValidator(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "resources.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	_, err = db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled,encrypted_assets_dir_mode) VALUES('a','video',?,1,'library'),('b','photo',?,1,'data')`, root, root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster", "thumbnail", "encrypt", "scrape"})
	rv := &recordingResourceValidator{}
	_, err = PreflightPublicationV2(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true, Capabilities: registry}), registry, rv)
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.calls) != 2 || rv.calls[0].ID == 0 || rv.calls[0].Path == "" || rv.calls[1].Mode != "data" {
		t.Fatalf("calls=%+v", rv.calls)
	}
}

func TestPlannerEncryptionValidatorClosesConfigurationRace(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 1, 0)
	v := &recordingResourceValidator{err: errors.New("vault revoked")}
	reg := NewCapabilityMatrix([]string{"encrypt"})
	tx, _ := db.BeginTx(context.Background(), nil)
	_, err := NewPlanner(PlanOptions{EncryptGlobal: true, Capabilities: reg, EncryptionValidator: v}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	_ = tx.Rollback()
	if err == nil || len(v.calls) != 1 {
		t.Fatalf("err=%v calls=%v", err, v.calls)
	}
	var runs int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mid).Scan(&runs)
	if runs != 0 {
		t.Fatalf("runs=%d", runs)
	}
}
