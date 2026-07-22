package scanner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"knox-media/internal/coreiface"
	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/docparse"
	"knox-media/internal/keystore"
	"knox-media/internal/mediastore"
	"knox-media/internal/musicparse"
	"knox-media/internal/musicstore"
	"knox-media/internal/photogeocode"
	"knox-media/internal/photoparse"
	"knox-media/internal/phototags"
	"knox-media/internal/relationshipsync"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"knox-media/internal/tvparse"
	"knox-media/internal/tvstore"
	"knox-media/pkg/ffprobe"
	"knox-media/pkg/fileutil"
	"knox-media/pkg/hashutil"
)

type Scanner struct {
	DB            *sql.DB
	Vault         *keystore.Vault
	FFprobePath   string
	SkipHash      bool
	PhotoGeocode  *photogeocode.Service
	PhotoCacheDir string
	CleanupRoots  []string
	// FFprobeExtra optional args before the input path (e.g. analyzeduration/probesize for faster scans).
	FFprobeExtra []string
	OnFile       func(path string, err error)
	OnMediaAdded func(mediaID int64, title string, fileType string)
	ProbePath    func(context.Context, int64, string) (*ffprobe.Summary, error)
	ParsePhoto   func(string) (photoparse.PhotoMeta, []error)
	// OnMediaRemoved is invoked after a stale catalog row is removed during sync.
	OnMediaRemoved func(mediaID int64, filePath string)
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
	var callback func(context.Context, int64, string, string) error
	if s.OnMediaAdded != nil {
		callback = func(_ context.Context, mediaID int64, title, fileType string) error {
			s.OnMediaAdded(mediaID, title, fileType)
			return nil
		}
	}
	return s.ScanLibraryFoldersWithContextAndMediaAdded(ctx, libraryID, roots, callback)
}

type MetadataDiagnostic struct {
	Source  string
	Message string
}

type MetadataAttempt struct {
	Attempted bool
	Fields    []string
	Errors    []MetadataDiagnostic
}

type ScanDiscovery struct {
	MediaID         int64
	Title           string
	FileType        string
	MetadataAttempt MetadataAttempt
}

type ScanCallbacks struct {
	OnFile              func(string, error)
	OnMediaAdded        func(context.Context, int64, string, string) error
	OnMediaDiscoveredTx func(context.Context, *sql.Tx, ScanDiscovery) error
}

func (s *Scanner) ScanLibraryFoldersWithContextAndMediaAdded(ctx context.Context, libraryID int64, roots []string, onMediaAdded func(context.Context, int64, string, string) error) (added int, err error) {
	return s.ScanLibraryFoldersWithContextAndCallbacks(ctx, libraryID, roots, ScanCallbacks{OnFile: s.OnFile, OnMediaAdded: onMediaAdded})
}

func (s *Scanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, libraryID int64, roots []string, callbacks ScanCallbacks) (added int, err error) {
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
	pretranscodeRoots := s.loadPretranscodeOutputRoots()
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
				if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
					return walkErr
				}
				if callbacks.OnFile != nil {
					callbacks.OnFile(path, walkErr)
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
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
				if shouldSkipScanDir(path) || shouldSkipPretranscodePath(path, pretranscodeRoots) {
					return filepath.SkipDir
				}
				return nil
			}
			if shouldSkipScanFile(path) || shouldSkipPretranscodePath(path, pretranscodeRoots) {
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
					if callbacks.OnFile != nil {
						callbacks.OnFile(path, nil)
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
					if callbacks.OnFile != nil {
						callbacks.OnFile(path, nil)
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
			metadataAttempt := MetadataAttempt{}
			if ft == "video" || ft == "audio" {
				probe := s.ProbePath
				if probe == nil {
					probe = func(_ context.Context, mediaID int64, path string) (*ffprobe.Summary, error) {
						return storage.ProbePath(s.DB, s.Vault, s.FFprobePath, mediaID, path, s.FFprobeExtra)
					}
				}
				metadataAttempt.Attempted = true
				pr, probeErr := probe(ctx, 0, path)
				if pr != nil {
					dur = pr.DurationSec
					w = pr.Width
					h = pr.Height
					br = pr.Bitrate
					format = pr.Format
					meta = pr.RawJSON
					metadataAttempt.Fields = probeMetadataFields(pr)
				}
				if probeErr != nil {
					metadataAttempt.addError("probe", probeErr)
				}
			} else if ft == "image" {
				metadataAttempt.Attempted = true
				parsePhoto := s.ParsePhoto
				if parsePhoto == nil {
					parsePhoto = photoparse.ParseFromFileWithDiagnostics
				}
				var photoErrors []error
				photoMeta, photoErrors = parsePhoto(path)
				for _, photoErr := range photoErrors {
					metadataAttempt.addError("photo", photoErr)
				}
				if s.PhotoGeocode != nil {
					s.PhotoGeocode.EnrichMeta(&photoMeta)
				}
				metadataAttempt.Fields = photoMetadataFields(photoMeta)
				w = photoMeta.Width
				h = photoMeta.Height
				format = strings.TrimPrefix(photoMeta.MimeType, "image/")
				if strings.TrimSpace(photoMeta.Title) != "" {
					title = photoMeta.Title
				}
			} else if ft == "document" {
				metadataAttempt.Attempted = true
				docMeta = docparse.ParseFromFile(path)
				format = docMeta.Format
				metadataAttempt.Fields = documentMetadataFields(docMeta)
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
							if callbacks.OnFile != nil {
								callbacks.OnFile(path, nil)
							}
							return nil
						}
						if normalizeMediaPath(oldPath) != normPath {
							if _, statErr := os.Stat(oldPath); statErr != nil && os.IsNotExist(statErr) {
								_, _ = s.DB.Exec(`UPDATE media SET file_path = ?, file_mtime = ?, status = 'active' WHERE id = ?`, normPath, curMtime, dupMediaID)
								_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &dupMediaID)
								if callbacks.OnFile != nil {
									callbacks.OnFile(path, nil)
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
					if callbacks.OnFile != nil {
						callbacks.OnFile(path, nil)
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
			metaJSON = phototags.NormalizeMetaJSON(metaJSON)
			if existingMediaID == 0 {
				if adopted := s.tryAdoptRenamedMedia(libraryID, normPath, seenMedia, ft, dur, w, h, md5sum); adopted > 0 {
					existingMediaID = adopted
				}
			}
			tx, e := s.DB.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			var res sql.Result
			if existingMediaID > 0 {
				res, e = tx.ExecContext(ctx, `
				UPDATE media
				SET library_id = ?, title = ?, file_path = ?, file_type = ?, duration = ?, width = ?, height = ?, bitrate = ?, md5 = ?, format = ?, meta_json = ?, photo_taken_at = CASE WHEN ? = 'image' THEN COALESCE(?, created_at_sort) ELSE photo_taken_at END, photo_place_id = CASE WHEN ? = 'image' THEN NULLIF(?, '') ELSE photo_place_id END, status = 'active', file_mtime = ?
				WHERE id = ?`,
					libraryID, title, normPath, ft, nullInt(dur), nullInt(w), nullInt(h), nullInt(br), nullString(md5sum), nullStringVal(format), metaJSON, ft, photoTimelineValue(metaJSON, existingMediaID), ft, store.PhotoPlaceID(metaJSON), curMtime, existingMediaID,
				)
			} else {
				res, e = tx.ExecContext(ctx, `
				INSERT INTO media (library_id, file_id, title, file_path, file_type, duration, width, height, bitrate, md5, format, meta_json, status, file_mtime, created_at_sort, photo_taken_at, photo_place_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, strftime('%Y-%m-%dT%H:%M:%f000Z','now'), CASE WHEN ? = 'image' THEN COALESCE(?, strftime('%Y-%m-%dT%H:%M:%f000Z','now')) END, NULLIF(?, ''))`,
					libraryID, fileID, title, normPath, ft, nullInt(dur), nullInt(w), nullInt(h), nullInt(br), nullString(md5sum), nullStringVal(format), metaJSON, curMtime, ft, photoTimelineValue(metaJSON, 0), store.PhotoPlaceID(metaJSON),
				)
			}
			if e != nil {
				if strings.Contains(e.Error(), "UNIQUE") {
					return nil
				}
				if callbacks.OnFile != nil {
					callbacks.OnFile(path, e)
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
				if tvLibrary || musicLibrary {
					if e = relationshipsync.SyncTx(ctx, tx, mediaID); e != nil {
						return e
					}
				}
				if existingMediaID == 0 && callbacks.OnMediaDiscoveredTx != nil {
					if e = callbacks.OnMediaDiscoveredTx(ctx, tx, ScanDiscovery{MediaID: mediaID, Title: title, FileType: ft, MetadataAttempt: metadataAttempt}); e != nil {
						return e
					}
				}
				if e = tx.Commit(); e != nil {
					return e
				}
				_ = s.upsertNode(libraryID, parentPath, nodePath, nodeName, "file", &mediaID)
				if documentLibrary && ft == "document" && s.OnDocumentScanned != nil {
					s.OnDocumentScanned(mediaID)
				}
				if existingMediaID == 0 && callbacks.OnMediaAdded != nil {
					if callbackErr := callbacks.OnMediaAdded(ctx, mediaID, title, ft); callbackErr != nil && callbacks.OnFile != nil {
						callbacks.OnFile(path, callbackErr)
					}
				}
			}
			if callbacks.OnFile != nil {
				callbacks.OnFile(path, nil)
			}
			return nil
		})
		if err != nil {
			return added, err
		}
	}
	s.syncMissingMedia(ctx, libraryID, cleanRoots, seenMedia, callbacks.OnFile)
	if tvLibrary {
		if err := tvstore.PruneOrphansForLibraryContext(ctx, s.DB, libraryID); err != nil {
			return added, err
		}
	}
	if musicLibrary {
		if err := musicstore.PruneOrphansForLibraryContext(ctx, s.DB, libraryID); err != nil {
			return added, err
		}
	}
	return added, nil
}

const maxMetadataDiagnosticMessage = 512

func (a *MetadataAttempt) addError(source string, err error) {
	if err == nil || len(a.Errors) >= 8 {
		return
	}
	message := err.Error()
	if len(message) > maxMetadataDiagnosticMessage {
		message = message[:maxMetadataDiagnosticMessage]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	a.Errors = append(a.Errors, MetadataDiagnostic{Source: source, Message: message})
}

func probeMetadataFields(pr *ffprobe.Summary) []string {
	fields := make([]string, 0, 6)
	if pr.DurationSec > 0 {
		fields = append(fields, "duration")
	}
	if pr.Width > 0 {
		fields = append(fields, "width")
	}
	if pr.Height > 0 {
		fields = append(fields, "height")
	}
	if pr.Bitrate > 0 {
		fields = append(fields, "bitrate")
	}
	if strings.TrimSpace(pr.Format) != "" {
		fields = append(fields, "format")
	}
	if strings.TrimSpace(pr.RawJSON) != "" {
		fields = append(fields, "meta_json")
	}
	return fields
}

func photoMetadataFields(meta photoparse.PhotoMeta) []string {
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil
	}
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func documentMetadataFields(meta docparse.DocumentMeta) []string {
	fields := make([]string, 0, 2)
	if strings.TrimSpace(meta.Title) != "" {
		fields = append(fields, "title")
	}
	if strings.TrimSpace(meta.Format) != "" {
		fields = append(fields, "format")
	}
	return fields
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

func shouldSkipPretranscodePath(path string, dbRoots []string) bool {
	if fileutil.IsPretranscodeOutputPath(path) {
		return true
	}
	return pathUnderPretranscodeRoots(path, dbRoots)
}

func pathUnderPretranscodeRoots(path string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	norm := normalizeMediaPath(path)
	if norm == "" {
		return false
	}
	sep := string(filepath.Separator)
	for _, root := range roots {
		root = normalizeMediaPath(root)
		if root == "" {
			continue
		}
		if norm == root || strings.HasPrefix(norm, root+sep) {
			return true
		}
	}
	return false
}

func (s *Scanner) loadPretranscodeOutputRoots() []string {
	if s == nil || s.DB == nil {
		return nil
	}
	rows, err := s.DB.Query(`SELECT DISTINCT COALESCE(output_path,'') FROM pretranscode_task_meta WHERE COALESCE(output_path,'') != ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	roots := make([]string, 0, 8)
	for rows.Next() {
		var root string
		if rows.Scan(&root) != nil {
			continue
		}
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		roots = append(roots, root)
	}
	return roots
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

func updateScannerMediaMeta(ctx context.Context, db *sql.DB, mediaID int64, metaJSON string, update func(*sql.Tx) error) error {
	metaJSON = phototags.NormalizeMetaJSON(metaJSON)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if update != nil {
		if err := update(tx); err != nil {
			return err
		}
	}
	if err := store.UpdateMediaMetaAndPhotoTime(ctx, tx, mediaID, metaJSON); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Scanner) refreshPhotoMeta(mediaID int64, path string) {
	var metaJSON sql.NullString
	_ = s.DB.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&metaJSON)
	photoMeta := photoparse.ParseForMedia(s.DB, s.Vault, mediaID, path)
	if s.PhotoGeocode != nil {
		s.PhotoGeocode.EnrichMeta(&photoMeta)
	}
	merged := photoparse.MergePhotoMetaJSON(metaJSON.String, photoMeta)
	err := updateScannerMediaMeta(context.Background(), s.DB, mediaID, merged, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE media SET width=?,height=?,format=? WHERE id=?`, nullInt(photoMeta.Width), nullInt(photoMeta.Height), nullStringVal(strings.TrimPrefix(photoMeta.MimeType, "image/")), mediaID)
		return err
	})
	if err != nil {
		log.Printf("scanner photo metadata media=%d: %v", mediaID, err)
	}
}

func (s *Scanner) refreshDocumentMeta(mediaID int64, path string) {
	var metaJSON sql.NullString
	_ = s.DB.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&metaJSON)
	docMeta := docparse.ParseForMedia(s.DB, s.Vault, mediaID, path)
	merged := docparse.MergeDocumentMetaJSON(metaJSON.String, docMeta)
	title := docparse.PickDocumentTitle(path, docMeta.Title)
	err := updateScannerMediaMeta(context.Background(), s.DB, mediaID, merged, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE media SET format = ?, title = ? WHERE id = ?`, nullStringVal(docMeta.Format), title, mediaID)
		return err
	})
	if err != nil {
		log.Printf("scanner document metadata media=%d: %v", mediaID, err)
	}
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

func (s *Scanner) syncMissingMedia(ctx context.Context, libraryID int64, roots []string, seenMedia map[string]struct{}, onFile func(string, error)) {
	if s == nil || s.DB == nil || libraryID <= 0 {
		return
	}
	rows, err := s.DB.Query(`SELECT id, COALESCE(file_id,''), COALESCE(file_path,'') FROM media WHERE library_id = ?`, libraryID)
	if err != nil {
		return
	}
	type staleMedia struct {
		id       int64
		fileID   string
		filePath string
	}
	var stale []staleMedia
	for rows.Next() {
		var mediaID int64
		var fileID, filePath string
		if rows.Scan(&mediaID, &fileID, &filePath) != nil || mediaID <= 0 || strings.TrimSpace(filePath) == "" {
			continue
		}
		if _, ok := seenMedia[normalizeMediaPath(filePath)]; ok {
			continue
		}
		if !s.mediaPathUnderRoots(filePath, roots) {
			continue
		}
		if storage.MediaFileStillPresentAfterEncrypt(s.DB, mediaID, filePath, seenMedia) {
			continue
		}
		if mediaPathExistsOnDisk(filePath) {
			continue
		}
		stale = append(stale, staleMedia{id: mediaID, fileID: fileID, filePath: filePath})
	}
	_ = rows.Close()

	for _, item := range stale {
		if mod := coreiface.PretranscodeModuleHandle(); mod != nil && strings.TrimSpace(item.fileID) != "" {
			_ = mod.OnMediaDeleted(ctx, item.id, []string{item.fileID})
		}
		tvstore.CleanupMedia(s.DB, item.id)
		musicstore.CleanupMedia(s.DB, item.id)
		cleanup, err := mediastore.DeleteCatalogAndCollect(ctx, s.DB, item.id, item.fileID, s.PhotoCacheDir)
		if err == nil {
			err = mediastore.CleanupFiles(ctx, s.DB, cleanup, append([]string{filepath.Dir(s.PhotoCacheDir)}, s.CleanupRoots...))
		}
		if err != nil {
			if onFile != nil {
				onFile(item.filePath, err)
			}
			continue
		}
		if s.OnMediaRemoved != nil {
			s.OnMediaRemoved(item.id, item.filePath)
		}
	}
}

func (s *Scanner) mediaPathUnderRoots(filePath string, roots []string) bool {
	norm := normalizeMediaPath(filePath)
	if norm == "" {
		return false
	}
	sep := string(filepath.Separator)
	for _, root := range roots {
		rootNorm := normalizeMediaPath(root)
		if rootNorm == "" {
			continue
		}
		if norm == rootNorm || strings.HasPrefix(norm, rootNorm+sep) {
			return true
		}
	}
	return false
}

func mediaPathExistsOnDisk(filePath string) bool {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return false
	}
	if _, err := os.Stat(filePath); err == nil {
		return true
	}
	return false
}

func (s *Scanner) tryAdoptRenamedMedia(libraryID int64, normPath string, seenMedia map[string]struct{}, ft string, dur, w, h int, md5sum sql.NullString) int64 {
	candidate := s.findRenamedMediaCandidate(libraryID, normPath, seenMedia, ft, dur, w, h, md5sum)
	if candidate <= 0 {
		return 0
	}
	_, _ = s.DB.Exec(`UPDATE media SET file_path = ?, status = 'active' WHERE id = ?`, normPath, candidate)
	_, _ = s.DB.Exec(`UPDATE media_encrypted_assets SET plain_path = ? WHERE media_id = ? AND status = 'encrypted'`, normPath, candidate)
	return candidate
}

func (s *Scanner) findRenamedMediaCandidate(libraryID int64, normPath string, seenMedia map[string]struct{}, ft string, dur, w, h int, md5sum sql.NullString) int64 {
	if s == nil || s.DB == nil || libraryID <= 0 || normPath == "" {
		return 0
	}
	if md5sum.Valid {
		var id int64
		var oldPath string
		if err := s.DB.QueryRow(`SELECT id, file_path FROM media WHERE library_id = ? AND md5 = ? LIMIT 1`, libraryID, md5sum.String).Scan(&id, &oldPath); err == nil && id > 0 {
			if normalizeMediaPath(oldPath) != normPath && !mediaPathExistsOnDisk(oldPath) {
				return id
			}
		}
	}
	if ft != "video" && ft != "audio" {
		return 0
	}
	if dur <= 0 && w <= 0 && h <= 0 {
		return 0
	}
	rows, err := s.DB.Query(`SELECT id, file_path, COALESCE(duration,0), COALESCE(width,0), COALESCE(height,0) FROM media WHERE library_id = ? AND file_type = ?`, libraryID, ft)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var candidate int64
	for rows.Next() {
		var id, rowDur, rowW, rowH int
		var oldPath string
		if rows.Scan(&id, &oldPath, &rowDur, &rowW, &rowH) != nil || id <= 0 {
			continue
		}
		if normalizeMediaPath(oldPath) == normPath {
			continue
		}
		if _, ok := seenMedia[normalizeMediaPath(oldPath)]; ok {
			continue
		}
		if mediaPathExistsOnDisk(oldPath) {
			continue
		}
		if !metadataMatchesForRename(dur, w, h, rowDur, rowW, rowH) {
			continue
		}
		if candidate > 0 {
			return 0
		}
		candidate = int64(id)
	}
	return candidate
}

func metadataMatchesForRename(dur, w, h, rowDur, rowW, rowH int) bool {
	matched := 0
	required := 0
	if dur > 0 {
		required++
		if rowDur == dur {
			matched++
		}
	}
	if w > 0 {
		required++
		if rowW == w {
			matched++
		}
	}
	if h > 0 {
		required++
		if rowH == h {
			matched++
		}
	}
	return required > 0 && matched == required
}

func photoTimelineValue(metaJSON string, fallbackID int64) any {
	value, _ := store.PhotoTimelineTime(metaJSON, "", fallbackID)
	if value == "" {
		return nil
	}
	return value
}
