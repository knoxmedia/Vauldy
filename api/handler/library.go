package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/libraryprocessing"
	"knox-media/internal/mediastore"
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
	SubtitleExtract                 *int     `json:"subtitle_extract"`
	ATrackExtract                   *int     `json:"atrack_extract"`
	SubtitleRecognize               *int     `json:"subtitle_recognize"`
	KeyframeExtract                 *int     `json:"keyframe_extract"`
	AIAnalysis                      *int     `json:"ai_analysis"`
	LyricRecognize                  *int     `json:"lyric_recognize"`
	AudioAnalysis                   *int     `json:"audio_analysis"`
	PhotoClassify                   *int     `json:"photo_classify"`
	PhotoGeocode                    *int     `json:"photo_geocode"`
	PhotoFace                       *int     `json:"photo_face"`
	ImageOCR                        *int     `json:"image_ocr"`
	DocumentConvert                 *int     `json:"document_convert"`
	DocumentFulltext                *int     `json:"document_fulltext"`
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
	id, auto, enabled, realtime, preview, subtitleExtract, atrackExtract, subtitleRecognize, keyframeExtract, aiAnalysis, drmEnabled, cleanupLocal, jitIngest, encAssets, encCleanupPlain, mediaCount int
	lyricRecognize, audioAnalysis, photoClassify, photoGeocode, photoFace, imageOCR, documentConvert, documentFulltext                                                                                     int
	scanTaskID, scanProcessed, scanTotal, scanAdded                                                                                                                                                        int64
	scanIngested, scanIngesting                                                                                                                                                                            int
	name, typ, path, encryptionMode, encDirMode, encCustomDir, metadataProviders, imageProviders, refreshPolicy, scraper, created, scanStatus, scanStarted                                                  string
}

func (h *Handler) ListLibraries(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), listLibrariesTimeout)
	defer cancel()
	widevineEnabled, powerdrmEnabled := h.drmCapabilities()
	var profile userPermissionProfile
	isAdmin, isAPIClient := middleware.IsAdmin(c), middleware.IsAPIClient(c)
	tx, err := h.App.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if !isAPIClient {
		if uid := middleware.UserID(c); uid > 0 {
			profile, err = loadUserPermissionProfileFrom(ctx, tx, uid)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	query := `SELECT l.id,l.name,l.type,l.path,l.auto_scan,l.enabled,l.realtime_monitor,l.preview_extract,l.subtitle_extract,l.atrack_extract,l.subtitle_recognize,l.keyframe_extract,l.ai_analysis,COALESCE(l.lyric_recognize,0),COALESCE(l.audio_analysis,0),COALESCE(l.photo_classify,0),COALESCE(l.photo_geocode,0),COALESCE(l.photo_face,0),COALESCE(l.image_ocr,0),COALESCE(l.document_convert,0),COALESCE(l.document_fulltext,0),l.drm_enabled,COALESCE(l.encryption_mode,'drm'),l.cleanup_local_source_after_package,l.jit_prepare_on_ingest,COALESCE(l.encrypted_assets_enabled,0),COALESCE(l.encrypted_assets_cleanup_plaintext,0),COALESCE(l.encrypted_assets_dir_mode,'library'),COALESCE(l.encrypted_assets_custom_dir,''),l.metadata_providers,l.image_providers,l.metadata_refresh_policy,l.scraper,l.created_at,COALESCE(mc.media_count,0),COALESCE(latest_scan.id,0),COALESCE(latest_scan.status,''),COALESCE(latest_scan.processed_count,0),COALESCE(latest_scan.total_count,0),COALESCE(latest_scan.added_count,0),COALESCE(latest_scan.started_at,'') FROM library l LEFT JOIN (SELECT library_id,COUNT(*) AS media_count FROM media WHERE publication_state IN ('published','degraded') GROUP BY library_id) mc ON mc.library_id=l.id LEFT JOIN (SELECT st.* FROM scan_task st JOIN (SELECT library_id,MAX(id) AS max_id FROM scan_task GROUP BY library_id) latest ON latest.max_id=st.id) latest_scan ON latest_scan.library_id=l.id`
	args := []any{}
	if strings.EqualFold(profile.LibraryScope, "selected") {
		ids := make([]int64, 0, len(profile.AllowedLibraryIDs))
		for id := range profile.AllowedLibraryIDs {
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			query += " WHERE 1=0"
		} else {
			query += " WHERE l.id IN (" + sqlPlaceholders(len(ids)) + ")"
			for _, id := range ids {
				args = append(args, id)
			}
		}
	}
	query += " ORDER BY l.id"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	visible := make([]libraryListRow, 0)
	for rows.Next() {
		var r libraryListRow
		if err := rows.Scan(&r.id, &r.name, &r.typ, &r.path, &r.auto, &r.enabled, &r.realtime, &r.preview, &r.subtitleExtract, &r.atrackExtract, &r.subtitleRecognize, &r.keyframeExtract, &r.aiAnalysis, &r.lyricRecognize, &r.audioAnalysis, &r.photoClassify, &r.photoGeocode, &r.photoFace, &r.imageOCR, &r.documentConvert, &r.documentFulltext, &r.drmEnabled, &r.encryptionMode, &r.cleanupLocal, &r.jitIngest, &r.encAssets, &r.encCleanupPlain, &r.encDirMode, &r.encCustomDir, &r.metadataProviders, &r.imageProviders, &r.refreshPolicy, &r.scraper, &r.created, &r.mediaCount, &r.scanTaskID, &r.scanStatus, &r.scanProcessed, &r.scanTotal, &r.scanAdded, &r.scanStarted); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !isAdmin && !isAPIClient && r.enabled != 1 {
			continue
		}
		visible = append(visible, r)
	}
	if err := rows.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
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
		ingestQuery := `SELECT r.scan_task_id,SUM(CASE WHEN r.status IN ('published','degraded','failed','cancelled') THEN 1 ELSE 0 END),SUM(CASE WHEN r.status='processing' THEN 1 ELSE 0 END) FROM media_ingest_run r WHERE r.scan_task_id IN (` + sqlPlaceholders(len(scanTaskIDs)) + `) AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL GROUP BY r.scan_task_id`
		ingestArgs := make([]any, len(scanTaskIDs))
		for i, id := range scanTaskIDs {
			ingestArgs[i] = id
		}
		ingestRows, err := tx.QueryContext(ctx, ingestQuery, ingestArgs...)
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
	ids := make([]int64, len(visible))
	for i := range visible {
		ids[i] = int64(visible[i].id)
	}
	folderMap, err := libraryFoldersForIDsContext(ctx, tx, ids)
	if err == nil {
		err = applyFolderScopedLibraryCounts(ctx, tx, visible, profile, folderMap)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	list := make([]gin.H, 0, len(visible))
	for _, r := range visible {
		item := gin.H{"id": r.id, "name": r.name, "type": r.typ, "path": r.path, "folders": responseFoldersForLibrary(profile, folderMap, int64(r.id), r.path), "auto_scan": r.auto, "enabled": r.enabled, "realtime_monitor": r.realtime, "preview_extract": r.preview, "subtitle_extract": r.subtitleExtract, "atrack_extract": r.atrackExtract, "subtitle_recognize": r.subtitleRecognize, "keyframe_extract": r.keyframeExtract, "ai_analysis": r.aiAnalysis, "lyric_recognize": r.lyricRecognize, "audio_analysis": r.audioAnalysis, "photo_classify": r.photoClassify, "photo_geocode": r.photoGeocode, "photo_face": r.photoFace, "image_ocr": r.imageOCR, "document_convert": r.documentConvert, "document_fulltext": r.documentFulltext, "processing_options": processingOptionsResponse(r.typ, processingOptionsFromInts(r.preview, r.subtitleExtract, r.atrackExtract, r.subtitleRecognize, r.keyframeExtract, r.aiAnalysis, r.lyricRecognize, r.audioAnalysis, r.photoClassify, r.photoGeocode, r.photoFace, r.imageOCR, r.documentConvert, r.documentFulltext)), "drm_enabled": r.drmEnabled, "encryption_mode": h.normalizeEncryptionMode(r.encryptionMode), "cleanup_local_source_after_package": r.cleanupLocal, "jit_prepare_on_ingest": r.jitIngest, "encrypted_assets_enabled": r.encAssets, "encrypted_assets_cleanup_plaintext": r.encCleanupPlain, "encrypted_assets_dir_mode": storage.NormalizeEncDirMode(r.encDirMode), "encrypted_assets_custom_dir": r.encCustomDir, "metadata_providers": splitCSVList(r.metadataProviders), "image_providers": splitCSVList(r.imageProviders), "metadata_refresh_policy": r.refreshPolicy, "scraper": r.scraper, "created_at": r.created, "media_count": r.mediaCount, "scan_task_id": r.scanTaskID, "scan_status": r.scanStatus, "scan_processed_count": r.scanProcessed, "scan_total_count": r.scanTotal, "scan_added_count": r.scanAdded, "scan_started_at": r.scanStarted, "scan_ingested_count": r.scanIngested, "scan_ingesting_count": r.scanIngesting}
		if u := h.libraryPreviewPublicURL(int64(r.id)); u != "" {
			item["preview_url"] = u
		} else if r.mediaCount > 0 {
			h.scheduleLibraryPreviewIfMissing(int64(r.id))
		}
		list = append(list, item)
	}
	encEnabled := true
	if h.App != nil && h.App.Config != nil {
		encEnabled = h.App.Config.EncryptedAssetsEnabled()
	}
	c.JSON(http.StatusOK, gin.H{"items": list, "drm_capabilities": gin.H{"widevine_enabled": widevineEnabled, "powerdrm_enabled": powerdrmEnabled}, "encrypted_assets_config": gin.H{"enabled": encEnabled, "data_dot_encrypted_dir": h.dataEncryptedDotDir()}})
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
func applyFolderScopedLibraryCounts(ctx context.Context, q contextQueryer, visible []libraryListRow, profile userPermissionProfile, folderMap map[int64][]string) error {
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
	rows, err := q.QueryContext(ctx, query, args...)
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
	if err := validateProcessingValues(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	preview := 0
	if body.PreviewExtract != nil {
		preview = *body.PreviewExtract
	}
	explicitProcessing := processingOptionsFromBody(body)
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
		`INSERT INTO library (name, type, path, auto_scan, enabled, realtime_monitor, preview_extract, subtitle_extract, atrack_extract, subtitle_recognize, keyframe_extract, ai_analysis, lyric_recognize, audio_analysis, photo_classify, photo_geocode, photo_face, image_ocr, document_convert, document_fulltext, drm_enabled, encryption_mode, cleanup_local_source_after_package, jit_prepare_on_ingest, encrypted_assets_enabled, encrypted_assets_cleanup_plaintext, encrypted_assets_dir_mode, encrypted_assets_custom_dir, metadata_providers, image_providers, metadata_refresh_policy, scraper) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		body.Name, body.Type, rootPath, auto, enabled, realtime, preview, boolInt(explicitProcessing.SubtitleExtract), boolInt(explicitProcessing.ATrackExtract), boolInt(explicitProcessing.SubtitleRecognize), boolInt(explicitProcessing.KeyframeExtract), boolInt(explicitProcessing.AIAnalysis), boolInt(explicitProcessing.LyricRecognize), boolInt(explicitProcessing.AudioAnalysis), boolInt(explicitProcessing.PhotoClassify), boolInt(explicitProcessing.PhotoGeocode), boolInt(explicitProcessing.PhotoFace), boolInt(explicitProcessing.ImageOCR), boolInt(explicitProcessing.DocumentConvert), boolInt(explicitProcessing.DocumentFulltext), drmEnabled, encryptionMode, cleanupLocal, jitIngest, encAssets, encCleanupPlain, encDirMode, encCustomDir, metadataProviders, imageProviders, refreshPolicy, scraper,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	_ = replaceLibraryFolders(h.App.DB, id, folders)
	c.JSON(http.StatusCreated, libraryProcessingItem(id, body.Type, explicitProcessing))
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
	if err := validateProcessingValues(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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
		`UPDATE library SET name = ?, type = ?, path = ?, auto_scan = ?, enabled = ?, realtime_monitor = ?, preview_extract = COALESCE(?, preview_extract), subtitle_extract = COALESCE(?, subtitle_extract), atrack_extract = COALESCE(?, atrack_extract), subtitle_recognize = COALESCE(?, subtitle_recognize), keyframe_extract = COALESCE(?, keyframe_extract), ai_analysis = COALESCE(?, ai_analysis), lyric_recognize = COALESCE(?, lyric_recognize), audio_analysis = COALESCE(?, audio_analysis), photo_classify = COALESCE(?, photo_classify), photo_geocode = COALESCE(?, photo_geocode), photo_face = COALESCE(?, photo_face), image_ocr = COALESCE(?, image_ocr), document_convert = COALESCE(?, document_convert), document_fulltext = COALESCE(?, document_fulltext), drm_enabled = ?, encryption_mode = ?, cleanup_local_source_after_package = ?, jit_prepare_on_ingest = ?, encrypted_assets_enabled = ?, encrypted_assets_cleanup_plaintext = ?, encrypted_assets_dir_mode = ?, encrypted_assets_custom_dir = ?, metadata_providers = ?, image_providers = ?, metadata_refresh_policy = ?, scraper = ?, scan_exclude_patterns = COALESCE(NULLIF(?, ''), scan_exclude_patterns) WHERE id = ?`,
		body.Name, body.Type, folders[0], auto, enabled, realtime, body.PreviewExtract, body.SubtitleExtract, body.ATrackExtract, body.SubtitleRecognize, body.KeyframeExtract, body.AIAnalysis, body.LyricRecognize, body.AudioAnalysis, body.PhotoClassify, body.PhotoGeocode, body.PhotoFace, body.ImageOCR, body.DocumentConvert, body.DocumentFulltext, drmEnabled, encryptionMode, cleanupLocal, jitIngest, encAssets, encCleanupPlain, encDirMode, encCustomDir, metadataProviders, imageProviders, refreshPolicy, scraper, strings.TrimSpace(body.ScanExcludePatterns), id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := replaceLibraryFolders(h.App.DB, id, folders); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	explicit, loadErr := loadLibraryProcessing(h.App.DB, id)
	if loadErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": loadErr.Error()})
		return
	}
	item := libraryProcessingItem(id, body.Type, explicit)
	item["ok"] = true
	c.JSON(http.StatusOK, item)
}

func (h *Handler) DeleteLibrary(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	cleanup, err := mediastore.DeleteLibraryAndCollect(ctx, h.App.DB, id, h.photoCacheDir())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err = mediastore.CleanupFiles(ctx, h.App.DB, cleanup, h.mediaCleanupRoots()); err != nil {
		log.Printf("delete library cleanup library=%d: %v", id, err)
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
	if err := h.App.DB.QueryRowContext(c.Request.Context(), `SELECT path FROM library WHERE id = ?`, id).Scan(&root); err != nil {
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
	return libraryFoldersByIDContext(context.Background(), db)
}

type contextQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func libraryFoldersByIDContext(ctx context.Context, db *sql.DB) (map[int64][]string, error) {
	return libraryFoldersForIDsContext(ctx, db, nil)
}
func libraryFoldersForIDsContext(ctx context.Context, db contextQueryer, ids []int64) (map[int64][]string, error) {
	query := `SELECT library_id,path FROM library_folder`
	args := []any{}
	if ids != nil {
		if len(ids) == 0 {
			return map[int64][]string{}, nil
		}
		query += ` WHERE library_id IN (` + sqlPlaceholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query += ` ORDER BY library_id,sort_order,id`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("library folder row has empty path for library %d", id)
		}
		out[id] = append(out[id], p)
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

func processingOptionsFromBody(body libraryBody) libraryprocessing.Options {
	return processingOptionsFromInts(pointerInt(body.PreviewExtract), pointerInt(body.SubtitleExtract), pointerInt(body.ATrackExtract), pointerInt(body.SubtitleRecognize), pointerInt(body.KeyframeExtract), pointerInt(body.AIAnalysis), pointerInt(body.LyricRecognize), pointerInt(body.AudioAnalysis), pointerInt(body.PhotoClassify), pointerInt(body.PhotoGeocode), pointerInt(body.PhotoFace), pointerInt(body.ImageOCR), pointerInt(body.DocumentConvert), pointerInt(body.DocumentFulltext))
}
func processingOptionsFromInts(preview, subtitle, atrack, recognize, keyframe, ai, lyric, audioAnalysis, photoClassify, photoGeocode, photoFace, imageOCR, documentConvert, documentFulltext int) libraryprocessing.Options {
	return libraryprocessing.Options{Preview: preview == 1, SubtitleExtract: subtitle == 1, ATrackExtract: atrack == 1, SubtitleRecognize: recognize == 1, KeyframeExtract: keyframe == 1, AIAnalysis: ai == 1, LyricRecognize: lyric == 1, AudioAnalysis: audioAnalysis == 1, PhotoClassify: photoClassify == 1, PhotoGeocode: photoGeocode == 1, PhotoFace: photoFace == 1, ImageOCR: imageOCR == 1, DocumentConvert: documentConvert == 1, DocumentFulltext: documentFulltext == 1}
}
func pointerInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func validateProcessingValues(body libraryBody) error {
	for name, value := range map[string]*int{
		"preview_extract": body.PreviewExtract, "subtitle_extract": body.SubtitleExtract, "atrack_extract": body.ATrackExtract, "subtitle_recognize": body.SubtitleRecognize, "keyframe_extract": body.KeyframeExtract, "ai_analysis": body.AIAnalysis,
		"lyric_recognize": body.LyricRecognize, "audio_analysis": body.AudioAnalysis,
		"photo_classify": body.PhotoClassify, "photo_geocode": body.PhotoGeocode, "photo_face": body.PhotoFace, "image_ocr": body.ImageOCR,
		"document_convert": body.DocumentConvert, "document_fulltext": body.DocumentFulltext,
	} {
		if value != nil && *value != 0 && *value != 1 {
			return fmt.Errorf("%s must be 0 or 1", name)
		}
	}
	return nil
}
func videoProcessingLibraryType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "movie", "tv", "video", "anime":
		return true
	default:
		return false
	}
}
func audioProcessingLibraryType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "music", "audio", "podcast":
		return true
	default:
		return false
	}
}
func imageProcessingLibraryType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "photo", "image", "picture":
		return true
	default:
		return false
	}
}
func documentProcessingLibraryType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "document", "book", "ebook":
		return true
	default:
		return false
	}
}
func processingOptionsMap(o libraryprocessing.Options) gin.H {
	return gin.H{
		"preview": o.Preview, "subtitle_extract": o.SubtitleExtract, "atrack_extract": o.ATrackExtract,
		"subtitle_recognize": o.SubtitleRecognize, "keyframe_extract": o.KeyframeExtract, "ai_analysis": o.AIAnalysis,
		"lyric_recognize": o.LyricRecognize, "audio_analysis": o.AudioAnalysis,
		"photo_classify": o.PhotoClassify, "photo_geocode": o.PhotoGeocode, "photo_face": o.PhotoFace, "image_ocr": o.ImageOCR,
		"document_convert": o.DocumentConvert, "document_fulltext": o.DocumentFulltext,
	}
}
func processingOptionsResponse(typ string, explicit libraryprocessing.Options) gin.H {
	effective, provenance := libraryprocessing.Close(typ, explicit)
	if !videoProcessingLibraryType(typ) && !audioProcessingLibraryType(typ) && !imageProcessingLibraryType(typ) && !documentProcessingLibraryType(typ) {
		effective = libraryprocessing.Options{}
		provenance.DependencyAdded = nil
	}
	if provenance.Explicit == nil {
		provenance.Explicit = []string{}
	}
	if provenance.DependencyAdded == nil {
		provenance.DependencyAdded = []string{}
	}
	return gin.H{"explicit": processingOptionsMap(explicit), "effective": processingOptionsMap(effective), "provenance": provenance}
}
func libraryProcessingItem(id int64, typ string, explicit libraryprocessing.Options) gin.H {
	return gin.H{
		"id": id,
		"preview_extract": boolInt(explicit.Preview), "subtitle_extract": boolInt(explicit.SubtitleExtract), "atrack_extract": boolInt(explicit.ATrackExtract),
		"subtitle_recognize": boolInt(explicit.SubtitleRecognize), "keyframe_extract": boolInt(explicit.KeyframeExtract), "ai_analysis": boolInt(explicit.AIAnalysis),
		"lyric_recognize": boolInt(explicit.LyricRecognize), "audio_analysis": boolInt(explicit.AudioAnalysis),
		"photo_classify": boolInt(explicit.PhotoClassify), "photo_geocode": boolInt(explicit.PhotoGeocode), "photo_face": boolInt(explicit.PhotoFace), "image_ocr": boolInt(explicit.ImageOCR),
		"document_convert": boolInt(explicit.DocumentConvert), "document_fulltext": boolInt(explicit.DocumentFulltext),
		"processing_options": processingOptionsResponse(typ, explicit),
	}
}
func loadLibraryProcessing(db *sql.DB, id int64) (libraryprocessing.Options, error) {
	var preview, subtitle, atrack, recognize, keyframe, ai, lyric, audioAnalysis, photoClassify, photoGeocode, photoFace, imageOCR, documentConvert, documentFulltext int
	err := db.QueryRow(`SELECT preview_extract,subtitle_extract,atrack_extract,subtitle_recognize,keyframe_extract,ai_analysis,COALESCE(lyric_recognize,0),COALESCE(audio_analysis,0),COALESCE(photo_classify,0),COALESCE(photo_geocode,0),COALESCE(photo_face,0),COALESCE(image_ocr,0),COALESCE(document_convert,0),COALESCE(document_fulltext,0) FROM library WHERE id=?`, id).Scan(&preview, &subtitle, &atrack, &recognize, &keyframe, &ai, &lyric, &audioAnalysis, &photoClassify, &photoGeocode, &photoFace, &imageOCR, &documentConvert, &documentFulltext)
	return processingOptionsFromInts(preview, subtitle, atrack, recognize, keyframe, ai, lyric, audioAnalysis, photoClassify, photoGeocode, photoFace, imageOCR, documentConvert, documentFulltext), err
}
