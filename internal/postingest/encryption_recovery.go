package postingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"time"
)

const (
	encryptionStageBatchMax       = 100
	encryptionRecoveryMaxAttempts = 5
)

var reconcileRestoreQuarantinedPlaintext = restoreQuarantinedPlaintext

func ReconcileEncryptionStages(ctx context.Context, db *sql.DB, roots EncryptionRecoveryRoots, limit int) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, errors.New("encryption stage reconcile: database required")
	}
	if roots.Resolver == nil {
		return 0, 0, errors.New("encryption stage reconcile: stage root resolver required")
	}
	if limit <= 0 || limit > encryptionStageBatchMax {
		limit = encryptionStageBatchMax
	}
	rows, err := db.QueryContext(ctx, `SELECT j.stage_id,j.task_id,j.attempt,j.media_id,j.run_id,j.step_id,j.generation,j.owner_token,j.source_path,j.quarantine_path,j.source_fingerprint,j.enc_path,j.wrapped_dek,j.iv,j.enc_sha256,j.enc_size,j.state FROM media_encryption_stage_journal j WHERE (j.state IN ('staged','quarantining','quarantined') OR (j.state='failed_closed' AND j.quarantine_path<>'' AND j.recovery_error LIKE 'restore_pending:%' AND j.recovery_attempts<? AND j.next_retry_at<=CURRENT_TIMESTAMP) OR (j.state='committed' AND (j.recovery_error='' OR j.recovery_error LIKE 'plaintext_cleanup_pending:%'))) AND (j.state='committed' OR j.state='quarantining' OR NOT EXISTS(SELECT 1 FROM post_ingest_task p WHERE p.id=j.task_id AND p.attempts=j.attempt AND p.status='running' AND p.lease_owner=j.owner_token)) ORDER BY CASE WHEN j.state='failed_closed' THEN j.next_retry_at ELSE j.updated_at END,j.stage_id LIMIT ?`, encryptionRecoveryMaxAttempts, limit)
	if err != nil {
		return 0, 0, err
	}
	type row struct {
		stage                                                        string
		task, attempt, media, run, step, generation                  int64
		owner, source, quarantine, fp, enc, wrapped, iv, hash, state string
		size                                                         int64
	}
	var batch []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.stage, &r.task, &r.attempt, &r.media, &r.run, &r.step, &r.generation, &r.owner, &r.source, &r.quarantine, &r.fp, &r.enc, &r.wrapped, &r.iv, &r.hash, &r.size, &r.state); err != nil {
			rows.Close()
			return checked, cleaned, err
		}
		batch = append(batch, r)
	}
	rows.Close()
	for _, r := range batch {
		checked++
		stageRoot, resolveErr := roots.Resolver.ResolveEncryptionStageRoot(ctx, r.media, r.source)
		if resolveErr != nil {
			return checked, cleaned, resolveErr
		}
		if !managedEncryptionPath(stageRoot, r.enc) || r.quarantine != "" && !managedEncryptionPath(roots.Quarantine, r.quarantine) {
			if _, err = db.ExecContext(ctx, `UPDATE media SET publication_state='failed',last_error='unsafe encryption recovery path' WHERE id=?`, r.media); err != nil {
				return checked, cleaned, err
			}
			if _, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error='unsafe_path',updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, r.stage); err != nil {
				return checked, cleaned, err
			}
			retErr = errors.Join(retErr, errors.New("unsafe encryption recovery path"))
			continue
		}
		var selected string
		if err = db.QueryRowContext(ctx, `SELECT file_path FROM media WHERE id=?`, r.media).Scan(&selected); err != nil {
			return checked, cleaned, err
		}
		if samePathForEvidence(selected, r.enc) {
			var n int
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.id=? AND p.attempts=? AND p.status='done' JOIN media_ingest_step s ON s.id=? AND s.status='done' JOIN media_ingest_run run ON run.id=? AND run.status IN ('published','degraded') WHERE e.stage_id=? AND e.kind='encrypt' AND e.media_id=? AND e.generation=?`, r.task, r.attempt, r.step, r.run, r.stage, r.media, r.generation).Scan(&n)
			if err != nil {
				return checked, cleaned, err
			}
			if n != 1 {
				if _, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error='committed_verification_mismatch',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, r.stage); err != nil {
					return checked, cleaned, err
				}
				retErr = errors.Join(retErr, errors.New("committed encryption recovery mismatch"))
				continue
			}
			if err = cleanupCommittedEncryptionPlaintext(ctx, db, r.stage, r.quarantine, defaultEncryptionFileOps()); err != nil {
				retErr = errors.Join(retErr, err)
			}
			continue
		}
		var active int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE id=? AND attempts=? AND status='running' AND lease_owner=?`, r.task, r.attempt, r.owner).Scan(&active); err != nil {
			return checked, cleaned, err
		}
		if active == 1 && r.state != "quarantining" {
			continue
		}
		if r.state == "quarantining" {
			sourceInfo, sourceErr := os.Stat(r.source)
			quarantineInfo, quarantineErr := os.Stat(r.quarantine)
			switch {
			case sourceErr == nil && os.IsNotExist(quarantineErr):
				actual, moveErr := quarantinePlaintext(r.source, roots.Quarantine, r.media, r.generation, r.stage)
				if moveErr != nil {
					return checked, cleaned, moveErr
				}
				if !samePathForEvidence(actual, r.quarantine) {
					return checked, cleaned, errors.New("recovery quarantine reservation mismatch")
				}
				_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantining'`, r.stage)
				if err != nil {
					return checked, cleaned, err
				}
				continue
			case os.IsNotExist(sourceErr) && quarantineErr == nil:
				_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantining'`, r.stage)
				if err != nil {
					return checked, cleaned, err
				}
				continue
			case sourceErr == nil && quarantineErr == nil:
				sourceHash, _ := fileSHA256(r.source)
				quarantineHash, _ := fileSHA256(r.quarantine)
				if sourceHash != quarantineHash {
					return checked, cleaned, errors.New("quarantine duplicate hash mismatch")
				}
				_ = sourceInfo
				_ = quarantineInfo
				_ = os.Remove(r.source)
				_, err = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantining'`, r.stage)
				if err != nil {
					return checked, cleaned, err
				}
				continue
			default:
				return checked, cleaned, errors.New("unknown quarantining filesystem state")
			}
		}
		if r.quarantine != "" {
			if err = reconcileRestoreQuarantinedPlaintext(r.quarantine, r.source, roots.Quarantine); err != nil {
				if strings.Contains(err.Error(), "unsafe encryption quarantine path") || strings.Contains(err.Error(), "restore target already exists") {
					if _, updateErr := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error=?,recovery_attempts=recovery_attempts+1,next_retry_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, boundedRecoveryError("failed_closed: ", err), r.stage); updateErr != nil {
						return checked, cleaned, updateErr
					}
					retErr = errors.Join(retErr, err)
					continue
				}
				marker := boundedRecoveryError("restore_pending: ", err)
				if _, updateErr := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error=?,recovery_attempts=recovery_attempts+1,next_retry_at=datetime(CURRENT_TIMESTAMP,'+' || min(3600,30*(1 << min(recovery_attempts,7))) || ' seconds'),updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, marker, r.stage); updateErr != nil {
					return checked, cleaned, updateErr
				}
				continue
			}
		}
		_ = os.Remove(r.enc)
		_, _ = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='restored',quarantine_path='',recovery_error='stale_restored',updated_at=CURRENT_TIMESTAMP WHERE stage_id=?`, r.stage)
		cleaned++
	}
	return checked, cleaned, retErr
}
func cleanupCommittedEncryptionPlaintext(ctx context.Context, db *sql.DB, stageID, quarantine string, ops encryptionFileOps) error {
	if quarantine != "" {
		if err := ops.remove(quarantine); err != nil && !os.IsNotExist(err) {
			marker := "plaintext_cleanup_pending:" + err.Error()
			if len(marker) > 512 {
				marker = marker[:512]
			}
			if _, updateErr := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, marker, stageID); updateErr != nil {
				return errors.Join(err, updateErr)
			}
			return err
		}
		if err := ops.syncDir(filepath.Dir(quarantine)); err != nil {
			marker := "plaintext_cleanup_pending:" + err.Error()
			if len(marker) > 512 {
				marker = marker[:512]
			}
			if _, updateErr := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, marker, stageID); updateErr != nil {
				return errors.Join(err, updateErr)
			}
			return err
		}
	}
	_, err := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error='verified_committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, stageID)
	return err
}

func managedEncryptionPath(root, path string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	ra, e := filepath.Abs(root)
	if e != nil {
		return false
	}
	pa, e := filepath.Abs(path)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(ra, pa)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func RunEncryptionStageReconciler(ctx context.Context, db *sql.DB, roots EncryptionRecoveryRoots, interval time.Duration, limit int, report func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		_, _, err := ReconcileEncryptionStages(ctx, db, roots, limit)
		if err != nil && report != nil {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
