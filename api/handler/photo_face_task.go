package handler

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
)

const (
	photoFaceInterval = 3 * time.Second
	photoFaceBatchMax = 2
)

func (h *Handler) StartPhotoFaceLoop(ctx context.Context) {
	go h.runPhotoFaceOnce()
	tk := time.NewTicker(photoFaceInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runPhotoFaceOnce()
		}
	}
}

func (h *Handler) runPhotoFaceOnce() {
	if h == nil || h.PhotoFaceWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM photo_face_task WHERE status IN ('pending', 'failed', 'running')`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > photoFaceBatchMax {
		limit = photoFaceBatchMax
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
				log.Printf("photo face worker: processed=%d ok=%d fail=%d last_error=%s", done+failed, done, failed, sample.String)
			} else {
				log.Printf("photo face worker: processed=%d ok=%d fail=%d", done+failed, done, failed)
			}
		} else {
			log.Printf("photo face worker: processed=%d ok=%d fail=%d", done+failed, done, failed)
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
	total, processed, withFaces, pending, err := h.PhotoFaceWorker.LibraryProgress(libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"processed": processed,
		"detected":  withFaces,
		"pending":   pending,
		"percent":   progressPercent(processed, total),
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
	go h.runPhotoFaceOnce()
	c.JSON(http.StatusOK, gin.H{"ok": true, "queued": n})
}
