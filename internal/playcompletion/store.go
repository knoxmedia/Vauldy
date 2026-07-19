package playcompletion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Store atomically persists playback evidence and user progress.
type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

// Evidence identifies one ordered event in a user's playback session.
type Evidence struct {
	UserID    int64
	MediaID   int64
	SessionID string
	Sequence  int64
	Position  int64
	Event     Event
}

// SaveResult is the authoritative progress state returned to the player.
type SaveResult struct {
	Completed         bool  `json:"completed"`
	AutoCompleted     bool  `json:"auto_completed"`
	EffectivePosition int64 `json:"effective_position"`
	Stale             bool  `json:"stale"`
}

// ErrInvalidEvidence identifies client evidence that cannot be persisted.
var ErrInvalidEvidence = errors.New("invalid playback evidence")

const MaxSessionIDBytes = 128

type mediaInfo struct {
	fileID   string
	fileType string
	duration int64
}

func validateIDs(userID, mediaID int64) error {
	if userID <= 0 {
		return fmt.Errorf("%w: user_id must be positive", ErrInvalidEvidence)
	}
	if mediaID <= 0 {
		return fmt.Errorf("%w: media_id must be positive", ErrInvalidEvidence)
	}
	return nil
}

// ValidateEvidence validates a protocol event without writing database state.
func ValidateEvidence(e Evidence, begin bool) error { return validateEvidence(e, begin) }

func validateEvidence(e Evidence, begin bool) error {
	invalid := func(field string) error { return fmt.Errorf("%w: %s", ErrInvalidEvidence, field) }
	if e.UserID <= 0 {
		return invalid("user_id must be positive")
	}
	if e.MediaID <= 0 {
		return invalid("media_id must be positive")
	}
	if strings.TrimSpace(e.SessionID) == "" {
		return invalid("session_id is required")
	}
	if !utf8.ValidString(e.SessionID) {
		return invalid("session_id must be valid UTF-8")
	}
	if len([]byte(e.SessionID)) > MaxSessionIDBytes {
		return invalid("session_id exceeds 128 bytes")
	}
	if e.Sequence <= 0 {
		return invalid("sequence must be positive")
	}
	if e.Position < 0 {
		return invalid("position must be non-negative")
	}
	if begin {
		if e.Event != EventStart {
			return invalid("begin session requires start event")
		}
		return nil
	}
	switch e.Event {
	case EventProgress, EventSeek, EventEnded:
		return nil
	default:
		return invalid("save evidence requires progress, seek, or ended event")
	}
}

func (s *Store) BeginSession(ctx context.Context, e Evidence) (SaveResult, error) {
	if err := validateEvidence(e, true); err != nil {
		return SaveResult{}, err
	}
	var out SaveResult
	err := s.write(ctx, func(tx *sql.Tx, now time.Time) error {
		media, err := resolveMedia(ctx, tx, e.MediaID)
		if err != nil {
			return err
		}
		if err = reconcileProgress(ctx, tx, e.UserID, media.fileID); err != nil {
			return err
		}

		// Check ordering before changing active ownership or running cleanup. The
		// current session row itself may be old, but ordering still has authority.
		// inactive historical session must not fence the actual active session.
		var savedSequence int64
		var savedActive int
		err = tx.QueryRowContext(ctx, `SELECT last_sequence,active FROM playback_completion_session
            WHERE user_id=? AND file_id=? AND session_id=?`, e.UserID, media.fileID, e.SessionID).Scan(&savedSequence, &savedActive)
		if err == nil && (savedActive != 1 || e.Sequence <= savedSequence) {
			out.Stale = true
			return loadProgressResultPreserveStale(ctx, tx, e.UserID, media.fileID, &out)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load playback session ordering: %w", err)
		}
		cutoff := sqliteTime(now.Add(-24 * time.Hour))
		if _, err = tx.ExecContext(ctx, `DELETE FROM playback_completion_session WHERE rowid IN (
            SELECT rowid FROM playback_completion_session WHERE updated_at < ? ORDER BY updated_at LIMIT 100
        )`, cutoff); err != nil {
			return fmt.Errorf("cleanup playback sessions: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE playback_completion_session SET active=0
            WHERE user_id=? AND file_id=? AND session_id<>? AND active=1`, e.UserID, media.fileID, e.SessionID); err != nil {
			return fmt.Errorf("deactivate playback session: %w", err)
		}

		// BeginSession itself establishes the requested active baseline. The next
		// adjacent progress report may therefore contribute natural-play time.
		position := acceptedPosition(e.Position, media.duration)
		state := State{LastPosition: position, LastReceivedAtMS: now.UnixMilli(), LastSequence: e.Sequence}
		stamp := sqliteTime(now)
		if _, err = tx.ExecContext(ctx, `INSERT INTO playback_completion_session
            (user_id,file_id,session_id,active,last_position,last_received_at_ms,last_sequence,valid_play_seconds,awaiting_baseline,created_at,updated_at)
            VALUES(?,?,?,1,?,?,?,?,?,?,?)
            ON CONFLICT(user_id,file_id,session_id) DO UPDATE SET active=1,last_position=excluded.last_position,
              last_received_at_ms=excluded.last_received_at_ms,last_sequence=excluded.last_sequence,
              valid_play_seconds=excluded.valid_play_seconds,awaiting_baseline=excluded.awaiting_baseline,updated_at=excluded.updated_at`,
			e.UserID, media.fileID, e.SessionID, state.LastPosition, state.LastReceivedAtMS, state.LastSequence, state.ValidPlaySeconds, boolInt(state.AwaitingBaseline), stamp, stamp); err != nil {
			return fmt.Errorf("upsert playback session: %w", err)
		}

		if err = upsertProgress(ctx, tx, e.UserID, media.fileID, state.LastPosition, false, true, false, stamp); err != nil {
			return err
		}
		return loadProgressResult(ctx, tx, e.UserID, media.fileID, &out)
	})
	return out, err
}

func (s *Store) SaveEvidence(ctx context.Context, e Evidence) (SaveResult, error) {
	if err := validateEvidence(e, false); err != nil {
		return SaveResult{}, err
	}
	var out SaveResult
	err := s.write(ctx, func(tx *sql.Tx, now time.Time) error {
		media, err := resolveMedia(ctx, tx, e.MediaID)
		if err != nil {
			return err
		}
		if err = reconcileProgress(ctx, tx, e.UserID, media.fileID); err != nil {
			return err
		}
		var state State
		var active, awaiting int
		var updated string
		err = tx.QueryRowContext(ctx, `SELECT active,last_position,last_received_at_ms,last_sequence,valid_play_seconds,awaiting_baseline,updated_at
            FROM playback_completion_session WHERE user_id=? AND file_id=? AND session_id=?`, e.UserID, media.fileID, e.SessionID).
			Scan(&active, &state.LastPosition, &state.LastReceivedAtMS, &state.LastSequence, &state.ValidPlaySeconds, &awaiting, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			out.Stale = true
			return loadProgressResultPreserveStale(ctx, tx, e.UserID, media.fileID, &out)
		}
		if err != nil {
			return fmt.Errorf("load playback session: %w", err)
		}
		state.AwaitingBaseline = awaiting != 0
		updatedAt, parseErr := parseSQLiteTime(updated)
		if active != 1 || parseErr != nil || updatedAt.Before(now.Add(-24*time.Hour)) {
			out.Stale = true
			return loadProgressResultPreserveStale(ctx, tx, e.UserID, media.fileID, &out)
		}
		var previousCompleted bool
		var completedInt int
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(completed,0) FROM play_progress WHERE user_id=? AND file_id=? ORDER BY id LIMIT 1`, e.UserID, media.fileID).Scan(&completedInt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load play progress: %w", err)
		}
		previousCompleted = completedInt != 0
		evaluated := Evaluate(state, Input{FileType: media.fileType, Duration: media.duration, Position: e.Position, Sequence: e.Sequence,
			ReceivedAtMS: now.UnixMilli(), Event: e.Event, PreviouslyCompleted: previousCompleted})
		if evaluated.Stale {
			out = SaveResult{Completed: previousCompleted, EffectivePosition: state.LastPosition, Stale: true}
			return nil
		}
		stamp := sqliteTime(now)
		ns := evaluated.State
		if _, err = tx.ExecContext(ctx, `UPDATE playback_completion_session SET last_position=?,last_received_at_ms=?,last_sequence=?,
            valid_play_seconds=?,awaiting_baseline=?,updated_at=? WHERE user_id=? AND file_id=? AND session_id=? AND active=1`,
			ns.LastPosition, ns.LastReceivedAtMS, ns.LastSequence, ns.ValidPlaySeconds, boolInt(ns.AwaitingBaseline), stamp, e.UserID, media.fileID, e.SessionID); err != nil {
			return fmt.Errorf("update playback session: %w", err)
		}
		ended := e.Event == EventEnded
		firstCompletion := !previousCompleted && evaluated.Completed
		if err = upsertProgress(ctx, tx, e.UserID, media.fileID, ns.LastPosition, evaluated.Completed, false, firstCompletion || ended, stamp); err != nil {
			return err
		}
		out = SaveResult{Completed: evaluated.Completed, AutoCompleted: evaluated.AutoCompleted, EffectivePosition: ns.LastPosition}
		return nil
	})
	return out, err
}

// MarkWatched explicitly completes progress while preserving position and
// playback history. Explicit state changes retire all completion evidence.
func (s *Store) MarkWatched(ctx context.Context, userID, mediaID int64) error {
	if err := validateIDs(userID, mediaID); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, now time.Time) error {
		if err := requireUser(ctx, tx, userID); err != nil {
			return err
		}
		media, err := resolveMedia(ctx, tx, mediaID)
		if err != nil {
			return err
		}
		if err = reconcileProgress(ctx, tx, userID, media.fileID); err != nil {
			return err
		}
		stamp := sqliteTime(now)
		if err = ensureProgressRow(ctx, tx, userID, media.fileID, stamp); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE play_progress SET completed=1,play_end_at=?,update_at=? WHERE user_id=? AND file_id=?`, stamp, stamp, userID, media.fileID); err != nil {
			return fmt.Errorf("mark progress watched: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM playback_completion_session WHERE user_id=? AND file_id=?`, userID, media.fileID); err != nil {
			return fmt.Errorf("delete playback sessions: %w", err)
		}
		return nil
	})
}
func (s *Store) MarkUnwatched(ctx context.Context, userID, mediaID int64) error {
	if err := validateIDs(userID, mediaID); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, now time.Time) error {
		if err := requireUser(ctx, tx, userID); err != nil {
			return err
		}
		media, err := resolveMedia(ctx, tx, mediaID)
		if err != nil {
			return err
		}
		if err = reconcileProgress(ctx, tx, userID, media.fileID); err != nil {
			return err
		}
		stamp := sqliteTime(now)
		if err = ensureProgressRow(ctx, tx, userID, media.fileID, stamp); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE play_progress SET completed=0,play_end_at=NULL WHERE user_id=? AND file_id=?`, userID, media.fileID); err != nil {
			return fmt.Errorf("mark progress unwatched: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM playback_completion_session WHERE user_id=? AND file_id=?`, userID, media.fileID); err != nil {
			return fmt.Errorf("delete playback sessions: %w", err)
		}
		return nil
	})
}

func (s *Store) ClearProgress(ctx context.Context, userID, mediaID int64) error {
	if err := validateIDs(userID, mediaID); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx, now time.Time) error {
		media, err := resolveMedia(ctx, tx, mediaID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM play_progress WHERE user_id=? AND file_id=?`, userID, media.fileID); err != nil {
			return fmt.Errorf("clear play progress: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM playback_completion_session WHERE user_id=? AND file_id=?`, userID, media.fileID); err != nil {
			return fmt.Errorf("delete playback sessions: %w", err)
		}
		return nil
	})
}

func (s *Store) write(ctx context.Context, fn func(*sql.Tx, time.Time) error) error {
	if s == nil || s.DB == nil {
		return errors.New("playcompletion: nil database")
	}
	return func() error {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		// Force SQLite's deferred transaction to acquire the write lock before any reads.
		if _, err = tx.ExecContext(ctx, `UPDATE playback_completion_session SET active=active WHERE rowid=-1`); err != nil {
			return err
		}
		now := time.Now().UTC()
		if s.Now != nil {
			now = s.Now().UTC()
		}
		if err = fn(tx, now); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}()
}

func requireUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM user WHERE id=?`, userID).Scan(&exists); err != nil {
		return fmt.Errorf("resolve user %d: %w", userID, err)
	}
	return nil
}

func resolveMedia(ctx context.Context, tx *sql.Tx, mediaID int64) (mediaInfo, error) {
	var m mediaInfo
	var duration sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT file_id,COALESCE(file_type,''),duration FROM media WHERE id=?`, mediaID).Scan(&m.fileID, &m.fileType, &duration)
	if err != nil {
		return m, fmt.Errorf("resolve media %d: %w", mediaID, err)
	}
	if duration.Valid {
		m.duration = duration.Int64
	}
	return m, nil
}

type progressRow struct {
	id, position, completed, playCount int64
	start, end, updated                sql.NullString
	updatedAt                          time.Time
	validTime                          bool
}

// reconcileProgress collapses legacy duplicates before completion decisions.
// Parseable update_at values are compared as actual instants, then by id. If
// every legacy timestamp is null/invalid, highest id wins deterministically.
// The canonical raw timestamp is retained; history and monotonic fields merge.
func reconcileProgress(ctx context.Context, tx *sql.Tx, userID int64, fileID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(position,0),COALESCE(completed,0),COALESCE(play_count,0),play_start_at,play_end_at,update_at
        FROM play_progress WHERE user_id=? AND file_id=?`, userID, fileID)
	if err != nil {
		return fmt.Errorf("load duplicate progress: %w", err)
	}
	defer rows.Close()
	var all []progressRow
	for rows.Next() {
		var row progressRow
		if err = rows.Scan(&row.id, &row.position, &row.completed, &row.playCount, &row.start, &row.end, &row.updated); err != nil {
			return fmt.Errorf("scan duplicate progress: %w", err)
		}
		if row.updated.Valid {
			row.updatedAt, row.validTime = parseProgressTime(row.updated.String)
		}
		all = append(all, row)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterate duplicate progress: %w", err)
	}
	if len(all) <= 1 {
		return nil
	}
	canonicalIndex := 0
	for i := 1; i < len(all); i++ {
		if progressRowFresher(all[i], all[canonicalIndex]) {
			canonicalIndex = i
		}
	}
	canonical := all[canonicalIndex]
	// Visit rows in freshness order for nullable history fallback.
	ordered := append([]progressRow(nil), all...)
	sort.SliceStable(ordered, func(i, j int) bool { return progressRowFresher(ordered[i], ordered[j]) })
	for _, row := range ordered {
		if row.completed > canonical.completed {
			canonical.completed = row.completed
		}
		if row.playCount > canonical.playCount {
			canonical.playCount = row.playCount
		}
		if !canonical.start.Valid && row.start.Valid {
			canonical.start = row.start
		}
		if !canonical.end.Valid && row.end.Valid {
			canonical.end = row.end
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE play_progress SET position=?,play_start_at=?,play_end_at=?,completed=?,play_count=?,update_at=? WHERE id=?`, canonical.position, canonical.start, canonical.end, canonical.completed, canonical.playCount, canonical.updated, canonical.id); err != nil {
		return fmt.Errorf("merge duplicate progress: %w", err)
	}
	for _, row := range all {
		if row.id == canonical.id {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM play_progress WHERE id=?`, row.id); err != nil {
			return fmt.Errorf("delete duplicate progress: %w", err)
		}
	}
	return nil
}

func progressRowFresher(a, b progressRow) bool {
	if a.validTime != b.validTime {
		return a.validTime
	}
	if a.validTime && !a.updatedAt.Equal(b.updatedAt) {
		return a.updatedAt.After(b.updatedAt)
	}
	return a.id > b.id
}

func parseProgressTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// upsertProgress uses the existing row id as the conflict key. This remains an
// actual UPSERT without requiring a new uniqueness constraint on legacy databases.
func upsertProgress(ctx context.Context, tx *sql.Tx, userID int64, fileID string, position int64, completed, begin, setEnd bool, stamp string) error {
	completedInt := boolInt(completed)
	beginInt := boolInt(begin)
	endInt := boolInt(setEnd)
	_, err := tx.ExecContext(ctx, `INSERT INTO play_progress(id,user_id,file_id,position,play_start_at,play_end_at,completed,play_count,update_at)
      VALUES((SELECT id FROM play_progress WHERE user_id=? AND file_id=? ORDER BY id LIMIT 1),?,?,?,CASE WHEN ?=1 THEN ? END,CASE WHEN ?=1 THEN ? END,?,CASE WHEN ?=1 THEN 1 ELSE 0 END,?)
      ON CONFLICT(id) DO UPDATE SET position=excluded.position,
        play_start_at=CASE WHEN ?=1 THEN excluded.update_at ELSE play_progress.play_start_at END,
        play_end_at=CASE WHEN ?=1 THEN excluded.update_at ELSE play_progress.play_end_at END,
        completed=MAX(COALESCE(play_progress.completed,0),excluded.completed),
        play_count=COALESCE(play_progress.play_count,0)+CASE WHEN ?=1 THEN 1 ELSE 0 END,update_at=excluded.update_at`,
		userID, fileID, userID, fileID, position, beginInt, stamp, endInt, stamp, completedInt, beginInt, stamp, beginInt, endInt, beginInt)
	if err != nil {
		return fmt.Errorf("upsert play progress: %w", err)
	}
	return nil
}

func ensureProgressRow(ctx context.Context, tx *sql.Tx, userID int64, fileID, stamp string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO play_progress(id,user_id,file_id,position,completed,play_count,update_at)
        VALUES((SELECT id FROM play_progress WHERE user_id=? AND file_id=? ORDER BY id LIMIT 1),?,?,0,0,0,?)
        ON CONFLICT(id) DO NOTHING`, userID, fileID, userID, fileID, stamp)
	if err != nil {
		return fmt.Errorf("ensure play progress: %w", err)
	}
	return nil
}

func loadProgressResult(ctx context.Context, tx *sql.Tx, userID int64, fileID string, out *SaveResult) error {
	var completed int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(completed,0),COALESCE(position,0) FROM play_progress WHERE user_id=? AND file_id=? ORDER BY id LIMIT 1`, userID, fileID).Scan(&completed, &out.EffectivePosition)
	if err != nil {
		return fmt.Errorf("load saved progress: %w", err)
	}
	out.Completed = completed != 0
	return nil
}
func loadProgressResultPreserveStale(ctx context.Context, tx *sql.Tx, userID int64, fileID string, out *SaveResult) error {
	stale := out.Stale
	err := loadProgressResult(ctx, tx, userID, fileID, out)
	if errors.Is(err, sql.ErrNoRows) {
		out.Stale = stale
		return nil
	}
	out.Stale = stale
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func sqliteTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000000") }
func parseSQLiteTime(v string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid sqlite time %q", v)
}

// BeginLegacyPlayback atomically records legacy playback-start metadata without
// creating natural-play evidence or clearing an existing completion.
func (s *Store) BeginLegacyPlayback(ctx context.Context, userID, mediaID, position int64) (SaveResult, error) {
	if err := validateIDs(userID, mediaID); err != nil {
		return SaveResult{}, err
	}
	if position < 0 {
		return SaveResult{}, fmt.Errorf("%w: position must be non-negative", ErrInvalidEvidence)
	}
	var out SaveResult
	err := s.write(ctx, func(tx *sql.Tx, now time.Time) error {
		media, err := resolveMedia(ctx, tx, mediaID)
		if err != nil {
			return err
		}
		if err = reconcileProgress(ctx, tx, userID, media.fileID); err != nil {
			return err
		}
		stamp := sqliteTime(now)
		if err = upsertProgress(ctx, tx, userID, media.fileID, position, false, true, false, stamp); err != nil {
			return err
		}
		return loadProgressResult(ctx, tx, userID, media.fileID, &out)
	})
	return out, err
}

// SaveLegacyProgress atomically persists old completed/position payloads. It
// never creates playback evidence and completion is monotonic.
func (s *Store) SaveLegacyProgress(ctx context.Context, userID, mediaID, position int64, completed bool) (SaveResult, error) {
	if err := validateIDs(userID, mediaID); err != nil {
		return SaveResult{}, err
	}
	if position < 0 {
		return SaveResult{}, fmt.Errorf("%w: position must be non-negative", ErrInvalidEvidence)
	}
	var out SaveResult
	err := s.write(ctx, func(tx *sql.Tx, now time.Time) error {
		media, err := resolveMedia(ctx, tx, mediaID)
		if err != nil {
			return err
		}
		if err = reconcileProgress(ctx, tx, userID, media.fileID); err != nil {
			return err
		}
		stamp := sqliteTime(now)
		if err = upsertProgress(ctx, tx, userID, media.fileID, position, completed, false, completed, stamp); err != nil {
			return err
		}
		return loadProgressResult(ctx, tx, userID, media.fileID, &out)
	})
	return out, err
}
