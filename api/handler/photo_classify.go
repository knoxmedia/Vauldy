package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/photoclass"
)

const (
	photoClassifyInterval = 6 * time.Second
	photoClassifyBatchMax = 8
)

// StartPhotoClassifyLoop drains pending photo classification tasks.
func (h *Handler) StartPhotoClassifyLoop(ctx context.Context) {
	go h.runPhotoClassifyOnce()
	tk := time.NewTicker(photoClassifyInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runPhotoClassifyOnce()
		}
	}
}

func (h *Handler) runPhotoClassifyOnce() {
	if h == nil || h.PhotoClassifyWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM photo_classify_task WHERE status IN ('pending', 'failed', 'running')`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > photoClassifyBatchMax {
		limit = photoClassifyBatchMax
	}
	done, failed := h.PhotoClassifyWorker.RunBatch(context.Background(), limit)
	if done+failed > 0 {
		log.Printf("photo classify worker: processed=%d ok=%d fail=%d", done+failed, done, failed)
	}
}

func (h *Handler) ListPhotoCategories(c *gin.Context) {
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	if _, err := photoclass.RepairLibraryPhotoTags(h.App.DB, libraryID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rows, err := h.App.DB.Query(`
		SELECT COALESCE(json_extract(meta_json, '$.photo.tags'), '[]')
		FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
	`, libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	counts := map[string]int64{} // keyed by category id
	var total int64
	for rows.Next() {
		var raw sql.NullString
		if rows.Scan(&raw) != nil {
			continue
		}
		total++
		for _, tag := range parseJSONStringArray(raw.String) {
			id, _ := photoclass.ResolveTag(tag)
			if id == "" {
				id = "custom:" + tag
			}
			counts[id]++
		}
	}

	type cat struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Type  string `json:"type"`
		Count int64  `json:"count"`
	}
	items := make([]cat, 0, len(counts)+1)
	items = append(items, cat{ID: "all", Name: "全部", Type: "all", Count: total})
	for _, def := range photoclass.CategoryCatalog {
		if counts[def.ID] > 0 {
			items = append(items, cat{ID: def.ID, Name: def.Name, Type: def.Kind, Count: counts[def.ID]})
		}
	}
	for id, cnt := range counts {
		if strings.HasPrefix(id, "custom:") {
			name := strings.TrimPrefix(id, "custom:")
			items = append(items, cat{ID: id, Name: name, Type: "custom", Count: cnt})
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

func parseJSONStringArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil
	}
	return photoclass.NormalizeTags(arr)
}

func (h *Handler) PhotoClassifyProgress(c *gin.Context) {
	if h.PhotoClassifyWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "photo classify disabled"})
		return
	}
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	total, classified, pending, err := h.PhotoClassifyWorker.LibraryProgress(libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":      total,
		"classified": classified,
		"pending":    pending,
		"percent":    progressPercent(classified, total),
	})
}

func progressPercent(done, total int64) int {
	if total <= 0 {
		return 100
	}
	p := int(done * 100 / total)
	if p > 100 {
		return 100
	}
	return p
}

func (h *Handler) EnqueuePhotoLibraryClassify(c *gin.Context) {
	if h.PhotoClassifyWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "photo classify disabled"})
		return
	}
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	force := c.Query("force") == "1" || strings.EqualFold(c.Query("force"), "true")
	var n int64
	if force {
		n, err = h.PhotoClassifyWorker.EnqueueLibraryAll(libraryID)
	} else {
		n, err = h.PhotoClassifyWorker.EnqueueLibrary(libraryID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "queued": n})
}

type updatePhotoTagsBody struct {
	Tags []string `json:"tags"`
}

func (h *Handler) UpdatePhotoTags(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mediaID, false); !ok {
		return
	}
	var body updatePhotoTagsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := photoclass.ApplyManualTags(h.App.DB, mediaID, body.Tags); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "tags": body.Tags})
}
