package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"knox-media/internal/scraper"
	"knox-media/internal/store"
)

// RepairLegacyMedia creates bounded, visibility-preserving ingest generations
// for active legacy videos that lack evidence required by the current plan.
func RepairLegacyMedia(ctx context.Context, db *sql.DB, planner *Planner, batchSize int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if db == nil {
		return 0, errors.New("publication repair: nil database")
	}
	if planner == nil {
		return 0, errors.New("publication repair: nil planner")
	}
	if batchSize <= 0 {
		return 0, nil
	}

	repaired, after := 0, int64(0)
	for {
		rows, err := db.QueryContext(ctx, `SELECT id FROM media WHERE id>? AND status='active' AND file_type='video' AND publication_state IN ('published','degraded') ORDER BY id LIMIT ?`, after, batchSize)
		if err != nil {
			return repaired, fmt.Errorf("publication repair: list candidates: %w", err)
		}
		ids := make([]int64, 0, batchSize)
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return repaired, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return repaired, err
		}
		if len(ids) == 0 {
			return repaired, nil
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return repaired, err
			}
			created, err := repairLegacyMediaOne(ctx, db, planner, id)
			if err != nil {
				return repaired, fmt.Errorf("publication repair media %d: %w", id, err)
			}
			if created {
				repaired++
			}
		}
		after = ids[len(ids)-1]
	}
}

var repairRetryPolicy = store.RetryPolicy{
	Operation:  "publication_legacy_repair_media",
	MaxElapsed: 2 * time.Second, BaseBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
}

func repairLegacyMediaOne(ctx context.Context, db *sql.DB, planner *Planner, mediaID int64) (bool, error) {
	created := false
	err := store.WithBusyRetryPolicyContext(ctx, nil, repairRetryPolicy, func(attemptCtx context.Context) error {
		var attemptCreated bool
		attemptCreated, err := repairLegacyMediaOneAttempt(attemptCtx, db, planner, mediaID)
		if err == nil {
			created = attemptCreated
		}
		return err
	})
	if err != nil && store.IsSQLiteConstraint(err) {
		// Only normalize a concurrent unique/CAS loser after independently
		// observing the winning current repair. Unrelated constraints remain errors.
		current, checkErr := currentRepairCoversRequiredKindsDB(ctx, db, planner, mediaID)
		if checkErr != nil {
			return false, checkErr
		}
		if current {
			return false, nil
		}
	}
	return created, err
}

func currentRepairCoversRequiredKindsDB(ctx context.Context, db *sql.DB, planner *Planner, mediaID int64) (bool, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	required, err := planner.requiredStepsTx(ctx, tx, mediaID)
	if err != nil {
		return false, err
	}
	covered, err := currentRepairCoversRequiredKindsTx(ctx, tx, mediaID, required)
	if err != nil {
		return false, err
	}
	return covered, tx.Commit()
}

func repairLegacyMediaOneAttempt(ctx context.Context, db *sql.DB, planner *Planner, mediaID int64) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT publication_state FROM media WHERE id=? AND status='active' AND file_type='video' AND publication_state IN ('published','degraded')`, mediaID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	required, err := planner.requiredStepsTx(ctx, tx, mediaID)
	if err != nil {
		return false, err
	}
	covered, err := currentRepairCoversRequiredKindsTx(ctx, tx, mediaID, required)
	if err != nil {
		return false, err
	}
	if covered {
		return false, nil
	}
	complete, err := hasCompleteEvidenceForStepsTx(ctx, tx, mediaID, required)
	if err != nil {
		return false, err
	}
	if complete {
		return false, nil
	}
	run, err := planner.RepairMediaTx(ctx, tx, mediaID)
	if err != nil {
		return false, err
	}
	if run.ID == 0 {
		return false, nil
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func currentRepairCoversRequiredKindsTx(ctx context.Context, tx *sql.Tx, mediaID int64, required []StepType) (bool, error) {
	var runID int64
	err := tx.QueryRowContext(ctx, `SELECT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.media_id=? AND r.reason='repair'`, mediaID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, step := range required {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media_ingest_step WHERE run_id=? AND step_type=? AND required=1)`, runID, step).Scan(&present); err != nil {
			return false, err
		}
		if present == 0 {
			return false, nil
		}
	}
	return true, nil
}

func hasCompleteEvidenceTx(ctx context.Context, tx *sql.Tx, planner *Planner, mediaID int64) (bool, error) {
	steps, err := planner.requiredStepsTx(ctx, tx, mediaID)
	if err != nil {
		return false, err
	}
	return hasCompleteEvidenceForStepsTx(ctx, tx, mediaID, steps)
}

func hasCompleteEvidenceForStepsTx(ctx context.Context, tx *sql.Tx, mediaID int64, steps []StepType) (bool, error) {
	for _, step := range steps {
		ok, err := stepEvidenceTx(ctx, tx, mediaID, step)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func (p *Planner) requiredStepsTx(ctx context.Context, tx *sql.Tx, mediaID int64) ([]StepType, error) {
	var preview, encrypted, prepare int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(l.preview_extract,0),COALESCE(l.encrypted_assets_enabled,0),COALESCE(l.jit_prepare_on_ingest,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&preview, &encrypted, &prepare); err != nil {
		return nil, err
	}
	steps := []StepType{StepPoster, StepScrape}
	if preview == 1 {
		steps = append(steps, StepPreview)
	}
	steps = append(steps, StepKeyframe)
	if p.options.SubtitleAuto {
		steps = append(steps, StepSubtitle)
	}
	if p.options.ATrackAuto {
		steps = append(steps, StepAtrack)
	}
	if p.options.EncryptGlobal && encrypted == 1 {
		steps = append(steps, StepEncrypt)
	}
	if p.options.PreparePlanner != nil && p.options.Capabilities != nil && p.options.Capabilities.Available(string(StepPrepare)) && prepare == 1 {
		steps = append(steps, StepPrepare)
	}
	return steps, nil
}

func stepEvidenceTx(ctx context.Context, tx *sql.Tx, mediaID int64, step StepType) (bool, error) {
	// A terminal successful run is durable intent evidence even if workers later
	// clean task history or an operator removes the physical artifact.
	var done int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media_ingest_step s JOIN media_ingest_run r ON r.id=s.run_id WHERE s.media_id=? AND s.step_type=? AND s.status IN ('done','skipped') AND r.status IN ('published','degraded'))`, mediaID, step).Scan(&done); err != nil {
		return false, err
	}
	if done == 1 {
		return true, nil
	}

	var query string
	switch step {
	case StepPoster:
		query = `SELECT EXISTS(SELECT 1 FROM media_derived_assets WHERE media_id=? AND artifact_kind='poster' AND TRIM(COALESCE(enc_path,''))<>'') OR EXISTS(SELECT 1 FROM media WHERE id=? AND (json_valid(meta_json) AND (TRIM(COALESCE(json_extract(meta_json,'$.scrape.poster'),''))<>'' OR TRIM(COALESCE(json_extract(meta_json,'$.scrape.extra.poster'),''))<>''))) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='poster' AND status='done')`
	case StepScrape:
		var taskDone int
		var metaJSON string
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM scrape_task WHERE media_id=? AND status='done'),COALESCE((SELECT meta_json FROM media WHERE id=?),'')`, mediaID, mediaID).Scan(&taskDone, &metaJSON); err != nil {
			return false, err
		}
		return taskDone == 1 || scraper.HasScrapedMetaJSON(metaJSON), nil
	case StepPreview:
		query = `SELECT EXISTS(SELECT 1 FROM preview_task WHERE media_id=? AND status='done' AND (TRIM(COALESCE(sprite_path,''))<>'' OR TRIM(COALESCE(vtt_path,''))<>'')) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='preview' AND status='done')`
	case StepKeyframe:
		query = `SELECT EXISTS(SELECT 1 FROM keyframe_task WHERE media_id=? AND status='done' AND (COALESCE(keyframe_count,0)>0 OR TRIM(COALESCE(output_dir,''))<>'')) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='keyframe' AND status='done')`
	case StepSubtitle:
		query = `SELECT EXISTS(SELECT 1 FROM media_subtitle WHERE media_id=? AND status='ready') OR EXISTS(SELECT 1 FROM subtitle_task WHERE media_id=? AND status='done') OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='subtitle' AND status='done')`
	case StepAtrack:
		query = `SELECT EXISTS(SELECT 1 FROM atrack_task WHERE media_id=? AND status='done') OR EXISTS(SELECT 1 FROM media_derived_assets WHERE media_id=? AND artifact_kind IN ('atrack_playlist','atrack_segment')) OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='atrack' AND status='done')`
	case StepEncrypt:
		query = `SELECT EXISTS(SELECT 1 FROM media_encrypted_assets WHERE media_id=? AND status='encrypted') OR EXISTS(SELECT 1 FROM post_ingest_task WHERE media_id=? AND task_type='encrypt' AND status='done')`
	case StepPrepare:
		query = `SELECT EXISTS(SELECT 1 FROM transcode_task WHERE file_id=(SELECT file_id FROM media WHERE id=?) AND task_type='pretranscode' AND status='done')`
	default:
		return false, fmt.Errorf("unknown step %q", step)
	}
	args := strings.Count(query, "?")
	values := make([]any, args)
	for i := range values {
		values[i] = mediaID
	}
	if err := tx.QueryRowContext(ctx, query, values...).Scan(&done); err != nil {
		return false, err
	}
	return done == 1, nil
}
