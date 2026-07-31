package taskalign

import (
	"context"
	"testing"

	"knox-media/internal/store"
)

func TestComputeCurrentGenerationAlignment(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	mustExec(`INSERT INTO library(id,name,type,path) VALUES(1,'alignment','video','/alignment')`)
	mustExec(`INSERT INTO media(id,library_id,file_id,file_type,ingest_generation) VALUES
		(1,1,'alignment-1','video',2),
		(2,1,'alignment-2','video',1)`)
	mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status) VALUES
		(1,1,'subtitle','done'),
		(1,2,'subtitle','waiting'),
		(2,1,'subtitle','failed'),
		(2,1,'encrypt','waiting')`)
	mustExec(`INSERT INTO subtitle_task(media_id,status) VALUES
		(1,'pending'),
		(2,'pending')`)

	got, err := Compute(context.Background(), db)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}

	if got.ByType["subtitle"]["waiting"] != 1 {
		t.Errorf("subtitle waiting = %d, want 1", got.ByType["subtitle"]["waiting"])
	}
	if got.ByType["subtitle"]["failed"] != 1 {
		t.Errorf("subtitle failed = %d, want 1", got.ByType["subtitle"]["failed"])
	}
	if got.ByType["subtitle"]["done"] != 0 {
		t.Errorf("subtitle done = %d, want 0 (stale generation must be ignored)", got.ByType["subtitle"]["done"])
	}
	if got.ByType["encrypt"]["waiting"] < 1 {
		t.Errorf("encrypt waiting = %d, want at least 1", got.ByType["encrypt"]["waiting"])
	}
}
