package handler

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
)

func (h *Handler) ListFavorites(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	profile, _ := h.loadUserPermissionProfile(uid)
	q := `
		SELECT m.id, m.library_id, m.file_id, m.title, m.original_title, m.file_path, m.file_type,
			m.duration, m.width, m.height, m.bitrate, m.format, m.status, m.created_at,
			COALESCE(
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''),
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.poster')), '')
			) AS poster_url,
			COALESCE(l.type, '') AS library_type
		FROM favorite f
		INNER JOIN media m ON m.id = f.media_id
		LEFT JOIN library l ON l.id = m.library_id
		WHERE f.user_id = ?
		ORDER BY datetime(f.created_at) DESC
		LIMIT 500`
	rows, err := h.App.DB.Query(q, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var mid int64
		var libID sql.NullInt64
		var fileID, title, orig, path, ftype, format, status, created, posterURL, libType sql.NullString
		var dur, w, h, br sql.NullInt64
		if err := rows.Scan(&mid, &libID, &fileID, &title, &orig, &path, &ftype, &dur, &w, &h, &br, &format, &status, &created, &posterURL, &libType); err != nil {
			continue
		}
		if strings.EqualFold(profile.LibraryScope, "selected") {
			if _, ok := profile.AllowedLibraryIDs[libID.Int64]; !ok {
				continue
			}
			if folders := profile.AllowedLibraryFolders[libID.Int64]; len(folders) > 0 && !pathMatchesAnyFolder(path.String, folders) {
				continue
			}
		}
		items = append(items, gin.H{
			"id": mid, "library_id": libID.Int64, "file_id": fileID.String,
			"title": title.String, "original_title": orig.String, "file_path": path.String,
			"file_type": ftype.String, "duration": dur.Int64, "width": w.Int64, "height": h.Int64,
			"bitrate": br.Int64, "format": format.String, "status": status.String, "created_at": created.String,
			"poster_url": posterURL.String, "library_type": strings.TrimSpace(libType.String),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) AddFavorite(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	mid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mid, false); !ok {
		return
	}
	var n int
	if err := h.App.DB.QueryRow(`SELECT COUNT(1) FROM media WHERE id = ?`, mid).Scan(&n); err != nil || n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}
	_, err = h.App.DB.Exec(`INSERT OR IGNORE INTO favorite (user_id, media_id) VALUES (?, ?)`, uid, mid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) RemoveFavorite(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	mid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mid, false); !ok {
		return
	}
	_, err = h.App.DB.Exec(`DELETE FROM favorite WHERE user_id = ? AND media_id = ?`, uid, mid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) FavoriteStatus(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	mid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mid, false); !ok {
		return
	}
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM favorite WHERE user_id = ? AND media_id = ?`, uid, mid).Scan(&n)
	c.JSON(http.StatusOK, gin.H{"favorited": n > 0})
}
