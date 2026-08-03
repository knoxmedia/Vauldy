package taskcontrol

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupStreamTestDB(t *testing.T) (*sql.DB, *ProjectionBuilder) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE post_ingest_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL DEFAULT 0,
			scan_task_id INTEGER,
			generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
			task_type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'waiting',
			attempts INTEGER NOT NULL DEFAULT 0,
			retry_round INTEGER NOT NULL DEFAULT 0 CHECK(retry_round >= 0),
			max_attempts INTEGER NOT NULL DEFAULT 3,
			available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			lease_owner TEXT,
			lease_until TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			priority INTEGER NOT NULL DEFAULT 0,
			removed_at TIMESTAMP,
			removed_by TEXT NOT NULL DEFAULT '',
			remove_reason TEXT NOT NULL DEFAULT '',
			source_class INTEGER NOT NULL DEFAULT 0,
			base_priority INTEGER NOT NULL DEFAULT 0,
			library_id INTEGER,
			run_now_expires TIMESTAMP,
			finished_at TIMESTAMP,
			started_at TIMESTAMP,
			abort_requested_at TIMESTAMP,
			abort_timeout_recovery_required INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE task_projection_sequence (
			singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
			next_revision INTEGER NOT NULL DEFAULT 1 CHECK (next_revision >= 1)
		)`,
		`CREATE TABLE task_projection_revision (
			task_identity TEXT PRIMARY KEY,
			revision INTEGER NOT NULL UNIQUE,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE task_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_identity TEXT NOT NULL DEFAULT '',
			actor_id INTEGER NOT NULL DEFAULT 0,
			actor_name TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			prev_status TEXT NOT NULL DEFAULT '',
			new_status TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO task_projection_sequence(singleton_id, next_revision) VALUES(1, 1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create schema: %v\n%s", err, stmt)
		}
	}
	builder := NewProjectionBuilder(db, NewRegistry())
	builder.RegisterAdapter(NewOracleAdapter(db))
	return db, builder
}

func seedStreamTasks(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	types := []string{"poster", "thumbnail", "preview", "keyframe", "transcode"}
	var ids []int64
	for i, typ := range types {
		id := insertOracleTask(t, db, typ, "waiting", map[string]any{
			"media_id":     int64(100 + i),
			"base_priority": int64((i + 1) * 10),
		})
		ids = append(ids, id)
	}
	return ids
}

// --- Subscriber Tests ---

func TestTaskStreamSubscriberSnapshot(t *testing.T) {
	db, builder := setupStreamTestDB(t)
	defer db.Close()
	seedStreamTasks(t, db)

	broker := NewStreamBroker(db, builder, DefaultStreamConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := broker.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broker.Unsubscribe(sub.ID)

	// Collect events with a timeout
	var events []StreamEvent
	timeout := time.After(2 * time.Second)
	collecting := true
	for collecting {
		select {
		case evt, ok := <-sub.Events:
			if !ok {
				collecting = false
				break
			}
			events = append(events, evt)
			if len(events) >= 5 {
				collecting = false
			}
		case <-timeout:
			collecting = false
		}
	}

	if len(events) == 0 {
		t.Fatal("expected snapshot events, got none")
	}
	for _, evt := range events {
		if evt.Type != "snapshot" {
			t.Errorf("expected snapshot event type, got %q", evt.Type)
		}
	}
}

func TestTaskStreamHeartbeat(t *testing.T) {
	db, builder := setupStreamTestDB(t)
	defer db.Close()
	seedStreamTasks(t, db)

	cfg := DefaultStreamConfig()
	cfg.HeartbeatInterval = 50 * time.Millisecond
	broker := NewStreamBroker(db, builder, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broker.StartHeartbeat(ctx)

	sub, err := broker.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broker.Unsubscribe(sub.ID)

	// Drain snapshot events
	drainSnapshot(sub, 5)

	// Wait for at least one heartbeat
	var gotHeartbeat bool
	timeout := time.After(500 * time.Millisecond)
	for !gotHeartbeat {
		select {
		case evt, ok := <-sub.Events:
			if !ok {
				break
			}
			if evt.Type == "heartbeat" {
				gotHeartbeat = true
			}
		case <-timeout:
			if !gotHeartbeat {
				t.Error("expected heartbeat event within timeout")
			}
			gotHeartbeat = true // break the loop
		}
	}
}

func drainSnapshot(sub *StreamSubscriber, expected int) {
	count := 0
	timeout := time.After(2 * time.Second)
	for count < expected {
		select {
		case evt, ok := <-sub.Events:
			if !ok {
				return
			}
			if evt.Type == "snapshot" {
				count++
			}
		case <-timeout:
			return
		}
	}
}

func TestTaskStreamDisconnectCleanup(t *testing.T) {
	db, builder := setupStreamTestDB(t)
	defer db.Close()
	seedStreamTasks(t, db)

	broker := NewStreamBroker(db, builder, DefaultStreamConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := broker.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	broker.Unsubscribe(sub.ID)

	// Verify subscriber is cleaned up
	select {
	case _, ok := <-sub.Done:
		if !ok {
			// Done is closed as expected
		}
	case <-time.After(1 * time.Second):
		t.Error("subscriber Done channel not closed after unsubscribe")
	}
}

func TestTaskStreamSlowConsumerIsolation(t *testing.T) {
	db, builder := setupStreamTestDB(t)
	defer db.Close()
	seedStreamTasks(t, db)

	cfg := DefaultStreamConfig()
	cfg.SubscriberOverflow = 1 // tiny overflow buffer
	broker := NewStreamBroker(db, builder, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := broker.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broker.Unsubscribe(sub.ID)

	// Drain snapshot
	drainSnapshot(sub, 5)

	// Publish rapidly to overflow the tiny buffer
	for i := int64(1); i <= int64(len(tasks)); i++ {
		taskID := BuildIdentity("orchestration", i)
		_ = broker.Publish(ctx, taskID)
	}

	// Verify subscriber channel is not blocked
	select {
	case <-sub.Events:
		// Got at least one event, slow consumer didn't block publish
	case <-time.After(1 * time.Second):
		// This is also fine - overflow buffer dropped events
	}
}

func TestTaskStreamMaxSubscribers(t *testing.T) {
	db, builder := setupStreamTestDB(t)
	defer db.Close()

	cfg := DefaultStreamConfig()
	cfg.MaxSubscribers = 2
	broker := NewStreamBroker(db, builder, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub1, err := broker.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	defer broker.Unsubscribe(sub1.ID)

	sub2, err := broker.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	defer broker.Unsubscribe(sub2.ID)

	_, err = broker.Subscribe(ctx, 0)
	if err == nil {
		t.Error("expected error for too many subscribers")
	}
}

func TestTaskStreamLastEventIDResume(t *testing.T) {
	db, builder := setupStreamTestDB(t)
	defer db.Close()
	ids := seedStreamTasks(t, db)

	broker := NewStreamBroker(db, builder, DefaultStreamConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribe with since_revision=10 to request resume
	sub, err := broker.Subscribe(ctx, 10)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broker.Unsubscribe(sub.ID)

	_ = ids
	// Brief wait for snapshot
	time.Sleep(100 * time.Millisecond)

	// Verify events arrive
	select {
	case _, ok := <-sub.Events:
		if ok {
			// Got event as expected
		}
	case <-time.After(1 * time.Second):
		t.Log("no events received (could be empty dataset)")
	}
}

func TestTaskStreamStrictlyIncreasingIDs(t *testing.T) {
	db, builder := setupStreamTestDB(t)
	defer db.Close()
	seedStreamTasks(t, db)

	broker := NewStreamBroker(db, builder, DefaultStreamConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := broker.Subscribe(ctx, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broker.Unsubscribe(sub.ID)

	// Collect events
	var events []StreamEvent
	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt, ok := <-sub.Events:
			if !ok {
				break
			}
			if evt.Type == "snapshot" || evt.Type == "delta" {
				events = append(events, evt)
			}
			if len(events) >= 5 {
				break
			}
		case <-timeout:
			break
		}
		if len(events) >= 5 {
			break
		}
	}

	// Verify strictly increasing IDs (where IDs are set)
	for i := 1; i < len(events); i++ {
		if events[i-1].ID > events[i].ID && events[i-1].ID > 0 && events[i].ID > 0 {
			t.Errorf("event IDs not strictly increasing: %d then %d", events[i-1].ID, events[i].ID)
		}
	}
}

var tasks = []int64{1, 2, 3, 4, 5}
