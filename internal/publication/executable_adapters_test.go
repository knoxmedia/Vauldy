package publication

import (
	"context"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestAIAnalysisExecutableEmptySuccessfulRecognitionIsNoop(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "ai-empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type) VALUES(1,1,'f','video')`); err != nil {
		t.Fatal(err)
	}
	exec := AIAnalysisExecutable{DB: db}
	if err := exec.Execute(context.Background(), 1); err != nil {
		t.Fatalf("empty recognition should no-op: %v", err)
	}
	if err := exec.Execute(context.Background(), 0); err == nil {
		t.Fatal("invalid media id should fail")
	}
}
