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

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/docparse"
	"knox-media/internal/musicparse"
	"knox-media/internal/musicstore"
	"knox-media/internal/photoparse"
	"knox-media/internal/photogeocode"
	"knox-media/internal/keystore"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"knox-media/internal/tvparse"
	"knox-media/internal/tvstore"
	"knox-media/pkg/fileutil"
	"knox-media/pkg/hashutil"
)

type Scanner struct {
	DB           *sql.DB
	Vault        *keystore.Vault
	FFprobePath  string
	SkipHash     bool
	PhotoGeocode *photogeocode.Service
	// FFprobeExtra optional args before the input path (e.g. analyzeduration/probesize for faster scans).
	FFprobeExtra []string
	OnFile       func(path string, err error)
	OnMediaAdded func(mediaID int64, title string, fileType string)
	// OnDocumentScanned is invoked after a document is inserted or updated during scan.
	OnDocumentScanned func(mediaID int64)
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
	libraryType := s.loadLibraryType(libraryID)
	tvLibrary := tvparse.IsTVLibraryType(libraryType)
	musicLibrary := musicparse.IsMusicLibraryType(libraryType)
	photoLibrary := photoparse.IsPhotoLibraryType(libraryType)
	documentLibrary := docparse.IsDocumentLibraryType(libraryType)
	excludePatterns := s.loadScanExcludePatterns(libraryID)
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
				if shouldSkipScanDir(path) {
					return filepath.SkipDir
				}
				return nil
			}
			if shouldSkipScanFile(path) {
				return nil
			}
			st, stErr := os.Stat(path)
			fileSize := int64(0)
			if stErr == nil && st != nil {
				fileSize = st.Size()
			}
			if documentLibrary && docparse.ShouldSkipPath(rel, fileSize, excludePatterns) {
				return nil
			}
			_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", nil)
			ft := fileutil.GuessFileType(path)
			if !photoparse.ShouldScanFile(libraryType, ft) {
				return nil
			}
			normPath := normalizeMediaPath(path)
			if _, exists := seenMedia[normPath]; exists {
				// Same file path encountered again in current scan (e.g. overlapping roots); skip duplicate.
				return nil
			}
			seenMedia[normPath] = struct{}{}
			if linkedID := storage.FindMediaIDByEncryptedPlainPath(s.DB, libraryID, normPath); linkedID > 0 {
				diskMD5 := ""
				if h, hashErr := hashutil.MD5File(path); hashErr == nil {
					diskMD5 = h
				}
				if storage.ShouldLinkEncryptedPlainPathScan(s.DB, linkedID, normPath, diskMD5) {
					_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &linkedID)
					if s.OnFile != nil {
						s.OnFile(path, nil)
					}
					return nil
				}
			}
			curMtime := int64(0)
			if st != nil {
				curMtime = st.ModTime().UTC().Unix()
			}
			var existingMediaID int64
			var existingMtime sql.NullInt64
			if e := s.DB.QueryRow(`SELECT id, file_mtime FROM media WHERE library_id = ? AND lower(file_path) = lower(?) LIMIT 1`, libraryID, normPath).Scan(&existingMediaID, &existingMtime); e == nil && existingMediaID > 0 {
				if existingMtime.Valid && existingMtime.Int64 == curMtime {
					_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &existingMediaID)
					if tvLibrary && ft == "video" {
						s.linkTVIfEpisode(libraryID, existingMediaID, path)
					}
					if musicLibrary && ft == "audio" {
						s.linkMusicIfTrack(libraryID, existingMediaID, path, "")
					}
					if photoLibrary && ft == "image" {
						s.refreshPhotoMeta(existingMediaID, path)
					}
					if documentLibrary && ft == "document" {
						s.refreshDocumentMeta(existingMediaID, path)
					}
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
			var tvInfo *tvparse.EpisodeInfo
			if tvLibrary && ft == "video" {
				if info, ok := tvparse.ParseVideoPath(path); ok {
					tvInfo = &info
					if strings.TrimSpace(info.SeriesTitle) != "" {
						title = tvparse.FormatEpisodeLabel(info)
					}
				}
			}
			fileID := uuid.NewString()
			var dur, w, h, br int
			var format, meta string
			var photoMeta photoparse.PhotoMeta
			var docMeta docparse.DocumentMeta
			if ft == "video" || ft == "audio" {
				if pr, e := storage.ProbePath(s.DB, s.Vault, s.FFprobePath, 0, path, s.FFprobeExtra); e == nil {
					dur = pr.DurationSec
					w = pr.Width
					h = pr.Height
					br = pr.Bitrate
					format = pr.Format
					meta = pr.RawJSON
				}
			} else if ft == "image" {
				photoMeta = photoparse.ParseFromFile(path)
				if s.PhotoGeocode != nil {
					s.PhotoGeocode.EnrichMeta(&photoMeta)
				}
				w = photoMeta.Width
				h = photoMeta.Height
				format = strings.TrimPrefix(photoMeta.MimeType, "image/")
				if strings.TrimSpace(photoMeta.Title) != "" {
					title = photoMeta.Title
				}
			} else if ft == "document" {
				docMeta = docparse.ParseFromFile(path)
				format = docMeta.Format
				if strings.TrimSpace(docMeta.Title) != "" {
					title = docMeta.Title
				}
			}
			var md5sum sql.NullString
			if !s.SkipHash {
				if h, e := hashutil.MD5File(path); e == nil {
					md5sum = sql.NullString{String: h, Valid: true}
					var dupMediaID int64
					var dupPath sql.NullString
					e2 := s.DB.QueryRow(`SELECT id, file_path FROM media WHERE md5 = ? AND library_id = ? LIMIT 1`, h, libraryID).Scan(&dupMediaID, &dupPath)
					if e2 == nil && dupMediaID > 0 && dupPath.Valid && strings.TrimSpace(dupPath.String) != "" {
						oldPath := dupPath.String
						if storage.IsMediaEncrypted(s.DB, dupMediaID, oldPath) {
							_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &dupMediaID)
							if s.OnFile != nil {
								s.OnFile(path, nil)
							}
							return nil
						}
						if normalizeMediaPath(oldPath) != normPath {
							if _, statErr := os.Stat(oldPath); statErr != nil && os.IsNotExist(statErr) {
								_, _ = s.DB.Exec(`UPDATE media SET file_path = ?, file_mtime = ?, status = 'active' WHERE id = ?`, normPath, curMtime, dupMediaID)
								_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &dupMediaID)
								if s.OnFile != nil {
									s.OnFile(path, nil)
								}
								return nil
							}
							// Same content on disk at another path: keep existing record and insert this path as new media.
						}
					}
				}
			}
			if md5sum.Valid {
				if linkedID := storage.FindMediaIDByEncryptedMD5(s.DB, libraryID, md5sum.String); linkedID > 0 {
					_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &linkedID)
					if s.OnFile != nil {
						s.OnFile(path, nil)
					}
					return nil
				}
			}
			metaJSON := meta
			if metaJSON == "" {
				b, _ := json.Marshal(map[string]string{"title": title})
				metaJSON = string(b)
			}
			var musicMeta musicparse.TrackMeta
			if musicLibrary && ft == "audio" {
				musicMeta = musicparse.ParseFromSources(path, meta, dur, br)
				if strings.TrimSpace(musicMeta.Title) != "" {
					title = musicMeta.Title
				}
				metaJSON = musicstore.MergeMusicMetaJSON(metaJSON, musicMeta)
			}
			if tvInfo != nil {
				metaJSON = mergeTVMetaJSON(metaJSON, *tvInfo)
			}
			if photoLibrary && ft == "image" {
				metaJSON = photoparse.MergePhotoMetaJSON(metaJSON, photoMeta)
			}
			if documentLibrary && ft == "document" {
				metaJSON = docparse.MergeDocumentMetaJSON(metaJSON, docMeta)
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
				if tvInfo != nil {
					_ = tvstore.LinkEpisode(s.DB, libraryID, mediaID, *tvInfo)
				}
				if musicLibrary && ft == "audio" {
					_ = musicstore.LinkTrack(s.DB, libraryID, mediaID, musicMeta)
				}
				if documentLibrary && ft == "document" && s.OnDocumentScanned != nil {
					s.OnDocumentScanned(mediaID)
				}
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
				var mid int64
				if s.DB.QueryRow(`SELECT id FROM media WHERE library_id = ? AND file_path = ?`, libraryID, p.String).Scan(&mid) == nil && mid > 0 {
					if storage.MediaFileStillPresentAfterEncrypt(s.DB, mid, p.String, seenMedia) {
						continue
					}
					tvstore.CleanupMedia(s.DB, mid)
					musicstore.CleanupMedia(s.DB, mid)
				}
				_, _ = s.DB.Exec(`DELETE FROM media WHERE library_id = ? AND file_path = ?`, libraryID, p.String)
			}
		}
	}
	if tvLibrary {
		_, _ = tvstore.BackfillLibraryTV(s.DB, libraryID)
		tvstore.PruneOrphansForLibrary(s.DB, libraryID)
	}
	if musicLibrary {
		_ = musicstore.MergeUnknownAlbums(s.DB, libraryID)
		_, _ = musicstore.BackfillLibraryMusic(s.DB, libraryID)
		musicstore.PruneOrphansForLibrary(s.DB, libraryID)
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

func shouldSkipScanDir(path string) bool {
	base := filepath.Base(filepath.Clean(path))
	switch base {
	case ".encrypted", ".knox-encrypted":
		return true
	default:
		return false
	}
}

func shouldSkipScanFile(path string) bool {
	if kcrypto.IsEncFile(path) {
		return true
	}
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for _, part := range parts {
		switch part {
		case ".encrypted", ".knox-encrypted":
			return true
		}
	}
	return false
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
	`, libraryID, parentPath, nodePath, nodeName, nodeType, mid)
	return err
}

func (s *Scanner) loadLibraryType(libraryID int64) string {
	if s == nil || s.DB == nil || libraryID <= 0 {
		return ""
	}
	var t sql.NullString
	if err := s.DB.QueryRow(`SELECT type FROM library WHERE id = ?`, libraryID).Scan(&t); err != nil {
		return ""
	}
	return t.String
}

func (s *Scanner) linkMusicIfTrack(libraryID, mediaID int64, path, ffprobeJSON string) {
	var metaJSON sql.NullString
	_ = s.DB.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&metaJSON)
	raw := strings.TrimSpace(ffprobeJSON)
	if raw == "" && metaJSON.Valid {
		raw = metaJSON.String
	}
	meta := musicstore.DecodeMusicMeta(raw, path)
	_ = musicstore.LinkTrack(s.DB, libraryID, mediaID, meta)
}

func (s *Scanner) refreshPhotoMeta(mediaID int64, path string) {
	var metaJSON sql.NullString
	_ = s.DB.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&metaJSON)
	photoMeta := photoparse.ParseForMedia(s.DB, s.Vault, mediaID, path)
	if s.PhotoGeocode != nil {
		s.PhotoGeocode.EnrichMeta(&photoMeta)
	}
	merged := photoparse.MergePhotoMetaJSON(metaJSON.String, photoMeta)
	_, _ = s.DB.Exec(`
		UPDATE media SET width = ?, height = ?, meta_json = ?, format = ?
		WHERE id = ?`,
		nullInt(photoMeta.Width), nullInt(photoMeta.Height), merged, nullStringVal(strings.TrimPrefix(photoMeta.MimeType, "image/")), mediaID,
	)
}

func (s *Scanner) refreshDocumentMeta(mediaID int64, path string) {
	var metaJSON sql.NullString
	_ = s.DB.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&metaJSON)
	docMeta := docparse.ParseForMedia(s.DB, s.Vault, mediaID, path)
	merged := docparse.MergeDocumentMetaJSON(metaJSON.String, docMeta)
	title := docparse.PickDocumentTitle(path, docMeta.Title)
	_, _ = s.DB.Exec(`
		UPDATE media SET meta_json = ?, format = ?, title = ?
		WHERE id = ?`,
		merged, nullStringVal(docMeta.Format), title, mediaID,
	)
}

func (s *Scanner) loadScanExcludePatterns(libraryID int64) []string {
	if s == nil || s.DB == nil || libraryID <= 0 {
		return nil
	}
	var raw sql.NullString
	if err := s.DB.QueryRow(`SELECT COALESCE(scan_exclude_patterns, '') FROM library WHERE id = ?`, libraryID).Scan(&raw); err != nil {
		return nil
	}
	return docparse.ParseExcludePatterns(raw.String)
}

func (s *Scanner) linkTVIfEpisode(libraryID, mediaID int64, path string) {
	var meta sql.NullString
	_ = s.DB.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&meta)
	info, ok := tvparse.ParseEpisodeFromMedia(path, meta.String)
	if ok {
		_ = tvstore.LinkEpisode(s.DB, libraryID, mediaID, info)
	}
}

func mergeTVMetaJSON(raw string, info tvparse.EpisodeInfo) string {
	var root map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	tv := map[string]any{
		"series_title": info.SeriesTitle,
		"season":       info.SeasonNum,
		"episode":      info.EpisodeNum,
		"is_special":   info.IsSpecial,
	}
	if info.Year > 0 {
		tv["year"] = info.Year
	}
	if info.TMDBID != "" {
		tv["tmdb_id"] = info.TMDBID
	}
	if info.TVDBID != "" {
		tv["tvdb_id"] = info.TVDBID
	}
	if info.EpisodeTitle != "" {
		tv["episode_title"] = info.EpisodeTitle
	}
	if info.SourceFolder != "" {
		tv["source_folder"] = info.SourceFolder
	}
	root["tv"] = tv
	b, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(b)
}

func pad2(n int) string {
	if n < 10 {
		return fmt.Sprintf("0%d", n)
	}
	return fmt.Sprintf("%d", n)
}

func padEp(n int) string {
	if n < 10 {
		return fmt.Sprintf("0%d", n)
	}
	return fmt.Sprintf("%d", n)
}
