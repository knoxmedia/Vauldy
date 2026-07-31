package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"os"
)

type EncryptionStateMachineSeams struct {
	BeforeMove, AfterMove, BeforeMarkQuarantined, BeforeFinalCommit func() error
	ImmediateTx                                                     func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error)
}

func (s EncryptionStateMachineSeams) immediate(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
	if s.ImmediateTx != nil {
		return s.ImmediateTx(ctx, db, fn)
	}
	return store.WithImmediateConnTx(ctx, db, fn)
}

func safeEncryptionStageID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func reserveEncryptionQuarantine(ctx context.Context, db *sql.DB, task Task, s storage.StagedMediaEncryption, root string, seams EncryptionStateMachineSeams) (string, error) {
	if !safeEncryptionStageID(s.StageID) {
		return "", errors.New("unsafe encryption stage id")
	}
	path, err := quarantinePath(root, task.MediaID, task.Generation, s.StageID)
	if err != nil {
		return "", err
	}
	_, err = seams.immediate(ctx, db, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET quarantine_path=?,state='quarantining',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND task_id=? AND attempt=? AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND source_path=? AND source_fingerprint=? AND state='staged'`, path, s.StageID, task.ID, task.Attempts, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, s.OriginalPath, s.SourceFingerprint)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errors.New("encryption quarantine reserve fence lost")
		}
		return nil
	})
	return path, err
}
func moveReservedEncryptionQuarantine(ctx context.Context, db *sql.DB, task Task, s storage.StagedMediaEncryption, root, path string, seams EncryptionStateMachineSeams) error {
	if seams.BeforeMove != nil {
		if err := seams.BeforeMove(); err != nil {
			return err
		}
	}
	actual, err := quarantinePlaintext(s.OriginalPath, root, task.MediaID, task.Generation, s.StageID)
	if err != nil {
		return err
	}
	if !samePathForEvidence(actual, path) {
		return errors.New("reserved quarantine path mismatch")
	}
	if seams.AfterMove != nil {
		if err = seams.AfterMove(); err != nil {
			return err
		}
	}
	if seams.BeforeMarkQuarantined != nil {
		if err = seams.BeforeMarkQuarantined(); err != nil {
			return err
		}
	}

	_, err = seams.immediate(ctx, db, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND task_id=? AND attempt=? AND media_id=? AND generation=? AND owner_token=? AND quarantine_path=? AND state='quarantining'`, s.StageID, task.ID, task.Attempts, task.MediaID, task.Generation, task.LeaseOwner, path)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errors.New("encryption quarantine mark fence lost")
		}
		return nil
	})
	return err
}
func verifyDurableQuarantine(ctx context.Context, tx store.SQLExecutor, task Task, s storage.StagedMediaEncryption, path string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_encryption_stage_journal WHERE stage_id=? AND task_id=? AND attempt=? AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND source_path=? AND source_fingerprint=? AND quarantine_path=? AND state='quarantined'`, s.StageID, task.ID, task.Attempts, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, s.OriginalPath, s.SourceFingerprint, path).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("durable quarantine identity missing")
	}
	if _, err = os.Stat(s.OriginalPath); !os.IsNotExist(err) {
		return fmt.Errorf("public plaintext remains: %v", err)
	}
	// Quarantine content hash is verified outside BEGIN IMMEDIATE; under the
	// write lock we only re-check journal identity and that the public path is gone.
	return nil
}
