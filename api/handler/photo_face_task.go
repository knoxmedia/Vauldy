package handler

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
)

func (h *Handler) StartPhotoFaceLoop(ctx context.Context) {
	go h.runPhotoFaceLoop(ctx)
}

func (h *Handler) runPhotoFaceLoop(ctx context.Context) {
	var loopMu sync.Mutex
	tk := time.NewTicker(h.photoFacePollInterval())
	defer tk.Stop()
	for {
		h.runPhotoFaceOnce(&loopMu)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

func (h *Handler) photoFacePollInterval() time.Duration {
	sec := 10
	if h != nil && h.App != nil && h.App.Config != nil {
		sec = h.App.Config.PhotoFacePollIntervalSeconds()
	}
	if sec < 3 {
		sec = 3
	}
	return time.Duration(sec) * time.Second
}

func (h *Handler) photoFaceBatchLimit() int {
	if h != nil && h.App != nil && h.App.Config != nil {
		return h.App.Config.PhotoFaceBatchLimit()
	}
	return 1
}

func (h *Handler) runPhotoFaceOnce(loopMu *sync.Mutex) {
	if h == nil || h.PhotoFaceWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	if loopMu != nil && !loopMu.TryLock() {
		return
	}
	if loopMu != nil {
		defer loopMu.Unlock()
	}
	if h.PhotoFaceWorker.ActiveCount() >= h.PhotoFaceWorker.MaxConcurrent() {
		return
	}
	var pending int
	_ = h.App.DB.QueryRow(`
		SELECT COUNT(1) FROM photo_face_task
		WHERE status = 'pending'
		   OR (status = 'running' AND started_at IS NOT NULL AND started_at < datetime('now', '-20 minutes'))
	`).Scan(&pending)
	if pending == 0 {
		return
	}
	limit := pending
	if cap := h.photoFaceBatchLimit(); limit > cap {
		limit = cap
	}
	slots := h.PhotoFaceWorker.MaxConcurrent() - h.PhotoFaceWorker.ActiveCount()
	if slots <= 0 {
		return
	}
	if limit > slots {
		limit = slots
	}
	done, failed := h.PhotoFaceWorker.RunBatch(context.Background(), limit)
	if done+failed > 0 {
		if failed > 0 {
			var sample sql.NullString
			_ = h.App.DB.QueryRow(`
				SELECT message FROM photo_face_task
				WHERE status = 'failed' AND message IS NOT NULL
				ORDER BY updated_at DESC LIMIT 1`).Scan(&sample)
			if sample.Valid {
				log.Printf("photo face worker: processed=%d ok=%d fail=%d active=%d max=%d last_error=%s",
					done+failed, done, failed, h.PhotoFaceWorker.ActiveCount(), h.PhotoFaceWorker.MaxConcurrent(), sample.String)
			} else {
				log.Printf("photo face worker: processed=%d ok=%d fail=%d active=%d max=%d",
					done+failed, done, failed, h.PhotoFaceWorker.ActiveCount(), h.PhotoFaceWorker.MaxConcurrent())
			}
		} else {
			log.Printf("photo face worker: processed=%d ok=%d fail=%d active=%d max=%d",
				done+failed, done, failed, h.PhotoFaceWorker.ActiveCount(), h.PhotoFaceWorker.MaxConcurrent())
		}
	}
}

func (h *Handler) PhotoFaceProgress(c *gin.Context) {
	if h.PhotoFaceWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "photo face worker disabled"})
		return
	}
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	total, processed, withFaces, pending, failed, err := h.PhotoFaceWorker.LibraryProgress(libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	finished := processed + failed
	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"processed": processed,
		"detected":  withFaces,
		"pending":   pending,
		"failed":    failed,
		"percent":   progressPercent(finished, total),
	})
}

func (h *Handler) BackfillPhotoFaces(c *gin.Context) {
	if !middleware.IsAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	if h.PhotoFaceWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "photo face worker disabled"})
		return
	}
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	n, err := h.PhotoFaceWorker.EnqueueLibraryAll(libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	go func() {
		var mu sync.Mutex
		h.runPhotoFaceOnce(&mu)
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true, "queued": n})
}
