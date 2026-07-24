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

const encryptionStageBatchMax = 100

func ReconcileEncryptionStages(ctx context.Context, db *sql.DB, roots EncryptionRecoveryRoots, limit int) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, errors.New("encryption stage reconcile: database required")
	}
	if limit <= 0 || limit > encryptionStageBatchMax {
		limit = encryptionStageBatchMax
	}
	rows, err := db.QueryContext(ctx, `SELECT stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state FROM media_encryption_stage_journal WHERE recovery_error NOT IN ('cleaned_unreferenced','verified_committed') ORDER BY updated_at,stage_id LIMIT ?`, limit)
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
		if !managedEncryptionPath(roots.Staged, r.enc) || r.quarantine != "" && !managedEncryptionPath(roots.Quarantine, r.quarantine) {
			_, _ = db.ExecContext(ctx, `UPDATE media SET publication_state='failed',last_error='unsafe encryption recovery path' WHERE id=?`, r.media)
			_, _ = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error='unsafe_path' WHERE stage_id=?`, r.stage)
			return checked, cleaned, errors.New("unsafe encryption recovery path")
		}
		var selected string
		_ = db.QueryRowContext(ctx, `SELECT file_path FROM media WHERE id=?`, r.media).Scan(&selected)
		if samePathForEvidence(selected, r.enc) {
			var n int
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.id=? AND p.attempts=? AND p.status='done' JOIN media_ingest_step s ON s.id=? AND s.status='done' JOIN media_ingest_run run ON run.id=? AND run.status IN ('published','degraded') WHERE e.stage_id=? AND e.kind='encrypt' AND e.media_id=? AND e.generation=?`, r.task, r.attempt, r.step, r.run, r.stage, r.media, r.generation).Scan(&n)
			if err != nil || n != 1 {
				return checked, cleaned, errors.New("committed encryption recovery mismatch")
			}
			if r.quarantine != "" {
				_ = os.Remove(r.quarantine)
			}
			_, _ = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='committed',recovery_error='verified_committed' WHERE stage_id=?`, r.stage)
			continue
		}
		var active int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE id=? AND attempts=? AND status='running' AND lease_owner=?`, r.task, r.attempt, r.owner).Scan(&active)
		if active == 1 {
			continue
		}
		if r.quarantine != "" {
			if err = restoreQuarantinedPlaintext(r.quarantine, r.source, roots.Quarantine); err != nil {
				_, _ = db.ExecContext(ctx, `UPDATE media SET publication_state='failed',last_error=? WHERE id=?`, "encryption recovery restore failed: "+err.Error(), r.media)
				_, _ = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error=? WHERE stage_id=?`, err.Error(), r.stage)
				return checked, cleaned, err
			}
		}
		_ = os.Remove(r.enc)
		_, _ = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='restored',quarantine_path='',recovery_error='stale_restored' WHERE stage_id=?`, r.stage)
		cleaned++
	}
	return checked, cleaned, retErr
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
