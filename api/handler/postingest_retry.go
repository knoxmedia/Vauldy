package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"knox-media/internal/postingest"
)

var explicitPostIngestLocks [64]sync.Mutex

func lockExplicitPostIngest(mediaID int64, typ postingest.TaskType) func() {
	hash := uint64(mediaID)
	for _, ch := range []byte(typ) {
		hash = hash*1099511628211 ^ uint64(ch)
	}
	mu := &explicitPostIngestLocks[hash%uint64(len(explicitPostIngestLocks))]
	mu.Lock()
	return mu.Unlock
}

type explicitPostIngestResult string

const (
	explicitPostIngestQueued         explicitPostIngestResult = "queued"
	explicitPostIngestAlreadyQueued  explicitPostIngestResult = "already_queued"
	explicitPostIngestAlreadyRunning explicitPostIngestResult = "already_running"
	explicitPostIngestAlreadyDone    explicitPostIngestResult = "already_done"
)

func (r explicitPostIngestResult) Queued() bool { return r == explicitPostIngestQueued }

type explicitResetTx func(context.Context, *sql.Tx) error

func subtitleResetTx(mediaID int64) explicitResetTx {
	return func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM media_subtitle WHERE media_id=?`, mediaID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO subtitle_task(media_id,status,message,created_at,started_at,finished_at,updated_at) VALUES(?,'pending',NULL,CURRENT_TIMESTAMP,NULL,NULL,CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO UPDATE SET status='pending',message=NULL,started_at=NULL,finished_at=NULL,updated_at=CURRENT_TIMESTAMP`, mediaID)
		return err
	}
}
func subtitleEnsureTx(mediaID int64) explicitResetTx {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO subtitle_task(media_id,status,message,created_at,started_at,finished_at,updated_at) VALUES(?,'pending',NULL,CURRENT_TIMESTAMP,NULL,NULL,CURRENT_TIMESTAMP)`, mediaID)
		return err
	}
}
func enqueueExplicitPostIngest(ctx context.Context, db *sql.DB, mediaID int64, typ postingest.TaskType, allowDone bool, resetTx explicitResetTx, afterCommit func()) (explicitPostIngestResult, error) {
	unlock := lockExplicitPostIngest(mediaID, typ)
	defer unlock()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id int64
	var status postingest.Status
	err = tx.QueryRowContext(ctx, `SELECT id,status FROM post_ingest_task WHERE media_id=? AND task_type=?`, mediaID, typ).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		if resetTx != nil {
			if err = resetTx(ctx, tx); err != nil {
				return "", err
			}
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,scan_task_id,task_type) VALUES(?,NULL,?) ON CONFLICT(media_id,task_type) DO NOTHING`, mediaID, typ)
		if e != nil {
			return "", e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return "", e
		}
		if e = tx.Commit(); e != nil {
			return "", e
		}
		if afterCommit != nil {
			afterCommit()
		}
		if n == 1 {
			return explicitPostIngestQueued, nil
		}
		return explicitPostIngestAlreadyQueued, nil
	}
	if err != nil {
		return "", err
	}
	switch status {
	case postingest.StatusWaiting:
		return explicitPostIngestAlreadyQueued, nil
	case postingest.StatusRunning:
		return explicitPostIngestAlreadyRunning, nil
	case postingest.StatusDone:
		if !allowDone {
			return explicitPostIngestAlreadyDone, nil
		}
	}
	if resetTx != nil {
		if err = resetTx(ctx, tx); err != nil {
			return "", err
		}
	}
	allowed := "'failed','cancelled'"
	if allowDone {
		allowed += " ,'done'"
	}
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE post_ingest_task SET status='waiting',scan_task_id=NULL,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,attempts=0,available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN (%s)`, allowed), id)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", fmt.Errorf("post-ingest task %d changed concurrently", id)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	if afterCommit != nil {
		afterCommit()
	}
	return explicitPostIngestQueued, nil
}
