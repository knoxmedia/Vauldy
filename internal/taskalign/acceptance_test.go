package taskalign

import (
	"context"
	"fmt"
	"testing"

	"knox-media/internal/store"
)

// TestAcceptanceProdSkewScenarios locks the known dual-table count divergences
// from production into Compute (+ Ensure for waiting-without-domain).
func TestAcceptanceProdSkewScenarios(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}

	mustExec(`INSERT INTO library(id,name,type,path) VALUES(1,'skew','video','/skew')`)

	// 10 waiting queue subtitle without domain → Ensure creates pending → waiting=10
	for id := int64(1); id <= 10; id++ {
		mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(?,?,?,1)`, id, 1, fmt.Sprintf("wait-%d", id))
		mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(?,1,'subtitle','waiting',3)`, id)
		if err := EnsureDomainWaiting(ctx, db, "subtitle", id); err != nil {
			t.Fatalf("ensure waiting media %d: %v", id, err)
		}
	}

	// 12 cancelled queue without domain → cancelled=12
	for id := int64(11); id <= 22; id++ {
		mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(?,?,?,1)`, id, 1, fmt.Sprintf("cancel-%d", id))
		mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(?,1,'subtitle','cancelled',3)`, id)
	}

	// 8 failed queue + pending domain → failed=8 (not waiting)
	for id := int64(23); id <= 30; id++ {
		mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(?,?,?,1)`, id, 1, fmt.Sprintf("fail-%d", id))
		mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(?,1,'subtitle','failed',3)`, id)
		mustExec(`INSERT INTO subtitle_task(media_id,status) VALUES(?,'pending')`, id)
	}

	// 6 media with two done generations → done=6 not 12
	for id := int64(31); id <= 36; id++ {
		mustExec(`INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(?,?,?,2)`, id, 1, fmt.Sprintf("done-%d", id))
		mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(?,1,'subtitle','done',3)`, id)
		mustExec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(?,2,'subtitle','done',3)`, id)
		mustExec(`INSERT INTO subtitle_task(media_id,status) VALUES(?,'done')`, id)
	}

	got, err := Compute(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	sub := got.ByType["subtitle"]

	if sub["waiting"] != 10 {
		t.Errorf("waiting = %d, want 10", sub["waiting"])
	}
	if sub["cancelled"] != 12 {
		t.Errorf("cancelled = %d, want 12", sub["cancelled"])
	}
	if sub["failed"] != 8 {
		t.Errorf("failed = %d, want 8 (queue failed + domain pending must not count as waiting)", sub["failed"])
	}
	if sub["done"] != 6 {
		t.Errorf("done = %d, want 6 (one per media, current generation only)", sub["done"])
	}
	if sub["running"] != 0 {
		t.Errorf("running = %d, want 0", sub["running"])
	}

	var rawDone int
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE task_type='subtitle' AND status='done'`).Scan(&rawDone); err != nil {
		t.Fatal(err)
	}
	if rawDone != 12 {
		t.Fatalf("fixture raw done rows = %d, want 12 (proves media-level dedupe)", rawDone)
	}
}
