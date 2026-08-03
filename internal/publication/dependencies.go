package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"knox-media/internal/store"
)

type impossibleDependencyReason struct {
	Code             string         `json:"code"`
	PredecessorID    int64          `json:"predecessor_step_id"`
	PredecessorType  StepType       `json:"predecessor_step_type"`
	PredecessorState string         `json:"predecessor_status"`
	DependencyKind   DependencyKind `json:"dependency_kind"`
	RetryRound       int            `json:"retry_round"`
	Generation       int64          `json:"generation"`
}

func dependencyReason(ctx context.Context, tx store.SQLExecutor, stepID, pred, gen int64, kind DependencyKind, status string, typ StepType) (string, error) {
	var round int
	if err := tx.QueryRowContext(ctx, `SELECT retry_round FROM media_ingest_step WHERE id=?`, stepID).Scan(&round); err != nil {
		return "", err
	}
	b, e := json.Marshal(impossibleDependencyReason{"dependency_impossible", pred, typ, status, kind, round, gen})
	return string(b), e
}

func syncSkippedQueueForStepTx(ctx context.Context, tx store.SQLExecutor, stepID, runID, gen int64, stepType StepType, reason string) error {
	if stepType == StepMediaVisible {
		return nil
	}
	family, table, errCol, err := queueFamilyForStep(stepType)
	if err != nil {
		return fmt.Errorf("publication dependencies: cannot sync queue for step %d (%s): %w", stepID, stepType, err)
	}
	finishedCol := "finished_at"
	if table == "transcode_task" {
		finishedCol = "completed_at"
	}
	r, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status='skipped',%s=?,lease_owner=NULL,lease_until=NULL,%s=COALESCE(%s,CURRENT_TIMESTAMP) WHERE ingest_step_id=? AND ingest_run_id=? AND generation=? AND status='waiting'`, table, errCol, finishedCol, finishedCol), reason, stepID, runID, gen)
	if err != nil {
		return fmt.Errorf("publication dependencies: %s queue skip for step %d: %w", family, stepID, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return fmt.Errorf("publication dependencies: %s queue rows-affected for step %d: %w", family, stepID, err)
	}
	if n == 0 {
		return fmt.Errorf("publication dependencies: orphan %s queue for step %d", family, stepID)
	}
	return nil
}

func PropagateImpossibleDependenciesTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication dependencies: invalid transaction or run")
	}
	var bad int
	if e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id LEFT JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE s.run_id=? AND (p.id IS NULL OR p.run_id<>s.run_id OR p.media_id<>s.media_id OR p.generation<>s.generation)`, runID).Scan(&bad); e != nil {
		return e
	}
	if bad > 0 {
		return fmt.Errorf("publication dependencies: malformed generation in run %d", runID)
	}
	for {
		rows, e := tx.QueryContext(ctx, `SELECT s.id,s.generation,s.step_type,d.depends_on_step_id,d.dependency_kind,p.status,p.step_type FROM media_ingest_step s JOIN media_ingest_step_dependency d ON d.step_id=s.id JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE s.run_id=? AND s.status='waiting' AND d.dependency_kind='success' AND p.status IN ('failed','cancelled','skipped') ORDER BY s.id,d.depends_on_step_id`, runID)
		if e != nil {
			return e
		}
		type c struct {
			id, gen, pred int64
			stepType      string
			kind, status  string
			predType      string
		}
		var cs []c
		for rows.Next() {
			var x c
			if e = rows.Scan(&x.id, &x.gen, &x.stepType, &x.pred, &x.kind, &x.status, &x.predType); e != nil {
				rows.Close()
				return e
			}
			cs = append(cs, x)
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		if e = rows.Close(); e != nil {
			return e
		}
		if len(cs) == 0 {
			return nil
		}
		for _, x := range cs {
			reason, e := dependencyReason(ctx, tx, x.id, x.pred, x.gen, DependencyKind(x.kind), x.status, StepType(x.predType))
			if e != nil {
				return e
			}
			r, e := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='skipped',last_error=?,lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND generation=? AND status='waiting'`, reason, x.id, runID, x.gen)
			if e != nil {
				return e
			}
			n, _ := r.RowsAffected()
			if n == 0 {
				continue
			}
			if e = syncSkippedQueueForStepTx(ctx, tx, x.id, runID, x.gen, StepType(x.stepType), reason); e != nil {
				return e
			}
		}
	}
}
