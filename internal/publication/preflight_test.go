package publication

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/store"
)

func TestPreflightRejectsEncryptedLibraryCapabilityGap(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "preflight.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled) VALUES('secure','video',?,1)`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster", "thumbnail", "scrape"})
	_, err = PreflightPublicationV2(context.Background(), db, NewPlanner(PlanOptions{EncryptGlobal: true, Capabilities: registry}), registry, &recordingResourceValidator{})
	if err == nil || !strings.Contains(err.Error(), "encrypted library") || !strings.Contains(err.Error(), "encrypt") {
		t.Fatalf("err=%v", err)
	}
}

func TestPreflightOptionalAdapterOnlyWarns(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "optional.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if _, err = db.Exec(`INSERT INTO library(name,type,path,preview_extract) VALUES('videos','video',?,1)`, root); err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster", "thumbnail", "scrape"})
	warnings, err := PreflightPublicationV2(context.Background(), db, NewPlanner(PlanOptions{Capabilities: registry}), registry, &recordingResourceValidator{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "preview") {
		t.Fatalf("warnings=%v", warnings)
	}
}
