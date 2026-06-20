package handler

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"knox-media/internal/photoparse"
	"knox-media/internal/preview"
	"knox-media/internal/scanner"
	"knox-media/internal/storage"
	"knox-media/pkg/ffprobe"
	"knox-media/pkg/fileutil"
)

func (h *Handler) ListScanTasks(c *gin.Context) {
	limit := 100
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.App.DB.Query(`
		SELECT t.id, t.library_id, COALESCE(l.name,''), t.status, t.source, t.processed_count, t.total_count, t.added_count, COALESCE(t.error_message,''), t.cancelled,
		       COALESCE(t.started_at,''), COALESCE(t.finished_at,''), t.created_at, t.updated_at
		FROM scan_task t
		LEFT JOIN library l ON l.id = t.library_id
		ORDER BY t.id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id, libraryID, processed, total, added, cancelled sql.NullInt64
		var libraryName, status, source, errMsg, startedAt, finishedAt, createdAt, updatedAt sql.NullString
		if rows.Scan(&id, &libraryID, &libraryName, &status, &source, &processed, &total, &added, &errMsg, &cancelled, &startedAt, &finishedAt, &createdAt, &updatedAt) != nil {
			continue
		}
		items = append(items, gin.H{
			"id":              id.Int64,
			"library_id":      libraryID.Int64,
			"library_name":    libraryName.String,
			"status":          status.String,
			"source":          source.String,
			"processed_count": processed.Int64,
			"total_count":     total.Int64,
			"added_count":     added.Int64,
			"error_message":   errMsg.String,
			"cancelled":       cancelled.Int64,
			"started_at":      startedAt.String,
			"finished_at":     finishedAt.String,
			"created_at":      createdAt.String,
			"updated_at":      updatedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) CancelScanTask(c *gin.Context) {
	taskID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || taskID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var libraryID int64
	var status string
	if err := h.App.DB.QueryRow(`SELECT library_id, status FROM scan_task WHERE id = ? LIMIT 1`, taskID).Scan(&libraryID, &status); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if status != "running" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task is not running"})
		return
	}
	h.scanMu.Lock()
	rt, ok := h.runningScans[libraryID]
	h.scanMu.Unlock()
	if !ok || rt.TaskID != taskID || rt.Cancel == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task is not cancellable"})
		return
	}
	rt.Cancel()
	c.JSON(http.StatusOK, gin.H{"ok": true, "cancelled": true})
}

func (h *Handler) startLibraryScanTask(libraryID int64, source string) (taskID int64, runningTaskID int64, err error) {
	h.scanMu.Lock()
	if rt, ok := h.runningScans[libraryID]; ok && rt.TaskID > 0 {
		h.scanMu.Unlock()
		return 0, rt.TaskID, nil
	}
	h.scanMu.Unlock()

	res, err := h.App.DB.Exec(`
		INSERT INTO scan_task (library_id, status, source, started_at, updated_at)
		VALUES (?, 'running', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, libraryID, source)
	if err != nil {
		return 0, 0, err
	}
	taskID, _ = res.LastInsertId()

	ctx, cancel := context.WithCancel(context.Background())
	h.scanMu.Lock()
	if rt, ok := h.runningScans[libraryID]; ok && rt.TaskID > 0 {
		h.scanMu.Unlock()
		cancel()
		_, _ = h.App.DB.Exec(`UPDATE scan_task SET status='failed', error_message='concurrent scan rejected', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, taskID)
		return 0, rt.TaskID, nil
	}
	h.runningScans[libraryID] = scanRuntime{TaskID: taskID, Cancel: cancel}
	h.scanMu.Unlock()

	var root string
	if err := h.App.DB.QueryRow(`SELECT path FROM library WHERE id = ?`, libraryID).Scan(&root); err != nil {
		h.scanMu.Lock()
		delete(h.runningScans, libraryID)
		h.scanMu.Unlock()
		cancel()
		_, _ = h.App.DB.Exec(`UPDATE scan_task SET status='failed', error_message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, err.Error(), taskID)
		return taskID, 0, err
	}
	folders := listLibraryFolders(h.App.DB, libraryID, root)
	go h.runLibraryScanTask(ctx, taskID, libraryID, folders)
	return taskID, 0, nil
}

func (h *Handler) runLibraryScanTask(ctx context.Context, taskID, libraryID int64, folders []string) {
	var processedCount int64
	var addedCount int64
	libraryType := h.loadLibraryType(libraryID)
	totalCount := countScannableFiles(folders, libraryType)
	_, _ = h.App.DB.Exec(`UPDATE scan_task SET total_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, totalCount, taskID)
	var ffprobeExtra []string
	if h.App.Config.LibraryScanFastFFprobe() {
		ffprobeExtra = ffprobe.ScanProbeExtraFast()
	}
	s := &scanner.Scanner{
		DB:           h.App.DB,
		Vault:        h.KeyVault,
		FFprobePath:  h.App.Config.FFmpeg.FFprobePath,
		SkipHash:     !h.App.Config.LibraryScanFileHash(),
		PhotoGeocode: h.PhotoGeocode,
		FFprobeExtra: ffprobeExtra,
		OnFile: func(path string, fileErr error) {
			n := atomic.AddInt64(&processedCount, 1)
			_, _ = h.App.DB.Exec(`UPDATE scan_task SET processed_count = ?, total_count = ?, added_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, n, totalCount, atomic.LoadInt64(&addedCount), taskID)
			action := "processed"
			msg := ""
			if fileErr != nil {
				action = "failed"
				msg = fileErr.Error()
			}
			_, _ = h.App.DB.Exec(`INSERT INTO scan_log (scan_task_id, library_id, file_path, action, message) VALUES (?, ?, ?, ?, ?)`, taskID, libraryID, path, action, msg)
		},
		OnMediaAdded: func(mediaID int64, title string, ft string) {
			_ = atomic.AddInt64(&addedCount, 1)
			_, _ = h.App.DB.Exec(`UPDATE scan_task SET processed_count = ?, total_count = ?, added_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, atomic.LoadInt64(&processedCount), totalCount, atomic.LoadInt64(&addedCount), taskID)
			var filePath sql.NullString
			_ = h.App.DB.QueryRow(`SELECT file_path FROM media WHERE id = ?`, mediaID).Scan(&filePath)
			_, _ = h.App.DB.Exec(`INSERT INTO scan_log (scan_task_id, library_id, file_path, action, message) VALUES (?, ?, ?, 'added', ?)`, taskID, libraryID, filePath.String, title)
			h.EnqueuePostIngestForNewMedia(mediaID, ft)
		},
		OnDocumentScanned: func(mediaID int64) {
			h.GenerateDocumentCover(mediaID)
		},
	}
	added, err := s.ScanLibraryFoldersWithContext(ctx, libraryID, folders)
	if added > 0 && int64(added) > atomic.LoadInt64(&addedCount) {
		atomic.StoreInt64(&addedCount, int64(added))
	}
	status := "done"
	cancelled := 0
	errMsg := ""
	if err != nil {
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
			cancelled = 1
		} else {
			status = "failed"
			errMsg = err.Error()
		}
	}
	_, _ = h.App.DB.Exec(`
		UPDATE scan_task
		SET status = ?, cancelled = ?, error_message = ?, processed_count = ?, total_count = ?, added_count = ?, finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, status, cancelled, errMsg, atomic.LoadInt64(&processedCount), totalCount, atomic.LoadInt64(&addedCount), taskID)

	h.scanMu.Lock()
	delete(h.runningScans, libraryID)
	h.scanMu.Unlock()
	if status == "done" {
		h.scheduleLibraryPreviewRefresh(libraryID)
		h.scheduleDocumentCoverBackfill(libraryID)
	}
}

// EnqueuePostIngestForNewMedia matches library-scan ingest: auto scrape, preview sprites (if enabled), local poster frame, optional subtitles.
// Upload merge/single must call this; realtime scanner uses main.enqueueAutoTasksOnMediaAdded instead.
func (h *Handler) EnqueuePostIngestForNewMedia(mediaID int64, fileType string) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return
	}
	go func(mid int64, ft string) {
		if ft != "image" {
			h.enqueueScrapeTask(mid, 0, "auto-scan")
		}
		h.enqueuePreviewTask(mid, ft)
		h.ensurePreviewGeneration(mid, ft)
		h.capturePosterFromVideo(mid, ft)
		if ft == "image" {
			h.GeneratePhotoVariants(mid)
		}
		if h.Subtitle != nil && h.App.Config != nil && h.App.Config.SubtitleAutoOnScan() && ft == "video" {
			_ = h.Subtitle.EnsurePendingSubtitleTask(mid)
		}
		if h.LyricWorker != nil && h.App.Config != nil && h.App.Config.LyricAutoOnScan() {
			_ = h.LyricWorker.EnsurePendingIfNoLyrics(mid, ft)
		}
		if h.PhotoClassifyWorker != nil && h.App.Config != nil && h.App.Config.PhotoClassifyAutoOnScan() && ft == "image" {
			_ = h.PhotoClassifyWorker.EnsurePendingIfPhoto(mid, ft)
		}
		if h.PhotoFaceWorker != nil && h.App.Config != nil && h.App.Config.PhotoFaceAutoOnScan() && ft == "image" {
			_ = h.PhotoFaceWorker.EnsurePendingIfPhoto(mid, ft)
		}
		if ft == "document" {
			h.GenerateDocumentCover(mid)
		}
		if ft == "video" && h.KeyframeWorker != nil {
			h.KeyframeWorker.Enqueue(mid)
		}
		h.KickEncryptMediaAsset(mid)
	}(mediaID, fileType)
}

// enqueuePreviewTask inserts/updates preview_task as waiting when library has preview_extract enabled.
func (h *Handler) enqueuePreviewTask(mediaID int64, fileType string) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 || fileType != "video" {
		return
	}
	var enabled sql.NullInt64
	var duration sql.NullInt64
	if err := h.App.DB.QueryRow(`
		SELECT COALESCE(l.preview_extract,0), COALESCE(m.duration,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&enabled, &duration); err != nil {
		return
	}
	if enabled.Int64 != 1 {
		return
	}
	dur := duration.Int64
	if dur <= 0 {
		dur = 600
	}
	intervalSec := int(math.Ceil(float64(dur) / 100.0))
	if intervalSec < 5 {
		intervalSec = 5
	}
	countNum := int(math.Ceil(float64(dur) / float64(intervalSec)))
	if countNum < 1 {
		countNum = 1
	}
	if countNum > 100 {
		countNum = 100
	}
	_ = preview.UpsertWaitingPreviewTask(h.App.DB, mediaID, intervalSec, countNum)
}

func (h *Handler) ensurePreviewGeneration(mediaID int64, fileType string) {
	if h == nil || h.PreviewWorker == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 || fileType != "video" {
		return
	}
	var libraryID sql.NullInt64
	var filePath sql.NullString
	var duration sql.NullInt64
	var enabled sql.NullInt64
	if err := h.App.DB.QueryRow(`
		SELECT m.library_id, m.file_path, COALESCE(m.duration,0), COALESCE(l.preview_extract,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&libraryID, &filePath, &duration, &enabled); err != nil || enabled.Int64 != 1 {
		return
	}
	inputPath := storage.PreferredFFmpegPath(h.App.DB, mediaID, libraryID.Int64, filePath.String)
	if inputPath == "" {
		return
	}
	_, _ = h.PreviewWorker.Ensure(context.Background(), mediaID, inputPath, duration.Int64)
}

func countScannableFiles(roots []string, libraryType string) int64 {
	var total int64
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			ft := fileutil.GuessFileType(path)
			if photoparse.ShouldScanFile(libraryType, ft) {
				total++
			}
			return nil
		})
	}
	return total
}

func (h *Handler) loadLibraryType(libraryID int64) string {
	if h == nil || h.App == nil || h.App.DB == nil || libraryID <= 0 {
		return ""
	}
	var t sql.NullString
	if err := h.App.DB.QueryRow(`SELECT type FROM library WHERE id = ?`, libraryID).Scan(&t); err != nil {
		return ""
	}
	return t.String
}

