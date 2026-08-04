package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"knox-media/internal/store"
)

// ReconcileStartupPublicationV2 atomically replaces active policy-v1 generations,
// validates current v2 plans, and aggregates their required outcomes.
func ReplaceActiveV1Runs(ctx context.Context, db *sql.DB, planner *Planner) (int, error) {
	if db == nil || planner == nil {
		return 0, errors.New("publication v2 startup reconcile: database and planner are required")
	}
	replaced := 0
	for {
		var runID int64
		err := db.QueryRowContext(ctx, `SELECT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE COALESCE(r.policy_version,1)=1 AND r.status='processing' AND r.superseded_at IS NULL ORDER BY r.id LIMIT 1`).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return replaced, err
		}
		changed := false
		_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
			var mediaID, generation int64
			var state string
			if e := tx.QueryRowContext(ctx, `SELECT r.media_id,r.generation,m.publication_state FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.id=? AND COALESCE(r.policy_version,1)=1 AND r.status='processing' AND r.superseded_at IS NULL`, runID).Scan(&mediaID, &generation, &state); errors.Is(e, sql.ErrNoRows) {
				return nil
			} else if e != nil {
				return e
			}
			preserve := state == "published" || state == "degraded"
			if preserve {
				compliant, complianceErr := encryptionSelectionCompliantTx(ctx, tx, mediaID, planner.options.EncryptGlobal)
				if complianceErr != nil {
					return complianceErr
				}
				preserve = compliant
			}
			_, e := planner.planReplacement(ctx, tx, mediaID, ReplacementOptions{Reason: PlanReasonRepair, PreserveVisibility: preserve, ExpectedGeneration: generation})
			if errors.Is(e, ErrGenerationConflict) {
				return nil
			}
			if e != nil {
				return e
			}
			changed = true
			return nil
		})
		if err != nil {
			return replaced, fmt.Errorf("publication v2 startup replace run %d: %w", runID, err)
		}
		if changed {
			replaced++
		} else {
			break
		}
	}
	return replaced, nil
}

func encryptionSelectionCompliantTx(ctx context.Context, q store.SQLExecutor, mediaID int64, global bool) (bool, error) {
	if !global {
		return true, nil
	}
	var enabled int
	var selected, encPath, wrapped, iv string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(l.encrypted_assets_enabled,0),COALESCE(m.file_path,''),COALESCE(e.enc_path,''),COALESCE(e.wrapped_dek,''),COALESCE(e.iv,'') FROM media m JOIN library l ON l.id=m.library_id LEFT JOIN media_encrypted_assets e ON e.media_id=m.id AND e.status='encrypted' WHERE m.id=?`, mediaID).Scan(&enabled, &selected, &encPath, &wrapped, &iv)
	if err != nil {
		return false, err
	}
	if enabled != 1 {
		return true, nil
	}
	return samePath(selected, encPath) && wrapped != "" && iv != "", nil
}
func ValidateAggregateCurrentPolicy(ctx context.Context, db *sql.DB, adapters ...ExecutableAdapterRegistry) error {
	if err := validateCurrentPolicyAdmission(ctx, db, firstAdapterRegistry(adapters)); err != nil {
		return err
	}
	if err := validateLegacyV2PersistedGraphs(ctx, db); err != nil {
		return err
	}
	ids, err := currentPolicyRunIDs(ctx, db)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = validateCurrentPolicyRunMode(ctx, db, id, true, firstAdapterRegistry(adapters)); err != nil {
			return fmt.Errorf("publication current-policy startup preflight run %d: %w", id, err)
		}
	}
	if _, err := RepairMissingQueueExecutions(ctx, db); err != nil {
		return fmt.Errorf("publication current-policy startup repair missing queues: %w", err)
	}
	if _, err := RepairDesyncedQueueStepStatus(ctx, db); err != nil {
		return fmt.Errorf("publication current-policy startup repair desynced queue/step status: %w", err)
	}
	if _, err := ReconcileOrphanFailedQueueState(ctx, db); err != nil {
		return fmt.Errorf("publication current-policy startup reconcile orphan failed queues: %w", err)
	}
	for _, id := range ids {
		_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
			if e := projectNodeTransitionTx(ctx, tx, id); e != nil {
				return e
			}
			if e := validateCurrentPolicyRun(ctx, tx, id, firstAdapterRegistry(adapters)); e != nil {
				return e
			}
			return AggregateTx(ctx, tx, id)
		})
		if err != nil {
			return fmt.Errorf("publication current-policy startup validate run %d: %w", id, err)
		}
	}
	return nil
}

// RepairDesyncedQueueStepStatus copies linked queue execution status onto the
// matching media_ingest_step when they diverge (e.g. admin retry updated the
// queue but failed to sync the step). Queue rows remain the execution authority.
func RepairDesyncedQueueStepStatus(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, errors.New("publication queue status repair: database is required")
	}
	repaired := 0
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		n, e := repairDesyncedQueueStepStatusTx(ctx, tx)
		repaired = n
		return e
	})
	return repaired, err
}

func repairDesyncedQueueStepStatusTx(ctx context.Context, tx store.SQLExecutor) (int, error) {
	changed := 0
	res, err := tx.ExecContext(ctx, `
UPDATE media_ingest_step SET
  status=(SELECT p.status FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  attempts=(SELECT p.attempts FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  max_attempts=(SELECT p.max_attempts FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  last_error=(SELECT p.last_error FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  available_at=(SELECT p.available_at FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  lease_owner=(SELECT p.lease_owner FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  lease_until=(SELECT p.lease_until FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  started_at=(SELECT p.started_at FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  finished_at=(SELECT p.finished_at FROM post_ingest_task p WHERE p.ingest_step_id=media_ingest_step.id),
  updated_at=CURRENT_TIMESTAMP
WHERE id IN (
  SELECT s.id FROM media_ingest_step s
  JOIN media_ingest_run r ON r.id=s.run_id
  JOIN media m ON m.id=s.media_id AND m.ingest_generation=s.generation
  JOIN post_ingest_task p ON p.ingest_step_id=s.id AND p.ingest_run_id=s.run_id AND p.media_id=s.media_id AND p.generation=s.generation AND p.task_type=CASE s.step_type WHEN 'subtitle_extract' THEN 'subtitle' WHEN 'atrack_extract' THEN 'atrack' WHEN 'keyframe_extract' THEN 'keyframe' ELSE s.step_type END
  WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL AND p.status<>s.status
)`)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		changed += int(n)
	}
	res, err = tx.ExecContext(ctx, `
UPDATE media_ingest_step SET
  status=(SELECT q.status FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  attempts=(SELECT COALESCE(q.fail_count,0) FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  last_error=(SELECT COALESCE(q.message,'') FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  available_at=(SELECT COALESCE(q.available_at,CURRENT_TIMESTAMP) FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  lease_owner=(SELECT q.lease_owner FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  lease_until=(SELECT q.lease_until FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  started_at=(SELECT q.started_at FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  finished_at=(SELECT q.finished_at FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id),
  updated_at=CURRENT_TIMESTAMP
WHERE id IN (
  SELECT s.id FROM media_ingest_step s
  JOIN media_ingest_run r ON r.id=s.run_id
  JOIN media m ON m.id=s.media_id AND m.ingest_generation=s.generation
  JOIN scrape_task q ON q.ingest_step_id=s.id AND q.ingest_run_id=s.run_id AND q.media_id=s.media_id AND q.generation=s.generation
  WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL AND s.step_type='scrape' AND q.status<>s.status
)`)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		changed += int(n)
	}
	hasPrepareLink, err := publicationColumnExistsTx(ctx, tx, "transcode_task", "task_type")
	if err != nil {
		return changed, err
	}
	if hasPrepareLink {
		setSQL := `
UPDATE media_ingest_step SET
  status=(SELECT q.status FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id),
  last_error=(SELECT COALESCE(q.error_message,'') FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id),
  lease_owner=(SELECT q.lease_owner FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id),
  lease_until=(SELECT q.lease_until FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id)`
		hasStarted, err := publicationColumnExistsTx(ctx, tx, "transcode_task", "started_at")
		if err != nil {
			return changed, err
		}
		hasCompleted, err := publicationColumnExistsTx(ctx, tx, "transcode_task", "completed_at")
		if err != nil {
			return changed, err
		}
		if hasStarted {
			setSQL += `,
  started_at=(SELECT q.started_at FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id)`
		}
		if hasCompleted {
			setSQL += `,
  finished_at=(SELECT q.completed_at FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id)`
		}
		setSQL += `,
  updated_at=CURRENT_TIMESTAMP
WHERE id IN (
  SELECT s.id FROM media_ingest_step s
  JOIN media_ingest_run r ON r.id=s.run_id
  JOIN media m ON m.id=s.media_id AND m.ingest_generation=s.generation
  JOIN transcode_task q ON q.ingest_step_id=s.id AND q.ingest_run_id=s.run_id AND q.media_id=s.media_id AND q.generation=s.generation AND q.task_type='pretranscode'
  WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL AND s.step_type='prepare' AND q.status<>s.status
)`
		res, err = tx.ExecContext(ctx, setSQL)
		if err != nil {
			return changed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed += int(n)
		}
	}
	return changed, nil
}

// RepairMissingQueueExecutions recreates 1:1 queue rows for current policy-v2 steps
// that lost their post_ingest/scrape/transcode execution rows. Step status/attempts
// are copied so terminal outcomes stay authoritative for aggregation.
func RepairMissingQueueExecutions(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, errors.New("publication queue repair: database is required")
	}
	repaired := 0
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		n, e := repairMissingQueueExecutionsTx(ctx, tx)
		repaired = n
		return e
	})
	return repaired, err
}

func repairMissingQueueExecutionsTx(ctx context.Context, tx store.SQLExecutor) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.run_id,s.media_id,s.generation,s.step_type,s.status,s.attempts,s.max_attempts,s.last_error,COALESCE(s.available_at,CURRENT_TIMESTAMP),s.started_at,s.finished_at,s.created_at,s.updated_at,r.scan_task_id
FROM media_ingest_step s
JOIN media_ingest_run r ON r.id=s.run_id
JOIN media m ON m.id=s.media_id AND m.ingest_generation=s.generation
WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL
AND (
  (s.step_type IN ('poster','thumbnail','preview','keyframe','subtitle','atrack','encrypt','subtitle_extract','atrack_extract','keyframe_extract','subtitle_recognize','ai_analysis') AND NOT EXISTS(SELECT 1 FROM post_ingest_task p WHERE p.ingest_step_id=s.id))
  OR (s.step_type='scrape' AND NOT EXISTS(SELECT 1 FROM scrape_task q WHERE q.ingest_step_id=s.id))
  OR (s.step_type='prepare' AND NOT EXISTS(SELECT 1 FROM transcode_task q WHERE q.ingest_step_id=s.id))
)
ORDER BY s.id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type missing struct {
		stepID, runID, mediaID, generation          int64
		stepType, status                            string
		attempts, maxAttempts                       int
		lastError                                   string
		availableAt                                 string
		startedAt, finishedAt, createdAt, updatedAt sql.NullString
		scanTaskID                                  sql.NullInt64
	}
	var items []missing
	for rows.Next() {
		var item missing
		if err = rows.Scan(&item.stepID, &item.runID, &item.mediaID, &item.generation, &item.stepType, &item.status, &item.attempts, &item.maxAttempts, &item.lastError, &item.availableAt, &item.startedAt, &item.finishedAt, &item.createdAt, &item.updatedAt, &item.scanTaskID); err != nil {
			return 0, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	repaired := 0
	for _, item := range items {
		switch StepType(item.stepType) {
		case StepScrape:
			res, e := tx.ExecContext(ctx, `INSERT INTO scrape_task(media_id,source,status,progress,fail_count,message,ingest_run_id,ingest_step_id,generation,available_at,started_at,finished_at,created_at)
VALUES(?,'auto-scan',?,CASE WHEN ? IN ('done','failed','cancelled') THEN 100 ELSE 0 END,?,?,?,?,?,?,?,?,COALESCE(?,CURRENT_TIMESTAMP))
ON CONFLICT(ingest_run_id,ingest_step_id,generation) DO NOTHING`, item.mediaID, item.status, item.status, item.attempts, item.lastError, item.runID, item.stepID, item.generation, item.availableAt, nullString(item.startedAt), nullString(item.finishedAt), nullString(item.createdAt))
			if e != nil {
				return repaired, fmt.Errorf("repair scrape step %d: %w", item.stepID, e)
			}
			n, _ := res.RowsAffected()
			repaired += int(n)
		case StepPrepare:
			var fileID string
			if e := tx.QueryRowContext(ctx, `SELECT file_id FROM media WHERE id=?`, item.mediaID).Scan(&fileID); e != nil {
				return repaired, fmt.Errorf("repair prepare step %d: %w", item.stepID, e)
			}
			cols := "file_id,media_id,status,progress,error_message,task_type,ingest_run_id,ingest_step_id,generation,created_at"
			vals := "?,?,?,CASE WHEN ? IN ('done','failed','cancelled') THEN 100 ELSE 0 END,?,'pretranscode',?,?,?,COALESCE(?,CURRENT_TIMESTAMP)"
			args := []any{fileID, item.mediaID, item.status, item.status, item.lastError, item.runID, item.stepID, item.generation, nullString(item.createdAt)}
			if hasStarted, e := publicationColumnExistsTx(ctx, tx, "transcode_task", "started_at"); e != nil {
				return repaired, e
			} else if hasStarted {
				cols += ",started_at"
				vals += ",?"
				args = append(args, nullString(item.startedAt))
			}
			if hasCompleted, e := publicationColumnExistsTx(ctx, tx, "transcode_task", "completed_at"); e != nil {
				return repaired, e
			} else if hasCompleted {
				cols += ",completed_at"
				vals += ",?"
				args = append(args, nullString(item.finishedAt))
			}
			res, e := tx.ExecContext(ctx, `INSERT INTO transcode_task(`+cols+`) VALUES(`+vals+`)`, args...)
			if e != nil {
				return repaired, fmt.Errorf("repair prepare step %d: %w", item.stepID, e)
			}
			n, _ := res.RowsAffected()
			repaired += int(n)
		case StepPoster, StepThumbnail, StepPreview, StepKeyframe, StepSubtitle, StepAtrack, StepEncrypt, StepSubtitleExtract, StepAtrackExtract, StepKeyframeExtract:
			// Rebind an orphaned same-(media,generation,type) row if present; else insert.
			res, e := tx.ExecContext(ctx, `UPDATE post_ingest_task SET ingest_run_id=?,ingest_step_id=?,scan_task_id=COALESCE(scan_task_id,?),status=?,attempts=?,max_attempts=?,last_error=?,available_at=?,started_at=?,finished_at=?,updated_at=COALESCE(?,CURRENT_TIMESTAMP)
WHERE media_id=? AND generation=? AND task_type=? AND (ingest_step_id IS NULL OR ingest_step_id<>?)`,
				item.runID, item.stepID, nullInt64(item.scanTaskID), item.status, item.attempts, item.maxAttempts, item.lastError, item.availableAt, nullString(item.startedAt), nullString(item.finishedAt), nullString(item.updatedAt),
				item.mediaID, item.generation, executionTaskType(StepType(item.stepType)), item.stepID)
			if e != nil {
				return repaired, fmt.Errorf("rebind %s step %d: %w", item.stepType, item.stepID, e)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				repaired += int(n)
				continue
			}
			res, e = tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,last_error,available_at,started_at,finished_at,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,COALESCE(?,CURRENT_TIMESTAMP),COALESCE(?,CURRENT_TIMESTAMP))`,
				item.mediaID, nullInt64(item.scanTaskID), item.runID, item.stepID, item.generation, executionTaskType(StepType(item.stepType)), item.status, item.attempts, item.maxAttempts, item.lastError, item.availableAt, nullString(item.startedAt), nullString(item.finishedAt), nullString(item.createdAt), nullString(item.updatedAt))
			if e != nil {
				return repaired, fmt.Errorf("repair %s step %d: %w", item.stepType, item.stepID, e)
			}
			n, _ := res.RowsAffected()
			repaired += int(n)
		default:
			return repaired, fmt.Errorf("repair unsupported step %s", item.stepType)
		}
	}
	return repaired, nil
}

func publicationColumnExistsTx(ctx context.Context, tx store.SQLExecutor, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typeName string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}
func nullString(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func nullInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// ReconcileOrphanFailedQueueState fixes current generations that were marked failed
// while required work is still waiting (reopen), and cancels optional waiting queues
// left on genuinely terminal failed/cancelled generations.
func ReconcileOrphanFailedQueueState(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, errors.New("publication orphan reconcile: database is required")
	}
	changed := 0
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		n, e := reconcileOrphanFailedQueueStateTx(ctx, tx)
		changed = n
		return e
	})
	return changed, err
}

func reconcileOrphanFailedQueueStateTx(ctx context.Context, tx store.SQLExecutor) (int, error) {
	changed := 0
	res, err := tx.ExecContext(ctx, `UPDATE media_ingest_run SET status='processing',error_message='',finished_at=NULL,updated_at=CURRENT_TIMESTAMP
WHERE policy_version IN (2,3) AND superseded_at IS NULL AND status='failed'
AND EXISTS(SELECT 1 FROM media m WHERE m.id=media_ingest_run.media_id AND m.ingest_generation=media_ingest_run.generation)
AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.run_id=media_ingest_run.id AND s.required=1 AND s.status='waiting')
AND NOT EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.run_id=media_ingest_run.id AND s.required=1 AND s.status IN ('failed','cancelled'))`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	changed += int(n)
	if n > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE media SET publication_state='processing',publication_error=''
WHERE publication_state='failed' AND id IN (
  SELECT r.media_id FROM media_ingest_run r
  WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL AND r.status='processing' AND r.finished_at IS NULL
  AND media.ingest_generation=r.generation
  AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.run_id=r.id AND s.required=1 AND s.status='waiting')
  AND NOT EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.run_id=r.id AND s.required=1 AND s.status IN ('failed','cancelled'))
)`); err != nil {
			return changed, err
		}
	}

	const orphanOptionalCancel = "cancelled: orphaned on failed generation"
	res, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='cancelled',lease_owner=NULL,lease_until=NULL,
last_error=CASE WHEN TRIM(COALESCE(last_error,''))='' THEN ? ELSE last_error END,
finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP
WHERE required=0 AND status='waiting' AND run_id IN (
  SELECT r.id FROM media_ingest_run r
  JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation
  WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL AND r.status IN ('failed','cancelled')
  AND NOT EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.run_id=r.id AND s.required=1 AND s.status='waiting')
  AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.run_id=r.id AND s.required=1 AND s.status IN ('failed','cancelled'))
)`, orphanOptionalCancel)
	if err != nil {
		return changed, err
	}
	if n, _ = res.RowsAffected(); n > 0 {
		changed += int(n)
	}
	res, err = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,
last_error=CASE WHEN TRIM(COALESCE(last_error,''))='' THEN ? ELSE last_error END,
finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP
WHERE status='waiting' AND ingest_step_id IN (
  SELECT s.id FROM media_ingest_step s
  JOIN media_ingest_run r ON r.id=s.run_id
  JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation
  WHERE s.required=0 AND s.status='cancelled' AND s.last_error=?
  AND r.policy_version IN (2,3) AND r.superseded_at IS NULL AND r.status IN ('failed','cancelled')
)`, orphanOptionalCancel, orphanOptionalCancel)
	if err != nil {
		return changed, err
	}
	if n, _ = res.RowsAffected(); n > 0 {
		changed += int(n)
	}
	res, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,
message=CASE WHEN TRIM(COALESCE(message,''))='' THEN ? ELSE message END,
progress=100,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP)
WHERE status IN ('waiting','failed') AND ingest_step_id IN (
  SELECT s.id FROM media_ingest_step s
  JOIN media_ingest_run r ON r.id=s.run_id
  JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation
  WHERE s.required=0 AND s.step_type='scrape' AND s.status='cancelled' AND s.last_error=?
  AND r.policy_version IN (2,3) AND r.superseded_at IS NULL AND r.status IN ('failed','cancelled')
)`, orphanOptionalCancel, orphanOptionalCancel)
	if err != nil {
		return changed, err
	}
	if n, _ = res.RowsAffected(); n > 0 {
		changed += int(n)
	}
	return changed, nil
}

type stepValidationRow struct {
	id       int64
	typ      string
	required int
}

func validateQueueSemantics(ctx context.Context, q store.SQLExecutor, runID, mediaID, generation int64, steps []stepValidationRow, allowRepairable ...bool) error {
	for _, step := range steps {
		var total, count int
		if err := q.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM post_ingest_task WHERE ingest_step_id=?)+(SELECT COUNT(*) FROM scrape_task WHERE ingest_step_id=?)+(SELECT COUNT(*) FROM transcode_task WHERE ingest_step_id=?)", step.id, step.id, step.id).Scan(&total); err != nil {
			return err
		}
		if StepType(step.typ) == StepMediaVisible {
			if total != 0 {
				return fmt.Errorf("queue execution count mismatch for step %s", step.typ)
			}
			continue
		}
		if total != 1 {
			if len(allowRepairable) > 0 && allowRepairable[0] && total == 0 && queueBacked(StepType(step.typ)) {
				continue
			}
			return fmt.Errorf("queue execution count mismatch for step %s", step.typ)
		}
		switch StepType(step.typ) {
		case StepScrape:
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM scrape_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND source='auto-scan' AND status=(SELECT status FROM media_ingest_step WHERE id=?)`, step.id, runID, mediaID, generation, step.id).Scan(&count); err != nil {
				return err
			}
		case StepPrepare:
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND task_type='pretranscode' AND status=(SELECT status FROM media_ingest_step WHERE id=?)`, step.id, runID, mediaID, generation, step.id).Scan(&count); err != nil {
				return err
			}
		case StepPoster, StepThumbnail, StepPreview, StepSubtitle, StepEncrypt, StepSubtitleExtract, StepAtrackExtract, StepKeyframeExtract, StepSubtitleRecognize, StepAIAnalysis:
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND task_type=? AND status=(SELECT status FROM media_ingest_step WHERE id=?)`, step.id, runID, mediaID, generation, executionTaskType(StepType(step.typ)), step.id).Scan(&count); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported current-policy step %s", step.typ)
		}
		if count != 1 {
			if len(allowRepairable) > 0 && allowRepairable[0] {
				var identityCount int
				if err := q.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM post_ingest_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND task_type=?)+(SELECT COUNT(*) FROM scrape_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND ?='scrape')+(SELECT COUNT(*) FROM transcode_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND ?='prepare')", step.id, runID, mediaID, generation, executionTaskType(StepType(step.typ)), step.id, runID, mediaID, generation, step.typ, step.id, runID, mediaID, generation, step.typ).Scan(&identityCount); err != nil {
					return err
				}
				if identityCount == 1 {
					continue
				}
			}
			return fmt.Errorf("queue semantics mismatch for step %s", step.typ)
		}
	}
	return nil
}
func validateCurrentPolicyRun(ctx context.Context, q store.SQLExecutor, runID int64, adapters ...ExecutableAdapterRegistry) error {
	return validateCurrentPolicyRunMode(ctx, q, runID, false, adapters...)
}
func repairMediaVisibleSnapshotDeps(ctx context.Context, q store.SQLExecutor, runID int64, snapshot *ConfigSnapshot, depCount int) (int, error) {
	// Identify media_visible dependencies in the snapshot that cannot be resolved
	// because no media_visible step exists in the run. Strip them.
	var hasVisibleStep int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND step_type='media_visible'`, runID).Scan(&hasVisibleStep); err != nil {
		return 0, err
	}
	repaired := 0
	var kept []Dependency
	for _, dep := range snapshot.Dependencies {
		if dep.Kind == "media_visible" {
			// media_visible deps with DependsOn set were already canonicalized.
			// Dependencies without DependsOn (legacy NULL depends_on_step_id)
			// are only valid if the run actually has a media_visible step.
			if dep.DependsOn == nil && hasVisibleStep == 0 {
				repaired++
				continue
			}
			// If DependsOn is set, verify the referenced step exists.
			if dep.DependsOn != nil {
				var actual any
				if err := q.QueryRowContext(ctx, `SELECT NULL FROM media_ingest_step WHERE run_id=? AND step_type=?`, runID, string(*dep.DependsOn)).Scan(&actual); err != nil {
					// Referenced step doesn't exist; strip this dep.
					repaired++
					continue
				}
			}
		}
		kept = append(kept, dep)
	}
	if repaired == 0 {
		return 0, nil
	}
	if len(kept) != depCount {
		return repaired, fmt.Errorf("cannot reconcile snapshot deps: expected %d after repair, got %d", depCount, len(kept))
	}
	snapshot.Dependencies = kept
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return repaired, fmt.Errorf("marshal repaired snapshot: %w", err)
	}
	if _, err = q.ExecContext(ctx, `UPDATE media_ingest_run SET config_snapshot_json=? WHERE id=? AND policy_version IN (2,3)`, string(raw), runID); err != nil {
		return repaired, fmt.Errorf("persist repaired snapshot: %w", err)
	}
	return repaired, nil
}
func validateCurrentPolicyRunMode(ctx context.Context, q store.SQLExecutor, runID int64, allowRepairable bool, adapters ...ExecutableAdapterRegistry) error {
	var raw string
	var mediaID, generation int64
	var policyVersion int
	if err := q.QueryRowContext(ctx, `SELECT media_id,generation,config_snapshot_json,policy_version FROM media_ingest_run WHERE id=? AND policy_version IN (2,3)`, runID).Scan(&mediaID, &generation, &raw, &policyVersion); err != nil {
		return err
	}
	var valid int
	if err := q.QueryRowContext(ctx, `SELECT json_valid(?)`, raw).Scan(&valid); err != nil || valid != 1 {
		return errors.New("malformed config snapshot")
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || snapshot.PolicyVersion != policyVersion || snapshot.FileType == "" || len(snapshot.RequiredSteps) == 0 {
		return errors.New("malformed current policy snapshot")
	}
	if policyVersion == PolicyV3 {
		for _, node := range snapshot.Graph.Nodes {
			if (node.Step == StepSubtitleRecognize || node.Step == StepAIAnalysis) && !hasExecutableAdapter(firstAdapterRegistry(adapters), node.Step) {
				return fmt.Errorf("executable adapter unavailable for %s under current policy v%d", node.Step, policyVersion)
			}
		}
		if len(snapshot.Graph.Nodes) == 0 || snapshot.ProcessingProvenance.Explicit == nil {
			return errors.New("malformed policy v3 snapshot")
		}
		if err := ValidatePlanGraph(snapshot.Graph); err != nil {
			return fmt.Errorf("invalid policy v3 graph: %w", err)
		}
	}

	rows, err := q.QueryContext(ctx, `SELECT id,step_type,required FROM media_ingest_step WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return err
	}
	var actual []stepValidationRow
	for rows.Next() {
		var row stepValidationRow
		if err = rows.Scan(&row.id, &row.typ, &row.required); err != nil {
			rows.Close()
			return err
		}
		actual = append(actual, row)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if policyVersion == PolicyV2 {
		legacy, loadErr := loadPersistedPlanGraph(ctx, q, runID)
		if loadErr != nil {
			return loadErr
		}
		if err := validateLegacyV2Graph(legacy); err != nil {
			return fmt.Errorf("invalid legacy policy v2 graph: %w", err)
		}
	}
	expected := append(append([]StepType{}, snapshot.RequiredSteps...), snapshot.OptionalSteps...)
	if len(actual) != len(expected) {
		return errors.New("snapshot step count mismatch")
	}
	for i, step := range expected {
		want := 0
		if i < len(snapshot.RequiredSteps) {
			want = 1
		}
		if actual[i].typ != string(step) || actual[i].required != want {
			return errors.New("snapshot step requiredness mismatch")
		}
	}
	if err := validateQueueSemantics(ctx, q, runID, mediaID, generation, actual, allowRepairable); err != nil {
		return err
	}
	if policyVersion == PolicyV3 {
		persisted, loadErr := loadPersistedPlanGraph(ctx, q, runID)
		if loadErr != nil {
			return loadErr
		}
		if !reflect.DeepEqual(persisted, snapshot.Graph) {
			return errors.New("persisted policy v3 graph differs from snapshot")
		}
		if err := ValidatePlanGraph(persisted); err != nil {
			return fmt.Errorf("invalid persisted policy v3 graph: %w", err)
		}
	}
	var depCount int
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=?`, runID).Scan(&depCount); err != nil {
		return err
	}
	if depCount != len(snapshot.Dependencies) {
		// The migration may have cleaned orphaned media_visible dependencies
		// from the table but the snapshot still references them. Strip any
		// media_visible deps that point to non-existent media_visible steps.
		if depCount < len(snapshot.Dependencies) && allowRepairable {
			if repaired, repairErr := repairMediaVisibleSnapshotDeps(ctx, q, runID, &snapshot, depCount); repairErr != nil {
				return repairErr
			} else if repaired > 0 {
				// Re-read depCount after snapshot repair.
				var newDepCount int
				if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=?`, runID).Scan(&newDepCount); err != nil {
					return err
				}
				if newDepCount != len(snapshot.Dependencies) {
					return errors.New("dependency snapshot mismatch")
				}
				depCount = newDepCount
			}
		}
		if depCount != len(snapshot.Dependencies) {
			return errors.New("dependency snapshot mismatch")
		}
	}
	for _, dep := range snapshot.Dependencies {
		var count int
		var target any
		if dep.DependsOn != nil {
			target = string(*dep.DependsOn)
		}
		if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id LEFT JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE s.run_id=? AND s.step_type=? AND d.dependency_kind=? AND ((? IS NULL AND p.id IS NULL) OR p.step_type=?)`, runID, dep.Step, dep.Kind, target, target).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return errors.New("dependency edge mismatch")
		}
	}
	var evidenceMismatch int
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN media_ingest_step s ON s.id=e.step_id WHERE e.run_id=? AND (e.media_id<>s.media_id OR e.generation<>s.generation OR (e.kind<>s.step_type AND NOT (s.step_type='scrape' AND e.kind='scrape_artwork')) OR s.status NOT IN ('done','skipped') OR json_valid(e.artifact_refs_json)<>1)`, runID).Scan(&evidenceMismatch); err != nil {
		return err
	}
	if evidenceMismatch != 0 {
		return errors.New("evidence semantics mismatch")
	}
	var mismatch int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND (media_id<>? OR generation<>?)`, runID, mediaID, generation).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch != 0 {
		return errors.New("step identity mismatch")
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id LEFT JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE s.run_id=? AND (s.media_id<>? OR s.generation<>? OR (p.id IS NOT NULL AND (p.run_id<>? OR p.media_id<>? OR p.generation<>?)))`, runID, mediaID, generation, runID, mediaID, generation).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch != 0 {
		return errors.New("dependency identity mismatch")
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence WHERE run_id=? AND (media_id<>? OR generation<>?)`, runID, mediaID, generation).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch != 0 {
		return errors.New("evidence identity mismatch")
	}
	return nil
}

func ReconcileStartupPublicationCurrentPolicy(ctx context.Context, db *sql.DB, planner *Planner) (int, error) {
	n, err := ReplaceActiveV1Runs(ctx, db, planner)
	if err != nil {
		return n, err
	}
	return n, ValidateAggregateCurrentPolicy(ctx, db, planner.options.ExecutableAdapters)
}

// ValidateAggregateCurrentV2 is retained for callers compiled against the v2 API;
// it validates and reconciles both supported current policies (v2 and v3).
func ValidateAggregateCurrentV2(ctx context.Context, db *sql.DB) error {
	return ValidateAggregateCurrentPolicy(ctx, db)
}
func firstAdapterRegistry(registries []ExecutableAdapterRegistry) ExecutableAdapterRegistry {
	if len(registries) == 0 {
		return nil
	}
	return registries[0]
}

// ReconcileStartupPublicationV2 is the compatibility name for the unified
// v1 replacement plus current-policy (v2/v3) startup path.
func ReconcileStartupPublicationV2(ctx context.Context, db *sql.DB, planner *Planner) (int, error) {
	return ReconcileStartupPublicationCurrentPolicy(ctx, db, planner)
}

func validateCurrentPolicyAdmission(ctx context.Context, q store.SQLExecutor, adapters ExecutableAdapterRegistry) error {
	rows, err := q.QueryContext(ctx, `SELECT r.id,r.config_snapshot_json FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.policy_version=3 AND r.superseded_at IS NULL ORDER BY r.id`)
	if err != nil {
		return err
	}
	type candidate struct {
		id       int64
		snapshot ConfigSnapshot
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		var raw string
		if err = rows.Scan(&item.id, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(raw), &item.snapshot); err != nil {
			rows.Close()
			return fmt.Errorf("publication current-policy startup validate run %d: malformed policy v3 snapshot", item.id)
		}
		candidates = append(candidates, item)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range candidates {
		if err = validateV3SnapshotAdmission(item.snapshot, adapters); err != nil {
			return fmt.Errorf("publication current-policy startup validate run %d: %w", item.id, err)
		}
		persisted, loadErr := loadPersistedPlanGraph(ctx, q, item.id)
		if loadErr != nil {
			return loadErr
		}
		if !reflect.DeepEqual(persisted, item.snapshot.Graph) {
			return fmt.Errorf("publication current-policy startup validate run %d: persisted policy v3 graph differs from snapshot", item.id)
		}
	}
	return nil
}
func validateV3SnapshotAdmission(snapshot ConfigSnapshot, adapters ExecutableAdapterRegistry) error {
	if snapshot.PolicyVersion != PolicyV3 || len(snapshot.Graph.Nodes) == 0 || snapshot.ProcessingProvenance.Explicit == nil {
		return errors.New("malformed policy v3 snapshot")
	}
	nodes := make(map[StepType]bool, len(snapshot.Graph.Nodes))
	for _, node := range snapshot.Graph.Nodes {
		nodes[node.Step] = true
	}
	selected := map[StepType]bool{StepPreview: snapshot.ProcessingEffective.Preview, StepSubtitleExtract: snapshot.ProcessingEffective.SubtitleExtract, StepAtrackExtract: snapshot.ProcessingEffective.ATrackExtract, StepSubtitleRecognize: snapshot.ProcessingEffective.SubtitleRecognize, StepKeyframeExtract: snapshot.ProcessingEffective.KeyframeExtract, StepAIAnalysis: snapshot.ProcessingEffective.AIAnalysis}
	for step, want := range selected {
		if nodes[step] != want {
			return fmt.Errorf("effective processing options differ from graph for %s", step)
		}
	}
	for _, step := range []StepType{StepSubtitleRecognize, StepAIAnalysis} {
		if selected[step] && !hasExecutableAdapter(adapters, step) {
			return fmt.Errorf("executable adapter unavailable for %s under current policy v%d", step, PolicyV3)
		}
	}
	if err := ValidatePlanGraph(snapshot.Graph); err != nil {
		return fmt.Errorf("invalid policy v3 graph: %w", err)
	}
	return nil
}

// The canonical migration materializes historical nullable media_visible dependencies
// as concrete success edges before startup validation reaches this loader.
func loadPersistedPlanGraph(ctx context.Context, q store.SQLExecutor, runID int64) (PlanGraph, error) {
	graph := PlanGraph{}
	rows, err := q.QueryContext(ctx, `SELECT step_type,generation,required FROM media_ingest_step WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var node PlanNode
		var required int
		if err = rows.Scan(&node.Step, &node.Generation, &required); err != nil {
			rows.Close()
			return graph, err
		}
		node.Required = required == 1
		graph.Nodes = append(graph.Nodes, node)
	}
	if err = rows.Close(); err != nil {
		return graph, err
	}
	rows, err = q.QueryContext(ctx, `SELECT s.step_type,d.dependency_kind,p.step_type,s.generation,p.generation FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE s.run_id=? ORDER BY s.id,p.id,d.dependency_kind`, runID)
	if err != nil {
		return graph, err
	}
	for rows.Next() {
		var edge Dependency
		var target StepType
		if err = rows.Scan(&edge.Step, &edge.Kind, &target, &edge.Generation, &edge.DependsOnGeneration); err != nil {
			rows.Close()
			return graph, err
		}
		edge.DependsOn = &target
		graph.Edges = append(graph.Edges, edge)
	}
	if err = rows.Close(); err != nil {
		return graph, err
	}
	return graph, nil
}

func validateLegacyV2PersistedGraphs(ctx context.Context, q store.SQLExecutor) error {
	rows, err := q.QueryContext(ctx, `SELECT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.policy_version=2 AND r.superseded_at IS NULL ORDER BY r.id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		graph, loadErr := loadPersistedPlanGraph(ctx, q, id)
		if loadErr != nil {
			return loadErr
		}
		if validateErr := validateLegacyV2Graph(graph); validateErr != nil {
			return fmt.Errorf("publication current-policy startup validate run %d: invalid legacy policy v2 graph: %w", id, validateErr)
		}
	}
	return nil
}

func currentPolicyRunIDs(ctx context.Context, q store.SQLExecutor) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.policy_version IN (2,3) AND r.superseded_at IS NULL ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
