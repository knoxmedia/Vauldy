package taskcontrol

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"knox-media/internal/scancoord"
	"knox-media/internal/store"
)

type scanCoordinatorSpy struct {
	requests []scancoord.ScanRequest
	result   scancoord.SubmitResult
	err      error
}

func (s *scanCoordinatorSpy) Submit(_ context.Context, req scancoord.ScanRequest) (scancoord.SubmitResult, error) {
	s.requests = append(s.requests, req)
	return s.result, s.err
}
func (*scanCoordinatorSpy) Cancel(context.Context, int64) (scancoord.CancelResult, error) {
	return scancoord.CancelResult{Cancelled: true, Status: "cancelled"}, nil
}

func openScanControlDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestScanTaskControllerRemovePreservesReferencesAndAudit(t *testing.T) {
	db := openScanControlDB(t)
	if _, err := db.Exec(`
INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/fallback');
INSERT INTO media(id,library_id,file_id,file_type) VALUES(1,1,'f','video');
INSERT INTO scan_task(id,library_id,status,source,finished_at) VALUES(10,1,'failed','manual',CURRENT_TIMESTAMP);
INSERT INTO scan_log(scan_task_id,library_id,file_path,action,message) VALUES(10,1,'/f','error','kept');
INSERT INTO media_ingest_run(id,media_id,generation,scan_task_id,reason,status,config_snapshot_json) VALUES(20,1,1,10,'scan','failed','{}');
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,1,1,'poster',1,'failed');
INSERT INTO post_ingest_task(id,media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,1,10,20,30,1,'poster','failed');
`); err != nil {
		t.Fatal(err)
	}
	req := ExternalOperationRequest{ID: 10, Identity: "scan_task:10", ActorID: 7, Reason: "clean history"}
	if err := NewScanTaskController(db, nil).Remove(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var tasks, logs, runs, post, audits int
	var logScan, runScan, postScan sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_task WHERE id=10`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*),scan_task_id FROM scan_log WHERE file_path='/f'`).Scan(&logs, &logScan); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*),scan_task_id FROM media_ingest_run WHERE id=20`).Scan(&runs, &runScan); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*),scan_task_id FROM post_ingest_task WHERE id=40`).Scan(&post, &postScan); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_control_audit WHERE task_identity='scan_task:10' AND action='remove'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 || logs != 1 || runs != 1 || post != 1 || logScan.Valid || runScan.Valid || postScan.Valid || audits != 1 {
		t.Fatalf("tasks=%d logs=%d/%v runs=%d/%v post=%d/%v audits=%d", tasks, logs, logScan, runs, runScan, post, postScan, audits)
	}
}

func TestScanTaskControllerRemoveRejectsActiveRows(t *testing.T) {
	db := openScanControlDB(t)
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO scan_task(id,library_id,status,source) VALUES(10,1,'waiting','manual'),(11,1,'running','manual')`); err != nil {
		t.Fatal(err)
	}
	controller := NewScanTaskController(db, nil)
	for _, id := range []int64{10, 11} {
		if err := controller.Remove(context.Background(), ExternalOperationRequest{ID: id, Identity: "scan_task:active"}); err == nil {
			t.Fatalf("remove active task %d unexpectedly succeeded", id)
		}
	}
}

func TestScanTaskControllerResetSubmitsNewScanWithLibraryFolders(t *testing.T) {
	db := openScanControlDB(t)
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/fallback'); INSERT INTO library_folder(library_id,path,sort_order) VALUES(1,'/b',2),(1,'/a',1); INSERT INTO scan_task(id,library_id,status,source,finished_at) VALUES(10,1,'failed','manual',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	spy := &scanCoordinatorSpy{result: scancoord.SubmitResult{TaskID: 22, Started: true}}
	req := ExternalOperationRequest{ID: 10, Identity: "scan_task:10", ActorID: 9, Reason: "retry"}
	if err := NewScanTaskController(db, spy).Reset(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) != 1 || spy.requests[0].LibraryID != 1 || spy.requests[0].Source != scancoord.SourceManual || len(spy.requests[0].Roots) != 2 || spy.requests[0].Roots[0] != filepath.Clean("/a") {
		t.Fatalf("requests=%+v", spy.requests)
	}
	var oldStatus, newStatus, outcome string
	var metadata string
	if err := db.QueryRow(`SELECT status FROM scan_task WHERE id=10`).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT new_status,outcome_code,metadata_json FROM task_control_audit WHERE task_identity='scan_task:10' AND action='reset'`).Scan(&newStatus, &outcome, &metadata); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "failed" || newStatus != "submitted" || outcome != "new" || metadata == "" {
		t.Fatalf("old=%s new=%s outcome=%s metadata=%s", oldStatus, newStatus, outcome, metadata)
	}
}

func TestScanTaskControllerResetTreatsExistingActiveScanAsSuccess(t *testing.T) {
	db := openScanControlDB(t)
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/fallback'); INSERT INTO scan_task(id,library_id,status,source,finished_at) VALUES(10,1,'cancelled','manual',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	spy := &scanCoordinatorSpy{result: scancoord.SubmitResult{ExistingTaskID: 99}}
	if err := NewScanTaskController(db, spy).Reset(context.Background(), ExternalOperationRequest{ID: 10, Identity: "scan_task:10"}); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := db.QueryRow(`SELECT outcome_code FROM task_control_audit WHERE task_identity='scan_task:10' AND action='reset'`).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "existing" {
		t.Fatalf("outcome=%s", outcome)
	}
}
