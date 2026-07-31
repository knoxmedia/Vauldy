package taskalign

import (
	"context"
	"fmt"
	"testing"

	"knox-media/internal/store"
)

func TestBackfillMissingDomainTasksCreatesAndIsIdempotent(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v", err)
		}
	}

	mustExec(`INSERT INTO library(id,name,type,path) VALUES(1,'bf','video','/bf')`)
	// 3 waiting subtitle without domain, 1 waiting with domain, 1 running without domain (skip),
	// 1 failed without domain, 1 prior-gen waiting without domain (skip).
	for id := int64(1); id <= 3; id++ {
		mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(?,?,?,1)`, id, 1, fmt.Sprintf("w-%d", id))
		mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(?,1,'subtitle','waiting',3)`, id)
	}
	mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(4,1,'w-4',1)`)
	mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(4,1,'subtitle','waiting',3)`)
	mustExec(`INSERT INTO subtitle_task(media_id,status) VALUES(4,'pending')`)

	mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(5,1,'r-5',1)`)
	mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(5,1,'subtitle','running',3)`)

	mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(6,1,'f-6',1)`)
	mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(6,1,'subtitle','failed',3)`)

	mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(7,1,'old-7',2)`)
	mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(7,1,'subtitle','waiting',3)`)

	mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(8,1,'p-8',1)`)
	mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(8,1,'preview','waiting',3)`)

	got, err := BackfillMissingDomainTasks(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if got.Created != 5 { // 3 subtitle waiting + 1 failed + 1 preview
		t.Fatalf("created=%d byType=%v, want 5", got.Created, got.ByType)
	}
	if got.ByType["subtitle"] != 4 || got.ByType["preview"] != 1 {
		t.Fatalf("byType=%v", got.ByType)
	}

	var pending int
	if err := db.QueryRow(`SELECT COUNT(1) FROM subtitle_task WHERE status='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 5 { // 4 created + 1 pre-existing
		t.Fatalf("subtitle pending=%d, want 5", pending)
	}
	var previewWaiting int
	if err := db.QueryRow(`SELECT COUNT(1) FROM preview_task WHERE media_id=8 AND status='waiting'`).Scan(&previewWaiting); err != nil || previewWaiting != 1 {
		t.Fatalf("preview waiting=%d err=%v", previewWaiting, err)
	}
	var runningDomain int
	if err := db.QueryRow(`SELECT COUNT(1) FROM subtitle_task WHERE media_id=5`).Scan(&runningDomain); err != nil || runningDomain != 0 {
		t.Fatalf("running queue must not get domain row, count=%d err=%v", runningDomain, err)
	}
	var staleDomain int
	if err := db.QueryRow(`SELECT COUNT(1) FROM subtitle_task WHERE media_id=7`).Scan(&staleDomain); err != nil || staleDomain != 0 {
		t.Fatalf("prior-gen waiting must not backfill, count=%d err=%v", staleDomain, err)
	}

	again, err := BackfillMissingDomainTasks(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created != 0 {
		t.Fatalf("second pass created=%d, want 0", again.Created)
	}
}
