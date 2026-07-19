package scancoord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"knox-media/internal/scanner"
	"knox-media/internal/store"
)

type Source string

var ErrScanLeaseLost = errors.New("scan lease lost")

const (
	SourceManual    Source = "manual"
	SourceScheduled Source = "scheduled"
	SourceMonitor   Source = "monitor"
)

type ScanRequest struct {
	LibraryID int64
	Source    Source
	Roots     []string
}

type CancelResult struct {
	Cancelled bool
	Status    string
}

type SubmitResult struct {
	TaskID         int64
	ExistingTaskID int64
	Started        bool
}

type Scanner interface {
	ScanLibraryFoldersWithContextAndCallbacks(context.Context, int64, []string, scanner.ScanCallbacks) (int, error)
}

type MediaAddedFunc func(context.Context, int64, int64, string, string) error

type ScanCancelledFunc func(context.Context, int64) error

type Options struct {
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	FinalizeTimeout   time.Duration
	OwnerInstanceID   string
	Scanner           Scanner
	OnMediaAdded      MediaAddedFunc
	OnScanCancelled   ScanCancelledFunc
	Metrics           *store.SQLiteMetrics
	OnError           func(error)
}

type Coordinator struct {
	db                *sql.DB
	leaseDuration     time.Duration
	heartbeatInterval time.Duration
	finalizeTimeout   time.Duration
	ownerInstanceID   string
	scanner           Scanner
	onMediaAdded      MediaAddedFunc
	onScanCancelled   ScanCancelledFunc
	metrics           *store.SQLiteMetrics
	onError           func(error)
	readCancelled     func(context.Context, int64) (int, error)
	// afterSubmitCommit is an internal synchronization seam used by same-package tests.
	afterSubmitCommit func()
	// afterCancelCommit is an internal synchronization seam used by same-package tests.
	afterCancelCommit func()

	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
	wg      sync.WaitGroup
}

func New(db *sql.DB, opts Options) (*Coordinator, error) {
	if db == nil {
		return nil, errors.New("scancoord: db is required")
	}
	if strings.TrimSpace(opts.OwnerInstanceID) == "" {
		return nil, errors.New("scancoord: OwnerInstanceID is required")
	}
	if opts.Scanner == nil {
		return nil, errors.New("scancoord: Scanner is required")
	}
	if opts.LeaseDuration == 0 {
		opts.LeaseDuration = 60 * time.Second
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = 20 * time.Second
	}
	if opts.FinalizeTimeout == 0 {
		opts.FinalizeTimeout = 10 * time.Second
	}
	if opts.FinalizeTimeout < 0 {
		return nil, errors.New("scancoord: FinalizeTimeout must be positive")
	}
	if opts.LeaseDuration < time.Second || opts.LeaseDuration%time.Second != 0 {
		return nil, errors.New("scancoord: LeaseDuration must be at least one second and use whole seconds")
	}
	if opts.HeartbeatInterval < time.Second || opts.HeartbeatInterval%time.Second != 0 {
		return nil, errors.New("scancoord: HeartbeatInterval must be at least one second and use whole seconds")
	}
	if opts.HeartbeatInterval >= opts.LeaseDuration {
		return nil, errors.New("scancoord: HeartbeatInterval must be less than LeaseDuration")
	}
	return &Coordinator{
		db:                db,
		leaseDuration:     opts.LeaseDuration,
		heartbeatInterval: opts.HeartbeatInterval,
		finalizeTimeout:   opts.FinalizeTimeout,
		ownerInstanceID:   opts.OwnerInstanceID,
		scanner:           opts.Scanner,
		onMediaAdded:      opts.OnMediaAdded,
		onScanCancelled:   opts.OnScanCancelled,
		metrics:           opts.Metrics,
		onError:           opts.OnError,
		cancels:           make(map[int64]context.CancelFunc),
	}, nil
}

func (c *Coordinator) Submit(ctx context.Context, req ScanRequest) (SubmitResult, error) {
	if err := validateRequest(req); err != nil {
		return SubmitResult{}, err
	}

	var result SubmitResult
	var owner string
	err := store.WithBusyRetry(ctx, c.metrics, func() error {
		result = SubmitResult{}
		owner = ""
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		insert, err := tx.ExecContext(ctx, `
			INSERT INTO scan_task (library_id, status, source, started_at, updated_at)
			VALUES (?, 'waiting', ?, NULL, CURRENT_TIMESTAMP)`, req.LibraryID, req.Source)
		if err != nil {
			return err
		}
		taskID, err := insert.LastInsertId()
		if err != nil {
			return err
		}
		result.TaskID = taskID
		owner = fmt.Sprintf("%s/%d/%s", c.ownerInstanceID, taskID, uuid.NewString())
		modifier := fmt.Sprintf("+%d seconds", int64(c.leaseDuration/time.Second))

		var previousTaskID int64
		var previousOwner string
		var previousExpired bool
		err = tx.QueryRowContext(ctx, `
			SELECT scan_task_id, owner_id, lease_until < CURRENT_TIMESTAMP
			FROM scan_lease WHERE library_id=?`, req.LibraryID).Scan(&previousTaskID, &previousOwner, &previousExpired)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && previousExpired && previousTaskID != taskID {
			if _, err := tx.ExecContext(ctx, `
				UPDATE scan_task SET
					status=CASE WHEN cancelled=1 THEN 'cancelled' ELSE 'failed' END,
					error_message='scan lease expired and was taken over',
					finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
				WHERE id=? AND status='running'`, previousTaskID); err != nil {
				return fmt.Errorf("scancoord: finalize expired lease owner %q task %d: %w", previousOwner, previousTaskID, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO scan_lease (library_id, scan_task_id, owner_id, lease_until)
			VALUES (?, ?, ?, datetime(CURRENT_TIMESTAMP, ?))
			ON CONFLICT(library_id) DO UPDATE SET
				scan_task_id=excluded.scan_task_id,
				owner_id=excluded.owner_id,
				lease_until=excluded.lease_until,
				updated_at=CURRENT_TIMESTAMP
			WHERE scan_lease.lease_until < CURRENT_TIMESTAMP`, req.LibraryID, taskID, owner, modifier); err != nil {
			return err
		}

		var acquired int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM scan_lease
			WHERE library_id=? AND scan_task_id=? AND owner_id=?`, req.LibraryID, taskID, owner).Scan(&acquired); err != nil {
			return err
		}
		if acquired == 1 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE scan_task SET status='running', started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
				WHERE id=?`, taskID); err != nil {
				return err
			}
			result.Started = true
		} else {
			if err := tx.QueryRowContext(ctx, `SELECT scan_task_id FROM scan_lease WHERE library_id=?`, req.LibraryID).Scan(&result.ExistingTaskID); err != nil {
				return err
			}
			message := fmt.Sprintf("concurrent scan; current task %d", result.ExistingTaskID)
			if _, err := tx.ExecContext(ctx, `
				UPDATE scan_task SET status='cancelled', cancelled=1, finished_at=CURRENT_TIMESTAMP,
					error_message=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, message, taskID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil {
		return SubmitResult{}, err
	}
	if result.Started {
		if c.afterSubmitCommit != nil {
			c.afterSubmitCommit()
		}
		runCtx, cancel := context.WithCancel(context.Background())
		c.mu.Lock()
		c.cancels[result.TaskID] = cancel
		c.wg.Add(1)
		c.mu.Unlock()
		go func() {
			defer c.wg.Done()
			c.run(runCtx, result.TaskID, req.LibraryID, owner, append([]string(nil), req.Roots...))
		}()
	}
	return result, nil
}

func validateRequest(req ScanRequest) error {
	if req.LibraryID <= 0 {
		return errors.New("scancoord: LibraryID must be positive")
	}
	switch req.Source {
	case SourceManual, SourceScheduled, SourceMonitor:
	default:
		return fmt.Errorf("scancoord: invalid source %q", req.Source)
	}
	if len(req.Roots) == 0 {
		return errors.New("scancoord: Roots must not be empty")
	}
	return nil
}

func (c *Coordinator) run(ctx context.Context, taskID, libraryID int64, owner string, roots []string) {
	defer func() {
		c.mu.Lock()
		delete(c.cancels, taskID)
		c.mu.Unlock()
	}()

	persistedCancelled, readErr := c.readCancellation(ctx, taskID)
	var scanErr error
	if readErr != nil {
		scanErr = fmt.Errorf("check cancellation: %w", readErr)
		c.reportError(fmt.Errorf("scancoord: check cancellation for task %d: %w", taskID, readErr))
	} else if persistedCancelled == 1 || ctx.Err() != nil {
		scanErr = context.Canceled
	} else {
		scanCtx, cancelScan := context.WithCancel(ctx)
		result := make(chan error, 1)
		progress := NewProgressWriter(c.db, c.metrics, taskID, libraryID, ProgressWriterOptions{})
		go func() {
			var enqueueMu sync.Mutex
			var enqueueErrors []error
			callbacks := scanner.ScanCallbacks{
				OnFile: progress.File,
				OnMediaAdded: func(callbackCtx context.Context, mediaID int64, title, fileType string) error {
					progress.MediaAdded(mediaID, title, fileType)
					if c.onMediaAdded == nil {
						return nil
					}
					if err := c.onMediaAdded(callbackCtx, taskID, mediaID, title, fileType); err != nil {
						enqueueErr := fmt.Errorf("enqueue media %d: %w", mediaID, err)
						enqueueMu.Lock()
						enqueueErrors = append(enqueueErrors, enqueueErr)
						enqueueMu.Unlock()
						progress.Log(ScanLog{FilePath: fmt.Sprintf("media:%d", mediaID), Action: "enqueue_error", Message: err.Error()})
						c.reportError(fmt.Errorf("scancoord: task %d %w", taskID, enqueueErr))
					}
					return nil
				},
			}
			_, err := c.scanner.ScanLibraryFoldersWithContextAndCallbacks(scanCtx, libraryID, roots, callbacks)
			enqueueMu.Lock()
			err = errors.Join(err, errors.Join(enqueueErrors...))
			enqueueMu.Unlock()
			flushCtx, flushCancel := context.WithTimeout(context.Background(), c.finalizeTimeout)
			if flushErr := progress.Flush(flushCtx, true); flushErr != nil {
				progressErr := fmt.Errorf("progress flush: %w", flushErr)
				c.reportError(fmt.Errorf("scancoord: force progress flush task %d: %w", taskID, flushErr))
				err = errors.Join(err, progressErr)
			}
			flushCancel()
			result <- err
		}()

		ticker := time.NewTicker(c.heartbeatInterval)
		var heartbeatErr error
		waiting := true
		for waiting {
			select {
			case scanErr = <-result:
				waiting = false
			case <-ticker.C:
				cancelled, err := c.readCancellation(scanCtx, taskID)
				if err != nil {
					heartbeatErr = fmt.Errorf("scan cancellation heartbeat: %w", err)
				} else if cancelled == 1 {
					cancelScan()
					scanErr = <-result
					waiting = false
					continue
				} else {
					renewed, renewErr := c.renewLease(scanCtx, libraryID, taskID, owner)
					if renewErr != nil {
						heartbeatErr = fmt.Errorf("scan lease heartbeat: %w", renewErr)
					} else if !renewed {
						heartbeatErr = ErrScanLeaseLost
					}
				}
				if heartbeatErr != nil {
					c.reportError(fmt.Errorf("scancoord: task %d: %w", taskID, heartbeatErr))
					cancelScan()
					scanErr = <-result
					waiting = false
				}
			case <-ctx.Done():
				cancelScan()
				scanErr = <-result
				waiting = false
			}
		}
		ticker.Stop()
		cancelScan()
		if heartbeatErr != nil {
			scanErr = heartbeatErr
		}
	}

	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), c.finalizeTimeout)
	defer finalizeCancel()

	status := "done"
	var errorMessage any
	finalCancelled, finalReadErr := c.readCancellation(finalizeCtx, taskID)
	if finalReadErr != nil {
		scanErr = fmt.Errorf("check final cancellation: %w", finalReadErr)
		c.reportError(fmt.Errorf("scancoord: check final cancellation for task %d: %w", taskID, finalReadErr))
	}
	if finalCancelled == 1 {
		status = "cancelled"
		if scanErr != nil {
			errorMessage = scanErr.Error()
		}
	} else if scanErr != nil {
		status = "failed"
		errorMessage = scanErr.Error()
	}
	if err := c.finalizeAndRelease(finalizeCtx, taskID, libraryID, owner, status, errorMessage); err != nil {
		c.reportError(fmt.Errorf("scancoord: finalize task %d: %w", taskID, err))
	}
}

func (c *Coordinator) readCancellation(ctx context.Context, taskID int64) (int, error) {
	if c.readCancelled != nil {
		return c.readCancelled(ctx, taskID)
	}
	var cancelled int
	err := c.db.QueryRowContext(ctx, `SELECT cancelled FROM scan_task WHERE id=?`, taskID).Scan(&cancelled)
	return cancelled, err
}
func (c *Coordinator) reportError(err error) {
	if c.onError != nil {
		c.onError(err)
	}
}

func (c *Coordinator) finalizeAndRelease(ctx context.Context, taskID, libraryID int64, owner, status string, errorMessage any) error {
	return store.WithBusyRetry(ctx, c.metrics, func() error {
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		result, err := tx.ExecContext(ctx, `
			UPDATE scan_task SET
				status=CASE WHEN cancelled=1 OR ?='cancelled' THEN 'cancelled' ELSE ? END,
				cancelled=CASE WHEN cancelled=1 OR ?='cancelled' THEN 1 ELSE cancelled END,
				error_message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND EXISTS (
				SELECT 1 FROM scan_lease
				WHERE library_id=? AND scan_task_id=? AND owner_id=?
			)`, status, status, status, errorMessage, taskID, libraryID, taskID, owner)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrScanLeaseLost
		}
		result, err = tx.ExecContext(ctx, `DELETE FROM scan_lease WHERE library_id=? AND scan_task_id=? AND owner_id=?`, libraryID, taskID, owner)
		if err != nil {
			return err
		}
		rows, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrScanLeaseLost
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}

// Shutdown cancels every scan currently owned by this process.
func (c *Coordinator) Shutdown() {
	_ = c.ShutdownContext(context.Background())
}

// ShutdownContext cancels local scans and waits for their database finalization.
func (c *Coordinator) ShutdownContext(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.cancels))
	for _, cancel := range c.cancels {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) Cancel(ctx context.Context, taskID int64) (CancelResult, error) {
	if taskID <= 0 {
		return CancelResult{}, errors.New("scancoord: taskID must be positive")
	}
	result := CancelResult{}
	updated := false
	if err := store.WithBusyRetry(ctx, c.metrics, func() error {
		result = CancelResult{}
		updated = false
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		updateResult, err := tx.ExecContext(ctx, `
			UPDATE scan_task SET
				cancelled=1,
				status=CASE WHEN status='waiting' THEN 'cancelled' ELSE status END,
				finished_at=CASE WHEN status='waiting' THEN CURRENT_TIMESTAMP ELSE finished_at END,
				updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND cancelled=0 AND status IN ('waiting','running')`, taskID)
		if err != nil {
			return err
		}
		rows, err := updateResult.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			var status string
			var cancelled int
			if err := tx.QueryRowContext(ctx, `SELECT status,cancelled FROM scan_task WHERE id=?`, taskID).Scan(&status, &cancelled); err != nil {
				return err
			}
			switch status {
			case "done", "failed", "cancelled", "running":
				if status == "running" && cancelled == 0 {
					return fmt.Errorf("scancoord: task %d cannot be cancelled from status %q", taskID, status)
				}
				result.Status = status
				if status == "running" {
					result.Status = "cancelling"
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				committed = true
				return nil
			default:
				return fmt.Errorf("scancoord: task %d cannot be cancelled from status %q", taskID, status)
			}
		}
		result.Cancelled = true
		if err := tx.QueryRowContext(ctx, `SELECT status FROM scan_task WHERE id=?`, taskID).Scan(&result.Status); err != nil {
			return err
		}
		if result.Status == "running" {
			result.Status = "cancelling"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE post_ingest_task SET status='cancelled', lease_owner=NULL, lease_until=NULL,
				last_error='scan cancelled', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			WHERE scan_task_id=? AND status='waiting'`, taskID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		updated = true
		return nil
	}); err != nil {
		return CancelResult{}, err
	}
	if !updated {
		return result, nil
	}
	if c.afterCancelCommit != nil {
		c.afterCancelCommit()
	}
	c.mu.Lock()
	cancel := c.cancels[taskID]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c.onScanCancelled != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := c.onScanCancelled(cleanupCtx, taskID); err != nil {
			return CancelResult{}, fmt.Errorf("scancoord: cancel post-ingest for task %d: %w", taskID, err)
		}
	}
	return result, nil
}
func (c *Coordinator) renewLease(ctx context.Context, libraryID, taskID int64, owner string) (bool, error) {
	var renewed bool
	err := store.WithBusyRetry(ctx, c.metrics, func() error {
		modifier := fmt.Sprintf("+%d seconds", int64(c.leaseDuration/time.Second))
		result, err := c.db.ExecContext(ctx, `
			UPDATE scan_lease SET lease_until=datetime(CURRENT_TIMESTAMP, ?), updated_at=CURRENT_TIMESTAMP
			WHERE library_id=? AND scan_task_id=? AND owner_id=?`, modifier, libraryID, taskID, owner)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		renewed = rows == 1
		return nil
	})
	return renewed, err
}

func (c *Coordinator) releaseLease(ctx context.Context, libraryID, taskID int64, owner string) (bool, error) {
	var released bool
	err := store.WithBusyRetry(ctx, c.metrics, func() error {
		result, err := c.db.ExecContext(ctx, `DELETE FROM scan_lease WHERE library_id=? AND scan_task_id=? AND owner_id=?`, libraryID, taskID, owner)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		released = rows == 1
		return nil
	})
	return released, err
}
