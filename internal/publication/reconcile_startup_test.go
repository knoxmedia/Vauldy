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
