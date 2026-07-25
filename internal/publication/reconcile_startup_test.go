package publication

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"knox-media/internal/store"
)

func seedActiveV1(t *testing.T, dbPath, fileType, state string) (*Planner, int64) {
	t.Helper()
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	libType := "video"
	if fileType == "image" {
		libType = "photo"
	}
	r, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('legacy',?,?)`, libType, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,published_at,ingest_generation) VALUES(?,'f','x',?,'active',?,CASE WHEN ? IN ('published','degraded') THEN CURRENT_TIMESTAMP END,1)`, lid, fileType, state, state)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing',0,'{}',1)`, mid)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := r.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,lease_owner) VALUES(?,?,1,'poster',1,'running','old')`, rid, mid); err != nil {
		t.Fatal(err)
	}
	return NewPlanner(PlanOptions{}), mid
}

func TestActiveV1ReplacementVideoAndPhotoCurrentPolicy(t *testing.T) {
	for _, tc := range []struct{ typ, state, required string }{{"video", "published", "poster"}, {"image", "degraded", "thumbnail"}} {
		t.Run(tc.typ, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v1.sqlite")
			planner, mid := seedActiveV1(t, path, tc.typ, tc.state)
			db, _ := store.OpenSQLite(path)
			defer db.Close()
			n, err := ReconcileStartupPublicationV2(context.Background(), db, planner)
			if err != nil || n != 1 {
				t.Fatalf("n=%d err=%v", n, err)
			}
			var generation, policy, preserve int
			var oldStatus, reason, step string
			if err = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mid).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT status,terminal_reason FROM media_ingest_run WHERE media_id=? AND generation=1`, mid).Scan(&oldStatus, &reason); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT r.policy_version,r.preserve_visibility,s.step_type FROM media_ingest_run r JOIN media_ingest_step s ON s.run_id=r.id WHERE r.media_id=? AND r.generation=2 AND s.required=1 ORDER BY s.id LIMIT 1`, mid).Scan(&policy, &preserve, &step); err != nil {
				t.Fatal(err)
			}
			if generation != 2 || policy != 2 || preserve != 1 || step != tc.required || oldStatus != "cancelled" || reason != "superseded_by_policy_v2" {
				t.Fatalf("gen=%d policy=%d preserve=%d step=%s old=%s reason=%s", generation, policy, preserve, step, oldStatus, reason)
			}
		})
	}
}

func TestActiveV1ReplacementConcurrentIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "race.sqlite")
	planner, mid := seedActiveV1(t, path, "video", "published")
	db1, _ := store.OpenSQLite(path)
	defer db1.Close()
	db2, _ := store.OpenSQLite(path)
	defer db2.Close()
	start := make(chan struct{})
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for _, db := range []*sql.DB{db1, db2} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			n, e := ReconcileStartupPublicationV2(context.Background(), db, planner)
			counts <- n
			errs <- e
		}(db)
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var runs int
	_ = db1.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=? AND policy_version=2`, mid).Scan(&runs)
	if total != 1 || runs != 1 {
		t.Fatalf("total=%d runs=%d", total, runs)
	}
	n, err := ReconcileStartupPublicationV2(context.Background(), db1, planner)
	if err != nil || n != 0 {
		t.Fatalf("second n=%d err=%v", n, err)
	}
}

func TestActiveV1ReplacementNewEncryptionHidesVideoAndPhoto(t *testing.T) {
	for _, typ := range []string{"video", "image"} {
		t.Run(typ, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "security.sqlite")
			planner, mid := seedActiveV1(t, path, typ, "published")
			db, _ := store.OpenSQLite(path)
			defer db.Close()
			_, _ = db.Exec(`UPDATE library SET encrypted_assets_enabled=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
			planner.options.EncryptGlobal = true
			n, err := ReplaceActiveV1Runs(context.Background(), db, planner)
			if err != nil || n != 1 {
				t.Fatalf("n=%d err=%v", n, err)
			}
			var preserve int
			var state string
			_ = db.QueryRow(`SELECT preserve_visibility FROM media_ingest_run WHERE media_id=? AND generation=2`, mid).Scan(&preserve)
			_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mid).Scan(&state)
			if preserve != 0 || state != "processing" {
				t.Fatalf("preserve=%d state=%s", preserve, state)
			}
		})
	}
}

func TestValidateCurrentV2RejectsEmptyAndMismatchedSnapshots(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{{"empty", `UPDATE media_ingest_run SET config_snapshot_json='{}'`}, {"required", `UPDATE media_ingest_step SET required=0 WHERE step_type='poster'`}, {"queue", `DELETE FROM post_ingest_task`}, {"dependency", `DELETE FROM media_ingest_step_dependency`}} {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			err := ValidateAggregateCurrentV2(context.Background(), db)
			if err == nil {
				t.Fatalf("run %d accepted malformed %s", run.ID, tc.name)
			}
		})
	}
}

func TestValidateCurrentV2RejectsExactQueueSemanticMismatches(t *testing.T) {
	cases := []struct{ name, mutation string }{
		{"post task type", `UPDATE post_ingest_task SET task_type='encrypt' WHERE task_type='poster'`},
		{"post missing", `DELETE FROM post_ingest_task WHERE task_type='poster'`},
		{"post extra wrong execution", `INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status) SELECT media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,'poster_repair',status FROM post_ingest_task WHERE task_type='poster'`},
		{"post status", `UPDATE post_ingest_task SET status='running' WHERE task_type='poster'`},
		{"post generation", `UPDATE post_ingest_task SET generation=generation+1 WHERE task_type='poster'`},
		{"post media", `UPDATE post_ingest_task SET media_id=media_id+100 WHERE task_type='poster'`},
		{"post run", `UPDATE post_ingest_task SET ingest_run_id=ingest_run_id+100 WHERE task_type='poster'`},
		{"scrape source", `UPDATE scrape_task SET source='manual'`},
		{"scrape status", `UPDATE scrape_task SET status='running'`},
		{"scrape generation", `UPDATE scrape_task SET generation=generation+1`},
		{"scrape media", `UPDATE scrape_task SET media_id=media_id+100`},
		{"scrape run", `UPDATE scrape_task SET ingest_run_id=ingest_run_id+100`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
			planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			_, _ = db.Exec("PRAGMA foreign_keys=OFF")
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			if err := ValidateAggregateCurrentV2(context.Background(), db); err == nil {
				t.Fatal("accepted invalid queue semantics")
			}
		})
	}
}

func TestValidateCurrentV2RejectsPrepareWrongTypeAndIdentity(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{{"type", `UPDATE transcode_task SET task_type='manual'`}, {"generation", `UPDATE transcode_task SET generation=generation+1`}, {"media", `UPDATE transcode_task SET media_id=media_id+100`}, {"run", `UPDATE transcode_task SET ingest_run_id=ingest_run_id+100`}, {"status", `UPDATE transcode_task SET status='running'`}} {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 1)
			planner := NewPlanner(PlanOptions{PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})})
			planAndCommit(t, db, planner, NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			_, _ = db.Exec(`PRAGMA foreign_keys=OFF`)
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			if err := ValidateAggregateCurrentV2(context.Background(), db); err == nil {
				t.Fatal("accepted invalid prepare queue")
			}
		})
	}
}
