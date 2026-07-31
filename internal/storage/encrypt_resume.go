package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

const EncryptResumeCheckpointBytes = 64 << 20

const encryptResumeSchema = `
CREATE TABLE IF NOT EXISTS media_encrypt_resume (
  media_id INTEGER NOT NULL,
  generation INTEGER NOT NULL,
  stage_id TEXT NOT NULL,
  enc_path TEXT NOT NULL,
  source_path TEXT NOT NULL,
  source_identity TEXT NOT NULL,
  wrapped_dek TEXT NOT NULL,
  iv TEXT NOT NULL,
  plain_offset INTEGER NOT NULL DEFAULT 0 CHECK(plain_offset>=0),
  enc_bytes_written INTEGER NOT NULL DEFAULT 0 CHECK(enc_bytes_written>=0),
  state TEXT NOT NULL CHECK(state IN ('encrypting','staged','abandoned')),
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(media_id, generation)
);`

type EncryptResumeRow struct {
	MediaID, Generation                        int64
	StageID, EncPath, SourcePath, SourceIdentity string
	WrappedDEK, IV, State                      string
	PlainOffset, EncBytesWritten               int64
}

func EnsureEncryptResumeSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("encrypt resume schema: nil db")
	}
	_, err := db.Exec(encryptResumeSchema)
	return err
}

func UpsertEncryptResume(ctx context.Context, db *sql.DB, row EncryptResumeRow) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO media_encrypt_resume (
  media_id, generation, stage_id, enc_path, source_path, source_identity,
  wrapped_dek, iv, plain_offset, enc_bytes_written, state, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(media_id, generation) DO UPDATE SET
  stage_id = excluded.stage_id,
  enc_path = excluded.enc_path,
  source_path = excluded.source_path,
  source_identity = excluded.source_identity,
  wrapped_dek = excluded.wrapped_dek,
  iv = excluded.iv,
  plain_offset = excluded.plain_offset,
  enc_bytes_written = excluded.enc_bytes_written,
  state = excluded.state,
  updated_at = CURRENT_TIMESTAMP`,
		row.MediaID, row.Generation, row.StageID, row.EncPath, row.SourcePath, row.SourceIdentity,
		row.WrappedDEK, row.IV, row.PlainOffset, row.EncBytesWritten, row.State,
	)
	return err
}

func LoadEncryptResume(ctx context.Context, db *sql.DB, mediaID, generation int64) (EncryptResumeRow, error) {
	var row EncryptResumeRow
	err := db.QueryRowContext(ctx, `
SELECT media_id, generation, stage_id, enc_path, source_path, source_identity,
       wrapped_dek, iv, plain_offset, enc_bytes_written, state
FROM media_encrypt_resume
WHERE media_id = ? AND generation = ?`,
		mediaID, generation,
	).Scan(
		&row.MediaID, &row.Generation, &row.StageID, &row.EncPath, &row.SourcePath, &row.SourceIdentity,
		&row.WrappedDEK, &row.IV, &row.PlainOffset, &row.EncBytesWritten, &row.State,
	)
	return row, err
}

func AbandonEncryptResume(ctx context.Context, db *sql.DB, mediaID, generation int64) error {
	res, err := db.ExecContext(ctx, `
UPDATE media_encrypt_resume
SET state = 'abandoned', updated_at = CURRENT_TIMESTAMP
WHERE media_id = ? AND generation = ?`,
		mediaID, generation,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func QuickSourceIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%d|%d", filepath.Clean(abs), info.Size(), info.ModTime().UnixNano()), nil
}
