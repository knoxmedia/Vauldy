package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
func ValidateAggregateCurrentV2(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.policy_version=2 AND r.superseded_at IS NULL ORDER BY r.id`)
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
		_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
			if e := validateCurrentV2Run(ctx, tx, id); e != nil {
				return e
			}
			return AggregateTx(ctx, tx, id)
		})
		if err != nil {
			return fmt.Errorf("publication v2 startup validate run %d: %w", id, err)
		}
	}
	return nil
}

type stepValidationRow struct {
	id       int64
	typ      string
	required int
}

func validateQueueSemantics(ctx context.Context, q store.SQLExecutor, runID, mediaID, generation int64, steps []stepValidationRow) error {
	for _, step := range steps {
		var total, count int
		if err := q.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM post_ingest_task WHERE ingest_step_id=?)+(SELECT COUNT(*) FROM scrape_task WHERE ingest_step_id=?)+(SELECT COUNT(*) FROM transcode_task WHERE ingest_step_id=?)", step.id, step.id, step.id).Scan(&total); err != nil {
			return err
		}
		if total != 1 {
			return fmt.Errorf("queue execution count mismatch for step %s", step.typ)
		}
		switch StepType(step.typ) {
		case StepScrape:
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM scrape_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND source='auto-scan' AND status=(SELECT status FROM media_ingest_step WHERE id=?)`, step.id, runID, mediaID, generation, step.id).Scan(&count); err != nil {
				return err
			}
		case StepPrepare:
			hasTaskType, err := publicationColumnExistsTx(ctx, q, "transcode_task", "task_type")
			if err != nil {
				return err
			}
			if !hasTaskType {
				return fmt.Errorf("queue semantics mismatch for step %s", step.typ)
			}
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND task_type='pretranscode' AND status=(SELECT status FROM media_ingest_step WHERE id=?)`, step.id, runID, mediaID, generation, step.id).Scan(&count); err != nil {
				return err
			}
		case StepPoster, StepThumbnail, StepPreview, StepSubtitle, StepEncrypt:
			if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE ingest_step_id=? AND ingest_run_id=? AND media_id=? AND generation=? AND task_type=? AND status=(SELECT status FROM media_ingest_step WHERE id=?)`, step.id, runID, mediaID, generation, step.typ, step.id).Scan(&count); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported policy v2 step %s", step.typ)
		}
		if count != 1 {
			return fmt.Errorf("queue semantics mismatch for step %s", step.typ)
		}
	}
	return nil
}
func validateCurrentV2Run(ctx context.Context, q store.SQLExecutor, runID int64) error {
	var raw string
	var mediaID, generation int64
	if err := q.QueryRowContext(ctx, `SELECT media_id,generation,config_snapshot_json FROM media_ingest_run WHERE id=? AND policy_version=2`, runID).Scan(&mediaID, &generation, &raw); err != nil {
		return err
	}
	var valid int
	if err := q.QueryRowContext(ctx, `SELECT json_valid(?)`, raw).Scan(&valid); err != nil || valid != 1 {
		return errors.New("malformed config snapshot")
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || snapshot.PolicyVersion != PolicyV2 || snapshot.FileType == "" || len(snapshot.RequiredSteps) == 0 {
		return errors.New("malformed policy v2 snapshot")
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
	if err := validateQueueSemantics(ctx, q, runID, mediaID, generation, actual); err != nil {
		return err
	}
	var depCount int
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=?`, runID).Scan(&depCount); err != nil {
		return err
	}
	if depCount != len(snapshot.Dependencies) {
		return errors.New("dependency snapshot mismatch")
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

func ReconcileStartupPublicationV2(ctx context.Context, db *sql.DB, planner *Planner) (int, error) {
	n, err := ReplaceActiveV1Runs(ctx, db, planner)
	if err != nil {
		return n, err
	}
	return n, ValidateAggregateCurrentV2(ctx, db)
}
