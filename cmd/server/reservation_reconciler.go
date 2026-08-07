package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	"knox-media/internal/scheduler"
)

// ReconcileStartupReservations releases all active scheduler reservations from
// a previous process. Every reservation is released because:
// - Its lease has expired (standard crash recovery), or
// - It has no lease (orphan: created but never properly acquired).
// Returns the number of reservations released.
func ReconcileStartupReservations(ctx context.Context, db *sql.DB, owner string) (int64, error) {
	now := time.Now()
	rows, err := db.QueryContext(ctx,
		`SELECT execution_id, lease_until FROM scheduler_reservation WHERE status='active'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var toRelease []string
	for rows.Next() {
		var execID string
		var leaseUntil *time.Time
		if err := rows.Scan(&execID, &leaseUntil); err != nil {
			return 0, err
		}
		if leaseUntil == nil || !leaseUntil.After(now) {
			toRelease = append(toRelease, execID)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var released int64
	for _, execID := range toRelease {
		result, err := db.ExecContext(ctx,
			`UPDATE scheduler_reservation SET status='released', released_at=CURRENT_TIMESTAMP, release_reason='startup_reconciliation', released_by=?, updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='active'`,
			owner, execID)
		if err != nil {
			return released, err
		}
		n, _ := result.RowsAffected()
		released += n
	}
	if released > 0 {
		log.Printf("startup reservation reconciliation: released %d expired/orphan reservations", released)
	}
	return released, nil
}

// ReservationExpiryReconciler periodically releases expired scheduler
// reservations. It is intended to be run under the BackgroundGroup so shutdown
// waits for the final reconciler cycle.
type ReservationExpiryReconciler struct {
	db *sql.DB
}

// NewReservationExpiryReconciler creates a reconciler that releases expired
// reservations when RunOnce is called.
func NewReservationExpiryReconciler(db *sql.DB) *ReservationExpiryReconciler {
	return &ReservationExpiryReconciler{db: db}
}

// RunOnce releases all active reservations whose lease has expired. It is safe
// to call from multiple goroutines.
func (r *ReservationExpiryReconciler) RunOnce(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	now := time.Now()
	rows, err := r.db.QueryContext(ctx,
		`SELECT execution_id, lease_until FROM scheduler_reservation WHERE status='active'`)
	if err != nil {
		log.Printf("reservation expiry reconciliation: query: %v", err)
		return
	}
	defer rows.Close()

	var toRelease []string
	for rows.Next() {
		var execID string
		var leaseUntil *time.Time
		if err := rows.Scan(&execID, &leaseUntil); err != nil {
			log.Printf("reservation expiry reconciliation: scan: %v", err)
			return
		}
		if leaseUntil != nil && !leaseUntil.After(now) {
			toRelease = append(toRelease, execID)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("reservation expiry reconciliation: rows: %v", err)
		return
	}

	var released int64
	for _, execID := range toRelease {
		result, err := r.db.ExecContext(ctx,
			`UPDATE scheduler_reservation SET status='released', released_at=CURRENT_TIMESTAMP, release_reason='lease_expired', released_by='system', updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='active'`,
			execID)
		if err != nil {
			log.Printf("reservation expiry reconciliation: release: %v", err)
			continue
		}
		n, _ := result.RowsAffected()
		released += n
	}
	if released > 0 {
		log.Printf("reservation expiry reconciliation: released %d expired reservations", released)
	}
}

// StartReservationExpiryReconciler runs the reconciler periodically under a
// context-driven loop. It is intended to be registered via BackgroundGroup.Go.
func StartReservationExpiryReconciler(ctx context.Context, db *sql.DB, interval time.Duration) {
	reconciler := NewReservationExpiryReconciler(db)
	// Run once immediately to clean up any expired reservations at startup.
	reconciler.RunOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconciler.RunOnce(ctx)
		}
	}
}

// buildSchedulerPolicyFromDefaults constructs the effective scheduler policy
// from compiled defaults merged with validated config. Returns an error when
// the merge product is invalid.
func buildSchedulerPolicyFromDefaults(cfg scheduler.SchedulerYAMLConfig) (scheduler.Policy, error) {
	p := scheduler.PolicyDefaults()
	p.MergeYAML(cfg)
	if err := p.Validate(); err != nil {
		return scheduler.Policy{}, err
	}
	return p, nil
}
