package playcompletion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newStoreDB(t *testing.T) (*sql.DB, *Store, *atomic.Int64) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "store.db")) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	schema := `
CREATE TABLE user(id INTEGER PRIMARY KEY);
CREATE TABLE media(id INTEGER PRIMARY KEY, file_id TEXT UNIQUE, file_type TEXT, duration INTEGER);
CREATE TABLE play_progress(id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, file_id TEXT, position INTEGER, play_start_at TIMESTAMP, play_end_at TIMESTAMP, completed INTEGER DEFAULT 0, play_count INTEGER DEFAULT 0, update_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX idx_progress_user_file ON play_progress(user_id,file_id);
CREATE TABLE playback_completion_session (
 user_id INTEGER NOT NULL, file_id TEXT NOT NULL, session_id TEXT NOT NULL,
 active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)), last_position INTEGER NOT NULL DEFAULT 0,
 last_received_at_ms INTEGER NOT NULL DEFAULT 0, last_sequence INTEGER NOT NULL DEFAULT 0,
 valid_play_seconds REAL NOT NULL DEFAULT 0, awaiting_baseline INTEGER NOT NULL DEFAULT 1 CHECK(awaiting_baseline IN (0,1)),
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(user_id,file_id,session_id), FOREIGN KEY(user_id) REFERENCES user(id) ON DELETE CASCADE);
CREATE UNIQUE INDEX idx_playback_completion_active ON playback_completion_session(user_id,file_id) WHERE active=1;
CREATE INDEX idx_playback_completion_updated ON playback_completion_session(updated_at);
INSERT INTO user(id) VALUES(1),(2); INSERT INTO media(id,file_id,file_type,duration) VALUES(10,'file-10','video',1000),(11,'file-11','audio',1000),(12,'file-12','video',600);`
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ms := &atomic.Int64{}
	ms.Store(time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC).UnixMilli())
	store := &Store{DB: db, Now: func() time.Time { return time.UnixMilli(ms.Load()).UTC() }}
	return db, store, ms
}

func ev(user int64, session string, seq, pos int64, event Event) Evidence {
	return Evidence{UserID: user, MediaID: 10, SessionID: session, Sequence: seq, Position: pos, Event: event}
}

func TestStoreBeginSessionFencesOldSessionAndPreservesCompletion(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed,play_count,play_end_at) VALUES(1,'file-10',900,1,4,'2026-07-18 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginSession(context.Background(), ev(1, "old", 1, 850, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(1000)
	got, err := s.BeginSession(context.Background(), ev(1, "new", 1, 860, EventStart))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Completed || got.EffectivePosition != 860 {
		t.Fatalf("result=%+v", got)
	}
	var oldActive, newActive, completed, count int
	if err := db.QueryRow(`SELECT (SELECT active FROM playback_completion_session WHERE session_id='old'),(SELECT active FROM playback_completion_session WHERE session_id='new'),completed,play_count FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&oldActive, &newActive, &completed, &count); err != nil {
		t.Fatal(err)
	}
	if oldActive != 0 || newActive != 1 || completed != 1 || count != 6 {
		t.Fatalf("old=%d new=%d completed=%d count=%d", oldActive, newActive, completed, count)
	}
}

func TestStoreNaturalResumeCompletesAfterSixIntervals(t *testing.T) {
	_, s, clock := newStoreDB(t)
	start := ev(1, "natural", 1, 510, EventStart)
	start.MediaID = 12
	if _, err := s.BeginSession(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	var got SaveResult
	for i := int64(1); i <= 6; i++ {
		clock.Add(10_000)
		var err error
		progress := ev(1, "natural", i+1, 510+i*10, EventProgress)
		progress.MediaID = 12
		got, err = s.SaveEvidence(context.Background(), progress)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !got.Completed || !got.AutoCompleted || got.EffectivePosition != 570 {
		t.Fatalf("result=%+v", got)
	}
}

func TestStoreSeekThenOneProgressDoesNotComplete(t *testing.T) {
	_, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "seek", 1, 850, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(1000)
	if got, err := s.SaveEvidence(context.Background(), ev(1, "seek", 2, 940, EventSeek)); err != nil || got.Completed {
		t.Fatalf("seek result=%+v err=%v", got, err)
	}
	clock.Add(10_000)
	got, err := s.SaveEvidence(context.Background(), ev(1, "seek", 3, 950, EventProgress))
	if err != nil || got.Completed {
		t.Fatalf("progress result=%+v err=%v", got, err)
	}
}

func TestStoreDuplicateAndOutOfOrderEvidenceIsExactlyOnce(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "dup", 1, 850, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(10_000)
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SaveEvidence(context.Background(), ev(1, "dup", 2, 860, EventProgress))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	clock.Add(10_000)
	got, err := s.SaveEvidence(context.Background(), ev(1, "dup", 1, 840, EventProgress))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stale || got.EffectivePosition != 860 {
		t.Fatalf("result=%+v", got)
	}
	var seconds float64
	var seq, pos int64
	if err := db.QueryRow(`SELECT valid_play_seconds,last_sequence,last_position FROM playback_completion_session WHERE session_id='dup'`).Scan(&seconds, &seq, &pos); err != nil {
		t.Fatal(err)
	}
	if seconds != 10 || seq != 2 || pos != 860 {
		t.Fatalf("seconds=%v seq=%d pos=%d", seconds, seq, pos)
	}
}

func TestStoreInactiveSessionCannotOverwriteActiveProgress(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "old", 1, 800, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(1000)
	if _, err := s.BeginSession(context.Background(), ev(1, "new", 1, 900, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(1000)
	got, err := s.SaveEvidence(context.Background(), ev(1, "old", 2, 100, EventProgress))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stale || got.EffectivePosition != 900 {
		t.Fatalf("result=%+v", got)
	}
	var pos int64
	if err := db.QueryRow(`SELECT position FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&pos); err != nil {
		t.Fatal(err)
	}
	if pos != 900 {
		t.Fatalf("position=%d", pos)
	}
}

func TestStoreUsersAreIsolated(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "same", 1, 800, EventStart)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginSession(context.Background(), ev(2, "same", 1, 300, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(10_000)
	if _, err := s.SaveEvidence(context.Background(), ev(1, "same", 2, 810, EventProgress)); err != nil {
		t.Fatal(err)
	}
	var p1, p2 int64
	if err := db.QueryRow(`SELECT MAX(CASE user_id WHEN 1 THEN position END),MAX(CASE user_id WHEN 2 THEN position END) FROM play_progress WHERE file_id='file-10'`).Scan(&p1, &p2); err != nil {
		t.Fatal(err)
	}
	if p1 != 810 || p2 != 300 {
		t.Fatalf("positions=%d,%d", p1, p2)
	}
}

func TestStoreProgressFailureRollsBackEvidence(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "rollback", 1, 800, EventStart)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_progress BEFORE UPDATE ON play_progress BEGIN SELECT RAISE(FAIL,'reject progress'); END`); err != nil {
		t.Fatal(err)
	}
	clock.Add(10_000)
	if _, err := s.SaveEvidence(context.Background(), ev(1, "rollback", 2, 810, EventProgress)); err == nil {
		t.Fatal("expected trigger error")
	}
	var seq, pos int64
	if err := db.QueryRow(`SELECT last_sequence,last_position FROM playback_completion_session WHERE session_id='rollback'`).Scan(&seq, &pos); err != nil {
		t.Fatal(err)
	}
	if seq != 1 || pos != 800 {
		t.Fatalf("evidence changed seq=%d pos=%d", seq, pos)
	}
}

func TestStoreBeginSessionCleanupIsBoundedAndUsesInjectedClock(t *testing.T) {
	db, s, _ := newStoreDB(t)
	old := "2026-07-17 00:00:00.000000000"
	recent := "2026-07-19 00:30:00.000000000"
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		if _, err = tx.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id,active,updated_at) VALUES(1,?, ?,0,?)`, fmt.Sprintf("old-%d", i), fmt.Sprintf("s-%d", i), old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id,active,updated_at) VALUES(2,'recent','recent-active',1,?)`, recent); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginSession(context.Background(), ev(1, "cleanup", 1, 1, EventStart)); err != nil {
		t.Fatal(err)
	}
	var oldCount, recentCount int
	if err := db.QueryRow(`SELECT SUM(updated_at=?),SUM(session_id='recent-active' AND active=1) FROM playback_completion_session`, old).Scan(&oldCount, &recentCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 5 || recentCount != 1 {
		t.Fatalf("old=%d recent=%d", oldCount, recentCount)
	}
}

func TestStoreExplicitClearMethodsDeleteSessionsAtomically(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "mark", 1, 900, EventStart)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE play_progress SET completed=1,play_end_at='x' WHERE user_id=1`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUnwatched(context.Background(), 1, 10); err != nil {
		t.Fatal(err)
	}
	var completed int
	var end sql.NullString
	if err := db.QueryRow(`SELECT completed,play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed, &end); err != nil {
		t.Fatal(err)
	}
	if completed != 0 || end.Valid {
		t.Fatalf("completed=%d end=%v", completed, end)
	}
	if _, err := s.BeginSession(context.Background(), ev(1, "clear", 1, 500, EventStart)); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearProgress(context.Background(), 1, 10); err != nil {
		t.Fatal(err)
	}
	var progress, sessions int
	_ = db.QueryRow(`SELECT COUNT(*) FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&progress)
	_ = db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session WHERE user_id=1 AND file_id='file-10'`).Scan(&sessions)
	if progress != 0 || sessions != 0 {
		t.Fatalf("progress=%d sessions=%d", progress, sessions)
	}
}

func TestStoreContextAndMediaErrorsPropagate(t *testing.T) {
	_, s, _ := newStoreDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.BeginSession(ctx, ev(1, "cancel", 1, 0, EventStart)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	missing := ev(1, "missing", 1, 0, EventStart)
	missing.MediaID = 999
	if _, err := s.BeginSession(context.Background(), missing); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("begin missing err=%v", err)
	}
	if err := s.MarkUnwatched(context.Background(), 1, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mark missing err=%v", err)
	}
}

func TestStoreBeginSessionDuplicateAndLowerSequencePreserveAllState(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "ordered", 5, 700, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(10_000)
	if _, err := s.SaveEvidence(context.Background(), ev(1, "ordered", 6, 710, EventProgress)); err != nil {
		t.Fatal(err)
	}
	var beforeSession struct {
		active                       int
		position, received, sequence int64
		seconds                      float64
		awaiting                     int
		updated                      string
	}
	if err := db.QueryRow(`SELECT active,last_position,last_received_at_ms,last_sequence,valid_play_seconds,awaiting_baseline,updated_at FROM playback_completion_session WHERE user_id=1 AND file_id='file-10' AND session_id='ordered'`).Scan(
		&beforeSession.active, &beforeSession.position, &beforeSession.received, &beforeSession.sequence, &beforeSession.seconds, &beforeSession.awaiting, &beforeSession.updated); err != nil {
		t.Fatal(err)
	}
	var beforePosition, beforeCompleted, beforeCount int64
	var beforeStart, beforeEnd, beforeUpdate sql.NullString
	if err := db.QueryRow(`SELECT position,completed,play_count,play_start_at,play_end_at,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(
		&beforePosition, &beforeCompleted, &beforeCount, &beforeStart, &beforeEnd, &beforeUpdate); err != nil {
		t.Fatal(err)
	}

	for _, sequence := range []int64{6, 4} {
		clock.Add(10_000)
		got, err := s.BeginSession(context.Background(), ev(1, "ordered", sequence, 100, EventStart))
		if err != nil {
			t.Fatal(err)
		}
		if !got.Stale || got.EffectivePosition != beforePosition || got.Completed != (beforeCompleted != 0) {
			t.Fatalf("sequence=%d result=%+v completed-before=%d", sequence, got, beforeCompleted)
		}
	}
	var afterSession = beforeSession
	if err := db.QueryRow(`SELECT active,last_position,last_received_at_ms,last_sequence,valid_play_seconds,awaiting_baseline,updated_at FROM playback_completion_session WHERE user_id=1 AND file_id='file-10' AND session_id='ordered'`).Scan(
		&afterSession.active, &afterSession.position, &afterSession.received, &afterSession.sequence, &afterSession.seconds, &afterSession.awaiting, &afterSession.updated); err != nil {
		t.Fatal(err)
	}
	if afterSession != beforeSession {
		t.Fatalf("session changed: before=%+v after=%+v", beforeSession, afterSession)
	}
	var afterPosition, afterCompleted, afterCount int64
	var afterStart, afterEnd, afterUpdate sql.NullString
	if err := db.QueryRow(`SELECT position,completed,play_count,play_start_at,play_end_at,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(
		&afterPosition, &afterCompleted, &afterCount, &afterStart, &afterEnd, &afterUpdate); err != nil {
		t.Fatal(err)
	}
	if afterPosition != beforePosition || afterCompleted != beforeCompleted || afterCount != beforeCount || afterStart != beforeStart || afterEnd != beforeEnd || afterUpdate != beforeUpdate {
		t.Fatalf("progress changed: before=(%d,%d,%d,%v,%v,%v) after=(%d,%d,%d,%v,%v,%v)", beforePosition, beforeCompleted, beforeCount, beforeStart, beforeEnd, beforeUpdate, afterPosition, afterCompleted, afterCount, afterStart, afterEnd, afterUpdate)
	}
}

func TestStoreStaleStartForInactiveSessionDoesNotFenceActiveSession(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "old-start", 5, 500, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(1000)
	if _, err := s.BeginSession(context.Background(), ev(1, "true-active", 1, 800, EventStart)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE play_progress SET completed=1,play_end_at='2026-07-18 02:00:00',update_at='2026-07-18 03:00:00' WHERE user_id=1 AND file_id='file-10'`); err != nil {
		t.Fatal(err)
	}
	var beforeOld, beforeActive struct {
		active                       int
		position, received, sequence int64
		seconds                      float64
		awaiting                     int
		created, updated             string
	}
	query := `SELECT active,last_position,last_received_at_ms,last_sequence,valid_play_seconds,awaiting_baseline,created_at,updated_at FROM playback_completion_session WHERE user_id=1 AND file_id='file-10' AND session_id=?`
	if err := db.QueryRow(query, "old-start").Scan(&beforeOld.active, &beforeOld.position, &beforeOld.received, &beforeOld.sequence, &beforeOld.seconds, &beforeOld.awaiting, &beforeOld.created, &beforeOld.updated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(query, "true-active").Scan(&beforeActive.active, &beforeActive.position, &beforeActive.received, &beforeActive.sequence, &beforeActive.seconds, &beforeActive.awaiting, &beforeActive.created, &beforeActive.updated); err != nil {
		t.Fatal(err)
	}
	var beforePosition, beforeCompleted, beforeCount int64
	var beforeStart, beforeEnd, beforeUpdate sql.NullString
	if err := db.QueryRow(`SELECT position,completed,play_count,play_start_at,play_end_at,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&beforePosition, &beforeCompleted, &beforeCount, &beforeStart, &beforeEnd, &beforeUpdate); err != nil {
		t.Fatal(err)
	}

	clock.Add(1000)
	got, err := s.BeginSession(context.Background(), ev(1, "old-start", 99, 100, EventStart))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stale || !got.Completed || got.EffectivePosition != beforePosition {
		t.Fatalf("result=%+v", got)
	}

	afterOld, afterActive := beforeOld, beforeActive
	if err := db.QueryRow(query, "old-start").Scan(&afterOld.active, &afterOld.position, &afterOld.received, &afterOld.sequence, &afterOld.seconds, &afterOld.awaiting, &afterOld.created, &afterOld.updated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(query, "true-active").Scan(&afterActive.active, &afterActive.position, &afterActive.received, &afterActive.sequence, &afterActive.seconds, &afterActive.awaiting, &afterActive.created, &afterActive.updated); err != nil {
		t.Fatal(err)
	}
	if afterOld != beforeOld || afterActive != beforeActive {
		t.Fatalf("sessions changed: old before=%+v after=%+v active before=%+v after=%+v", beforeOld, afterOld, beforeActive, afterActive)
	}
	var afterPosition, afterCompleted, afterCount int64
	var afterStart, afterEnd, afterUpdate sql.NullString
	if err := db.QueryRow(`SELECT position,completed,play_count,play_start_at,play_end_at,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&afterPosition, &afterCompleted, &afterCount, &afterStart, &afterEnd, &afterUpdate); err != nil {
		t.Fatal(err)
	}
	if afterPosition != beforePosition || afterCompleted != beforeCompleted || afterCount != beforeCount || afterStart != beforeStart || afterEnd != beforeEnd || afterUpdate != beforeUpdate {
		t.Fatalf("progress changed: before=(%d,%d,%d,%v,%v,%v) after=(%d,%d,%d,%v,%v,%v)", beforePosition, beforeCompleted, beforeCount, beforeStart, beforeEnd, beforeUpdate, afterPosition, afterCompleted, afterCount, afterStart, afterEnd, afterUpdate)
	}
}

func TestStoreNewerStartForSameSessionResetsBaselineOnce(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "restart", 1, 500, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(10_000)
	if _, err := s.SaveEvidence(context.Background(), ev(1, "restart", 2, 510, EventProgress)); err != nil {
		t.Fatal(err)
	}
	clock.Add(1000)
	got, err := s.BeginSession(context.Background(), ev(1, "restart", 3, 700, EventStart))
	if err != nil || got.Stale {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	var seq, pos, count int64
	var seconds float64
	if err := db.QueryRow(`SELECT last_sequence,last_position,valid_play_seconds,(SELECT play_count FROM play_progress WHERE user_id=1 AND file_id='file-10') FROM playback_completion_session WHERE session_id='restart'`).Scan(&seq, &pos, &seconds, &count); err != nil {
		t.Fatal(err)
	}
	if seq != 3 || pos != 700 || seconds != 0 || count != 2 {
		t.Fatalf("seq=%d pos=%d seconds=%v count=%d", seq, pos, seconds, count)
	}
}

func TestStoreMarkUnwatchedPreservesPlaybackHistoryFields(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,play_start_at,play_end_at,completed,play_count,update_at) VALUES(1,'file-10',777,'2026-07-18 01:00:00','2026-07-18 02:00:00',1,9,'2026-07-18 03:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-10','evidence')`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUnwatched(context.Background(), 1, 10); err != nil {
		t.Fatal(err)
	}
	var position, completed, count int
	var start, end, updated sql.NullString
	if err := db.QueryRow(`SELECT position,completed,play_count,play_start_at,play_end_at,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&position, &completed, &count, &start, &end, &updated); err != nil {
		t.Fatal(err)
	}
	if position != 777 || completed != 0 || count != 9 || start.String != "2026-07-18T01:00:00Z" || end.Valid || updated.String != "2026-07-18T03:00:00Z" {
		t.Fatalf("position=%d completed=%d count=%d start=%v end=%v updated=%v", position, completed, count, start, end, updated)
	}
	var sessions int
	_ = db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session WHERE user_id=1 AND file_id='file-10'`).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("sessions=%d", sessions)
	}
}

func TestStoreMarkUnwatchedCreatesMissingProgressRow(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if err := s.MarkUnwatched(context.Background(), 2, 10); err != nil {
		t.Fatal(err)
	}
	var position, completed, count int
	if err := db.QueryRow(`SELECT COALESCE(position,0),completed,play_count FROM play_progress WHERE user_id=2 AND file_id='file-10'`).Scan(&position, &completed, &count); err != nil {
		t.Fatal(err)
	}
	if position != 0 || completed != 0 || count != 0 {
		t.Fatalf("position=%d completed=%d count=%d", position, completed, count)
	}
}

func TestStoreInjectedClockControlsProgressTimestamps(t *testing.T) {
	db, s, clock := newStoreDB(t)
	originalEnd := "2026-07-18 00:00:00.000000000"
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,play_start_at,play_end_at,completed,play_count) VALUES(1,'file-12',500,'2026-07-18 01:00:00',?,1,2)`, originalEnd); err != nil {
		t.Fatal(err)
	}
	start := ev(1, "clock", 1, 510, EventStart)
	start.MediaID = 12
	if _, err := s.BeginSession(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	var started, ended string
	if err := db.QueryRow(`SELECT play_start_at,play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-12'`).Scan(&started, &ended); err != nil {
		t.Fatal(err)
	}
	if started != time.UnixMilli(clock.Load()).UTC().Format(time.RFC3339Nano) || ended != "2026-07-18T00:00:00Z" {
		t.Fatalf("start=%q end=%q", started, ended)
	}
	clock.Add(5000)
	start.Sequence = 2
	start.Position = 520
	if _, err := s.BeginSession(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT play_start_at,play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-12'`).Scan(&started, &ended); err != nil {
		t.Fatal(err)
	}
	if started != time.UnixMilli(clock.Load()).UTC().Format(time.RFC3339Nano) || ended != "2026-07-18T00:00:00Z" {
		t.Fatalf("advanced start=%q end=%q", started, ended)
	}
}

func TestStoreCompletionEndTimestampIsMonotonic(t *testing.T) {
	db, s, clock := newStoreDB(t)
	start := ev(1, "finish", 1, 510, EventStart)
	start.MediaID = 12
	if _, err := s.BeginSession(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	var got SaveResult
	for i := int64(1); i <= 6; i++ {
		clock.Add(10_000)
		progress := ev(1, "finish", i+1, 510+i*10, EventProgress)
		progress.MediaID = 12
		var err error
		got, err = s.SaveEvidence(context.Background(), progress)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !got.AutoCompleted {
		t.Fatalf("result=%+v", got)
	}
	wantEnd := time.UnixMilli(clock.Load()).UTC().Format(time.RFC3339Nano)
	var end string
	if err := db.QueryRow(`SELECT play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-12'`).Scan(&end); err != nil {
		t.Fatal(err)
	}
	if end != wantEnd {
		t.Fatalf("end=%q want=%q", end, wantEnd)
	}
	clock.Add(10_000)
	progress := ev(1, "finish", 8, 580, EventProgress)
	progress.MediaID = 12
	if _, err := s.SaveEvidence(context.Background(), progress); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-12'`).Scan(&end); err != nil {
		t.Fatal(err)
	}
	if end != wantEnd {
		t.Fatalf("ordinary progress changed end=%q want=%q", end, wantEnd)
	}
}

func TestStoreEndedSetsInjectedEndTimestampWhenIncomplete(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "ended-clock", 1, 100, EventStart)); err != nil {
		t.Fatal(err)
	}
	clock.Add(2500)
	got, err := s.SaveEvidence(context.Background(), ev(1, "ended-clock", 2, 120, EventEnded))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Completed {
		t.Fatalf("result=%+v", got)
	}
	var end string
	if err := db.QueryRow(`SELECT play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&end); err != nil {
		t.Fatal(err)
	}
	if end != time.UnixMilli(clock.Load()).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("end=%q", end)
	}
}

func TestStoreStaleStartIsCheckedBeforeCleanup(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "aged-duplicate", 5, 700, EventStart)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE playback_completion_session SET updated_at='2026-07-17 00:00:00.000000000' WHERE session_id='aged-duplicate'`); err != nil {
		t.Fatal(err)
	}
	got, err := s.BeginSession(context.Background(), ev(1, "aged-duplicate", 5, 100, EventStart))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stale || got.EffectivePosition != 700 {
		t.Fatalf("result=%+v", got)
	}
	var seq, pos, count int64
	if err := db.QueryRow(`SELECT last_sequence,last_position,(SELECT play_count FROM play_progress WHERE user_id=1 AND file_id='file-10') FROM playback_completion_session WHERE session_id='aged-duplicate'`).Scan(&seq, &pos, &count); err != nil {
		t.Fatal(err)
	}
	if seq != 5 || pos != 700 || count != 1 {
		t.Fatalf("seq=%d pos=%d count=%d", seq, pos, count)
	}
}

func TestStoreReconcilesLegacyProgressDuplicatesDeterministically(t *testing.T) {
	for _, newestInsertedFirst := range []bool{false, true} {
		name := "newest_inserted_last"
		if newestInsertedFirst {
			name = "newest_inserted_first"
		}
		t.Run(name, func(t *testing.T) {
			db, s, _ := newStoreDB(t)
			older := []any{int64(1), "file-10", int64(111), "2026-07-17 01:00:00", "2026-07-17 02:00:00", int64(1), int64(7), "2026-07-17 03:00:00"}
			newer := []any{int64(1), "file-10", int64(888), "2026-07-18 01:00:00", nil, int64(0), int64(3), "2026-07-18 03:00:00"}
			insert := func(values []any) {
				t.Helper()
				if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,play_start_at,play_end_at,completed,play_count,update_at) VALUES(?,?,?,?,?,?,?,?)`, values...); err != nil {
					t.Fatal(err)
				}
			}
			if newestInsertedFirst {
				insert(newer)
				insert(older)
			} else {
				insert(older)
				insert(newer)
			}
			if _, err := db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id,active,last_position,last_received_at_ms,last_sequence,updated_at) VALUES(1,'file-10','legacy',1,700,1000,5,'2026-07-19 00:00:00')`); err != nil {
				t.Fatal(err)
			}
			got, err := s.BeginSession(context.Background(), ev(1, "legacy", 5, 100, EventStart))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Stale || !got.Completed || got.EffectivePosition != 888 {
				t.Fatalf("result=%+v", got)
			}
			var rows, position, completed, count int
			var start, end, updated sql.NullString
			if err := db.QueryRow(`SELECT COUNT(*),position,completed,play_count,play_start_at,play_end_at,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&rows, &position, &completed, &count, &start, &end, &updated); err != nil {
				t.Fatal(err)
			}
			if rows != 1 || position != 888 || completed != 1 || count != 7 || start.String != "2026-07-18T01:00:00Z" || end.String != "2026-07-17T02:00:00Z" || updated.String != "2026-07-18T03:00:00Z" {
				t.Fatalf("rows=%d position=%d completed=%d count=%d start=%v end=%v updated=%v", rows, position, completed, count, start, end, updated)
			}
		})
	}
}

func TestStoreSaveEvidenceReconcilesDuplicatesBeforeCompletionDecision(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := s.BeginSession(context.Background(), ev(1, "duplicate-save", 1, 500, EventStart)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE play_progress SET position=500,completed=0,play_count=2,update_at='2026-07-18 03:00:00' WHERE user_id=1 AND file_id='file-10'; INSERT INTO play_progress(user_id,file_id,position,play_end_at,completed,play_count,update_at) VALUES(1,'file-10',900,'2026-07-17 02:00:00',1,9,'2026-07-17 03:00:00')`); err != nil {
		t.Fatal(err)
	}
	clock.Add(10_000)
	got, err := s.SaveEvidence(context.Background(), ev(1, "duplicate-save", 2, 510, EventProgress))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Completed || got.EffectivePosition != 510 {
		t.Fatalf("result=%+v", got)
	}
	var rows, completed, count int
	var end sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*),completed,play_count,play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&rows, &completed, &count, &end); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || completed != 1 || count != 9 || end.String != "2026-07-17T02:00:00Z" {
		t.Fatalf("rows=%d completed=%d count=%d end=%v", rows, completed, count, end)
	}
}

func TestStoreMarkUnwatchedReconcilesDuplicatesBeforeClearingCompletion(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,play_start_at,completed,play_count,update_at) VALUES(1,'file-10',700,'2026-07-18 01:00:00',0,4,'2026-07-18 03:00:00'),(1,'file-10',300,'2026-07-17 01:00:00',1,8,'2026-07-17 03:00:00')`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUnwatched(context.Background(), 1, 10); err != nil {
		t.Fatal(err)
	}
	var rows, position, completed, count int
	if err := db.QueryRow(`SELECT COUNT(*),position,completed,play_count FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&rows, &position, &completed, &count); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || position != 700 || completed != 0 || count != 8 {
		t.Fatalf("rows=%d position=%d completed=%d count=%d", rows, position, completed, count)
	}
}

func TestStoreRejectsInvalidEvidenceBeforeAnyWrite(t *testing.T) {
	cases := []struct {
		name   string
		begin  bool
		mutate func(*Evidence)
	}{
		{"zero_user", true, func(e *Evidence) { e.UserID = 0 }}, {"zero_media", true, func(e *Evidence) { e.MediaID = 0 }},
		{"empty_session", true, func(e *Evidence) { e.SessionID = "" }},
		{"invalid_utf8_session", true, func(e *Evidence) { e.SessionID = string([]byte{0xff}) }}, {"oversized_session", true, func(e *Evidence) { e.SessionID = string(make([]byte, 129)) }},
		{"zero_sequence", true, func(e *Evidence) { e.Sequence = 0 }}, {"negative_position", true, func(e *Evidence) { e.Position = -1 }},
		{"begin_non_start", true, func(e *Evidence) { e.Event = EventProgress }}, {"begin_unknown", true, func(e *Evidence) { e.Event = Event("bogus") }},
		{"save_start", false, func(e *Evidence) { e.Event = EventStart }}, {"save_unknown", false, func(e *Evidence) { e.Event = Event("bogus") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, s, _ := newStoreDB(t)
			if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed,play_count,update_at) VALUES(1,'file-10',700,1,4,'2026-07-18 03:00:00'); INSERT INTO playback_completion_session(user_id,file_id,session_id,active,last_position,last_sequence,updated_at) VALUES(1,'file-10','valid',1,700,5,'2026-07-19 00:00:00'); INSERT INTO playback_completion_session(user_id,file_id,session_id,active,updated_at) VALUES(2,'stale-file','stale',0,'2026-07-17 00:00:00')`); err != nil {
				t.Fatal(err)
			}
			before := databaseState(t, db)
			e := ev(1, "valid", 6, 710, EventStart)
			tc.mutate(&e)
			var err error
			if tc.begin {
				_, err = s.BeginSession(context.Background(), e)
			} else {
				_, err = s.SaveEvidence(context.Background(), e)
			}
			if !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("err=%v", err)
			}
			after := databaseState(t, db)
			if after != before {
				t.Fatalf("database changed before=%q after=%q", before, after)
			}
		})
	}
}

func TestStoreInvalidEvidencePrecedesNilDatabaseCheck(t *testing.T) {
	s := &Store{}
	_, err := s.BeginSession(context.Background(), Evidence{})
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("err=%v", err)
	}
}

func databaseState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var progress, sessions string
	if err := db.QueryRow(`SELECT group_concat(printf('%d|%d|%s|%s|%s|%s|%s|%s|%s',id,user_id,file_id,position,play_start_at,play_end_at,completed,play_count,update_at),';') FROM (SELECT * FROM play_progress ORDER BY id)`).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT group_concat(printf('%d|%s|%s|%d|%d|%d|%s',user_id,file_id,session_id,active,last_position,last_sequence,updated_at),';') FROM (SELECT * FROM playback_completion_session ORDER BY user_id,file_id,session_id)`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	return progress + "//" + sessions
}

func TestStoreReconcileOrdersMixedTimestampFormatsByActualInstant(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed,play_count,update_at) VALUES
        (1,'file-10',100,0,1,'2026-07-18T03:00:00.100Z'),
        (1,'file-10',900,1,2,'2026-07-18 03:00:00.900')`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUnwatched(context.Background(), 1, 10); err != nil {
		t.Fatal(err)
	}
	var rows, position, count int
	var updated string
	if err := db.QueryRow(`SELECT COUNT(*),position,play_count,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&rows, &position, &count, &updated); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || position != 900 || count != 2 || updated != "2026-07-18T03:00:00.9Z" {
		t.Fatalf("rows=%d position=%d count=%d updated=%q", rows, position, count, updated)
	}
}

func TestStoreReconcileInvalidTimestampsFallbackToHighestID(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed,play_count,update_at) VALUES
        (1,'file-10',111,1,5,'not-a-time'),
        (1,'file-10',222,0,3,NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUnwatched(context.Background(), 1, 10); err != nil {
		t.Fatal(err)
	}
	var rows, position, count int
	var updated sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*),position,play_count,update_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&rows, &position, &count, &updated); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || position != 222 || count != 5 || updated.Valid {
		t.Fatalf("rows=%d position=%d count=%d updated=%v", rows, position, count, updated)
	}
}

func TestStoreExplicitMethodsValidateIDsBeforeDatabaseAccess(t *testing.T) {
	cases := []struct {
		name            string
		userID, mediaID int64
		clear           bool
	}{
		{"mark_zero_user", 0, 10, false}, {"mark_negative_user", -1, 10, false}, {"mark_zero_media", 1, 0, false}, {"mark_negative_media", 1, -1, false},
		{"clear_zero_user", 0, 10, true}, {"clear_negative_user", -1, 10, true}, {"clear_zero_media", 1, 0, true}, {"clear_negative_media", 1, -1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, s, _ := newStoreDB(t)
			if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed) VALUES(1,'file-10',700,1); INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-10','keep')`); err != nil {
				t.Fatal(err)
			}
			before := databaseState(t, db)
			var err error
			if tc.clear {
				err = s.ClearProgress(context.Background(), tc.userID, tc.mediaID)
			} else {
				err = s.MarkUnwatched(context.Background(), tc.userID, tc.mediaID)
			}
			if !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("err=%v", err)
			}
			if after := databaseState(t, db); after != before {
				t.Fatalf("database changed before=%q after=%q", before, after)
			}
		})
	}
}

func TestStoreExplicitMethodValidationPrecedesNilDatabase(t *testing.T) {
	s := &Store{}
	if err := s.MarkUnwatched(context.Background(), 0, 10); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("mark err=%v", err)
	}
	if err := s.ClearProgress(context.Background(), 1, 0); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("clear err=%v", err)
	}
}

func TestStoreSaveLegacyProgressIsTransactionalAndMonotonic(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed) VALUES(1,'file-10',900,1)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.SaveLegacyProgress(context.Background(), 1, 10, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Completed || got.EffectivePosition != 100 || got.AutoCompleted || got.Stale {
		t.Fatalf("result=%+v", got)
	}
	var completed, sessions int
	if err := db.QueryRow(`SELECT completed FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if completed != 1 || sessions != 0 {
		t.Fatalf("completed=%d sessions=%d", completed, sessions)
	}
}

func TestStoreSaveLegacyCompletedMarksNonVideoEnded(t *testing.T) {
	_, s, _ := newStoreDB(t)
	got, err := s.SaveLegacyProgress(context.Background(), 1, 11, 77, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Completed || got.EffectivePosition != 77 {
		t.Fatalf("result=%+v", got)
	}
}

func TestStoreBeginLegacyPlaybackPreservesWatched(t *testing.T) {
	db, s, _ := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed,play_count) VALUES(1,'file-10',900,1,2)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.BeginLegacyPlayback(context.Background(), 1, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Completed || got.EffectivePosition != 100 {
		t.Fatalf("result=%+v", got)
	}
	var count int
	if err := db.QueryRow(`SELECT play_count FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("play_count=%d want 3", count)
	}
}

func TestStoreSaveLegacyProgressPreservesReportedPositionBeyondDuration(t *testing.T) {
	_, s, _ := newStoreDB(t)
	got, err := s.SaveLegacyProgress(context.Background(), 1, 10, 1200, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectivePosition != 1200 {
		t.Fatalf("position=%d want 1200", got.EffectivePosition)
	}
}

func TestStoreMarkWatchedPreservesProgressAndResetsEvidence(t *testing.T) {
	db, s, clock := newStoreDB(t)
	if _, err := db.Exec(`INSERT INTO play_progress(user_id,file_id,position,play_start_at,play_end_at,completed,play_count,update_at) VALUES(1,'file-10',777,'2026-07-18 01:00:00','2026-07-18 02:00:00',0,9,'2026-07-18 03:00:00'); INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-10','retire')`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkWatched(context.Background(), 1, 10); err != nil {
		t.Fatal(err)
	}
	var position, completed, count int
	var start, end string
	if err := db.QueryRow(`SELECT position,completed,play_count,play_start_at,play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&position, &completed, &count, &start, &end); err != nil {
		t.Fatal(err)
	}
	wantEnd := time.UnixMilli(clock.Load()).UTC().Format(time.RFC3339Nano)
	if position != 777 || completed != 1 || count != 9 || start != "2026-07-18T01:00:00Z" || end != wantEnd {
		t.Fatalf("position=%d completed=%d count=%d start=%q end=%q wantEnd=%q", position, completed, count, start, end, wantEnd)
	}
	if _, sessions := func() (int, int) {
		var p, ss int
		_ = db.QueryRow(`SELECT COUNT(*) FROM play_progress`).Scan(&p)
		_ = db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session`).Scan(&ss)
		return p, ss
	}(); sessions != 0 {
		t.Fatalf("sessions=%d", sessions)
	}
}

func TestStoreExplicitActionsRollbackOnSessionDeleteFailure(t *testing.T) {
	cases := []struct {
		name  string
		setup string
		call  func(*Store) error
	}{
		{"watched", `INSERT INTO play_progress(user_id,file_id,position,completed) VALUES(1,'file-10',700,0)`, func(s *Store) error { return s.MarkWatched(context.Background(), 1, 10) }},
		{"unwatched", `INSERT INTO play_progress(user_id,file_id,position,completed) VALUES(1,'file-10',700,1)`, func(s *Store) error { return s.MarkUnwatched(context.Background(), 1, 10) }},
		{"clear", `INSERT INTO play_progress(user_id,file_id,position,completed) VALUES(1,'file-10',700,1)`, func(s *Store) error { return s.ClearProgress(context.Background(), 1, 10) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, s, _ := newStoreDB(t)
			if _, err := db.Exec(tc.setup + `; INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-10','keep'); CREATE TRIGGER reject_session_delete BEFORE DELETE ON playback_completion_session BEGIN SELECT RAISE(FAIL,'reject session delete'); END`); err != nil {
				t.Fatal(err)
			}
			before := databaseState(t, db)
			if err := tc.call(s); err == nil {
				t.Fatal("expected trigger error")
			}
			if after := databaseState(t, db); after != before {
				t.Fatalf("changed before=%q after=%q", before, after)
			}
		})
	}
}

func databaseStateAllowEmpty(t *testing.T, db *sql.DB) string {
	t.Helper()
	var progress, sessions string
	if err := db.QueryRow(`SELECT COALESCE(group_concat(printf('%d|%d|%s|%s|%s|%s|%s|%s|%s',id,user_id,file_id,position,play_start_at,play_end_at,completed,play_count,update_at),';'),'') FROM (SELECT * FROM play_progress ORDER BY id)`).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(group_concat(printf('%d|%s|%s|%d|%d|%d|%s',user_id,file_id,session_id,active,last_position,last_sequence,updated_at),';'),'') FROM (SELECT * FROM playback_completion_session ORDER BY user_id,file_id,session_id)`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	return progress + "//" + sessions
}
func TestStoreWatchedActionsRejectMissingUserWithoutMutations(t *testing.T) {
	cases := []struct {
		name string
		seed string
		call func(*Store) error
	}{
		{"watched_clean", "", func(s *Store) error { return s.MarkWatched(context.Background(), 99, 10) }},
		{"unwatched_clean", "", func(s *Store) error { return s.MarkUnwatched(context.Background(), 99, 10) }},
		{"watched_orphans", `INSERT INTO play_progress(user_id,file_id,position,completed,play_count,update_at) VALUES(99,'file-10',111,0,2,'2026-07-17 03:00:00'),(99,'file-10',888,1,7,'2026-07-18 03:00:00'); INSERT INTO playback_completion_session(user_id,file_id,session_id,active) VALUES(99,'file-10','orphan',1)`, func(s *Store) error { return s.MarkWatched(context.Background(), 99, 10) }},
		{"unwatched_orphans", `INSERT INTO play_progress(user_id,file_id,position,completed,play_count,update_at) VALUES(99,'file-10',111,0,2,'2026-07-17 03:00:00'),(99,'file-10',888,1,7,'2026-07-18 03:00:00'); INSERT INTO playback_completion_session(user_id,file_id,session_id,active) VALUES(99,'file-10','orphan',1)`, func(s *Store) error { return s.MarkUnwatched(context.Background(), 99, 10) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, s, _ := newStoreDB(t)
			if tc.seed != "" {
				if _, err := db.Exec(`PRAGMA foreign_keys=OFF; ` + tc.seed + `; PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
			}
			before := databaseStateAllowEmpty(t, db)
			err := tc.call(s)
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("err=%v", err)
			}
			if after := databaseStateAllowEmpty(t, db); after != before {
				t.Fatalf("changed before=%q after=%q", before, after)
			}
		})
	}
}
