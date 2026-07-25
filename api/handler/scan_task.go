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
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/photoparse"
	"knox-media/internal/preview"
	"knox-media/internal/scancoord"
	"knox-media/internal/storage"
	"knox-media/pkg/fileutil"
)

func (h *Handler) ListScanTasks(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	limit := 100
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.App.DB.QueryContext(ctx, `
		SELECT t.id, t.library_id, COALESCE(l.name,''), t.status, t.source, t.processed_count, t.total_count, t.added_count, t.failed_count, COALESCE(t.error_message,''), t.cancelled,
		       COALESCE(t.started_at,''), COALESCE(t.finished_at,''), t.created_at, t.updated_at
		FROM scan_task t
		LEFT JOIN library l ON l.id = t.library_id
		ORDER BY t.id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			c.JSON(http.StatusGatewayTimeout, gin.H{"code": "scan_tasks_timeout"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "scan_tasks_internal"})
		}
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id, libraryID, processed, total, added, failed, cancelled sql.NullInt64
		var libraryName, status, source, errMsg, startedAt, finishedAt, createdAt, updatedAt sql.NullString
		if err := rows.Scan(&id, &libraryID, &libraryName, &status, &source, &processed, &total, &added, &failed, &errMsg, &cancelled, &startedAt, &finishedAt, &createdAt, &updatedAt); err != nil {
			rows.Close()
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				c.JSON(http.StatusGatewayTimeout, gin.H{"code": "scan_tasks_timeout"})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"code": "scan_tasks_internal"})
			}
			return
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
			"failed_count":    failed.Int64,
			"error_message":   errMsg.String,
			"cancelled":       cancelled.Int64,
			"started_at":      startedAt.String,
			"finished_at":     finishedAt.String,
			"created_at":      createdAt.String,
			"updated_at":      updatedAt.String,
		})
	}
	if err := rows.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			c.JSON(http.StatusGatewayTimeout, gin.H{"code": "scan_tasks_timeout"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "scan_tasks_internal"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func respondScanQueryError(c *gin.Context, ctx context.Context, err error, prefix string) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		c.JSON(http.StatusGatewayTimeout, gin.H{"code": prefix + "_timeout"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"code": prefix + "_internal"})
}
func (h *Handler) CancelScanTask(c *gin.Context) {
	taskID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || taskID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if h == nil || h.ScanCoordinator == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "scan coordinator is not configured"})
		return
	}
	result, err := h.ScanCoordinator.Cancel(c.Request.Context(), taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "cancelled": result.Cancelled, "status": result.Status})
}

func (h *Handler) submitLibraryScan(ctx context.Context, libraryID int64, roots []string, source scancoord.Source) (scancoord.SubmitResult, error) {
	if h == nil || h.ScanCoordinator == nil {
		return scancoord.SubmitResult{}, errors.New("scan coordinator is not configured")
	}
	return h.ScanCoordinator.Submit(ctx, scancoord.ScanRequest{LibraryID: libraryID, Source: source, Roots: roots})
}

func (h *Handler) startLibraryScanTask(ctx context.Context, libraryID int64, source string) (taskID int64, runningTaskID int64, err error) {
	var root string
	if h == nil || h.App == nil || h.App.DB == nil {
		return 0, 0, errors.New("database is not configured")
	}
	if err := h.App.DB.QueryRowContext(ctx, `SELECT path FROM library WHERE id = ?`, libraryID).Scan(&root); err != nil {
		return 0, 0, err
	}
	scanSource := scancoord.SourceManual
	if source == "schedule" || source == string(scancoord.SourceScheduled) {
		scanSource = scancoord.SourceScheduled
	}
	result, err := h.submitLibraryScan(ctx, libraryID, listLibraryFoldersContext(ctx, h.App.DB, libraryID, root), scanSource)
	if err != nil {
		return 0, 0, err
	}
	return result.TaskID, result.ExistingTaskID, nil
}

// EnqueuePostIngestForNewMedia synchronously submits unified post-ingest work.
// Upload callers have no scan task, so their queue rows use a nil scan task ID.
func (h *Handler) EnqueuePostIngestForNewMedia(mediaID int64, fileType string) error {
	return h.enqueuePostIngestForNewMedia(context.Background(), mediaID, nil, fileType)
}

func (h *Handler) enqueuePostIngestForNewMedia(ctx context.Context, mediaID int64, scanTaskID *int64, fileType string) error {
	if h == nil || h.PostIngestEnqueuer == nil {
		return errors.New("post-ingest enqueuer is not configured")
	}
	_, err := h.PostIngestEnqueuer.EnqueueMedia(ctx, mediaID, scanTaskID, fileType)
	if err != nil && h.OnPostIngestError != nil {
		h.OnPostIngestError(err)
	}
	return err
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
	return h.loadLibraryTypeContext(context.Background(), libraryID)
}
func (h *Handler) loadLibraryTypeContext(ctx context.Context, libraryID int64) string {
	if h == nil || h.App == nil || h.App.DB == nil || libraryID <= 0 {
		return ""
	}
	var t sql.NullString
	if err := h.App.DB.QueryRowContext(ctx, `SELECT type FROM library WHERE id = ?`, libraryID).Scan(&t); err != nil {
		return ""
	}
	return t.String
}
