package publication

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func TestPlanReplacementSupersedesActiveGenerationGraph(t *testing.T) {
	skipIfEnterprisePrepareUnavailable(t)
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	old := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if _, err := db.Exec(`UPDATE media_ingest_run SET error_message='immutable error' WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='running',lease_owner='old-owner',lease_until='2040-01-01',started_at=CURRENT_TIMESTAMP WHERE ingest_run_id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='running',lease_owner='old-owner',lease_until='2040-01-01',started_at=CURRENT_TIMESTAMP WHERE run_id=? AND step_type='poster'`, old.ID); err != nil {
		t.Fatal(err)
	}
	var scrapeStep int64
	if err := db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='scrape'`, old.ID).Scan(&scrapeStep); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE scrape_task SET status='running',lease_owner='scraper',lease_until='2040-01-01',started_at=CURRENT_TIMESTAMP WHERE ingest_run_id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='running',lease_owner='scraper',lease_until='2040-01-01',started_at=CURRENT_TIMESTAMP WHERE id=?`, scrapeStep); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,lease_owner,lease_until) VALUES(?,?,?,'prepare',0,'running','encoder','2040-01-01')`, old.ID, mediaID, old.Generation)
	if err != nil {
		t.Fatal(err)
	}
	prepareStep, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until) VALUES('supersede','running','pretranscode',?,?,?,?, 'encoder','2040-01-01')`, mediaID, old.ID, prepareStep, old.Generation)
	if err != nil {
		t.Fatal(err)
	}
	transcodeID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,lease_owner,lease_until) VALUES(?,1,'active','running','encoder','2040-01-01'),(?,2,'terminal','done',NULL,NULL)`, transcodeID, transcodeID); err != nil {
		t.Fatal(err)
	}

	next := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: old.Generation})
	var status, reason, oldError string
	var superseded sql.NullInt64
	var supersededAt, finished sql.NullTime
	if err = db.QueryRow(`SELECT status,terminal_reason,error_message,superseded_by_generation,superseded_at,finished_at FROM media_ingest_run WHERE id=?`, old.ID).Scan(&status, &reason, &oldError, &superseded, &supersededAt, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || reason != supersededTerminalReason || oldError != "immutable error" || !superseded.Valid || superseded.Int64 != next.NewGeneration || !supersededAt.Valid || !finished.Valid {
		t.Fatalf("old run=%s/%q/%q superseded=%v at=%v finished=%v", status, reason, oldError, superseded, supersededAt, finished)
	}
	for table, where := range map[string]string{
		"media_ingest_step": "run_id", "post_ingest_task": "ingest_run_id", "scrape_task": "ingest_run_id", "transcode_task": "ingest_run_id",
	} {
		var active, uncleared int
		q := `SELECT COUNT(*),COALESCE(SUM(lease_owner IS NOT NULL OR lease_until IS NOT NULL),0) FROM ` + table + ` WHERE ` + where + `=? AND status IN ('waiting','running')`
		if err = db.QueryRow(q, old.ID).Scan(&active, &uncleared); err != nil {
			t.Fatal(err)
		}
		if active != 0 || uncleared != 0 {
			t.Fatalf("%s active=%d uncleared=%d", table, active, uncleared)
		}
	}
	var activeJobs, terminalJobs int
	if err = db.QueryRow(`SELECT COALESCE(SUM(status IN ('waiting','running')),0),COALESCE(SUM(status='done'),0) FROM pretranscode_rendition_job WHERE task_id=?`, transcodeID).Scan(&activeJobs, &terminalJobs); err != nil {
		t.Fatal(err)
	}
	if activeJobs != 0 || terminalJobs != 1 {
		t.Fatalf("jobs active=%d terminal=%d", activeJobs, terminalJobs)
	}
}

func TestPlanReplacementPreservesTerminalRunOutcomeAndRollsBackAtomically(t *testing.T) {
	t.Run("terminal outcome", func(t *testing.T) {
		db := openPlannerTestDB(t)
		_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
		old := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
		if _, err := db.Exec(`UPDATE media_ingest_run SET status='failed',terminal_reason='original',error_message='original error',finished_at='2020-01-01' WHERE id=?`, old.ID); err != nil {
			t.Fatal(err)
		}
		next := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonManualRetry, ExpectedGeneration: old.Generation})
		var status, reason, oldError, finished string
		var superseded int64
		if err := db.QueryRow(`SELECT status,terminal_reason,error_message,finished_at,superseded_by_generation FROM media_ingest_run WHERE id=?`, old.ID).Scan(&status, &reason, &oldError, &finished, &superseded); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || reason != "original" || oldError != "original error" || finished != "2020-01-01T00:00:00Z" || superseded != next.NewGeneration {
			t.Fatalf("terminal changed: %q %q %q %q %d", status, reason, oldError, finished, superseded)
		}
	})
	t.Run("rollback", func(t *testing.T) {
		db := openPlannerTestDB(t)
		_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
		old := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
		if _, err := db.Exec(`CREATE TRIGGER reject_supersession BEFORE UPDATE ON media_ingest_run WHEN OLD.id=` + fmt.Sprint(old.ID) + ` BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewPlanner(PlanOptions{}).PlanReplacementTx(context.Background(), tx, mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: old.Generation})
		if err == nil {
			t.Fatal("replacement unexpectedly succeeded")
		}
		_ = tx.Rollback()
		var generation, runs int
		var status string
		if err = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, old.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if generation != int(old.Generation) || runs != 1 || status != "processing" {
			t.Fatalf("rollback generation=%d runs=%d status=%s", generation, runs, status)
		}
	})
}

func TestAggregateSupersededRunRemainsCancelled(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "cancelled", 1, map[string]string{"poster": "done"})
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2,publication_state='processing' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_run SET superseded_by_generation=2,superseded_at=CURRENT_TIMESTAMP,terminal_reason=? WHERE id=?`, supersededTerminalReason, runID); err != nil {
		t.Fatal(err)
	}
	aggregateCall(t, db, runID)
	var run, media string
	_ = db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&run)
	_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&media)
	if run != "cancelled" || media != "processing" {
		t.Fatalf("run=%s media=%s", run, media)
	}
}

func TestPlanReplacementSupportsCommunityWithoutEnterpriseTables(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	old := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	dropEnterprisePrepareTablesIfPresent(t, db)
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: old.Generation})
	if result.NewGeneration != old.Generation+1 {
		t.Fatalf("generation=%d", result.NewGeneration)
	}
}

func TestPlanReplacementRejectsPartialEnterpriseSchema(t *testing.T) {
	skipIfEnterprisePrepareUnavailable(t)
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	old := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if _, err := db.Exec(`DROP TABLE pretranscode_rendition_job`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPlanner(PlanOptions{}).PlanReplacementTx(context.Background(), tx, mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: old.Generation})
	if err == nil {
		t.Fatal("partial enterprise schema accepted")
	}
	_ = tx.Rollback()
}

func TestAggregateSupersededRunPreservesAllTerminalFields(t *testing.T) {
	for _, state := range []string{"cancelled", "failed"} {
		t.Run(state, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, state, 1, map[string]string{"poster": "waiting"})
			if _, err := db.Exec(`UPDATE media SET ingest_generation=2,publication_state='degraded',publication_error='media immutable' WHERE id=?`, mediaID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media_ingest_run SET terminal_reason='original reason',error_message='original error',finished_at='2020-01-01',superseded_by_generation=2,superseded_at='2020-01-02' WHERE id=?`, runID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media_ingest_step SET status='done',last_error='mutated' WHERE run_id=?`, runID); err != nil {
				t.Fatal(err)
			}
			aggregateCall(t, db, runID)
			var gotStatus, reason, runError, finished, supersededAt, mediaState, mediaError string
			var superseded int64
			if err := db.QueryRow(`SELECT status,terminal_reason,error_message,finished_at,superseded_by_generation,superseded_at FROM media_ingest_run WHERE id=?`, runID).Scan(&gotStatus, &reason, &runError, &finished, &superseded, &supersededAt); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT publication_state,publication_error FROM media WHERE id=?`, mediaID).Scan(&mediaState, &mediaError); err != nil {
				t.Fatal(err)
			}
			if gotStatus != state || reason != "original reason" || runError != "original error" || finished != "2020-01-01T00:00:00Z" || superseded != 2 || supersededAt != "2020-01-02T00:00:00Z" || mediaState != "degraded" || mediaError != "media immutable" {
				t.Fatalf("run=%q/%q/%q/%q/%d/%q media=%q/%q", gotStatus, reason, runError, finished, superseded, supersededAt, mediaState, mediaError)
			}
		})
	}
}
