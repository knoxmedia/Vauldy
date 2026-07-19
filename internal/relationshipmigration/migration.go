package relationshipmigration

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"knox-media/internal/musicparse"
	"knox-media/internal/musicstore"
	"knox-media/internal/sqliteretry"
	"knox-media/internal/tvparse"
	"knox-media/internal/tvstore"
)

const (
	migrationName = "media_relationships_v1"
	batchSize     = 128
)

type mediaRow struct {
	id, libraryID                     int64
	libraryType, fileType, path, meta string
	linked                            bool
}

// MigrateMediaRelationships incrementally recovers historical music and TV links.
// The high-water mark is advanced only after each row has been processed, so a
// cancelled or failed startup resumes without rescanning completed rows.
func MigrateMediaRelationships(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	if err := ensureSchema(ctx, db); err != nil {
		return err
	}
	for {
		var phase string
		if err := db.QueryRowContext(ctx, `SELECT phase FROM relationship_migration_state WHERE name=?`, migrationName).Scan(&phase); err != nil {
			return err
		}
		var done bool
		var err error
		switch phase {
		case "precise":
			done, err = runPreciseBatch(ctx, db)
		case "loose_populate":
			done, err = populateLooseBatch(ctx, db)
		case "loose_process":
			done, err = processLooseBatch(ctx, db)
		case "complete":
			if err = runCompleteCheck(ctx, db); err == nil {
				var next string
				err = db.QueryRowContext(ctx, `SELECT phase FROM relationship_migration_state WHERE name=?`, migrationName).Scan(&next)
				if err == nil && next != "complete" {
					continue
				}
			}
			return err
		default:
			return fmt.Errorf("unknown relationship migration phase %q", phase)
		}
		if err != nil {
			return err
		}
		_ = done
	}
}
func ensureSchema(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS relationship_migration_state(name TEXT PRIMARY KEY,version INTEGER NOT NULL,last_media_id INTEGER NOT NULL DEFAULT 0,phase TEXT NOT NULL DEFAULT 'precise',loose_last_media_id INTEGER NOT NULL DEFAULT 0,loose_work_last_media_id INTEGER NOT NULL DEFAULT 0,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`, `CREATE TABLE IF NOT EXISTS relationship_migration_loose_counter(library_id INTEGER NOT NULL,folder_key TEXT NOT NULL,next_episode INTEGER NOT NULL,PRIMARY KEY(library_id,folder_key))`, `CREATE TABLE IF NOT EXISTS relationship_migration_loose_work(media_id INTEGER PRIMARY KEY,library_id INTEGER NOT NULL,folder_key TEXT NOT NULL,show_name TEXT NOT NULL,file_path TEXT NOT NULL,episode_num INTEGER NOT NULL,status TEXT NOT NULL DEFAULT 'pending')`} {
		if err := sqliteretry.WithBusyRetry(ctx, func() error { _, e := db.ExecContext(ctx, q); return e }); err != nil {
			return err
		}
	}
	for _, col := range []struct{ name, ddl string }{{"phase", "TEXT NOT NULL DEFAULT 'precise'"}, {"loose_last_media_id", "INTEGER NOT NULL DEFAULT 0"}, {"loose_work_last_media_id", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := addColumnIfMissing(ctx, db, "relationship_migration_state", col.name, col.ddl); err != nil {
			return err
		}
	}
	return sqliteretry.WithBusyRetry(ctx, func() error {
		_, e := db.ExecContext(ctx, `INSERT INTO relationship_migration_state(name,version,last_media_id,phase) VALUES(?,1,0,'precise') ON CONFLICT(name) DO NOTHING`, migrationName)
		return e
	})
}
func addColumnIfMissing(ctx context.Context, db *sql.DB, table, column, ddl string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	return sqliteretry.WithBusyRetry(ctx, func() error {
		_, e := db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+ddl)
		return e
	})
}
func lockState(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE relationship_migration_state SET updated_at=updated_at WHERE name=?`, migrationName)
	return err
}
func runPreciseBatch(ctx context.Context, db *sql.DB) (bool, error) {
	done := false
	err := sqliteretry.WithBusyRetry(ctx, func() error {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = lockState(ctx, tx); e != nil {
			return e
		}
		var phase string
		var last int64
		if e = tx.QueryRowContext(ctx, `SELECT phase,last_media_id FROM relationship_migration_state WHERE name=?`, migrationName).Scan(&phase, &last); e != nil {
			return e
		}
		if phase != "precise" {
			done = true
			return tx.Commit()
		}
		batch, e := loadBatch(ctx, tx, last)
		if e != nil {
			return e
		}
		if len(batch) == 0 {
			_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_state SET phase='loose_populate',updated_at=CURRENT_TIMESTAMP WHERE name=?`, migrationName)
			done = true
		} else {
			for _, row := range batch {
				if e = processRowTx(ctx, tx, row); e != nil {
					return fmt.Errorf("media relationship migration id=%d: %w", row.id, e)
				}
				last = row.id
			}
			_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_state SET last_media_id=?,updated_at=CURRENT_TIMESTAMP WHERE name=?`, last, migrationName)
		}
		if e != nil {
			return e
		}
		return tx.Commit()
	})
	return done, err
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadBatch(ctx context.Context, db queryer, lastID int64) ([]mediaRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.id,m.library_id,COALESCE(l.type,''),COALESCE(m.file_type,''),COALESCE(m.file_path,''),COALESCE(m.meta_json,''),CASE WHEN mt.media_id IS NOT NULL OR em.media_id IS NOT NULL THEN 1 ELSE 0 END FROM media m JOIN library l ON l.id=m.library_id LEFT JOIN music_track mt ON mt.media_id=m.id LEFT JOIN episode_media em ON em.media_id=m.id WHERE m.id>? AND m.status='active' ORDER BY m.id LIMIT ?`, lastID, batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]mediaRow, 0, batchSize)
	for rows.Next() {
		var r mediaRow
		if err = rows.Scan(&r.id, &r.libraryID, &r.libraryType, &r.fileType, &r.path, &r.meta, &r.linked); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func runCompleteCheck(ctx context.Context, db *sql.DB) error {
	return sqliteretry.WithBusyRetry(ctx, func() error {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = lockState(ctx, tx); e != nil {
			return e
		}
		var phase string
		var precise, maxID int64
		if e = tx.QueryRowContext(ctx, `SELECT phase,last_media_id FROM relationship_migration_state WHERE name=?`, migrationName).Scan(&phase, &precise); e != nil {
			return e
		}
		if phase != "complete" {
			return tx.Commit()
		}
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM media`).Scan(&maxID); e != nil {
			return e
		}
		if maxID > precise {
			_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_state SET phase='precise',loose_last_media_id=?,loose_work_last_media_id=? WHERE name=?`, precise, precise, migrationName)
			if e != nil {
				return e
			}
			return tx.Commit()
		}
		return tx.Commit()
	})
}

func processRowTx(ctx context.Context, tx *sql.Tx, row mediaRow) error {
	if row.linked {
		return nil
	}
	switch {
	case row.fileType == "audio" && musicparse.IsMusicLibraryType(row.libraryType):
		return musicstore.LinkTrackTx(ctx, tx, row.libraryID, row.id, musicstore.DecodeMusicMeta(row.meta, row.path))
	case row.fileType == "video" && tvparse.IsTVLibraryType(row.libraryType):
		info, ok := tvparse.ParseEpisodeFromMedia(row.path, row.meta)
		if !ok || strings.TrimSpace(info.SeriesTitleNorm) == "" {
			return nil
		}
		return tvstore.LinkEpisodeTx(ctx, tx, row.libraryID, row.id, info)
	}
	return nil
}

type looseCandidate struct {
	id, libraryID int64
	path, meta    string
	linked        bool
}

func populateLooseBatch(ctx context.Context, db *sql.DB) (bool, error) {
	done := false
	err := sqliteretry.WithBusyRetry(ctx, func() error {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = lockState(ctx, tx); e != nil {
			return e
		}
		var phase string
		var last int64
		if e = tx.QueryRowContext(ctx, `SELECT phase,loose_last_media_id FROM relationship_migration_state WHERE name=?`, migrationName).Scan(&phase, &last); e != nil {
			return e
		}
		if phase != "loose_populate" {
			done = true
			return tx.Commit()
		}
		rows, e := tx.QueryContext(ctx, `SELECT m.id,m.library_id,m.file_path,COALESCE(m.meta_json,''),CASE WHEN em.media_id IS NULL THEN 0 ELSE 1 END FROM media m JOIN library l ON l.id=m.library_id LEFT JOIN episode_media em ON em.media_id=m.id WHERE m.id>? AND m.file_type='video' AND m.status='active' AND lower(trim(l.type)) IN('tv','anime','television','series') ORDER BY m.id LIMIT ?`, last, batchSize)
		if e != nil {
			return e
		}
		items := []looseCandidate{}
		for rows.Next() {
			var it looseCandidate
			if e = rows.Scan(&it.id, &it.libraryID, &it.path, &it.meta, &it.linked); e != nil {
				rows.Close()
				return e
			}
			items = append(items, it)
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		if len(items) == 0 {
			_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_state SET phase='loose_process',updated_at=CURRENT_TIMESTAMP WHERE name=?`, migrationName)
			done = true
		} else {
			for _, it := range items {
				show := tvparse.ShowFolderName(it.path)
				folder := normalizeLooseFolder(filepath.Dir(it.path))
				if tvparse.IsValidShowFolderName(show) {
					var next int
					e = tx.QueryRowContext(ctx, `SELECT next_episode FROM relationship_migration_loose_counter WHERE library_id=? AND folder_key=?`, it.libraryID, folder).Scan(&next)
					if e == sql.ErrNoRows {
						next = 1
						_, e = tx.ExecContext(ctx, `INSERT INTO relationship_migration_loose_counter VALUES(?,?,?)`, it.libraryID, folder, 2)
					} else if e == nil {
						_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_loose_counter SET next_episode=? WHERE library_id=? AND folder_key=?`, next+1, it.libraryID, folder)
					}
					if e != nil {
						return e
					}
					if _, precise := tvparse.ParseEpisodeFromMedia(it.path, it.meta); !precise && !it.linked {
						if _, e = tx.ExecContext(ctx, `INSERT INTO relationship_migration_loose_work(media_id,library_id,folder_key,show_name,file_path,episode_num,status) VALUES(?,?,?,?,?,?,'pending') ON CONFLICT(media_id) DO NOTHING`, it.id, it.libraryID, folder, show, it.path, next); e != nil {
							return e
						}
					}
				}
				last = it.id
			}
			_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_state SET loose_last_media_id=?,updated_at=CURRENT_TIMESTAMP WHERE name=?`, last, migrationName)
		}
		if e != nil {
			return e
		}
		return tx.Commit()
	})
	return done, err
}
func processLooseBatch(ctx context.Context, db *sql.DB) (bool, error) {
	done := false
	err := sqliteretry.WithBusyRetry(ctx, func() error {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = lockState(ctx, tx); e != nil {
			return e
		}
		var phase string
		var last int64
		if e = tx.QueryRowContext(ctx, `SELECT phase,loose_work_last_media_id FROM relationship_migration_state WHERE name=?`, migrationName).Scan(&phase, &last); e != nil {
			return e
		}
		if phase != "loose_process" {
			done = true
			return tx.Commit()
		}
		rows, e := tx.QueryContext(ctx, `SELECT media_id,library_id,show_name,file_path,episode_num FROM relationship_migration_loose_work WHERE media_id>? ORDER BY media_id LIMIT ?`, last, batchSize)
		if e != nil {
			return e
		}
		type work struct {
			id, lib    int64
			show, path string
			ep         int
		}
		items := []work{}
		for rows.Next() {
			var w work
			if e = rows.Scan(&w.id, &w.lib, &w.show, &w.path, &w.ep); e != nil {
				rows.Close()
				return e
			}
			items = append(items, w)
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		if len(items) == 0 {
			_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_state SET phase='complete',updated_at=CURRENT_TIMESTAMP WHERE name=?`, migrationName)
			done = true
		} else {
			for _, w := range items {
				info := tvparse.BuildEpisodeInfoFromFolder(w.path, w.show, w.ep)
				if e = tvstore.LinkEpisodeTx(ctx, tx, w.lib, w.id, info); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `UPDATE relationship_migration_loose_work SET status='done' WHERE media_id=?`, w.id); e != nil {
					return e
				}
				last = w.id
			}
			_, e = tx.ExecContext(ctx, `UPDATE relationship_migration_state SET loose_work_last_media_id=?,updated_at=CURRENT_TIMESTAMP WHERE name=?`, last, migrationName)
		}
		if e != nil {
			return e
		}
		return tx.Commit()
	})
	return done, err
}
func normalizeLooseFolder(p string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(p))))
}
