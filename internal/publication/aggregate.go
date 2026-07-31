package publication

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"knox-media/internal/store"
)

const (
	maxPublicationErrorBytes = 1500
	publicationErrorEllipsis = "..."
	requiredStepFallback     = "required step exhausted"
)

type requiredStepDiagnostic struct {
	stepType string
	status   string
	detail   string
	id       int64
}

func requiredDiagnostics(ctx context.Context, tx store.SQLExecutor, runID int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT step_type,status,last_error,id FROM media_ingest_step WHERE run_id=? AND required=1 AND status IN ('failed','cancelled') ORDER BY CASE step_type WHEN 'poster' THEN 1 WHEN 'thumbnail' THEN 2 WHEN 'encrypt' THEN 3 ELSE 4 END,id`, runID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	diagnostics := make([]requiredStepDiagnostic, 0, 3)
	for rows.Next() {
		var diagnostic requiredStepDiagnostic
		if err := rows.Scan(&diagnostic.stepType, &diagnostic.status, &diagnostic.detail, &diagnostic.id); err != nil {
			return "", err
		}
		if diagnostic.detail != "" {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(diagnostics) == 0 {
		return requiredStepFallback, nil
	}

	parts := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		parts = append(parts, diagnostic.stepType+": "+diagnostic.detail)
	}
	return truncatePublicationError(strings.Join(parts, "; ")), nil
}

func truncatePublicationError(message string) string {
	if len(message) <= maxPublicationErrorBytes {
		return message
	}
	limit := maxPublicationErrorBytes - len(publicationErrorEllipsis)
	for limit > 0 && !utf8.RuneStart(message[limit]) {
		limit--
	}
	return message[:limit] + publicationErrorEllipsis
}

// AggregateTx reconciles a run and, when it is current, its media visibility.
func AggregateTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication aggregate: invalid transaction or run")
	}
	var mediaID, generation int64
	var preserve int
	var runState, terminalReason, runError, reason string
	var supersededBy sql.NullInt64
	var supersededAt, finishedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT media_id,generation,status,preserve_visibility,terminal_reason,error_message,reason,superseded_by_generation,superseded_at,finished_at FROM media_ingest_run WHERE id=?`, runID).Scan(&mediaID, &generation, &runState, &preserve, &terminalReason, &runError, &reason, &supersededBy, &supersededAt, &finishedAt); err != nil {
		return err
	}
	if supersededBy.Valid || supersededAt.Valid {
		return nil
	}
	if finishedAt.Valid && (runState == "published" || runState == "degraded") {
		return nil
	}
	var waiting, running, failed, cancelled int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='waiting'),0),COALESCE(SUM(status='running'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status='cancelled'),0) FROM media_ingest_step WHERE run_id=? AND required=1`, runID).Scan(&waiting, &running, &failed, &cancelled); err != nil {
		return err
	}
	next := runState
	switch {
	case runState == "cancelled":
		next = "cancelled"
	case runState == "failed":
		next = "failed"
	case running > 0:
		// Keep processing while any required step is actively leased.
		next = "processing"
	case failed > 0 || cancelled > 0:
		// A terminal required failure must not be held open by sibling required
		// steps that are still waiting (typically blocked on the failed dependency).
		if err := cancelBlockedRequiredWaiting(ctx, tx, runID); err != nil {
			return err
		}
		if preserve == 1 {
			next = "degraded"
		} else {
			next = "failed"
		}
	case waiting > 0:
		next = "processing"
	default:
		next = "published"
	}

	diagnostic := ""
	if next == "degraded" || next == "failed" {
		var err error
		diagnostic, err = requiredDiagnostics(ctx, tx, runID)
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_ingest_run SET status=?,error_message=CASE WHEN ?='cancelled' THEN error_message WHEN ? IN ('degraded','failed') THEN ? ELSE '' END,finished_at=CASE WHEN ? IN ('published','degraded','failed','cancelled') THEN COALESCE(finished_at,CURRENT_TIMESTAMP) ELSE NULL END WHERE id=?`, next, next, next, diagnostic, next, runID); err != nil {
		return err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&current); err != nil {
		return err
	}
	if current != generation {
		return nil
	}
	if next == "processing" && preserve == 1 {
		return nil
	}
	if next == "published" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='published',published_at=COALESCE(published_at,CURRENT_TIMESTAMP),publication_error='' WHERE id=?`, mediaID)
		return err
	}
	if next == "degraded" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='degraded',publication_error=? WHERE id=?`, diagnostic, mediaID)
		return err
	}
	if next == "cancelled" {
		if preserve == 1 {
			_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='degraded',publication_error=COALESCE(NULLIF(?,''),'cancelled') WHERE id=?`, terminalReason, mediaID)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='cancelled',published_at=NULL,publication_error=COALESCE(NULLIF(?,''),'cancelled') WHERE id=?`, terminalReason, mediaID)
		return err
	}
	if next == "failed" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='failed',published_at=CASE WHEN ?='repair' THEN published_at ELSE NULL END,publication_error=? WHERE id=?`, reason, diagnostic, mediaID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='processing',publication_error='' WHERE id=?`, mediaID)
	return err
}

const blockedRequiredCancelMessage = "cancelled: blocked by required failure"

func cancelBlockedRequiredWaiting(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error=CASE WHEN TRIM(COALESCE(last_error,''))='' THEN ? ELSE last_error END,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND required=1 AND status='waiting'`, blockedRequiredCancelMessage, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error=CASE WHEN TRIM(COALESCE(last_error,''))='' THEN ? ELSE last_error END,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND status IN ('waiting','running') AND ingest_step_id IN (SELECT id FROM media_ingest_step WHERE run_id=? AND required=1 AND status='cancelled')`, blockedRequiredCancelMessage, runID, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scrape_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,message=CASE WHEN TRIM(COALESCE(message,''))='' THEN ? ELSE message END,progress=100,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND status IN ('waiting','running','failed') AND ingest_step_id IN (SELECT id FROM media_ingest_step WHERE run_id=? AND required=1 AND status='cancelled')`, blockedRequiredCancelMessage, runID, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE transcode_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,error_message=CASE WHEN TRIM(COALESCE(error_message,''))='' THEN ? ELSE error_message END,progress=100,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND status IN ('waiting','running') AND ingest_step_id IN (SELECT id FROM media_ingest_step WHERE run_id=? AND required=1 AND status='cancelled')`, blockedRequiredCancelMessage, runID, runID); err != nil {
		return err
	}
	return nil
}
