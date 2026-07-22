package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"knox-media/internal/coreiface"
)

var ErrIngestNotFound = errors.New("publication ingest not found")
var ErrNoRetryableWork = errors.New("publication ingest has no retryable work")

// RetryIngest retries exactly one current media generation. Terminal media get a
// new immutable generation rebuilt from the previous run snapshot.
func RetryIngest(ctx context.Context, db *sql.DB, mediaID int64, prepare coreiface.IngestPreparePlanner) error {
	if db == nil || mediaID <= 0 {
		return ErrIngestNotFound
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation int64
	var mediaState string
	if err = tx.QueryRowContext(ctx, `SELECT ingest_generation,publication_state FROM media WHERE id=?`, mediaID).Scan(&generation, &mediaState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIngestNotFound
		}
		return err
	}
	var runID int64
	var runState string
	var snapshot string
	var policyVersion int
	var scanTaskID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id,status,config_snapshot_json,policy_version,scan_task_id FROM media_ingest_run WHERE media_id=? AND generation=?`, mediaID, generation).Scan(&runID, &runState, &snapshot, &policyVersion, &scanTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIngestNotFound
	}
	if err != nil {
		return err
	}
	available := time.Now().UTC().Add(5 * time.Minute).Format("2006-01-02 15:04:05")
	if mediaState == "degraded" && runState == "degraded" {
		changed, e := retryDegradedRunTx(ctx, tx, runID, generation, available)
		if e != nil {
			return e
		}
		if changed == 0 {
			return ErrNoRetryableWork
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_run SET reason='manual_retry',preserve_visibility=1,error_message='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND generation=?`, runID, generation); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return nil
	}
	if mediaState != "failed" && mediaState != "cancelled" {
		return ErrNoRetryableWork
	}
	if err = validateRetryPolicySnapshot(policyVersion, snapshot); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE media SET ingest_generation=ingest_generation+1,publication_state='processing',publication_error='' WHERE id=? AND ingest_generation=? AND publication_state IN ('failed','cancelled')`, mediaID, generation)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrNoRetryableWork
	}
	newGeneration := generation + 1
	result, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(?,?,?,'manual_retry','processing',0,?,?)`, mediaID, newGeneration, nullInt(scanTaskID), snapshot, policyVersion)
	if err != nil {
		return fmt.Errorf("retry ingest insert run: %w", err)
	}
	newRunID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,step_type,required,max_attempts FROM media_ingest_step WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return err
	}
	type oldStep struct {
		id            int64
		typ           string
		required, max int
	}
	var steps []oldStep
	for rows.Next() {
		var s oldStep
		if err = rows.Scan(&s.id, &s.typ, &s.required, &s.max); err != nil {
			rows.Close()
			return err
		}
		steps = append(steps, s)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(steps) == 0 {
		return ErrNoRetryableWork
	}
	stepIDMap := make(map[int64]int64, len(steps))
	for _, s := range steps {
		result, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,max_attempts,available_at) VALUES(?,?,?,?,?,'waiting',?,?)`, newRunID, mediaID, newGeneration, s.typ, s.required, s.max, available)
		if err != nil {
			return err
		}
		stepID, idErr := result.LastInsertId()
		if idErr != nil {
			return idErr
		}
		stepIDMap[s.id] = stepID
		switch s.typ {
		case "scrape":
			_, err = tx.ExecContext(ctx, `INSERT INTO scrape_task(media_id,source,status,progress,available_at,ingest_run_id,ingest_step_id,generation) VALUES(?,'manual-retry','waiting',0,?,?,?,?)`, mediaID, available, newRunID, stepID, newGeneration)
		case "prepare":
			err = clonePrepareExecutionTx(ctx, tx, s.id, newRunID, stepID, newGeneration, available)
			if errors.Is(err, sql.ErrNoRows) {
				if prepare == nil {
					return errors.New("publication retry: prepare immutable snapshot unavailable")
				}
				return errors.New("publication retry: legacy prepare execution has no immutable snapshot")
			}
		default:
			if queueBacked(StepType(s.typ)) {
				_, err = tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts,available_at) VALUES(?,?,?,?,?,?,'waiting',?,?)`, mediaID, nullInt(scanTaskID), newRunID, stepID, newGeneration, s.typ, s.max, available)
			}
		}
		if err != nil {
			return fmt.Errorf("retry ingest enqueue %s: %w", s.typ, err)
		}
	}
	if err = cloneRetryDependenciesTx(ctx, tx, runID, mediaID, generation, newRunID, newGeneration, stepIDMap); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func retryDegradedRunTx(ctx context.Context, tx *sql.Tx, runID, generation int64, available string) (int64, error) {
	var changed int64
	res, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND generation=? AND required=1 AND status IN ('failed','cancelled') AND ((step_type='scrape' AND EXISTS(SELECT 1 FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id AND q.ingest_run_id=? AND q.generation=? AND q.status IN ('failed','cancelled','abandoned'))) OR (step_type='prepare' AND EXISTS(SELECT 1 FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id AND q.ingest_run_id=? AND q.generation=? AND q.status IN ('failed','cancelled'))) OR (step_type NOT IN ('scrape','prepare') AND EXISTS(SELECT 1 FROM post_ingest_task q WHERE q.ingest_step_id=media_ingest_step.id AND q.ingest_run_id=? AND q.generation=? AND q.status IN ('failed','cancelled'))))`, available, runID, generation, runID, generation, runID, generation, runID, generation)
	if err != nil {
		return 0, err
	}
	changed, _ = res.RowsAffected()
	if changed == 0 {
		return 0, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND generation=? AND status IN ('failed','cancelled') AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=post_ingest_task.ingest_step_id AND s.required=1 AND s.status='waiting')`, available, runID, generation); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='waiting',fail_count=0,message='',progress=0,lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=? WHERE ingest_run_id=? AND generation=? AND status IN ('failed','cancelled','abandoned') AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=scrape_task.ingest_step_id AND s.required=1 AND s.status='waiting')`, available, runID, generation); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE transcode_task SET status='waiting',progress=0,error_message='',started_at=NULL,completed_at=NULL WHERE ingest_run_id=? AND generation=? AND status IN ('failed','cancelled') AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=transcode_task.ingest_step_id AND s.required=1 AND s.status='waiting')`, runID, generation); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET status='waiting',progress=0,error_message='',available_at=?,lease_owner=NULL,lease_until=NULL,started_at=NULL,completed_at=NULL WHERE task_id IN(SELECT id FROM transcode_task WHERE ingest_run_id=? AND generation=? AND status='waiting') AND status IN ('failed','cancelled')`, available, runID, generation); err != nil {
		return 0, err
	}
	return changed, nil
}
func nullInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func clonePrepareExecutionTx(ctx context.Context, tx *sql.Tx, oldStepID, newRunID, newStepID, newGeneration int64, available string) error {
	var oldTaskID int64
	err := tx.QueryRowContext(ctx, `SELECT t.id FROM transcode_task t WHERE t.ingest_step_id=? ORDER BY t.id LIMIT 1`, oldStepID).Scan(&oldTaskID)
	if err != nil {
		return err
	}
	var invalid int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pretranscode_rendition_job WHERE task_id=? AND (COALESCE(config_snapshot_json,'')='' OR json_valid(config_snapshot_json)=0)`, oldTaskID).Scan(&invalid); err != nil {
		return err
	}
	if invalid > 0 {
		return errors.New("publication retry: malformed prepare immutable snapshot")
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO transcode_task(file_id,quality,status,progress,task_type,preset_id,media_id,ingest_run_id,ingest_step_id,generation) SELECT file_id,quality,'waiting',0,task_type,preset_id,media_id,?,?,? FROM transcode_task WHERE id=?`, newRunID, newStepID, newGeneration, oldTaskID)
	if err != nil {
		return err
	}
	newTaskID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,encryption_mode,priority,output_path,ingest_jobs_snapshot_json) SELECT ?,preset_id,output_format,encryption_mode,priority,output_path,ingest_jobs_snapshot_json FROM pretranscode_task_meta WHERE task_id=?`, newTaskID, oldTaskID)
	if err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,available_at,config_snapshot_json) SELECT ?,rendition_id,rendition_name,'waiting',0,?,config_snapshot_json FROM pretranscode_rendition_job WHERE task_id=?`, newTaskID, available, oldTaskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func validateRetryPolicySnapshot(policyVersion int, snapshot string) error {
	if policyVersion == 1 {
		return nil
	}
	if policyVersion != PolicyV2 {
		return fmt.Errorf("publication retry: unsupported policy version %d", policyVersion)
	}
	var decoded struct {
		PolicyVersion int `json:"policy_version"`
	}
	if err := json.Unmarshal([]byte(snapshot), &decoded); err != nil {
		return fmt.Errorf("publication retry: decode v2 policy snapshot: %w", err)
	}
	if decoded.PolicyVersion != policyVersion {
		return fmt.Errorf("publication retry: policy snapshot version %d does not match run version %d", decoded.PolicyVersion, policyVersion)
	}
	return nil
}

func cloneRetryDependenciesTx(ctx context.Context, tx *sql.Tx, oldRunID, mediaID, oldGeneration, newRunID, newGeneration int64, stepIDMap map[int64]int64) (retErr error) {
	rows, err := tx.QueryContext(ctx, `
SELECT d.step_id,d.depends_on_step_id,d.dependency_kind,
       s.run_id,s.media_id,s.generation,
       t.run_id,t.media_id,t.generation
FROM media_ingest_step_dependency d
JOIN media_ingest_step s ON s.id=d.step_id
LEFT JOIN media_ingest_step t ON t.id=d.depends_on_step_id
WHERE s.run_id=?
ORDER BY d.step_id,d.dependency_kind,d.depends_on_step_id`, oldRunID)
	if err != nil {
		return fmt.Errorf("retry ingest query dependencies: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, rows.Close()) }()
	type oldDependency struct {
		newStepID int64
		dependsOn sql.NullInt64
		kind      string
	}
	var dependencies []oldDependency
	for rows.Next() {
		var oldStepID, sourceRun, sourceMedia, sourceGeneration int64
		var oldTargetID, targetRun, targetMedia, targetGeneration sql.NullInt64
		var kind string
		if err := rows.Scan(&oldStepID, &oldTargetID, &kind, &sourceRun, &sourceMedia, &sourceGeneration, &targetRun, &targetMedia, &targetGeneration); err != nil {
			return fmt.Errorf("retry ingest scan dependency: %w", err)
		}
		newStepID, ok := stepIDMap[oldStepID]
		if !ok || sourceRun != oldRunID || sourceMedia != mediaID || sourceGeneration != oldGeneration {
			return errors.New("publication retry: dependency source belongs to a different run/media/generation")
		}
		dep := oldDependency{newStepID: newStepID, dependsOn: oldTargetID, kind: kind}
		if kind == string(DependencyMediaVisible) {
			if oldTargetID.Valid {
				return errors.New("publication retry: media_visible dependency has target")
			}
		} else if kind == string(DependencyStepDone) {
			if !oldTargetID.Valid || !targetRun.Valid || !targetMedia.Valid || !targetGeneration.Valid || targetRun.Int64 != oldRunID || targetMedia.Int64 != mediaID || targetGeneration.Int64 != oldGeneration {
				return errors.New("publication retry: dependency target belongs to a different run/media/generation")
			}
			if _, ok := stepIDMap[oldTargetID.Int64]; !ok {
				return errors.New("publication retry: dependency target has no cloned step")
			}
		} else {
			return fmt.Errorf("publication retry: unsupported dependency kind %q", kind)
		}
		dependencies = append(dependencies, dep)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("retry ingest iterate dependencies: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("retry ingest close dependencies: %w", err)
	}
	for _, dep := range dependencies {
		var target any
		if dep.dependsOn.Valid {
			target = stepIDMap[dep.dependsOn.Int64]
		}
		if err := validateDependencyTx(ctx, tx, dep.newStepID, target, mediaID, newGeneration, newRunID); err != nil {
			return fmt.Errorf("retry ingest validate dependency: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(?,?,?)`, dep.newStepID, target, dep.kind); err != nil {
			return fmt.Errorf("retry ingest insert dependency: %w", err)
		}
	}
	return nil
}
