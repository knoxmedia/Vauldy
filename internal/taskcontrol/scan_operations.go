package taskcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"knox-media/internal/scancoord"
	"knox-media/internal/store"
)

// ScanCoordinator is the lifecycle surface needed by ScanTaskController.
type ScanCoordinator interface {
	Submit(context.Context, scancoord.ScanRequest) (scancoord.SubmitResult, error)
	Cancel(context.Context, int64) (scancoord.CancelResult, error)
}

// ScanTaskController owns safe lifecycle controls for scan_task rows.
type ScanTaskController struct {
	db          *sql.DB
	coordinator ScanCoordinator
}

func NewScanTaskController(db *sql.DB, coordinator ScanCoordinator) *ScanTaskController {
	return &ScanTaskController{db: db, coordinator: coordinator}
}

func invalidScanOperation(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidOperation, msg) }

func (c *ScanTaskController) Abort(ctx context.Context, req ExternalOperationRequest) error {
	if c == nil || c.coordinator == nil {
		return invalidScanOperation("scan controller unavailable")
	}
	_, err := c.coordinator.Cancel(ctx, req.ID)
	return err
}

// Reset preserves the terminal row and submits a new manual scan for the same library.
// Submit de-duplicates against an active scan for that library, making retries safe.
func (c *ScanTaskController) Reset(ctx context.Context, req ExternalOperationRequest) error {
	if c == nil || c.db == nil || c.coordinator == nil {
		return invalidScanOperation("scan controller unavailable")
	}
	var libraryID int64
	var status, fallbackPath string
	err := c.db.QueryRowContext(ctx, `SELECT t.library_id,CASE WHEN COALESCE(t.cancelled,0)=1 THEN 'cancelled' ELSE COALESCE(t.status,'waiting') END,COALESCE(l.path,'') FROM scan_task t JOIN library l ON l.id=t.library_id WHERE t.id=?`, req.ID).Scan(&libraryID, &status, &fallbackPath)
	if errors.Is(err, sql.ErrNoRows) {
		return invalidScanOperation("scan task missing")
	}
	if err != nil {
		return err
	}
	if status != "failed" && status != "cancelled" {
		return invalidScanOperation("only failed or cancelled scan tasks can be reset")
	}
	roots, err := c.libraryRoots(ctx, libraryID, fallbackPath)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return invalidScanOperation("library has no scan folders")
	}
	result, err := c.coordinator.Submit(ctx, scancoord.ScanRequest{LibraryID: libraryID, Source: scancoord.SourceManual, Roots: roots})
	if err != nil {
		return err
	}
	newID := result.TaskID
	outcome := "new"
	if result.ExistingTaskID > 0 {
		newID = result.ExistingTaskID
		outcome = "existing"
	}
	auditReason := strings.TrimSpace(req.Reason)
	if auditReason != "" {
		auditReason += "; "
	}
	auditReason += fmt.Sprintf("%s scan_task:%d", outcome, newID)
	if _, auditErr := c.db.ExecContext(ctx, `INSERT INTO task_control_audit(task_identity,task_type,actor_id,action,reason,previous_status,new_status,outcome_code,metadata_json) VALUES(?,'scan',?,'reset',?,?,'submitted',?,json_object('scan_task_id',?,'submit_outcome',?))`, req.Identity, req.ActorID, auditReason, status, outcome, newID, outcome); auditErr != nil {
		// The scan has already been submitted. Reporting failure here would invite a
		// duplicate retry; Coordinator.Submit still de-duplicates active scans.
		log.Printf("task control: audit scan reset %s -> scan_task:%d: %v", req.Identity, newID, auditErr)
	}
	return nil
}

func (c *ScanTaskController) libraryRoots(ctx context.Context, libraryID int64, fallbackPath string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT path FROM library_folder WHERE library_id=? ORDER BY sort_order,id`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	var roots []string
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if !raw.Valid || strings.TrimSpace(raw.String) == "" {
			continue
		}
		root := filepath.Clean(strings.TrimSpace(raw.String))
		if _, ok := seen[root]; !ok {
			seen[root] = struct{}{}
			roots = append(roots, root)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(roots) == 0 && strings.TrimSpace(fallbackPath) != "" {
		roots = append(roots, filepath.Clean(strings.TrimSpace(fallbackPath)))
	}
	return roots, nil
}

func (c *ScanTaskController) Remove(ctx context.Context, req ExternalOperationRequest) error {
	if c == nil || c.db == nil {
		return invalidScanOperation("scan controller unavailable")
	}
	_, err := store.WithImmediateConnTx(ctx, c.db, func(tx store.ImmediateConnTx) error {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN COALESCE(cancelled,0)=1 THEN 'cancelled' ELSE COALESCE(status,'waiting') END FROM scan_task WHERE id=?`, req.ID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return invalidScanOperation("scan task missing")
		}
		if err != nil {
			return err
		}
		if status != "done" && status != "failed" && status != "cancelled" {
			return invalidScanOperation("only terminal scan tasks can be removed")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_control_audit(task_identity,task_type,actor_id,action,reason,previous_status,new_status) VALUES(?,'scan',?,'remove',?,?,'removed')`, req.Identity, req.ActorID, req.Reason, status); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scan_log SET scan_task_id=NULL WHERE scan_task_id=?`, req.ID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM scan_task WHERE id=? AND (status IN ('done','failed','cancelled') OR COALESCE(cancelled,0)=1)`, req.ID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return invalidScanOperation("scan task changed concurrently")
		}
		return nil
	})
	return err
}

func ScanAbortAction(row *ProjectionRow) bool {
	return row != nil && row.RawStatus == "running"
}

func ScanResetAction(row *ProjectionRow) bool {
	return row != nil && (row.RawStatus == "failed" || row.RawStatus == "cancelled")
}

func ScanRemoveAction(row *ProjectionRow) bool {
	return row != nil && (row.RawStatus == "done" || row.RawStatus == "failed" || row.RawStatus == "cancelled")
}
