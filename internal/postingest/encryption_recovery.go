package postingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

const encryptionStageBatchMax = 100

func ReconcileEncryptionStages(ctx context.Context, db *sql.DB, limit int) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, errors.New("encryption stage reconcile: database required")
	}
	if limit <= 0 || limit > encryptionStageBatchMax {
		limit = encryptionStageBatchMax
	}
	rows, err := db.QueryContext(ctx, `SELECT stage_id,media_id,enc_path,state FROM media_encryption_stage_journal WHERE recovery_error NOT IN ('cleaned_unreferenced','verified_committed') ORDER BY updated_at,stage_id LIMIT ?`, limit)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	type row struct {
		stage       string
		media       int64
		path, state string
	}
	var batch []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.stage, &r.media, &r.path, &r.state); err != nil {
			return checked, cleaned, err
		}
		batch = append(batch, r)
	}
	if err = rows.Err(); err != nil {
		return checked, cleaned, err
	}
	for _, r := range batch {
		checked++
		var refs int
		err = db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM media m WHERE m.id=? AND m.file_path=?)+(SELECT COUNT(*) FROM media_ingest_evidence e WHERE e.stage_id=?)+(SELECT COUNT(*) FROM post_ingest_task p JOIN media_encryption_stage_journal j ON j.task_id=p.id AND j.attempt=p.attempts WHERE j.stage_id=? AND p.status='running' AND p.lease_owner=j.owner_token)`, r.media, r.path, r.stage, r.stage).Scan(&refs)
		if err != nil {
			return checked, cleaned, err
		}
		if refs > 0 {
			if r.state == "committed" {
				_, _ = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET recovery_error='verified_committed' WHERE stage_id=?`, r.stage)
			}
			continue
		}
		if err = os.Remove(r.path); err != nil && !os.IsNotExist(err) {
			retErr = err
			continue
		}
		res, e := db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',recovery_error='cleaned_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state<>'committed'`, r.stage)
		if e != nil {
			return checked, cleaned, e
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			cleaned++
		}
	}
	return checked, cleaned, retErr
}
