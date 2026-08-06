package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"knox-media/internal/store"
)

// ErrReservationNotActive is returned when releasing a reservation that is not
// currently active (already released or absent). Queue lifecycle transitions
// treat it as a successful no-op so the queue row still transitions.
var ErrReservationNotActive = errors.New("reservation not active")

// ReleaseReservationTx marks a reservation as released with evidence inside an
// open transaction. Uses WHERE execution_id=? AND status='active' to guarantee
// exactly-once semantics. Returns an error if the reservation was already
// released (rows_affected == 0).
func ReleaseReservationTx(ctx context.Context, tx store.SQLExecutor, executionID, reason, releasedBy string) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE scheduler_reservation SET status='released', released_at=CURRENT_TIMESTAMP, release_reason=?, released_by=?, updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='active'`,
		reason, releasedBy, executionID)
	if err != nil {
		return fmt.Errorf("release reservation: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrReservationNotActive, executionID)
	}
	return nil
}

// RenewReservationTx extends the lease of an active reservation inside an open
// transaction. A reservation that is already released or absent is a successful
// no-op: the same execution unit may legitimately hold no reservation (e.g. a
// legacy claim or a GPU-fallback fence). This mirrors ReleaseReservationTx's
// exactly-once semantics so periodic task heartbeats can keep the reservation
// alive in lockstep with the queue row.
func RenewReservationTx(ctx context.Context, tx store.SQLExecutor, executionID string, leaseTTL time.Duration) error {
	if strings.TrimSpace(executionID) == "" {
		return nil
	}
	if leaseTTL <= 0 {
		leaseTTL = 90 * time.Second
	}
	// Bind the lease deadline as a Go time value so it serializes in the same
	// format admission uses (InsertAdmissionReservation). Using SQLite's
	// datetime(CURRENT_TIMESTAMP,...) would store "YYYY-MM-DD HH:MM:SS" (UTC,
	// space separator) while admission writes the driver's Go time format; the
	// admission budget checks compare lease_until > ? as strings, so mixing
	// formats makes renewed reservations look expired and silently disables
	// concurrency limits.
	renewUntil := time.Now().Add(leaseTTL)
	result, err := tx.ExecContext(ctx,
		`UPDATE scheduler_reservation SET lease_until=?, updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='active'`,
		renewUntil, executionID)
	if err != nil {
		return fmt.Errorf("renew reservation: %w", err)
	}
	if _, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("renew reservation rows: %w", err)
	}
	return nil
}

// RenewReservationDirect extends the lease of an active reservation using the
// database connection directly (outside an open transaction). Already-released
// or absent reservations are a successful no-op, mirroring
// ReleaseReservationTx's exactly-once semantics.
func RenewReservationDirect(ctx context.Context, db *sql.DB, executionID string, leaseTTL time.Duration) error {
	if strings.TrimSpace(executionID) == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := RenewReservationTx(ctx, tx, executionID, leaseTTL); err != nil {
		return err
	}
	return tx.Commit()
}

// GetReservationTx returns a reservation by execution id within an open
// transaction.
func GetReservationTx(ctx context.Context, tx store.SQLExecutor, executionID string) (*Reservation, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id,execution_id,task_type,reserved_units,policy_revision_id,status,lease_until,released_at,release_reason,released_by,created_at,updated_at FROM scheduler_reservation WHERE execution_id=?`, executionID)
	var r Reservation
	var lease, released sql.NullTime
	if err := row.Scan(&r.ID, &r.ExecutionID, &r.TaskType, &r.ReservedUnits, &r.PolicyRevisionID,
		&r.Status, &lease, &released, &r.ReleaseReason, &r.ReleasedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan reservation: %w", err)
	}
	if lease.Valid {
		r.LeaseUntil = &lease.Time
	}
	if released.Valid {
		r.ReleasedAt = &released.Time
	}
	return &r, nil
}

// ActiveReservationCountTx returns the count of unreleased non-expired
// reservations for a task type visible through tx.
func ActiveReservationCountTx(ctx context.Context, tx store.SQLExecutor, taskType string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduler_reservation WHERE task_type=? AND status='active'`,
		taskType).Scan(&count)
	return count, err
}
