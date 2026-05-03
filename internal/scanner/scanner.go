package scanner

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"

	"knox-media/internal/scraper"
	"knox-media/pkg/ffprobe"
	"knox-media/pkg/fileutil"
	"knox-media/pkg/hashutil"
)

type Scanner struct {
	DB           *sql.DB
	FFprobePath  string
	SkipHash     bool
	// FFprobeExtra optional args before the input path (e.g. analyzeduration/probesize for faster scans).
	FFprobeExtra []string
	OnFile       func(path string, err error)
	OnMediaAdded func(mediaID int64, title string, fileType string)
}

func (s *Scanner) ScanLibrary(libraryID int64, rootPath string) (added int, err error) {
	return s.ScanLibraryFoldersWithContext(context.Background(), libraryID, []string{rootPath})
}

func (s *Scanner) ScanLibraryFolders(libraryID int64, roots []string) (added int, err error) {
	return s.ScanLibraryFoldersWithContext(context.Background(), libraryID, roots)
}

func (s *Scanner) ScanLibraryFoldersWithContext(ctx context.Context, libraryID int64, roots []string) (added int, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		r = filepath.Clean(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		fi, e := os.Stat(r)
		if e != nil || !fi.IsDir() {
			continue
		}
		cleanRoots = append(cleanRoots, r)
	}
	if len(cleanRoots) == 0 {
		return 0, os.ErrNotExist
	}
	if _, err := s.DB.Exec(`DELETE FROM library_node WHERE library_id = ?`, libraryID); err != nil {
		return 0, err
	}
	seenMedia := make(map[string]struct{})
	for idx, rootPath := range cleanRoots {
		rootLabel := filepath.Base(rootPath)
		if rootLabel == "" || rootLabel == "." || rootLabel == string(filepath.Separator) {
			rootLabel = fmt.Sprintf("folder-%d", idx+1)
		}
		rootNodePath := fmt.Sprintf("%02d_%s", idx+1, filepath.ToSlash(rootLabel))
		_ = s.upsertNode(libraryID, "", rootNodePath, rootLabel, "dir", nil)
		err = filepath.WalkDir(rootPath, func(path string, d os.DirEntry, walkErr error) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if walkErr != nil {
				return walkErr
			}
			rel, relErr := filepath.Rel(rootPath, path)
			if relErr != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				rel = ""
			}
			parentPath := rootNodePath
			nodePath := rootNodePath
			nodeName := filepath.Base(path)
			if rel != "" {
				parentPath = filepath.ToSlash(filepath.Dir(rel))
				if parentPath == "." {
					parentPath = rootNodePath
				} else {
					parentPath = rootNodePath + "/" + parentPath
				}
				nodePath = rootNodePath + "/" + rel
				nodeName = filepath.Base(rel)
			}
			if d.IsDir() {
				if rel != "" {
					_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "dir", nil)
				}
				return nil
			}
			_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", nil)
			ft := fileutil.GuessFileType(path)
			if ft != "video" && ft != "audio" {
				return nil
			}
			normPath := normalizeMediaPath(path)
			if _, exists := seenMedia[normPath]; exists {
				// Same file path encountered again in current scan (e.g. overlapping roots); skip duplicate.
				return nil
			}
			seenMedia[normPath] = struct{}{}
			st, _ := os.Stat(path)
			curMtime := int64(0)
			if st != nil {
				curMtime = st.ModTime().UTC().Unix()
			}
			var existingMediaID int64
			var existingMtime sql.NullInt64
			if e := s.DB.QueryRow(`SELECT id, file_mtime FROM media WHERE lower(file_path) = lower(?) LIMIT 1`, normPath).Scan(&existingMediaID, &existingMtime); e == nil && existingMediaID > 0 {
				if existingMtime.Valid && existingMtime.Int64 == curMtime {
					_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &existingMediaID)
					if s.OnFile != nil {
						s.OnFile(path, nil)
					}
					return nil
				}
			}
			rawTitle := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			title := scraper.NormalizeTitle(rawTitle)
			if title == "" {
				title = rawTitle
			}
			fileID := uuid.NewString()
			var dur, w, h, br int
			var format, meta string
			if ft == "video" || ft == "audio" {
				if pr, e := ffprobe.ProbeOptions(s.FFprobePath, path, s.FFprobeExtra); e == nil {
					dur = pr.DurationSec
					w = pr.Width
					h = pr.Height
					br = pr.Bitrate
					format = pr.Format
					meta = pr.RawJSON
				}
			}
			var md5sum sql.NullString
			if !s.SkipHash {
				if h, e := hashutil.MD5File(path); e == nil {
					md5sum = sql.NullString{String: h, Valid: true}
					var existing string
					e2 := s.DB.QueryRow(`SELECT file_id FROM media WHERE md5 = ? LIMIT 1`, h).Scan(&existing)
					if e2 == nil && existing != "" {
						if s.OnFile != nil {
							s.OnFile(path, nil)
						}
						return nil
					}
				}
			}
			metaJSON := meta
			if metaJSON == "" {
				b, _ := json.Marshal(map[string]string{"title": title})
				metaJSON = string(b)
			}
			var res sql.Result
			var e error
			if existingMediaID > 0 {
				res, e = s.DB.Exec(`
				UPDATE media
				SET library_id = ?, title = ?, file_path = ?, file_type = ?, duration = ?, width = ?, height = ?, bitrate = ?, md5 = ?, format = ?, meta_json = ?, status = 'active', file_mtime = ?
				WHERE id = ?`,
					libraryID, title, normPath, ft, nullInt(dur), nullInt(w), nullInt(h), nullInt(br), nullString(md5sum), nullStringVal(format), metaJSON, curMtime, existingMediaID,
				)
			} else {
				res, e = s.DB.Exec(`
				INSERT INTO media (library_id, file_id, title, file_path, file_type, duration, width, height, bitrate, md5, format, meta_json, status, file_mtime)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?)`,
					libraryID, fileID, title, normPath, ft, nullInt(dur), nullInt(w), nullInt(h), nullInt(br), nullString(md5sum), nullStringVal(format), metaJSON, curMtime,
				)
			}
			if e != nil {
				if strings.Contains(e.Error(), "UNIQUE") {
					return nil
				}
				if s.OnFile != nil {
					s.OnFile(path, e)
				}
				return nil
			}
			if existingMediaID == 0 {
				added++
			}
			var mediaID = existingMediaID
			if mediaID == 0 {
				if mid, midErr := res.LastInsertId(); midErr == nil && mid > 0 {
					mediaID = mid
				}
			}
			if mediaID > 0 {
				_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &mediaID)
				if existingMediaID == 0 && s.OnMediaAdded != nil {
					s.OnMediaAdded(mediaID, title, ft)
				}
			}
			if s.OnFile != nil {
				s.OnFile(path, nil)
			}
			return nil
		})
		if err != nil {
			return added, err
		}
	}
	// sync deletion: remove files no longer present
	rows, qerr := s.DB.Query(`SELECT file_path FROM media WHERE library_id = ?`, libraryID)
	if qerr == nil {
		defer rows.Close()
		for rows.Next() {
			var p sql.NullString
			if rows.Scan(&p) != nil || !p.Valid || p.String == "" {
				continue
			}
			if _, ok := seenMedia[normalizeMediaPath(p.String)]; !ok {
				_, _ = s.DB.Exec(`DELETE FROM media WHERE library_id = ? AND file_path = ?`, libraryID, p.String)
			}
		}
	}
	return added, nil
}

func normalizeMediaPath(p string) string {
	cleaned := filepath.Clean(strings.TrimSpace(p))
	if runtime.GOOS == "windows" {
		// Windows file paths are case-insensitive; normalize to lower case for dedupe checks.
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullString(ns sql.NullString) any {
	if !ns.Valid {
		return nil
	}
	return ns.String
}

func nullStringVal(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Scanner) upsertNode(libraryID int64, parentPath, nodePath, nodeName, nodeType string, mediaID *int64) error {
	var mid any
	if mediaID != nil && *mediaID > 0 {
		mid = *mediaID
	}
	_, err := s.DB.Exec(`
		INSERT INTO library_node (library_id, parent_path, node_path, node_name, node_type, media_id)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(library_id, node_path) DO UPDATE SET
			parent_path = excluded.parent_path,
			node_name = excluded.node_name,
			node_type = excluded.node_type,
			media_id = CASE
				WHEN excluded.media_id IS NOT NULL THEN excluded.media_id
				ELSE library_node.media_id
			END
	`, libraryID, nullStringVal(parentPath), nodePath, nodeName, nodeType, mid)
	return err
}
