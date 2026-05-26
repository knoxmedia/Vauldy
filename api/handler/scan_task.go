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

	"knox-media/internal/scanner"
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
	totalCount := countScannableFiles(folders)
	_, _ = h.App.DB.Exec(`UPDATE scan_task SET total_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, totalCount, taskID)
	var ffprobeExtra []string
	if h.App.Config.LibraryScanFastFFprobe() {
		ffprobeExtra = ffprobe.ScanProbeExtraFast()
	}
	s := &scanner.Scanner{
		DB:           h.App.DB,
		FFprobePath:  h.App.Config.FFmpeg.FFprobePath,
		SkipHash:     !h.App.Config.LibraryScanFileHash(),
		FFprobeExtra: ffprobeExtra,
		OnFile: func(_ string, _ error) {
			n := atomic.AddInt64(&processedCount, 1)
			_, _ = h.App.DB.Exec(`UPDATE scan_task SET processed_count = ?, total_count = ?, added_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, n, totalCount, atomic.LoadInt64(&addedCount), taskID)
		},
		OnMediaAdded: func(mediaID int64, _ string, ft string) {
			_ = atomic.AddInt64(&addedCount, 1)
			_, _ = h.App.DB.Exec(`UPDATE scan_task SET processed_count = ?, total_count = ?, added_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, atomic.LoadInt64(&processedCount), totalCount, atomic.LoadInt64(&addedCount), taskID)
			h.EnqueuePostIngestForNewMedia(mediaID, ft)
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
	}
}

// EnqueuePostIngestForNewMedia matches library-scan ingest: auto scrape, preview sprites (if enabled), local poster frame, optional subtitles.
// Upload merge/single must call this; realtime scanner uses main.enqueueAutoTasksOnMediaAdded instead.
func (h *Handler) EnqueuePostIngestForNewMedia(mediaID int64, fileType string) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return
	}
	go func(mid int64, ft string) {
		h.enqueueScrapeTask(mid, 0, "auto-scan")
		h.enqueuePreviewTask(mid, ft)
		h.capturePosterFromVideo(mid, ft)
		if h.Subtitle != nil && h.App.Config != nil && h.App.Config.SubtitleAutoOnScan() && ft == "video" {
			_ = h.Subtitle.EnsurePendingSubtitleTask(mid)
		}
		if h.LyricWorker != nil && h.App.Config != nil && h.App.Config.LyricAutoOnScan() {
			_ = h.LyricWorker.EnsurePendingIfNoLyrics(mid, ft)
		}
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
	_, _ = h.App.DB.Exec(
		`INSERT INTO preview_task (media_id, status, interval_sec, thumb_count, thumb_width, thumb_height, updated_at)
		 VALUES (?, 'waiting', ?, ?, 240, 135, CURRENT_TIMESTAMP)
		 ON CONFLICT(media_id) DO UPDATE SET
		   status='waiting',
		   interval_sec=excluded.interval_sec,
		   thumb_count=excluded.thumb_count,
		   updated_at=CURRENT_TIMESTAMP,
		   error_message=NULL`,
		mediaID, intervalSec, countNum,
	)
}

func countScannableFiles(roots []string) int64 {
	var total int64
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			ft := fileutil.GuessFileType(path)
			if ft == "video" || ft == "audio" {
				total++
			}
			return nil
		})
	}
	return total
}

