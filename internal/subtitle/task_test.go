package subtitle

import (
	"strings"
	"testing"

	"knox-media/internal/store"
)

const deletedByAdminMarker = "deleted by admin"

func openSubtitleDeleteTestDB(t *testing.T) *Service {
	t.Helper()
	db, err := store.OpenSQLite(t.TempDir() + "/subtitle-delete.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'sub','video','/sub')`); err != nil {
		t.Fatal(err)
	}
	return &Service{DB: db}
}

func insertDeleteMedia(t *testing.T, s *Service, id, generation int64) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(?,1,?,'video',?,'processing')`, id, "f", generation); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSubtitleTaskCancelsQueueOnlyWaitingTask(t *testing.T) {
	s := openSubtitleDeleteTestDB(t)
	insertDeleteMedia(t, s, 1, 2)
	if _, err := s.DB.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,lease_owner,lease_until,last_error) VALUES(1,2,'subtitle','waiting','owner','2040-01-01','old')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtitleTask(1); err != nil {
		t.Fatalf("delete queue-only task: %v", err)
	}
	var status, owner, lease, lastError string
	if err := s.DB.QueryRow(`SELECT status,COALESCE(lease_owner,''),COALESCE(lease_until,''),last_error FROM post_ingest_task WHERE media_id=1 AND generation=2`).Scan(&status, &owner, &lease, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || owner != "" || lease != "" || lastError != deletedByAdminMarker {
		t.Fatalf("queue state = status %q owner %q lease %q error %q", status, owner, lease, lastError)
	}
}

func TestDeleteSubtitleTaskSynchronizesLinkedQueueStepAndRun(t *testing.T) {
	s := openSubtitleDeleteTestDB(t)
	insertDeleteMedia(t, s, 2, 1)
	if _, err := s.DB.Exec(`INSERT INTO subtitle_task(media_id,status) VALUES(2,'failed'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(20,2,1,'scan','processing',0,'{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,lease_owner,lease_until,last_error) VALUES(30,20,2,1,'subtitle',1,'failed','step-owner','2040-01-01','old step'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,lease_owner,lease_until,last_error) VALUES(40,2,20,30,1,'subtitle','failed','queue-owner','2040-01-01','old queue')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtitleTask(2); err != nil {
		t.Fatal(err)
	}
	var domainCount int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM subtitle_task WHERE media_id=2`).Scan(&domainCount); err != nil || domainCount != 0 {
		t.Fatalf("domain count=%d err=%v", domainCount, err)
	}
	for table, id := range map[string]int64{"post_ingest_task": 40, "media_ingest_step": 30} {
		var status, owner, lease, lastError string
		if err := s.DB.QueryRow(`SELECT status,COALESCE(lease_owner,''),COALESCE(lease_until,''),last_error FROM `+table+` WHERE id=?`, id).Scan(&status, &owner, &lease, &lastError); err != nil {
			t.Fatal(err)
		}
		if status != "cancelled" || owner != "" || lease != "" || lastError != deletedByAdminMarker {
			t.Errorf("%s state = status %q owner %q lease %q error %q", table, status, owner, lease, lastError)
		}
	}
	var runStatus string
	if err := s.DB.QueryRow(`SELECT status FROM media_ingest_run WHERE id=20`).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "failed" {
		t.Fatalf("aggregated run status=%q, want failed", runStatus)
	}
}

func TestDeleteSubtitleTaskPreservesPriorGeneration(t *testing.T) {
	s := openSubtitleDeleteTestDB(t)
	insertDeleteMedia(t, s, 3, 2)
	if _, err := s.DB.Exec(`INSERT INTO subtitle_task(media_id,status) VALUES(3,'done'); INSERT INTO post_ingest_task(media_id,generation,task_type,status,last_error) VALUES (3,1,'subtitle','done','prior'),(3,2,'subtitle','done','current')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtitleTask(3); err != nil {
		t.Fatal(err)
	}
	var ps, pe, cs, ce string
	_ = s.DB.QueryRow(`SELECT status,last_error FROM post_ingest_task WHERE media_id=3 AND generation=1`).Scan(&ps, &pe)
	_ = s.DB.QueryRow(`SELECT status,last_error FROM post_ingest_task WHERE media_id=3 AND generation=2`).Scan(&cs, &ce)
	if ps != "done" || pe != "prior" {
		t.Errorf("prior generation changed: %q %q", ps, pe)
	}
	if cs != "cancelled" || ce != deletedByAdminMarker {
		t.Errorf("current generation = %q %q", cs, ce)
	}
}

func TestDeleteSubtitleTaskRejectsRunning(t *testing.T) {
	t.Run("queue running", func(t *testing.T) {
		s := openSubtitleDeleteTestDB(t)
		insertDeleteMedia(t, s, 4, 1)
		_, _ = s.DB.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status) VALUES(4,1,'subtitle','running')`)
		if err := s.DeleteSubtitleTask(4); err == nil || !strings.Contains(err.Error(), "running") {
			t.Fatalf("error=%v, want running", err)
		}
	})
	t.Run("domain running without current queue", func(t *testing.T) {
		s := openSubtitleDeleteTestDB(t)
		insertDeleteMedia(t, s, 5, 2)
		_, _ = s.DB.Exec(`INSERT INTO subtitle_task(media_id,status) VALUES(5,'running'); INSERT INTO post_ingest_task(media_id,generation,task_type,status) VALUES(5,1,'subtitle','done')`)
		if err := s.DeleteSubtitleTask(5); err == nil || !strings.Contains(err.Error(), "running") {
			t.Fatalf("error=%v, want running", err)
		}
	})
	t.Run("stale domain running with terminal current queue", func(t *testing.T) {
		s := openSubtitleDeleteTestDB(t)
		insertDeleteMedia(t, s, 6, 1)
		_, _ = s.DB.Exec(`INSERT INTO subtitle_task(media_id,status) VALUES(6,'running'); INSERT INTO post_ingest_task(media_id,generation,task_type,status) VALUES(6,1,'subtitle','failed')`)
		if err := s.DeleteSubtitleTask(6); err != nil {
			t.Fatalf("delete stale running domain: %v", err)
		}
	})
}

func TestDeleteSubtitleTaskNotFound(t *testing.T) {
	s := openSubtitleDeleteTestDB(t)
	insertDeleteMedia(t, s, 7, 1)
	if err := s.DeleteSubtitleTask(7); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error=%v, want not found", err)
	}
}

func TestDeleteSubtitleTaskRollsBackAllChanges(t *testing.T) {
	s := openSubtitleDeleteTestDB(t)
	insertDeleteMedia(t, s, 8, 1)
	if _, err := s.DB.Exec(`INSERT INTO subtitle_task(media_id,status) VALUES(8,'failed'); INSERT INTO post_ingest_task(media_id,generation,task_type,status,last_error) VALUES(8,1,'subtitle','waiting','old'); CREATE TRIGGER reject_subtitle_domain_delete BEFORE DELETE ON subtitle_task BEGIN SELECT RAISE(ABORT,'reject domain delete'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtitleTask(8); err == nil {
		t.Fatal("expected delete failure")
	}
	var domainCount int
	var queueStatus, queueError string
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM subtitle_task WHERE media_id=8`).Scan(&domainCount)
	_ = s.DB.QueryRow(`SELECT status,last_error FROM post_ingest_task WHERE media_id=8`).Scan(&queueStatus, &queueError)
	if domainCount != 1 || queueStatus != "waiting" || queueError != "old" {
		t.Fatalf("rollback failed: domain=%d queue=%q error=%q", domainCount, queueStatus, queueError)
	}
}

func TestDeleteSubtitleTaskCancelsRecognizeQueue(t *testing.T) {
	s := openSubtitleDeleteTestDB(t)
	insertDeleteMedia(t, s, 9, 1)
	if _, err := s.DB.Exec(`INSERT INTO subtitle_task(media_id,status) VALUES(9,'failed');
INSERT INTO post_ingest_task(media_id,generation,task_type,status,last_error) VALUES
 (9,1,'subtitle','done','ok'),(9,1,'subtitle_recognize','failed','recog')`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubtitleTask(9); err != nil {
		t.Fatal(err)
	}
	var extractStatus, recognizeStatus string
	_ = s.DB.QueryRow(`SELECT status FROM post_ingest_task WHERE media_id=9 AND task_type='subtitle'`).Scan(&extractStatus)
	_ = s.DB.QueryRow(`SELECT status FROM post_ingest_task WHERE media_id=9 AND task_type='subtitle_recognize'`).Scan(&recognizeStatus)
	if extractStatus != "cancelled" || recognizeStatus != "cancelled" {
		t.Fatalf("extract=%s recognize=%s want both cancelled", extractStatus, recognizeStatus)
	}
}

func TestCleanupSubtitleTasksFailedRemovesRecognizeQueue(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'sub','video','/sub');
		INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(12,1,'f12',1);
		INSERT INTO subtitle_task(media_id,status) VALUES(12,'failed');
		INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES
			(12,1,'subtitle','failed',3),(12,1,'subtitle_recognize','failed',3)`); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	if _, err := s.CleanupSubtitleTasksFailed(); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=12`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("queue left=%d err=%v", left, err)
	}
}

func TestCleanupSubtitleTasksFailedSyncsPostIngest(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'sub','video','/sub');
		INSERT INTO media(id,library_id,file_id,ingest_generation) VALUES(10,1,'f10',1),(11,1,'f11',2);
		INSERT INTO subtitle_task(media_id,status) VALUES(10,'failed'),(11,'failed');
		INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES
			(10,1,'subtitle','failed',3),
			(11,1,'subtitle','done',3),
			(11,2,'subtitle','failed',3)`); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	n, err := s.CleanupSubtitleTasksFailed()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted domain rows want 2 got %d", n)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(1) FROM subtitle_task`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("domain rows left=%d err=%v", left, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=10 AND task_type='subtitle'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("media 10 queue should be gone, left=%d err=%v", left, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=11 AND generation=2 AND task_type='subtitle'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("media 11 current-gen queue should be gone, left=%d err=%v", left, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM post_ingest_task WHERE media_id=11 AND generation=1 AND task_type='subtitle'`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("prior-gen done should remain, left=%d err=%v", left, err)
	}
}
