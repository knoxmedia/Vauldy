package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

func TestRestartRecoveryResetInterruptedTasksPreservesResourceControlledScans(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "restart-reset.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	libraryResult, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('restart','video','/restart')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := libraryResult.LastInsertId()

	insertScan := func(status string, cancelled int) int64 {
		t.Helper()
		result, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,cancelled,started_at) VALUES(?,?,?,?,CURRENT_TIMESTAMP)`, libraryID, status, "manual", cancelled)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	activeScan := insertScan("running", 0)
	orphanScan := insertScan("running", 0)
	cancelledScan := insertScan("cancelled", 1)
	if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 hour'))`, libraryID, activeScan, "other-process/active"); err != nil {
		t.Fatal(err)
	}

	mediaResult, err := db.Exec(`INSERT INTO media(library_id,file_id,file_type) VALUES(?,'restart-reset-media','video')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaResult.LastInsertId()
	postResult, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES(?,?,?,'running',1,3,'dead-worker/generation',datetime(CURRENT_TIMESTAMP,'-1 second'))`, mediaID, orphanScan, postingest.TaskPoster)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postResult.LastInsertId()

	store.ResetInterruptedTasks(db)

	for id, want := range map[int64]struct {
		status    string
		cancelled int
	}{
		activeScan:    {"running", 0},
		orphanScan:    {"running", 0},
		cancelledScan: {"cancelled", 1},
	} {
		var status string
		var cancelled int
		if err := db.QueryRow(`SELECT status,cancelled FROM scan_task WHERE id=?`, id).Scan(&status, &cancelled); err != nil {
			t.Fatal(err)
		}
		if status != want.status || cancelled != want.cancelled {
			t.Fatalf("scan %d status=%s cancelled=%d want %s,%d", id, status, cancelled, want.status, want.cancelled)
		}
	}

	q := postingest.NewQueue(db, "new-worker", nil)
	if recovered, err := q.RecoverExpired(context.Background()); err != nil || recovered != 1 {
		t.Fatalf("RecoverExpired=(%d,%v) want (1,nil)", recovered, err)
	}
	var postStatus postingest.Status
	var owner sql.NullString
	if err := db.QueryRow(`SELECT status,lease_owner FROM post_ingest_task WHERE id=?`, postID).Scan(&postStatus, &owner); err != nil {
		t.Fatal(err)
	}
	if postStatus != postingest.StatusWaiting || owner.Valid {
		t.Fatalf("post status=%s owner=%v want waiting,nil", postStatus, owner)
	}
}
