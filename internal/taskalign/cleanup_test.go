package taskalign

import (
	"context"
	"database/sql"
	"testing"

	"knox-media/internal/store"
)

func openCleanupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'cleanup','video','/cleanup');
		INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES
			(11,1,'cleanup-11',2),
			(12,1,'cleanup-12',1),
			(13,1,'cleanup-13',1),
			(14,1,'cleanup-14',1);
		INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES
			(11,2,'subtitle','failed',3),
			(11,1,'subtitle','done',3),
			(12,1,'preview','waiting',3),
			(12,1,'atrack','cancelled',3),
			(12,1,'keyframe','failed',3),
			(13,1,'preview','running',3),
			(14,1,'subtitle','running',3)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func countQueue(t *testing.T, db *sql.DB, mediaID int64, taskType, status string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=? AND task_type=? AND status=?`, mediaID, taskType, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteCurrentGenQueueTasksRemovesTerminalRows(t *testing.T) {
	db := openCleanupTestDB(t)
	ctx := context.Background()

	if err := DeleteCurrentGenQueueTasks(ctx, db, "subtitle", 11); err != nil {
		t.Fatal(err)
	}
	if got := countQueue(t, db, 11, "subtitle", "failed"); got != 0 {
		t.Fatalf("current-gen failed should be deleted, got %d", got)
	}
	if got := countQueue(t, db, 11, "subtitle", "done"); got != 1 {
		t.Fatalf("prior-gen done should remain, got %d", got)
	}

	if err := DeleteCurrentGenQueueTasks(ctx, db, "preview", 12); err != nil {
		t.Fatal(err)
	}
	if err := DeleteCurrentGenQueueTasks(ctx, db, "atrack", 12); err != nil {
		t.Fatal(err)
	}
	if err := DeleteCurrentGenQueueTasks(ctx, db, "keyframe", 12); err != nil {
		t.Fatal(err)
	}
	if countQueue(t, db, 12, "preview", "waiting") != 0 ||
		countQueue(t, db, 12, "atrack", "cancelled") != 0 ||
		countQueue(t, db, 12, "keyframe", "failed") != 0 {
		t.Fatal("expected preview/atrack/keyframe terminal rows deleted")
	}
}

func TestDeleteCurrentGenQueueTasksSkipsRunning(t *testing.T) {
	db := openCleanupTestDB(t)
	if err := DeleteCurrentGenQueueTasks(context.Background(), db, "subtitle", 14); err != nil {
		t.Fatal(err)
	}
	if got := countQueue(t, db, 14, "subtitle", "running"); got != 1 {
		t.Fatalf("running queue row must remain, got %d", got)
	}
	if err := DeleteCurrentGenQueueTasks(context.Background(), db, "preview", 13); err != nil {
		t.Fatal(err)
	}
	if got := countQueue(t, db, 13, "preview", "running"); got != 1 {
		t.Fatalf("running preview must remain, got %d", got)
	}
}

func TestDeleteCurrentGenQueueTasksNoopForUnaligned(t *testing.T) {
	if err := DeleteCurrentGenQueueTasks(context.Background(), nil, "encrypt", 1); err != nil {
		t.Fatal(err)
	}
}
