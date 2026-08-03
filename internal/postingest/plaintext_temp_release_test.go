package postingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"knox-media/internal/storage"
)

func TestAdminCancelTaskReleasesPlaintextTemp(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,attempts,max_attempts,lease_owner,lease_until)
		VALUES(? ,1, ?,'running',1,3,'owner/tok',datetime(CURRENT_TIMESTAMP,'+1 hour'))`, mediaID, TaskPreview)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()

	root := filepath.Join(t.TempDir(), ".task-plaintext")
	boundDir := filepath.Join(root, strconv.FormatInt(mediaID, 10), "1", strconv.FormatInt(taskID, 10))
	if err := os.MkdirAll(boundDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boundDir, "work.bin"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	storage.SetDefaultTaskPlaintextTemp(storage.NewTaskPlaintextTemp(root))
	t.Cleanup(func() { storage.SetDefaultTaskPlaintextTemp(nil) })

	q := NewQueue(db, "owner", nil)
	if err := q.AdminCancelTask(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(boundDir); !os.IsNotExist(err) {
		t.Fatalf("admin cancel left orphan temp %s: %v", boundDir, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusCancelled) {
		t.Fatalf("status=%s", status)
	}
}

func TestRecoverExpiredReleasesPlaintextTempOnRequeue(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,attempts,max_attempts,lease_owner,lease_until)
		VALUES(?,2,?,'running',1,3,'dead-owner',datetime(CURRENT_TIMESTAMP,'-1 second'))`, mediaID, TaskSubtitle)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()

	root := filepath.Join(t.TempDir(), ".task-plaintext")
	boundDir := filepath.Join(root, strconv.FormatInt(mediaID, 10), "2", strconv.FormatInt(taskID, 10))
	if err := os.MkdirAll(boundDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boundDir, "work.bin"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	storage.SetDefaultTaskPlaintextTemp(storage.NewTaskPlaintextTemp(root))
	t.Cleanup(func() { storage.SetDefaultTaskPlaintextTemp(nil) })

	q := NewQueue(db, "recovery", nil)
	n, err := q.RecoverExpired(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("RecoverExpired=(%d,%v) want (1,nil)", n, err)
	}
	if _, err := os.Stat(boundDir); !os.IsNotExist(err) {
		t.Fatalf("recover left orphan temp %s: %v", boundDir, err)
	}
	var status string
	var owner sql.NullString
	if err := db.QueryRow(`SELECT status, lease_owner FROM post_ingest_task WHERE id=?`, taskID).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusWaiting) || owner.Valid {
		t.Fatalf("status=%s owner=%v want waiting with cleared lease", status, owner)
	}
}
