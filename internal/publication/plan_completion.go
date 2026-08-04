package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"knox-media/internal/store"
)

type PlanCompletion struct {
	Total, Terminal, Waiting, Running, Done, Skipped, Failed, Cancelled int
	AllTerminal                                                         bool
}

func RecomputePlanCompletionTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication plan completion: invalid transaction or run")
	}
	var mediaID, gen int64
	if e := tx.QueryRowContext(ctx, `SELECT media_id,generation FROM media_ingest_run WHERE id=?`, runID).Scan(&mediaID, &gen); e != nil {
		return e
	}
	var p PlanCompletion
	if e := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status IN ('done','skipped','failed','cancelled')),0),COALESCE(SUM(status='waiting'),0),COALESCE(SUM(status='running'),0),COALESCE(SUM(status='done'),0),COALESCE(SUM(status='skipped'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status='cancelled'),0) FROM media_ingest_step WHERE run_id=? AND generation=?`, runID, gen).Scan(&p.Total, &p.Terminal, &p.Waiting, &p.Running, &p.Done, &p.Skipped, &p.Failed, &p.Cancelled); e != nil {
		return e
	}
	if p.Total == 0 {
		return fmt.Errorf("publication plan completion: empty plan %d", runID)
	}
	if p.Terminal != p.Done+p.Skipped+p.Failed+p.Cancelled || p.Waiting+p.Running+p.Terminal != p.Total || p.Terminal > p.Total {
		return fmt.Errorf("publication plan completion: malformed plan %d", runID)
	}
	p.AllTerminal = p.Total > 0 && p.Terminal == p.Total
	_, e := tx.ExecContext(ctx, `INSERT INTO media_plan_completion(run_id,media_id,generation,all_terminal,total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count,completed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?, ?,CASE WHEN ?=1 THEN CURRENT_TIMESTAMP ELSE NULL END,CURRENT_TIMESTAMP) ON CONFLICT(run_id) DO UPDATE SET all_terminal=excluded.all_terminal,total_count=excluded.total_count,terminal_count=excluded.terminal_count,waiting_count=excluded.waiting_count,running_count=excluded.running_count,done_count=excluded.done_count,skipped_count=excluded.skipped_count,failed_count=excluded.failed_count,cancelled_count=excluded.cancelled_count,completed_at=CASE WHEN excluded.all_terminal=1 THEN COALESCE(media_plan_completion.completed_at,CURRENT_TIMESTAMP) ELSE NULL END,updated_at=CURRENT_TIMESTAMP`, runID, mediaID, gen, boolInt(p.AllTerminal), p.Total, p.Terminal, p.Waiting, p.Running, p.Done, p.Skipped, p.Failed, p.Cancelled, boolInt(p.AllTerminal))
	return e
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type ReopenRequest struct {
	RunID, StepID, ActorID int64
	Target                 StepType
	Reason                 string
	ExpectedGeneration     int64
	ExpectedRetryRound     int
}

type queueIdentity struct {
	Family     string
	Table      string
	ID         int64
	Status     string
	RetryRound int
	ErrorText  string
}

func queueFamilyForStep(stepType StepType) (family, table, errCol string, err error) {
	switch stepType {
	case StepScrape:
		return "scrape", "scrape_task", "message", nil
	case StepPrepare:
		return "prepare", "transcode_task", "error_message", nil
	case StepPreview, StepSubtitle, StepSubtitleExtract, StepAtrackExtract, StepSubtitleRecognize, StepKeyframeExtract, StepAIAnalysis, StepPoster, StepThumbnail, StepKeyframe, StepAtrack, StepEncrypt, StepPackage, StepPretranscode, StepMetadata:
		return "post_ingest", "post_ingest_task", "last_error", nil
	default:
		return "", "", "", fmt.Errorf("publication reopen: step %s has no queue family", stepType)
	}
}

func resolveQueueIdentity(ctx context.Context, tx store.SQLExecutor, stepID, runID, generation int64, stepType StepType) (queueIdentity, error) {
	family, table, errCol, err := queueFamilyForStep(stepType)
	if err != nil {
		return queueIdentity{}, err
	}
	q := fmt.Sprintf("SELECT id,status,retry_round,COALESCE(%s,'') FROM %s WHERE ingest_step_id=? AND ingest_run_id=? AND generation=?", errCol, table)
	rows, err := tx.QueryContext(ctx, q, stepID, runID, generation)
	if err != nil {
		return queueIdentity{}, err
	}
	defer rows.Close()
	var out queueIdentity
	var count int
	for rows.Next() {
		count++
		if count > 1 {
			return queueIdentity{}, fmt.Errorf("publication reopen: ambiguous %s queue identity", family)
		}
		if err := rows.Scan(&out.ID, &out.Status, &out.RetryRound, &out.ErrorText); err != nil {
			return queueIdentity{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return queueIdentity{}, err
	}
	if err := rows.Close(); err != nil {
		return queueIdentity{}, err
	}
	if count != 1 {
		return queueIdentity{}, fmt.Errorf("publication reopen: missing %s queue identity", family)
	}
	out.Family, out.Table = family, table
	return out, nil
}

func reopenQueueErrorColumn(table string) string {
	switch table {
	case "scrape_task":
		return "message"
	case "transcode_task":
		return "error_message"
	default:
		return "last_error"
	}
}

// reopenQueueResetFragment mirrors syncSkippedQueueForStepTx / RetryOptionalPrepare:
// prepare (transcode_task) uses completed_at and has no available_at; other families
// clear finished_at and bump available_at.
func reopenQueueResetFragment(table string) (finishedCol, availableClause string) {
	if table == "transcode_task" {
		return "completed_at", ""
	}
	return "finished_at", ",available_at=CURRENT_TIMESTAMP"
}

func writeReopenAudit(ctx context.Context, tx store.SQLExecutor, req ReopenRequest, identity queueIdentity, stepType string, previousStatus string, attempts int, previousStepError string, nextRound int) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO media_ingest_optional_retry_audit(media_id,run_id,step_id,task_id,generation,task_family,task_type,actor_id,reason,previous_queue_status,previous_step_status,previous_attempts,previous_queue_error,previous_step_error,retry_round)
SELECT media_id,run_id,id,?,generation,?,?,?,?,?,?,?,?,?,? FROM media_ingest_step WHERE id=? AND run_id=?`,
		identity.ID, identity.Family, stepType, req.ActorID, req.Reason, identity.Status, previousStatus, attempts, identity.ErrorText, previousStepError, nextRound, req.StepID, req.RunID)
	return err
}

func reopenStepAndQueue(ctx context.Context, tx store.SQLExecutor, req ReopenRequest, identity queueIdentity, stepType string, gen int64, status string, attempts int, lastError string, nodeRound int) (int, error) {
	if identity.RetryRound != nodeRound {
		return 0, fmt.Errorf("publication reopen: retry round desync")
	}
	if req.ExpectedRetryRound != nodeRound {
		return 0, fmt.Errorf("publication reopen: retry round fence mismatch")
	}
	nextRound := nodeRound + 1
	if err := writeReopenAudit(ctx, tx, req, identity, stepType, status, attempts, lastError, nextRound); err != nil {
		return 0, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting',retry_round=?,last_error='',lease_owner=NULL,lease_until=NULL,finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND generation=? AND status IN ('failed','cancelled','skipped') AND retry_round=?`, nextRound, req.StepID, req.RunID, gen, nodeRound)
	if err != nil {
		return 0, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return 0, fmt.Errorf("publication reopen: step %d was not reopened", req.StepID)
	}
	errCol := reopenQueueErrorColumn(identity.Table)
	finishedCol, availableClause := reopenQueueResetFragment(identity.Table)
	r, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status='waiting',%s='',retry_round=?,lease_owner=NULL,lease_until=NULL,%s=NULL%s WHERE id=? AND ingest_step_id=? AND ingest_run_id=? AND generation=? AND status IN ('failed','cancelled','skipped') AND retry_round=?`, identity.Table, errCol, finishedCol, availableClause), nextRound, identity.ID, req.StepID, req.RunID, gen, identity.RetryRound)
	if err != nil {
		return 0, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return 0, fmt.Errorf("publication reopen: queue %s/%d was not reopened", identity.Family, identity.ID)
	}
	return nextRound, nil
}

func validateAIReopenPredecessors(ctx context.Context, tx store.SQLExecutor, stepID, runID, gen int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT d.dependency_kind,p.status FROM media_ingest_step_dependency d JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE d.step_id=? AND p.run_id=? AND p.generation=?`, stepID, runID, gen)
	if err != nil {
		return err
	}
	defer rows.Close()
	var saw int
	for rows.Next() {
		saw++
		var kind, status string
		if err := rows.Scan(&kind, &status); err != nil {
			return err
		}
		switch DependencyKind(kind) {
		case DependencySuccess:
			if status != "done" {
				return fmt.Errorf("publication reopen: success predecessor is not done")
			}
		case DependencyTerminal:
			if status != "done" && status != "skipped" && status != "failed" && status != "cancelled" {
				return fmt.Errorf("publication reopen: terminal predecessor is not terminal")
			}
		default:
			return fmt.Errorf("publication reopen: unknown dependency kind %s", kind)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if saw == 0 {
		return fmt.Errorf("publication reopen: ai node has no persisted predecessors")
	}
	return nil
}

func cascadeRecognitionSkippedAI(ctx context.Context, tx store.SQLExecutor, req ReopenRequest, recognitionID, runID, gen int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.status,s.attempts,s.last_error,s.retry_round FROM media_ingest_step s JOIN media_ingest_step_dependency d ON d.step_id=s.id WHERE s.run_id=? AND s.generation=? AND s.step_type='ai_analysis' AND s.status='skipped' AND d.depends_on_step_id=? AND d.dependency_kind='success' ORDER BY s.id`, runID, gen, recognitionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type candidate struct {
		id                   int64
		status, lastError    string
		attempts, retryRound int
	}
	var cs []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.status, &c.attempts, &c.lastError, &c.retryRound); err != nil {
			return err
		}
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, c := range cs {
		var reason impossibleDependencyReason
		if err := json.Unmarshal([]byte(c.lastError), &reason); err != nil {
			continue
		}
		if reason.Code != "dependency_impossible" || reason.PredecessorID != recognitionID || reason.PredecessorType != StepSubtitleRecognize || reason.DependencyKind != DependencySuccess || reason.Generation != gen {
			continue
		}
		if reason.PredecessorState != "failed" && reason.PredecessorState != "cancelled" && reason.PredecessorState != "skipped" {
			continue
		}
		identity, err := resolveQueueIdentity(ctx, tx, c.id, runID, gen, StepAIAnalysis)
		if err != nil {
			return err
		}
		cascadeReq := req
		cascadeReq.StepID = c.id
		cascadeReq.Target = StepAIAnalysis
		cascadeReq.ExpectedRetryRound = c.retryRound
		if _, err := reopenStepAndQueue(ctx, tx, cascadeReq, identity, string(StepAIAnalysis), gen, c.status, c.attempts, c.lastError, c.retryRound); err != nil {
			return err
		}
	}
	return nil
}

func ReopenNodeTx(ctx context.Context, tx store.SQLExecutor, req ReopenRequest) error {
	if tx == nil || req.RunID <= 0 || req.StepID <= 0 || req.ActorID <= 0 || req.Reason == "" {
		return fmt.Errorf("publication reopen: explicit actor and reason are required")
	}
	var gen int64
	var status, stepType, lastError string
	var required, attempts, nodeRound int
	if err := tx.QueryRowContext(ctx, `SELECT generation,step_type,status,required,attempts,last_error,retry_round FROM media_ingest_step WHERE id=? AND run_id=?`, req.StepID, req.RunID).Scan(&gen, &stepType, &status, &required, &attempts, &lastError, &nodeRound); err != nil {
		return err
	}
	if req.Target != "" && req.Target != StepType(stepType) {
		return fmt.Errorf("publication reopen: target mismatch")
	}
	if req.ExpectedGeneration != 0 && req.ExpectedGeneration != gen {
		return fmt.Errorf("publication reopen: generation fence mismatch")
	}
	if required != 0 {
		return fmt.Errorf("publication reopen: required node is not operator-reopenable")
	}
	switch StepType(stepType) {
	case StepPreview, StepSubtitle, StepScrape, StepPrepare, StepSubtitleExtract, StepAtrackExtract, StepSubtitleRecognize, StepKeyframeExtract, StepAIAnalysis:
	default:
		return fmt.Errorf("publication reopen: step %s is not operator-reopenable", stepType)
	}
	if status != "failed" && status != "cancelled" && status != "skipped" {
		return fmt.Errorf("publication reopen: step %d is not terminal", req.StepID)
	}
	if StepType(stepType) == StepAIAnalysis {
		if err := validateAIReopenPredecessors(ctx, tx, req.StepID, req.RunID, gen); err != nil {
			return err
		}
	}
	identity, err := resolveQueueIdentity(ctx, tx, req.StepID, req.RunID, gen, StepType(stepType))
	if err != nil {
		return err
	}
	if _, err := reopenStepAndQueue(ctx, tx, req, identity, stepType, gen, status, attempts, lastError, nodeRound); err != nil {
		return err
	}
	if StepType(stepType) == StepSubtitleRecognize {
		if err := cascadeRecognitionSkippedAI(ctx, tx, req, req.StepID, req.RunID, gen); err != nil {
			return err
		}
	}
	return FinalizeNodeTransitionTx(ctx, tx, req.RunID)
}
