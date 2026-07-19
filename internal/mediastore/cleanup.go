package mediastore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func pathUnderRoots(path string, roots []string) bool {
	clean, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil || clean == "" {
		return false
	}
	for _, root := range roots {
		r, e := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
		if e != nil || r == "" {
			continue
		}
		rel, e := filepath.Rel(r, clean)
		if e == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
func failCleanup(ctx context.Context, db *sql.DB, path string, cause error) error {
	_, err := db.ExecContext(ctx, `UPDATE media_file_cleanup_task SET status='pending',attempts=attempts+1,next_retry_at=datetime('now','+'||MIN(1440,(1 << MIN(10,attempts)))||' minutes'),last_error=?,updated_at=CURRENT_TIMESTAMP WHERE path=?`, cause.Error(), path)
	return err
}
func cleanupOne(ctx context.Context, db *sql.DB, path string, roots []string) error {
	if !pathUnderRoots(path, roots) {
		cause := fmt.Errorf("cleanup path outside allowed roots")
		if err := failCleanup(ctx, db, path, cause); err != nil {
			return err
		}
		return cause
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		if e := failCleanup(ctx, db, path, err); e != nil {
			return e
		}
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM media_file_cleanup_task WHERE path=?`, path)
	return err
}
func CleanupFiles(ctx context.Context, db *sql.DB, info CleanupInfo, allowedRoots []string) error {
	var first error
	for _, path := range info.Paths {
		if err := cleanupOne(ctx, db, path, allowedRoots); err != nil && first == nil {
			first = err
		}
	}
	return first
}
func RunCleanupBatch(ctx context.Context, db *sql.DB, allowedRoots []string, limit int) (done, failed int, err error) {
	if db == nil || limit <= 0 {
		return
	}
	rows, e := db.QueryContext(ctx, `SELECT path FROM media_file_cleanup_task WHERE status='pending' AND next_retry_at<=CURRENT_TIMESTAMP ORDER BY next_retry_at,path LIMIT ?`, limit)
	if e != nil {
		return 0, 0, e
	}
	var paths []string
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			rows.Close()
			return 0, 0, e
		}
		paths = append(paths, p)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return 0, 0, e
	}
	rows.Close()
	for _, p := range paths {
		if e = cleanupOne(ctx, db, p, allowedRoots); e != nil {
			failed++
		} else {
			done++
		}
	}
	return
}
