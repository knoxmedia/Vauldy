package handler

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/photoface"
	"knox-media/internal/storage"
)

func (h *Handler) ListPhotoPersons(c *gin.Context) {
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	if !h.requirePhotoAggregateAccess(c, libraryID) {
		return
	}
	rows, err := h.App.DB.Query(`
		SELECT id, label, cover_face_id, media_count
		FROM photo_person
		WHERE library_id = ? AND media_count > 0
		ORDER BY media_count DESC, label ASC`, libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type personRow struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Count       int64  `json:"count"`
		CoverFaceID int64  `json:"cover_face_id,omitempty"`
	}
	items := make([]personRow, 0)
	for rows.Next() {
		var row personRow
		var cover sql.NullInt64
		if rows.Scan(&row.ID, &row.Name, &cover, &row.Count) != nil {
			continue
		}
		if cover.Valid {
			row.CoverFaceID = cover.Int64
		}
		if strings.TrimSpace(row.Name) == "" {
			row.Name = "未命名人物"
		}
		items = append(items, row)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type updatePhotoPersonBody struct {
	Name string `json:"name"`
}

func (h *Handler) UpdatePhotoPerson(c *gin.Context) {
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	personID, err := strconv.ParseInt(c.Param("personId"), 10, 64)
	if err != nil || personID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid person id"})
		return
	}
	if !h.requirePhotoAggregateAccess(c, libraryID) {
		return
	}
	var body updatePhotoPersonBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	res, err := h.App.DB.ExecContext(c.Request.Context(), `
		UPDATE photo_person SET label = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND library_id = ?`, name, personID, libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "person not found"})
		return
	}
	display := name
	if display == "" {
		display = "未命名人物"
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": display})
}

func (h *Handler) ServePhotoFaceThumb(c *gin.Context) {
	faceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || faceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid face id"})
		return
	}
	c.Header("Cache-Control", "private, no-store")
	var mediaID int64
	var bboxX, bboxY, bboxW, bboxH float64
	if err := h.App.DB.QueryRowContext(c.Request.Context(), `
		SELECT media_id, bbox_x, bbox_y, bbox_w, bbox_h FROM photo_face WHERE id = ?`, faceID).
		Scan(&mediaID, &bboxX, &bboxY, &bboxW, &bboxH); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, ok := h.requireMediaAccess(c, mediaID, false); !ok {
		return
	}
	if encPath, ok := storage.LookupEncPath(h.App.DB, mediaID, photoface.FaceThumbnailArtifactKind, photoface.FaceThumbnailLogicalName(faceID)); ok {
		h.serveDerivedAssetKind(c, mediaID, encPath, "image/jpeg", photoface.FaceThumbnailArtifactKind, photoface.FaceThumbnailLogicalName(faceID))
		return
	}
	facePath := photoface.ExpectedFaceThumbnailPath(h.photoCacheDir(), faceID)
	if photoface.ValidateFaceJPEG(facePath) != nil {
		c.Header("X-Thumbnail-Pending", "1")
		c.Header("Retry-After", "5")
		c.JSON(http.StatusNotFound, gin.H{"error": "face thumbnail not ready"})
		return
	}
	data, err := os.ReadFile(facePath)
	if err != nil {
		c.Header("X-Thumbnail-Pending", "1")
		c.Header("Retry-After", "5")
		c.JSON(http.StatusNotFound, gin.H{"error": "face thumbnail not ready"})
		return
	}
	c.Header("Content-Type", "image/jpeg")
	c.Writer.Write(data)
}
