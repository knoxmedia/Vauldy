package store

import (
	"context"
	"database/sql"
)

type mediaMetaExec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// UpdateMediaMetaAndPhotoTime updates media metadata through the supplied
// database or transaction. Vauldy does not materialize Knox photo sort fields.
func UpdateMediaMetaAndPhotoTime(ctx context.Context, exec mediaMetaExec, mediaID int64, metaJSON string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := exec.ExecContext(ctx, `UPDATE media SET meta_json=? WHERE id=?`, metaJSON, mediaID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
