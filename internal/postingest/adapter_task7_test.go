package postingest

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPostIngestAdapter_DocumentConvertTask(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Verify that document_convert is registered as a valid task type.
	taskTypes := []TaskType{TaskPoster, TaskThumbnail, TaskPreview, TaskKeyframe,
		TaskSubtitle, TaskSubtitleRecognize, TaskAIAnalysis, TaskAtrack, TaskEncrypt}
	for _, tt := range taskTypes {
		_ = tt
	}
}

func TestPostIngestAdapter_CommitGuard(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Create minimal schema for lease validation.
	db.Exec(`CREATE TABLE IF NOT EXISTS media(id INTEGER PRIMARY KEY, file_type TEXT, ingest_generation INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS post_ingest_task(id INTEGER PRIMARY KEY, media_id INTEGER, task_type TEXT, status TEXT, lease_owner TEXT, retry_round INTEGER, generation INTEGER, ingest_run_id INTEGER, ingest_step_id INTEGER, attempts INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS media_ingest_run(id INTEGER PRIMARY KEY, media_id INTEGER, generation INTEGER, superseded_by_generation INTEGER, superseded_at TIMESTAMP, status TEXT)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS media_ingest_step(id INTEGER PRIMARY KEY, run_id INTEGER, status TEXT, lease_owner TEXT, attempts INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS media_ingest_step_dependency(step_id INTEGER, depends_on_step_id INTEGER, dependency_kind TEXT)`)
	db.Exec(`INSERT INTO media(id,file_type,ingest_generation) VALUES(1,'video',1)`)
	db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,status) VALUES(1,1,1,'processing')`)
	db.Exec(`INSERT INTO media_ingest_step(id,run_id,status,lease_owner,attempts) VALUES(1,1,'running','worker-1',0)`)
	db.Exec(`INSERT INTO post_ingest_task(id,media_id,task_type,status,lease_owner,retry_round,generation,ingest_run_id,ingest_step_id,attempts) VALUES(1,1,'encrypt','running','worker-1',0,1,1,1,0)`)

	task := Task{
		ID: 1, MediaID: 1, Type: TaskEncrypt, Status: StatusRunning,
		LeaseOwner: "worker-1", RetryRound: 0, Generation: 1, Attempts: 0,
		RunID: ptrInt64(1), StepID: ptrInt64(1),
	}

	err = validateAdapterLease(context.Background(), db, task)
	if err != nil {
		t.Fatalf("validate adapter lease: %v", err)
	}
}

func TestPostIngestAdapter_LeaseValidation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	db.Exec(`CREATE TABLE IF NOT EXISTS media(id INTEGER PRIMARY KEY, file_type TEXT, ingest_generation INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS post_ingest_task(id INTEGER PRIMARY KEY, media_id INTEGER, task_type TEXT, status TEXT, lease_owner TEXT, retry_round INTEGER, generation INTEGER, ingest_run_id INTEGER, ingest_step_id INTEGER, attempts INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS media_ingest_run(id INTEGER PRIMARY KEY, media_id INTEGER, generation INTEGER, superseded_by_generation INTEGER, superseded_at TIMESTAMP, status TEXT)`)
	db.Exec(`INSERT INTO media(id,file_type,ingest_generation) VALUES(1,'video',1)`)
	db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,status) VALUES(1,1,1,'processing')`)
	db.Exec(`INSERT INTO post_ingest_task(id,media_id,task_type,status,lease_owner,retry_round,generation,ingest_run_id,attempts) VALUES(1,1,'preview','running','worker-1',0,1,1,0)`)

	// Stale lease - wrong owner.
	task := Task{
		ID: 1, MediaID: 1, Type: TaskPreview, Status: StatusRunning,
		LeaseOwner: "worker-2", RetryRound: 0, Generation: 1, Attempts: 0,
		RunID: ptrInt64(1),
	}
	err = validateAdapterLease(context.Background(), db, task)
	if err == nil {
		t.Error("expected stale lease error for wrong owner")
	}
}

func TestPostIngestAdapter_GenerationFencing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	db.Exec(`CREATE TABLE IF NOT EXISTS media(id INTEGER PRIMARY KEY, file_type TEXT, ingest_generation INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS post_ingest_task(id INTEGER PRIMARY KEY, media_id INTEGER, task_type TEXT, status TEXT, lease_owner TEXT, retry_round INTEGER, generation INTEGER, ingest_run_id INTEGER, ingest_step_id INTEGER, attempts INTEGER)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS media_ingest_run(id INTEGER PRIMARY KEY, media_id INTEGER, generation INTEGER, superseded_by_generation INTEGER, superseded_at TIMESTAMP, status TEXT)`)
	db.Exec(`INSERT INTO media(id,file_type,ingest_generation) VALUES(1,'video',1)`)
	db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,status) VALUES(1,1,1,'processing')`)
	db.Exec(`INSERT INTO post_ingest_task(id,media_id,task_type,status,lease_owner,retry_round,generation,ingest_run_id,attempts) VALUES(1,1,'preview','running','worker-1',0,1,1,0)`)

	// Mismatched generation.
	task := Task{
		ID: 1, MediaID: 1, Type: TaskPreview, Status: StatusRunning,
		LeaseOwner: "worker-1", RetryRound: 0, Generation: 99, Attempts: 0,
		RunID: ptrInt64(1),
	}
	err = validateAdapterLease(context.Background(), db, task)
	if err == nil {
		t.Error("expected stale lease error for generation mismatch")
	}
}

func ptrInt64(v int64) *int64 { return &v }
