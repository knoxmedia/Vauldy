package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	photoLocationInterval = 2 * time.Second
	photoLocationBatchMax = 4
)

// StartPhotoLocationLoop drains pending photo location tasks.
func (h *Handler) StartPhotoLocationLoop(ctx context.Context) {
	go h.runPhotoLocationOnce()
	tk := time.NewTicker(photoLocationInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runPhotoLocationOnce()
		}
	}
}

func (h *Handler) runPhotoLocationOnce() {
	if h == nil || h.PhotoLocationWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM photo_location_task WHERE status IN ('pending', 'failed', 'running')`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > photoLocationBatchMax {
		limit = photoLocationBatchMax
	}
	done, failed := h.PhotoLocationWorker.RunBatch(context.Background(), limit)
	if done+failed > 0 {
		log.Printf("photo location worker: processed=%d ok=%d fail=%d", done+failed, done, failed)
	}
}

func (h *Handler) PhotoLocationProgress(c *gin.Context) {
	if h.PhotoLocationWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "photo location worker disabled"})
		return
	}
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	total, located, pending, err := h.PhotoLocationWorker.LibraryProgress(libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":    total,
		"located":  located,
		"pending":  pending,
		"percent":  progressPercent(located, total),
	})
}
