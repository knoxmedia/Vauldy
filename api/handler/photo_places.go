package handler

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/photogeocode"
)

func (h *Handler) geocoder() *photogeocode.Service {
	if h.PhotoGeocode == nil {
		h.PhotoGeocode = photogeocode.New(h.App.DB)
		_ = h.PhotoGeocode.EnsureSchema()
	}
	return h.PhotoGeocode
}

func (h *Handler) ListPhotoPlaces(c *gin.Context) {
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}

	rows, err := h.App.DB.Query(`
		SELECT
			json_extract(meta_json, '$.photo.place_id') AS place_id,
			json_extract(meta_json, '$.photo.location_name') AS location_name,
			COUNT(1) AS cnt,
			MIN(id) AS cover_id
		FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
			AND NULLIF(json_extract(meta_json, '$.photo.place_id'), '') IS NOT NULL
		GROUP BY place_id, location_name
		ORDER BY cnt DESC, location_name ASC`, libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type placeRow struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Count   int64  `json:"count"`
		CoverID int64  `json:"cover_id,omitempty"`
	}
	items := make([]placeRow, 0)
	for rows.Next() {
		var placeID, name sql.NullString
		var cnt, coverID int64
		if rows.Scan(&placeID, &name, &cnt, &coverID) != nil {
			continue
		}
		if !placeID.Valid || strings.TrimSpace(placeID.String) == "" {
			continue
		}
		items = append(items, placeRow{
			ID:      placeID.String,
			Name:    strings.TrimSpace(name.String),
			Type:    "place",
			Count:   cnt,
			CoverID: coverID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) BackfillPhotoLocations(c *gin.Context) {
	if !middleware.IsAdmin(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	if h.PhotoLocationWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "photo location worker disabled"})
		return
	}
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}

	n, err := h.PhotoLocationWorker.EnqueueLibraryAll(libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	go h.runPhotoLocationOnce()
	c.JSON(http.StatusOK, gin.H{"ok": true, "queued": n})
}
