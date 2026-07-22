package scancoord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"knox-media/internal/store"
)

type finalizeRecovery struct {
	TaskID, LibraryID int64
	Owner, Status     string
	ErrorMessage      *string
	Cancelled         bool
}

func nullableErrorMessage(v any) *string {
	if v == nil {
		return nil
	}
	s := fmt.Sprint(v)
	return &s
}
func boolToDB(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (c *Coordinator) persistFinalizeRecovery(ctx context.Context, r finalizeRecovery) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO scan_finalize_recovery(task_id,library_id,owner_id,desired_status,error_message,cancelled,next_available_at,last_error,updated_at) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP,'',CURRENT_TIMESTAMP) ON CONFLICT(task_id,owner_id) DO UPDATE SET desired_status=excluded.desired_status,error_message=excluded.error_message,cancelled=excluded.cancelled,next_available_at=CURRENT_TIMESTAMP,last_error='',updated_at=CURRENT_TIMESTAMP`, r.TaskID, r.LibraryID, r.Owner, r.Status, r.ErrorMessage, boolToDB(r.Cancelled))
	return err
}
func recoveryKey(r finalizeRecovery) string { return fmt.Sprintf("%d/%s", r.TaskID, r.Owner) }
func (c *Coordinator) signalRecovery() {
	select {
	case c.recoveryWake <- struct{}{}:
	default:
	}
}
func (c *Coordinator) enqueueFinalizeRecovery(r finalizeRecovery) {
	c.recoveryMu.Lock()
	c.recoveryPending[recoveryKey(r)] = r
	if !c.recoveryStarted {
		c.recoveryStarted = true
		c.recoveryWG.Add(1)
		go c.runRecoveryPersistence()
	}
	c.recoveryMu.Unlock()
	c.signalRecovery()
}
func (c *Coordinator) pendingFinalizeRecoveryCount() int {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	return len(c.recoveryPending)
}
func (c *Coordinator) runRecoveryPersistence() {
	defer func() { c.recoveryMu.Lock(); c.recoveryStarted = false; c.recoveryMu.Unlock(); c.recoveryWG.Done() }()
	backoff := 25 * time.Millisecond
	reported := false
	for {
		c.recoveryMu.Lock()
		var key string
		var r finalizeRecovery
		for key, r = range c.recoveryPending {
			break
		}
		stopping := c.recoveryStopping
		empty := len(c.recoveryPending) == 0
		c.recoveryMu.Unlock()
		if empty {
			if stopping {
				return
			}
			select {
			case <-c.recoveryCtx.Done():
				return
			case <-c.recoveryWake:
			}
			continue
		}
		attempt := c.persistRecoveryAttempt
		if attempt == nil {
			attempt = c.persistFinalizeRecovery
		}
		err := attempt(c.recoveryCtx, r)
		if err == nil {
			c.recoveryMu.Lock()
			delete(c.recoveryPending, key)
			c.recoveryMu.Unlock()
			backoff = 25 * time.Millisecond
			reported = false
			continue
		}
		owned, checkErr := c.scanOwnershipExists(c.recoveryCtx, r)
		if checkErr == nil && !owned {
			c.recoveryMu.Lock()
			delete(c.recoveryPending, key)
			c.recoveryMu.Unlock()
			c.reportError(store.WithSQLiteDiagnosticContext(ErrScanLeaseLost, c.db, r.Owner, "scan_finalize_recovery_ownership", 1, 0, store.SQLiteDiagnosticContext{TaskID: r.TaskID, LibraryID: r.LibraryID}))
			continue
		}
		if !reported {
			c.reportError(store.WithSQLiteDiagnosticContext(err, c.db, r.Owner, "scan_finalize_recovery_persist", 1, 0, store.SQLiteDiagnosticContext{TaskID: r.TaskID, LibraryID: r.LibraryID}))
			reported = true
		}
		timer := time.NewTimer(backoff)
		select {
		case <-c.recoveryCtx.Done():
			timer.Stop()
			return
		case <-c.recoveryWake:
			timer.Stop()
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}
func (c *Coordinator) scanOwnershipExists(ctx context.Context, r finalizeRecovery) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scan_task t JOIN scan_lease l ON l.scan_task_id=t.id AND l.library_id=t.library_id WHERE t.id=? AND t.library_id=? AND l.owner_id=?`, r.TaskID, r.LibraryID, r.Owner).Scan(&n)
	return n == 1, err
}

type pendingFinalize struct {
	id, taskID, libraryID int64
	owner, status         string
	errorMessage          sql.NullString
	cancelled             int
}
type ErrFinalizeRecoveryClaimLost struct{ ID int64 }

func (e ErrFinalizeRecoveryClaimLost) Error() string {
	return fmt.Sprintf("finalize recovery claim %d lost", e.ID)
}

func RecoverPendingFinalizations(ctx context.Context, db *sql.DB, limit int) (int, error) {
	recovered := 0
	for i := 0; i < limit; i++ {
		p, token, ok, err := claimOneFinalizeRecovery(ctx, db)
		if err != nil {
			return recovered, err
		}
		if !ok {
			break
		}
		err = finalizeClaimedRecovery(ctx, db, p, token)
		if err == nil {
			recovered++
			continue
		}
		var lost ErrFinalizeRecoveryClaimLost
		if errors.As(err, &lost) {
			return recovered, err
		}
		if errors.Is(err, ErrScanLeaseLost) || isTaskMissing(ctx, db, p.taskID) {
			if err := deleteClaimedRecovery(ctx, db, p.id, token); err != nil {
				return recovered, err
			}
			continue
		}
		if err := backoffClaimedRecovery(ctx, db, p.id, token, err); err != nil {
			return recovered, err
		}
	}
	return recovered, nil
}
func claimOneFinalizeRecovery(ctx context.Context, db *sql.DB) (pendingFinalize, string, bool, error) {
	token := "finalize-recovery/" + uuid.NewString()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return pendingFinalize{}, "", false, err
	}
	defer tx.Rollback()
	var p pendingFinalize
	err = tx.QueryRowContext(ctx, `SELECT id,task_id,library_id,owner_id,desired_status,error_message,cancelled FROM scan_finalize_recovery WHERE next_available_at<=CURRENT_TIMESTAMP AND (claim_until IS NULL OR claim_until<CURRENT_TIMESTAMP) ORDER BY id LIMIT 1`).Scan(&p.id, &p.taskID, &p.libraryID, &p.owner, &p.status, &p.errorMessage, &p.cancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return p, "", false, nil
	}
	if err != nil {
		return p, "", false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE scan_finalize_recovery SET claim_owner=?,claim_until=datetime(CURRENT_TIMESTAMP,'+45 seconds'),updated_at=CURRENT_TIMESTAMP WHERE id=? AND (claim_until IS NULL OR claim_until<CURRENT_TIMESTAMP)`, token, p.id)
	if err != nil {
		return p, "", false, err
	}
	n, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return p, "", false, rowsErr
	}
	if n != 1 {
		return p, "", false, ErrFinalizeRecoveryClaimLost{p.id}
	}
	if err = tx.Commit(); err != nil {
		return p, "", false, err
	}
	return p, token, true, nil
}
func finalizeClaimedRecovery(ctx context.Context, db *sql.DB, p pendingFinalize, token string) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scan_finalize_recovery WHERE id=? AND claim_owner=? AND claim_until>CURRENT_TIMESTAMP`, p.id, token).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrFinalizeRecoveryClaimLost{p.id}
	}
	var msg any
	if p.errorMessage.Valid {
		msg = p.errorMessage.String
	}
	if err := finalizeAndReleaseDB(ctx, db, p.taskID, p.libraryID, p.owner, p.status, msg); err != nil {
		return err
	}
	return deleteClaimedRecovery(ctx, db, p.id, token)
}
func deleteClaimedRecovery(ctx context.Context, db *sql.DB, id int64, token string) error {
	r, e := db.ExecContext(ctx, `DELETE FROM scan_finalize_recovery WHERE id=? AND claim_owner=?`, id, token)
	if e != nil {
		return e
	}
	n, rowsErr := r.RowsAffected()
	if rowsErr != nil {
		return rowsErr
	}
	if n != 1 {
		return ErrFinalizeRecoveryClaimLost{id}
	}
	return nil
}
func backoffClaimedRecovery(ctx context.Context, db *sql.DB, id int64, token string, cause error) error {
	r, e := db.ExecContext(ctx, `UPDATE scan_finalize_recovery SET attempts=attempts+1,next_available_at=datetime(CURRENT_TIMESTAMP,'+'||MIN(60,1<<MIN(attempts,5))||' seconds'),last_error=?,claim_owner=NULL,claim_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND claim_owner=?`, cause.Error(), id, token)
	if e != nil {
		return errors.Join(cause, e)
	}
	n, rowsErr := r.RowsAffected()
	if rowsErr != nil {
		return rowsErr
	}
	if n != 1 {
		return ErrFinalizeRecoveryClaimLost{id}
	}
	return nil
}
func isTaskMissing(ctx context.Context, db *sql.DB, id int64) bool {
	var n int
	return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scan_task WHERE id=?`, id).Scan(&n) == nil && n == 0
}
func finalizeAndReleaseDB(ctx context.Context, db *sql.DB, taskID, libraryID int64, owner, status string, msg any) error {
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := tx.ExecContext(ctx, `UPDATE scan_task SET status=CASE WHEN cancelled=1 OR ?='cancelled' THEN 'cancelled' ELSE ? END,cancelled=CASE WHEN cancelled=1 OR ?='cancelled' THEN 1 ELSE cancelled END,error_message=?,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND EXISTS(SELECT 1 FROM scan_lease WHERE library_id=? AND scan_task_id=? AND owner_id=?)`, status, status, status, msg, taskID, libraryID, taskID, owner)
	if e != nil {
		return e
	}
	n, rowsErr := r.RowsAffected()
	if rowsErr != nil {
		return rowsErr
	}
	if n != 1 {
		return ErrScanLeaseLost
	}
	r, e = tx.ExecContext(ctx, `DELETE FROM scan_lease WHERE library_id=? AND scan_task_id=? AND owner_id=?`, libraryID, taskID, owner)
	if e != nil {
		return e
	}
	n, rowsErr = r.RowsAffected()
	if rowsErr != nil {
		return rowsErr
	}
	if n != 1 {
		return ErrScanLeaseLost
	}
	return tx.Commit()
}
