package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"knox-media/internal/keystore"
	"knox-media/internal/publication"
)

// PlaintextTempBound scopes a temporary plaintext materialization to one task attempt.
type PlaintextTempBound struct {
	MediaID    int64
	Generation int64
	TaskID     int64
	TaskType   string
	LeaseOwner string
}

func (b PlaintextTempBound) valid() error {
	if b.MediaID <= 0 || b.Generation <= 0 || b.TaskID <= 0 {
		return fmt.Errorf("plaintext temp: media/generation/task identity required")
	}
	if strings.TrimSpace(b.TaskType) == "" || strings.TrimSpace(b.LeaseOwner) == "" {
		return fmt.Errorf("plaintext temp: task type and lease owner required")
	}
	return nil
}

// TaskPlaintextTemp materializes encrypted sources into a protected, lease-bound directory.
type TaskPlaintextTemp struct {
	Root string
}

var (
	defaultTaskPlaintextMu sync.RWMutex
	defaultTaskPlaintext   *TaskPlaintextTemp
)

// NewTaskPlaintextTemp creates a service rooted at root (created on demand).
func NewTaskPlaintextTemp(root string) *TaskPlaintextTemp {
	return &TaskPlaintextTemp{Root: strings.TrimSpace(root)}
}

// SetDefaultTaskPlaintextTemp installs the process-wide task plaintext temp service.
// Pass nil to clear (tests and shutdown). Also wires publication cancel/recover
// hooks so lease-end paths can ReleaseBoundForAttempt without importing storage.
func SetDefaultTaskPlaintextTemp(svc *TaskPlaintextTemp) {
	defaultTaskPlaintextMu.Lock()
	defaultTaskPlaintext = svc
	defaultTaskPlaintextMu.Unlock()
	if svc == nil {
		publication.SetPostIngestTempRelease(nil)
		return
	}
	publication.SetPostIngestTempRelease(func(mediaID, generation, taskID int64) {
		_ = ReleaseBoundForAttempt(mediaID, generation, taskID)
	})
}

func defaultTaskPlaintextTemp() *TaskPlaintextTemp {
	defaultTaskPlaintextMu.RLock()
	defer defaultTaskPlaintextMu.RUnlock()
	return defaultTaskPlaintext
}

// DefaultTaskPlaintextTemp returns the process-wide installed plaintext temp service.
func DefaultTaskPlaintextTemp() *TaskPlaintextTemp {
	return defaultTaskPlaintextTemp()
}

// Materialize decrypts an encrypted source into the bound task directory, or returns path as-is.
func (s *TaskPlaintextTemp) Materialize(db *sql.DB, vault *keystore.Vault, bound PlaintextTempBound, path string) (workPath string, release func(), err error) {
	noop := func() {}
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return "", noop, fmt.Errorf("plaintext temp: root is not configured")
	}
	if err := bound.valid(); err != nil {
		return "", noop, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", noop, fmt.Errorf("plaintext temp: empty media path")
	}
	if !InputNeedsPipe(db, bound.MediaID, path) {
		return path, noop, nil
	}
	if db == nil || vault == nil {
		return "", noop, fmt.Errorf("encrypted source requires keystore")
	}
	dir := s.boundDir(bound)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", noop, err
	}
	seeker, err := OpenPlaintextSeeker(db, vault, bound.MediaID, path)
	if err != nil {
		return "", noop, err
	}
	defer func() { _ = seeker.Close() }()

	ext := tempPlaintextExt(db, bound.MediaID, path)
	tmp, err := os.CreateTemp(dir, "plain-*"+ext)
	if err != nil {
		return "", noop, err
	}
	tmpPath := tmp.Name()
	if _, err = io.Copy(tmp, seeker); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", noop, err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", noop, err
	}
	if err := s.ensureInsideRoot(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", noop, err
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		_ = os.Remove(tmpPath)
		_ = os.Remove(dir) // best-effort; non-empty dirs remain until Recover
	}
	return tmpPath, release, nil
}

func (s *TaskPlaintextTemp) boundDir(bound PlaintextTempBound) string {
	return filepath.Join(s.Root,
		fmt.Sprintf("%d", bound.MediaID),
		fmt.Sprintf("%d", bound.Generation),
		fmt.Sprintf("%d", bound.TaskID),
	)
}

func (s *TaskPlaintextTemp) ensureInsideRoot(path string) error {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("plaintext temp: path escaped task bounds")
	}
	return nil
}

// ReleaseBound removes all temp files for a finished/cancelled/failed task attempt.
func (s *TaskPlaintextTemp) ReleaseBound(bound PlaintextTempBound) error {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return nil
	}
	if err := bound.valid(); err != nil {
		return err
	}
	dir := s.boundDir(bound)
	if err := s.ensureInsideRoot(dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// ReleaseBoundForAttempt removes plaintext temps for a task attempt by durable
// media/generation/task identity. Lease owner is intentionally not required so
// cancel/recover paths can release after clearing lease_owner. A new claim must
// materialize under a fresh bound (same task id is fine; directory is recreated).
func ReleaseBoundForAttempt(mediaID, generation, taskID int64) error {
	svc := defaultTaskPlaintextTemp()
	if svc == nil || strings.TrimSpace(svc.Root) == "" {
		return nil
	}
	if mediaID <= 0 || generation <= 0 || taskID <= 0 {
		return nil
	}
	dir := filepath.Join(svc.Root,
		fmt.Sprintf("%d", mediaID),
		fmt.Sprintf("%d", generation),
		fmt.Sprintf("%d", taskID),
	)
	if err := svc.ensureInsideRoot(dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// ReleaseBoundForTask releases plaintext temps for a terminal task attempt using the
// process-wide TaskPlaintextTemp service (no-op when unset or identity incomplete).
func ReleaseBoundForTask(bound PlaintextTempBound) error {
	return ReleaseBoundForAttempt(bound.MediaID, bound.Generation, bound.TaskID)
}

// BoundFromPostIngestTask builds a plaintext-temp bound from a claimed post-ingest task.
func BoundFromPostIngestTask(mediaID, generation, taskID int64, taskType, leaseOwner string) PlaintextTempBound {
	return PlaintextTempBound{
		MediaID:    mediaID,
		Generation: generation,
		TaskID:     taskID,
		TaskType:   taskType,
		LeaseOwner: leaseOwner,
	}
}

// Recover deletes orphaned plaintext temp trees under the service root.
func (s *TaskPlaintextTemp) Recover(ctx context.Context) error {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if _, err := os.Stat(s.Root); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(s.Root)
}
