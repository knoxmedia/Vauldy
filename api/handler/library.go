package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/storage"
)

type libraryBody struct {
	Name                            string   `json:"name" binding:"required"`
	Type                            string   `json:"type" binding:"required"`
	Path                            string   `json:"path"`
	Folders                         []string `json:"folders"`
	AutoScan                        *int     `json:"auto_scan"`
	Enabled                         *int     `json:"enabled"`
	RealtimeMonitor                 *int     `json:"realtime_monitor"`
	PreviewExtract                  *int     `json:"preview_extract"`
	DRMEnabled                      *int     `json:"drm_enabled"`
	EncryptionMode                  string   `json:"encryption_mode"`
	CleanupLocalSource              *int     `json:"cleanup_local_source_after_package"`
	MetadataProviders               []string `json:"metadata_providers"`
	ImageProviders                  []string `json:"image_providers"`
	MetadataRefreshPolicy           string   `json:"metadata_refresh_policy"`
	Scraper                         string   `json:"scraper"`
	JITPrepareOnIngest              *int     `json:"jit_prepare_on_ingest"`
	EncryptedAssetsEnabled          *int     `json:"encrypted_assets_enabled"`
	EncryptedAssetsCleanupPlaintext *int     `json:"encrypted_assets_cleanup_plaintext"`
	EncryptedAssetsDirMode          string   `json:"encrypted_assets_dir_mode"`
	EncryptedAssetsCustomDir        string   `json:"encrypted_assets_custom_dir"`
	ScanExcludePatterns             string   `json:"scan_exclude_patterns"`
}

type libraryListRow struct {
	id, auto, enabled, realtime, preview, drmEnabled, cleanupLocal, jitIngest, encAssets, encCleanupPlain, mediaCount                                      int
	scanTaskID, scanProcessed, scanTotal, scanAdded                                                                                                        int64
	scanIngested, scanIngesting                                                                                                                                                        int
	name, typ, path, encryptionMode, encDirMode, encCustomDir, metadataProviders, imageProviders, refreshPolicy, scraper, created, scanStatus, scanStarted string
}

func (h *Handler) ListLibraries(c *gin.Context) {
	widevineEnabled, powerdrmEnabled := h.drmCapabilities()
	var profile userPermissionProfile
	isAdmin := middleware.IsAdmin(c)
	if !middleware.IsAPIClient(c) {
		uid := middleware.UserID(c)
		if uid > 0 {
			p, err := h.loadUserPermissionProfile(uid)
			if err == nil {
				profile = p
			}
		}
	}
	folderMap, err := libraryFoldersByID(h.App.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rows, err := h.App.DB.Query(`
		SELECT l.id, l.name, l.type, l.path, l.auto_scan, l.enabled, l.realtime_monitor, l.preview_extract, l.drm_enabled, COALESCE(l.encryption_mode,'drm'), l.cleanup_local_source_after_package, l.jit_prepare_on_ingest, COALESCE(l.encrypted_assets_enabled,0), COALESCE(l.encrypted_assets_cleanup_plaintext,0), COALESCE(l.encrypted_assets_dir_mode,'library'), COALESCE(l.encrypted_assets_custom_dir,''), l.metadata_providers, l.image_providers, l.metadata_refresh_policy, l.scraper, l.created_at,
			(SELECT COUNT(1) FROM media m WHERE m.library_id = l.id AND m.publication_state IN ('published','degraded')) AS media_count,
			(SELECT id FROM scan_task st WHERE st.library_id = l.id ORDER BY st.id DESC LIMIT 1) AS scan_task_id,
			(SELECT COALESCE(status,'') FROM scan_task st WHERE st.library_id = l.id ORDER BY st.id DESC LIMIT 1) AS scan_status,
			(SELECT COALESCE(processed_count,0) FROM scan_task st WHERE st.library_id = l.id ORDER BY st.id DESC LIMIT 1) AS scan_processed_count,
			(SELECT COALESCE(total_count,0) FROM scan_task st WHERE st.library_id = l.id ORDER BY st.id DESC LIMIT 1) AS scan_total_count,
			(SELECT COALESCE(added_count,0) FROM scan_task st WHERE st.library_id = l.id ORDER BY st.id DESC LIMIT 1) AS scan_added_count,
			(SELECT COALESCE(started_at,'') FROM scan_task st WHERE st.library_id = l.id ORDER BY st.id DESC LIMIT 1) AS scan_started_at
		FROM library l ORDER BY l.id`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	visible := make([]libraryListRow, 0)
	for rows.Next() {
		var r libraryListRow
		var scanTaskID, scanProcessed, scanTotal, scanAdded sql.NullInt64
		var scanStatus, scanStarted sql.NullString
		if err := rows.Scan(&r.id, &r.name, &r.typ, &r.path, &r.auto, &r.enabled, &r.realtime, &r.preview, &r.drmEnabled, &r.encryptionMode, &r.cleanupLocal, &r.jitIngest, &r.encAssets, &r.encCleanupPlain, &r.encDirMode, &r.encCustomDir, &r.metadataProviders, &r.imageProviders, &r.refreshPolicy, &r.scraper, &r.created, &r.mediaCount, &scanTaskID, &scanStatus, &scanProcessed, &scanTotal, &scanAdded, &scanStarted); err != nil {
			continue
		}
		r.scanTaskID, r.scanProcessed, r.scanTotal, r.scanAdded = scanTaskID.Int64, scanProcessed.Int64, scanTotal.Int64, scanAdded.Int64
		r.scanStatus, r.scanStarted = scanStatus.String, scanStarted.String
		if strings.EqualFold(profile.LibraryScope, "selected") {
			if _, ok := profile.AllowedLibraryIDs[int64(r.id)]; !ok {
				continue
			}
		}
		if !isAdmin && !middleware.IsAPIClient(c) && r.enabled != 1 {
			continue
		}
		visible = append(visible, r)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Fetch ingest progress for libraries with a scan task.
	scanTaskIDs := make([]int64, 0, len(visible))
	for _, r := range visible {
		if r.scanTaskID > 0 {
			scanTaskIDs = append(scanTaskIDs, r.scanTaskID)
		}
	}
	if len(scanTaskIDs) > 0 {
		ctx := c.Request.Context()
		ingestQuery := `SELECT r.scan_task_id,SUM(CASE WHEN r.status IN ('published','degraded','failed','cancelled') THEN 1 ELSE 0 END),SUM(CASE WHEN r.status='processing' THEN 1 ELSE 0 END) FROM media_ingest_run r WHERE r.scan_task_id IN (` + sqlPlaceholders(len(scanTaskIDs)) + `) AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL GROUP BY r.scan_task_id`
		ingestArgs := make([]any, len(scanTaskIDs))
		for i, id := range scanTaskIDs {
			ingestArgs[i] = id
		}
		ingestRows, err := h.App.DB.QueryContext(ctx, ingestQuery, ingestArgs...)
		if err == nil {
			ingestMap := map[int64][2]int{}
			for ingestRows.Next() {
				var taskID int64
				var ingested, ingesting int
				if err := ingestRows.Scan(&taskID, &ingested, &ingesting); err == nil {
					ingestMap[taskID] = [2]int{ingested, ingesting}
				}
			}
			ingestRows.Close()
			for i := range visible {
				if counts, ok := ingestMap[visible[i].scanTaskID]; ok {
					visible[i].scanIngested = counts[0]
					visible[i].scanIngesting = counts[1]
				}
			}
		}
	}
	if err := applyFolderScopedLibraryCounts(h.App.DB, visible, profile, folderMap); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var list []gin.H
	for _, r := range visible {
		item := gin.H{
			"id": r.id, "name": r.name, "type": r.typ, "path": r.path,
			"folders":   responseFoldersForLibrary(profile, folderMap, int64(r.id), r.path),
			"auto_scan": r.auto, "enabled": r.enabled, "realtime_monitor": r.realtime, "preview_extract": r.preview, "drm_enabled": r.drmEnabled, "encryption_mode": h.normalizeEncryptionMode(r.encryptionMode), "cleanup_local_source_after_package": r.cleanupLocal, "jit_prepare_on_ingest": r.jitIngest,
			"encrypted_assets_enabled": r.encAssets, "encrypted_assets_cleanup_plaintext": r.encCleanupPlain,
			"encrypted_assets_dir_mode": storage.NormalizeEncDirMode(r.encDirMode), "encrypted_assets_custom_dir": r.encCustomDir,
			"metadata_providers": splitCSVList(r.metadataProviders), "image_providers": splitCSVList(r.imageProviders), "metadata_refresh_policy": r.refreshPolicy,
			"scraper": r.scraper, "created_at": r.created,
			"media_count":          r.mediaCount,
			"scan_task_id":         r.scanTaskID,
			"scan_status":          r.scanStatus,
			"scan_processed_count": r.scanProcessed,
			"scan_total_count":     r.scanTotal,
			"scan_added_count":     r.scanAdded,
			"scan_started_at":      r.scanStarted,
			"scan_ingested_count":  r.scanIngested,
			"scan_ingesting_count": r.scanIngesting,
		}
		if previewURL := h.libraryPreviewPublicURL(int64(r.id)); previewURL != "" {
			item["preview_url"] = previewURL
		} else if r.mediaCount > 0 {
			h.scheduleLibraryPreviewRefresh(int64(r.id))
		}
		list = append(list, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"items": list,
		"drm_capabilities": gin.H{
			"widevine_enabled": widevineEnabled,
			"powerdrm_enabled": powerdrmEnabled,
		},
		"encrypted_assets_config": gin.H{
			"data_dot_encrypted_dir": h.dataEncryptedDotDir(),
		},
	})
}

func (h *Handler) CreateLibrary(c *gin.Context) {
	var body libraryBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	auto := 1
	if body.AutoScan != nil {
		auto = *body.AutoScan
	}
	scraper := body.Scraper
	if scraper == "" {
		scraper = "tmdb"
	}
	enabled := 1
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	realtime := 0
	if body.RealtimeMonitor != nil {
		realtime = *body.RealtimeMonitor
	}
	preview := 0
	if body.PreviewExtract != nil {
		preview = *body.PreviewExtract
	}
	drmEnabled := 0
	if body.DRMEnabled != nil {
		drmEnabled = *body.DRMEnabled
	}
	encryptionMode := h.normalizeEncryptionMode(body.EncryptionMode)
	cleanupLocal := 0
	if body.CleanupLocalSource != nil {
		cleanupLocal = *body.CleanupLocalSource
	}
	jitIngest := 0
	if body.JITPrepareOnIngest != nil {
		jitIngest = *body.JITPrepareOnIngest
	}
	encAssets := 0
	if body.EncryptedAssetsEnabled != nil {
		encAssets = *body.EncryptedAssetsEnabled
	}
	encCleanupPlain := 0
	if body.EncryptedAssetsCleanupPlaintext != nil {
		encCleanupPlain = *body.EncryptedAssetsCleanupPlaintext
	} else if h.App != nil && h.App.Config != nil && h.App.Config.EncryptedAssetsCleanupDefault() {
		encCleanupPlain = 1
	}
	encDirMode := storage.NormalizeEncDirMode(body.EncryptedAssetsDirMode)
	encCustomDir := strings.TrimSpace(body.EncryptedAssetsCustomDir)
	folders := normalizeFolders(body.Folders, body.Path)
	if len(folders) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one folder required"})
		return
	}
	if err := h.validateEncryptedAssetsSettings(encAssets, encDirMode, encCustomDir, folders); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rootPath := folders[0]
	metadataProviders := strings.Join(defaultCSV(body.MetadataProviders, []string{"tmdb", "omdb"}), ",")
	imageProviders := strings.Join(defaultCSV(body.ImageProviders, []string{"tmdb", "omdb", "embedded", "screen_grabber"}), ",")
	refreshPolicy := strings.TrimSpace(body.MetadataRefreshPolicy)
	if refreshPolicy == "" {
		refreshPolicy = "never"
	}
	res, err := h.App.DB.Exec(
		`INSERT INTO library (name, type, path, auto_scan, enabled, realtime_monitor, preview_extract, drm_enabled, encryption_mode, cleanup_local_source_after_package, jit_prepare_on_ingest, encrypted_assets_enabled, encrypted_assets_cleanup_plaintext, encrypted_assets_dir_mode, encrypted_assets_custom_dir, metadata_providers, image_providers, metadata_refresh_policy, scraper) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		body.Name, body.Type, rootPath, auto, enabled, realtime, preview, drmEnabled, encryptionMode, cleanupLocal, jitIngest, encAssets, encCleanupPlain, encDirMode, encCustomDir, metadataProviders, imageProviders, refreshPolicy, scraper,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	_ = replaceLibraryFolders(h.App.DB, id, folders)
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (h *Handler) UpdateLibrary(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body libraryBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folders := normalizeFolders(body.Folders, body.Path)
	if len(folders) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one folder required"})
		return
	}
	auto := 1
	if body.AutoScan != nil {
		auto = *body.AutoScan
	}
	enabled := 1
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	realtime := 0
	if body.RealtimeMonitor != nil {
		realtime = *body.RealtimeMonitor
	}
	preview := 0
	if body.PreviewExtract != nil {
		preview = *body.PreviewExtract
	}
	drmEnabled := 0
	if body.DRMEnabled != nil {
		drmEnabled = *body.DRMEnabled
	}
	encryptionMode := h.normalizeEncryptionMode(body.EncryptionMode)
	cleanupLocal := 0
	if body.CleanupLocalSource != nil {
		cleanupLocal = *body.CleanupLocalSource
	}
	metadataProviders := strings.Join(defaultCSV(body.MetadataProviders, []string{"tmdb", "omdb"}), ",")
	imageProviders := strings.Join(defaultCSV(body.ImageProviders, []string{"tmdb", "omdb", "embedded", "screen_grabber"}), ",")
	refreshPolicy := strings.TrimSpace(body.MetadataRefreshPolicy)
	if refreshPolicy == "" {
		refreshPolicy = "never"
	}
	scraper := strings.TrimSpace(body.Scraper)
	if scraper == "" {
		scraper = "tmdb"
	}
	jitIngest := 0
	if body.JITPrepareOnIngest != nil {
		jitIngest = *body.JITPrepareOnIngest
	}
	encAssets := 0
	if body.EncryptedAssetsEnabled != nil {
		encAssets = *body.EncryptedAssetsEnabled
	}
	encCleanupPlain := 0
	if body.EncryptedAssetsCleanupPlaintext != nil {
		encCleanupPlain = *body.EncryptedAssetsCleanupPlaintext
	}
	encDirMode := storage.NormalizeEncDirMode(body.EncryptedAssetsDirMode)
	encCustomDir := strings.TrimSpace(body.EncryptedAssetsCustomDir)
	if err := h.validateEncryptedAssetsSettings(encAssets, encDirMode, encCustomDir, folders); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, err = h.App.DB.Exec(
		`UPDATE library SET name = ?, type = ?, path = ?, auto_scan = ?, enabled = ?, realtime_monitor = ?, preview_extract = ?, drm_enabled = ?, encryption_mode = ?, cleanup_local_source_after_package = ?, jit_prepare_on_ingest = ?, encrypted_assets_enabled = ?, encrypted_assets_cleanup_plaintext = ?, encrypted_assets_dir_mode = ?, encrypted_assets_custom_dir = ?, metadata_providers = ?, image_providers = ?, metadata_refresh_policy = ?, scraper = ?, scan_exclude_patterns = COALESCE(NULLIF(?, ''), scan_exclude_patterns) WHERE id = ?`,
		body.Name, body.Type, folders[0], auto, enabled, realtime, preview, drmEnabled, encryptionMode, cleanupLocal, jitIngest, encAssets, encCleanupPlain, encDirMode, encCustomDir, metadataProviders, imageProviders, refreshPolicy, scraper, strings.TrimSpace(body.ScanExcludePatterns), id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := replaceLibraryFolders(h.App.DB, id, folders); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) DeleteLibrary(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, err := h.App.DB.Exec(`DELETE FROM media WHERE library_id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.App.DB.Exec(`DELETE FROM library_folder WHERE library_id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.App.DB.Exec(`DELETE FROM library_node WHERE library_id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.App.DB.Exec(`DELETE FROM library WHERE id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) ScanLibrary(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var root string
	if err := h.App.DB.QueryRow(`SELECT path FROM library WHERE id = ?`, id).Scan(&root); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	taskID, runningTaskID, err := h.startLibraryScanTask(c.Request.Context(), id, "manual")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if runningTaskID > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "scan already running", "library_id": id, "task_id": runningTaskID, "running": true})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "library_id": id, "task_id": taskID, "status": "running"})
}

func normalizeFolders(folders []string, fallback string) []string {
	out := make([]string, 0, len(folders)+1)
	seen := map[string]struct{}{}
	for _, p := range folders {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 && strings.TrimSpace(fallback) != "" {
		out = append(out, strings.TrimSpace(fallback))
	}
	return out
}

func defaultCSV(in []string, fallback []string) []string {
	if len(in) == 0 {
		return fallback
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// libraryFoldersByID returns all library_folder paths in one query (avoids N+1 during list).
func libraryFoldersByID(db *sql.DB) (map[int64][]string, error) {
	rows, err := db.Query(`SELECT library_id, path FROM library_folder ORDER BY library_id, sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var libID int64
		var p string
		if err := rows.Scan(&libID, &p); err != nil {
			continue
		}
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out[libID] = append(out[libID], p)
	}
	return out, rows.Err()
}

func foldersForLibrary(byID map[int64][]string, libraryID int64, fallbackPath string) []string {
	if fs := byID[libraryID]; len(fs) > 0 {
		return fs
	}
	if strings.TrimSpace(fallbackPath) == "" {
		return nil
	}
	return []string{strings.TrimSpace(fallbackPath)}
}

func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func responseFoldersForLibrary(profile userPermissionProfile, folderMap map[int64][]string, libraryID int64, fallback string) []string {
	if allowed := profile.AllowedLibraryFolders[libraryID]; len(allowed) > 0 {
		out := make([]string, 0, len(allowed))
		for _, p := range allowed {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return foldersForLibrary(folderMap, libraryID, fallback)
}

func applyFolderScopedLibraryCounts(db *sql.DB, visible []libraryListRow, profile userPermissionProfile, folderMap map[int64][]string) error {
	ids := make([]int64, 0)
	index := map[int64]int{}
	for i := range visible {
		id := int64(visible[i].id)
		if len(profile.AllowedLibraryFolders[id]) > 0 {
			ids = append(ids, id)
			index[id] = i
			visible[i].mediaCount = 0
			folderMap[id] = nil
		}
	}
	if len(ids) == 0 {
		return nil
	}
	query := `SELECT library_id,file_path FROM media WHERE publication_state IN ('published','degraded') AND library_id IN (` + sqlPlaceholders(len(ids)) + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return err
		}
		if pathMatchesAnyFolder(p, profile.AllowedLibraryFolders[id]) {
			visible[index[id]].mediaCount++
		}
	}
	return rows.Err()
}

func listLibraryFolders(db *sql.DB, libraryID int64, fallbackPath string) []string {
	return listLibraryFoldersContext(context.Background(), db, libraryID, fallbackPath)
}

func listLibraryFoldersContext(ctx context.Context, db *sql.DB, libraryID int64, fallbackPath string) []string {
	rows, err := db.QueryContext(ctx, `SELECT path FROM library_folder WHERE library_id = ? ORDER BY sort_order, id`, libraryID)
	if err != nil {
		if strings.TrimSpace(fallbackPath) == "" {
			return nil
		}
		return []string{fallbackPath}
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p sql.NullString
		if rows.Scan(&p) == nil && p.Valid && strings.TrimSpace(p.String) != "" {
			out = append(out, strings.TrimSpace(p.String))
		}
	}
	if len(out) == 0 && strings.TrimSpace(fallbackPath) != "" {
		return []string{fallbackPath}
	}
	return out
}

func replaceLibraryFolders(db *sql.DB, libraryID int64, folders []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM library_folder WHERE library_id = ?`, libraryID); err != nil {
		return err
	}
	for i, p := range folders {
		if _, err = tx.Exec(`INSERT INTO library_folder (library_id, path, sort_order) VALUES (?, ?, ?)`, libraryID, p, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func splitCSVList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeEncryptionMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "standard", "hls_aes_128", "aes_128":
		return "standard"
	case "powerdrm":
		return "powerdrm"
	case "drm":
		return "drm"
	default:
		return "drm"
	}
}

func (h *Handler) drmCapabilities() (widevineEnabled bool, powerdrmEnabled bool) {
	widevineEnabled = true
	powerdrmEnabled = false
	if h == nil || h.App == nil || h.App.Config == nil {
		return
	}
	widevineEnabled = h.App.Config.WidevineEnabled()
	powerdrmEnabled = h.App.Config.DRM.PowerDRM.Enabled
	return
}

func (h *Handler) normalizeEncryptionMode(v string) string {
	mode := normalizeEncryptionMode(v)
	widevineEnabled, powerdrmEnabled := h.drmCapabilities()
	switch mode {
	case "drm":
		if !widevineEnabled {
			return "standard"
		}
		return "drm"
	case "powerdrm":
		if powerdrmEnabled {
			return "powerdrm"
		}
		if widevineEnabled {
			return "drm"
		}
		return "standard"
	default:
		return "standard"
	}
}

func (h *Handler) dataEncryptedDotDir() string {
	if h == nil || h.App == nil || h.App.Config == nil {
		return ""
	}
	return h.App.Config.DataEncryptedDotDir()
}

func (h *Handler) validateEncryptedAssetsSettings(encEnabled int, mode, customDir string, folders []string) error {
	if encEnabled != 1 {
		return nil
	}
	mode = storage.NormalizeEncDirMode(mode)
	switch mode {
	case storage.EncDirModeCustom:
		return storage.ValidateCustomEncDir(customDir)
	case storage.EncDirModeLibrary:
		if len(folders) == 0 || strings.TrimSpace(folders[0]) == "" {
			return errors.New("library folder required for encrypted directory")
		}
	}
	return nil
}
