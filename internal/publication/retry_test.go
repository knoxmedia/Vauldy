package publication

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func openRetryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedRetryV2(t *testing.T, db *sql.DB) (mediaID, runID, posterID, scrapeID int64, snapshot string) {
	t.Helper()
	snapshot = `{"policy_version":2,"library_id":1,"file_type":"video","steps":["poster","scrape"],"required_steps":["poster"],"optional_steps":["scrape"],"dependencies":[{"step":"poster","kind":"media_visible"},{"step":"scrape","kind":"step_done","depends_on":"poster"}]}`
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('retry','video','/retry')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,publication_state,ingest_generation) VALUES(?,'retry-v2','video','failed',1)`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','failed',?,2)`, mediaID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,max_attempts) VALUES(?,?,1,'poster',1,'failed',4)`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	posterID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,max_attempts) VALUES(?,?,1,'scrape',0,'cancelled',5)`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	scrapeID, _ = res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(?,NULL,'media_visible'),(?,?,'step_done')`, posterID, scrapeID, posterID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(?,?,?,1,'poster','failed')`, mediaID, runID, posterID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO scrape_task(media_id,source,status,ingest_run_id,ingest_step_id,generation) VALUES(?,'auto-scan','failed',?,?,1)`, mediaID, runID, scrapeID); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRetryIngestV2PreservesPolicyAndDependencies(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, oldRunID, _, _, snapshot := seedRetryV2(t, db)
	if err := RetryIngest(context.Background(), db, mediaID, nil); err != nil {
		t.Fatalf("retry: %v", err)
	}

	var newRunID int64
	var policy int
	var gotSnapshot string
	if err := db.QueryRow(`SELECT id,policy_version,config_snapshot_json FROM media_ingest_run WHERE media_id=? AND generation=2`, mediaID).Scan(&newRunID, &policy, &gotSnapshot); err != nil {
		t.Fatal(err)
	}
	if newRunID == oldRunID || policy != PolicyV2 || gotSnapshot != snapshot {
		t.Fatalf("new run=%d policy=%d snapshot=%s", newRunID, policy, gotSnapshot)
	}

	rows, err := db.Query(`SELECT s.step_type,s.required,s.status,s.max_attempts,d.dependency_kind,COALESCE(t.step_type,'') FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id LEFT JOIN media_ingest_step t ON t.id=d.depends_on_step_id WHERE s.run_id=? ORDER BY s.step_type`, newRunID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type edge struct {
		step         string
		required     int
		status       string
		max          int
		kind, target string
	}
	var got []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.step, &e.required, &e.status, &e.max, &e.kind, &e.target); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (edge{"poster", 1, "waiting", 4, "media_visible", ""}) || got[1] != (edge{"scrape", 0, "waiting", 5, "step_done", "poster"}) {
		t.Fatalf("dependencies=%+v", got)
	}
	for _, table := range []string{"post_ingest_task", "scrape_task"} {
		var bad int
		q := `SELECT COUNT(*) FROM ` + table + ` q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.ingest_run_id=? AND (s.run_id<>q.ingest_run_id OR s.generation<>q.generation)`
		if err := db.QueryRow(q, newRunID).Scan(&bad); err != nil || bad != 0 {
			t.Fatalf("%s linkage bad=%d err=%v", table, bad, err)
		}
	}
}

func TestRetryIngestV2LeavesEvidenceAndStagesBehind(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, runID, posterID, _, _ := seedRetryV2(t, db)
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('stage-old',?,?,?,1,'owner','fp','poster','committed','/stage','{}')`, mediaID, runID, posterID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,verified_at,stage_id) VALUES(?,?,?,1,'poster','fp','{}',CURRENT_TIMESTAMP,'stage-old')`, runID, posterID, mediaID); err != nil {
		t.Fatal(err)
	}
	if err := RetryIngest(context.Background(), db, mediaID, nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"media_ingest_evidence", "media_asset_stage_journal"} {
		var oldCount, newCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE run_id=?`, runID).Scan(&oldCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE media_id=? AND generation=2`, mediaID).Scan(&newCount); err != nil {
			t.Fatal(err)
		}
		if oldCount != 1 || newCount != 0 {
			t.Fatalf("%s old=%d new=%d", table, oldCount, newCount)
		}
	}
}

func TestRetryIngestMalformedOldDependencyRollsBack(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, runID, _, scrapeID, _ := seedRetryV2(t, db)
	res, err := db.Exec(`INSERT INTO media(library_id,file_id,file_type,publication_state,ingest_generation) SELECT library_id,'other','video','processing',1 FROM media WHERE id=?`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	otherMedia, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing','{"policy_version":2}',2)`, otherMedia)
	if err != nil {
		t.Fatal(err)
	}
	otherRun, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'poster',1,'waiting')`, otherRun, otherMedia)
	if err != nil {
		t.Fatal(err)
	}
	foreignStep, _ := res.LastInsertId()
	if _, err = db.Exec(`UPDATE media_ingest_step_dependency SET depends_on_step_id=? WHERE step_id=? AND dependency_kind='step_done'`, foreignStep, scrapeID); err != nil {
		t.Fatal(err)
	}

	if err := RetryIngest(context.Background(), db, mediaID, nil); err == nil {
		t.Fatal("retry accepted cross-run old dependency")
	}
	var generation, runs int
	if err := db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || runs != 1 {
		t.Fatalf("rollback generation=%d runs=%d oldRun=%d", generation, runs, runID)
	}
}
