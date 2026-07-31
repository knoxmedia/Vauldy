package taskalign

import (
	"context"
	"database/sql"
	"testing"

	"knox-media/internal/store"
)

func openEnsureTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'ensure','video','/ensure');
		INSERT INTO media(id,library_id,file_id) VALUES
			(41,1,'ensure-41'),
			(51,1,'ensure-51'),
			(61,1,'ensure-61')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestEnsureDomainWaitingCreatesAlignedRows(t *testing.T) {
	db := openEnsureTestDB(t)
	ctx := context.Background()

	for _, tc := range []struct {
		taskType string
		table    string
		want     string
	}{
		{"subtitle", "subtitle_task", "pending"},
		{"preview", "preview_task", "waiting"},
		{"atrack", "atrack_task", "waiting"},
		{"keyframe", "keyframe_task", "waiting"},
	} {
		t.Run(tc.taskType, func(t *testing.T) {
			if err := EnsureDomainWaiting(ctx, db, tc.taskType, 41); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := db.QueryRow(`SELECT status FROM ` + tc.table + ` WHERE media_id=41`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("status=%q want %q", got, tc.want)
			}
		})
	}
}

func TestEnsureDomainWaitingPreservesExistingRows(t *testing.T) {
	db := openEnsureTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO subtitle_task(media_id,status) VALUES(51,'failed');
		INSERT INTO preview_task(media_id,status) VALUES(51,'ready');
		INSERT INTO atrack_task(media_id,status) VALUES(51,'done');
		INSERT INTO keyframe_task(media_id,status) VALUES(51,'running')`); err != nil {
		t.Fatal(err)
	}

	for _, taskType := range []string{"subtitle", "preview", "atrack", "keyframe"} {
		if err := EnsureDomainWaiting(context.Background(), db, taskType, 51); err != nil {
			t.Fatal(err)
		}
	}

	for table, want := range map[string]string{
		"subtitle_task": "failed",
		"preview_task":  "ready",
		"atrack_task":   "done",
		"keyframe_task": "running",
	} {
		var got string
		if err := db.QueryRow(`SELECT status FROM ` + table + ` WHERE media_id=51`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s status=%q want %q", table, got, want)
		}
	}
}

func TestEnsureDomainWaitingIgnoresUnalignedTypes(t *testing.T) {
	for _, taskType := range []string{"encrypt", "poster", "unknown"} {
		if err := EnsureDomainWaiting(context.Background(), nil, taskType, 61); err != nil {
			t.Fatalf("%s: %v", taskType, err)
		}
	}
}
