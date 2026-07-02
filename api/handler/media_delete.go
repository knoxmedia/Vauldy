package handler

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/musicstore"
	"knox-media/internal/tvstore"
)

var sidecarDeleteExts = map[string]struct{}{
	".srt": {}, ".ass": {}, ".ssa": {}, ".vtt": {}, ".sub": {},
	".idx": {}, ".sup": {},
}

type mediaDeleteInfo struct {
	ID         int64
	LibraryID  int64
	FileID     string
	FilePath   string
	AbsMain    string
}

func (h *Handler) loadMediaDeleteInfo(id int64) (mediaDeleteInfo, error) {
	var info mediaDeleteInfo
	var libID sql.NullInt64
	var fileID, filePath sql.NullString
	if err := h.App.DB.QueryRow(
		`SELECT id, library_id, file_id, file_path FROM media WHERE id = ?`, id,
	).Scan(&info.ID, &libID, &fileID, &filePath); err != nil {
		return info, err
	}
	info.LibraryID = libID.Int64
	info.FileID = strings.TrimSpace(fileID.String)
	info.FilePath = strings.TrimSpace(filePath.String)
	info.AbsMain = h.resolveMediaAbsolutePath(info.LibraryID, info.FilePath)
	return info, nil
}

func (h *Handler) collectMediaDeletionPlan(info mediaDeleteInfo) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if info.FilePath != "" {
		add(info.FilePath)
	}
	for _, p := range discoverSidecarPaths(info.AbsMain) {
		add(toDisplayPath(info.LibraryID, p, h))
	}
	if h != nil && h.App != nil {
		uploadDir := strings.TrimSpace(h.App.Config.Data.Upload)
		if uploadDir != "" {
			poster := filepath.Join(uploadDir, "posters", strconv.FormatInt(info.ID, 10)+".jpg")
			if st, err := os.Stat(poster); err == nil && !st.IsDir() {
				add("/uploads/posters/" + strconv.FormatInt(info.ID, 10) + ".jpg")
			}
		}
	}
	return out
}

func toDisplayPath(libraryID int64, absPath string, h *Handler) string {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return ""
	}
	if libraryID > 0 && h != nil && h.App != nil {
		var libPath string
		if err := h.App.DB.QueryRow(`SELECT path FROM library WHERE id = ?`, libraryID).Scan(&libPath); err == nil {
			libPath = filepath.Clean(strings.TrimSpace(libPath))
			if libPath != "" {
				rel, err := filepath.Rel(libPath, absPath)
				if err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
					return filepath.ToSlash(rel)
				}
			}
		}
	}
	return filepath.ToSlash(absPath)
}

func discoverSidecarPaths(mainAbs string) []string {
	mainAbs = strings.TrimSpace(mainAbs)
	if mainAbs == "" {
		return nil
	}
	dir := filepath.Dir(mainAbs)
	base := strings.TrimSuffix(filepath.Base(mainAbs), filepath.Ext(mainAbs))
	if base == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		ext := strings.ToLower(filepath.Ext(name))
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if stem != base {
			continue
		}
		if _, ok := sidecarDeleteExts[ext]; ok || ext == ".nfo" {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
}

func (h *Handler) collectMediaDeletionAbsPaths(info mediaDeleteInfo) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if info.AbsMain != "" {
		add(info.AbsMain)
	}
	for _, p := range discoverSidecarPaths(info.AbsMain) {
		add(p)
	}
	if h != nil && h.App != nil {
		cfg := h.App.Config.Data
		uploadDir := strings.TrimSpace(cfg.Upload)
		if uploadDir != "" {
			add(filepath.Join(uploadDir, "posters", strconv.FormatInt(info.ID, 10)+".jpg"))
		}
	}
	return out
}

func (h *Handler) collectMediaDeletionDirs(info mediaDeleteInfo) []string {
	if h == nil || h.App == nil {
		return nil
	}
	cfg := h.App.Config.Data
	idStr := strconv.FormatInt(info.ID, 10)
	var dirs []string
	for _, base := range []string{
		strings.TrimSpace(cfg.Subtitle),
		strings.TrimSpace(cfg.Preview),
		strings.TrimSpace(cfg.ATracks),
	} {
		if base == "" {
			continue
		}
		d := filepath.Join(base, idStr)
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
		}
	}
	var outputPath sql.NullString
	_ = h.App.DB.QueryRow(`SELECT output_path FROM package_task WHERE media_id = ? AND output_path IS NOT NULL AND trim(output_path) != '' ORDER BY id DESC LIMIT 1`, info.ID).Scan(&outputPath)
	if outputPath.Valid && strings.TrimSpace(outputPath.String) != "" {
		dirs = append(dirs, filepath.Clean(outputPath.String))
	}
	var manifestPath sql.NullString
	_ = h.App.DB.QueryRow(`SELECT manifest_path FROM drm_asset WHERE media_id = ?`, info.ID).Scan(&manifestPath)
	if manifestPath.Valid && strings.TrimSpace(manifestPath.String) != "" {
		dirs = append(dirs, filepath.Dir(filepath.Clean(manifestPath.String)))
	}
	derivedBase := filepath.Join(strings.TrimSpace(cfg.Dir), ".derived", idStr)
	if st, err := os.Stat(derivedBase); err == nil && st.IsDir() {
		dirs = append(dirs, derivedBase)
	}
	return dirs
}

func (h *Handler) deleteMediaRecords(id int64, fileID string) error {
	tx, err := h.App.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM favorite WHERE media_id = ?`, []any{id}},
		{`DELETE FROM favorite_folder_item WHERE media_id = ?`, []any{id}},
		{`DELETE FROM playlist_item WHERE media_id = ?`, []any{id}},
		{`DELETE FROM scrape_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM scrape_history WHERE media_id = ?`, []any{id}},
		{`DELETE FROM media_subtitle WHERE media_id = ?`, []any{id}},
		{`DELETE FROM subtitle_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM lyric_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM atrack_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM keyframe_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM preview_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM media_derived_assets WHERE media_id = ?`, []any{id}},
		{`DELETE FROM package_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM drm_license_audit WHERE media_id = ?`, []any{id}},
		{`DELETE FROM drm_key_material WHERE media_id = ?`, []any{id}},
		{`DELETE FROM drm_asset WHERE media_id = ?`, []any{id}},
		{`DELETE FROM library_node WHERE media_id = ?`, []any{id}},
		{`DELETE FROM music_track WHERE media_id = ?`, []any{id}},
		{`DELETE FROM episode_media WHERE media_id = ?`, []any{id}},
		{`DELETE FROM photo_face WHERE media_id = ?`, []any{id}},
		{`DELETE FROM photo_face_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM photo_classify_task WHERE media_id = ?`, []any{id}},
		{`DELETE FROM photo_location_task WHERE media_id = ?`, []any{id}},
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s.q, s.args...); err != nil {
			return err
		}
	}
	if fileID != "" {
		if _, err := tx.Exec(`DELETE FROM play_progress WHERE file_id = ?`, fileID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM media WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (h *Handler) purgeMediaFiles(info mediaDeleteInfo) {
	if h.DerivedStore != nil {
		_ = h.DerivedStore.DeleteForMedia(context.Background(), info.ID)
	}
	for _, p := range h.collectMediaDeletionAbsPaths(info) {
		_ = os.Remove(p)
	}
	for _, d := range h.collectMediaDeletionDirs(info) {
		_ = os.RemoveAll(d)
	}
}

func (h *Handler) GetMediaDeletionPlan(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	info, err := h.loadMediaDeleteInfo(id)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": h.collectMediaDeletionPlan(info)})
}

func (h *Handler) DeleteMedia(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	info, err := h.loadMediaDeleteInfo(id)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	libraryID := info.LibraryID
	if err := h.deleteMediaRecords(info.ID, info.FileID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.purgeMediaFiles(info)
	h.scheduleLibraryPreviewRefresh(libraryID)
	if libraryID > 0 {
		musicstore.PruneOrphansForLibrary(h.App.DB, libraryID)
		tvstore.PruneOrphansForLibrary(h.App.DB, libraryID)
	}
	mid := info.ID
	h.logActivity(middleware.UserID(c), middleware.Username(c), "media.delete", &mid, info.FilePath)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
